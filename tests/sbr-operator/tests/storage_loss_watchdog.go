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
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

func buildNHCForSBRStorageLoss(nhcName string) *unstructured.Unstructured {
	return buildNHC(nhcName)
}

var _ = Describe(
	"SBR Functional — Node Failure: Total Storage I/O Loss (Watchdog Path)",
	Ordered,
	ContinueOnFailure,
	Label(
		labels.OperatorSBR,
		labels.TierAcceptance,
		labels.FrequencyNightly,
		labels.DisruptionDestructive,
		labels.PlatformAny,
		labels.ComponentRemediation,
		labels.OperatorSBR,
	), func() {
		var (
			targetNodeName   string
			rwxStorageClass  string
			testSBRC         *unstructured.Unstructured
			nhcCreatedByTest bool
			bootIDBeforeTest string
		)

		BeforeAll(func() {
			By("Checking NodeHealthCheck CRD is installed")

			if !isNHCCRDInstalled() {
				Skip("NodeHealthCheck CRD not installed; skipping total storage I/O loss test")
			}

			By("Discovering RWX-capable StorageClass")

			rwxStorageClass = discoverRWXStorageClass()

			if rwxStorageClass == "" {
				Skip("No RWX-capable StorageClass found; skipping total storage I/O loss test")
			}

			// This test blocks CephFS ports (3300, 6789, 6800-7300) to trigger SBRStorageUnhealthy.
			// NFS provisioners use different ports and would not be affected by these rules.
			storageClass, scErr := APIClient.StorageV1Interface.StorageClasses().Get(
				context.TODO(), rwxStorageClass, metav1.GetOptions{})
			if scErr != nil {
				Skip(fmt.Sprintf("StorageClass %q not found (%v); skipping", rwxStorageClass, scErr))
			}

			if !strings.Contains(strings.ToLower(storageClass.Provisioner), "ceph") {
				Skip(fmt.Sprintf("StorageClass %q provisioner %q is not CephFS-backed; "+
					"iptables rules block CephFS ports only — skipping", rwxStorageClass, storageClass.Provisioner))
			}

			GinkgoWriter.Printf("Using StorageClass %q for shared storage\n", rwxStorageClass)

			By("Creating StorageBasedRemediationConfig with shared storage")

			testSBRC = buildSBRC(sbrparams.SBRCWatchdogPathTestName, map[string]interface{}{
				"sharedStorageClass": rwxStorageClass,
			})

			createErr := APIClient.Create(context.TODO(), testSBRC)
			Expect(createErr).ToNot(HaveOccurred(),
				"StorageBasedRemediationConfig %q must be created", sbrparams.SBRCWatchdogPathTestName)

			waitForSBRCReady(sbrparams.SBRCWatchdogPathTestName)

			By("Ensuring NodeHealthCheck CR exists for SBR storage-loss detection")

			nhcObj := buildNHCForSBRStorageLoss(sbrparams.NHCSBRTestName)

			createNHCErr := APIClient.Create(context.TODO(), nhcObj)
			if createNHCErr != nil && !k8serrors.IsAlreadyExists(createNHCErr) {
				Fail(fmt.Sprintf("Failed to create NodeHealthCheck CR %q: %v",
					sbrparams.NHCSBRTestName, createNHCErr))
			}

			nhcCreatedByTest = createNHCErr == nil

			By("Selecting target worker node (schedulable, not controller pod host)")

			controllerNodes := controllerPodNodes()

			nodeList, listErr := APIClient.CoreV1Interface.Nodes().List(context.TODO(), metav1.ListOptions{
				LabelSelector: "node-role.kubernetes.io/worker",
			})
			Expect(listErr).ToNot(HaveOccurred(), "Failed to list worker nodes")

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
				Skip("No schedulable worker node available (excluding controller pod hosts)")
			}

			GinkgoWriter.Printf("Target node for OCP-88880: %q\n", targetNodeName)
		})

		AfterAll(func() {
			By("AfterAll: removing StorageBasedRemediationConfig")

			if testSBRC != nil {
				Eventually(func() error {
					delErr := APIClient.Delete(context.TODO(), testSBRC)
					if delErr == nil || k8serrors.IsNotFound(delErr) {
						return nil
					}

					return delErr
				}, medik8sparams.DefaultTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
					"Failed to delete StorageBasedRemediationConfig %s", sbrparams.SBRCWatchdogPathTestName)

				Eventually(func() error {
					getErr := APIClient.Get(context.TODO(),
						types.NamespacedName{Name: sbrparams.SBRCWatchdogPathTestName,
							Namespace: medik8sparams.OperatorNs},
						testSBRC.DeepCopy())

					if k8serrors.IsNotFound(getErr) {
						return nil
					}

					if getErr != nil {
						return getErr
					}

					return fmt.Errorf("SBRC %s still present", sbrparams.SBRCWatchdogPathTestName)
				}, medik8sparams.DefaultTimeout, sbrparams.DefaultPollInterval).Should(Succeed())
			}

			if nhcCreatedByTest {
				By("AfterAll: removing NodeHealthCheck CR created by this test")

				cleanupNHCCR(sbrparams.NHCSBRTestName)
			}

			By("AfterAll: force-deleting any leftover StorageBasedRemediation CRs")

			if targetNodeName != "" {
				cleanupSBRCR(targetNodeName)
			}
		})

		It("Total storage I/O loss: watchdog fires, node reboots, fencing completes",
			reportxml.ID("88880"),
			Label(
				labels.OperatorSBR,
				labels.TierAcceptance,
				labels.FrequencyNightly,
				labels.DisruptionDestructive,
				labels.PlatformAny,
				labels.ComponentRemediation,
			), func() {
				By("Recording node boot ID before storage injection")

				var bootIDErr error

				bootIDBeforeTest, bootIDErr = getNodeBootID(targetNodeName)
				Expect(bootIDErr).ToNot(HaveOccurred(),
					"Failed to get boot ID for node %s before injection", targetNodeName)

				GinkgoWriter.Printf("Node %s boot ID before test: %s\n", targetNodeName, bootIDBeforeTest)

				By("Creating privileged injector pod on target node")

				injectorPodName := sbrparams.InjectorPodName + "-88880"

				if existing, pullErr := pod.Pull(APIClient, injectorPodName, medik8sparams.OperatorNs); pullErr == nil {
					if _, delErr := existing.Delete(); delErr != nil {
						GinkgoWriter.Printf("Warning: failed to delete stale injector pod: %v\n", delErr)
					}

					Eventually(func() error {
						_, checkErr := pod.Pull(APIClient, injectorPodName, medik8sparams.OperatorNs)
						if checkErr != nil {
							return nil
						}

						return fmt.Errorf("stale injector pod %s still present", injectorPodName)
					}, medik8sparams.DefaultTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
						"Stale injector pod %s must terminate before creating a replacement", injectorPodName)
				}

				injectorPod, createErr := pod.NewBuilder(
					APIClient, injectorPodName, medik8sparams.OperatorNs, sbrparams.WatchdogDebugImage).
					DefineOnNode(targetNodeName).
					WithHostPid(true).
					WithHostNetwork().
					WithPrivilegedFlag().
					WithRestartPolicy(corev1.RestartPolicyNever).
					RedefineDefaultCMD([]string{"sleep", "3600"}).
					CreateAndWaitUntilRunning(medik8sparams.DefaultTimeout)
				Expect(createErr).ToNot(HaveOccurred(),
					"Failed to create injector pod on node %s", targetNodeName)

				DeferCleanup(func() {
					By("DeferCleanup: removing injector pod (iptables rules are cleared on reboot)")

					if existing, pullErr := pod.Pull(APIClient, injectorPodName, medik8sparams.OperatorNs); pullErr == nil {
						// Best-effort: remove rules if node did not reboot; ignore errors.
						// WithHostNetwork() already places the pod in the host network namespace,
						// so no nsenter is needed.
						_, _ = existing.ExecCommand([]string{
							"sh", "-c",
							"iptables -D INPUT -p tcp --dport 3300 -j REJECT 2>/dev/null; " +
								"iptables -D INPUT -p tcp --dport 6789 -j REJECT 2>/dev/null; " +
								"iptables -D INPUT -p tcp -m multiport --dports 6800:7300 -j REJECT 2>/dev/null; " +
								"iptables -D OUTPUT -p tcp --dport 3300 -j REJECT 2>/dev/null; " +
								"iptables -D OUTPUT -p tcp --dport 6789 -j REJECT 2>/dev/null; " +
								"iptables -D OUTPUT -p tcp -m multiport --dports 6800:7300 -j REJECT 2>/dev/null; " +
								"true",
						})

						_, _ = existing.Delete()
					}
				})

				By("Injecting CephFS REJECT rules (INPUT + OUTPUT) on target node via nsenter")

				injectScript := strings.Join([]string{
					"iptables -I INPUT -p tcp --dport 3300 -j REJECT",
					"iptables -I INPUT -p tcp --dport 6789 -j REJECT",
					"iptables -I INPUT -p tcp -m multiport --dports 6800:7300 -j REJECT",
					"iptables -I OUTPUT -p tcp --dport 3300 -j REJECT",
					"iptables -I OUTPUT -p tcp --dport 6789 -j REJECT",
					"iptables -I OUTPUT -p tcp -m multiport --dports 6800:7300 -j REJECT",
				}, " && ")

				// WithHostNetwork() places the pod in the host network namespace directly;
				// no nsenter is needed to reach the host iptables.
				_, execErr := injectorPod.ExecCommand([]string{
					"sh", "-c", injectScript,
				})
				Expect(execErr).ToNot(HaveOccurred(),
					"Failed to inject CephFS REJECT rules on node %s", targetNodeName)

				GinkgoWriter.Printf("CephFS REJECT rules injected on node %s\n", targetNodeName)

				By("Waiting for SBRStorageUnhealthy=True on target node (~18s with CephFS)")

				Eventually(func() error {
					node, nodeErr := APIClient.CoreV1Interface.Nodes().Get(
						context.TODO(), targetNodeName, metav1.GetOptions{})
					if nodeErr != nil {
						return fmt.Errorf("failed to get node %s: %w", targetNodeName, nodeErr)
					}

					for _, cond := range node.Status.Conditions {
						if string(cond.Type) == sbrparams.SBRStorageUnhealthyCondition {
							if cond.Status == corev1.ConditionTrue {
								return nil
							}

							return fmt.Errorf("node %s: SBRStorageUnhealthy=%s (want True)",
								targetNodeName, cond.Status)
						}
					}

					return fmt.Errorf("node %s: SBRStorageUnhealthy condition not yet set", targetNodeName)
				}, sbrparams.StorageInjectionTimeout, sbrparams.StorageInjectionPollInterval).Should(Succeed(),
					"SBRStorageUnhealthy must become True on node %s after CephFS REJECT injection", targetNodeName)

				By("Waiting for NHC to create StorageBasedRemediation CR for target node")

				Eventually(func() error {
					_, pullErr := pullSBRCR(targetNodeName)

					if k8serrors.IsNotFound(pullErr) {
						return fmt.Errorf("StorageBasedRemediation CR for node %s not yet created by NHC",
							targetNodeName)
					}

					return pullErr
				}, sbrparams.NHCSBRCRCreationTimeout, sbrparams.NHCSBRCRCreationPollInterval).Should(Succeed(),
					"NHC must create StorageBasedRemediation CR for node %s", targetNodeName)

				GinkgoWriter.Printf("StorageBasedRemediation CR for node %s created by NHC\n", targetNodeName)

				By("Waiting for target node to become NotReady (watchdog fires)")

				Eventually(func() error {
					node, nodeErr := APIClient.CoreV1Interface.Nodes().Get(
						context.TODO(), targetNodeName, metav1.GetOptions{})
					if nodeErr != nil {
						return fmt.Errorf("failed to get node %s: %w", targetNodeName, nodeErr)
					}

					for _, cond := range node.Status.Conditions {
						if cond.Type == corev1.NodeReady {
							if cond.Status != corev1.ConditionTrue {
								return nil
							}

							return fmt.Errorf("node %s is still Ready; waiting for watchdog to fire",
								targetNodeName)
						}
					}

					return fmt.Errorf("node %s: Ready condition not found", targetNodeName)
				}, sbrparams.NodeRebootTimeout, sbrparams.NodeRebootPollInterval).Should(Succeed(),
					"Node %s must become NotReady after watchdog fires", targetNodeName)

				GinkgoWriter.Printf("Node %s is NotReady — watchdog fired\n", targetNodeName)

				By("Waiting for target node to return Ready (reboot recovery)")

				Eventually(func() error {
					node, nodeErr := APIClient.CoreV1Interface.Nodes().Get(
						context.TODO(), targetNodeName, metav1.GetOptions{})
					if nodeErr != nil {
						return fmt.Errorf("failed to get node %s: %w", targetNodeName, nodeErr)
					}

					for _, cond := range node.Status.Conditions {
						if cond.Type == corev1.NodeReady {
							if cond.Status == corev1.ConditionTrue {
								return nil
							}

							return fmt.Errorf("node %s not yet Ready (status=%s)", targetNodeName, cond.Status)
						}
					}

					return fmt.Errorf("node %s: Ready condition not found", targetNodeName)
				}, sbrparams.NodeRebootTimeout, sbrparams.NodeRebootPollInterval).Should(Succeed(),
					"Node %s must return Ready after watchdog reboot", targetNodeName)

				GinkgoWriter.Printf("Node %s is Ready again\n", targetNodeName)

				By("Verifying boot ID changed — confirming actual reboot occurred")

				newBootID, newBootIDErr := getNodeBootID(targetNodeName)
				Expect(newBootIDErr).ToNot(HaveOccurred(),
					"Failed to get boot ID for node %s after reboot", targetNodeName)

				Expect(newBootID).ToNot(Equal(bootIDBeforeTest),
					"Node %s boot ID must change after watchdog reboot (before=%s, after=%s)",
					targetNodeName, bootIDBeforeTest, newBootID)

				GinkgoWriter.Printf("Node %s rebooted: boot ID %s -> %s\n",
					targetNodeName, bootIDBeforeTest, newBootID)

				By("Verifying FencingSucceeded=True on StorageBasedRemediation CR")

				var fencingObserved bool

				Eventually(func() error {
					sbrCR, pullErr := pullSBRCR(targetNodeName)
					if pullErr != nil {
						if k8serrors.IsNotFound(pullErr) {
							if fencingObserved {
								return nil // CR cleaned up after confirmed fencing
							}

							return fmt.Errorf(
								"StorageBasedRemediation/%s already gone before FencingSucceeded=True was observed",
								targetNodeName)
						}

						return pullErr
					}

					conditions, _, _ := unstructured.NestedSlice(sbrCR.Object, "status", "conditions")

					for _, rawCond := range conditions {
						condMap, ok := rawCond.(map[string]interface{})
						if !ok {
							continue
						}

						condType, _, _ := unstructured.NestedString(condMap, "type")
						condStatus, _, _ := unstructured.NestedString(condMap, "status")

						if condType == sbrparams.FencingSucceededCondition {
							if condStatus == string(corev1.ConditionTrue) {
								fencingObserved = true

								return nil
							}

							return fmt.Errorf("StorageBasedRemediation/%s: FencingSucceeded=%s (want True)",
								targetNodeName, condStatus)
						}
					}

					return fmt.Errorf("StorageBasedRemediation/%s: FencingSucceeded condition not yet set",
						targetNodeName)
				}, sbrparams.SBRCRCleanupTimeout, sbrparams.SBRCRCleanupPollInterval).Should(Succeed(),
					"StorageBasedRemediation/%s must have FencingSucceeded=True", targetNodeName)

				By("Verifying StorageBasedRemediation CR is cleaned up after fencing")

				Eventually(func() error {
					_, pullErr := pullSBRCR(targetNodeName)

					if k8serrors.IsNotFound(pullErr) {
						return nil
					}

					if pullErr != nil {
						return pullErr
					}

					return fmt.Errorf("StorageBasedRemediation/%s still exists; waiting for cleanup",
						targetNodeName)
				}, sbrparams.SBRCRCleanupTimeout, sbrparams.SBRCRCleanupPollInterval).Should(Succeed(),
					"StorageBasedRemediation/%s must be fully removed after fencing", targetNodeName)

				GinkgoWriter.Printf("OCP-88880 complete: node %s rebooted via watchdog, FencingSucceeded=True\n",
					targetNodeName)
			})
	})
