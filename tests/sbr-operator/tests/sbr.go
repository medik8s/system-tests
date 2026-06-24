package tests

import (
	"context"
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"
	oplmV1alpha1 "github.com/rh-ecosystem-edge/eco-goinfra/pkg/schemes/olm/operators/v1alpha1"

	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/deployment"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/infrastructure"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/olm"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/pod"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/reportxml"

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
	Label(sbrparams.Label), func() {
		It("Discover /dev/watchdog* devices on all cluster nodes",
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

func findActiveCSV(csvs []*olm.ClusterServiceVersionBuilder) *olm.ClusterServiceVersionBuilder {
	for _, csv := range csvs {
		phase, err := csv.GetPhase()
		if err == nil && phase == oplmV1alpha1.CSVPhaseSucceeded {
			return csv
		}
	}

	return nil
}

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

func filterRunningPods(pods []*pod.Builder) []*pod.Builder {
	var running []*pod.Builder

	for _, sbrPod := range pods {
		if sbrPod.Object.Status.Phase != corev1.PodRunning || sbrPod.Object.DeletionTimestamp != nil {
			continue
		}

		allReady := true

		for _, cs := range sbrPod.Object.Status.ContainerStatuses {
			if !cs.Ready {
				allReady = false

				break
			}
		}

		if allReady {
			running = append(running, sbrPod)
		}
	}

	return running
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

		sbrCSV = findActiveCSV(sbrCSVs)
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
	Label(sbrparams.Label), func() {
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

					runningCount := int32(len(filterRunningPods(sbrPods)))

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

					runningPods := filterRunningPods(sbrPods)

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

					running := filterRunningPods(sbrPods)
					if len(running) == 0 {
						return fmt.Errorf("no running SBR controller pods found")
					}

					runningPods = running

					return nil
				}, medik8sparams.DefaultTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
					"At least one running SBR controller pod should be found")

				var errorMessages []string

				for _, sbrPod := range runningPods {
					By(fmt.Sprintf("Verifying security context for pod %s", sbrPod.Object.Name))

					By("Checking pod-level runAsNonRoot security context")

					if sbrPod.Object.Spec.SecurityContext == nil {
						errorMessages = append(errorMessages,
							fmt.Sprintf("Pod %s has nil SecurityContext", sbrPod.Object.Name))
					} else if sbrPod.Object.Spec.SecurityContext.RunAsNonRoot == nil {
						errorMessages = append(errorMessages,
							fmt.Sprintf("Pod %s has nil runAsNonRoot", sbrPod.Object.Name))
					} else if !*sbrPod.Object.Spec.SecurityContext.RunAsNonRoot {
						errorMessages = append(errorMessages,
							fmt.Sprintf("Incorrect runAsNonRoot for pod %s. Expected true, found: %v",
								sbrPod.Object.Name,
								*sbrPod.Object.Spec.SecurityContext.RunAsNonRoot))
					}

					By("Checking manager container security context")

					managerFound := false

					for _, container := range sbrPod.Object.Spec.Containers {
						if container.Name != sbrparams.ManagerContainerName {
							continue
						}

						managerFound = true
						securityContext := container.SecurityContext

						if securityContext == nil {
							errorMessages = append(errorMessages,
								fmt.Sprintf("Container %s in pod %s has nil SecurityContext",
									container.Name, sbrPod.Object.Name))

							continue
						}

						if securityContext.RunAsUser != nil && *securityContext.RunAsUser == 0 {
							errorMessages = append(errorMessages,
								fmt.Sprintf("Container %s in pod %s runs as root (UID 0)",
									container.Name, sbrPod.Object.Name))
						}

						if securityContext.AllowPrivilegeEscalation == nil || *securityContext.AllowPrivilegeEscalation {
							errorMessages = append(errorMessages,
								fmt.Sprintf(
									"Container %s in pod %s: AllowPrivilegeEscalation must be explicitly false",
									container.Name, sbrPod.Object.Name))
						}

						if securityContext.Capabilities == nil {
							errorMessages = append(errorMessages,
								fmt.Sprintf(
									"Container %s in pod %s: Capabilities block is nil, must drop ALL",
									container.Name, sbrPod.Object.Name))
						} else {
							hasDropAll := false

							for _, cap := range securityContext.Capabilities.Drop {
								if cap == "ALL" {
									hasDropAll = true

									break
								}
							}

							if !hasDropAll {
								errorMessages = append(errorMessages,
									fmt.Sprintf("Container %s in pod %s does not drop ALL capabilities",
										container.Name, sbrPod.Object.Name))
							}
						}

						seccompOk := false
						if securityContext.SeccompProfile != nil &&
							securityContext.SeccompProfile.Type == corev1.SeccompProfileTypeRuntimeDefault {
							seccompOk = true
						} else if sbrPod.Object.Spec.SecurityContext != nil &&
							sbrPod.Object.Spec.SecurityContext.SeccompProfile != nil &&
							sbrPod.Object.Spec.SecurityContext.SeccompProfile.Type ==
								corev1.SeccompProfileTypeRuntimeDefault {
							seccompOk = true
						}

						if !seccompOk {
							errorMessages = append(errorMessages,
								fmt.Sprintf(
									"Container %s in pod %s missing RuntimeDefault seccomp profile",
									container.Name, sbrPod.Object.Name))
						}
					}

					if !managerFound {
						errorMessages = append(errorMessages,
							fmt.Sprintf("Pod %s has no container named %q",
								sbrPod.Object.Name, sbrparams.ManagerContainerName))
					}
				}

				if len(errorMessages) > 0 {
					errMsg := "Testing security context of SBR container failed due to:\n"
					for _, msg := range errorMessages {
						errMsg += fmt.Sprintf("- %s\n", msg)
					}

					Fail(errMsg)
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

// waitForSBRCReady blocks until the agent DaemonSet for the named SBRC is fully rolled out
// (NumberReady == DesiredNumberScheduled). The SBRRemediationReconciler runs inside agent pods,
// so this must be called before creating any StorageBasedRemediation CR whose reconciliation
// depends on active agents.
func waitForSBRCReady(sbrcName string) {
	dsName := sbrparams.SBRAgentDaemonSetPrefix + sbrcName

	Eventually(func() error {
		agentDS, err := APIClient.DaemonSets(medik8sparams.OperatorNs).Get(
			context.TODO(), dsName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("DaemonSet %s not found: %w", dsName, err)
		}

		if agentDS.Status.NumberReady < agentDS.Status.DesiredNumberScheduled {
			return fmt.Errorf("DaemonSet %s: %d/%d pods ready",
				dsName, agentDS.Status.NumberReady, agentDS.Status.DesiredNumberScheduled)
		}

		return nil
	}, sbrparams.SBRCReadyTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
		"SBRC %q agent DaemonSet must be fully rolled out before functional tests begin", sbrcName)
}

var _ = Describe(
	"SBR Negative Tests",
	Ordered,
	ContinueOnFailure,
	Label(sbrparams.Label), func() {
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
