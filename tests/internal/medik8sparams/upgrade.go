package medik8sparams

import (
	"os"
	"time"
)

const (
	// OperatorUpgradeTimeout is the time for a CSV to reach Succeeded after an operator upgrade.
	OperatorUpgradeTimeout = 15 * time.Minute
	// OCPUpgradeTimeout is the time for an OCP cluster upgrade to complete.
	OCPUpgradeTimeout = 120 * time.Minute
	// OCPUpgradeStartTimeout is the time for an OCP upgrade to begin progressing.
	OCPUpgradeStartTimeout = 10 * time.Minute
	// PostUpgradeRecoveryTimeout is the time for pods and CSV to recover after an OCP upgrade.
	PostUpgradeRecoveryTimeout = 30 * time.Minute
	// MCPRolloutTimeout is the time to wait for MachineConfigPool rollout after IDMS apply.
	MCPRolloutTimeout = 20 * time.Minute
	// MCPDetectionTimeout is the time to wait for MCO to detect an IDMS change.
	MCPDetectionTimeout = 5 * time.Minute

	// GAOperatorCatalog is the built-in CatalogSource name that ships with OCP.
	GAOperatorCatalog = "redhat-operators"
	// GACatalogNamespace is the namespace where the GA catalog lives.
	GACatalogNamespace = "openshift-marketplace"
	// GAChannel is the OLM channel used for the GA operator version.
	GAChannel = "stable"

	// UpgradeCatalogName is the CatalogSource created by the medik8s-catalogsource CI step.
	UpgradeCatalogName = "medik8s-catalog"
)

var (
	// SharedDir is the ci-operator shared directory path between test steps.
	SharedDir = os.Getenv("SHARED_DIR")
	// OperatorPackage is the OLM package name, from MEDIK8S_OPERATOR_PACKAGE.
	OperatorPackage = envOrDefault("MEDIK8S_OPERATOR_PACKAGE", "fence-agents-remediation")
	// TargetChannel is the OLM channel in the target catalog, from MEDIK8S_TARGET_CHANNEL.
	TargetChannel = envOrDefault("MEDIK8S_TARGET_CHANNEL", GAChannel)
	// TargetOCPImage is the OCP release payload for the upgrade target version.
	TargetOCPImage = envOrDefault("OPENSHIFT_UPGRADE_RELEASE_IMAGE_OVERRIDE",
		os.Getenv("RELEASE_IMAGE_LATEST"))
	// SkipOCPUpgrade opts out of the OCP ClusterVersion upgrade step
	SkipOCPUpgrade = os.Getenv("MEDIK8S_SKIP_OCP_UPGRADE") == "true"
	// KubeletStopViaOCDebug switches the kubelet-stop remediation trigger from
	// SSH to "oc debug node/"
	KubeletStopViaOCDebug = os.Getenv("MEDIK8S_KUBELET_STOP_OCDEBUG") == "true"
)

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return fallback
}
