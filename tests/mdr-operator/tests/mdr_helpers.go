package tests

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/pod"

	"github.com/medik8s/system-tests/tests/internal/helpers"
	. "github.com/medik8s/system-tests/tests/internal/medik8sinittools"
	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
	"github.com/medik8s/system-tests/tests/mdr-operator/internal/mdrparams"

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

// mdrGVK is the GroupVersionKind for MachineDeletionRemediation CRs.
var mdrGVK = schema.GroupVersionKind{
	Group:   mdrparams.CRDGroup,
	Version: mdrparams.CRDVersion,
	Kind:    "MachineDeletionRemediation",
}

// mdrtGVK is the GroupVersionKind for MachineDeletionRemediationTemplate CRs.
var mdrtGVK = schema.GroupVersionKind{
	Group:   mdrparams.CRDGroup,
	Version: mdrparams.CRDVersion,
	Kind:    "MachineDeletionRemediationTemplate",
}

// nhcGVK is the GroupVersionKind for NodeHealthCheck CRs.
var nhcGVK = schema.GroupVersionKind{
	Group:   mdrparams.NHCAPIGroup,
	Version: mdrparams.NHCAPIVersion,
	Kind:    "NodeHealthCheck",
}

// buildMDRT builds an unstructured MachineDeletionRemediationTemplate CR.
func buildMDRT(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": mdrparams.CRDGroup + "/" + mdrparams.CRDVersion,
			"kind":       "MachineDeletionRemediationTemplate",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": medik8sparams.OperatorNs,
			},
			"spec": map[string]interface{}{
				"template": map[string]interface{}{
					"spec": map[string]interface{}{},
				},
			},
		},
	}
}

// buildNHCForMDR builds an unstructured NodeHealthCheck CR that monitors
// worker nodes and triggers MDR remediation via the named MDRT.
func buildNHCForMDR(name, mdrtName string) *unstructured.Unstructured {
	nhc := &unstructured.Unstructured{}
	nhc.SetGroupVersionKind(nhcGVK)
	nhc.SetName(name)

	nhc.Object["spec"] = map[string]interface{}{
		"selector": map[string]interface{}{
			"matchExpressions": []interface{}{
				map[string]interface{}{
					"key":      mdrparams.WorkerRoleLabel,
					"operator": "Exists",
				},
			},
		},
		"remediationTemplate": map[string]interface{}{
			"apiVersion": mdrparams.CRDGroup + "/" + mdrparams.CRDVersion,
			"kind":       "MachineDeletionRemediationTemplate",
			"name":       mdrtName,
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

// isNHCCRDInstalled checks whether the NodeHealthCheck CRD is registered
// in the cluster. Returns false only for genuine NotFound; fails the test
// on transient API errors to avoid silently skipping tests.
func isNHCCRDInstalled() bool {
	crd := &apiextensionsv1.CustomResourceDefinition{}

	err := APIClient.Get(
		context.Background(),
		types.NamespacedName{Name: mdrparams.NHCCRDName},
		crd,
	)
	if err == nil {
		return true
	}

	if k8serrors.IsNotFound(err) {
		return false
	}

	Fail(fmt.Sprintf("isNHCCRDInstalled: unexpected error checking CRD %s: %v",
		mdrparams.NHCCRDName, err))

	return false
}

// stopKubeletForRemediation wraps helpers.StopKubelet with error
// suppression for expected failure modes during kubelet stop.
func stopKubeletForRemediation(ctx context.Context, nodeName string) error {
	err := helpers.StopKubelet(ctx, nodeName, mdrparams.OcDebugTimeout, GinkgoWriter.Printf)
	if err == nil {
		return nil
	}

	errMsg := err.Error()

	if (strings.Contains(errMsg, "oc debug on node") && strings.Contains(errMsg, "timed out")) ||
		strings.Contains(errMsg, "unable to create the debug pod") ||
		(strings.Contains(errMsg, "exit status 1") && strings.Contains(errMsg, "Starting pod")) {
		GinkgoWriter.Printf(
			"stopKubeletForRemediation(%s): suppressed expected error "+
				"(kubelet likely stopped): %v\n", nodeName, err)

		return nil
	}

	return err
}

// deleteRemediationCR performs a retry-safe deletion of an unstructured
// CR. Follows the same get-delete-confirm pattern as SNR and FAR.
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
		ctx, mdrparams.DefaultPollInterval,
		mdrparams.RemediationCRDeletionTimeout, true,
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
			gvk.Kind, name, mdrparams.RemediationCRDeletionTimeout, waitErr)
	}
}

