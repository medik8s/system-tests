package tests

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"
	oplmV1alpha1 "github.com/rh-ecosystem-edge/eco-goinfra/pkg/schemes/olm/operators/v1alpha1"

	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/deployment"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/infrastructure"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/olm"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/pod"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/reportxml"

	"github.com/medik8s/system-tests/tests/internal/helpers"
	"github.com/medik8s/system-tests/tests/internal/labels"
	. "github.com/medik8s/system-tests/tests/internal/medik8sinittools"
	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
	"github.com/medik8s/system-tests/tests/sbr-operator/internal/sbrparams"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

// WatchdogDevicesByNode stores the /dev/watchdog* paths found on each node.
// Populated by the "SBR Debug — Cluster Watchdog Inventory" suite; readable by subsequent tests.
var WatchdogDevicesByNode map[string][]string

// watchdogDebugPodName returns a valid pod name for the per-node watchdog discovery pod.
func watchdogDebugPodName(nodeName string) string {
	safe := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}

		return '-'
	}, strings.ToLower(nodeName))

	name := "sbr-wdog-dbg-" + safe
	if len(name) > 253 {
		name = name[:253]
	}

	name = strings.TrimRight(name, "-")

	return name
}

var _ = Describe(
	"SBR Debug — Cluster Watchdog Inventory",
	Ordered,
	Label(labels.OperatorSBR, labels.ComponentPostDeploy), func() {
		It("Discover /dev/watchdog* devices on all cluster nodes",
			reportxml.ID("90163"),
			Label(
				labels.DisruptionNonDestructive,
				labels.TierSmoke,
				labels.PlatformAny,
				labels.ComponentController,
				labels.FrequencyPresubmit,
			), func() {
				WatchdogDevicesByNode = make(map[string][]string)

				nodeList, err := APIClient.CoreV1Interface.Nodes().List(context.TODO(), metav1.ListOptions{})
				Expect(err).ToNot(HaveOccurred(), "Failed to list cluster nodes for watchdog inventory")

				By(fmt.Sprintf("Probing %d node(s) for /dev/watchdog* devices", len(nodeList.Items)))

				for i := range nodeList.Items {
					nodeName := nodeList.Items[i].Name
					podName := watchdogDebugPodName(nodeName)

					By(fmt.Sprintf("Creating watchdog discovery pod on node %s", nodeName))

					debugPod, createErr := pod.NewBuilder(
						APIClient, podName, medik8sparams.OperatorNs, sbrparams.WatchdogDebugImage).
						DefineOnNode(nodeName).
						WithHostPid(true).
						WithPrivilegedFlag().
						CreateAndWaitUntilRunning(medik8sparams.DefaultTimeout)
					if createErr != nil {
						GinkgoWriter.Printf("Warning: could not create watchdog debug pod for node %s: %v\n",
							nodeName, createErr)
						WatchdogDevicesByNode[nodeName] = nil

						continue
					}

					// Safety net: ensure the privileged pod is deleted even if the inline Delete below fails.
					DeferCleanup(func(name string) {
						if existing, pullErr := pod.Pull(APIClient, name, medik8sparams.OperatorNs); pullErr == nil {
							if _, delErr := existing.Delete(); delErr != nil {
								GinkgoWriter.Printf("DeferCleanup: failed to delete watchdog debug pod %s: %v\n",
									name, delErr)
							}
						}
					}, podName)

					// /proc/1/root is the host's root filesystem inside a hostPID+privileged container.
					buf, execErr := debugPod.ExecCommand(
						[]string{"sh", "-c", "ls -1 /proc/1/root/dev/watchdog* 2>/dev/null || true"})

					if _, delErr := debugPod.Delete(); delErr != nil {
						GinkgoWriter.Printf("Warning: failed to delete watchdog debug pod for node %s: %v\n",
							nodeName, delErr)
					}

					if execErr != nil {
						GinkgoWriter.Printf("Warning: exec failed on node %s: %v\n", nodeName, execErr)
						WatchdogDevicesByNode[nodeName] = nil

						continue
					}

					// Use a non-nil empty slice so that "no devices found" is distinguishable
					// from "probe failed" (nil) — the watchdog integration test fast path
					// relies on this three-state contract.
					devices := make([]string, 0)

					for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
						for _, token := range strings.Fields(line) {
							devices = append(devices, strings.TrimPrefix(token, "/proc/1/root"))
						}
					}

					WatchdogDevicesByNode[nodeName] = devices
				}

				GinkgoWriter.Println("=== /dev/watchdog* Inventory ===")

				for _, node := range nodeList.Items {
					switch devs := WatchdogDevicesByNode[node.Name]; {
					case devs == nil:
						GinkgoWriter.Printf("  %s: probe-failed\n", node.Name)
					case len(devs) == 0:
						GinkgoWriter.Printf("  %s: none\n", node.Name)
					default:
						GinkgoWriter.Printf("  %s: %v\n", node.Name, devs)
					}
				}
			})
	})

// isNodeSchedulable returns true when a node is Ready and not cordoned.
// NotReady and unschedulable nodes are excluded so that scheduling failures
// are not misattributed to failures in the operator under test.
func isNodeSchedulable(node *corev1.Node) bool {
	if node.Spec.Unschedulable {
		return false
	}

	for _, cond := range node.Status.Conditions {
		if cond.Type == corev1.NodeReady {
			return cond.Status == corev1.ConditionTrue
		}
	}

	return false
}

