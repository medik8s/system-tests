package tests

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/deployment"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/olm"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/pod"
	olmV1alpha1 "github.com/rh-ecosystem-edge/eco-goinfra/pkg/schemes/olm/operators/v1alpha1"

	"github.com/medik8s/system-tests/tests/internal/helpers"
	"github.com/medik8s/system-tests/tests/internal/labels"
	. "github.com/medik8s/system-tests/tests/internal/medik8sinittools"
	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
	"github.com/medik8s/system-tests/tests/snr-operator/internal/snrparams"
	"github.com/medik8s/system-tests/tests/snr-operator/internal/snrutils"
)

var _ = Describe("SNR Operator Upgrade",
	Serial,
	Label(labels.OperatorSNR, snrparams.Label,
		labels.TierUpgrade, labels.DisruptionDestructive,
		labels.PlatformAny, labels.FrequencyWeekly,
		labels.ComponentOLM),
	func() {
		var (
			ctx             context.Context
			previousCSV     *olm.ClusterServiceVersionBuilder
			preUpgradeImage string
			currentSNRName  string
		)

		BeforeAll(func() {
			ctx = context.Background()

			Expect(snrparams.TargetCatalogImage).NotTo(BeEmpty(),
				"MEDIK8S_TARGET_CATALOG_IMAGE must be set")
			Expect(snrparams.TargetOCPImage).NotTo(BeEmpty(),
				"OPENSHIFT_UPGRADE_RELEASE_IMAGE_OVERRIDE must be set")

			By("Verifying at least 2 Ready worker nodes")

			workerCount, err := helpers.CountReadyWorkerNodes(ctx, APIClient)
			Expect(err).ToNot(HaveOccurred())
			Expect(workerCount).To(BeNumerically(">=", 2),
				"Upgrade tests require at least 2 Ready worker nodes")
		})

		AfterAll(func() {
			snrutils.CleanupUpgradeResources(APIClient, GinkgoWriter.Printf)
		})

		JustAfterEach(func() {
			if CurrentSpecReport().Failed() {
				GinkgoWriter.Println("Upgrade test failed - collecting SNR controller state")
				logSNRControllerState(ctx)
			}

			if currentSNRName != "" {
				nodeName := currentSNRName

				By("Cleaning up SNR CR " + currentSNRName)
				deleteRemediationCR(ctx, APIClient, snrGVK, currentSNRName)
				currentSNRName = ""

				By("Safety net: waiting for node " + nodeName + " to become Ready")

				if err := helpers.WaitForNodeReady(
					ctx, APIClient, nodeName,
					snrparams.DefaultPollInterval, snrparams.NodeReadyTimeout,
				); err != nil {
					GinkgoWriter.Printf(
						"WARNING: node %s did not become Ready within %s: %v\n",
						nodeName, snrparams.NodeReadyTimeout, err)
				}
			}
		})

		It("should survive OCP upgrade and operator upgrade with working remediation",
			Label(labels.ComponentRemediation),
			func() {
				By("Step 1: Install SNR operator GA version from redhat-operators")

				_, err := snrutils.InstallGAOperator(APIClient)
				Expect(err).NotTo(HaveOccurred(), "Failed to install GA SNR operator")

				By("Step 2: Wait for CSV to reach Succeeded and deployment to be Ready")

				Eventually(func() error {
					var csvErr error
					previousCSV, csvErr = snrutils.FindSucceededCSV(
						APIClient, snrparams.UpgradeOperatorPackage)

					return csvErr
				}, snrparams.OperatorUpgradeTimeout, snrparams.DefaultPollInterval).Should(Succeed(),
					"SNR CSV must reach Succeeded phase")

				snrDeploy, err := deployment.Pull(
					APIClient, snrparams.OperatorDeploymentName, medik8sparams.OperatorNs)
				Expect(err).NotTo(HaveOccurred(), "Failed to get SNR deployment")
				Expect(snrDeploy.IsReady(medik8sparams.DefaultTimeout)).To(BeTrue(),
					"SNR deployment is not Ready")

				By("Step 3: Record GA install checkpoint")

				preUpgradeImage, err = snrutils.GetSNRControllerImage(APIClient)
				Expect(err).NotTo(HaveOccurred())
				GinkgoWriter.Printf("GA operator image: %s\n", preUpgradeImage)
				GinkgoWriter.Printf("GA SNR CSV: %s\n", previousCSV.Object.Name)

				Eventually(verifyDSPodsRunning,
					snrparams.PostUpgradeRecoveryTimeout, snrparams.DefaultPollInterval).
					Should(Succeed(), "SNR DaemonSet pods must be Running after GA install")

				By("Step 4: Upgrade OCP from N-1 to N")

				upgradeOCP(ctx, snrparams.TargetOCPImage)

				By("Step 5: Verify SNR operator survived OCP upgrade")

				Eventually(func() error {
					_, csvErr := snrutils.FindSucceededCSV(
						APIClient, snrparams.UpgradeOperatorPackage)

					return csvErr
				}, snrparams.PostUpgradeRecoveryTimeout, snrparams.DefaultPollInterval).Should(Succeed(),
					"SNR CSV not in Succeeded phase after OCP upgrade")

				snrDeploy, err = deployment.Pull(
					APIClient, snrparams.OperatorDeploymentName, medik8sparams.OperatorNs)
				Expect(err).NotTo(HaveOccurred())
				Expect(snrDeploy.IsReady(snrparams.PostUpgradeRecoveryTimeout)).To(BeTrue(),
					"SNR deployment not Ready after OCP upgrade")

				Eventually(verifyDSPodsRunning,
					snrparams.PostUpgradeRecoveryTimeout, snrparams.DefaultPollInterval).
					Should(Succeed(), "SNR DaemonSet pods must be Running after OCP upgrade")

				By("Step 6: Validate GA SNR remediation on OCP N")

				currentSNRName, err = runSNRRemediationCycle(ctx, "post-ocp-upgrade")
				Expect(err).NotTo(HaveOccurred(),
					"Post-OCP-upgrade remediation failed with GA operator")

				By("Cleaning up SNR CR from post-OCP-upgrade remediation")
				deleteRemediationCR(ctx, APIClient, snrGVK, currentSNRName)
				currentSNRName = ""

				By("Step 7: Switch operator Subscription to Konflux CatalogSource")

				_, err = snrutils.CreateUpgradeCatalogSource(APIClient)
				Expect(err).NotTo(HaveOccurred(), "Failed to create upgrade CatalogSource")

				_, err = snrutils.SwitchSubscriptionCatalog(APIClient)
				Expect(err).NotTo(HaveOccurred(),
					"Failed to switch Subscription to target catalog")

				By("Step 8: Wait for new CSV to reach Succeeded")

				Eventually(func() error {
					csvs, listErr := olm.ListClusterServiceVersionWithNamePattern(
						APIClient, snrparams.UpgradeOperatorPackage, medik8sparams.OperatorNs)
					if listErr != nil {
						return listErr
					}

					for _, csv := range csvs {
						csvPhase, _ := csv.GetPhase()
						if csvPhase == olmV1alpha1.CSVPhaseSucceeded &&
							csv.Object.Name != previousCSV.Object.Name {
							GinkgoWriter.Printf("New CSV: %s (was: %s)\n",
								csv.Object.Name, previousCSV.Object.Name)

							return nil
						}
					}

					return fmt.Errorf("new SNR CSV not yet in Succeeded phase")
				}, snrparams.OperatorUpgradeTimeout, snrparams.DefaultPollInterval).Should(Succeed(),
					"Operator upgrade did not complete")

				By("Step 9: Verify SNR controller pods restarted with new image")

				Eventually(func() error {
					currentImage, imgErr := snrutils.GetSNRControllerImage(APIClient)
					if imgErr != nil {
						return imgErr
					}

					if currentImage == preUpgradeImage {
						return fmt.Errorf("controller still running old image %s", preUpgradeImage)
					}

					GinkgoWriter.Printf("Controller image updated: %s\n", currentImage)

					return nil
				}, snrparams.OperatorUpgradeTimeout, snrparams.DefaultPollInterval).Should(Succeed(),
					"SNR controller pods did not restart with new image")

				Eventually(verifyDSPodsRunning,
					snrparams.PostUpgradeRecoveryTimeout, snrparams.DefaultPollInterval).
					Should(Succeed(), "SNR DaemonSet pods must be Running after operator upgrade")

				By("Step 10: Validate pre-GA SNR remediation on OCP N")

				currentSNRName, err = runSNRRemediationCycle(ctx, "post-operator-upgrade")
				Expect(err).NotTo(HaveOccurred(),
					"Post-operator-upgrade remediation failed with pre-GA operator")

				By("Cleaning up SNR CR from post-operator-upgrade remediation")
				deleteRemediationCR(ctx, APIClient, snrGVK, currentSNRName)
				currentSNRName = ""
			})
	})

