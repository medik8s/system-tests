package tests

import (
	"context"
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/deployment"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/reportxml"

	"github.com/medik8s/system-tests/tests/internal/labels"
	. "github.com/medik8s/system-tests/tests/internal/medik8sinittools"
	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
	"github.com/medik8s/system-tests/tests/snr-operator/internal/snrparams"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe(
	"SNR CRD and Negative Validation tests",
	Ordered,
	ContinueOnFailure,
	Label(labels.OperatorSNR), func() {
		BeforeAll(func() {
			By("Verify SNR admission webhook is ready before running webhook-dependent tests")

			snrDeployment, err := deployment.Pull(
				APIClient, snrparams.OperatorDeploymentName, medik8sparams.OperatorNs)
			Expect(err).ToNot(HaveOccurred(), "Failed to get SNR deployment")
			Expect(snrDeployment.IsReady(medik8sparams.DefaultTimeout)).To(BeTrue(),
				"SNR deployment is not Ready")
		})

		It("Verify CRD description of safeTimeToAssumeNodeRebootedSeconds",
			reportxml.ID("60824"),
			Label(labels.TierSmoke, labels.DisruptionNonDestructive,
				labels.PlatformAny, labels.FrequencyPresubmit,
				labels.ComponentController), func() {
				By("Getting SelfNodeRemediationConfig CRD")

				snrCRD := &apiextensionsv1.CustomResourceDefinition{}

				err := APIClient.Get(context.TODO(),
					client.ObjectKey{Name: snrparams.SNRCRDName},
					snrCRD)
				Expect(err).ToNot(HaveOccurred(),
					"Failed to get CRD %q", snrparams.SNRCRDName)

				By("Extracting safeTimeToAssumeNodeRebootedSeconds description from storage version")

				Expect(snrCRD.Spec.Versions).ToNot(BeEmpty(), "CRD should have at least one version")

				var description string

				for _, ver := range snrCRD.Spec.Versions {
					if !ver.Storage {
						continue
					}

					props := ver.Schema.OpenAPIV3Schema.Properties["spec"].Properties
					if safeTimeProp, exists := props["safeTimeToAssumeNodeRebootedSeconds"]; exists {
						description = safeTimeProp.Description

						break
					}
				}

				Expect(description).ToNot(BeEmpty(),
					"safeTimeToAssumeNodeRebootedSeconds description not found in CRD")
				Expect(description).To(ContainSubstring(snrparams.SafeTimeToAssumeNodeRebootedDescription),
					"CRD description does not contain expected text")
			})

		It("Verify non-default SNRC creation is rejected",
			reportxml.ID("50961"),
			Label(labels.TierAcceptance, labels.DisruptionNonDestructive,
				labels.PlatformAny, labels.FrequencyPresubmit,
				labels.ComponentController), func() {
				By("Attempting to create a non-default SelfNodeRemediationConfig")

				nonDefaultSNRC := buildSNRCR("SelfNodeRemediationConfig",
					"non-default-snr-config-1", map[string]interface{}{})

				err := APIClient.Create(context.TODO(), nonDefaultSNRC)
				if err == nil {
					deferDeleteCR(nonDefaultSNRC)
				}

				Expect(err).To(HaveOccurred(),
					"Creating a non-default SNRC should be rejected")
				Expect(err.Error()).To(ContainSubstring(snrparams.SNRCNonDefaultErrMsg),
					"Error should mention single SNRC enforcement")
			})

		It("Verify invalid values in SNRC are rejected",
			reportxml.ID("47330"),
			Label(labels.TierAcceptance, labels.DisruptionNonDestructive,
				labels.PlatformAny, labels.FrequencyPresubmit,
				labels.ComponentController), func() {
				By("Creating SNRC with invalid string duration values")

				invalidStringSNRC := buildSNRCR("SelfNodeRemediationConfig",
					snrparams.SNRCTestName, map[string]interface{}{
						"apiServerTimeout": "foo",
						"apiCheckInterval": "string",
					})

				err := APIClient.Create(context.TODO(), invalidStringSNRC)
				if err == nil {
					deferDeleteCR(invalidStringSNRC)
				}

				Expect(err).To(HaveOccurred(), "SNRC with invalid string values should be rejected")
				Expect(err.Error()).To(ContainSubstring("Invalid value"),
					"Error should mention invalid value")

				By("Creating SNRC with too-small duration values")

				invalidDurationSNRC := buildSNRCR("SelfNodeRemediationConfig",
					snrparams.SNRCTestName, map[string]interface{}{
						"apiServerTimeout":     "0s",
						"apiCheckInterval":     "0.2ms",
						"peerApiServerTimeout": "0.001s",
						"peerDialTimeout":      "0.009s",
						"peerRequestTimeout":   "0.003ms",
						"peerUpdateInterval":   "0ms",
					})

				err = APIClient.Create(context.TODO(), invalidDurationSNRC)
				if err == nil {
					deferDeleteCR(invalidDurationSNRC)
				}

				Expect(err).To(HaveOccurred(), "SNRC with too-small duration values should be rejected")

				// The SNR webhook currently aggregates all validation failures into a
				// single error. If it switches to fail-fast, split into per-field tests.
				expectedErrors := []string{
					"ApiServerTimeout cannot be less than 10ms",
					"ApiCheckInterval cannot be less than 1s",
					"PeerApiServerTimeout cannot be less than 10ms",
					"PeerDialTimeout cannot be less than 10ms",
					"PeerRequestTimeout cannot be less than 10ms",
					"PeerUpdateInterval cannot be less than 10s",
				}

				var missingErrors []string

				for _, expectedErr := range expectedErrors {
					if !strings.Contains(err.Error(), expectedErr) {
						missingErrors = append(missingErrors,
							fmt.Sprintf("expected error %q not found", expectedErr))
					}
				}

				if len(missingErrors) > 0 {
					errMsg := fmt.Sprintf("SNRC validation missing expected errors (got: %s):\n", err.Error())
					for _, msg := range missingErrors {
						errMsg += fmt.Sprintf("- %s\n", msg)
					}

					Fail(errMsg)
				}
			})

		It("Verify lastError is captured for non-existent node",
			reportxml.ID("50583"),
			Label(labels.TierAcceptance, labels.DisruptionNonDestructive,
				labels.PlatformAny, labels.FrequencyPresubmit,
				labels.ComponentController), func() {
				By("Verifying test node name does not exist in the cluster")

				nodeObj := &corev1.Node{}
				nodeErr := APIClient.Get(context.TODO(),
					client.ObjectKey{Name: snrparams.SNRTestNodeName},
					nodeObj)
				Expect(k8serrors.IsNotFound(nodeErr)).To(BeTrue(),
					"Node %q must not exist in the cluster to avoid triggering real fencing",
					snrparams.SNRTestNodeName)

				By("Creating SNR with non-existent node name")

				snrCR := buildSNRCR("SelfNodeRemediation", snrparams.SNRTestNodeName, nil)

				err := APIClient.Create(context.TODO(), snrCR)
				Expect(err).ToNot(HaveOccurred(),
					"Failed to create SNR for non-existent node")

				deferDeleteCR(snrCR)

				By("Verifying lastError field is populated")

				liveSNR := &unstructured.Unstructured{}
				liveSNR.SetGroupVersionKind(schema.GroupVersionKind{
					Group:   snrparams.CRDGroup,
					Version: snrparams.CRDVersion,
					Kind:    "SelfNodeRemediation",
				})

				Eventually(func() (string, error) {
					getErr := APIClient.Get(context.TODO(),
						client.ObjectKey{
							Name:      snrparams.SNRTestNodeName,
							Namespace: medik8sparams.OperatorNs,
						},
						liveSNR)
					if getErr != nil {
						return "", getErr
					}

					lastError, _, _ := unstructured.NestedString(
						liveSNR.Object, "status", "lastError")

					return lastError, nil
				}, medik8sparams.DefaultTimeout, snrparams.DefaultPollInterval).Should(
					ContainSubstring(snrparams.SNRLastErrorNodeNotFound),
					"lastError should contain %q", snrparams.SNRLastErrorNodeNotFound)
			})
	})
