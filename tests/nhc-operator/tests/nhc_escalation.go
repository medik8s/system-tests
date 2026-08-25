package tests

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/reportxml"

	"github.com/medik8s/system-tests/tests/internal/helpers"
	"github.com/medik8s/system-tests/tests/internal/labels"
	. "github.com/medik8s/system-tests/tests/internal/medik8sinittools"
	"github.com/medik8s/system-tests/tests/nhc-operator/internal/nhcparams"
)

// minWorkersForEscalationTests is the minimum number of Ready workers
// needed for destructive escalation tests (target + at least 1 survivor).
const minWorkersForEscalationTests = 2

// escalationEnableTimeout is the max time to retry SSH reconnection
// after a node reboot with kubelet disabled.
const escalationEnableTimeout = 5 * time.Minute

func recoverEscalationNode(ctx context.Context, nodeName string) error {
	if err := helpers.EnableKubeletSSH(ctx, APIClient, nodeName,
		escalationEnableTimeout, GinkgoWriter.Printf); err != nil {
		return fmt.Errorf("EnableKubeletSSH on %s: %w", nodeName, err)
	}

	return helpers.WaitForNodeReady(ctx, APIClient, nodeName,
		nhcparams.DestructivePollInterval, nhcparams.NodeReadyTimeout,
		GinkgoWriter.Printf)
}

func deferEscalationNodeRecovery(ctx context.Context, nodeName string) {
	DeferCleanup(func() {
		GinkgoWriter.Printf("Safety-net cleanup: re-enabling kubelet on %s\n", nodeName)

		if err := helpers.EnableKubeletSSH(ctx, APIClient, nodeName,
			escalationEnableTimeout, GinkgoWriter.Printf); err != nil {
			GinkgoWriter.Printf("WARNING: EnableKubeletSSH failed for %s: %v\n", nodeName, err)
			AddReportEntry("escalation-cleanup-failed",
				fmt.Sprintf("node %s: %v", nodeName, err))
		}

		if err := helpers.WaitForNodeReady(ctx, APIClient, nodeName,
			nhcparams.DestructivePollInterval, nhcparams.NodeReadyTimeout, GinkgoWriter.Printf); err != nil {
			GinkgoWriter.Printf("WARNING: WaitForNodeReady failed for %s: %v\n", nodeName, err)
			AddReportEntry("escalation-cleanup-failed",
				fmt.Sprintf("node %s did not recover: %v", nodeName, err))
		}
	})
}

func deferKubeletStartRecovery(ctx context.Context, nodeName string) {
	DeferCleanup(func() {
		if err := startKubeletForRemediation(ctx, nodeName); err != nil {
			GinkgoWriter.Printf("WARNING: startKubelet failed for %s: %v\n", nodeName, err)
			AddReportEntry("escalation-cleanup-failed",
				fmt.Sprintf("node %s: %v", nodeName, err))
		}

		if err := helpers.WaitForNodeReady(ctx, APIClient, nodeName,
			nhcparams.DestructivePollInterval, nhcparams.NodeReadyTimeout, GinkgoWriter.Printf); err != nil {
			GinkgoWriter.Printf("WARNING: WaitForNodeReady failed for %s: %v\n", nodeName, err)
			AddReportEntry("escalation-cleanup-failed",
				fmt.Sprintf("node %s did not recover: %v", nodeName, err))
		}
	})
}

