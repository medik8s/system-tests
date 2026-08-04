package tests

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/deployment"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/reportxml"

	"github.com/medik8s/system-tests/tests/far-operator/internal/farparams"
	"github.com/medik8s/system-tests/tests/far-operator/internal/farutils"
	"github.com/medik8s/system-tests/tests/internal/helpers"
	"github.com/medik8s/system-tests/tests/internal/labels"
	. "github.com/medik8s/system-tests/tests/internal/medik8sinittools"
	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
)

const (
	nhcDeploymentName = "node-healthcheck-controller-manager"
	nhcOldDefaultName = "nhc-worker-default"
)

var nhcGVK = schema.GroupVersionKind{
	Group:   "remediation.medik8s.io",
	Version: "v1alpha1",
	Kind:    "NodeHealthCheck",
}

var _ = Describe("NHC+FAR Interop",
	Ordered, ContinueOnFailure, Serial,
	Label(labels.OperatorFAR, labels.OperatorNHC, labels.OperatorInterop,
		labels.TierInterop, labels.DisruptionDestructive,
		labels.PlatformAWS, labels.FrequencyWeekly, labels.ComponentRemediation),
	func() {
		var (
			ctx          context.Context
			fenceAgent   string
			leaderNode   string
			sharedParams map[string]interface{}
			nodeParams   map[string]interface{}

			currentNHCName    string
			currentFARTName   string
			currentLabelVal   string
			currentTargetNode string
		)

		BeforeAll(func() {
			ctx = context.Background()

			By("Verifying NHC deployment is Ready")

			nhcDeploy, err := deployment.Pull(
				APIClient, nhcDeploymentName, medik8sparams.OperatorNs)
			if err != nil {
				Skip("NHC operator not installed; skipping interop tests")
			}

			Expect(nhcDeploy.IsReady(medik8sparams.DefaultTimeout)).To(BeTrue(),
				"NHC deployment is not Ready")

			prereqs := setupAWSFARPrerequisites(ctx, APIClient)
			fenceAgent = prereqs.fenceAgent
			leaderNode = prereqs.leaderNode
			sharedParams = prereqs.sharedParams
			nodeParams = prereqs.nodeParams

			By("Removing default NHC to prevent remediation conflict")
			deleteRemediationCR(ctx, APIClient, nhcGVK, nhcOldDefaultName)
		})

		JustAfterEach(func() {
			if CurrentSpecReport().Failed() {
				GinkgoWriter.Println("Test failed - collecting diagnostics")
				logFARControllerState(ctx, APIClient)
				logNHCDiagnostics(ctx, currentNHCName, currentTargetNode)
			}

			if currentTargetNode != "" {
				By("Cleanup: waiting for FAR CR to reach Succeeded before deletion")

				pollCtx, pollCancel := context.WithTimeout(ctx, farparams.FARConditionTimeout)
				defer pollCancel()

				if pollErr := wait.PollUntilContextCancel(pollCtx, farparams.DefaultPollInterval, true,
					func(ctx context.Context) (bool, error) {
						farObj := &unstructured.Unstructured{}
						farObj.SetGroupVersionKind(farGVK)

						if err := APIClient.Get(ctx, client.ObjectKey{
							Name:      currentTargetNode,
							Namespace: medik8sparams.OperatorNs,
						}, farObj); err != nil {
							if k8serrors.IsNotFound(err) {
								return true, nil
							}

							return false, fmt.Errorf("get FAR CR %s: %w", currentTargetNode, err)
						}

						return farConditionSucceeded(farObj), nil
					},
				); pollErr != nil {
					message := fmt.Sprintf("FAR CR %s did not reach Succeeded or disappear before cleanup: %v",
						currentTargetNode, pollErr)
					GinkgoWriter.Printf("WARNING: %s\n", message)
					AddReportEntry("nhc-cleanup-far-wait-failed", message)
				}

				By("Cleanup: deleting FAR CR for " + currentTargetNode)
				deleteRemediationCR(ctx, APIClient, farGVK, currentTargetNode)
			}

			if currentNHCName != "" {
				By("Cleanup: deleting NHC " + currentNHCName)
				deleteRemediationCR(ctx, APIClient, nhcGVK, currentNHCName)
				currentNHCName = ""
			}

			if currentFARTName != "" {
				By("Cleanup: deleting FART " + currentFARTName)
				deleteRemediationCR(ctx, APIClient, fartGVK, currentFARTName)
				currentFARTName = ""
			}

			if currentTargetNode != "" && currentLabelVal != "" {
				By("Cleanup: removing interop label from " + currentTargetNode)

				updateErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
					node := &corev1.Node{}
					if err := APIClient.Get(ctx, client.ObjectKey{
						Name: currentTargetNode,
					}, node); err != nil {
						if k8serrors.IsNotFound(err) {
							return nil
						}

						return err
					}

					delete(node.Labels, farparams.NHCInteropLabelKey)

					return APIClient.Update(ctx, node)
				})
				if updateErr != nil {
					message := fmt.Sprintf("failed to remove interop label from %s: %v",
						currentTargetNode, updateErr)
					GinkgoWriter.Printf("WARNING: %s\n", message)
					AddReportEntry("nhc-cleanup-label-removal-failed", message)
				}

				currentLabelVal = ""
			}

			if currentTargetNode != "" {
				nodeName := currentTargetNode
				currentTargetNode = ""

				By("Cleanup: waiting for " + nodeName + " to become Ready")

				if err := farutils.WaitForNodeReady(
					ctx, APIClient, nodeName,
					farparams.NodeReadyTimeout, GinkgoWriter.Printf); err != nil {
					message := fmt.Sprintf("node %s did not become Ready within %s: %v",
						nodeName, farparams.NodeReadyTimeout, err)
					GinkgoWriter.Printf("WARNING: %s\n", message)
					AddReportEntry("nhc-cleanup-node-recovery-failed", message)
				}

				By("Cleanup: unmasking and restarting kubelet on " + nodeName)
				startKubeletAfterRemediation(ctx, nodeName)
			}
		})

		triggerNHCRemediation := func(testPrefix string, includeAction bool) (string, string) {
			By("Selecting a non-leader worker node")

			targetNode, err := helpers.SelectWorkerNode(ctx, APIClient, leaderNode)
			Expect(err).ToNot(HaveOccurred())

			currentTargetNode = targetNode.Name
			GinkgoWriter.Printf("Selected target node: %s\n", targetNode.Name)

			By("Labeling target node " + targetNode.Name + " for NHC scope")

			labelValue := fmt.Sprintf("%s-%d", testPrefix, time.Now().UnixMilli())
			currentLabelVal = labelValue

			node := &corev1.Node{}
			Expect(APIClient.Get(ctx, client.ObjectKey{Name: targetNode.Name}, node)).To(Succeed())

			if node.Labels == nil {
				node.Labels = make(map[string]string)
			}

			node.Labels[farparams.NHCInteropLabelKey] = labelValue
			Expect(APIClient.Update(ctx, node)).To(Succeed())

			By("Cleaning CRI-O overlay on " + targetNode.Name)
			removeWorkloadImage(ctx, targetNode.Name)

			By("Recording boot ID before remediation")

			oldBootID, err := farutils.GetNodeBootIDFromAPI(ctx, APIClient, targetNode.Name)
			Expect(err).ToNot(HaveOccurred())

			fartName := fmt.Sprintf("fart-%s", testPrefix)
			currentFARTName = fartName

			By("Creating FART " + fartName)

			fartSharedParams := make(map[string]interface{}, len(sharedParams))
			for k, v := range sharedParams {
				if !includeAction && k == "--action" {
					continue
				}

				fartSharedParams[k] = v
			}

			fart := buildFARTUnstructured(fartName, fenceAgent, fartSharedParams, nodeParams)
			deleteRemediationCR(ctx, APIClient, fartGVK, fartName)
			Expect(APIClient.Create(ctx, fart)).To(Succeed(),
				"Failed to create FART %s", fartName)

			nhcName := fmt.Sprintf("nhc-%s", testPrefix)
			currentNHCName = nhcName

			By("Creating NHC " + nhcName + " pointing to FART " + fartName)

			nhc := buildNHCUnstructured(nhcName, fartName, medik8sparams.OperatorNs, labelValue)

			deleteRemediationCR(ctx, APIClient, nhcGVK, nhcName)

			By("Removing any stale FAR remediation for " + targetNode.Name)
			deleteRemediationCR(ctx, APIClient, farGVK, targetNode.Name)

			testStartTime := time.Now()

			Expect(APIClient.Create(ctx, nhc)).To(Succeed(),
				"Failed to create NHC %s", nhcName)

			By("Waiting for NHC " + nhcName + " to reach Enabled phase")
			waitForNHCEnabled(ctx, nhcName)

			By("Stopping kubelet on " + targetNode.Name)

			Expect(stopKubeletForRemediation(
				ctx, targetNode.Name)).To(Succeed(),
				"Failed to stop kubelet on %s", targetNode.Name)

			By("Verifying " + targetNode.Name + " becomes NotReady")

			Expect(farutils.WaitForNodeNotReady(
				ctx, APIClient, targetNode.Name,
				farparams.NodeNotReadyTimeout, GinkgoWriter.Printf)).To(Succeed(),
				"Node %s did not become NotReady after kubelet stop", targetNode.Name)

			By("Waiting for NHC to create FAR CR for " + targetNode.Name)
			waitForNHCCreatedFAR(ctx, APIClient, targetNode.Name, testStartTime)

			return targetNode.Name, oldBootID
		}

		// Keep OCP-61309 separate for its happy-path traceability; OCP-90159
		// extends the same flow with lifecycle assertions.
		It("should remediate unhealthy node when NHC uses FART",
			reportxml.ID("61309"),
			Label(labels.TierAcceptance),
			func() {
				By("Triggering NHC remediation through a FART")

				nodeName, oldBootID := triggerNHCRemediation("nhc-far", true)

				By("Verifying the node rebooted and recovered")

				waitForRemediation(ctx, APIClient, nodeName, oldBootID)
			})

		It("should default to reboot when FART omits action",
			reportxml.ID("66204"),
			Label(labels.TierAcceptance),
			func() {
				By("Triggering NHC remediation through a FART without an action")

				nodeName, oldBootID := triggerNHCRemediation("nhc-far-noaction", false)

				By("Verifying the node rebooted and recovered")

				waitForRemediation(ctx, APIClient, nodeName, oldBootID)
			})

		It("should emit expected FAR controller log messages during NHC-triggered remediation",
			reportxml.ID("70872"),
			Label(labels.TierAcceptance),
			func() {
				logStartTime := time.Now()
				nodeName, oldBootID := triggerNHCRemediation("nhc-far-logs", true)
				waitForRemediation(ctx, APIClient, nodeName, oldBootID)

				By("Fetching FAR controller logs since test start")

				By("Verifying expected log messages are present")

				expectedMessages := []string{
					"Finalizer was added",
					"Execute the fence agent",
					"FAR remediation taint was added",
					"FenceAgentsRemediation CR has completed to remediate the node",
				}

				Eventually(func(g Gomega) {
					logs := getFARControllerLogsSince(ctx, logStartTime, nodeName)
					g.Expect(logs).ToNot(BeEmpty(), "Failed to retrieve FAR controller logs")

					for _, msg := range expectedMessages {
						g.Expect(logs).To(ContainSubstring(msg),
							"Expected log message %q not found in FAR controller logs", msg)
					}
				}, farparams.ControllerLogsTimeout, farparams.DefaultPollInterval).Should(Succeed())
			})

		It("should complete full NHC+FAR interop lifecycle",
			reportxml.ID("90159"),
			Label(labels.TierInterop),
			func() {
				nodeName, oldBootID := triggerNHCRemediation("nhc-far-lifecycle", true)

				By("Verifying FAR CR exists for " + nodeName)

				farObj := &unstructured.Unstructured{}
				farObj.SetGroupVersionKind(farGVK)
				Expect(APIClient.Get(ctx, client.ObjectKey{
					Name: nodeName, Namespace: medik8sparams.OperatorNs,
				}, farObj)).To(Succeed(),
					"FAR CR should exist while remediation is in progress")

				waitForRemediation(ctx, APIClient, nodeName, oldBootID)

				By("Verifying FAR CR reached Succeeded")

				Eventually(func(g Gomega) { //nolint:varnamelen
					fresh := &unstructured.Unstructured{}
					fresh.SetGroupVersionKind(farGVK)
					g.Expect(APIClient.Get(ctx, client.ObjectKey{
						Name: nodeName, Namespace: medik8sparams.OperatorNs,
					}, fresh)).To(Succeed())

					g.Expect(farConditionSucceeded(fresh)).To(BeTrue(),
						"FAR CR did not reach Succeeded condition")
				}, farparams.FARConditionTimeout, farparams.DefaultPollInterval).Should(Succeed())

				By("Verifying node is Ready, schedulable, and has no FAR taint")

				Eventually(func(g Gomega) { //nolint:varnamelen
					node := &corev1.Node{}
					g.Expect(APIClient.Get(ctx, client.ObjectKey{
						Name: nodeName,
					}, node)).To(Succeed())

					g.Expect(helpers.IsNodeReady(node)).To(BeTrue(),
						"Node should be Ready after remediation")
					g.Expect(node.Spec.Unschedulable).To(BeFalse(),
						"Node should be schedulable after remediation")

					for _, taint := range node.Spec.Taints {
						g.Expect(taint.Key).ToNot(Equal(farparams.FARNoScheduleTaintKey),
							"FAR NoSchedule taint should be removed after remediation")
					}
				}, farparams.NHCRecoveryTimeout, farparams.DefaultPollInterval).Should(Succeed())
			})
	})

