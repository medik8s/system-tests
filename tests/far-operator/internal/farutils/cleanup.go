package farutils

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/medik8s/system-tests/tests/far-operator/internal/farparams"
	"github.com/medik8s/system-tests/tests/internal/helpers"
)

// CleanupFARRemediation waits for a FAR CR to reach Succeeded, deletes the CR,
// and verifies the NoSchedule taint is removed. farName is used as the node name
// for taint verification because FAR CRs are named after their target node.
// Callers should follow up with WaitForNodeReady if they also need to wait for
// the node to recover.
func CleanupFARRemediation(
	ctx context.Context, k8sClient client.Client,
	farGVK schema.GroupVersionKind,
	farName, namespace string,
	logf func(string, ...interface{}),
) {
	waitForFARSucceeded(ctx, k8sClient, farGVK, farName, namespace, logf)

	helpers.DeleteRemediationCR(ctx, k8sClient, farGVK, farName,
		namespace, farparams.DefaultPollInterval,
		farparams.RemediationCRDeletionTimeout, logf)

	waitForTaintRemoved(ctx, k8sClient, farName, logf)
}

func waitForFARSucceeded(
	ctx context.Context, k8sClient client.Client,
	farGVK schema.GroupVersionKind,
	farName, namespace string,
	logf func(string, ...interface{}),
) {
	pollCtx, pollCancel := context.WithTimeout(ctx, farparams.FARConditionTimeout)
	defer pollCancel()

	if waitErr := wait.PollUntilContextCancel(pollCtx, farparams.DefaultPollInterval, true,
		func(pollCtx context.Context) (bool, error) {
			farObj := &unstructured.Unstructured{}
			farObj.SetGroupVersionKind(farGVK)

			if err := k8sClient.Get(pollCtx, client.ObjectKey{
				Name:      farName,
				Namespace: namespace,
			}, farObj); err != nil {
				return false, nil
			}

			conditions, found, nestedErr := unstructured.NestedSlice(
				farObj.Object, "status", "conditions")
			if nestedErr != nil {
				logf("WARNING: failed to read FAR conditions: %v\n", nestedErr)

				return false, nil
			}

			if !found {
				return false, nil
			}

			for _, c := range conditions {
				condMap, ok := c.(map[string]interface{})
				if !ok {
					continue
				}

				if condMap["type"] == farparams.FARConditionSucceeded &&
					condMap["status"] == string(metav1.ConditionTrue) {
					return true, nil
				}
			}

			return false, nil
		}); waitErr != nil {
		logf("WARNING: FAR CR %s did not reach Succeeded within %s: %v\n",
			farName, farparams.FARConditionTimeout, waitErr)
	}
}

func waitForTaintRemoved(
	ctx context.Context, k8sClient client.Client,
	nodeName string,
	logf func(string, ...interface{}),
) {
	taintCtx, taintCancel := context.WithTimeout(ctx, farparams.FARConditionTimeout)
	defer taintCancel()

	if taintErr := wait.PollUntilContextCancel(taintCtx, farparams.DefaultPollInterval, true,
		func(pollCtx context.Context) (bool, error) {
			node := &corev1.Node{}
			if err := k8sClient.Get(pollCtx, client.ObjectKey{Name: nodeName}, node); err != nil {
				return false, nil
			}

			for _, taint := range node.Spec.Taints {
				if taint.Key == farparams.FARNoScheduleTaintKey {
					return false, nil
				}
			}

			return true, nil
		}); taintErr != nil {
		logf("WARNING: FAR taint still present on node %s after %s: %v\n",
			nodeName, farparams.FARConditionTimeout, taintErr)
	}
}
