# FAR Operator Post-Deployment Tests

Automated tests validating the Fence Agents Remediation (FAR) operator deployment, security posture, and high-availability configuration.

## Prerequisites

- OpenShift cluster with FAR operator installed via OLM
- `KUBECONFIG` set with cluster-admin access
- FAR installed in `openshift-workload-availability` namespace

## Running

```bash
ginkgo --label-filter="far" ./tests/far-operator/...
```

## Tests

### 1. Verify FAR Operator Pod is Running ([OCP-66026](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-66026))

Validates that FAR controller-manager pods are in Running state and the pod count matches the cluster topology (2 on multi-node, 1 on SNO).

- **Operators**: FAR v0.8.0
- **Cluster**: Any topology (MNO or SNO)
- **Storage**: None
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="far" --focus="pod is running" ./tests/far-operator/...`
- **Pass criteria**: All pods Running, count matches expected replicas for the topology

### 2. Verify FAR CSV Has Required Annotations ([OCP-70637](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-70637))

Validates that the active FAR ClusterServiceVersion (in Succeeded phase) has all required OLM feature annotations: disconnected support, FIPS compliance, suggested namespace, and feature flags.

- **Operators**: FAR v0.8.0
- **Cluster**: Any topology
- **Storage**: None
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="far" --focus="required annotations" ./tests/far-operator/...`
- **Pass criteria**: All required annotations present with expected values on the active CSV

### 3. Verify FAR Controller Replicas and Node Distribution ([OCP-61222](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-61222))

Validates that 2 replicas are running and scheduled on different nodes for high availability. Skipped on SNO clusters where only 1 replica is expected.

- **Operators**: FAR v0.8.0
- **Cluster**: Multi-node only (skips on SNO)
- **Storage**: None
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="far" --focus="correct number of replicas" ./tests/far-operator/...`
- **Pass criteria**: 2 ready replicas on 2 different nodes

### 4. Verify FAR Container Security Context ([OCP-89231](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-89231))

Validates the manager container follows the restricted security posture: runAsNonRoot at pod level, runAsUser is not UID 0 when set, allowPrivilegeEscalation=false, capabilities.drop=ALL, readOnlyRootFilesystem=true, and seccompProfile=RuntimeDefault (at container or pod level). Only checks the `manager` container, not sidecars.

- **Operators**: FAR v0.8.0
- **Cluster**: Any topology
- **Storage**: None
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="far" --focus="non-root user" ./tests/far-operator/...`
- **Pass criteria**: All security context fields match expected restricted profile

### 5. Verify FAR CRDs Are Installed and Established ([OCP-89548](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-89548))

Validates that both FAR Custom Resource Definitions are registered as cluster-level resources and have the `Established=True` status condition, confirming the API endpoints are active and ready for clients.

- **Operators**: FAR v0.8.0
- **Cluster**: Any topology
- **Storage**: None
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="far" --focus="CRDs are installed" ./tests/far-operator/...`
- **Pass criteria**: Both CRDs (`fenceagentsremediations` and `fenceagentsremediationtemplates`) exist with Established=True

### 6. Verify FAR Operator Namespace Has Correct PSA Enforcement Label ([OCP-89549](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-89549))

Validates that the operator namespace (`openshift-workload-availability`) has the correct Pod Security Admission enforcement label set to `privileged`, ensuring the namespace admission policy allows the operator pods to run with required permissions.

- **Operators**: FAR v0.8.0
- **Cluster**: Any topology
- **Storage**: None
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="far" --focus="PSA enforcement label" ./tests/far-operator/...`
- **Pass criteria**: Namespace has `pod-security.kubernetes.io/enforce=privileged` label

### 7. Verify FAR Controller Has system-cluster-critical Priority Class ([OCP-66211](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-66211))

Validates that all FAR controller-manager pods have `priorityClassName` set to `system-cluster-critical`, ensuring the controller retains scheduling priority during node pressure events.

