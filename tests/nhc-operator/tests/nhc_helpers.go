package tests

import (
	"context"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/deployment"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/pod"

	"github.com/medik8s/system-tests/tests/internal/helpers"
	. "github.com/medik8s/system-tests/tests/internal/medik8sinittools"
	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
	"github.com/medik8s/system-tests/tests/nhc-operator/internal/nhcparams"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// nhcGVK is the GroupVersionKind for NodeHealthCheck CRs.
var nhcGVK = schema.GroupVersionKind{
	Group:   nhcparams.CRDGroup,
	Version: nhcparams.CRDVersion,
	Kind:    "NodeHealthCheck",
}

// snrGVK is the GroupVersionKind for SelfNodeRemediation CRs.
var snrGVK = schema.GroupVersionKind{
	Group:   nhcparams.SNRCRDGroup,
	Version: nhcparams.SNRCRDVersion,
	Kind:    "SelfNodeRemediation",
}

// snrtGVK is the GroupVersionKind for SelfNodeRemediationTemplate CRs.
var snrtGVK = schema.GroupVersionKind{
	Group:   nhcparams.SNRCRDGroup,
	Version: nhcparams.SNRCRDVersion,
	Kind:    nhcparams.SNRTemplateKind,
}

// buildSNRT builds an unstructured SelfNodeRemediationTemplate CR.
func buildSNRT(name string) *unstructured.Unstructured {
	snrt := &unstructured.Unstructured{}
	snrt.SetGroupVersionKind(snrtGVK)
	snrt.SetName(name)
	snrt.SetNamespace(medik8sparams.OperatorNs)
	snrt.Object["spec"] = map[string]interface{}{
		"template": map[string]interface{}{
			"spec": map[string]interface{}{
				"remediationStrategy": "ResourceDeletion",
			},
		},
	}

	return snrt
}

// cleanupSNRT safely deletes a SelfNodeRemediationTemplate CR by name.
func cleanupSNRT(ctx context.Context, name string) {
	helpers.DeleteRemediationCR(
		ctx, APIClient, snrtGVK, name, medik8sparams.OperatorNs,
		nhcparams.DefaultPollInterval, nhcparams.RemediationCRDeletionTimeout,
		GinkgoWriter.Printf)
}

// buildNHCWithSNRT builds an NHC CR that uses a specific SNRT as the remediator.
func buildNHCWithSNRT(nhcName, snrtName string) *unstructured.Unstructured {
	nhc := buildNHCForWorkers(nhcName)
	spec := nhcSpec(nhc)
	spec["remediationTemplate"] = map[string]interface{}{
		"apiVersion": nhcparams.SNRCRDGroup + "/" + nhcparams.SNRCRDVersion,
		"kind":       nhcparams.SNRTemplateKind,
		"name":       snrtName,
		"namespace":  medik8sparams.OperatorNs,
	}

	return nhc
}

// buildNHCForWorkers builds an unstructured NodeHealthCheck CR that monitors
// worker nodes. Uses SNR as the remediator for these tests; NHC works with
// any operator that provides a remediation template CRD.
func buildNHCForWorkers(name string) *unstructured.Unstructured {
	return buildNHC(name, "node-role.kubernetes.io/worker", "Exists", nil)
}

// buildNHCWithHostnameSelector builds an NHC CR that monitors a single node
// by hostname label. Uses minHealthy=0 because a single-node selector with
// minHealthy=1 blocks remediation entirely (0/1 healthy < 1 required).
func buildNHCWithHostnameSelector(name, hostname string) *unstructured.Unstructured {
	nhc := buildNHC(name, "", "", map[string]interface{}{
		"kubernetes.io/hostname": hostname,
	})

	// Override minHealthy for single-node selector.
	nhcSpec(nhc)["minHealthy"] = int64(0)

	return nhc
}

// buildNHC builds an unstructured NodeHealthCheck CR with configurable selector.
func buildNHC(name, selectorKey, selectorOp string, matchLabels map[string]interface{}) *unstructured.Unstructured {
	nhc := &unstructured.Unstructured{}
	nhc.SetGroupVersionKind(nhcGVK)
	nhc.SetName(name)

	spec := map[string]interface{}{
		"remediationTemplate": map[string]interface{}{
			"apiVersion": nhcparams.SNRCRDGroup + "/" + nhcparams.SNRCRDVersion,
			"kind":       nhcparams.SNRTemplateKind,
			"name":       nhcparams.SNRTemplateName,
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

	if matchLabels != nil {
		spec["selector"] = map[string]interface{}{
			"matchLabels": matchLabels,
		}
	} else {
		spec["selector"] = map[string]interface{}{
			"matchExpressions": []interface{}{
				map[string]interface{}{
					"key":      selectorKey,
					"operator": selectorOp,
				},
			},
		}
	}

	nhc.Object["spec"] = spec

	return nhc
}

// isSNRCRDInstalled checks whether the SelfNodeRemediation CRD is registered.
func isSNRCRDInstalled(ctx context.Context) bool {
	crd := &apiextensionsv1.CustomResourceDefinition{}
	err := APIClient.Get(
		ctx,
		types.NamespacedName{Name: nhcparams.SNRCRDName},
		crd,
	)

	if err == nil {
		return true
	}

	if k8serrors.IsNotFound(err) {
		return false
	}

	Fail(fmt.Sprintf("isSNRCRDInstalled: unexpected error checking CRD %s: %v",
		nhcparams.SNRCRDName, err))

	return false
}

// stopKubeletForRemediation stops kubelet on the target node to trigger
// remediation. SSH is the default because:
//   - oc debug can timeout for 5+ minutes on Prow AWS (unreliable)
//   - SSH is deterministic and fast (~1s via ssh-bastion proxy)
//   - Matches the Python reference implementation (invoke_ssh_on_the_node)
//
// On Prow AWS, SSH traffic is proxied through the ssh-bastion service.
func stopKubeletForRemediation(ctx context.Context, nodeName string) error {
	if medik8sparams.KubeletStopViaOCDebug {
		return helpers.StopKubelet(ctx, nodeName, nhcparams.OCDebugKubeletStopTimeout, GinkgoWriter.Printf)
	}

	return helpers.StopKubeletSSH(ctx, APIClient, nodeName, nhcparams.SSHTimeout)
}

// startKubeletForRemediation starts kubelet on the target node via SSH.
// SSH is required because oc debug cannot schedule a pod on a node
// whose kubelet is stopped. Used by test 4 (OCP-66814) and as a
// safety-net recovery step in JustAfterEach for all destructive tests.
func startKubeletForRemediation(ctx context.Context, nodeName string) error {
	return helpers.StartKubeletSSH(ctx, APIClient, nodeName, nhcparams.SSHTimeout)
}

// isSSHAvailable returns true if an SSH key can be found.
// Used to skip tests that require SSH (e.g. manual kubelet restart).
func isSSHAvailable() bool {
	_, err := helpers.FindSSHKey()

	return err == nil
}

// cleanupNHCCR safely deletes a NodeHealthCheck CR by name.
// NHC CRs are cluster-scoped (namespace is empty).
func cleanupNHCCR(ctx context.Context, name string) {
	helpers.DeleteRemediationCR(
		ctx, APIClient, nhcGVK, name, "",
		nhcparams.DefaultPollInterval, nhcparams.RemediationCRDeletionTimeout,
		GinkgoWriter.Printf)
}

// cleanupSNRCR safely deletes a SelfNodeRemediation CR by name.
// SNR CRs are namespaced in the operator namespace.
func cleanupSNRCR(ctx context.Context, name string) {
	helpers.DeleteRemediationCR(
		ctx, APIClient, snrGVK, name, medik8sparams.OperatorNs,
		nhcparams.DefaultPollInterval, nhcparams.RemediationCRDeletionTimeout,
		GinkgoWriter.Printf)
}

// getNHCPhase returns the current .status.phase of the named NHC CR.
func getNHCPhase(ctx context.Context, name string) (string, error) {
	nhc := &unstructured.Unstructured{}
	nhc.SetGroupVersionKind(nhcGVK)

	if err := APIClient.Get(ctx, client.ObjectKey{Name: name}, nhc); err != nil {
		return "", fmt.Errorf("get NHC %s phase: %w", name, err)
	}

	phase, found, err := unstructured.NestedString(nhc.Object, "status", "phase")
	if err != nil || !found {
		return "", fmt.Errorf("NHC %s has no status.phase", name)
	}

	return phase, nil
}

// getNHCStatusInt64 returns an int64 status field from the named NHC CR.
func getNHCStatusInt64(ctx context.Context, name, field string) (int64, error) {
	nhc := &unstructured.Unstructured{}
	nhc.SetGroupVersionKind(nhcGVK)

	if err := APIClient.Get(ctx, client.ObjectKey{Name: name}, nhc); err != nil {
		return 0, fmt.Errorf("get NHC %s: %w", name, err)
	}

	value, found, err := unstructured.NestedInt64(nhc.Object, "status", field)
	if err != nil || !found {
		return 0, fmt.Errorf("NHC %s has no status.%s", name, field)
	}

	return value, nil
}

// getNHCStatusString returns a string status field from the named NHC CR.
func getNHCStatusString(ctx context.Context, name, field string) (string, error) {
	nhc := &unstructured.Unstructured{}
	nhc.SetGroupVersionKind(nhcGVK)

	if err := APIClient.Get(ctx, client.ObjectKey{Name: name}, nhc); err != nil {
		return "", fmt.Errorf("get NHC %s: %w", name, err)
	}

	value, found, err := unstructured.NestedString(nhc.Object, "status", field)
	if err != nil || !found {
		return "", fmt.Errorf("NHC %s has no status.%s", name, field)
	}

	return value, nil
}

// getNHCObservedNodes returns the .status.observedNodes count from the named NHC CR.
func getNHCObservedNodes(ctx context.Context, name string) (int64, error) {
	return getNHCStatusInt64(ctx, name, "observedNodes")
}

// getNHCHealthyNodes returns the .status.healthyNodes count from the named NHC CR.
func getNHCHealthyNodes(ctx context.Context, name string) (int64, error) {
	return getNHCStatusInt64(ctx, name, "healthyNodes")
}

// getNHCReason returns the current .status.reason of the named NHC CR.
func getNHCReason(ctx context.Context, name string) (string, error) {
	return getNHCStatusString(ctx, name, "reason")
}

// waitForNHCPhase polls until the NHC CR reaches the expected phase.
func waitForNHCPhase(ctx context.Context, name, expectedPhase string, timeout time.Duration) error {
	return wait.PollUntilContextTimeout(
		ctx, nhcparams.DefaultPollInterval, timeout, true,
		func(ctx context.Context) (bool, error) {
			phase, err := getNHCPhase(ctx, name)
			if err != nil {
				// "has no status.phase" means the CR exists but the controller
				// hasn't reconciled yet -- retry.
				if strings.Contains(err.Error(), "has no status.phase") {
					return false, nil
				}

				// Real API errors (RBAC, network) -- fail fast.
				return false, err
			}

			return phase == expectedPhase, nil
		},
	)
}

// waitForSNRRemediationComplete polls until the SNR remediation cycle finishes
// for the given node: SNR CR deleted + boot ID changed.
func waitForSNRRemediationComplete(
	ctx context.Context, nodeName, previousBootID string, timeout time.Duration,
) error {
	var snrSeen bool

	return wait.PollUntilContextTimeout(
		ctx, nhcparams.DestructivePollInterval, timeout, true,
		func(ctx context.Context) (bool, error) {
			obj := &unstructured.Unstructured{}
			obj.SetGroupVersionKind(snrGVK)

			err := APIClient.Get(ctx, types.NamespacedName{
				Name:      nodeName,
				Namespace: medik8sparams.OperatorNs,
			}, obj)

			switch {
			case err == nil:
				if !snrSeen {
					GinkgoWriter.Printf("SNR CR %s detected -- remediation in progress\n", nodeName)
					snrSeen = true
				}

				return false, nil

			case k8serrors.IsNotFound(err):
				// SNR CR gone. Check if boot ID changed (node rebooted).
				currentBootID, bootErr := helpers.GetNodeBootIDFromAPI(ctx, APIClient, nodeName)
				if bootErr != nil {
					return false, nil
				}

				if currentBootID != previousBootID {
					if snrSeen {
						GinkgoWriter.Printf("SNR remediation complete: boot ID changed for %s\n", nodeName)
					} else {
						GinkgoWriter.Printf(
							"SNR CR already gone, boot ID changed -- "+
								"remediation completed before observation for %s\n", nodeName)
					}

					return true, nil
				}

				return false, nil

			default:
				return false, nil
			}
		},
	)
}

// logNHCControllerState logs NHC controller pod states for failure triage.
func logNHCControllerState() {
	pods, err := pod.List(APIClient, medik8sparams.OperatorNs,
		metav1.ListOptions{LabelSelector: nhcparams.OperatorControllerPodLabelSelector})
	if err != nil {
		GinkgoWriter.Printf("logNHCControllerState: failed to list pods: %v\n", err)

		return
	}

	GinkgoWriter.Printf("=== NHC Controller State (%d pods) ===\n", len(pods))

	for _, p := range pods {
		phase := p.Object.Status.Phase
		ready := "ready"

		for _, cs := range p.Object.Status.ContainerStatuses {
			if !cs.Ready {
				ready = "not-ready"

				break
			}
		}

		if len(p.Object.Status.ContainerStatuses) == 0 {
			ready = "no-containers"
		}

		GinkgoWriter.Printf("  %s: phase=%s containers=%s node=%s\n",
			p.Object.Name, phase, ready, p.Object.Spec.NodeName)
	}
}

// countReadyControllerPods returns the number of NHC controller pods that are
// Running with all containers ready.
func countReadyControllerPods(_ context.Context, labelSelector string) (int, error) {
	pods, err := pod.List(APIClient, medik8sparams.OperatorNs,
		metav1.ListOptions{LabelSelector: labelSelector})
	if err != nil {
		return 0, err
	}

	ready := 0

	for _, p := range pods {
		// Skip terminating pods (being evicted from stopped-kubelet nodes)
		if p.Object.DeletionTimestamp != nil {
			continue
		}

		if p.Object.Status.Phase != corev1.PodRunning {
			continue
		}

		// Skip pods with no container statuses yet
		if len(p.Object.Status.ContainerStatuses) == 0 {
			continue
		}

		allReady := true

		for _, cs := range p.Object.Status.ContainerStatuses {
			if !cs.Ready {
				allReady = false

				break
			}
		}

		if allReady {
			ready++
		}
	}

	return ready, nil
}

// TestRemediation dummy CRD helpers (for multi-NHC test OCP-66814).

// testRemediationGVK is the GVK for TestRemediation CRs.
var testRemediationGVK = schema.GroupVersionKind{
	Group:   nhcparams.TestRemediationGroup,
	Version: nhcparams.TestRemediationVersion,
	Kind:    "TestRemediation",
}

// buildTestCRD creates a cluster-scoped CRD with spec/status preserve-unknown schema.
func buildTestCRD(
	name, kind, plural, singular, shortName string,
) *apiextensionsv1.CustomResourceDefinition {
	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: nhcparams.TestRemediationGroup,
			Scope: apiextensionsv1.ClusterScoped,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Kind:       kind,
				Plural:     plural,
				Singular:   singular,
				ShortNames: []string{shortName},
			},
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{
				Name:    nhcparams.TestRemediationVersion,
				Served:  true,
				Storage: true,
				Subresources: &apiextensionsv1.CustomResourceSubresources{
					Status: &apiextensionsv1.CustomResourceSubresourceStatus{},
				},
				Schema: &apiextensionsv1.CustomResourceValidation{
					OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
						Type: "object",
						Properties: map[string]apiextensionsv1.JSONSchemaProps{
							"spec":   {Type: "object", XPreserveUnknownFields: ptr.To(true)},
							"status": {Type: "object", XPreserveUnknownFields: ptr.To(true)},
						},
					},
				},
			}},
		},
	}
}

