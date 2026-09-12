package tests

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	nmov1beta1 "github.com/medik8s/node-maintenance-operator/api/v1beta1"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/deployment"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/reportxml"

	"github.com/medik8s/system-tests/tests/internal/helpers"
	"github.com/medik8s/system-tests/tests/internal/labels"
	. "github.com/medik8s/system-tests/tests/internal/medik8sinittools"
	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
	"github.com/medik8s/system-tests/tests/nmo-operator/internal/nmoparams"
)

var nmGVK = schema.GroupVersionKind{
	Group:   nmoparams.CRDGroup,
	Version: nmoparams.CRDVersion,
	Kind:    nmoparams.KindNodeMaintenance,
}

// buildNMUnstructured returns an unstructured NodeMaintenance CR. It is used for negative
// tests that must send field values a typed nmov1beta1.NodeMaintenance struct cannot
// represent -- e.g. an integer in the string-typed spec.reason field.
func buildNMUnstructured(name, nodeName string, reason interface{}) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": nmoparams.CRDGroup + "/" + nmoparams.CRDVersion,
			"kind":       nmoparams.KindNodeMaintenance,
			"metadata": map[string]interface{}{
				"name": name,
			},
			"spec": map[string]interface{}{
				"nodeName": nodeName,
				"reason":   reason,
			},
		},
	}
}

// This suite covers only the invalid-node (OCP-29598) and malformed-data (OCP-52834)
// negative cases. Duplicate-CR rejection and spec.nodeName immutability are tracked
// separately and are intentionally out of scope here.
var _ = Describe("NMO Negative Validation",
	Serial, Ordered, ContinueOnFailure,
	Label(labels.OperatorNMO, nmoparams.Label,
		labels.DisruptionNonDestructive, labels.FrequencyWeekly),
	func() {
		var ctx context.Context

		// allNegativeTestNames drives the BeforeAll pre-clean and AfterEach sweep. In a
		// normal run NONE of these CRs is ever persisted -- every create is rejected at
		// admission -- so the sweep is a no-op regression-safety net that only matters if a
		// webhook/schema regression lets an invalid CR through. MalformedNMName is omitted
		// because its name is invalid and can never be created or fetched.
		allNegativeTestNames := []string{
			nmoparams.InvalidNodeNMName,
			nmoparams.IncorrectFormatNMName,
		}

		BeforeAll(func() {
			ctx = context.Background()

			By("Registering NMO API scheme")

			err := APIClient.AttachScheme(nmov1beta1.AddToScheme)
			Expect(err).ToNot(HaveOccurred(), "Failed to register NMO scheme")

			By("Verifying NMO deployment is Ready")

			nmoDeployment, err := deployment.Pull(
				APIClient, nmoparams.OperatorDeploymentName, medik8sparams.OperatorNs)
			Expect(err).ToNot(HaveOccurred(), "Failed to get NMO deployment")
			Expect(nmoDeployment.IsReady(medik8sparams.DefaultTimeout)).To(BeTrue(),
				"NMO deployment is not Ready")

			By("Pre-cleaning stale NodeMaintenance CRs from previous interrupted runs")

			for _, name := range allNegativeTestNames {
				cleanupNMCR(ctx, name)
			}
		})

		AfterEach(func() {
			for _, name := range allNegativeTestNames {
				cleanupNMCR(ctx, name)
			}
		})

		Context("webhook rejection", func() {
			It("Reject NodeMaintenance referencing a non-existent node",
				reportxml.ID("29598"),
				Label(labels.TierAcceptance, labels.PlatformAny,
					labels.ComponentWebhook), func() {
					By(fmt.Sprintf("Confirming no node named %q exists on the cluster",
						nmoparams.InvalidNodeName))

					getErr := APIClient.Get(ctx,
						client.ObjectKey{Name: nmoparams.InvalidNodeName}, &corev1.Node{})
					Expect(k8serrors.IsNotFound(getErr)).To(BeTrue(),
						fmt.Sprintf("Test requires node %q to be absent; Get returned: %v",
							nmoparams.InvalidNodeName, getErr))

					By("Attempting to create a NodeMaintenance CR for the non-existent node")

					nodeMaintenance := buildNMUnstructured(
						nmoparams.InvalidNodeNMName, nmoparams.InvalidNodeName, nmoparams.ValidReason)

					err := APIClient.Create(ctx, nodeMaintenance)
					Expect(err).To(HaveOccurred(),
						"NodeMaintenance creation for a non-existent node should fail")
					Expect(err).To(MatchError(ContainSubstring(
						fmt.Sprintf(nmoparams.WebhookInvalidNodeNameFmt, nmoparams.InvalidNodeName))),
						"Rejection message did not match expected invalid-nodeName error")

					By("Verifying the NodeMaintenance CR was not created")

					absentErr := APIClient.Get(ctx,
						client.ObjectKey{Name: nmoparams.InvalidNodeNMName}, &nmov1beta1.NodeMaintenance{})
					Expect(k8serrors.IsNotFound(absentErr)).To(BeTrue(),
						"NodeMaintenance CR should not exist after a rejected create")
				})
		})

		Context("API server schema and name validation", func() {
			It("Reject NodeMaintenance with malformed field data",
				reportxml.ID("52834"),
				Label(labels.TierAcceptance, labels.PlatformAny,
					labels.ComponentController), func() {
					// A non-existent node name is used deliberately: schema (type) and
					// metadata.name validation run before the node-existence webhook, so no
					// real node is needed and none is ever touched.
					By("Attempting to create a NodeMaintenance CR with an integer reason")

					wrongTypeNM := buildNMUnstructured(
						nmoparams.IncorrectFormatNMName, nmoparams.InvalidNodeName, nmoparams.IncorrectFormatReason)

					wrongTypeErr := APIClient.Create(ctx, wrongTypeNM)
					Expect(wrongTypeErr).To(HaveOccurred(),
						"NodeMaintenance creation with an integer reason should fail")
					Expect(wrongTypeErr).To(MatchError(ContainSubstring(nmoparams.SchemaReasonTypeMsg)),
						"Rejection message did not match expected reason-type error")

					By("Verifying the NodeMaintenance CR was not created")

					absentErr := APIClient.Get(ctx,
						client.ObjectKey{Name: nmoparams.IncorrectFormatNMName}, &nmov1beta1.NodeMaintenance{})
					Expect(k8serrors.IsNotFound(absentErr)).To(BeTrue(),
						"NodeMaintenance CR should not exist after a rejected create")

					By("Attempting to create a NodeMaintenance CR with a malformed name")

					badNameNM := buildNMUnstructured(
						nmoparams.MalformedNMName, nmoparams.InvalidNodeName, nmoparams.ValidReason)

					badNameErr := APIClient.Create(ctx, badNameNM)
					Expect(badNameErr).To(HaveOccurred(),
						"NodeMaintenance creation with a malformed name should fail")
					Expect(badNameErr).To(MatchError(ContainSubstring(nmoparams.SchemaBadNameMsg)),
						"Rejection message did not match expected malformed-name error")
				})
		})
	})

// cleanupNMCR best-effort deletes a NodeMaintenance CR by name and waits for it to
// disappear, logging a warning on failure instead of failing the spec. Mirrors the
// cross-operator cleanup pattern (helpers.DeleteRemediationCR).
func cleanupNMCR(ctx context.Context, name string) {
	helpers.DeleteRemediationCR(
		ctx, APIClient, nmGVK, name, "",
		nmoparams.DefaultPollInterval, nmoparams.CRDeletionTimeout,
		GinkgoWriter.Printf)
}
