package tests

import (
	"context"
	"fmt"
	"os"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/deployment"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/pod"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/reportxml"
	"sigs.k8s.io/controller-runtime/pkg/client"

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

// cephFSFlushCmd removes the CephFS REJECT iptables rules injected by the detect-only storage test.
const cephFSFlushCmd = "iptables -D OUTPUT -p tcp --dport 3300 -j REJECT 2>/dev/null || true; " +
	"iptables -D INPUT -p tcp --sport 3300 -j REJECT 2>/dev/null || true; " +
	"iptables -D OUTPUT -p tcp --dport 6789 -j REJECT 2>/dev/null || true; " +
	"iptables -D INPUT -p tcp --sport 6789 -j REJECT 2>/dev/null || true; " +
	"iptables -D OUTPUT -p tcp --dport 6800:7300 -j REJECT 2>/dev/null || true; " +
	"iptables -D INPUT -p tcp --sport 6800:7300 -j REJECT 2>/dev/null || true"

// keepalivePodName returns a valid pod name for the per-node watchdog-keepalive pod.
func keepalivePodName(nodeName string) string {
	safe := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}

		return '-'
	}, strings.ToLower(nodeName))

	name := sbrparams.WatchdogKeepaliveNamePrefix + safe
	if len(name) > 253 {
		name = name[:253]
	}

	return strings.TrimRight(name, "-")
}

// deleteKeepalivePods removes any running watchdog-keepalive pods. Safe to call when pods may not exist.
func deleteKeepalivePods(podNames []string) {
	for _, podName := range podNames {
		kp, pullErr := pod.Pull(APIClient, podName, medik8sparams.OperatorNs)
		if pullErr != nil {
			continue
		}

		if _, delErr := kp.Delete(); delErr != nil && !k8serrors.IsNotFound(delErr) {
			GinkgoT().Logf("Warning: delete keepalive pod %s: %v", podName, delErr)
		}
	}
}

