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

const nhcDeploymentName = "node-healthcheck-controller-manager"

var nhcGVK = schema.GroupVersionKind{
	Group:   "remediation.medik8s.io",
	Version: "v1alpha1",
	Kind:    "NodeHealthCheck",
}

var _ = Describe("NHC+FAR Interop",
	Ordered, Serial,
	Label(labels.OperatorFAR, labels.OperatorNHC, labels.OperatorInterop,
		labels.TierInterop, labels.DisruptionDestructive,
		labels.PlatformAWS, labels.FrequencyWeekly),
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

			interopSetupSkipped bool
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

			By("Removing default NHC to prevent remediation conflict")
			deleteNHCByName(ctx, "nhc-worker-default")

			prereqs := setupAWSFARPrerequisites(ctx, APIClient)
			fenceAgent = prereqs.fenceAgent
			leaderNode = prereqs.leaderNode
			sharedParams = prereqs.sharedParams
			nodeParams = prereqs.nodeParams
		})

		BeforeEach(func() {
			if interopSetupSkipped {
				Skip("NHC+FAR interop tests require AWS with both FAR and NHC operators")
			}
		})

		JustAfterEach(func() {
			if CurrentSpecReport().Failed() {
				GinkgoWriter.Println("Test failed - collecting diagnostics")
				logFARControllerState(ctx, APIClient)
				logNHCDiagnostics(ctx, currentNHCName, currentTargetNode)
			}

			if currentNHCName != "" {
				By("Cleanup: deleting NHC " + currentNHCName)

				nhcObj := &unstructured.Unstructured{}
				nhcObj.SetGroupVersionKind(nhcGVK)
				nhcObj.SetName(currentNHCName)

				if err := APIClient.Delete(ctx, nhcObj); err != nil && !k8serrors.IsNotFound(err) {
					GinkgoWriter.Printf("WARNING: failed to delete NHC %s: %v\n",
						currentNHCName, err)
				}

				currentNHCName = ""
			}

			if currentTargetNode != "" {
				By("Cleanup: waiting for FAR CR to reach Succeeded before deletion")

				pollCtx, pollCancel := context.WithTimeout(ctx, farparams.FARConditionTimeout)
				defer pollCancel()

				if waitErr := wait.PollUntilContextCancel(pollCtx, farparams.DefaultPollInterval, true,
					func(ctx context.Context) (bool, error) {
						farObj := &unstructured.Unstructured{}
						farObj.SetGroupVersionKind(farGVK)

						if err := APIClient.Get(ctx, client.ObjectKey{
							Name:      currentTargetNode,
							Namespace: medik8sparams.OperatorNs,
						}, farObj); err != nil {
							return false, nil
						}

						conditions, found, condErr := unstructured.NestedSlice(
							farObj.Object, "status", "conditions")
						if condErr != nil || !found {
							return false, nil
						}

						for _, c := range conditions {
							condMap, ok := c.(map[string]interface{})
							if ok && condMap["type"] == farparams.FARConditionSucceeded &&
								condMap["status"] == string(metav1.ConditionTrue) {
								return true, nil
							}
						}

						return false, nil
					},
				); waitErr != nil {
					GinkgoWriter.Printf(
						"WARNING: FAR CR %s did not reach Succeeded within %s: %v\n",
						currentTargetNode, farparams.FARConditionTimeout, waitErr)
				}

				By("Cleanup: deleting FAR CR for " + currentTargetNode)
				deleteRemediationCR(ctx, APIClient, farGVK, currentTargetNode)
			}

			if currentFARTName != "" {
				By("Cleanup: deleting FART " + currentFARTName)
				deleteRemediationCR(ctx, APIClient, fartGVK, currentFARTName)
				currentFARTName = ""
			}

			if currentTargetNode != "" && currentLabelVal != "" {
				By("Cleanup: removing interop label from " + currentTargetNode)

				node := &corev1.Node{}
				if err := APIClient.Get(ctx, client.ObjectKey{
					Name: currentTargetNode,
				}, node); err == nil {
					delete(node.Labels, farparams.NHCInteropLabelKey)

					if updateErr := APIClient.Update(ctx, node); updateErr != nil {
						GinkgoWriter.Printf("WARNING: failed to remove interop label from %s: %v\n",
							currentTargetNode, updateErr)
					}
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
					GinkgoWriter.Printf(
						"WARNING: node %s did not become Ready within %s: %v\n",
						nodeName, farparams.NodeReadyTimeout, err)

					return
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

			oldBootID, err := helpers.GetNodeBootIDFromAPI(ctx, APIClient, targetNode.Name)
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

			nhcObj := &unstructured.Unstructured{}
			nhcObj.SetGroupVersionKind(nhcGVK)

			if err := APIClient.Get(ctx, client.ObjectKey{Name: nhcName}, nhcObj); err == nil {
				_ = APIClient.Delete(ctx, nhcObj)

				Eventually(func() bool {
					err := APIClient.Get(ctx, client.ObjectKey{Name: nhcName}, nhcObj)

					return k8serrors.IsNotFound(err)
				}, 30*time.Second, farparams.DefaultPollInterval).Should(BeTrue(),
					"NHC %s was not fully deleted before re-creation", nhcName)
			} else if !k8serrors.IsNotFound(err) {
				Fail(fmt.Sprintf("unexpected error checking for stale NHC %s: %v", nhcName, err))
			}

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
			waitForNHCCreatedFAR(ctx, APIClient, targetNode.Name)

			return targetNode.Name, oldBootID
		}

		It("should remediate unhealthy node when NHC uses FART",
			reportxml.ID("61309"),
			Label(labels.TierAcceptance, labels.ComponentRemediation),
			func() {
				nodeName, oldBootID := triggerNHCRemediation("nhc-far", true)
				waitForRemediation(ctx, APIClient, nodeName, oldBootID)
			})

		It("should default to reboot when FART omits action",
			reportxml.ID("66204"),
			Label(labels.TierAcceptance, labels.ComponentRemediation),
			func() {
				nodeName, oldBootID := triggerNHCRemediation("nhc-far-noaction", false)
				waitForRemediation(ctx, APIClient, nodeName, oldBootID)
			})

		It("should emit expected FAR controller log messages during NHC-triggered remediation",
			reportxml.ID("70872"),
			Label(labels.TierAcceptance, labels.ComponentRemediation),
			func() {
				logStartTime := time.Now()
				nodeName, oldBootID := triggerNHCRemediation("nhc-far-logs", true)
				waitForRemediation(ctx, APIClient, nodeName, oldBootID)

				By("Fetching FAR controller logs since test start")

				logs := getFARControllerLogsSince(ctx, logStartTime)
				Expect(logs).ToNot(BeEmpty(), "Failed to retrieve FAR controller logs")

				By("Verifying expected log messages are present")

				expectedMessages := []string{
					"Remediation started",
					"Fence agent",
					"Taint was added",
					"Remediation finished",
				}

				for _, msg := range expectedMessages {
					Expect(logs).To(ContainSubstring(msg),
						"Expected log message %q not found in FAR controller logs", msg)
				}
			})

		It("should complete full NHC+FAR interop lifecycle",
			reportxml.ID("90159"),
			Label(labels.TierInterop, labels.ComponentRemediation),
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

					conditions, found, condErr := unstructured.NestedSlice(
						fresh.Object, "status", "conditions")
					g.Expect(condErr).ToNot(HaveOccurred())
					g.Expect(found).To(BeTrue(), "FAR CR has no status.conditions")

					succeeded := false

					for _, c := range conditions {
						cMap, ok := c.(map[string]interface{})
						if ok && cMap["type"] == farparams.FARConditionSucceeded &&
							cMap["status"] == string(metav1.ConditionTrue) {
							succeeded = true

							break
						}
					}

					g.Expect(succeeded).To(BeTrue(),
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

		It("should clean up FAR taints when NHC deletes the FAR CR after node recovery",
			reportxml.ID("90264"),
			Label(labels.TierAcceptance, labels.ComponentRemediation),
			func() {
				nodeName, oldBootID := triggerNHCRemediation("nhc-far-taint-cleanup", true)

				By("Verifying FAR NoSchedule taint is applied to " + nodeName)

				Eventually(func(g Gomega) {
					node := &corev1.Node{}
					g.Expect(APIClient.Get(ctx, client.ObjectKey{Name: nodeName}, node)).To(Succeed())

					found := false

					for _, taint := range node.Spec.Taints {
						if taint.Key == farparams.FARNoScheduleTaintKey {
							found = true

							break
						}
					}

					g.Expect(found).To(BeTrue(),
						"FAR NoSchedule taint not found on node %s", nodeName)
				}, farparams.FARConditionTimeout, farparams.DefaultPollInterval).Should(Succeed())

				By("Waiting for node to reboot and become Ready (fence_aws + kubelet restart)")
				waitForRemediation(ctx, APIClient, nodeName, oldBootID)

				By("Waiting for NHC to detect healthy node and delete FAR CR")

				Eventually(func() bool {
					farObj := &unstructured.Unstructured{}
					farObj.SetGroupVersionKind(farGVK)

					err := APIClient.Get(ctx, client.ObjectKey{
						Name: nodeName, Namespace: medik8sparams.OperatorNs,
					}, farObj)

					return k8serrors.IsNotFound(err)
				}, farparams.FARCRGoneTimeout, farparams.DefaultPollInterval).Should(BeTrue(),
					"NHC should delete FAR CR for %s via HandleHealthyNode after node recovery",
					nodeName)

				By("Asserting all FAR-applied taints are removed from " + nodeName)

				Eventually(func(g Gomega) {
					node := &corev1.Node{}
					g.Expect(APIClient.Get(ctx, client.ObjectKey{Name: nodeName}, node)).To(Succeed())

					for _, taint := range node.Spec.Taints {
						g.Expect(taint.Key).ToNot(Equal(farparams.FARNoScheduleTaintKey),
							"FAR NoSchedule taint still present on %s after CR deletion", nodeName)
						g.Expect(taint.Key).ToNot(Equal(farparams.FAROutOfServiceTaintKey),
							"out-of-service taint still present on %s after CR deletion", nodeName)
					}
				}, farparams.NHCTaintCleanupTimeout, farparams.DefaultPollInterval).Should(Succeed())

				By("Verifying taints stay absent (no NHC re-remediation)")

				Consistently(func(g Gomega) {
					node := &corev1.Node{}
					g.Expect(APIClient.Get(ctx, client.ObjectKey{Name: nodeName}, node)).To(Succeed())

					for _, taint := range node.Spec.Taints {
						g.Expect(taint.Key).ToNot(Equal(farparams.FARNoScheduleTaintKey))
					}
				}, 30*time.Second, farparams.DefaultPollInterval).Should(Succeed(),
					"FAR taint reappeared on %s within stabilization window", nodeName)
			})

		It("should delete FAR CR cleanly when target node is already gone",
			reportxml.ID("90265"),
			Label(labels.TierAcceptance, labels.ComponentRemediation),
			func() {
				By("Selecting a non-leader worker node")

				targetNode, err := helpers.SelectWorkerNode(ctx, APIClient, leaderNode)
				Expect(err).ToNot(HaveOccurred())

				currentTargetNode = targetNode.Name

				By("Creating FAR CR targeting " + targetNode.Name)

				farCR := buildFARUnstructured(targetNode.Name, fenceAgent, sharedParams, nodeParams)
				createFARCR(ctx, APIClient, farCR)

				By("Waiting for FAR NoSchedule taint on " + targetNode.Name)

				Eventually(func(g Gomega) {
					node := &corev1.Node{}
					g.Expect(APIClient.Get(ctx, client.ObjectKey{Name: targetNode.Name}, node)).To(Succeed())

					found := false

					for _, taint := range node.Spec.Taints {
						if taint.Key == farparams.FARNoScheduleTaintKey {
							found = true

							break
						}
					}

					g.Expect(found).To(BeTrue(),
						"FAR NoSchedule taint not found on %s", targetNode.Name)
				}, farparams.FARConditionTimeout, farparams.DefaultPollInterval).Should(Succeed())

				By("Verifying FAR CR has finalizer before proceeding")

				farObj := &unstructured.Unstructured{}
				farObj.SetGroupVersionKind(farGVK)
				Expect(APIClient.Get(ctx, client.ObjectKey{
					Name: targetNode.Name, Namespace: medik8sparams.OperatorNs,
				}, farObj)).To(Succeed())
				Expect(farObj.GetFinalizers()).ToNot(BeEmpty(),
					"FAR CR should have a finalizer before testing deletion path")

				By("Recording FAR controller pod restart count")

				farPods := &corev1.PodList{}
				Expect(APIClient.List(ctx, farPods,
					client.InNamespace(medik8sparams.OperatorNs),
					client.MatchingLabels(farparams.OperatorControllerPodLabels))).To(Succeed())

				preDeleteRestarts := int32(0)

				for i := range farPods.Items {
					for _, cs := range farPods.Items[i].Status.ContainerStatuses {
						preDeleteRestarts += cs.RestartCount
					}
				}

				By("Stopping kubelet on " + targetNode.Name + " to prevent node re-registration")

				Expect(stopKubeletForRemediation(ctx, targetNode.Name)).To(Succeed(),
					"Failed to stop kubelet on %s", targetNode.Name)

				By("Deleting Node object " + targetNode.Name)

				nodeObj := &corev1.Node{}
				Expect(APIClient.Get(ctx, client.ObjectKey{Name: targetNode.Name}, nodeObj)).To(Succeed())
				Expect(APIClient.Delete(ctx, nodeObj)).To(Succeed(),
					"Failed to delete Node %s", targetNode.Name)

				By("Verifying Node object is gone")

				Eventually(func() bool {
					err := APIClient.Get(ctx, client.ObjectKey{Name: targetNode.Name}, &corev1.Node{})

					return k8serrors.IsNotFound(err)
				}, 30*time.Second, farparams.DefaultPollInterval).Should(BeTrue(),
					"Node %s should be deleted", targetNode.Name)

				By("Deleting FAR CR to trigger nil-safe handleFARDeletion")

				deleteRemediationCR(ctx, APIClient, farGVK, targetNode.Name)

				By("Asserting FAR CR is fully deleted (finalizer cleared)")

				Eventually(func() bool {
					fresh := &unstructured.Unstructured{}
					fresh.SetGroupVersionKind(farGVK)

					err := APIClient.Get(ctx, client.ObjectKey{
						Name: targetNode.Name, Namespace: medik8sparams.OperatorNs,
					}, fresh)

					return k8serrors.IsNotFound(err)
				}, farparams.FARCRGoneTimeout, farparams.DefaultPollInterval).Should(BeTrue(),
					"FAR CR %s should be fully deleted after node removal", targetNode.Name)

				By("Verifying FAR controller did not crash during deletion")

				postDeleteRestarts := int32(0)

				Expect(APIClient.List(ctx, farPods,
					client.InNamespace(medik8sparams.OperatorNs),
					client.MatchingLabels(farparams.OperatorControllerPodLabels))).To(Succeed())

				for i := range farPods.Items {
					for _, cs := range farPods.Items[i].Status.ContainerStatuses {
						postDeleteRestarts += cs.RestartCount
					}
				}

				Expect(postDeleteRestarts).To(Equal(preDeleteRestarts),
					"FAR controller restarted during node-gone CR deletion "+
						"(pre=%d, post=%d)", preDeleteRestarts, postDeleteRestarts)

				By("Starting kubelet to allow node re-registration")
				startKubeletAfterRemediation(ctx, targetNode.Name)

				By("Waiting for node to re-register and become Ready")

				Eventually(func() bool {
					node := &corev1.Node{}
					err := APIClient.Get(ctx, client.ObjectKey{Name: targetNode.Name}, node)

					return err == nil && helpers.IsNodeReady(node)
				}, farparams.NodeReadyTimeout, farparams.DefaultPollInterval).Should(BeTrue(),
					"Node %s should re-register and become Ready after kubelet restart",
					targetNode.Name)
			})
	})

func buildNHCUnstructured(
	name, fartName, fartNamespace, labelValue string,
) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "remediation.medik8s.io/v1alpha1",
			"kind":       "NodeHealthCheck",
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
					"apiVersion": "fence-agents-remediation.medik8s.io/v1alpha1",
					"kind":       "FenceAgentsRemediationTemplate",
					"name":       fartName,
					"namespace":  fartNamespace,
				},
			},
		},
	}
}

