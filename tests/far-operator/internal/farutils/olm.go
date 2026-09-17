package farutils

import (
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/clients"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/olm"

	"github.com/medik8s/system-tests/tests/far-operator/internal/farparams"
	"github.com/medik8s/system-tests/tests/internal/helpers"
	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
)

// InstallGAOperator creates a Subscription for the FAR operator from the built-in
// redhat-operators catalog on the current OCP cluster.
func InstallGAOperator(apiClient *clients.Settings) (*olm.SubscriptionBuilder, error) {
	return helpers.InstallGAOperatorSubscription(
		apiClient,
		farparams.UpgradeSubName,
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
		farparams.UpgradeSubName,
		medik8sparams.OperatorNs,
		catalogName,
		medik8sparams.TargetChannel,
	)
}

// GetFARControllerImage returns the manager container image of the first running
// FAR controller pod.
func GetFARControllerImage(apiClient *clients.Settings) (string, error) {
	return helpers.GetControllerImage(
		apiClient,
		medik8sparams.OperatorNs,
		farparams.OperatorControllerPodLabelSelector,
		farparams.ManagerContainerName,
	)
}

// CleanupUpgradeResources removes the Subscription created during the upgrade test.
func CleanupUpgradeResources(apiClient *clients.Settings, logf func(string, ...interface{})) {
	helpers.DeleteSubscription(apiClient, farparams.UpgradeSubName, medik8sparams.OperatorNs, logf)
}
