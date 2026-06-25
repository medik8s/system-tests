package tests

import (
	"context"
	"fmt"
	"sort"
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

func buildSplitBrainNHC() *unstructured.Unstructured {
	return buildNHC(sbrparams.NHCSplitBrainTestName)
}

// sbrCRExists checks whether a StorageBasedRemediation CR exists for the given node name.
// Returns nil if the CR exists, non-nil (including IsNotFound errors) if it does not.
func sbrCRExists(nodeName string) error {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion(sbrparams.CRDGroup + "/" + sbrparams.CRDVersion)
	obj.SetKind("StorageBasedRemediation")

	return APIClient.Get(context.TODO(),
		types.NamespacedName{Name: nodeName, Namespace: medik8sparams.OperatorNs}, obj)
}

// getNodeStorageCondition returns the named node condition, or nil if absent.
func getNodeStorageCondition(nodeName, condType string) *corev1.NodeCondition {
	node, err := APIClient.CoreV1Interface.Nodes().Get(context.TODO(), nodeName, metav1.GetOptions{})
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
	"SBR Functional — Node Failure: Split-Brain (Storage Arbitration)",
	Ordered,
	ContinueOnFailure,
	Label(sbrparams.Label), func() {
		var (
			targetNodeName  string
			healthyNodes    []string
			testSBRC        *unstructured.Unstructured
			nhcCR           *unstructured.Unstructured
			nhcCreatedByUs  bool
			storageClass    string
			injectorPodName string
		)

		BeforeAll(func() {
			By("Checking whether NHC CRD is installed")

			crd := &apiextensionsv1.CustomResourceDefinition{}
			crdErr := APIClient.Get(context.TODO(),
				types.NamespacedName{Name: sbrparams.NHCCRDName}, crd)

			if k8serrors.IsNotFound(crdErr) {
				Skip("NodeHealthCheck CRD not found — NHC operator not installed; skipping split-brain test")
			}

			Expect(crdErr).ToNot(HaveOccurred(),
				"Unexpected error while checking for NodeHealthCheck CRD")

			By("Listing schedulable worker nodes (need at least 3)")

			controllerNodes := controllerPodNodes()

			nodeList, err := APIClient.CoreV1Interface.Nodes().List(context.TODO(), metav1.ListOptions{
				LabelSelector: "node-role.kubernetes.io/worker",
			})
			Expect(err).ToNot(HaveOccurred(), "Failed to list worker nodes")

			var candidates []string

			for nodeIdx := range nodeList.Items {
				node := &nodeList.Items[nodeIdx]
				if !isNodeSchedulable(node) {
					GinkgoWriter.Printf("Skipping unschedulable node %s\n", node.Name)

					continue
				}

				if controllerNodes[node.Name] {
					GinkgoWriter.Printf("Skipping node %s (SBR controller pod runs there)\n", node.Name)

					continue
				}

				candidates = append(candidates, node.Name)
			}

			if len(candidates) < 3 {
				Skip(fmt.Sprintf("Need at least 3 schedulable worker nodes (not hosting controller pods); "+
					"found %d — skipping split-brain test", len(candidates)))
			}

			// Sort for deterministic target selection across runs and clusters.
			sort.Strings(candidates)

			// First candidate is the target; rest are healthy witnesses.
			targetNodeName = candidates[0]
			healthyNodes = candidates[1:]

			injectorPodName = "sbr-split-brain-injector-" +
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

			GinkgoWriter.Printf("Target node: %q\nHealthy witnesses: %v\nInjector pod: %q\n",
				targetNodeName, healthyNodes, injectorPodName)

			By("Discovering RWX storage class")

			storageClass = discoverRWXStorageClass()
			Expect(storageClass).ToNot(BeEmpty(),
				"Could not discover a RWX storage class; set SBR_STORAGE_CLASS to override")

			By(fmt.Sprintf("Creating StorageBasedRemediationConfig %q with sharedStorageClass=%q",
				sbrparams.SBRCSplitBrainTestName, storageClass))

			testSBRC = buildSBRC(sbrparams.SBRCSplitBrainTestName, map[string]interface{}{
				"sharedStorageClass": storageClass,
			})

			createErr := APIClient.Create(context.TODO(), testSBRC)
			Expect(createErr).ToNot(HaveOccurred(),
				"Failed to create StorageBasedRemediationConfig %q", sbrparams.SBRCSplitBrainTestName)

			By("Waiting for SBRC agent DaemonSet to have at least one ready pod")

			waitForSBRCReady(sbrparams.SBRCSplitBrainTestName)

			By("Creating NodeHealthCheck CR for split-brain test (or reusing an existing one)")

			existingNHC := &unstructured.Unstructured{}
			existingNHC.SetGroupVersionKind(schema.GroupVersionKind{
				Group:   sbrparams.NHCAPIGroup,
				Version: sbrparams.NHCAPIVersion,
				Kind:    "NodeHealthCheck",
			})

			getErr := APIClient.Get(context.TODO(),
				types.NamespacedName{Name: sbrparams.NHCSplitBrainTestName}, existingNHC)

			switch {
			case k8serrors.IsNotFound(getErr):
				nhcCR = buildSplitBrainNHC()

				nhcCreateErr := APIClient.Create(context.TODO(), nhcCR)
				Expect(nhcCreateErr).ToNot(HaveOccurred(),
					"Failed to create NodeHealthCheck CR %q", sbrparams.NHCSplitBrainTestName)

				nhcCreatedByUs = true

			case getErr != nil:
				Expect(getErr).ToNot(HaveOccurred(),
					"Unexpected error fetching NodeHealthCheck %q", sbrparams.NHCSplitBrainTestName)

			default:
				nhcCR = existingNHC
				nhcCreatedByUs = false

				GinkgoWriter.Printf("NodeHealthCheck %q already exists; using it as-is\n",
					sbrparams.NHCSplitBrainTestName)
			}
		})

		AfterAll(func() {
			By("AfterAll: deleting StorageBasedRemediationConfig")

			if testSBRC != nil {
				if deleteErr := APIClient.Delete(context.TODO(), testSBRC); deleteErr != nil &&
					!k8serrors.IsNotFound(deleteErr) {
					GinkgoT().Logf("Warning: cleanup delete StorageBasedRemediationConfig %s: %v",
						sbrparams.SBRCSplitBrainTestName, deleteErr)
				} else {
					Eventually(func() error {
						getErr := APIClient.Get(context.TODO(),
							types.NamespacedName{Name: sbrparams.SBRCSplitBrainTestName,
								Namespace: medik8sparams.OperatorNs},
							testSBRC.DeepCopy())

						if k8serrors.IsNotFound(getErr) {
							return nil
						}

						if getErr != nil {
							return getErr
						}

						return fmt.Errorf("SBRC %s still present", sbrparams.SBRCSplitBrainTestName)
					}, medik8sparams.DefaultTimeout, sbrparams.DefaultPollInterval).Should(Succeed())
				}
			}

			By("AfterAll: removing NodeHealthCheck CR if created by this test")

			if nhcCreatedByUs && nhcCR != nil {
				if deleteErr := APIClient.Delete(context.TODO(), nhcCR); deleteErr != nil &&
					!k8serrors.IsNotFound(deleteErr) {
					GinkgoT().Logf("Warning: cleanup delete NodeHealthCheck %s: %v",
						sbrparams.NHCSplitBrainTestName, deleteErr)
				} else {
					Eventually(func() error {
						getErr := APIClient.Get(context.TODO(),
							types.NamespacedName{Name: sbrparams.NHCSplitBrainTestName},
							nhcCR.DeepCopy())

						if k8serrors.IsNotFound(getErr) {
							return nil
						}

						if getErr != nil {
							return getErr
						}

						return fmt.Errorf("NHC %s still present", sbrparams.NHCSplitBrainTestName)
					}, medik8sparams.DefaultTimeout, sbrparams.DefaultPollInterval).Should(Succeed())
				}
			}

			By("AfterAll: force-removing any leftover SBR CRs")

			allNodes := append([]string{targetNodeName}, healthyNodes...)
			for _, nodeName := range allNodes {
				if nodeName == "" {
					continue
				}

				cleanupSBRCR(nodeName)
			}

			By("AfterAll: ensuring injector pod iptables rules are removed")

			cleanupPod, pullErr := pod.Pull(APIClient, injectorPodName, medik8sparams.OperatorNs)
			if pullErr == nil {
				_, flushErr := cleanupPod.ExecCommand([]string{
					"nsenter", "--target", "1", "--net", "--mount", "--",
					"sh", "-c",
					"iptables -D OUTPUT -p tcp --dport 3300 -j REJECT 2>/dev/null || true; " +
						"iptables -D INPUT -p tcp --sport 3300 -j REJECT 2>/dev/null || true; " +
						"iptables -D OUTPUT -p tcp --dport 6789 -j REJECT 2>/dev/null || true; " +
						"iptables -D INPUT -p tcp --sport 6789 -j REJECT 2>/dev/null || true; " +
						"iptables -D OUTPUT -p tcp --dport 6800:7300 -j REJECT 2>/dev/null || true; " +
						"iptables -D INPUT -p tcp --sport 6800:7300 -j REJECT 2>/dev/null || true",
				})
				if flushErr != nil {
					GinkgoWriter.Printf("Warning: iptables -D cleanup in AfterAll on node %s: %v\n",
						targetNodeName, flushErr)
				}

				if _, delErr := cleanupPod.Delete(); delErr != nil {
					GinkgoWriter.Printf("Warning: delete injector pod in AfterAll: %v\n", delErr)
				}
			}
		})

		It("Split-brain: only the storage-isolated node is fenced; healthy witnesses are untouched",
			reportxml.ID("88877"),
			Label(
				labels.OperatorSBR,
				labels.TierAcceptance,
				labels.FrequencyNightly,
				labels.DisruptionDestructive,
				labels.PlatformAny,
				labels.ComponentRemediation,
			), func() {
				DeferCleanup(func() {
					By("DeferCleanup: removing iptables REJECT rules on target node")

					cleanupPod, pullErr := pod.Pull(APIClient, injectorPodName, medik8sparams.OperatorNs)
					if pullErr == nil {
						_, flushErr := cleanupPod.ExecCommand([]string{
							"nsenter", "--target", "1", "--net", "--mount", "--",
							"sh", "-c",
							"iptables -D OUTPUT -p tcp --dport 3300 -j REJECT 2>/dev/null || true; " +
								"iptables -D INPUT -p tcp --sport 3300 -j REJECT 2>/dev/null || true; " +
								"iptables -D OUTPUT -p tcp --dport 6789 -j REJECT 2>/dev/null || true; " +
								"iptables -D INPUT -p tcp --sport 6789 -j REJECT 2>/dev/null || true; " +
								"iptables -D OUTPUT -p tcp --dport 6800:7300 -j REJECT 2>/dev/null || true; " +
								"iptables -D INPUT -p tcp --sport 6800:7300 -j REJECT 2>/dev/null || true",
						})
						if flushErr != nil {
							GinkgoWriter.Printf("Warning: iptables -D cleanup failed: %v\n", flushErr)
						}

						if _, delErr := cleanupPod.Delete(); delErr != nil {
							GinkgoWriter.Printf("Warning: delete injector pod: %v\n", delErr)
						}
					}

					By("DeferCleanup: force-removing SBR CRs for all involved nodes")

					allNodes := append([]string{targetNodeName}, healthyNodes...)
					for _, nodeName := range allNodes {
						cleanupSBRCR(nodeName)
					}
				})

				By(fmt.Sprintf("Recording boot IDs before injection (target=%q, healthy=%v)",
					targetNodeName, healthyNodes))

				targetBootID, targetBootIDErr := getNodeBootID(targetNodeName)
				Expect(targetBootIDErr).ToNot(HaveOccurred(),
					"Could not retrieve boot ID for target node %q", targetNodeName)

				healthyBootIDs := make(map[string]string, len(healthyNodes))

				for _, nodeName := range healthyNodes {
					bid, bidErr := getNodeBootID(nodeName)
					Expect(bidErr).ToNot(HaveOccurred(),
						"Could not retrieve boot ID for healthy node %q", nodeName)

					healthyBootIDs[nodeName] = bid
				}

				By(fmt.Sprintf("Creating privileged injector pod on target node %q", targetNodeName))

				injectorPod, createErr := pod.NewBuilder(
					APIClient, injectorPodName, medik8sparams.OperatorNs, sbrparams.WatchdogDebugImage).
					DefineOnNode(targetNodeName).
					WithHostPid(true).
					WithPrivilegedFlag().
					CreateAndWaitUntilRunning(medik8sparams.DefaultTimeout)

				Expect(createErr).ToNot(HaveOccurred(),
					"Failed to create injector pod on node %q", targetNodeName)

				By("Injecting CephFS port REJECT rules (INPUT+OUTPUT) on target node only via nsenter")

				// CephFS uses: 3300 (msgr2), 6789 (msgr1 mon), 6800-7300 (OSD/MDS).
				// REJECT causes immediate RST so the SBR agent detects storage loss quickly.
				// Both INPUT and OUTPUT are blocked so the isolated node cannot reach monitors/OSDs.
				rejectRules := [][]string{
					{"nsenter", "--target", "1", "--net", "--mount", "--",
						"iptables", "-I", "OUTPUT", "-p", "tcp", "--dport", "3300", "-j", "REJECT"},
					{"nsenter", "--target", "1", "--net", "--mount", "--",
						"iptables", "-I", "INPUT", "-p", "tcp", "--sport", "3300", "-j", "REJECT"},
					{"nsenter", "--target", "1", "--net", "--mount", "--",
						"iptables", "-I", "OUTPUT", "-p", "tcp", "--dport", "6789", "-j", "REJECT"},
					{"nsenter", "--target", "1", "--net", "--mount", "--",
						"iptables", "-I", "INPUT", "-p", "tcp", "--sport", "6789", "-j", "REJECT"},
					{"nsenter", "--target", "1", "--net", "--mount", "--",
						"iptables", "-I", "OUTPUT", "-p", "tcp", "--dport", "6800:7300", "-j", "REJECT"},
					{"nsenter", "--target", "1", "--net", "--mount", "--",
						"iptables", "-I", "INPUT", "-p", "tcp", "--sport", "6800:7300", "-j", "REJECT"},
				}

				for _, rule := range rejectRules {
					_, execErr := injectorPod.ExecCommand(rule)
					Expect(execErr).ToNot(HaveOccurred(),
						"Failed to inject iptables rule %v on node %q", rule, targetNodeName)
				}

				GinkgoWriter.Printf("CephFS port REJECT rules (INPUT+OUTPUT) applied on node %q only\n",
					targetNodeName)

				By(fmt.Sprintf("Waiting for node %q to acquire %s=True condition",
					targetNodeName, sbrparams.SBRStorageUnhealthyCondition))

				Eventually(func() error {
					cond := getNodeStorageCondition(targetNodeName, sbrparams.SBRStorageUnhealthyCondition)
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
					"Node %q must reach %s=True after CephFS port injection",
					targetNodeName, sbrparams.SBRStorageUnhealthyCondition)

				By("Asserting healthy nodes do NOT acquire an SBR CR (Consistently for 60s)")

				// The Consistently block runs concurrently with NHC deciding to act on the target.
				// If any healthy node gets an SBR CR during this window the test fails immediately.
				Consistently(func() error {
					for _, nodeName := range healthyNodes {
						getErr := sbrCRExists(nodeName)
						if getErr == nil {
							return fmt.Errorf(
								"healthy node %q unexpectedly received a StorageBasedRemediation CR "+
									"— split-brain isolation is broken", nodeName)
						}

						if !k8serrors.IsNotFound(getErr) {
							return fmt.Errorf("unexpected error checking SBR CR for healthy node %q: %w",
								nodeName, getErr)
						}
					}

					return nil
				}, sbrparams.SplitBrainHealthyNodeCheckDuration,
					sbrparams.SplitBrainHealthyNodeCheckInterval).Should(Succeed(),
					"No SBR CR must appear for healthy nodes during the split-brain scenario")

				By(fmt.Sprintf("Waiting for NHC to create StorageBasedRemediation CR for target node %q",
					targetNodeName))

				Eventually(func() error {
					getErr := sbrCRExists(targetNodeName)

					if k8serrors.IsNotFound(getErr) {
						return fmt.Errorf("StorageBasedRemediation/%s not yet created by NHC", targetNodeName)
					}

					return getErr
				}, sbrparams.NHCSBRCRCreationTimeout,
					sbrparams.NHCSBRCRCreationPollInterval).Should(Succeed(),
					"NHC must create StorageBasedRemediation CR for target node %q", targetNodeName)

				GinkgoWriter.Printf("StorageBasedRemediation CR for target node %q created by NHC\n",
					targetNodeName)

				By(fmt.Sprintf("Waiting for target node %q: NotReady → Ready (reboot cycle)", targetNodeName))

				// Phase 1: wait for NotReady.
				Eventually(func() error {
					node, nodeErr := APIClient.CoreV1Interface.Nodes().Get(
						context.TODO(), targetNodeName, metav1.GetOptions{})
					if nodeErr != nil {
						return nodeErr
					}

					for _, cond := range node.Status.Conditions {
						if cond.Type == corev1.NodeReady && cond.Status != corev1.ConditionTrue {
							return nil
						}
					}

					return fmt.Errorf("node %s is still Ready; waiting for NotReady", targetNodeName)
				}, sbrparams.NodeRebootTimeout, sbrparams.NodeRebootPollInterval).Should(Succeed(),
					"Target node %q must become NotReady during SBR-triggered reboot", targetNodeName)

				GinkgoWriter.Printf("Node %q is NotReady — reboot in progress\n", targetNodeName)

				// Phase 2: wait for Ready.
				Eventually(func() error {
					node, nodeErr := APIClient.CoreV1Interface.Nodes().Get(
						context.TODO(), targetNodeName, metav1.GetOptions{})
					if nodeErr != nil {
						return nodeErr
					}

					for _, cond := range node.Status.Conditions {
						if cond.Type == corev1.NodeReady {
							if cond.Status == corev1.ConditionTrue {
								return nil
							}

							return fmt.Errorf("node %s: Ready=%s", targetNodeName, cond.Status)
						}
					}

					return fmt.Errorf("node %s has no Ready condition", targetNodeName)
				}, sbrparams.NodeRebootTimeout, sbrparams.NodeRebootPollInterval).Should(Succeed(),
					"Target node %q must return to Ready after SBR-triggered reboot", targetNodeName)

				GinkgoWriter.Printf("Node %q is Ready again\n", targetNodeName)

				By("Verifying target node boot ID changed (confirms an actual reboot occurred)")

				newTargetBootID, newBootIDErr := getNodeBootID(targetNodeName)
				Expect(newBootIDErr).ToNot(HaveOccurred(),
					"Could not retrieve boot ID for target node %q after reboot", targetNodeName)
				Expect(newTargetBootID).ToNot(Equal(targetBootID),
					"Target node %q boot ID must change after SBR-triggered reboot "+
						"(pre=%q, post=%q — node did not actually reboot)",
					targetNodeName, targetBootID, newTargetBootID)

				By("Asserting all healthy nodes remain schedulable and their boot IDs are unchanged")

				for _, nodeName := range healthyNodes {
					var node *corev1.Node

					Eventually(func() error {
						var getErr error

						node, getErr = APIClient.CoreV1Interface.Nodes().Get(
							context.TODO(), nodeName, metav1.GetOptions{})

						return getErr
					}, medik8sparams.DefaultTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
						"Failed to get healthy node %q for post-test verification", nodeName)

					Expect(isNodeSchedulable(node)).To(BeTrue(),
						"Healthy node %q must remain schedulable after split-brain scenario", nodeName)

					currentBootID := node.Status.NodeInfo.BootID
					Expect(currentBootID).To(Equal(healthyBootIDs[nodeName]),
						"Healthy node %q boot ID must NOT change during split-brain scenario "+
							"(pre=%q, post=%q — node was spuriously fenced)",
						nodeName, healthyBootIDs[nodeName], currentBootID)
				}

				By("Waiting for StorageBasedRemediation CR to be cleaned up by the controller")

				Eventually(func() error {
					getErr := sbrCRExists(targetNodeName)

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

				GinkgoWriter.Printf("Split-brain scenario completed: target node %q fenced and recovered; "+
					"healthy nodes %v untouched\n", targetNodeName, healthyNodes)
			})
	})
