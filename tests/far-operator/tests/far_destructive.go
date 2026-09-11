package tests

import (
	"context"
	"fmt"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/clients"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/deployment"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/reportxml"

	"github.com/medik8s/system-tests/tests/far-operator/internal/farparams"
	"github.com/medik8s/system-tests/tests/far-operator/internal/farutils"
	"github.com/medik8s/system-tests/tests/internal/helpers"
	"github.com/medik8s/system-tests/tests/internal/labels"
	. "github.com/medik8s/system-tests/tests/internal/medik8sinittools"
	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
)

var farGVK = schema.GroupVersionKind{
	Group:   "fence-agents-remediation.medik8s.io",
	Version: "v1alpha1",
	Kind:    "FenceAgentsRemediation",
}

var farTemplateGVK = schema.GroupVersionKind{
	Group:   "fence-agents-remediation.medik8s.io",
	Version: "v1alpha1",
	Kind:    "FenceAgentsRemediationTemplate",
}

var _ = Describe("FAR Destructive Tests",
	Serial,
	Label(labels.OperatorFAR, farparams.Label,
		labels.DisruptionDestructive,
		labels.PlatformAWS, labels.FrequencyWeekly),
	func() {
		var (
			ctx                    context.Context
			platform               configv1.PlatformType
			region                 string
			fenceAgent             string
			leaderNode             string
			targetNode             *corev1.Node
			sharedParams           map[string]interface{}
			nodeParams             map[string]interface{}
			currentFARTemplateName string
			currentFARName         string

			destructiveSetupDone    bool
			destructiveSetupSkipped bool
		)

		BeforeEach(func() {
			if destructiveSetupSkipped {
				Skip("FAR destructive tests require AWS")
			}

			if destructiveSetupDone {
				return
			}

			ctx = context.Background()

			By("Detecting cluster platform")

			var err error

			platform, region, err = helpers.DetectPlatform(ctx, APIClient)
			Expect(err).ToNot(HaveOccurred())

			if platform != configv1.AWSPlatformType {
				destructiveSetupSkipped = true

				Skip(fmt.Sprintf(
					"FAR destructive tests require AWS, got %s", platform))
			}

			By("Resolving fence agent for platform")

			fenceAgent, _, err = farutils.FenceAgentForPlatform(platform)
			Expect(err).ToNot(HaveOccurred())
			GinkgoWriter.Printf(
				"Platform: %s, Agent: %s, Region: %s\n",
				platform, fenceAgent, region)

			By("Verifying FAR operator deployment is ready")

			farDeployment, err := deployment.Pull(
				APIClient, farparams.OperatorDeploymentName, medik8sparams.OperatorNs)
			Expect(err).ToNot(HaveOccurred(), "Failed to get FAR deployment")
			Expect(farDeployment.IsReady(medik8sparams.DefaultTimeout)).To(BeTrue(),
				"FAR deployment is not Ready")

			By("Reading AWS credentials from CCO Secret")

			awsAccessKey, awsSecretKey, err := farutils.GetAWSCredentials(
				ctx, APIClient, medik8sparams.OperatorNs)
			Expect(err).ToNot(HaveOccurred(),
				"AWS credentials must be provisioned by the "+
					"medik8s-aws-credentials CI step")

			By("Creating shared credentials Secret for FAR SharedSecretName")

			credentialsSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      farparams.SharedCredentialsSecretName,
					Namespace: medik8sparams.OperatorNs,
				},
				StringData: map[string]string{
					"--access-key": awsAccessKey,
					"--secret-key": awsSecretKey,
				},
			}

			err = APIClient.Create(ctx, credentialsSecret)
			if err != nil && !k8serrors.IsAlreadyExists(err) {
				Expect(err).ToNot(HaveOccurred(),
					"Failed to create shared credentials Secret")
			}

			By("Building fence_aws shared parameters")

			sharedParams = map[string]interface{}{
				"--region":          region,
				"--action":          "reboot",
				"--skip-race-check": "",
			}

			By("Building node parameters (--plug = EC2 instance ID)")

			awsNodeParams, err := farutils.BuildAWSNodeParameters(
				ctx, APIClient)
			Expect(err).ToNot(HaveOccurred())

			nodeParams = make(map[string]interface{})

			for paramName, nodeMap := range awsNodeParams {
				inner := make(map[string]interface{}, len(nodeMap))
				for nodeName, val := range nodeMap {
					inner[nodeName] = val
				}

				nodeParams[paramName] = inner
			}

			By("Identifying active FAR controller node")

			Eventually(func() error {
				var leaderErr error

				leaderNode, leaderErr = farutils.GetActiveFARControllerNode(ctx, APIClient)

				return leaderErr
			}, farparams.ControllerHandoverTimeout, farparams.DefaultPollInterval).Should(Succeed(),
				"FAR leader election did not settle")
			GinkgoWriter.Printf("FAR leader is on node: %s\n", leaderNode)

			destructiveSetupDone = true
		})

		JustAfterEach(func() {
			spec := CurrentSpecReport()
			if spec.Failed() {
				GinkgoWriter.Println(
					"Test failed - collecting diagnostics")
				logFARControllerState(ctx, APIClient)
				logFARRemediationDiagnostics(ctx, APIClient, currentFARName)
			}

			if currentFARName != "" {
				By("Waiting for FAR CR to reach Succeeded before cleanup")

				pollCtx, pollCancel := context.WithTimeout(ctx, farparams.FARConditionTimeout)
				defer pollCancel()

				if waitErr := wait.PollUntilContextCancel(pollCtx, farparams.DefaultPollInterval, true,
					func(ctx context.Context) (bool, error) {
						farObj := &unstructured.Unstructured{}
						farObj.SetGroupVersionKind(farGVK)

						if err := APIClient.Get(ctx, client.ObjectKey{
							Name:      currentFARName,
							Namespace: medik8sparams.OperatorNs,
						}, farObj); err != nil {
							return false, nil
						}

						conditions, found, condErr := unstructured.NestedSlice(
							farObj.Object, "status", "conditions")
						if condErr != nil {
							GinkgoWriter.Printf(
								"WARNING: failed to read FAR CR conditions: %v\n", condErr)

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
					},
				); waitErr != nil {
					GinkgoWriter.Printf(
						"WARNING: FAR CR %s did not reach Succeeded within %s: %v\n",
						currentFARName, farparams.FARConditionTimeout, waitErr)
				}

				By("Deleting FAR CR " + currentFARName)
				farNodeName := currentFARName
				deleteRemediationCR(ctx, APIClient, farGVK, currentFARName)
				currentFARName = ""

				By("Verifying FAR NoSchedule taint removed after CR deletion")

				taintCtx, taintCancel := context.WithTimeout(ctx, farparams.FARConditionTimeout)
				defer taintCancel()

				if taintErr := wait.PollUntilContextCancel(taintCtx, farparams.DefaultPollInterval, true,
					func(ctx context.Context) (bool, error) {
						node := &corev1.Node{}
						if err := APIClient.Get(ctx, client.ObjectKey{Name: farNodeName}, node); err != nil {
							return false, nil
						}

						for _, taint := range node.Spec.Taints {
							if taint.Key == farparams.FARNoScheduleTaintKey {
								return false, nil
							}
						}

						return true, nil
					},
				); taintErr != nil {
					GinkgoWriter.Printf(
						"WARNING: FAR taint %s still present on node %s after %s: %v\n",
						farparams.FARNoScheduleTaintKey, farNodeName,
						farparams.FARConditionTimeout, taintErr)
				}
			}

			if currentFARTemplateName != "" {
				By("Safety net: deleting FARTemplate " + currentFARTemplateName)
				deleteRemediationCR(ctx, APIClient, farTemplateGVK, currentFARTemplateName)
				currentFARTemplateName = ""
			}

			if targetNode != nil {
				nodeName := targetNode.Name
				targetNode = nil

				By("Safety net: waiting for node " + nodeName + " to become Ready")

				if err := farutils.WaitForNodeReady(
					ctx, APIClient, nodeName,
					farparams.NodeReadyTimeout, GinkgoWriter.Printf); err != nil {
					GinkgoWriter.Printf(
						"WARNING: safety net: node %s did not become Ready within %s: %v\n",
						nodeName, farparams.NodeReadyTimeout, err)
					AddReportEntry("safety-net-recovery-failed",
						fmt.Sprintf("node %s did not recover: %v", nodeName, err))
				}
			}
		})

		Context("Standalone FAR remediation", func() {
			BeforeEach(func() {
				By("Verifying minimum Ready worker nodes for destructive tests")

				workerCount, err := helpers.CountReadyWorkerNodes(ctx, APIClient)
				Expect(err).ToNot(HaveOccurred())

				if workerCount < farparams.MinWorkersForDestructiveTests {
					Skip(fmt.Sprintf(
						"Standalone destructive tests require at least %d Ready worker nodes, found %d",
						farparams.MinWorkersForDestructiveTests, workerCount))
				}

				By("Verifying FAR controller is Ready before test")

				farDeployment, err := deployment.Pull(
					APIClient, farparams.OperatorDeploymentName, medik8sparams.OperatorNs)
				Expect(err).ToNot(HaveOccurred())
				Expect(farDeployment.IsReady(medik8sparams.DefaultTimeout)).To(BeTrue(),
					"FAR controller is not Ready - webhook will be unreachable")

				By("Finding active leader node")

				Eventually(func() error {
					var leaderErr error

					leaderNode, leaderErr = farutils.GetActiveFARControllerNode(ctx, APIClient)

					return leaderErr
				}, farparams.ControllerHandoverTimeout, farparams.DefaultPollInterval).Should(Succeed(),
					"FAR leader election did not settle - lease may point to a replaced pod")
				GinkgoWriter.Printf("FAR controller Ready, leader on node: %s\n", leaderNode)
			})

			Context("non-leader worker target", func() {
				var (
					oldBootID   string
					workloadPod *corev1.Pod
				)

				BeforeEach(func() {
					By("Selecting a non-leader worker node")

					var err error

					targetNode, err = helpers.SelectWorkerNode(ctx, APIClient, leaderNode)
					Expect(err).ToNot(HaveOccurred())

					By("Cleaning CRI-O overlay storage on " + targetNode.Name)
					removeWorkloadImage(ctx, targetNode.Name)

					By("Recording boot ID before remediation")

					oldBootID, err = farutils.GetNodeBootIDFromAPI(ctx, APIClient, targetNode.Name)
					Expect(err).ToNot(HaveOccurred())

					By("Creating a test workload pod pinned to " + targetNode.Name)

					workloadPod = createWorkloadPod(ctx, APIClient, targetNode.Name)
				})

				JustAfterEach(func() {
					if CurrentSpecReport().Failed() {
						logPodDiagnostics(ctx, APIClient, workloadPod)

						return
					}

					By("Verifying workload pod was deleted or evicted")

					Eventually(func() bool {
						pod := &corev1.Pod{}
						err := APIClient.Get(ctx, client.ObjectKey{
							Name: workloadPod.Name, Namespace: workloadPod.Namespace,
						}, pod)

						return k8serrors.IsNotFound(err) || pod.DeletionTimestamp != nil
					}, farparams.WorkloadEvictionTimeout, farparams.DefaultPollInterval).Should(BeTrue(),
						"Workload pod was not deleted/evicted after remediation")
				})

				It("should remediate a worker node via standalone FAR CR",
					Label(labels.TierAcceptance, labels.ComponentRemediation),
					reportxml.ID("61229"),
					func() {
						creationTimestamp := targetNode.CreationTimestamp

						By("Creating FAR CR targeting " + targetNode.Name)

						farCR := buildFARUnstructured(targetNode.Name, fenceAgent, sharedParams, nodeParams)
						createFARCR(ctx, APIClient, farCR)

						currentFARName = targetNode.Name

						waitForRemediation(ctx, APIClient, targetNode.Name, oldBootID)

						By("Verifying node was rebooted, not re-created")

						node := &corev1.Node{}
						Expect(APIClient.Get(ctx, client.ObjectKey{Name: targetNode.Name}, node)).To(Succeed())
						Expect(node.CreationTimestamp.Equal(&creationTimestamp)).To(BeTrue(),
							"Node creation timestamp changed - node was re-created instead of rebooted")

						By("Verifying FAR lifecycle events on CR")

						Expect(helpers.WaitForEvents(ctx, APIClient.K8sClient,
							helpers.InvolvedObjectRef{
								Kind:      "FenceAgentsRemediation",
								Name:      targetNode.Name,
								Namespace: medik8sparams.OperatorNs,
								UID:       string(farCR.GetUID()),
							},
							[]helpers.EventExpectation{
								{Reason: farparams.FAREventRemediationStarted, Type: corev1.EventTypeNormal},
								{Reason: farparams.FAREventFenceAgentSucceeded, Type: corev1.EventTypeNormal},
								{Reason: farparams.FAREventRemediationFinished, Type: corev1.EventTypeNormal},
							},
							farparams.EventVerifyTimeout, farparams.EventVerifyInterval,
						)).To(Succeed(), "FAR lifecycle events not found on CR %s", targetNode.Name)

						By("Verifying remediation completion event on Node")

						Expect(helpers.WaitForEvents(ctx, APIClient.K8sClient,
							helpers.InvolvedObjectRef{
								Kind: "Node",
								Name: targetNode.Name,
								UID:  string(node.GetUID()),
							},
							[]helpers.EventExpectation{
								{Reason: farparams.FAREventNodeRemediationCompleted, Type: corev1.EventTypeNormal},
							},
							farparams.EventVerifyTimeout, farparams.EventVerifyInterval,
						)).To(Succeed(), "NodeRemediationCompleted event not found on node %s", targetNode.Name)
					})

				It("should apply FAR NoSchedule taint during remediation",
					Label(labels.TierAcceptance, labels.ComponentRemediation),
					reportxml.ID("65960"),
					func() {
						By("Creating FAR CR targeting " + targetNode.Name)

						farCR := buildFARUnstructured(targetNode.Name, fenceAgent, sharedParams, nodeParams)
						createFARCR(ctx, APIClient, farCR)

						currentFARName = targetNode.Name

						By("Verifying FAR NoSchedule taint is applied to the node")

						Eventually(func(assertion Gomega) {
							node := &corev1.Node{}
							assertion.Expect(APIClient.Get(ctx, client.ObjectKey{Name: targetNode.Name}, node)).To(Succeed())

							found := false

							for _, taint := range node.Spec.Taints {
								if taint.Key == farparams.FARNoScheduleTaintKey &&
									taint.Effect == corev1.TaintEffectNoSchedule {
									found = true

									break
								}
							}

							assertion.Expect(found).To(BeTrue(),
								"FAR NoSchedule taint %s not found on node %s",
								farparams.FARNoScheduleTaintKey, targetNode.Name)
						}, farparams.FARConditionTimeout, farparams.DefaultPollInterval).Should(Succeed())

						waitForRemediation(ctx, APIClient, targetNode.Name, oldBootID)
					})

				It("should report correct FAR CR status conditions after remediation",
					Label(labels.TierAcceptance, labels.ComponentRemediation),
					reportxml.ID("67015"),
					func() {
						By("Creating FAR CR targeting " + targetNode.Name)

						farCR := buildFARUnstructured(targetNode.Name, fenceAgent, sharedParams, nodeParams)
						createFARCR(ctx, APIClient, farCR)

						currentFARName = targetNode.Name

						waitForRemediation(ctx, APIClient, targetNode.Name, oldBootID)

						By("Verifying FAR CR status conditions")

						expectedConditions := map[string]string{
							farparams.FARConditionProcessing:          string(metav1.ConditionFalse),
							farparams.FARConditionFenceAgentSucceeded: string(metav1.ConditionTrue),
							farparams.FARConditionSucceeded:           string(metav1.ConditionTrue),
						}

						Eventually(func(assertion Gomega) {
							farObj := &unstructured.Unstructured{}
							farObj.SetGroupVersionKind(farGVK)
							assertion.Expect(APIClient.Get(ctx, client.ObjectKey{
								Name: targetNode.Name, Namespace: medik8sparams.OperatorNs,
							}, farObj)).To(Succeed())

							conditions, found, condErr := unstructured.NestedSlice(
								farObj.Object, "status", "conditions")
							assertion.Expect(condErr).ToNot(HaveOccurred())
							assertion.Expect(found).To(BeTrue(), "FAR CR has no status.conditions")

							for condType, expectedStatus := range expectedConditions {
								condFound := false

								for _, c := range conditions {
									condMap, ok := c.(map[string]interface{})
									if !ok {
										continue
									}

									if condMap["type"] == condType {
										condFound = true

										assertion.Expect(condMap["status"]).To(Equal(expectedStatus),
											"Condition %s has unexpected status", condType)

										break
									}
								}

								assertion.Expect(condFound).To(BeTrue(),
									"Condition %s not found in FAR CR status", condType)
							}
						}, farparams.FARConditionTimeout, farparams.DefaultPollInterval).Should(Succeed())
					})

				Context("without --action parameter", func() {
					It("should default to reboot action when --action is omitted",
						Label(labels.TierAcceptance, labels.ComponentRemediation),
						reportxml.ID("66203"),
						func() {
							By("Building shared parameters WITHOUT --action")

							noActionParams := make(map[string]interface{}, len(sharedParams))
							for k, v := range sharedParams {
								if k != "--action" {
									noActionParams[k] = v
								}
							}

							By("Creating FAR CR without explicit action")

							farCR := buildFARUnstructured(targetNode.Name, fenceAgent, noActionParams, nodeParams)
							createFARCR(ctx, APIClient, farCR)

							currentFARName = targetNode.Name

							waitForRemediation(ctx, APIClient, targetNode.Name, oldBootID)
						})
				})
			})

			Context("leader node target", func() {
				It("should remediate the node hosting the active FAR controller",
					Label(labels.TierAcceptance, labels.ComponentRemediation),
					reportxml.ID("70638"),
					func() {
						By("Targeting the active FAR controller node")

						var err error

						activeLeader, err := farutils.GetActiveFARControllerNode(ctx, APIClient)
						Expect(err).ToNot(HaveOccurred())
						Expect(activeLeader).ToNot(BeEmpty())

						node := &corev1.Node{}
						Expect(APIClient.Get(ctx, client.ObjectKey{Name: activeLeader}, node)).To(Succeed())
						targetNode = node

						By("Cleaning CRI-O overlay storage on " + targetNode.Name)
						removeWorkloadImage(ctx, targetNode.Name)

						By("Recording boot ID before remediation")

						oldBootID, err := farutils.GetNodeBootIDFromAPI(ctx, APIClient, targetNode.Name)
						Expect(err).ToNot(HaveOccurred())

						By("Creating a test workload pod pinned to " + targetNode.Name)

						workloadPod := createWorkloadPod(ctx, APIClient, targetNode.Name)

						By("Recording pre-reboot lease holder for failover verification")

						preRebootLease := &coordinationv1.Lease{}
						Expect(APIClient.Get(ctx, client.ObjectKey{
							Name:      farparams.ControllerLeaseName,
							Namespace: medik8sparams.OperatorNs,
						}, preRebootLease)).To(Succeed())
						Expect(preRebootLease.Spec.HolderIdentity).ToNot(BeNil(),
							"Lease has no holder before reboot")
						oldLeaderHolder := *preRebootLease.Spec.HolderIdentity
						GinkgoWriter.Printf("Pre-reboot lease holder: %s\n", oldLeaderHolder)

						By("Creating FAR CR targeting the active controller node " + targetNode.Name)

						farCR := buildFARUnstructured(targetNode.Name, fenceAgent, sharedParams, nodeParams)
						createFARCR(ctx, APIClient, farCR)

						currentFARName = targetNode.Name

						waitForRemediation(ctx, APIClient, targetNode.Name, oldBootID)

						By("Verifying FAR controller replicas recovered")

						farDeployment, err := deployment.Pull(
							APIClient, farparams.OperatorDeploymentName, medik8sparams.OperatorNs)
						Expect(err).ToNot(HaveOccurred())
						Expect(farDeployment.IsReady(medik8sparams.DefaultTimeout)).To(BeTrue(),
							"FAR controller replicas did not recover after leader node reboot")

						By("Verifying controller lease transferred to a different pod")

						Eventually(func(assertion Gomega) {
							lease := &coordinationv1.Lease{}
							assertion.Expect(APIClient.Get(ctx, client.ObjectKey{
								Name:      farparams.ControllerLeaseName,
								Namespace: medik8sparams.OperatorNs,
							}, lease)).To(Succeed())
							assertion.Expect(lease.Spec.HolderIdentity).ToNot(BeNil(),
								"Lease has no holder after leader node reboot")

							if lease.Spec.HolderIdentity != nil {
								assertion.Expect(*lease.Spec.HolderIdentity).ToNot(Equal(oldLeaderHolder),
									"Lease is still held by pre-reboot pod %s", oldLeaderHolder)
							}
						}, farparams.ControllerHandoverTimeout, farparams.DefaultPollInterval).Should(Succeed(),
							"Controller lease did not transfer to a different pod after leader node reboot")

						By("Verifying FAR lifecycle events survived leader failover")

						node = &corev1.Node{}
						Expect(APIClient.Get(ctx, client.ObjectKey{Name: targetNode.Name}, node)).To(Succeed())

						Expect(helpers.WaitForEvents(ctx, APIClient.K8sClient,
							helpers.InvolvedObjectRef{
								Kind:      "FenceAgentsRemediation",
								Name:      targetNode.Name,
								Namespace: medik8sparams.OperatorNs,
								UID:       string(farCR.GetUID()),
							},
							[]helpers.EventExpectation{
								{Reason: farparams.FAREventRemediationStarted, Type: corev1.EventTypeNormal},
								{Reason: farparams.FAREventFenceAgentSucceeded, Type: corev1.EventTypeNormal},
								{Reason: farparams.FAREventRemediationFinished, Type: corev1.EventTypeNormal},
							},
							farparams.EventVerifyTimeout, farparams.EventVerifyInterval,
						)).To(Succeed(), "FAR lifecycle events not found on CR after leader failover")

						By("Verifying remediation completion event on Node after failover")

						Expect(helpers.WaitForEvents(ctx, APIClient.K8sClient,
							helpers.InvolvedObjectRef{
								Kind: "Node",
								Name: targetNode.Name,
								UID:  string(node.GetUID()),
							},
							[]helpers.EventExpectation{
								{Reason: farparams.FAREventNodeRemediationCompleted, Type: corev1.EventTypeNormal},
							},
							farparams.EventVerifyTimeout, farparams.EventVerifyInterval,
						)).To(Succeed(), "NodeRemediationCompleted event not found on node after leader failover")

						By("Verifying workload pod was evicted from leader node")

						Eventually(func() bool {
							pod := &corev1.Pod{}
							err := APIClient.Get(ctx, client.ObjectKey{
								Name: workloadPod.Name, Namespace: workloadPod.Namespace,
							}, pod)

							return k8serrors.IsNotFound(err) || pod.DeletionTimestamp != nil
						}, farparams.WorkloadEvictionTimeout, farparams.DefaultPollInterval).Should(BeTrue(),
							"Workload pod was not evicted from leader node after remediation")
					})
			})
		})

		Context("NHC+FAR interop", func() {
			// RHWA-1035: 4 NHC+FAR interop tests will be added here.
			// These tests install both NHC and FAR, configure NHC to use
			// FAR as the remediator, then trigger remediation via NHC by
			// stopping kubelet and waiting for NHC to detect the unhealthy
			// node and create a FAR CR automatically.
		})

		Context("control plane node target",
			Label(labels.TopologyControlPlane),
			func() {
				It("should remediate a control plane node and preserve etcd quorum",
					Label(labels.TierAcceptance, labels.ComponentRemediation),
					reportxml.ID("90217"),
					func() {
						By("Verifying at least 3 Ready control plane nodes for etcd quorum")

						cpNodes, err := farutils.GetReadyControlPlaneNodes(ctx, APIClient)
						Expect(err).ToNot(HaveOccurred())

						if len(cpNodes) < farparams.MinControlPlaneNodes {
							Skip(fmt.Sprintf(
								"CP remediation requires at least %d Ready CP nodes, found %d",
								farparams.MinControlPlaneNodes, len(cpNodes)))
						}

						By("Verifying etcd ClusterOperator is healthy before remediation")

						Expect(farutils.WaitForClusterOperatorHealthy(
							ctx, APIClient, "etcd", medik8sparams.DefaultTimeout,
							GinkgoWriter.Printf)).To(Succeed(),
							"etcd ClusterOperator not healthy before test")

						By("Selecting a CP node as remediation target")

						cpTarget, err := farutils.SelectControlPlaneNode(ctx, APIClient)
						Expect(err).ToNot(HaveOccurred())

						targetNode = cpTarget
						GinkgoWriter.Printf("CP remediation target: %s\n", targetNode.Name)

						By("Cleaning CRI-O overlay storage on " + targetNode.Name)
						removeWorkloadImage(ctx, targetNode.Name)

						By("Recording boot ID before remediation")

						oldBootID, err := farutils.GetNodeBootIDFromAPI(ctx, APIClient, targetNode.Name)
						Expect(err).ToNot(HaveOccurred())

						By("Creating a test workload pod pinned to CP node " + targetNode.Name)

						workloadPod := createWorkloadPod(ctx, APIClient, targetNode.Name)

						By("Creating FAR CR targeting CP node " + targetNode.Name)

						farCR := buildFARUnstructured(targetNode.Name, fenceAgent, sharedParams, nodeParams)
						createFARCR(ctx, APIClient, farCR)

						currentFARName = targetNode.Name

						waitForRemediationWithTimeouts(ctx, APIClient, targetNode.Name, oldBootID,
							farparams.CPRebootTimeout, farparams.CPNodeReadyTimeout)

						By("Verifying workload pod was evicted from fenced CP node")

						Eventually(func() bool {
							pod := &corev1.Pod{}
							getErr := APIClient.Get(ctx, client.ObjectKey{
								Name: workloadPod.Name, Namespace: workloadPod.Namespace,
							}, pod)

							return k8serrors.IsNotFound(getErr) || pod.DeletionTimestamp != nil
						}, farparams.WorkloadEvictionTimeout, farparams.DefaultPollInterval).Should(BeTrue(),
							"Workload pod was not evicted from CP node after remediation")

						By("Verifying etcd ClusterOperator recovered after CP node rejoin")

						Expect(farutils.WaitForClusterOperatorHealthy(
							ctx, APIClient, "etcd", farparams.EtcdRejoinTimeout,
							GinkgoWriter.Printf)).To(Succeed(),
							"etcd ClusterOperator did not recover after CP node reboot")

						By("Verifying FAR CR status conditions (fence action + overall outcome)")

						// The fence agent's success is verified via the DURABLE
						// FenceAgentActionSucceeded status condition rather than the
						// transient FenceAgentSucceeded Kubernetes Event. That event is
						// emitted the instant fencing triggers the control-plane reboot,
						// exactly when the target node's apiserver/etcd is disrupted, so
						// best-effort event delivery can drop it (see the note below).
						// Status conditions are persisted on the CR and updated with retry,
						// so they survive the disruption.
						expectedConditions := map[string]string{
							farparams.FARConditionProcessing:          string(metav1.ConditionFalse),
							farparams.FARConditionFenceAgentSucceeded: string(metav1.ConditionTrue),
							farparams.FARConditionSucceeded:           string(metav1.ConditionTrue),
						}

						Eventually(func(assertion Gomega) {
							farObj := &unstructured.Unstructured{}
							farObj.SetGroupVersionKind(farGVK)
							assertion.Expect(APIClient.Get(ctx, client.ObjectKey{
								Name: targetNode.Name, Namespace: medik8sparams.OperatorNs,
							}, farObj)).To(Succeed())

							conditions, found, condErr := unstructured.NestedSlice(
								farObj.Object, "status", "conditions")
							assertion.Expect(condErr).ToNot(HaveOccurred())
							assertion.Expect(found).To(BeTrue(), "FAR CR has no status.conditions")

							for condType, expectedStatus := range expectedConditions {
								condFound := false

								for _, c := range conditions {
									condMap, ok := c.(map[string]interface{})
									if !ok {
										continue
									}

									if condMap["type"] == condType {
										condFound = true

										assertion.Expect(condMap["status"]).To(Equal(expectedStatus),
											"Condition %s has unexpected status", condType)

										break
									}
								}

								assertion.Expect(condFound).To(BeTrue(),
									"Condition %s not found in FAR CR status", condType)
							}
						}, farparams.FARConditionTimeout, farparams.DefaultPollInterval).Should(Succeed())

						// FAR lifecycle Events are intentionally NOT asserted on a
						// control-plane target. Kubernetes Events are best-effort:
						// client-go's broadcaster drops them when its queue is full and
						// the sink drops them after a bounded retry limit, so delivery is
						// never guaranteed. Fencing a control-plane node reboots it and
						// briefly disrupts apiserver/etcd (quorum drops to 2/3 with a short
						// write stall), so ANY Event emitted across that window can be lost
						// even when the remediation fully succeeds - including
						// RemediationFinished (emitted as the node rejoins) and the Node's
						// NodeRemediationCompleted. This spec previously flaked
						// non-deterministically on whichever Event happened to be dropped
						// (FenceAgentSucceeded, then RemediationFinished on later runs). The
						// remediation outcome is instead proven above by the DURABLE FAR CR
						// status conditions (Processing=False, FenceAgentActionSucceeded=True,
						// Succeeded=True) plus observable cluster state (boot-ID change, node
						// Ready, workload eviction, etcd ClusterOperator recovery). The full
						// Event bundle is still asserted on the worker specs, where fencing
						// does not disrupt the control plane and Events are reliable.
					})
			})

		Context("minimal 2-worker topology",
			Label(labels.TopologyMinimalWorker),
			func() {
				It("should fence the leader and complete remediation with only 2 schedulable workers",
					Label(labels.TierAcceptance, labels.ComponentRemediation),
					reportxml.ID("90218"),
					func() {
						By("Verifying at least 2 Ready worker nodes")

						workerCount, err := helpers.CountReadyWorkerNodes(ctx, APIClient)
						Expect(err).ToNot(HaveOccurred())

						if workerCount < farparams.MinWorkersForTwoWorkerTest {
							Skip(fmt.Sprintf(
								"2-worker test requires at least %d Ready workers, found %d",
								farparams.MinWorkersForTwoWorkerTest, workerCount))
						}

						By("Identifying the two nodes hosting the FAR controller replicas")

						// The 2-worker premise (a durable ReadyReplicas dip while the fenced
						// leader is down) only holds if the two kept-schedulable workers are
						// exactly the two nodes already running the two controller replicas.
						// A random survivor can leave the second replica on a node that then
						// gets cordoned while the survivor sits empty; fencing the leader then
						// lets the replacement replica schedule onto the empty survivor and
						// ReadyReplicas recovers to 2 before the dip is observable. Pin both
						// kept nodes to the live replica placement instead.
						var replicaNodes []string

						Eventually(func() error {
							pods, podsErr := farutils.GetFARControllerPods(ctx, APIClient)
							if podsErr != nil {
								return podsErr
							}

							if len(pods) != int(farparams.ExpectedReplicas) {
								return fmt.Errorf("expected %d running controller replicas, found %d",
									farparams.ExpectedReplicas, len(pods))
							}

							nodes := make(map[string]struct{}, len(pods))
							for i := range pods {
								if node := pods[i].Spec.NodeName; node != "" {
									nodes[node] = struct{}{}
								}
							}

							if len(nodes) != int(farparams.ExpectedReplicas) {
								return fmt.Errorf(
									"controller replicas are not spread across %d distinct nodes: %v",
									farparams.ExpectedReplicas, nodes)
							}

							replicaNodes = replicaNodes[:0]
							for node := range nodes {
								replicaNodes = append(replicaNodes, node)
							}

							return nil
						}, farparams.ControllerHandoverTimeout, farparams.DefaultPollInterval).Should(Succeed())

						By("Verifying both replica nodes are schedulable workers")

						for _, name := range replicaNodes {
							node := &corev1.Node{}
							Expect(APIClient.Get(ctx, client.ObjectKey{Name: name}, node)).To(Succeed())

							if _, isWorker := node.Labels["node-role.kubernetes.io/worker"]; !isWorker {
								Skip(fmt.Sprintf(
									"2-worker test requires both controller replicas on worker nodes; "+
										"%s is not a worker", name))
							}
						}

						keepNames := append([]string(nil), replicaNodes...)

						// Declared at It scope so it can be uncordoned inline (to restore
						// schedulable capacity before verifying FAR recovery) and again via
						// DeferCleanup as an idempotent safety net.
						var cordonedNodes []string

						if workerCount > farparams.MinWorkersForTwoWorkerTest {
							By("Cordoning extra workers to simulate 2-worker topology")

							var cordonErr error

							cordonedNodes, cordonErr = farutils.CordonExtraWorkers(ctx, APIClient, keepNames)

							DeferCleanup(func() {
								if len(cordonedNodes) > 0 {
									By("Restoring topology: uncordoning extra worker nodes")
									farutils.UncordonNodes(ctx, APIClient, cordonedNodes, GinkgoWriter.Printf)
								}
							})
							Expect(cordonErr).ToNot(HaveOccurred())
							GinkgoWriter.Printf("Cordoned %d extra workers: %v\n",
								len(cordonedNodes), cordonedNodes)
						}

						// Clean CRI-O overlay on both candidate nodes up front. The fenced
						// node is chosen from the live lease immediately before creating the
						// FAR CR (below), so this disruptive debug-pod step must not run
						// between leader selection and fencing, where it could flip leadership.
						By("Cleaning CRI-O overlay storage on both replica nodes")

						for _, name := range replicaNodes {
							removeWorkloadImage(ctx, name)
						}

						By("Selecting the current leader as the fence target")

						// Re-read the lease AFTER the disruptive cleanup so the target reflects
						// the leader that is active now, and record that same holder as the
						// pre-reboot identity. Only fast API calls run between here and CR
						// creation, so leadership cannot silently move off the target.
						var survivorName string

						Eventually(func() error {
							currentLeader, leaderErr := farutils.GetActiveFARControllerNode(ctx, APIClient)
							if leaderErr != nil {
								return leaderErr
							}

							if currentLeader != replicaNodes[0] && currentLeader != replicaNodes[1] {
								return fmt.Errorf("leader %q is not on either kept replica node %v",
									currentLeader, replicaNodes)
							}

							leaderNode = currentLeader
							if replicaNodes[0] == leaderNode {
								survivorName = replicaNodes[1]
							} else {
								survivorName = replicaNodes[0]
							}

							return nil
						}, farparams.ControllerHandoverTimeout, farparams.DefaultPollInterval).Should(Succeed())

						targetNode = &corev1.Node{}
						Expect(APIClient.Get(ctx, client.ObjectKey{Name: leaderNode}, targetNode)).To(Succeed())
						GinkgoWriter.Printf("2-worker target (leader): %s, survivor: %s\n",
							targetNode.Name, survivorName)

						By("Recording boot ID before remediation")

						oldBootID, err := farutils.GetNodeBootIDFromAPI(ctx, APIClient, targetNode.Name)
						Expect(err).ToNot(HaveOccurred())

						By("Recording pre-reboot lease holder for failover verification")

						preRebootLease := &coordinationv1.Lease{}
						Expect(APIClient.Get(ctx, client.ObjectKey{
							Name:      farparams.ControllerLeaseName,
							Namespace: medik8sparams.OperatorNs,
						}, preRebootLease)).To(Succeed())
						Expect(preRebootLease.Spec.HolderIdentity).ToNot(BeNil(),
							"Lease has no holder before fencing the leader")
						oldLeaderHolder := *preRebootLease.Spec.HolderIdentity
						GinkgoWriter.Printf("Pre-reboot lease holder: %s\n", oldLeaderHolder)

						By("Creating a test workload pod pinned to " + targetNode.Name)

						workloadPod := createWorkloadPod(ctx, APIClient, targetNode.Name)

						By("Creating FAR CR targeting leader node " + targetNode.Name)

						farCR := buildFARUnstructured(targetNode.Name, fenceAgent, sharedParams, nodeParams)
						createFARCR(ctx, APIClient, farCR)

						currentFARName = targetNode.Name

						// ReadyReplicas is the deployment-level signal that the leader pod became
						// NotReady. It is more reliable than the node Ready condition, which flips
						// to NotReady before the pod on that node stops counting as Ready. A
						// transient Pull error returns ExpectedReplicas (full capacity) so it
						// neither false-detects a drop below nor fails the survival check.
						readyReplicas := func() int32 {
							dep, depErr := deployment.Pull(
								APIClient, farparams.OperatorDeploymentName,
								medik8sparams.OperatorNs)
							if depErr != nil {
								return farparams.ExpectedReplicas
							}

							return dep.Object.Status.ReadyReplicas
						}

						By("Waiting for FAR to reflect reduced capacity (leader replica no longer Ready)")

						// Gate the survival check on the leader replica actually dropping, so it
						// runs during genuine degraded capacity instead of passing on the first
						// poll while both replicas are still Ready. The drop is durably observable
						// because on this 2-worker topology anti-affinity keeps the replacement
						// replica Pending, so ReadyReplicas stays at 1 for the reboot window.
						Eventually(readyReplicas, medik8sparams.DefaultTimeout, farparams.DefaultPollInterval).Should(
							BeNumerically("<", farparams.ExpectedReplicas),
							"FAR ReadyReplicas never dropped below %d after fencing the leader node",
							farparams.ExpectedReplicas)

						By("Verifying at least 1 FAR replica stays Running throughout degraded capacity")

						Consistently(readyReplicas, farparams.ControllerHandoverTimeout, farparams.DefaultPollInterval).Should(
							BeNumerically(">=", 1),
							"FAR dropped below 1 Ready replica during degraded capacity")

						By("Verifying controller lease transferred to the survivor during degraded capacity")

						// Assert leadership moved off the fenced leader WHILE it is still down:
						// the fenced node is the only other controller host, so a changed holder
						// here must be the survivor. This mirrors OCP-70638's HolderIdentity check
						// but runs during the degraded window, so it proves transfer TO the survivor
						// rather than only that the old pod identity is gone after the node recovers.
						Eventually(func(assertion Gomega) {
							lease := &coordinationv1.Lease{}
							assertion.Expect(APIClient.Get(ctx, client.ObjectKey{
								Name:      farparams.ControllerLeaseName,
								Namespace: medik8sparams.OperatorNs,
							}, lease)).To(Succeed())
							assertion.Expect(lease.Spec.HolderIdentity).ToNot(BeNil(),
								"Lease has no holder during degraded capacity")

							if lease.Spec.HolderIdentity != nil {
								assertion.Expect(*lease.Spec.HolderIdentity).ToNot(Equal(oldLeaderHolder),
									"Lease is still held by pre-reboot pod %s", oldLeaderHolder)
							}
						}, farparams.ControllerHandoverTimeout, farparams.DefaultPollInterval).Should(Succeed(),
							"Controller lease did not transfer to the survivor during degraded capacity")

						waitForRemediation(ctx, APIClient, targetNode.Name, oldBootID)

						By("Verifying workload pod was evicted from fenced worker")

						Eventually(func() bool {
							pod := &corev1.Pod{}
							getErr := APIClient.Get(ctx, client.ObjectKey{
								Name: workloadPod.Name, Namespace: workloadPod.Namespace,
							}, pod)

							return k8serrors.IsNotFound(getErr) || pod.DeletionTimestamp != nil
						}, farparams.WorkloadEvictionTimeout, farparams.DefaultPollInterval).Should(BeTrue(),
							"Workload pod was not evicted after remediation in 2-worker topology")

						By("Verifying FAR CR reached Succeeded after leader failover")

						// Mirrors the standalone (OCP-67015) and CP tests: without the
						// Succeeded assertion a CR stuck in Processing would pass silently.
						expectedConditions := map[string]string{
							farparams.FARConditionProcessing:          string(metav1.ConditionFalse),
							farparams.FARConditionFenceAgentSucceeded: string(metav1.ConditionTrue),
							farparams.FARConditionSucceeded:           string(metav1.ConditionTrue),
						}

						Eventually(func(assertion Gomega) {
							farObj := &unstructured.Unstructured{}
							farObj.SetGroupVersionKind(farGVK)
							assertion.Expect(APIClient.Get(ctx, client.ObjectKey{
								Name: targetNode.Name, Namespace: medik8sparams.OperatorNs,
							}, farObj)).To(Succeed())

							conditions, found, condErr := unstructured.NestedSlice(
								farObj.Object, "status", "conditions")
							assertion.Expect(condErr).ToNot(HaveOccurred())
							assertion.Expect(found).To(BeTrue(), "FAR CR has no status.conditions")

							for condType, expectedStatus := range expectedConditions {
								condFound := false

								for _, c := range conditions {
									condMap, ok := c.(map[string]interface{})
									if !ok {
										continue
									}

									if condMap["type"] == condType {
										condFound = true

										assertion.Expect(condMap["status"]).To(Equal(expectedStatus),
											"Condition %s has unexpected status", condType)

										break
									}
								}

								assertion.Expect(condFound).To(BeTrue(),
									"Condition %s not found in FAR CR status", condType)
							}
						}, farparams.FARConditionTimeout, farparams.DefaultPollInterval).Should(Succeed())

						// Restore schedulable capacity so FAR can recover its second replica.
						// The fenced leader node stays NoSchedule-tainted until its CR is deleted
						// (in JustAfterEach), so recovery to 2 replicas relies on the uncordoned
						// extra worker(s). On a cluster with exactly 2 workers there is nothing to
						// uncordon and full recovery only completes after CR deletion; this test
						// targets the 3+-worker CI topology.
						if len(cordonedNodes) > 0 {
							By("Restoring schedulable capacity: uncordoning extra worker(s)")
							farutils.UncordonNodes(ctx, APIClient, cordonedNodes, GinkgoWriter.Printf)
							// Clear so the DeferCleanup safety net does not re-uncordon
							// already-restored nodes (matches the 0-worker test).
							cordonedNodes = nil
						}

						By("Verifying FAR recovers to 2 Ready replicas after capacity is restored")

						Eventually(func() int32 {
							dep, depErr := deployment.Pull(
								APIClient, farparams.OperatorDeploymentName,
								medik8sparams.OperatorNs)
							if depErr != nil {
								return 0
							}

							return dep.Object.Status.ReadyReplicas
						}, medik8sparams.DefaultTimeout, farparams.DefaultPollInterval).Should(
							BeNumerically(">=", farparams.ExpectedReplicas),
							"FAR should recover to %d Ready replicas after capacity is restored",
							farparams.ExpectedReplicas)
					})
			})

		Context("0-worker topology",
			Label(labels.TopologyZeroWorker),
			func() {
				It("should report FAR deployment unavailable when no workers are schedulable",
					Label(labels.TierAcceptance, labels.ComponentController),
					reportxml.ID("90308"),
					func() {
						By("Verifying FAR deployment is Ready before test")

						farDeployment, err := deployment.Pull(
							APIClient, farparams.OperatorDeploymentName, medik8sparams.OperatorNs)
						Expect(err).ToNot(HaveOccurred())
						Expect(farDeployment.IsReady(medik8sparams.DefaultTimeout)).To(BeTrue(),
							"FAR deployment is not Ready before 0-worker test")

						By("Verifying at least 1 Ready worker node exists to cordon")

						workerCount, err := helpers.CountReadyWorkerNodes(ctx, APIClient)
						Expect(err).ToNot(HaveOccurred())

						if workerCount < 1 {
							Skip(fmt.Sprintf(
								"0-worker test requires at least 1 Ready worker to cordon, found %d",
								workerCount))
						}

						By("Cordoning all Ready worker nodes to simulate 0-worker topology")

						cordonedNodes, err := farutils.CordonExtraWorkers(ctx, APIClient, nil)

						DeferCleanup(func() {
							if len(cordonedNodes) > 0 {
								By("Restoring topology: uncordoning all worker nodes")
								farutils.UncordonNodes(ctx, APIClient, cordonedNodes, GinkgoWriter.Printf)
							}
						})
						Expect(err).ToNot(HaveOccurred())

						// Skip (do not fail) if nothing was cordoned: the upfront count
						// found workers, so an empty result here means the topology changed
						// during setup (a worker went NotReady between the count and the
						// cordon). Failing on that race is the hard-fail this replaced.
						if len(cordonedNodes) == 0 {
							Skip("0-worker test found no Ready workers to cordon " +
								"(topology changed during setup)")
						}

						GinkgoWriter.Printf("Cordoned all %d workers: %v\n",
							len(cordonedNodes), cordonedNodes)

						By("Deleting FAR controller pods to trigger reschedule on cordoned nodes")

						podList := &corev1.PodList{}
						Expect(APIClient.List(ctx, podList,
							client.InNamespace(medik8sparams.OperatorNs),
							client.MatchingLabels(farparams.OperatorControllerPodLabels),
						)).To(Succeed())

						for i := range podList.Items {
							Expect(APIClient.Delete(ctx, &podList.Items[i])).To(Succeed())
							GinkgoWriter.Printf("Deleted FAR pod %s\n", podList.Items[i].Name)
						}

						By("Verifying FAR deployment has 0 Ready replicas")

						Eventually(func() int32 {
							dep, depErr := deployment.Pull(
								APIClient, farparams.OperatorDeploymentName,
								medik8sparams.OperatorNs)
							if depErr != nil {
								return -1
							}

							return dep.Object.Status.ReadyReplicas
						}, medik8sparams.DefaultTimeout, farparams.DefaultPollInterval).Should(
							BeNumerically("==", 0),
							"FAR deployment should have 0 Ready replicas with no schedulable workers")

						By("Uncordoning workers and verifying FAR recovery")

						farutils.UncordonNodes(ctx, APIClient, cordonedNodes, GinkgoWriter.Printf)
						cordonedNodes = nil

						By("Verifying FAR deployment recovers to 2 Ready replicas")

						Eventually(func() int32 {
							dep, depErr := deployment.Pull(
								APIClient, farparams.OperatorDeploymentName,
								medik8sparams.OperatorNs)
							if depErr != nil {
								return 0
							}

							return dep.Object.Status.ReadyReplicas
						}, medik8sparams.DefaultTimeout, farparams.DefaultPollInterval).Should(
							BeNumerically(">=", farparams.ExpectedReplicas),
							"FAR deployment should recover to %d Ready replicas after workers are uncordoned",
							farparams.ExpectedReplicas)
					})
			})
	})

