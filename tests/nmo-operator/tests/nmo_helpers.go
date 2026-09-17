package tests

import (
	"context"
	"fmt"
	"math/rand"
	"sort"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"

	nmov1beta1 "github.com/medik8s/node-maintenance-operator/api/v1beta1"

	"github.com/medik8s/system-tests/tests/internal/helpers"
	. "github.com/medik8s/system-tests/tests/internal/medik8sinittools"
	"github.com/medik8s/system-tests/tests/nmo-operator/internal/nmoparams"
)

// isControlPlane reports whether the node carries a master or control-plane role label.
// On compact topologies control-plane nodes also carry the worker label, so they must be
// filtered out: putting one under maintenance can trip the etcd-quorum webhook and reject
// the first (expected-to-succeed) NodeMaintenance for reasons unrelated to these tests.
func isControlPlane(node *corev1.Node) bool {
	if _, ok := node.Labels["node-role.kubernetes.io/master"]; ok {
		return true
	}

	if _, ok := node.Labels["node-role.kubernetes.io/control-plane"]; ok {
		return true
	}

	return false
}

// listSchedulableWorkers returns Ready, schedulable, non-control-plane worker nodes. This is
// the Go equivalent of the Python get_schedulable_nodes_list(node_role=WORKER_ROLE) helper.
func listSchedulableWorkers(ctx context.Context) ([]corev1.Node, error) {
	nodeList := &corev1.NodeList{}
	if err := APIClient.List(ctx, nodeList,
		client.MatchingLabels{"node-role.kubernetes.io/worker": ""}); err != nil {
		return nil, fmt.Errorf("listing worker nodes: %w", err)
	}

	var eligible []corev1.Node

	for i := range nodeList.Items {
		node := &nodeList.Items[i]

		if node.Spec.Unschedulable || isControlPlane(node) {
			continue
		}

		if helpers.IsNodeReady(node) {
			eligible = append(eligible, *node)
		}
	}

	return eligible, nil
}

// selectSchedulableWorker returns the name of a random Ready, schedulable, non-control-plane
// worker node (Python: choice(get_schedulable_nodes_list(WORKER_ROLE))). It retries until an
// eligible node appears, so a node still uncordoning from a prior interrupted run does not
// cause a one-shot failure.
func selectSchedulableWorker(ctx context.Context) string {
	GinkgoHelper()

	var selected string

	Eventually(func() (int, error) {
		eligible, err := listSchedulableWorkers(ctx)
		if err != nil {
			return 0, err
		}

		if len(eligible) == 0 {
			return 0, nil
		}

		sort.Slice(eligible, func(i, j int) bool { return eligible[i].Name < eligible[j].Name })
		selected = eligible[rand.Intn(len(eligible))].Name

		return len(eligible), nil
	}, nmoparams.UncordonTimeout, nmoparams.DefaultPollInterval).Should(BeNumerically(">", 0),
		"no eligible schedulable non-control-plane worker node found")

	return selected
}

// newNodeMaintenance builds a NodeMaintenance CR for the given name and node using the
// shared collision-test reason.
func newNodeMaintenance(name, nodeName string) *nmov1beta1.NodeMaintenance {
	return &nmov1beta1.NodeMaintenance{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: nmov1beta1.NodeMaintenanceSpec{
			NodeName: nodeName,
			Reason:   nmoparams.CollisionReason,
		},
	}
}

// waitForMaintenanceSucceeded blocks until the named NodeMaintenance reaches the Succeeded
// phase, re-fetching the CR each poll and failing fast on unexpected errors.
func waitForMaintenanceSucceeded(ctx context.Context, name string) {
	GinkgoHelper()

	Eventually(func() (nmov1beta1.MaintenancePhase, error) {
		current := &nmov1beta1.NodeMaintenance{}
		if err := APIClient.Get(ctx, client.ObjectKey{Name: name}, current); err != nil {
			return "", fmt.Errorf("getting NodeMaintenance %s: %w", name, err)
		}

		return current.Status.Phase, nil
	}, nmoparams.MaintenanceTimeout, nmoparams.DefaultPollInterval).Should(Equal(nmov1beta1.MaintenanceSucceeded),
		"NodeMaintenance %s did not reach Succeeded phase", name)
}

