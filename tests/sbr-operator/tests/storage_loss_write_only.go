package tests

import (
	"context"
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/pod"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/reportxml"

	"github.com/medik8s/system-tests/tests/internal/labels"
	. "github.com/medik8s/system-tests/tests/internal/medik8sinittools"
	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
	"github.com/medik8s/system-tests/tests/sbr-operator/internal/sbrparams"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

// buildWriteLossNHCUnstructured returns an unstructured NodeHealthCheck CR that triggers
// SBR remediation when SBRStorageUnhealthy=True for NHCUnhealthyDuration.
func buildWriteLossNHCUnstructured() *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": sbrparams.NHCAPIGroup + "/" + sbrparams.NHCAPIVersion,
			"kind":       "NodeHealthCheck",
			"metadata": map[string]interface{}{
				"name": sbrparams.NHCWriteLossTestName,
			},
			"spec": map[string]interface{}{
				"selector": map[string]interface{}{
					"matchExpressions": []interface{}{
						map[string]interface{}{
							"key":      "node-role.kubernetes.io/worker",
							"operator": "Exists",
						},
					},
				},
				"unhealthyConditions": []interface{}{
					map[string]interface{}{
						"type":     sbrparams.SBRStorageUnhealthyCondition,
						"status":   "True",
						"duration": sbrparams.NHCUnhealthyDuration,
					},
				},
				"remediationTemplate": map[string]interface{}{
					"apiVersion": sbrparams.CRDGroup + "/" + sbrparams.CRDVersion,
					"kind":       "StorageBasedRemediationTemplate",
					"name":       sbrparams.SBRTemplateName,
					"namespace":  medik8sparams.OperatorNs,
				},
			},
		},
	}
}

// getSBRCRConditionWrite returns the named status condition from an unstructured SBR CR, or nil.
func getSBRCRConditionWrite(sbrObj *unstructured.Unstructured, condType string) map[string]interface{} {
	conditions, found, err := unstructured.NestedSlice(sbrObj.Object, "status", "conditions")
	if err != nil || !found {
		return nil
	}

	for _, c := range conditions {
		cond, ok := c.(map[string]interface{})
		if !ok {
			continue
		}

		if cond["type"] == condType {
			return cond
		}
	}

	return nil
}

// getNodeConditionWrite fetches a named condition from a node's status.
// Returns nil when the condition is not present or the node cannot be retrieved.
func getNodeConditionWrite(ctx context.Context, nodeName, condType string) *corev1.NodeCondition {
	node, err := APIClient.CoreV1Interface.Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return nil
	}

	for i := range node.Status.Conditions {
		if string(node.Status.Conditions[i].Type) == condType {
			return &node.Status.Conditions[i]
		}
	}

	return nil
}