func fetchActiveCSV() *olm.ClusterServiceVersionBuilder {
	var sbrCSV *olm.ClusterServiceVersionBuilder

	Eventually(func() error {
		sbrCSVs, err := olm.ListClusterServiceVersionWithNamePattern(
			APIClient, sbrparams.CSVNamePattern, medik8sparams.OperatorNs)
		if err != nil {
			return fmt.Errorf("failed to list SBR ClusterServiceVersions: %w", err)
		}

		if len(sbrCSVs) == 0 {
			return fmt.Errorf("no SBR ClusterServiceVersion found in namespace %s", medik8sparams.OperatorNs)
		}

		sbrCSV = helpers.FindActiveCSV(sbrCSVs)
		if sbrCSV == nil {
			return fmt.Errorf("no SBR CSV in Succeeded phase found yet")
		}

		return nil
	}, medik8sparams.DefaultTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
		"SBR CSV must reach Succeeded phase")

	return sbrCSV
}

var _ = Describe(
	"SBR Post Deployment tests",
	Ordered,
	ContinueOnFailure,
	Label(labels.OperatorSBR, labels.ComponentPostDeploy), func() {
		var controlPlaneTopology configv1.TopologyMode

		BeforeAll(func() {
			By("Get SBR deployment object and verify it is Ready")

			sbrDeployment, err := deployment.Pull(
				APIClient, sbrparams.OperatorDeploymentName, medik8sparams.OperatorNs)
			Expect(err).ToNot(HaveOccurred(), "Failed to get SBR deployment")
			Expect(sbrDeployment.IsReady(medik8sparams.DefaultTimeout)).To(BeTrue(),
				"SBR deployment is not Ready")

			By("Pull cluster topology for use in topology-aware tests")

			infraConfig, infraErr := infrastructure.Pull(APIClient)
			Expect(infraErr).ToNot(HaveOccurred(), "Failed to pull infrastructure configuration")

			controlPlaneTopology = infraConfig.Object.Status.ControlPlaneTopology
		})

		It("Verify Storage-Based Remediation Operator pod is running",
			reportxml.ID("89232"),
			Label(
				labels.DisruptionNonDestructive,
				labels.TierSmoke,
				labels.PlatformAny,
				labels.ComponentController,
				labels.FrequencyPresubmit,
			), func() {
				expectedCount := sbrparams.ExpectedReplicas
				if controlPlaneTopology == configv1.SingleReplicaTopologyMode {
					expectedCount = int32(1)
				}

				listOptions := metav1.ListOptions{LabelSelector: sbrparams.OperatorControllerPodLabelSelector}

				By("Verifying pod count matches expected replicas")

				Eventually(func() error {
					sbrPods, listErr := pod.List(APIClient, medik8sparams.OperatorNs, listOptions)
					if listErr != nil {
						return listErr
					}

					for _, sbrPod := range sbrPods {
						if sbrPod.Object.DeletionTimestamp != nil {
							continue
						}

						if sbrPod.Object.Status.Phase != corev1.PodRunning {
							return fmt.Errorf("pod %s is in phase %s, expected Running",
								sbrPod.Object.Name, sbrPod.Object.Status.Phase)
						}
					}

					runningCount := int32(len(helpers.FilterRunningPods(sbrPods)))

					if runningCount != expectedCount {
						return fmt.Errorf("expected %d running SBR pod(s), found %d",
							expectedCount, runningCount)
					}

					return nil
				}, medik8sparams.DefaultTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
					"SBR pods did not reach expected running count of %d", expectedCount)
			})

		It("Verify SBR CSV has required annotations",
			reportxml.ID("89233"),
			Label(
				labels.DisruptionNonDestructive,
				labels.TierSmoke,
				labels.PlatformAny,
				labels.ComponentOLM,
				labels.FrequencyPresubmit,
			), func() {
				By("Getting SBR ClusterServiceVersion")

				By("Finding the active (Succeeded) CSV")

				sbrCSV := fetchActiveCSV()

				By("Checking annotation values on SBR CSV")

				Expect(sbrCSV.Object.Annotations).ToNot(BeNil(), "CSV annotations should not be nil")

				var annotationErrors []string

				for annotationKey, expectedValue := range sbrparams.RequiredAnnotations {
					annotationValue, exists := sbrCSV.Object.Annotations[annotationKey]
					if !exists {
						annotationErrors = append(annotationErrors,
							fmt.Sprintf("required annotation %q is missing", annotationKey))

						continue
					}

					if annotationValue != expectedValue {
						annotationErrors = append(annotationErrors,
							fmt.Sprintf("annotation %q: expected %q, got %q",
								annotationKey, expectedValue, annotationValue))
					}
				}

				if len(annotationErrors) > 0 {
					errMsg := "SBR CSV annotation validation failures:\n"
					for _, msg := range annotationErrors {
						errMsg += fmt.Sprintf("- %s\n", msg)
					}

					Fail(errMsg)
				}
			})

		It("Verify SBR controller manager has correct number of replicas",
			reportxml.ID("89234"),
			Label(
				labels.DisruptionNonDestructive,
				labels.TierSmoke,
				labels.PlatformAny,
				labels.ComponentController,
				labels.FrequencyPresubmit,
			), func() {
				By("Checking cluster topology")

				if controlPlaneTopology == configv1.SingleReplicaTopologyMode {
					Skip("Skipping test on SNO (Single Node OpenShift) cluster")
				}

				By("Verifying replica count, ready replicas, and pod HA distribution")

				listOptions := metav1.ListOptions{LabelSelector: sbrparams.OperatorControllerPodLabelSelector}

				Eventually(func() error {
					liveDeploy, pullErr := deployment.Pull(
						APIClient, sbrparams.OperatorDeploymentName, medik8sparams.OperatorNs)
					if pullErr != nil {
						return pullErr
					}

					if liveDeploy.Object.Spec.Replicas == nil ||
						*liveDeploy.Object.Spec.Replicas != sbrparams.ExpectedReplicas {
						desired := int32(0)
						if liveDeploy.Object.Spec.Replicas != nil {
							desired = *liveDeploy.Object.Spec.Replicas
						}

						return fmt.Errorf("expected %d desired replica(s), found %d",
							sbrparams.ExpectedReplicas, desired)
					}

					if liveDeploy.Object.Status.ReadyReplicas != sbrparams.ExpectedReplicas {
						return fmt.Errorf("expected %d ready replica(s), found %d",
							sbrparams.ExpectedReplicas, liveDeploy.Object.Status.ReadyReplicas)
					}

					sbrPods, listErr := pod.List(APIClient, medik8sparams.OperatorNs, listOptions)
					if listErr != nil {
						return listErr
					}

					runningPods := helpers.FilterRunningPods(sbrPods)

					if len(runningPods) != int(sbrparams.ExpectedReplicas) {
						return fmt.Errorf("expected %d running SBR pod(s) for HA check, found %d",
							sbrparams.ExpectedReplicas, len(runningPods))
					}

					nodeNames := make(map[string]bool)

					for _, p := range runningPods {
						if p.Object.Spec.NodeName == "" {
							return fmt.Errorf("pod %s has not been assigned to a node", p.Object.Name)
						}

						nodeNames[p.Object.Spec.NodeName] = true
					}

					if len(nodeNames) != int(sbrparams.ExpectedReplicas) {
						return fmt.Errorf(
							"SBR pods must run on different nodes for HA, found pods on %d unique node(s)",
							len(nodeNames))
					}

					return nil
				}, medik8sparams.DefaultTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
					"SBR deployment did not stabilise at %d ready replicas on distinct nodes",
					sbrparams.ExpectedReplicas)
			})

		It("Verify SBR container runs as non-root user",
			reportxml.ID("89235"),
			Label(
				labels.DisruptionNonDestructive,
				labels.TierSmoke,
				labels.PlatformAny,
				labels.ComponentController,
				labels.FrequencyPresubmit,
			), func() {
				By("Getting SBR controller pod names")

				listOptions := metav1.ListOptions{LabelSelector: sbrparams.OperatorControllerPodLabelSelector}

				var runningPods []*pod.Builder

				Eventually(func() error {
					sbrPods, listErr := pod.List(APIClient, medik8sparams.OperatorNs, listOptions)
					if listErr != nil {
						return listErr
					}

					running := helpers.FilterRunningPods(sbrPods)
					if len(running) == 0 {
						return fmt.Errorf("no running SBR controller pods found")
					}

					runningPods = running

					return nil
				}, medik8sparams.DefaultTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
					"At least one running SBR controller pod should be found")

				errorMessages := helpers.ValidateNonRootSecurityContext(
					runningPods, sbrparams.ManagerContainerName, false)

				if len(errorMessages) > 0 {
					Fail("Testing security context of SBR container failed due to:\n- " +
						strings.Join(errorMessages, "\n- "))
				}
			})

		It("Verify SBR uses correct API and OLM naming",
			reportxml.ID("88822"),
			Label(
				labels.OperatorSBR,
				labels.DisruptionNonDestructive,
				labels.TierSmoke,
				labels.PlatformAny,
				labels.ComponentOLM,
				labels.FrequencyPresubmit,
			), func() {
				By("Getting active SBR ClusterServiceVersion")

				sbrCSV := fetchActiveCSV()

				By("Verifying CSV display name uses Storage-Based Remediation naming (not SBD)")
				Expect(sbrCSV.Object.Spec.DisplayName).To(ContainSubstring("Storage-Based Remediation"),
					"CSV display name should contain 'Storage-Based Remediation' (not 'SBD'), got: %q",
					sbrCSV.Object.Spec.DisplayName)
				Expect(sbrCSV.Object.Spec.DisplayName).ToNot(ContainSubstring("SBD"),
					"CSV display name should not use 'SBD' naming, got: %q",
					sbrCSV.Object.Spec.DisplayName)

				By(fmt.Sprintf("Verifying all owned CRDs use API group %s", sbrparams.CRDGroup))

				ownedCRDs := sbrCSV.Object.Spec.CustomResourceDefinitions.Owned
				Expect(ownedCRDs).ToNot(BeEmpty(), "CSV should declare at least one owned CRD")

				for _, expectedKind := range sbrparams.ExpectedCRDKinds {
					By(fmt.Sprintf("Checking owned CRD for kind %s", expectedKind))

					var matchedCRD *oplmV1alpha1.CRDDescription

					for i := range ownedCRDs {
						if ownedCRDs[i].Kind == expectedKind {
							matchedCRD = &ownedCRDs[i]

							break
						}
					}

					Expect(matchedCRD).ToNot(BeNil(),
						"CSV should own a CRD with kind %s", expectedKind)
					Expect(matchedCRD.Name).To(ContainSubstring(sbrparams.CRDGroup),
						"CRD %s name %q should include API group %s", expectedKind, matchedCRD.Name, sbrparams.CRDGroup)
					Expect(matchedCRD.Version).To(Equal(sbrparams.CRDVersion),
						"CRD %s should be at version %s", expectedKind, sbrparams.CRDVersion)
				}
			})
	})

