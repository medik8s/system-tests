package tests

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	nmov1beta1 "github.com/medik8s/node-maintenance-operator/api/v1beta1"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/deployment"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/nodes"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/reportxml"

	"github.com/medik8s/system-tests/tests/internal/helpers"
	"github.com/medik8s/system-tests/tests/internal/labels"
	. "github.com/medik8s/system-tests/tests/internal/medik8sinittools"
	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
	"github.com/medik8s/system-tests/tests/nmo-operator/internal/nmoparams"
)

const schedulePodName = "nmo-schedule-test"

var _ = Describe(
	"NMO Maintenance Lifecycle",
	Ordered,
	ContinueOnFailure,
	Serial,
	Label(labels.OperatorNMO), func() {
		var (
			targetNodeName string
			nmCRName       string
		)

		BeforeAll(func() {
			By("Registering NMO API scheme")

			err := APIClient.AttachScheme(nmov1beta1.AddToScheme)
			Expect(err).ToNot(HaveOccurred(), "Failed to register NMO scheme")

			By("Verifying NMO deployment is Ready")

			nmoDeployment, err := deployment.Pull(
				APIClient, nmoparams.OperatorDeploymentName, medik8sparams.OperatorNs)
			Expect(err).ToNot(HaveOccurred(), "Failed to get NMO deployment")
			Expect(nmoDeployment.IsReady(medik8sparams.DefaultTimeout)).To(BeTrue(),
				"NMO deployment is not Ready")

			By("Selecting a schedulable worker node")

			eligible, err := listSchedulableWorkers(context.Background())
			Expect(err).ToNot(HaveOccurred(), "Failed to list schedulable worker nodes")
			Expect(len(eligible)).To(BeNumerically(">=", nmoparams.MinWorkerNodesForMaintenance),
				"At least %d schedulable worker nodes are required (one for maintenance, one for cluster health)",
				nmoparams.MinWorkerNodesForMaintenance)

			targetNodeName = selectSchedulableWorker(context.Background())
			nmCRName = fmt.Sprintf("test-maintenance-%s", targetNodeName)

			By(fmt.Sprintf("Selected worker node: %s", targetNodeName))

			By("Cleaning up pre-existing schedule test pod if present")

			staleTestPod := &corev1.Pod{}

			err = APIClient.Get(context.Background(),
				client.ObjectKey{Name: schedulePodName, Namespace: medik8sparams.OperatorNs}, staleTestPod)

			switch {
			case err == nil:
				if delErr := APIClient.Delete(context.Background(), staleTestPod); delErr != nil {
					GinkgoWriter.Printf("WARNING: failed to delete stale schedule test pod: %v\n", delErr)
				}
			case !errors.IsNotFound(err):
				GinkgoWriter.Printf("WARNING: unexpected error checking stale schedule test pod: %v\n", err)
			}

			By("Verifying no pre-existing NodeMaintenance CR for target node")
			deleteAndWaitForNMCR(context.Background(), nmCRName, nmoparams.UncordonTimeout)
		})

		AfterAll(func() {
			By("Safety cleanup: removing NodeMaintenance CR if still exists")

			nmCleanup := &nmov1beta1.NodeMaintenance{}

			cleanupErr := APIClient.Get(context.Background(), client.ObjectKey{Name: nmCRName}, nmCleanup)

			switch {
			case cleanupErr == nil:
				if delErr := APIClient.Delete(context.Background(), nmCleanup); delErr != nil {
					GinkgoWriter.Printf("WARNING: failed to delete NodeMaintenance CR %s: %v\n",
						nmCRName, delErr)
				}
			case !errors.IsNotFound(cleanupErr):
				GinkgoWriter.Printf("WARNING: unexpected error checking NodeMaintenance CR %s: %v\n",
					nmCRName, cleanupErr)
			}

			By("Safety cleanup: removing schedule test pod if still exists")

			testPod := &corev1.Pod{}

			cleanupErr = APIClient.Get(context.Background(),
				client.ObjectKey{Name: schedulePodName, Namespace: medik8sparams.OperatorNs}, testPod)

			switch {
			case cleanupErr == nil:
				if delErr := APIClient.Delete(context.Background(), testPod); delErr != nil {
					GinkgoWriter.Printf("WARNING: failed to delete schedule test pod: %v\n", delErr)
				}
			case !errors.IsNotFound(cleanupErr):
				GinkgoWriter.Printf("WARNING: unexpected error checking schedule test pod: %v\n", cleanupErr)
			}

			if targetNodeName != "" {
				waitForNodeReadyAndUncordoned(context.Background(), targetNodeName, nmoparams.RebootTimeout)
			}
		})

		It("Start node maintenance",
			reportxml.ID("29592"),
			Label(
				labels.OperatorNMO,
				labels.DisruptionDestructive,
				labels.TierAcceptance,
				labels.PlatformAny,
				labels.ComponentController,
				labels.FrequencyWeekly,
			), func() {
				By(fmt.Sprintf("Creating NodeMaintenance CR for node %s", targetNodeName))
				nodeMaintenance := &nmov1beta1.NodeMaintenance{
					ObjectMeta: metav1.ObjectMeta{
						Name: nmCRName,
					},
					Spec: nmov1beta1.NodeMaintenanceSpec{
						NodeName: targetNodeName,
						Reason:   "system-tests lifecycle validation (RHWA-1250)",
					},
				}
				Expect(APIClient.Create(context.Background(), nodeMaintenance)).To(Succeed(),
					"Failed to create NodeMaintenance CR")

				By("Waiting for NodeMaintenance to reach Succeeded phase")
				waitForMaintenanceSucceeded(context.Background(), nmCRName)

				By("Verifying target node is cordoned and drain taint is applied")
				assertNodeCordonAndTaint(targetNodeName, true, nmoparams.MaintenanceTimeout)

				By("Verifying NodeMaintenance CR status shows drain completed")
				assertDrainCompleted(context.Background(), nmCRName)

				By("Verifying BeginMaintenance and SucceedMaintenance events were emitted")
				assertMaintenanceEvent(context.Background(), nmCRName, nmoparams.EventReasonBeginMaintenance)
				assertMaintenanceEvent(context.Background(), nmCRName, nmoparams.EventReasonSucceedMaintenance)

				By("Verifying maintenance lease exists with correct fields")
				assertMaintenanceLease(context.Background(), targetNodeName, true)
			})

		It("Schedule pod to node under maintenance",
			reportxml.ID("29603"),
			Label(
				labels.OperatorNMO,
				labels.DisruptionDestructive,
				labels.TierAcceptance,
				labels.PlatformAny,
				labels.ComponentController,
				labels.FrequencyWeekly,
			), func() {
				By(fmt.Sprintf("Creating pod with nodeSelector targeting %s", targetNodeName))
				testPod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      schedulePodName,
						Namespace: medik8sparams.OperatorNs,
					},
					Spec: corev1.PodSpec{
						NodeSelector: map[string]string{
							"kubernetes.io/hostname": targetNodeName,
						},
						Containers: []corev1.Container{{
							Name:    "sleep",
							Image:   nmoparams.WorkloadTestImage,
							Command: []string{"sleep", "3600"},
						}},
						RestartPolicy: corev1.RestartPolicyNever,
					},
				}
				Expect(APIClient.Create(context.Background(), testPod)).To(Succeed(),
					"Failed to create schedule test pod")

				By("Verifying pod stays in Pending state (node is unschedulable)")
				Consistently(func() corev1.PodPhase {
					pod := &corev1.Pod{}

					err := APIClient.Get(context.Background(),
						client.ObjectKey{Name: schedulePodName, Namespace: medik8sparams.OperatorNs}, pod)
					if err != nil {
						return ""
					}

					return pod.Status.Phase
				}, nmoparams.ScheduleCheckTimeout, nmoparams.DefaultPollInterval).Should(Equal(corev1.PodPending),
					"Pod should remain Pending on a cordoned node")

				By("Verifying pod was not scheduled (no nodeName assigned)")

				pod := &corev1.Pod{}
				Expect(APIClient.Get(context.Background(),
					client.ObjectKey{Name: schedulePodName, Namespace: medik8sparams.OperatorNs}, pod)).To(Succeed())
				Expect(pod.Spec.NodeName).To(BeEmpty(),
					"Pod should not be assigned to any node")

				By("Verifying NodeMaintenance CR status confirms drain completed")
				assertDrainCompleted(context.Background(), nmCRName)

				By("Cleaning up schedule test pod")
				Expect(APIClient.Delete(context.Background(), pod)).To(Succeed())
				Eventually(func() bool {
					err := APIClient.Get(context.Background(),
						client.ObjectKey{Name: schedulePodName, Namespace: medik8sparams.OperatorNs}, &corev1.Pod{})

					return errors.IsNotFound(err)
				}, nmoparams.ScheduleCheckTimeout, nmoparams.DefaultPollInterval).Should(BeTrue(),
					"Schedule test pod was not deleted")
			})

		It("Maintenance mode persists after node reboot",
			reportxml.ID("46761"),
			Label(
				labels.OperatorNMO,
				labels.DisruptionDestructive,
				labels.TierAcceptance,
				labels.PlatformAny,
				labels.ComponentController,
				labels.FrequencyWeekly,
			), func() {
				By("Capturing boot ID before reboot")

				previousBootID, err := helpers.GetNodeBootIDFromAPI(
					context.Background(), APIClient, targetNodeName)
				Expect(err).ToNot(HaveOccurred(), "Failed to get boot ID before reboot")

				By(fmt.Sprintf("Rebooting node %s via oc debug", targetNodeName))

				_, rebootErr := helpers.RunOnNode(
					context.Background(), targetNodeName, nmoparams.RunOnNodeTimeout, "systemctl", "reboot")
				if rebootErr != nil {
					GinkgoWriter.Printf("INFO: reboot command returned error (expected due to connection drop): %v\n",
						rebootErr)
				}

				By("Waiting for node to reboot (boot ID change)")
				Expect(helpers.WaitForNodeReboot(
					context.Background(), APIClient, targetNodeName,
					previousBootID, nmoparams.DefaultPollInterval, nmoparams.RebootTimeout,
					GinkgoWriter.Printf,
				)).To(Succeed(), "Node did not reboot (boot ID unchanged)")

				By("Waiting for node to recover and become Ready")
				Expect(helpers.WaitForNodeReady(
					context.Background(), APIClient, targetNodeName,
					nmoparams.DefaultPollInterval, nmoparams.RebootTimeout,
					GinkgoWriter.Printf,
				)).To(Succeed(), "Node did not return to Ready state after reboot")

				By("Verifying NodeMaintenance CR still exists and phase is Succeeded")
				Eventually(func(g Gomega) {
					nm := &nmov1beta1.NodeMaintenance{}
					g.Expect(APIClient.Get(context.Background(), client.ObjectKey{Name: nmCRName}, nm)).To(Succeed(),
						"NodeMaintenance CR should still exist after reboot")
					g.Expect(nm.Status.Phase).To(Equal(nmov1beta1.MaintenanceSucceeded),
						"NodeMaintenance phase should still be Succeeded after reboot")
				}, nmoparams.MaintenanceTimeout, nmoparams.DefaultPollInterval).Should(Succeed())

				By("Verifying target node remains cordoned and tainted after reboot")
				assertNodeCordonAndTaint(targetNodeName, true, nmoparams.MaintenanceTimeout)

				By("Verifying maintenance lease persists after reboot")
				assertMaintenanceLease(context.Background(), targetNodeName, true)
			})

		It("Stop node maintenance",
			reportxml.ID("29594"),
			Label(
				labels.OperatorNMO,
				labels.DisruptionDestructive,
				labels.TierAcceptance,
				labels.PlatformAny,
				labels.ComponentController,
				labels.FrequencyWeekly,
			), func() {
				By(fmt.Sprintf("Deleting NodeMaintenance CR %s and waiting for removal", nmCRName))
				deleteAndWaitForNMCR(context.Background(), nmCRName, nmoparams.UncordonTimeout)

				By("Verifying target node is uncordoned and drain taint removed")
				assertNodeCordonAndTaint(targetNodeName, false, nmoparams.UncordonTimeout)

				By("Verifying RemovedMaintenance event was emitted")
				assertMaintenanceEvent(context.Background(), nmCRName, nmoparams.EventReasonRemovedMaintenance)

				By("Verifying maintenance lease is deleted")
				assertMaintenanceLease(context.Background(), targetNodeName, false)
			})
	})

