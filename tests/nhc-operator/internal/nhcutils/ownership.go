package nhcutils

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// RunLabel identifies resources created by one upgrade scenario execution.
const RunLabel = "test.medik8s.io/nhc-upgrade-run"

// MissingAPI permits a genuinely empty cluster, but never hides authorization
// failures, transport errors, or unavailable API services.
func MissingAPI(err error) bool {
	return apierrors.IsNotFound(err) || meta.IsNoMatchError(err)
}

// CheckClean deliberately requires a new namespace. Installing this sample in
// a shared namespace is unsupported: SDK cleanup is scoped by package name.
func CheckClean(ctx context.Context, api client.Client, namespace string) error {
	plugin := consolePlugin()
	if err := api.Get(ctx, client.ObjectKeyFromObject(plugin), plugin); !MissingAPI(err) {
		if err != nil {
			return err
		}

		return fmt.Errorf("pre-existing ConsolePlugin %s would be overwritten by NHC", plugin.GetName())
	}

	prior := &OwnedRun{API: api, Namespace: namespace}

	clusterObjects, err := prior.clusterObjects(ctx)
	if err != nil {
		return err
	}

	if len(clusterObjects) != 0 {
		return fmt.Errorf("pre-existing OLM cluster objects reference namespace %s: %v", namespace, clusterObjects)
	}

	ns := &corev1.Namespace{}
	if err := api.Get(ctx, client.ObjectKey{Name: namespace}, ns); !apierrors.IsNotFound(err) {
		if err != nil {
			return err
		}

		return fmt.Errorf("namespace %s already exists; it is not owned by this run", namespace)
	}

	for _, gvk := range []schema.GroupVersionKind{
		{Group: "operators.coreos.com", Version: "v1alpha1", Kind: "ClusterServiceVersion"},
		{Group: "operators.coreos.com", Version: "v1alpha1", Kind: "Subscription"},
		{Group: "operators.coreos.com", Version: "v1alpha1", Kind: "InstallPlan"},
		{Group: "apps", Version: "v1", Kind: "Deployment"},
		{Group: "apps", Version: "v1", Kind: "DaemonSet"},
		{Group: "remediation.medik8s.io", Version: "v1alpha1", Kind: "NodeHealthCheck"},
		{Group: "self-node-remediation.medik8s.io", Version: "v1alpha1", Kind: "SelfNodeRemediation"},
		{Group: "self-node-remediation.medik8s.io", Version: "v1alpha1", Kind: "SelfNodeRemediationTemplate"},
	} {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind + "List"))

		if err := api.List(ctx, list); err != nil {
			if MissingAPI(err) {
				continue
			}

			return fmt.Errorf("checking %s: %w", gvk.Kind, err)
		}

		for _, object := range list.Items {
			// Inspect spec as well as names: Subscriptions/InstallPlans may use
			// arbitrary names while still installing the conflicting package.
			text := fmt.Sprint(object.Object["spec"], object.GetName())
			if gvk.Group == "remediation.medik8s.io" || gvk.Group == "self-node-remediation.medik8s.io" ||
				strings.Contains(text, "node-healthcheck") || strings.Contains(text, "self-node-remediation") {
				return fmt.Errorf("pre-existing %s %s/%s is not owned by this run",
					gvk.Kind, object.GetNamespace(), object.GetName())
			}
		}
	}

	return nil
}

// OwnedRun acquires its namespace using a unique marker and records successful
// CREATE identities. A failed preflight therefore has nothing to clean up.
type OwnedRun struct {
	API            client.Client
	Namespace      string
	Token          string
	NamespaceUID   types.UID
	Objects        []*unstructured.Unstructured
	Packages       []string
	SDK            string
	CleanupPackage func(context.Context, string, string, string) (string, error)
}

// CreateNamespace creates and records the namespace owned by this run.
func (run *OwnedRun) CreateNamespace(ctx context.Context) error {
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: run.Namespace, Labels: map[string]string{
		RunLabel: run.Token,
		"security.openshift.io/scc.podSecurityLabelSync": "false",
		"pod-security.kubernetes.io/enforce":             "privileged",
	}}}

	err := run.API.Create(ctx, namespace)
	if err == nil {
		run.NamespaceUID = namespace.UID
	}

	return err
}