func snapshotDaemonSetNames() map[string]bool {
	dsList, listErr := APIClient.DaemonSets(medik8sparams.OperatorNs).List(
		context.TODO(), metav1.ListOptions{})
	Expect(listErr).ToNot(HaveOccurred(), "Failed to list DaemonSets in operator namespace")

	names := make(map[string]bool, len(dsList.Items))
	for _, ds := range dsList.Items {
		names[ds.Name] = true
	}

	return names
}

// buildSBRUnstructured returns an unstructured SBR CR for the given kind and name.
// It is the shared builder for buildSBRC and buildSBR.
func buildSBRUnstructured(kind, name string, spec map[string]interface{}) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": sbrparams.CRDGroup + "/" + sbrparams.CRDVersion,
			"kind":       kind,
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": medik8sparams.OperatorNs,
			},
			"spec": spec,
		},
	}
}

func buildSBRC(name string, spec map[string]interface{}) *unstructured.Unstructured {
	return buildSBRUnstructured("StorageBasedRemediationConfig", name, spec)
}

// discoverRWXStorageClass returns a StorageClass name that supports ReadWriteMany.
// Reads SBR_STORAGE_CLASS env var first; auto-discovers by filtering CephFS provisioners.
// Calls Skip when no CephFS class is found and the env var is unset.
func discoverRWXStorageClass() string {
	if sbrparams.SBRStorageClass != "" {
		return sbrparams.SBRStorageClass
	}

	scList, err := APIClient.StorageV1Interface.StorageClasses().List(context.TODO(), metav1.ListOptions{})
	Expect(err).ToNot(HaveOccurred(), "Failed to list StorageClasses for RWX auto-discovery")

	for idx := range scList.Items {
		provisioner := scList.Items[idx].Provisioner
		if strings.Contains(provisioner, "cephfs") {
			GinkgoWriter.Printf("Auto-discovered CephFS StorageClass: %s (provisioner: %s)\n",
				scList.Items[idx].Name, provisioner)

			return scList.Items[idx].Name
		}
	}

	Skip("No CephFS StorageClass found; set SBR_STORAGE_CLASS env var to override")

	return ""
}