func assertNodeCordonAndTaint(nodeName string, expectCordoned bool, timeout time.Duration) {
	EventuallyWithOffset(1, func(g Gomega) {
		node, err := nodes.Pull(APIClient, nodeName)
		g.Expect(err).ToNot(HaveOccurred())
		g.Expect(node.Object.Spec.Unschedulable).To(Equal(expectCordoned),
			fmt.Sprintf("Node %s Unschedulable should be %v", nodeName, expectCordoned))
		g.Expect(hasDrainTaint(node.Object.Spec.Taints)).To(Equal(expectCordoned),
			fmt.Sprintf("Node %s drain taint presence should be %v", nodeName, expectCordoned))
	}, timeout, nmoparams.DefaultPollInterval).Should(Succeed())
}

func assertDrainCompleted(ctx context.Context, nmCRName string) {
	currentNM := &nmov1beta1.NodeMaintenance{}
	ExpectWithOffset(1, APIClient.Get(ctx, client.ObjectKey{Name: nmCRName}, currentNM)).To(Succeed())
	ExpectWithOffset(1, currentNM.Status.Phase).To(Equal(nmov1beta1.MaintenanceSucceeded),
		"Maintenance phase should be Succeeded")
	ExpectWithOffset(1, currentNM.Status.DrainProgress).To(Equal(nmoparams.DrainProgressComplete),
		"Drain progress should be 100%%")
	ExpectWithOffset(1, currentNM.Status.PendingPods).To(BeEmpty(),
		"No pods should be pending eviction")
}

