package helpers

import (
	"context"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// WaitForClusterOperatorHealthy polls until the named ClusterOperator reports
// Available=True, Progressing=False, Degraded=False.
func WaitForClusterOperatorHealthy(
	ctx context.Context, k8sClient client.Client,
	operatorName string, timeout, pollInterval time.Duration,
	logf func(string, ...interface{}),
) error {
	log := func(format string, args ...interface{}) {
		if logf != nil {
			logf(format, args...)
		}
	}

	return wait.PollUntilContextTimeout(ctx, pollInterval, timeout, true,
		func(ctx context.Context) (bool, error) {
			clusterOperator := &configv1.ClusterOperator{}
			if err := k8sClient.Get(ctx, client.ObjectKey{Name: operatorName}, clusterOperator); err != nil {
				log("WARNING: failed to get ClusterOperator %s: %v\n", operatorName, err)

				return false, nil
			}

			available := false
			progressing := true
			degraded := true

			for _, cond := range clusterOperator.Status.Conditions {
				switch cond.Type { //nolint:exhaustive
				case configv1.OperatorAvailable:
					available = cond.Status == configv1.ConditionTrue
				case configv1.OperatorProgressing:
					progressing = cond.Status == configv1.ConditionTrue
				case configv1.OperatorDegraded:
					degraded = cond.Status == configv1.ConditionTrue
				}
			}

			if available && !progressing && !degraded {
				return true, nil
			}

			log("ClusterOperator %s: Available=%v Progressing=%v Degraded=%v\n",
				operatorName, available, progressing, degraded)

			return false, nil
		})
}