// cleanupNHCCR safely deletes a NodeHealthCheck CR by name.
func cleanupNHCCR(name string) {
	deleteRemediationCR(
		context.Background(), APIClient, nhcGVK, name)
}

// cleanupMDRT safely deletes a MachineDeletionRemediationTemplate CR by name.
func cleanupMDRT(name string) {
	deleteRemediationCR(
		context.Background(), APIClient, mdrtGVK, name)
}

// cleanupMDRCR safely deletes a MachineDeletionRemediation CR by name.
func cleanupMDRCR(name string) {
	deleteRemediationCR(
		context.Background(), APIClient, mdrGVK, name)
}

// waitForMDRRemediationComplete polls until MDR remediation finishes.
// Unlike SNR/SBR (where the node reboots and keeps its name), MDR deletes
// the Machine and the cloud provisions a new VM. The replacement node
// typically has a DIFFERENT name (new EC2 instance = new hostname).
//
// Detection uses two strategies:
//  1. Name-based: a worker whose name is NOT in initialWorkerNames (AWS/Azure/GCP)
//  2. Time-based: a worker whose CreationTimestamp is after testStartTime
//     (vSphere, where the replacement may reuse the same hostname)
//
// The function requires observing the MDR CR at least once (mdrCRSeen guard)
// before accepting NotFound as completion. This prevents false positives if
// the CR was never created (e.g., NHC failed to create it) and worker count
// recovers for unrelated reasons.
//
// Returns the name of the replacement node.
func waitForMDRRemediationComplete(
	ctx context.Context, originalNodeName string,
	expectedWorkerCount int, initialWorkerNames map[string]bool,
	testStartTime time.Time, timeout time.Duration,
) (string, error) {
	var (
		newNodeName string
		mdrCRSeen   bool
	)

	err := wait.PollUntilContextTimeout(
		ctx, mdrparams.DefaultPollInterval, timeout, true,
		func(ctx context.Context) (bool, error) {
			// Check if MDR CR still exists.
			mdrObj := &unstructured.Unstructured{}
			mdrObj.SetGroupVersionKind(mdrGVK)

			getErr := APIClient.Get(ctx, types.NamespacedName{
				Name:      originalNodeName,
				Namespace: medik8sparams.OperatorNs,
			}, mdrObj)
			if getErr == nil {
				// MDR CR exists -- remediation in progress.
				if !mdrCRSeen {
					GinkgoWriter.Printf("MDR CR %s detected -- remediation in progress\n", originalNodeName)

					mdrCRSeen = true
				}

				return false, nil
			}

			if !k8serrors.IsNotFound(getErr) {
				// Transient API error, retry.
				return false, nil
			}

			// MDR CR is NotFound. Only accept this as completion if we
			// observed the CR at least once (proves NHC created it and
			// the operator processed it).
			if !mdrCRSeen {
				return false, nil
			}

			// MDR CR gone after being observed. Check if worker count has recovered.
			currentCount, countErr := helpers.CountReadyWorkerNodes(ctx, APIClient)
			if countErr != nil {
				return false, nil
			}

			if currentCount < expectedWorkerCount {
				return false, nil
			}

			// Worker count restored. Find the replacement node.
			nodeList := &corev1.NodeList{}
			if listErr := APIClient.List(ctx, nodeList,
				client.MatchingLabels{mdrparams.WorkerRoleLabel: ""}); listErr != nil {
				return false, nil
			}

			for i := range nodeList.Items {
				node := &nodeList.Items[i]

				// Strategy 1: new name not in initial set (AWS/Azure/GCP).
				if !initialWorkerNames[node.Name] {
					newNodeName = node.Name
					GinkgoWriter.Printf(
						"MDR remediation complete: replacement node %s (new name, original: %s)\n",
						newNodeName, originalNodeName)

					return true, nil
				}

				// Strategy 2: same name but created after test start (vSphere).
				if node.Name == originalNodeName &&
					node.CreationTimestamp.Time.After(testStartTime) {
					newNodeName = node.Name
					GinkgoWriter.Printf(
						"MDR remediation complete: replacement node %s (same name, re-created at %s, test started %s)\n",
						newNodeName, node.CreationTimestamp.Time, testStartTime)

					return true, nil
				}
			}

			return false, nil
		},
	)

	return newNodeName, err
}

