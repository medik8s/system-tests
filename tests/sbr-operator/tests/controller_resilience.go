package tests

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	gomegatypes "github.com/onsi/gomega/types"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/deployment"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/pod"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/reportxml"

	"github.com/medik8s/system-tests/tests/internal/helpers"
	"github.com/medik8s/system-tests/tests/internal/labels"
	. "github.com/medik8s/system-tests/tests/internal/medik8sinittools"
	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
	"github.com/medik8s/system-tests/tests/sbr-operator/internal/sbrparams"
)

// setNodeUnschedulable patches a node's spec.unschedulable field.
func setNodeUnschedulable(ctx context.Context, nodeName string, unschedulable bool) error {
	patch := []byte(fmt.Sprintf(`{"spec":{"unschedulable":%t}}`, unschedulable))

	_, err := APIClient.CoreV1Interface.Nodes().Patch(
		ctx, nodeName, types.MergePatchType, patch, metav1.PatchOptions{})

	return err
}

// uncordonNodesBestEffort uncordons every node in the list, logging warnings on failure.
// Intended for DeferCleanup/AfterAll where masking the original failure is worse than
// leaving a node cordoned.
func uncordonNodesBestEffort(ctx context.Context, nodeNames []string) {
	for _, name := range nodeNames {
		if err := setNodeUnschedulable(ctx, name, false); err != nil {
			msg := fmt.Sprintf("cleanup failed to uncordon node %s: %v", name, err)
			GinkgoWriter.Printf("Warning: %s\n", msg)
			AddReportEntry("uncordon-cleanup-failed", msg)
		}
	}
}

// listRunningControllerPods returns SBR controller pods that are Running, not terminating,
// and have all containers ready.
func listRunningControllerPods() ([]*pod.Builder, error) {
	controllerPods, err := pod.List(
		APIClient, medik8sparams.OperatorNs,
		metav1.ListOptions{LabelSelector: sbrparams.OperatorControllerPodLabelSelector})
	if err != nil {
		return nil, fmt.Errorf("listing SBR controller pods in %s: %w", medik8sparams.OperatorNs, err)
	}

	return helpers.FilterRunningPods(controllerPods), nil
}

// uniqueNodeNames returns the set of distinct node names that the given pods are scheduled on.
func uniqueNodeNames(pods []*pod.Builder) map[string]bool {
	nodes := make(map[string]bool, len(pods))
	for _, p := range pods {
		if p.Object.Spec.NodeName != "" {
			nodes[p.Object.Spec.NodeName] = true
		}
	}

	return nodes
}

// waitForPodTermination polls until the named pod in the operator namespace is gone
// (NotFound), surfacing any non-NotFound Get error instead of masking it as "still present".
func waitForPodTermination(ctx context.Context, podName string) {
	GinkgoHelper()

	Eventually(func() error {
		_, getErr := APIClient.CoreV1Interface.Pods(medik8sparams.OperatorNs).Get(
			ctx, podName, metav1.GetOptions{})
		if k8serrors.IsNotFound(getErr) {
			return nil
		}

		if getErr != nil {
			return fmt.Errorf("getting pod %s: %w", podName, getErr)
		}

		return fmt.Errorf("pod %s still exists", podName)
	}, medik8sparams.DefaultTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
		"Pod %s did not terminate", podName)
}

// pullControllerDeployment pulls the SBR controller-manager deployment using the fixed
// operator deployment name and namespace. The error is returned (never collapsed to zero) so
// pollers surface transient API faults instead of masking them as "not ready".
func pullControllerDeployment() (*deployment.Builder, error) {
	dep, err := deployment.Pull(APIClient, sbrparams.OperatorDeploymentName, medik8sparams.OperatorNs)
	if err != nil {
		return nil, fmt.Errorf("pulling SBR controller deployment %s/%s: %w",
			medik8sparams.OperatorNs, sbrparams.OperatorDeploymentName, err)
	}

	return dep, nil
}

// controllerReadyReplicas returns the SBR controller deployment's ReadyReplicas count,
// propagating any Pull error rather than reporting zero.
func controllerReadyReplicas() (int32, error) {
	dep, err := pullControllerDeployment()
	if err != nil {
		return 0, err
	}

	return dep.Object.Status.ReadyReplicas, nil
}

// waitForControllerReadyReplicas polls the controller deployment's ReadyReplicas until the
// count satisfies matcher or timeout elapses. A transient Pull error fails the poll iteration
// (surfacing on timeout) instead of being masked as zero replicas.
func waitForControllerReadyReplicas(
	timeout time.Duration,
	matcher gomegatypes.GomegaMatcher,
	messageArgs ...interface{},
) {
	GinkgoHelper()

	Eventually(func(g Gomega) int32 {
		replicas, err := controllerReadyReplicas()
		g.Expect(err).ToNot(HaveOccurred())

		return replicas
	}, timeout, sbrparams.DefaultPollInterval).Should(matcher, messageArgs...)
}

