# SNR Operator Tests

Automated tests validating the Self Node Remediation (SNR) operator
deployment, configuration, OLM metadata, CRD validation, config lifecycle,
and destructive remediation (kubelet stop, node reboot via NHC detection).

## Prerequisites

- OpenShift cluster with SNR operator installed via OLM
- `KUBECONFIG` set with cluster-admin access
- SNR installed in `openshift-workload-availability` namespace
- For destructive worker tests (15-17): NHC operator installed, 2+ worker nodes
- For destructive master tests (18-19): NHC operator installed, 1+ worker nodes,
  3+ master nodes for etcd quorum safety

## Running

```bash
# All SNR tests (non-destructive + destructive)
ginkgo --label-filter="snr" ./tests/snr-operator/...

# Non-destructive tests only
ginkgo --label-filter="snr && disruption:nondestructive" ./tests/snr-operator/...

# Destructive remediation tests only
ginkgo --label-filter="snr && disruption:destructive" ./tests/snr-operator/...
```

Or via the test runner:

```bash
export KUBECONFIG=/path/to/kubeconfig
export ECO_TEST_FEATURES="snr-operator"
make run-tests
```

## Tests

### 1. Verify SNR Resources Are Installed and Running ([OCP-54205](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-54205))

Validates that the SelfNodeRemediationConfig CR exists, DaemonSet pods
are running, and controller-manager pods are in Running state.

- **Operators**: SNR v0.13.0+
- **Cluster**: Any topology (MNO or SNO)
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="snr" --focus="resources are installed" ./tests/snr-operator/...`
- **Pass criteria**: SNRC exists by name, at least one DS pod running, all controller pods running

### 2. Verify Only Automatic Remediation Template Exists ([OCP-71010](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-71010))

Validates that the "Automatic" SelfNodeRemediationTemplate exists with
the correct remediation strategy, and that unsupported templates
(ResourceDeletion, NodeDeletion) do not exist.

- **Operators**: SNR v0.13.0+
- **Cluster**: Any topology
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="snr" --focus="Automatic remediation template" ./tests/snr-operator/...`
- **Pass criteria**: Automatic template exists with strategy "Automatic", ResourceDeletion and NodeDeletion templates return NotFound

### 3. Verify SNR CSV Annotations ([OCP-52136](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-52136))

Validates that the active SNR ClusterServiceVersion (in Succeeded phase)
has required OLM annotations: valid-subscription, support contact,
repository URL, and at least one maintainer.

- **Operators**: SNR v0.13.0+
- **Cluster**: Any topology
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="snr" --focus="CSV annotations" ./tests/snr-operator/...`
- **Pass criteria**: All required annotations present, maintainers list non-empty

### 4. Verify SNR CSV Metadata ([OCP-70705](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-70705))

Validates infrastructure feature annotations (disconnected, fips-compliant,
proxy-aware, etc.), suggested-namespace points to
openshift-workload-availability, replaces field references previous SNR
version, and controller replicas match expected count on multi-node clusters.

- **Operators**: SNR v0.13.0+
- **Cluster**: Multi-node for replica check (skips replica validation on SNO)
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="snr" --focus="CSV metadata" ./tests/snr-operator/...`
- **Pass criteria**: All infrastructure annotations match expected values, suggested-namespace correct, replaces field contains "self-node-remediation", 2 ready replicas on MNO

### 5. Verify SNR with Unsupported NodeDeletion Strategy Is Rejected ([OCP-60877](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-60877))

Validates that creating a SelfNodeRemediation CR with the unsupported
NodeDeletion remediation strategy is rejected by CRD validation.

- **Operators**: SNR v0.12.1+
- **Cluster**: Any topology
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="snr" --focus="SNR with unsupported" ./tests/snr-operator/...`
- **Pass criteria**: API server rejects with "Unsupported value" and "NodeDeletion"

### 6. Verify SNRT with Unsupported NodeDeletion Strategy Is Rejected ([OCP-60822](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-60822))

Validates that creating a SelfNodeRemediationTemplate with the unsupported
NodeDeletion remediation strategy is rejected by CRD validation.

- **Operators**: SNR v0.12.1+
- **Cluster**: Any topology
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="snr" --focus="SNRT with unsupported" ./tests/snr-operator/...`
- **Pass criteria**: API server rejects with "Unsupported value" and "NodeDeletion"