// setupTestRemediationResources creates the dummy TestRemediation CRDs,
// template CR, and RBAC needed for multi-NHC tests.
func setupTestRemediationResources(ctx context.Context) {
	By("Creating TestRemediationTemplate CRD")

	trtCRD := buildTestCRD(nhcparams.TestRemediationTemplateCRDName,
		"TestRemediationTemplate", "testremediationtemplates", "testremediationtemplate", "trt")

	if err := APIClient.Create(ctx, trtCRD); err != nil && !k8serrors.IsAlreadyExists(err) {
		Fail(fmt.Sprintf("Failed to create TestRemediationTemplate CRD: %v", err))
	}

	By("Creating TestRemediation CRD")

	trCRD := buildTestCRD(nhcparams.TestRemediationCRDName,
		"TestRemediation", "testremediations", "testremediation", "tr")

	if err := APIClient.Create(ctx, trCRD); err != nil && !k8serrors.IsAlreadyExists(err) {
		Fail(fmt.Sprintf("Failed to create TestRemediation CRD: %v", err))
	}

	By("Waiting for CRDs to become Established")

	waitForCRDEstablished(ctx, nhcparams.TestRemediationTemplateCRDName)
	waitForCRDEstablished(ctx, nhcparams.TestRemediationCRDName)

	By("Creating TestRemediationTemplate CR")

	trtCR := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": nhcparams.TestRemediationGroup + "/" + nhcparams.TestRemediationVersion,
			"kind":       "TestRemediationTemplate",
			"metadata": map[string]interface{}{
				"name": nhcparams.TestRemediationTemplateName,
			},
			"spec": map[string]interface{}{
				"template": map[string]interface{}{
					"spec": map[string]interface{}{
						"strategy": map[string]interface{}{
							"retryLimit": int64(1),
							"timeout":    "5m0s",
							"type":       "Wait",
						},
					},
				},
			},
		},
	}

	if err := APIClient.Create(ctx, trtCR); err != nil && !k8serrors.IsAlreadyExists(err) {
		Fail(fmt.Sprintf("Failed to create TestRemediationTemplate CR: %v", err))
	}

	By("Creating RBAC for NHC to manage TestRemediation CRs")

	clusterRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: nhcparams.TestRemediationClusterRoleName,
		},
		Rules: []rbacv1.PolicyRule{{
			APIGroups: []string{nhcparams.TestRemediationGroup},
			Resources: []string{"testremediations", "testremediations/status", "testremediationtemplates"},
			Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
		}},
	}

	if err := APIClient.Create(ctx, clusterRole); err != nil && !k8serrors.IsAlreadyExists(err) {
		Fail(fmt.Sprintf("Failed to create ClusterRole: %v", err))
	}

	clusterRoleBinding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: nhcparams.TestRemediationClusterRoleBindingName,
		},
		Subjects: []rbacv1.Subject{{
			Kind:      "ServiceAccount",
			Name:      nhcparams.NHCControllerServiceAccount,
			Namespace: medik8sparams.OperatorNs,
		}},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     nhcparams.TestRemediationClusterRoleName,
		},
	}

	if err := APIClient.Create(ctx, clusterRoleBinding); err != nil && !k8serrors.IsAlreadyExists(err) {
		Fail(fmt.Sprintf("Failed to create ClusterRoleBinding: %v", err))
	}
}

