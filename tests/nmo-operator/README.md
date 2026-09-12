# NMO Operator Post-Deployment Tests

Automated tests validating the Node Maintenance Operator (NMO)
deployment, OLM metadata, and security posture.

## Prerequisites

- OpenShift cluster with NMO operator installed via OLM
- `KUBECONFIG` set with cluster-admin access
- NMO installed in `openshift-workload-availability` namespace

## Running

```bash
ginkgo --label-filter="operator:nmo" ./tests/nmo-operator/...
```

Or via the test runner:

```bash
export KUBECONFIG=/path/to/kubeconfig
export ECO_TEST_FEATURES="nmo-operator"
make run-tests
```

## Tests

### 1. Verify Node Maintenance Operator Pod Is Running ([OCP-46315](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-46315))

Validates that the NMO controller-manager pod is in Running state
with all containers ready.

- **Operators**: NMO v0.17.0+
- **Cluster**: Any topology (MNO or SNO)
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="operator:nmo" --focus="pod is running" ./tests/nmo-operator/...`
- **Pass criteria**: At least 1 running NMO controller pod with all containers ready

### 2. Verify NMO CSV Has Required Annotations ([OCP-89626](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-89626))

Validates that the active NMO ClusterServiceVersion (in Succeeded phase)
has all required OLM infrastructure annotations with expected values.

- **Operators**: NMO v0.17.0+
- **Cluster**: Any topology
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="operator:nmo" --focus="CSV has required annotations" ./tests/nmo-operator/...`
- **Pass criteria**: All 8 required annotations present with correct values (disconnected, fips-compliant, proxy-aware, tls-profiles, token-auth-aws/azure/gcp, suggested-namespace)

### 3. Verify NMO Controller Manager Has Correct Number of Replicas ([OCP-89627](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-89627))

Validates that the NMO deployment has the expected replica count
and all replicas are ready. NMO runs a single replica on all
cluster topologies (MNO and SNO).

- **Operators**: NMO v0.17.0+
- **Cluster**: Any topology
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="operator:nmo" --focus="correct number of replicas" ./tests/nmo-operator/...`
- **Pass criteria**: spec.replicas == 1 and status.readyReplicas == 1

### 4. Verify NMO Container Runs as Non-Root User ([OCP-89628](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-89628))

Validates that the NMO manager container enforces a restricted
security context: runAsNonRoot, no privilege escalation,
read-only root filesystem, all capabilities dropped, and
RuntimeDefault seccomp profile.

- **Operators**: NMO v0.17.0+
- **Cluster**: Any topology
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="operator:nmo" --focus="runs as non-root" ./tests/nmo-operator/...`
- **Pass criteria**: Pod runAsNonRoot=true; expected manager container exists; manager container runAsUser != 0; allowPrivilegeEscalation=false; readOnlyRootFilesystem=true; capabilities.drop=[ALL]; seccomp profile RuntimeDefault

### 5. Start Node Maintenance ([OCP-29592](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-29592))

Creates a NodeMaintenance CR for a schedulable worker node and validates
that the node enters maintenance mode (cordoned, Succeeded phase).

- **Operators**: NMO v0.17.0+
- **Cluster**: MNO with at least 2 schedulable worker nodes
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="operator:nmo" --focus="Start node maintenance" ./tests/nmo-operator/...`
- **Pass criteria**: NodeMaintenance CR reaches Succeeded phase; target node is cordoned (Unschedulable=true) with medik8s.io/drain NoSchedule taint; DrainProgress=100 and PendingPods empty; BeginMaintenance and SucceedMaintenance events emitted; maintenance lease created in medik8s-leases namespace with correct LeaseDurationSeconds and HolderIdentity

### 6. Schedule Pod to Node Under Maintenance ([OCP-29603](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-29603))

Attempts to schedule a pod targeting a cordoned node and verifies it
remains Pending with no node assignment.

- **Operators**: NMO v0.17.0+
- **Cluster**: MNO with at least 2 schedulable worker nodes
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="operator:nmo" --focus="Schedule pod to node under maintenance" ./tests/nmo-operator/...`
- **Pass criteria**: Pod stays in Pending phase; pod has no nodeName assigned; DrainProgress=100 and PendingPods empty

### 7. Maintenance Mode Persists After Node Reboot ([OCP-46761](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-46761))

Reboots the node under maintenance via `oc debug` and verifies that
maintenance mode (cordon + drain taint + Succeeded phase) survives the reboot.

- **Operators**: NMO v0.17.0+
- **Cluster**: MNO with at least 2 schedulable worker nodes
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="operator:nmo" --focus="Maintenance mode persists" ./tests/nmo-operator/...`
- **Pass criteria**: Node reboots (boot ID changes) and recovers to Ready; NodeMaintenance CR still exists with Succeeded phase; node remains cordoned with medik8s.io/drain taint; maintenance lease persists

### 8. Stop Node Maintenance ([OCP-29594](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-29594))

Deletes the NodeMaintenance CR and validates the node is uncordoned
and returns to schedulable state.

- **Operators**: NMO v0.17.0+
- **Cluster**: MNO with at least 2 schedulable worker nodes
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="operator:nmo" --focus="Stop node maintenance" ./tests/nmo-operator/...`
- **Pass criteria**: NodeMaintenance CR is fully deleted; target node is uncordoned (Unschedulable=false) and medik8s.io/drain taint removed; RemovedMaintenance event emitted; maintenance lease deleted

