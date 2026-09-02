package nhcutils

import (
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/clients"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/olm"

	"github.com/medik8s/system-tests/tests/internal/helpers"
	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
	"github.com/medik8s/system-tests/tests/nhc-operator/internal/nhcparams"
)

// InstallGAOperator creates a Subscription for the NHC operator from the built-in
// redhat-operators catalog on the current OCP cluster.
func InstallGAOperator(apiClient *clients.Settings) (*olm.SubscriptionBuilder, error) {
	return helpers.InstallGAOperatorSubscription(
		apiClient,
		nhcparams.UpgradeSubName,
		medik8sparams.OperatorNs,
		medik8sparams.GAOperatorCatalog,
		medik8sparams.GACatalogNamespace,
		medik8sparams.OperatorPackage,
		medik8sparams.GAChannel,
	)
}

// SwitchSubscriptionCatalog updates the upgrade-test Subscription to point to the
// given CatalogSource name and target channel.
func SwitchSubscriptionCatalog(
	apiClient *clients.Settings, catalogName string,
) (*olm.SubscriptionBuilder, error) {
	return helpers.SwitchSubscriptionCatalog(
		apiClient,
		nhcparams.UpgradeSubName,
		medik8sparams.OperatorNs,
		catalogName,
		medik8sparams.TargetChannel,
	)
}

// GetNHCControllerImage returns the manager container image of the first running
// NHC controller pod.
func GetNHCControllerImage(apiClient *clients.Settings) (string, error) {
	return helpers.GetControllerImage(
		apiClient,
		medik8sparams.OperatorNs,
		nhcparams.OperatorControllerPodLabelSelector,
		nhcparams.ManagerContainerName,
	)
}

// CleanupUpgradeResources removes the Subscription created during the upgrade test.
func CleanupUpgradeResources(apiClient *clients.Settings, logf func(string, ...interface{})) {
	helpers.DeleteSubscription(apiClient, nhcparams.UpgradeSubName, medik8sparams.OperatorNs, logf)
	helpers.DeleteStaleCSVsAndInstallPlans(
		apiClient, nhcparams.CSVNamePattern, medik8sparams.OperatorNs, logf)
}