// cleanupTestRemediationResources removes all TestRemediation CRDs, CRs, and RBAC.
// Uses retry + NotFound handling consistent with cleanupNHCCR/cleanupSNRCR.
func cleanupTestRemediationResources(ctx context.Context) {
	deleteWithRetry := func(obj client.Object, desc string) {
		if waitErr := wait.PollUntilContextTimeout(
			ctx, nhcparams.DefaultPollInterval, nhcparams.RemediationCRDeletionTimeout, true,
			func(ctx context.Context) (bool, error) {
				if err := APIClient.Get(ctx, client.ObjectKeyFromObject(obj), obj); err != nil {
					if k8serrors.IsNotFound(err) {
						return true, nil
					}

					return false, nil
				}

				if delErr := APIClient.Delete(ctx, obj); delErr != nil {
					if k8serrors.IsNotFound(delErr) {
						return true, nil
					}

					return false, nil
				}

				return false, nil
			},
		); waitErr != nil {
			GinkgoWriter.Printf("Warning: %s not fully deleted within %s: %v\n",
				desc, nhcparams.RemediationCRDeletionTimeout, waitErr)
		}
	}

	// Delete CRs first
	trtCR := &unstructured.Unstructured{}
	trtCR.SetGroupVersionKind(schema.GroupVersionKind{
		Group: nhcparams.TestRemediationGroup, Version: nhcparams.TestRemediationVersion, Kind: "TestRemediationTemplate"})
	trtCR.SetName(nhcparams.TestRemediationTemplateName)

	deleteWithRetry(trtCR, "TestRemediationTemplate CR")

	// Delete RBAC
	deleteWithRetry(&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: nhcparams.TestRemediationClusterRoleBindingName}}, "ClusterRoleBinding")
	deleteWithRetry(&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: nhcparams.TestRemediationClusterRoleName}}, "ClusterRole")

	// Delete CRDs (this also GCs all CRs)
	deleteWithRetry(&apiextensionsv1.CustomResourceDefinition{ObjectMeta: metav1.ObjectMeta{Name: nhcparams.TestRemediationCRDName}}, "TestRemediation CRD")
	deleteWithRetry(&apiextensionsv1.CustomResourceDefinition{ObjectMeta: metav1.ObjectMeta{Name: nhcparams.TestRemediationTemplateCRDName}}, "TestRemediationTemplate CRD")
}

