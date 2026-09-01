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
	"github.com/medik8s/system-tests/tests/snr-operator/internal/snrparams"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("SNR Functional - Worker Remediation",
	Serial, Ordered, ContinueOnFailure,
	Label(labels.OperatorSNR, snrparams.Label,
		labels.DisruptionDestructive, labels.FrequencyNightly),
	func() {
		var (
			ctx              context.Context
			targetWorkerName string
			oldBootID        string
			creationTS       metav1.Time
			currentNHCName   string
			currentSNRTName  string
		)

		BeforeAll(func() {
			ctx = context.Background()

			By("Checking NHC CRD is installed")

			if !isNHCCRDInstalled() {
				Skip("NodeHealthCheck CRD not found; NHC operator not installed -- skipping worker remediation tests")
			}

			By("Verifying at least 2 Ready worker nodes")

			// 2 workers minimum: target (kubelet stopped, rebooted) + at least
			// 1 surviving worker so the cluster remains schedulable and pods
			// can be evicted to a healthy node (OCP-50772, OCP-61594).
			workerCount, err := helpers.CountReadyWorkerNodes(ctx, APIClient)
			Expect(err).ToNot(HaveOccurred())
			Expect(workerCount).To(BeNumerically(">=", 2),
				"Worker remediation tests require at least 2 Ready worker nodes")

			By("Selecting target worker node")

			targetNode, err := helpers.SelectWorkerNode(ctx, APIClient)
			Expect(err).ToNot(HaveOccurred(), "Failed to select worker node")

			targetWorkerName = targetNode.Name
			GinkgoWriter.Printf("Target worker node: %s\n", targetWorkerName)
		})

		BeforeEach(func() {
			By("Removing any stale kubelet stop guard from a previous run")

			Expect(helpers.RemoveKubeletStopGuard(ctx, targetWorkerName, snrparams.OcDebugTimeout)).To(Succeed(),
				"Failed to remove kubelet stop guard on node %s", targetWorkerName)

			By("Verifying SNR operator deployment is ready")

			snrDeployment, err := deployment.Pull(
				APIClient, snrparams.OperatorDeploymentName, medik8sparams.OperatorNs)
			Expect(err).ToNot(HaveOccurred(), "Failed to get SNR deployment")
			Expect(snrDeployment.IsReady(medik8sparams.DefaultTimeout)).To(BeTrue(),
				"SNR deployment is not Ready")

			By("Recording boot ID and creation timestamp")

			oldBootID, err = helpers.GetNodeBootIDFromAPI(ctx, APIClient, targetWorkerName)
			Expect(err).ToNot(HaveOccurred(),
				"Must read boot ID from node %s", targetWorkerName)

			node := &corev1.Node{}
			Expect(APIClient.Get(ctx, client.ObjectKey{Name: targetWorkerName}, node)).To(Succeed())
			Expect(helpers.IsNodeReady(node)).To(BeTrue(),
				"Target node %s is not Ready before test", targetWorkerName)

			creationTS = node.CreationTimestamp

			By("Pre-cleaning any stale CRs from previous runs")

			cleanupSNRCR(targetWorkerName)
			cleanupNHCCR(snrparams.NHCTestName)

			GinkgoWriter.Printf("Pre-remediation boot ID: %s\n", oldBootID)
		})

		JustAfterEach(func() {
			// Cleanup order: CRs first (only needs API server), then node
			// recovery.
			if currentNHCName != "" {
				By("Safety net: deleting NHC CR " + currentNHCName)
				cleanupNHCCR(currentNHCName)
				currentNHCName = ""
			}

			if currentSNRTName != "" {
				By("Safety net: deleting SNRT " + currentSNRTName)
				cleanupSNRT(currentSNRTName)
				currentSNRTName = ""
			}

			if targetWorkerName != "" {
				By("Safety net: deleting any leftover SNR CR for " + targetWorkerName)
				cleanupSNRCR(targetWorkerName)

				By("Safety net: waiting for node " + targetWorkerName + " to become Ready")

				if err := helpers.WaitForNodeReady(
					ctx, APIClient, targetWorkerName,
					snrparams.DefaultPollInterval, snrparams.NodeReadyTimeout,
					GinkgoWriter.Printf,
				); err != nil {
					GinkgoWriter.Printf(
						"WARNING: node %s did not become Ready within %s: %v\n",
						targetWorkerName, snrparams.NodeReadyTimeout, err)
					AddReportEntry("safety-net-recovery-failed",
						fmt.Sprintf("node %s did not recover: %v", targetWorkerName, err))
				} else {
					By("Removing kubelet stop guard file")

					bestEffortRemoveKubeletStopGuard(ctx, targetWorkerName)
				}
			}

			By("Safety net: verifying SNR DS pods are running")

			Eventually(verifyDSPodsRunning, snrparams.DSPodRestartTimeout, snrparams.DefaultPollInterval).Should(Succeed(),
				"SNR DaemonSet pods did not recover after remediation")
		})

		// verifyRemediationAndRecovery is the shared verification sequence
		// used by all worker tests after kubelet stop.
		verifyRemediationAndRecovery := func() {
			By("Waiting for SNR remediation to complete (node rebooted, SNR CR gone)")

			Expect(waitForRemediationComplete(
				ctx, APIClient, targetWorkerName, oldBootID,
			)).To(Succeed(),
				"SNR remediation did not complete for node %s within %s",
				targetWorkerName, snrparams.SNRDeletionTimeout)

			By("Waiting for node " + targetWorkerName + " to become Ready")

			Expect(helpers.WaitForNodeReady(
				ctx, APIClient, targetWorkerName,
				snrparams.DefaultPollInterval, snrparams.NodeReadyTimeout,
				GinkgoWriter.Printf,
			)).To(Succeed(),
				"Node %s did not become Ready after remediation", targetWorkerName)

			By("Verifying node was rebooted, not re-created")

			updatedNode := &corev1.Node{}
			Expect(APIClient.Get(ctx, client.ObjectKey{Name: targetWorkerName}, updatedNode)).To(Succeed())
			Expect(updatedNode.CreationTimestamp.Equal(&creationTS)).To(BeTrue(),
				"Node creation timestamp changed -- node was re-created instead of rebooted")
		}

		It("should remediate a worker node after kubelet stop",
			reportxml.ID("52416"),
			Label(labels.TierAcceptance, labels.DisruptionDestructive,
				labels.PlatformAny, labels.ComponentRemediation),
			func() {
				By("Creating NHC CR pointing to default Automatic SNRT")

				nhcCR := buildNHCForWorkers(snrparams.NHCTestName, snrparams.SNRTemplateName)
				Expect(APIClient.Create(ctx, nhcCR)).To(Succeed(),
					"Failed to create NHC CR %s", snrparams.NHCTestName)

				currentNHCName = snrparams.NHCTestName

				By(fmt.Sprintf("Stopping kubelet on worker node %s", targetWorkerName))

				Expect(stopKubeletForRemediation(ctx, targetWorkerName)).To(Succeed(),
					"Failed to stop kubelet on node %s", targetWorkerName)

				By("Verifying out-of-service taint is applied during remediation")

				// The out-of-service taint is transient: SNR adds it when
				// remediation starts and removes it after the node recovers.
				// On fast clusters or when oc debug takes long, the entire
				// cycle may complete before we check. In that case, boot ID
				// change proves the taint was applied and removed.
				Eventually(func() bool {
					node := &corev1.Node{}
					if err := APIClient.Get(ctx,
						client.ObjectKey{Name: targetWorkerName}, node); err != nil {
						return false
					}

					for _, taint := range node.Spec.Taints {
						if taint.Key == snrparams.OutOfServiceTaintKey {
							GinkgoWriter.Println("Out-of-service taint observed on node")

							return true
						}
					}

					// Taint not present -- check if remediation already
					// completed (boot ID changed = taint was applied and removed).
					currentBootID, bootErr := helpers.GetNodeBootIDFromAPI(
						ctx, APIClient, targetWorkerName)
					if bootErr != nil {
						return false
					}

					if currentBootID != oldBootID {
						GinkgoWriter.Println(
							"Taint already removed, boot ID changed -- " +
								"remediation completed before taint check")

						return true
					}

					return false
				}, snrparams.SNRDeletionTimeout, snrparams.DefaultPollInterval).Should(BeTrue(),
					"Out-of-service taint not found on node %s during remediation",
					targetWorkerName)

				verifyRemediationAndRecovery()

				By("Deleting NHC CR")

				cleanupNHCCR(currentNHCName)
				currentNHCName = ""
			})

		Context("strategy-specific remediation with workload pod", func() {
			runStrategyTest := func(strategyName, snrtName string) {
				By("Pre-cleaning any stale SNRT from previous runs")

				cleanupSNRT(snrtName)

				By(fmt.Sprintf("Creating %s SNRT", strategyName))

				snrt := buildSNRT(snrtName, strategyName)
				Expect(APIClient.Create(ctx, snrt)).To(Succeed(),
					"Failed to create %s SNRT", strategyName)

				currentSNRTName = snrtName

				By(fmt.Sprintf("Creating test workload pod on node %s", targetWorkerName))

				workloadPod := createWorkloadPodOnNode(ctx, targetWorkerName)

				By("Creating NHC CR pointing to " + strategyName + " SNRT")

				nhcCR := buildNHCForWorkers(snrparams.NHCTestName, snrtName)
				Expect(APIClient.Create(ctx, nhcCR)).To(Succeed(),
					"Failed to create NHC CR")

				currentNHCName = snrparams.NHCTestName

				By(fmt.Sprintf("Stopping kubelet on worker node %s", targetWorkerName))

				Expect(stopKubeletForRemediation(ctx, targetWorkerName)).To(Succeed())

				verifyRemediationAndRecovery()

				By("Verifying workload pod was evicted from remediated node")

				waitForPodEvictedFromNode(ctx,
					workloadPod.Name, workloadPod.Namespace, targetWorkerName)

				By("Cleaning up NHC CR and SNRT")

				cleanupNHCCR(currentNHCName)
				currentNHCName = ""

				cleanupSNRT(currentSNRTName)
				currentSNRTName = ""
			}

			It("should evict workload pod using ResourceDeletion strategy",
				reportxml.ID("50772"),
				Label(labels.TierAcceptance, labels.DisruptionDestructive,
					labels.PlatformAny, labels.ComponentRemediation),
				func() {
					runStrategyTest("ResourceDeletion", snrparams.SNRTResourceDeletionName)
				})

			It("should evict workload pod using OutOfServiceTaint strategy",
				reportxml.ID("61594"),
				Label(labels.TierAcceptance, labels.DisruptionDestructive,
					labels.PlatformAny, labels.ComponentRemediation),
				func() {
					runStrategyTest("OutOfServiceTaint", snrparams.SNRTOutOfServiceTaintName)
				})
		})
	})