var _ = Describe(
	"SBR Controller Resilience",
	Ordered,
	ContinueOnFailure,
	Serial,
	Label(labels.OperatorSBR), func() {
		BeforeAll(func() {
			By("Verifying SBR controller deployment is Ready")

			sbrDeployment, err := deployment.Pull(
				APIClient, sbrparams.OperatorDeploymentName, medik8sparams.OperatorNs)
			Expect(err).ToNot(HaveOccurred(), "Failed to get SBR deployment")
			Expect(sbrDeployment.IsReady(medik8sparams.DefaultTimeout)).To(BeTrue(),
				"SBR deployment is not Ready")
		})

		It("should maintain controller availability with one worker",
			reportxml.ID("90306"),
			Label(
				labels.DisruptionDestructive,
				labels.TierAcceptance,
				labels.FrequencyWeekly,
				labels.PlatformAny,
				labels.ComponentController,
			), func() {
				ctx := context.Background()

				By("Listing schedulable worker-only nodes")

				workerNodes, err := helpers.ListSchedulableWorkerNodes(ctx, APIClient)
				Expect(err).ToNot(HaveOccurred(), "Failed to list worker nodes")

				if len(workerNodes) < sbrparams.MinWorkerNodesForResilienceTest {
					Skip(fmt.Sprintf(
						"Test requires at least %d schedulable worker-only nodes, found %d",
						sbrparams.MinWorkerNodesForResilienceTest, len(workerNodes)))
				}

				By("Verifying SBR controller starts with expected replicas on different nodes")

				var initialPods []*pod.Builder

				Eventually(func(assertion Gomega) {
					pods, listErr := listRunningControllerPods()
					assertion.Expect(listErr).ToNot(HaveOccurred())
					assertion.Expect(len(pods)).To(Equal(int(sbrparams.ExpectedReplicas)),
						"expected %d running controller pods", sbrparams.ExpectedReplicas)
					assertion.Expect(len(uniqueNodeNames(pods))).To(Equal(int(sbrparams.ExpectedReplicas)),
						"controller pods must run on different nodes for HA")

					initialPods = pods
				}, medik8sparams.DefaultTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
					"SBR controller did not stabilize at %d ready replicas on distinct nodes",
					sbrparams.ExpectedReplicas)

				By("Selecting one worker to keep (one already hosting a controller pod)")

				controllerNodeNames := uniqueNodeNames(initialPods)
				workerNodeNames := make(map[string]bool, len(workerNodes))

				for _, node := range workerNodes {
					workerNodeNames[node.Name] = true
				}

				var keeperNode string

				for _, p := range initialPods {
					if workerNodeNames[p.Object.Spec.NodeName] {
						keeperNode = p.Object.Spec.NodeName

						break
					}
				}

				if keeperNode == "" {
					Skip(fmt.Sprintf(
						"No controller pod runs on an eligible worker node (controller nodes: %v, workers: %v)",
						controllerNodeNames, workerNodeNames))
				}

				GinkgoWriter.Printf("Keeper node: %s\n", keeperNode)

				var nodesToCordon []string

				for _, node := range workerNodes {
					if node.Name != keeperNode {
						nodesToCordon = append(nodesToCordon, node.Name)
					}
				}

				var cordonedNodes []string

				DeferCleanup(func() {
					By("DeferCleanup: uncordoning all nodes modified by the test")

					uncordonNodesBestEffort(ctx, cordonedNodes)

					By("DeferCleanup: waiting for controller to return to expected replicas")

					waitForControllerReadyReplicas(
						sbrparams.ControllerScaleBackTimeout,
						Equal(sbrparams.ExpectedReplicas),
						"Controller deployment did not recover to %d ready replicas during cleanup",
						sbrparams.ExpectedReplicas)
				})

				By(fmt.Sprintf("Cordoning %d worker node(s), leaving %s schedulable",
					len(nodesToCordon), keeperNode))

				for _, nodeName := range nodesToCordon {
					Expect(setNodeUnschedulable(ctx, nodeName, true)).To(Succeed(),
						"Failed to cordon node %s", nodeName)
					cordonedNodes = append(cordonedNodes, nodeName)
				}

				By("Verifying cordoned nodes report Unschedulable before proceeding")

				for _, nodeName := range cordonedNodes {
					Eventually(func(g Gomega) bool {
						node, getErr := APIClient.CoreV1Interface.Nodes().Get(
							ctx, nodeName, metav1.GetOptions{})
						g.Expect(getErr).ToNot(HaveOccurred())

						return node.Spec.Unschedulable
					}, medik8sparams.DefaultTimeout, sbrparams.DefaultPollInterval).Should(BeTrue(),
						"Node %s did not become unschedulable", nodeName)
				}

				cordonedSet := make(map[string]struct{}, len(cordonedNodes))
				for _, nodeName := range cordonedNodes {
					cordonedSet[nodeName] = struct{}{}
				}

				By("Deleting controller pods from cordoned nodes")

				for _, controllerPod := range initialPods {
					if _, cordoned := cordonedSet[controllerPod.Object.Spec.NodeName]; !cordoned {
						continue
					}

					podName := controllerPod.Object.Name
					podNode := controllerPod.Object.Spec.NodeName

					GinkgoWriter.Printf("Deleting controller pod %s from cordoned node %s\n",
						podName, podNode)

					delErr := APIClient.CoreV1Interface.Pods(medik8sparams.OperatorNs).Delete(
						ctx, podName, metav1.DeleteOptions{})
					Expect(delErr).ToNot(HaveOccurred(),
						"Failed to delete controller pod %s", podName)
				}

				By("Waiting for deleted pods to terminate")

				for _, controllerPod := range initialPods {
					if _, cordoned := cordonedSet[controllerPod.Object.Spec.NodeName]; !cordoned {
						continue
					}

					waitForPodTermination(ctx, controllerPod.Object.Name)
				}

				By("Verifying at least one controller pod is Running on the keeper node")

				Eventually(func(assertion Gomega) {
					pods, listErr := listRunningControllerPods()
					assertion.Expect(listErr).ToNot(HaveOccurred())
					assertion.Expect(len(pods)).To(BeNumerically(">=", sbrparams.MinReplicasWhenDegraded),
						"expected at least %d running controller pod(s)", sbrparams.MinReplicasWhenDegraded)

					hasKeeperPod := false

					for _, p := range pods {
						if p.Object.Spec.NodeName == keeperNode {
							hasKeeperPod = true

							break
						}
					}

					assertion.Expect(hasKeeperPod).To(BeTrue(),
						"No running controller pod on keeper node %s", keeperNode)
				}, sbrparams.ControllerRescheduleTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
					"Controller did not stabilize with at least 1 ready replica on the keeper node")

				By("Verifying deployment reports at least 1 ready replica")

				waitForControllerReadyReplicas(
					sbrparams.ControllerRescheduleTimeout,
					BeNumerically(">=", sbrparams.MinReplicasWhenDegraded),
					"Deployment did not report at least %d ready replica(s) while degraded",
					sbrparams.MinReplicasWhenDegraded)

				By("Consistently verifying controller stays available during degraded phase")

				Consistently(func(assertion Gomega) {
					readyReplicas, pullErr := controllerReadyReplicas()
					assertion.Expect(pullErr).ToNot(HaveOccurred())
					assertion.Expect(readyReplicas).To(
						BeNumerically(">=", sbrparams.MinReplicasWhenDegraded),
						"Controller deployment must maintain at least %d ready replica(s)",
						sbrparams.MinReplicasWhenDegraded)
				}, sbrparams.ControllerDegradedConsistentDuration, sbrparams.DefaultPollInterval).Should(Succeed(),
					"Controller availability was not sustained during the single-worker degraded phase")

				By("Uncordoning all worker nodes")

				for _, nodeName := range cordonedNodes {
					Expect(setNodeUnschedulable(ctx, nodeName, false)).To(Succeed(),
						"Failed to uncordon node %s", nodeName)
				}

				cordonedNodes = nil

				By("Verifying controller scales back to expected replicas on different nodes")

				Eventually(func(assertion Gomega) {
					readyReplicas, pullErr := controllerReadyReplicas()
					assertion.Expect(pullErr).ToNot(HaveOccurred())
					assertion.Expect(readyReplicas).To(Equal(sbrparams.ExpectedReplicas),
						"expected %d ready replicas after uncordoning", sbrparams.ExpectedReplicas)

					pods, listErr := listRunningControllerPods()
					assertion.Expect(listErr).ToNot(HaveOccurred())
					assertion.Expect(len(pods)).To(Equal(int(sbrparams.ExpectedReplicas)))
					assertion.Expect(len(uniqueNodeNames(pods))).To(Equal(int(sbrparams.ExpectedReplicas)),
						"Controller pods must run on different nodes after recovery")
				}, sbrparams.ControllerScaleBackTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
					"Controller deployment did not scale back to %d ready replicas on distinct nodes",
					sbrparams.ExpectedReplicas)
			})

		It("should transfer controller leadership when active pod is deleted",
			reportxml.ID("90307"),
			Label(
				labels.DisruptionNonDestructive,
				labels.TierAcceptance,
				labels.FrequencyWeekly,
				labels.PlatformAny,
				labels.ComponentController,
			), func() {
				ctx := context.Background()

				By("Verifying cluster has enough workers for leadership handover")

				workerNodes, err := helpers.ListSchedulableWorkerNodes(ctx, APIClient)
				Expect(err).ToNot(HaveOccurred(), "Failed to list worker nodes")

				if len(workerNodes) < sbrparams.MinWorkerNodesForHandoverTest {
					Skip(fmt.Sprintf(
						"Leadership handover test requires at least %d schedulable worker-only nodes, found %d",
						sbrparams.MinWorkerNodesForHandoverTest, len(workerNodes)))
				}

				By("Verifying SBR controller deployment starts healthy")

				waitForControllerReadyReplicas(
					medik8sparams.DefaultTimeout,
					Equal(sbrparams.ExpectedReplicas),
					"expected %d ready replicas before handover test",
					sbrparams.ExpectedReplicas)

				By("Reading the lease and resolving the current leader to a live pod")

				var (
					oldLeaderPodName  string
					oldLeaderIdentity string
					leaderPod         *pod.Builder
				)

				// Leader election is eventually-consistent: the prior resilience spec
				// (run first in this Ordered, ContinueOnFailure container) deletes
				// controller pods, so the Lease can transiently name a pod that no
				// longer exists until re-election updates it. Retry until the Lease
				// resolves to a live pod instead of failing on a one-shot lookup.
				Eventually(func(assertion Gomega) {
					var leaderErr error

					oldLeaderPodName, oldLeaderIdentity, leaderErr = helpers.GetLeaderPodName(
						ctx, APIClient, sbrparams.ControllerLeaseName, medik8sparams.OperatorNs)
					assertion.Expect(leaderErr).ToNot(HaveOccurred(),
						"Failed to identify SBR controller leader")

					leaderPod, leaderErr = pod.Pull(APIClient, oldLeaderPodName, medik8sparams.OperatorNs)
					assertion.Expect(leaderErr).ToNot(HaveOccurred(),
						"Lease names pod %q (identity %q) which is not yet a live pod",
						oldLeaderPodName, oldLeaderIdentity)
				}, sbrparams.ControllerHandoverTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
					"Controller lease did not resolve to a live controller pod")

				GinkgoWriter.Printf("Current leader identity: %s\n", oldLeaderIdentity)

				By("Deleting the leader controller pod " + leaderPod.Object.Name)

				delErr := APIClient.CoreV1Interface.Pods(medik8sparams.OperatorNs).Delete(
					ctx, leaderPod.Object.Name, metav1.DeleteOptions{})
				Expect(delErr).ToNot(HaveOccurred(),
					"Failed to delete leader pod %s", leaderPod.Object.Name)

				By("Waiting for the deleted leader pod to terminate")

				waitForPodTermination(ctx, leaderPod.Object.Name)

				By("Waiting for SBR controller deployment to return to full ready replicas")

				sbrDeployment, err := pullControllerDeployment()
				Expect(err).ToNot(HaveOccurred())
				Expect(sbrDeployment.IsReady(sbrparams.ControllerHandoverTimeout)).To(BeTrue(),
					"SBR deployment did not become ready after leader pod deletion")

				By("Verifying controller lease transferred to a different pod")

				Eventually(func(assertion Gomega) {
					newLeaderPodName, newLeaderIdentity, leaderErr := helpers.GetLeaderPodName(
						ctx, APIClient, sbrparams.ControllerLeaseName, medik8sparams.OperatorNs)
					assertion.Expect(leaderErr).ToNot(HaveOccurred())
					assertion.Expect(newLeaderIdentity).ToNot(Equal(oldLeaderIdentity),
						"Lease is still held by deleted pod %s", oldLeaderIdentity)

					pods, listErr := listRunningControllerPods()
					assertion.Expect(listErr).ToNot(HaveOccurred())

					hasNewLeaderPod := false

					for _, runningPod := range pods {
						if runningPod.Object.Name == newLeaderPodName {
							hasNewLeaderPod = true

							break
						}
					}

					assertion.Expect(hasNewLeaderPod).To(BeTrue(),
						"New lease holder %q (pod %q) does not match any running controller pod",
						newLeaderIdentity, newLeaderPodName)
				}, sbrparams.ControllerHandoverTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
					"Controller leadership did not transfer after pod deletion")

				By("Verifying deployment has full ready replicas after handover")

				waitForControllerReadyReplicas(
					sbrparams.ControllerHandoverTimeout,
					Equal(sbrparams.ExpectedReplicas),
					"Deployment did not return to %d ready replicas after leadership handover",
					sbrparams.ExpectedReplicas)
			})
	})
