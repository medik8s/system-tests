package helpers

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	configv1 "github.com/openshift/api/config/v1"

	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// WaitForClusterVersionCondition polls the ClusterVersion object until the
// specified condition reaches the expected status or the timeout expires.
func WaitForClusterVersionCondition(
	ctx context.Context, k8sClient client.Client,
	condType string, condStatus configv1.ConditionStatus,
	timeout, pollInterval time.Duration,
) error {
	err := wait.PollUntilContextTimeout(ctx, pollInterval, timeout, true,
		func(ctx context.Context) (bool, error) {
			clsVer := &configv1.ClusterVersion{}
			if getErr := k8sClient.Get(ctx, client.ObjectKey{Name: "version"}, clsVer); getErr != nil {
				return false, nil
			}

			for _, cond := range clsVer.Status.Conditions {
				if string(cond.Type) == condType && cond.Status == condStatus {
					return true, nil
				}
			}

			return false, nil
		})
	if err != nil {
		return fmt.Errorf("ClusterVersion condition %s did not reach %s: %w", condType, condStatus, err)
	}

	return nil
}

// ApplyIDMSFromSharedDir applies the IDMS YAML saved by the medik8s-catalogsource
// CI step. Returns true if the IDMS was created/configured (MCP rollout expected),
// false if unchanged (no MCP rollout needed).
func ApplyIDMSFromSharedDir(
	ctx context.Context, sharedDir string,
	logf func(string, ...interface{}),
) (bool, error) {
	if sharedDir == "" {
		return false, fmt.Errorf("sharedDir is empty; SHARED_DIR env var may not be set")
	}

	idmsPath := filepath.Join(sharedDir, "idms.yaml")

	if _, statErr := os.Stat(idmsPath); statErr != nil {
		return false, fmt.Errorf("%s not found; medik8s-catalogsource CI step may not have run: %w",
			idmsPath, statErr)
	}

	childCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(childCtx, "oc", "apply", "-f", idmsPath)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("oc apply -f %s failed: %w\nOutput: %s", idmsPath, err, output)
	}

	result := strings.TrimSpace(string(output))
	logf("Applied IDMS from %s: %s\n", idmsPath, result)

	return !strings.Contains(result, "unchanged"), nil
}

// MCPGeneration holds the metadata.generation for a single MachineConfigPool,
// captured before a config change so WaitForMCPRollout can detect new rollouts.
type MCPGeneration struct {
	Name       string `json:"name"`
	Generation int64  `json:"generation"`
}

// GetMCPGenerations returns the current metadata.generation for every MCP.
func GetMCPGenerations(ctx context.Context) ([]MCPGeneration, error) {
	childCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(childCtx, "oc", "get", "mcp", "-o",
		"jsonpath={range .items[*]}{.metadata.name}{\" \"}{.metadata.generation}{\"\\n\"}{end}")

	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to get MCP generations: %w\nOutput: %s", err, output)
	}

	var gens []MCPGeneration

	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}

		gen, parseErr := strconv.ParseInt(fields[1], 10, 64)
		if parseErr != nil {
			return nil, fmt.Errorf("failed to parse generation for MCP %s: %w", fields[0], parseErr)
		}

		gens = append(gens, MCPGeneration{Name: fields[0], Generation: gen})
	}

	return gens, nil
}

// WaitForMCPRollout waits for any MCP's generation to increment (detecting the
// IDMS change), then waits for all MCPs to reach Updated=True.
func WaitForMCPRollout(
	ctx context.Context, preGens []MCPGeneration,
	detectionTimeout, rolloutTimeout, pollInterval time.Duration,
	logf func(string, ...interface{}),
) error {
	preGenMap := make(map[string]int64, len(preGens))
	for _, g := range preGens {
		preGenMap[g.Name] = g.Generation
	}

	genJSON, _ := json.Marshal(preGens)
	logf("Pre-IDMS MCP generations: %s\n", genJSON)

	if err := wait.PollUntilContextTimeout(ctx, pollInterval, detectionTimeout, true,
		func(ctx context.Context) (bool, error) {
			currentGens, err := GetMCPGenerations(ctx)
			if err != nil {
				logf("WARNING: failed to read MCP generations: %v\n", err)

				return false, nil
			}

			for _, cur := range currentGens {
				if prev, ok := preGenMap[cur.Name]; ok && cur.Generation > prev {
					logf("MCP %s generation changed: %d -> %d\n",
						cur.Name, prev, cur.Generation)

					return true, nil
				}
			}

			return false, nil
		}); err != nil {
		return fmt.Errorf("MCO did not detect IDMS change within %s: %w", detectionTimeout, err)
	}

	mcpCtx, cancel := context.WithTimeout(ctx, rolloutTimeout+time.Minute)
	defer cancel()

	cmd := exec.CommandContext(mcpCtx, "oc", "wait", "mcp", "--all",
		"--for=condition=Updated",
		fmt.Sprintf("--timeout=%ds", int(rolloutTimeout.Seconds())))

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("MCP rollout did not complete: %w\nOutput: %s", err, output)
	}

	logf("All MachineConfigPools updated\n")

	return nil
}
