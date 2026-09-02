# MDR Operator Post-Deployment Tests

Automated tests validating the Machine Deletion Remediation (MDR) operator
deployment, OLM metadata, and security posture.

## Prerequisites

- OpenShift cluster with MDR operator installed via OLM
- `KUBECONFIG` set with cluster-admin access
- MDR installed in `openshift-workload-availability` namespace

## Running

```bash
ginkgo --label-filter="mdr" ./tests/mdr-operator/...
```

Or via the test runner:

```bash
export KUBECONFIG=/path/to/kubeconfig
export ECO_TEST_FEATURES="mdr-operator"
make run-tests
```

## Tests

### 1. Verify Machine Deletion Remediation Operator Pod Is Running ([OCP-65767](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-65767))

Validates that MDR controller-manager pods are in Running state and the
pod count matches the expected replica count (1). Filters
pods by deployment ownership to avoid counting unrelated pods with the
same label selector.

- **Operators**: MDR v0.7.0+
- **Cluster**: Any topology (MNO or SNO)
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="mdr" --focus="pod is running" ./tests/mdr-operator/...`
- **Pass criteria**: All MDR pods Running, count matches expected replica count (1)

### 2. Verify MDR CSV Has Required Annotations ([OCP-70221](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-70221))

Validates that the active MDR ClusterServiceVersion (in Succeeded phase)
has all required OLM infrastructure annotations with expected values.

- **Operators**: MDR v0.7.0+
- **Cluster**: Any topology
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="mdr" --focus="CSV has required annotations" ./tests/mdr-operator/...`
- **Pass criteria**: All required annotations present with correct values (disconnected, fips-compliant, proxy-aware, tls-profiles, cnf, cni, csi, token-auth-aws/azure/gcp, suggested-namespace)

### 3. Verify MDR Controller Manager Has Correct Number of Replicas ([OCP-89624](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-89624))

Validates that the MDR deployment has the expected replica count (1),
all replicas are ready, and each pod is assigned to a node. Skipped on
SNO clusters.

- **Operators**: MDR v0.7.0+
- **Cluster**: Multi-node only (skips on SNO)
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="mdr" --focus="correct number of replicas" ./tests/mdr-operator/...`
- **Pass criteria**: spec.replicas == 1, status.readyReplicas == 1, pod assigned to a node

### 4. Verify MDR Container Runs as Non-Root User ([OCP-89625](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-89625))

Validates that the MDR manager container enforces a restricted security
context: runAsNonRoot at pod level, runAsUser is not UID 0 when set,
allowPrivilegeEscalation=false, capabilities.drop=ALL,
readOnlyRootFilesystem=true, and seccompProfile=RuntimeDefault (at
container or pod level). Only checks the `manager` container.

- **Operators**: MDR v0.7.0+
- **Cluster**: Any topology
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="mdr" --focus="runs as non-root" ./tests/mdr-operator/...`
- **Pass criteria**: Pod runAsNonRoot=true; expected manager container exists; manager container runAsUser != 0; allowPrivilegeEscalation=false; readOnlyRootFilesystem=true; capabilities.drop=[ALL]; seccomp profile RuntimeDefault

## Condition Tests

### 5. Verify MDR Conditions With NHC Timed-Out Annotation ([OCP-65763](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-65763))

Creates an MDR CR with the `remediation.medik8s.io/nhc-timed-out` annotation.
Verifies that the Processing and Succeeded conditions have reason
`RemediationStoppedByNHC`, and that the controller log contains the
"NHC time out annotation found, stopping remediation" message.

- **Operators**: MDR v0.7.0+
- **Cluster**: Any topology (MNO or SNO)
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="mdr" --focus="nhc-timed-out annotation" ./tests/mdr-operator/...`
- **Pass criteria**: Processing reason=RemediationStoppedByNHC; Succeeded reason=RemediationStoppedByNHC; controller log contains remediation-stopped message; MDR controller pod running after test


### 6. Verify MDR Conditions With Non-Existent Node ([OCP-66137](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-66137))

Creates an MDR CR named `non-existent-node`. Verifies that the Processing and
Succeeded conditions have status=False and reason
`RemediationCannotStartNodeNotFound`. Guards against accidental real fencing
by asserting the node name does not exist in the cluster before creating the CR.

- **Operators**: MDR v0.7.0+
- **Cluster**: Any topology (MNO or SNO)
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="mdr" --focus="non-existent node" ./tests/mdr-operator/...`
- **Pass criteria**: Node name confirmed absent from cluster; Processing status=False reason=RemediationCannotStartNodeNotFound; Succeeded status=False reason=RemediationCannotStartNodeNotFound; MDR controller pod running after test

