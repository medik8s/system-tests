package helpers

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// LogControllerState lists controller pods matching the given labels and logs
// their phase, node, and readiness for debugging.
func LogControllerState(
	ctx context.Context, k8sClient client.Client,
	namespace string, podLabels map[string]string,
	logf func(string, ...interface{}),
) {
	pods := &corev1.PodList{}

	if err := k8sClient.List(ctx, pods,
		client.InNamespace(namespace),
		client.MatchingLabels(podLabels)); err != nil {
		logf("WARNING: could not list controller pods: %v\n", err)

		return
	}

	for i := range pods.Items {
		pod := &pods.Items[i]
		ready := false

		for _, cond := range pod.Status.Conditions {
			if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
				ready = true

				break
			}
		}

		logf("Controller pod %s: Phase=%s, Node=%s, Ready=%v\n",
			pod.Name, pod.Status.Phase, pod.Spec.NodeName, ready)
	}
}
