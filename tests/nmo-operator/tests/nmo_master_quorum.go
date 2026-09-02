package tests

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	configv1 "github.com/openshift/api/config/v1"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	nmov1beta1 "github.com/medik8s/node-maintenance-operator/api/v1beta1"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/deployment"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/infrastructure"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/nodes"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/reportxml"

	"github.com/medik8s/system-tests/tests/internal/helpers"
	"github.com/medik8s/system-tests/tests/internal/labels"
	. "github.com/medik8s/system-tests/tests/internal/medik8sinittools"
	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
	"github.com/medik8s/system-tests/tests/nmo-operator/internal/nmoparams"
)

const nmoQuorumReason = "system-tests master quorum validation (RHWA-1251)"

var _ = Describe(
	"NMO Master Quorum",
	Ordered,
	Serial,
	Label(labels.OperatorNMO), func() {
		BeforeAll(func() {
			ctx := context.Background()

			By("Registering NMO API scheme")
			Expect(APIClient.AttachScheme(nmov1beta1.AddToScheme)).To(Succeed(),
				"Failed to register NMO scheme")

			By("Verifying NMO deployment is Ready")

			nmoDeployment, err := deployment.Pull(
				APIClient, nmoparams.OperatorDeploymentName, medik8sparams.OperatorNs)
			Expect(err).ToNot(HaveOccurred(), "Failed to get NMO deployment")
			Expect(nmoDeployment.IsReady(medik8sparams.DefaultTimeout)).To(BeTrue(),
				"NMO deployment is not Ready")

			By("Skipping on topologies that cannot exercise an etcd quorum")

			infraConfig, infraErr := infrastructure.Pull(APIClient)
			Expect(infraErr).ToNot(HaveOccurred(), "Failed to pull infrastructure configuration")

			if infraConfig.Object.Status.ControlPlaneTopology == configv1.SingleReplicaTopologyMode {
				Skip("Master quorum test requires a multi-node control plane (SNO detected)")
			}

			controlPlaneCount, err := helpers.CountControlPlaneNodes(ctx, APIClient)
			Expect(err).ToNot(HaveOccurred(), "Failed to count control-plane nodes")

			if controlPlaneCount < nmoparams.MinControlPlaneNodesForQuorum {
				Skip(fmt.Sprintf(
					"Master quorum test requires at least %d control-plane nodes, found %d",
					nmoparams.MinControlPlaneNodesForQuorum, controlPlaneCount))
			}

			By("Skipping unless etcd exposes a single PDB tolerating exactly one disruption")

			// The webhook can only assess quorum when openshift-etcd holds exactly one
			// PDB; treat "cannot assess" (missing/multiple PDB, or read error) as a Skip
			// rather than a failure, matching the webhook's own guard.
			pdb, pdbErr := getEtcdQuorumPDB(ctx)
			if pdbErr != nil {
				Skip(fmt.Sprintf("Cannot assess etcd quorum, skipping: %v", pdbErr))
			}

			if pdb.Status.DisruptionsAllowed != nmoparams.ExpectedQuorumDisruptions {
				Skip(fmt.Sprintf(
					"Master quorum test requires an etcd PDB that tolerates exactly %d disruption, found DisruptionsAllowed=%d",
					nmoparams.ExpectedQuorumDisruptions, pdb.Status.DisruptionsAllowed))
			}

			By("Verifying the NodeMaintenance validating webhook is installed")
			// Refuse to run a destructive control-plane test if the admission webhook
			// (the component under test, and the guard preventing a second master drain)
			// is absent -- otherwise a second maintenance could be admitted and disrupt etcd.
			assertNodeMaintenanceWebhookPresent(ctx)
		})

		It("Rejects a second control-plane NodeMaintenance that would violate etcd quorum",
			reportxml.ID("46790"),
			Label(
				labels.DisruptionDestructive,
				labels.TierAcceptance,
				labels.PlatformAny,
				labels.ComponentWebhook,
				labels.FrequencyWeekly,
			), func() {
				ctx := context.Background()

				By("Reading the etcd quorum PodDisruptionBudget")
				logEtcdQuorumPDB(ctx)

				By("Selecting a schedulable control-plane node for maintenance")

				firstNode, err := helpers.SelectControlPlaneNode(ctx, APIClient)
				Expect(err).ToNot(HaveOccurred(), "Failed to select a control-plane node")

				firstNodeName := firstNode.Name
				firstNMName := fmt.Sprintf("quorum-first-%s", firstNodeName)

				By(fmt.Sprintf("Pre-cleaning any stale NodeMaintenance for node %s", firstNodeName))
				deleteNMsForNode(ctx, firstNodeName, nmoparams.UncordonTimeout)

				By("Registering cleanup to end maintenance and recover the control-plane node")
				DeferCleanup(func() {
					cleanupCtx := context.Background()
					deleteAndWaitForNMCR(cleanupCtx, firstNMName, nmoparams.UncordonTimeout)
					waitForNodeReadyAndUncordoned(cleanupCtx, firstNodeName, nmoparams.RebootTimeout)
				})

				By(fmt.Sprintf("Placing first control-plane node %s into maintenance", firstNodeName))

				firstNM := &nmov1beta1.NodeMaintenance{
					ObjectMeta: metav1.ObjectMeta{
						Name: firstNMName,
					},
					Spec: nmov1beta1.NodeMaintenanceSpec{
						NodeName: firstNodeName,
						Reason:   nmoQuorumReason,
					},
				}
				Expect(APIClient.Create(ctx, firstNM)).To(Succeed(),
					"Failed to create first control-plane NodeMaintenance CR")

				By("Waiting for the first NodeMaintenance to reach Succeeded phase")
				Eventually(func() (nmov1beta1.MaintenancePhase, error) {
					current := &nmov1beta1.NodeMaintenance{}

					getErr := APIClient.Get(ctx, client.ObjectKey{Name: firstNMName}, current)
					if errors.IsNotFound(getErr) {
						// The CR we just created is gone -- no point retrying, fail immediately.
						return "", StopTrying(fmt.Sprintf(
							"NodeMaintenance %s disappeared while awaiting Succeeded phase", firstNMName)).Wrap(getErr)
					}

					if getErr != nil {
						// Transient API error -- Eventually keeps polling; surface it in diagnostics.
						return "", getErr
					}

					return current.Status.Phase, nil
				}, nmoparams.MasterMaintenanceTimeout, nmoparams.DefaultPollInterval).Should(
					Equal(nmov1beta1.MaintenanceSucceeded),
					"First control-plane NodeMaintenance did not reach Succeeded phase")

				By("Verifying the first control-plane node is cordoned with the drain taint")
				assertNodeCordonAndTaint(firstNodeName, true, nmoparams.MasterMaintenanceTimeout)

				By("Verifying the first NodeMaintenance reports drain completed")
				assertDrainCompleted(ctx, firstNMName)

				By("Waiting until etcd no longer tolerates another control-plane disruption")
				Eventually(func() (int32, error) {
					pdb, pdbErr := getEtcdQuorumPDB(ctx)
					if pdbErr != nil {
						return 0, pdbErr
					}

					return pdb.Status.DisruptionsAllowed, nil
				}, nmoparams.QuorumUpdateTimeout, nmoparams.DefaultPollInterval).Should(
					Equal(int32(0)),
					"etcd PDB should report zero allowed disruptions while a master is under maintenance")

				By("Selecting a second, distinct schedulable control-plane node")

				secondNode, err := helpers.SelectControlPlaneNode(ctx, APIClient, firstNodeName)
				Expect(err).ToNot(HaveOccurred(), "Failed to select a second control-plane node")

				secondNodeName := secondNode.Name
				secondNMName := fmt.Sprintf("quorum-second-%s", secondNodeName)

				By(fmt.Sprintf("Pre-cleaning any stale NodeMaintenance for node %s", secondNodeName))
				deleteNMsForNode(ctx, secondNodeName, nmoparams.UncordonTimeout)

				By("Registering safety cleanup for the second control-plane node")
				DeferCleanup(func() {
					cleanupCtx := context.Background()
					deleteAndWaitForNMCR(cleanupCtx, secondNMName, nmoparams.UncordonTimeout)
					waitForNodeReadyAndUncordoned(cleanupCtx, secondNodeName, nmoparams.RebootTimeout)
				})

				By(fmt.Sprintf(
					"Verifying the second node %s still holds a Ready etcd guard pod", secondNodeName))
				// The webhook admits a second maintenance if the target's etcd guard pod is
				// NOT Ready (it treats the node as already disrupted). Confirming the guard pod
				// is Ready here makes the rejection deterministic, so the assertion below does
				// not flake. Actual safety comes from the dry-run create, which is never
				// persisted and so can never drain a second master.
				assertEtcdGuardPodReady(ctx, secondNodeName)

				By(fmt.Sprintf(
					"Attempting to place second control-plane node %s into maintenance", secondNodeName))

				secondNM := &nmov1beta1.NodeMaintenance{
					ObjectMeta: metav1.ObjectMeta{
						Name: secondNMName,
					},
					Spec: nmov1beta1.NodeMaintenanceSpec{
						NodeName: secondNodeName,
						Reason:   nmoQuorumReason,
					},
				}

				By("Verifying the admission webhook rejects it on etcd-quorum grounds")
				// Dry-run: the validating webhook still runs and returns the same rejection,
				// but the CR is never persisted, so this test cannot itself drain a second master.
				Expect(APIClient.Create(ctx, secondNM, client.DryRunAll)).To(
					MatchError(ContainSubstring(nmoparams.WebhookMsgQuorumViolation)),
					"Second control-plane NodeMaintenance should be rejected to preserve etcd quorum")

				By("Verifying the second control-plane node remains schedulable")

				observedSecondNode := false

				Consistently(func(assertion Gomega) {
					node, pullErr := nodes.Pull(APIClient, secondNodeName)
					if pullErr != nil {
						// Tolerate a transient get error rather than failing a destructive test.
						return
					}

					observedSecondNode = true

					assertion.Expect(node.Object.Spec.Unschedulable).To(BeFalse(),
						"second control-plane node must not be cordoned after a rejected maintenance")
				}, nmoparams.ScheduleCheckTimeout, nmoparams.DefaultPollInterval).Should(Succeed())

				Expect(observedSecondNode).To(BeTrue(),
					"second control-plane node status was never read successfully")
			})
	})