### 7. Verify MDR Conditions With Control-Plane Node ([OCP-66351](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-66351))

Creates an MDR CR named after a random control-plane node. On baremetal,
control-plane Machines have no controller owner and MDR sets
RemediationCannotStartNoControllerOwner. Skipped on cloud platforms where
CPMS gives control-plane Machines a controller owner, causing MDR to start
Machine deletion (destructive).

- **Operators**: MDR v0.7.0+
- **Cluster**: Any topology (MNO or SNO), baremetal only
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="mdr && platform:baremetal" --focus="control-plane node name" ./tests/mdr-operator/...`
- **Pass criteria**: Processing status=False reason=RemediationCannotStartNoControllerOwner; Succeeded status=False reason=RemediationCannotStartNoControllerOwner; MDR controller pod running after test

### 8. Verify PermanentNodeDeletionExpected Condition With Control-Plane Node ([OCP-66317](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-66317))

Creates an MDR CR for a control-plane node and verifies the
`PermanentNodeDeletionExpected` condition on baremetal: status=False,
reason=KeepsNodeName, message confirms node name is preserved.
Skipped on cloud platforms (same reason as test 7).

- **Operators**: MDR v0.7.0+
- **Cluster**: Any topology (MNO or SNO), baremetal only
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="mdr && platform:baremetal" --focus="PermanentNodeDeletionExpected" ./tests/mdr-operator/...`
- **Pass criteria**: PermanentNodeDeletionExpected status=False reason=KeepsNodeName message="...NOT expected to have a new name"; MDR controller pod running after test

## Negative Validation Tests

### 9. Verify MDRT With Invalid Values Is Rejected ([OCP-60889](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-60889))

Validates that the API server rejects MachineDeletionRemediationTemplate CRs
with invalid metadata. First attempts creation with a non-existent namespace
(`mdr-test-nonexistent-ns`), then with an invalid name (`-1-invalid-value`)
that violates RFC 1123 subdomain rules.

- **Operators**: MDR v0.7.0+
- **Cluster**: Any topology (MNO or SNO)
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="mdr" --focus="invalid values" ./tests/mdr-operator/...`
- **Pass criteria**: MDRT with non-existent namespace rejected with NotFound error; MDRT with invalid name rejected with Invalid error (k8serrors.IsInvalid)

## Destructive Tests -- NHC-Triggered Remediation

Tests that stop kubelet on a worker node, let NHC detect the unhealthy node
and trigger MDR remediation. MDR deletes the Machine object and the cloud
provider provisions a new VM. The node is re-created (new creation timestamp).

### Prerequisites (Remediation)

- MDR and NHC operators installed
- Cloud platform with MachineAPI (AWS, Azure, GCP, vSphere). Skips on baremetal/None
- At least 2 Ready worker nodes (target + spare for cluster schedulability)
- `KUBECONFIG` set with cluster-admin access

### 10. MDR Remediation with Condition Transitions ([OCP-66138](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-66138))

Stops kubelet on a worker node. NHC detects the unhealthy node and creates
an MDR CR via the MDR template. Verifies the MDR CR status conditions
transition correctly (Processing=True/RemediationStarted,
Succeeded=Unknown/RemediationStarted) during active remediation. MDR deletes
the Machine, the cloud provider provisions a new VM, and the replacement
node joins the cluster.

- **Operators**: MDR v0.7.0+, NHC v0.12.0+
- **Cluster**: Multi-node (2+ workers), cloud platform
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="mdr && disruption:destructive" --focus="Processing and Succeeded conditions" ./tests/mdr-operator/...`
- **Pass criteria**: Processing=True with RemediationStarted reason, Succeeded=Unknown with RemediationStarted reason, replacement node Ready
