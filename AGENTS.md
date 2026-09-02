# AGENTS.md

This file provides guidance to all coding agents working in this repository.

## Coding Agent Guidelines

See [SKILLS.md](.agents/SKILLS.md) for the full behavioral guidelines that apply to all code written or reviewed here.

## Project Conventions for Agents

- **Keep the repository root clean.** Do not add new files to the root unless strictly required by a tool or standard (e.g. `go.mod`, `Makefile`, `LICENSE`). Documentation, guidelines, and agent-specific files belong in subdirectories.
- **No agent-proprietary directories or files.** Do not create tool-specific directories (e.g. `.claude/`, `.cursor/`) or files unless the tool itself requires it and there is no neutral alternative. Use `.agents/` for cross-agent content.
- **Prefer `AGENTS.md` over `CLAUDE.md`.** All project guidance goes in `AGENTS.md`. `CLAUDE.md` exists only as a redirect stub; do not add content to it.

---

## Project Overview

**system-tests** (`github.com/medik8s/system-tests`) — E2E tests for all [medik8s](https://www.medik8s.io/) operators: FAR (Fence Agents Remediation), MDR (Machine Deletion Remediation), NHC (Node Health Check), NMO (Node Maintenance Operator), SNR (Self Node Remediation), SBR (Storage Based Remediation), and CUR (Customized User Remediation). Forked from [eco-gotests](https://github.com/rh-ecosystem-edge/eco-gotests). Uses Ginkgo v2 and requires a live OCP ≥4.13 cluster via `KUBECONFIG`.

## Commands

```bash
make lint                        # golangci-lint v2.11.4 (auto-installed if missing)
make test                        # unit tests — no cluster needed
make vet                         # go vet all packages
make deps-update                 # go mod tidy
make install                     # deps-update + install ginkgo v2
make run-tests                   # execute test-runner.sh (requires ECO_TEST_FEATURES)
make sync-eco-goinfra            # bump eco-goinfra dep (ECO_GOINFRA_BRANCH=release-4.20 optional)
make build-docker-image          # build podman image system-tests:latest
```

### Running Tests Against a Cluster

```bash
export KUBECONFIG=/path/to/kubeconfig
export ECO_TEST_FEATURES="far-operator"        # space-separated suite dir names; "all" runs everything
export ECO_TEST_LABELS='operator:far && tier:smoke'   # Ginkgo label-filter expression (optional)
export ECO_TEST_VERBOSE=true                   # -vv output
export ECO_TEST_TRACE=true                     # full stack trace on failure
export ECO_DUMP_FAILED_TESTS=true              # dump cluster state on failure
export ECO_REPORTS_DUMP_DIR=/tmp/reports       # default: /tmp/reports
make run-tests
```

### Running a Single Suite Directly

```bash
ginkgo -v ./tests/far-operator/...
ginkgo -v --label-filter="operator:far && tier:smoke" ./tests/far-operator/...
```

### Unit Tests Only

```bash
UNIT_TEST=true go test -v ./tests/internal/...
make test
```

## Architecture

### Test Suite Layout

Every operator suite under `tests/<operator>/` follows this structure:

```
tests/<operator>/
  ├── <operator>_suite_test.go   # Ginkgo bootstrap: registers suite, JUnit report, JustAfterEach reporter
  ├── internal/
  │   └── <op>params/
  │       ├── const.go           # Label constant, replica counts, timeouts
  │       └── <op>vars.go        # Deployment names, reporter config (namespaces + CRDs to dump)
  └── tests/
      └── <feature>.go           # Test specs (NOT *_test.go — compiles into binary via suite import)
```

Test files under `tests/` are regular `.go` files (not `_test.go`). They are compiled into the test binary because the suite's `_suite_test.go` imports the `tests` package with a blank import (`_ "github.com/medik8s/system-tests/tests/<op>/tests"`).

### Initialization Chain

There is a two-layer init pattern used by dot imports:

1. `tests/internal/inittools` — base layer; exports `APIClient` and `GeneralConfig`. Reads `ECO_*` env vars and loads `tests/internal/config/default.yaml`. Skip-safe when `UNIT_TEST=true`.
2. `tests/internal/medik8sinittools` — medik8s layer; exports `APIClient` (same pointer) and `Medik8sConfig` (embeds `GeneralConfig` + reads `tests/internal/medik8sconfig/default.yaml`).

Test specs dot-import `medik8sinittools` to access `APIClient` and `Medik8sConfig` without a package prefix.

### Shared Internal Packages (`tests/internal/`)

| Package            | Purpose                                                                                                             |
| ------------------ | ------------------------------------------------------------------------------------------------------------------- |
| `inittools`        | Base init; global `APIClient`, `GeneralConfig`                                                                      |
| `medik8sinittools` | Medik8s-specific init; wraps inittools                                                                              |
| `medik8sconfig`    | `Medik8sConfig` struct — embeds GeneralConfig                                                                       |
| `medik8sparams`    | Shared constants: `OperatorNs = "openshift-workload-availability"`, `DefaultTimeout`, top-level `Label = "medik8s"` |
| `labels`           | Full label taxonomy constants (see below)                                                                           |
| `reporter`         | `ReportIfFailed()` — wraps k8sreporter to dump namespaces + CRDs on failure                                         |
| `config`           | `GeneralConfig` struct; reads YAML + `ECO_*` env vars via envconfig                                                 |

### Label Taxonomy

All `It`/`DescribeTable` specs should be labelled using constants from `tests/internal/labels`:

| Axis       | Examples                                                                                                           |
| ---------- | ------------------------------------------------------------------------------------------------------------------ |
| Operator   | `operator:far`, `operator:nhc`, `operator:snr`, `operator:mdr`, `operator:nmo`, `operator:sbr`, `operator:cur`     |
| Tier       | `tier:smoke`, `tier:acceptance`, `tier:resiliency`, `tier:upgrade`, `tier:interop`                                 |
| Frequency  | `frequency:presubmit`, `frequency:nightly`, `frequency:weekly`, `frequency:release`                                |
| Disruption | `disruption:destructive`, `disruption:nondestructive`                                                              |
| Component  | `component:controller`, `component:remediation`, `component:webhook`, `component:metrics`, `component:post-deploy` |
| Platform   | `platform:aws`, `platform:baremetal`, `platform:any`                                                               |
| Topology   | `topology:control-plane`, `topology:minimal-worker`, `topology:zero-worker`                                        |

### Key Conventions

- Every `It`/`DescribeTable` spec must call `reportxml.ID("…")` to assign a test case ID.
- Document test steps with `By("description")` blocks inside each `It`.
- Use `Eventually`/`Consistently` with explicit timeout + poll interval from `<op>params`/`medik8sparams` — never `time.Sleep`.
- Gomega assertions belong only in test files, never in `internal/` packages.
- Run `go mod tidy` after any dependency change.
- To update eco-goinfra: use the GitHub Actions "eco-goinfra-bump" workflow, or run `make sync-eco-goinfra ECO_GOINFRA_BRANCH=release-4.20`.
- CI gates: lint, `ECO_DRY_RUN=true` dry-run, unit tests. ≥2 reviewer approvals required to merge.

### Migrating Tests from Python (ocp-edge-auto)

When porting E2E tests from Python to Go, always read the **full Python implementation** before writing Go code -- not just the Polarion test plan:

1. Read the Python **test method body** (what it verifies)
2. Read **every helper function** the test calls -- the mechanism matters (e.g. Python uses SSH for kubelet stop/start; `oc debug` cannot work when kubelet is stopped)
3. Read the Python **teardown/cleanup** (often contains recovery logic absent from the Polarion plan)
4. Match the Python approach in Go -- if Python uses two SNR NHCs, use two SNR NHCs; don't substitute a different design

The Polarion test plan describes WHAT to verify. The Python code shows HOW. Follow both.

### Commit Message Format

```
<operator|infra|ci|readme>: <short summary (≤72 chars total)>
```

Examples: `far-operator: add CSV annotation test`, `infra: move timeout const to medik8sparams`, `ci: update golangci-lint version`. No capital letters, no internal test IDs in the title.