// buildNHCWithTestRemediation builds an NHC CR that uses the TestRemediation
// template instead of SNR, with a 10s unhealthy duration. Reuses
// buildNHCForWorkers for the base spec and overrides the remediator.
func buildNHCWithTestRemediation(name string) *unstructured.Unstructured {
	nhc := buildNHCForWorkers(name)
	spec := nhcSpec(nhc)

	// TestRemediationTemplate is cluster-scoped, so no namespace is needed.
	spec["remediationTemplate"] = map[string]interface{}{
		"apiVersion": nhcparams.TestRemediationGroup + "/" + nhcparams.TestRemediationVersion,
		"kind":       "TestRemediationTemplate",
		"name":       nhcparams.TestRemediationTemplateName,
	}

	spec["unhealthyConditions"] = []interface{}{
		map[string]interface{}{
			"type": "Ready", "status": "False", "duration": "10s",
		},
		map[string]interface{}{
			"type": "Ready", "status": "Unknown", "duration": "10s",
		},
	}

	return nhc
}

// testRemediationCRExists checks if a TestRemediation CR exists for the given node.
// Returns (bool, error) so Gomega propagates API failures instead of treating
// transient errors as "not found".
func testRemediationCRExists(ctx context.Context, nodeName string) (bool, error) {
	tr := &unstructured.Unstructured{}
	tr.SetGroupVersionKind(testRemediationGVK)

	err := APIClient.Get(ctx, client.ObjectKey{Name: nodeName}, tr)
	if err == nil {
		return true, nil
	}

	if k8serrors.IsNotFound(err) {
		return false, nil
	}

	return false, err
}