func buildNHCUnstructured(
	name, fartName, fartNamespace, labelValue string,
) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": nhcGVK.GroupVersion().String(),
			"kind":       nhcGVK.Kind,
			"metadata": map[string]interface{}{
				"name": name,
			},
			"spec": map[string]interface{}{
				"selector": map[string]interface{}{
					"matchLabels": map[string]interface{}{
						farparams.NHCInteropLabelKey: labelValue,
					},
				},
				"minHealthy": int64(0),
				"unhealthyConditions": []interface{}{
					map[string]interface{}{
						"type":     "Ready",
						"status":   "False",
						"duration": farparams.NHCUnhealthyDuration,
					},
					map[string]interface{}{
						"type":     "Ready",
						"status":   "Unknown",
						"duration": farparams.NHCUnhealthyDuration,
					},
				},
				"remediationTemplate": map[string]interface{}{
					"apiVersion": fartGVK.GroupVersion().String(),
					"kind":       fartGVK.Kind,
					"name":       fartName,
					"namespace":  fartNamespace,
				},
			},
		},
	}
}

func farConditionSucceeded(obj *unstructured.Unstructured) bool {
	conditions, found, err := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if err != nil || !found {
		return false
	}

	for _, condition := range conditions {
		conditionMap, ok := condition.(map[string]interface{})
		if ok && conditionMap["type"] == farparams.FARConditionSucceeded &&
			conditionMap["status"] == string(metav1.ConditionTrue) {
			return true
		}
	}

	return false
}

