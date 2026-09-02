package snrparams

import (
	"os"
	"time"
)

// Upgrade test timeouts.
const (
	// OperatorUpgradeTimeout is the time for a CSV to reach Succeeded after an operator upgrade.
	OperatorUpgradeTimeout = 15 * time.Minute
	// OCPUpgradeTimeout is the time for an OCP cluster upgrade to complete.
	OCPUpgradeTimeout = 120 * time.Minute
	// OCPUpgradeStartTimeout is the time for an OCP upgrade to begin progressing.
	OCPUpgradeStartTimeout = 10 * time.Minute
	// PostUpgradeRecoveryTimeout is the time for pods and CSV to recover after an OCP upgrade.
	PostUpgradeRecoveryTimeout = 10 * time.Minute

	// GAOperatorCatalog is the built-in CatalogSource name that ships with OCP.
	GAOperatorCatalog = "redhat-operators"
	// GACatalogNamespace is the namespace where the GA catalog lives.
	GACatalogNamespace = "openshift-marketplace"
	// GAChannel is the OLM channel used for the GA operator version.
	GAChannel = "stable"

	// UpgradeCatalogName is the CatalogSource name for the target (pre-GA) operator.
	UpgradeCatalogName = "medik8s-upgrade-catalog"
	// UpgradeSubName is the Subscription name used in the upgrade test.
	UpgradeSubName = "snr-upgrade-sub"

	// UpgradeOperatorPackageDefault is the default OLM package name for SNR.
	UpgradeOperatorPackageDefault = "self-node-remediation"
	// TargetChannelDefault is the default OLM channel in the target catalog.
	TargetChannelDefault = "stable"
)

// Upgrade test environment variables.
var (
	// UpgradeOperatorPackage is the OLM package name, from MEDIK8S_OPERATOR_PACKAGE.
	UpgradeOperatorPackage = envOrDefault("MEDIK8S_OPERATOR_PACKAGE", UpgradeOperatorPackageDefault)
	// TargetCatalogImage is the Konflux FBC catalog image for the target operator version.
	TargetCatalogImage = os.Getenv("MEDIK8S_TARGET_CATALOG_IMAGE")
	// TargetChannel is the OLM channel in the target catalog, from MEDIK8S_TARGET_CHANNEL.
	TargetChannel = envOrDefault("MEDIK8S_TARGET_CHANNEL", TargetChannelDefault)
	// TargetOCPImage is the OCP release payload for the upgrade target version.
	TargetOCPImage = os.Getenv("OPENSHIFT_UPGRADE_RELEASE_IMAGE_OVERRIDE")
)

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return fallback
}
