package tests

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/deployment"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/reportxml"

	"github.com/medik8s/system-tests/tests/far-operator/internal/farparams"
	"github.com/medik8s/system-tests/tests/far-operator/internal/farutils"
	"github.com/medik8s/system-tests/tests/internal/labels"
	. "github.com/medik8s/system-tests/tests/internal/medik8sinittools"
	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
)

var _ = Describe("FAR Controller Lifecycle Tests",
	Serial,
	Label(labels.OperatorFAR, farparams.Label,
		labels.DisruptionNonDestructive),
	func() {
		var ctx context.Context

		BeforeEach(func() {
			ctx = context.Background()

			By("Verifying FAR controller deployment is Ready")

			farDeployment, err := deployment.Pull(
				APIClient, farparams.OperatorDeploymentName, medik8sparams.OperatorNs)
			Expect(err).ToNot(HaveOccurred(), "Failed to get FAR deployment")
			Expect(farDeployment.IsReady(medik8sparams.DefaultTimeout)).To(BeTrue(),
				"FAR deployment is not Ready")
		})

		It("should transfer controller leadership when the active pod is deleted",
			Label(labels.TierAcceptance, labels.ComponentController),
			reportxml.ID("70636"),
			func() {
				By("Getting the current active FAR controller pod")

				// Leader election is eventually-consistent: a prior destructive spec
				// (the 0-worker test deletes FAR pods; fencing specs evict them) can
				// leave the controller Lease naming a pod that no longer exists until
				// re-election updates it. Retry until the Lease resolves to a live,
				// Running controller pod instead of failing on a one-shot lookup.
				var (
					oldLeaderPod  *corev1.Pod
					oldLeaderNode string
				)

				Eventually(func(assertion Gomega) {
					pods, err := farutils.GetFARControllerPods(ctx, APIClient)
					assertion.Expect(err).ToNot(HaveOccurred())
					assertion.Expect(pods).ToNot(BeEmpty(), "No running FAR controller pods found")

					leaderNode, err := farutils.GetActiveFARControllerNode(ctx, APIClient)
					assertion.Expect(err).ToNot(HaveOccurred())

					oldLeaderPod = nil

					for i := range pods {
						if pods[i].Spec.NodeName == leaderNode {
							oldLeaderPod = &pods[i]

							break
						}
					}

					assertion.Expect(oldLeaderPod).ToNot(BeNil(),
						"Lease leader node %s has no Running controller pod yet", leaderNode)
					oldLeaderNode = leaderNode
				}, farparams.ControllerHandoverTimeout, farparams.DefaultPollInterval).Should(Succeed(),
					"controller lease did not resolve to a live controller pod")

				oldPodName := oldLeaderPod.Name
				GinkgoWriter.Printf("Active controller pod: %s on node %s\n",
					oldPodName, oldLeaderNode)

				By("Deleting the active controller pod " + oldPodName)

				Expect(APIClient.Delete(ctx, oldLeaderPod)).To(Succeed())

				By("Waiting for FAR controller deployment to become ready")

				farDeployment, err := deployment.Pull(
					APIClient, farparams.OperatorDeploymentName, medik8sparams.OperatorNs)
				Expect(err).ToNot(HaveOccurred(), "Failed to pull FAR controller deployment")
				Expect(farDeployment.IsReady(farparams.ControllerHandoverTimeout)).To(BeTrue(),
					"FAR deployment did not become ready after pod deletion")

				By("Verifying controller lease transferred to a different pod")

				Eventually(func(assertion Gomega) {
					lease := &coordinationv1.Lease{}
					assertion.Expect(APIClient.Get(ctx, client.ObjectKey{
						Name:      farparams.ControllerLeaseName,
						Namespace: medik8sparams.OperatorNs,
					}, lease)).To(Succeed())
					assertion.Expect(lease.Spec.HolderIdentity).ToNot(BeNil(),
						"Lease has no holder after pod deletion")

					if lease.Spec.HolderIdentity != nil {
						assertion.Expect(*lease.Spec.HolderIdentity).ToNot(Equal(oldPodName),
							"Lease is still held by deleted pod %s", oldPodName)
					}

					newPods, err := farutils.GetFARControllerPods(ctx, APIClient)
					assertion.Expect(err).ToNot(HaveOccurred())

					hasNewRunningPod := false

					for _, p := range newPods {
						if p.Name != oldPodName && p.Status.Phase == corev1.PodRunning {
							hasNewRunningPod = true

							break
						}
					}

					assertion.Expect(hasNewRunningPod).To(BeTrue(),
						"No new Running controller pod found after deleting %s", oldPodName)
				}, farparams.ControllerHandoverTimeout, farparams.DefaultPollInterval).Should(Succeed(),
					"Controller leadership did not transfer after pod deletion")
			})
	})