// getEtcdQuorumPDB returns the single etcd quorum PodDisruptionBudget in the
// openshift-etcd namespace. It mirrors the NMO webhook, which lists PDBs in that
// namespace and can only assess quorum when exactly one exists -- so anything
// other than one PDB is reported as an error (callers treat that as a Skip).
func getEtcdQuorumPDB(ctx context.Context) (*policyv1.PodDisruptionBudget, error) {
	pdbList := &policyv1.PodDisruptionBudgetList{}
	if err := APIClient.List(ctx, pdbList, client.InNamespace(nmoparams.EtcdNamespace)); err != nil {
		return nil, fmt.Errorf("listing PodDisruptionBudgets in %s: %w", nmoparams.EtcdNamespace, err)
	}

	if len(pdbList.Items) != 1 {
		return nil, fmt.Errorf("expected exactly one PodDisruptionBudget in %s, found %d",
			nmoparams.EtcdNamespace, len(pdbList.Items))
	}

	return &pdbList.Items[0], nil
}

// logEtcdQuorumPDB prints the etcd quorum PDB name and its currently allowed
// disruptions -- the runtime equivalent of the Polarion plan's
// "print current master quorum" step.
func logEtcdQuorumPDB(ctx context.Context) {
	GinkgoHelper()

	pdb, err := getEtcdQuorumPDB(ctx)
	Expect(err).ToNot(HaveOccurred(), "Failed to read etcd quorum PDB")

	GinkgoWriter.Printf(
		"etcd quorum PDB %q: DisruptionsAllowed=%d, DesiredHealthy=%d, ExpectedPods=%d\n",
		pdb.Name, pdb.Status.DisruptionsAllowed, pdb.Status.DesiredHealthy, pdb.Status.ExpectedPods)
}

