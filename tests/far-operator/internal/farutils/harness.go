package farutils

import (
	"context"
	"fmt"
	"sort"
	"strings"

	configv1 "github.com/openshift/api/config/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/medik8s/system-tests/tests/far-operator/internal/farparams"
	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
)

// DetectPlatform returns the cluster platform type and AWS region (if applicable).
func DetectPlatform(ctx context.Context, k8sClient client.Client) (configv1.PlatformType, string, error) {
	infra := &configv1.Infrastructure{}
	if err := k8sClient.Get(ctx, client.ObjectKey{Name: "cluster"}, infra); err != nil {
		return "", "", fmt.Errorf("failed to get Infrastructure/cluster: %w", err)
	}

	if infra.Status.PlatformStatus == nil {
		return "", "", fmt.Errorf("Infrastructure.status.platformStatus is nil")
	}

	platform := infra.Status.PlatformStatus.Type

	var region string
	if platform == configv1.AWSPlatformType && infra.Status.PlatformStatus.AWS != nil {
		region = infra.Status.PlatformStatus.AWS.Region
	}

	return platform, region, nil
}

// FenceAgentForPlatform returns the fence agent binary name and node identifier
// parameter for the given platform.
func FenceAgentForPlatform(platform configv1.PlatformType) (agent, nodeIDParam string, err error) {
	switch platform { //nolint:exhaustive // only AWS and BareMetal are supported; default rejects all others.
	case configv1.AWSPlatformType:
		return farparams.FenceAgentAWS, farparams.NodeIdentifierAWS, nil
	case configv1.BareMetalPlatformType:
		return farparams.FenceAgentIPMI, farparams.NodeIdentifierIPMI, nil
	default:
		return "", "", fmt.Errorf("unsupported platform for FAR destructive tests: %s", platform)
	}
}

// ExtractAWSInstanceID parses the EC2 instance ID from a node's spec.providerID.
// Provider ID format: aws:///us-east-1a/i-0abc123def456
func ExtractAWSInstanceID(node *corev1.Node) (string, error) {
	providerID := node.Spec.ProviderID
	if providerID == "" {
		return "", fmt.Errorf("node %s has no providerID", node.Name)
	}

	if !strings.HasPrefix(providerID, "aws://") {
		return "", fmt.Errorf("node %s providerID is not AWS: %s", node.Name, providerID)
	}

	parts := strings.Split(providerID, "/")
	instanceID := parts[len(parts)-1]

	if instanceID == "" || !strings.HasPrefix(instanceID, "i-") {
		return "", fmt.Errorf("failed to parse instance ID from providerID: %s", providerID)
	}

	return instanceID, nil
}

// GetAWSCredentials reads the CCO-provisioned Secret and returns the access key
// and secret key for fence_aws.
func GetAWSCredentials(
	ctx context.Context, k8sClient client.Client, namespace string,
) (accessKey, secretKey string, err error) {
	secret := &corev1.Secret{}
	key := client.ObjectKey{
		Name:      farparams.AWSCredentialsSecretName,
		Namespace: namespace,
	}

	if err := k8sClient.Get(ctx, key, secret); err != nil {
		return "", "", fmt.Errorf("failed to get AWS credentials secret %s/%s: %w",
			namespace, farparams.AWSCredentialsSecretName, err)
	}

	accessKey = string(secret.Data[farparams.AWSAccessKeyField])
	secretKey = string(secret.Data[farparams.AWSSecretKeyField])

	if accessKey == "" || secretKey == "" {
		return "", "", fmt.Errorf("AWS credentials secret is missing required keys (%s, %s)",
			farparams.AWSAccessKeyField, farparams.AWSSecretKeyField)
	}

	return accessKey, secretKey, nil
}

// SelectWorkerNode returns a Ready, schedulable worker node that is not in the
// excludeNodes list. Returns an error if no eligible node is found.
func SelectWorkerNode(ctx context.Context, k8sClient client.Client, excludeNodes ...string) (*corev1.Node, error) {
	nodeList := &corev1.NodeList{}

	if err := k8sClient.List(ctx, nodeList, client.MatchingLabels{"node-role.kubernetes.io/worker": ""}); err != nil {
		return nil, fmt.Errorf("failed to list worker nodes: %w", err)
	}

	sort.Slice(nodeList.Items, func(i, j int) bool {
		return nodeList.Items[i].Name < nodeList.Items[j].Name
	})

	excluded := make(map[string]bool, len(excludeNodes))
	for _, name := range excludeNodes {
		excluded[name] = true
	}

	for i := range nodeList.Items {
		node := &nodeList.Items[i]

		if excluded[node.Name] || node.Spec.Unschedulable {
			continue
		}

		if isNodeReady(node) {
			return node, nil
		}
	}

	return nil, fmt.Errorf("no eligible Ready worker node found (excluded: %v)", excludeNodes)
}

