package farutils

import (
	"context"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/medik8s/system-tests/tests/far-operator/internal/farparams"
	"github.com/medik8s/system-tests/tests/internal/helpers"
)

// RunOnNode executes a command on the specified node using oc debug,
// with the FAR-specific OcDebugTimeout.
func RunOnNode(ctx context.Context, nodeName string, cmd ...string) (string, error) {
	return helpers.RunOnNode(ctx, nodeName, farparams.OcDebugTimeout, cmd...)
}

// StopKubelet stops the kubelet service on the target node.
func StopKubelet(
	ctx context.Context, nodeName string,
	logf func(string, ...interface{}),
) error {
	return helpers.StopKubelet(ctx, nodeName, farparams.OcDebugTimeout, logf)
}

// StartKubelet attempts to start the kubelet service on the target node.
// NOTE: This uses "oc debug node/" which requires a running kubelet to
// schedule the debug pod. It CANNOT recover a node whose kubelet was
// stopped via StopKubelet. Use it only as a best-effort safety net for
// scenarios where kubelet has already restarted (e.g., after a node
// reboot triggered by the FAR operator).
func StartKubelet(ctx context.Context, nodeName string) error {
	return helpers.StartKubelet(ctx, nodeName, farparams.OcDebugTimeout)
}

// GetNodeBootID retrieves the boot_id from /proc on the target node via
// oc debug, with the FAR-specific OcDebugTimeout.
func GetNodeBootID(ctx context.Context, nodeName string) (string, error) {
	return helpers.GetNodeBootID(ctx, nodeName, farparams.OcDebugTimeout)
}

// GetNodeBootIDFromAPI retrieves the boot ID from the node's
// status.nodeInfo.bootID via the Kubernetes API.
func GetNodeBootIDFromAPI(
	ctx context.Context, k8sClient client.Client, nodeName string,
) (string, error) {
	return helpers.GetNodeBootIDFromAPI(ctx, k8sClient, nodeName)
}

// WaitForNodeNotReady polls until the node's Ready condition is not True.
func WaitForNodeNotReady(
	ctx context.Context, k8sClient client.Client, nodeName string,
	timeout time.Duration,
	logf func(string, ...interface{}),
) error {
	return helpers.WaitForNodeNotReady(
		ctx, k8sClient, nodeName, farparams.DefaultPollInterval, timeout, logf,
	)
}

// WaitForNodeReady polls until the node's Ready condition is True.
func WaitForNodeReady(
	ctx context.Context, k8sClient client.Client, nodeName string,
	timeout time.Duration,
	logf func(string, ...interface{}),
) error {
	return helpers.WaitForNodeReady(
		ctx, k8sClient, nodeName, farparams.DefaultPollInterval, timeout, logf,
	)
}

// WaitForNodeReboot polls until the node's boot ID (via API) differs
// from the previous boot ID, indicating a reboot occurred.
func WaitForNodeReboot(
	ctx context.Context, k8sClient client.Client, nodeName string,
	previousBootID string, timeout time.Duration,
	logf func(string, ...interface{}),
) error {
	return helpers.WaitForNodeReboot(
		ctx, k8sClient, nodeName, previousBootID,
		farparams.BootIDPollInterval, timeout, logf,
	)
}