// assertEtcdGuardPodReady verifies the etcd guard pod on the given node is Ready.
// It selects guard pods via the etcd PDB's own selector (matching the webhook's
// logic) and requires the pod on nodeName to report a Ready condition of True or
// Unknown -- the same states the webhook treats as "still a voting member".
func assertEtcdGuardPodReady(ctx context.Context, nodeName string) {
	GinkgoHelper()

	pdb, err := getEtcdQuorumPDB(ctx)
	Expect(err).ToNot(HaveOccurred(), "Failed to read etcd quorum PDB")

	selector, err := metav1.LabelSelectorAsSelector(pdb.Spec.Selector)
	Expect(err).ToNot(HaveOccurred(), "Failed to parse etcd PDB selector")
	Expect(selector.Empty()).To(BeFalse(),
		"etcd PDB selector must not be empty (would match all pods)")

	Eventually(func(assertion Gomega) {
		podList := &corev1.PodList{}
		assertion.Expect(APIClient.List(ctx, podList,
			client.InNamespace(nmoparams.EtcdNamespace),
			client.MatchingLabelsSelector{Selector: selector})).To(Succeed())

		var guard *corev1.Pod

		for i := range podList.Items {
			if podList.Items[i].Spec.NodeName == nodeName {
				guard = &podList.Items[i]

				break
			}
		}

		assertion.Expect(guard).ToNot(BeNil(),
			fmt.Sprintf("no etcd guard pod found on node %s", nodeName))

		// Match the webhook, which treats Ready True OR Unknown as "still a voting
		// member" and therefore rejects disrupting it.
		ready := false

		for _, cond := range guard.Status.Conditions {
			if cond.Type == corev1.PodReady &&
				(cond.Status == corev1.ConditionTrue || cond.Status == corev1.ConditionUnknown) {
				ready = true

				break
			}
		}

		assertion.Expect(ready).To(BeTrue(),
			fmt.Sprintf("etcd guard pod %s on node %s is not Ready", guard.Name, nodeName))
	}, nmoparams.QuorumUpdateTimeout, nmoparams.DefaultPollInterval).Should(Succeed())
}

