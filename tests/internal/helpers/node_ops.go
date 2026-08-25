package helpers

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"
)

// RunOnNode executes a command on the specified node using
// "oc debug node/<name> -- chroot /host <cmd>".
func RunOnNode(
	ctx context.Context, nodeName string, timeout time.Duration,
	cmd ...string,
) (string, error) {
	if nodeName == "" {
		return "", fmt.Errorf("RunOnNode: nodeName must not be empty")
	}

	if len(cmd) == 0 {
		return "", fmt.Errorf("RunOnNode: cmd must not be empty")
	}

	args := append(
		[]string{"debug", "node/" + nodeName, "-n", "default", "--", "chroot", "/host"},
		cmd...,
	)

	childCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	command := exec.CommandContext(childCtx, "oc", args...)

	var stdout, stderr bytes.Buffer

	command.Stdout = &stdout
	command.Stderr = &stderr

	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf(
				"oc debug on node %s: parent context expired (stderr: %s)",
				nodeName, stderr.String(),
			)
		}

		if childCtx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf(
				"oc debug on node %s timed out after %s (stderr: %s)",
				nodeName, timeout, stderr.String(),
			)
		}

		return "", fmt.Errorf(
			"oc debug on node %s failed: %w (stderr: %s)",
			nodeName, err, stderr.String(),
		)
	}

	return strings.TrimSpace(stdout.String()), nil
}

// kubeletStopGuardPath is the host path used by StopKubelet to prevent
// re-execution of "systemctl stop kubelet" after a node reboot.
const kubeletStopGuardPath = "/var/tmp/.medik8s-kubelet-stop-guard"

// defaultBastionUser is the SSH user for both in-cluster and external
// bastions when no bastion_ssh_user file is present in SHARED_DIR.
const defaultBastionUser = "core"

// defaultNodeUser is the SSH user for connecting to cluster nodes
// (distinct from defaultBastionUser which is for the bastion hop).
const defaultNodeUser = "core"

// StopKubelet stops the kubelet service on the target node via
// "oc debug node/". A guard file is created on the host before
// stopping kubelet so that if the debug pod is re-executed after a
// hard reboot (CRI-O cleans stale containers, kubelet re-discovers
// the pod), the stop is skipped. Callers must call
// RemoveKubeletStopGuard after the node recovers so that future
// StopKubelet calls take effect.
func StopKubelet(
	ctx context.Context, nodeName string, timeout time.Duration,
	logf func(string, ...interface{}),
) error {
	cmd := fmt.Sprintf(
		`g=%s; [ -f "$g" ] && echo GUARD_SKIP && exit 0; touch "$g" && systemctl stop kubelet || { rm -f "$g"; exit 1; }`,
		kubeletStopGuardPath,
	)

	output, err := RunOnNode(ctx, nodeName, timeout, "sh", "-c", cmd)
	if output == "GUARD_SKIP" {
		logf("StopKubelet(%s): guard file found, skipping (previous stop still active)\n", nodeName)

		return nil
	}

	if err != nil {
		errMsg := err.Error()
		if strings.Contains(errMsg, "connection reset") ||
			strings.Contains(errMsg, "lost connection") ||
			strings.Contains(errMsg, "closed network connection") ||
			strings.Contains(errMsg, "broken pipe") ||
			strings.Contains(errMsg, "transport is closing") {
			logf("StopKubelet(%s): suppressed expected connection-loss error "+
				"(kubelet likely stopped): %v\n", nodeName, err)

			return nil
		}

		return err
	}

	return nil
}

// RemoveKubeletStopGuard removes the guard file left by StopKubelet so
// that a subsequent StopKubelet call on the same node will take effect.
func RemoveKubeletStopGuard(
	ctx context.Context, nodeName string, timeout time.Duration,
) error {
	_, err := RunOnNode(ctx, nodeName, timeout,
		"rm", "-f", kubeletStopGuardPath)

	return err
}

