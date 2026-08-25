package tests

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/deployment"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/reportxml"

	"github.com/medik8s/system-tests/tests/internal/helpers"
	"github.com/medik8s/system-tests/tests/internal/labels"
	. "github.com/medik8s/system-tests/tests/internal/medik8sinittools"
	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
	"github.com/medik8s/system-tests/tests/nhc-operator/internal/nhcparams"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// NHC works with any operator providing a remediation template CRD.
// These tests use SNR as the remediator because it is co-installed
// in the Prow CI job and provides automatic node reboot recovery.
var _ = Describe("NHC Functional -- Remediation Trigger and CR Lifecycle",
	Serial, Ordered, ContinueOnFailure,
	Label(labels.OperatorNHC, nhcparams.Label,
		labels.DisruptionDestructive, labels.FrequencyWeekly),
	func() {
		var (
			ctx                   context.Context
			targetWorkerName      string
			oldBootID             string
			nhcControllerNodeName string // set by test3 (OCP-69711) for control-plane disruption
		)

		BeforeAll(func() {
			ctx = context.Background()

			By("Checking SSH access is available for kubelet stop/start")

			if !isSSHAvailable() {
				Skip("SSH not available -- NHC remediation trigger tests require SSH access to worker nodes")
			}

			By("Checking SNR CRD is installed (used as remediator in these tests)")

			if !isSNRCRDInstalled(ctx) {
				Skip("SelfNodeRemediation CRD not found; SNR operator not installed -- skipping NHC remediation trigger tests")
			}

			By("Verifying NHC operator deployment is ready")

			nhcDeployment, err := deployment.Pull(
				APIClient, nhcparams.OperatorDeploymentName, medik8sparams.OperatorNs)
			Expect(err).ToNot(HaveOccurred(), "Failed to get NHC deployment")
			Expect(nhcDeployment.IsReady(medik8sparams.DefaultTimeout)).To(BeTrue(),
				"NHC deployment is not Ready")

			By("Verifying at least 2 Ready worker nodes")

			workerCount, err := helpers.CountReadyWorkerNodes(ctx, APIClient)
			Expect(err).ToNot(HaveOccurred())
			Expect(workerCount).To(BeNumerically(">=", 2),
				"NHC remediation trigger tests require at least 2 Ready worker nodes")

			By("Selecting target worker node")

			targetNode, err := helpers.SelectWorkerNode(ctx, APIClient)
			Expect(err).ToNot(HaveOccurred(), "Failed to select worker node")

			targetWorkerName = targetNode.Name
			GinkgoWriter.Printf("Target worker node: %s\n", targetWorkerName)
		})

		BeforeEach(func() {
			By("Verifying NHC operator deployment is ready")

			nhcDeployment, err := deployment.Pull(
				APIClient, nhcparams.OperatorDeploymentName, medik8sparams.OperatorNs)
			Expect(err).ToNot(HaveOccurred())
			Expect(nhcDeployment.IsReady(medik8sparams.DefaultTimeout)).To(BeTrue(),
				"NHC deployment is not Ready")

			By("Verifying target node is Ready")

			node := &corev1.Node{}
			Expect(APIClient.Get(ctx, client.ObjectKey{Name: targetWorkerName}, node)).To(Succeed())
			Expect(helpers.IsNodeReady(node)).To(BeTrue(),
				"Target node %s is not Ready before test", targetWorkerName)

			By("Recording boot ID")

			oldBootID, err = helpers.GetNodeBootIDFromAPI(ctx, APIClient, targetWorkerName)
			Expect(err).ToNot(HaveOccurred(),
				"Must read boot ID from node %s", targetWorkerName)

			By("Pre-cleaning stale CRs")

			cleanupSNRCR(ctx, targetWorkerName)
			cleanupNHCCR(ctx, nhcparams.NHCTestName)
			cleanupNHCCR(ctx, nhcparams.NHCSecondTestName)
			cleanupNHCCR(ctx, nhcparams.NHCOldDefaultName)
			cleanupNHCCR(ctx, nhcparams.NHCControlPlaneTestName)

			GinkgoWriter.Printf("Pre-remediation boot ID: %s\n", oldBootID)
		})

		JustAfterEach(func() {
			if CurrentSpecReport().Failed() {
				logNHCControllerState()
			}

			// Cleanup order: NHC CRs first, then SNR CR, then node recovery.
			cleanupNHCCR(ctx, nhcparams.NHCTestName)
			cleanupNHCCR(ctx, nhcparams.NHCSecondTestName)
			cleanupNHCCR(ctx, nhcparams.NHCOldDefaultName)
			cleanupNHCCR(ctx, nhcparams.NHCControlPlaneTestName)

			// Recover all disrupted nodes. Best-effort SSH kubelet restart
			// followed by WaitForNodeReady safety net. MUST NOT use Expect --
			// failure would abort JustAfterEach, skipping remaining cleanup.
			for _, nodeName := range []string{targetWorkerName, nhcControllerNodeName} {
				if nodeName == "" {
					continue
				}

				cleanupSNRCR(ctx, nodeName)

				if isSSHAvailable() {
					if sshErr := startKubeletForRemediation(ctx, nodeName); sshErr != nil {
						GinkgoWriter.Printf(
							"WARNING: SSH kubelet restart failed for %s (best-effort): %v\n",
							nodeName, sshErr)
						AddReportEntry("ssh-kubelet-restart-failed",
							fmt.Sprintf("node %s: %v", nodeName, sshErr))
					}
				}

				By("Safety net: waiting for node " + nodeName + " to become Ready")

				if err := helpers.WaitForNodeReady(ctx, APIClient,
					nodeName,
					nhcparams.DefaultPollInterval, nhcparams.NodeReadyTimeout,
					GinkgoWriter.Printf,
				); err != nil {
					GinkgoWriter.Printf(
						"WARNING: node %s did not become Ready within %s: %v\n",
						nodeName, nhcparams.NodeReadyTimeout, err)
					AddReportEntry("safety-net-recovery-failed",
						fmt.Sprintf("node %s did not recover: %v", nodeName, err))
				}
			}

			nhcControllerNodeName = "" // reset for next test
		})

		It("should block selector editing and deletion during remediation",
			reportxml.ID("56600"),
			Label(labels.TierAcceptance, labels.PlatformAny, labels.ComponentRemediation),
			func() {
				By("Creating NHC CR targeting single node by hostname")

				nhcCR := buildNHCWithHostnameSelector(nhcparams.NHCTestName, targetWorkerName)
				Expect(APIClient.Create(ctx, nhcCR)).To(Succeed(),
					"Failed to create NHC CR")

				By("Waiting for NHC to reach Enabled phase")

				Expect(waitForNHCPhase(ctx, nhcparams.NHCTestName,
					nhcparams.NHCPhaseEnabled, medik8sparams.DefaultTimeout)).To(Succeed())

				By(fmt.Sprintf("Stopping kubelet on %s to trigger remediation", targetWorkerName))

				Expect(stopKubeletForRemediation(ctx, targetWorkerName)).To(Succeed())

				By("Waiting for NHC to enter Remediating phase")

				Expect(waitForNHCPhase(ctx, nhcparams.NHCTestName,
					nhcparams.NHCPhaseRemediating, nhcparams.NodeNotReadyTimeout)).To(Succeed(),
					"NHC did not enter Remediating phase")

				By("Verifying minHealthy and unhealthyConditions are editable during remediation")

				// Non-selector fields should remain editable even during active remediation.
				editableTarget := &unstructured.Unstructured{}
				editableTarget.SetGroupVersionKind(nhcGVK)
				editableTarget.SetName(nhcparams.NHCTestName)

				editablePatch := []byte(`{"spec":{"minHealthy":"0%","unhealthyConditions":[{"type":"Ready","status":"False","duration":"60s"},{"type":"Ready","status":"Unknown","duration":"60s"}]}}`)
				Expect(APIClient.Patch(ctx, editableTarget,
					client.RawPatch(types.MergePatchType, editablePatch),
				)).To(Succeed(),
					"minHealthy and unhealthyConditions should be editable during remediation")
				GinkgoWriter.Println("minHealthy and unhealthyConditions edit succeeded during remediation (expected)")

				By("Verifying selector edit is rejected during remediation")

				// NHC webhook should reject selector changes during active remediation.
				target := &unstructured.Unstructured{}
				target.SetGroupVersionKind(nhcGVK)
				target.SetName(nhcparams.NHCTestName)

				patchBytes := []byte(`{"spec":{"selector":{"matchLabels":{"kubernetes.io/hostname":"other-node"}}}}`)
				patchErr := APIClient.Patch(ctx, target,
					client.RawPatch(types.MergePatchType, patchBytes),
				)

				Expect(patchErr).To(MatchError(ContainSubstring("selector update prohibited due to running remediation")),
					"Selector edit should be rejected by NHC webhook during remediation")
				GinkgoWriter.Printf("Selector edit rejected (expected): %v\n", patchErr)

				By("Verifying NHC CR deletion is blocked during remediation")

				deleteErr := APIClient.Delete(ctx, nhcCR)
				// NHC webhook rejects deletion during active remediation.
				Expect(deleteErr).To(MatchError(ContainSubstring("deletion prohibited due to running remediation")),
					"NHC webhook should reject deletion during active remediation")
				GinkgoWriter.Printf("NHC deletion rejected (expected): %v\n", deleteErr)

				// Verify the CR still exists and is still Remediating.
				phase, getErr := getNHCPhase(ctx, nhcparams.NHCTestName)
				Expect(getErr).ToNot(HaveOccurred(),
					"NHC CR should still exist during remediation")
				Expect(phase).To(Equal(nhcparams.NHCPhaseRemediating),
					"NHC should still be Remediating after delete attempt")
				GinkgoWriter.Printf("NHC phase after delete attempt: %s (deleteErr: %v)\n", phase, deleteErr)

				By("Waiting for SNR remediation to complete")

				Expect(waitForSNRRemediationComplete(
					ctx, targetWorkerName, oldBootID, nhcparams.RemediationCompletionTimeout,
				)).To(Succeed(),
					"SNR remediation did not complete for %s", targetWorkerName)

				By("Waiting for node to become Ready")

				Expect(helpers.WaitForNodeReady(ctx, APIClient,
					targetWorkerName,
					nhcparams.DefaultPollInterval, nhcparams.NodeReadyTimeout,
					GinkgoWriter.Printf,
				)).To(Succeed())

				By("Waiting for NHC to return to Enabled phase")

				Expect(waitForNHCPhase(ctx, nhcparams.NHCTestName,
					nhcparams.NHCPhaseEnabled, nhcparams.RemediationCompletionTimeout)).To(Succeed(),
					"NHC did not return to Enabled after remediation")
			})

		It("should handle old default NHC CR name without crash",
			reportxml.ID("69711"),
			Label(labels.TierAcceptance, labels.PlatformAny, labels.ComponentRemediation),
			func() {
				By("Creating NHC CR with legacy name 'nhc-worker-default'")

				nhcWorker := buildNHCForWorkers(nhcparams.NHCOldDefaultName)
				Expect(APIClient.Create(ctx, nhcWorker)).To(Succeed(),
					"Failed to create NHC CR with old default name")

				By("Waiting for worker NHC to reach Enabled phase")

				Expect(waitForNHCPhase(ctx, nhcparams.NHCOldDefaultName,
					nhcparams.NHCPhaseEnabled, medik8sparams.DefaultTimeout)).To(Succeed())

				By("Creating NHC CR for control-plane nodes")

				nhcCP := buildNHC(nhcparams.NHCControlPlaneTestName,
					"node-role.kubernetes.io/control-plane", "Exists", nil)
				Expect(APIClient.Create(ctx, nhcCP)).To(Succeed(),
					"Failed to create control-plane NHC CR")

				By("Waiting for control-plane NHC to reach Enabled phase")

				Expect(waitForNHCPhase(ctx, nhcparams.NHCControlPlaneTestName,
					nhcparams.NHCPhaseEnabled, medik8sparams.DefaultTimeout)).To(Succeed())

				By("Finding the node hosting the active NHC controller")

				var err error
				nhcControllerNodeName, err = helpers.GetActiveControllerNode(
					ctx, APIClient, nhcparams.ControllerLeaseName, medik8sparams.OperatorNs)
				Expect(err).ToNot(HaveOccurred(),
					"Failed to find active NHC controller node")
				GinkgoWriter.Printf("Active NHC controller is on node: %s\n", nhcControllerNodeName)

				By("Verifying control-plane node is Ready before disruption")

				cpNode := &corev1.Node{}
				Expect(APIClient.Get(ctx, client.ObjectKey{Name: nhcControllerNodeName}, cpNode)).To(Succeed())
				Expect(helpers.IsNodeReady(cpNode)).To(BeTrue(),
					"Control-plane node %s is not Ready before test", nhcControllerNodeName)

				By(fmt.Sprintf("Stopping kubelet on worker %s", targetWorkerName))

				Expect(stopKubeletForRemediation(ctx, targetWorkerName)).To(Succeed())

				By("Verifying SNR CR is created for the worker (NHC triggered via legacy-named CR)")

				Eventually(func() (bool, error) {
					return snrCRExists(ctx, targetWorkerName)
				}, nhcparams.NodeNotReadyTimeout, nhcparams.DefaultPollInterval).Should(BeTrue(),
					"SNR CR should be created for %s via legacy-named NHC CR", targetWorkerName)

				GinkgoWriter.Println("SNR CR created -- NHC triggered remediation via legacy CR name")

				By(fmt.Sprintf("Stopping kubelet on NHC controller node %s", nhcControllerNodeName))

				Expect(stopKubeletForRemediation(ctx, nhcControllerNodeName)).To(Succeed())

				By("Waiting for NHC controller leader to fail over to another node")

				Eventually(func() (string, error) {
					return helpers.GetActiveControllerNode(
						ctx, APIClient, nhcparams.ControllerLeaseName, medik8sparams.OperatorNs)
				}, nhcparams.RemediationCompletionTimeout, nhcparams.DefaultPollInterval).ShouldNot(
					Equal(nhcControllerNodeName),
					"NHC controller leader should move to a different node after kubelet stop")

				newLeaderNode, _ := helpers.GetActiveControllerNode(
					ctx, APIClient, nhcparams.ControllerLeaseName, medik8sparams.OperatorNs)
				GinkgoWriter.Printf("NHC controller leader failed over: %s -> %s\n",
					nhcControllerNodeName, newLeaderNode)

				By("Verifying NHC controller has 2 ready replicas")

				Eventually(func() (int, error) {
					return countReadyControllerPods(ctx, nhcparams.OperatorControllerPodLabelSelector)
				}, nhcparams.RemediationCompletionTimeout, nhcparams.DefaultPollInterval).Should(
					BeNumerically(">=", 2),
					"NHC controller should have at least 2 ready replicas after failover")

				By("Waiting for all nodes to become Ready (both remediations)")

				Expect(helpers.WaitForNodeReady(ctx, APIClient,
					targetWorkerName,
					nhcparams.DefaultPollInterval, nhcparams.NodeReadyTimeout,
					GinkgoWriter.Printf,
				)).To(Succeed(), "Worker node did not become Ready")

				Expect(helpers.WaitForNodeReady(ctx, APIClient,
					nhcControllerNodeName,
					nhcparams.DefaultPollInterval, nhcparams.NodeReadyTimeout,
					GinkgoWriter.Printf,
				)).To(Succeed(), "Control-plane node did not become Ready")

				By("Verifying NHC controller is still running (not crashed)")

				nhcDeployment, err := deployment.Pull(
					APIClient, nhcparams.OperatorDeploymentName, medik8sparams.OperatorNs)
				Expect(err).ToNot(HaveOccurred())
				Expect(nhcDeployment.IsReady(medik8sparams.DefaultTimeout)).To(BeTrue(),
					"NHC deployment should be Ready after dual remediation with old default CR name")

				By("Verifying control-plane NHC phase is Enabled")

				Expect(waitForNHCPhase(ctx, nhcparams.NHCControlPlaneTestName,
					nhcparams.NHCPhaseEnabled, nhcparams.RemediationCompletionTimeout)).To(Succeed(),
					"Control-plane NHC did not return to Enabled after remediation")
			})

		It("should remediate only one CR at a time when multiple NHCs exist",
			reportxml.ID("66814"),
			Label(labels.TierAcceptance, labels.PlatformAny, labels.ComponentRemediation),
			func() {
				// Per Polarion OCP-66814: two NHC CRs with DIFFERENT remediators.
				// TestRemediation (10s duration) triggers first; SNR (30s) should
				// NOT create a remediation CR because the node is already being
				// remediated by TestRemediation.
				// SSH availability is checked in BeforeAll; this test uses SSH
				// to restart kubelet because TestRemediation has no controller.

				By("Setting up TestRemediation dummy CRDs and RBAC")

				setupTestRemediationResources(ctx)
				DeferCleanup(func() { cleanupTestRemediationResources(ctx) })

				By("Creating SNR-based NHC CR (30s unhealthy duration)")

				nhcSNR := buildNHCForWorkers(nhcparams.NHCTestName)
				nhcSpec(nhcSNR)["unhealthyConditions"] = []interface{}{
					map[string]interface{}{
						"type": "Ready", "status": "False", "duration": nhcparams.UnhealthyConditionDuration,
					},
					map[string]interface{}{
						"type": "Ready", "status": "Unknown", "duration": nhcparams.UnhealthyConditionDuration,
					},
				}

				Expect(APIClient.Create(ctx, nhcSNR)).To(Succeed())

				By("Creating TestRemediation-based NHC CR (10s unhealthy duration)")

				nhcTR := buildNHCWithTestRemediation(nhcparams.NHCSecondTestName)
				Expect(APIClient.Create(ctx, nhcTR)).To(Succeed())

				By("Waiting for both NHCs to reach Enabled phase")

				Expect(waitForNHCPhase(ctx, nhcparams.NHCTestName,
					nhcparams.NHCPhaseEnabled, medik8sparams.DefaultTimeout)).To(Succeed())
				Expect(waitForNHCPhase(ctx, nhcparams.NHCSecondTestName,
					nhcparams.NHCPhaseEnabled, medik8sparams.DefaultTimeout)).To(Succeed())

				By(fmt.Sprintf("Stopping kubelet on %s", targetWorkerName))

				Expect(stopKubeletForRemediation(ctx, targetWorkerName)).To(Succeed())

				By("Waiting for TestRemediation CR to be created (10s NHC triggers first)")

				Eventually(func() (bool, error) {
					return testRemediationCRExists(ctx, targetWorkerName)
				}, nhcparams.NodeNotReadyTimeout, nhcparams.DefaultPollInterval).Should(BeTrue(),
					"TestRemediation CR should be created for %s", targetWorkerName)

				GinkgoWriter.Println("TestRemediation CR created -- 10s NHC triggered")

				By("Verifying SNR CR was NOT created (node already being remediated)")

				Consistently(func() (bool, error) {
					return snrCRExists(ctx, targetWorkerName)
				}, nhcparams.NegativeAssertionHoldDuration, nhcparams.DefaultPollInterval).Should(BeFalse(),
					"SNR CR should NOT be created while TestRemediation is active for %s", targetWorkerName)

				GinkgoWriter.Println("SNR CR not created -- one-at-a-time constraint verified")

				// TestRemediation has no controller, so the node stays unhealthy.
				// Restart kubelet via SSH to recover (oc debug can't schedule
				// pods when kubelet is stopped).
				By("Starting kubelet via SSH to recover node")

				Expect(startKubeletForRemediation(ctx, targetWorkerName)).To(Succeed(),
					"Failed to start kubelet on %s", targetWorkerName)

				By("Waiting for TestRemediation NHC to return to Enabled")

				Expect(waitForNHCPhase(ctx, nhcparams.NHCSecondTestName,
					nhcparams.NHCPhaseEnabled, nhcparams.RemediationCompletionTimeout)).To(Succeed(),
					"TestRemediation NHC did not return to Enabled after kubelet restart")

				By("Waiting for node to become Ready")

				Expect(helpers.WaitForNodeReady(ctx, APIClient,
					targetWorkerName,
					nhcparams.DefaultPollInterval, nhcparams.NodeReadyTimeout,
					GinkgoWriter.Printf,
				)).To(Succeed())
			})

		It("should allow deletion of non-remediating NHC while another is remediating",
			reportxml.ID("71171"),
			Label(labels.TierAcceptance, labels.PlatformAny, labels.ComponentRemediation),
			func() {
				// Per OCP-71171 / Python test_nhc_cr_deletion: two SNR-based NHC CRs
				// with different unhealthy durations. The faster one (10s) triggers
				// first via SNR; the slower one (11s) should NOT start remediating.
				// Deleting the non-remediating NHC should succeed.
				// Recovery is automatic: SNR reboots the node.

				By("Creating first SNR-based NHC CR (10s, triggers first)")

				nhcFirst := buildNHCForWorkers(nhcparams.NHCSecondTestName)
				nhcSpec(nhcFirst)["unhealthyConditions"] = []interface{}{
					map[string]interface{}{
						"type": "Ready", "status": "False", "duration": "10s",
					},
					map[string]interface{}{
						"type": "Ready", "status": "Unknown", "duration": "10s",
					},
				}

				Expect(APIClient.Create(ctx, nhcFirst)).To(Succeed())

				By("Creating second SNR-based NHC CR (11s, triggers slower)")

				nhcSecond := buildNHCForWorkers(nhcparams.NHCTestName)
				nhcSpec(nhcSecond)["unhealthyConditions"] = []interface{}{
					map[string]interface{}{
						"type": "Ready", "status": "False", "duration": "11s",
					},
					map[string]interface{}{
						"type": "Ready", "status": "Unknown", "duration": "11s",
					},
				}

				Expect(APIClient.Create(ctx, nhcSecond)).To(Succeed())

				By("Waiting for both NHCs to reach Enabled")

				Expect(waitForNHCPhase(ctx, nhcparams.NHCSecondTestName,
					nhcparams.NHCPhaseEnabled, medik8sparams.DefaultTimeout)).To(Succeed())
				Expect(waitForNHCPhase(ctx, nhcparams.NHCTestName,
					nhcparams.NHCPhaseEnabled, medik8sparams.DefaultTimeout)).To(Succeed())

				By(fmt.Sprintf("Stopping kubelet on %s", targetWorkerName))

				Expect(stopKubeletForRemediation(ctx, targetWorkerName)).To(Succeed())

				By("Waiting for first NHC to enter Remediating phase")

				Expect(waitForNHCPhase(ctx, nhcparams.NHCSecondTestName,
					nhcparams.NHCPhaseRemediating, nhcparams.NodeNotReadyTimeout)).To(Succeed(),
					"First NHC did not enter Remediating phase")

				By("Verifying second NHC stays non-Remediating (Consistently)")

				Consistently(func() (string, error) {
					return getNHCPhase(ctx, nhcparams.NHCTestName)
				}, nhcparams.NegativeAssertionHoldDuration, nhcparams.DefaultPollInterval).ShouldNot(
					Equal(nhcparams.NHCPhaseRemediating),
					"Second NHC should NOT enter Remediating while first NHC is active")

				GinkgoWriter.Println("Second NHC stayed non-Remediating -- one-at-a-time verified")

				By("Deleting second NHC (non-remediating) -- should succeed")

				nhcToDelete := &unstructured.Unstructured{}
				nhcToDelete.SetGroupVersionKind(nhcGVK)
				nhcToDelete.SetName(nhcparams.NHCTestName)
				Expect(APIClient.Delete(ctx, nhcToDelete)).To(Succeed(),
					"Deletion of non-remediating NHC should succeed")

				By("Waiting for first NHC to return to Enabled (SNR reboots node)")

				Expect(waitForNHCPhase(ctx, nhcparams.NHCSecondTestName,
					nhcparams.NHCPhaseEnabled, nhcparams.RemediationCompletionTimeout)).To(Succeed(),
					"First NHC did not return to Enabled after SNR remediation")

				By("Waiting for node to become Ready")

				Expect(helpers.WaitForNodeReady(ctx, APIClient,
					targetWorkerName,
					nhcparams.DefaultPollInterval, nhcparams.NodeReadyTimeout,
					GinkgoWriter.Printf,
				)).To(Succeed())
			})
	})

