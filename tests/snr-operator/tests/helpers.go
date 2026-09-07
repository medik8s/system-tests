package tests

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/pod"

	"github.com/medik8s/system-tests/tests/internal/helpers"
	. "github.com/medik8s/system-tests/tests/internal/medik8sinittools"
	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
	"github.com/medik8s/system-tests/tests/snr-operator/internal/snrparams"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// buildSNRCR builds an unstructured SNR custom resource of the given kind.
func buildSNRCR(kind, name string, spec map[string]interface{}) *unstructured.Unstructured {
	resource := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": snrparams.CRDGroup + "/" + snrparams.CRDVersion,
			"kind":       kind,
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": medik8sparams.OperatorNs,
			},
		},
	}

	if spec != nil {
		resource.Object["spec"] = spec
	}

	return resource
}

// buildSNRWithAnnotations creates an SNR CR with optional annotations.
func buildSNRWithAnnotations(
	name string, annotations map[string]string,
) *unstructured.Unstructured {
	metadata := map[string]interface{}{
		"name":      name,
		"namespace": medik8sparams.OperatorNs,
	}

	if annotations != nil {
		annotationMap := make(map[string]interface{}, len(annotations))
		for key, val := range annotations {
			annotationMap[key] = val
		}

		metadata["annotations"] = annotationMap
	}

	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": snrparams.CRDGroup + "/" + snrparams.CRDVersion,
			"kind":       "SelfNodeRemediation",
			"metadata":   metadata,
		},
	}
}

// deferDeleteCR registers cleanup for a CR, retrying deletion with Eventually.
func deferDeleteCR(resource *unstructured.Unstructured) {
	DeferCleanup(func() {
		Eventually(func() error {
			deleteErr := APIClient.Delete(context.TODO(), resource)
			if k8serrors.IsNotFound(deleteErr) {
				return nil
			}

			return deleteErr
		}, medik8sparams.DefaultTimeout, snrparams.DefaultPollInterval).Should(Succeed(),
			"cleanup of test CR %q must succeed", resource.GetName())
	})
}

// snrGVK is the GroupVersionKind for SelfNodeRemediation CRs.
var snrGVK = schema.GroupVersionKind{
	Group:   snrparams.CRDGroup,
	Version: snrparams.CRDVersion,
	Kind:    "SelfNodeRemediation",
}

// snrcGVK is the GroupVersionKind for SelfNodeRemediationConfig CRs.
var snrcGVK = schema.GroupVersionKind{
	Group:   snrparams.CRDGroup,
	Version: snrparams.CRDVersion,
	Kind:    "SelfNodeRemediationConfig",
}

// snrcForPatch returns a minimal unstructured object suitable for client.Patch calls.
func snrcForPatch(name string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(snrcGVK)
	obj.SetName(name)
	obj.SetNamespace(medik8sparams.OperatorNs)

	return obj
}

// verifyDSPodsRunning checks that SNR DaemonSet pods exist and are all Running
// with ready containers.
func verifyDSPodsRunning() error {
	dsListOptions := metav1.ListOptions{
		LabelSelector: snrparams.DaemonSetPodLabelSelector,
	}

	dsPods, listErr := pod.List(APIClient, medik8sparams.OperatorNs, dsListOptions)
	if listErr != nil {
		return fmt.Errorf("failed to list SNR DaemonSet pods: %w", listErr)
	}

	if len(dsPods) == 0 {
		return fmt.Errorf("no SNR DaemonSet pods found")
	}

	for _, dsPod := range dsPods {
		if dsPod.Object.Status.Phase != corev1.PodRunning {
			return fmt.Errorf("SNR DaemonSet pod %q is %s, expected Running",
				dsPod.Object.Name, dsPod.Object.Status.Phase)
		}

		for _, cs := range dsPod.Object.Status.ContainerStatuses {
			if !cs.Ready {
				return fmt.Errorf("SNR DaemonSet pod %q container %q is not ready",
					dsPod.Object.Name, cs.Name)
			}
		}
	}

	return nil
}

