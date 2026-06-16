package farutils

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/medik8s/system-tests/tests/far-operator/internal/farparams"
)

// RunOnNode executes a command on the specified node using
// "oc debug node/<name> -- chroot /host <cmd>".
func RunOnNode(nodeName string, cmd ...string) (string, error) {
	args := append(
		[]string{"debug", "node/" + nodeName, "--", "chroot", "/host"},
		cmd...,
	)

	ctx, cancel := context.WithTimeout(context.Background(), farparams.OcDebugTimeout)
	defer cancel()

	command := exec.CommandContext(ctx, "oc", args...)

	var stdout, stderr bytes.Buffer

	command.Stdout = &stdout
	command.Stderr = &stderr

	if err := command.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf(
				"oc debug on node %s timed out after %s (stderr: %s)",
				nodeName, farparams.OcDebugTimeout, stderr.String(),
			)
		}

		return "", fmt.Errorf(
			"oc debug on node %s failed: %w (stderr: %s)",
			nodeName, err, stderr.String(),
		)
	}

	return strings.TrimSpace(stdout.String()), nil
}

// StopKubelet stops the kubelet service on the target node.
func StopKubelet(nodeName string) error {
	_, err := RunOnNode(nodeName, "systemctl", "stop", "kubelet")
	if err != nil && strings.Contains(err.Error(), "connection refused") {
		return nil
	}

	return err
}

// StartKubelet starts the kubelet service on the target node.
func StartKubelet(nodeName string) error {
	_, err := RunOnNode(nodeName, "systemctl", "start", "kubelet")

	return err
}

// GetNodeBootID retrieves the boot_id from /proc on the target node.
func GetNodeBootID(nodeName string) (string, error) {
	output, err := RunOnNode(
		nodeName, "cat", "/proc/sys/kernel/random/boot_id",
	)
	if err != nil {
		return "", fmt.Errorf(
			"failed to get boot_id from node %s: %w", nodeName, err,
		)
	}

	return output, nil
}

// GetNodeBootIDFromAPI retrieves the boot ID from the node's
// status.nodeInfo.bootID via the Kubernetes API. This works even when
// the node is recovering (kubelet updates the API before oc debug
// becomes available).
func GetNodeBootIDFromAPI(
	ctx context.Context, k8sClient client.Client, nodeName string,
) (string, error) {
	node := &corev1.Node{}
	if err := k8sClient.Get(ctx, client.ObjectKey{Name: nodeName}, node); err != nil {
		return "", err
	}

	return node.Status.NodeInfo.BootID, nil
}

// WaitForNodeNotReady polls until the node's Ready condition is not True.
func WaitForNodeNotReady(
	ctx context.Context, k8sClient client.Client, nodeName string,
	timeout time.Duration,
) error {
	return wait.PollUntilContextTimeout(
		ctx, farparams.DefaultPollInterval, timeout, true,
		func(ctx context.Context) (bool, error) {
			node := &corev1.Node{}
			if err := k8sClient.Get(
				ctx, client.ObjectKey{Name: nodeName}, node,
			); err != nil {
				return false, err
			}

			for _, cond := range node.Status.Conditions {
				if cond.Type == corev1.NodeReady {
					return cond.Status != corev1.ConditionTrue, nil
				}
			}

			return false, nil
		},
	)
}

// WaitForNodeReady polls until the node's Ready condition is True.
func WaitForNodeReady(
	ctx context.Context, k8sClient client.Client, nodeName string,
	timeout time.Duration,
) error {
	return wait.PollUntilContextTimeout(
		ctx, farparams.DefaultPollInterval, timeout, true,
		func(ctx context.Context) (bool, error) {
			node := &corev1.Node{}
			if err := k8sClient.Get(
				ctx, client.ObjectKey{Name: nodeName}, node,
			); err != nil {
				return false, nil
			}

			for _, cond := range node.Status.Conditions {
				if cond.Type == corev1.NodeReady {
					return cond.Status == corev1.ConditionTrue, nil
				}
			}

			return false, nil
		},
	)
}

// WaitForNodeReboot polls until the node's boot ID (via API) differs
// from the previous boot ID, indicating a reboot occurred.
func WaitForNodeReboot(
	ctx context.Context, k8sClient client.Client, nodeName string,
	previousBootID string, timeout time.Duration,
) error {
	return wait.PollUntilContextTimeout(
		ctx, 10*time.Second, timeout, true,
		func(ctx context.Context) (bool, error) {
			currentID, err := GetNodeBootIDFromAPI(
				ctx, k8sClient, nodeName,
			)
			if err != nil {
				return false, nil
			}

			return currentID != "" && currentID != previousBootID, nil
		},
	)
}
