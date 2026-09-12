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
	"github.com/medik8s/system-tests/tests/nmo-operator/internal/nmoparams"
	"github.com/medik8s/system-tests/tests/nmo-operator/internal/nmoutils"
)

// This spec exercises only the OCP N-1 -> N cluster upgrade path; the
// operator/catalog upgrade path is exercised independently by
// "NMO Operator Upgrade" in upgrade_operator.go. Each spec is fully
// self-contained (its own fresh GA install). If MEDIK8S_SKIP_OCP_UPGRADE=true,
// this whole spec is skipped -- there's nothing left to test if the one thing
// it exists for doesn't run.
var _ = Describe("NMO Cluster Upgrade",
	Serial, Ordered,
	Label(labels.OperatorNMO, nmoparams.Label,
		labels.TierUpgradeCluster, labels.DisruptionDestructive,
		labels.PlatformAny, labels.FrequencyWeekly,
		labels.ComponentOLM),
	func() {
		var (
			ctx           context.Context
			currentNMName string
			currentNMNode string
		)

		BeforeAll(func() {
			ctx = context.Background()
			registerNMOScheme()

			if medik8sparams.SkipOCPUpgrade {
				Skip("MEDIK8S_SKIP_OCP_UPGRADE=true: nothing to test in the cluster-upgrade-only spec")
			}

			Expect(medik8sparams.TargetOCPImage).NotTo(BeEmpty(),
				"OPENSHIFT_UPGRADE_RELEASE_IMAGE_OVERRIDE or RELEASE_IMAGE_LATEST must be set")
		})

		AfterAll(func() {
			nmoutils.CleanupUpgradeResources(APIClient, GinkgoWriter.Printf)
		})

		JustAfterEach(func() {
			nmoUpgradeSafetyNet(ctx, func() string { return currentNMName }, func() string { return currentNMNode })
		})

		It("should survive OCP upgrade with working maintenance",
			Label(labels.ComponentRemediation),
			func() {
				By("Step 1: Install NMO operator GA version from redhat-operators")

				_, err := nmoutils.InstallGAOperator(APIClient)
				Expect(err).NotTo(HaveOccurred(), "Failed to install GA NMO operator")

				By("Step 2: Wait for CSV to reach Succeeded and deployment to be Ready")

				var previousCSV *olm.ClusterServiceVersionBuilder

				Eventually(func() error {
					var csvErr error
					previousCSV, csvErr = nmoutils.FindSucceededCSV(
						APIClient, medik8sparams.OperatorPackage)

					return csvErr
				}, medik8sparams.OperatorUpgradeTimeout, nmoparams.DefaultPollInterval).Should(Succeed(),
					"NMO CSV must reach Succeeded phase")

				nmoDeploy, err := deployment.Pull(
					APIClient, nmoparams.OperatorDeploymentName, medik8sparams.OperatorNs)
				Expect(err).NotTo(HaveOccurred(), "Failed to get NMO deployment")
				Expect(nmoDeploy.IsReady(medik8sparams.DefaultTimeout)).To(BeTrue(),
					"NMO deployment is not Ready")

				GinkgoWriter.Printf("GA NMO CSV: %s\n", previousCSV.Object.Name)

				By("Step 3: Validate GA NMO maintenance cycle on OCP N-1")

				currentNMName, currentNMNode, err = runNMOMaintenanceCycle(ctx, "pre-ocp-upgrade")
				Expect(err).NotTo(HaveOccurred(), "Pre-OCP-upgrade maintenance cycle failed")

				By("Cleaning up NodeMaintenance from pre-OCP-upgrade cycle")
				endNMOMaintenanceCycle(ctx, currentNMName, currentNMNode)
				currentNMName, currentNMNode = "", ""

				By("Step 4: Upgrade OCP from N-1 to N")

				upgradeOCP(ctx, medik8sparams.TargetOCPImage)

				By("Step 5: Verify NMO operator survived OCP upgrade")

				Eventually(func() error {
					_, csvErr := nmoutils.FindSucceededCSV(
						APIClient, medik8sparams.OperatorPackage)

					return csvErr
				}, medik8sparams.PostUpgradeRecoveryTimeout, nmoparams.DefaultPollInterval).Should(Succeed(),
					"NMO CSV not in Succeeded phase after OCP upgrade")

				nmoDeploy, err = deployment.Pull(
					APIClient, nmoparams.OperatorDeploymentName, medik8sparams.OperatorNs)
				Expect(err).NotTo(HaveOccurred())
				Expect(nmoDeploy.IsReady(medik8sparams.PostUpgradeRecoveryTimeout)).To(BeTrue(),
					"NMO deployment not Ready after OCP upgrade")

				By("Step 6: Validate NMO maintenance cycle on OCP N")

				currentNMName, currentNMNode, err = runNMOMaintenanceCycle(ctx, "post-ocp-upgrade")
				Expect(err).NotTo(HaveOccurred(), "Post-OCP-upgrade maintenance cycle failed")
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
		medik8sparams.OCPUpgradeStartTimeout, nmoparams.DefaultPollInterval,
	)).To(Succeed(), "OCP upgrade did not start progressing")

	Expect(helpers.WaitForClusterVersionCondition(ctx, APIClient,
		"Progressing", configv1.ConditionFalse,
		medik8sparams.OCPUpgradeTimeout, nmoparams.DefaultPollInterval,
	)).To(Succeed(), "OCP upgrade did not complete (still Progressing)")

	Expect(helpers.WaitForClusterVersionCondition(ctx, APIClient,
		"Available", configv1.ConditionTrue,
		medik8sparams.PostUpgradeRecoveryTimeout, nmoparams.DefaultPollInterval,
	)).To(Succeed(), "Cluster not Available after OCP upgrade")

	Expect(helpers.WaitForClusterVersionCondition(ctx, APIClient,
		"Failing", configv1.ConditionFalse,
		medik8sparams.PostUpgradeRecoveryTimeout, nmoparams.DefaultPollInterval,
	)).To(Succeed(), "Cluster is Failing after OCP upgrade")

	GinkgoWriter.Println("OCP upgrade completed and cluster is healthy")
}