func buildFARUnstructured(
	nodeName, agent string,
	sharedParams, nodeParams map[string]interface{},
) *unstructured.Unstructured {
	spec := map[string]interface{}{
		"agent":               agent,
		"sharedparameters":    sharedParams,
		"nodeparameters":      nodeParams,
		"retrycount":          farparams.FARCRRetryCount,
		"retryinterval":       farparams.FARCRRetryInterval,
		"timeout":             farparams.FARCRTimeout,
		"remediationStrategy": farparams.FARCRRemediationStrategy,
		"sharedSecretName":    farparams.SharedCredentialsSecretName,
	}

	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "fence-agents-remediation.medik8s.io/v1alpha1",
			"kind":       "FenceAgentsRemediation",
			"metadata": map[string]interface{}{
				"name":      nodeName,
				"namespace": medik8sparams.OperatorNs,
			},
			"spec": spec,
		},
	}
}

func buildFARTemplateUnstructured(
	name, agent string,
	sharedParams, nodeParams map[string]interface{},
) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "fence-agents-remediation.medik8s.io/v1alpha1",
			"kind":       "FenceAgentsRemediationTemplate",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": medik8sparams.OperatorNs,
			},
			"spec": map[string]interface{}{
				"template": map[string]interface{}{
					"spec": map[string]interface{}{
						"agent":               agent,
						"sharedparameters":    sharedParams,
						"nodeparameters":      nodeParams,
						"retrycount":          farparams.FARCRRetryCount,
						"retryinterval":       farparams.FARCRRetryInterval,
						"timeout":             farparams.FARCRTimeout,
						"remediationStrategy": farparams.FARCRRemediationStrategy,
						"sharedSecretName":    farparams.SharedCredentialsSecretName,
					},
				},
			},
		},
	}
}

