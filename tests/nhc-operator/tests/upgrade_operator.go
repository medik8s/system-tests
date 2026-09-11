//nolint:lll // Scenario descriptions and exact evidence fields are more useful when kept intact.
package tests

import (
	"context"
	"crypto/rand"
	"fmt"
	"time"

	"github.com/medik8s/system-tests/tests/internal/helpers"
	"github.com/medik8s/system-tests/tests/internal/labels"
	. "github.com/medik8s/system-tests/tests/internal/medik8sinittools"
	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
	"github.com/medik8s/system-tests/tests/nhc-operator/internal/nhcparams"
	"github.com/medik8s/system-tests/tests/nhc-operator/internal/nhcutils"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/deployment"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/olm"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/reportxml"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("NHC operator bundle upgrade", Serial, Ordered,
	Label(labels.OperatorNHC, nhcparams.Label, labels.TierUpgradeOperator,
		labels.DisruptionNonDestructive, labels.PlatformAny, labels.ComponentOLM), func() {
		var (
			ctx        context.Context
			inputs     nhcparams.UpgradeInputs
			oldCSV     *olm.ClusterServiceVersionBuilder
			configUID  string
			configSpec map[string]interface{}
			owned      *nhcutils.OwnedRun
		)

		BeforeAll(func() {
			ctx = context.Background()

			var err error

			inputs, err = nhcparams.LoadUpgradeInputs()
			Expect(err).NotTo(HaveOccurred())
			inputs, err = nhcutils.ResolveAndVerifyUpgradeInputs(ctx, inputs)
			Expect(err).NotTo(HaveOccurred())
			Expect(inputs.Namespace).To(Equal(medik8sparams.OperatorNs), "NHC uses its established operator namespace")
			sdkVersion, err := nhcutils.RunOperatorSDK(ctx, inputs.OperatorSDK, "version")
			Expect(err).NotTo(HaveOccurred())
			Expect(sdkVersion).To(ContainSubstring("v1.42.2"))
			AddReportEntry("nhc-upgrade-sdk", sdkVersion)

			clusterVersion := &configv1.ClusterVersion{}
			Expect(APIClient.Get(ctx, client.ObjectKey{Name: "version"}, clusterVersion)).To(Succeed())
			Expect(clusterVersion.Status.Desired.Version).To(HavePrefix("5.0."), "requires an OpenShift 5.0 cluster")
			AddReportEntry("nhc-upgrade-inputs", map[string]string{
				"candidateBundle": inputs.CandidateBundle, "candidateCommit": inputs.CandidateCommit, "candidateImage": inputs.CandidateImage,
				"candidateVersion": inputs.CandidateVersion, "namespace": inputs.Namespace, "oldBundle": inputs.OldBundle,
				"oldImage": inputs.OldImage, "oldVersion": inputs.OldVersion, "package": inputs.Package,
				"sdk": inputs.OperatorSDK, "snrBundle": inputs.SNRBundle, "systemTestsRevision": inputs.TestRevision,
			})
		})

		JustAfterEach(func() {
			if CurrentSpecReport().Failed() && ctx != nil && inputs.Namespace != "" {
				AddReportEntry("nhc-upgrade-failure-evidence", nhcutils.CollectFailureEvidence(ctx, inputs.Namespace))
			}
		})

		It("installs a pinned old bundle and upgrades its preserved configuration", reportxml.ID("REPLACE_WITH_POLARION_ID"), func() {
			By("rejecting leftover resources owned by this standalone scenario")
			Expect(nhcutils.CheckClean(ctx, APIClient, inputs.Namespace)).To(Succeed())
			owned = &nhcutils.OwnedRun{API: APIClient, Namespace: inputs.Namespace,
				Token: rand.Text(), SDK: inputs.OperatorSDK, CleanupPackage: nhcutils.CleanupBundle}
			// Register before CREATE/install, including partial failures. A rejected
			// preflight never registers or invokes package cleanup.
			DeferCleanup(func() {
				failed := CurrentSpecReport().Failed()

				cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
				defer cancel()

				err := owned.Cleanup(cleanupCtx)
				if err != nil {
					AddReportEntry("nhc-upgrade-cleanup-failure", err.Error())
					AddReportEntry("nhc-upgrade-cleanup-evidence", nhcutils.CollectFailureEvidence(cleanupCtx, inputs.Namespace))
				}

				if !failed {
					Expect(err).NotTo(HaveOccurred(), "test-owned resources must be removed")
				}
			})
			Expect(owned.CreateNamespace(ctx)).To(Succeed())
			By("installing the pinned SNR prerequisite and its remediation template")

			owned.Packages = append(owned.Packages, inputs.SNRPackage)
			output, err := nhcutils.InstallBundle(ctx, inputs.OperatorSDK, inputs.Namespace, inputs.SNRBundle)
			GinkgoWriter.Printf("operator-sdk run bundle (SNR) output:\n%s\n", output)
			Expect(err).NotTo(HaveOccurred())
			Expect(waitForUpgradeAPI(ctx, upgradeTemplate(inputs.Namespace))).To(Succeed())
			Expect(owned.Create(ctx, buildSNRT(nhcparams.NHCUpgradeTemplateName))).To(Succeed())
			Expect(waitForSNRTemplate(ctx, nhcparams.NHCUpgradeTemplateName)).To(Succeed())
			By("installing the explicitly pinned older upstream NHC bundle")

			owned.Packages = append(owned.Packages, inputs.Package)
			output, err = nhcutils.InstallBundle(ctx, inputs.OperatorSDK, inputs.Namespace, inputs.OldBundle)
			GinkgoWriter.Printf("operator-sdk run bundle (old NHC) output:\n%s\n", output)
			Expect(err).NotTo(HaveOccurred())

			oldCSV = waitForNHCUpgradeCSV(inputs.Namespace, inputs.OldVersion, inputs.OldImage, "old")
			oldImage, err := nhcutils.GetNHCControllerImage(APIClient)
			Expect(err).NotTo(HaveOccurred())
			Expect(oldImage).To(Equal(inputs.OldImage))
			By("creating a safe, observable NodeHealthCheck configuration")
			Expect(waitForUpgradeAPI(ctx, upgradeNHC())).To(Succeed())
			nhc := upgradeNHC()
			nhc.Object["spec"] = nhcutils.SafeSpec(nhcparams.NHCUpgradeTemplateName, inputs.Namespace, owned.Token)
			Expect(owned.Create(ctx, nhc)).To(Succeed())
			Expect(waitForPauseResponse(ctx, nhc.GetUID(), owned.Token, nhc.GetResourceVersion())).To(Succeed())
			configUID, configSpec = captureNHCConfiguration(ctx)

			By("upgrading in place to the explicitly supplied candidate bundle")

			output, err = nhcutils.UpgradeBundle(ctx, inputs.OperatorSDK, inputs.Namespace, inputs.CandidateBundle)
			GinkgoWriter.Printf("operator-sdk run bundle-upgrade output:\n%s\n", output)
			Expect(err).NotTo(HaveOccurred(), "the old operator must not be uninstalled before upgrade")
			By("requiring a new CSV and the candidate version and image")

			newCSV := waitForNHCUpgradeCSV(inputs.Namespace, inputs.CandidateVersion, inputs.CandidateImage, "candidate")
			Expect(newCSV.Object.Name).NotTo(Equal(oldCSV.Object.Name), "version parity is not an upgrade")

			candidateImage, err := nhcutils.GetNHCControllerImage(APIClient)
			Expect(err).NotTo(HaveOccurred())
			Expect(candidateImage).To(Equal(inputs.CandidateImage))
			By("verifying the same configuration identity, specification, and reconciliation")

			uid, spec := captureNHCConfiguration(ctx)
			Expect(uid).To(Equal(configUID), "upgrade must preserve the existing NodeHealthCheck")
			Expect(spec).To(Equal(configSpec), "upgrade must preserve the NodeHealthCheck specification")
			By("requiring a fresh candidate-controller response to a unique pause request")

			probe := owned.Token + "-candidate"
			changeUpgradePause(ctx, types.UID(configUID), probe)
			By("restoring the original configuration and requiring another controller response")
			changeUpgradePause(ctx, types.UID(configUID), owned.Token)
			uid, spec = captureNHCConfiguration(ctx)
			Expect(uid).To(Equal(configUID))
			Expect(spec).To(Equal(configSpec))
		})
	})

