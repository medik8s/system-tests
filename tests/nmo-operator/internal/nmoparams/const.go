package nmoparams

import "time"

const (
	// Label is the operator name used in the suite-level Labels array.
	Label = "nmo"
	// DefaultPollInterval is the polling interval used with Eventually calls.
	DefaultPollInterval = 5 * time.Second

	// ExpectedReplicas defines the expected number of replicas for NMO controller manager.
	// NMO always runs a single replica regardless of cluster topology (MNO or SNO).
	ExpectedReplicas = int32(1)

	// ManagerContainerName is the name of the main controller container in the NMO pod.
	ManagerContainerName = "manager"

	// OperatorDeploymentName is the name of the NMO operator controller manager deployment.
	OperatorDeploymentName = "node-maintenance-operator-controller-manager"

	// OperatorControllerPodLabelSelector is the label selector string to filter NMO controller pods.
	// NMO's upstream pod template only defines control-plane=controller-manager (no app.kubernetes.io/name).
	OperatorControllerPodLabelSelector = "control-plane=controller-manager"

	// CSVNamePattern is the substring used to match the NMO operator ClusterServiceVersion by name.
	CSVNamePattern = "node-maintenance-operator"

	// MaintenanceTimeout is the maximum wait for a NodeMaintenance CR to reach Succeeded phase.
	MaintenanceTimeout = 5 * time.Minute
	// RebootTimeout is the maximum wait for a node to recover after reboot.
	RebootTimeout = 10 * time.Minute
	// UncordonTimeout is the maximum wait for a node to become schedulable after maintenance ends.
	UncordonTimeout = 2 * time.Minute
	// ScheduleCheckTimeout is the timeout for verifying pod scheduling behavior on cordoned nodes.
	ScheduleCheckTimeout = 30 * time.Second
	// RunOnNodeTimeout is the timeout for running commands on a node via oc debug.
	RunOnNodeTimeout = 2 * time.Minute

	// DrainProgressComplete is the DrainProgress value indicating all pods have been evicted.
	DrainProgressComplete = 100

	// DrainTaintKey is the taint key applied by NMO to nodes under maintenance.
	DrainTaintKey = "medik8s.io/drain"

	// LeaseNamespace is the namespace where NMO creates maintenance leases (medik8s/common default).
	LeaseNamespace = "medik8s-leases"
	// LeaseHolderIdentity is the identity string set on maintenance leases by the NMO controller.
	LeaseHolderIdentity = "node-maintenance"
	// LeaseDurationSeconds is the expected lease duration set by the NMO controller.
	LeaseDurationSeconds = int32(3600)

	// EventReasonBeginMaintenance is emitted when a NodeMaintenance CR is created.
	EventReasonBeginMaintenance = "BeginMaintenance"
	// EventReasonSucceedMaintenance is emitted when drain completes successfully.
	EventReasonSucceedMaintenance = "SucceedMaintenance"
	// EventReasonRemovedMaintenance is emitted when a NodeMaintenance CR is deleted.
	EventReasonRemovedMaintenance = "RemovedMaintenance"

	// EventTimeout is the maximum wait for a Kubernetes event to appear.
	EventTimeout = 3 * time.Minute
	// LeaseTimeout is the maximum wait for a maintenance lease to appear or be deleted.
	LeaseTimeout = 1 * time.Minute

	// EtcdNamespace is the namespace holding the etcd quorum PodDisruptionBudget.
	// The NMO webhook lists the single PDB here to assess control-plane quorum.
	EtcdNamespace = "openshift-etcd"
	// MinControlPlaneNodesForQuorum is the minimum number of control-plane nodes
	// required to exercise the etcd-quorum guard (a real quorum needs 3 masters).
	MinControlPlaneNodesForQuorum = 3
	// ExpectedQuorumDisruptions is the etcd PDB DisruptionsAllowed value the master
	// quorum test requires before starting: exactly one, i.e. a 3-member etcd control
	// plane that tolerates a single disruption. Larger etcd (5 members) tolerates more,
	// so a single maintenance would not exhaust quorum and the test does not apply.
	ExpectedQuorumDisruptions = int32(1)
	// MasterMaintenanceTimeout is the maximum wait for a control-plane NodeMaintenance
	// to reach Succeeded. Control-plane drains are slower than worker drains.
	MasterMaintenanceTimeout = 10 * time.Minute
	// QuorumUpdateTimeout is the maximum wait for the etcd PDB to reflect that a
	// control-plane node under maintenance has consumed the allowed disruption.
	QuorumUpdateTimeout = 3 * time.Minute
	// WebhookMsgQuorumViolation is the substring emitted by the NMO admission
	// webhook when a second control-plane NodeMaintenance would violate etcd quorum.
	// Verified against node-maintenance-operator errorControlPlaneQuorumViolation,
	// identical from tag v0.18.0 through the pinned v0.21.0 (see go.mod).
	WebhookMsgQuorumViolation = "will violate etcd quorum"

	// MinWorkerNodesForMaintenance is the minimum number of schedulable worker nodes
	// required by the destructive collision tests: one node is put under real
	// maintenance while at least one other remains available (for cluster health and,
	// in the name-duplication test, as the distinct target of the rejected second CR).
	MinWorkerNodesForMaintenance = 2

	// DuplicateNMName is the NodeMaintenance CR name reused by the name-duplication
	// collision test (OCP-29632); both the first CR and the rejected second CR share it.
	DuplicateNMName = "node-maintenance-test"
	// FirstNMName is the first NodeMaintenance CR name in the same-node collision test (OCP-29630).
	FirstNMName = "first-node-maintenance"
	// SecondNMName is the rejected second NodeMaintenance CR name in the same-node collision test (OCP-29630).
	SecondNMName = "second-node-maintenance"

	// CollisionReason is the spec.reason set on NodeMaintenance CRs created by the collision tests.
	CollisionReason = "system-tests collision validation (RHWA-1251)"

	// NMResourceQualified is the resource.group identifier the API server uses in
	// AlreadyExists errors for NodeMaintenance CRs (`<resource> "<name>" already exists`).
	NMResourceQualified = "nodemaintenances.nodemaintenance.medik8s.io"
)
