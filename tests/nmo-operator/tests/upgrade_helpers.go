package tests

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	nmov1beta1 "github.com/medik8s/node-maintenance-operator/api/v1beta1"

	. "github.com/medik8s/system-tests/tests/internal/medik8sinittools"
)

// registerNMOScheme attaches the NodeMaintenance v1beta1 scheme to APIClient.
// nmo_collision.go/nmo_lifecycle.go normally do this in their own BeforeEach,
// but an upgrade spec must not depend on those specs having run in the same
// process -- each upgrade spec is meant to be fully self-contained. Safe to
// call more than once (AddToScheme registration is idempotent).
func registerNMOScheme() {
	Expect(APIClient.AttachScheme(nmov1beta1.AddToScheme)).To(Succeed(),
		"Failed to register NodeMaintenance scheme")
}

// nmoUpgradeSafetyNet is used as an upgrade spec's JustAfterEach: cleans up
// any NodeMaintenance CR left over from a maintenance cycle that didn't
// complete its own inline cleanup, and waits for the node to recover before
// the spec finishes.
func nmoUpgradeSafetyNet(ctx context.Context, getName, getNode func() string) {
	name := getName()
	if name == "" {
		return
	}

	node := getNode()

	By("Safety net: deleting NodeMaintenance " + name)
	deleteNMBestEffort(ctx, name)

	By("Safety net: waiting for node " + node + " to recover")
	waitForNodeRecoveryBestEffort(ctx, node)
}

// runNMOMaintenanceCycle selects a schedulable worker, puts it under maintenance
// via a NodeMaintenance CR, and verifies it cordons, taints, and drains
// correctly. Reuses the existing collision-test helpers (nmo_helpers.go) --
// newNodeMaintenance, assertMaintenanceSucceeded, selectSchedulableWorker --
// rather than duplicating them.
func runNMOMaintenanceCycle(ctx context.Context, phase string) (nmName, nodeName string, err error) {
	nodeName = selectSchedulableWorker(ctx)
	nmName = fmt.Sprintf("nmo-upgrade-%s", phase)

	GinkgoWriter.Printf("[%s] Target node: %s\n", phase, nodeName)

	By(fmt.Sprintf("[%s] Creating NodeMaintenance for %s", phase, nodeName))

	nm := newNodeMaintenance(nmName, nodeName)
	if createErr := APIClient.Create(ctx, nm); createErr != nil {
		return nmName, nodeName, fmt.Errorf("[%s] failed to create NodeMaintenance %s: %w",
			phase, nmName, createErr)
	}

	By(fmt.Sprintf("[%s] Waiting for maintenance to succeed on %s", phase, nodeName))

	assertMaintenanceSucceeded(ctx, nmName, nodeName)

	GinkgoWriter.Printf("[%s] Maintenance cycle succeeded for node %s\n", phase, nodeName)

	return nmName, nodeName, nil
}

// endNMOMaintenanceCycle deletes the NodeMaintenance CR and waits for the node
// to recover (uncordoned, untainted, Ready) before the next cycle begins.
func endNMOMaintenanceCycle(ctx context.Context, nmName, nodeName string) {
	deleteNMBestEffort(ctx, nmName)
	waitForNodeRecoveryBestEffort(ctx, nodeName)
}
