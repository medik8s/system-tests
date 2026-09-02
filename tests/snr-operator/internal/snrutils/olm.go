package snrutils

import (
	"fmt"

	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/clients"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/olm"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/pod"
	olmV1alpha1 "github.com/rh-ecosystem-edge/eco-goinfra/pkg/schemes/olm/operators/v1alpha1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/medik8s/system-tests/tests/internal/helpers"
	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
	"github.com/medik8s/system-tests/tests/snr-operator/internal/snrparams"
)

// InstallGAOperator creates a Subscription for the SNR operator from the built-in
// redhat-operators catalog on the current OCP cluster.
func InstallGAOperator(apiClient *clients.Settings) (*olm.SubscriptionBuilder, error) {
	sub := olm.NewSubscriptionBuilder(
		apiClient,
		snrparams.UpgradeSubName,
		medik8sparams.OperatorNs,
		snrparams.GAOperatorCatalog,
		snrparams.GACatalogNamespace,
		snrparams.UpgradeOperatorPackage,
	)

	sub.WithChannel(snrparams.GAChannel).
		WithInstallPlanApproval(olmV1alpha1.ApprovalAutomatic)

	sub, err := sub.Create()
	if err != nil {
		return nil, fmt.Errorf("failed to create GA Subscription: %w", err)
	}

	return sub, nil
}

// FindSucceededCSV returns the first CSV matching the given name pattern that is
// in the Succeeded phase. Callers should wrap this in Eventually for polling.
func FindSucceededCSV(
	apiClient *clients.Settings, namePattern string,
) (*olm.ClusterServiceVersionBuilder, error) {
	csvs, err := olm.ListClusterServiceVersionWithNamePattern(
		apiClient, namePattern, medik8sparams.OperatorNs)
	if err != nil {
		return nil, fmt.Errorf("failed to list CSVs matching %q: %w", namePattern, err)
	}

	for _, csv := range csvs {
		phase, phaseErr := csv.GetPhase()
		if phaseErr == nil && phase == olmV1alpha1.CSVPhaseSucceeded {
			return csv, nil
		}
	}

	return nil, fmt.Errorf("no CSV matching %q in Succeeded phase", namePattern)
}

// CreateUpgradeCatalogSource creates a grpc CatalogSource from the target catalog image.
func CreateUpgradeCatalogSource(
	apiClient *clients.Settings,
) (*olm.CatalogSourceBuilder, error) {
	catalog := olm.NewCatalogSourceBuilder(
		apiClient, snrparams.UpgradeCatalogName, snrparams.GACatalogNamespace)
	catalog.Definition.Spec.SourceType = "grpc"
	catalog.Definition.Spec.Image = snrparams.TargetCatalogImage
	catalog.Definition.Spec.DisplayName = "medik8s Upgrade Catalog"
	catalog.Definition.Spec.Publisher = "medik8s QE"

	catalog, err := catalog.Create()
	if err != nil {
		return nil, fmt.Errorf("failed to create upgrade CatalogSource: %w", err)
	}

	return catalog, nil
}

// SwitchSubscriptionCatalog updates an existing Subscription to point to the
// upgrade CatalogSource and target channel.
func SwitchSubscriptionCatalog(
	apiClient *clients.Settings,
) (*olm.SubscriptionBuilder, error) {
	sub, err := olm.PullSubscription(
		apiClient, snrparams.UpgradeSubName, medik8sparams.OperatorNs)
	if err != nil {
		return nil, fmt.Errorf("failed to pull Subscription: %w", err)
	}

	sub.Definition.Spec.CatalogSource = snrparams.UpgradeCatalogName
	sub.Definition.Spec.Channel = snrparams.TargetChannel

	sub, err = sub.Update()
	if err != nil {
		return nil, fmt.Errorf("failed to update Subscription to target catalog: %w", err)
	}

	return sub, nil
}

// GetSNRControllerImage returns the manager container image of the first running
// SNR controller pod.
func GetSNRControllerImage(apiClient *clients.Settings) (string, error) {
	listOptions := metav1.ListOptions{
		LabelSelector: snrparams.OperatorControllerPodLabelSelector,
	}

	snrPods, err := pod.List(apiClient, medik8sparams.OperatorNs, listOptions)
	if err != nil {
		return "", fmt.Errorf("failed to list SNR controller pods: %w", err)
	}

	runningPods := helpers.FilterRunningPods(snrPods)
	if len(runningPods) == 0 {
		return "", fmt.Errorf("no running SNR controller pods found")
	}

	for _, container := range runningPods[0].Object.Spec.Containers {
		if container.Name == snrparams.ManagerContainerName {
			return container.Image, nil
		}
	}

	return "", fmt.Errorf("container %s not found in SNR controller pod",
		snrparams.ManagerContainerName)
}

// CleanupUpgradeResources removes the upgrade CatalogSource and Subscription
// created during the test.
func CleanupUpgradeResources(apiClient *clients.Settings, logf func(string, ...interface{})) {
	if sub, err := olm.PullSubscription(
		apiClient, snrparams.UpgradeSubName, medik8sparams.OperatorNs); err == nil {
		if delErr := sub.Delete(); delErr != nil {
			logf("WARNING: failed to delete upgrade Subscription: %v\n", delErr)
		}
	}

	if catalog, err := olm.PullCatalogSource(
		apiClient, snrparams.UpgradeCatalogName, snrparams.GACatalogNamespace); err == nil {
		if delErr := catalog.Delete(); delErr != nil {
			logf("WARNING: failed to delete upgrade CatalogSource: %v\n", delErr)
		}
	}
}
