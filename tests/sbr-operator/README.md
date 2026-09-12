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
- **Cluster**: Non-HyperShift topologies only (IPI, SNO, etc.). **Skipped on HyperShift** due to architectural limitations: node collection via `oc adm inspect nodes` fails (0 nodes collected), and MachineHealthCheck is only accessible on the management cluster
- **Storage**: None
- **Environment**: Connected by default; the image defaults to the upstream
  `quay.io/medik8s/must-gather:latest` (matching the FAR suite), and its resolved
  digest is logged on every run. Disconnected clusters can set `MUST_GATHER_IMAGE`
  to an accessible mirrored image
- **Standalone**: `ginkgo --label-filter="sbr" --focus="must-gather" ./tests/sbr-operator/...`
- **Pass criteria**: SBR deployment is Ready; must-gather completes successfully; output contains node YAMLs for all cluster nodes, all 3 SBR CRD definition files, and MachineHealthCheck data. On HyperShift, the test skips with an explanatory message

### 10. Verify Controller Availability With One Worker (Controller Resilience)

Validates that the SBR controller maintains at least one available replica when all but one
worker node is cordoned. The SBR deployment uses `topologySpreadConstraints` with
`whenUnsatisfiable: DoNotSchedule`, so only one replica can schedule on a single node.

The test cordons all eligible workers except one, deletes controller pods from cordoned nodes,
and verifies the surviving replica stays available throughout the degraded phase. After
uncordoning, verifies the deployment scales back to the expected replica count on different nodes.

- **Operators**: SBR v0.3.0
- **Cluster**: 3+ schedulable worker-only nodes (standard AWS Prow cluster)
- **Storage**: None
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="sbr" --focus="controller availability with one worker" ./tests/sbr-operator/...`
- **Pass criteria**: At least 1 controller pod remains Running on the uncordoned node; availability sustained for 30s (Consistently); deployment scales back to 2 replicas on different nodes after uncordoning

### 11. Verify Controller Leadership Handover

Validates that when the active SBR controller pod (the lease holder) is deleted, leadership
transfers to a different controller pod. Follows the same pattern as the FAR controller
lifecycle test (OCP-70636).

- **Operators**: SBR v0.3.0
- **Cluster**: Any topology with at least 2 schedulable worker-only (non-control-plane) nodes
- **Storage**: None
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="sbr" --focus="controller leadership" ./tests/sbr-operator/...`
- **Pass criteria**: Lease `holderIdentity` changes to a different Running controller pod; deployment returns to full ready replicas

### 12. Verify operator handles a stale RWX test PVC and reconciles successfully (RHWA-1017)

Covers the `testRWXSupport` reconciliation path used when an SBRC references a StorageClass
whose provisioner is not recognized by SBR (for example `sbr.io/nfs-provisioner` on the CI
`nfs-sbr-dynamic` StorageClass). That path creates a transient RWX PVC to probe shared-storage
support. If a prior run left behind a PVC with the same name 
(for example after an operator crash mid-reconcile), reconciliation can stall on PVC
`AlreadyExists` unless the operator removes the stale object first.

The test pre-creates such a stale PVC, then creates an SBRC with `detectOnlyMode: Enabled` and a
worker `nodeSelector` (matching manual RHWA verification on VMs without accessible watchdog),
pointing at the unknown-provisioner StorageClass. It observes whether reconciliation proceeds
past `testRWXSupport`: the stale PVC is handled, the transient RWX test PVC is removed after
validation, and the operator reports `SharedStorageReady=True` with a bound shared-storage PVC.

- **Operators**: SBR
- **Cluster**: Any topology where the NFS dynamic provisioner step has deployed StorageClass `nfs-sbr-dynamic`
- **Storage**: NFS dynamic provisioner (`nfs-sbr-dynamic`); suite skips when that StorageClass is absent
- **Environment**: Connected (NFS provisioner deployed by CI)
- **Standalone**: `ginkgo --label-filter="sbr" --focus="stale RWX test PVC" ./tests/sbr-operator/...`
- **Pass criteria**: Stale PVC no longer references the fake StorageClass; RWX test PVC `<sbrc-name>-rwx-test` is deleted after `testRWXSupport`; SBRC reports `SharedStorageReady=True` and shared-storage PVC is Bound on `nfs-sbr-dynamic`

### 13. Verify Released PVs from testRWXSupport do not accumulate while SBRC is alive (RHWA-1046)