// StartKubelet attempts to start the kubelet service on the target node.
// NOTE: This uses "oc debug node/" which requires a running kubelet to
// schedule the debug pod. It CANNOT recover a node whose kubelet was
// stopped via StopKubelet. Use it only as a best-effort safety net for
// scenarios where kubelet has already restarted (e.g., after a reboot).
func StartKubelet(
	ctx context.Context, nodeName string, timeout time.Duration,
) error {
	_, err := RunOnNode(ctx, nodeName, timeout, "systemctl", "start", "kubelet")

	return err
}

// GetNodeInternalIP returns the InternalIP address for the given node.
func GetNodeInternalIP(
	ctx context.Context, k8sClient client.Client, nodeName string,
) (string, error) {
	node := &corev1.Node{}
	if err := k8sClient.Get(ctx, client.ObjectKey{Name: nodeName}, node); err != nil {
		return "", fmt.Errorf("failed to get node %s: %w", nodeName, err)
	}

	for _, addr := range node.Status.Addresses {
		if addr.Type == corev1.NodeInternalIP {
			return addr.Address, nil
		}
	}

	return "", fmt.Errorf("no InternalIP found for node %s", nodeName)
}

// sshKeyOnce ensures findSSHKey resolves the key once per process.
var sshKeyOnce sync.Once
var sshKeyPath string
var sshKeyErr error

// findSSHKey finds the first available SSH private key, copies it to a
// temp file with 0600 permissions, and caches the path for reuse.
// The copy is needed because ci-operator mounts secrets as 0644
// (Kubernetes read-only mount) and ssh rejects keys with open permissions.
// The temp file lives for the process lifetime (no cleanup needed).
// FindSSHKey is the exported version for callers that need to check availability.
var FindSSHKey = findSSHKey

func findSSHKey() (string, error) {
	sshKeyOnce.Do(func() {
		candidates := []string{
			os.Getenv("CLUSTER_PROFILE_DIR") + "/ssh-privatekey",
			"/home/kni/.ssh/id_rsa",
			os.Getenv("HOME") + "/.ssh/id_rsa",
		}

		for _, p := range candidates {
			if p == "/ssh-privatekey" || p == "/.ssh/id_rsa" {
				continue // skip if env var was empty
			}

			data, err := os.ReadFile(p)
			if err != nil {
				continue
			}

			tmpFile, createErr := os.CreateTemp("", "ssh-key-*")
			if createErr != nil {
				sshKeyErr = fmt.Errorf("findSSHKey: create temp file: %w", createErr)

				return
			}

			if err := tmpFile.Chmod(0o600); err != nil {
				os.Remove(tmpFile.Name())

				sshKeyErr = fmt.Errorf("findSSHKey: chmod: %w", err)

				return
			}

			if _, err := tmpFile.Write(data); err != nil {
				os.Remove(tmpFile.Name())

				sshKeyErr = fmt.Errorf("findSSHKey: write: %w", err)

				return
			}

			if err := tmpFile.Close(); err != nil {
				os.Remove(tmpFile.Name())

				sshKeyErr = fmt.Errorf("findSSHKey: close: %w", err)

				return
			}

			fmt.Fprintf(os.Stderr, "findSSHKey: using %s (copied from %s)\n",
				tmpFile.Name(), p)
			sshKeyPath = tmpFile.Name()

			return
		}

		sshKeyErr = fmt.Errorf(
			"no SSH private key found; checked: $CLUSTER_PROFILE_DIR/ssh-privatekey, " +
				"/home/kni/.ssh/id_rsa, $HOME/.ssh/id_rsa")
	})

	return sshKeyPath, sshKeyErr
}