// Create labels and records an object before asking the API to create it.
func (run *OwnedRun) Create(ctx context.Context, object *unstructured.Unstructured) error {
	labels := object.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}

	labels[RunLabel] = run.Token
	object.SetLabels(labels)
	// Retain the marker before CREATE, covering an accepted request whose
	// response was lost. Cleanup recovers the UID only with this exact marker.
	run.Objects = append(run.Objects, object)

	return run.API.Create(ctx, object)
}

// DeleteIdentity deletes only the exact object UID that the run observed.
func DeleteIdentity(ctx context.Context, api client.Client, object client.Object) error {
	uid := object.GetUID()
	if uid == "" {
		return fmt.Errorf("refusing deletion without UID: %s", object.GetName())
	}

	err := api.Delete(ctx, object, client.Preconditions{UID: &uid})

	return client.IgnoreNotFound(err)
}

// Cleanup attempts every owned deletion and returns all failures. Shared CRDs
// are deliberately retained; the owned namespace also contains SDK unpack jobs,
// catalogs, InstallPlans and any partially installed namespaced resources.
//
//nolint:gocognit,funlen // Cleanup attempts every recovery step independently and joins all failures.
func (run *OwnedRun) Cleanup(ctx context.Context) error {
	var failures []error

	for i := len(run.Objects) - 1; i >= 0; i-- {
		object := run.Objects[i]

		current := object.DeepCopy()
		if err := run.API.Get(ctx, client.ObjectKeyFromObject(current), current); err != nil {
			if !MissingAPI(err) {
				failures = append(failures, err)
			}

			continue
		}

		if current.GetLabels()[RunLabel] != run.Token || (object.GetUID() != "" && current.GetUID() != object.GetUID()) {
			failures = append(failures, fmt.Errorf("preserved unowned/replaced %s %s", current.GetKind(), current.GetName()))

			continue
		}

		if err := DeleteIdentity(ctx, run.API, current); err != nil {
			failures = append(failures, err)

			continue
		}
		// Keep controllers alive while any CR finalizers finish.
		if err := wait.PollUntilContextTimeout(
			ctx, 2*time.Second, 3*time.Minute, true, func(ctx context.Context) (bool, error) {
				err := run.API.Get(ctx, client.ObjectKeyFromObject(current), current)
				if MissingAPI(err) {
					return true, nil
				}

				return false, err
			}); err != nil {
			failures = append(failures, fmt.Errorf("waiting for %s %s deletion: %w", current.GetKind(), current.GetName(), err))
		}
	}

	namespace := &corev1.Namespace{}

	err := run.API.Get(ctx, client.ObjectKey{Name: run.Namespace}, namespace)
	if apierrors.IsNotFound(err) {
		return errors.Join(failures...)
	}

	if err != nil {
		return errors.Join(append(failures, err)...)
	}

	if namespace.Labels[RunLabel] != run.Token ||
		(run.NamespaceUID != "" && namespace.UID != run.NamespaceUID) {
		// No cleanup at all after rejection of a pre-existing namespace.
		if run.NamespaceUID == "" {
			return errors.Join(failures...)
		}

		return errors.Join(append(failures,
			fmt.Errorf("preserved replaced namespace %s", namespace.Name))...)
	}

	clusterObjects, err := run.clusterObjects(ctx)
	if err != nil {
		failures = append(failures, err)
	}

	for i := len(run.Packages) - 1; i >= 0; i-- {
		output, err := run.CleanupPackage(ctx, run.SDK, run.Namespace, run.Packages[i])
		if err != nil {
			failures = append(failures, fmt.Errorf("cleanup %s: %w\n%s", run.Packages[i], err, output))
		}
	}
	// Include resources that appeared while a partial install was stopping.
	lateObjects, err := run.clusterObjects(ctx)
	if err != nil {
		failures = append(failures, err)
	}

	clusterObjects = append(clusterObjects, lateObjects...)
	for _, object := range clusterObjects {
		if err := DeleteIdentity(ctx, run.API, object); err != nil {
			failures = append(failures, err)
		}
	}

	if err := DeleteIdentity(ctx, run.API, namespace); err != nil {
		failures = append(failures, err)
	}
	// Verify CR and cluster object removal as well as namespace termination.
	remaining := append([]*unstructured.Unstructured{}, clusterObjects...)
	remaining = append(remaining, run.Objects...)

	err = wait.PollUntilContextTimeout(ctx, 2*time.Second, 10*time.Minute, true, func(ctx context.Context) (bool, error) {
		for _, object := range remaining {
			current := object.DeepCopy()
			if err := run.API.Get(ctx, client.ObjectKeyFromObject(current), current); err == nil {
				return false, nil
			} else if !MissingAPI(err) {
				return false, err
			}
		}

		err := run.API.Get(ctx, client.ObjectKey{Name: run.Namespace}, &corev1.Namespace{})

		return apierrors.IsNotFound(err), client.IgnoreNotFound(err)
	})
	if err != nil {
		failures = append(failures, fmt.Errorf(
			"owned resources remain in/from namespace %s (run %s): %w", run.Namespace, run.Token, err))
	}

	return errors.Join(failures...)
}