### 7. Verify SNR Conditions with nhc-timed-out Annotation ([OCP-60881](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-60881))

Validates that creating a SelfNodeRemediation CR with the nhc-timed-out
annotation causes Processing and Succeeded conditions to reflect
RemediationTimeoutByNHC reason.

- **Operators**: SNR v0.12.1+
- **Cluster**: Any topology
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="snr" --focus="nhc-timed-out annotation" ./tests/snr-operator/...`
- **Pass criteria**: Processing=False/RemediationTimeoutByNHC, Succeeded=False/RemediationTimeoutByNHC

### 8. Verify SNR Conditions with Non-Existent Node Name ([OCP-70584](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-70584))

Validates that creating a SelfNodeRemediation CR with a non-existent node
name causes Processing and Succeeded conditions to reflect
RemediationSkippedNodeNotFound reason.

- **Operators**: SNR v0.12.1+
- **Cluster**: Any topology
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="snr" --focus="non-existent node name" ./tests/snr-operator/...`
- **Pass criteria**: Processing=False/RemediationSkippedNodeNotFound, Succeeded=False/RemediationSkippedNodeNotFound

### 9. Verify CRD Description of safeTimeToAssumeNodeRebootedSeconds ([OCP-60824](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-60824))

Validates that the SelfNodeRemediationConfig CRD schema contains the
expected description text for the safeTimeToAssumeNodeRebootedSeconds
field in the storage version.

- **Operators**: SNR v0.12.1+
- **Cluster**: Any topology
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="snr" --focus="CRD description" ./tests/snr-operator/...`
- **Pass criteria**: Field description contains expected text about safe time to assume node rebooted

### 10. Verify Non-Default SNRC Creation Is Rejected ([OCP-50961](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-50961))

Validates that creating a second SelfNodeRemediationConfig with a
non-default name is rejected, enforcing the single-instance constraint.

- **Operators**: SNR v0.12.1+
- **Cluster**: Any topology
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="snr" --focus="non-default SNRC" ./tests/snr-operator/...`
- **Pass criteria**: API server rejects with error mentioning single SNRC enforcement

### 11. Verify Invalid Values in SNRC Are Rejected ([OCP-47330](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-47330))

Validates that creating a SelfNodeRemediationConfig with invalid string
values or too-small duration values is rejected by the webhook with
specific validation error messages for each field.

- **Operators**: SNR v0.12.1+
- **Cluster**: Any topology
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="snr" --focus="invalid values in SNRC" ./tests/snr-operator/...`
- **Pass criteria**: Invalid string values rejected; too-small durations rejected with per-field minimum messages

### 12. Verify lastError Is Captured for Non-Existent Node ([OCP-50583](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-50583))

Validates that creating a SelfNodeRemediation CR targeting a node name
that does not exist in the cluster causes the lastError status field to
be populated with a "not found" message.

- **Operators**: SNR v0.12.1+
- **Cluster**: Any topology
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="snr" --focus="lastError is captured" ./tests/snr-operator/...`
- **Pass criteria**: lastError field contains node-not-found message

### 13. Verify SNR Auto-Detects Softdog Path ([OCP-50770](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-50770))

Validates that when the SNRC watchdogFilePath is set to an invalid path,
the SNR agent auto-detects the softdog path and logs a message. The test
patches the SNRC, waits for DS pods to restart, checks logs, and
restores the original path.

- **Operators**: SNR v0.12.1+
- **Cluster**: Any topology with softdog support
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="snr" --focus="auto-detects softdog" ./tests/snr-operator/...`
- **Pass criteria**: DS pods restart after config change; at least one pod log contains softdog auto-detection message

### 14. Verify SNRC Deletion Disables SNR and Recreation Re-Enables It ([OCP-74298](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-74298))

Validates the full SNRC lifecycle: deleting the default SNRC removes DS
pods and disables remediation (Disabled condition with
ConfigurationNotFound reason); recreating the SNRC brings DS pods back
and the Disabled condition disappears.

- **Operators**: SNR v0.12.1+
- **Cluster**: Any topology with at least one worker node
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="snr" --focus="SNRC deletion disables" ./tests/snr-operator/...`
- **Pass criteria**: DS pods deleted after SNRC removal; SNR CR shows Disabled/ConfigurationNotFound; after SNRC recreation DS pods return and Disabled condition is absent

