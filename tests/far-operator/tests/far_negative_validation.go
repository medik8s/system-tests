package tests

import (
	"context"
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/deployment"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/pod"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/reportxml"

	"github.com/medik8s/system-tests/tests/far-operator/internal/farparams"
	"github.com/medik8s/system-tests/tests/internal/helpers"
	"github.com/medik8s/system-tests/tests/internal/labels"
	. "github.com/medik8s/system-tests/tests/internal/medik8sinittools"
	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
)

var _ = Describe("FAR Negative -- Misconfiguration",
	Serial, Ordered, ContinueOnFailure,
	Label(labels.OperatorFAR, farparams.Label,
		labels.DisruptionNonDestructive, labels.FrequencyWeekly,
		labels.TierAcceptance, labels.PlatformAny),
	func() {
		var ctx context.Context

		BeforeAll(func() {
			ctx = context.Background()

			By("Verifying FAR controller deployment is Ready")

			farDeployment, err := deployment.Pull(
				APIClient, farparams.OperatorDeploymentName, medik8sparams.OperatorNs)
			Expect(err).ToNot(HaveOccurred(), "Failed to get FAR deployment")
			Expect(farDeployment.IsReady(medik8sparams.DefaultTimeout)).To(BeTrue(),
				"FAR deployment is not Ready -- webhook will be unreachable")

			By("Waiting for the FAR admission webhook to be reachable")

			waitForFARWebhookReady(ctx)

			By("Verifying MisconfigTestCRName is not a real cluster node")

			node := &corev1.Node{}
			err = APIClient.Get(ctx, client.ObjectKey{Name: farparams.MisconfigTestCRName}, node)
			Expect(k8serrors.IsNotFound(err)).To(BeTrue(),
				"Test requires %q to not be a real node name -- a matching node would trigger fencing",
				farparams.MisconfigTestCRName)

			By("Pre-cleaning stale FAR CRs from previous interrupted runs")

			cleanupFARCR(farparams.MisconfigTestCRName)
			cleanupFARCR(farparams.WebhookTestCRName)
			cleanupFARTemplateCR(farparams.MisconfigFARTemplateName)

			DeferCleanup(func() {
				By("Cleaning up log-test FAR CR")
				cleanupFARCR(farparams.MisconfigTestCRName)

				By("Cleaning up webhook-test FAR CR")
				cleanupFARCR(farparams.WebhookTestCRName)

				By("Cleaning up test FARTemplate")
				cleanupFARTemplateCR(farparams.MisconfigFARTemplateName)
			})
		})

		Context("controller log messages", func() {
			It("should log node-not-found error for CR with non-existent node name",
				reportxml.ID("65954"),
				Label(labels.ComponentRemediation),
				func() {
					By("Building FAR CR with name that does not match any cluster node")

					farCR := buildFARForNegativeTest(
						farparams.MisconfigTestCRName,
						farparams.FenceAgentIPMI,
						ipmiSharedParams(nil),
						ipmiNodeParams(farparams.MisconfigTestCRName))

					By("Creating FAR CR")

					Expect(APIClient.Create(ctx, farCR)).To(Succeed(),
						"Failed to create FAR CR with non-existent node name")

					By("Verifying FAR CR exists")

					created := &unstructured.Unstructured{}
					created.SetGroupVersionKind(farGVK)
					Expect(APIClient.Get(ctx, client.ObjectKey{
						Name:      farparams.MisconfigTestCRName,
						Namespace: medik8sparams.OperatorNs,
					}, created)).To(Succeed(),
						"FAR CR %s should exist after creation", farparams.MisconfigTestCRName)

					By("Waiting for node-not-found message in FAR controller logs")

					Eventually(func() error {
						return findAnyMessageInFARControllerLogs(
							farparams.NodeNotFoundMsgs...)
					}, farparams.LogSearchTimeout, farparams.DefaultPollInterval).Should(Succeed(),
						"FAR controller logs should contain one of %v",
						farparams.NodeNotFoundMsgs)
				})
		})

		Context("webhook rejection", Label(labels.ComponentWebhook), func() {
			It("should reject FAR CR with unsupported action",
				reportxml.ID("66090"),
				func() {
					verifyAdmissionRejections(ctx,
						admissionRejectionCase{
							desc: "Creating FAR CR with unsupported --action=status",
							cr: buildFARForNegativeTest(
								farparams.WebhookTestCRName,
								farparams.FenceAgentIPMI,
								ipmiSharedParams(map[string]interface{}{"--action": "status"}),
								ipmiNodeParams(farparams.WebhookTestCRName)),
							wantSubstring: farparams.UnsupportedActionMsg,
						},
					)
				})

			// OCP-71219 covers two verifications (unsupported agent + invalid
			// fence_ prefix) under a single Polarion ID, matching the test plan.
			// Both are kept in one It block to avoid duplicating the reportxml.ID
			// -- duplicate IDs cause one Polarion result to overwrite the other.
			It("should reject FAR CR with unsupported or invalid fence agent name",
				reportxml.ID("71219"),
				func() {
					verifyAdmissionRejections(ctx,
						admissionRejectionCase{
							desc: "Creating FAR CR with unsupported fence agent (fence_incorrect)",
							cr: buildFARForNegativeTest(
								farparams.WebhookTestCRName,
								farparams.MisconfigUnsupportedAgent,
								ipmiSharedParams(nil),
								ipmiNodeParams(farparams.WebhookTestCRName)),
							wantSubstring: farparams.UnsupportedAgentMsg,
						},
						admissionRejectionCase{
							desc: "Creating FAR CR with agent name missing fence_ prefix (incorrect_fence)",
							cr: buildFARForNegativeTest(
								farparams.WebhookTestCRName,
								farparams.MisconfigInvalidPrefixAgent,
								ipmiSharedParams(nil),
								ipmiNodeParams(farparams.WebhookTestCRName)),
							wantSubstring: farparams.InvalidAgentPatternFARMsg,
						},
					)
				})

			// OCP-71220 covers the same two verifications for FARTemplate under a
			// single Polarion ID (see the OCP-71219 note above).
			It("should reject FARTemplate with unsupported or invalid fence agent name",
				reportxml.ID("71220"),
				func() {
					verifyAdmissionRejections(ctx,
						admissionRejectionCase{
							desc: "Creating FARTemplate with unsupported fence agent (fence_incorrect)",
							cr: buildFARTemplateUnstructured(
								farparams.MisconfigFARTemplateName,
								farparams.MisconfigUnsupportedAgent,
								ipmiSharedParams(nil),
								ipmiNodeParams(farparams.PlaceholderNodeName)),
							wantSubstring: farparams.UnsupportedAgentMsg,
						},
						admissionRejectionCase{
							desc: "Creating FARTemplate with agent name missing fence_ prefix (incorrect_fence)",
							cr: buildFARTemplateUnstructured(
								farparams.MisconfigFARTemplateName,
								farparams.MisconfigInvalidPrefixAgent,
								ipmiSharedParams(nil),
								ipmiNodeParams(farparams.PlaceholderNodeName)),
							wantSubstring: farparams.InvalidAgentPatternFARTemplateMsg,
						},
					)
				})
		})
	})