func waitForRemediation(
	ctx context.Context, k8sClient client.Client,
	nodeName, oldBootID string,
) {
	GinkgoHelper()

	waitForRemediationWithTimeouts(ctx, k8sClient, nodeName, oldBootID,
		farparams.NodeRebootTimeout, farparams.NodeReadyTimeout)
}

func waitForRemediationWithTimeouts(
	ctx context.Context, k8sClient client.Client,
	nodeName, oldBootID string,
	rebootTimeout, readyTimeout time.Duration,
) {
	// GinkgoHelper() attributes any failure below to the calling It block,
	// correctly whether this is invoked directly (CP test) or through the
	// waitForRemediation wrapper (which adds a stack frame). It composes
	// across nesting depth, unlike a fixed ExpectWithOffset.
	GinkgoHelper()

	By("Waiting for node to reboot")

	Expect(farutils.WaitForNodeReboot(
		ctx, k8sClient, nodeName, oldBootID,
		rebootTimeout, GinkgoWriter.Printf)).To(Succeed(),
		"Node %s did not reboot", nodeName)

	By("Waiting for node to become Ready")

	Expect(farutils.WaitForNodeReady(
		ctx, k8sClient, nodeName,
		readyTimeout, GinkgoWriter.Printf)).To(Succeed(),
		"Node %s did not become Ready after reboot", nodeName)
}