// waitForCRDEstablished polls until a CRD's Established condition is True.
// This is needed after creating a CRD before creating CRs of that type,
// because the API server needs time to register the new resource.
func waitForCRDEstablished(ctx context.Context, crdName string) {
	if waitErr := wait.PollUntilContextTimeout(
		ctx, 2*time.Second, 60*time.Second, true,
		func(ctx context.Context) (bool, error) {
			crd := &apiextensionsv1.CustomResourceDefinition{}
			if err := APIClient.Get(ctx, types.NamespacedName{Name: crdName}, crd); err != nil {
				return false, nil
			}

			for _, cond := range crd.Status.Conditions {
				if cond.Type == apiextensionsv1.Established && cond.Status == apiextensionsv1.ConditionTrue {
					return true, nil
				}
			}

			return false, nil
		},
	); waitErr != nil {
		Fail(fmt.Sprintf("CRD %s did not become Established within 60s: %v", crdName, waitErr))
	}
}

// snrCRExists checks if a SelfNodeRemediation CR exists for the given node.
// NHC creates SNR CRs with the node name as a prefix plus a random suffix
// (e.g. "worker-0-8lwbf"), so this uses List + prefix match instead of
// exact Get (matching the Python reference: re.search(node_name, cr.name)).
// Returns (bool, error) so Gomega propagates API failures instead of treating
// transient errors as "not found".
func snrCRExists(ctx context.Context, nodeName string) (bool, error) {
	snrList := &unstructured.UnstructuredList{}
	snrList.SetGroupVersionKind(snrGVK)

	if err := APIClient.List(ctx, snrList, client.InNamespace(medik8sparams.OperatorNs)); err != nil {
		return false, err
	}

	for i := range snrList.Items {
		if strings.HasPrefix(snrList.Items[i].GetName(), nodeName) {
			return true, nil
		}
	}

	return false, nil
}

