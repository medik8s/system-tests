package farutils

import (
	"context"
	"fmt"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/medik8s/system-tests/tests/far-operator/internal/farparams"
	"github.com/medik8s/system-tests/tests/internal/helpers"
)

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

// BuildAWSNodeParameters builds the --plug node parameter map for fence_aws
// from all Ready nodes (workers and control plane) so that both worker and
// CP remediation tests can use the same parameter map.
func BuildAWSNodeParameters(ctx context.Context, k8sClient client.Client) (map[string]map[string]string, error) {
	nodeList := &corev1.NodeList{}
	if err := k8sClient.List(ctx, nodeList); err != nil {
		return nil, fmt.Errorf("failed to list nodes: %w", err)
	}

	plugMap := make(map[string]string)

	for i := range nodeList.Items {
		node := &nodeList.Items[i]

		if !helpers.IsNodeReady(node) {
			continue
		}

		instanceID, err := helpers.ExtractAWSInstanceID(node)
		if err != nil {
			return nil, fmt.Errorf("ready node %s has invalid providerID: %w", node.Name, err)
		}

		plugMap[node.Name] = instanceID
	}

	if len(plugMap) == 0 {
		return nil, fmt.Errorf("no nodes with valid AWS providerID")
	}

	return map[string]map[string]string{farparams.NodeIdentifierAWS: plugMap}, nil
}

// GetReadyControlPlaneNodes returns all Ready control plane nodes.
func GetReadyControlPlaneNodes(ctx context.Context, k8sClient client.Client) ([]corev1.Node, error) {
	nodeList := &corev1.NodeList{}
	if err := k8sClient.List(ctx, nodeList); err != nil {
		return nil, fmt.Errorf("failed to list nodes: %w", err)
	}

	var cpNodes []corev1.Node

	for i := range nodeList.Items {
		node := &nodeList.Items[i]

		_, hasCP := node.Labels["node-role.kubernetes.io/control-plane"]
		_, hasMaster := node.Labels["node-role.kubernetes.io/master"]

		if (hasCP || hasMaster) && helpers.IsNodeReady(node) {
			cpNodes = append(cpNodes, *node)
		}
	}

	return cpNodes, nil
}

// SelectControlPlaneNode returns a Ready CP node that is not in the exclude list.
func SelectControlPlaneNode(
	ctx context.Context, k8sClient client.Client, excludeNodes ...string,
) (*corev1.Node, error) {
	cpNodes, err := GetReadyControlPlaneNodes(ctx, k8sClient)
	if err != nil {
		return nil, err
	}

	excluded := make(map[string]bool, len(excludeNodes))
	for _, name := range excludeNodes {
		excluded[name] = true
	}

	for i := range cpNodes {
		if !excluded[cpNodes[i].Name] {
			return &cpNodes[i], nil
		}
	}

	return nil, fmt.Errorf("no eligible CP node found (excluded: %v)", excludeNodes)
}

// CordonExtraWorkers cordons all Ready worker nodes except those in keepNames.
// Returns the names of nodes that were cordoned by this function.
func CordonExtraWorkers(
	ctx context.Context, k8sClient client.Client, keepNames []string,
) ([]string, error) {
	keepSet := make(map[string]bool, len(keepNames))
	for _, name := range keepNames {
		keepSet[name] = true
	}

	nodeList := &corev1.NodeList{}
	if err := k8sClient.List(ctx, nodeList,
		client.MatchingLabels{"node-role.kubernetes.io/worker": ""}); err != nil {
		return nil, fmt.Errorf("failed to list worker nodes: %w", err)
	}

	var cordoned []string

	for i := range nodeList.Items {
		node := &nodeList.Items[i]

		if keepSet[node.Name] || node.Spec.Unschedulable || !helpers.IsNodeReady(node) {
			continue
		}

		patch := client.MergeFrom(node.DeepCopy())
		node.Spec.Unschedulable = true

		if node.Annotations == nil {
			node.Annotations = make(map[string]string)
		}

		node.Annotations[farparams.TestCordonAnnotation] = "true"

		if err := k8sClient.Patch(ctx, node, patch); err != nil {
			return cordoned, fmt.Errorf("failed to cordon node %s: %w", node.Name, err)
		}

		cordoned = append(cordoned, node.Name)
	}

	return cordoned, nil
}

// UncordonNodes restores schedulability for the given node names.
func UncordonNodes(
	ctx context.Context, k8sClient client.Client, nodeNames []string,
	logf ...func(string, ...interface{}),
) {
	log := func(format string, args ...interface{}) {
		if len(logf) > 0 {
			logf[0](format, args...)
		}
	}

	for _, name := range nodeNames {
		node := &corev1.Node{}
		if err := k8sClient.Get(ctx, client.ObjectKey{Name: name}, node); err != nil {
			log("WARNING: failed to get node %s for uncordon: %v\n", name, err)

			continue
		}

		if !node.Spec.Unschedulable {
			continue
		}

		patch := client.MergeFrom(node.DeepCopy())
		node.Spec.Unschedulable = false
		delete(node.Annotations, farparams.TestCordonAnnotation)

		if err := k8sClient.Patch(ctx, node, patch); err != nil {
			log("WARNING: failed to uncordon node %s: %v\n", name, err)
		}
	}
}

// WaitForClusterOperatorHealthy polls until the named ClusterOperator reports
// Available=True, Progressing=False, Degraded=False.
func WaitForClusterOperatorHealthy(
	ctx context.Context, k8sClient client.Client,
	operatorName string, timeout time.Duration,
	logf func(string, ...interface{}),
) error {
	return helpers.WaitForClusterOperatorHealthy(ctx, k8sClient, operatorName, timeout,
		farparams.DefaultPollInterval, logf)
}
