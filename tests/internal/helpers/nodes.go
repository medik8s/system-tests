package helpers

import (
	"context"
	"fmt"
	"math/rand"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// IsNodeReady returns true if the node has a Ready condition with status True.
func IsNodeReady(node *corev1.Node) bool {
	for _, cond := range node.Status.Conditions {
		if cond.Type == corev1.NodeReady {
			return cond.Status == corev1.ConditionTrue
		}
	}

	return false
}

// pickRandomReadyNode filters candidates to Ready, schedulable nodes not in the
// excludeNodes list and returns one at random, or nil if none qualify.
// Randomization avoids deterministic reuse of the same node across sequential
// destructive tests.
func pickRandomReadyNode(candidates []corev1.Node, excludeNodes ...string) *corev1.Node {
	excluded := make(map[string]bool, len(excludeNodes))
	for _, name := range excludeNodes {
		excluded[name] = true
	}

	var eligible []corev1.Node

	for i := range candidates {
		node := &candidates[i]

		if excluded[node.Name] || node.Spec.Unschedulable {
			continue
		}

		if IsNodeReady(node) {
			eligible = append(eligible, *node)
		}
	}

	if len(eligible) == 0 {
		return nil
	}

	return &eligible[rand.Intn(len(eligible))]
}

// SelectWorkerNode returns a random Ready, schedulable worker node that is not
// in the excludeNodes list.
func SelectWorkerNode(ctx context.Context, k8sClient client.Client, excludeNodes ...string) (*corev1.Node, error) {
	nodeList := &corev1.NodeList{}

	if err := k8sClient.List(ctx, nodeList, client.MatchingLabels{"node-role.kubernetes.io/worker": ""}); err != nil {
		return nil, fmt.Errorf("failed to list worker nodes: %w", err)
	}

	node := pickRandomReadyNode(nodeList.Items, excludeNodes...)
	if node == nil {
		return nil, fmt.Errorf("no eligible Ready worker node found (excluded: %v)", excludeNodes)
	}

	return node, nil
}

// controlPlaneRoleLabels are the node-role labels that identify a control-plane
// (master) node. Clusters may carry either or both depending on OCP version.
var controlPlaneRoleLabels = []string{
	"node-role.kubernetes.io/control-plane",
	"node-role.kubernetes.io/master",
}

// isControlPlaneNode reports whether the node carries a control-plane/master role label.
func isControlPlaneNode(node *corev1.Node) bool {
	for _, label := range controlPlaneRoleLabels {
		if _, ok := node.Labels[label]; ok {
			return true
		}
	}

	return false
}

// CountControlPlaneNodes returns the total number of nodes carrying a
// control-plane (master) role label. It intentionally counts ALL such nodes
// regardless of Ready/schedulable state, because it is used as a topology guard
// (a real etcd quorum needs at least 3 control-plane nodes) rather than to
// assess current availability.
func CountControlPlaneNodes(ctx context.Context, k8sClient client.Client) (int, error) {
	nodeList := &corev1.NodeList{}
	if err := k8sClient.List(ctx, nodeList); err != nil {
		return 0, fmt.Errorf("failed to list nodes: %w", err)
	}

	count := 0

	for i := range nodeList.Items {
		if isControlPlaneNode(&nodeList.Items[i]) {
			count++
		}
	}

	return count, nil
}

// SelectControlPlaneNode returns a random Ready, schedulable control-plane
// (master) node that is not in the excludeNodes list.
func SelectControlPlaneNode(
	ctx context.Context, k8sClient client.Client, excludeNodes ...string) (*corev1.Node, error) {
	nodeList := &corev1.NodeList{}
	if err := k8sClient.List(ctx, nodeList); err != nil {
		return nil, fmt.Errorf("failed to list nodes: %w", err)
	}

	var masters []corev1.Node

	for i := range nodeList.Items {
		if isControlPlaneNode(&nodeList.Items[i]) {
			masters = append(masters, nodeList.Items[i])
		}
	}

	node := pickRandomReadyNode(masters, excludeNodes...)
	if node == nil {
		return nil, fmt.Errorf("no eligible Ready control-plane node found (excluded: %v)", excludeNodes)
	}

	return node, nil
}

// CountReadyWorkerNodes returns the number of Ready, schedulable worker nodes.
func CountReadyWorkerNodes(ctx context.Context, k8sClient client.Client) (int, error) {
	nodeList := &corev1.NodeList{}
	if err := k8sClient.List(ctx, nodeList, client.MatchingLabels{"node-role.kubernetes.io/worker": ""}); err != nil {
		return 0, fmt.Errorf("failed to list worker nodes: %w", err)
	}

	count := 0

	for i := range nodeList.Items {
		node := &nodeList.Items[i]

		if node.Spec.Unschedulable {
			continue
		}

		if IsNodeReady(node) {
			count++
		}
	}

	return count, nil
}

// ListSchedulableWorkerNodes returns Ready, schedulable nodes that carry the worker role and
// do NOT carry a master or control-plane role label. Excluding control-plane nodes keeps
// resilience/cordon tests from touching control-plane capacity on compact clusters.
func ListSchedulableWorkerNodes(ctx context.Context, k8sClient client.Client) ([]corev1.Node, error) {
	nodeList := &corev1.NodeList{}
	if err := k8sClient.List(ctx, nodeList, client.MatchingLabels{"node-role.kubernetes.io/worker": ""}); err != nil {
		return nil, fmt.Errorf("failed to list worker nodes: %w", err)
	}

	var eligible []corev1.Node

	for i := range nodeList.Items {
		node := &nodeList.Items[i]

		if node.Spec.Unschedulable || !IsNodeReady(node) {
			continue
		}

		if _, hasMaster := node.Labels["node-role.kubernetes.io/master"]; hasMaster {
			continue
		}

		if _, hasCP := node.Labels["node-role.kubernetes.io/control-plane"]; hasCP {
			continue
		}

		eligible = append(eligible, *node)
	}

	return eligible, nil
}
