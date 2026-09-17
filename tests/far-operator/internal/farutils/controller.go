package farutils

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/medik8s/system-tests/tests/far-operator/internal/farparams"
	"github.com/medik8s/system-tests/tests/internal/helpers"
	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
)

// GetActiveFARControllerNode returns the node name hosting the active FAR
// controller pod by inspecting the leader election lease.
func GetActiveFARControllerNode(ctx context.Context, k8sClient client.Client) (string, error) {
	return helpers.GetActiveControllerNode(ctx, k8sClient,
		farparams.ControllerLeaseName, medik8sparams.OperatorNs)
}

// GetFARControllerPods returns the running FAR controller manager pods.
func GetFARControllerPods(ctx context.Context, k8sClient client.Client) ([]corev1.Pod, error) {
	podList := &corev1.PodList{}
	opts := []client.ListOption{
		client.InNamespace(medik8sparams.OperatorNs),
		client.MatchingLabels(farparams.OperatorControllerPodLabels),
	}

	if err := k8sClient.List(ctx, podList, opts...); err != nil {
		return nil, fmt.Errorf("failed to list FAR controller pods: %w", err)
	}

	var running []corev1.Pod

	for _, pod := range podList.Items {
		if pod.Status.Phase != corev1.PodRunning || pod.DeletionTimestamp != nil {
			continue
		}

		if len(pod.Status.ContainerStatuses) == 0 ||
			len(pod.Status.ContainerStatuses) != len(pod.Spec.Containers) {
			continue
		}

		allReady := true

		for _, cs := range pod.Status.ContainerStatuses {
			if !cs.Ready {
				allReady = false

				break
			}
		}

		if allReady {
			running = append(running, pod)
		}
	}

	return running, nil
}