// waitForSBRCReady blocks until all pods in the agent DaemonSet for the named SBRC are ready.
// The SBRRemediationReconciler runs inside agent pods, so this must be called before creating
// any StorageBasedRemediation CR whose reconciliation depends on active agents. Waiting for all
// pods (not just the first) ensures the target node's agent has had time to complete its initial
// storage health check before a CR targeting that node is created.
func waitForSBRCReady(sbrcName string) {
	dsName := sbrparams.SBRAgentDaemonSetPrefix + sbrcName

	Eventually(func() error {
		agentDS, err := APIClient.DaemonSets(medik8sparams.OperatorNs).Get(
			context.TODO(), dsName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("DaemonSet %s not found: %w", dsName, err)
		}

		if agentDS.Status.DesiredNumberScheduled == 0 || agentDS.Status.NumberReady < agentDS.Status.DesiredNumberScheduled {
			return fmt.Errorf("DaemonSet %s: %d/%d pods ready",
				dsName, agentDS.Status.NumberReady, agentDS.Status.DesiredNumberScheduled)
		}

		return nil
	}, sbrparams.SBRCReadyTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
		// Gomega calls this lazily, only on timeout, so the diagnostics land directly in the
		// Ginkgo [FAILED] block (and therefore in the Prow log) without any happy-path cost.
		func() string {
			return fmt.Sprintf(
				"SBRC %q agent DaemonSet must have all pods ready before functional tests begin\n%s",
				sbrcName, sbrcReadinessDiagnostics(sbrcName, dsName))
		})
}