// verifyDSPodsGone checks that no SNR DaemonSet pods exist.
func verifyDSPodsGone() error {
	dsListOptions := metav1.ListOptions{
		LabelSelector: snrparams.DaemonSetPodLabelSelector,
	}

	dsPods, listErr := pod.List(APIClient, medik8sparams.OperatorNs, dsListOptions)
	if listErr != nil {
		return fmt.Errorf("failed to list DS pods: %w", listErr)
	}

	if len(dsPods) > 0 {
		return fmt.Errorf("still %d SNR DS pods running, expected 0", len(dsPods))
	}

	return nil
}

// findMessageInDSPodLogs searches SNR DS pod logs from the last logWindow
// for the given message. Returns nil when found in at least one pod.
func findMessageInDSPodLogs(message string, logWindow time.Duration) error {
	dsListOptions := metav1.ListOptions{
		LabelSelector: snrparams.DaemonSetPodLabelSelector,
	}

	dsPods, listErr := pod.List(APIClient, medik8sparams.OperatorNs, dsListOptions)
	if listErr != nil {
		return fmt.Errorf("failed to list SNR DaemonSet pods: %w", listErr)
	}

	if len(dsPods) == 0 {
		return fmt.Errorf("no SNR DaemonSet pods found")
	}

	var lastLogErr error

	for _, dsPod := range dsPods {
		logStr, logErr := dsPod.GetLog(logWindow, "")
		if logErr != nil {
			lastLogErr = fmt.Errorf("pod %s: %w", dsPod.Object.Name, logErr)

			continue
		}

		if strings.Contains(logStr, message) {
			return nil
		}
	}

	if lastLogErr != nil {
		return fmt.Errorf("message %q not found; last log error: %w", message, lastLogErr)
	}

	return fmt.Errorf("message %q not found in any SNR DS pod logs (last %s)",
		message, logWindow)
}

// --- Remediation test helpers ---

// stopKubeletForRemediation wraps helpers.StopKubelet with additional
// error suppression for expected failure modes during kubelet stop:
//   - Timeout: kubelet death kills the debug pod before oc debug returns
//   - "unable to create the debug pod": oc debug race condition on cleanup
//   - "exit status 1" with "Starting pod": command was sent but oc debug
//     reported an error during pod teardown
//
// These are all expected because stopping kubelet inherently disrupts
// the debug pod that sent the stop command.
func stopKubeletForRemediation(ctx context.Context, nodeName string) error {
	err := helpers.StopKubelet(ctx, nodeName, snrparams.OcDebugTimeout, GinkgoWriter.Printf)
	if err == nil {
		return nil
	}

	errMsg := err.Error()

	// Suppress specific oc debug error patterns that indicate the stop
	// command was likely sent before the debug pod connection dropped.
	if strings.Contains(errMsg, "oc debug on node") && strings.Contains(errMsg, "timed out") ||
		strings.Contains(errMsg, "unable to create the debug pod") ||
		(strings.Contains(errMsg, "exit status 1") && strings.Contains(errMsg, "Starting pod")) {
		GinkgoWriter.Printf(
			"stopKubeletForRemediation(%s): suppressed expected error "+
				"(kubelet likely stopped): %v\n", nodeName, err)

		return nil
	}

	return err
}

// nhcGVK is the GroupVersionKind for NodeHealthCheck CRs.
// Uses snrparams.NHCAPIGroup/NHCAPIVersion (same values as sbrparams
// equivalents, to ease future extraction to a shared package).
var nhcGVK = schema.GroupVersionKind{
	Group:   snrparams.NHCAPIGroup,
	Version: snrparams.NHCAPIVersion,
	Kind:    "NodeHealthCheck",
}

// snrtGVK is the GroupVersionKind for SelfNodeRemediationTemplate CRs.
var snrtGVK = schema.GroupVersionKind{
	Group:   snrparams.CRDGroup,
	Version: snrparams.CRDVersion,
	Kind:    "SelfNodeRemediationTemplate",
}

// isNHCCRDInstalled checks whether the NodeHealthCheck CRD is registered
// in the cluster.
func isNHCCRDInstalled() bool {
	crd := &apiextensionsv1.CustomResourceDefinition{}
	err := APIClient.Get(
		context.TODO(),
		types.NamespacedName{Name: snrparams.NHCCRDName},
		crd,
	)

	return err == nil
}

