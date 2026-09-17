package tests

import (
	"context"
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
	"github.com/medik8s/system-tests/tests/snr-operator/internal/snrparams"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe(
	"SNR Post Deployment tests",
	Ordered,
	ContinueOnFailure,
	Label(labels.OperatorSNR), func() {
		var snrCSV *olm.ClusterServiceVersionBuilder

		BeforeAll(func() {
			By("Get SNR deployment object and verify readiness")

			snrDeployment, err := deployment.Pull(
				APIClient, snrparams.OperatorDeploymentName, medik8sparams.OperatorNs)
			Expect(err).ToNot(HaveOccurred(), "Failed to get SNR deployment")
			Expect(snrDeployment.IsReady(medik8sparams.DefaultTimeout)).To(BeTrue(), "SNR deployment is not Ready")

			By("Get SNR ClusterServiceVersion")

			Eventually(func() error {
				snrCSVs, listErr := olm.ListClusterServiceVersionWithNamePattern(
					APIClient, snrparams.CSVNamePattern, medik8sparams.OperatorNs)
				if listErr != nil {
					return fmt.Errorf("failed to list SNR ClusterServiceVersions: %w", listErr)
				}

				if len(snrCSVs) == 0 {
					return fmt.Errorf("no SNR ClusterServiceVersion found in namespace %s",
						medik8sparams.OperatorNs)
				}

				snrCSV = helpers.FindActiveCSV(snrCSVs)
				if snrCSV == nil {
					return fmt.Errorf("no SNR CSV in Succeeded phase found yet")
				}

				return nil
			}, medik8sparams.DefaultTimeout, snrparams.DefaultPollInterval).Should(Succeed(),
				"SNR CSV must reach Succeeded phase")
		})

		It("Verify SNR resources are installed and running",
			reportxml.ID("54205"),
			Label(labels.TierSmoke, labels.DisruptionNonDestructive,
				labels.PlatformAny, labels.FrequencyPresubmit,
				labels.ComponentDaemonSet), func() {
				By("Verifying SelfNodeRemediationConfig exists")

				snrcObj := &unstructured.Unstructured{}
				snrcObj.SetGroupVersionKind(schema.GroupVersionKind{
					Group:   snrparams.CRDGroup,
					Version: snrparams.CRDVersion,
					Kind:    "SelfNodeRemediationConfig",
				})

				err := APIClient.Get(context.TODO(),
					client.ObjectKey{
						Name:      snrparams.SNRConfigName,
						Namespace: medik8sparams.OperatorNs,
					},
					snrcObj)
				Expect(err).ToNot(HaveOccurred(),
					"SelfNodeRemediationConfig %q not found", snrparams.SNRConfigName)

				By("Verifying SNR DaemonSet pods are running")

				dsListOptions := metav1.ListOptions{
					LabelSelector: snrparams.DaemonSetPodLabelSelector,
				}

				Eventually(func() error {
					dsPods, listErr := pod.List(APIClient, medik8sparams.OperatorNs, dsListOptions)
					if listErr != nil {
						return fmt.Errorf("failed to list SNR DaemonSet pods: %w", listErr)
					}

					if len(dsPods) == 0 {
						return fmt.Errorf("no SNR DaemonSet pods found")
					}

					running := helpers.FilterRunningPods(dsPods)
					if len(running) != len(dsPods) {
						readySet := make(map[string]struct{}, len(running))
						for _, ready := range running {
							readySet[ready.Object.Name] = struct{}{}
						}

						var notReady []string

						for _, candidate := range dsPods {
							if _, ok := readySet[candidate.Object.Name]; !ok {
								notReady = append(notReady, candidate.Object.Name)
							}
						}

						return fmt.Errorf("expected %d running and ready pods, got %d; not ready: %s",
							len(dsPods), len(running), strings.Join(notReady, ", "))
					}

					return nil
				}, medik8sparams.DefaultTimeout, snrparams.DefaultPollInterval).Should(Succeed(),
					"SNR DaemonSet pods are not all running and ready")

				By("Verifying SNR controller-manager pods are running")

				ctrlListOptions := metav1.ListOptions{
					LabelSelector: snrparams.OperatorControllerPodLabelSelector,
				}

				_, err = pod.WaitForAllPodsInNamespaceRunning(
					APIClient,
					medik8sparams.OperatorNs,
					medik8sparams.DefaultTimeout,
					ctrlListOptions,
				)
				Expect(err).ToNot(HaveOccurred(), "SNR controller pods are not running")
			})

		It("Verify only Automatic remediation template exists",
			reportxml.ID("71010"),
			Label(labels.TierSmoke, labels.DisruptionNonDestructive,
				labels.PlatformAny, labels.FrequencyPresubmit,
				labels.ComponentController), func() {
				By("Verifying Automatic SelfNodeRemediationTemplate exists")

				automaticTemplate := &unstructured.Unstructured{}
				automaticTemplate.SetGroupVersionKind(schema.GroupVersionKind{
					Group:   snrparams.CRDGroup,
					Version: snrparams.CRDVersion,
					Kind:    "SelfNodeRemediationTemplate",
				})

				err := APIClient.Get(context.TODO(),
					client.ObjectKey{
						Name:      snrparams.SNRTemplateName,
						Namespace: medik8sparams.OperatorNs,
					},
					automaticTemplate)
				Expect(err).ToNot(HaveOccurred(),
					"Automatic SelfNodeRemediationTemplate %q not found", snrparams.SNRTemplateName)

				By("Verifying remediation strategy is Automatic")

				strategy, found, err := unstructured.NestedString(
					automaticTemplate.Object,
					"spec", "template", "spec", "remediationStrategy")
				Expect(err).ToNot(HaveOccurred(), "Failed to get remediationStrategy field")
				Expect(found).To(BeTrue(), "remediationStrategy field not found in template spec")
				Expect(strategy).To(Equal(snrparams.SNRTemplateExpectedStrategy),
					"Expected remediationStrategy %q, got %q",
					snrparams.SNRTemplateExpectedStrategy, strategy)

				By("Verifying unsupported templates do not exist")

				for _, unsupported := range snrparams.UnsupportedTemplateNames {
					tmpl := &unstructured.Unstructured{}
					tmpl.SetGroupVersionKind(schema.GroupVersionKind{
						Group:   snrparams.CRDGroup,
						Version: snrparams.CRDVersion,
						Kind:    "SelfNodeRemediationTemplate",
					})

					err := APIClient.Get(context.TODO(),
						client.ObjectKey{
							Name:      unsupported,
							Namespace: medik8sparams.OperatorNs,
						},
						tmpl)
					Expect(k8serrors.IsNotFound(err)).To(BeTrue(),
						"Unsupported template %q should not exist", unsupported)
				}
			})

		It("Verify SNR CSV annotations",
			reportxml.ID("52136"),
			Label(labels.TierSmoke, labels.DisruptionNonDestructive,
				labels.PlatformAny, labels.FrequencyPresubmit,
				labels.ComponentOLM), func() {
				By("Checking valid-subscription annotation")

				annotations := snrCSV.Object.Annotations
				Expect(annotations).ToNot(BeNil(), "CSV annotations should not be nil")

				_, hasValidSubscription := annotations["operators.openshift.io/valid-subscription"]
				Expect(hasValidSubscription).To(BeTrue(),
					"CSV should have operators.openshift.io/valid-subscription annotation")

				By("Checking support annotation")

				supportValue, hasSupport := annotations["support"]
				Expect(hasSupport).To(BeTrue(), "CSV should have support annotation")
				Expect(strings.TrimSpace(supportValue)).ToNot(BeEmpty(),
					"CSV support annotation should not be empty")

				By("Checking repository annotation")

				repoValue, hasRepository := annotations["repository"]
				Expect(hasRepository).To(BeTrue(), "CSV should have repository annotation")
				Expect(strings.TrimSpace(repoValue)).ToNot(BeEmpty(),
					"CSV repository annotation should not be empty")

				By("Checking maintainers")

				maintainers := snrCSV.Object.Spec.Maintainers
				Expect(len(maintainers)).To(BeNumerically(">", 0),
					"CSV should have at least one maintainer")
			})

		It("Verify SNR CSV metadata",
			reportxml.ID("70705"),
			Label(labels.TierSmoke, labels.DisruptionNonDestructive,
				labels.PlatformAny, labels.FrequencyPresubmit,
				labels.ComponentOLM), func() {
				By("Checking required CSV annotations")

				annotations := snrCSV.Object.Annotations
				Expect(annotations).ToNot(BeNil(), "CSV annotations should not be nil")

				var annotationErrors []string

				for annotationKey, expectedValue := range snrparams.RequiredAnnotations {
					annotationValue, exists := annotations[annotationKey]
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
					errMsg := "SNR CSV annotation validation failures:\n"
					for _, msg := range annotationErrors {
						errMsg += fmt.Sprintf("- %s\n", msg)
					}

					Fail(errMsg)
				}

				By("Checking replaces field")

				replaces := snrCSV.Object.Spec.Replaces
				Expect(replaces).ToNot(BeEmpty(), "CSV spec.replaces should not be empty")
				Expect(replaces).To(ContainSubstring(snrparams.CSVNamePattern),
					"replaces field should contain %q, got %q", snrparams.CSVNamePattern, replaces)

				By("Checking cluster topology for replica validation")

				infraConfig, err := infrastructure.Pull(APIClient)
				Expect(err).ToNot(HaveOccurred(), "Failed to pull infrastructure configuration")

				if infraConfig.Object.Status.ControlPlaneTopology == configv1.SingleReplicaTopologyMode {
					Skip("Skipping replica validation on SNO (Single Node OpenShift) cluster")
				}

				By("Verifying controller replicas on multi-node cluster")

				Eventually(func() error {
					liveDeploy, pullErr := deployment.Pull(
						APIClient, snrparams.OperatorDeploymentName, medik8sparams.OperatorNs)
					if pullErr != nil {
						return fmt.Errorf("failed to pull deployment: %w", pullErr)
					}

					if liveDeploy.Object.Spec.Replicas == nil {
						return fmt.Errorf("deployment replicas is nil")
					}

					if *liveDeploy.Object.Spec.Replicas != snrparams.ExpectedReplicas {
						return fmt.Errorf("expected %d replica(s), found %d",
							snrparams.ExpectedReplicas, *liveDeploy.Object.Spec.Replicas)
					}

					if liveDeploy.Object.Status.ReadyReplicas != snrparams.ExpectedReplicas {
						return fmt.Errorf("expected %d ready replica(s), found %d",
							snrparams.ExpectedReplicas, liveDeploy.Object.Status.ReadyReplicas)
					}

					return nil
				}, medik8sparams.DefaultTimeout, snrparams.DefaultPollInterval).Should(Succeed(),
					"SNR controller replicas not matching expected count")
			})
	})