func waitForNHCCreatedFAR(
	ctx context.Context, k8sClient client.Client, nodeName string, notBefore time.Time,
) {
	GinkgoHelper()

	Eventually(func() (bool, error) {
		farObj := &unstructured.Unstructured{}
		farObj.SetGroupVersionKind(farGVK)

		err := k8sClient.Get(ctx, client.ObjectKey{
			Name: nodeName, Namespace: medik8sparams.OperatorNs,
		}, farObj)
		if err != nil {
			if k8serrors.IsNotFound(err) {
				return false, nil
			}

			return false, fmt.Errorf("get FAR CR %s created by NHC: %w", nodeName, err)
		}

		creationTimestamp := farObj.GetCreationTimestamp()

		return !creationTimestamp.IsZero() && !creationTimestamp.Time.Before(notBefore), nil
	}, farparams.NHCDetectionTimeout, farparams.DefaultPollInterval).Should(BeTrue(),
		"NHC did not create FAR CR for node %s within %s",
		nodeName, farparams.NHCDetectionTimeout)
}

// waitForNHCEnabled polls the NHC CR's status.phase until it reaches "Enabled".
// NHC must be Enabled (watches set up, dynamic RBAC created) before the test
// makes the target node unhealthy, otherwise NHC may miss the event.
func waitForNHCEnabled(ctx context.Context, nhcName string) {
	GinkgoHelper()

	Eventually(func(assertion Gomega) {
		nhcObj := &unstructured.Unstructured{}
		nhcObj.SetGroupVersionKind(nhcGVK)

		assertion.Expect(APIClient.Get(ctx, client.ObjectKey{Name: nhcName}, nhcObj)).To(Succeed())

		phase, found, err := unstructured.NestedString(nhcObj.Object, "status", "phase")
		assertion.Expect(err).ToNot(HaveOccurred())
		assertion.Expect(found).To(BeTrue(), "NHC %s has no status.phase yet", nhcName)
		assertion.Expect(phase).To(Equal(farparams.NHCEnabledPhase),
			"NHC %s phase is %q, expected Enabled", nhcName, phase)
	}, farparams.NHCEnabledTimeout, farparams.DefaultPollInterval).Should(Succeed(),
		"NHC %s did not reach Enabled phase", nhcName)

	GinkgoWriter.Printf("NHC %s reached Enabled phase\n", nhcName)
}

