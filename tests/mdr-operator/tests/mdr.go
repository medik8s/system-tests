package tests

import (
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"

	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/deployment"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/infrastructure"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/olm"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/pod"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/reportxml"

	"github.com/medik8s/system-tests/tests/internal/helpers"
	"github.com/medik8s/system-tests/tests/internal/labels"
	. "github.com/medik8s/system-tests/tests/internal/medik8sinittools"
	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
	"github.com/medik8s/system-tests/tests/mdr-operator/internal/mdrparams"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func fetchActiveCSV() *olm.ClusterServiceVersionBuilder {
	var mdrCSV *olm.ClusterServiceVersionBuilder

	Eventually(func() error {
		mdrCSVs, err := olm.ListClusterServiceVersionWithNamePattern(
			APIClient, mdrparams.CSVNamePattern, medik8sparams.OperatorNs)
		if err != nil {
			return fmt.Errorf("failed to list MDR ClusterServiceVersions: %w", err)
		}

		if len(mdrCSVs) == 0 {
			return fmt.Errorf("no MDR ClusterServiceVersion found in namespace %s", medik8sparams.OperatorNs)
		}

		mdrCSV = helpers.FindActiveCSV(mdrCSVs)
		if mdrCSV == nil {
			return fmt.Errorf("no MDR CSV in Succeeded phase found yet")
		}

		return nil
	}, medik8sparams.DefaultTimeout, mdrparams.DefaultPollInterval).Should(Succeed(),
		"MDR CSV must reach Succeeded phase")

	return mdrCSV
}

var _ = Describe(
	"MDR Post Deployment tests",
	Ordered,
	ContinueOnFailure,
	Label(labels.OperatorMDR), func() {
		var controlPlaneTopology configv1.TopologyMode

		BeforeAll(func() {
			By("Get MDR deployment object and verify it is Ready")

			mdrDeployment, err := deployment.Pull(
				APIClient, mdrparams.OperatorDeploymentName, medik8sparams.OperatorNs)
			Expect(err).ToNot(HaveOccurred(), "Failed to get MDR deployment")
			Expect(mdrDeployment.IsReady(medik8sparams.DefaultTimeout)).To(BeTrue(),
				"MDR deployment is not Ready")

			By("Pull cluster topology for use in topology-aware tests")

			infraConfig, infraErr := infrastructure.Pull(APIClient)
			Expect(infraErr).ToNot(HaveOccurred(), "Failed to pull infrastructure configuration")

			controlPlaneTopology = infraConfig.Object.Status.ControlPlaneTopology
		})

		It("Verify Machine Deletion Remediation Operator pod is running",
			reportxml.ID("65767"),
			Label(
				labels.OperatorMDR,
				labels.DisruptionNonDestructive,
				labels.TierSmoke,
				labels.PlatformAny,
				labels.ComponentController,
				labels.FrequencyPresubmit,
			), func() {
				expectedCount := mdrparams.ExpectedReplicas
				if controlPlaneTopology == configv1.SingleReplicaTopologyMode {
					expectedCount = int32(1)
				}

				listOptions := metav1.ListOptions{LabelSelector: mdrparams.OperatorControllerPodLabelSelector}

				By("Verifying pod count matches expected replicas")

				Eventually(func() error {
					allPods, listErr := pod.List(APIClient, medik8sparams.OperatorNs, listOptions)
					if listErr != nil {
						return listErr
					}

					mdrPods := helpers.FilterPodsByDeployment(allPods, mdrparams.OperatorDeploymentName)

					for _, mdrPod := range mdrPods {
						if mdrPod.Object.DeletionTimestamp != nil {
							continue
						}

						if mdrPod.Object.Status.Phase != corev1.PodRunning {
							return fmt.Errorf("pod %s is in phase %s, expected Running",
								mdrPod.Object.Name, mdrPod.Object.Status.Phase)
						}
					}

					runningCount := int32(len(helpers.FilterRunningPods(mdrPods)))

					if runningCount != expectedCount {
						return fmt.Errorf("expected %d running MDR pod(s), found %d",
							expectedCount, runningCount)
					}

					return nil
				}, medik8sparams.DefaultTimeout, mdrparams.DefaultPollInterval).Should(Succeed(),
					"MDR pods did not reach expected running count of %d", expectedCount)
			})

		It("Verify MDR CSV has required annotations",
			reportxml.ID("70221"),
			Label(
				labels.OperatorMDR,
				labels.DisruptionNonDestructive,
				labels.TierSmoke,
				labels.PlatformAny,
				labels.ComponentOLM,
				labels.FrequencyPresubmit,
			), func() {
				By("Finding the active (Succeeded) CSV")

				mdrCSV := fetchActiveCSV()

				By("Checking annotation values on MDR CSV")

				Expect(mdrCSV.Object.Annotations).ToNot(BeNil(), "CSV annotations should not be nil")

				var annotationErrors []string

				for annotationKey, expectedValue := range mdrparams.RequiredAnnotations {
					annotationValue, exists := mdrCSV.Object.Annotations[annotationKey]
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
					errMsg := "MDR CSV annotation validation failures:\n"
					for _, msg := range annotationErrors {
						errMsg += fmt.Sprintf("- %s\n", msg)
					}

					Fail(errMsg)
				}
			})

		It("Verify MDR controller manager has correct number of replicas",
			reportxml.ID("89624"),
			Label(
				labels.OperatorMDR,
				labels.DisruptionNonDestructive,
				labels.TierSmoke,
				labels.PlatformAny,
				labels.ComponentController,
				labels.FrequencyPresubmit,
			), func() {
				By("Checking cluster topology")

				if controlPlaneTopology == configv1.SingleReplicaTopologyMode {
					Skip("Skipping test on SNO (Single Node OpenShift) cluster")
				}

				By("Verifying replica count, ready replicas, and pod HA distribution")

				listOptions := metav1.ListOptions{LabelSelector: mdrparams.OperatorControllerPodLabelSelector}

				Eventually(func() error {
					liveDeploy, pullErr := deployment.Pull(
						APIClient, mdrparams.OperatorDeploymentName, medik8sparams.OperatorNs)
					if pullErr != nil {
						return pullErr
					}

					if liveDeploy.Object.Spec.Replicas == nil ||
						*liveDeploy.Object.Spec.Replicas != mdrparams.ExpectedReplicas {
						desired := int32(0)
						if liveDeploy.Object.Spec.Replicas != nil {
							desired = *liveDeploy.Object.Spec.Replicas
						}

						return fmt.Errorf("expected %d desired replica(s), found %d",
							mdrparams.ExpectedReplicas, desired)
					}

					if liveDeploy.Object.Status.ReadyReplicas != mdrparams.ExpectedReplicas {
						return fmt.Errorf("expected %d ready replica(s), found %d",
							mdrparams.ExpectedReplicas, liveDeploy.Object.Status.ReadyReplicas)
					}

					allPods, listErr := pod.List(APIClient, medik8sparams.OperatorNs, listOptions)
					if listErr != nil {
						return listErr
					}

					runningPods := helpers.FilterRunningPods(
						helpers.FilterPodsByDeployment(allPods, mdrparams.OperatorDeploymentName))

					if len(runningPods) != int(mdrparams.ExpectedReplicas) {
						return fmt.Errorf("expected %d running MDR pod(s) for HA check, found %d",
							mdrparams.ExpectedReplicas, len(runningPods))
					}

					nodeNames := make(map[string]bool)

					for _, p := range runningPods {
						if p.Object.Spec.NodeName == "" {
							return fmt.Errorf("pod %s has not been assigned to a node", p.Object.Name)
						}

						nodeNames[p.Object.Spec.NodeName] = true
					}

					if len(nodeNames) != int(mdrparams.ExpectedReplicas) {
						return fmt.Errorf(
							"MDR pods must run on different nodes for HA, found pods on %d unique node(s)",
							len(nodeNames))
					}

					return nil
				}, medik8sparams.DefaultTimeout, mdrparams.DefaultPollInterval).Should(Succeed(),
					"MDR deployment did not stabilise at %d ready replicas on distinct nodes",
					mdrparams.ExpectedReplicas)
			})

		It("Verify MDR container runs as non-root user",
			reportxml.ID("89625"),
			Label(
				labels.OperatorMDR,
				labels.DisruptionNonDestructive,
				labels.TierSmoke,
				labels.PlatformAny,
				labels.ComponentController,
				labels.FrequencyPresubmit,
			), func() {
				By("Getting MDR controller pod names")

				listOptions := metav1.ListOptions{LabelSelector: mdrparams.OperatorControllerPodLabelSelector}

				var runningPods []*pod.Builder

				Eventually(func() error {
					allPods, listErr := pod.List(APIClient, medik8sparams.OperatorNs, listOptions)
					if listErr != nil {
						return listErr
					}

					running := helpers.FilterRunningPods(
						helpers.FilterPodsByDeployment(allPods, mdrparams.OperatorDeploymentName))
					if len(running) == 0 {
						return fmt.Errorf("no running MDR controller pods found")
					}

					runningPods = running

					return nil
				}, medik8sparams.DefaultTimeout, mdrparams.DefaultPollInterval).Should(Succeed(),
					"At least one running MDR controller pod should be found")

				errorMessages := helpers.ValidateNonRootSecurityContext(
					runningPods, mdrparams.ManagerContainerName, true)

				if len(errorMessages) > 0 {
					Fail("Testing security context of MDR container failed due to:\n- " +
						strings.Join(errorMessages, "\n- "))
				}
			})
	})