// readSharedDirFile reads a file from the $SHARED_DIR directory and
// returns its trimmed content. Returns empty string if SHARED_DIR is
// unset, the file does not exist, an unexpected read error occurs
// (logged to stderr), or the content is empty after trimming.
func readSharedDirFile(name string) string {
	dir := os.Getenv("SHARED_DIR")
	if dir == "" {
		return ""
	}

	path := filepath.Join(dir, name)

	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "readSharedDirFile: unexpected error reading %s: %v\n", path, err)
		}

		return ""
	}

	return strings.TrimSpace(string(data))
}

func isInvalidHostname(addr string) bool {
	invalid := strings.ContainsFunc(addr, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') &&
			(r < '0' || r > '9') && r != '.' && r != '-' && r != ':'
	})
	if invalid {
		fmt.Fprintf(os.Stderr,
			"findSSHBastion: bastion_public_address contains invalid characters: %q\n", addr)
	}

	return invalid
}

func isInvalidSSHUser(user string) bool {
	invalid := strings.ContainsFunc(user, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') &&
			(r < '0' || r > '9') && r != '-' && r != '_' && r != '.'
	})
	if invalid {
		fmt.Fprintf(os.Stderr,
			"findSSHBastion: bastion_ssh_user contains invalid characters: %q, using default %q\n",
			user, defaultBastionUser)
	}

	return invalid
}

// sshBastionOnce resolves the bastion host once per process.
var sshBastionOnce sync.Once
var sshBastionHost string
var sshBastionUser string

// findSSHBastion checks for an SSH bastion host in order:
//  1. External bastion from $SHARED_DIR/bastion_public_address
//     (provisioned by disconnected AWS workflows via ipi-aws-pre-disconnected)
//  2. In-cluster ssh-bastion Service in test-ssh-bastion namespace
//     (deployed by the ssh-bastion step-registry ref on connected clusters)
//
// The external bastion is checked first because in disconnected environments
// the in-cluster ssh-bastion pod cannot pull its image (quay.io is unreachable).
// Returns the bastion hostname, or empty string if not available.
func findSSHBastion() string {
	sshBastionOnce.Do(func() {
		if addr := readSharedDirFile("bastion_public_address"); addr != "" &&
			!isInvalidHostname(addr) {
			sshBastionHost = addr

			if user := readSharedDirFile("bastion_ssh_user"); user != "" &&
				!isInvalidSSHUser(user) {
				sshBastionUser = user
			} else {
				sshBastionUser = defaultBastionUser
			}

			fmt.Fprintf(os.Stderr,
				"findSSHBastion: using external bastion %s (user: %s)\n",
				sshBastionHost, sshBastionUser)

			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		out, err := exec.CommandContext(ctx, "oc", "get", "service", "ssh-bastion",
			"-n", "test-ssh-bastion",
			"-o", "jsonpath={.status.loadBalancer.ingress[0].hostname}").Output()
		if err == nil && len(out) > 0 {
			sshBastionHost = strings.TrimSpace(string(out))
			sshBastionUser = defaultBastionUser

			fmt.Fprintf(os.Stderr, "findSSHBastion: using in-cluster bastion %s\n", sshBastionHost)
		}
	})

	return sshBastionHost
}

// findSSHBastionUser returns the SSH user for the bastion host.
// Internally calls findSSHBastion() (guarded by sync.Once) to ensure
// both host and user are resolved from the same source.
func findSSHBastionUser() string {
	findSSHBastion()

	if sshBastionUser == "" {
		return defaultBastionUser
	}

	return sshBastionUser
}

// runSSH executes a command on a node via SSH to the core user.
// Unlike oc debug, SSH works even when kubelet is stopped because
// sshd runs independently of kubelet.
// On Prow AWS, SSH is proxied through an SSH bastion (ProxyCommand).
func runSSH(
	ctx context.Context, nodeIP string, timeout time.Duration, cmd string,
) (string, error) {
	childCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "BatchMode=yes",
		"-o", "LogLevel=ERROR",
		"-o", "ConnectTimeout=10",
	}

	keyPath, keyErr := findSSHKey()
	if keyErr != nil {
		return "", fmt.Errorf("runSSH: %w", keyErr)
	}

	args = append(args, "-i", keyPath)

	// On Prow AWS, direct SSH is blocked by security groups.
	// Proxy through an SSH bastion if available (external or in-cluster).
	// ProxyCommand (not ProxyJump) is used so we can pass -i and
	// host-key options to the bastion hop explicitly.
	if bastion := findSSHBastion(); bastion != "" {
		bastionUser := findSSHBastionUser()
		proxyCmd := fmt.Sprintf(
			"ProxyCommand=ssh -i %s -o StrictHostKeyChecking=no "+
				"-o UserKnownHostsFile=/dev/null -W %%h:%%p %s@%s",
			keyPath, bastionUser, bastion)
		args = append(args, "-o", proxyCmd)
	}

	args = append(args, fmt.Sprintf("%s@%s", defaultNodeUser, nodeIP), cmd)

	command := exec.CommandContext(childCtx, "ssh", args...)

	var stdout, stderr bytes.Buffer

	command.Stdout = &stdout
	command.Stderr = &stderr

	if err := command.Run(); err != nil {
		return "", fmt.Errorf(
			"SSH to %s@%s failed: %w (stderr: %s)",
			defaultNodeUser, nodeIP, err, stderr.String(),
		)
	}

	return strings.TrimSpace(stdout.String()), nil
}

