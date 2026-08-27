package tests

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"

	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/deployment"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/olm"
	olmV1alpha1 "github.com/rh-ecosystem-edge/eco-goinfra/pkg/schemes/olm/operators/v1alpha1"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/medik8s/system-tests/tests/internal/helpers"
	"github.com/medik8s/system-tests/tests/internal/labels"
	. "github.com/medik8s/system-tests/tests/internal/medik8sinittools"
	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
	"github.com/medik8s/system-tests/tests/sbr-operator/internal/sbrparams"
	"github.com/medik8s/system-tests/tests/sbr-operator/internal/sbrutils"
)

// SBR's full remediation path needs ODF/CephFS storage, watchdog devices, and
// NHC-triggered fault injection (see nhc_integration.go). Exercising that
// whole chain inside an already-long OCP+operator upgrade test would make it
// both slower and more infra-fragile than it needs to be. Instead this test
// uses the simplest real proof that the operator is functioning end to end:
// creating a StorageBasedRemediationConfig and confirming its agent
// DaemonSet reaches Ready (reusing buildSBRC/waitForSBRCReady from sbr.go --
// no new SBR-typed helpers are added here).
var _ = Describe("SBR Operator Upgrade",
	Serial, Ordered,
	Label(labels.OperatorSBR, sbrparams.Label,
		labels.TierUpgrade, labels.DisruptionDestructive,
		labels.PlatformAny, labels.FrequencyWeekly,
		labels.ComponentOLM),
	func() {
		var (
			ctx             context.Context
			previousCSV     *olm.ClusterServiceVersionBuilder
			preUpgradeImage string
		)

		BeforeAll(func() {
			ctx = context.Background()

			Expect(medik8sparams.TargetOCPImage).NotTo(BeEmpty(),
				"OPENSHIFT_UPGRADE_RELEASE_IMAGE_OVERRIDE or RELEASE_IMAGE_LATEST must be set")
		})

		AfterAll(func() {
			cleanupUpgradeSBRC()
			sbrutils.CleanupUpgradeResources(APIClient, GinkgoWriter.Printf)
		})

		JustAfterEach(func() {
			if CurrentSpecReport().Failed() {
				GinkgoWriter.Println("Upgrade test failed - collecting SBR controller state")
				helpers.LogControllerState(ctx, APIClient,
					medik8sparams.OperatorNs,
					map[string]string{
						"app.kubernetes.io/name": sbrparams.OperatorControllerPodLabel,
						"control-plane":          "controller-manager",
					},
					GinkgoWriter.Printf)
			}
		})

		It("should survive OCP upgrade and operator upgrade with a working agent DaemonSet",
			Label(labels.ComponentRemediation),
			func() {
				By("Step 1: Install SBR operator GA version from redhat-operators")

				_, err := sbrutils.InstallGAOperator(APIClient)
				Expect(err).NotTo(HaveOccurred(), "Failed to install GA SBR operator")

				By("Step 2: Wait for CSV to reach Succeeded and deployment to be Ready")

				Eventually(func() error {
					var csvErr error
					previousCSV, csvErr = sbrutils.FindSucceededCSV(
						APIClient, medik8sparams.OperatorPackage)

					return csvErr
				}, medik8sparams.OperatorUpgradeTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
					"SBR CSV must reach Succeeded phase")

				sbrDeploy, err := deployment.Pull(
					APIClient, sbrparams.OperatorDeploymentName, medik8sparams.OperatorNs)
				Expect(err).NotTo(HaveOccurred(), "Failed to get SBR deployment")
				Expect(sbrDeploy.IsReady(medik8sparams.DefaultTimeout)).To(BeTrue(),
					"SBR deployment is not Ready")

				By("Step 3: Record GA install checkpoint")

				preUpgradeImage, err = sbrutils.GetSBRControllerImage(APIClient)
				Expect(err).NotTo(HaveOccurred())
				GinkgoWriter.Printf("GA operator image: %s\n", preUpgradeImage)
				GinkgoWriter.Printf("GA SBR CSV: %s\n", previousCSV.Object.Name)

				By("Step 4: Validate GA SBR agent DaemonSet on OCP N-1")

				runSBRCFunctionCheck("pre-ocp-upgrade")

				By("Step 5: Upgrade OCP from N-1 to N")

				upgradeOCP(ctx, medik8sparams.TargetOCPImage)

				By("Step 6: Verify SBR operator survived OCP upgrade")

				Eventually(func() error {
					_, csvErr := sbrutils.FindSucceededCSV(
						APIClient, medik8sparams.OperatorPackage)

					return csvErr
				}, medik8sparams.PostUpgradeRecoveryTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
					"SBR CSV not in Succeeded phase after OCP upgrade")

				sbrDeploy, err = deployment.Pull(
					APIClient, sbrparams.OperatorDeploymentName, medik8sparams.OperatorNs)
				Expect(err).NotTo(HaveOccurred())
				Expect(sbrDeploy.IsReady(medik8sparams.PostUpgradeRecoveryTimeout)).To(BeTrue(),
					"SBR deployment not Ready after OCP upgrade")

				By("Step 7: Validate GA SBR agent DaemonSet on OCP N")

				runSBRCFunctionCheck("post-ocp-upgrade")

				By("Step 8: Apply deferred IDMS for Konflux catalog images")

				Expect(medik8sparams.SharedDir).NotTo(BeEmpty(),
					"SHARED_DIR must be set (provided by ci-operator)")

				preIDMSGens, genErr := helpers.GetMCPGenerations(ctx)
				Expect(genErr).NotTo(HaveOccurred(),
					"Failed to capture MCP generations before IDMS apply")

				idmsChanged, applyErr := helpers.ApplyIDMSFromSharedDir(ctx,
					medik8sparams.SharedDir, GinkgoWriter.Printf)
				Expect(applyErr).NotTo(HaveOccurred(),
					"Failed to apply IDMS from SHARED_DIR")

				if idmsChanged {
					By("Waiting for MCP rollout after IDMS change")

					Expect(helpers.WaitForMCPRollout(ctx, preIDMSGens,
						medik8sparams.MCPDetectionTimeout,
						medik8sparams.MCPRolloutTimeout,
						10*time.Second, GinkgoWriter.Printf,
					)).To(Succeed(), "MCP rollout failed after IDMS apply")
				} else {
					GinkgoWriter.Println("IDMS unchanged, skipping MCP rollout wait")
				}

				By("Step 9: Switch operator Subscription to Konflux CatalogSource")

				_, err = sbrutils.SwitchSubscriptionCatalog(
					APIClient, medik8sparams.UpgradeCatalogName)
				Expect(err).NotTo(HaveOccurred(),
					"Failed to switch Subscription to target catalog")

				By("Step 10: Wait for new CSV to reach Succeeded")

				var operatorUpgraded bool

				Eventually(func() error {
					csvs, listErr := olm.ListClusterServiceVersionWithNamePattern(
						APIClient, medik8sparams.OperatorPackage, medik8sparams.OperatorNs)
					if listErr != nil {
						return listErr
					}

					for _, csv := range csvs {
						csvPhase, _ := csv.GetPhase()
						if csvPhase != olmV1alpha1.CSVPhaseSucceeded {
							continue
						}

						if csv.Object.Name != previousCSV.Object.Name {
							GinkgoWriter.Printf("New CSV: %s (was: %s)\n",
								csv.Object.Name, previousCSV.Object.Name)

							operatorUpgraded = true
						} else {
							GinkgoWriter.Printf(
								"Version parity: Konflux catalog offers same version %s as GA\n",
								csv.Object.Name)
						}

						return nil
					}

					return fmt.Errorf("SBR CSV not yet in Succeeded phase after catalog switch")
				}, medik8sparams.OperatorUpgradeTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
					"Operator upgrade or catalog switch verification failed")

				if operatorUpgraded {
					By("Step 11: Verify SBR controller pods restarted with new image")

					Eventually(func() error {
						currentImage, imgErr := sbrutils.GetSBRControllerImage(APIClient)
						if imgErr != nil {
							return imgErr
						}

						if currentImage == preUpgradeImage {
							return fmt.Errorf("controller still running old image %s", preUpgradeImage)
						}

						GinkgoWriter.Printf("Controller image updated: %s\n", currentImage)

						return nil
					}, medik8sparams.OperatorUpgradeTimeout,
						sbrparams.DefaultPollInterval).Should(Succeed(),
						"SBR controller pods did not restart with new image")
				} else {
					GinkgoWriter.Println(
						"Step 11: Skipped (no operator upgrade occurred, " +
							"Konflux and GA catalogs at same version)")
				}

				By("Step 12: Validate SBR agent DaemonSet on OCP N (post-catalog-switch)")

				runSBRCFunctionCheck("post-catalog-switch")
			})
	})

