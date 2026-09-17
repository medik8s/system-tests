package tests

import (
	"context"
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/pod"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/reportxml"

	"github.com/medik8s/system-tests/tests/internal/helpers"
	"github.com/medik8s/system-tests/tests/internal/labels"
	. "github.com/medik8s/system-tests/tests/internal/medik8sinittools"
	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
	"github.com/medik8s/system-tests/tests/sbr-operator/internal/sbrparams"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

var _ = Describe(
	"SBR Functional — Observability and Metrics",
	Ordered,
	Label(labels.OperatorSBR),
	func() {
		BeforeAll(func() {
			By("Pre-cleanup: removing any leftover metrics-test SBRC from previous runs")

			Eventually(func() error {
				stale := buildSBRC(sbrparams.SBRCMetricsTestName, map[string]interface{}{})

				deleteErr := APIClient.Delete(context.TODO(), stale)
				if deleteErr == nil || k8serrors.IsNotFound(deleteErr) {
					return nil
				}

				return deleteErr
			}, medik8sparams.DefaultTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
				"Pre-test cleanup of stale SBRC %s failed", sbrparams.SBRCMetricsTestName)

			By("Waiting for stale SBRC to disappear before recreating")

			Eventually(func() error {
				check := buildSBRC(sbrparams.SBRCMetricsTestName, map[string]interface{}{})

				getErr := APIClient.Get(context.TODO(),
					types.NamespacedName{Name: sbrparams.SBRCMetricsTestName, Namespace: medik8sparams.OperatorNs},
					check)
				if k8serrors.IsNotFound(getErr) {
					return nil
				}

				if getErr != nil {
					return getErr
				}

				return fmt.Errorf("SBRC %s still terminating", sbrparams.SBRCMetricsTestName)
			}, medik8sparams.DefaultTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
				"Stale SBRC must disappear before recreating")

			By("Waiting for stale agent DaemonSet to be garbage collected")

			staleAgentDSName := sbrparams.SBRAgentDaemonSetPrefix + sbrparams.SBRCMetricsTestName

			Eventually(func() error {
				_, err := APIClient.DaemonSets(medik8sparams.OperatorNs).Get(
					context.TODO(), staleAgentDSName, metav1.GetOptions{})
				if k8serrors.IsNotFound(err) {
					return nil
				}

				if err != nil {
					return err
				}

				return fmt.Errorf("stale DaemonSet %s still exists", staleAgentDSName)
			}, medik8sparams.DefaultTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
				"Stale agent DaemonSet must be gone before creating new SBRC")

			By("Creating StorageBasedRemediationConfig with sharedStorageClass so agent DaemonSet starts")

			// sharedStorageClass is required: without it the controller never creates the
			// agent DaemonSet, so waitForSBRCReady below would time out.
			storageClass := discoverRWXStorageClass()
			if storageClass == "" {
				Skip("No RWX storage class found; set SBR_STORAGE_CLASS env or deploy ODF/CephFS before running")
			}

			sbrc := buildSBRC(sbrparams.SBRCMetricsTestName, map[string]interface{}{
				"sharedStorageClass": storageClass,
			})

			// DeferCleanup fires even when BeforeAll panics after Create succeeds, unlike AfterAll.
			DeferCleanup(func() {
				Eventually(func() error {
					stale := buildSBRC(sbrparams.SBRCMetricsTestName, map[string]interface{}{})

					deleteErr := APIClient.Delete(context.TODO(), stale)
					if deleteErr == nil || k8serrors.IsNotFound(deleteErr) {
						return nil
					}

					return deleteErr
				}, medik8sparams.DefaultTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
					"DeferCleanup: failed to delete metrics SBRC %s", sbrparams.SBRCMetricsTestName)

				Eventually(func() error {
					getErr := APIClient.Get(context.TODO(),
						types.NamespacedName{Name: sbrparams.SBRCMetricsTestName, Namespace: medik8sparams.OperatorNs},
						buildSBRC(sbrparams.SBRCMetricsTestName, map[string]interface{}{}))
					if k8serrors.IsNotFound(getErr) {
						return nil
					}

					if getErr != nil {
						return getErr
					}

					return fmt.Errorf("SBRC %s still exists in DeferCleanup", sbrparams.SBRCMetricsTestName)
				}, medik8sparams.DefaultTimeout, sbrparams.DefaultPollInterval).Should(Succeed())
			})

			Expect(APIClient.Create(context.TODO(), sbrc)).To(Succeed(),
				"Failed to create StorageBasedRemediationConfig %s", sbrparams.SBRCMetricsTestName)

			waitForSBRCReady(sbrparams.SBRCMetricsTestName)
		})

		AfterAll(func() {
			By("Deleting metrics-test StorageBasedRemediationConfig")

			Eventually(func() error {
				stale := buildSBRC(sbrparams.SBRCMetricsTestName, map[string]interface{}{})

				deleteErr := APIClient.Delete(context.TODO(), stale)
				if deleteErr == nil || k8serrors.IsNotFound(deleteErr) {
					return nil
				}

				return deleteErr
			}, medik8sparams.DefaultTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
				"AfterAll: failed to delete metrics-test SBRC %s", sbrparams.SBRCMetricsTestName)

			By("Waiting for metrics-test StorageBasedRemediationConfig to be fully removed")

			Eventually(func() error {
				getErr := APIClient.Get(context.TODO(),
					types.NamespacedName{Name: sbrparams.SBRCMetricsTestName, Namespace: medik8sparams.OperatorNs},
					buildSBRC(sbrparams.SBRCMetricsTestName, map[string]interface{}{}))
				if k8serrors.IsNotFound(getErr) {
					return nil
				}

				if getErr != nil {
					return getErr
				}

				return fmt.Errorf("SBRC %s still exists", sbrparams.SBRCMetricsTestName)
			}, medik8sparams.DefaultTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
				"SBRC %s was not removed within the expected timeout", sbrparams.SBRCMetricsTestName)
		})

		It("Verify SBR agent pods expose required Prometheus metrics at :"+sbrparams.AgentMetricsPort+"/metrics",
			reportxml.ID("89202"),
			Label(
				labels.OperatorSBR,
				labels.TierSmoke,
				labels.FrequencyPresubmit,
				labels.DisruptionNonDestructive,
				labels.PlatformAny,
				labels.ComponentMetrics,
			), func() {
				By("Fetching /metrics via exec into the agent pod and asserting all required metric names are present")

				Eventually(func() error {
					// Scope to the SBRC-specific DaemonSet by filtering on name prefix so we
					// don't accidentally exec into a pod owned by a different SBRC's DaemonSet.
					metricsDSPodPrefix := sbrparams.SBRAgentDaemonSetPrefix + sbrparams.SBRCMetricsTestName + "-"

					freshPods, listErr := pod.List(APIClient, medik8sparams.OperatorNs,
						metav1.ListOptions{LabelSelector: sbrparams.AgentPodLabelSelector})
					if listErr != nil {
						return fmt.Errorf("failed to list agent pods: %w", listErr)
					}

					var metricsPods []*pod.Builder

					for _, p := range freshPods {
						if strings.HasPrefix(p.Object.Name, metricsDSPodPrefix) {
							metricsPods = append(metricsPods, p)
						}
					}

					running := helpers.FilterRunningPods(metricsPods)
					if len(running) == 0 {
						return fmt.Errorf("no Running+Ready SBR agent pods found for DaemonSet %s%s",
							sbrparams.SBRAgentDaemonSetPrefix, sbrparams.SBRCMetricsTestName)
					}

					agentPod := running[0]
					podName := agentPod.Object.Name

					// ProxyGet returns HTTP 503 in OVN-K environments because the API server
					// cannot route directly to arbitrary pod ports. Exec into the pod and hit
					// localhost:<port>/metrics from inside the container instead.
					// Port 8082 is the SBR agent's Prometheus metrics endpoint (agent-metrics);
					// port 8080 serves controller-runtime's built-in metrics (runtime-metrics).
					metricsURL := "http://localhost:" + sbrparams.AgentMetricsPort + "/metrics"

					buf, execErr := agentPod.ExecCommand([]string{
						"sh", "-c",
						"curl -sf " + metricsURL + " 2>/dev/null || wget -qO- " + metricsURL + " 2>/dev/null",
					})
					if execErr != nil {
						return fmt.Errorf("failed to read metrics from pod %s: %w", podName, execErr)
					}

					metricsOutput := buf.String()

					if metricsOutput == "" {
						return fmt.Errorf("metrics endpoint returned empty output from pod %s"+
							" -- curl/wget may not be available in the agent image", podName)
					}

					for _, metricName := range sbrparams.AgentExpectedMetricNames {
						if !strings.Contains(metricsOutput, metricName) {
							return fmt.Errorf("metric %q not found in /metrics output of pod %s",
								metricName, podName)
						}
					}

					return nil
				}, medik8sparams.DefaultTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
					"SBR agent pod must expose all required Prometheus metrics at :%s/metrics",
					sbrparams.AgentMetricsPort)
			})
	})