// waitForFARWebhookReady blocks until the FAR mutating admission webhook is
// reachable. deployment.IsReady only confirms the controller pods are Ready;
// the webhook server and its backing Service endpoints can still lag, so the
// first CR create races with them and fails with
// "failed calling webhook ... context deadline exceeded". This probes the
// webhook with a dry-run create -- which still invokes the webhook but persists
// nothing -- and retries until the call no longer fails on connectivity.
func waitForFARWebhookReady(ctx context.Context) {
	GinkgoHelper()

	probe := buildFARForNegativeTest(
		"far-webhook-readiness-probe",
		farparams.FenceAgentIPMI,
		ipmiSharedParams(nil),
		ipmiNodeParams("far-webhook-readiness-probe"))

	Eventually(func() error {
		err := APIClient.Create(ctx, probe, client.DryRunAll)
		if err != nil && strings.Contains(err.Error(), "failed calling webhook") {
			return err
		}

		// Any other outcome (admitted, or rejected on content) means the
		// webhook endpoint is reachable, which is all we are waiting for.
		return nil
	}, farparams.WebhookReadyTimeout, farparams.DefaultPollInterval).Should(Succeed(),
		"FAR admission webhook did not become reachable")
}

// cleanupFARCR safely deletes a FenceAgentsRemediation CR by name.
func cleanupFARCR(name string) {
	GinkgoHelper()

	helpers.DeleteRemediationCR(
		context.Background(), APIClient, farGVK, name, medik8sparams.OperatorNs,
		farparams.DefaultPollInterval, farparams.RemediationCRDeletionTimeout,
		GinkgoWriter.Printf)
}

