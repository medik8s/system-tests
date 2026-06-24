package sbrparams

import (
	"os"
	"time"
)

const (
	// DefaultPollInterval is the polling interval used with Eventually calls.
	DefaultPollInterval = 5 * time.Second

	// NoNewDaemonSetCheckDuration is how long the controller is observed to confirm no unexpected DaemonSet is created.
	NoNewDaemonSetCheckDuration = 30 * time.Second

	// NoNewDaemonSetCheckInterval is the polling interval used with NoNewDaemonSetCheckDuration.
	NoNewDaemonSetCheckInterval = 5 * time.Second

	// Label represents SBR operator label that can be used for test cases selection.
	Label = "sbr"

	// ExpectedReplicas defines the expected number of replicas for SBR controller manager.
	ExpectedReplicas = int32(2)

	// ManagerContainerName is the name of the main controller container in the SBR pod.
	ManagerContainerName = "manager"

	// CRDGroup is the Kubernetes API group for all SBR custom resources.
	CRDGroup = "storage-based-remediation.medik8s.io"

	// CRDVersion is the API version for all SBR custom resources.
	CRDVersion = "v1alpha1"

	// SBRCTimeoutSecondsMin is the minimum allowed value for sbrTimeoutSeconds (CRD schema enforced).
	SBRCTimeoutSecondsMin = 10

	// SBRCTimeoutSecondsMax is the maximum allowed value for sbrTimeoutSeconds (CRD schema enforced).
	SBRCTimeoutSecondsMax = 300

	// SBRCMaxConsecutiveFailuresMin is the minimum allowed value for maxConsecutiveFailures (CRD schema enforced).
	SBRCMaxConsecutiveFailuresMin = 2

	// SBRCMaxConsecutiveFailuresMax is the maximum allowed value for maxConsecutiveFailures (CRD schema enforced).
	SBRCMaxConsecutiveFailuresMax = 32

	// SBRCInvalidTestName is the name used for short-lived invalid StorageBasedRemediationConfig CRs in negative tests.
	SBRCInvalidTestName = "test-invalid-sbrc"

	// SBRCControllerTestName is the name used for StorageBasedRemediationConfig CRs testing controller-layer validation.
	SBRCControllerTestName = "test-controller-invalid-sbrc"

	// SBRCWatchdogTestName is the name used for StorageBasedRemediationConfig CRs testing invalid watchdog path handling.
	SBRCWatchdogTestName = "test-sbrc-watchdog-neg"

	// SBRCNoMatchSelectorTestName is the name used for StorageBasedRemediationConfig CRs
	// testing non-matching nodeSelector handling.
	SBRCNoMatchSelectorTestName = "test-sbrc-selector-neg"

	// SBRCInvalidWatchdogPath is a watchdog device path guaranteed not to exist on test nodes.
	SBRCInvalidWatchdogPath = "/dev/sbr-test-nonexistent-watchdog"

	// SBRCNoMatchSelectorKey is the node label key used to create a StorageBasedRemediationConfig
	// nodeSelector that matches no nodes.
	SBRCNoMatchSelectorKey = "sbr-test-no-match-selector"

	// SBRCNoMatchSelectorValue is the node label value used to create a StorageBasedRemediationConfig
	// nodeSelector that matches no nodes.
	SBRCNoMatchSelectorValue = "sbr-test-no-match-value"

	// OperatorDeploymentName is the name of the SBR operator controller manager deployment.
	OperatorDeploymentName = "sbr-operator-controller-manager"

	// OperatorControllerPodLabel is the app label value used to select SBR controller pods.
	OperatorControllerPodLabel = "sbr-operator"

	// OperatorControllerPodLabelSelector is the label selector string to filter SBR controller pods,
	// excluding device-init and agent pods that share the app.kubernetes.io/name label.
	OperatorControllerPodLabelSelector = "app.kubernetes.io/name=" + OperatorControllerPodLabel +
		",control-plane=controller-manager"

	// CSVNamePattern is the substring used to match the SBR operator ClusterServiceVersion by name.
	CSVNamePattern = "storage-based-remediation"

	// SBRRemediationFinalizer is the finalizer the controller adds to every StorageBasedRemediation CR.
	// Sourced from internal/controller/storagebasedremediation_controller.go.
	SBRRemediationFinalizer = "medik8s.io/sbr-remediation-finalizer"

	// SBRCFunctionalTestName is the name of the temporary SBRC created in functional tests
	// that need agent DaemonSet pods to process StorageBasedRemediation CRs.
	// The SBRRemediationReconciler runs inside agent pods, so an SBRC (and its DaemonSet) must
	// exist before any StorageBasedRemediation CR can be reconciled.
	SBRCFunctionalTestName = "test-sbrc-functional"

	// SBRAgentDaemonSetPrefix is the naming prefix the SBRC controller uses for agent DaemonSets.
	// DaemonSet name = SBRAgentDaemonSetPrefix + <sbrcName>.
	SBRAgentDaemonSetPrefix = "sbr-agent-"

	// SBRCReadyTimeout is the time allowed for the SBRC's agent DaemonSet to reach at least one
	// ready pod before a functional test begins.
	SBRCReadyTimeout = 3 * time.Minute

	// SBRStorageUnhealthyCondition is the node condition type set by the SBR agent when storage is unavailable.
	SBRStorageUnhealthyCondition = "SBRStorageUnhealthy"

	// StorageInjectionTimeout is how long to wait for SBRStorageUnhealthy=True after injecting faults.
	StorageInjectionTimeout = 3 * time.Minute

	// StorageInjectionPollInterval is the polling interval while waiting for the storage unhealthy condition.
	StorageInjectionPollInterval = 10 * time.Second

	// NHCCRDName is the CRD name for NodeHealthCheck, used to detect if NHC is installed.
	NHCCRDName = "nodehealthchecks.remediation.medik8s.io"

	// NHCAPIGroup is the API group for NodeHealthCheck CRs.
	NHCAPIGroup = "remediation.medik8s.io"

	// NHCAPIVersion is the API version for NodeHealthCheck CRs.
	NHCAPIVersion = "v1alpha1"

	// SBRTemplateName is the name of the StorageBasedRemediationTemplate that NHC references.
	SBRTemplateName = "storagebasedremediationtemplate-sample"

	// NHCUnhealthyDuration is how long a node must hold SBRStorageUnhealthy=True before NHC acts.
	NHCUnhealthyDuration = "30s"

	// NHCSplitBrainTestName is the NodeHealthCheck CR name used for the split-brain test.
	NHCSplitBrainTestName = "test-nhc-sbr-split-brain"

	// SBRCSplitBrainTestName is the SBRC name used in the split-brain test.
	SBRCSplitBrainTestName = "test-sbrc-split-brain"

	// NHCSBRCRCreationTimeout is how long to wait for NHC to create the StorageBasedRemediation CR.
	NHCSBRCRCreationTimeout = 5 * time.Minute

	// NHCSBRCRCreationPollInterval is the poll interval while waiting for NHC to create the SBR CR.
	NHCSBRCRCreationPollInterval = 10 * time.Second

	// NodeRebootTimeout is the maximum time to wait for a node to complete a full reboot cycle.
	NodeRebootTimeout = 20 * time.Minute

	// NodeRebootPollInterval is the polling interval while waiting for a node to become NotReady then Ready.
	NodeRebootPollInterval = 15 * time.Second

	// SplitBrainHealthyNodeCheckDuration is how long to Consistently assert no SBR CR exists for healthy nodes.
	SplitBrainHealthyNodeCheckDuration = 60 * time.Second

	// SplitBrainHealthyNodeCheckInterval is the poll interval for the healthy-node Consistently assertion.
	SplitBrainHealthyNodeCheckInterval = 5 * time.Second

	// SBRCWatchdogPathTestName is the SBRC name used in the storage I/O loss watchdog test (OCP-88880).
	SBRCWatchdogPathTestName = "test-sbrc-storage-loss-watchdog"

	// NHCSBRTestName is the NodeHealthCheck CR name for the storage I/O loss test (OCP-88880).
	NHCSBRTestName = "nhc-sbr-storage-loss"

	// InjectorPodName is the name prefix for privileged host-namespace pods used in SBR disruptive tests.
	InjectorPodName = "sbr-storage-injector"

	// SBRCDetectOnlyTestName is the SBRC name used in the detectOnlyMode test (OCP-88876).
	SBRCDetectOnlyTestName = "test-sbrc-detect-only"

	// NHCDetectOnlyTestName is the NHC CR name used in the detect-only suppression test.
	NHCDetectOnlyTestName = "nhc-sbr-detect-only"

	// WatchdogKeepaliveNamePrefix is the name prefix for privileged pods that hold /dev/watchdog
	// open during the RHWA-1068 regression test.
	WatchdogKeepaliveNamePrefix = "sbr-wdog-keepalive-"

	// WatchdogKeepaliveTimeout is how long to wait for all watchdog-keepalive pods to reach Running.
	WatchdogKeepaliveTimeout = 2 * time.Minute

	// DetectOnlyAllPodsReadyTimeout is how long to wait for ALL agent DaemonSet pods to reach Ready
	// in the detect-only watchdog contention regression test (RHWA-1068).
	DetectOnlyAllPodsReadyTimeout = 5 * time.Minute

	// DetectOnlySuppressionCheckDuration is how long to Consistently assert no fencing occurs while
	// SBRStorageUnhealthy=True and detectOnlyMode is Enabled.
	DetectOnlySuppressionCheckDuration = 90 * time.Second

	// DetectOnlySuppressionCheckInterval is the poll interval for the suppression Consistently check.
	DetectOnlySuppressionCheckInterval = 10 * time.Second

	// SBRCDaemonSetGCTimeout is how long to wait for a DaemonSet to be garbage-collected after its SBRC is deleted.
	SBRCDaemonSetGCTimeout = 5 * time.Minute

	// FencingSucceededCondition is the condition type set on a StorageBasedRemediation CR after fencing.
	FencingSucceededCondition = "FencingSucceeded"

	// SBRCRCleanupTimeout is how long to wait for an SBR CR to be cleaned up after fencing.
	SBRCRCleanupTimeout = 10 * time.Minute

	// SBRCRCleanupPollInterval is the polling interval while waiting for SBR CR removal.
	SBRCRCleanupPollInterval = 20 * time.Second

	// SBRCNodeHangTestName is the SBRC name for the hard node hang test (OCP-88738).
	SBRCNodeHangTestName = "test-sbrc-node-hang"

	// NHCNodeHangTestName is the NodeHealthCheck CR name for the node hang test (OCP-88738).
	NHCNodeHangTestName = "nhc-sbr-node-hang"

	// SBRCStorageLossWriteName is the SBRC name for the write-only storage loss test.
	SBRCStorageLossWriteName = "test-sbrc-storage-loss-write"

	// NHCWriteLossTestName is the NHC CR name for the write-only storage loss test.
	NHCWriteLossTestName = "nhc-sbr-write-loss"

	// StorageLossWriteInjectorPodName is the injector pod name prefix for the write-only test.
	StorageLossWriteInjectorPodName = "sbr-write-injector"

	// SBRCTransientTestName is the SBRC name for the transient storage failure self-healing test.
	SBRCTransientTestName = "test-sbrc-transient"

	// TransientInjectorPodName is the name prefix for the injector pod in the transient storage test.
	TransientInjectorPodName = "sbr-transient-injector"
)

// SBRStorageClass is the StorageClass name to use when creating SBRCs that require shared storage.
// Set SBR_STORAGE_CLASS env var to override auto-discovery (useful in air-gapped or non-ODF environments).
// When empty, tests auto-discover a CephFS StorageClass (provisioner containing "cephfs").
var SBRStorageClass = os.Getenv("SBR_STORAGE_CLASS")

// WatchdogDebugImage is the container image for /dev/watchdog* discovery pods.
// Must provide sh and ls. Set SBR_WATCHDOG_DEBUG_IMAGE to override (e.g. in disconnected clusters).
var WatchdogDebugImage = func() string {
	if img := os.Getenv("SBR_WATCHDOG_DEBUG_IMAGE"); img != "" {
		return img
	}

	return "registry.access.redhat.com/ubi9/ubi-minimal:latest"
}()
