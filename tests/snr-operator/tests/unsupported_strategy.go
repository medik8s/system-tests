package tests

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/reportxml"

	"github.com/medik8s/system-tests/tests/internal/labels"
	. "github.com/medik8s/system-tests/tests/internal/medik8sinittools"
	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
	"github.com/medik8s/system-tests/tests/snr-operator/internal/snrparams"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const unsupportedStrategy = "NodeDeletion"

var _ = Describe(
	"SNR Unsupported Strategy tests",
	Label(labels.OperatorSNR), func() {
		BeforeEach(func() {
			// These tests validate CRD/CEL admission rules for SNR and SNRT kinds.
			// Check the CRDs actually used by the tests to catch partial installs.
			By("Verify SNR and SNRT CRDs exist")

			for _, crdName := range []string{
				"selfnoderemediations." + snrparams.CRDGroup,
				"selfnoderemediationtemplates." + snrparams.CRDGroup,
			} {
				crd := &apiextensionsv1.CustomResourceDefinition{}
				err := APIClient.Get(context.TODO(), client.ObjectKey{Name: crdName}, crd)
				Expect(err).ToNot(HaveOccurred(), "CRD %q not found", crdName)
			}
		})

		It("Verify SNR with unsupported NodeDeletion strategy is rejected",
			reportxml.ID("60877"),
			Label(labels.TierAcceptance, labels.DisruptionNonDestructive,
				labels.PlatformAny, labels.FrequencyPresubmit,
				labels.ComponentController), func() {
				By("Creating SNR with NodeDeletion remediationStrategy")

				snrCR := buildSNRCR("SelfNodeRemediation", "test-unsupported-snr",
					map[string]interface{}{
						"remediationStrategy": unsupportedStrategy,
					})

				err := APIClient.Create(context.TODO(), snrCR)
				if err == nil {
					deferDeleteCR(snrCR)
				}

				Expect(err).To(MatchError(ContainSubstring("Unsupported value")),
					"Error should mention unsupported value")
				Expect(err).To(MatchError(ContainSubstring(unsupportedStrategy)),
					"Error should mention the unsupported strategy")
			})

		It("Verify SNRT with unsupported NodeDeletion strategy is rejected",
			reportxml.ID("60822"),
			Label(labels.TierAcceptance, labels.DisruptionNonDestructive,
				labels.PlatformAny, labels.FrequencyPresubmit,
				labels.ComponentController), func() {
				By("Creating SelfNodeRemediationTemplate with NodeDeletion strategy")

				snrtCR := &unstructured.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": snrparams.CRDGroup + "/" + snrparams.CRDVersion,
						"kind":       "SelfNodeRemediationTemplate",
						"metadata": map[string]interface{}{
							"name":      "test-unsupported-snrt",
							"namespace": medik8sparams.OperatorNs,
						},
						"spec": map[string]interface{}{
							"template": map[string]interface{}{
								"spec": map[string]interface{}{
									"remediationStrategy": unsupportedStrategy,
								},
							},
						},
					},
				}

				err := APIClient.Create(context.TODO(), snrtCR)
				if err == nil {
					deferDeleteCR(snrtCR)
				}

				Expect(err).To(MatchError(ContainSubstring("Unsupported value")),
					"Error should mention unsupported value")
				Expect(err).To(MatchError(ContainSubstring(unsupportedStrategy)),
					"Error should mention the unsupported strategy")
			})
	})