var _ = Describe(
	"SBR Functional — detectOnlyMode",
	Ordered,
	ContinueOnFailure,
	Label(sbrparams.Label), func() {
		var (
			detectOnlySBRC  *unstructured.Unstructured
			nhcCR           *unstructured.Unstructured
			nhcCreatedByUs  bool
			rwxStorageClass string
			targetNodeName  string
			injectorPodName string
			keepalivePods   []string // pod names, one per worker node
		)

		BeforeAll(func() {
			By("Verifying SBR operator deployment is ready")

			sbrDeployment, pullErr := deployment.Pull(
				APIClient, sbrparams.OperatorDeploymentName, medik8sparams.OperatorNs)
			Expect(pullErr).ToNot(HaveOccurred(), "Failed to get SBR operator deployment")
			Expect(sbrDeployment.IsReady(medik8sparams.DefaultTimeout)).To(BeTrue(),
				"SBR operator deployment must be Ready before running detectOnlyMode tests")

			By("Discovering RWX storage class")

			rwxStorageClass = discoverRWXStorageClass()

			GinkgoWriter.Printf("Using storage class: %s\n", rwxStorageClass)

			By("Listing schedulable worker nodes")

			nodeList, listErr := APIClient.CoreV1Interface.Nodes().List(context.TODO(), metav1.ListOptions{
				LabelSelector: "node-role.kubernetes.io/worker",
			})
			Expect(listErr).ToNot(HaveOccurred(), "Failed to list worker nodes")

			var workerNodes []string

			for i := range nodeList.Items {
				node := &nodeList.Items[i]
				if isNodeSchedulable(node) {
					workerNodes = append(workerNodes, node.Name)
				}
			}

			Expect(workerNodes).ToNot(BeEmpty(), "No schedulable worker nodes found")

			targetNodeName = workerNodes[0]
			injectorPodName = "sbr-detect-only-injector-" + strings.Map(func(r rune) rune {
				if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
					return r
				}

				return '-'
			}, strings.ToLower(targetNodeName))

			if len(injectorPodName) > 63 {
				injectorPodName = injectorPodName[:63]
			}

			injectorPodName = strings.TrimRight(injectorPodName, "-")

			By("Cleaning up any stale detectOnly StorageBasedRemediationConfig from a previous run")

			stale := buildSBRC(sbrparams.SBRCDetectOnlyTestName, map[string]interface{}{})

			if deleteErr := APIClient.Delete(context.TODO(), stale); deleteErr != nil &&
				!k8serrors.IsNotFound(deleteErr) {
				GinkgoT().Logf("Warning: pre-test cleanup of %s failed: %v",
					sbrparams.SBRCDetectOnlyTestName, deleteErr)
			}

			By("Waiting for stale StorageBasedRemediationConfig to be fully removed before recreating")

			Eventually(func() error {
				staleCheck := buildSBRC(sbrparams.SBRCDetectOnlyTestName, map[string]interface{}{})

				getErr := APIClient.Get(context.TODO(),
					types.NamespacedName{Name: sbrparams.SBRCDetectOnlyTestName, Namespace: medik8sparams.OperatorNs},
					staleCheck)
				if k8serrors.IsNotFound(getErr) {
					return nil
				}

				if getErr != nil {
					return getErr
				}

				return fmt.Errorf("SBRC %s still terminating", sbrparams.SBRCDetectOnlyTestName)
			}, sbrparams.SBRCReadyTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
				"Stale SBRC must be fully gone before recreating")

			By("Creating watchdog-keepalive pods on all worker nodes (RHWA-1068 precondition)")
			// Each pod holds /dev/watchdog open via nsenter into the host mount namespace, simulating
			// a scenario where another process (e.g., SNR) already holds the device. If /dev/watchdog
			// is already held or does not exist the pod falls back to sleep so it stays Running.
			for _, nodeName := range workerNodes {
				podName := keepalivePodName(nodeName)

				keepalivePod, createErr := pod.NewBuilder(
					APIClient, podName, medik8sparams.OperatorNs, sbrparams.WatchdogDebugImage).
					DefineOnNode(nodeName).
					WithHostPid(true).
					WithPrivilegedFlag().
					RedefineDefaultCMD([]string{"/bin/bash", "-c",
						"nsenter --target 1 --mount -- sh -c " +
							"'exec 200>/dev/watchdog 2>/dev/null; " +
							"while true; do printf V >&200 2>/dev/null; sleep 10; done' " +
							"|| sleep 999999"}).
					CreateAndWaitUntilRunning(sbrparams.WatchdogKeepaliveTimeout)
				if createErr != nil {
					GinkgoT().Logf("Warning: keepalive pod %s on node %s: %v; "+
						"watchdog contention may not be simulated on this node", podName, nodeName, createErr)
				} else {
					GinkgoWriter.Printf("Keepalive pod %s Running on node %s\n",
						keepalivePod.Definition.Name, nodeName)

					keepalivePods = append(keepalivePods, podName)
				}
			}

			By("Creating StorageBasedRemediationConfig with detectOnlyMode: Enabled")

			detectOnlySBRC = buildSBRC(sbrparams.SBRCDetectOnlyTestName, map[string]interface{}{
				"detectOnlyMode":     "Enabled",
				"sharedStorageClass": rwxStorageClass,
			})

			createErr := APIClient.Create(context.TODO(), detectOnlySBRC)
			Expect(createErr).ToNot(HaveOccurred(),
				"StorageBasedRemediationConfig with detectOnlyMode: Enabled must be admitted by the API server")

			By("Waiting for agent DaemonSet to become ready with detectOnlyMode: Enabled")

			waitForSBRCReady(sbrparams.SBRCDetectOnlyTestName)
		})

		AfterAll(func() {
			By("AfterAll: removing watchdog-keepalive pods")

			deleteKeepalivePods(keepalivePods)
			keepalivePods = nil

			By("AfterAll: removing NHC CR if created by this test")

			if nhcCreatedByUs && nhcCR != nil {
				cleanupNHCCR(sbrparams.NHCDetectOnlyTestName)
			}

			By("AfterAll: removing any SBR CR for target node")

			if targetNodeName != "" {
				cleanupSBRCR(targetNodeName)
			}

			By("AfterAll: flushing any remaining injector pod and iptables rules")

			cleanupPod, pullErr := pod.Pull(APIClient, injectorPodName, medik8sparams.OperatorNs)
			if pullErr == nil {
				_, _ = cleanupPod.ExecCommand([]string{
					"nsenter", "--target", "1", "--net", "--",
					"sh", "-c", cephFSFlushCmd,
				})
				_, _ = cleanupPod.Delete()
			}

			By("AfterAll: removing detect-only StorageBasedRemediationConfig")

			if detectOnlySBRC != nil {
				if deleteErr := APIClient.Delete(context.TODO(), detectOnlySBRC); deleteErr != nil {
					if !k8serrors.IsNotFound(deleteErr) {
						GinkgoT().Logf("Warning: AfterAll cleanup delete %s failed: %v",
							sbrparams.SBRCDetectOnlyTestName, deleteErr)
					}
				} else {
					Eventually(func() error {
						getErr := APIClient.Get(context.TODO(),
							types.NamespacedName{Name: sbrparams.SBRCDetectOnlyTestName,
								Namespace: medik8sparams.OperatorNs},
							detectOnlySBRC.DeepCopy())

						if k8serrors.IsNotFound(getErr) {
							return nil
						}

						if getErr != nil {
							return getErr
						}

						return fmt.Errorf("SBRC %s still present after delete", sbrparams.SBRCDetectOnlyTestName)
					}, medik8sparams.DefaultTimeout, sbrparams.DefaultPollInterval).Should(Succeed())
				}
			}
		})

		// RHWA-1068: SBR agent crashes on startup in detect-only mode when watchdog device is busy.
		// The keepalive pods created in BeforeAll hold /dev/watchdog open to simulate the condition.
		// This test is guarded by SBR_TEST_RHWA1068=true because the bug is not yet fixed in the
		// current operator version. Enable it once the operator fix ships.
		It("RHWA-1068 regression: agent pods reach Ready despite /dev/watchdog held by another process",
			reportxml.ID("88876"),
			Label(
				labels.OperatorSBR,
				labels.TierAcceptance,
				labels.FrequencyNightly,
				labels.DisruptionNonDestructive,
				labels.PlatformAny,
				labels.ComponentController,
			), func() {
				if os.Getenv("SBR_TEST_RHWA1068") != "true" {
					GinkgoWriter.Printf("WARNING: skipping RHWA-1068 regression check — " +
						"bug not yet fixed in current operator version. " +
						"Set SBR_TEST_RHWA1068=true once the operator fix ships.\n")
					Skip("RHWA-1068 not yet fixed; set SBR_TEST_RHWA1068=true to enable this check")
				}

				DeferCleanup(func() {
					By("DeferCleanup: deleting watchdog-keepalive pods to release /dev/watchdog")

					deleteKeepalivePods(keepalivePods)
					keepalivePods = nil
				})

				dsName := sbrparams.SBRAgentDaemonSetPrefix + sbrparams.SBRCDetectOnlyTestName

				By("Asserting ALL agent DaemonSet pods are Ready — none should be in CrashLoopBackOff")
				// Without the RHWA-1068 fix the watchdog preflight runs unconditionally before the
				// agent checks detectOnlyMode. All pods fail with "watchdog device is not available"
				// and NumberReady stays at 0 until this Eventually times out.
				Eventually(func() error {
					agentDS, err := APIClient.DaemonSets(medik8sparams.OperatorNs).Get(
						context.TODO(), dsName, metav1.GetOptions{})
					if err != nil {
						return fmt.Errorf("DaemonSet %s not found: %w", dsName, err)
					}

					if agentDS.Status.NumberReady < agentDS.Status.DesiredNumberScheduled {
						return fmt.Errorf("DaemonSet %s: %d/%d pods ready — some agents may be in CrashLoopBackOff",
							dsName, agentDS.Status.NumberReady, agentDS.Status.DesiredNumberScheduled)
					}

					return nil
				}, sbrparams.DetectOnlyAllPodsReadyTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
					"All agent pods must be Ready; detect-only mode must skip the watchdog preflight (RHWA-1068)")

				By("Verifying no agent pod log contains the RHWA-1068 crash message")

				agentDS, dsErr := APIClient.DaemonSets(medik8sparams.OperatorNs).Get(
					context.TODO(), dsName, metav1.GetOptions{})
				Expect(dsErr).ToNot(HaveOccurred(), "Failed to get agent DaemonSet %s", dsName)

				var selectorParts []string

				for k, v := range agentDS.Spec.Selector.MatchLabels {
					selectorParts = append(selectorParts, k+"="+v)
				}

				agentPodList, listErr := APIClient.CoreV1Interface.Pods(medik8sparams.OperatorNs).List(
					context.TODO(), metav1.ListOptions{LabelSelector: strings.Join(selectorParts, ",")})
				Expect(listErr).ToNot(HaveOccurred(), "Failed to list agent pods for DaemonSet %s", dsName)

				const crashMsg = "Pre-flight checks failed: watchdog device is not available"

				for i := range agentPodList.Items {
					agentPod := &agentPodList.Items[i]

					rawLogs, logsErr := APIClient.CoreV1Interface.Pods(medik8sparams.OperatorNs).
						GetLogs(agentPod.Name, &corev1.PodLogOptions{}).DoRaw(context.TODO())
					if logsErr != nil {
						GinkgoWriter.Printf("Warning: could not fetch logs for agent pod %s: %v\n",
							agentPod.Name, logsErr)

						continue
					}

					Expect(string(rawLogs)).ToNot(ContainSubstring(crashMsg),
						"Agent pod %s must not log watchdog preflight failure in detect-only mode (RHWA-1068)",
						agentPod.Name)

					// If the pod restarted, the crash is in the previous (terminated) container.
					for _, cs := range agentPod.Status.ContainerStatuses {
						if cs.RestartCount > 0 {
							prevLogs, prevErr := APIClient.CoreV1Interface.Pods(medik8sparams.OperatorNs).
								GetLogs(agentPod.Name, &corev1.PodLogOptions{Previous: true}).DoRaw(context.TODO())
							if prevErr == nil {
								Expect(string(prevLogs)).ToNot(ContainSubstring(crashMsg),
									"Agent pod %s previous container must not log watchdog preflight failure (RHWA-1068)",
									agentPod.Name)
							}

							break
						}
					}
				}
			})

		It("Verify SBRC detectOnlyMode field: spec reflection, storage-fault suppression, toggle, and DaemonSet GC",
			reportxml.ID("88876"),
			Label(
				labels.OperatorSBR,
				labels.TierAcceptance,
				labels.FrequencyNightly,
				labels.DisruptionNonDestructive,
				labels.PlatformAny,
				labels.ComponentController,
			), func() {
				DeferCleanup(func() {
					By("DeferCleanup: flushing iptables REJECT rules and deleting injector pod")

					cleanupPod, pullErr := pod.Pull(APIClient, injectorPodName, medik8sparams.OperatorNs)
					if pullErr == nil {
						_, _ = cleanupPod.ExecCommand([]string{
							"nsenter", "--target", "1", "--net", "--",
							"sh", "-c", cephFSFlushCmd,
						})
						_, _ = cleanupPod.Delete()
					}

					By("DeferCleanup: removing SBR CR for target node")

					cleanupSBRCR(targetNodeName)

					By("DeferCleanup: removing NHC CR if created by this test")

					if nhcCreatedByUs && nhcCR != nil {
						cleanupNHCCR(sbrparams.NHCDetectOnlyTestName)

						nhcCreatedByUs = false
					}
				})

				By("Fetching the StorageBasedRemediationConfig from the cluster")

				liveSBRC := &unstructured.Unstructured{}
				liveSBRC.SetGroupVersionKind(detectOnlySBRC.GroupVersionKind())

				getErr := APIClient.Get(context.TODO(),
					types.NamespacedName{Name: sbrparams.SBRCDetectOnlyTestName, Namespace: medik8sparams.OperatorNs},
					liveSBRC)
				Expect(getErr).ToNot(HaveOccurred(),
					"StorageBasedRemediationConfig %q must exist on the cluster", sbrparams.SBRCDetectOnlyTestName)

				mode, found, nestedErr := unstructured.NestedString(liveSBRC.Object, "spec", "detectOnlyMode")
				Expect(nestedErr).ToNot(HaveOccurred(),
					"detectOnlyMode field must be readable from StorageBasedRemediationConfig spec")
				Expect(found).To(BeTrue(),
					"detectOnlyMode must be present in StorageBasedRemediationConfig spec")
				Expect(mode).To(Equal("Enabled"),
					"detectOnlyMode must be Enabled in the StorageBasedRemediationConfig spec")

				nhcInstalled := isNHCCRDInstalled()

				if nhcInstalled {
					By("NHC is installed — creating NodeHealthCheck CR for detect-only suppression test")

					nhcCR = buildNHC(sbrparams.NHCDetectOnlyTestName)

					if createNHCErr := APIClient.Create(context.TODO(), nhcCR); createNHCErr != nil {
						if !k8serrors.IsAlreadyExists(createNHCErr) {
							Expect(createNHCErr).ToNot(HaveOccurred(),
								"Failed to create NodeHealthCheck CR %q", sbrparams.NHCDetectOnlyTestName)
						}

						GinkgoWriter.Printf("NodeHealthCheck %q already exists; using it as-is\n",
							sbrparams.NHCDetectOnlyTestName)
					} else {
						nhcCreatedByUs = true
					}
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

				By("Injecting CephFS port REJECT rules on target node")
				// CephFS uses: 3300 (msgr2), 6789 (msgr1 mon), 6800-7300 (OSD/MDS).
				// REJECT causes immediate RST so the SBR agent detects storage loss quickly.
				rejectRules := [][]string{
					{"nsenter", "--target", "1", "--net", "--",
						"iptables", "-I", "OUTPUT", "-p", "tcp", "--dport", "3300", "-j", "REJECT"},
					{"nsenter", "--target", "1", "--net", "--",
						"iptables", "-I", "INPUT", "-p", "tcp", "--sport", "3300", "-j", "REJECT"},
					{"nsenter", "--target", "1", "--net", "--",
						"iptables", "-I", "OUTPUT", "-p", "tcp", "--dport", "6789", "-j", "REJECT"},
					{"nsenter", "--target", "1", "--net", "--",
						"iptables", "-I", "INPUT", "-p", "tcp", "--sport", "6789", "-j", "REJECT"},
					{"nsenter", "--target", "1", "--net", "--",
						"iptables", "-I", "OUTPUT", "-p", "tcp", "--dport", "6800:7300", "-j", "REJECT"},
					{"nsenter", "--target", "1", "--net", "--",
						"iptables", "-I", "INPUT", "-p", "tcp", "--sport", "6800:7300", "-j", "REJECT"},
				}

				for _, rule := range rejectRules {
					_, execErr := injectorPod.ExecCommand(rule)
					Expect(execErr).ToNot(HaveOccurred(),
						"Failed to inject iptables rule %v on node %q", rule, targetNodeName)
				}

				By(fmt.Sprintf("Waiting for node %q to acquire %s=True",
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

				if nhcInstalled {
					By("Waiting for NHC to create StorageBasedRemediation CR for target node (expected NHC behavior)")

					Eventually(func() error {
						getErr := sbrCRExists(targetNodeName)
						if k8serrors.IsNotFound(getErr) {
							return fmt.Errorf("StorageBasedRemediation/%s not yet created by NHC", targetNodeName)
						}

						return getErr
					}, sbrparams.NHCSBRCRCreationTimeout, sbrparams.NHCSBRCRCreationPollInterval).Should(Succeed(),
						"NHC must create StorageBasedRemediation CR for target node %q", targetNodeName)
				}

				By(fmt.Sprintf("Consistently asserting node %q is not cordoned or fenced while detectOnlyMode is Enabled",
					targetNodeName))

				Consistently(func() error {
					node, nodeErr := APIClient.CoreV1Interface.Nodes().Get(
						context.TODO(), targetNodeName, metav1.GetOptions{})
					if nodeErr != nil {
						return fmt.Errorf("failed to get node %s: %w", targetNodeName, nodeErr)
					}

					if node.Spec.Unschedulable {
						return fmt.Errorf(
							"node %s was cordoned — SBR controller must not cordon in detect-only mode",
							targetNodeName)
					}

					for _, cond := range node.Status.Conditions {
						if cond.Type == corev1.NodeReady && cond.Status != corev1.ConditionTrue {
							return fmt.Errorf(
								"node %s is not Ready — unexpected reboot or fence occurred in detect-only mode",
								targetNodeName)
						}
					}

					if nhcInstalled {
						// The SBR CR may exist (NHC creates it — that is correct and expected).
						// Assert the controller has not set FencingSucceeded=True on it.
						sbrCR := &unstructured.Unstructured{}
						sbrCR.SetAPIVersion(sbrparams.CRDGroup + "/" + sbrparams.CRDVersion)
						sbrCR.SetKind("StorageBasedRemediation")

						if crGetErr := APIClient.Get(context.TODO(),
							types.NamespacedName{Name: targetNodeName, Namespace: medik8sparams.OperatorNs},
							sbrCR); crGetErr == nil {
							conditions, _, _ := unstructured.NestedSlice(sbrCR.Object, "status", "conditions")
							for _, c := range conditions {
								condMap, ok := c.(map[string]interface{})
								if !ok {
									continue
								}

								if condMap["type"] == sbrparams.FencingSucceededCondition &&
									condMap["status"] == string(corev1.ConditionTrue) {
									return fmt.Errorf(
										"StorageBasedRemediation CR has FencingSucceeded=True — " +
											"controller must not fence in detect-only mode")
								}
							}
						}
					}

					return nil
				}, sbrparams.DetectOnlySuppressionCheckDuration, sbrparams.DetectOnlySuppressionCheckInterval).
					Should(Succeed(),
						"Node %q must remain Ready and schedulable for %s while detectOnlyMode is Enabled",
						targetNodeName, sbrparams.DetectOnlySuppressionCheckDuration)

				By("Removing CephFS REJECT rules before toggling detectOnlyMode to avoid a real fence cycle")

				_, _ = injectorPod.ExecCommand([]string{
					"nsenter", "--target", "1", "--net", "--",
					"sh", "-c", cephFSFlushCmd,
				})
				_, _ = injectorPod.Delete()

				By("Patching StorageBasedRemediationConfig to detectOnlyMode: Disabled")

				patchTarget := &unstructured.Unstructured{}
				patchTarget.SetGroupVersionKind(detectOnlySBRC.GroupVersionKind())
				patchTarget.SetName(sbrparams.SBRCDetectOnlyTestName)
				patchTarget.SetNamespace(medik8sparams.OperatorNs)

				patchErr := APIClient.Patch(context.TODO(), patchTarget,
					client.RawPatch(types.MergePatchType, []byte(`{"spec":{"detectOnlyMode":"Disabled"}}`)))
				Expect(patchErr).ToNot(HaveOccurred(),
					"Patching StorageBasedRemediationConfig detectOnlyMode to Disabled must succeed")

				By("Verifying detectOnlyMode is now Disabled in the StorageBasedRemediationConfig spec")

				Eventually(func() error {
					freshSBRC := &unstructured.Unstructured{}
					freshSBRC.SetGroupVersionKind(detectOnlySBRC.GroupVersionKind())

					if fetchErr := APIClient.Get(context.TODO(),
						types.NamespacedName{Name: sbrparams.SBRCDetectOnlyTestName, Namespace: medik8sparams.OperatorNs},
						freshSBRC); fetchErr != nil {
						return fmt.Errorf("failed to fetch StorageBasedRemediationConfig: %w", fetchErr)
					}

					currentMode, mFound, mErr := unstructured.NestedString(freshSBRC.Object, "spec", "detectOnlyMode")
					if mErr != nil {
						return fmt.Errorf("reading spec.detectOnlyMode: %w", mErr)
					}

					if !mFound || currentMode != "Disabled" {
						return fmt.Errorf("detectOnlyMode is %q, want Disabled", currentMode)
					}

					return nil
				}, medik8sparams.DefaultTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
					"StorageBasedRemediationConfig detectOnlyMode must reflect Disabled after patch")

				By("Verifying agent DaemonSet remains ready after toggling detectOnlyMode to Disabled")

				waitForSBRCReady(sbrparams.SBRCDetectOnlyTestName)

				By("Deleting the StorageBasedRemediationConfig")

				liveRef := detectOnlySBRC.DeepCopy()

				deleteErr := APIClient.Delete(context.TODO(), liveRef)
				Expect(deleteErr).ToNot(HaveOccurred(),
					"StorageBasedRemediationConfig %q must be deletable", sbrparams.SBRCDetectOnlyTestName)

				// Nil out so AfterAll does not attempt a second delete.
				detectOnlySBRC = nil

				By("Verifying agent DaemonSet is garbage-collected after StorageBasedRemediationConfig deletion")

				dsName := sbrparams.SBRAgentDaemonSetPrefix + sbrparams.SBRCDetectOnlyTestName

				Eventually(func() error {
					_, getErr := APIClient.DaemonSets(medik8sparams.OperatorNs).Get(
						context.TODO(), dsName, metav1.GetOptions{})

					if k8serrors.IsNotFound(getErr) {
						return nil
					}

					if getErr != nil {
						return fmt.Errorf("unexpected error checking DaemonSet %s: %w", dsName, getErr)
					}

					return fmt.Errorf("DaemonSet %q still exists after StorageBasedRemediationConfig deletion",
						dsName)
				}, sbrparams.SBRCDaemonSetGCTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
					"Agent DaemonSet %q must be garbage-collected after StorageBasedRemediationConfig deletion",
					dsName)
			})
	})