### Destructive Tests

Tests that stop kubelet on nodes, triggering NHC-based health detection
and SNR remediation with node reboots. Require NHC operator installed,
2+ worker nodes, and 3+ master nodes. Run time: ~5-10 minutes per test.

### 15. Verify Worker Node Remediation After Kubelet Stop ([OCP-52416](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-52416))

Stops kubelet on a worker node, NHC detects the unhealthy node and
creates an SNR CR, SNR remediates by rebooting the node, then verifies
the node recovers. Also validates that the OutOfServiceTaint strategy
was auto-selected (OCP 4.15+) via controller-manager logs.

- **Operators**: SNR v0.13.0+, NHC v0.12.0+
- **Cluster**: Multi-node with 2+ workers
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="snr" --focus="worker node after kubelet stop" ./tests/snr-operator/...`
- **Pass criteria**: Node rebooted (boot ID changed), creation timestamp unchanged (not deleted/recreated), OutOfServiceTaint auto-selected log message found

### 16. Verify ResourceDeletion Strategy Evicts Workload Pod ([OCP-50772](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-50772))

Creates a ResourceDeletion SNRT, deploys a test workload pod on the
target worker, stops kubelet, and verifies the pod is evicted from the
remediated node after SNR completes the remediation cycle.

- **Operators**: SNR v0.13.0+, NHC v0.12.0+
- **Cluster**: Multi-node with 2+ workers (skips if insufficient)
- **Environment**: Connected or disconnected (workload pod uses OCP internal registry)
- **Standalone**: `ginkgo --label-filter="snr" --focus="ResourceDeletion" ./tests/snr-operator/...`
- **Pass criteria**: Node rebooted, creation timestamp unchanged, workload pod evicted (deleted or moved off remediated node)

### 17. Verify OutOfServiceTaint Strategy Evicts Workload Pod ([OCP-61594](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-61594))

Creates an OutOfServiceTaint SNRT, deploys a test workload pod on the
target worker, stops kubelet, and verifies the pod is evicted from the
remediated node after SNR completes the remediation cycle.

- **Operators**: SNR v0.13.0+, NHC v0.12.0+
- **Cluster**: Multi-node with 2+ workers (skips if insufficient)
- **Environment**: Connected or disconnected (workload pod uses OCP internal registry)
- **Standalone**: `ginkgo --label-filter="snr" --focus="OutOfServiceTaint" ./tests/snr-operator/...`
- **Pass criteria**: Node rebooted, creation timestamp unchanged, workload pod evicted (deleted or moved off remediated node)

### 18. Verify Master Node Remediation After Kubelet Stop ([OCP-55059](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-55059))

Stops kubelet on a master/control-plane node, NHC detects the unhealthy
node and creates an SNR CR, SNR remediates by rebooting the node, then
verifies the node recovers and was not deleted/recreated.

- **Operators**: SNR v0.13.0+, NHC v0.12.0+
- **Cluster**: Multi-node with 3+ masters (etcd quorum safety)
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="snr" --focus="master node after kubelet stop" ./tests/snr-operator/...`
- **Pass criteria**: Master rebooted (boot ID changed), creation timestamp unchanged (not deleted/recreated), node returns to Ready

### 19. Verify Simultaneous Master and Worker Remediation ([OCP-56069](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-56069))

Stops kubelet on both a master and a worker node simultaneously, creates
separate NHC CRs for each role, and verifies both nodes are remediated
concurrently -- both reboot and recover independently.

- **Operators**: SNR v0.13.0+, NHC v0.12.0+
- **Cluster**: Multi-node with 3+ masters and 1+ workers
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="snr" --focus="simultaneously" ./tests/snr-operator/...`
- **Pass criteria**: Both nodes rebooted (boot IDs changed), creation timestamps unchanged (not deleted/recreated), both recovered to Ready state