func assertMaintenanceEvent(ctx context.Context, nmCRName, expectedReason string) {
	EventuallyWithOffset(1, func(assertion Gomega) {
		eventList := &corev1.EventList{}
		assertion.Expect(APIClient.List(ctx, eventList)).To(Succeed())

		found := false

		for i := range eventList.Items {
			event := &eventList.Items[i]
			if event.InvolvedObject.Kind == "NodeMaintenance" &&
				event.InvolvedObject.Name == nmCRName &&
				event.Reason == expectedReason {
				found = true

				break
			}
		}

		assertion.Expect(found).To(BeTrue(),
			fmt.Sprintf("Expected %s event for NodeMaintenance %s", expectedReason, nmCRName))
	}, nmoparams.EventTimeout, nmoparams.DefaultPollInterval).Should(Succeed())
}

func assertMaintenanceLease(ctx context.Context, nodeName string, shouldExist bool) {
	leaseName := fmt.Sprintf("node-%s", nodeName)

	if shouldExist {
		EventuallyWithOffset(1, func(assertion Gomega) {
			lease := &coordinationv1.Lease{}
			assertion.Expect(APIClient.Get(ctx, client.ObjectKey{
				Namespace: nmoparams.LeaseNamespace,
				Name:      leaseName,
			}, lease)).To(Succeed())
			assertion.Expect(lease.Spec.LeaseDurationSeconds).ToNot(BeNil())
			assertion.Expect(*lease.Spec.LeaseDurationSeconds).To(Equal(nmoparams.LeaseDurationSeconds),
				"Lease duration should be 3600s")
			assertion.Expect(lease.Spec.HolderIdentity).ToNot(BeNil())
			assertion.Expect(*lease.Spec.HolderIdentity).To(Equal(nmoparams.LeaseHolderIdentity),
				"Lease holder should be node-maintenance")
		}, nmoparams.LeaseTimeout, nmoparams.DefaultPollInterval).Should(Succeed())
	} else {
		EventuallyWithOffset(1, func() bool {
			lease := &coordinationv1.Lease{}
			err := APIClient.Get(ctx, client.ObjectKey{
				Namespace: nmoparams.LeaseNamespace,
				Name:      leaseName,
			}, lease)

			return errors.IsNotFound(err)
		}, nmoparams.LeaseTimeout, nmoparams.DefaultPollInterval).Should(BeTrue(),
			fmt.Sprintf("Lease %s should be deleted after maintenance stop", leaseName))
	}
}