// Separate Describe for non-destructive NHC tests that don't disrupt nodes.
var _ = Describe("NHC Functional -- Selector and CR Management",
	Serial, Ordered,
	Label(labels.OperatorNHC, nhcparams.Label,
		labels.DisruptionNonDestructive, labels.FrequencyWeekly),
	func() {
		var ctx context.Context

		BeforeAll(func() {
			ctx = context.Background()

			By("Checking SNR CRD is installed")

			if !isSNRCRDInstalled(ctx) {
				Skip("SelfNodeRemediation CRD not found; SNR operator not installed")
			}

			By("Verifying NHC operator deployment is ready")

			nhcDeployment, err := deployment.Pull(
				APIClient, nhcparams.OperatorDeploymentName, medik8sparams.OperatorNs)
			Expect(err).ToNot(HaveOccurred())
			Expect(nhcDeployment.IsReady(medik8sparams.DefaultTimeout)).To(BeTrue(),
				"NHC deployment is not Ready")
		})

		AfterEach(func() {
			cleanupNHCCR(ctx, nhcparams.NHCTestName)
		})

		It("should update observed nodes when selector is edited",
			reportxml.ID("56938"),
			Label(labels.TierAcceptance, labels.PlatformAny, labels.ComponentRemediation),
			func() {
				By("Creating NHC CR for workers")

				nhcCR := buildNHCForWorkers(nhcparams.NHCTestName)
				Expect(APIClient.Create(ctx, nhcCR)).To(Succeed(),
					"Failed to create NHC CR")

				By("Waiting for NHC to reach Enabled phase")

				Expect(waitForNHCPhase(ctx, nhcparams.NHCTestName,
					nhcparams.NHCPhaseEnabled, medik8sparams.DefaultTimeout)).To(Succeed(),
					"NHC did not reach Enabled phase")

				By("Verifying observed nodes > 0")

				Eventually(func() (int64, error) {
					return getNHCObservedNodes(ctx, nhcparams.NHCTestName)
				}, medik8sparams.DefaultTimeout, nhcparams.DefaultPollInterval).Should(
					BeNumerically(">", int64(0)),
					"NHC should observe worker nodes")

				By("Editing NHC selector to non-existent key via merge patch")

				patchBytes := []byte(`{"spec":{"selector":{"matchExpressions":[{"key":"doesNotExist","operator":"Exists"}]}}}`)
				target := &unstructured.Unstructured{}
				target.SetGroupVersionKind(nhcGVK)
				target.SetName(nhcparams.NHCTestName)

				Expect(APIClient.Patch(ctx, target,
					client.RawPatch(types.MergePatchType, patchBytes),
				)).To(Succeed(), "Failed to patch NHC selector")

				By("Verifying observed nodes dropped to 0")

				Eventually(func() (int64, error) {
					return getNHCObservedNodes(ctx, nhcparams.NHCTestName)
				}, medik8sparams.DefaultTimeout, nhcparams.DefaultPollInterval).Should(
					Equal(int64(0)),
					"NHC should observe 0 nodes after selector change")

				By("Verifying NHC is still Enabled (not crashed)")

				phase, err := getNHCPhase(ctx, nhcparams.NHCTestName)
				Expect(err).ToNot(HaveOccurred())
				Expect(phase).To(Equal(nhcparams.NHCPhaseEnabled),
					"NHC should remain Enabled after selector edit")

				By("Verifying invalid selector operator value is rejected by webhook")

				invalidOpPatch := []byte(`{"spec":{"selector":{"matchExpressions":[{"key":"node-role.kubernetes.io/worker","operator":"doesNotExist"}]}}}`)
				invalidTarget := &unstructured.Unstructured{}
				invalidTarget.SetGroupVersionKind(nhcGVK)
				invalidTarget.SetName(nhcparams.NHCTestName)

				invalidOpErr := APIClient.Patch(ctx, invalidTarget,
					client.RawPatch(types.MergePatchType, invalidOpPatch),
				)
				Expect(invalidOpErr).To(MatchError(ContainSubstring("is not a valid")),
					"Editing NHC with invalid selector operator should be rejected")
				GinkgoWriter.Printf("Invalid operator edit rejected (expected): %v\n", invalidOpErr)

				By("Verifying empty selector is rejected by webhook")

				emptySelectorPatch := []byte(`{"spec":{"selector":{"matchExpressions":[]}}}`)
				emptyTarget := &unstructured.Unstructured{}
				emptyTarget.SetGroupVersionKind(nhcGVK)
				emptyTarget.SetName(nhcparams.NHCTestName)

				emptyErr := APIClient.Patch(ctx, emptyTarget,
					client.RawPatch(types.MergePatchType, emptySelectorPatch),
				)
				Expect(emptyErr).To(MatchError(ContainSubstring("Selector is mandatory")),
					"Editing NHC with empty selector should be rejected")
				GinkgoWriter.Printf("Empty selector edit rejected (expected): %v\n", emptyErr)

				By("Verifying NHC state unchanged after rejected edits")

				phase, err = getNHCPhase(ctx, nhcparams.NHCTestName)
				Expect(err).ToNot(HaveOccurred())
				Expect(phase).To(Equal(nhcparams.NHCPhaseEnabled),
					"NHC should remain Enabled after rejected edits")

				observedNodes, err := getNHCObservedNodes(ctx, nhcparams.NHCTestName)
				Expect(err).ToNot(HaveOccurred())
				Expect(observedNodes).To(Equal(int64(0)),
					"Observed nodes should still be 0 (doesNotExist key selector)")
			})
	})