// sbrcReadinessDiagnostics gathers the cluster state that explains why an SBRC agent DaemonSet
// has not become ready. The usual bottleneck is the first SBRC's cold start on a freshly
// provisioned ODF/CephFS cluster: the SBD device PVC bind and the SBD device-init Job can lag,
// leaving agent pods Pending. It lists (by dynamic discovery, never by assumed operator-internal
// names) the DaemonSet status, the agent pods' container/scheduling state, every PVC and Job in
// the operator namespace, and the most recent Warning events. Returned as a string so it can be a
// lazily-evaluated Gomega failure description; every lookup is best-effort so one failure does not
// mask the rest.
func sbrcReadinessDiagnostics(sbrcName, dsName string) string {
	// Bound the diagnostic API calls so a slow or wedged apiserver cannot hang the already-failed
	// test while the failure description is being built.
	ctx, cancel := context.WithTimeout(context.Background(), sbrparams.SBRCReadyDiagTimeout)
	defer cancel()

	namespace := medik8sparams.OperatorNs

	var report strings.Builder

	fmt.Fprintf(&report, "=== SBRC %q readiness diagnostics (namespace %s) ===\n", sbrcName, namespace)

	agentDS, dsErr := APIClient.DaemonSets(namespace).Get(ctx, dsName, metav1.GetOptions{})
	if dsErr != nil {
		fmt.Fprintf(&report, "DaemonSet %s: GET failed: %v\n", dsName, dsErr)
	} else {
		dsStatus := agentDS.Status
		fmt.Fprintf(&report,
			"DaemonSet %s: desired=%d current=%d ready=%d available=%d updated=%d misscheduled=%d\n",
			dsName, dsStatus.DesiredNumberScheduled, dsStatus.CurrentNumberScheduled, dsStatus.NumberReady,
			dsStatus.NumberAvailable, dsStatus.UpdatedNumberScheduled, dsStatus.NumberMisscheduled)
	}

	// Prefer the DaemonSet's own label selector (discovered, not assumed) to find its agent pods;
	// fall back to the "<dsName>-" name prefix when the DaemonSet itself could not be fetched.
	podSelector := ""

	if dsErr == nil && agentDS.Spec.Selector != nil {
		if selector, selErr := metav1.LabelSelectorAsSelector(agentDS.Spec.Selector); selErr == nil {
			podSelector = selector.String()
		}
	}

	report.WriteString(agentPodDiagnostics(ctx, namespace, dsName, podSelector))
	report.WriteString(pvcDiagnostics(ctx, namespace))
	report.WriteString(jobDiagnostics(ctx, namespace))
	report.WriteString(recentWarningEvents(ctx, namespace))

	return report.String()
}

// agentPodDiagnostics reports the SBRC agent pods' scheduling/container state and the PVC each one
// mounts. Pods are selected by the DaemonSet's label selector when known, otherwise by the
// "<dsName>-" name prefix (DaemonSet pods are named "<dsName>-<hash>").
func agentPodDiagnostics(ctx context.Context, namespace, dsName, podSelector string) string {
	listOptions := metav1.ListOptions{}
	if podSelector != "" {
		listOptions.LabelSelector = podSelector
	}

	podList, err := APIClient.Pods(namespace).List(ctx, listOptions)
	if err != nil {
		return fmt.Sprintf("Pods: LIST failed: %v\n", err)
	}

	var report strings.Builder

	found := false

	for idx := range podList.Items {
		agentPod := &podList.Items[idx]
		if podSelector == "" && !strings.HasPrefix(agentPod.Name, dsName+"-") {
			continue
		}

		found = true

		fmt.Fprintf(&report, "Pod %s: phase=%s node=%q\n",
			agentPod.Name, agentPod.Status.Phase, agentPod.Spec.NodeName)

		for _, cond := range agentPod.Status.Conditions {
			if cond.Status != corev1.ConditionTrue {
				fmt.Fprintf(&report, "  condition %s=%s reason=%s msg=%s\n",
					cond.Type, cond.Status, cond.Reason, cond.Message)
			}
		}

		for _, vol := range agentPod.Spec.Volumes {
			if vol.PersistentVolumeClaim != nil {
				fmt.Fprintf(&report, "  volume %s -> PVC %s\n", vol.Name, vol.PersistentVolumeClaim.ClaimName)
			}
		}

		for _, initStatus := range agentPod.Status.InitContainerStatuses {
			report.WriteString("  init " + containerStateSummary(initStatus))
		}

		for _, ctrStatus := range agentPod.Status.ContainerStatuses {
			report.WriteString("  " + containerStateSummary(ctrStatus))
		}
	}

	if !found {
		fmt.Fprintf(&report, "Pods: none found for DaemonSet %s\n", dsName)
	}

	return report.String()
}

// pvcDiagnostics reports every PVC in the namespace. The first SBRC's SBD device PVC bind is the
// usual cold-start bottleneck, and a Pending PVC keeps the agent pods from starting.
func pvcDiagnostics(ctx context.Context, namespace string) string {
	pvcList, err := APIClient.PersistentVolumeClaims(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Sprintf("PVCs: LIST failed: %v\n", err)
	}

	if len(pvcList.Items) == 0 {
		return "PVCs: none in namespace\n"
	}

	var report strings.Builder

	for idx := range pvcList.Items {
		pvc := &pvcList.Items[idx]

		storageClass := ""
		if pvc.Spec.StorageClassName != nil {
			storageClass = *pvc.Spec.StorageClassName
		}

		fmt.Fprintf(&report, "PVC %s: phase=%s storageClass=%q\n", pvc.Name, pvc.Status.Phase, storageClass)
	}

	return report.String()
}