func createFARCR(
	ctx context.Context, k8sClient client.Client,
	farCR *unstructured.Unstructured,
) {
	// GinkgoHelper() attributes any failure below (including the Eventually
	// creation poll) to the calling It block rather than to this helper,
	// composing across nesting depth like the sibling waitForRemediation
	// helpers, unlike a fixed EventuallyWithOffset.
	GinkgoHelper()

	deleteRemediationCR(ctx, k8sClient, farCR.GroupVersionKind(),
		farCR.GetName())

	Eventually(func(assertion Gomega) {
		err := k8sClient.Create(ctx, farCR)
		if err != nil {
			if k8serrors.IsAlreadyExists(err) {
				GinkgoWriter.Printf(
					"INFO: FAR CR %s already exists (prior delete may not have finalized), treating as success\n",
					farCR.GetName())

				return
			}

			assertion.Expect(err).ToNot(HaveOccurred(),
				"Failed to create FAR CR")
		}
	}, farparams.FARConditionTimeout, 10*farparams.DefaultPollInterval).Should(Succeed(),
		"FAR CR creation timed out - webhook may be unreachable")
}

func logFARControllerState(ctx context.Context, k8sClient client.Client) {
	pods := &corev1.PodList{}

	if err := k8sClient.List(ctx, pods,
		client.InNamespace(medik8sparams.OperatorNs),
		client.MatchingLabels(farparams.OperatorControllerPodLabels)); err != nil {
		GinkgoWriter.Printf("WARNING: could not list controller pods: %v\n", err)

		return
	}

	for i := range pods.Items {
		pod := &pods.Items[i]
		ready := false

		for _, cond := range pod.Status.Conditions {
			if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
				ready = true

				break
			}
		}

		GinkgoWriter.Printf("FAR controller pod %s: Phase=%s, Node=%s, Ready=%v\n",
			pod.Name, pod.Status.Phase, pod.Spec.NodeName, ready)
	}
}