// logNHCDiagnostics dumps NHC CR status, controller logs, and node
// conditions to help diagnose why NHC did not create a remediation CR.
func logNHCDiagnostics(ctx context.Context, nhcName, nodeName string) {
	GinkgoWriter.Println("=== NHC Diagnostics ===")

	if nhcName != "" {
		logNHCCRStatus(ctx, nhcName)
	}

	logNHCControllerLogs(ctx)

	if nodeName != "" {
		logNodeReadyCondition(ctx, nodeName)
	}

	GinkgoWriter.Println("=== End NHC Diagnostics ===")
}

func logNHCCRStatus(ctx context.Context, nhcName string) {
	nhcObj := &unstructured.Unstructured{}
	nhcObj.SetGroupVersionKind(nhcGVK)

	if err := APIClient.Get(ctx, client.ObjectKey{Name: nhcName}, nhcObj); err != nil {
		GinkgoWriter.Printf("WARNING: failed to get NHC %s: %v\n", nhcName, err)

		return
	}

	phase, _, _ := unstructured.NestedString(nhcObj.Object, "status", "phase")
	GinkgoWriter.Printf("NHC %s: phase=%s\n", nhcName, phase)

	conditions, found, _ := unstructured.NestedSlice(nhcObj.Object, "status", "conditions")
	if found {
		for _, c := range conditions {
			cMap, ok := c.(map[string]interface{})
			if !ok {
				continue
			}

			GinkgoWriter.Printf("  condition: type=%v status=%v reason=%v message=%v\n",
				cMap["type"], cMap["status"], cMap["reason"], cMap["message"])
		}
	}

	unhealthyNodes, found, _ := unstructured.NestedSlice(nhcObj.Object, "status", "unhealthyNodes")
	if found {
		GinkgoWriter.Printf("  unhealthyNodes: %v\n", unhealthyNodes)
	}

	inFlightRemediations, found, _ := unstructured.NestedSlice(nhcObj.Object, "status", "inFlightRemediations")
	if found {
		for _, r := range inFlightRemediations {
			GinkgoWriter.Printf("  inFlightRemediation: %v\n", r)
		}
	}
}

