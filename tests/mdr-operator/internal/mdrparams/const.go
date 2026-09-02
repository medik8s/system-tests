package mdrparams

import "time"

const (
	// Label is the operator name used in the suite-level Labels array.
	Label = "mdr"
	// DefaultPollInterval is the polling interval used with Eventually calls.
	DefaultPollInterval = 5 * time.Second

	// ExpectedReplicas defines the expected number of replicas for MDR controller manager.
	// MDR runs a single replica (unlike FAR which runs 2 for HA).
	ExpectedReplicas = int32(1)

	// ManagerContainerName is the name of the main controller container in the MDR pod.
	ManagerContainerName = "manager"

	// CRDGroup is the Kubernetes API group for all MDR custom resources.
	CRDGroup = "machine-deletion-remediation.medik8s.io"

	// CRDVersion is the API version for all MDR custom resources.
	CRDVersion = "v1alpha1"

	// CSVNamePattern is the substring used to match the MDR operator ClusterServiceVersion by name.
	CSVNamePattern = "machine-deletion-remediation"

	// Remediation test constants (RHWA-1248)

	// NHCCRDName is the CRD name for NodeHealthCheck, used to detect if NHC is installed.
	NHCCRDName = "nodehealthchecks.remediation.medik8s.io"

	// NHCAPIGroup is the API group for NodeHealthCheck CRs.
	NHCAPIGroup = "remediation.medik8s.io"

	// NHCAPIVersion is the API version for NodeHealthCheck CRs.
	NHCAPIVersion = "v1alpha1"

	// NHCTestName is the NodeHealthCheck CR name for the MDR remediation test.
	NHCTestName = "mdr-test-nhc-workers"

	// MDRTestTemplateName is the name of the MDRT created for remediation tests.
	MDRTestTemplateName = "mdr-test-template"

	// OcDebugTimeout is the maximum time to wait for oc debug commands.
	OcDebugTimeout = 5 * time.Minute

	// NodeNotReadyTimeout is the maximum time to wait for a node to become NotReady.
	NodeNotReadyTimeout = 5 * time.Minute

	// RemediationCompleteTimeout is the maximum time to wait for MDR remediation
	// to complete (Machine deleted + new VM provisioned + node joins Ready).
	// AWS VM provisioning takes ~15-25 min.
	RemediationCompleteTimeout = 35 * time.Minute

	// NodeReadyTimeout is the maximum time to wait for a node to become Ready.
	NodeReadyTimeout = 15 * time.Minute

	// RemediationCRDeletionTimeout is the maximum time to wait for an MDR CR to be deleted.
	RemediationCRDeletionTimeout = 5 * time.Minute

	// ProcessingConditionType is the MDR condition set when remediation is in progress.
	ProcessingConditionType = "Processing"

	// SucceededConditionType is the MDR condition set when remediation completes.
	SucceededConditionType = "Succeeded"

	// ConditionReasonRemediationStarted is the reason set on Processing and Succeeded
	// conditions when remediation begins.
	ConditionReasonRemediationStarted = "RemediationStarted"

	// Condition test constants (RHWA-1249).

	// ConditionReasonStoppedByNHC is the reason set when NHC timed-out annotation is present.
	ConditionReasonStoppedByNHC = "RemediationStoppedByNHC"

	// ConditionReasonNodeNotFound is the reason set when the target node does not exist.
	ConditionReasonNodeNotFound = "RemediationCannotStartNodeNotFound"

	// ConditionReasonNoControllerOwner is the reason set when the target node has no controller owner.
	ConditionReasonNoControllerOwner = "RemediationCannotStartNoControllerOwner"

	// PermanentNodeDeletionExpectedConditionType is the condition indicating node name persistence.
	PermanentNodeDeletionExpectedConditionType = "PermanentNodeDeletionExpected"

	// ConditionReasonKeepsNodeName is the reason for baremetal clusters where the node name is preserved.
	ConditionReasonKeepsNodeName = "MachineDeletionOnBareMetalProviderKeepsNodeName"

	// ConditionMessageKeepsNodeName is the expected message for baremetal PermanentNodeDeletionExpected.
	ConditionMessageKeepsNodeName = "Machine will be deleted as part of remediation. " +
		"This is a BareMetal cluster provider: the new node is NOT expected to have a new name"

	// ConditionStatusFalse is the status value "False" for condition checks.
	ConditionStatusFalse = "False"

	// NHCTimedOutAnnotationKey is the annotation that signals NHC timed out.
	NHCTimedOutAnnotationKey = "remediation.medik8s.io/nhc-timed-out"

	// NHCTimedOutAnnotationValue is an RFC3339 timestamp for the annotation.
	// The MDR controller checks only for key presence, not value format.
	NHCTimedOutAnnotationValue = "2024-01-01T00:00:00Z"

	// MDRRemStoppedLogMsg is the log message emitted when MDR stops due to NHC timeout.
	MDRRemStoppedLogMsg = "NHC time out annotation found, stopping remediation"

	// MDRConditionTestName is the MDR CR name for the NHC timed-out condition test.
	MDRConditionTestName = "mdr-test-timeout-annotation"

	// MDRNonExistentNodeTestName is the MDR CR name for the non-existent node test.
	MDRNonExistentNodeTestName = "non-existent-node"

	// ControllerLogWindow is the time window for searching controller logs.
	ControllerLogWindow = 5 * time.Minute

	// Node role labels (mirrors medik8s/common/pkg/labels).

	// MasterRoleLabel is the old role label for control-plane nodes.
	MasterRoleLabel = "node-role.kubernetes.io/master"

	// ControlPlaneRoleLabel is the new role label for control-plane nodes.
	ControlPlaneRoleLabel = "node-role.kubernetes.io/control-plane"

	// WorkerRoleLabel is the role label for worker nodes.
	WorkerRoleLabel = "node-role.kubernetes.io/worker"

	// MDRTNegativeTestName is the MDRT name used in negative validation tests.
	MDRTNegativeTestName = "mdr-negative-test-template"

	// MDRTInvalidTestName is a name that violates RFC 1123 subdomain rules.
	MDRTInvalidTestName = "-1-invalid-value"

	// MDRTInvalidTestNamespace is a syntactically valid namespace that does not exist.
	MDRTInvalidTestNamespace = "mdr-test-nonexistent-ns"
)