// upgradeOCP triggers an OCP cluster upgrade and waits for completion.
func upgradeOCP(ctx context.Context, targetImage string) {
	clusterVersion := &configv1.ClusterVersion{}
	Expect(APIClient.Get(ctx, client.ObjectKey{Name: "version"}, clusterVersion)).
		To(Succeed(), "Failed to get ClusterVersion")

	clusterVersion.Spec.DesiredUpdate = &configv1.Update{
		Image: targetImage,
		Force: true,
	}

	Expect(APIClient.Update(ctx, clusterVersion)).
		To(Succeed(), "Failed to set desired OCP update")

	GinkgoWriter.Printf("OCP upgrade initiated to image: %s\n", targetImage)

	waitForClusterVersionCondition(ctx,
		"Progressing", configv1.ConditionTrue,
		snrparams.OCPUpgradeStartTimeout,
		"OCP upgrade did not start progressing")

	waitForClusterVersionCondition(ctx,
		"Progressing", configv1.ConditionFalse,
		snrparams.OCPUpgradeTimeout,
		"OCP upgrade did not complete (still Progressing)")

	waitForClusterVersionCondition(ctx,
		"Available", configv1.ConditionTrue,
		snrparams.PostUpgradeRecoveryTimeout,
		"Cluster not Available after OCP upgrade")

	waitForClusterVersionCondition(ctx,
		"Degraded", configv1.ConditionFalse,
		snrparams.PostUpgradeRecoveryTimeout,
		"Cluster is Degraded after OCP upgrade")

	GinkgoWriter.Println("OCP upgrade completed and cluster is healthy")
}

