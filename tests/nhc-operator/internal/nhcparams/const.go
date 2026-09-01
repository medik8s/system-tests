package nhcparams

import "time"

const (
	// Label is the operator name used in the suite-level Labels array.
	Label = "nhc"
	// DefaultPollInterval is the polling interval used with Eventually/Consistently calls.
	DefaultPollInterval = 5 * time.Second

	// DestructivePollInterval is a longer polling interval for destructive tests
	// where rapid polling adds API load without benefit (e.g. waiting for node reboot).
	DestructivePollInterval = 10 * time.Second

	// ExpectedReplicas defines the expected number of replicas for NHC controller manager.
	ExpectedReplicas = int32(2)

	// CRDGroup is the Kubernetes API group for NHC custom resources.
	CRDGroup = "remediation.medik8s.io"

	// CRDVersion is the API version for NHC custom resources.
	CRDVersion = "v1alpha1"

	// CSVNamePattern is the CSV name pattern used to find the NHC ClusterServiceVersion.
	CSVNamePattern = "node-healthcheck-operator"

	// ManagerContainerName is the name of the main controller container in the NHC pod.
	ManagerContainerName = "manager"

	// Remediation trigger test constants (RHWA-1243).

	// SNRCRDGroup is the API group for SNR custom resources (used as remediator).
	SNRCRDGroup = "self-node-remediation.medik8s.io"

	// SNRCRDVersion is the API version for SNR custom resources.
	SNRCRDVersion = "v1alpha1"

	// SNRTemplateKind is the Kind string for SelfNodeRemediationTemplate CRs.
	SNRTemplateKind = "SelfNodeRemediationTemplate"

	// SNRTemplateName is the default SNR template name deployed by the operator.
	SNRTemplateName = "self-node-remediation-automatic-strategy-template"

	// NHCTestName is the NHC CR name used in remediation trigger tests.
	// In multi-CR tests, this is the slower/standard-duration NHC.
	// Named "nhc-test-b-*" so it sorts AFTER the short-duration NHC
	// alphabetically, ensuring the NHC controller processes the
	// short-duration CR first.
	NHCTestName = "nhc-test-b-standard"

	// NHCSecondTestName is the short-duration NHC CR used in multi-CR tests.
	// Named "nhc-test-a-*" so it sorts BEFORE the standard NHC.
	NHCSecondTestName = "nhc-test-a-short"

	// NHCOldDefaultName is the legacy default NHC CR name (pre-rename).
	NHCOldDefaultName = "nhc-worker-default"

	// NHCControlPlaneTestName is the NHC CR name for control-plane monitoring.
	NHCControlPlaneTestName = "nhc-cp"

	// SSHTimeout is the maximum time to wait for SSH commands on nodes.
	// SSH is used instead of oc debug for kubelet stop/start because
	// oc debug cannot schedule pods when kubelet is stopped.
	SSHTimeout = 30 * time.Second

	// OCDebugKubeletStopTimeout is the maximum time to wait for the "oc debug"
	// kubelet-stop command used
	OCDebugKubeletStopTimeout = 5 * time.Minute

	// LeaderAcquisitionTimeout is the maximum time to wait for the NHC
	// controller's leader-election Lease to point at a pod that exists.
	LeaderAcquisitionTimeout = 1 * time.Minute

	// NodeNotReadyTimeout is the maximum time to wait for NHC to detect an
	// unhealthy node and enter Remediating. Includes SSH timeout (30s)
	// + NHC unhealthy condition duration (60s) + detection lag.
	NodeNotReadyTimeout = 3 * time.Minute

	// RemediationCompletionTimeout is the maximum time to wait for a full
	// remediation cycle: SNR CR deletion, node reboot, NHC phase recovery,
	// or controller failover.
	RemediationCompletionTimeout = 15 * time.Minute

	// NodeReadyTimeout is the maximum time to wait for a node to become Ready.
	NodeReadyTimeout = 15 * time.Minute

	// RemediationCRDeletionTimeout is the timeout for retry-safe CR deletion.
	RemediationCRDeletionTimeout = 5 * time.Minute

	// NHCPhaseEnabled is the NHC status phase when healthy.
	NHCPhaseEnabled = "Enabled"

	// NHCPhaseRemediating is the NHC status phase during active remediation.
	NHCPhaseRemediating = "Remediating"

	// NegativeAssertionHoldDuration is how long Consistently polls to verify
	// a negative assertion holds (e.g. "SNR CR should NOT be created").
	NegativeAssertionHoldDuration = 30 * time.Second

	// SNRCRDName is the CRD name for SelfNodeRemediation, used to detect if SNR is installed.
	SNRCRDName = "selfnoderemediations.self-node-remediation.medik8s.io"

	// TestRemediation dummy CRD constants (for multi-NHC tests).

	// TestRemediationGroup is the API group for the dummy TestRemediation CRDs.
	TestRemediationGroup = "test.medik8s.io"

	// TestRemediationVersion is the API version for TestRemediation CRDs.
	TestRemediationVersion = "v1alpha1"

	// TestRemediationTemplateCRDName is the CRD name for TestRemediationTemplate.
	TestRemediationTemplateCRDName = "testremediationtemplates.test.medik8s.io"

	// TestRemediationCRDName is the CRD name for TestRemediation.
	TestRemediationCRDName = "testremediations.test.medik8s.io"

	// TestRemediationTemplateName is the name of the TestRemediationTemplate CR.
	TestRemediationTemplateName = "test-remediation-template"

	// TestRemediationClusterRoleName is the ClusterRole granting NHC access to TestRemediation.
	TestRemediationClusterRoleName = "test-remediation-cluster-role"

	// TestRemediationClusterRoleBindingName is the ClusterRoleBinding for the above role.
	TestRemediationClusterRoleBindingName = "test-remediation-binding"

	// NHCControllerServiceAccount is the NHC controller's ServiceAccount name.
	NHCControllerServiceAccount = "node-healthcheck-controller-manager"

	// ControllerLeaseName is the NHC leader election lease name (LeaderElectionID in cmd/main.go).
	ControllerLeaseName = "e1f13584.medik8s.io"

	// Negative validation test constants (RHWA-1244).

	// NHCPhaseDisabled is the NHC status phase when the template is invalid or missing.
	NHCPhaseDisabled = "Disabled"

	// NHCReasonTemplateNotFound is the NHC status reason when the remediation template is missing.
	NHCReasonTemplateNotFound = "RemediationTemplateNotFound"

	// NHCDuplicateTestName is the NHC CR name used by the duplicate-name test.
	NHCDuplicateTestName = "nhc-test-duplicate"

	// NHCIncorrectTemplateTestName is the NHC CR name for the non-existent template test.
	NHCIncorrectTemplateTestName = "nhc-test-incorrect-template"

	// NHCInvalidValuesTestName is the NHC CR name for the invalid-values test.
	NHCInvalidValuesTestName = "nhc-test-invalid-values"

	// NHCMissingNsTestName is the NHC CR name for the missing-namespace test.
	NHCMissingNsTestName = "nhc-test-missing-ns"

	// NHCEmptySelectorTestName is the NHC CR name for the empty-selector test.
	NHCEmptySelectorTestName = "nhc-test-empty-selector"

	// NHCZeroHealthyTestName is the NHC CR name for the zero-healthy-nodes test.
	NHCZeroHealthyTestName = "nhc-test-zero-healthy"

	// Template management test constants (RHWA-1246).

	// NHCTemplateWatchTestName is the NHC CR name for the template-watch test.
	NHCTemplateWatchTestName = "nhc-test-template-watch"

	// NHCCustomTemplateTestName is the NHC CR name for the custom TestRemediationTemplate test.
	NHCCustomTemplateTestName = "nhc-test-custom-template"

	// SNRTTestName is the SNRT CR name created for template-watch tests.
	SNRTTestName = "snrt-test-sample"

	// Status field test constants (RHWA-1247).

	// NHCStatusTestName is the NHC CR name for the status field tracking test.
	NHCStatusTestName = "nhc-test-status-field"

	// NHCReasonEnabled is the expected reason substring when NHC is healthy.
	NHCReasonEnabled = "no ongoing remediation"

	// NHCReasonRemediating is the expected reason substring during active remediation.
	NHCReasonRemediating = "remediating"
)