- **Operators**: FAR v0.8.0
- **Cluster**: Any topology
- **Storage**: None
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="far" --focus="priority class" ./tests/far-operator/...`
- **Pass criteria**: All running FAR pods have `priorityClassName: system-cluster-critical`

### 8. Verify FAR Controller Pod Has Correct Kubernetes Labels ([OCP-66209](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-66209))

Validates that FAR controller-manager pods carry the standard `app.kubernetes.io/name` label with the correct value, ensuring service discovery and monitoring tools can identify FAR pods.

- **Operators**: FAR v0.8.0
- **Cluster**: Any topology
- **Storage**: None
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="far" --focus="Kubernetes labels" ./tests/far-operator/...`
- **Pass criteria**: All running FAR pods have `app.kubernetes.io/name=fence-agents-remediation-operator`

### 9. Verify FAR Controller Container Includes Expected Fence Agents ([OCP-78407](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-78407))

Validates that the FAR controller container image ships the minimum expected set of fence agent binaries in `/usr/sbin/`. Execs into the container and lists all `fence_*` binaries, then checks that a core subset (fence_aws, fence_azure_arm, fence_gce, fence_ipmilan, fence_kubevirt, fence_redfish) is present.

- **Operators**: FAR v0.8.0
- **Cluster**: Any topology
- **Storage**: None
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="far" --focus="fence agents" ./tests/far-operator/...`
- **Pass criteria**: All expected fence agent binaries are present in the container

## Non-Destructive Tests -- Negative Validation and Webhook Rejection

Tests that verify FAR webhook rejection of invalid CRs and controller
behavior with misconfigured resources. No node disruption -- all tests
are pure API-level.

### Prerequisites (Negative Validation)

- FAR operator installed
- `KUBECONFIG` set with cluster-admin access

### 10. Verify Node-Not-Found Error for Non-Existent CR Name ([OCP-65954](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-65954))

Creates a FAR CR with a name that does not match any cluster node. Verifies
the controller logs the node-not-found error message.

- **Operators**: FAR v0.8.0+
- **Cluster**: Any topology
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="far && disruption:nondestructive" --focus="node-not-found" ./tests/far-operator/...`
- **Pass criteria**: FAR CR created successfully; controller log contains node-not-found message

### 11. Verify Unsupported Action Rejection ([OCP-66090](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-66090))

Creates a FAR CR with `--action=status` (unsupported). Verifies the webhook rejects the CR at creation time with an error about unsupported action.

- **Operators**: FAR v0.8.0+
- **Cluster**: Any topology
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="far && disruption:nondestructive" --focus="unsupported action" ./tests/far-operator/...`
- **Pass criteria**: CR creation rejected with error containing "FAR doesn't support any other action than"

### 12. Verify Restriction of Fence Agents in FAR CR ([OCP-71219](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-71219))

Single test case covering two verifications (per the Polarion test plan), both in one `It` block so results map to one Polarion ID:
1. FAR CR with a fence agent name that passes the `fence_` prefix check but is not installed -- webhook rejects with "unsupported fence agent".
2. FAR CR with an agent name missing the `fence_` prefix -- CRD schema validation rejects it.

Both sub-cases run even if one fails (failures are collected and reported together).

- **Operators**: FAR v0.8.0+
- **Cluster**: Any topology
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="far && disruption:nondestructive" --focus="unsupported or invalid fence agent name" ./tests/far-operator/...`
- **Pass criteria**: unsupported-agent CR rejected with "unsupported fence agent"; invalid-prefix CR rejected with "spec.agent in body should match"

### 13. Verify Restriction of Fence Agents in FARTemplate ([OCP-71220](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-71220))

Single test case covering the same two verifications for `FenceAgentsRemediationTemplate`, both in one `It` block mapping to one Polarion ID:
1. FARTemplate with an unsupported (but correctly prefixed) fence agent -- webhook rejects with "unsupported fence agent".
2. FARTemplate with an agent name missing the `fence_` prefix -- CRD schema validation rejects it.

Both sub-cases run even if one fails (failures are collected and reported together).

