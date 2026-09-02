package helpers

import (
	"context"
	"fmt"

	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/clients"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/olm"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/pod"
	olmV1alpha1 "github.com/rh-ecosystem-edge/eco-goinfra/pkg/schemes/olm/operators/v1alpha1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// InstallGAOperatorSubscription creates an OLM OperatorGroup (if missing) and
// Subscription for the GA operator from the specified catalog. OLM requires
// exactly one OperatorGroup per namespace to process Subscriptions.
func InstallGAOperatorSubscription(
	apiClient *clients.Settings,
	subName, ns, catalog, catalogNs, pkg, channel string,
) (*olm.SubscriptionBuilder, error) {
	if err := EnsureOperatorGroup(apiClient, ns); err != nil {
		return nil, fmt.Errorf("failed to ensure OperatorGroup in %s: %w", ns, err)
	}

	sub := olm.NewSubscriptionBuilder(
		apiClient, subName, ns, catalog, catalogNs, pkg,
	)

	sub.WithChannel(channel).
		WithInstallPlanApproval(olmV1alpha1.ApprovalAutomatic)

	sub, err := sub.Create()
	if err != nil {
		return nil, fmt.Errorf("failed to create GA Subscription: %w", err)
	}

	return sub, nil
}

// EnsureOperatorGroup creates an AllNamespaces OperatorGroup in the given
// namespace if one does not already exist. Uses AllNamespaces mode (empty
// targetNamespaces) because medik8s operators do not support OwnNamespace.
func EnsureOperatorGroup(apiClient *clients.Settings, ns string) error {
	og := olm.NewOperatorGroupBuilder(apiClient, "medik8s-og", ns)
	if og.Exists() {
		return nil
	}

	og.Definition.Spec.TargetNamespaces = nil

	_, err := og.Create()
	if err != nil {
		return fmt.Errorf("failed to create OperatorGroup: %w", err)
	}

	return nil
}

// FindSucceededCSV returns the first CSV matching the given name pattern that is
// in the Succeeded phase. Returns an error if no matching CSV is found. Callers
// should wrap this in Eventually for polling behavior.
func FindSucceededCSV(
	apiClient *clients.Settings, namePattern, namespace string,
) (*olm.ClusterServiceVersionBuilder, error) {
	csvs, err := olm.ListClusterServiceVersionWithNamePattern(
		apiClient, namePattern, namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to list CSVs matching %q: %w", namePattern, err)
	}

	var lastPhaseErr error

	for _, csv := range csvs {
		phase, phaseErr := csv.GetPhase()
		if phaseErr != nil {
			lastPhaseErr = phaseErr

			continue
		}

		if phase == olmV1alpha1.CSVPhaseSucceeded {
			return csv, nil
		}
	}

	if lastPhaseErr != nil {
		return nil, fmt.Errorf("no CSV matching %q in Succeeded phase (last GetPhase error: %w)",
			namePattern, lastPhaseErr)
	}

	return nil, fmt.Errorf("no CSV matching %q in Succeeded phase", namePattern)
}

// SwitchSubscriptionCatalog updates an existing Subscription to point to the
// given CatalogSource name and target channel.
func SwitchSubscriptionCatalog(
	apiClient *clients.Settings, subName, ns, catalogName, channel string,
) (*olm.SubscriptionBuilder, error) {
	sub, err := olm.PullSubscription(apiClient, subName, ns)
	if err != nil {
		return nil, fmt.Errorf("failed to pull Subscription: %w", err)
	}

	sub.Definition.Spec.CatalogSource = catalogName
	sub.Definition.Spec.Channel = channel

	sub, err = sub.Update()
	if err != nil {
		return nil, fmt.Errorf("failed to update Subscription to target catalog: %w", err)
	}

	return sub, nil
}

// DeleteSubscription removes an OLM Subscription by name.
func DeleteSubscription(
	apiClient *clients.Settings, subName, ns string,
	logf func(string, ...interface{}),
) {
	sub, err := olm.PullSubscription(apiClient, subName, ns)
	if err != nil {
		logf("WARNING: failed to pull upgrade Subscription for cleanup: %v\n", err)

		return
	}

	if delErr := sub.Delete(); delErr != nil {
		logf("WARNING: failed to delete upgrade Subscription: %v\n", delErr)
	}
}

// CleanupSubscriptionAndCSV deletes the Subscription plus any CSV and
// InstallPlan left behind for it, so a subsequent InstallGAOperatorSubscription
// call starts from a clean slate. CSVs/InstallPlans are filtered by namePattern
// (the operator's OLM package name) rather than deleted namespace-wide, since
// other operators (e.g. NHC) share the same namespace and must not be touched.
func CleanupSubscriptionAndCSV(
	apiClient *clients.Settings, subName, namePattern, ns string,
	logf func(string, ...interface{}),
) {
	DeleteSubscription(apiClient, subName, ns, logf)

	csvs, csvErr := olm.ListClusterServiceVersionWithNamePattern(apiClient, namePattern, ns)
	if csvErr != nil {
		logf("WARNING: failed to list CSVs matching %q for cleanup: %v\n", namePattern, csvErr)

		return
	}

	csvNames := make(map[string]bool, len(csvs))

	for _, csv := range csvs {
		csvNames[csv.Object.Name] = true

		if delErr := csv.Delete(); delErr != nil {
			logf("WARNING: failed to delete leftover CSV %s: %v\n", csv.Object.Name, delErr)
		}
	}

	plans, planErr := olm.ListInstallPlan(apiClient, ns)
	if planErr != nil {
		logf("WARNING: failed to list InstallPlans for cleanup: %v\n", planErr)

		return
	}

	for _, plan := range plans {
		matches := false

		for _, csvName := range plan.Object.Spec.ClusterServiceVersionNames {
			if csvNames[csvName] {
				matches = true

				break
			}
		}

		if !matches {
			continue
		}

		if delErr := plan.Delete(); delErr != nil {
			logf("WARNING: failed to delete leftover InstallPlan %s: %v\n", plan.Object.Name, delErr)
		}
	}
}

// GetControllerImage returns the manager container image of the first running
// controller pod matching the given label selector and container name.
func GetControllerImage(
	apiClient *clients.Settings, namespace, labelSelector, containerName string,
) (string, error) {
	listOptions := metav1.ListOptions{
		LabelSelector: labelSelector,
	}

	pods, err := pod.List(apiClient, namespace, listOptions)
	if err != nil {
		return "", fmt.Errorf("failed to list controller pods: %w", err)
	}

	runningPods := FilterRunningPods(pods)
	if len(runningPods) == 0 {
		return "", fmt.Errorf("no running controller pods found")
	}

	for _, container := range runningPods[0].Object.Spec.Containers {
		if container.Name == containerName {
			return container.Image, nil
		}
	}

	return "", fmt.Errorf("container %s not found in controller pod", containerName)
}

// LogOLMDiagnostics logs the state of OLM resources in a namespace to help
// diagnose operator installation failures. Uses known resource names from the
// upgrade test since eco-goinfra does not expose list functions for all OLM types.
func LogOLMDiagnostics(
	_ context.Context, apiClient *clients.Settings,
	ns, catalogName string,
	logf func(string, ...interface{}),
) {
	logf("=== OLM Diagnostics for namespace %s ===\n", ns)

	og, ogErr := olm.PullOperatorGroup(apiClient, "medik8s-og", ns)
	if ogErr != nil {
		logf("  OperatorGroup medik8s-og: not found (%v)\n", ogErr)
	} else {
		logf("  OperatorGroup medik8s-og: exists (targets: %v)\n",
			og.Object.Spec.TargetNamespaces)
	}

	sub, subErr := olm.PullSubscription(apiClient, "far-upgrade-sub", ns)
	if subErr != nil {
		logf("  Subscription far-upgrade-sub: not found (%v)\n", subErr)
	} else {
		state := ""
		if sub.Object.Status.State != "" {
			state = string(sub.Object.Status.State)
		}

		logf("  Subscription far-upgrade-sub: state=%s currentCSV=%s installedCSV=%s\n",
			state, sub.Object.Status.CurrentCSV, sub.Object.Status.InstalledCSV)

		for _, cond := range sub.Object.Status.Conditions {
			logf("    condition: %s=%s reason=%s message=%s\n",
				cond.Type, cond.Status, cond.Reason, cond.Message)
		}
	}

	cs, csErr := olm.PullCatalogSource(apiClient, catalogName, "openshift-marketplace")
	if csErr != nil {
		logf("  CatalogSource %s: not found (%v)\n", catalogName, csErr)
	} else {
		logf("  CatalogSource %s: status=%s\n",
			catalogName, cs.Object.Status.GRPCConnectionState.LastObservedState)
	}

	csvs, csvErr := olm.ListClusterServiceVersionWithNamePattern(apiClient, "", ns)
	if csvErr != nil {
		logf("  CSVs: error listing: %v\n", csvErr)
	} else {
		logf("  CSVs: %d found\n", len(csvs))

		for _, csv := range csvs {
			phase, _ := csv.GetPhase()
			logf("    - %s (phase=%s)\n", csv.Object.Name, phase)
		}
	}

	logf("=== End OLM Diagnostics ===\n")
}