// assertNodeMaintenanceWebhookPresent fails if no ValidatingWebhookConfiguration
// intercepts NodeMaintenance resources. Matching by resource name keeps it
// independent of the operator's webhook configuration naming across versions.
func assertNodeMaintenanceWebhookPresent(ctx context.Context) {
	GinkgoHelper()

	vwcList := &admissionregistrationv1.ValidatingWebhookConfigurationList{}
	Expect(APIClient.List(ctx, vwcList)).To(Succeed(),
		"Failed to list ValidatingWebhookConfigurations")

	found := false

	for i := range vwcList.Items {
		for _, webhook := range vwcList.Items[i].Webhooks {
			for _, rule := range webhook.Rules {
				for _, resource := range rule.Resources {
					if resource == "nodemaintenances" {
						found = true
					}
				}
			}
		}
	}

	Expect(found).To(BeTrue(),
		"No ValidatingWebhookConfiguration for nodemaintenances found -- "+
			"refusing to run a destructive quorum test without the admission webhook")
}

// deleteNMsForNode deletes any NodeMaintenance CR targeting nodeName (regardless
// of CR name) and waits for each to be removed. The webhook validates
// "no NodeMaintenance for this node" before the quorum check, so a stale CR on
// the target node (from an aborted run) would otherwise mask the quorum error.
func deleteNMsForNode(ctx context.Context, nodeName string, timeout time.Duration) {
	GinkgoHelper()

	nmList := &nmov1beta1.NodeMaintenanceList{}
	Expect(APIClient.List(ctx, nmList)).To(Succeed(), "Failed to list NodeMaintenance CRs")

	for i := range nmList.Items {
		if nmList.Items[i].Spec.NodeName == nodeName {
			deleteAndWaitForNMCR(ctx, nmList.Items[i].Name, timeout)
		}
	}
}
