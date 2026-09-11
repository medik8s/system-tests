//nolint:lll // Long fixture commands are kept intact so their exact shell inputs remain reviewable.
package nhcutils

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func writeScriptFixture(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()

	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func TestBundleInspector(t *testing.T) {
	dir := t.TempDir()
	writeScriptFixture(t, filepath.Join(dir, "csv.yaml"), `metadata:
  name: node-healthcheck-operator.v5.8.0
  annotations:
    containerImage: candidate-manager
    olm.skipRange: '>=0.1.0 <5.8.0'
spec:
  version: 5.8.0
  relatedImages:
  - image: explicit-related
  install:
    spec:
      deployments:
      - spec:
          template:
            spec:
              containers:
              - name: manager
                image: candidate-manager
                env:
                - name: RELATED_IMAGE_MUST_GATHER
                  value: must-gather
                - name: OTHER
                  value: not-an-image
              - name: console
                image: console
              initContainers:
              - name: init
                image: init
`, 0600)
	writeScriptFixture(t, filepath.Join(dir, "annotations.yaml"), "annotations:\n  operators.operatorframework.io.bundle.package.v1: node-healthcheck-operator\n", 0600)
	writeScriptFixture(t, filepath.Join(dir, "oc"), `#!/usr/bin/env bash
set -euo pipefail
if [[ $2 == extract ]]; then
  shift 3
  while [[ $# -gt 0 ]]; do
    if [[ $1 == --path ]]; then
      case $2 in
        /manifests/:*) cp "$FIXTURE_DIR/csv.yaml" "${2#*:}/nhc.clusterserviceversion.yaml" ;;
        /metadata/:*) cp "$FIXTURE_DIR/annotations.yaml" "${2#*:}/annotations.yaml" ;;
      esac
      shift 2
    else shift; fi
  done
elif [[ $2 == info ]]; then
  if [[ $3 == wrong-operator ]]; then
    printf '{"digest":"sha256:%064d"}\n' 2
  else
    printf '{"digest":"sha256:%064d"}\n' 1
  fi
else exit 99; fi
`, 0700)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("FIXTURE_DIR", dir)

	bundle, err := inspectBundle(context.Background(), "bundle")
	if err != nil {
		t.Fatal(err)
	}

	if err := verifyBundle(bundle, "node-healthcheck-operator", "5.8.0"); err != nil {
		t.Fatal(err)
	}

	if err := requireSameImage(context.Background(), bundle.ManagerImage, "built-operator"); err != nil {
		t.Fatal(err)
	}

	if err := requireSameImage(context.Background(), bundle.ManagerImage, "wrong-operator"); err == nil {
		t.Fatal("accepted unrelated operator image")
	}
}

func TestRunnerPreservesReportsAndOriginalFailure(t *testing.T) {
	root, err := filepath.Abs("../../../..")
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	ginkgo := filepath.Join(dir, "ginkgo")
	writeScriptFixture(t, ginkgo, `#!/usr/bin/env bash
mkdir -p "$ECO_REPORTS_DUMP_DIR"
printf '<report/>\n' > "$ECO_REPORTS_DUMP_DIR/nhc_testrun.xml"
printf 'Ran 1 of 24 Specs\noriginal test failure\n'
exit 7
`, 0700)
	t.Setenv("GINKGO", ginkgo)
	t.Setenv("ECO_TEST_FEATURES", "nhc-operator")
	t.Setenv("ECO_TEST_LABELS", "tier:upgrade-operator")
	t.Setenv("SHARED_DIR", filepath.Join(dir, "missing-copy-destination"))

	for _, ci := range []bool{false, true} {
		reports := filepath.Join(dir, "local")
		t.Setenv("ECO_REPORTS_DUMP_DIR", reports)
		t.Setenv("ARTIFACT_DIR", "")

		if ci {
			reports = filepath.Join(dir, "artifacts")
			t.Setenv("ARTIFACT_DIR", reports)
		}

		command := exec.Command("bash", "scripts/test-runner.sh")
		command.Dir = root
		output, err := command.CombinedOutput()

		exitError := &exec.ExitError{}

		ok := errors.As(err, &exitError)
		if !ok || exitError.ExitCode() != 7 || !strings.Contains(string(output), "original test failure") {
			t.Fatalf("lost original failure: %v %s", err, output)
		}

		if _, err := os.Stat(filepath.Join(reports, "nhc_testrun.xml")); err != nil {
			t.Fatal("report missing", err)
		}
	}
}

func TestPreparationUsesExactMakeInputs(t *testing.T) {
	root, err := filepath.Abs("../../../..")
	if err != nil {
		t.Fatal(err)
	}
	// The real helper requires a persistent source context. This temporary
	// fixture lives under this package and is removed when this test finishes.
	//nolint:usetesting // The helper requires its source path to persist beneath the package working directory.
	dir, err := os.MkdirTemp(".", "prepare-fixture-")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	dir, err = filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}

	writeScriptFixture(t, filepath.Join(dir, "git"), `#!/usr/bin/env bash
set -eu
printf 'git %s\n' "$*" >> "$COMMAND_LOG"
if [[ $1 == clone ]]; then mkdir -p "$4/.git";
elif [[ $3 == rev-parse ]]; then printf '%s\n' "$NHC_EXPECTED_SOURCE_COMMIT";
fi
`, 0700)
	writeScriptFixture(t, filepath.Join(dir, "podman"), "#!/usr/bin/env bash\nprintf 'podman %s\\n' \"$*\" >> \"$COMMAND_LOG\"\n", 0700)
	writeScriptFixture(t, filepath.Join(dir, "make"), "#!/usr/bin/env bash\nprintf 'make %s\\n' \"$*\" >> \"$COMMAND_LOG\"\nmkdir -p \"$NHC_BUILD_DIR/bundle\"\n", 0700)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("COMMAND_LOG", filepath.Join(dir, "commands"))
	t.Setenv("NHC_SOURCE_REPOSITORY", filepath.Join(dir, "source"))
	t.Setenv("NHC_BUILD_DIR", filepath.Join(dir, "build"))
	t.Setenv("NHC_BUILD_REPORT_DIR", filepath.Join(dir, "reports"))
	t.Setenv("NHC_EXPECTED_SOURCE_COMMIT", strings.Repeat("a", 40))
	t.Setenv("VERSION", "5.8.0")
	t.Setenv("OPERATOR_BUILD_IMAGE", "registry.test/operator:build")
	t.Setenv("BUNDLE_BUILD_IMAGE", "registry.test/bundle:build")
	t.Setenv("CONSOLE_PLUGIN_IMAGE", "registry.test/console@sha256:"+strings.Repeat("1", 64))
	t.Setenv("MUST_GATHER_IMAGE", "registry.test/gather@sha256:"+strings.Repeat("2", 64))
	t.Setenv("NHC_OPERATOR_IMAGE", "registry.test/operator@sha256:"+strings.Repeat("3", 64))

	for _, stage := range []string{"operator", "bundle"} {
		output, err := exec.Command("bash", filepath.Join(root, "scripts/nhc-upgrade-prepare.sh"), stage).CombinedOutput()
		if err != nil {
			t.Fatalf("stage %s: %v\n%s", stage, err, output)
		}
	}

	commands, err := os.ReadFile(filepath.Join(dir, "commands"))
	if err != nil {
		t.Fatal(err)
	}

	for _, required := range []string{"clone --no-hardlinks", "org.opencontainers.image.revision=", "bundle-build-ocp VERSION=5.8.0", "IMG=" + os.Getenv("NHC_OPERATOR_IMAGE"), "BUNDLE_IMG=registry.test/bundle:build", "CONSOLE_PLUGIN_IMAGE=", "MUST_GATHER_IMAGE=", "PREVIOUS_VERSION=0.12.0 SKIP_RANGE_LOWER=0.1.0"} {
		if !strings.Contains(string(commands), required) {
			t.Fatalf("missing %q in %s", required, commands)
		}
	}

	if strings.Contains(string(commands), "podman push") {
		t.Fatal("helper unexpectedly publishes images")
	}
}