// jobDiagnostics reports every Job in the namespace. The SBD device-init Job runs once per SBRC,
// and a stuck or failed Job leaves the agent pods not ready.
func jobDiagnostics(ctx context.Context, namespace string) string {
	jobList, err := APIClient.K8sClient.BatchV1().Jobs(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Sprintf("Jobs: LIST failed: %v\n", err)
	}

	if len(jobList.Items) == 0 {
		return "Jobs: none in namespace\n"
	}

	var report strings.Builder

	for idx := range jobList.Items {
		job := &jobList.Items[idx]
		fmt.Fprintf(&report, "Job %s: active=%d succeeded=%d failed=%d\n",
			job.Name, job.Status.Active, job.Status.Succeeded, job.Status.Failed)
	}

	return report.String()
}

// containerStateSummary renders a single container's current state on one line for diagnostics.
func containerStateSummary(containerStatus corev1.ContainerStatus) string {
	switch {
	case containerStatus.State.Waiting != nil:
		return fmt.Sprintf("container %s: Waiting reason=%s msg=%s\n",
			containerStatus.Name, containerStatus.State.Waiting.Reason, containerStatus.State.Waiting.Message)
	case containerStatus.State.Terminated != nil:
		return fmt.Sprintf("container %s: Terminated reason=%s exit=%d\n",
			containerStatus.Name, containerStatus.State.Terminated.Reason, containerStatus.State.Terminated.ExitCode)
	case containerStatus.State.Running != nil:
		return fmt.Sprintf("container %s: Running ready=%t restarts=%d\n",
			containerStatus.Name, containerStatus.Ready, containerStatus.RestartCount)
	default:
		return fmt.Sprintf("container %s: state unknown ready=%t\n", containerStatus.Name, containerStatus.Ready)
	}
}

// recentWarningEvents returns up to SBRCReadyDiagMaxEvents of the most recent Warning events in
// the namespace, newest first with age, formatted one per line for diagnostics.
func recentWarningEvents(ctx context.Context, namespace string) string {
	evList, err := APIClient.Events(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Sprintf("Events: LIST failed: %v\n", err)
	}

	warnings := make([]corev1.Event, 0, len(evList.Items))

	for idx := range evList.Items {
		if evList.Items[idx].Type == corev1.EventTypeWarning {
			warnings = append(warnings, evList.Items[idx])
		}
	}

	if len(warnings) == 0 {
		return "Events: no Warning events\n"
	}

	sort.Slice(warnings, func(i, j int) bool {
		return eventTimestamp(warnings[i]).After(eventTimestamp(warnings[j]))
	})

	limit := len(warnings)
	if limit > sbrparams.SBRCReadyDiagMaxEvents {
		limit = sbrparams.SBRCReadyDiagMaxEvents
	}

	var report strings.Builder

	fmt.Fprintf(&report, "Warning events (most recent %d of %d):\n", limit, len(warnings))

	for idx := 0; idx < limit; idx++ {
		event := warnings[idx]
		age := time.Since(eventTimestamp(event)).Round(time.Second)
		fmt.Fprintf(&report, "  %s %s/%s (%s ago): %s\n",
			event.Reason, event.InvolvedObject.Kind, event.InvolvedObject.Name, age, event.Message)
	}

	return report.String()
}

// eventTimestamp returns the most representative timestamp for an Event, preferring the newer
// Series/EventTime fields and falling back to the legacy timestamps, so "most recent" ordering is
// correct on clusters that leave LastTimestamp unset.
func eventTimestamp(event corev1.Event) time.Time {
	switch {
	case event.Series != nil && !event.Series.LastObservedTime.IsZero():
		return event.Series.LastObservedTime.Time
	case !event.LastTimestamp.IsZero():
		return event.LastTimestamp.Time
	case !event.EventTime.IsZero():
		return event.EventTime.Time
	default:
		return event.FirstTimestamp.Time
	}
}

