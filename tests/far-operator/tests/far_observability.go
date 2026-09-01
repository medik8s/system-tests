package tests

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/clients"
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

var _ = Describe("FAR Observability Tests",
	Serial,
	Ordered,
	ContinueOnFailure,
	Label(labels.OperatorFAR, farparams.Label,
		labels.TierAcceptance, labels.FrequencyWeekly),
	func() {
		var (
			ctx          context.Context
			platform     configv1.PlatformType
			region       string
			fenceAgent   string
			leaderNode   string
			sharedParams map[string]interface{}
			nodeParams   map[string]interface{}
		)

		BeforeAll(func() {
			ctx = context.Background()

			By("Detecting cluster platform")

			var err error

			platform, region, err = helpers.DetectPlatform(ctx, APIClient)
			Expect(err).ToNot(HaveOccurred())

			if platform != configv1.AWSPlatformType {
				Skip(fmt.Sprintf(
					"FAR observability tests require AWS, got %s", platform))
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
			Expect(farDeployment.Object.Spec.Replicas).To(HaveValue(Equal(farparams.ExpectedReplicas)),
				"FAR observability specs require %d controller replicas for HA leader election",
				farparams.ExpectedReplicas)

			By("Verifying sufficient worker nodes")

			workerCount, err := helpers.CountReadyWorkerNodes(ctx, APIClient)
			Expect(err).ToNot(HaveOccurred())

			if workerCount < farparams.MinWorkersForObservabilityTests {
				Skip(fmt.Sprintf(
					"Observability tests require at least %d Ready worker nodes, found %d",
					farparams.MinWorkersForObservabilityTests, workerCount))
			}

			By("Identifying active FAR controller node")

			Eventually(func() error {
				var leaderErr error

				leaderNode, leaderErr = farutils.GetActiveFARControllerNode(ctx, APIClient)

				return leaderErr
			}, farparams.ControllerHandoverTimeout, farparams.DefaultPollInterval).Should(Succeed(),
				"FAR leader election did not settle")
			GinkgoWriter.Printf("FAR leader is on node: %s\n", leaderNode)

			By("Reading AWS credentials and creating shared Secret")

			awsAccessKey, awsSecretKey, err := farutils.GetAWSCredentials(
				ctx, APIClient, medik8sparams.OperatorNs)
			Expect(err).ToNot(HaveOccurred())

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
				Expect(err).ToNot(HaveOccurred())
			}

			By("Building shared and node parameters for fence agent")

			sharedParams = map[string]interface{}{
				"--region":          region,
				"--action":          "reboot",
				"--skip-race-check": "",
			}

			awsNodeParams, err := farutils.BuildAWSNodeParameters(ctx, APIClient)
			Expect(err).ToNot(HaveOccurred())

			nodeParams = make(map[string]interface{})
			for paramName, nodeMap := range awsNodeParams {
				inner := make(map[string]interface{}, len(nodeMap))
				for nodeName, val := range nodeMap {
					inner[nodeName] = val
				}

				nodeParams[paramName] = inner
			}
		})

		JustAfterEach(func() {
			if CurrentSpecReport().Failed() {
				GinkgoWriter.Println("Test failed - collecting FAR controller diagnostics")
				helpers.LogControllerState(ctx, APIClient,
					medik8sparams.OperatorNs, farparams.OperatorControllerPodLabels,
					GinkgoWriter.Printf)
			}
		})

		Context("must-gather", func() {
			It("collects FAR data via oc adm must-gather during active remediation",
				Label(labels.DisruptionDestructive, labels.PlatformAWS, labels.ComponentController),
				reportxml.ID("61480"),
				func() {
					By("Selecting a non-leader worker node")

					targetNode, err := helpers.SelectWorkerNode(ctx, APIClient, leaderNode)
					Expect(err).ToNot(HaveOccurred())
					GinkgoWriter.Printf("Target node for must-gather test: %s\n", targetNode.Name)

					By("Recording boot ID before remediation")

					oldBootID, err := farutils.GetNodeBootIDFromAPI(ctx, APIClient, targetNode.Name)
					Expect(err).ToNot(HaveOccurred())

					By("Creating temporary must-gather output directory")

					mustGatherDir, mkdirErr := os.MkdirTemp("", "far-must-gather-*")
					Expect(mkdirErr).ToNot(HaveOccurred())

					DeferCleanup(func() {
						if removeErr := os.RemoveAll(mustGatherDir); removeErr != nil {
							GinkgoWriter.Printf("WARNING: failed to remove must-gather dir %s: %v\n",
								mustGatherDir, removeErr)
						}
					})

					By("Creating FAR CR to trigger remediation on " + targetNode.Name)

					farCR := buildFARUnstructured(targetNode.Name, fenceAgent, sharedParams, nodeParams)
					createFARCR(ctx, APIClient, farCR)

					farCRName := targetNode.Name

					DeferCleanup(func() {
						By("Cleaning up FAR CR " + farCRName)
						farutils.CleanupFARRemediation(ctx, APIClient, farGVK,
							farCRName, medik8sparams.OperatorNs, GinkgoWriter.Printf)

						By("Waiting for node " + farCRName + " to become Ready")

						if recoverErr := farutils.WaitForNodeReady(
							ctx, APIClient, farCRName,
							farparams.NodeReadyTimeout, GinkgoWriter.Printf); recoverErr != nil {
							GinkgoWriter.Printf(
								"WARNING: node %s did not recover: %v\n", farCRName, recoverErr)
						}
					})

					By("Waiting for FAR remediation to start (Processing condition)")

					Eventually(func() bool {
						farObj := &unstructured.Unstructured{}
						farObj.SetGroupVersionKind(farGVK)

						if getErr := APIClient.Get(ctx, client.ObjectKey{
							Name:      farCRName,
							Namespace: medik8sparams.OperatorNs,
						}, farObj); getErr != nil {
							return false
						}

						conditions, found, condErr := unstructured.NestedSlice(
							farObj.Object, "status", "conditions")
						if condErr != nil {
							GinkgoWriter.Printf("WARNING: failed to read FAR CR conditions: %v\n", condErr)

							return false
						}

						if !found {
							return false
						}

						for _, c := range conditions {
							condMap, ok := c.(map[string]interface{})
							if !ok {
								continue
							}

							if condMap["type"] == farparams.FARConditionProcessing &&
								condMap["status"] == string(metav1.ConditionTrue) {
								return true
							}
						}

						return false
					}, farparams.FARConditionTimeout, farparams.DefaultPollInterval).Should(BeTrue(),
						"FAR CR did not reach Processing state")

					By("Verifying RemediationStarted event on the FAR CR")

					Expect(helpers.WaitForEvents(ctx, APIClient.K8sClient,
						helpers.InvolvedObjectRef{
							Kind:      "FenceAgentsRemediation",
							Name:      farCRName,
							Namespace: medik8sparams.OperatorNs,
							UID:       string(farCR.GetUID()),
						},
						[]helpers.EventExpectation{
							{Reason: farparams.FAREventRemediationStarted, Type: corev1.EventTypeNormal},
						},
						farparams.FARConditionTimeout, farparams.DefaultPollInterval,
					)).To(Succeed(), "RemediationStarted event not found on FAR CR %s", farCRName)

					By("Running oc adm must-gather")

					Expect(farutils.RunMustGather(ctx, mustGatherDir, farparams.MustGatherTimeout,
						GinkgoWriter.Printf)).To(Succeed(), "oc adm must-gather failed")

					By("Validating must-gather output contains FAR data")

					expectations := []farutils.MustGatherExpectation{
						{
							Description:  "node YAML files",
							PathContains: "nodes",
							NameGlob:     "*.yaml",
							MinCount:     1,
						},
						{
							Description: "FAR CRD definitions",
							NameGlob:    "*fenceagentsremediation*.yaml",
							MinCount:    1,
						},
						{
							Description:  "FAR operator namespace resources",
							PathContains: medik8sparams.OperatorNs,
							NameGlob:     "*.yaml",
							MinCount:     1,
						},
						{
							Description:  "active FenceAgentsRemediation CR instance",
							PathContains: "fence-agents-remediation.medik8s.io/fenceagentsremediations",
							NameGlob:     "*.yaml",
							MinCount:     1,
						},
					}

					missingItems := farutils.ValidateMustGatherContents(mustGatherDir, expectations)
					Expect(missingItems).To(BeEmpty(),
						"Must-gather validation failed:\n%s", strings.Join(missingItems, "\n"))

					By("Waiting for node to reboot and recover")

					Expect(farutils.WaitForNodeReboot(
						ctx, APIClient, targetNode.Name, oldBootID,
						farparams.NodeRebootTimeout, GinkgoWriter.Printf)).To(Succeed(),
						"Node %s did not reboot", targetNode.Name)

					Expect(farutils.WaitForNodeReady(
						ctx, APIClient, targetNode.Name,
						farparams.NodeReadyTimeout, GinkgoWriter.Printf)).To(Succeed(),
						"Node %s did not become Ready after reboot", targetNode.Name)
				})
		})

		Context("timed-out remediation", func() {
			It("logs fence agent timeout messages matching retry count",
				Label(labels.DisruptionNonDestructive, labels.PlatformAWS, labels.ComponentRemediation),
				reportxml.ID("70873"),
				func() {
					By("Deleting FAR controller pods to clear logs")

					controllerPods, err := farutils.GetFARControllerPods(ctx, APIClient)
					Expect(err).ToNot(HaveOccurred())
					Expect(controllerPods).ToNot(BeEmpty(), "No FAR controller pods found")

					for i := range controllerPods {
						Expect(APIClient.Delete(ctx, &controllerPods[i])).To(Succeed(),
							"Failed to delete FAR controller pod %s", controllerPods[i].Name)
					}

					By("Waiting for new controller pods to become Ready")

					Eventually(func() (int, error) {
						pods, listErr := farutils.GetFARControllerPods(ctx, APIClient)

						return len(pods), listErr
					}, farparams.ControllerPodReadyTimeout, farparams.DefaultPollInterval).Should(
						Equal(int(farparams.ExpectedReplicas)),
						"FAR controller pods did not recover after deletion")

					By("Re-resolving FAR leader after pod restart")

					Eventually(func() error {
						var leaderErr error

						leaderNode, leaderErr = farutils.GetActiveFARControllerNode(ctx, APIClient)

						return leaderErr
					}, farparams.ControllerHandoverTimeout, farparams.DefaultPollInterval).Should(Succeed(),
						"FAR leader election did not settle after pod restart")

					By("Selecting a non-leader worker node")

					targetNode, err := helpers.SelectWorkerNode(ctx, APIClient, leaderNode)
					Expect(err).ToNot(HaveOccurred())

					By("Overriding --plug for target node with invalid instance ID")

					timedOutNodeParams := make(map[string]interface{})
					for k, v := range nodeParams {
						timedOutNodeParams[k] = v
					}

					plugMap, ok := timedOutNodeParams[farparams.NodeIdentifierAWS].(map[string]interface{})
					Expect(ok).To(BeTrue(), "--plug node parameter map has unexpected type")

					overriddenPlugMap := make(map[string]interface{})
					for k, v := range plugMap {
						overriddenPlugMap[k] = v
					}

					overriddenPlugMap[targetNode.Name] = farparams.TimedOutBadInstanceID
					timedOutNodeParams[farparams.NodeIdentifierAWS] = overriddenPlugMap

					retryCount := farparams.FARCRRetryCount
					retryIntervalDuration, parseErr := time.ParseDuration(farparams.FARCRRetryInterval)
					Expect(parseErr).ToNot(HaveOccurred())

					farCRName := targetNode.Name

					By("Creating FAR CR with invalid --plug targeting " + targetNode.Name)

					farCR := buildFARUnstructured(farCRName, fenceAgent, sharedParams, timedOutNodeParams)
					createFARCR(ctx, APIClient, farCR)

					DeferCleanup(func() {
						By("Deleting timed-out FAR CR " + farCRName)
						deleteRemediationCR(ctx, APIClient, farGVK, farCRName)
					})

					waitDuration := time.Duration(retryCount) * (retryIntervalDuration + farparams.TimedOutRetryBuffer)

					By("Identifying active FAR controller pod")

					activeLeaderNode, err := farutils.GetActiveFARControllerNode(ctx, APIClient)
					Expect(err).ToNot(HaveOccurred())

					controllerPods, err = farutils.GetFARControllerPods(ctx, APIClient)
					Expect(err).ToNot(HaveOccurred())

					var activeControllerPodName string

					for i := range controllerPods {
						if controllerPods[i].Spec.NodeName == activeLeaderNode {
							activeControllerPodName = controllerPods[i].Name

							break
						}
					}

					Expect(activeControllerPodName).ToNot(BeEmpty(),
						"Could not find active controller pod on leader node %s", activeLeaderNode)

					timedOutPattern := regexp.MustCompile(farparams.TimedOutLogPattern)

					// Resolving the leader pod once is safe here: the timed-out fencing is
					// non-destructive, so the controller leader stays stable through the retry
					// window. On a transient log-fetch error, reuse the last observed count so a
					// blip during the Consistently window is not a false failure (the count is
					// monotonic, so a real extra entry still surfaces on the next successful poll).
					lastFailureCount := 0
					countFailureLogs := func() int {
						logs, logErr := getControllerContainerLogs(
							APIClient, activeControllerPodName, farparams.ManagerContainerName,
							medik8sparams.OperatorNs)
						if logErr != nil {
							GinkgoWriter.Printf(
								"WARNING: failed to fetch controller logs (reusing last count %d): %v\n",
								lastFailureCount, logErr)

							return lastFailureCount
						}

						lastFailureCount = len(timedOutPattern.FindAllString(logs, -1))

						return lastFailureCount
					}

					By(fmt.Sprintf("Waiting for %d fence agent failure log entries (polls, exits early)", retryCount))

					Eventually(countFailureLogs, waitDuration, farparams.DefaultPollInterval).Should(
						BeNumerically(">=", retryCount),
						"expected at least %d %q entries on pod %s",
						retryCount, farparams.TimedOutLogPattern, activeControllerPodName)

					By("Confirming the failure count settles at exactly retryCount (no overshoot)")

					Consistently(countFailureLogs, farparams.TimedOutRetryBuffer, farparams.DefaultPollInterval).Should(
						Equal(retryCount),
						"expected exactly %d %q entries on pod %s",
						retryCount, farparams.TimedOutLogPattern, activeControllerPodName)
				})
		})
	})

func getControllerContainerLogs(
	apiClient *clients.Settings, podName, containerName, namespace string,
) (string, error) {
	podBuilder, err := pod.Pull(apiClient, podName, namespace)
	if err != nil {
		return "", fmt.Errorf("failed to pull pod %s in namespace %s: %w",
			podName, namespace, err)
	}

	logs, err := podBuilder.GetFullLog(containerName)
	if err != nil {
		return "", fmt.Errorf("failed to get logs for pod %s container %s: %w",
			podName, containerName, err)
	}

	return logs, nil
}