// StopKubeletSSH stops kubelet on the target node via SSH.
// Uses SSH instead of oc debug because the debug pod connection
// drops when kubelet stops, causing unreliable timeout errors.
func StopKubeletSSH(
	ctx context.Context, k8sClient client.Client,
	nodeName string, timeout time.Duration,
) error {
	ip, err := GetNodeInternalIP(ctx, k8sClient, nodeName)
	if err != nil {
		return err
	}

	_, err = runSSH(ctx, ip, timeout, "sudo systemctl stop kubelet")
	if err != nil {
		// When kubelet stops, the SSH connection may drop.
		// This is expected behavior -- kubelet is likely stopped.
		errMsg := err.Error()
		if strings.Contains(errMsg, "connection reset") ||
			strings.Contains(errMsg, "closed network connection") ||
			strings.Contains(errMsg, "broken pipe") ||
			strings.Contains(errMsg, "transport is closing") ||
			strings.Contains(errMsg, "lost connection") ||
			strings.Contains(errMsg, "closed by remote host") {
			fmt.Fprintf(os.Stderr,
				"StopKubeletSSH(%s): suppressed expected connection-loss "+
					"error (kubelet likely stopped): %v\n", nodeName, err)

			return nil
		}

		return err
	}

	return nil
}

// StartKubeletSSH starts kubelet on the target node via SSH.
// This is the only reliable way to restart kubelet on a node where
// it was previously stopped -- oc debug cannot schedule a pod when
// kubelet is down, but SSH connects directly to sshd which runs
// independently of kubelet.
// After starting kubelet, a daemon-reload is issued to ensure systemd
// picks up any unit file changes from the reboot cycle (matches the
// Python reference implementation).
func StartKubeletSSH(
	ctx context.Context, k8sClient client.Client,
	nodeName string, timeout time.Duration,
) error {
	ip, err := GetNodeInternalIP(ctx, k8sClient, nodeName)
	if err != nil {
		return err
	}

	if _, err := runSSH(ctx, ip, timeout, "sudo systemctl daemon-reload"); err != nil {
		return err
	}

	_, err = runSSH(ctx, ip, timeout, "sudo systemctl start kubelet")

	return err
}