var _ = Describe(
	"SBR Negative Tests",
	Ordered,
	ContinueOnFailure,
	Label(labels.OperatorSBR, labels.ComponentPostDeploy), func() {
		BeforeAll(func() {
			By("Cleaning up any leftover test SBRCs from previous runs")

			staleNames := []string{
				sbrparams.SBRCControllerTestName,
				sbrparams.SBRCWatchdogTestName,
				sbrparams.SBRCNoMatchSelectorTestName,
				fmt.Sprintf("%s-below-min-timeout", sbrparams.SBRCInvalidTestName),
				fmt.Sprintf("%s-above-max-timeout", sbrparams.SBRCInvalidTestName),
				fmt.Sprintf("%s-below-min-failures", sbrparams.SBRCInvalidTestName),
				fmt.Sprintf("%s-above-max-failures", sbrparams.SBRCInvalidTestName),
			}

			for _, name := range staleNames {
				staleRef := buildSBRC(name, map[string]interface{}{})

				deleteErr := APIClient.Delete(context.TODO(), staleRef)
				if deleteErr != nil && !k8serrors.IsNotFound(deleteErr) {
					GinkgoT().Logf("Warning: pre-test cleanup of stale StorageBasedRemediationConfig %s failed: %v", name, deleteErr)
				}
			}

			By("Waiting for stale DaemonSets to be garbage-collected before snapshotting baseline")

			staleNamesSet := make(map[string]bool, len(staleNames))
			for _, n := range staleNames {
				staleNamesSet[n] = true
			}

			Eventually(func() error {
				dsList, listErr := APIClient.DaemonSets(medik8sparams.OperatorNs).List(
					context.TODO(), metav1.ListOptions{})
				if listErr != nil {
					return listErr
				}

				for _, ds := range dsList.Items {
					if staleNamesSet[ds.Name] {
						return fmt.Errorf("stale DaemonSet %q still present; waiting for GC", ds.Name)
					}
				}

				return nil
			}, medik8sparams.DefaultTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
				"Stale DaemonSets from prior runs must be GC'd before snapshotting baseline")
		})

		It("Verify StorageBasedRemediationConfig CR validation rejects invalid field values",
			reportxml.ID("88881"),
			Label(
				labels.OperatorSBR,
				labels.DisruptionNonDestructive,
				labels.TierAcceptance,
				labels.PlatformAny,
				labels.ComponentController,
				labels.FrequencyNightly,
			), func() {
				By("Layer 1: CRD OpenAPI schema — API server rejects out-of-range field values")

				type invalidSBRCCase struct {
					name  string
					field string
					value int64
				}

				By("Recording baseline DaemonSet names before any StorageBasedRemediationConfig is created")

				baselineDSNames := snapshotDaemonSetNames()

				var schemaErrors []string

				// DeferCleanup so schema errors are reported even when Layer 2 also fails —
				// a direct Fail() would abort the It block before Layer 2 runs.
				DeferCleanup(func() {
					if len(schemaErrors) == 0 {
						return
					}

					errMsg := "CRD schema validation failures:\n"
					for _, msg := range schemaErrors {
						errMsg += fmt.Sprintf("- %s\n", msg)
					}

					Fail(errMsg)
				})

				for _, invalidCase := range []invalidSBRCCase{
					{"below-min-timeout", "sbrTimeoutSeconds", sbrparams.SBRCTimeoutSecondsMin - 1},
					{"above-max-timeout", "sbrTimeoutSeconds", sbrparams.SBRCTimeoutSecondsMax + 1},
					{"below-min-failures", "maxConsecutiveFailures", sbrparams.SBRCMaxConsecutiveFailuresMin - 1},
					{"above-max-failures", "maxConsecutiveFailures", sbrparams.SBRCMaxConsecutiveFailuresMax + 1},
				} {
					By(fmt.Sprintf("Attempting to create StorageBasedRemediationConfig with %s=%d (expect rejection)",
						invalidCase.field, invalidCase.value))

					invalidSBRC := buildSBRC(
						fmt.Sprintf("%s-%s", sbrparams.SBRCInvalidTestName, invalidCase.name),
						map[string]interface{}{invalidCase.field: invalidCase.value},
					)

					createErr := APIClient.Create(context.TODO(), invalidSBRC)
					if createErr == nil {
						invalidSBRCRef := invalidSBRC.DeepCopy()

						DeferCleanup(func() {
							deleteErr := APIClient.Delete(context.TODO(), invalidSBRCRef)
							if deleteErr != nil && !k8serrors.IsNotFound(deleteErr) {
								GinkgoT().Logf("Warning: failed to delete unexpectedly-admitted StorageBasedRemediationConfig %s: %v",
									invalidSBRCRef.GetName(), deleteErr)
							}
						})

						schemaErrors = append(schemaErrors,
							fmt.Sprintf("StorageBasedRemediationConfig with %s=%d was unexpectedly admitted by the API server",
								invalidCase.field, invalidCase.value))

						continue
					}

					if !k8serrors.IsInvalid(createErr) && !k8serrors.IsBadRequest(createErr) {
						schemaErrors = append(schemaErrors,
							fmt.Sprintf("expected Invalid or BadRequest error for %s=%d, got: %v",
								invalidCase.field, invalidCase.value, createErr))
					}
				}

				By("Layer 2: Controller validation — StorageBasedRemediationConfig with non-existent " +
					"StorageClass is admitted but DaemonSet is not deployed")

				sbrc := buildSBRC(sbrparams.SBRCControllerTestName,
					map[string]interface{}{
						"sharedStorageClass": "nonexistent-storage-class",
					})

				err := APIClient.Create(context.TODO(), sbrc)
				Expect(err).ToNot(HaveOccurred(),
					"StorageBasedRemediationConfig with invalid StorageClass reference should be admitted by API server")

				sbrcRef := sbrc.DeepCopy()

				DeferCleanup(func() {
					By("Cleaning up controller-layer test StorageBasedRemediationConfig")

					deleteErr := APIClient.Delete(context.TODO(), sbrcRef)
					if deleteErr != nil && !k8serrors.IsNotFound(deleteErr) {
						GinkgoT().Logf("Warning: failed to delete test StorageBasedRemediationConfig %s: %v",
							sbrparams.SBRCControllerTestName, deleteErr)
					}
				})

				By("Verifying controller does not deploy a new DaemonSet for the invalid StorageBasedRemediationConfig")

				Consistently(func() error {
					dsList, dsListErr := APIClient.DaemonSets(medik8sparams.OperatorNs).List(
						context.TODO(), metav1.ListOptions{})
					if dsListErr != nil {
						return dsListErr
					}

					for _, ds := range dsList.Items {
						if !baselineDSNames[ds.Name] {
							return fmt.Errorf(
								"unexpected new DaemonSet %q appeared for StorageBasedRemediationConfig with non-existent StorageClass",
								ds.Name)
						}
					}

					return nil
				}, sbrparams.NoNewDaemonSetCheckDuration, sbrparams.NoNewDaemonSetCheckInterval).Should(Succeed(),
					"No new DaemonSet should appear for a StorageBasedRemediationConfig with a non-existent StorageClass")
			})

		It("Verify StorageBasedRemediationConfig controller handles invalid watchdog path "+
			"and non-matching nodeSelector without scheduling agent pods",
			reportxml.ID("88741"),
			Label(
				labels.OperatorSBR,
				labels.DisruptionNonDestructive,
				labels.TierAcceptance,
				labels.PlatformAny,
				labels.ComponentController,
				labels.FrequencyNightly,
			), func() {
				type invalidSBRCCase struct {
					name               string
					spec               map[string]interface{}
					desc               string
					requireNoDaemonSet bool
				}

				for _, invalidCase := range []invalidSBRCCase{
					{
						name: sbrparams.SBRCWatchdogTestName,
						spec: map[string]interface{}{
							"watchdogPath": sbrparams.SBRCInvalidWatchdogPath,
						},
						desc:               "invalid watchdog device path",
						requireNoDaemonSet: true,
					},
					{
						name: sbrparams.SBRCNoMatchSelectorTestName,
						spec: map[string]interface{}{
							"nodeSelector": map[string]interface{}{
								sbrparams.SBRCNoMatchSelectorKey: sbrparams.SBRCNoMatchSelectorValue,
							},
						},
						desc:               "nodeSelector matching no cluster nodes",
						requireNoDaemonSet: false,
					},
				} {
					By(fmt.Sprintf("Recording baseline DaemonSet names before creating StorageBasedRemediationConfig with %s",
						invalidCase.desc))

					// Snapshot per-iteration so DaemonSets created by prior iterations' SBRCs
					// (which remain alive until DeferCleanup fires after the It body) are treated
					// as baseline and don't contaminate this iteration's Consistently check.
					baselineDSNames := snapshotDaemonSetNames()

					By(fmt.Sprintf("Creating StorageBasedRemediationConfig with %s", invalidCase.desc))

					sbrc := buildSBRC(invalidCase.name, invalidCase.spec)

					createErr := APIClient.Create(context.TODO(), sbrc)
					Expect(createErr).ToNot(HaveOccurred(),
						"StorageBasedRemediationConfig with %s should be admitted by the API server", invalidCase.desc)

					sbrcRef := sbrc.DeepCopy()

					DeferCleanup(func() {
						By(fmt.Sprintf("Cleaning up test StorageBasedRemediationConfig %s", sbrcRef.GetName()))

						deleteErr := APIClient.Delete(context.TODO(), sbrcRef)
						if deleteErr != nil && !k8serrors.IsNotFound(deleteErr) {
							GinkgoT().Logf("Warning: failed to delete test StorageBasedRemediationConfig %s: %v",
								sbrcRef.GetName(), deleteErr)
						}
					})

					By(fmt.Sprintf("Verifying controller does not schedule agent pods for StorageBasedRemediationConfig with %s",
						invalidCase.desc))

					// Both SBRCs coexist during iteration 2 (DeferCleanup fires after the It body).
					// The watchdog StorageBasedRemediationConfig never produces a DaemonSet: the controller exits reconciliation
					// early with "no shared storage configured" before reaching buildDaemonSet, so
					// there is no cross-iteration DS to evaluate.
					Consistently(func() error {
						dsList, listErr := APIClient.DaemonSets(medik8sparams.OperatorNs).List(
							context.TODO(), metav1.ListOptions{})
						if listErr != nil {
							return listErr
						}

						for _, daemonSet := range dsList.Items {
							if baselineDSNames[daemonSet.Name] {
								continue
							}

							if invalidCase.requireNoDaemonSet {
								return fmt.Errorf("new DaemonSet %q must not exist for StorageBasedRemediationConfig with %s",
									daemonSet.Name, invalidCase.desc)
							}

							if daemonSet.Status.DesiredNumberScheduled > 0 {
								return fmt.Errorf(
									"new DaemonSet %q has %d agent pod(s) scheduled; expected 0 for StorageBasedRemediationConfig with %s",
									daemonSet.Name,
									daemonSet.Status.DesiredNumberScheduled,
									invalidCase.desc)
							}
						}

						return nil
					}, sbrparams.NoNewDaemonSetCheckDuration, sbrparams.NoNewDaemonSetCheckInterval).Should(Succeed(),
						"Controller must not schedule agent pods for StorageBasedRemediationConfig with %s", invalidCase.desc)

					By(fmt.Sprintf("Verifying StorageBasedRemediationConfig %s still exists after controller reconciliation",
						invalidCase.name))

					sbrcCheck := &unstructured.Unstructured{}
					sbrcCheck.SetGroupVersionKind(sbrcRef.GroupVersionKind())

					getErr := APIClient.Get(context.TODO(),
						types.NamespacedName{Name: invalidCase.name, Namespace: medik8sparams.OperatorNs},
						sbrcCheck)
					Expect(getErr).ToNot(HaveOccurred(),
						"StorageBasedRemediationConfig %q must still exist after controller reconciliation with %s",
						invalidCase.name, invalidCase.desc)
				}
			})
	})
