package farparams

import "time"

const (
	// Label represents far operator label that can be used for test cases selection.
	Label = "far"

	// ExpectedReplicas defines the expected number of replicas for FAR controller manager.
	ExpectedReplicas = int32(2)

	// ManagerContainerName is the name of the main controller container in the FAR pod.
	ManagerContainerName = "manager"

	// DefaultPollInterval is the polling interval used with Eventually calls.
	DefaultPollInterval = 5 * time.Second

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
	OcDebugTimeout = 60 * time.Second

	// FARConditionTimeout is how long to wait for a FAR CR condition to appear.
	FARConditionTimeout = 2 * time.Minute

	// ControllerLeasePrefix is the prefix for the FAR leader election lease name.
	ControllerLeasePrefix = "fence-agents-remediation"
)