// cleanupFARTemplateCR safely deletes a FenceAgentsRemediationTemplate CR by name.
func cleanupFARTemplateCR(name string) {
	GinkgoHelper()

	helpers.DeleteRemediationCR(
		context.Background(), APIClient, farTemplateGVK, name, medik8sparams.OperatorNs,
		farparams.DefaultPollInterval, farparams.RemediationCRDeletionTimeout,
		GinkgoWriter.Printf)
}

// admissionRejectionCase is one create-and-expect-rejection sub-case that
// belongs to a larger Polarion test case with multiple verification steps.
type admissionRejectionCase struct {
	desc          string
	cr            *unstructured.Unstructured
	wantSubstring string
}

// verifyAdmissionRejections runs every sub-case -- creating each CR and
// asserting the API server rejects it with the expected message -- collecting
// all failures so every sub-case executes even if an earlier one fails. The
// sub-cases share one It block / reportxml.ID because the Polarion test plan
// defines them as a single test case with multiple verification steps.
func verifyAdmissionRejections(ctx context.Context, cases ...admissionRejectionCase) {
	GinkgoHelper()

	var failures []string

	for _, testCase := range cases {
		By(testCase.desc)

		err := APIClient.Create(ctx, testCase.cr)

		matched, matchErr := MatchError(ContainSubstring(testCase.wantSubstring)).Match(err)
		if matchErr != nil || !matched {
			failures = append(failures,
				fmt.Sprintf("%s: expected error containing %q, got: %v",
					testCase.desc, testCase.wantSubstring, err))
		}
	}

	if len(failures) > 0 {
		Fail("admission rejection failures:\n- " + strings.Join(failures, "\n- "))
	}
}

// findAnyMessageInFARControllerLogs fetches the full log from all running
// FAR controller pods, then checks for any of the given messages in a
// single pass. Uses GetFullLog (no sinceSeconds) to avoid clock-skew
// issues between the test runner and pod nodes.
func findAnyMessageInFARControllerLogs(messages ...string) error {
	farPods, err := pod.List(APIClient, medik8sparams.OperatorNs, metav1.ListOptions{
		LabelSelector: farparams.OperatorControllerPodLabelSelector,
	})
	if err != nil {
		return fmt.Errorf("failed to list FAR controller pods: %w", err)
	}

	runningPods := helpers.FilterRunningPods(farPods)
	if len(runningPods) == 0 {
		return fmt.Errorf("no running FAR controller pods found (total listed: %d)", len(farPods))
	}

	var lastLogErr error

	for _, farPod := range runningPods {
		logStr, logErr := farPod.GetFullLog(farparams.ManagerContainerName)
		if logErr != nil {
			lastLogErr = fmt.Errorf("pod %s: %w", farPod.Object.Name, logErr)

			continue
		}

		for _, msg := range messages {
			if strings.Contains(logStr, msg) {
				return nil
			}
		}
	}

	if lastLogErr != nil {
		return fmt.Errorf("none of %v found; last log error: %w", messages, lastLogErr)
	}

	return fmt.Errorf("none of %v found in any FAR controller pod logs",
		messages)
}

// buildFARForNegativeTest builds a FAR CR without sharedSecretName.
// The validating webhook checks that the referenced secret exists, so
// negative tests that need the CR to pass admission (e.g. OCP-65954)
// must omit it.
func buildFARForNegativeTest(
	name, agent string,
	sharedParams, nodeParams map[string]interface{},
) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "fence-agents-remediation.medik8s.io/v1alpha1",
			"kind":       "FenceAgentsRemediation",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": medik8sparams.OperatorNs,
			},
			"spec": map[string]interface{}{
				"agent":               agent,
				"sharedparameters":    sharedParams,
				"nodeparameters":      nodeParams,
				"retrycount":          farparams.FARCRRetryCount,
				"retryinterval":       farparams.FARCRRetryInterval,
				"timeout":             farparams.FARCRTimeout,
				"remediationStrategy": farparams.FARCRRemediationStrategy,
			},
		},
	}
}

// ipmiSharedParams returns default IPMI shared parameters with optional overrides.
func ipmiSharedParams(overrides map[string]interface{}) map[string]interface{} {
	params := map[string]interface{}{
		"--action":   "reboot",
		"--ip":       "192.168.123.1",
		"--lanplus":  "",
		"--password": "password",
		"--username": "admin",
	}

	for k, v := range overrides {
		params[k] = v
	}

	return params
}

// ipmiNodeParams returns default IPMI node parameters for the given node name.
func ipmiNodeParams(nodeName string) map[string]interface{} {
	return map[string]interface{}{
		farparams.NodeIdentifierIPMI: map[string]interface{}{
			nodeName: farparams.IPMIPortValue,
		},
	}
}