// getMDRCRCondition returns the named status condition from an unstructured MDR CR, or nil.
func getMDRCRCondition(nodeName, condType string) (map[string]interface{}, error) {
	mdrObj := &unstructured.Unstructured{}
	mdrObj.SetGroupVersionKind(mdrGVK)

	if err := APIClient.Get(context.Background(), types.NamespacedName{
		Name:      nodeName,
		Namespace: medik8sparams.OperatorNs,
	}, mdrObj); err != nil {
		return nil, err
	}

	conditions, found, err := unstructured.NestedSlice(mdrObj.Object, "status", "conditions")
	if err != nil || !found {
		return nil, fmt.Errorf("no conditions found on MDR CR %s", nodeName)
	}

	for _, c := range conditions {
		cond, ok := c.(map[string]interface{})
		if !ok {
			continue
		}

		if cond["type"] == condType {
			return cond, nil
		}
	}

	return nil, fmt.Errorf("condition %s not found on MDR CR %s", condType, nodeName)
}

// buildMDR builds an unstructured MachineDeletionRemediation CR.
func buildMDR(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": mdrparams.CRDGroup + "/" + mdrparams.CRDVersion,
			"kind":       "MachineDeletionRemediation",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": medik8sparams.OperatorNs,
			},
			"spec": map[string]interface{}{},
		},
	}
}

// buildMDRWithAnnotations builds an unstructured MachineDeletionRemediation CR
// with optional annotations.
func buildMDRWithAnnotations(
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
			"apiVersion": mdrparams.CRDGroup + "/" + mdrparams.CRDVersion,
			"kind":       "MachineDeletionRemediation",
			"metadata":   metadata,
			"spec":       map[string]interface{}{},
		},
	}
}

// expectedCondition defines the expected values for a status condition.
type expectedCondition struct {
	conditionType string
	status        string // empty means don't check
	reason        string
	message       string // empty means don't check
}

// verifyMDRConditionsByType checks MDR conditions by looking up each expected
// condition by its type field, not by positional index.
func verifyMDRConditionsByType(
	mdrObj *unstructured.Unstructured, expected ...expectedCondition,
) error {
	conditions, found, err := unstructured.NestedSlice(
		mdrObj.Object, "status", "conditions")
	if err != nil {
		return fmt.Errorf("failed to get status.conditions: %w", err)
	}

	if !found || len(conditions) == 0 {
		return fmt.Errorf("no status.conditions found")
	}

	for _, exp := range expected {
		condMap, findErr := findMDRConditionByType(conditions, exp.conditionType)
		if findErr != nil {
			return findErr
		}

		if err := checkConditionField(condMap, exp.conditionType, "reason", exp.reason); err != nil {
			return err
		}

		if exp.status != "" {
			if err := checkConditionField(condMap, exp.conditionType, "status", exp.status); err != nil {
				return err
			}
		}

		if exp.message != "" {
			if err := checkConditionField(condMap, exp.conditionType, "message", exp.message); err != nil {
				return err
			}
		}
	}

	return nil
}

// checkConditionField validates a single field on a condition map.
func checkConditionField(
	condMap map[string]interface{}, condType, field, expected string,
) error {
	actual, found, err := unstructured.NestedString(condMap, field)
	if err != nil {
		return fmt.Errorf("condition %q %s field error: %w", condType, field, err)
	}

	if !found {
		return fmt.Errorf("condition %q %s field not yet written by controller",
			condType, field)
	}

	if actual != expected {
		return fmt.Errorf("condition %q %s: expected %q, got %q",
			condType, field, expected, actual)
	}

	return nil
}