// nhcSpec returns the spec map of an NHC unstructured object built by the
// local builders. It fails the test if the shape is unexpected.
func nhcSpec(nhc *unstructured.Unstructured) map[string]interface{} {
	GinkgoHelper()

	spec, ok := nhc.Object["spec"].(map[string]interface{})
	Expect(ok).To(BeTrue(), "NHC object has no map spec")

	return spec
}

// nhcUnhealthyConditions returns the unhealthyConditions slice from an NHC spec.
func nhcUnhealthyConditions(spec map[string]interface{}) []interface{} {
	GinkgoHelper()

	conditions, ok := spec["unhealthyConditions"].([]interface{})
	Expect(ok).To(BeTrue(), "NHC spec has no unhealthyConditions slice")

	return conditions
}

// verifyNHCNodeCount polls until the given int64 status field matches the expected value.
// Used for healthyNodes and observedNodes assertions.
func verifyNHCNodeCount(
	ctx context.Context, nhcName string,
	getter func(context.Context, string) (int64, error),
	expected int64, timeout time.Duration, msg string,
) {
	GinkgoHelper()

	Eventually(func(g Gomega) {
		value, err := getter(ctx, nhcName)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(value).To(Equal(expected), msg)
	}).WithPolling(nhcparams.DefaultPollInterval).
		WithTimeout(timeout).Should(Succeed())
}