func (run *OwnedRun) clusterObjects(ctx context.Context) ([]*unstructured.Unstructured, error) {
	var (
		objects  []*unstructured.Unstructured
		failures []error
	)
	// Both NHC versions create this without an ownerReference. Preflight
	// rejects any existing instance, and its backend binds it to our new ns.
	plugin := consolePlugin()
	if err := run.API.Get(ctx, client.ObjectKeyFromObject(plugin), plugin); err == nil {
		namespace, _, _ := unstructured.NestedString(plugin.Object, "spec", "backend", "service", "namespace")

		service, _, _ := unstructured.NestedString(plugin.Object, "spec", "backend", "service", "name")
		if namespace == run.Namespace && service == "node-healthcheck-node-remediation-console-plugin" {
			objects = append(objects, plugin)
		}
	} else if !MissingAPI(err) {
		failures = append(failures, err)
	}

	for _, gvk := range []schema.GroupVersionKind{
		{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "ClusterRole"},
		{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "ClusterRoleBinding"},
		{Group: "admissionregistration.k8s.io", Version: "v1", Kind: "ValidatingWebhookConfiguration"},
		{Group: "admissionregistration.k8s.io", Version: "v1", Kind: "MutatingWebhookConfiguration"},
	} {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind + "List"))
		// OLM sets this ownership label, not a before/after cluster-wide diff.
		if err := run.API.List(ctx, list, client.MatchingLabels{"olm.owner.namespace": run.Namespace}); err != nil {
			failures = append(failures, err)

			continue
		}

		for i := range list.Items {
			objects = append(objects, list.Items[i].DeepCopy())
		}
	}

	return objects, errors.Join(failures...)
}

func consolePlugin() *unstructured.Unstructured {
	object := &unstructured.Unstructured{}
	object.SetAPIVersion("console.openshift.io/v1")
	object.SetKind("ConsolePlugin")
	object.SetName("node-remediation-console-plugin")

	return object
}

// SafeSpec uses a logical contradiction, so no present or future node can
// match it. pauseRequests adds a second guard and an observable status token.
func SafeSpec(template, namespace, token string) map[string]interface{} {
	return map[string]interface{}{
		"selector": map[string]interface{}{"matchExpressions": []interface{}{
			map[string]interface{}{"key": RunLabel, "operator": "Exists"},
			map[string]interface{}{"key": RunLabel, "operator": "DoesNotExist"},
		}},
		"minHealthy":          int64(1),
		"pauseRequests":       []interface{}{token},
		"unhealthyConditions": []interface{}{map[string]interface{}{"type": "Ready", "status": "False", "duration": "60s"}},
		"remediationTemplate": map[string]interface{}{
			"apiVersion": "self-node-remediation.medik8s.io/v1alpha1",
			"kind":       "SelfNodeRemediationTemplate",
			"name":       template,
			"namespace":  namespace,
		},
	}
}

// PauseResponse confirms that the same NHC freshly acknowledged a unique pause token.
func PauseResponse(object *unstructured.Unstructured, uid types.UID, token, previousRV string) bool {
	phase, _, _ := unstructured.NestedString(object.Object, "status", "phase")
	reason, _, _ := unstructured.NestedString(object.Object, "status", "reason")

	return object.GetUID() == uid && object.GetResourceVersion() != previousRV &&
		phase == "Paused" && reason == "NHC is paused: "+token
}
