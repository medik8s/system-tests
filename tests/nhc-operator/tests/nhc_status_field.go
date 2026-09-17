package tests

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/reportxml"

	"github.com/medik8s/system-tests/tests/internal/helpers"
	"github.com/medik8s/system-tests/tests/internal/labels"
	. "github.com/medik8s/system-tests/tests/internal/medik8sinittools"
	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
	"github.com/medik8s/system-tests/tests/nhc-operator/internal/nhcparams"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("NHC Status Field Tracking",
	Serial, Ordered, ContinueOnFailure,
	Label(labels.OperatorNHC, nhcparams.Label,
		labels.DisruptionDestructive, labels.FrequencyWeekly),
	func() {
		var (
			ctx              context.Context
			targetWorkerName string
			oldBootID        string
		)

		BeforeAll(func() {
			ctx = context.Background()

			By("Checking SSH access is available")

			if !isSSHAvailable() {
				Skip("SSH not available -- status field test requires SSH to stop kubelet")
			}

			By("Checking SNR CRD is installed")

			if !isSNRCRDInstalled(ctx) {
				Skip("SelfNodeRemediation CRD not found -- status field test requires SNR")
			}

			By("Verifying NHC controller deployment is ready")

			verifyNHCDeploymentReady()

			By("Verifying at least 2 Ready worker nodes")

			workerCount, err := helpers.CountReadyWorkerNodes(ctx, APIClient)
			Expect(err).ToNot(HaveOccurred())
			Expect(workerCount).To(BeNumerically(">=", 2),
				"Status field test requires at least 2 Ready worker nodes")

			By("Selecting target worker node")

			targetNode, err := helpers.SelectWorkerNode(ctx, APIClient)
			Expect(err).ToNot(HaveOccurred(), "Failed to select worker node")

			targetWorkerName = targetNode.Name
			GinkgoWriter.Printf("Target worker node: %s\n", targetWorkerName)

			By("Pre-cleaning stale CRs")

			cleanupNHCCR(ctx, nhcparams.NHCStatusTestName)
		})

		BeforeEach(func() {
			By("Verifying NHC controller deployment is ready")

			verifyNHCDeploymentReady()

			By("Verifying target node is Ready")

			Eventually(func(g Gomega) {
				node := &corev1.Node{}
				g.Expect(APIClient.Get(ctx, client.ObjectKey{Name: targetWorkerName}, node)).To(Succeed())
				g.Expect(helpers.IsNodeReady(node)).To(BeTrue(),
					"Target node %s is not Ready before test", targetWorkerName)
			}).WithPolling(nhcparams.DefaultPollInterval).
				WithTimeout(nhcparams.NodeReadyTimeout).Should(Succeed())

			By("Recording boot ID")

			var err error

			oldBootID, err = helpers.GetNodeBootIDFromAPI(ctx, APIClient, targetWorkerName)
			Expect(err).ToNot(HaveOccurred(),
				"Must read boot ID from node %s", targetWorkerName)

			By("Pre-cleaning stale CRs and confirming gone")

			cleanupNHCCR(ctx, nhcparams.NHCStatusTestName)
			waitForNHCGone(ctx, nhcparams.NHCStatusTestName)
			cleanupSNRCR(ctx, targetWorkerName)

			GinkgoWriter.Printf("Pre-remediation boot ID: %s\n", oldBootID)
		})

		JustAfterEach(func() {
			if CurrentSpecReport().Failed() {
				logNHCControllerState()
			}

			cleanupNHCCR(ctx, nhcparams.NHCStatusTestName)
			cleanupSNRCR(ctx, targetWorkerName)

			if isSSHAvailable() {
				if sshErr := startKubeletForRemediation(ctx, targetWorkerName); sshErr != nil {
					GinkgoWriter.Printf(
						"WARNING: SSH kubelet restart failed for %s: %v\n",
						targetWorkerName, sshErr)
					AddReportEntry("ssh-kubelet-restart-failed",
						fmt.Sprintf("node %s: %v", targetWorkerName, sshErr))
				}
			}

			By("Safety net: waiting for node " + targetWorkerName + " to become Ready")

			if err := helpers.WaitForNodeReady(ctx, APIClient,
				targetWorkerName,
				nhcparams.DefaultPollInterval, nhcparams.NodeReadyTimeout,
				GinkgoWriter.Printf,
			); err != nil {
				GinkgoWriter.Printf(
					"WARNING: node %s did not become Ready within %s: %v\n",
					targetWorkerName, nhcparams.NodeReadyTimeout, err)
				AddReportEntry("safety-net-recovery-failed",
					fmt.Sprintf("node %s did not recover: %v", targetWorkerName, err))
			}
		})

		It("Verifying NHC status phase and reason transitions during remediation",
			reportxml.ID("53093"),
			Label(labels.TierAcceptance, labels.PlatformAny,
				labels.ComponentRemediation), func() {
				nhcName := nhcparams.NHCStatusTestName

				By("Creating NHC CR for workers")

				nhc := buildNHCForWorkers(nhcName)
				Expect(APIClient.Create(ctx, nhc)).To(Succeed(),
					"Failed to create NHC CR %q", nhcName)

				By("Verifying pre-remediation status: phase=Enabled, reason contains 'no ongoing remediation'")

				verifyNHCPhaseAndReason(ctx, nhcName,
					nhcparams.NHCPhaseEnabled, nhcparams.NHCReasonEnabled,
					medik8sparams.DefaultTimeout)

				By("Stopping kubelet on target node to trigger remediation")

				Expect(stopKubeletForRemediation(ctx, targetWorkerName)).To(Succeed(),
					"Failed to stop kubelet on %s", targetWorkerName)

				By("Verifying during-remediation status: phase=Remediating, reason contains 'remediating'")

				verifyNHCPhaseAndReason(ctx, nhcName,
					nhcparams.NHCPhaseRemediating, nhcparams.NHCReasonRemediating,
					nhcparams.NodeNotReadyTimeout)

				By("Waiting for SNR remediation to complete (node reboot)")

				Expect(waitForSNRRemediationComplete(ctx, targetWorkerName, oldBootID)).To(Succeed(),
					"SNR remediation should complete for node %s", targetWorkerName)

				By("Verifying post-recovery status: phase=Enabled, reason back to 'no ongoing remediation'")

				verifyNHCPhaseAndReason(ctx, nhcName,
					nhcparams.NHCPhaseEnabled, nhcparams.NHCReasonEnabled,
					nhcparams.RemediationCompletionTimeout)
			})
	})
