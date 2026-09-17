package tests

import (
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/deployment"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/olm"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/pod"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/reportxml"

	"github.com/medik8s/system-tests/tests/internal/helpers"
	"github.com/medik8s/system-tests/tests/internal/labels"
	. "github.com/medik8s/system-tests/tests/internal/medik8sinittools"
	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
	"github.com/medik8s/system-tests/tests/nmo-operator/internal/nmoparams"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func fetchActiveCSV() *olm.ClusterServiceVersionBuilder {
	var nmoCSV *olm.ClusterServiceVersionBuilder

	Eventually(func() error {
		nmoCSVs, err := olm.ListClusterServiceVersionWithNamePattern(
			APIClient, nmoparams.CSVNamePattern, medik8sparams.OperatorNs)
		if err != nil {
			return fmt.Errorf("failed to list NMO ClusterServiceVersions: %w", err)
		}

		if len(nmoCSVs) == 0 {
			return fmt.Errorf("no NMO ClusterServiceVersion found in namespace %s", medik8sparams.OperatorNs)
		}

		nmoCSV = helpers.FindActiveCSV(nmoCSVs)
		if nmoCSV == nil {
			return fmt.Errorf("no NMO CSV in Succeeded phase found yet")
		}

		return nil
	}, medik8sparams.DefaultTimeout, nmoparams.DefaultPollInterval).Should(Succeed(),
		"NMO CSV must reach Succeeded phase")

	return nmoCSV
}

var _ = Describe(
	"NMO Post Deployment tests",
	Ordered,
	ContinueOnFailure,
	Label(labels.OperatorNMO), func() {
		BeforeAll(func() {
			By("Get NMO deployment object and verify it is Ready")

			nmoDeployment, err := deployment.Pull(
				APIClient, nmoparams.OperatorDeploymentName, medik8sparams.OperatorNs)
			Expect(err).ToNot(HaveOccurred(), "Failed to get NMO deployment")
			Expect(nmoDeployment.IsReady(medik8sparams.DefaultTimeout)).To(BeTrue(),
				"NMO deployment is not Ready")
		})

		It("Verify Node Maintenance Operator pod is running",
			reportxml.ID("46315"),
			Label(
				labels.OperatorNMO,
				labels.DisruptionNonDestructive,
				labels.TierSmoke,
				labels.PlatformAny,
				labels.ComponentController,
				labels.FrequencyPresubmit,
			), func() {
				listOptions := metav1.ListOptions{LabelSelector: nmoparams.OperatorControllerPodLabelSelector}

				By("Verifying pod count matches expected replicas")

				Eventually(func() error {
					allPods, listErr := pod.List(APIClient, medik8sparams.OperatorNs, listOptions)
					if listErr != nil {
						return listErr
					}

					nmoPods := helpers.FilterPodsByDeployment(allPods, nmoparams.OperatorDeploymentName)

					for _, nmoPod := range nmoPods {
						if nmoPod.Object.DeletionTimestamp != nil {
							continue
						}

						if nmoPod.Object.Status.Phase != corev1.PodRunning {
							return fmt.Errorf("pod %s is in phase %s, expected Running",
								nmoPod.Object.Name, nmoPod.Object.Status.Phase)
						}
					}

					runningCount := int32(len(helpers.FilterRunningPods(nmoPods)))

					if runningCount < nmoparams.ExpectedReplicas {
						return fmt.Errorf("expected at least %d running NMO pod(s), found %d",
							nmoparams.ExpectedReplicas, runningCount)
					}

					return nil
				}, medik8sparams.DefaultTimeout, nmoparams.DefaultPollInterval).Should(Succeed(),
					"NMO pods did not reach expected running count of %d", nmoparams.ExpectedReplicas)
			})

		It("Verify NMO CSV has required annotations",
			reportxml.ID("89626"),
			Label(
				labels.OperatorNMO,
				labels.DisruptionNonDestructive,
				labels.TierSmoke,
				labels.PlatformAny,
				labels.ComponentOLM,
				labels.FrequencyPresubmit,
			), func() {
				By("Finding the active (Succeeded) CSV")

				nmoCSV := fetchActiveCSV()

				By("Checking annotation values on NMO CSV")

				Expect(nmoCSV.Object.Annotations).ToNot(BeNil(), "CSV annotations should not be nil")

				var annotationErrors []string

				for annotationKey, expectedValue := range nmoparams.RequiredAnnotations {
					annotationValue, exists := nmoCSV.Object.Annotations[annotationKey]
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
					errMsg := "NMO CSV annotation validation failures:\n"
					for _, msg := range annotationErrors {
						errMsg += fmt.Sprintf("- %s\n", msg)
					}

					Fail(errMsg)
				}
			})

		It("Verify NMO controller manager has correct number of replicas",
			reportxml.ID("89627"),
			Label(
				labels.OperatorNMO,
				labels.DisruptionNonDestructive,
				labels.TierSmoke,
				labels.PlatformAny,
				labels.ComponentController,
				labels.FrequencyPresubmit,
			), func() {
				By("Verifying replica count and ready replicas")

				Eventually(func() error {
					liveDeploy, pullErr := deployment.Pull(
						APIClient, nmoparams.OperatorDeploymentName, medik8sparams.OperatorNs)
					if pullErr != nil {
						return pullErr
					}

					if liveDeploy.Object.Spec.Replicas == nil ||
						*liveDeploy.Object.Spec.Replicas != nmoparams.ExpectedReplicas {
						desired := int32(0)
						if liveDeploy.Object.Spec.Replicas != nil {
							desired = *liveDeploy.Object.Spec.Replicas
						}

						return fmt.Errorf("expected %d desired replica(s), found %d",
							nmoparams.ExpectedReplicas, desired)
					}

					if liveDeploy.Object.Status.ReadyReplicas != nmoparams.ExpectedReplicas {
						return fmt.Errorf("expected %d ready replica(s), found %d",
							nmoparams.ExpectedReplicas, liveDeploy.Object.Status.ReadyReplicas)
					}

					return nil
				}, medik8sparams.DefaultTimeout, nmoparams.DefaultPollInterval).Should(Succeed(),
					"NMO deployment did not stabilise at %d ready replica(s)",
					nmoparams.ExpectedReplicas)
			})

		It("Verify NMO container runs as non-root user",
			reportxml.ID("89628"),
			Label(
				labels.OperatorNMO,
				labels.DisruptionNonDestructive,
				labels.TierSmoke,
				labels.PlatformAny,
				labels.ComponentController,
				labels.FrequencyPresubmit,
			), func() {
				By("Getting NMO controller pod names")

				listOptions := metav1.ListOptions{LabelSelector: nmoparams.OperatorControllerPodLabelSelector}

				var runningPods []*pod.Builder

				Eventually(func() error {
					allPods, listErr := pod.List(APIClient, medik8sparams.OperatorNs, listOptions)
					if listErr != nil {
						return listErr
					}

					running := helpers.FilterRunningPods(
						helpers.FilterPodsByDeployment(allPods, nmoparams.OperatorDeploymentName))
					if len(running) == 0 {
						return fmt.Errorf("no running NMO controller pods found")
					}

					runningPods = running

					return nil
				}, medik8sparams.DefaultTimeout, nmoparams.DefaultPollInterval).Should(Succeed(),
					"At least one running NMO controller pod should be found")

				errorMessages := helpers.ValidateNonRootSecurityContext(
					runningPods, nmoparams.ManagerContainerName, true)

				if len(errorMessages) > 0 {
					Fail("Testing security context of NMO container failed due to:\n- " +
						strings.Join(errorMessages, "\n- "))
				}
			})
	})