- **Operators**: FAR v0.8.0+
- **Cluster**: Any topology
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="far && disruption:nondestructive" --focus="FARTemplate with unsupported or invalid" ./tests/far-operator/...`
- **Pass criteria**: unsupported-agent template rejected with "unsupported fence agent"; invalid-prefix template rejected with "spec.template.spec.agent in body should match"

## Destructive Tests

Tests that trigger node fencing via `fence_aws` and cause node reboots. Require AWS IPI cluster with 3+ worker nodes and AWS fencing credentials.

### 14. Verify Standalone FAR Remediation ([OCP-61229](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-61229))

Creates a FenceAgentsRemediation CR targeting a worker node. Validates that the fence agent reboots the node and the node object is preserved (not re-created).

- **Operators**: FAR v0.8.0+
- **Cluster**: AWS IPI, 3+ worker nodes
- **Storage**: None
- **Environment**: Connected
- **Standalone**: `ginkgo --label-filter="far && disruption:destructive" --focus="standalone FAR CR" ./tests/far-operator/...`
- **Pass criteria**: Node boot ID changes, node creation timestamp unchanged, node returns to Ready, FAR lifecycle events emitted on CR (RemediationStarted, FenceAgentSucceeded, RemediationFinished) and NodeRemediationCompleted event emitted on Node

### 15. Verify Remediation on Active Controller Node ([OCP-70638](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-70638))

Creates a FAR CR targeting the node hosting the active FAR controller pod. Validates that controller failover occurs and remediation completes despite the leader being fenced.

- **Operators**: FAR v0.8.0+
- **Cluster**: AWS IPI, 3+ worker nodes
- **Storage**: None
- **Environment**: Connected
- **Standalone**: `ginkgo --label-filter="far && disruption:destructive" --focus="active FAR controller" ./tests/far-operator/...`
- **Pass criteria**: Node reboots, node returns to Ready, FAR controller replicas recover, controller lease transfers to a different pod, workload pod evicted, FAR lifecycle events survive leader failover (RemediationStarted, FenceAgentSucceeded, RemediationFinished on CR; NodeRemediationCompleted on Node)

### 16. Verify FAR NoSchedule Taint During Remediation ([OCP-65960](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-65960))

Creates a FAR CR and verifies that the FAR NoSchedule taint is applied to the target node during the remediation process.

- **Operators**: FAR v0.8.0+
- **Cluster**: AWS IPI, 3+ worker nodes
- **Storage**: None
- **Environment**: Connected
- **Standalone**: `ginkgo --label-filter="far && disruption:destructive" --focus="NoSchedule taint" ./tests/far-operator/...`
- **Pass criteria**: FAR taint `remediation.medik8s.io/fence-agents-remediation:NoSchedule` applied during remediation

### 17. Verify FAR CR Status Conditions After Remediation ([OCP-67015](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-67015))

Creates a FAR CR and after remediation completes, verifies the CR status conditions match the expected terminal state: Processing=False, FenceAgentActionSucceeded=True, Succeeded=True.

- **Operators**: FAR v0.8.0+
- **Cluster**: AWS IPI, 3+ worker nodes
- **Storage**: None
- **Environment**: Connected
- **Standalone**: `ginkgo --label-filter="far && disruption:destructive" --focus="status conditions" ./tests/far-operator/...`
- **Pass criteria**: All three FAR CR conditions present with expected values

### 18. Verify FAR Default Reboot Action ([OCP-66203](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-66203))

Creates a FAR CR without the `--action` parameter in shared parameters. Validates that FAR defaults to the reboot action and the node is successfully rebooted.

- **Operators**: FAR v0.8.0+
- **Cluster**: AWS IPI, 3+ worker nodes
- **Storage**: None
- **Environment**: Connected
- **Standalone**: `ginkgo --label-filter="far && disruption:destructive" --focus="action is omitted" ./tests/far-operator/...`
- **Pass criteria**: Node reboots despite no explicit action parameter

### 19. Verify Controller Leadership Handover ([OCP-70636](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-70636))

Deletes the active FAR controller pod and validates that a new pod acquires the controller lease. This test does not fence any nodes; it verifies leader election recovery only.

- **Operators**: FAR v0.8.0+
- **Cluster**: Multi-node, 2+ controller replicas
- **Storage**: None
- **Environment**: Connected or disconnected
- **Standalone**: `ginkgo --label-filter="far" --focus="controller leadership" ./tests/far-operator/...`
- **Pass criteria**: FAR deployment becomes ready, controller lease is held by a different pod

### 20. Verify FAR Operator Survives OCP and Operator Upgrade ([OCP-89717](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-89717))

Validates the full customer upgrade path: install GA FAR from redhat-operators on OCP N-1, upgrade OCP to N, run remediation to confirm GA operator works on upgraded cluster, switch Subscription to Konflux FBC catalog (pre-GA), and run remediation again to confirm upgraded operator works. Each remediation cycle creates a workload pod on the target node, fences the node via fence_aws, and verifies node reboot, recovery, and workload eviction via OutOfServiceTaint.

- **Operators**: FAR GA (from redhat-operators) + FAR pre-GA (from Konflux FBC)
- **Cluster**: AWS IPI, 3+ worker nodes, OCP N-1 at start (upgraded to N during test)
- **Storage**: None
- **Environment**: Connected
- **Labels**: `tier:upgrade`, `disruption:destructive`, `platform:aws`, `frequency:weekly`, `component:olm`
- **Env vars (required)**: `OPENSHIFT_UPGRADE_RELEASE_IMAGE_OVERRIDE` (falls back to `RELEASE_IMAGE_LATEST` if unset)
- **CI prerequisite**: `medik8s-catalogsource` step must run before the test (creates the `medik8s-catalog` CatalogSource)
- **Env vars (optional, have defaults)**: `MEDIK8S_OPERATOR_PACKAGE` (default: `fence-agents-remediation`), `MEDIK8S_TARGET_CHANNEL` (default: `stable`)
- **Standalone**: `ginkgo --label-filter="far && tier:upgrade" ./tests/far-operator/...`
- **Pass criteria**: FAR deployment Ready on OCP N-1, OCP upgrade completes (Progressing=False, Available=True, Degraded=False), FAR deployment Ready after OCP upgrade, FAR CSV in Succeeded phase after catalog switch (new CSV if Konflux version is higher than GA, same CSV if versions match), controller image changes after operator upgrade (skipped on version parity), remediation succeeds after OCP upgrade and after catalog switch (node rebooted via boot ID change, node recovers to Ready), workload pods evicted after each fencing cycle

### 17. Remediate a Control Plane Node and Verify etcd Quorum Preservation ([OCP-90217](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-90217))

Fences any control plane node via fence_aws and verifies etcd quorum is preserved. Checks etcd ClusterOperator health before and after remediation. Creates a test workload pod pinned to the CP target node, then verifies it is evicted after fencing. Confirms the fenced CP node reboots (boot ID change), returns to Ready, the etcd ClusterOperator recovers (Available=True, Degraded=False), and the FAR CR reaches its terminal status conditions (Processing=False, FenceAgentActionSucceeded=True, Succeeded=True). FAR lifecycle Events are deliberately not asserted on a control-plane target: Kubernetes Events are best-effort and can be dropped during the apiserver/etcd disruption the CP reboot causes, so the durable CR status conditions prove the outcome instead (the full Event bundle is still asserted on the worker specs).

- **Operators**: FAR
- **Cluster**: AWS IPI, 3 control plane nodes (etcd quorum requires majority)
- **Storage**: None
- **Environment**: Connected
- **Labels**: `tier:acceptance`, `disruption:destructive`, `platform:aws`, `frequency:weekly`, `component:remediation`, `topology:control-plane`
- **Env vars (required)**: AWS credentials provisioned by the `medik8s-aws-credentials` CI step
- **Standalone**: `ginkgo --label-filter="far && topology:control-plane" ./tests/far-operator/...`
- **Pass criteria**: etcd ClusterOperator healthy before and after remediation, CP node rebooted (boot ID change), CP node returns to Ready, workload pod evicted from CP node, FAR CR status conditions Processing=False, FenceAgentActionSucceeded=True, Succeeded=True (FAR lifecycle Events are not asserted on a control-plane target - they are best-effort and can be dropped during the CP reboot's apiserver/etcd disruption)

### 18. Fence Leader and Complete FAR Remediation with Only 2 Schedulable Workers ([OCP-90218](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-90218))

Simulates a minimal 2-worker topology by cordoning extra workers, then fences the FAR leader node via fence_aws, forcing leader election failover to the surviving worker. Verifies the surviving worker takes over leadership and completes remediation despite degraded capacity (at least 1 FAR replica stays Running), workloads are evicted, and FAR recovers to 2 replicas once the cordoned capacity is restored (the fenced node stays NoSchedule-tainted until its CR is deleted, so the recovered replica lands on an uncordoned worker).

- **Operators**: FAR
- **Cluster**: AWS IPI, 2+ worker nodes (extra workers are cordoned to simulate topology)
- **Storage**: None
- **Environment**: Connected
- **Labels**: `tier:acceptance`, `disruption:destructive`, `platform:aws`, `frequency:weekly`, `component:remediation`, `topology:minimal-worker`
- **Env vars (required)**: AWS credentials provisioned by the `medik8s-aws-credentials` CI step
- **Standalone**: `ginkgo --label-filter="far && topology:minimal-worker" ./tests/far-operator/...`
- **Pass criteria**: At least 1 FAR replica Running during degraded capacity, leader node rebooted (boot ID change), leader node returns to Ready, workload pod evicted, FAR recovers to 2 replicas after schedulable capacity is restored

### 19. Verify FAR Deployment Unavailability with Zero Schedulable Workers ([OCP-90308](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-90308))

Validates FAR operator behavior when all worker nodes are cordoned, simulating a 0-worker topology. Cordons all workers, deletes FAR pods to force reschedule attempts, verifies the deployment reports 0 Ready replicas, then uncordons workers and verifies FAR recovers to full replica count.

- **Operators**: FAR
- **Cluster**: AWS IPI, 1+ worker nodes (all cordoned to simulate 0-worker topology)
- **Storage**: None
- **Environment**: Connected
- **Labels**: `tier:acceptance`, `disruption:destructive`, `platform:aws`, `frequency:weekly`, `component:controller`, `topology:zero-worker`
- **Env vars (required)**: AWS credentials provisioned by the `medik8s-aws-credentials` CI step
- **Standalone**: `ginkgo --label-filter="far && topology:zero-worker" ./tests/far-operator/...`
- **Pass criteria**: FAR deployment Ready before test, FAR deployment has 0 Ready replicas after pods deleted on cordoned workers, FAR deployment recovers to 2 Ready replicas after uncordoning

### 20. Verify FAR Must-Gather Collection During Active Remediation ([OCP-61480](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-61480))

Triggers a FAR remediation on a non-leader worker node, waits for the Processing condition, then runs `oc adm must-gather` with the medik8s image and validates the collected output. Confirms the must-gather image captures FAR operator data while a remediation is actively in progress.

- **Operators**: FAR v0.8.0+
- **Cluster**: AWS, 3+ Ready worker nodes
- **Storage**: None
- **Environment**: Connected (must-gather image must be pullable)
- **Labels**: `disruption:destructive`, `platform:aws`, `component:controller`, `tier:acceptance`, `frequency:weekly`
- **Env vars (optional, has default)**: `MUST_GATHER_IMAGE` (overrides the default medik8s must-gather image)
- **Standalone**: `ginkgo --label-filter="far" --focus="must-gather during active remediation" ./tests/far-operator/...`
- **Pass criteria**: must-gather output contains a node YAML for every cluster node, FAR CRD definitions, and operator namespace resources; the fenced node reboots (boot ID changes) and returns Ready

### 21. Verify FAR Timed-Out Remediation Retry Logging ([OCP-70873](https://polarion.engineering.redhat.com/polarion/#/project/OSE/workitem?id=OCP-70873))

Deletes the FAR controller pods to isolate logs, then creates a FAR CR with an invalid AWS instance ID so every fence attempt fails, and verifies the active controller logs one failure entry per retry. Confirms the retry mechanism logs failure messages matching the configured retry count.

- **Operators**: FAR v0.8.0+
- **Cluster**: AWS, 3+ Ready worker nodes
- **Storage**: None
- **Environment**: Connected
- **Labels**: `disruption:nondestructive`, `platform:aws`, `component:remediation`, `tier:acceptance`, `frequency:weekly`
- **Standalone**: `ginkgo --label-filter="far" --focus="timeout messages matching retry count" ./tests/far-operator/...`
- **Pass criteria**: the active FAR controller log contains exactly 10 (`FARCRRetryCount`) `command failed` entries, one per retry
