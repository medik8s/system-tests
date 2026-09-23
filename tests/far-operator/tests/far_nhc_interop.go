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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/deployment"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/pod"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/reportxml"

	"github.com/medik8s/system-tests/tests/far-operator/internal/farparams"
	"github.com/medik8s/system-tests/tests/far-operator/internal/farutils"
	"github.com/medik8s/system-tests/tests/internal/helpers"
	"github.com/medik8s/system-tests/tests/internal/labels"
	. "github.com/medik8s/system-tests/tests/internal/medik8sinittools"
	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
)

const (
	nhcDeploymentName             = "node-healthcheck-controller-manager"
	nhcOldDefaultName             = "nhc-worker-default"
	nhcControllerPodLabelSelector = "app.kubernetes.io/component=controller-manager," +
		"app.kubernetes.io/name=node-healthcheck-operator"
)

var nhcGVK = schema.GroupVersionKind{
	Group:   "remediation.medik8s.io",
	Version: "v1alpha1",
	Kind:    "NodeHealthCheck",
}

// nhcRemediationState holds the mutable test state produced by
// triggerNHCRemediation so the JustAfterEach cleanup can reference it.
type nhcRemediationState struct {
	targetNode      string
	nhcName         string
	farTemplateName string
	labelValue      string
	previousLabel   string
	hadLabel        bool
	labelApplied    bool
	farName         string
	oldBootID       string
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

			nhcState             nhcRemediationState
			secondNHCState       nhcRemediationState
			kubeletStopAttempted bool
			preservedDefaultNHC  *unstructured.Unstructured
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

			defaultNHC := &unstructured.Unstructured{}
			defaultNHC.SetGroupVersionKind(nhcGVK)

			err = APIClient.Get(ctx, client.ObjectKey{Name: nhcOldDefaultName}, defaultNHC)
			if err == nil {
				preservedDefaultNHC = defaultNHC.DeepCopy()
			} else {
				Expect(k8serrors.IsNotFound(err)).To(BeTrue())
			}

			Expect(deleteRemediationCR(ctx, APIClient, nhcGVK, nhcOldDefaultName)).To(Succeed(),
				"default NHC must be removed before running interop tests")
		})

		AfterAll(func() {
			if preservedDefaultNHC == nil {
				return
			}

			preservedDefaultNHC.SetResourceVersion("")
			preservedDefaultNHC.SetUID("")
			preservedDefaultNHC.SetCreationTimestamp(metav1.Time{})
			Expect(APIClient.Create(ctx, preservedDefaultNHC)).To(Succeed(),
				"failed to restore default NHC")
		})

		JustAfterEach(func() {
			if CurrentSpecReport().Failed() {
				GinkgoWriter.Println("Test failed - collecting diagnostics")
				logFARControllerState(ctx, APIClient)
				logNHCDiagnostics(ctx, nhcState.nhcName, nhcState.targetNode)
				logKubeletDiagnostics(ctx, nhcState.targetNode)
			}

			nhcRemoved := true

			if nhcState.nhcName != "" {
				By("Cleanup: deleting NHC " + nhcState.nhcName)

				if err := deleteRemediationCR(ctx, APIClient, nhcGVK, nhcState.nhcName); err != nil {
					message := fmt.Sprintf("failed to delete NHC %s: %v", nhcState.nhcName, err)
					GinkgoWriter.Printf("WARNING: %s\n", message)
					AddReportEntry("nhc-cleanup-delete-failed", message)

					nhcRemoved = false
				} else {
					nhcState.nhcName = ""
				}
			}

			if nhcRemoved && nhcState.farName != "" {
				By("Cleanup: waiting for FAR CR to reach Succeeded before deletion")

				pollCtx, pollCancel := context.WithTimeout(ctx, farparams.FARConditionTimeout)
				defer pollCancel()

				if pollErr := wait.PollUntilContextCancel(pollCtx, farparams.DefaultPollInterval, true,
					func(ctx context.Context) (bool, error) {
						farObj := &unstructured.Unstructured{}
						farObj.SetGroupVersionKind(farGVK)

						if err := APIClient.Get(ctx, client.ObjectKey{
							Name:      nhcState.farName,
							Namespace: medik8sparams.OperatorNs,
						}, farObj); err != nil {
							if k8serrors.IsNotFound(err) {
								return true, nil
							}

							return false, fmt.Errorf("get FAR CR %s: %w", nhcState.targetNode, err)
						}

						return farConditionSucceeded(farObj), nil
					},
				); pollErr != nil {
					message := fmt.Sprintf("FAR CR %s did not reach Succeeded or disappear before cleanup: %v",
						nhcState.targetNode, pollErr)
					GinkgoWriter.Printf("WARNING: %s\n", message)
					AddReportEntry("nhc-cleanup-far-wait-failed", message)
				}

				By("Cleanup: deleting FAR CR for " + nhcState.targetNode)

				if err := deleteRemediationCR(ctx, APIClient, farGVK, nhcState.farName); err != nil {
					message := fmt.Sprintf("failed to delete FAR CR %s: %v", nhcState.farName, err)
					GinkgoWriter.Printf("WARNING: %s\n", message)
					AddReportEntry("nhc-cleanup-far-delete-failed", message)
				}

				nhcState.farName = ""
			}

			if nhcRemoved && nhcState.farTemplateName != "" {
				By("Cleanup: deleting FAR template " + nhcState.farTemplateName)

				if err := deleteRemediationCR(ctx, APIClient, farTemplateGVK, nhcState.farTemplateName); err != nil {
					message := fmt.Sprintf("failed to delete FAR template %s: %v", nhcState.farTemplateName, err)
					GinkgoWriter.Printf("WARNING: %s\n", message)
					AddReportEntry("nhc-cleanup-template-delete-failed", message)
				} else {
					nhcState.farTemplateName = ""
				}
			}

			if nhcState.targetNode != "" && nhcState.labelApplied {
				By("Cleanup: removing interop label from " + nhcState.targetNode)

				updateErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
					node := &corev1.Node{}
					if err := APIClient.Get(ctx, client.ObjectKey{
						Name: nhcState.targetNode,
					}, node); err != nil {
						if k8serrors.IsNotFound(err) {
							return nil
						}

						return err
					}

					if nhcState.hadLabel {
						node.Labels[farparams.NHCInteropLabelKey] = nhcState.previousLabel
					} else {
						delete(node.Labels, farparams.NHCInteropLabelKey)
					}

					return APIClient.Update(ctx, node)
				})
				if updateErr != nil {
					message := fmt.Sprintf("failed to remove interop label from %s: %v",
						nhcState.targetNode, updateErr)
					GinkgoWriter.Printf("WARNING: %s\n", message)
					AddReportEntry("nhc-cleanup-label-removal-failed", message)
				}

				nhcState.labelValue = ""
				nhcState.labelApplied = false
			}

			if nhcState.targetNode != "" {
				nodeName := nhcState.targetNode
				nhcState.targetNode = ""

				if kubeletStopAttempted {
					By("Cleanup: unmasking and restarting kubelet on " + nodeName)
					startKubeletAfterRemediation(ctx, nodeName)

					kubeletStopAttempted = false
				}

				By("Cleanup: waiting for " + nodeName + " to become Ready")

				if err := farutils.WaitForNodeReady(
					ctx, APIClient, nodeName,
					farparams.NodeReadyTimeout, GinkgoWriter.Printf); err != nil {
					message := fmt.Sprintf("node %s did not become Ready within %s: %v",
						nodeName, farparams.NodeReadyTimeout, err)
					GinkgoWriter.Printf("WARNING: %s\n", message)
					AddReportEntry("nhc-cleanup-node-recovery-failed", message)
				}
			}

			if secondNHCState.targetNode != "" {
				By("Cleanup: restoring second NHC target " + secondNHCState.targetNode)

				if secondNHCState.farName != "" {
					if err := deleteRemediationCR(ctx, APIClient, farGVK, secondNHCState.farName); err != nil {
						message := fmt.Sprintf("failed to delete second FAR CR %s: %v", secondNHCState.farName, err)
						GinkgoWriter.Printf("WARNING: %s\n", message)
						AddReportEntry("nhc-cleanup-second-far-delete-failed", message)
					}
				}

				node := &corev1.Node{}
				if err := APIClient.Get(ctx, client.ObjectKey{Name: secondNHCState.targetNode}, node); err == nil {
					if secondNHCState.hadLabel {
						node.Labels[farparams.NHCInteropLabelKey] = secondNHCState.previousLabel
					} else {
						delete(node.Labels, farparams.NHCInteropLabelKey)
					}

					if updateErr := APIClient.Update(ctx, node); updateErr != nil {
						message := fmt.Sprintf("failed to restore label on second target %s: %v",
							secondNHCState.targetNode, updateErr)
						GinkgoWriter.Printf("WARNING: %s\n", message)
						AddReportEntry("nhc-cleanup-second-label-restore-failed", message)
					}
				}

				startKubeletAfterRemediation(ctx, secondNHCState.targetNode)

				if err := farutils.WaitForNodeReady(ctx, APIClient, secondNHCState.targetNode,
					farparams.NodeReadyTimeout, GinkgoWriter.Printf); err != nil {
					message := fmt.Sprintf("second target node %s did not become Ready within %s: %v",
						secondNHCState.targetNode, farparams.NodeReadyTimeout, err)
					GinkgoWriter.Printf("WARNING: %s\n", message)
					AddReportEntry("nhc-cleanup-second-node-recovery-failed", message)
				}

				secondNHCState = nhcRemediationState{}
			}
		})

		// Keep OCP-61309 separate for its happy-path traceability; OCP-90159
		// extends the same flow with lifecycle assertions.
		It("should remediate unhealthy node when NHC uses a FAR template",
			reportxml.ID("61309"),
			Label(labels.TierAcceptance),
			func() {
				By("Triggering NHC remediation through a FAR template")
				ensureDestructiveWorkerCapacity(ctx, APIClient)

				nhcState = nhcRemediationState{}
				triggerNHCRemediation(ctx, APIClient, &nhcState, &kubeletStopAttempted, leaderNode, fenceAgent,
					"nhc-far", true, sharedParams, nodeParams)
				triggerSecondNHCRemediation(ctx, APIClient, &secondNHCState, &kubeletStopAttempted,
					leaderNode, nhcState.targetNode, nhcState.nhcName, nhcState.labelValue)

				By("Verifying both nodes rebooted and recovered")

				waitForRemediation(ctx, APIClient, nhcState.targetNode, nhcState.oldBootID)
				waitForRemediation(ctx, APIClient, secondNHCState.targetNode, secondNHCState.oldBootID)
			})

		It("should default to reboot when FAR template omits action",
			reportxml.ID("66204"),
			Label(labels.TierAcceptance),
			func() {
				By("Triggering NHC remediation through a FAR template without an action")

				nhcState = nhcRemediationState{}
				triggerNHCRemediation(ctx, APIClient, &nhcState, &kubeletStopAttempted, leaderNode, fenceAgent,
					"nhc-far-noaction", false, sharedParams, nodeParams)

				By("Verifying the node rebooted and recovered")

				waitForRemediation(ctx, APIClient, nhcState.targetNode, nhcState.oldBootID)
			})

		It("should emit expected FAR controller log messages during NHC-triggered remediation",
			reportxml.ID("70872"),
			Label(labels.TierAcceptance),
			func() {
				logStartTime := time.Now()

				nhcState = nhcRemediationState{}
				triggerNHCRemediation(ctx, APIClient, &nhcState, &kubeletStopAttempted, leaderNode, fenceAgent,
					"nhc-far-logs", true, sharedParams, nodeParams)

				waitForRemediation(ctx, APIClient, nhcState.targetNode, nhcState.oldBootID)

				By("Fetching FAR controller logs since test start")

				By("Verifying expected log messages are present")

				expectedMessages := []string{
					"Finalizer was added",
					"Execute the fence agent",
					"FAR remediation taint was added",
					"FenceAgentsRemediation CR has completed to remediate the node",
				}

				Eventually(func(g Gomega) {
					logs := getFARControllerLogsSince(ctx, logStartTime, nhcState.targetNode)
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
				nhcState = nhcRemediationState{}
				triggerNHCRemediation(ctx, APIClient, &nhcState, &kubeletStopAttempted, leaderNode, fenceAgent,
					"nhc-far-lifecycle", true, sharedParams, nodeParams)

				By("Verifying FAR CR exists for " + nhcState.targetNode)

				farObj := &unstructured.Unstructured{}
				farObj.SetGroupVersionKind(farGVK)
				Expect(APIClient.Get(ctx, client.ObjectKey{
					Name: nhcState.farName, Namespace: medik8sparams.OperatorNs,
				}, farObj)).To(Succeed(),
					"FAR CR should exist while remediation is in progress")

				By("Waiting for node to reboot")
				Expect(farutils.WaitForNodeReboot(
					ctx, APIClient, nhcState.targetNode, nhcState.oldBootID,
					farparams.NodeRebootTimeout, GinkgoWriter.Printf)).To(Succeed(),
					"Node %s did not reboot", nhcState.targetNode)

				By("Verifying FAR CR reached Succeeded or was cleaned up by NHC")

				Eventually(func() error {
					fresh := &unstructured.Unstructured{}
					fresh.SetGroupVersionKind(farGVK)

					err := APIClient.Get(ctx, client.ObjectKey{
						Name: nhcState.farName, Namespace: medik8sparams.OperatorNs,
					}, fresh)
					if k8serrors.IsNotFound(err) {
						// NHC deletes the remediation CR once the node recovers.
						// NotFound after a confirmed reboot is equivalent to success.
						return nil
					}

					if err != nil {
						return err
					}

					if !farConditionSucceeded(fresh) {
						return fmt.Errorf("FAR CR %s exists but has not reached Succeeded", nhcState.farName)
					}

					return nil
				}, farparams.FARConditionTimeout, farparams.DefaultPollInterval).Should(Succeed(),
					"FAR CR did not reach Succeeded or get cleaned up by NHC")

				By("Verifying node is Ready, schedulable, and has no FAR taint")

				Eventually(func(g Gomega) { //nolint:varnamelen
					node := &corev1.Node{}
					g.Expect(APIClient.Get(ctx, client.ObjectKey{
						Name: nhcState.targetNode,
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

//nolint:funlen // orchestration helper; splitting would fragment the remediation flow
func triggerNHCRemediation(
	ctx context.Context,
	apiClient client.Client,
	state *nhcRemediationState,
	kubeletStopAttempted *bool,
	leaderNode, fenceAgent, testPrefix string,
	includeAction bool,
	sharedParams, nodeParams map[string]interface{},
) {
	GinkgoHelper()

	By("Selecting a non-leader worker node")

	targetNode, err := selectDedicatedWorkerNode(ctx, apiClient, leaderNode)
	Expect(err).ToNot(HaveOccurred())

	GinkgoWriter.Printf("Selected target node: %s\n", targetNode.Name)

	By("Labeling target node " + targetNode.Name + " for NHC scope")

	labelValue := fmt.Sprintf("%s-%d", testPrefix, time.Now().UnixMilli())
	state.targetNode = targetNode.Name
	state.labelValue = labelValue

	node := &corev1.Node{}
	Expect(apiClient.Get(ctx, client.ObjectKey{Name: targetNode.Name}, node)).To(Succeed())

	if node.Labels == nil {
		node.Labels = make(map[string]string)
	}

	state.previousLabel, state.hadLabel = node.Labels[farparams.NHCInteropLabelKey]

	node.Labels[farparams.NHCInteropLabelKey] = labelValue
	Expect(apiClient.Update(ctx, node)).To(Succeed())

	state.labelApplied = true

	By("Cleaning CRI-O overlay on " + targetNode.Name)
	removeWorkloadImage(ctx, targetNode.Name)

	By("Recording boot ID before remediation")

	oldBootID, err := farutils.GetNodeBootIDFromAPI(ctx, apiClient, targetNode.Name)
	Expect(err).ToNot(HaveOccurred())

	farTemplateName := fmt.Sprintf("far-template-%s", testPrefix)
	state.farTemplateName = farTemplateName

	By("Creating FAR template " + farTemplateName)

	farTemplateSharedParams := make(map[string]interface{}, len(sharedParams))
	for k, v := range sharedParams {
		if !includeAction && k == "--action" {
			continue
		}

		farTemplateSharedParams[k] = v
	}

	farTemplate := buildFARTemplateUnstructured(farTemplateName, fenceAgent, farTemplateSharedParams, nodeParams)
	Expect(deleteRemediationCR(ctx, apiClient, farTemplateGVK, farTemplateName)).To(Succeed())
	Expect(apiClient.Create(ctx, farTemplate)).To(Succeed(),
		"Failed to create FAR template %s", farTemplateName)

	nhcName := fmt.Sprintf("nhc-%s", testPrefix)
	state.nhcName = nhcName

	By("Creating NHC " + nhcName + " pointing to FAR template " + farTemplateName)

	nhc := buildNHCUnstructured(nhcName, farTemplateName, medik8sparams.OperatorNs, labelValue)

	Expect(deleteRemediationCR(ctx, apiClient, nhcGVK, nhcName)).To(Succeed())

	By("Removing any stale FAR remediation for " + targetNode.Name)
	Expect(deleteRemediationCR(ctx, apiClient, farGVK, targetNode.Name)).To(Succeed())

	Expect(apiClient.Create(ctx, nhc)).To(Succeed(),
		"Failed to create NHC %s", nhcName)
	Expect(apiClient.Get(ctx, client.ObjectKey{Name: nhcName}, nhc)).To(Succeed(),
		"Failed to read created NHC %s", nhcName)

	By("Waiting for NHC " + nhcName + " to reach Enabled phase")
	waitForNHCEnabled(ctx, nhcName)

	By("Stopping kubelet on " + targetNode.Name)

	*kubeletStopAttempted = true

	Expect(stopKubeletForRemediation(
		ctx, targetNode.Name)).To(Succeed(),
		"Failed to stop kubelet on %s", targetNode.Name)

	By("Verifying " + targetNode.Name + " becomes NotReady")

	Expect(farutils.WaitForNodeNotReady(
		ctx, apiClient, targetNode.Name,
		farparams.NodeNotReadyTimeout, GinkgoWriter.Printf)).To(Succeed(),
		"Node %s did not become NotReady after kubelet stop", targetNode.Name)

	By("Waiting for NHC to create FAR CR for " + targetNode.Name)

	state.oldBootID = oldBootID
	state.farName = waitForNHCCreatedFAR(ctx, apiClient, targetNode.Name, nhc.GetUID())
}

func triggerSecondNHCRemediation(
	ctx context.Context,
	apiClient client.Client,
	state *nhcRemediationState,
	kubeletStopAttempted *bool,
	leaderNode, firstTargetNode, nhcName, labelValue string,
) {
	GinkgoHelper()

	By("Selecting a second non-leader worker node")

	targetNode, err := selectDedicatedWorkerNode(ctx, apiClient, leaderNode, firstTargetNode)
	Expect(err).ToNot(HaveOccurred())

	state.targetNode = targetNode.Name
	state.labelValue = labelValue

	node := &corev1.Node{}
	Expect(apiClient.Get(ctx, client.ObjectKey{Name: targetNode.Name}, node)).To(Succeed())

	if node.Labels == nil {
		node.Labels = make(map[string]string)
	}

	state.previousLabel, state.hadLabel = node.Labels[farparams.NHCInteropLabelKey]
	node.Labels[farparams.NHCInteropLabelKey] = labelValue
	Expect(apiClient.Update(ctx, node)).To(Succeed())

	state.labelApplied = true

	removeWorkloadImage(ctx, targetNode.Name)
	state.oldBootID, err = farutils.GetNodeBootIDFromAPI(ctx, apiClient, targetNode.Name)
	Expect(err).ToNot(HaveOccurred())

	*kubeletStopAttempted = true

	Expect(stopKubeletForRemediation(ctx, targetNode.Name)).To(Succeed(),
		"Failed to stop kubelet on %s", targetNode.Name)
	Expect(farutils.WaitForNodeNotReady(ctx, apiClient, targetNode.Name,
		farparams.NodeNotReadyTimeout, GinkgoWriter.Printf)).To(Succeed(),
		"Node %s did not become NotReady after kubelet stop", targetNode.Name)

	nhc := &unstructured.Unstructured{}
	nhc.SetGroupVersionKind(nhcGVK)
	Expect(apiClient.Get(ctx, client.ObjectKey{Name: nhcName}, nhc)).To(Succeed())
	state.farName = waitForNHCCreatedFAR(ctx, apiClient, targetNode.Name, nhc.GetUID())
}

func selectDedicatedWorkerNode(
	ctx context.Context, apiClient client.Client, excludedNodes ...string,
) (*corev1.Node, error) {
	workers, err := helpers.ListSchedulableWorkerNodes(ctx, apiClient)
	if err != nil {
		return nil, err
	}

	for index := range workers {
		isExcluded := false

		for _, excludedNode := range excludedNodes {
			if workers[index].Name == excludedNode {
				isExcluded = true

				break
			}
		}

		if !isExcluded {
			return &workers[index], nil
		}
	}

	return nil, fmt.Errorf("no eligible dedicated Ready worker node found")
}

func buildNHCUnstructured(
	name, farTemplateName, farTemplateNamespace, labelValue string,
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
					"apiVersion": farTemplateGVK.GroupVersion().String(),
					"kind":       farTemplateGVK.Kind,
					"name":       farTemplateName,
					"namespace":  farTemplateNamespace,
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
	ctx context.Context, k8sClient client.Client, nodeName string, nhcUID types.UID,
) string {
	GinkgoHelper()

	var farName string

	Eventually(func() (bool, error) {
		farList := &unstructured.UnstructuredList{}
		farList.SetGroupVersionKind(farGVK.GroupVersion().WithKind(farGVK.Kind + "List"))

		if err := k8sClient.List(ctx, farList, client.InNamespace(medik8sparams.OperatorNs)); err != nil {
			return false, fmt.Errorf("list FAR CRs created by NHC UID %s: %w", nhcUID, err)
		}

		for index := range farList.Items {
			farObj := &farList.Items[index]
			if farObj.GetName() != nodeName && !strings.HasPrefix(farObj.GetName(), nodeName+"-") {
				continue
			}

			for _, owner := range farObj.GetOwnerReferences() {
				if owner.Kind == nhcGVK.Kind && owner.UID == nhcUID {
					farName = farObj.GetName()

					return true, nil
				}
			}
		}

		return false, nil
	}, farparams.NHCDetectionTimeout, farparams.DefaultPollInterval).Should(BeTrue(),
		"NHC did not create FAR CR for node %s within %s",
		nodeName, farparams.NHCDetectionTimeout)

	return farName
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
	controllerPods, err := pod.List(APIClient, medik8sparams.OperatorNs,
		metav1.ListOptions{LabelSelector: nhcControllerPodLabelSelector})
	if err != nil {
		GinkgoWriter.Printf("WARNING: failed to list NHC controller pods: %v\n", err)

		return
	}

	if len(controllerPods) == 0 {
		GinkgoWriter.Printf("WARNING: no NHC controller pods found with selector %q\n",
			nhcControllerPodLabelSelector)

		return
	}

	for i := range controllerPods {
		controllerPod := controllerPods[i]
		for _, container := range controllerPod.Object.Spec.Containers {
			logs, logErr := getControllerContainerLogs(
				APIClient, controllerPod.Object.Name, container.Name, medik8sparams.OperatorNs)
			if logErr != nil {
				GinkgoWriter.Printf("WARNING: failed to get NHC logs for pod %s container %s: %v\n",
					controllerPod.Object.Name, container.Name, logErr)

				continue
			}

			GinkgoWriter.Printf("NHC controller logs (pod=%s container=%s):\n%s\n",
				controllerPod.Object.Name, container.Name, tailLines(logs, farparams.DiagnosticsLogTailLines))
		}
	}

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

func logKubeletDiagnostics(ctx context.Context, nodeName string) {
	if nodeName == "" {
		return
	}

	GinkgoWriter.Println("=== Kubelet Diagnostics ===")

	diagnosticCommands := []string{
		"sudo systemctl status kubelet --no-pager",
		"sudo systemctl is-enabled kubelet",
		"pgrep -a kubelet",
		"sudo journalctl -u kubelet -n 100 --no-pager",
	}

	for _, diagnosticCommand := range diagnosticCommands {
		output, err := helpers.RunSSHCommand(
			ctx, APIClient, nodeName, farparams.SSHTimeout, diagnosticCommand)
		if err != nil {
			GinkgoWriter.Printf("%s failed: %v\n", diagnosticCommand, err)

			continue
		}

		GinkgoWriter.Printf("$ %s\n%s\n", diagnosticCommand, output)
	}

	GinkgoWriter.Println("=== End Kubelet Diagnostics ===")
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