// logFARRemediationDiagnostics dumps, on test failure, the ground-truth state
// needed to classify a remediation failure that pod-phase logging alone cannot:
// the FAR CR's status conditions (whether the controller reached Succeeded, and
// the fence-action outcome) and the active controller's recent logs (where a
// stalled reconcile surfaces). Called while currentFARName is still set, before
// the cleanup path deletes the CR. Both halves are best-effort so a diagnostics
// fetch error never masks the original test failure.
func logFARRemediationDiagnostics(ctx context.Context, apiClient *clients.Settings, farName string) {
	if farName != "" {
		logFARCRConditions(ctx, apiClient, farName)
	}

	logActiveControllerLogs(ctx, apiClient)
}

// logFARCRConditions prints every status condition on the named FAR CR so a
// failed remediation shows its terminal (or stuck) state instead of just the
// controller pod being Ready elsewhere.
func logFARCRConditions(ctx context.Context, k8sClient client.Client, farName string) {
	farObj := &unstructured.Unstructured{}
	farObj.SetGroupVersionKind(farGVK)

	if err := k8sClient.Get(ctx, client.ObjectKey{
		Name:      farName,
		Namespace: medik8sparams.OperatorNs,
	}, farObj); err != nil {
		GinkgoWriter.Printf("WARNING: could not fetch FAR CR %s for diagnostics: %v\n", farName, err)

		return
	}

	conditions, found, err := unstructured.NestedSlice(farObj.Object, "status", "conditions")
	if err != nil || !found {
		GinkgoWriter.Printf("FAR CR %s has no status conditions yet (found=%v, err=%v)\n",
			farName, found, err)

		return
	}

	GinkgoWriter.Printf("FAR CR %s status conditions:\n", farName)

	for _, c := range conditions {
		condMap, ok := c.(map[string]interface{})
		if !ok {
			continue
		}

		GinkgoWriter.Printf("  type=%v status=%v reason=%v message=%q\n",
			condMap["type"], condMap["status"], condMap["reason"], condMap["message"])
	}
}