var _ = Describe(
	"SBR Functional — Write-Only Storage Loss",
	Ordered,
	ContinueOnFailure,
	Label(
		labels.OperatorSBR,
		labels.TierAcceptance,
		labels.FrequencyNightly,
		labels.DisruptionDestructive,
		labels.PlatformAny,
		labels.ComponentRemediation,
		sbrparams.Label,
	), func() {
		var (
			targetNodeName  string
			testSBRC        *unstructured.Unstructured
			nhcCR           *unstructured.Unstructured
			nhcCreatedByUs  bool
			storageClass    string
			injectorPodName string
			injectorPod     *pod.Builder
		)

		BeforeAll(func() {
			By("Checking whether NHC CRD is installed")

			crd := &apiextensionsv1.CustomResourceDefinition{}

			crdErr := APIClient.Get(context.TODO(),
				types.NamespacedName{Name: sbrparams.NHCCRDName}, crd)
			if k8serrors.IsNotFound(crdErr) {
				Skip("NodeHealthCheck CRD not found — NHC operator not installed; " +
					"skipping write-only storage loss test")
			}

			Expect(crdErr).ToNot(HaveOccurred(),
				"Unexpected error while checking for NodeHealthCheck CRD")

			By("Discovering RWX storage class")

			storageClass = discoverRWXStorageClass()
			Expect(storageClass).ToNot(BeEmpty(),
				"Could not discover a RWX storage class; set SBR_STORAGE_CLASS to override")

			By("Pre-cleanup: removing any stale StorageBasedRemediationConfig from a previous run")

			staleRef := buildSBRC(sbrparams.SBRCStorageLossWriteName, map[string]interface{}{})
			if delErr := APIClient.Delete(context.TODO(), staleRef); delErr != nil &&
				!k8serrors.IsNotFound(delErr) {
				GinkgoT().Logf("Warning: pre-cleanup delete SBRC %s: %v",
					sbrparams.SBRCStorageLossWriteName, delErr)
			}

			Eventually(func() error {
				check := buildSBRC(sbrparams.SBRCStorageLossWriteName, map[string]interface{}{})
				if getErr := APIClient.Get(context.TODO(),
					types.NamespacedName{Name: sbrparams.SBRCStorageLossWriteName,
						Namespace: medik8sparams.OperatorNs}, check); k8serrors.IsNotFound(getErr) {
					return nil
				}

				return fmt.Errorf("stale SBRC %s still present; waiting for deletion",
					sbrparams.SBRCStorageLossWriteName)
			}, sbrparams.SBRCReadyTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
				"Stale SBRC must be gone before recreating")

			By(fmt.Sprintf("Creating StorageBasedRemediationConfig %q with sharedStorageClass=%q",
				sbrparams.SBRCStorageLossWriteName, storageClass))

			testSBRC = buildSBRC(sbrparams.SBRCStorageLossWriteName, map[string]interface{}{
				"sharedStorageClass": storageClass,
			})

			createErr := APIClient.Create(context.TODO(), testSBRC)
			Expect(createErr).ToNot(HaveOccurred(),
				"Failed to create StorageBasedRemediationConfig %q", sbrparams.SBRCStorageLossWriteName)

			By("Waiting for agent DaemonSet to have at least one ready pod")

			waitForSBRCReady(sbrparams.SBRCStorageLossWriteName)

			By("Creating NodeHealthCheck CR for the write-only storage loss test")

			existingNHC := &unstructured.Unstructured{}
			existingNHC.SetGroupVersionKind(schema.GroupVersionKind{
				Group:   sbrparams.NHCAPIGroup,
				Version: sbrparams.NHCAPIVersion,
				Kind:    "NodeHealthCheck",
			})

			getErr := APIClient.Get(context.TODO(),
				types.NamespacedName{Name: sbrparams.NHCWriteLossTestName}, existingNHC)

			switch {
			case k8serrors.IsNotFound(getErr):
				nhcCR = buildWriteLossNHCUnstructured()

				nhcCreateErr := APIClient.Create(context.TODO(), nhcCR)
				Expect(nhcCreateErr).ToNot(HaveOccurred(),
					"Failed to create NodeHealthCheck CR %q", sbrparams.NHCWriteLossTestName)

				nhcCreatedByUs = true

			case getErr != nil:
				Expect(getErr).ToNot(HaveOccurred(),
					"Unexpected error fetching NodeHealthCheck %q", sbrparams.NHCWriteLossTestName)

			default:
				nhcCR = existingNHC
				nhcCreatedByUs = false

				GinkgoWriter.Printf("NodeHealthCheck %q already exists; using it as-is\n",
					sbrparams.NHCWriteLossTestName)
			}

			By("Selecting a target worker node (schedulable, not hosting SBR controller pods)")

			controllerNodes := controllerPodNodes()

			nodeList, err := APIClient.CoreV1Interface.Nodes().List(context.TODO(), metav1.ListOptions{
				LabelSelector: "node-role.kubernetes.io/worker",
			})
			Expect(err).ToNot(HaveOccurred(), "Failed to list worker nodes")

			for nodeIdx := range nodeList.Items {
				node := &nodeList.Items[nodeIdx]
				if controllerNodes[node.Name] {
					GinkgoWriter.Printf("Skipping node %s (SBR controller pod runs there)\n", node.Name)

					continue
				}

				if isNodeSchedulable(node) {
					targetNodeName = node.Name

					break
				}
			}

			if targetNodeName == "" {
				Skip("No schedulable worker node available that does not host an SBR controller pod; " +
					"skipping write-only storage loss test")
			}

			injectorPodName = sbrparams.StorageLossWriteInjectorPodName + "-" +
				strings.Map(func(r rune) rune {
					if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
						return r
					}

					return '-'
				}, strings.ToLower(targetNodeName))

			if len(injectorPodName) > 63 {
				injectorPodName = injectorPodName[:63]
			}

			injectorPodName = strings.TrimRight(injectorPodName, "-")

			GinkgoWriter.Printf("Target node: %q | injector pod: %q\n", targetNodeName, injectorPodName)
		})

		AfterAll(func() {
			By("AfterAll: deleting StorageBasedRemediationConfig")

			if testSBRC != nil {
				Eventually(func() error {
					deleteErr := APIClient.Delete(context.TODO(), testSBRC)
					if deleteErr == nil || k8serrors.IsNotFound(deleteErr) {
						return nil
					}

					return deleteErr
				}, medik8sparams.DefaultTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
					"Failed to delete StorageBasedRemediationConfig %s", sbrparams.SBRCStorageLossWriteName)
			}

			By("AfterAll: removing NodeHealthCheck CR if created by this test")

			if nhcCreatedByUs && nhcCR != nil {
				Eventually(func() error {
					deleteErr := APIClient.Delete(context.TODO(), nhcCR)
					if deleteErr == nil || k8serrors.IsNotFound(deleteErr) {
						return nil
					}

					return deleteErr
				}, medik8sparams.DefaultTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
					"Failed to delete NodeHealthCheck %s", sbrparams.NHCWriteLossTestName)
			}

			By("AfterAll: force-removing any leftover StorageBasedRemediation CR")

			if targetNodeName != "" {
				cleanupSBRCR(targetNodeName)
			}
		})

		It("Write-only storage loss: fence-message-read path triggers self-fencing",
			reportxml.ID("89200"),
			Label(
				labels.OperatorSBR,
				labels.TierAcceptance,
				labels.FrequencyNightly,
				labels.DisruptionDestructive,
				labels.PlatformAny,
				labels.ComponentRemediation,
			), func() {
				var preRebootBootID string

				DeferCleanup(func() {
					By("DeferCleanup: removing OUTPUT REJECT rules on target node")

					if injectorPod == nil {
						// Pod was never created (creation failed before assignment);
						// attempt a best-effort cleanup pod on the target node.
						cleanupPodName := func(name string) string {
							safe := strings.Map(func(r rune) rune {
								if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
									return r
								}

								return '-'
							}, strings.ToLower(name))

							result := "sbr-write-cleanup-" + safe
							if len(result) > 63 {
								result = result[:63]
							}

							return strings.TrimRight(result, "-")
						}(targetNodeName)

						cleanupPod, cleanupErr := pod.NewBuilder(APIClient,
							cleanupPodName,
							medik8sparams.OperatorNs, sbrparams.WatchdogDebugImage).
							DefineOnNode(targetNodeName).
							WithHostPid(true).
							WithPrivilegedFlag().
							CreateAndWaitUntilRunning(medik8sparams.DefaultTimeout)
						if cleanupErr != nil {
							GinkgoWriter.Printf("Warning: could not create cleanup pod on node %s: %v\n",
								targetNodeName, cleanupErr)
						} else {
							injectorPod = cleanupPod
						}
					}

					if injectorPod != nil {
						// Delete rules individually to avoid flushing the whole OUTPUT chain
						// which could affect other concurrent tests.
						outputRulesCleanup := [][]string{
							{"nsenter", "--target", "1", "--net", "--mount", "--",
								"iptables", "-D", "OUTPUT", "-p", "tcp", "--dport", "3300", "-j", "REJECT"},
							{"nsenter", "--target", "1", "--net", "--mount", "--",
								"iptables", "-D", "OUTPUT", "-p", "tcp", "--dport", "6789", "-j", "REJECT"},
							{"nsenter", "--target", "1", "--net", "--mount", "--",
								"iptables", "-D", "OUTPUT", "-p", "tcp", "--match", "multiport",
								"--dports", "6800:7300", "-j", "REJECT"},
						}

						for _, cmd := range outputRulesCleanup {
							if _, flushErr := injectorPod.ExecCommand(cmd); flushErr != nil {
								GinkgoWriter.Printf("Warning: iptables cleanup on node %s (cmd %v): %v\n",
									targetNodeName, cmd, flushErr)
							}
						}

						if _, delErr := injectorPod.Delete(); delErr != nil {
							GinkgoWriter.Printf("Warning: delete injector pod: %v\n", delErr)
						}
					}

					By("DeferCleanup: force-removing StorageBasedRemediation CR if still present")

					cleanupSBRCR(targetNodeName)

					By("DeferCleanup: waiting for node to become schedulable after CR cleanup")

					Eventually(func() error {
						node, nodeErr := APIClient.CoreV1Interface.Nodes().Get(
							context.TODO(), targetNodeName, metav1.GetOptions{})
						if nodeErr != nil {
							return fmt.Errorf("failed to get node %s: %w", targetNodeName, nodeErr)
						}

						if node.Spec.Unschedulable {
							return fmt.Errorf("node %s still cordoned after SBR CR cleanup", targetNodeName)
						}

						return nil
					}, medik8sparams.DefaultTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
						"Node %s must not be cordoned after StorageBasedRemediation CR is removed", targetNodeName)
				})

				By(fmt.Sprintf("Creating privileged injector pod on node %q", targetNodeName))

				var createErr error

				injectorPod, createErr = pod.NewBuilder(
					APIClient, injectorPodName, medik8sparams.OperatorNs, sbrparams.WatchdogDebugImage).
					DefineOnNode(targetNodeName).
					WithHostPid(true).
					WithPrivilegedFlag().
					CreateAndWaitUntilRunning(medik8sparams.DefaultTimeout)

				Expect(createErr).ToNot(HaveOccurred(),
					"Failed to create injector pod on node %q", targetNodeName)

				By("Recording pre-injection boot ID from node status")

				var preBootIDErr error

				preRebootBootID, preBootIDErr = getNodeBootID(targetNodeName)
				Expect(preBootIDErr).ToNot(HaveOccurred(),
					"Failed to read boot ID from node %q before injection", targetNodeName)
				Expect(preRebootBootID).ToNot(BeEmpty(),
					"Boot ID must not be empty on node %q", targetNodeName)

				GinkgoWriter.Printf("Pre-injection boot ID on node %q: %q\n", targetNodeName, preRebootBootID)

				By("Injecting OUTPUT-only CephFS REJECT rules (INPUT kept open for fence-message-read path)")

				// Block only the write/OUTPUT path to CephFS storage:
				//   - port 3300: msgr2 protocol
				//   - port 6789: msgr1 protocol
				//   - ports 6800-7300: OSD/MDS data plane
				// INPUT traffic is intentionally left open so the target node can still read
				// the fence message written by peers into shared storage, which triggers self-fencing.
				// This is the key distinction from OCP-88880 (full bidirectional block).
				rejectRules := [][]string{
					{"nsenter", "--target", "1", "--net", "--mount", "--",
						"iptables", "-I", "OUTPUT", "-p", "tcp", "--dport", "3300", "-j", "REJECT"},
					{"nsenter", "--target", "1", "--net", "--mount", "--",
						"iptables", "-I", "OUTPUT", "-p", "tcp", "--dport", "6789", "-j", "REJECT"},
					{"nsenter", "--target", "1", "--net", "--mount", "--",
						"iptables", "-I", "OUTPUT", "-p", "tcp", "--match", "multiport",
						"--dports", "6800:7300", "-j", "REJECT"},
				}

				for _, rule := range rejectRules {
					_, execErr := injectorPod.ExecCommand(rule)
					Expect(execErr).ToNot(HaveOccurred(),
						"Failed to inject iptables OUTPUT REJECT rule %v on node %q", rule, targetNodeName)
				}

				GinkgoWriter.Printf("CephFS OUTPUT REJECT rules applied on node %q "+
					"(INPUT kept open for fence-message-read path)\n", targetNodeName)

				By(fmt.Sprintf("Waiting for node %q to acquire SBRStorageUnhealthy=True "+
					"(peers detect stale heartbeat from target)", targetNodeName))

				Eventually(func() error {
					cond := getNodeConditionWrite(
						context.TODO(), targetNodeName, sbrparams.SBRStorageUnhealthyCondition)
					if cond == nil {
						return fmt.Errorf("node %s: condition %s not yet present",
							targetNodeName, sbrparams.SBRStorageUnhealthyCondition)
					}

					if cond.Status != corev1.ConditionTrue {
						return fmt.Errorf("node %s: %s=%s, want True",
							targetNodeName, sbrparams.SBRStorageUnhealthyCondition, cond.Status)
					}

					return nil
				}, sbrparams.StorageInjectionTimeout, sbrparams.StorageInjectionPollInterval).Should(Succeed(),
					"Node %q must reach %s=True after write-path storage injection",
					targetNodeName, sbrparams.SBRStorageUnhealthyCondition)

				By(fmt.Sprintf("Waiting for NHC to create StorageBasedRemediation CR for node %q",
					targetNodeName))

				Eventually(func() error {
					_, getErr := pullSBRCR(targetNodeName)
					if k8serrors.IsNotFound(getErr) {
						return fmt.Errorf("StorageBasedRemediation/%s not yet created by NHC", targetNodeName)
					}

					return getErr
				}, sbrparams.NHCSBRCRCreationTimeout, sbrparams.NHCSBRCRCreationPollInterval).Should(Succeed(),
					"NHC must create StorageBasedRemediation CR for node %q within timeout", targetNodeName)

				GinkgoWriter.Printf("StorageBasedRemediation CR for node %q created by NHC\n", targetNodeName)

				By(fmt.Sprintf("Waiting for node %q to go NotReady "+
					"(self-fencing via fence-message-read path)", targetNodeName))

				Eventually(func() error {
					node, nodeGetErr := APIClient.CoreV1Interface.Nodes().Get(
						context.TODO(), targetNodeName, metav1.GetOptions{})
					if nodeGetErr != nil {
						return nodeGetErr
					}

					for _, cond := range node.Status.Conditions {
						if cond.Type == corev1.NodeReady && cond.Status != corev1.ConditionTrue {
							return nil
						}
					}

					return fmt.Errorf("node %s is still Ready; waiting for NotReady after fence-message-read",
						targetNodeName)
				}, sbrparams.NodeRebootTimeout, sbrparams.NodeRebootPollInterval).Should(Succeed(),
					"Node %q must become NotReady when it self-fences after reading the fence message",
					targetNodeName)

				GinkgoWriter.Printf("Node %q is NotReady — fence-message-read reboot in progress\n",
					targetNodeName)

				By(fmt.Sprintf("Waiting for node %q to return Ready after reboot", targetNodeName))

				Eventually(func() error {
					node, nodeGetErr := APIClient.CoreV1Interface.Nodes().Get(
						context.TODO(), targetNodeName, metav1.GetOptions{})
					if nodeGetErr != nil {
						return nodeGetErr
					}

					for _, cond := range node.Status.Conditions {
						if cond.Type == corev1.NodeReady {
							if cond.Status == corev1.ConditionTrue {
								return nil
							}

							return fmt.Errorf("node %s condition Ready=%s", targetNodeName, cond.Status)
						}
					}

					return fmt.Errorf("node %s has no Ready condition", targetNodeName)
				}, sbrparams.NodeRebootTimeout, sbrparams.NodeRebootPollInterval).Should(Succeed(),
					"Node %q must return to Ready after reboot", targetNodeName)

				GinkgoWriter.Printf("Node %q is Ready again\n", targetNodeName)

				By("Verifying boot ID changed (confirms actual reboot, not transient state)")

				postRebootBootID, postBootIDErr := getNodeBootID(targetNodeName)
				Expect(postBootIDErr).ToNot(HaveOccurred(),
					"Failed to read boot ID from node %q after reboot", targetNodeName)
				Expect(postRebootBootID).ToNot(BeEmpty(),
					"Boot ID must not be empty on node %q after reboot", targetNodeName)
				Expect(postRebootBootID).ToNot(Equal(preRebootBootID),
					"Node %q boot ID must change after reboot (pre=%q, post=%q) — "+
						"node did not actually reboot",
					targetNodeName, preRebootBootID, postRebootBootID)

				GinkgoWriter.Printf("Boot ID changed on node %q: %q to %q\n",
					targetNodeName, preRebootBootID, postRebootBootID)

				By("Waiting for FencingSucceeded=True on the StorageBasedRemediation CR")

				var fencingObserved bool

				Eventually(func() error {
					sbrObj, getErr := pullSBRCR(targetNodeName)
					if k8serrors.IsNotFound(getErr) {
						if fencingObserved {
							return nil // CR cleaned up after confirmed fencing
						}

						return fmt.Errorf(
							"StorageBasedRemediation/%s already gone before FencingSucceeded=True was observed",
							targetNodeName)
					}

					if getErr != nil {
						return getErr
					}

					cond := getSBRCRConditionWrite(sbrObj, "FencingSucceeded")
					if cond == nil {
						return fmt.Errorf("StorageBasedRemediation/%s: FencingSucceeded condition not present",
							targetNodeName)
					}

					if cond["status"] != "True" {
						return fmt.Errorf("StorageBasedRemediation/%s: FencingSucceeded=%v, want True",
							targetNodeName, cond["status"])
					}

					fencingObserved = true

					return nil
				}, medik8sparams.DefaultTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
					"StorageBasedRemediation/%s must reach FencingSucceeded=True", targetNodeName)

				By("Waiting for the SBR CR to be deleted (controller cleanup after successful fencing)")

				Eventually(func() error {
					_, getErr := pullSBRCR(targetNodeName)
					if k8serrors.IsNotFound(getErr) {
						return nil
					}

					if getErr != nil {
						return getErr
					}

					return fmt.Errorf("StorageBasedRemediation/%s still exists; waiting for controller cleanup",
						targetNodeName)
				}, medik8sparams.DefaultTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
					"StorageBasedRemediation/%s must be deleted by controller after successful fencing",
					targetNodeName)

				GinkgoWriter.Printf("Write-only storage loss (fence-message-read) fencing completed "+
					"successfully for node %q\n", targetNodeName)
			})
	})