// findMDRConditionByType finds a condition map by its type field.
func findMDRConditionByType(
	conditions []interface{}, condType string,
) (map[string]interface{}, error) {
	for _, cond := range conditions {
		condMap, ok := cond.(map[string]interface{})
		if !ok {
			continue
		}

		typeName, found, err := unstructured.NestedString(condMap, "type")
		if err != nil || !found {
			continue
		}

		if typeName == condType {
			return condMap, nil
		}
	}

	return nil, fmt.Errorf("condition with type %q not found", condType)
}

// deferDeleteMDRCR registers cleanup for an MDR CR via DeferCleanup.
func deferDeleteMDRCR(name string) {
	DeferCleanup(func() {
		cleanupMDRCR(name)
	})
}

// listControlPlaneNodes returns all master/control-plane nodes, trying both
// the "master" and "control-plane" role labels for OCP 4.14+ compat.
func listControlPlaneNodes(ctx context.Context, k8sClient client.Client) (*corev1.NodeList, error) {
	nodeList := &corev1.NodeList{}

	if err := k8sClient.List(ctx, nodeList,
		client.MatchingLabels{mdrparams.MasterRoleLabel: ""}); err != nil {
		return nil, fmt.Errorf("failed to list master nodes: %w", err)
	}

	if len(nodeList.Items) == 0 {
		if err := k8sClient.List(ctx, nodeList,
			client.MatchingLabels{mdrparams.ControlPlaneRoleLabel: ""}); err != nil {
			return nil, fmt.Errorf("failed to list control-plane nodes: %w", err)
		}
	}

	sort.Slice(nodeList.Items, func(i, j int) bool {
		return nodeList.Items[i].Name < nodeList.Items[j].Name
	})

	return nodeList, nil
}

// findMessageInControllerLogs searches MDR controller manager pod logs
// for the given message within the specified time window.
func findMessageInControllerLogs(message string, logWindow time.Duration) error {
	listOptions := metav1.ListOptions{
		LabelSelector: mdrparams.OperatorControllerPodLabelSelector,
	}

	mdrPods, listErr := pod.List(APIClient, medik8sparams.OperatorNs, listOptions)
	if listErr != nil {
		return fmt.Errorf("failed to list MDR controller pods: %w", listErr)
	}

	filteredPods := helpers.FilterPodsByDeployment(mdrPods, mdrparams.OperatorDeploymentName)
	if len(filteredPods) == 0 {
		return fmt.Errorf("no MDR controller pods found")
	}

	var lastLogErr error

	for _, mdrPod := range filteredPods {
		logStr, logErr := mdrPod.GetLog(logWindow, mdrparams.ManagerContainerName)
		if logErr != nil {
			lastLogErr = fmt.Errorf("pod %s: %w", mdrPod.Object.Name, logErr)

			continue
		}

		if strings.Contains(logStr, message) {
			return nil
		}
	}

	if lastLogErr != nil {
		return fmt.Errorf("message %q not found; last log error: %w", message, lastLogErr)
	}

	return fmt.Errorf("message %q not found in any MDR controller pod logs (last %s)",
		message, logWindow)
}

// logMDRControllerState logs the MDR controller pod states for failure triage.
func logMDRControllerState() {
	pods, err := pod.List(APIClient, medik8sparams.OperatorNs,
		metav1.ListOptions{LabelSelector: mdrparams.OperatorControllerPodLabelSelector})
	if err != nil {
		GinkgoWriter.Printf("logMDRControllerState: failed to list MDR pods: %v\n", err)

		return
	}

	GinkgoWriter.Printf("=== MDR Controller State (%d pods) ===\n", len(pods))

	for _, testPod := range pods {
		phase := testPod.Object.Status.Phase
		ready := "not-ready"

		for _, cs := range testPod.Object.Status.ContainerStatuses {
			if cs.Ready {
				ready = "ready"
			}
		}

		GinkgoWriter.Printf("  %s: phase=%s containers=%s node=%s\n",
			testPod.Object.Name, phase, ready, testPod.Object.Spec.NodeName)
	}
}