// listMasterNodes returns all master/control-plane nodes, trying both
// the "master" and "control-plane" role labels for OCP 4.14+ compat.
func listMasterNodes(ctx context.Context, k8sClient client.Client) (*corev1.NodeList, error) {
	nodeList := &corev1.NodeList{}

	// Try "master" label first (OCP <= 4.13).
	if err := k8sClient.List(ctx, nodeList,
		client.MatchingLabels{"node-role.kubernetes.io/master": ""}); err != nil {
		return nil, fmt.Errorf("failed to list master nodes: %w", err)
	}

	// Fall back to "control-plane" label (OCP 4.14+).
	if len(nodeList.Items) == 0 {
		if err := k8sClient.List(ctx, nodeList,
			client.MatchingLabels{"node-role.kubernetes.io/control-plane": ""}); err != nil {
			return nil, fmt.Errorf("failed to list control-plane nodes: %w", err)
		}
	}

	sort.Slice(nodeList.Items, func(i, j int) bool {
		return nodeList.Items[i].Name < nodeList.Items[j].Name
	})

	return nodeList, nil
}

// selectMasterNode returns a Ready master node that is not in the
// excludeNodes list. Tries both "master" and "control-plane" role labels.
func selectMasterNode(
	ctx context.Context, k8sClient client.Client, excludeNodes ...string,
) (*corev1.Node, error) {
	nodeList, err := listMasterNodes(ctx, k8sClient)
	if err != nil {
		return nil, err
	}

	excluded := make(map[string]bool, len(excludeNodes))
	for _, name := range excludeNodes {
		excluded[name] = true
	}

	for i := range nodeList.Items {
		node := &nodeList.Items[i]

		if excluded[node.Name] || node.Spec.Unschedulable {
			continue
		}

		if helpers.IsNodeReady(node) {
			return node, nil
		}
	}

	return nil, fmt.Errorf(
		"no eligible Ready master node found (excluded: %v)", excludeNodes)
}

// countReadyMasterNodes returns the number of Ready master/control-plane nodes.
func countReadyMasterNodes(ctx context.Context, k8sClient client.Client) (int, error) {
	nodeList, err := listMasterNodes(ctx, k8sClient)
	if err != nil {
		return 0, err
	}

	count := 0

	for i := range nodeList.Items {
		if helpers.IsNodeReady(&nodeList.Items[i]) {
			count++
		}
	}

	return count, nil
}

// buildNHCForWorkers builds an unstructured NodeHealthCheck CR that
// monitors worker nodes and triggers SNR remediation via the named SNRT.
func buildNHCForWorkers(name, snrtName string) *unstructured.Unstructured {
	return buildNHC(name, snrtName, "node-role.kubernetes.io/worker")
}

// buildNHCForMasters builds an unstructured NodeHealthCheck CR that
// monitors master/control-plane nodes and triggers SNR remediation.
// Uses "control-plane" label for OCP 4.14+ compatibility (older OCP
// has both "master" and "control-plane"; newer may only have "control-plane").
func buildNHCForMasters(name, snrtName string) *unstructured.Unstructured {
	return buildNHC(name, snrtName, "node-role.kubernetes.io/control-plane")
}

// buildNHC builds an unstructured NodeHealthCheck CR with a selector
// matching nodes that have the given role label, and a remediation
// template pointing to the named SNRT in the operator namespace.
func buildNHC(name, snrtName, roleLabel string) *unstructured.Unstructured {
	nhc := &unstructured.Unstructured{}
	nhc.SetGroupVersionKind(nhcGVK)
	nhc.SetName(name)

	// minHealthy is required by the NHC admission webhook. Using integer 1
	// instead of percentage to avoid ceil rounding issues on small clusters
	// (e.g. ceil(0.51 * 2) = 2 would block remediation on 2-worker).
	// on 2-worker clusters).
	nhc.Object["spec"] = map[string]interface{}{
		"selector": map[string]interface{}{
			"matchExpressions": []interface{}{
				map[string]interface{}{
					"key":      roleLabel,
					"operator": "Exists",
				},
			},
		},
		"remediationTemplate": map[string]interface{}{
			"apiVersion": snrparams.CRDGroup + "/" + snrparams.CRDVersion,
			"kind":       "SelfNodeRemediationTemplate",
			"name":       snrtName,
			"namespace":  medik8sparams.OperatorNs,
		},
		"minHealthy": int64(1),
		"unhealthyConditions": []interface{}{
			map[string]interface{}{
				"type":     "Ready",
				"status":   "False",
				"duration": "60s",
			},
			map[string]interface{}{
				"type":     "Ready",
				"status":   "Unknown",
				"duration": "60s",
			},
		},
	}

	return nhc
}