// upgradeOCP triggers an OCP cluster upgrade and waits for completion.
func upgradeOCP(ctx context.Context, targetImage string) {
	clusterVersion := &configv1.ClusterVersion{}
	Expect(APIClient.Get(ctx, client.ObjectKey{Name: "version"}, clusterVersion)).
		To(Succeed(), "Failed to get ClusterVersion")

	clusterVersion.Spec.DesiredUpdate = &configv1.Update{
		Image: targetImage,
		Force: true, // CI release images lack signed update graph metadata
	}

	Expect(APIClient.Update(ctx, clusterVersion)).
		To(Succeed(), "Failed to set desired OCP update")

	GinkgoWriter.Printf("OCP upgrade initiated to image: %s\n", targetImage)

	Expect(helpers.WaitForClusterVersionCondition(ctx, APIClient,
		"Progressing", configv1.ConditionTrue,
		medik8sparams.OCPUpgradeStartTimeout, sbrparams.DefaultPollInterval,
	)).To(Succeed(), "OCP upgrade did not start progressing")

	Expect(helpers.WaitForClusterVersionCondition(ctx, APIClient,
		"Progressing", configv1.ConditionFalse,
		medik8sparams.OCPUpgradeTimeout, sbrparams.DefaultPollInterval,
	)).To(Succeed(), "OCP upgrade did not complete (still Progressing)")

	Expect(helpers.WaitForClusterVersionCondition(ctx, APIClient,
		"Available", configv1.ConditionTrue,
		medik8sparams.PostUpgradeRecoveryTimeout, sbrparams.DefaultPollInterval,
	)).To(Succeed(), "Cluster not Available after OCP upgrade")

	Expect(helpers.WaitForClusterVersionCondition(ctx, APIClient,
		"Failing", configv1.ConditionFalse,
		medik8sparams.PostUpgradeRecoveryTimeout, sbrparams.DefaultPollInterval,
	)).To(Succeed(), "Cluster is Failing after OCP upgrade")

	GinkgoWriter.Println("OCP upgrade completed and cluster is healthy")
}

