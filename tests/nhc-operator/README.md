# NHC Operator Post-Deployment Tests

## Standalone operator-bundle upgrade (OpenShift 5.0)

The `tier:upgrade-operator` scenario installs its own SNR and upstream
NHC 0.12.0, upgrades to the supplied 5.8.0 candidate, and checks the new CSV,
manager image, preserved NHC UID/full spec, and a fresh controller response.
The independently selectable `tier:fresh-install` scenario starts clean,
installs the same SNR prerequisite and candidate NHC bundle, and verifies the
candidate version, image, pods, configuration response, and cleanup.

Use a disposable OpenShift 5.0 cluster. The namespace
`openshift-workload-availability` must **not exist**. Preflight also rejects
NHC/SNR installations and CRs in other namespaces, leftover OLM cluster objects
associated with that namespace, and the existing remediation ConsolePlugin.
A rejected preflight makes no changes. Setup marks a newly created namespace
and CRs with a random run identifier; cleanup checks UIDs, attempts every
installed package (including partial failures), removes owned objects and the
namespace, and retains shared CRDs. This is deliberately unsuitable for a
shared operator namespace. The second run must pass the same clean preflight.

The NHC selector requires the same label both to exist and not exist, so no
current or future node can match. A pause request supplies a second guard.
Both v0.12.0 and PR #430 validate this selector with `LabelSelectorAsSelector`;
their controllers select only matching nodes and write `Paused` plus the exact
pause request in `status.reason`. After old controller pods are gone, the test
changes that token, waits for the exact new reason, restores the original
token, and waits again. A persisted status or an invented observedGeneration
field cannot satisfy this check. No node/kubelet action is performed.

### Local image preparation

First inspect the new cluster's integrated registry without changing it:

```bash
: "${KUBECONFIG:?Set the absolute path to the new disposable cluster kubeconfig first}"
: "${IMAGE_NAMESPACE:?Set the intended registry image namespace}"
oc whoami --show-server
oc get clusterversion version
oc get configs.imageregistry.operator.openshift.io/cluster -o yaml
oc get clusteroperator image-registry -o yaml
oc get pods,pvc,service,route -n openshift-image-registry -o wide
oc auth can-i create imagestreams -n "$IMAGE_NAMESPACE"
oc auth can-i update imagestreams/layers -n "$IMAGE_NAMESPACE"
```

Supply `IMAGE_NAMESPACE` explicitly before these checks. Confirm storage,
registry health, a laptop-accessible push endpoint and node pull access; RBAC
answers alone do not prove a push/pull works. If unavailable, obtain the team's
supported scratch destination. Registry endpoints, image tags, credentials and
related-image digests remain environment inputs. Do not create a personal
Quay catalog. The commands below build/publish images only when the operator
has authorized that work; the preparation fixes themselves do not run them.

