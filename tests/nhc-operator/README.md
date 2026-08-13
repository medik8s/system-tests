# NHC Operator Post-Deployment Tests

Automated tests validating the Node Health Check (NHC) operator
deployment, OLM metadata, and security posture.

## Prerequisites

- OpenShift cluster with NHC operator installed via OLM
- `KUBECONFIG` set with cluster-admin access
- NHC installed in `openshift-workload-availability` namespace
- Minimum tested version: NHC v0.12.0 (RHWA 4.22 GA baseline)

## Running

```bash
ginkgo --label-filter="nhc" ./tests/nhc-operator/...
```

Or via the test runner:

```bash
export KUBECONFIG=/path/to/kubeconfig
export ECO_TEST_FEATURES="nhc-operator"
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
- **Pass criteria**: Part 1: NHC Disabled with RemediationTemplateNotFound when namespace missing, Enabled after namespace added, Disabled again after namespace removed with same reason; Part 2: NHC Enabled with cluster-scoped TRT and no namespace

## Destructive Tests -- Status Field Tracking

Tests that verify NHC status.phase and status.reason transitions through
the full remediation lifecycle: Enabled -> Remediating -> Enabled.

### Prerequisites (Status Field)

- NHC and SNR operators installed
- SSH access to worker nodes (2+ Ready workers)
- `KUBECONFIG` set with cluster-admin access

### 10. Status Phase and Reason Transitions ([OCP-53093](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-53093))

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

### 11. Zero Healthy Nodes During Remediation ([OCP-56599](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-56599))

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

### 12. NHC Selector Editing ([OCP-56938](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-56938))

Edits the NHC selector to a non-existent key and verifies observed nodes
drops to 0 without crashing the NHC controller. Also verifies webhook
rejects invalid selector operator values and empty selectors.

- **Operators**: NHC v0.12.0+, SNR
- **Cluster**: Multi-node (2+ workers)
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="nhc && disruption:nondestructive" --focus="selector is edited" ./tests/nhc-operator/...`
- **Pass criteria**: Observed nodes drops to 0, NHC remains Enabled, invalid operator value rejected ("is not a valid"), empty selector rejected ("Selector is mandatory"), NHC state unchanged after rejected edits

### 13. NHC Editing and Deletion Blocked During Remediation ([OCP-56600](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-56600))

Stops kubelet via SSH to trigger remediation, then verifies non-selector
fields (minHealthy, unhealthyConditions) remain editable, while NHC
webhook blocks selector editing and CR deletion during active remediation.

- **Operators**: NHC v0.12.0+, SNR
- **Cluster**: Multi-node (2+ workers), SSH access to worker nodes
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="nhc && disruption:destructive" --focus="selector editing and deletion" ./tests/nhc-operator/...`
- **Pass criteria**: minHealthy and unhealthyConditions edit succeeds during remediation, selector edit rejected ("selector update prohibited due to running remediation"), CR deletion rejected ("deletion prohibited due to running remediation"), NHC CR still exists and Remediating after delete attempt, SNR remediation completes, node recovers, NHC returns to Enabled

### 14. Old Default NHC CR Name ([OCP-69711](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-69711))

Creates NHC CRs with the legacy name "nhc-worker-default" and a
control-plane NHC, stops kubelet on a worker and on the control-plane
node hosting the active NHC controller, and verifies the NHC controller
fails over to another node and both remediations complete.

- **Operators**: NHC v0.12.0+, SNR
- **Cluster**: Multi-node (2+ workers), SSH access to worker and control-plane nodes
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="nhc && disruption:destructive" --focus="old default NHC CR" ./tests/nhc-operator/...`
- **Pass criteria**: SNR CR created for worker (NHC triggered via legacy CR), NHC controller restarts on another node (2 ready replicas), both worker and control-plane nodes recover to Ready, control-plane NHC returns to Enabled, NHC deployment remains Ready

### 15. Only One NHC CR Remediates at a Time ([OCP-66814](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-66814))

