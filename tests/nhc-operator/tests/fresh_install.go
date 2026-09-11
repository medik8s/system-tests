//nolint:lll // Exact evidence fields and scenario descriptions are more useful when kept intact.
package tests

import (
	"context"
	"crypto/rand"
	"time"

	"github.com/medik8s/system-tests/tests/internal/labels"
	. "github.com/medik8s/system-tests/tests/internal/medik8sinittools"
	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
	"github.com/medik8s/system-tests/tests/nhc-operator/internal/nhcparams"
	"github.com/medik8s/system-tests/tests/nhc-operator/internal/nhcutils"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/reportxml"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("NHC candidate fresh installation", Serial,
	Label(labels.OperatorNHC, nhcparams.Label, labels.TierFreshInstall,
		labels.DisruptionNonDestructive, labels.PlatformAny, labels.ComponentOLM), func() {
		var (
			ctx    context.Context
			inputs nhcparams.UpgradeInputs
		)

		BeforeEach(func() {
			ctx = context.Background()

			var err error

			inputs, err = nhcparams.LoadUpgradeInputs()
			Expect(err).NotTo(HaveOccurred())
			inputs, err = nhcutils.ResolveAndVerifyUpgradeInputs(ctx, inputs)
			Expect(err).NotTo(HaveOccurred())
			Expect(inputs.Namespace).To(Equal(medik8sparams.OperatorNs))

			clusterVersion := &configv1.ClusterVersion{}
			Expect(APIClient.Get(ctx, client.ObjectKey{Name: "version"}, clusterVersion)).To(Succeed())
			Expect(clusterVersion.Status.Desired.Version).To(HavePrefix("5.0."), "requires an OpenShift 5.0 cluster")
		})

		JustAfterEach(func() {
			if CurrentSpecReport().Failed() && inputs.Namespace != "" {
				AddReportEntry("nhc-fresh-install-failure-evidence", nhcutils.CollectFailureEvidence(ctx, inputs.Namespace))
			}
		})

		It("installs and reconciles the pinned candidate on a clean cluster", reportxml.ID("REPLACE_WITH_POLARION_ID"), func() {
			Expect(nhcutils.CheckClean(ctx, APIClient, inputs.Namespace)).To(Succeed())

			owned := &nhcutils.OwnedRun{
				API: APIClient, Namespace: inputs.Namespace, Token: rand.Text(),
				SDK: inputs.OperatorSDK, CleanupPackage: nhcutils.CleanupBundle,
			}

			DeferCleanup(func() {
				failed := CurrentSpecReport().Failed()

				cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
				defer cancel()

				err := owned.Cleanup(cleanupCtx)
				if err != nil {
					AddReportEntry("nhc-fresh-install-cleanup-failure", err.Error())
				}

				if !failed {
					Expect(err).NotTo(HaveOccurred(), "test-owned resources must be removed")
				}
			})

			Expect(owned.CreateNamespace(ctx)).To(Succeed())

			By("installing the pinned SNR prerequisite")

			owned.Packages = append(owned.Packages, inputs.SNRPackage)
			output, err := nhcutils.InstallBundle(ctx, inputs.OperatorSDK, inputs.Namespace, inputs.SNRBundle)
			GinkgoWriter.Printf("operator-sdk run bundle (SNR) output:\n%s\n", output)
			Expect(err).NotTo(HaveOccurred())
			Expect(waitForUpgradeAPI(ctx, upgradeTemplate(inputs.Namespace))).To(Succeed())
			Expect(owned.Create(ctx, buildSNRT(nhcparams.NHCUpgradeTemplateName))).To(Succeed())
			Expect(waitForSNRTemplate(ctx, nhcparams.NHCUpgradeTemplateName)).To(Succeed())

			By("fresh-installing the explicitly pinned candidate bundle")

			owned.Packages = append(owned.Packages, inputs.Package)
			output, err = nhcutils.InstallBundle(ctx, inputs.OperatorSDK, inputs.Namespace, inputs.CandidateBundle)
			GinkgoWriter.Printf("operator-sdk run bundle (candidate NHC) output:\n%s\n", output)
			Expect(err).NotTo(HaveOccurred())
			waitForNHCUpgradeCSV(inputs.Namespace, inputs.CandidateVersion, inputs.CandidateImage, "fresh candidate")

			candidateImage, err := nhcutils.GetNHCControllerImage(APIClient)
			Expect(err).NotTo(HaveOccurred())
			Expect(candidateImage).To(Equal(inputs.CandidateImage))

			By("requiring a fresh response from the newly installed controller")
			Expect(waitForUpgradeAPI(ctx, upgradeNHC())).To(Succeed())
			nhc := upgradeNHC()
			nhc.Object["spec"] = nhcutils.SafeSpec(nhcparams.NHCUpgradeTemplateName, inputs.Namespace, owned.Token)
			Expect(owned.Create(ctx, nhc)).To(Succeed())
			Expect(waitForPauseResponse(ctx, nhc.GetUID(), owned.Token, nhc.GetResourceVersion())).To(Succeed())
		})
	})
