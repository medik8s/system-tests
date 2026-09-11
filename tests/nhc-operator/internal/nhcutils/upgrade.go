package nhcutils

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"

	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/clients"

	"github.com/medik8s/system-tests/tests/internal/helpers"
	"github.com/medik8s/system-tests/tests/nhc-operator/internal/nhcparams"
)

// RunOperatorSDK runs one bounded operator-sdk command and preserves its combined output.
func RunOperatorSDK(ctx context.Context, binary string, args ...string) (string, error) {
	commandCtx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()

	command := exec.CommandContext(commandCtx, binary, args...)

	var output bytes.Buffer

	command.Stdout, command.Stderr = &output, &output
	if err := command.Run(); err != nil {
		return output.String(), fmt.Errorf("%s %v: %w", binary, args, err)
	}

	return output.String(), nil
}

// InstallBundle installs a bundle into the scenario's namespace.
func InstallBundle(ctx context.Context, binary, namespace, bundle string) (string, error) {
	return RunOperatorSDK(ctx, binary, "run", "bundle", "-n", namespace, bundle)
}

// UpgradeBundle upgrades the existing bundle installation in place.
func UpgradeBundle(ctx context.Context, binary, namespace, bundle string) (string, error) {
	return RunOperatorSDK(ctx, binary, "run", "bundle-upgrade", "-n", namespace, bundle)
}

// CleanupBundle intentionally leaves CRDs and the namespace OperatorGroup
// alone, since both can be shared by other operators.
func CleanupBundle(ctx context.Context, binary, namespace, packageName string) (string, error) {
	return RunOperatorSDK(ctx, binary, "cleanup", packageName, "-n", namespace,
		"--delete-all=false", "--delete-crds=false", "--delete-operator-groups=false")
}

// GetNHCControllerImage returns the manager image from a running controller.
func GetNHCControllerImage(apiClient *clients.Settings) (string, error) {
	return helpers.GetControllerImage(apiClient, "openshift-workload-availability",
		nhcparams.OperatorControllerPodLabelSelector, nhcparams.ManagerContainerName)
}

// CollectFailureEvidence is best-effort so the original assertion remains the
// reported failure when the local environment has no oc binary.
func CollectFailureEvidence(ctx context.Context, namespace string) string {
	var evidence bytes.Buffer

	for _, args := range [][]string{
		{"get", "subscriptions,clusterserviceversions,installplans,catalogsources,pods", "-n", namespace, "-o", "yaml"},
		{"get", "events", "-n", namespace, "--sort-by=.lastTimestamp"},
		{"get", "nodehealthchecks", "-o", "yaml"},
		{"logs", "deployment/" + nhcparams.OperatorDeploymentName, "-n", namespace, "--all-containers=true", "--tail=500"},
	} {
		output, err := RunCommand(ctx, "oc", args...)
		fmt.Fprintf(&evidence, "oc %v (error=%v):\n%s\n", args, err, output)
	}

	return evidence.String()
}

// RunCommand runs a bounded diagnostic command and returns its combined output.
func RunCommand(ctx context.Context, binary string, args ...string) (string, error) {
	commandCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	command := exec.CommandContext(commandCtx, binary, args...)

	var output bytes.Buffer

	command.Stdout, command.Stderr = &output, &output
	err := command.Run()

	return output.String(), err
}
