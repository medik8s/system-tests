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

	// NodeNotReadyTimeout is how long to wait for a node to become NotReady after kubelet stop.
	NodeNotReadyTimeout = 5 * time.Minute

	// NodeRebootTimeout is how long to wait for a node reboot to complete.
	NodeRebootTimeout = 6 * time.Minute

	// OcDebugTimeout is the timeout for oc debug node commands.
	// Matches SNR's timeout (snrparams.OcDebugTimeout). In Prow CI on AWS,
	// oc debug pod startup (schedule + image pull + attach) routinely takes
	// 30-90s; 60s caused deterministic failures in interop tests.
	OcDebugTimeout = 5 * time.Minute

	// SSHTimeout is the timeout for SSH-based node operations.
	SSHTimeout = 30 * time.Second

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

	// RemediationCRDeletionTimeout is how long to wait for a FAR/FART CR to be fully deleted.
	RemediationCRDeletionTimeout = 2 * time.Minute
	// NHCEnabledTimeout is how long to wait for a test NodeHealthCheck to become enabled.
	NHCEnabledTimeout = 2 * time.Minute
	// ControllerLogsTimeout is the deadline for collecting controller logs.
	ControllerLogsTimeout = 1 * time.Minute
	// ControllerRBACTimeout is the deadline for collecting controller RBAC diagnostics.
	ControllerRBACTimeout = 15 * time.Second

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

	// FARCRRetryCount is the retry count for FAR/FART CR spec (matches upstream default).
	FARCRRetryCount = 10
	// FARCRRetryInterval is the retry interval for FAR/FART CR spec.
	FARCRRetryInterval = "20s"
	// FARCRTimeout is the fence agent command timeout for FAR/FART CR spec.
	FARCRTimeout = "60s"
	// FARCRRemediationStrategy is the default remediation strategy for FAR CRs.
	FARCRRemediationStrategy = "OutOfServiceTaint"

	// CrioCleanupTimeout is the timeout for the post-remediation CRI-O overlay cleanup.
	CrioCleanupTimeout = 2 * time.Minute

	// SharedCredentialsSecretName is the Secret created by the test suite to hold
	// fence agent credentials in the format expected by SharedSecretName.
	SharedCredentialsSecretName = "far-test-shared-credentials"

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

	// NHCInteropLabelKey is the label applied to the target node during NHC interop tests
	// to scope the NHC selector to a single node.
	NHCInteropLabelKey = "e2e.medik8s.io/far-nhc-target"

	// NHCDetectionTimeout is how long to wait for NHC to detect an unhealthy node
	// and create a FAR CR.
	NHCDetectionTimeout = 5 * time.Minute

	// NHCRecoveryTimeout is how long to wait for NHC to clear unhealthy state
	// after the node recovers.
	NHCRecoveryTimeout = 5 * time.Minute

	// NHCUnhealthyDuration is the duration a node must be unhealthy before NHC
	// triggers remediation. Kept short for test speed.
	NHCUnhealthyDuration = "30s"
	// NHCEnabledPhase is the phase reported when NHC starts watching its selected nodes.
	NHCEnabledPhase = "Enabled"
)

// WorkloadTestImage is the container image used for test workload pods.
var WorkloadTestImage = medik8sparams.WorkloadImage