// buildSNRT builds an unstructured SelfNodeRemediationTemplate CR with
// the given remediation strategy. Valid strategies: "Automatic",
// "ResourceDeletion", "OutOfServiceTaint".
func buildSNRT(name, strategy string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": snrparams.CRDGroup + "/" + snrparams.CRDVersion,
			"kind":       "SelfNodeRemediationTemplate",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": medik8sparams.OperatorNs,
			},
			"spec": map[string]interface{}{
				"template": map[string]interface{}{
					"spec": map[string]interface{}{
						"remediationStrategy": strategy,
					},
				},
			},
		},
	}
}

// waitForRemediationComplete polls until the SNR remediation cycle
// finishes for the given node. It handles the case where the entire
// cycle (SNR CR created -> node rebooted -> SNR CR deleted) completes
// before the test starts checking, which happens when StopKubelet
// takes a long time (oc debug timeout on ARM64).
//
// Success is defined as: SNR CR does not exist AND boot ID has changed.
// This covers both:
//   - Normal flow: we observe the CR, then it's deleted, boot ID changed
//   - Fast flow: cycle already completed, CR gone, boot ID already changed
func waitForRemediationComplete(
	ctx context.Context, k8sClient client.Client,
	nodeName, previousBootID string,
) error {
	var snrSeen bool

	return wait.PollUntilContextTimeout(
		ctx, snrparams.DefaultPollInterval, snrparams.SNRDeletionTimeout, true,
		func(ctx context.Context) (bool, error) {
			// Check if SNR CR exists.
			obj := &unstructured.Unstructured{}
			obj.SetGroupVersionKind(snrGVK)

			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      nodeName,
				Namespace: medik8sparams.OperatorNs,
			}, obj)

			switch {
			case err == nil:
				// SNR CR exists -- remediation in progress.
				if !snrSeen {
					GinkgoWriter.Printf("SNR CR %s detected -- remediation in progress\n", nodeName)

					snrSeen = true
				}

				return false, nil

			case k8serrors.IsNotFound(err):
				// SNR CR gone. Check if boot ID changed (node rebooted).
				currentBootID, bootErr := helpers.GetNodeBootIDFromAPI(ctx, k8sClient, nodeName)
				if bootErr != nil {
					// Node might be rebooting, API temporarily unavailable.
					return false, nil
				}

				if currentBootID != previousBootID {
					if snrSeen {
						GinkgoWriter.Printf("SNR CR deleted, boot ID changed -- remediation complete\n")
					} else {
						GinkgoWriter.Printf(
							"SNR CR already gone, boot ID changed -- remediation completed before check\n")
					}

					return true, nil
				}

				// Boot ID unchanged -- remediation hasn't started yet or node
				// hasn't rebooted yet. Keep polling.
				return false, nil

			default:
				// Transient API error, retry.
				return false, nil
			}
		},
	)
}

// createWorkloadPodOnNode creates a pause container pod pinned to the
// given node, registers DeferCleanup for deletion, and waits until the
// pod reaches Running phase with all containers ready.
func createWorkloadPodOnNode(ctx context.Context, nodeName string) *corev1.Pod {
	workloadPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "snr-workload-test-",
			Namespace:    medik8sparams.OperatorNs,
		},
		Spec: corev1.PodSpec{
			NodeName:      nodeName,
			RestartPolicy: corev1.RestartPolicyAlways,
			Containers: []corev1.Container{{
				Name:    "workload",
				Image:   snrparams.WorkloadTestImage,
				Command: []string{"sleep", "infinity"},
			}},
		},
	}

	Expect(APIClient.Create(ctx, workloadPod)).To(Succeed(),
		"Failed to create test workload pod on node %s", nodeName)

	DeferCleanup(func() {
		_ = APIClient.Delete(context.TODO(), workloadPod)
	})

	Eventually(func() bool {
		pod := &corev1.Pod{}
		if getErr := APIClient.Get(ctx, client.ObjectKey{
			Name: workloadPod.Name, Namespace: workloadPod.Namespace,
		}, pod); getErr != nil {
			return false
		}

		if pod.Status.Phase != corev1.PodRunning {
			return false
		}

		for _, cs := range pod.Status.ContainerStatuses {
			if !cs.Ready {
				return false
			}
		}

		return true
	}, snrparams.WorkloadPodReadyTimeout, snrparams.DefaultPollInterval).Should(BeTrue(),
		"Workload pod did not reach Running/Ready phase on node %s", nodeName)

	return workloadPod
}