func logNHCControllerLogs(ctx context.Context) {
	logCtx, cancel := context.WithTimeout(ctx, farparams.ControllerLogsTimeout)
	defer cancel()

	output, err := runOcLogs(logCtx, "app.kubernetes.io/name=node-healthcheck-operator", "manager", "--tail=50")
	if err != nil {
		GinkgoWriter.Printf("WARNING: failed to get NHC controller logs: %v\n", err)

		return
	}

	GinkgoWriter.Printf("NHC controller logs (last 50 lines):\n%s\n", string(output))

	rbacCtx, rbacCancel := context.WithTimeout(ctx, farparams.ControllerRBACTimeout)
	defer rbacCancel()

	rbacCmd := exec.CommandContext(rbacCtx, "oc", "get", "clusterrole",
		"node-healthcheck-operator-aggregation", "-o", "yaml")

	rbacOut, rbacErr := rbacCmd.CombinedOutput()
	if rbacErr != nil {
		GinkgoWriter.Printf("WARNING: failed to get NHC aggregation role: %v\n", rbacErr)
	} else {
		GinkgoWriter.Printf("NHC aggregation ClusterRole rules:\n%s\n", string(rbacOut))
	}
}

func logNodeReadyCondition(ctx context.Context, nodeName string) {
	node := &corev1.Node{}

	if err := APIClient.Get(ctx, client.ObjectKey{Name: nodeName}, node); err != nil {
		return
	}

	for _, cond := range node.Status.Conditions {
		if cond.Type == corev1.NodeReady {
			GinkgoWriter.Printf("Node %s: Ready=%s (reason=%s, since=%s)\n",
				nodeName, cond.Status, cond.Reason, cond.LastTransitionTime)
		}
	}
}