// logActiveControllerLogs dumps the trailing lines of the active FAR controller
// pod's log, resolved via the leader-election lease so the output is the replica
// that actually reconciled the remediation rather than a passive standby.
func logActiveControllerLogs(ctx context.Context, apiClient *clients.Settings) {
	leaderNode, err := farutils.GetActiveFARControllerNode(ctx, apiClient)
	if err != nil {
		GinkgoWriter.Printf("WARNING: could not resolve active FAR controller for logs: %v\n", err)

		return
	}

	pods, err := farutils.GetFARControllerPods(ctx, apiClient)
	if err != nil {
		GinkgoWriter.Printf("WARNING: could not list FAR controller pods for logs: %v\n", err)

		return
	}

	var activePodName string

	for i := range pods {
		if pods[i].Spec.NodeName == leaderNode {
			activePodName = pods[i].Name

			break
		}
	}

	if activePodName == "" {
		GinkgoWriter.Printf("WARNING: no FAR controller pod found on leader node %s\n", leaderNode)

		return
	}

	logs, err := getControllerContainerLogs(
		apiClient, activePodName, farparams.ManagerContainerName, medik8sparams.OperatorNs)
	if err != nil {
		GinkgoWriter.Printf("WARNING: could not fetch logs for controller pod %s: %v\n", activePodName, err)

		return
	}

	GinkgoWriter.Printf("Last %d log lines from active FAR controller pod %s:\n%s\n",
		farparams.DiagnosticsLogTailLines, activePodName,
		tailLines(logs, farparams.DiagnosticsLogTailLines))
}

