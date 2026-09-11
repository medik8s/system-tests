package farparams

import (
	"time"

	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
)

const (
	// Label is the operator name used in the suite-level Labels array.
	Label = "far"
	// DefaultPollInterval is the polling interval used with Eventually calls.
	DefaultPollInterval = 5 * time.Second
	// BootIDPollInterval is the polling interval for reboot detection via boot ID changes.
	// Longer than DefaultPollInterval because kubelet may lag updating status.nodeInfo.bootID.
	BootIDPollInterval = 10 * time.Second

	// ExpectedReplicas defines the expected number of replicas for FAR controller manager.
	ExpectedReplicas = int32(2)

	// ManagerContainerName is the name of the main controller container in the FAR pod.
	ManagerContainerName = "manager"

	// DiagnosticsLogTailLines is how many trailing lines of the active FAR
	// controller log to dump when a destructive test fails, enough to capture
	// the reconcile that stalled without flooding the test output.
	DiagnosticsLogTailLines = 100

	// FenceAgentsRemediationCRDName is the full CRD name for FenceAgentsRemediation.
	FenceAgentsRemediationCRDName = "fenceagentsremediations.fence-agents-remediation.medik8s.io"
	// FenceAgentsRemediationTemplateCRDName is the full CRD name for FenceAgentsRemediationTemplate.
	FenceAgentsRemediationTemplateCRDName = "fenceagentsremediationtemplates.fence-agents-remediation.medik8s.io"

	// PSAEnforceLabelKey is the Pod Security Admission enforcement label key.
	PSAEnforceLabelKey = "pod-security.kubernetes.io/enforce"
	// PSAExpectedLevel is the expected PSA enforcement level for the operator namespace.
	PSAExpectedLevel = "privileged"

	// ExpectedPriorityClassName is the priorityClassName that FAR controller pods must have.
	ExpectedPriorityClassName = "system-cluster-critical"

	// ControllerPodLabelKey is the standard K8s label key for the FAR controller pod.
	ControllerPodLabelKey = "app.kubernetes.io/name"

	// FenceAgentBinaryPrefix is the filename prefix for fence agent binaries in /usr/sbin.
	FenceAgentBinaryPrefix = "fence_"

	// FenceAgentAWS is the fence agent binary for AWS EC2 fencing.
	FenceAgentAWS = "fence_aws"

	// FenceAgentIPMI is the fence agent binary for IPMI fencing.
	FenceAgentIPMI = "fence_ipmilan"

	// NodeIdentifierAWS is the fence agent parameter for AWS instance ID.
	NodeIdentifierAWS = "--plug"

	// NodeIdentifierIPMI is the fence agent parameter for IPMI port.
	NodeIdentifierIPMI = "--ipport"

	// AWSCredentialsSecretName is the Secret name provisioned by the CredentialsRequest.
	AWSCredentialsSecretName = "aws-cloud-fencing-credentials-secret"

	// AWSAccessKeyField is the Secret data key for the AWS access key ID.
	AWSAccessKeyField = "aws_access_key_id"

	// AWSSecretKeyField is the Secret data key for the AWS secret access key.
	AWSSecretKeyField = "aws_secret_access_key"

	// NodeReadyTimeout is how long to wait for a node to become Ready after reboot.
	NodeReadyTimeout = 10 * time.Minute

	// NodeRebootTimeout is how long to wait for a node reboot to complete.
	NodeRebootTimeout = 6 * time.Minute

	// OcDebugTimeout is the timeout for oc debug node commands.
	OcDebugTimeout = 60 * time.Second

	// FARConditionTimeout is how long to wait for a FAR CR condition to appear.
	FARConditionTimeout = 2 * time.Minute

	// EventVerifyTimeout is how long to wait for Kubernetes lifecycle events to
	// appear. Longer than FARConditionTimeout because event delivery lags the CR
	// condition and the shared, rate-limited API client can throttle event List
	// calls during the destructive suite.
	EventVerifyTimeout = 5 * time.Minute
	// EventVerifyInterval is the poll interval for event verification. Longer than
	// DefaultPollInterval to reduce List pressure on the shared rate-limited client.
	EventVerifyInterval = 10 * time.Second

	// RemediationCRDeletionTimeout is how long to wait for a FAR/FARTemplate CR to be fully deleted.
	RemediationCRDeletionTimeout = 2 * time.Minute

	// ControllerLeaseName is the FAR leader election lease name (LeaderElectionID in cmd/main.go).
	ControllerLeaseName = "cb305759.medik8s.io"

	// FARConditionProcessing is the condition type for remediation progress.
	FARConditionProcessing = "Processing"
	// FARConditionFenceAgentSucceeded is the condition type for fence agent action result.
	FARConditionFenceAgentSucceeded = "FenceAgentActionSucceeded"
	// FARConditionSucceeded is the condition type for overall remediation outcome.
	FARConditionSucceeded = "Succeeded"

	// FARNoScheduleTaintKey is the taint key applied by FAR during remediation.
	FARNoScheduleTaintKey = "remediation.medik8s.io/fence-agents-remediation"

	// FAREventRemediationStarted is the event reason emitted when remediation begins.
	FAREventRemediationStarted = "RemediationStarted"
	// FAREventFenceAgentSucceeded is the event reason emitted when the fence agent action succeeds.
	FAREventFenceAgentSucceeded = "FenceAgentSucceeded"
	// FAREventRemediationFinished is the event reason emitted when remediation completes.
	FAREventRemediationFinished = "RemediationFinished"
	// FAREventNodeRemediationCompleted is the event reason emitted on the Node when remediation completes.
	FAREventNodeRemediationCompleted = "NodeRemediationCompleted"

	// ControllerHandoverTimeout is how long to wait for controller leadership transfer.
	ControllerHandoverTimeout = 3 * time.Minute
	// WorkloadEvictionTimeout is how long to wait for workload pods to be evicted.
	WorkloadEvictionTimeout = 5 * time.Minute
	// WorkloadPodReadyTimeout is how long to wait for a test workload pod to reach Running.
	WorkloadPodReadyTimeout = 2 * time.Minute

	// FARCRRetryCount is the retry count for FAR/FARTemplate CR spec (matches upstream default).
	FARCRRetryCount = 10
	// FARCRRetryInterval is the retry interval for FAR/FARTemplate CR spec.
	FARCRRetryInterval = "20s"
	// FARCRTimeout is the fence agent command timeout for FAR/FARTemplate CR spec.
	FARCRTimeout = "60s"
	// FARCRRemediationStrategy is the default remediation strategy for FAR CRs.
	FARCRRemediationStrategy = "OutOfServiceTaint"

	// CrioCleanupTimeout is the timeout for the post-remediation CRI-O overlay cleanup.
	CrioCleanupTimeout = 2 * time.Minute

	// SharedCredentialsSecretName is the Secret created by the test suite to hold
	// fence agent credentials in the format expected by SharedSecretName.
	SharedCredentialsSecretName = "far-test-shared-credentials"

	// LogSearchTimeout is the Eventually timeout when polling controller logs for a message.
	LogSearchTimeout = 2 * time.Minute

	// WebhookReadyTimeout is how long to wait for the FAR admission webhook to become
	// reachable. deployment readiness does not guarantee the webhook server and its
	// Service endpoints are serving yet, so the first CR create can fail with
	// "failed calling webhook ... context deadline exceeded".
	WebhookReadyTimeout = 2 * time.Minute

	// UnsupportedActionMsg is the webhook error when an unsupported action is configured.
	UnsupportedActionMsg = "FAR doesn't support any other action than"

	// UnsupportedAgentMsg is the webhook error when a fence agent binary is not in the container.
	UnsupportedAgentMsg = "unsupported fence agent"

	// InvalidAgentPatternFARMsg is the CRD validation error for FAR CR agent name not matching fence_ prefix.
	InvalidAgentPatternFARMsg = "spec.agent in body should match"

	// InvalidAgentPatternFARTemplateMsg is the CRD validation error for FARTemplate agent name not matching fence_ prefix.
	InvalidAgentPatternFARTemplateMsg = "spec.template.spec.agent in body should match"

	// MisconfigTestCRName is the FAR CR name used by the invalid-name misconfiguration test.
	MisconfigTestCRName = "non-existing-node"

	// MisconfigUnsupportedAgent is a fence agent name that passes prefix validation but is not installed.
	MisconfigUnsupportedAgent = "fence_incorrect"

	// MisconfigInvalidPrefixAgent is a fence agent name that fails the fence_ prefix validation.
	MisconfigInvalidPrefixAgent = "incorrect_fence"

	// MisconfigFARTemplateName is the FARTemplate name used by misconfiguration tests.
	MisconfigFARTemplateName = "fenceagentsremediationtemplate-test"

	// WebhookTestCRName is the FAR CR name used by webhook rejection tests.
	// Uses a placeholder (not a real node) since webhook validates agent/action, not node.
	WebhookTestCRName = "far-webhook-test-node"

	// PlaceholderNodeName is a non-node name used in FARTemplate webhook tests;
	// the template is rejected on agent validation before any node matching.
	PlaceholderNodeName = "placeholder-node"

	// IPMIPortValue is a dummy IPMI --ipport value used in negative-test node parameters.
	IPMIPortValue = "6233"

	// MinControlPlaneNodes is the minimum Ready CP nodes needed for safe CP remediation.
	MinControlPlaneNodes = 3

	// CPRebootTimeout is how long to wait for a CP node reboot (slower than worker due to etcd).
	CPRebootTimeout = 10 * time.Minute
	// CPNodeReadyTimeout is how long to wait for a CP node to return Ready after reboot.
	CPNodeReadyTimeout = 12 * time.Minute
	// EtcdRejoinTimeout is how long to wait for etcd ClusterOperator to recover.
	EtcdRejoinTimeout = 10 * time.Minute

	// MinWorkersForDestructiveTests is the minimum Ready workers for standard destructive tests.
	MinWorkersForDestructiveTests = 3
	// MinWorkersForTwoWorkerTest is the minimum Ready workers for the 2-worker topology test.
	MinWorkersForTwoWorkerTest = 2

	// TestCordonAnnotation marks nodes cordoned by the test for cleanup identification.
	TestCordonAnnotation = "system-tests.medik8s.io/cordoned-for-topology-test"

	// MustGatherImageEnvVar is the environment variable that overrides the must-gather image.
	MustGatherImageEnvVar = "MUST_GATHER_IMAGE"
	// DefaultMustGatherImage is the default medik8s must-gather container image.
	// The :latest tag is intentional: this suite validates that must-gather collects
	// the correct FAR data using the build a customer would actually pull, not a
	// pinned must-gather version. The resolved image digest is logged on every run
	// (see farutils.RunMustGather) so a failure is reproducible against the exact
	// build, and MUST_GATHER_IMAGE overrides this default when a specific ref is needed.
	DefaultMustGatherImage = "quay.io/medik8s/must-gather:latest"
	// MustGatherTimeout is how long to allow `oc adm must-gather` to run.
	MustGatherTimeout = 10 * time.Minute

	// TimedOutBadInstanceID is an invalid AWS EC2 instance ID that forces fence_aws
	// to fail with InvalidInstanceID.NotFound on every retry attempt.
	TimedOutBadInstanceID = "i-00000000000000000"
	// TimedOutLogPattern matches per-retry fence agent failure log entries.
	// The FAR controller logs FenceAgentFailedCommandMessage ("command failed")
	// exactly once per failed retry in cliexecuter.go runWithRetry().
	TimedOutLogPattern = `command failed`
	// TimedOutRetryBuffer is extra time per retry to account for fence agent execution overhead.
	TimedOutRetryBuffer = 30 * time.Second
	// ControllerPodReadyTimeout is how long to wait for controller pods to become Ready after deletion.
	ControllerPodReadyTimeout = 2 * time.Minute

	// MinWorkersForObservabilityTests is the minimum number of Ready worker nodes the
	// observability suite requires: ExpectedReplicas (the FAR controller pods, spread
	// across that many distinct nodes for HA per OCP-61222) plus 1. The must-gather spec
	// reboots a randomly selected non-leader worker (SelectWorkerNode excludes only the
	// leader), which may be the node hosting the second replica; OutOfServiceTaint then
	// evicts that replica, and anti-affinity forbids the leader's node, so a spare Ready
	// worker must exist for it to reschedule onto while the target reboots. The timed-out
	// spec needs only ExpectedReplicas nodes; this gate is driven by must-gather.
	MinWorkersForObservabilityTests = int(ExpectedReplicas) + 1
)

// WorkloadTestImage is the container image used for test workload pods.
var WorkloadTestImage = medik8sparams.WorkloadImage

// NodeNotFoundMsgs contains all known variants of the FAR controller
// node-not-found log message across versions. The log fetch checks all
// in a single pass to avoid pod-restart races between separate fetches.
var NodeNotFoundMsgs = []string{
	"Could not find CR's target node",
	"couldn't find node matching remediation",
}