When an SBRC references a StorageClass whose provisioner is not recognized by SBR, reconciliation
runs `testRWXSupport`: a transient RWX PVC is created, access is validated, and the PVC is removed.
On StorageClasses with `reclaimPolicy: Retain` (such as CI `nfs-sbr-dynamic`), each probe can
leave a backing PV in `Released` phase. Repeated `testRWXSupport` runs can increase that count
over time while the SBRC remains.

Requires a reconciled SBRC `test-sbrc-unknown-prov` in `openshift-workload-availability` pointing
at `nfs-sbr-dynamic` with `SharedStorageReady=True`. In the ordered suite, test 12 establishes
this state; the spec skips when that SBRC has not reconciled. The test waits until no PVs for
`nfs-sbr-dynamic` are in `Released` phase with `Retain` reclaim policy, then observes for 90s
that the count stays at zero.

- **Operators**: SBR
- **Cluster**: Any topology
- **Storage**: NFS dynamic provisioner (`nfs-sbr-dynamic`, `reclaimPolicy: Retain`)
- **Environment**: Connected (NFS provisioner deployed by CI)
- **Standalone**: `ginkgo --label-filter="sbr" --focus="Released PVs from testRWXSupport" ./tests/sbr-operator/...`
- **Pass criteria**: Zero Released+Retain PVs for `nfs-sbr-dynamic` after the initial `testRWXSupport` pass; count does not reach 2 or more for 90s while SBRC is alive (tolerates a brief count of 1 during testRWXSupport PV cleanup)

### 14. Verify testRWXSupport does not recreate the RWX test PVC on every reconcile (RHWA-1047)

When an SBRC references a StorageClass whose provisioner is not recognized by SBR, reconciliation
runs `testRWXSupport` using a transient RWX PVC named `<sbrc-name>-rwx-test`. After a successful
probe, that PVC is removed. If `testRWXSupport` runs again on subsequent reconciles, the PVC
reappears periodically while the SBRC remains.

Requires a reconciled SBRC `test-sbrc-unknown-prov` in `openshift-workload-availability` pointing
at `nfs-sbr-dynamic`, with agent DaemonSet pods Ready. In the ordered suite, test 12 establishes
this state; the spec skips when that SBRC has not reconciled. The test records any SBRC status
condition whose type mentions RWX, then observes for 90s that PVC `test-sbrc-unknown-prov-rwx-test`
is absent.

- **Operators**: SBR
- **Cluster**: Any topology
- **Storage**: NFS dynamic provisioner (`nfs-sbr-dynamic`)
- **Environment**: Connected (NFS provisioner deployed by CI)
- **Standalone**: `ginkgo --label-filter="sbr" --focus="does not recreate the RWX test PVC" ./tests/sbr-operator/...`
- **Pass criteria**: RWX test PVC `test-sbrc-unknown-prov-rwx-test` stays absent for 90s while SBRC `test-sbrc-unknown-prov` is alive

### 15. Verify shared-storage PV is cleaned up after SBRC deletion (RHWA-1046)

When an SBRC with unknown-provisioner shared storage reconciles successfully, the operator
provisions a long-lived shared-storage PVC (`<sbrc-name>-shared-storage`) backed by a dynamically
provisioned PV. On SBRC deletion, `handleDeletion` cleans up that PVC and its backing volume.
With `reclaimPolicy: Retain` on the StorageClass, the PV can remain in `Released` phase unless
reclaim policy is updated during cleanup.

Requires a reconciled SBRC `test-sbrc-unknown-prov` in `openshift-workload-availability` with
a bound shared-storage PVC; the spec skips when that SBRC has not reconciled. The test records
the shared-storage PV name, deletes the SBRC, waits for the CR to be removed, then checks that
the backing PV is deleted or no longer in `Released` phase with `Retain` reclaim policy.

- **Operators**: SBR
- **Cluster**: Any topology
- **Storage**: NFS dynamic provisioner (`nfs-sbr-dynamic`)
- **Environment**: Connected (NFS provisioner deployed by CI)
- **Standalone**: `ginkgo --label-filter="sbr" --focus="shared-storage PV is cleaned up" ./tests/sbr-operator/...`
- **Pass criteria**: SBRC `test-sbrc-unknown-prov` is fully removed after delete; shared-storage PV is deleted or no longer Released+Retain within the PV cleanup timeout