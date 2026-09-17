package tests

import (
	"context"
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"
	olmV1alpha1 "github.com/rh-ecosystem-edge/eco-goinfra/pkg/schemes/olm/operators/v1alpha1"

	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/deployment"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/infrastructure"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/olm"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/pod"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/reportxml"

	"github.com/medik8s/system-tests/tests/far-operator/internal/farparams"
	"github.com/medik8s/system-tests/tests/internal/helpers"
	"github.com/medik8s/system-tests/tests/internal/labels"
	. "github.com/medik8s/system-tests/tests/internal/medik8sinittools"
	"github.com/medik8s/system-tests/tests/internal/medik8sparams"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe(
	"FAR Post Deployment tests",
	Ordered,
	ContinueOnFailure,
	Label(labels.OperatorFAR), func() {
		var controlPlaneTopology configv1.TopologyMode

		BeforeAll(func() {
			By("Get FAR deployment object and verify it is Ready")

			farDeployment, err := deployment.Pull(
				APIClient, farparams.OperatorDeploymentName, medik8sparams.OperatorNs)
			Expect(err).ToNot(HaveOccurred(), "Failed to get FAR deployment")
			Expect(farDeployment.IsReady(medik8sparams.DefaultTimeout)).To(BeTrue(),
				"FAR deployment is not Ready")

			By("Pull cluster topology for use in topology-aware tests")

			infraConfig, infraErr := infrastructure.Pull(APIClient)
			Expect(infraErr).ToNot(HaveOccurred(), "Failed to pull infrastructure configuration")

			controlPlaneTopology = infraConfig.Object.Status.ControlPlaneTopology
		})
		It("Verify Fence Agents Remediation Operator pod is running",
			reportxml.ID("66026"),
			Label(labels.TierSmoke, labels.DisruptionNonDestructive,
				labels.PlatformAny, labels.ComponentController, labels.FrequencyPresubmit),
			func() {
				expectedCount := farparams.ExpectedReplicas
				if controlPlaneTopology == configv1.SingleReplicaTopologyMode {
					expectedCount = int32(1)
				}

				listOptions := metav1.ListOptions{
					LabelSelector: farparams.OperatorControllerPodLabelSelector,
				}

				By("Waiting for expected number of Running FAR pods")

				Eventually(func(assertion Gomega) {
					farPods, err := pod.List(APIClient, medik8sparams.OperatorNs, listOptions)
					assertion.Expect(err).ToNot(HaveOccurred(), "Failed to list FAR pods")

					runningPods := helpers.FilterRunningPods(farPods)

					assertion.Expect(int32(len(runningPods))).To(Equal(expectedCount),
						"Expected %d running FAR pod(s), found %d", expectedCount, len(runningPods))
				}, medik8sparams.DefaultTimeout, farparams.DefaultPollInterval).Should(Succeed())
			})

		It("Verify FAR CSV has required annotations",
			reportxml.ID("70637"),
			Label(labels.TierSmoke, labels.DisruptionNonDestructive,
				labels.PlatformAny, labels.ComponentOLM, labels.FrequencyPresubmit),
			func() {
				By("Getting FAR ClusterServiceVersion")

				var farCSV *olm.ClusterServiceVersionBuilder

				Eventually(func() error {
					farCSVs, err := olm.ListClusterServiceVersionWithNamePattern(
						APIClient, "fence-agents-remediation", medik8sparams.OperatorNs)
					if err != nil {
						return err
					}

					for _, csv := range farCSVs {
						phase, phaseErr := csv.GetPhase()
						if phaseErr == nil && phase == olmV1alpha1.CSVPhaseSucceeded {
							farCSV = csv

							return nil
						}
					}

					return fmt.Errorf("no FAR CSV in Succeeded phase found yet")
				}, medik8sparams.DefaultTimeout, farparams.DefaultPollInterval).Should(Succeed(),
					"FAR CSV must reach Succeeded phase")

				By("Checking annotation values on FAR CSV")

				Expect(farCSV.Object.Annotations).ToNot(BeNil(), "CSV annotations should not be nil")

				var annotationErrors []string

				for annotationKey, expectedValue := range farparams.RequiredAnnotations {
					annotationValue, exists := farCSV.Object.Annotations[annotationKey]
					if !exists {
						annotationErrors = append(annotationErrors,
							fmt.Sprintf("required annotation %q is missing", annotationKey))

						continue
					}

					if annotationValue != expectedValue {
						annotationErrors = append(annotationErrors,
							fmt.Sprintf("annotation %q: expected %q, got %q",
								annotationKey, expectedValue, annotationValue))
					}
				}

				if len(annotationErrors) > 0 {
					errMsg := "CSV annotation validation failures:\n"
					for _, msg := range annotationErrors {
						errMsg += fmt.Sprintf("- %s\n", msg)
					}

					Fail(errMsg)
				}
			})

		It("Verify FAR controller manager has correct number of replicas",
			reportxml.ID("61222"),
			Label(labels.TierSmoke, labels.DisruptionNonDestructive,
				labels.PlatformAny, labels.ComponentController, labels.FrequencyPresubmit),
			func() {
				if controlPlaneTopology == configv1.SingleReplicaTopologyMode {
					Skip("Skipping test on SNO (Single Node OpenShift) cluster")
				}

				By("Verifying replica count, ready replicas, and pod HA distribution")

				Eventually(func() error {
					liveDeploy, pullErr := deployment.Pull(
						APIClient, farparams.OperatorDeploymentName, medik8sparams.OperatorNs)
					if pullErr != nil {
						return pullErr
					}

					if liveDeploy.Object.Spec.Replicas == nil {
						return fmt.Errorf("deployment Spec.Replicas is nil")
					}

					if *liveDeploy.Object.Spec.Replicas != farparams.ExpectedReplicas {
						return fmt.Errorf("expected %d desired replica(s), found %d",
							farparams.ExpectedReplicas, *liveDeploy.Object.Spec.Replicas)
					}

					if liveDeploy.Object.Status.ReadyReplicas != farparams.ExpectedReplicas {
						return fmt.Errorf("expected %d ready replica(s), found %d",
							farparams.ExpectedReplicas, liveDeploy.Object.Status.ReadyReplicas)
					}

					listOptions := metav1.ListOptions{
						LabelSelector: farparams.OperatorControllerPodLabelSelector,
					}

					farPods, listErr := pod.List(APIClient, medik8sparams.OperatorNs, listOptions)
					if listErr != nil {
						return listErr
					}

					runningPods := helpers.FilterRunningPods(farPods)

					if int32(len(runningPods)) != farparams.ExpectedReplicas {
						return fmt.Errorf("expected %d running pod(s), found %d",
							farparams.ExpectedReplicas, len(runningPods))
					}

					nodeNames := make(map[string]bool)

					for _, p := range runningPods {
						if p.Object.Spec.NodeName == "" {
							return fmt.Errorf("pod %s has not been assigned to a node", p.Object.Name)
						}

						nodeNames[p.Object.Spec.NodeName] = true
					}

					if len(nodeNames) != int(farparams.ExpectedReplicas) {
						return fmt.Errorf(
							"FAR pods must run on different nodes for HA, found pods on %d unique node(s)",
							len(nodeNames))
					}

					return nil
				}, medik8sparams.DefaultTimeout, farparams.DefaultPollInterval).Should(Succeed(),
					"deployment should have %d ready replicas on distinct nodes", farparams.ExpectedReplicas)
			})

		It("Verify FAR container runs as non-root user",
			reportxml.ID("89231"),
			Label(labels.TierSmoke, labels.DisruptionNonDestructive,
				labels.PlatformAny, labels.ComponentController, labels.FrequencyPresubmit),
			func() {
				By("Waiting for FAR controller pods to be running")

				listOptions := metav1.ListOptions{
					LabelSelector: farparams.OperatorControllerPodLabelSelector,
				}

				_, err := pod.WaitForAllPodsInNamespaceRunning(
					APIClient,
					medik8sparams.OperatorNs,
					medik8sparams.DefaultTimeout,
					listOptions,
				)
				Expect(err).ToNot(HaveOccurred(), "FAR controller pods are not ready")

				By("Listing FAR controller pods")

				farPods, err := pod.List(APIClient, medik8sparams.OperatorNs, listOptions)
				Expect(err).ToNot(HaveOccurred(), "Failed to get FAR controller pods")

				runningPods := helpers.FilterRunningPods(farPods)
				Expect(runningPods).ToNot(BeEmpty(), "No running FAR controller pods found")

				errorMessages := helpers.ValidateNonRootSecurityContext(
					runningPods, farparams.ManagerContainerName, true)

				if len(errorMessages) > 0 {
					Fail("Testing security context of FAR container failed due to:\n- " +
						strings.Join(errorMessages, "\n- "))
				}
			})

		It("Verify FAR CRDs are installed and Established",
			reportxml.ID("89548"),
			Label(labels.TierSmoke, labels.DisruptionNonDestructive,
				labels.PlatformAny, labels.ComponentController, labels.FrequencyPresubmit),
			func() {
				crdNames := []string{
					farparams.FenceAgentsRemediationCRDName,
					farparams.FenceAgentsRemediationTemplateCRDName,
				}

				for _, crdName := range crdNames {
					By("Verifying CRD " + crdName + " exists with Established=True")

					Eventually(func() error {
						crd := &apiextensionsv1.CustomResourceDefinition{}
						if err := APIClient.Get(context.TODO(), client.ObjectKey{Name: crdName}, crd); err != nil {
							return fmt.Errorf("CRD %s should exist: %w", crdName, err)
						}

						if !crdIsEstablished(crd) {
							return fmt.Errorf("CRD %s should have Established=True", crdName)
						}

						return nil
					}, medik8sparams.DefaultTimeout, farparams.DefaultPollInterval).Should(Succeed())
				}
			})

		It("Verify FAR operator namespace has correct PSA enforcement label",
			reportxml.ID("89549"),
			Label(labels.TierSmoke, labels.DisruptionNonDestructive,
				labels.PlatformAny, labels.ComponentController, labels.FrequencyPresubmit),
			func() {
				By("Verifying operator namespace " + medik8sparams.OperatorNs + " has correct PSA labels")

				Eventually(func() error {
					namespace := &corev1.Namespace{}
					if err := APIClient.Get(context.TODO(), client.ObjectKey{Name: medik8sparams.OperatorNs}, namespace); err != nil {
						return fmt.Errorf("failed to get namespace %s: %w", medik8sparams.OperatorNs, err)
					}

					if namespace.Labels == nil {
						return fmt.Errorf("namespace %s has no labels", medik8sparams.OperatorNs)
					}

					if namespace.Labels[farparams.PSAEnforceLabelKey] != farparams.PSAExpectedLevel {
						return fmt.Errorf("namespace %s should have %s=%s, got %q",
							medik8sparams.OperatorNs, farparams.PSAEnforceLabelKey, farparams.PSAExpectedLevel,
							namespace.Labels[farparams.PSAEnforceLabelKey])
					}

					return nil
				}, medik8sparams.DefaultTimeout, farparams.DefaultPollInterval).Should(Succeed())
			})

		It("Verify FAR controller manager has system-cluster-critical priority class",
			reportxml.ID("66211"),
			Label(labels.TierSmoke, labels.DisruptionNonDestructive,
				labels.PlatformAny, labels.ComponentController, labels.FrequencyPresubmit),
			func() {
				By("Waiting for FAR controller pods to be running")

				listOptions := metav1.ListOptions{
					LabelSelector: farparams.OperatorControllerPodLabelSelector,
				}

				_, err := pod.WaitForAllPodsInNamespaceRunning(
					APIClient,
					medik8sparams.OperatorNs,
					medik8sparams.DefaultTimeout,
					listOptions,
				)
				Expect(err).ToNot(HaveOccurred(), "FAR controller pods are not ready")

				By("Listing FAR controller pods")

				farPods, err := pod.List(APIClient, medik8sparams.OperatorNs, listOptions)
				Expect(err).ToNot(HaveOccurred(), "Failed to list FAR pods")

				runningPods := helpers.FilterRunningPods(farPods)
				Expect(runningPods).ToNot(BeEmpty(), "No running FAR controller pods found")

				for _, p := range runningPods {
					By(fmt.Sprintf("Verifying priorityClassName on pod %s", p.Object.Name))
					Expect(p.Object.Spec.PriorityClassName).To(Equal(farparams.ExpectedPriorityClassName),
						"Pod %s has priorityClassName %q, expected %q",
						p.Object.Name, p.Object.Spec.PriorityClassName, farparams.ExpectedPriorityClassName)
				}
			})

		It("Verify FAR controller pod has correct Kubernetes labels",
			reportxml.ID("66209"),
			Label(labels.TierSmoke, labels.DisruptionNonDestructive,
				labels.PlatformAny, labels.ComponentController, labels.FrequencyPresubmit),
			func() {
				By("Waiting for FAR controller pods to be running")

				_, err := pod.WaitForAllPodsInNamespaceRunning(
					APIClient,
					medik8sparams.OperatorNs,
					medik8sparams.DefaultTimeout,
					metav1.ListOptions{
						LabelSelector: farparams.OperatorControllerPodLabelSelector,
					},
				)
				Expect(err).ToNot(HaveOccurred(), "FAR controller pods are not ready")

				By("Listing all pods in operator namespace by deployment name prefix")

				allPods, err := pod.List(APIClient, medik8sparams.OperatorNs, metav1.ListOptions{})
				Expect(err).ToNot(HaveOccurred(), "Failed to list pods in operator namespace")

				var farPods []*pod.Builder

				for _, p := range allPods {
					if strings.HasPrefix(p.Object.Name, farparams.OperatorDeploymentName) {
						farPods = append(farPods, p)
					}
				}

				runningPods := helpers.FilterRunningPods(farPods)
				Expect(runningPods).ToNot(BeEmpty(),
					"No running FAR controller pods found matching name prefix %q",
					farparams.OperatorDeploymentName)

				for _, p := range runningPods {
					By(fmt.Sprintf("Verifying labels on pod %s", p.Object.Name))
					Expect(p.Object.Labels).To(HaveKeyWithValue(
						farparams.ControllerPodLabelKey, farparams.OperatorControllerPodLabel),
						"Pod %s missing expected label", p.Object.Name)
				}
			})

		It("Verify FAR controller container includes expected fence agents",
			reportxml.ID("78407"),
			Label(labels.TierSmoke, labels.DisruptionNonDestructive,
				labels.PlatformAny, labels.ComponentController, labels.FrequencyPresubmit),
			func() {
				By("Waiting for FAR controller pods to be running")

				listOptions := metav1.ListOptions{
					LabelSelector: farparams.OperatorControllerPodLabelSelector,
				}

				_, err := pod.WaitForAllPodsInNamespaceRunning(
					APIClient,
					medik8sparams.OperatorNs,
					medik8sparams.DefaultTimeout,
					listOptions,
				)
				Expect(err).ToNot(HaveOccurred(), "FAR controller pods are not ready")

				By("Listing FAR controller pods")

				farPods, err := pod.List(APIClient, medik8sparams.OperatorNs, listOptions)
				Expect(err).ToNot(HaveOccurred(), "Failed to list FAR pods")

				runningPods := helpers.FilterRunningPods(farPods)
				Expect(runningPods).ToNot(BeEmpty(), "No running FAR controller pods found")

				for _, targetPod := range runningPods {
					By(fmt.Sprintf("Checking fence agents in container %s of pod %s",
						farparams.ManagerContainerName, targetPod.Object.Name))

					buf, err := targetPod.ExecCommand(
						[]string{"ls", "-1", "/usr/sbin"},
						farparams.ManagerContainerName,
					)
					Expect(err).ToNot(HaveOccurred(),
						"Failed to exec in FAR controller pod %s", targetPod.Object.Name)

					output := buf.String()

					var availableAgents []string

					for _, line := range strings.Split(output, "\n") {
						trimmed := strings.TrimSpace(line)
						if strings.HasPrefix(trimmed, farparams.FenceAgentBinaryPrefix) {
							availableAgents = append(availableAgents, trimmed)
						}
					}

					By(fmt.Sprintf("Found %d fence agents in pod %s", len(availableAgents), targetPod.Object.Name))
					Expect(availableAgents).ToNot(BeEmpty(),
						"No fence agent binaries found in /usr/sbin of pod %s", targetPod.Object.Name)

					var missing []string

					for _, expected := range farparams.MinExpectedFenceAgents {
						found := false

						for _, agent := range availableAgents {
							if agent == expected {
								found = true

								break
							}
						}

						if !found {
							missing = append(missing, expected)
						}
					}

					Expect(missing).To(BeEmpty(),
						"Pod %s missing expected fence agents: %s\nAvailable: %s",
						targetPod.Object.Name, strings.Join(missing, ", "), strings.Join(availableAgents, ", "))
				}
			})
	})

func crdIsEstablished(crd *apiextensionsv1.CustomResourceDefinition) bool {
	for _, cond := range crd.Status.Conditions {
		if cond.Type == apiextensionsv1.Established {
			return cond.Status == apiextensionsv1.ConditionTrue
		}
	}

	return false
}