// tailLines returns the last maxLines lines of s, or all of s when it has fewer
// than maxLines lines.
func tailLines(s string, maxLines int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= maxLines {
		return strings.Join(lines, "\n")
	}

	return strings.Join(lines[len(lines)-maxLines:], "\n")
}

func logPodDiagnostics(ctx context.Context, k8sClient client.Client, pod *corev1.Pod) {
	if pod == nil || pod.Name == "" {
		return
	}

	fresh := &corev1.Pod{}

	if err := k8sClient.Get(ctx, client.ObjectKey{
		Name: pod.Name, Namespace: pod.Namespace,
	}, fresh); err != nil {
		GinkgoWriter.Printf("WARNING: could not fetch pod %s for diagnostics: %v\n",
			pod.Name, err)

		return
	}

	GinkgoWriter.Printf("Pod %s diagnostics: Phase=%s, Node=%s\n",
		fresh.Name, fresh.Status.Phase, fresh.Spec.NodeName)

	for _, cond := range fresh.Status.Conditions {
		if cond.Status != corev1.ConditionTrue {
			GinkgoWriter.Printf("  Condition %s=%s: %s (%s)\n",
				cond.Type, cond.Status, cond.Reason, cond.Message)
		}
	}

	for _, ctrStatus := range fresh.Status.ContainerStatuses {
		GinkgoWriter.Printf("  Container %s: Ready=%v, RestartCount=%d\n",
			ctrStatus.Name, ctrStatus.Ready, ctrStatus.RestartCount)

		if ctrStatus.State.Waiting != nil {
			GinkgoWriter.Printf("    Waiting: %s - %s\n",
				ctrStatus.State.Waiting.Reason, ctrStatus.State.Waiting.Message)
		}

		if ctrStatus.State.Terminated != nil {
			GinkgoWriter.Printf("    Terminated: %s (exit %d) - %s\n",
				ctrStatus.State.Terminated.Reason,
				ctrStatus.State.Terminated.ExitCode,
				ctrStatus.State.Terminated.Message)
		}
	}

	eventList := &corev1.EventList{}

	if err := k8sClient.List(ctx, eventList,
		client.InNamespace(pod.Namespace)); err != nil {
		GinkgoWriter.Printf("WARNING: could not list events: %v\n", err)

		return
	}

	GinkgoWriter.Printf("  Events for pod %s:\n", fresh.Name)

	eventFound := false

	for i := range eventList.Items {
		podEvent := &eventList.Items[i]

		if podEvent.InvolvedObject.Name != fresh.Name ||
			podEvent.InvolvedObject.Kind != "Pod" {
			continue
		}

		eventFound = true

		ts := podEvent.LastTimestamp.Format("15:04:05")
		GinkgoWriter.Printf("    [%s] %s %s: %s (x%d)\n",
			ts, podEvent.Type, podEvent.Reason, podEvent.Message, podEvent.Count)
	}

	if !eventFound {
		GinkgoWriter.Println("    (no events found)")
	}
}