// verifyNHCDeploymentReady checks that the NHC controller deployment exists and is ready.
func verifyNHCDeploymentReady() {
	GinkgoHelper()

	nhcDeployment, err := deployment.Pull(
		APIClient, nhcparams.OperatorDeploymentName, medik8sparams.OperatorNs)
	Expect(err).ToNot(HaveOccurred(), "Failed to get NHC deployment")
	Expect(nhcDeployment.IsReady(medik8sparams.DefaultTimeout)).To(BeTrue(),
		"NHC deployment is not Ready")
}

// verifyNHCNotCreated asserts that an NHC CR with the given name does not exist.
// Used after webhook-rejected creation attempts where the API server synchronously
// refused the request, so the CR should never appear.
func verifyNHCNotCreated(ctx context.Context, nhcName string) {
	notCreated := &unstructured.Unstructured{}
	notCreated.SetGroupVersionKind(nhcGVK)

	getErr := APIClient.Get(ctx, client.ObjectKey{Name: nhcName}, notCreated)

	if getErr == nil {
		Fail(fmt.Sprintf("NHC CR %q exists but should not have been created", nhcName))
	}

	Expect(k8serrors.IsNotFound(getErr)).To(BeTrue(),
		"NHC CR %q: expected NotFound, got: %v", nhcName, getErr)
}

// verifyNHCPhaseAndReason waits for NHC to reach the expected phase and then
// verifies the status.reason contains the expected substring.
func verifyNHCPhaseAndReason(ctx context.Context, nhcName, phase, expectedReason string, timeout time.Duration) {
	GinkgoHelper()

	Expect(waitForNHCPhase(ctx, nhcName, phase, timeout)).To(Succeed(),
		"NHC %q should reach phase %s", nhcName, phase)

	Eventually(func(g Gomega) {
		reason, err := getNHCReason(ctx, nhcName)
		g.Expect(err).ToNot(HaveOccurred(), "Failed to get NHC %q reason", nhcName)
		g.Expect(reason).To(ContainSubstring(expectedReason),
			"NHC %q reason should contain %q", nhcName, expectedReason)
	}).WithPolling(nhcparams.DefaultPollInterval).
		WithTimeout(timeout).Should(Succeed())
}

// verifyNHCDisabledWithReason waits for NHC to reach Disabled phase and then
// verifies the status.reason contains the expected substring.
func verifyNHCDisabledWithReason(ctx context.Context, nhcName, expectedReason string, timeout time.Duration) {
	GinkgoHelper()
	verifyNHCPhaseAndReason(ctx, nhcName, nhcparams.NHCPhaseDisabled, expectedReason, timeout)
}

// waitForNHCGone polls until the named NHC CR is confirmed deleted.
// Returns (bool, error) so Gomega fails fast on non-NotFound API errors
// instead of retrying until timeout.
func waitForNHCGone(ctx context.Context, nhcName string) {
	GinkgoHelper()

	Eventually(ctx, func() (bool, error) {
		obj := &unstructured.Unstructured{}
		obj.SetGroupVersionKind(nhcGVK)

		err := APIClient.Get(ctx, client.ObjectKey{Name: nhcName}, obj)
		if k8serrors.IsNotFound(err) {
			return true, nil
		}

		if err != nil {
			return false, err
		}

		return false, nil
	}).WithPolling(nhcparams.DefaultPollInterval).
		WithTimeout(nhcparams.RemediationCRDeletionTimeout).Should(BeTrue(),
		"NHC CR %q still exists after cleanup", nhcName)
}