func waitForClusterVersionCondition(
	ctx context.Context,
	condType string, condStatus configv1.ConditionStatus,
	timeout time.Duration, failureMsg string,
) {
	Eventually(func() bool {
		clsVer := &configv1.ClusterVersion{}
		if getErr := APIClient.Get(ctx, client.ObjectKey{Name: "version"}, clsVer); getErr != nil {
			return false
		}

		for _, cond := range clsVer.Status.Conditions {
			if string(cond.Type) == condType && cond.Status == condStatus {
				return true
			}
		}

		return false
	}, timeout, snrparams.DefaultPollInterval).Should(BeTrue(), failureMsg)
}

// runSNRRemediationCycle selects a worker node, stops its kubelet to trigger
// SNR self-remediation, and verifies the node reboots and recovers.
func runSNRRemediationCycle(ctx context.Context, phase string) (string, error) {
	// Select a worker node that is NOT running the SNR controller-manager leader
	// to avoid disrupting the operator itself during the test.
	controllerNodes, err := getSNRControllerNodes(ctx)
	if err != nil {
		return "", fmt.Errorf("[%s] failed to get controller nodes: %w", phase, err)
	}

	selectedNode, err := helpers.SelectWorkerNode(ctx, APIClient, controllerNodes...)
	if err != nil {
		return "", fmt.Errorf("[%s] failed to select target node: %w", phase, err)
	}

	nodeName := selectedNode.Name
	GinkgoWriter.Printf("[%s] Target node: %s (excluding controller nodes: %v)\n",
		phase, nodeName, controllerNodes)

	previousBootID, err := helpers.GetNodeBootIDFromAPI(ctx, APIClient, nodeName)
	if err != nil {
		return "", fmt.Errorf("[%s] failed to get boot ID: %w", phase, err)
	}

	By(fmt.Sprintf("[%s] Creating workload pod on %s", phase, nodeName))

	workloadPod := createWorkloadPodOnNode(ctx, nodeName)

	GinkgoWriter.Printf("[%s] Workload pod %s running on %s\n",
		phase, workloadPod.Name, nodeName)

	By(fmt.Sprintf("[%s] Stopping kubelet on %s to trigger remediation", phase, nodeName))

	Expect(stopKubeletForRemediation(ctx, nodeName)).To(Succeed(),
		"[%s] Failed to stop kubelet on %s", phase, nodeName)

	By(fmt.Sprintf("[%s] Waiting for SNR remediation to complete", phase))

	Expect(waitForRemediationComplete(
		ctx, APIClient, nodeName, previousBootID, snrparams.SNRDeletionTimeout,
	)).To(Succeed(), "[%s] Remediation did not complete for node %s", phase, nodeName)

	By(fmt.Sprintf("[%s] Waiting for node %s to become Ready", phase, nodeName))

	Expect(helpers.WaitForNodeReady(
		ctx, APIClient, nodeName,
		snrparams.DefaultPollInterval, snrparams.NodeReadyTimeout,
	)).To(Succeed(), "[%s] Node %s did not become Ready after remediation", phase, nodeName)

	By(fmt.Sprintf("[%s] Verifying workload pod was evicted from %s", phase, nodeName))

	waitForPodEvictedFromNode(ctx, workloadPod.Name, workloadPod.Namespace, nodeName)

	GinkgoWriter.Printf("[%s] Remediation cycle completed for node %s\n", phase, nodeName)

	return nodeName, nil
}

