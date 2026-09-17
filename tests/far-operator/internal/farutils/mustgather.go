package farutils

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/medik8s/system-tests/tests/far-operator/internal/farparams"
)

// MustGatherExpectation describes a required artifact in the must-gather output.
// PathContains is matched against each file's relative path (substring match).
// NameGlob is matched against each file's basename using filepath.Match.
// Both conditions must be true for a file to count as a match.
// Set PathContains to "" to match any path (basename-only matching).
type MustGatherExpectation struct {
	Description  string
	PathContains string
	NameGlob     string
	MinCount     int
}

// RunMustGather executes `oc adm must-gather` with the medik8s image into destDir.
// The image comes from MUST_GATHER_IMAGE, falling back to DefaultMustGatherImage
// (an intentionally mutable :latest tag, so the test exercises the must-gather
// build a customer would actually pull). Because the default tag is mutable, the
// resolved image digest is logged via logf on every run so a failure is
// reproducible against the exact build that was pulled.
func RunMustGather(
	ctx context.Context, destDir string, timeout time.Duration, logf func(format string, args ...interface{}),
) error {
	image := os.Getenv(farparams.MustGatherImageEnvVar)
	if image == "" {
		image = farparams.DefaultMustGatherImage
	}

	if digest, digestErr := resolveImageDigest(ctx, image); digestErr != nil {
		logf("WARNING: could not resolve must-gather image digest for %q: %v\n", image, digestErr)
	} else {
		logf("Using must-gather image %s (digest %s)\n", image, digest)
	}

	childCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(childCtx, "oc", "adm", "must-gather",
		"--image="+image,
		"--dest-dir="+destDir,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		if childCtx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("must-gather timed out after %s: %w\nOutput: %s", timeout, err, string(output))
		}

		return fmt.Errorf("must-gather failed: %w\nOutput: %s", err, string(output))
	}

	return nil
}

// resolveImageDigest returns the manifest digest a mutable image reference
// currently resolves to, so a run using a floating tag (e.g. :latest) can be
// reproduced against the exact build that was pulled. Best-effort: callers log
// the error and continue rather than failing the test.
func resolveImageDigest(ctx context.Context, image string) (string, error) {
	infoCtx, cancel := context.WithTimeout(ctx, farparams.OcDebugTimeout)
	defer cancel()

	out, err := exec.CommandContext(infoCtx, "oc", "image", "info", image,
		"--filter-by-os=linux/amd64", "-o", "json").Output()
	if err != nil {
		return "", fmt.Errorf("oc image info %s: %w", image, err)
	}

	var info struct {
		Digest string `json:"digest"`
	}
	if err := json.Unmarshal(out, &info); err != nil {
		return "", fmt.Errorf("parsing oc image info output: %w", err)
	}

	if info.Digest == "" {
		return "", fmt.Errorf("oc image info returned no digest for %s", image)
	}

	return info.Digest, nil
}

// ValidateMustGatherContents checks that the must-gather output directory
// contains all expected artifacts. Returns a list of missing expectations;
// an empty list means all checks passed.
func ValidateMustGatherContents(baseDir string, expectations []MustGatherExpectation) []string {
	allFiles, walkErr := collectFiles(baseDir)
	if walkErr != nil {
		return []string{fmt.Sprintf("failed to walk must-gather directory %s: %v", baseDir, walkErr)}
	}

	var missing []string

	for _, exp := range expectations {
		count := 0

		for _, f := range allFiles {
			if matchesExpectation(f.relPath, f.name, exp) {
				count++
			}
		}

		if count < exp.MinCount {
			missing = append(missing, fmt.Sprintf(
				"%s: expected at least %d match(es) (pathContains=%q, nameGlob=%q), found %d",
				exp.Description, exp.MinCount, exp.PathContains, exp.NameGlob, count))
		}
	}

	return missing
}

type fileEntry struct {
	relPath string
	name    string
}

func collectFiles(baseDir string) ([]fileEntry, error) {
	var files []fileEntry

	err := filepath.Walk(baseDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walk error at %s: %w", path, walkErr)
		}

		if info.IsDir() {
			return nil
		}

		relPath, relErr := filepath.Rel(baseDir, path)
		if relErr != nil {
			return fmt.Errorf("failed to compute relative path for %s: %w", path, relErr)
		}

		files = append(files, fileEntry{
			relPath: relPath,
			name:    info.Name(),
		})

		return nil
	})

	return files, err
}

func matchesExpectation(relPath, name string, exp MustGatherExpectation) bool {
	if exp.PathContains != "" && !strings.Contains(relPath, exp.PathContains) {
		return false
	}

	if exp.NameGlob != "" {
		matched, err := filepath.Match(exp.NameGlob, name)
		if err != nil || !matched {
			return false
		}
	}

	return true
}