Creates two NHC CRs with different remediators (SNR at 30s, TestRemediation
at 10s), stops kubelet via SSH, and verifies only the shorter-duration
TestRemediation NHC creates a remediation CR. The SNR NHC must NOT create
an SNR CR while the node is already being remediated.

- **Operators**: NHC v0.12.0+, SNR
- **Cluster**: Multi-node (2+ workers), SSH access to worker nodes
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="nhc && disruption:destructive" --focus="one CR at a time" ./tests/nhc-operator/...`
- **Pass criteria**: TestRemediation CR created for target node, SNR CR NOT created (Consistently), TestRemediation NHC returns to Enabled after kubelet restart, target node recovers to Ready

### 16. Non-Remediating NHC CR Deletion During Active Remediation ([OCP-71171](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-71171))

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

Tests that verify NHC escalatingRemediations behavior end-to-end:
escalation when first remediator times out, no escalation when first
remediator succeeds, and escalation after SNR timeout. Uses SSH to
disable kubelet persistently (survives reboot) or stop kubelet
(recoverable after reboot).

### Prerequisites (Escalation E2E)

- NHC and SNR operators installed
- At least 2 Ready worker nodes
- SSH access to worker nodes
- `KUBECONFIG` set with cluster-admin access

### 17. Escalation from TestRemediation to SNR ([OCP-60857](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-60857))

Creates NHC with two-step escalation: TestRemediation (order=0,
timeout=60s) then SNR (order=1, timeout=180s). Disables kubelet
persistently via SSH. Verifies TestRemediation CR appears first, SNR CR
does not exist yet, then after TestRemediation timeout SNR CR appears
(both coexist). SNR reboots the node, kubelet is re-enabled, NHC
returns to Enabled and cleans up both CRs.

- **Operators**: NHC v0.12.0+, SNR
- **Cluster**: Multi-node (2+ workers), SSH access to worker nodes
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="nhc && disruption:destructive" --focus="Escalates from TestRemediation to SNR" ./tests/nhc-operator/...`
- **Pass criteria**: TestRemediation CR created for target node, SNR CR absent before timeout, SNR CR created after TestRemediation timeout, both CRs coexist, boot ID changes (node rebooted), kubelet re-enabled, NHC returns to Enabled, both CRs cleaned up

### 18. No Escalation When First Remediator Restores Health ([OCP-60858](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-60858))

Creates NHC with SNR first (order=0, timeout=180s) then TestRemediation
(order=1, timeout=600s). Stops kubelet (recoverable after reboot). SNR
reboots the node and kubelet auto-restarts. Verifies TestRemediation CR
was NEVER created (Consistently held for NegativeAssertionHoldDuration).
NHC returns to Enabled.

- **Operators**: NHC v0.12.0+, SNR
- **Cluster**: Multi-node (2+ workers), SSH access to worker nodes
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="nhc && disruption:destructive" --focus="Does not escalate" ./tests/nhc-operator/...`
- **Pass criteria**: SNR reboots node (boot ID changes), node becomes Ready, TestRemediation CR never created (Consistently), NHC returns to Enabled

### 19. Escalation After SNR Timeout ([OCP-66806](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-66806))

Creates NHC with SNR first (order=0, timeout=60s) then TestRemediation
(order=1, timeout=600s). Disables kubelet persistently via SSH. SNR
reboots the node but kubelet stays disabled, so the node remains
unhealthy. After SNR timeout, NHC escalates to TestRemediation. Both
CRs coexist. Kubelet is re-enabled, NHC returns to Enabled and cleans
up both CRs.

- **Operators**: NHC v0.12.0+, SNR
- **Cluster**: Multi-node (2+ workers), SSH access to worker nodes
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="nhc && disruption:destructive" --focus="Escalates after SNR timeout" ./tests/nhc-operator/...`
- **Pass criteria**: SNR CR created for target node, TestRemediation CR created after SNR timeout, both CRs coexist, kubelet re-enabled, NHC returns to Enabled, both CRs cleaned up
