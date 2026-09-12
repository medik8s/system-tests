package labels

const (
	// OperatorSBR marks tests for the SBR operator.
	OperatorSBR = "operator:sbr"
	// OperatorSNR marks tests for the Self Node Remediation operator.
	OperatorSNR = "operator:snr"
	// OperatorNHC marks tests for the Node Health Check operator.
	OperatorNHC = "operator:nhc"
	// OperatorFAR marks tests for the Fence Agents Remediation operator.
	OperatorFAR = "operator:far"
	// OperatorMDR marks tests for the Machine Deletion Remediation operator.
	OperatorMDR = "operator:mdr"
	// OperatorNMO marks tests for the Node Maintenance Operator.
	OperatorNMO = "operator:nmo"
	// OperatorInterop marks cross-operator interoperability tests.
	OperatorInterop = "operator:interop"

	// TierSmoke marks smoke-level tests.
	TierSmoke = "tier:smoke"
	// TierAcceptance marks acceptance-level tests.
	TierAcceptance = "tier:acceptance"
	// TierInterop marks interoperability tests.
	TierInterop = "tier:interop"
	// TierUpgrade marks upgrade tests.
	TierUpgrade = "tier:upgrade"
	// TierUpgradeOperator marks operator/catalog-upgrade-only tests.
	TierUpgradeOperator = "tier:upgrade-operator"
	// TierUpgradeCluster marks OCP-cluster-upgrade-only tests.
	TierUpgradeCluster = "tier:upgrade-cluster"
	// TierResiliency marks resiliency tests.
	TierResiliency = "tier:resiliency"

	// FrequencyPresubmit marks tests that run in presubmit jobs.
	FrequencyPresubmit = "frequency:presubmit"
	// FrequencyNightly marks tests that run nightly.
	FrequencyNightly = "frequency:nightly"
	// FrequencyWeekly marks tests that run weekly.
	FrequencyWeekly = "frequency:weekly"
	// FrequencyRelease marks tests that run for releases.
	FrequencyRelease = "frequency:release"

	// DisruptionDestructive marks tests that reboot or drain nodes.
	DisruptionDestructive = "disruption:destructive"
	// DisruptionNonDestructive marks tests that do not disrupt nodes.
	DisruptionNonDestructive = "disruption:nondestructive"

	// ComponentOLM marks tests for the OLM component.
	ComponentOLM = "component:olm"
	// ComponentController marks tests for the controller component.
	ComponentController = "component:controller"
	// ComponentDaemonSet marks tests for the DaemonSet component.
	ComponentDaemonSet = "component:daemonset"
	// ComponentWebhook marks tests for the webhook component.
	ComponentWebhook = "component:webhook"
	// ComponentRemediation marks tests for the remediation component.
	ComponentRemediation = "component:remediation"
	// ComponentMetrics marks tests for the metrics component.
	ComponentMetrics = "component:metrics"
	// ComponentPostDeploy marks post-deploy validation tests that do not require
	// functional remediation infrastructure (safe for disconnected environments).
	ComponentPostDeploy = "component:post-deploy"

	// PlatformAWS marks tests that require AWS infrastructure.
	PlatformAWS = "platform:aws"
	// PlatformBareMetal marks tests that require bare-metal infrastructure.
	PlatformBareMetal = "platform:baremetal"
	// PlatformAny marks tests that run on any platform.
	PlatformAny = "platform:any"

	// TopologyControlPlane marks tests targeting control plane nodes.
	TopologyControlPlane = "topology:control-plane"
	// TopologyMinimalWorker marks tests with reduced worker count.
	TopologyMinimalWorker = "topology:minimal-worker"
	// TopologyZeroWorker marks tests with zero schedulable workers.
	TopologyZeroWorker = "topology:zero-worker"
)