func deleteAndWaitForNMCR(ctx context.Context, name string, timeout time.Duration) {
	existing := &nmov1beta1.NodeMaintenance{}

	err := APIClient.Get(ctx, client.ObjectKey{Name: name}, existing)

	switch {
	case err == nil:
		ExpectWithOffset(1, APIClient.Delete(ctx, existing)).To(Succeed(),
			fmt.Sprintf("Failed to delete NodeMaintenance CR %s", name))
		EventuallyWithOffset(1, func() bool {
			err := APIClient.Get(ctx,
				client.ObjectKey{Name: name}, &nmov1beta1.NodeMaintenance{})

			return errors.IsNotFound(err)
		}, timeout, nmoparams.DefaultPollInterval).Should(BeTrue(),
			fmt.Sprintf("NodeMaintenance CR %s was not deleted in time", name))
	case errors.IsNotFound(err):
		return
	default:
		ExpectWithOffset(1, err).ToNot(HaveOccurred(),
			fmt.Sprintf("Unexpected error checking NodeMaintenance CR %s", name))
	}
}

func hasDrainTaint(taints []corev1.Taint) bool {
	for _, taint := range taints {
		if taint.Key == nmoparams.DrainTaintKey && taint.Effect == corev1.TaintEffectNoSchedule {
			return true
		}
	}

	return false
}

func waitForNodeReadyAndUncordoned(ctx context.Context, nodeName string, timeout time.Duration) {
	By("Waiting for target node to become Ready")

	if err := helpers.WaitForNodeReady(
		ctx, APIClient, nodeName,
		nmoparams.DefaultPollInterval, timeout,
		GinkgoWriter.Printf,
	); err != nil {
		GinkgoWriter.Printf("WARNING: node %s did not become Ready: %v\n", nodeName, err)
	}

	By("Verifying target node is uncordoned and drain taint removed")
	assertNodeCordonAndTaint(nodeName, false, nmoparams.UncordonTimeout)
}
