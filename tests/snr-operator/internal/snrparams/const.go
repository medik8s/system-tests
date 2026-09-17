package snrparams

import (
	"time"

	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
)

const (
	// Label is the operator name used in the suite-level Labels array.
	Label = "snr"
	// DefaultPollInterval is the polling interval used with Eventually calls.
	DefaultPollInterval = 5 * time.Second

	// ExpectedReplicas defines the expected number of replicas for SNR controller manager.
	ExpectedReplicas = int32(2)

	// ManagerContainerName is the name of the main controller container in the SNR pod.
	ManagerContainerName = "manager"

	// CRDGroup is the Kubernetes API group for all SNR custom resources.
	CRDGroup = "self-node-remediation.medik8s.io"

	// CRDVersion is the API version for all SNR custom resources.
	CRDVersion = "v1alpha1"

	// SNRConfigName is the name of the default SelfNodeRemediationConfig CR.
	SNRConfigName = "self-node-remediation-config"

	// SNRTemplateName is the name of the default automatic strategy template.
	SNRTemplateName = "self-node-remediation-automatic-strategy-template"

	// SNRTemplateExpectedStrategy is the expected remediation strategy for the automatic template.
	SNRTemplateExpectedStrategy = "Automatic"

	// CSVNamePattern is the CSV name pattern used to find the SNR ClusterServiceVersion.
	CSVNamePattern = "self-node-remediation"

	// SNRCRDName is the full CRD name for SelfNodeRemediationConfig.
	SNRCRDName = "selfnoderemediationconfigs.self-node-remediation.medik8s.io"

	// SafeTimeToAssumeNodeRebootedDescription is the expected description
	// text substring for the safeTimeToAssumeNodeRebootedSeconds CRD field.
	SafeTimeToAssumeNodeRebootedDescription = "SafeTimeToAssumeNodeRebootedSeconds " +
		"is the time after which the healthy self node remediation"

	// SNRCNonDefaultErrMsg is the expected error when creating a non-default SNRC.
	SNRCNonDefaultErrMsg = "to enforce only one SelfNodeRemediationConfig " +
		"in the cluster, a name other than " +
		"self-node-remediation-config is not allowed"

	// SNRTestNodeName is the fake node name used in negative tests.
	SNRTestNodeName = "incorrect-node-name"

	// SNRCTestName is the name used for test SNRC CRs.
	SNRCTestName = "test-snrc"

	// DSPodRestartTimeout is the time allowed for SNR DaemonSet pods to
	// restart or terminate after a config change.
	DSPodRestartTimeout = 15 * time.Minute

	// SoftdogAutoDetectMessage is the log message emitted by the SNR agent
	// when it auto-detects the softdog watchdog path after an invalid path is configured.
	SoftdogAutoDetectMessage = "auto detected softdog path"

	// SNRReasonConfigNotFound is the status condition reason when SNRC is missing.
	SNRReasonConfigNotFound = "ConfigurationNotFound"

	// SNRMessageConfigNotFound is the status condition message when SNRC is missing.
	SNRMessageConfigNotFound = "SelfNodeRemediation is disabled because configuration does not exist"

	// --- Destructive (remediation) test constants ---.

	// OcDebugTimeout is the timeout for oc debug node/ commands.
	// 5 minutes to allow for slow debug pod scheduling on ARM64/nested virt.
	OcDebugTimeout = 5 * time.Minute

	// SNRDeletionTimeout is how long to wait for the SNR CR to be deleted
	// after remediation completes. This is the longest wait in the
	// remediation cycle -- the Python tests use 800s.
	SNRDeletionTimeout = 15 * time.Minute

	// NodeReadyTimeout is how long to wait for a node to return to Ready
	// after reboot.
	NodeReadyTimeout = 15 * time.Minute

	// RemediationCRDeletionTimeout is how long to wait for a CR to be
	// fully deleted during cleanup (retry-safe deletion). NHC CRs can
	// take several minutes to delete due to webhook finalizer processing.
	RemediationCRDeletionTimeout = 5 * time.Minute

	// WorkloadPodReadyTimeout is how long to wait for a test workload pod
	// to reach Running phase.
	WorkloadPodReadyTimeout = 2 * time.Minute

	// WorkloadEvictionTimeout is how long to wait for workload pods to be
	// evicted or rescheduled after remediation.
	WorkloadEvictionTimeout = 5 * time.Minute

	// NHCCRDName is the CRD name for NodeHealthCheck, used to detect if
	// NHC is installed.
	NHCCRDName = "nodehealthchecks.remediation.medik8s.io"

	// NHCAPIGroup is the API group for NodeHealthCheck CRs.
	NHCAPIGroup = "remediation.medik8s.io"

	// NHCAPIVersion is the API version for NodeHealthCheck CRs.
	NHCAPIVersion = "v1alpha1"

	// NHCTestName is the name used for test NHC CRs targeting workers.
	NHCTestName = "snr-test-nhc-workers"

	// NHCMasterTestName is the name used for test NHC CRs targeting masters.
	NHCMasterTestName = "snr-test-nhc-masters"

	// SNRTResourceDeletionName is the name for the ResourceDeletion strategy SNRT.
	SNRTResourceDeletionName = "snr-test-resource-deletion-template"

	// SNRTOutOfServiceTaintName is the name for the OutOfServiceTaint strategy SNRT.
	SNRTOutOfServiceTaintName = "snr-test-out-of-service-taint-template"

	// OutOfServiceTaintKey is the taint key applied by SNR when using the
	// OutOfServiceTaint remediation strategy (standard K8s taint).
	OutOfServiceTaintKey = "node.kubernetes.io/out-of-service"

	// MinReadyMasterNodes is the minimum number of Ready master nodes
	// required for master remediation tests (etcd quorum safety).
	MinReadyMasterNodes = 3
)

// WorkloadTestImage is the container image used for test workload pods.
var WorkloadTestImage = medik8sparams.WorkloadImage