// DisableKubeletSSH disables and stops kubelet on the target node via SSH.
// Unlike StopKubeletSSH, kubelet will NOT restart after a node reboot.
// Used by NHC escalation tests where the node must remain unhealthy
// even after SNR reboots it, forcing escalation to the next remediator.
// Callers MUST register a DeferCleanup with EnableKubeletSSH.
func DisableKubeletSSH(
	ctx context.Context, k8sClient client.Client,
	nodeName string, timeout time.Duration,
) error {
	ip, err := GetNodeInternalIP(ctx, k8sClient, nodeName)
	if err != nil {
		return err
	}

	_, err = runSSH(ctx, ip, timeout, "sudo systemctl disable kubelet --now")
	if err != nil {
		errMsg := err.Error()
		if strings.Contains(errMsg, "connection reset") ||
			strings.Contains(errMsg, "closed network connection") ||
			strings.Contains(errMsg, "broken pipe") ||
			strings.Contains(errMsg, "lost connection") ||
			strings.Contains(errMsg, "closed by remote host") ||
			strings.Contains(errMsg, "transport is closing") {
			fmt.Fprintf(os.Stderr,
				"DisableKubeletSSH(%s): suppressed expected connection-loss "+
					"error (kubelet likely disabled): %v\n", nodeName, err)

			return nil
		}

		return err
	}

	return nil
}

// EnableKubeletSSH re-enables and starts kubelet on the target node via SSH.
// Used to recover a node after DisableKubeletSSH, including after a reboot
// (where kubelet would not auto-start because it was disabled).
// Retries SSH connection because the node may be mid-reboot when called.
func EnableKubeletSSH(
	ctx context.Context, k8sClient client.Client,
	nodeName string, retryTimeout time.Duration,
	logf func(string, ...interface{}),
) error {
	ip, err := GetNodeInternalIP(ctx, k8sClient, nodeName)
	if err != nil {
		return err
	}

	const (
		sshAttemptTimeout = 15 * time.Second
		sshRetryInterval  = 5 * time.Second
	)

	return wait.PollUntilContextTimeout(ctx, sshRetryInterval, retryTimeout, true,
		func(ctx context.Context) (bool, error) {
			if _, sshErr := runSSH(ctx, ip, sshAttemptTimeout, "sudo systemctl enable kubelet"); sshErr != nil {
				logf("EnableKubeletSSH(%s): enable attempt failed (may be mid-reboot): %v\n",
					nodeName, sshErr)

				return false, nil
			}

			if _, sshErr := runSSH(ctx, ip, sshAttemptTimeout, "sudo systemctl daemon-reload"); sshErr != nil {
				logf("EnableKubeletSSH(%s): daemon-reload failed: %v\n", nodeName, sshErr)

				return false, nil
			}

			if _, sshErr := runSSH(ctx, ip, sshAttemptTimeout, "sudo systemctl start kubelet"); sshErr != nil {
				logf("EnableKubeletSSH(%s): start attempt failed: %v\n", nodeName, sshErr)

				return false, nil
			}

			logf("EnableKubeletSSH(%s): kubelet enabled and started\n", nodeName)

			return true, nil
		})
}

