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
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("SNR Functional - Master Remediation",
	Serial, Ordered, ContinueOnFailure,
	Label(labels.OperatorSNR, snrparams.Label,
		labels.DisruptionDestructive, labels.FrequencyNightly),
	func() {
		var (
			ctx              context.Context
			targetMasterName string
			currentNHCNames  []string
		)

		BeforeAll(func() {
			ctx = context.Background()

			By("Checking NHC CRD is installed")

			if !isNHCCRDInstalled() {
				Skip("NodeHealthCheck CRD not found; NHC operator not installed -- skipping master remediation tests")
			}

			By("Verifying at least 3 Ready master nodes for etcd quorum safety")

			// 3 masters minimum: target (kubelet stopped) + 2 surviving
			// masters to maintain etcd quorum (majority requirement).
			masterCount, err := countReadyMasterNodes(ctx, APIClient)
			Expect(err).ToNot(HaveOccurred())
			Expect(masterCount).To(BeNumerically(">=", snrparams.MinReadyMasterNodes),
				"Master remediation tests require at least %d Ready master nodes for etcd quorum",
				snrparams.MinReadyMasterNodes)

			By("Selecting target master node")

			masterNode, err := selectMasterNode(ctx, APIClient)
			Expect(err).ToNot(HaveOccurred(), "Failed to select master node")

			targetMasterName = masterNode.Name
			GinkgoWriter.Printf("Target master node: %s\n", targetMasterName)
		})

		BeforeEach(func() {
			By("Removing any stale kubelet stop guard from a previous run")

			Expect(helpers.RemoveKubeletStopGuard(ctx, targetMasterName, snrparams.OcDebugTimeout)).To(Succeed(),
				"Failed to remove kubelet stop guard on master %s", targetMasterName)

			By("Verifying SNR operator deployment is ready")

			snrDeployment, err := deployment.Pull(
				APIClient, snrparams.OperatorDeploymentName, medik8sparams.OperatorNs)
			Expect(err).ToNot(HaveOccurred(), "Failed to get SNR deployment")
			Expect(snrDeployment.IsReady(medik8sparams.DefaultTimeout)).To(BeTrue(),
				"SNR deployment is not Ready")
		})

		JustAfterEach(func() {
			// Cleanup order: CRs first (only needs API server), then node
			// recovery.
			for _, nhcName := range currentNHCNames {
				By("Safety net: deleting NHC CR " + nhcName)
				cleanupNHCCR(nhcName)
			}

			currentNHCNames = nil

			if targetMasterName != "" {
				By("Safety net: deleting any leftover SNR CR for master " + targetMasterName)
				cleanupSNRCR(targetMasterName)

				By("Safety net: waiting for master " + targetMasterName + " to become Ready")

				if err := helpers.WaitForNodeReady(
					ctx, APIClient, targetMasterName,
					snrparams.DefaultPollInterval, snrparams.NodeReadyTimeout,
					GinkgoWriter.Printf,
				); err != nil {
					GinkgoWriter.Printf(
						"WARNING: master %s did not become Ready within %s: %v\n",
						targetMasterName, snrparams.NodeReadyTimeout, err)
					AddReportEntry("safety-net-recovery-failed",
						fmt.Sprintf("master %s did not recover: %v", targetMasterName, err))
				} else {
					By("Removing kubelet stop guard file")

					bestEffortRemoveKubeletStopGuard(ctx, targetMasterName)
				}
			}

			By("Safety net: verifying SNR DS pods are running")

			Eventually(verifyDSPodsRunning,
				snrparams.DSPodRestartTimeout, snrparams.DefaultPollInterval).Should(Succeed(),
				"SNR DaemonSet pods did not recover after remediation")
		})

		It("should remediate a master node after kubelet stop",
			reportxml.ID("55059"),
			Label(labels.TierAcceptance, labels.DisruptionDestructive,
				labels.PlatformAny, labels.ComponentRemediation),
			func() {
				By("Recording boot ID and creation timestamp")

				oldBootID, err := helpers.GetNodeBootIDFromAPI(ctx, APIClient, targetMasterName)
				Expect(err).ToNot(HaveOccurred(),
					"Must read boot ID from master node %s", targetMasterName)

				node := &corev1.Node{}
				Expect(APIClient.Get(ctx, client.ObjectKey{Name: targetMasterName}, node)).To(Succeed())

				creationTimestamp := node.CreationTimestamp

				GinkgoWriter.Printf("Pre-remediation master boot ID: %s\n", oldBootID)

				By("Pre-cleaning any stale CRs from previous runs")

				cleanupSNRCR(targetMasterName)
				cleanupNHCCR(snrparams.NHCMasterTestName)

				By("Creating NHC CR targeting master nodes")

				nhcCR := buildNHCForMasters(snrparams.NHCMasterTestName, snrparams.SNRTemplateName)
				Expect(APIClient.Create(ctx, nhcCR)).To(Succeed(),
					"Failed to create NHC CR for masters")

				currentNHCNames = []string{snrparams.NHCMasterTestName}

				By(fmt.Sprintf("Stopping kubelet on master node %s", targetMasterName))

				Expect(stopKubeletForRemediation(ctx, targetMasterName)).To(Succeed(),
					"Failed to stop kubelet on master %s", targetMasterName)

				By("Waiting for SNR remediation to complete (master rebooted, SNR CR gone)")

				Expect(waitForRemediationComplete(
					ctx, APIClient, targetMasterName, oldBootID,
				)).To(Succeed(),
					"SNR remediation did not complete for master %s", targetMasterName)

				By("Waiting for master " + targetMasterName + " to become Ready")

				Expect(helpers.WaitForNodeReady(
					ctx, APIClient, targetMasterName,
					snrparams.DefaultPollInterval, snrparams.NodeReadyTimeout,
					GinkgoWriter.Printf,
				)).To(Succeed(),
					"Master %s did not become Ready after remediation", targetMasterName)

				By("Verifying boot ID changed (master rebooted)")

				newBootID, err := helpers.GetNodeBootIDFromAPI(ctx, APIClient, targetMasterName)
				Expect(err).ToNot(HaveOccurred())
				Expect(newBootID).ToNot(Equal(oldBootID),
					"Boot ID unchanged -- master %s did not reboot", targetMasterName)
				GinkgoWriter.Printf("Master boot ID changed: %s -> %s\n", oldBootID, newBootID)

				By("Verifying node creation timestamp unchanged")

				updatedNode := &corev1.Node{}
				Expect(APIClient.Get(ctx, client.ObjectKey{Name: targetMasterName}, updatedNode)).To(Succeed())
				Expect(updatedNode.CreationTimestamp.Equal(&creationTimestamp)).To(BeTrue(),
					"Master creation timestamp changed -- node was re-created instead of rebooted")

				By("Cleaning up NHC CR")

				cleanupNHCCR(snrparams.NHCMasterTestName)

				currentNHCNames = nil
			})

		It("should remediate master and worker simultaneously after kubelet stop",
			reportxml.ID("56069"),
			Label(labels.TierAcceptance, labels.DisruptionDestructive,
				labels.PlatformAny, labels.ComponentRemediation),
			func() {
				By("Verifying at least 1 Ready worker node")

				workerCount, err := helpers.CountReadyWorkerNodes(ctx, APIClient)
				Expect(err).ToNot(HaveOccurred())

				if workerCount < 1 {
					Skip("Simultaneous test requires at least 1 Ready worker node")
				}

				By("Selecting target worker node")

				targetWorkerNode, err := helpers.SelectWorkerNode(ctx, APIClient)
				Expect(err).ToNot(HaveOccurred(), "Failed to select worker node")

				targetWorkerName := targetWorkerNode.Name
				GinkgoWriter.Printf("Target worker node: %s\n", targetWorkerName)

				DeferCleanup(func() {
					By("Removing deferred worker kubelet stop guard")

					bestEffortRemoveKubeletStopGuard(ctx, targetWorkerName)
				})

				By("Recording boot IDs and creation timestamps for both nodes")

				oldMasterBootID, err := helpers.GetNodeBootIDFromAPI(ctx, APIClient, targetMasterName)
				Expect(err).ToNot(HaveOccurred())

				oldWorkerBootID, err := helpers.GetNodeBootIDFromAPI(ctx, APIClient, targetWorkerName)
				Expect(err).ToNot(HaveOccurred())

				masterNode := &corev1.Node{}
				Expect(APIClient.Get(ctx, client.ObjectKey{Name: targetMasterName}, masterNode)).To(Succeed())

				masterCreationTS := masterNode.CreationTimestamp

				workerNode := &corev1.Node{}
				Expect(APIClient.Get(ctx, client.ObjectKey{Name: targetWorkerName}, workerNode)).To(Succeed())

				workerCreationTS := workerNode.CreationTimestamp

				GinkgoWriter.Printf("Pre-remediation boot IDs: master=%s, worker=%s\n",
					oldMasterBootID, oldWorkerBootID)

				By("Removing any stale kubelet stop guard on worker")

				Expect(helpers.RemoveKubeletStopGuard(ctx, targetWorkerName, snrparams.OcDebugTimeout)).To(Succeed(),
					"Failed to remove kubelet stop guard on worker %s", targetWorkerName)

				By("Pre-cleaning any stale CRs from previous runs")

				cleanupSNRCR(targetMasterName)
				cleanupSNRCR(targetWorkerName)
				cleanupNHCCR(snrparams.NHCMasterTestName)
				cleanupNHCCR(snrparams.NHCTestName)

				By("Creating NHC CR for master nodes")

				nhcMaster := buildNHCForMasters(snrparams.NHCMasterTestName, snrparams.SNRTemplateName)
				Expect(APIClient.Create(ctx, nhcMaster)).To(Succeed(),
					"Failed to create NHC CR for masters")

				By("Creating NHC CR for worker nodes")

				nhcWorker := buildNHCForWorkers(snrparams.NHCTestName, snrparams.SNRTemplateName)
				Expect(APIClient.Create(ctx, nhcWorker)).To(Succeed(),
					"Failed to create NHC CR for workers")

				currentNHCNames = []string{snrparams.NHCMasterTestName, snrparams.NHCTestName}

				// Note: stops are sequential (each oc debug can take up to
				// OcDebugTimeout). True simultaneity would require goroutines,
				// but the stagger is acceptable -- NHC's 60s unhealthy
				// threshold means both nodes are detected in the same cycle.
				By(fmt.Sprintf("Stopping kubelet on master %s and worker %s",
					targetMasterName, targetWorkerName))

				Expect(stopKubeletForRemediation(ctx, targetMasterName)).To(Succeed(),
					"Failed to stop kubelet on master %s", targetMasterName)
				Expect(stopKubeletForRemediation(ctx, targetWorkerName)).To(Succeed(),
					"Failed to stop kubelet on worker %s", targetWorkerName)

				By("Waiting for SNR remediation to complete on both nodes")

				Expect(waitForRemediationComplete(
					ctx, APIClient, targetMasterName, oldMasterBootID,
				)).To(Succeed(),
					"SNR remediation did not complete for master %s", targetMasterName)

				Expect(waitForRemediationComplete(
					ctx, APIClient, targetWorkerName, oldWorkerBootID,
				)).To(Succeed(),
					"SNR remediation did not complete for worker %s", targetWorkerName)

				By("Waiting for both nodes to become Ready")

				Expect(helpers.WaitForNodeReady(
					ctx, APIClient, targetMasterName,
					snrparams.DefaultPollInterval, snrparams.NodeReadyTimeout,
					GinkgoWriter.Printf,
				)).To(Succeed(), "Master %s did not become Ready", targetMasterName)

				Expect(helpers.WaitForNodeReady(
					ctx, APIClient, targetWorkerName,
					snrparams.DefaultPollInterval, snrparams.NodeReadyTimeout,
					GinkgoWriter.Printf,
				)).To(Succeed(), "Worker %s did not become Ready", targetWorkerName)

				By("Verifying both nodes were rebooted, not re-created")

				updatedMaster := &corev1.Node{}
				Expect(APIClient.Get(ctx, client.ObjectKey{Name: targetMasterName}, updatedMaster)).To(Succeed())
				Expect(updatedMaster.CreationTimestamp.Equal(&masterCreationTS)).To(BeTrue(),
					"Master creation timestamp changed -- node was re-created instead of rebooted")

				updatedWorker := &corev1.Node{}
				Expect(APIClient.Get(ctx, client.ObjectKey{Name: targetWorkerName}, updatedWorker)).To(Succeed())
				Expect(updatedWorker.CreationTimestamp.Equal(&workerCreationTS)).To(BeTrue(),
					"Worker creation timestamp changed -- node was re-created instead of rebooted")

				GinkgoWriter.Printf("Both nodes rebooted and recovered: master=%s, worker=%s\n",
					targetMasterName, targetWorkerName)

				By("Cleaning up NHC CRs")

				for _, nhcName := range currentNHCNames {
					cleanupNHCCR(nhcName)
				}

				currentNHCNames = nil
			})
	})