var _ = Describe("NHC Escalation -- Functional E2E",
	Serial, Ordered, ContinueOnFailure,
	Label(labels.OperatorNHC, nhcparams.Label,
		labels.DisruptionDestructive, labels.FrequencyWeekly),
	func() {
		var (
			ctx              context.Context
			targetWorkerName string
		)

		BeforeAll(func() {
			ctx = context.Background()

			By("Checking SSH access is available")

			if !isSSHAvailable() {
				Skip("SSH not available -- NHC escalation E2E tests require SSH access")
			}

			By("Checking SNR CRD is installed")

			if !isSNRCRDInstalled(ctx) {
				Skip("SelfNodeRemediation CRD not found -- skipping NHC escalation E2E tests")
			}

			By("Verifying NHC operator deployment is ready")

			verifyNHCDeploymentReady()

			By("Verifying minimum worker count")

			workerCount, err := helpers.CountReadyWorkerNodes(ctx, APIClient)
			Expect(err).ToNot(HaveOccurred())
			Expect(workerCount).To(BeNumerically(">=", minWorkersForEscalationTests),
				"NHC escalation E2E tests require at least %d Ready workers", minWorkersForEscalationTests)

			By("Pre-cleaning stale NHC CRs from previous interrupted runs")

			for _, suffix := range []string{"-basic", "-stops", "-timeout"} {
				cleanupNHCCR(nhcparams.NHCEscalationTestName + suffix)
			}

			By("Selecting target worker node")

			targetNode, nodeErr := helpers.SelectWorkerNode(ctx, APIClient)
			Expect(nodeErr).ToNot(HaveOccurred(), "Failed to select worker node")

			targetWorkerName = targetNode.Name
			GinkgoWriter.Printf("Target worker node for escalation E2E: %s\n", targetWorkerName)

			By("Setting up TestRemediation CRDs and RBAC")

			setupTestRemediationResources()
		})

		AfterAll(func() {
			cleanupTestRemediationResources()
		})

		BeforeEach(func() {
			By("Verifying NHC deployment is ready before each test")

			verifyNHCDeploymentReady()

			By("Verifying target worker is Ready before test")

			Expect(helpers.WaitForNodeReady(ctx, APIClient, targetWorkerName,
				nhcparams.DefaultPollInterval, nhcparams.NodeReadyTimeout,
				GinkgoWriter.Printf)).To(Succeed(),
				"Target worker %s should be Ready before test", targetWorkerName)
		})

		JustAfterEach(func() {
			if CurrentSpecReport().Failed() {
				logNHCControllerState()
			}

			cleanupTestRemediationCR(targetWorkerName)
			cleanupSNRCR(targetWorkerName)
		})

		It("Escalates from TestRemediation to SNR when first remediator times out",
			reportxml.ID("60857"),
			Label(labels.TierResiliency, labels.PlatformAny,
				labels.ComponentRemediation), func() {
				nhcName := nhcparams.NHCEscalationTestName + "-basic"

				By("Recording initial boot ID")

				oldBootID, err := helpers.GetNodeBootIDFromAPI(ctx, APIClient, targetWorkerName)
				Expect(err).ToNot(HaveOccurred(), "Failed to get boot ID for %s", targetWorkerName)

				By("Creating NHC with escalation: TestRemediation (order=0, timeout=60s) then SNR (order=1, timeout=180s)")

				nhc := buildNHCWithEscalation(nhcName, []escalationStep{
					testRemediationEscalationStep(0, nhcparams.EscalationFirstStepTimeout),
					snrEscalationStep(1, nhcparams.EscalationSNRStepTimeout),
				})

				Expect(APIClient.Create(ctx, nhc)).To(Succeed(), "Failed to create NHC with escalation")
				DeferCleanup(func() { cleanupNHCCR(nhcName) })

				By(fmt.Sprintf("Disabling kubelet on %s (persistent across reboot)", targetWorkerName))

				Expect(helpers.DisableKubeletSSH(ctx, APIClient, targetWorkerName,
					nhcparams.SSHTimeout)).To(Succeed())
				deferEscalationNodeRecovery(ctx, targetWorkerName)

				By("Waiting for NHC to enter Remediating phase")

				Expect(waitForNHCPhase(ctx, nhcName, nhcparams.NHCPhaseRemediating,
					nhcparams.NodeNotReadyTimeout)).To(Succeed())

				By("Verifying TestRemediation CR is created first (order=0)")

				Eventually(func(assertion Gomega) {
					exists, checkErr := testRemediationCRExists(ctx, targetWorkerName)
					assertion.Expect(checkErr).ToNot(HaveOccurred())
					assertion.Expect(exists).To(BeTrue())
				}).WithPolling(nhcparams.DefaultPollInterval).
					WithTimeout(nhcparams.NodeNotReadyTimeout).Should(Succeed(),
					"TestRemediation CR should be created for %s", targetWorkerName)

				By("Verifying SNR CR does NOT exist yet (TestRemediation has not timed out)")

				Consistently(func(assertion Gomega) {
					snrExists, snrErr := snrCRExists(ctx, targetWorkerName)
					assertion.Expect(snrErr).ToNot(HaveOccurred())
					assertion.Expect(snrExists).To(BeFalse(),
						"SNR CR should not exist before TestRemediation timeout")
				}).WithPolling(nhcparams.DefaultPollInterval).
					WithTimeout(nhcparams.NegativeAssertionHoldDuration).Should(Succeed())

				By("Waiting for TestRemediation timeout and escalation to SNR (order=1)")

				// Total wait: 60s TestRemediation timeout + NHC reconcile interval + buffer
				Eventually(func(assertion Gomega) {
					exists, checkErr := snrCRExists(ctx, targetWorkerName)
					assertion.Expect(checkErr).ToNot(HaveOccurred())
					assertion.Expect(exists).To(BeTrue())
				}).WithPolling(nhcparams.DestructivePollInterval).
					WithTimeout(nhcparams.EscalationWaitTimeout).Should(Succeed(),
					"SNR CR should be created after TestRemediation timeout")

				By("Verifying TestRemediation CR still exists (both CRs coexist)")

				trExists, trErr := testRemediationCRExists(ctx, targetWorkerName)
				Expect(trErr).ToNot(HaveOccurred())
				Expect(trExists).To(BeTrue(),
					"TestRemediation CR should still exist alongside SNR CR")

				By("Verifying node was rebooted by SNR (boot ID changed)")

				Eventually(func(assertion Gomega) {
					currentBootID, bootErr := helpers.GetNodeBootIDFromAPI(ctx, APIClient, targetWorkerName)
					assertion.Expect(bootErr).ToNot(HaveOccurred())
					assertion.Expect(currentBootID).ToNot(Equal(oldBootID),
						"Boot ID should change after SNR reboots the node")
				}).WithPolling(nhcparams.DestructivePollInterval).
					WithTimeout(nhcparams.RemediationCompletionTimeout).Should(Succeed())

				By(fmt.Sprintf("Re-enabling kubelet on %s and waiting for node recovery", targetWorkerName))

				Expect(recoverEscalationNode(ctx, targetWorkerName)).To(Succeed(),
					"Failed to recover node %s", targetWorkerName)

				By("Waiting for NHC to return to Enabled and clean up both CRs")

				Expect(waitForNHCPhase(ctx, nhcName, nhcparams.NHCPhaseEnabled,
					nhcparams.RemediationCompletionTimeout)).To(Succeed())

				Eventually(func(assertion Gomega) {
					trGone, trCheckErr := testRemediationCRExists(ctx, targetWorkerName)
					assertion.Expect(trCheckErr).ToNot(HaveOccurred())
					assertion.Expect(trGone).To(BeFalse(), "TestRemediation CR should be cleaned up")

					snrGone, snrCheckErr := snrCRExists(ctx, targetWorkerName)
					assertion.Expect(snrCheckErr).ToNot(HaveOccurred())
					assertion.Expect(snrGone).To(BeFalse(), "SNR CR should be cleaned up")
				}).WithPolling(nhcparams.DefaultPollInterval).
					WithTimeout(nhcparams.RemediationCompletionTimeout).Should(Succeed())
			})

		It("Does not escalate when first remediator restores node health",
			reportxml.ID("60858"),
			Label(labels.TierResiliency, labels.PlatformAny,
				labels.ComponentRemediation), func() {
				nhcName := nhcparams.NHCEscalationTestName + "-stops"

				By("Creating NHC with escalation: SNR first (order=0), TestRemediation second (order=1, timeout=600s)")

				nhc := buildNHCWithEscalation(nhcName, []escalationStep{
					snrEscalationStep(0, nhcparams.EscalationSNRStepTimeout),
					testRemediationEscalationStep(1, nhcparams.EscalationLongTimeout),
				})

				Expect(APIClient.Create(ctx, nhc)).To(Succeed())
				DeferCleanup(func() { cleanupNHCCR(nhcName) })

				By("Recording initial boot ID")

				oldBootID, err := helpers.GetNodeBootIDFromAPI(ctx, APIClient, targetWorkerName)
				Expect(err).ToNot(HaveOccurred())

				By(fmt.Sprintf("Stopping kubelet on %s (recoverable after reboot)", targetWorkerName))

				Expect(stopKubeletForRemediation(ctx, targetWorkerName)).To(Succeed())
				deferKubeletStartRecovery(ctx, targetWorkerName)

				By("Waiting for NHC to enter Remediating phase")

				Expect(waitForNHCPhase(ctx, nhcName, nhcparams.NHCPhaseRemediating,
					nhcparams.NodeNotReadyTimeout)).To(Succeed())

				By("Waiting for SNR to reboot the node (boot ID changes)")

				Expect(waitForSNRRemediationComplete(
					ctx, targetWorkerName, oldBootID,
					nhcparams.RemediationCompletionTimeout)).To(Succeed(),
					"SNR should reboot the node")

				By("Waiting for node to become Ready (kubelet restarts on boot)")

				Expect(helpers.WaitForNodeReady(ctx, APIClient, targetWorkerName,
					nhcparams.DestructivePollInterval, nhcparams.NodeReadyTimeout,
					GinkgoWriter.Printf)).To(Succeed())

				By("Verifying TestRemediation CR was NEVER created (no escalation)")

				// Hold the assertion for NegativeAssertionHoldDuration to confirm
				// escalation does not occur even with some processing delay.
				Consistently(func(assertion Gomega) {
					exists, checkErr := testRemediationCRExists(ctx, targetWorkerName)
					assertion.Expect(checkErr).ToNot(HaveOccurred())
					assertion.Expect(exists).To(BeFalse(),
						"TestRemediation CR should never be created when SNR succeeds")
				}).WithPolling(nhcparams.DefaultPollInterval).
					WithTimeout(nhcparams.NegativeAssertionHoldDuration).Should(Succeed())

				By("Waiting for NHC to return to Enabled phase")

				Expect(waitForNHCPhase(ctx, nhcName, nhcparams.NHCPhaseEnabled,
					nhcparams.RemediationCompletionTimeout)).To(Succeed())
			})

		It("Escalates after SNR timeout when node remains unhealthy",
			reportxml.ID("66806"),
			Label(labels.TierResiliency, labels.PlatformAny,
				labels.ComponentRemediation), func() {
				nhcName := nhcparams.NHCEscalationTestName + "-timeout"

				By("Creating NHC with escalation: SNR first (order=0, timeout=60s), TestRemediation second (order=1)")

				nhc := buildNHCWithEscalation(nhcName, []escalationStep{
					snrEscalationStep(0, nhcparams.EscalationFirstStepTimeout),
					testRemediationEscalationStep(1, nhcparams.EscalationLongTimeout),
				})

				Expect(APIClient.Create(ctx, nhc)).To(Succeed())
				DeferCleanup(func() { cleanupNHCCR(nhcName) })

				By(fmt.Sprintf("Disabling kubelet on %s (persistent across reboot)", targetWorkerName))

				Expect(helpers.DisableKubeletSSH(ctx, APIClient, targetWorkerName,
					nhcparams.SSHTimeout)).To(Succeed())
				deferEscalationNodeRecovery(ctx, targetWorkerName)

				By("Waiting for NHC to enter Remediating phase")

				Expect(waitForNHCPhase(ctx, nhcName, nhcparams.NHCPhaseRemediating,
					nhcparams.NodeNotReadyTimeout)).To(Succeed())

				By("Verifying SNR CR is created (first remediator)")

				Eventually(func(assertion Gomega) {
					exists, checkErr := snrCRExists(ctx, targetWorkerName)
					assertion.Expect(checkErr).ToNot(HaveOccurred())
					assertion.Expect(exists).To(BeTrue())
				}).WithPolling(nhcparams.DefaultPollInterval).
					WithTimeout(nhcparams.NodeNotReadyTimeout).Should(Succeed(),
					"SNR CR should be created for %s", targetWorkerName)

				By("Waiting for escalation to TestRemediation after SNR timeout")

				// SNR reboots the node but kubelet is disabled, so node stays unhealthy.
				// After the 60s SNR timeout, NHC escalates to TestRemediation.
				// Total wait: SNR reboot time + 60s timeout + NHC reconcile + buffer.
				Eventually(func(assertion Gomega) {
					exists, checkErr := testRemediationCRExists(ctx, targetWorkerName)
					assertion.Expect(checkErr).ToNot(HaveOccurred())
					assertion.Expect(exists).To(BeTrue())
				}).WithPolling(nhcparams.DestructivePollInterval).
					WithTimeout(nhcparams.RemediationCompletionTimeout).Should(Succeed(),
					"TestRemediation CR should be created after SNR timeout")

				By("Verifying both SNR and TestRemediation CRs coexist")

				snrStillExists, snrErr := snrCRExists(ctx, targetWorkerName)
				Expect(snrErr).ToNot(HaveOccurred())
				Expect(snrStillExists).To(BeTrue(), "SNR CR should still exist alongside TestRemediation")

				By(fmt.Sprintf("Re-enabling kubelet on %s and waiting for node recovery", targetWorkerName))

				Expect(recoverEscalationNode(ctx, targetWorkerName)).To(Succeed(),
					"Failed to recover node %s", targetWorkerName)

				By("Waiting for NHC to clean up both remediation CRs")

				Expect(waitForNHCPhase(ctx, nhcName, nhcparams.NHCPhaseEnabled,
					nhcparams.RemediationCompletionTimeout)).To(Succeed())

				Eventually(func(assertion Gomega) {
					trGone, trCheckErr := testRemediationCRExists(ctx, targetWorkerName)
					assertion.Expect(trCheckErr).ToNot(HaveOccurred())
					assertion.Expect(trGone).To(BeFalse(), "TestRemediation CR should be cleaned up")

					snrGone, snrCheckErr := snrCRExists(ctx, targetWorkerName)
					assertion.Expect(snrCheckErr).ToNot(HaveOccurred())
					assertion.Expect(snrGone).To(BeFalse(), "SNR CR should be cleaned up")
				}).WithPolling(nhcparams.DefaultPollInterval).
					WithTimeout(nhcparams.RemediationCompletionTimeout).Should(Succeed())
			})
	})