func createWorkloadPod(
	ctx context.Context, k8sClient client.Client, nodeName string,
) *corev1.Pod {
	workloadPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "far-workload-test-",
			Namespace:    medik8sparams.OperatorNs,
		},
		Spec: corev1.PodSpec{
			NodeName:      nodeName,
			RestartPolicy: corev1.RestartPolicyAlways,
			Containers: []corev1.Container{{
				Name:    "workload",
				Image:   farparams.WorkloadTestImage,
				Command: []string{"sleep", "infinity"},
			}},
		},
	}

	ExpectWithOffset(1, k8sClient.Create(ctx, workloadPod)).To(Succeed())
	DeferCleanup(func() {
		if err := k8sClient.Delete(ctx, workloadPod); err != nil && !k8serrors.IsNotFound(err) {
			GinkgoWriter.Printf("WARNING: failed to delete workload pod %s: %v\n",
				workloadPod.Name, err)
		}
	})

	EventuallyWithOffset(1, func() corev1.PodPhase {
		pod := &corev1.Pod{}
		if err := k8sClient.Get(ctx, client.ObjectKey{
			Name: workloadPod.Name, Namespace: workloadPod.Namespace,
		}, pod); err != nil {
			return corev1.PodPending
		}

		return pod.Status.Phase
	}, farparams.WorkloadPodReadyTimeout, farparams.DefaultPollInterval).Should(Equal(corev1.PodRunning))

	return workloadPod
}

func removeWorkloadImage(ctx context.Context, nodeName string) {
	GinkgoWriter.Printf("Removing workload image from node %s to prevent corrupt overlay layers\n", nodeName)

	output, err := helpers.RunOnNode(
		ctx, nodeName, farparams.CrioCleanupTimeout,
		"bash", "-c",
		"crictl rmi "+farparams.WorkloadTestImage+" 2>/dev/null; "+
			"echo done",
	)
	if err != nil {
		GinkgoWriter.Printf(
			"WARNING: image removal on node %s failed: %v (output: %s)\n",
			nodeName, err, output)

		return
	}

	GinkgoWriter.Printf("Workload image removed from node %s (output: %s)\n",
		nodeName, output)
}

func deleteRemediationCR(
	ctx context.Context, k8sClient client.Client,
	gvk schema.GroupVersionKind, name string,
) {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)

	key := client.ObjectKey{Name: name, Namespace: medik8sparams.OperatorNs}

	if waitErr := wait.PollUntilContextTimeout(
		ctx, farparams.DefaultPollInterval, farparams.RemediationCRDeletionTimeout, true,
		func(ctx context.Context) (bool, error) {
			if err := k8sClient.Get(ctx, key, obj); err != nil {
				if k8serrors.IsNotFound(err) {
					return true, nil
				}

				return false, nil
			}

			if delErr := k8sClient.Delete(ctx, obj); delErr != nil {
				if k8serrors.IsNotFound(delErr) {
					return true, nil
				}

				return false, nil
			}

			return false, nil
		},
	); waitErr != nil {
		GinkgoWriter.Printf(
			"Warning: %s %s not fully deleted within %s: %v\n",
			gvk.Kind, name, farparams.RemediationCRDeletionTimeout, waitErr)
	}
}
