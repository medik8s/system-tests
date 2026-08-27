# SBR Operator Post-Deployment Tests

Automated tests validating the Storage-Based Remediation (SBR) operator
deployment, security posture, and high-availability configuration.

## Prerequisites

- OpenShift cluster with SBR operator installed via OLM
- `KUBECONFIG` set with cluster-admin access
- SBR installed in `openshift-workload-availability` namespace

## Running

```bash
ginkgo --label-filter="sbr" ./tests/sbr-operator/...
```

## Tests

### 1. Verify SBR Operator Pod is Running ([OCP-89232](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-89232))

Validates that SBR controller-manager pods are in Running state and the
pod count matches the cluster topology (2 on multi-node, 1 on SNO).

- **Operators**: SBR v0.3.0
- **Cluster**: Any topology (MNO or SNO)
- **Storage**: None
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="sbr" --focus="pod is running" ./tests/sbr-operator/...`
- **Pass criteria**: All pods Running, count matches expected replicas

### 2. Verify SBR CSV Has Required Annotations ([OCP-89233](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-89233))

Validates that the active SBR ClusterServiceVersion (in Succeeded phase)
has all required OLM feature annotations: disconnected support, FIPS
compliance flag, suggested namespace, and feature flags.

- **Operators**: SBR v0.3.0
- **Cluster**: Any topology
- **Storage**: None
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="sbr" --focus="required annotations" ./tests/sbr-operator/...`
- **Pass criteria**: Required annotations present with expected values

### 3. Verify SBR Controller Replicas and Node Distribution ([OCP-89234](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-89234))

Validates that 2 replicas are running and scheduled on different nodes
for high availability. Skipped on SNO clusters where only 1 replica is
expected.

- **Operators**: SBR v0.3.0
- **Cluster**: Multi-node only (skips on SNO)
- **Storage**: None
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="sbr" --focus="correct number of replicas" ./tests/sbr-operator/...`
- **Pass criteria**: 2 ready replicas on 2 different nodes

### 4. Verify SBR Container Security Context ([OCP-89235](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-89235))

Validates the manager container follows the restricted security posture:
runAsNonRoot at pod level, allowPrivilegeEscalation=false,
capabilities.drop=ALL, and seccompProfile=RuntimeDefault (at container
or pod level). Only checks the `manager` container, not sidecars.

- **Operators**: SBR v0.3.0
- **Cluster**: Any topology
- **Storage**: None
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="sbr" --focus="non-root user" ./tests/sbr-operator/...`
- **Pass criteria**: All security context fields match restricted profile

### 5. Verify SBR Uses Correct API and OLM Naming ([OCP-88822](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-88822))

Validates that the active SBR CSV display name uses "Storage-Based Remediation"
(not the legacy "SBD" branding) and that all owned CRDs are registered under the
correct API group `storage-based-remediation.medik8s.io`.

- **Operators**: SBR v0.3.0
- **Cluster**: Any topology
- **Storage**: None
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="sbr" --focus="correct API and OLM naming" ./tests/sbr-operator/...`
- **Pass criteria**: CSV display name contains "Storage-Based Remediation", does not contain "SBD", all CRD API groups match expected value

### 6. Verify StorageBasedRemediationConfig CRD Schema Rejects Invalid Values ([OCP-88881](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-88881))

Validates two layers of StorageBasedRemediationConfig validation:

**Layer 1 (CRD OpenAPI schema)**: The API server rejects StorageBasedRemediationConfig resources with
out-of-range field values for `sbrTimeoutSeconds` and `maxConsecutiveFailures`.

**Layer 2 (Controller validation)**: A StorageBasedRemediationConfig referencing a non-existent
StorageClass is admitted by the API server but the controller does not schedule
a DaemonSet for it.

- **Operators**: SBR v0.3.0
- **Cluster**: Any topology
- **Storage**: None
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="sbr" --focus="StorageBasedRemediationConfig" ./tests/sbr-operator/...`
- **Pass criteria**: Out-of-range StorageBasedRemediationConfig fields rejected; invalid-StorageClass StorageBasedRemediationConfig admitted but no DaemonSet created

### 7. Verify StorageBasedRemediationConfig Controller Handles Invalid Inputs Without Scheduling Agent Pods ([OCP-88741](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-88741))

Validates that the SBR controller does not schedule agent DaemonSets when
`StorageBasedRemediationConfig` resources specify inputs the controller cannot
act on:

- **Invalid watchdog path**: StorageBasedRemediationConfig with a non-existent watchdog device path
  (`/dev/sbr-test-nonexistent-watchdog`) is admitted by the API server but the
  controller schedules no DaemonSet.