// runSBRCFunctionCheck creates (or reuses) the upgrade-test SBRC and waits for
// its agent DaemonSet to reach Ready, then tears it down -- proving the
// controller, agent image, and DaemonSet-management chain all function at
// this point in the upgrade sequence.
func runSBRCFunctionCheck(phase string) {
	storageClass := discoverRWXStorageClass()

	By(fmt.Sprintf("[%s] Creating SBRC %s (storageClass=%s)", phase, sbrparams.UpgradeSBRCName, storageClass))

	sbrc := buildSBRC(sbrparams.UpgradeSBRCName, map[string]interface{}{
		"sharedStorageClass": storageClass,
	})
	Expect(APIClient.Create(context.TODO(), sbrc)).To(Succeed(),
		"[%s] Failed to create SBRC %s", phase, sbrparams.UpgradeSBRCName)

	By(fmt.Sprintf("[%s] Waiting for SBRC %s agent DaemonSet to be Ready", phase, sbrparams.UpgradeSBRCName))

	waitForSBRCReady(sbrparams.UpgradeSBRCName)

	GinkgoWriter.Printf("[%s] SBRC %s agent DaemonSet Ready\n", phase, sbrparams.UpgradeSBRCName)

	cleanupUpgradeSBRC()
}

// cleanupUpgradeSBRC deletes the upgrade-test SBRC and waits for its agent
// DaemonSet to be garbage-collected, so each phase starts from a clean slate.
func cleanupUpgradeSBRC() {
	sbrc := buildSBRC(sbrparams.UpgradeSBRCName, map[string]interface{}{})

	if delErr := APIClient.Delete(context.TODO(), sbrc); delErr != nil && !k8serrors.IsNotFound(delErr) {
		GinkgoWriter.Printf("WARNING: failed to delete SBRC %s: %v\n", sbrparams.UpgradeSBRCName, delErr)
	}

	dsName := sbrparams.SBRAgentDaemonSetPrefix + sbrparams.UpgradeSBRCName

	Eventually(func() bool {
		_, getErr := APIClient.DaemonSets(medik8sparams.OperatorNs).Get(
			context.TODO(), dsName, metav1.GetOptions{})

		return k8serrors.IsNotFound(getErr)
	}, sbrparams.SBRCDaemonSetGCTimeout, sbrparams.DefaultPollInterval).Should(BeTrue(),
		"agent DaemonSet %s was not garbage-collected after SBRC deletion", dsName)
}
