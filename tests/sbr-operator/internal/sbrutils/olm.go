package sbrutils

import (
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/clients"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/olm"

	"github.com/medik8s/system-tests/tests/internal/helpers"
	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
	"github.com/medik8s/system-tests/tests/sbr-operator/internal/sbrparams"
)

// InstallGAOperator creates a Subscription for the SBR operator from the built-in
// redhat-operators catalog on the current OCP cluster.
func InstallGAOperator(apiClient *clients.Settings) (*olm.SubscriptionBuilder, error) {
	return helpers.InstallGAOperatorSubscription(
		apiClient,
		sbrparams.UpgradeSubName,
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
		sbrparams.UpgradeSubName,
		medik8sparams.OperatorNs,
		catalogName,
		medik8sparams.TargetChannel,
	)
}

// GetSBRControllerImage returns the manager container image of the first running
// SBR controller pod.
func GetSBRControllerImage(apiClient *clients.Settings) (string, error) {
	return helpers.GetControllerImage(
		apiClient,
		medik8sparams.OperatorNs,
		sbrparams.OperatorControllerPodLabelSelector,
		sbrparams.ManagerContainerName,
	)
}

// CleanupUpgradeResources removes the Subscription created during the upgrade test.
func CleanupUpgradeResources(apiClient *clients.Settings, logf func(string, ...interface{})) {
	helpers.DeleteSubscription(apiClient, sbrparams.UpgradeSubName, medik8sparams.OperatorNs, logf)
}