- **Non-matching nodeSelector**: StorageBasedRemediationConfig with a nodeSelector that matches no cluster
  nodes is admitted and may produce a DaemonSet, but `DesiredNumberScheduled`
  must remain 0 for the duration of the observation window.

- **Operators**: SBR v0.3.0
- **Cluster**: Any topology
- **Storage**: None
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="sbr" --focus="invalid watchdog path and non-matching nodeSelector" ./tests/sbr-operator/...`
- **Pass criteria**: No agent pods scheduled for either invalid StorageBasedRemediationConfig input; StorageBasedRemediationConfig CRs remain present after controller reconciliation

### 8. Verify Watchdog Device Accessibility and Softdog Module Availability ([OCP-88878](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-88878))

Validates that every schedulable cluster node either has accessible hardware watchdog character
devices or has the softdog kernel module available as a fallback.

The test reuses the `/dev/watchdog*` inventory populated by the "SBR Debug — Cluster Watchdog
Inventory" suite when it ran in the same Ginkgo session; otherwise it discovers devices
independently using a short-lived privileged hostPID pod per node.

- **Operators**: SBR v0.3.0
- **Cluster**: Any topology (BM or VM nodes with watchdog hardware or softdog)
- **Storage**: None
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="sbr" --focus="Verify watchdog device" ./tests/sbr-operator/...`
- **Pass criteria**: All hardware watchdog devices are character devices; nodes without hardware watchdog have softdog.ko present in the kernel module tree

### 9. Verify SBR Must-Gather Collects Diagnostic Data ([OCP-88733](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-88733))

Validates that `oc adm must-gather` with the medik8s must-gather image collects
SBR-related diagnostic data: node manifests, CRD definitions, and
MachineHealthCheck resources.

- **Operators**: SBR v0.3.0
- **Cluster**: Any topology
- **Storage**: None
- **Environment**: Connected by default; the image defaults to the upstream
  `quay.io/medik8s/must-gather:latest` (matching the FAR suite), and its resolved
  digest is logged on every run. Disconnected clusters can set `MUST_GATHER_IMAGE`
  to an accessible mirrored image
- **Standalone**: `ginkgo --label-filter="sbr" --focus="must-gather" ./tests/sbr-operator/...`
- **Pass criteria**: SBR deployment is Ready; must-gather completes successfully; output contains node YAMLs for all cluster nodes, all 3 SBR CRD definition files, and MachineHealthCheck data

### 10. Verify SBR Operator Survives OCP and Operator Upgrade ([OCP-89720](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-89720))

Validates the full customer upgrade path: install GA SBR from redhat-operators on OCP N-1, upgrade OCP to N, verify the GA operator still functions on the upgraded cluster, switch Subscription to Konflux FBC catalog (pre-GA), and verify the upgraded operator functions. SBR's full remediation path needs ODF/CephFS storage, watchdog devices, and NHC-triggered fault injection (see #6); exercising that whole chain inside an already-long upgrade test would make it both slower and more infra-fragile than necessary. Instead this test uses the simplest real proof the operator is functioning end to end: creating a StorageBasedRemediationConfig and confirming its agent DaemonSet reaches Ready, then tearing it down.

- **Operators**: SBR GA (from redhat-operators) + SBR pre-GA (from Konflux FBC)
- **Cluster**: Any topology, OCP N-1 at start (upgraded to N during test)
- **Storage**: A CephFS-backed StorageClass must be available (auto-discovered, or set via `SBR_STORAGE_CLASS`)
- **Environment**: Connected
- **Labels**: `tier:upgrade`, `disruption:destructive`, `platform:any`, `frequency:weekly`, `component:olm`
- **Env vars (required)**: `OPENSHIFT_UPGRADE_RELEASE_IMAGE_OVERRIDE` (falls back to `RELEASE_IMAGE_LATEST` if unset)
- **CI prerequisite**: `medik8s-catalogsource` step must run before the test (creates the `medik8s-catalog` CatalogSource)
- **Env vars (optional, have defaults)**: `MEDIK8S_OPERATOR_PACKAGE` (default: `storage-based-remediation`), `MEDIK8S_TARGET_CHANNEL` (default: `stable`)
- **Standalone**: `ginkgo --label-filter="sbr && tier:upgrade" ./tests/sbr-operator/...`
- **Pass criteria**: SBR deployment Ready on OCP N-1, OCP upgrade completes (Progressing=False, Available=True, Failing=False), SBR deployment Ready after OCP upgrade, SBR CSV in Succeeded phase after catalog switch (new CSV if Konflux version is higher than GA, same CSV if versions match), controller image changes after operator upgrade (skipped on version parity), SBRC agent DaemonSet reaches Ready after OCP upgrade and after catalog switch