// waitForPodEvictedFromNode polls until the pod is either deleted or
// no longer on the specified node.
func waitForPodEvictedFromNode(
	ctx context.Context, podName, podNamespace, nodeName string,
) {
	Eventually(func() bool {
		pod := &corev1.Pod{}
		getErr := APIClient.Get(ctx, client.ObjectKey{
			Name: podName, Namespace: podNamespace,
		}, pod)

		if k8serrors.IsNotFound(getErr) {
			return true
		}

		if getErr != nil {
			return false
		}

		return pod.Spec.NodeName != nodeName
	}, snrparams.WorkloadEvictionTimeout, snrparams.DefaultPollInterval).Should(BeTrue(),
		"Workload pod %s was not evicted from node %s", podName, nodeName)
}

// deleteRemediationCR performs a retry-safe deletion of an unstructured
// CR. Each poll iteration: get the CR (NotFound = done), delete it
// (NotFound = done), then keep polling until it's fully gone
// (finalizers, GC). Follows the same signature and pattern as FAR's
// deleteRemediationCR in far_destructive.go (PR #49) to ease future
// extraction to tests/internal/helpers/.
func deleteRemediationCR(
	ctx context.Context, k8sClient client.Client,
	gvk schema.GroupVersionKind, name string,
) {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)

	key := types.NamespacedName{
		Name:      name,
		Namespace: medik8sparams.OperatorNs,
	}

	if waitErr := wait.PollUntilContextTimeout(
		ctx, snrparams.DefaultPollInterval,
		snrparams.RemediationCRDeletionTimeout, true,
		func(ctx context.Context) (bool, error) {
			if err := k8sClient.Get(ctx, key, obj); err != nil {
				if k8serrors.IsNotFound(err) {
					return true, nil
				}

				return false, nil
			}

			if delErr := k8sClient.Delete(ctx, obj); delErr != nil {
				if k8serrors.IsNotFound(delErr) {
					return true, nil
				}

				return false, nil
			}

			return false, nil
		},
	); waitErr != nil {
		GinkgoWriter.Printf(
			"Warning: %s %s not fully deleted within %s: %v\n",
			gvk.Kind, name, snrparams.RemediationCRDeletionTimeout, waitErr)
	}
}

// bestEffortRemoveKubeletStopGuard attempts to remove the kubelet stop
// guard file from the given node. On failure it logs a warning and adds
// a report entry instead of failing the test.
func bestEffortRemoveKubeletStopGuard(ctx context.Context, nodeName string) {
	if guardErr := helpers.RemoveKubeletStopGuard(ctx, nodeName, snrparams.OcDebugTimeout); guardErr != nil {
		GinkgoWriter.Printf(
			"WARNING: failed to remove kubelet stop guard on %s: %v\n",
			nodeName, guardErr)
		AddReportEntry("guard-cleanup-failed",
			fmt.Sprintf("%s: %v", nodeName, guardErr))
	}
}

// cleanupNHCCR safely deletes a NodeHealthCheck CR by name, retrying
// on transient errors. Waits until the CR is fully gone (NHC uses
// webhook finalizers that can take several minutes to process).
func cleanupNHCCR(name string) {
	deleteRemediationCR(
		context.TODO(), APIClient, nhcGVK, name)
	GinkgoWriter.Printf("cleanupNHCCR(%s): deletion complete\n", name)
}

// cleanupSNRT safely deletes a SelfNodeRemediationTemplate CR by name.
func cleanupSNRT(name string) {
	deleteRemediationCR(
		context.TODO(), APIClient, snrtGVK, name)
}

// cleanupSNRCR safely deletes a SelfNodeRemediation CR by name.
func cleanupSNRCR(name string) {
	deleteRemediationCR(
		context.TODO(), APIClient, snrGVK, name)
}