// getSNRControllerNodes returns the node names where SNR controller-manager pods
// are running, so we can exclude them from remediation targets.
func getSNRControllerNodes(ctx context.Context) ([]string, error) {
	listOptions := metav1.ListOptions{
		LabelSelector: snrparams.OperatorControllerPodLabelSelector,
	}

	controllerPods, err := pod.List(APIClient, medik8sparams.OperatorNs, listOptions)
	if err != nil {
		return nil, fmt.Errorf("failed to list SNR controller pods: %w", err)
	}

	runningPods := helpers.FilterRunningPods(controllerPods)
	nodes := make([]string, 0, len(runningPods))

	for _, p := range runningPods {
		if p.Object.Spec.NodeName != "" {
			nodes = append(nodes, p.Object.Spec.NodeName)
		}
	}

	return nodes, nil
}

// logSNRControllerState dumps SNR controller pod status for debugging.
func logSNRControllerState(ctx context.Context) {
	listOptions := metav1.ListOptions{
		LabelSelector: snrparams.OperatorControllerPodLabelSelector,
	}

	pods, err := pod.List(APIClient, medik8sparams.OperatorNs, listOptions)
	if err != nil {
		GinkgoWriter.Printf("Failed to list SNR controller pods: %v\n", err)

		return
	}

	for _, p := range pods {
		GinkgoWriter.Printf("SNR controller pod: %s node=%s phase=%s\n",
			p.Object.Name, p.Object.Spec.NodeName, p.Object.Status.Phase)
	}

	// Also dump DS pods.
	dsListOptions := metav1.ListOptions{
		LabelSelector: snrparams.DaemonSetPodLabelSelector,
	}

	dsPods, err := pod.List(APIClient, medik8sparams.OperatorNs, dsListOptions)
	if err != nil {
		GinkgoWriter.Printf("Failed to list SNR DS pods: %v\n", err)

		return
	}

	for _, p := range dsPods {
		GinkgoWriter.Printf("SNR DS pod: %s node=%s phase=%s\n",
			p.Object.Name, p.Object.Spec.NodeName, p.Object.Status.Phase)
	}

}
