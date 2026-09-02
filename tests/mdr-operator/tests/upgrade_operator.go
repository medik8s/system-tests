package tests

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/deployment"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/olm"
	olmV1alpha1 "github.com/rh-ecosystem-edge/eco-goinfra/pkg/schemes/olm/operators/v1alpha1"

	"github.com/medik8s/system-tests/tests/internal/helpers"
	"github.com/medik8s/system-tests/tests/internal/labels"
	. "github.com/medik8s/system-tests/tests/internal/medik8sinittools"
	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
	"github.com/medik8s/system-tests/tests/mdr-operator/internal/mdrparams"
	"github.com/medik8s/system-tests/tests/mdr-operator/internal/mdrutils"
)

// Unlike FAR/SNR, MDR has no standalone remediation trigger -- it is always
// invoked by NHC via a MachineDeletionRemediationTemplate. This spec exercises
// only the operator/catalog upgrade path (GA -> Konflux pre-GA catalog); the
// OCP N-1 -> N cluster upgrade path is exercised independently by
// "MDR Cluster Upgrade" in upgrade_cluster.go. Each spec is fully
// self-contained (its own fresh GA install).
var _ = Describe("MDR Operator Upgrade",
	Serial, Ordered,
	Label(labels.OperatorMDR, mdrparams.Label,
		labels.TierUpgradeOperator, labels.DisruptionDestructive,
		labels.PlatformAny, labels.FrequencyWeekly,
		labels.ComponentOLM),
	func() {
		var (
			ctx                context.Context
			previousCSV        *olm.ClusterServiceVersionBuilder
			preUpgradeImage    string
			initialWorkerCount int
			initialWorkerNames map[string]bool
			targetWorkerName   string
		)

		BeforeAll(func() {
			ctx = context.Background()
			initialWorkerCount, initialWorkerNames = setupMDRUpgradePrereqs(ctx)
			createMDRUpgradeCRs(ctx)
		})

		AfterAll(func() {
			cleanupNHCCR(mdrparams.UpgradeNHCName)
			cleanupMDRT(mdrparams.UpgradeMDRTName)
			mdrutils.CleanupUpgradeResources(APIClient, GinkgoWriter.Printf)
		})

		JustAfterEach(func() {
			mdrUpgradeSafetyNet(ctx, func() string { return targetWorkerName }, initialWorkerCount)
		})

		It("should survive operator upgrade with working remediation",
			Label(labels.ComponentRemediation),
			func() {
				By("Step 1: Install MDR operator GA version from redhat-operators")

				_, err := mdrutils.InstallGAOperator(APIClient)
				Expect(err).NotTo(HaveOccurred(), "Failed to install GA MDR operator")

				By("Step 2: Wait for CSV to reach Succeeded and deployment to be Ready")

				Eventually(func() error {
					var csvErr error
					previousCSV, csvErr = mdrutils.FindSucceededCSV(
						APIClient, medik8sparams.OperatorPackage)

					return csvErr
				}, medik8sparams.OperatorUpgradeTimeout, mdrparams.DefaultPollInterval).Should(Succeed(),
					"MDR CSV must reach Succeeded phase")

				mdrDeploy, err := deployment.Pull(
					APIClient, mdrparams.OperatorDeploymentName, medik8sparams.OperatorNs)
				Expect(err).NotTo(HaveOccurred(), "Failed to get MDR deployment")
				Expect(mdrDeploy.IsReady(medik8sparams.DefaultTimeout)).To(BeTrue(),
					"MDR deployment is not Ready")

				By("Step 3: Record GA install checkpoint")

				preUpgradeImage, err = mdrutils.GetMDRControllerImage(APIClient)
				Expect(err).NotTo(HaveOccurred())
				GinkgoWriter.Printf("GA operator image: %s\n", preUpgradeImage)
				GinkgoWriter.Printf("GA MDR CSV: %s\n", previousCSV.Object.Name)

				By("Step 4: Validate GA MDR remediation baseline")

				targetWorkerName, err = runMDRRemediationCycle(ctx, initialWorkerCount,
					initialWorkerNames, "pre-catalog-switch")
				Expect(err).NotTo(HaveOccurred(),
					"Baseline remediation failed with GA operator")

				delete(initialWorkerNames, targetWorkerName)

				By("Cleaning up MDR CR from baseline remediation")
				cleanupMDRCR(targetWorkerName)
				initialWorkerNames[targetWorkerName] = true

				By("Step 5: Apply deferred IDMS for Konflux catalog images")

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

				By("Step 6: Switch operator Subscription to Konflux CatalogSource")

				_, err = mdrutils.SwitchSubscriptionCatalog(
					APIClient, medik8sparams.UpgradeCatalogName)
				Expect(err).NotTo(HaveOccurred(),
					"Failed to switch Subscription to target catalog")

				By("Step 7: Wait for new CSV to reach Succeeded")

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

					return fmt.Errorf("MDR CSV not yet in Succeeded phase after catalog switch")
				}, medik8sparams.OperatorUpgradeTimeout, mdrparams.DefaultPollInterval).Should(Succeed(),
					"Operator upgrade or catalog switch verification failed")

				if operatorUpgraded {
					By("Step 8: Verify MDR controller pods restarted with new image")

					Eventually(func() error {
						currentImage, imgErr := mdrutils.GetMDRControllerImage(APIClient)
						if imgErr != nil {
							return imgErr
						}

						if currentImage == preUpgradeImage {
							return fmt.Errorf("controller still running old image %s", preUpgradeImage)
						}

						GinkgoWriter.Printf("Controller image updated: %s\n", currentImage)

						return nil
					}, medik8sparams.OperatorUpgradeTimeout,
						mdrparams.DefaultPollInterval).Should(Succeed(),
						"MDR controller pods did not restart with new image")
				} else {
					GinkgoWriter.Println(
						"Step 8: Skipped (no operator upgrade occurred, " +
							"Konflux and GA catalogs at same version)")
				}

				By("Step 9: Validate MDR on upgraded operator (post-catalog-switch remediation)")

				targetWorkerName, err = runMDRRemediationCycle(ctx, initialWorkerCount,
					initialWorkerNames, "post-catalog-switch")
				Expect(err).NotTo(HaveOccurred(),
					"Post-catalog-switch remediation failed")
			})
	})