Use Linux amd64, Podman, Git, Make, oc, and the Go version required by the
checked-out source (PR #430 requests toolchain 1.26.5). The Dockerfile downloads
an amd64 toolchain and needs network access. Set these real inputs first:
`NHC_SOURCE_REPOSITORY` (existing local NHC repo), `NHC_BUILD_DIR` (new persistent
standalone clone), `NHC_BUILD_REPORT_DIR` (persistent evidence directory),
`OPERATOR_BUILD_IMAGE` and `BUNDLE_BUILD_IMAGE` (approved writable tagged
destinations), `CONSOLE_PLUGIN_IMAGE` and `MUST_GATHER_IMAGE` (verified digest
pullspecs). The helper rejects an existing build directory and preserves the
earlier PR430 verification worktree. A standalone clone is needed because the
NHC Dockerfile copies `.git/`; a Git worktree's `.git` file is insufficient.

```bash
export NHC_EXPECTED_SOURCE_COMMIT=36c6c7cf31898acbc0be1beb927606af1de2a8f5
export VERSION=5.8.0
bash scripts/nhc-upgrade-prepare.sh operator
podman push --digestfile "$NHC_BUILD_REPORT_DIR/operator.digest" "$OPERATOR_BUILD_IMAGE"
export NHC_OPERATOR_IMAGE="${OPERATOR_BUILD_IMAGE%:*}@$(< "$NHC_BUILD_REPORT_DIR/operator.digest")"
oc image info "$NHC_OPERATOR_IMAGE" -o json > "$NHC_BUILD_REPORT_DIR/published-operator.json"
bash scripts/nhc-upgrade-prepare.sh bundle
podman push --digestfile "$NHC_BUILD_REPORT_DIR/bundle.digest" "$BUNDLE_BUILD_IMAGE"
export NHC_UPGRADE_CANDIDATE_BUNDLE="${BUNDLE_BUILD_IMAGE%:*}@$(< "$NHC_BUILD_REPORT_DIR/bundle.digest")"
export YQ="$NHC_BUILD_DIR/bin/yq"
export NHC_UPGRADE_OPERATOR_SDK="$NHC_BUILD_DIR/bin/operator-sdk"
"$NHC_UPGRADE_OPERATOR_SDK" version
"$NHC_UPGRADE_OPERATOR_SDK" run --help
"$NHC_UPGRADE_OPERATOR_SDK" bundle validate "$NHC_UPGRADE_CANDIDATE_BUNDLE"
export NHC_UPGRADE_CANDIDATE_IMAGE="$NHC_OPERATOR_IMAGE"
export NHC_UPGRADE_CANDIDATE_VERSION="$VERSION"
```

The helper builds the existing Dockerfile, then calls the real
`bundle-build-ocp` Makefile target with consistent `VERSION`, `IMG`, `BUNDLE_IMG`,
`CONSOLE_PLUGIN_IMAGE`, `MUST_GATHER_IMAGE`, `PREVIOUS_VERSION=0.12.0`, and
`SKIP_RANGE_LOWER=0.1.0`. It records the source revision and generated diff.
The operator binary derives its embedded version from `hack/build.sh` and Git;
the explicit 5.8.0 here is the CSV version. The inspector compares the bundle
manager digest to the separately supplied built operator, verifies package,
CSV version and image annotation, and records all manager/console/init/related
image digests plus upgrade metadata. An image found inside a bundle alone is
not candidate provenance. This procedure still needs real registry execution.

SDK v1.42.2 `bundle-upgrade` adds the new bundle to the existing temporary
catalog and updates its Subscription. Its FBC catalog merge constructs a
channel entry replacing the existing channel head (`internal/olm/operator/
registry/index_image.go`). PR #430's default-version bundle has no CSV
`replaces`, and has skipRange `>=0.1.0 <5.8.0`. Missing CSV `replaces` alone
therefore does not establish an SDK upgrade blocker. Actual OLM resolution
remains a first-run check; this says nothing about downstream catalog edges.

Before a test, inspect the pinned old NHC and SNR bundles and verify every
referenced image is pullable. `oc image info` is a laptop/registry check, not
proof of node pull access. Confirm node access during the approved cluster
attempt and retain image-pull failures. The manager image input must equal the
literal old CSV/pod image string; record its resolved digest separately instead
of substituting an equivalent digest string and breaking the equality check.

### Two local runs, then CI

Set `KUBECONFIG` to the disposable cluster and `NHC_UPGRADE_OLD_BUNDLE` and
`NHC_UPGRADE_SNR_BUNDLE` to inspected upstream bundle digest pullspecs. The
starting tags for lookup are NHC `v0.12.0` and SNR `v0.13.0`; they are not digest
claims. Set `NHC_UPGRADE_OLD_IMAGE` to the old bundle's exact manager pullspec
and `NHC_RUN_REPORT_DIR` to a persistent **new** directory for each run.
The Go test resolves the supplied bundle tags to digests, extracts their CSVs,
and verifies package, version, and operator-image identity before installation.

```bash
: "${KUBECONFIG:?}" "${NHC_UPGRADE_OLD_BUNDLE:?}" "${NHC_UPGRADE_OLD_IMAGE:?}"
: "${NHC_UPGRADE_SNR_BUNDLE:?}" "${NHC_RUN_REPORT_DIR:?}"
export ECO_TEST_FEATURES=nhc-operator ECO_TEST_LABELS='tier:upgrade-operator'
export WORKLOAD_IMAGE=unused-by-nhc-operator-upgrade
export NHC_UPGRADE_NAMESPACE=openshift-workload-availability
export NHC_UPGRADE_PACKAGE=node-healthcheck-operator NHC_UPGRADE_SNR_PACKAGE=self-node-remediation
export NHC_UPGRADE_OLD_VERSION=0.12.0
export NHC_UPGRADE_CANDIDATE_COMMIT="$NHC_EXPECTED_SOURCE_COMMIT"
export NHC_UPGRADE_TEST_REVISION="$(git rev-parse HEAD)"
export ECO_REPORTS_DUMP_DIR="$NHC_RUN_REPORT_DIR"
mkdir -p "$ECO_REPORTS_DUMP_DIR"
set -o pipefail
make run-tests 2>&1 | tee "$ECO_REPORTS_DUMP_DIR/run.log"
```

For the separate fresh-install checkpoint, use a new report directory and
select only its label; keep the same pinned candidate inputs:

```bash
export ECO_TEST_LABELS='tier:fresh-install'
export NHC_RUN_REPORT_DIR=/absolute/path/to/a/new/fresh-install-report-directory
export ECO_REPORTS_DUMP_DIR="$NHC_RUN_REPORT_DIR"
mkdir -p "$ECO_REPORTS_DUMP_DIR"
make run-tests 2>&1 | tee "$ECO_REPORTS_DUMP_DIR/run.log"
```

Keep `ARTIFACT_DIR`/`SHARED_DIR` unset locally unless using their real intended
destinations; ARTIFACT_DIR overrides the local report path. Retain build
evidence, XML/JUnit and logs for run1 and run2. Confirm exactly one scenario ran
and cleanup finished each time. Failure diagnostics and cleanup errors are
report entries; they do not replace the original test failure.

Only after both local runs, publish/review the system-tests change and prepare
the release PR. Its pinned test commit must be reachable (prefer the actual
merged commit); a local SHA cannot be cloned by CI. Rehearse the proposed
release configuration **before merging it** and inspect actual source refs.
The optional/manual job consumes `my-bundle` as `OO_BUNDLE` and independently
consumes that execution's operator image. It compares their digests, records
JOB_SPEC and the checkout SHA, and requires the expected PR #430 commit in the
source history. Target-branch-only rehearsals fail this requirement. Update
the expected source SHA deliberately if #430 is rebased. The actual PR job
and its exact source/bundle/test revisions must be recorded separately.

The upstream sample is not a cluster upgrade, fresh-install coverage, downstream
catalog qualification, or remediation test. Related-image tags in the unmodified
CI candidate are resolved and recorded, but are still mutable references; local
bundle generation pins console and must-gather explicitly.

## OpenShift 4.22 to 5.0 cluster-upgrade scenario

`tier:upgrade-cluster` is a separate destructive scenario. It installs released
NHC and SNR from the OpenShift 4.22 `redhat-operators` catalog, records the NHC
CSV and controller image, performs the real OpenShift update to the supplied 5.0
release payload, requires the same NHC CSV and image afterward, and completes a
real NHC/SNR node-remediation cycle. The test owns its setup and does not require
another Ginkgo spec or a hand-installed SNR prerequisite.

Select only this scenario with:

```bash
export KUBECONFIG=/absolute/path/to/the/disposable-4.22-cluster-kubeconfig
export ECO_TEST_FEATURES=nhc-operator
export ECO_TEST_LABELS='tier:upgrade-cluster'
export MEDIK8S_OPERATOR_PACKAGE=node-healthcheck-operator
export OPENSHIFT_UPGRADE_RELEASE_IMAGE_OVERRIDE='the-approved-5.0-release-payload-pullspec'
export MEDIK8S_SKIP_DOWNSTREAM_OPERATOR_UPGRADE=true
export ECO_SSH_KEY_PATH=/absolute/path/to/the-cluster-private-key
make run-tests
```

This invocation does not simulate the update: the test changes the cluster's
`ClusterVersion` desired payload and waits for the update to finish healthy. It
requires a disposable multi-node 4.22 cluster and reboots one worker during the
functional check. If SSH is unavailable, set
`MEDIK8S_KUBELET_STOP_OCDEBUG=true`; automatic SNR recovery still performs the
reboot, but SSH provides the safer failure-recovery path.

`MEDIK8S_SKIP_DOWNSTREAM_OPERATOR_UPGRADE=true` stops only after the complete
4.22-to-5.0 survival and remediation checkpoint. Omit it only when the real
downstream catalog, its IDMS files in `SHARED_DIR`, and `medik8s-catalog`
CatalogSource are present; the remaining steps then switch the Subscription,
require the downstream operator upgrade, and repeat remediation. A source-bundle
upgrade result and a downstream-catalog result must be reported separately.

For a directly pullable public catalog, set
`MEDIK8S_SKIP_UPGRADE_IDMS=true` and provide the exact
`NHC_UPGRADE_CANDIDATE_VERSION` and `NHC_UPGRADE_CANDIDATE_IMAGE`. The test
still switches the existing Subscription to `medik8s-catalog`, requires the
exact candidate CSV and controller image, and repeats remediation; it only
skips the IDMS and MachineConfigPool rollout that public image references do
not need.

For a candidate source PR, set `MEDIK8S_UPGRADE_TO_PR_BUNDLE=true` instead
and provide the same digest-pinned `NHC_UPGRADE_CANDIDATE_*`, operator-sdk,
package, namespace, and revision inputs used by the standalone tests. After the
real 4.22-to-5.0 update and released-NHC remediation checkpoint, the scenario
runs `operator-sdk run bundle-upgrade` against that PR bundle, requires the new
CSV and exact PR-built controller image, and repeats remediation. This proves a
PR candidate across the OpenShift update without claiming to test the downstream
Red Hat catalog path.

Automated tests validating the Node Health Check (NHC) operator
deployment, OLM metadata, and security posture.

## Prerequisites

- OpenShift cluster with NHC operator installed via OLM
- `KUBECONFIG` set with cluster-admin access
- NHC installed in `openshift-workload-availability` namespace
- Minimum tested version: NHC v0.12.0 (RHWA 4.22 GA baseline)

## Running

```bash
ginkgo --label-filter="nhc && !tier:upgrade-operator && !tier:fresh-install && !tier:upgrade-cluster" ./tests/nhc-operator/...
```

Or via the test runner:

```bash
export KUBECONFIG=/path/to/kubeconfig
export ECO_TEST_FEATURES="nhc-operator"
export ECO_TEST_LABELS='!tier:upgrade-operator && !tier:fresh-install && !tier:upgrade-cluster'
make run-tests
```

## Tests

### 1. Verify NHC Resources Are Installed and Running ([OCP-89629](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-89629))

Validates that the NodeHealthCheck CRD is registered and listable, and
that NHC controller-manager pods are in Running state with all
containers ready.

- **Operators**: NHC v0.12.0+
- **Cluster**: Any topology (MNO or SNO)
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="nhc" --focus="resources are installed" ./tests/nhc-operator/...`
- **Pass criteria**: NodeHealthCheck API is listable; all controller-manager pods are Running with all containers ready

### 2. Verify NHC CSV Annotations ([OCP-89630](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-89630))

Validates that the active NHC ClusterServiceVersion (in Succeeded phase)
has required OLM annotations: valid-subscription, support contact,
repository URL, and at least one maintainer.

- **Operators**: NHC v0.12.0+
- **Cluster**: Any topology
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="nhc" --focus="CSV annotations" ./tests/nhc-operator/...`
- **Pass criteria**: All required annotations present, maintainers list non-empty

### 3. Verify NHC CSV Metadata ([OCP-89631](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-89631))

Validates that infrastructure feature annotations (disconnected, fips-compliant,
proxy-aware, etc.) match expected values, the `replaces` field references
the previous NHC version when present, and controller replicas match the
expected count on multi-node clusters. Skips replica validation on SNO.

- **Operators**: NHC v0.12.0+
- **Cluster**: Multi-node for replica check (skips replica validation on SNO)
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="nhc" --focus="CSV metadata" ./tests/nhc-operator/...`
- **Pass criteria**: All infrastructure annotations match expected values, suggested-namespace correct, replaces field contains "node-healthcheck-operator" when set, 2 ready replicas on MNO

### 4. Verify NHC Container Runs as Non-Root User ([OCP-89632](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-89632))

Validates that the NHC manager container enforces a restricted security
context: runAsNonRoot at pod level, runAsUser is not UID 0 when set,
allowPrivilegeEscalation=false, capabilities.drop=ALL,
readOnlyRootFilesystem=true, and seccompProfile=RuntimeDefault (at
container or pod level). Only checks the `manager` container.

- **Operators**: NHC v0.12.0+
- **Cluster**: Any topology
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="nhc" --focus="runs as non-root" ./tests/nhc-operator/...`
- **Pass criteria**: Pod runAsNonRoot=true; expected manager container exists; manager container runAsUser != 0; allowPrivilegeEscalation=false; readOnlyRootFilesystem=true; capabilities.drop=[ALL]; seccomp profile RuntimeDefault

## Non-Destructive Tests -- Negative Validation and Webhook Rejection

Tests that verify NHC webhook rejection of invalid CRs and controller
behavior with misconfigured remediation templates. No node disruption --
all tests are pure API-level. NHC works with any operator that provides
a remediation template CRD; the template management test uses SNR and a
dummy TestRemediation CRD.

### Prerequisites (Negative Validation)

- NHC operator installed
- SNR operator installed (for template namespace tests)
- `KUBECONFIG` set with cluster-admin access

### 5. Duplicate NHC Name Rejection ([OCP-53769](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-53769))

Creates an NHC CR, then attempts to create another with the same name.
Verifies the API server rejects the duplicate with AlreadyExists error
and only one CR with that name exists.

- **Operators**: NHC v0.12.0+
- **Cluster**: Any topology
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="nhc && disruption:nondestructive" --focus="duplicate NHC name" ./tests/nhc-operator/...`
- **Pass criteria**: First NHC created successfully, second NHC rejected with AlreadyExists, listing shows exactly one CR with that name

### 6. Invalid Values Rejection ([OCP-51626](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-51626))

Attempts to create NHC CRs with invalid field values: negative numbers
(minHealthy: "-30%", duration: "-30s") and arbitrary strings
(minHealthy: "string", duration: "string"). Verifies the webhook rejects
both and no CR is created.

- **Operators**: NHC v0.12.0+
- **Cluster**: Any topology
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="nhc && disruption:nondestructive" --focus="invalid values" ./tests/nhc-operator/...`
- **Pass criteria**: Negative-value creation rejected with spec.minHealthy and spec.unhealthyConditions errors, string-value creation rejected with same errors, CR does not exist after either attempt

### 7. Empty Selector Rejection ([OCP-61591](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-61591))

Attempts to create an NHC CR with an empty matchExpressions selector.
Verifies the webhook rejects with "Selector is mandatory" and no CR is
created.

- **Operators**: NHC v0.12.0+
- **Cluster**: Any topology
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="nhc && disruption:nondestructive" --focus="empty selector" ./tests/nhc-operator/...`
- **Pass criteria**: Creation rejected with "Selector is mandatory", CR does not exist

### 8. Non-Existent Remediation Template ([OCP-51625](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-51625))

Creates NHC CRs with remediation templates that do not exist: first with
a wrong template name in the SNR API group, then with a completely
non-existent API group (poison-pill). Verifies both enter Disabled phase
with RemediationTemplateNotFound reason.

- **Operators**: NHC v0.12.0+
- **Cluster**: Any topology
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="nhc && disruption:nondestructive" --focus="non-existent remediation template" ./tests/nhc-operator/...`
- **Pass criteria**: NHC with wrong SNR template name reaches phase=Disabled with reason containing "RemediationTemplateNotFound"; NHC with poison-pill API group reaches same Disabled state

### 9. Missing Template Namespace ([OCP-71184](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-71184))

Tests NHC behavior when the remediationTemplate reference omits the
namespace field. Part 1: namespaced SNRT without namespace -- NHC goes
Disabled; add namespace via patch -- NHC goes Enabled; remove namespace
-- NHC returns to Disabled. Part 2: cluster-scoped TestRemediation
template without namespace -- NHC is Enabled (namespace not needed for
cluster-scoped CRDs).

- **Operators**: NHC v0.12.0+, SNR (Part 1)
- **Cluster**: Any topology
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="nhc && disruption:nondestructive" --focus="missing template namespace" ./tests/nhc-operator/...`
- **Pass criteria**: Part 1: NHC Disabled with RemediationTemplateNotFound when namespace missing, Enabled after namespace added, Disabled again after namespace removed with same reason; Part 2: NHC Enabled with cluster-scoped TestRemediationTemplate (TRT) and no namespace

## Non-Destructive Tests -- Template Watch

Tests that verify NHC watches remediation template lifecycle. Deleting a
template causes NHC to transition to Disabled; re-creating it causes NHC
to return to Enabled. Requires SNR operator for the SNRT template.

### 10. NHC Watches Template Deletion and Re-Creation ([OCP-71185](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-71185))

Creates an SNRT (SelfNodeRemediationTemplate) and an NHC pointing to it.
Verifies NHC is Enabled, then deletes the SNRT and verifies NHC transitions
to Disabled with RemediationTemplateNotFound. Re-creates the SNRT and
verifies NHC returns to Enabled.

- **Operators**: NHC v0.12.0+, SNR
- **Cluster**: Any topology
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="nhc && disruption:nondestructive" --focus="watches template deletion" ./tests/nhc-operator/...`
- **Pass criteria**: NHC Enabled with SNRT present, NHC Disabled with RemediationTemplateNotFound after SNRT deletion, NHC Enabled again after SNRT re-creation

## Destructive Tests -- Custom Remediation Template

Tests that verify NHC triggers remediation with a custom TestRemediation
template (not SNR). Creates dummy CRDs and RBAC, stops kubelet, and
verifies the full remediation lifecycle with status field tracking.

### Prerequisites (Custom Remediation)

- NHC operator installed
- SSH access to worker nodes (2+ Ready workers)
- `KUBECONFIG` set with cluster-admin access

### 11. NHC Custom Remediation Template ([OCP-61976](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-61976))

Creates TestRemediation CRDs, RBAC, and an NHC using a TestRemediationTemplate (TRT) as the remediator.
Verifies pre-remediation status (healthyNodes=N, observedNodes=N, Enabled).
Stops kubelet, verifies during-remediation status (healthyNodes=N-1,
observedNodes=N, Remediating), confirms TestRemediation CR created. Starts
kubelet, verifies post-recovery status and TestRemediation CR cleanup.

- **Operators**: NHC v0.12.0+
- **Cluster**: Multi-node (2+ workers), SSH access to worker nodes
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="nhc && disruption:destructive" --focus="custom TestRemediationTemplate" ./tests/nhc-operator/...`
- **Pass criteria**: Pre-remediation healthyNodes=N and observedNodes=N with Enabled phase, Remediating after kubelet stop with healthyNodes=N-1, TestRemediation CR created for target node, kubelet starts and node returns to Ready, post-recovery healthyNodes=N and observedNodes=N with Enabled phase, TestRemediation CR deleted

## Destructive Tests -- Status Field Tracking

Tests that verify NHC status.phase and status.reason transitions through
the full remediation lifecycle: Enabled -> Remediating -> Enabled.

### Prerequisites (Status Field)

- NHC and SNR operators installed
- SSH access to worker nodes (2+ Ready workers)
- `KUBECONFIG` set with cluster-admin access

### 12. Status Phase and Reason Transitions ([OCP-53093](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-53093))

Creates an NHC CR for workers and verifies pre-remediation status
(phase=Enabled, reason="no ongoing remediation"). Stops kubelet via SSH
to trigger SNR remediation. Verifies during-remediation status
(phase=Remediating, reason contains "remediating"). Waits for SNR to
reboot the node. Verifies post-recovery status returns to Enabled with
original reason.

- **Operators**: NHC v0.12.0+, SNR
- **Cluster**: Multi-node (2+ workers), SSH access to worker nodes
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="nhc && disruption:destructive" --focus="status phase and reason transitions" ./tests/nhc-operator/...`
- **Pass criteria**: Pre-remediation phase=Enabled with reason "no ongoing remediation", phase=Remediating with reason containing "remediating" after kubelet stop, SNR remediation completes (node reboots), phase=Enabled with reason "no ongoing remediation" after recovery

## Destructive Tests -- Zero Healthy Nodes

Tests that verify NHC status field tracking when all monitored nodes are
unhealthy. Stops kubelet to trigger remediation and validates the
healthyNodes counter drops to zero while observedNodes remains stable.

### Prerequisites (Zero Healthy Nodes)

- NHC and SNR operators installed
- SSH access to worker nodes
- `KUBECONFIG` set with cluster-admin access

### 13. Zero Healthy Nodes During Remediation ([OCP-56599](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-56599))

Creates an NHC CR targeting a single worker node (hostname selector,
minHealthy=0). Verifies healthyNodes=1 and observedNodes=1 before
remediation. Stops kubelet via SSH to trigger SNR remediation. Verifies
healthyNodes drops to 0 while observedNodes remains 1. Waits for SNR to
reboot the node and NHC to return to Enabled. Verifies healthyNodes and
observedNodes return to 1 after recovery.

- **Operators**: NHC v0.12.0+, SNR
- **Cluster**: Multi-node (1+ worker), SSH access to worker nodes
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="nhc && disruption:destructive" --focus="healthyNodes drops to zero" ./tests/nhc-operator/...`
- **Pass criteria**: Pre-remediation healthyNodes=1 and observedNodes=1, NHC enters Remediating after kubelet stop, healthyNodes=0 during remediation, observedNodes=1 throughout, SNR remediation completes (node reboots), NHC returns to Enabled, post-recovery healthyNodes=1 and observedNodes=1

## Destructive Tests -- Remediation Trigger and CR Lifecycle

Tests that stop kubelet on worker nodes and verify NHC behavior during
active remediation: selector editing, CR deletion blocking, multi-CR
coordination, and legacy CR name handling. NHC works with any operator
that provides a remediation template CRD; these tests use SNR as the
remediator.

### Prerequisites (Remediation Trigger)

- NHC and SNR operators installed (SNR is used as the remediator in these tests)
- At least 2 Ready worker nodes
- `KUBECONFIG` set with cluster-admin access

### 14. NHC Selector Editing ([OCP-56938](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-56938))

Edits the NHC selector to a non-existent key and verifies observed nodes
drops to 0 without crashing the NHC controller. Also verifies webhook
rejects invalid selector operator values and empty selectors.

- **Operators**: NHC v0.12.0+, SNR
- **Cluster**: Multi-node (2+ workers)
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="nhc && disruption:nondestructive" --focus="selector is edited" ./tests/nhc-operator/...`
- **Pass criteria**: Observed nodes drops to 0, NHC remains Enabled, invalid operator value rejected ("is not a valid"), empty selector rejected ("Selector is mandatory"), NHC state unchanged after rejected edits

### 15. NHC Editing and Deletion Blocked During Remediation ([OCP-56600](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-56600))

Stops kubelet via SSH to trigger remediation, then verifies non-selector
fields (minHealthy, unhealthyConditions) remain editable, while NHC
webhook blocks selector editing and CR deletion during active remediation.

- **Operators**: NHC v0.12.0+, SNR
- **Cluster**: Multi-node (2+ workers), SSH access to worker nodes
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="nhc && disruption:destructive" --focus="selector editing and deletion" ./tests/nhc-operator/...`
- **Pass criteria**: minHealthy and unhealthyConditions edit succeeds during remediation, selector edit rejected ("selector update prohibited due to running remediation"), CR deletion rejected ("deletion prohibited due to running remediation"), NHC CR still exists and Remediating after delete attempt, SNR remediation completes, node recovers, NHC returns to Enabled

### 16. Old Default NHC CR Name ([OCP-69711](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-69711))

Creates NHC CRs with the legacy name "nhc-worker-default" and a
control-plane NHC, stops kubelet on a worker and on the control-plane
node hosting the active NHC controller, and verifies the NHC controller
fails over to another node and both remediations complete.

- **Operators**: NHC v0.12.0+, SNR
- **Cluster**: Multi-node (2+ workers), SSH access to worker and control-plane nodes
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="nhc && disruption:destructive" --focus="old default NHC CR" ./tests/nhc-operator/...`
- **Pass criteria**: SNR CR created for worker (NHC triggered via legacy CR), NHC controller restarts on another node (2 ready replicas), both worker and control-plane nodes recover to Ready, control-plane NHC returns to Enabled, NHC deployment remains Ready

### 17. Only One NHC CR Remediates at a Time ([OCP-66814](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-66814))

Creates two NHC CRs with different remediators (SNR at 30s, TestRemediation
at 10s), stops kubelet via SSH, and verifies only the shorter-duration
TestRemediation NHC creates a remediation CR. The SNR NHC must NOT create
an SNR CR while the node is already being remediated.

- **Operators**: NHC v0.12.0+, SNR
- **Cluster**: Multi-node (2+ workers), SSH access to worker nodes
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="nhc && disruption:destructive" --focus="one CR at a time" ./tests/nhc-operator/...`
- **Pass criteria**: TestRemediation CR created for target node, SNR CR NOT created (Consistently), TestRemediation NHC returns to Enabled after kubelet restart, target node recovers to Ready

### 18. Non-Remediating NHC CR Deletion During Active Remediation ([OCP-71171](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-71171))

Creates two SNR-based NHC CRs with different unhealthy durations (10s and
11s). The faster NHC triggers SNR remediation first. Verifies the slower
NHC did NOT enter Remediating, then deletes it -- the deletion must succeed.
SNR reboots the node for automatic recovery.

- **Operators**: NHC v0.12.0+, SNR
- **Cluster**: Multi-node (2+ workers), SSH access to worker nodes
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="nhc && disruption:destructive" --focus="non-remediating NHC" ./tests/nhc-operator/...`
- **Pass criteria**: Second NHC phase is not Remediating, Delete() succeeds (asserted), first NHC returns to Enabled after SNR remediation, node recovers

## Destructive Tests -- Escalation Functional E2E

Tests that verify NHC escalatingRemediations behavior end-to-end: escalation when first remediator times out, no escalation when first remediator succeeds, and escalation after SNR timeout. Uses SSH to disable kubelet persistently (survives reboot) or stop kubelet (recoverable after reboot).

### Prerequisites (Escalation E2E)

- NHC and SNR operators installed
- At least 2 Ready worker nodes
- SSH access to worker nodes
- `KUBECONFIG` set with cluster-admin access

### 19. Escalation from TestRemediation to SNR ([OCP-60857](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-60857))

Creates NHC with two-step escalation: TestRemediation (order=0, timeout=60s) then SNR (order=1, timeout=180s). Stops kubelet via SSH. Verifies TestRemediation CR appears first, SNR CR does not exist yet, then after TestRemediation timeout SNR CR appears (both coexist). SNR reboots the node and kubelet auto-starts, after which NHC returns to Enabled and cleans up both CRs.

- **Operators**: NHC v0.12.0+, SNR
- **Cluster**: Multi-node (2+ workers), SSH access to worker nodes
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="nhc && disruption:destructive" --focus="Escalates from TestRemediation to SNR" ./tests/nhc-operator/...`
- **Pass criteria**: TestRemediation CR created first, SNR CR created after timeout, both CRs coexist, node rebooted, kubelet re-enabled, NHC returns to Enabled, both CRs cleaned up

### 20. No Escalation When First Remediator Restores Health ([OCP-60858](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-60858))

Creates NHC with SNR first and TestRemediation second. Stops kubelet, verifies SNR reboots the node and kubelet auto-restarts, and verifies TestRemediation CR is never created.

- **Operators**: NHC v0.12.0+, SNR
- **Cluster**: Multi-node (2+ workers), SSH access to worker nodes
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="nhc && disruption:destructive" --focus="Does not escalate" ./tests/nhc-operator/...`
- **Pass criteria**: SNR reboots the node, node becomes Ready, TestRemediation CR never created, NHC returns to Enabled

### 21. Escalation After SNR Timeout ([OCP-66806](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-66806))

Creates NHC with SNR first and TestRemediation second. Disables kubelet persistently via SSH, verifies SNR reboots the node but it remains unhealthy, and verifies NHC escalates to TestRemediation after the SNR timeout.

- **Operators**: NHC v0.12.0+, SNR
- **Cluster**: Multi-node (2+ workers), SSH access to worker nodes
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="nhc && disruption:destructive" --focus="Escalates after SNR timeout" ./tests/nhc-operator/...`
- **Pass criteria**: SNR CR and TestRemediation CR coexist after escalation, kubelet re-enabled, NHC returns to Enabled, both CRs cleaned up

### 22. Escalation Order Field Validation ([OCP-60863](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-60863))

Exercises webhook validation of the `order` field in escalatingRemediations.
Rejection cases: creating NHC CRs with a missing order field and with
duplicate order values -- verifies the webhook rejects both and the CRs are
not persisted. Acceptance case: creating an NHC CR with very large order
values (9999999998 / 9999999999) -- verifies the webhook accepts it and the
CR is created.

- **Operators**: NHC v0.12.0+
- **Cluster**: Any (no node disruption)
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="nhc && disruption:nondestructive" --focus="order field validation" ./tests/nhc-operator/...`
- **Pass criteria**: API returns error containing "order" for missing order and "duplicate order" for duplicate values, with the CR not created in either rejection case; NHC creation with very large order values succeeds and the CR is persisted

### 23. Escalation Timeout Field Required and Minimum Value ([OCP-60862](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-60862))

Attempts to create NHC CRs with escalatingRemediations that have a missing
timeout field and a timeout below the 60s minimum. Verifies the webhook
rejects both.

- **Operators**: NHC v0.12.0+
- **Cluster**: Any (no node disruption)
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="nhc && disruption:nondestructive" --focus="timeout field is required" ./tests/nhc-operator/...`
- **Pass criteria**: API returns error containing "timeout" for missing timeout, "at least" for below-minimum timeout, CR not created in either case

### 24. Duplicate Remediator Kind Forbidden ([OCP-66838](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-66838))

Attempts to create an NHC CR with two escalation steps using the same
remediator Kind (two TestRemediation templates). Uses TestRemediation
because SNR supports multiple templates of the same Kind (via the
`multiple-templates-support` annotation). Verifies the webhook rejects
the duplicate for remediators that do not support this feature.

- **Operators**: NHC v0.12.0+
- **Cluster**: Any (no node disruption)
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="nhc && disruption:nondestructive" --focus="duplicate remediator kind" ./tests/nhc-operator/...`
- **Pass criteria**: API returns error containing "same kind", CR not created

### 25. Multiple Same-Kind Templates Accepted ([OCP-74932](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-74932))

Companion to the negative case above: creates two `TestRemediationTemplate` CRs
of the same Kind that both carry the `multiple-templates-support` annotation, then
creates an NHC CR with escalating remediations referencing both. Verifies the
webhook accepts multiple templates of the same Kind when every template of that
Kind supports the feature.

- **Operators**: NHC v0.12.0+
- **Cluster**: Any (no node disruption)
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="nhc && disruption:nondestructive" --focus="accepted when templates support multiple" ./tests/nhc-operator/...`
- **Pass criteria**: NHC CR is created and persisted

### 26. Escalation Order Change Rejected During Active Remediation ([OCP-60865](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-60865))

Creates an NHC with escalating remediations (TestRemediation then SNR),
stops kubelet on a worker to trigger remediation, waits for the
TestRemediation CR to appear, then attempts to swap the escalation order
values. Verifies the webhook rejects the update while remediation is active.

- **Operators**: NHC v0.12.0+, SNR
- **Cluster**: Multi-node (2+ workers), SSH access to worker nodes
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="nhc && disruption:destructive" --focus="order change is rejected" ./tests/nhc-operator/...`
- **Pass criteria**: TestRemediation CR created, update rejected with error containing "escalating remediations", node recovers after kubelet restart
