package helpers

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// CreateWorkloadPod creates a pod pinned to the specified node and waits for it
// to reach Running. Returns the generated pod name. The caller is responsible
// for cleanup via DeleteWorkloadPod.
func CreateWorkloadPod(
	ctx context.Context, k8sClient client.Client,
	nodeName, namespace, image, generateNamePrefix string,
	readyTimeout, pollInterval time.Duration,
) (string, error) {
	workloadPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: generateNamePrefix,
			Namespace:    namespace,
		},
		Spec: corev1.PodSpec{
			NodeName:      nodeName,
			RestartPolicy: corev1.RestartPolicyAlways,
			Containers: []corev1.Container{{
				Name:    "workload",
				Image:   image,
				Command: []string{"sleep", "infinity"},
			}},
		},
	}

	if err := k8sClient.Create(ctx, workloadPod); err != nil {
		return "", fmt.Errorf("failed to create workload pod on %s: %w", nodeName, err)
	}

	podName := workloadPod.Name

	if err := wait.PollUntilContextTimeout(ctx, pollInterval, readyTimeout, true,
		func(ctx context.Context) (bool, error) {
			pod := &corev1.Pod{}
			if getErr := k8sClient.Get(ctx, client.ObjectKey{
				Name:      podName,
				Namespace: namespace,
			}, pod); getErr != nil {
				return false, nil
			}

			if pod.Status.Phase != corev1.PodRunning {
				return false, nil
			}

			for _, containerStatus := range pod.Status.ContainerStatuses {
				if !containerStatus.Ready {
					return false, nil
				}
			}

			return len(pod.Status.ContainerStatuses) == len(pod.Spec.Containers), nil
		}); err != nil {
		return podName, fmt.Errorf("workload pod %s did not reach Running on %s: %w",
			podName, nodeName, err)
	}

	return podName, nil
}

// DeleteWorkloadPod deletes a workload pod by name, ignoring NotFound errors.
func DeleteWorkloadPod(
	ctx context.Context, k8sClient client.Client,
	podName, namespace string,
	logf func(string, ...interface{}),
) {
	cleanupPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: namespace,
		},
	}

	if delErr := k8sClient.Delete(ctx, cleanupPod); delErr != nil &&
		!k8serrors.IsNotFound(delErr) {
		logf("WARNING: failed to delete workload pod %s: %v\n",
			podName, delErr)
	}
}

// RemoveWorkloadImage runs crictl rmi on a node to prevent CRI-O overlay
// corruption after reboot.
func RemoveWorkloadImage(
	ctx context.Context, nodeName, image string, timeout time.Duration,
	logf func(string, ...interface{}),
) {
	logf("Removing workload image from node %s to prevent corrupt overlay layers\n",
		nodeName)

	output, err := RunOnNode(
		ctx, nodeName, timeout,
		"bash", "-c",
		"crictl rmi "+image+" 2>/dev/null; echo done",
	)
	if err != nil {
		logf("WARNING: image removal on node %s failed: %v (output: %s)\n",
			nodeName, err, output)

		return
	}

	logf("Workload image removed from node %s (output: %s)\n",
		nodeName, output)
}