func upgradeNHC() *unstructured.Unstructured {
	object := &unstructured.Unstructured{}
	object.SetGroupVersionKind(nhcGVK)
	object.SetName(nhcparams.NHCUpgradeTestName)

	return object
}

func upgradeTemplate(namespace string) *unstructured.Unstructured {
	object := &unstructured.Unstructured{}
	object.SetGroupVersionKind(snrtGVK)
	object.SetName(nhcparams.NHCUpgradeTemplateName)
	object.SetNamespace(namespace)

	return object
}

func waitForSNRTemplate(ctx context.Context, name string) error {
	return wait.PollUntilContextTimeout(ctx, nhcparams.DefaultPollInterval, medik8sparams.DefaultTimeout, true, func(context.Context) (bool, error) {
		object := upgradeTemplate(medik8sparams.OperatorNs)
		object.SetName(name)
		err := APIClient.Get(ctx, client.ObjectKeyFromObject(object), object)

		return err == nil, client.IgnoreNotFound(err)
	})
}

func waitForNHCUpgradeCSV(namespace, expectedVersion, expectedImage, phase string) *olm.ClusterServiceVersionBuilder {
	var found *olm.ClusterServiceVersionBuilder

	Eventually(func(assertion Gomega) {
		csv, err := helpers.FindSucceededCSV(APIClient, nhcparams.CSVNamePattern, namespace)
		assertion.Expect(err).NotTo(HaveOccurred())
		assertion.Expect(csv.Object.Spec.Version.String()).To(Equal(expectedVersion))

		controller, err := deployment.Pull(APIClient, nhcparams.OperatorDeploymentName, namespace)
		assertion.Expect(err).NotTo(HaveOccurred())
		assertion.Expect(controller.IsReady(medik8sparams.DefaultTimeout)).To(BeTrue())

		pods := &corev1.PodList{}
		assertion.Expect(APIClient.List(context.Background(), pods, client.InNamespace(namespace),
			client.MatchingLabels(controller.Object.Spec.Selector.MatchLabels))).To(Succeed())
		assertion.Expect(pods.Items).NotTo(BeEmpty())

		for _, pod := range pods.Items {
			// Even a terminating old controller must be gone before the probe.
			assertion.Expect(pod.DeletionTimestamp).To(BeNil())

			managerFound := false

			for _, container := range pod.Spec.Containers {
				if container.Name == nhcparams.ManagerContainerName {
					managerFound = true

					assertion.Expect(container.Image).To(Equal(expectedImage))
				}
			}

			assertion.Expect(managerFound).To(BeTrue())
		}

		found = csv
	}, 15*time.Minute, nhcparams.DefaultPollInterval).Should(Succeed(), "%s NHC CSV did not become ready", phase)
	GinkgoWriter.Printf("%s NHC CSV: %s version=%s\n", phase, found.Object.Name, found.Object.Spec.Version.String())

	return found
}