// assertMaintenanceSucceeded verifies a node has fully entered maintenance, mirroring the
// Python is_maintenance_succeeded helper: the NodeMaintenance reaches Succeeded, the node is
// cordoned and carries the drain taint, and the drain has completed (all pods evicted). This
// also guarantees the node is Unschedulable, which is what lets a subsequent
// selectSchedulableWorker deterministically pick a different node for a name-collision test.
func assertMaintenanceSucceeded(ctx context.Context, name, nodeName string) {
	GinkgoHelper()

	waitForMaintenanceSucceeded(ctx, name)
	assertNodeCordonAndTaint(nodeName, true, nmoparams.MaintenanceTimeout)
	assertDrainCompleted(ctx, name)
}

// deleteNMBestEffort deletes the named NodeMaintenance and waits until it is gone. It is safe
// to call from cleanup: it uses wait.PollUntilContextTimeout (never Fail/panic) and retries
// through any transient (non-NotFound) API error until the timeout, logging a warning, so a
// single blip on a busy cluster does not abandon the deletion.
func deleteNMBestEffort(ctx context.Context, name string) {
	nm := &nmov1beta1.NodeMaintenance{ObjectMeta: metav1.ObjectMeta{Name: name}}

	waitErr := wait.PollUntilContextTimeout(ctx, nmoparams.DefaultPollInterval, nmoparams.UncordonTimeout, true,
		func(ctx context.Context) (bool, error) {
			if delErr := APIClient.Delete(ctx, nm); delErr != nil && !errors.IsNotFound(delErr) {
				GinkgoWriter.Printf("WARNING: deleting NodeMaintenance %s failed, retrying: %v\n", name, delErr)

				return false, nil
			}

			getErr := APIClient.Get(ctx, client.ObjectKey{Name: name}, &nmov1beta1.NodeMaintenance{})
			if getErr != nil && !errors.IsNotFound(getErr) {
				GinkgoWriter.Printf("WARNING: checking NodeMaintenance %s after delete failed, retrying: %v\n", name, getErr)

				return false, nil
			}

			return errors.IsNotFound(getErr), nil
		})
	if waitErr != nil {
		GinkgoWriter.Printf("WARNING: NodeMaintenance %s not confirmed deleted within timeout: %v\n", name, waitErr)
	}
}

// waitForNodeRecoveryBestEffort waits until the node is Ready, uncordoned, and free of the
// drain taint. Like deleteNMBestEffort it is cleanup-safe (wait.PollUntilContextTimeout, warn
// only) and retries through any transient error until the timeout.
func waitForNodeRecoveryBestEffort(ctx context.Context, nodeName string) {
	waitErr := wait.PollUntilContextTimeout(ctx, nmoparams.DefaultPollInterval, nmoparams.UncordonTimeout, true,
		func(ctx context.Context) (bool, error) {
			node := &corev1.Node{}
			if err := APIClient.Get(ctx, client.ObjectKey{Name: nodeName}, node); err != nil {
				GinkgoWriter.Printf("WARNING: getting node %s during recovery, retrying: %v\n", nodeName, err)

				return false, nil
			}

			if node.Spec.Unschedulable || hasDrainTaint(node.Spec.Taints) {
				return false, nil
			}

			return helpers.IsNodeReady(node), nil
		})
	if waitErr != nil {
		GinkgoWriter.Printf("WARNING: node %s did not fully recover (Ready, uncordoned, untainted) within timeout: %v\n",
			nodeName, waitErr)
	}
}

// cleanupCollisionNMs deletes the given NodeMaintenance CRs and waits for the target node to
// return to a Ready, uncordoned state. All steps are best-effort (no Fail/panic) so a slow
// node recovery cannot leave earlier deletions unattempted. This mirrors the Python
// teardown_method, which deletes every NodeMaintenance object left by the test.
func cleanupCollisionNMs(ctx context.Context, nodeName string, names ...string) {
	for _, name := range names {
		deleteNMBestEffort(ctx, name)
	}

	if nodeName != "" {
		waitForNodeRecoveryBestEffort(ctx, nodeName)
	}
}
