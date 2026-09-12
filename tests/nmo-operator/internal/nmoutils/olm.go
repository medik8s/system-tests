package nmoutils

import (
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/clients"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/olm"

	"github.com/medik8s/system-tests/tests/internal/helpers"
	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
	"github.com/medik8s/system-tests/tests/nmo-operator/internal/nmoparams"
)

// InstallGAOperator creates a Subscription for the NMO operator from the built-in
// redhat-operators catalog on the current OCP cluster.
func InstallGAOperator(apiClient *clients.Settings) (*olm.SubscriptionBuilder, error) {
	return helpers.InstallGAOperatorSubscription(
		apiClient,
		nmoparams.UpgradeSubName,
		medik8sparams.OperatorNs,
		medik8sparams.GAOperatorCatalog,
		medik8sparams.GACatalogNamespace,
		medik8sparams.OperatorPackage,
		medik8sparams.GAChannel,
	)
}

// FindSucceededCSV returns the first CSV matching the given name pattern that is
// in the Succeeded phase.
func FindSucceededCSV(
	apiClient *clients.Settings, namePattern string,
) (*olm.ClusterServiceVersionBuilder, error) {
	return helpers.FindSucceededCSV(apiClient, namePattern, medik8sparams.OperatorNs)
}

// SwitchSubscriptionCatalog updates an existing Subscription to point to the
// given CatalogSource name and target channel.
func SwitchSubscriptionCatalog(
	apiClient *clients.Settings, catalogName string,
) (*olm.SubscriptionBuilder, error) {
	return helpers.SwitchSubscriptionCatalog(
		apiClient,
		nmoparams.UpgradeSubName,
		medik8sparams.OperatorNs,
		catalogName,
		medik8sparams.TargetChannel,
	)
}

// GetNMOControllerImage returns the manager container image of the first running
// NMO controller pod.
func GetNMOControllerImage(apiClient *clients.Settings) (string, error) {
	return helpers.GetControllerImage(
		apiClient,
		medik8sparams.OperatorNs,
		nmoparams.OperatorControllerPodLabelSelector,
		nmoparams.ManagerContainerName,
	)
}

// CleanupUpgradeResources removes the Subscription, CSV, and InstallPlan
// created during the upgrade test.
func CleanupUpgradeResources(apiClient *clients.Settings, logf func(string, ...interface{})) {
	helpers.CleanupSubscriptionAndCSV(
		apiClient, nmoparams.UpgradeSubName, medik8sparams.OperatorPackage, medik8sparams.OperatorNs, logf)
}
