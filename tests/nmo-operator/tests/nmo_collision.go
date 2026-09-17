package tests

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	nmov1beta1 "github.com/medik8s/node-maintenance-operator/api/v1beta1"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/deployment"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/reportxml"

	"github.com/medik8s/system-tests/tests/internal/labels"
	. "github.com/medik8s/system-tests/tests/internal/medik8sinittools"
	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
	"github.com/medik8s/system-tests/tests/nmo-operator/internal/nmoparams"
)

// NodeMaintenance collision negative tests (RHWA-1251). Each test creates one real
// NodeMaintenance CR (which cordons and drains a worker node) and then verifies that a
// colliding second CR is rejected -- by name (OCP-29632) or by node (OCP-29630). Shared
// helpers live in nmo_helpers.go.
//
// Both It blocks are tagged ComponentWebhook at the Describe level. OCP-29630 is a genuine
// NMO validating-webhook rejection; OCP-29632 is actually the API server's name-uniqueness
// (AlreadyExists) check, but it shares the label to match the duplicate-name test convention
// used across the operator suites (no API-server-specific component label exists).
//
// Note: the upstream operator e2e also covers spec.NodeName immutability (patching an
// existing CR's nodeName is rejected). That is tracked as a separate RHWA-1251 parity item
// and is intentionally out of scope for this PR.
var _ = Describe(
	"NMO Maintenance Collisions",
	Ordered,
	ContinueOnFailure,
	Serial,
	Label(
		labels.OperatorNMO,
		labels.DisruptionDestructive,
		labels.TierAcceptance,
		labels.PlatformAny,
		labels.FrequencyWeekly,
		labels.ComponentWebhook,
	), func() {
		var ctx context.Context

		BeforeAll(func() {
			ctx = context.Background()

			By("Registering NMO API scheme")
			Expect(APIClient.AttachScheme(nmov1beta1.AddToScheme)).To(Succeed(),
				"Failed to register NMO scheme")

			By("Verifying NMO deployment is Ready")

			nmoDeployment, err := deployment.Pull(
				APIClient, nmoparams.OperatorDeploymentName, medik8sparams.OperatorNs)
			Expect(err).ToNot(HaveOccurred(), "Failed to get NMO deployment")
			Expect(nmoDeployment.IsReady(medik8sparams.DefaultTimeout)).To(BeTrue(),
				"NMO deployment is not Ready")

			By("Verifying enough schedulable, non-control-plane worker nodes exist")

			eligible, err := listSchedulableWorkers(ctx)
			Expect(err).ToNot(HaveOccurred(), "Failed to list schedulable worker nodes")

			if len(eligible) < nmoparams.MinWorkerNodesForMaintenance {
				Skip(fmt.Sprintf(
					"Collision tests require at least %d schedulable non-control-plane worker nodes, found %d",
					nmoparams.MinWorkerNodesForMaintenance, len(eligible)))
			}
		})

		It("Reject a second NodeMaintenance with a duplicate name",
			reportxml.ID("29632"), func() {
				By("Pre-cleaning any stale NodeMaintenance CR from a previous run")
				deleteNMBestEffort(ctx, nmoparams.DuplicateNMName)

				By("Selecting the first schedulable worker node")

				firstNodeName := selectSchedulableWorker(ctx)

				By(fmt.Sprintf("Registering cleanup for %q and recovery of node %s",
					nmoparams.DuplicateNMName, firstNodeName))
				DeferCleanup(func() {
					cleanupCollisionNMs(ctx, firstNodeName, nmoparams.DuplicateNMName)
				})

				By(fmt.Sprintf("Creating the first NodeMaintenance %q on node %s",
					nmoparams.DuplicateNMName, firstNodeName))

				firstNM := newNodeMaintenance(nmoparams.DuplicateNMName, firstNodeName)
				Expect(APIClient.Create(ctx, firstNM)).To(Succeed(),
					"Failed to create the first NodeMaintenance CR")

				By("Waiting for the first NodeMaintenance to fully enter maintenance")
				assertMaintenanceSucceeded(ctx, nmoparams.DuplicateNMName, firstNodeName)

				// The first node is now cordoned/unschedulable, so selecting a fresh schedulable
				// worker yields a different node (as the Python test does). This keeps the
				// collision on the object NAME (apiserver AlreadyExists): admission webhooks run
				// before the apiserver name-uniqueness check, so a same-node second CR would be
				// rejected by the node webhook first instead.
				By("Selecting a schedulable worker node for the duplicate")

				secondNodeName := selectSchedulableWorker(ctx)

				By("Attempting to create a second NodeMaintenance with the same name (expect rejection)")

				duplicateNM := newNodeMaintenance(nmoparams.DuplicateNMName, secondNodeName)
				createErr := APIClient.Create(ctx, duplicateNM)

				Expect(createErr).To(MatchError(ContainSubstring(
					fmt.Sprintf("%s %q already exists", nmoparams.NMResourceQualified, nmoparams.DuplicateNMName))),
					"Second create with a duplicate name should be rejected with an already-exists error")
			})

		It("Reject a second NodeMaintenance for a node already under maintenance",
			reportxml.ID("29630"), func() {
				By("Pre-cleaning any stale NodeMaintenance CRs from a previous run")
				deleteNMBestEffort(ctx, nmoparams.FirstNMName)
				deleteNMBestEffort(ctx, nmoparams.SecondNMName)

				By("Selecting a schedulable worker node")

				targetNodeName := selectSchedulableWorker(ctx)

				By(fmt.Sprintf("Registering cleanup for the collision CRs and recovery of node %s",
					targetNodeName))
				DeferCleanup(func() {
					cleanupCollisionNMs(ctx, targetNodeName, nmoparams.FirstNMName, nmoparams.SecondNMName)
				})

				By(fmt.Sprintf("Creating the first NodeMaintenance %q for node %s",
					nmoparams.FirstNMName, targetNodeName))

				firstNM := newNodeMaintenance(nmoparams.FirstNMName, targetNodeName)
				Expect(APIClient.Create(ctx, firstNM)).To(Succeed(),
					"Failed to create the first NodeMaintenance CR")

				By("Waiting for the first NodeMaintenance to fully enter maintenance")
				assertMaintenanceSucceeded(ctx, nmoparams.FirstNMName, targetNodeName)

				By(fmt.Sprintf("Attempting a second NodeMaintenance %q for the same node (expect rejection)",
					nmoparams.SecondNMName))

				secondNM := newNodeMaintenance(nmoparams.SecondNMName, targetNodeName)
				createErr := APIClient.Create(ctx, secondNM)

				// The NMO validating webhook (vnodemaintenance.kb.io) denies the request with
				// "invalid nodeName, a NodeMaintenance for node <node> already exists". The
				// substring below is the stable, node-named core of that message.
				Expect(createErr).To(MatchError(ContainSubstring(
					fmt.Sprintf("NodeMaintenance for node %s already exists", targetNodeName))),
					"Second create for a node already under maintenance should be rejected by the webhook")
			})
	})
