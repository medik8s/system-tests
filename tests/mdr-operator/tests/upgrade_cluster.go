package tests

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"

	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/deployment"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/olm"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/medik8s/system-tests/tests/internal/helpers"
	"github.com/medik8s/system-tests/tests/internal/labels"
	. "github.com/medik8s/system-tests/tests/internal/medik8sinittools"
	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
	"github.com/medik8s/system-tests/tests/mdr-operator/internal/mdrparams"
	"github.com/medik8s/system-tests/tests/mdr-operator/internal/mdrutils"
)

// This spec exercises only the OCP N-1 -> N cluster upgrade path; the
// operator/catalog upgrade path is exercised independently by
// "MDR Operator Upgrade" in upgrade_operator.go. Each spec is fully
// self-contained (its own fresh GA install). If MEDIK8S_SKIP_OCP_UPGRADE=true,
// this whole spec is skipped -- there's nothing left to test if the one thing
// it exists for doesn't run.
var _ = Describe("MDR Cluster Upgrade",
	Serial, Ordered,
	Label(labels.OperatorMDR, mdrparams.Label,
		labels.TierUpgradeCluster, labels.DisruptionDestructive,
		labels.PlatformAny, labels.FrequencyWeekly,
		labels.ComponentOLM),
	func() {
		var (
			ctx                context.Context
			initialWorkerCount int
			initialWorkerNames map[string]bool
			targetWorkerName   string
		)

		BeforeAll(func() {
			ctx = context.Background()

			if medik8sparams.SkipOCPUpgrade {
				Skip("MEDIK8S_SKIP_OCP_UPGRADE=true: nothing to test in the cluster-upgrade-only spec")
			}

			Expect(medik8sparams.TargetOCPImage).NotTo(BeEmpty(),
				"OPENSHIFT_UPGRADE_RELEASE_IMAGE_OVERRIDE or RELEASE_IMAGE_LATEST must be set")

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

		It("should survive OCP upgrade with working remediation",
			Label(labels.ComponentRemediation),
			func() {
				By("Step 1: Install MDR operator GA version from redhat-operators")

				_, err := mdrutils.InstallGAOperator(APIClient)
				Expect(err).NotTo(HaveOccurred(), "Failed to install GA MDR operator")

				By("Step 2: Wait for CSV to reach Succeeded and deployment to be Ready")

				var previousCSV *olm.ClusterServiceVersionBuilder

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

				GinkgoWriter.Printf("GA MDR CSV: %s\n", previousCSV.Object.Name)

				By("Step 3: Validate GA MDR remediation on OCP N-1")

				targetWorkerName, err = runMDRRemediationCycle(ctx, initialWorkerCount,
					initialWorkerNames, "pre-ocp-upgrade")
				Expect(err).NotTo(HaveOccurred(),
					"Pre-OCP-upgrade remediation failed with GA operator")

				delete(initialWorkerNames, targetWorkerName)

				By("Cleaning up MDR CR from pre-OCP-upgrade remediation")
				cleanupMDRCR(targetWorkerName)
				initialWorkerNames[targetWorkerName] = true

				By("Step 4: Upgrade OCP from N-1 to N")

				upgradeOCP(ctx, medik8sparams.TargetOCPImage)

				By("Step 5: Verify MDR operator survived OCP upgrade")

				Eventually(func() error {
					_, csvErr := mdrutils.FindSucceededCSV(
						APIClient, medik8sparams.OperatorPackage)

					return csvErr
				}, medik8sparams.PostUpgradeRecoveryTimeout, mdrparams.DefaultPollInterval).Should(Succeed(),
					"MDR CSV not in Succeeded phase after OCP upgrade")

				mdrDeploy, err = deployment.Pull(
					APIClient, mdrparams.OperatorDeploymentName, medik8sparams.OperatorNs)
				Expect(err).NotTo(HaveOccurred())
				Expect(mdrDeploy.IsReady(medik8sparams.PostUpgradeRecoveryTimeout)).To(BeTrue(),
					"MDR deployment not Ready after OCP upgrade")

				By("Step 6: Validate MDR remediation on OCP N")

				targetWorkerName, err = runMDRRemediationCycle(ctx, initialWorkerCount,
					initialWorkerNames, "post-ocp-upgrade")
				Expect(err).NotTo(HaveOccurred(),
					"Post-OCP-upgrade remediation failed")
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
		medik8sparams.OCPUpgradeStartTimeout, mdrparams.DefaultPollInterval,
	)).To(Succeed(), "OCP upgrade did not start progressing")

	Expect(helpers.WaitForClusterVersionCondition(ctx, APIClient,
		"Progressing", configv1.ConditionFalse,
		medik8sparams.OCPUpgradeTimeout, mdrparams.DefaultPollInterval,
	)).To(Succeed(), "OCP upgrade did not complete (still Progressing)")

	Expect(helpers.WaitForClusterVersionCondition(ctx, APIClient,
		"Available", configv1.ConditionTrue,
		medik8sparams.PostUpgradeRecoveryTimeout, mdrparams.DefaultPollInterval,
	)).To(Succeed(), "Cluster not Available after OCP upgrade")

	Expect(helpers.WaitForClusterVersionCondition(ctx, APIClient,
		"Failing", configv1.ConditionFalse,
		medik8sparams.PostUpgradeRecoveryTimeout, mdrparams.DefaultPollInterval,
	)).To(Succeed(), "Cluster is Failing after OCP upgrade")

	GinkgoWriter.Println("OCP upgrade completed and cluster is healthy")
}