func waitForUpgradeAPI(ctx context.Context, object *unstructured.Unstructured) error {
	return wait.PollUntilContextTimeout(ctx, nhcparams.DefaultPollInterval, medik8sparams.DefaultTimeout, true, func(ctx context.Context) (bool, error) {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(object.GroupVersionKind().GroupVersion().WithKind(object.GetKind() + "List"))

		err := APIClient.List(ctx, list, client.InNamespace(object.GetNamespace()))
		if nhcutils.MissingAPI(err) {
			return false, nil
		}

		return err == nil, err
	})
}

func waitForPauseResponse(ctx context.Context, uid types.UID, token, previousRV string) error {
	return wait.PollUntilContextTimeout(ctx, nhcparams.DefaultPollInterval, medik8sparams.DefaultTimeout, true, func(ctx context.Context) (bool, error) {
		object := upgradeNHC()
		if err := APIClient.Get(ctx, client.ObjectKeyFromObject(object), object); err != nil {
			return false, err
		}

		if object.GetUID() != uid {
			return false, fmt.Errorf("NHC UID changed during reconciliation probe")
		}

		return nhcutils.PauseResponse(object, uid, token, previousRV), nil
	})
}

func changeUpgradePause(ctx context.Context, uid types.UID, token string) {
	object := upgradeNHC()
	Expect(APIClient.Get(ctx, client.ObjectKeyFromObject(object), object)).To(Succeed())
	Expect(object.GetUID()).To(Equal(uid))
	original := object.DeepCopy()
	Expect(unstructured.SetNestedStringSlice(object.Object, []string{token}, "spec", "pauseRequests")).To(Succeed())
	Expect(APIClient.Patch(ctx, object, client.MergeFromWithOptions(original, client.MergeFromWithOptimisticLock{}))).To(Succeed())
	Expect(waitForPauseResponse(ctx, uid, token, object.GetResourceVersion())).To(Succeed())
}

func captureNHCConfiguration(ctx context.Context) (string, map[string]interface{}) {
	nhc := upgradeNHC()
	Expect(APIClient.Get(ctx, client.ObjectKeyFromObject(nhc), nhc)).To(Succeed())
	spec, found, err := unstructured.NestedMap(nhc.Object, "spec")
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())

	return string(nhc.GetUID()), spec
}