// GetActiveFARControllerNode returns the node name hosting the active FAR
// controller pod by inspecting the leader election lease.
func GetActiveFARControllerNode(ctx context.Context, k8sClient client.Client) (string, error) {
	leaseList := &coordinationv1.LeaseList{}
	if err := k8sClient.List(ctx, leaseList, client.InNamespace(medik8sparams.OperatorNs)); err != nil {
		return "", fmt.Errorf("failed to list leases: %w", err)
	}

	var leaderLease *coordinationv1.Lease

	for i := range leaseList.Items {
		if strings.Contains(leaseList.Items[i].Name, farparams.ControllerLeasePrefix) {
			leaderLease = &leaseList.Items[i]

			break
		}
	}

	if leaderLease == nil || leaderLease.Spec.HolderIdentity == nil {
		return "", fmt.Errorf("FAR leader lease not found or has no holder")
	}

	holderID := *leaderLease.Spec.HolderIdentity
	parts := strings.Split(holderID, "_")
	podName := parts[0]

	pod := &corev1.Pod{}
	if err := k8sClient.Get(ctx, client.ObjectKey{Name: podName, Namespace: medik8sparams.OperatorNs}, pod); err != nil {
		return "", fmt.Errorf("failed to get FAR leader pod %s: %w", podName, err)
	}

	return pod.Spec.NodeName, nil
}

// BuildAWSNodeParameters builds the --plug node parameter map for fence_aws
// from the list of worker nodes.
func BuildAWSNodeParameters(ctx context.Context, k8sClient client.Client) (map[string]map[string]string, error) {
	nodeList := &corev1.NodeList{}
	if err := k8sClient.List(ctx, nodeList, client.MatchingLabels{"node-role.kubernetes.io/worker": ""}); err != nil {
		return nil, fmt.Errorf("failed to list worker nodes: %w", err)
	}

	plugMap := make(map[string]string)

	for i := range nodeList.Items {
		node := &nodeList.Items[i]

		if node.Spec.Unschedulable || !isNodeReady(node) {
			continue
		}

		instanceID, err := ExtractAWSInstanceID(node)
		if err != nil {
			return nil, fmt.Errorf("ready worker %s has invalid providerID: %w", node.Name, err)
		}

		plugMap[node.Name] = instanceID
	}

	if len(plugMap) == 0 {
		return nil, fmt.Errorf("no worker nodes with valid AWS providerID")
	}

	return map[string]map[string]string{farparams.NodeIdentifierAWS: plugMap}, nil
}

// CountReadyWorkerNodes returns the number of Ready worker nodes.
func CountReadyWorkerNodes(ctx context.Context, k8sClient client.Client) (int, error) {
	nodeList := &corev1.NodeList{}
	if err := k8sClient.List(ctx, nodeList, client.MatchingLabels{"node-role.kubernetes.io/worker": ""}); err != nil {
		return 0, fmt.Errorf("failed to list worker nodes: %w", err)
	}

	count := 0

	for i := range nodeList.Items {
		if isNodeReady(&nodeList.Items[i]) {
			count++
		}
	}

	return count, nil
}

// GetFARControllerPods returns the running FAR controller manager pods.
func GetFARControllerPods(ctx context.Context, k8sClient client.Client) ([]corev1.Pod, error) {
	podList := &corev1.PodList{}
	opts := []client.ListOption{
		client.InNamespace(medik8sparams.OperatorNs),
		client.MatchingLabels{
			"control-plane":                 "controller-manager",
			farparams.ControllerPodLabelKey: farparams.OperatorControllerPodLabel,
		},
	}

	if err := k8sClient.List(ctx, podList, opts...); err != nil {
		return nil, fmt.Errorf("failed to list FAR controller pods: %w", err)
	}

	var running []corev1.Pod

	for _, pod := range podList.Items {
		if pod.Status.Phase != corev1.PodRunning || pod.DeletionTimestamp != nil {
			continue
		}

		if len(pod.Status.ContainerStatuses) == 0 {
			continue
		}

		allReady := true

		for _, cs := range pod.Status.ContainerStatuses {
			if !cs.Ready {
				allReady = false

				break
			}
		}

		if allReady {
			running = append(running, pod)
		}
	}

	return running, nil
}

func isNodeReady(node *corev1.Node) bool {
	for _, cond := range node.Status.Conditions {
		if cond.Type == corev1.NodeReady {
			return cond.Status == corev1.ConditionTrue
		}
	}

	return false
}