// stopKubeletForRemediation stops kubelet on the target node through SSH.
// SSH remains available when kubelet is stopped, unlike an oc debug pod.
func stopKubeletForRemediation(ctx context.Context, nodeName string) error {
	return helpers.StopKubeletSSH(ctx, APIClient, nodeName, farparams.SSHTimeout)
}

// startKubeletAfterRemediation starts kubelet on the target node through SSH.
// Used as cleanup safety net after stopKubeletForRemediation.
func startKubeletAfterRemediation(ctx context.Context, nodeName string) {
	err := helpers.StartKubeletSSH(ctx, APIClient, nodeName, farparams.SSHTimeout)
	if err != nil {
		message := fmt.Sprintf("startKubeletAfterRemediation(%s): %v", nodeName, err)
		GinkgoWriter.Printf("WARNING: %s\n", message)
		AddReportEntry("nhc-cleanup-kubelet-restart-failed", message)
	}
}

func getFARControllerLogsSince(ctx context.Context, since time.Time, nodeName string) string {
	sinceStr := since.UTC().Format(time.RFC3339Nano)

	logCtx, cancel := context.WithTimeout(ctx, farparams.ControllerLogsTimeout)
	defer cancel()

	output, err := runOcLogs(logCtx, farparams.OperatorControllerPodLabelSelector,
		farparams.ManagerContainerName, "--since-time="+sinceStr, "--tail=-1")
	if err != nil {
		GinkgoWriter.Printf("WARNING: failed to get FAR controller logs: %v\n%s\n",
			err, strings.TrimSpace(string(output)))

		return ""
	}

	var targetLines []string

	for _, line := range strings.Split(string(output), "\n") {
		if strings.Contains(line, nodeName) {
			targetLines = append(targetLines, line)
		}
	}

	return strings.Join(targetLines, "\n")
}

func runOcLogs(ctx context.Context, selector, container string, extraArgs ...string) ([]byte, error) {
	args := []string{"logs", "-l", selector, "-n", medik8sparams.OperatorNs, "-c", container}
	args = append(args, extraArgs...)

	return exec.CommandContext(ctx, "oc", args...).CombinedOutput()
}