func waitForNHCCreatedFAR(
	ctx context.Context, k8sClient client.Client, nodeName string,
) {
	Eventually(func(g Gomega) { //nolint:varnamelen
		farObj := &unstructured.Unstructured{}
		farObj.SetGroupVersionKind(farGVK)

		err := k8sClient.Get(ctx, client.ObjectKey{
			Name: nodeName, Namespace: medik8sparams.OperatorNs,
		}, farObj)
		g.Expect(err).ToNot(HaveOccurred(),
			"FAR CR for node %s not yet created by NHC", nodeName)
	}, farparams.NHCDetectionTimeout, farparams.DefaultPollInterval).Should(Succeed(),
		"NHC did not create FAR CR for node %s within %s",
		nodeName, farparams.NHCDetectionTimeout)
}

// waitForNHCEnabled polls the NHC CR's status.phase until it reaches "Enabled".
// NHC must be Enabled (watches set up, dynamic RBAC created) before the test
// makes the target node unhealthy, otherwise NHC may miss the event.
func waitForNHCEnabled(ctx context.Context, nhcName string) {
	Eventually(func(assertion Gomega) {
		nhcObj := &unstructured.Unstructured{}
		nhcObj.SetGroupVersionKind(nhcGVK)

		assertion.Expect(APIClient.Get(ctx, client.ObjectKey{Name: nhcName}, nhcObj)).To(Succeed())

		phase, found, err := unstructured.NestedString(nhcObj.Object, "status", "phase")
		assertion.Expect(err).ToNot(HaveOccurred())
		assertion.Expect(found).To(BeTrue(), "NHC %s has no status.phase yet", nhcName)
		assertion.Expect(phase).To(Equal("Enabled"),
			"NHC %s phase is %q, expected Enabled", nhcName, phase)
	}, 2*time.Minute, farparams.DefaultPollInterval).Should(Succeed(),
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
	logCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(logCtx, "oc", "logs",
		"-l", "app.kubernetes.io/name=node-healthcheck-operator",
		"-n", medik8sparams.OperatorNs,
		"-c", "manager",
		"--tail=50")

	output, err := cmd.CombinedOutput()
	if err != nil {
		GinkgoWriter.Printf("WARNING: failed to get NHC controller logs: %v\n", err)

		return
	}

	GinkgoWriter.Printf("NHC controller logs (last 50 lines):\n%s\n", string(output))

	rbacCtx, rbacCancel := context.WithTimeout(ctx, 15*time.Second)
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

// deleteNHCByName deletes a NodeHealthCheck CR by name if it exists.
// Used to remove the default nhc-worker-default CR that ships with the
// NHC operator and would conflict with custom NHC CRs via dedup logic.
func deleteNHCByName(ctx context.Context, name string) {
	nhcObj := &unstructured.Unstructured{}
	nhcObj.SetGroupVersionKind(nhcGVK)
	nhcObj.SetName(name)

	if err := APIClient.Delete(ctx, nhcObj); err != nil {
		if !k8serrors.IsNotFound(err) {
			GinkgoWriter.Printf("WARNING: failed to delete NHC %s: %v\n", name, err)
		}

		return
	}

	Eventually(func() bool {
		err := APIClient.Get(ctx, client.ObjectKey{Name: name}, nhcObj)

		return k8serrors.IsNotFound(err)
	}, 30*time.Second, farparams.DefaultPollInterval).Should(BeTrue(),
		"NHC %s was not fully deleted", name)

	GinkgoWriter.Printf("Deleted default NHC %s\n", name)
}

// stopKubeletForRemediation masks and stops kubelet on the target node.
// Uses "systemctl mask" to prevent systemd from restarting kubelet after
// the debug pod dies (which happens because stopping kubelet kills the
// pod that sent the stop command). Without masking, kubelet auto-restarts
// within ~25s, which is shorter than NHC's unhealthyConditions duration.
// Cleanup must call startKubeletAfterRemediation to unmask and restart.
func stopKubeletForRemediation(ctx context.Context, nodeName string) error {
	cmd := `systemctl mask kubelet && systemctl stop kubelet`

	_, err := helpers.RunOnNode(ctx, nodeName, farparams.OcDebugTimeout, "sh", "-c", cmd)
	if err == nil {
		GinkgoWriter.Printf("stopKubeletForRemediation(%s): kubelet masked and stopped\n", nodeName)

		return nil
	}

	errMsg := err.Error()

	if strings.Contains(errMsg, "connection reset") ||
		strings.Contains(errMsg, "lost connection") ||
		strings.Contains(errMsg, "closed network connection") ||
		strings.Contains(errMsg, "broken pipe") ||
		strings.Contains(errMsg, "transport is closing") ||
		(strings.Contains(errMsg, "oc debug on node") && strings.Contains(errMsg, "timed out")) ||
		(strings.Contains(errMsg, "exit status 1") && strings.Contains(errMsg, "Starting pod")) {
		GinkgoWriter.Printf(
			"stopKubeletForRemediation(%s): suppressed expected error "+
				"(kubelet likely masked+stopped): %v\n", nodeName, err)

		return nil
	}

	return err
}

// startKubeletAfterRemediation unmasks and starts kubelet on the target
// node. Used as cleanup safety net after stopKubeletForRemediation.
func startKubeletAfterRemediation(ctx context.Context, nodeName string) {
	cmd := `systemctl unmask kubelet && systemctl start kubelet`

	_, err := helpers.RunOnNode(ctx, nodeName, farparams.OcDebugTimeout, "sh", "-c", cmd)
	if err != nil {
		GinkgoWriter.Printf("WARNING: startKubeletAfterRemediation(%s): %v\n", nodeName, err)
	}
}

func getFARControllerLogsSince(ctx context.Context, since time.Time) string {
	sinceStr := since.UTC().Format(time.RFC3339)

	logCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(logCtx, "oc", "logs",
		"-l", farparams.OperatorControllerPodLabelSelector,
		"-n", medik8sparams.OperatorNs,
		"-c", farparams.ManagerContainerName,
		"--since-time="+sinceStr,
		"--tail=-1")

	output, err := cmd.CombinedOutput()
	if err != nil {
		GinkgoWriter.Printf("WARNING: failed to get FAR controller logs: %v\n%s\n",
			err, strings.TrimSpace(string(output)))

		return ""
	}

	return string(output)
}