// GetNodeBootID retrieves the boot_id from /proc on the target node via
// oc debug. Requires a running kubelet; use GetNodeBootIDFromAPI when the
// node is down or recovering.
func GetNodeBootID(
	ctx context.Context, nodeName string, timeout time.Duration,
) (string, error) {
	output, err := RunOnNode(
		ctx, nodeName, timeout, "cat", "/proc/sys/kernel/random/boot_id",
	)
	if err != nil {
		return "", fmt.Errorf(
			"failed to get boot_id from node %s: %w", nodeName, err,
		)
	}

	if output == "" {
		return "", fmt.Errorf("node %s returned empty boot_id", nodeName)
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

	bootID := node.Status.NodeInfo.BootID
	if bootID == "" {
		return "", fmt.Errorf("node %s has empty status.nodeInfo.bootID", nodeName)
	}

	return bootID, nil
}

// WaitForNodeNotReady polls until the node's Ready condition is not True.
func WaitForNodeNotReady(
	ctx context.Context, k8sClient client.Client, nodeName string,
	pollInterval, timeout time.Duration,
	logf func(string, ...interface{}),
) error {
	return waitForNodeCondition(
		ctx, k8sClient, nodeName, pollInterval, timeout,
		func(node *corev1.Node) bool { return !IsNodeReady(node) },
		"NotReady", logf,
	)
}

// WaitForNodeReady polls until the node's Ready condition is True.
func WaitForNodeReady(
	ctx context.Context, k8sClient client.Client, nodeName string,
	pollInterval, timeout time.Duration,
	logf func(string, ...interface{}),
) error {
	return waitForNodeCondition(
		ctx, k8sClient, nodeName, pollInterval, timeout,
		IsNodeReady,
		"Ready", logf,
	)
}

func waitForNodeCondition(
	ctx context.Context, k8sClient client.Client, nodeName string,
	pollInterval, timeout time.Duration,
	conditionFn func(*corev1.Node) bool, conditionDesc string,
	logf func(string, ...interface{}),
) error {
	err := wait.PollUntilContextTimeout(
		ctx, pollInterval, timeout, true,
		func(ctx context.Context) (bool, error) {
			node := &corev1.Node{}
			if err := k8sClient.Get(
				ctx, client.ObjectKey{Name: nodeName}, node,
			); err != nil {
				if k8serrors.IsNotFound(err) {
					return false, fmt.Errorf(
						"node %s was deleted during readiness wait: %w",
						nodeName, err)
				}

				if k8serrors.IsForbidden(err) || k8serrors.IsUnauthorized(err) {
					return false, fmt.Errorf(
						"permanent API error fetching node %s: %w",
						nodeName, err)
				}

				logf("waitForNodeCondition(%s, %s): transient API error, retrying: %v\n",
					nodeName, conditionDesc, err)

				return false, nil
			}

			return conditionFn(node), nil
		},
	)
	if err != nil {
		if wait.Interrupted(err) {
			return fmt.Errorf(
				"timed out after %s waiting for node %s to become %s: %w",
				timeout, nodeName, conditionDesc, err)
		}

		return fmt.Errorf(
			"failed waiting for node %s to become %s: %w",
			nodeName, conditionDesc, err)
	}

	return nil
}

// WaitForNodeReboot polls until the node's boot ID (via API) differs
// from the previous boot ID, indicating a reboot occurred.
func WaitForNodeReboot(
	ctx context.Context, k8sClient client.Client, nodeName string,
	previousBootID string, pollInterval, timeout time.Duration,
	logf func(string, ...interface{}),
) error {
	if previousBootID == "" {
		return fmt.Errorf("WaitForNodeReboot(%s): previousBootID must not be empty", nodeName)
	}

	err := wait.PollUntilContextTimeout(
		ctx, pollInterval, timeout, true,
		func(ctx context.Context) (bool, error) {
			currentID, err := GetNodeBootIDFromAPI(
				ctx, k8sClient, nodeName,
			)
			if err != nil {
				if k8serrors.IsNotFound(err) {
					return false, fmt.Errorf("node %s was deleted during reboot wait: %w", nodeName, err)
				}

				if k8serrors.IsForbidden(err) || k8serrors.IsUnauthorized(err) {
					return false, fmt.Errorf(
						"permanent API error fetching node %s: %w",
						nodeName, err)
				}

				logf("WaitForNodeReboot(%s): transient API error, retrying: %v\n",
					nodeName, err)

				return false, nil
			}

			return currentID != "" && currentID != previousBootID, nil
		},
	)
	if err != nil {
		if wait.Interrupted(err) {
			return fmt.Errorf(
				"timed out after %s waiting for node %s to reboot (previous boot ID: %s): %w",
				timeout, nodeName, previousBootID, err)
		}

		return fmt.Errorf(
			"failed waiting for node %s to reboot: %w",
			nodeName, err)
	}

	return nil
}