### 9. Reject a Second NodeMaintenance With a Duplicate Name ([OCP-29632](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-29632))

Creates a NodeMaintenance CR on one worker and drives it to maintenance, then
attempts to create a second CR that reuses the same name (targeting a fresh
schedulable worker -- the first node is now cordoned -- so the collision is on the
object name, not the node) and verifies the API server rejects it.

- **Operators**: NMO v0.17.0+
- **Cluster**: MNO with at least 2 schedulable worker nodes
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="operator:nmo" --focus="duplicate name" ./tests/nmo-operator/...`
- **Pass criteria**: First NodeMaintenance CR is created and reaches Succeeded phase; the second create with the same name fails with an error whose message contains `nodemaintenances.nodemaintenance.medik8s.io "node-maintenance-test" already exists`; cleanup then attempts (best-effort, logged if incomplete) to delete the CR and wait for the target node to return to Ready and uncordoned

### 10. Reject a Second NodeMaintenance for a Node Already Under Maintenance ([OCP-29630](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-29630))

Creates a NodeMaintenance CR for a worker and drives it to maintenance, then
attempts to create a second CR (different name) for the same node and verifies the
NMO validating webhook rejects it.

- **Operators**: NMO v0.17.0+
- **Cluster**: MNO with at least 2 schedulable worker nodes
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="operator:nmo" --focus="already under maintenance" ./tests/nmo-operator/...`
- **Pass criteria**: First NodeMaintenance CR is created and reaches Succeeded phase; the second create for the same node fails with a webhook error whose message contains `NodeMaintenance for node <node> already exists`; cleanup then attempts (best-effort, logged if incomplete) to delete both CR names and wait for the target node to return to Ready and uncordoned

### 11. Verify NMO Operator Survives Operator/Catalog Upgrade ([OCP-89719](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-89719))

Validates the operator-upgrade half of the customer upgrade path: install GA NMO from redhat-operators, run a maintenance cycle as a baseline, apply the deferred IDMS from `SHARED_DIR`, switch Subscription to the Konflux FBC catalog (pre-GA), and run another maintenance cycle to confirm the upgraded operator works. Fully self-contained (does not depend on #12 having run) -- does not touch the OCP cluster version at all. NMO does not remediate -- the maintenance cycle (create a NodeMaintenance CR, verify cordon/taint/drain, delete it, wait for recovery) is the upgrade-test analog to FAR/SNR/MDR's remediation cycle, reusing the existing collision-test helpers (see #9, #10).

- **Operators**: NMO GA (from redhat-operators) + NMO pre-GA (from Konflux FBC)
- **Cluster**: MNO with at least 1 schedulable worker node
- **Storage**: None
- **Environment**: Connected
- **Labels**: `tier:upgrade-operator`, `disruption:destructive`, `platform:any`, `frequency:weekly`, `component:olm`
- **CI prerequisite**: `medik8s-catalogsource` step must run before the test (creates the `medik8s-catalog` CatalogSource)
- **Env vars (optional, have defaults)**: `MEDIK8S_OPERATOR_PACKAGE` (default: `node-maintenance-operator`), `MEDIK8S_TARGET_CHANNEL` (default: `stable`)
- **Standalone**: `ginkgo --label-filter="operator:nmo && tier:upgrade-operator" ./tests/nmo-operator/...`
- **Pass criteria**: NMO deployment Ready, NMO CSV in Succeeded phase after catalog switch (new CSV if Konflux version is higher than GA, same CSV if versions match), controller image changes after operator upgrade (skipped on version parity), maintenance cycle succeeds both before and after the catalog switch

### 12. Verify NMO Operator Survives OCP Cluster Upgrade ([OCP-89719](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-89719))

Validates the OCP-upgrade half of the customer upgrade path: install GA NMO from redhat-operators on OCP N-1, run a maintenance cycle as a baseline, upgrade OCP to N, and run another maintenance cycle to confirm the operator still works on the upgraded cluster. Fully self-contained (does not depend on #11 having run) -- does not touch the operator Subscription/catalog at all.

- **Operators**: NMO GA (from redhat-operators)
- **Cluster**: MNO with at least 1 schedulable worker node, OCP N-1 at start (upgraded to N during test)
- **Storage**: None
- **Environment**: Connected
- **Labels**: `tier:upgrade-cluster`, `disruption:destructive`, `platform:any`, `frequency:weekly`, `component:olm`
- **Env vars (required)**: `OPENSHIFT_UPGRADE_RELEASE_IMAGE_OVERRIDE` (falls back to `RELEASE_IMAGE_LATEST` if unset) -- if `MEDIK8S_SKIP_OCP_UPGRADE=true`, this spec is skipped entirely instead
- **Env vars (optional, have defaults)**: `MEDIK8S_OPERATOR_PACKAGE` (default: `node-maintenance-operator`)
- **Standalone**: `ginkgo --label-filter="operator:nmo && tier:upgrade-cluster" ./tests/nmo-operator/...`
- **Pass criteria**: NMO deployment Ready on OCP N-1, OCP upgrade completes (Progressing=False, Available=True, Failing=False), NMO deployment Ready after OCP upgrade, maintenance cycle succeeds both before and after the OCP upgrade
