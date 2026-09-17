package tests

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/reportxml"

	"github.com/medik8s/system-tests/tests/internal/helpers"
	"github.com/medik8s/system-tests/tests/internal/labels"
	. "github.com/medik8s/system-tests/tests/internal/medik8sinittools"
	"github.com/medik8s/system-tests/tests/nhc-operator/internal/nhcparams"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("NHC Escalation -- Validation and Webhook",
	Serial, Ordered, ContinueOnFailure,
	Label(labels.OperatorNHC, nhcparams.Label,
		labels.DisruptionNonDestructive, labels.FrequencyWeekly),
	func() {
		var ctx context.Context

		escalationValidationNames := []string{
			nhcparams.NHCEscalationValidationPrefix + "-order",
			nhcparams.NHCEscalationValidationPrefix + "-dup-order",
			nhcparams.NHCEscalationValidationPrefix + "-timeout",
			nhcparams.NHCEscalationValidationPrefix + "-short-timeout",
			nhcparams.NHCEscalationValidationPrefix + "-dup-kind",
			nhcparams.NHCEscalationValidationPrefix + "-big-order",
			nhcparams.NHCEscalationValidationPrefix + "-multi-tmpl",
		}

		BeforeAll(func() {
			ctx = context.Background()

			By("Verifying NHC controller deployment is ready")

			verifyNHCDeploymentReady()

			By("Checking SNR CRD is installed")

			if !isSNRCRDInstalled(ctx) {
				Skip("SelfNodeRemediation CRD not found -- skipping escalation validation tests")
			}

			By("Pre-cleaning stale NHC CRs from previous interrupted runs")

			for _, name := range escalationValidationNames {
				cleanupNHCCR(ctx, name)
			}
		})

		AfterEach(func() {
			for _, name := range escalationValidationNames {
				cleanupNHCCR(ctx, name)
			}
		})

		JustAfterEach(func() {
			if CurrentSpecReport().Failed() {
				logNHCControllerState()
			}
		})

		Context("escalation webhook validation", func() {
			It("Verifying escalation order field validation",
				reportxml.ID("60863"),
				Label(labels.TierAcceptance, labels.PlatformAny,
					labels.ComponentWebhook), func() {
					By("Attempting to create NHC with missing order field")

					nhcName := nhcparams.NHCEscalationValidationPrefix + "-order"

					step := validEscalationStepRaw(0, nhcparams.EscalationFirstStepTimeout)
					delete(step, "order") // intentionally omit required field

					nhc := buildNHCWithEscalationRaw(nhcName, []map[string]interface{}{step})

					err := APIClient.Create(ctx, nhc)
					Expect(err).To(HaveOccurred(), "NHC creation should fail when order is omitted")
					Expect(err.Error()).To(ContainSubstring(nhcparams.EscalationWebhookOrderRequired),
						"Error should mention missing order field")

					verifyNHCNotCreated(ctx, nhcName)

					By("Attempting to create NHC with duplicate order values")

					nhcName = nhcparams.NHCEscalationValidationPrefix + "-dup-order"

					step1 := validEscalationStepRaw(0, nhcparams.EscalationFirstStepTimeout)
					step2 := testRemediationStepRaw(0, "120s") // same order=0 as step1

					nhc = buildNHCWithEscalationRaw(nhcName, []map[string]interface{}{step1, step2})

					err = APIClient.Create(ctx, nhc)
					Expect(err).To(HaveOccurred(), "NHC creation should fail with duplicate order values")
					Expect(err.Error()).To(ContainSubstring(nhcparams.EscalationWebhookDuplicateOrder),
						"Error should mention duplicate order")

					verifyNHCNotCreated(ctx, nhcName)

					By("Creating NHC with escalation order values exceeding int32 max (accepted)")

					nhcName = nhcparams.NHCEscalationValidationPrefix + "-big-order"

					step1 = testRemediationStepRaw(9999999998, nhcparams.EscalationFirstStepTimeout)
					step2 = validEscalationStepRaw(9999999999, "180s")

					nhc = buildNHCWithEscalationRaw(nhcName, []map[string]interface{}{step1, step2})

					err = APIClient.Create(ctx, nhc)
					Expect(err).ToNot(HaveOccurred(),
						"NHC creation should succeed with very large order values")

					created := &unstructured.Unstructured{}
					created.SetGroupVersionKind(nhcGVK)
					Expect(APIClient.Get(ctx, client.ObjectKey{Name: nhcName}, created)).To(Succeed(),
						"NHC CR %q should be persisted after creation with large order values", nhcName)
				})

			It("Verifying escalation timeout field is required and has minimum value",
				reportxml.ID("60862"),
				Label(labels.TierAcceptance, labels.PlatformAny,
					labels.ComponentWebhook), func() {
					By("Attempting to create NHC with missing timeout field")

					nhcName := nhcparams.NHCEscalationValidationPrefix + "-timeout"

					step := validEscalationStepRaw(0, nhcparams.EscalationFirstStepTimeout)
					delete(step, "timeout") // intentionally omit required field

					nhc := buildNHCWithEscalationRaw(nhcName, []map[string]interface{}{step})

					err := APIClient.Create(ctx, nhc)
					Expect(err).To(HaveOccurred(), "NHC creation should fail when timeout is omitted")
					Expect(err.Error()).To(ContainSubstring(nhcparams.EscalationWebhookTimeoutRequired),
						"Error should mention missing timeout field")

					verifyNHCNotCreated(ctx, nhcName)

					By("Attempting to create NHC with timeout below minimum (30s < 60s)")

					nhcName = nhcparams.NHCEscalationValidationPrefix + "-short-timeout"

					step = validEscalationStepRaw(0, "30s") // below 60s minimum

					nhc = buildNHCWithEscalationRaw(nhcName, []map[string]interface{}{step})

					err = APIClient.Create(ctx, nhc)
					Expect(err).To(HaveOccurred(), "NHC creation should fail when timeout is below 60s")
					Expect(err.Error()).To(ContainSubstring(nhcparams.EscalationWebhookTimeoutMinimum),
						"Error should mention minimum timeout requirement")

					verifyNHCNotCreated(ctx, nhcName)
				})

			It("Verifying duplicate remediator kind is forbidden in escalation chain",
				reportxml.ID("66838"),
				Label(labels.TierAcceptance, labels.PlatformAny,
					labels.ComponentWebhook), func() {
					nhcName := nhcparams.NHCEscalationValidationPrefix + "-dup-kind"

					By("Attempting to create NHC with two TestRemediation templates (same Kind)")

					step1 := testRemediationStepRaw(0, nhcparams.EscalationFirstStepTimeout)
					step2 := testRemediationStepRaw(1, "120s") // same Kind as step1

					nhc := buildNHCWithEscalationRaw(nhcName, []map[string]interface{}{step1, step2})

					err := APIClient.Create(ctx, nhc)
					Expect(err).To(HaveOccurred(),
						"NHC creation should fail with duplicate remediator Kind")
					Expect(err.Error()).To(ContainSubstring(nhcparams.EscalationWebhookDuplicateKind),
						"Error should mention duplicate template kind")

					verifyNHCNotCreated(ctx, nhcName)
				})

			It("Verifying duplicate remediator kind is accepted when templates support multiple",
				reportxml.ID("74932"),
				Label(labels.TierAcceptance, labels.PlatformAny,
					labels.ComponentWebhook), func() {
					nhcName := nhcparams.NHCEscalationValidationPrefix + "-multi-tmpl"

					By("Creating two same-Kind TestRemediationTemplates that support multiple templates")

					tmpl1, tmpl2 := setupMultipleTemplateSupport(ctx)

					DeferCleanup(func() { cleanupMultipleTemplateSupport(ctx) })

					By("Creating NHC with two same-Kind templates carrying the multiple-templates annotation")

					step1 := multiTemplateStepRaw(tmpl1, 0, nhcparams.EscalationFirstStepTimeout)
					step2 := multiTemplateStepRaw(tmpl2, 1, "120s") // same Kind as step1, annotation supported

					nhc := buildNHCWithEscalationRaw(nhcName, []map[string]interface{}{step1, step2})

					err := APIClient.Create(ctx, nhc)
					Expect(err).ToNot(HaveOccurred(),
						"NHC creation should succeed when duplicate-kind templates support multiple templates")

					created := &unstructured.Unstructured{}
					created.SetGroupVersionKind(nhcGVK)
					Expect(APIClient.Get(ctx, client.ObjectKey{Name: nhcName}, created)).To(Succeed(),
						"NHC CR %q should be persisted after creation", nhcName)
				})
		})
	})

var _ = Describe("NHC Escalation -- Edit During Remediation",
	Serial, Ordered, ContinueOnFailure,
	Label(labels.OperatorNHC, nhcparams.Label,
		labels.DisruptionDestructive, labels.FrequencyWeekly),
	func() {
		var ctx context.Context

		BeforeAll(func() {
			ctx = context.Background()

			By("Verifying NHC controller deployment is ready")

			verifyNHCDeploymentReady()

			By("Checking SNR CRD is installed")

			if !isSNRCRDInstalled(ctx) {
				Skip("SelfNodeRemediation CRD not found -- skipping escalation edit test")
			}

			By("Pre-cleaning stale NHC CR from previous interrupted runs")

			cleanupNHCCR(ctx, nhcparams.NHCEscalationEditTestName)
		})

		JustAfterEach(func() {
			if CurrentSpecReport().Failed() {
				logNHCControllerState()
			}
		})

		It("Verifying escalation order change is rejected while remediation is in progress",
			reportxml.ID("60865"),
			Label(labels.TierAcceptance, labels.PlatformAny,
				labels.ComponentWebhook), func() {
				if !isSSHAvailable() {
					Skip("SSH not available -- this test requires kubelet stop via SSH")
				}

				nhcName := nhcparams.NHCEscalationEditTestName

				By("Setting up TestRemediation CRDs and RBAC")

				setupTestRemediationResources(ctx)
				DeferCleanup(func() { cleanupTestRemediationResources(ctx) })

				By("Creating NHC with escalation: TestRemediation (order=0, timeout=600s) then SNR (order=1)")

				nhc := buildNHCWithEscalation(nhcName, []escalationStep{
					testRemediationEscalationStep(0, nhcparams.EscalationLongTimeout),
					snrEscalationStep(1, nhcparams.EscalationLongTimeout),
				})

				err := APIClient.Create(ctx, nhc)
				Expect(err).ToNot(HaveOccurred(), "Failed to create NHC with escalation")
				DeferCleanup(func() { cleanupNHCCR(ctx, nhcName) })

				By("Selecting a worker node and stopping kubelet to trigger remediation")

				targetNode, nodeErr := helpers.SelectWorkerNode(ctx, APIClient)
				Expect(nodeErr).ToNot(HaveOccurred(), "Failed to select worker node")

				GinkgoWriter.Printf("Target worker node: %s\n", targetNode.Name)

				Expect(stopKubeletForRemediation(ctx, targetNode.Name)).To(Succeed(),
					"Failed to stop kubelet on %s", targetNode.Name)
				DeferCleanup(func() {
					if sshErr := startKubeletForRemediation(ctx, targetNode.Name); sshErr != nil {
						GinkgoWriter.Printf(
							"WARNING: SSH kubelet restart failed for %s: %v\n",
							targetNode.Name, sshErr)
						AddReportEntry("ssh-kubelet-restart-failed",
							fmt.Sprintf("node %s: %v", targetNode.Name, sshErr))
					}

					if waitErr := helpers.WaitForNodeReady(ctx, APIClient, targetNode.Name,
						nhcparams.DestructivePollInterval, nhcparams.NodeReadyTimeout,
						GinkgoWriter.Printf); waitErr != nil {
						GinkgoWriter.Printf(
							"WARNING: node %s did not become Ready within %s: %v\n",
							targetNode.Name, nhcparams.NodeReadyTimeout, waitErr)
						AddReportEntry("safety-net-recovery-failed",
							fmt.Sprintf("node %s did not recover: %v", targetNode.Name, waitErr))
					}
				})

				By("Waiting for NHC to enter Remediating phase")

				Expect(waitForNHCPhase(ctx, nhcName, nhcparams.NHCPhaseRemediating,
					nhcparams.NodeNotReadyTimeout)).To(Succeed(),
					"NHC should enter Remediating phase")

				By("Waiting for TestRemediation CR to be created (remediation is in progress)")

				Eventually(func(g Gomega) {
					exists, checkErr := testRemediationCRExists(ctx, targetNode.Name)
					g.Expect(checkErr).ToNot(HaveOccurred())
					g.Expect(exists).To(BeTrue(),
						"TestRemediation CR should be created for node %s", targetNode.Name)
				}).WithPolling(nhcparams.DefaultPollInterval).
					WithTimeout(nhcparams.NodeNotReadyTimeout).Should(Succeed())

				By("Attempting to modify escalation order while remediation is active")

				current := &unstructured.Unstructured{}
				current.SetGroupVersionKind(nhcGVK)
				Expect(APIClient.Get(ctx, client.ObjectKey{Name: nhcName}, current)).To(Succeed())

				spec := nhcSpec(current)
				escalations, ok := spec["escalatingRemediations"].([]interface{})
				Expect(ok).To(BeTrue(), "NHC spec should have escalatingRemediations slice")
				Expect(escalations).To(HaveLen(2),
					"NHC should have exactly the 2 escalation steps it was created with")

				step0, ok0 := escalations[0].(map[string]interface{})
				Expect(ok0).To(BeTrue(), "escalation step 0 should be a map")

				step1, ok1 := escalations[1].(map[string]interface{})
				Expect(ok1).To(BeTrue(), "escalation step 1 should be a map")

				step0["order"] = int64(1)
				step1["order"] = int64(0)

				updateErr := APIClient.Update(ctx, current)
				Expect(updateErr).To(HaveOccurred(),
					"Updating escalation order should be rejected during active remediation")
				Expect(updateErr.Error()).To(ContainSubstring(nhcparams.EscalationWebhookUpdateProhibited),
					"Error should mention the escalating remediations field")
				Expect(updateErr.Error()).To(ContainSubstring(nhcparams.EscalationWebhookOngoingRemediation),
					"Error should mention the update is prohibited due to running remediation")

				By(fmt.Sprintf("Attempting kubelet re-enable (best-effort) and waiting for node recovery on %s",
					targetNode.Name))

				// On AWS Nitro, stopping kubelet stops the watchdog heartbeat; the platform
				// auto-reboots the node after ~60-90s. SSH may fail mid-reboot, but the
				// reboot restores kubelet automatically, so treat SSH failure as non-fatal.
				// WaitForNodeReady below is the real recovery gate.
				if sshErr := startKubeletForRemediation(ctx, targetNode.Name); sshErr != nil {
					GinkgoWriter.Printf(
						"kubelet restart via SSH failed (node may be rebooting via AWS watchdog): %v\n",
						sshErr)
					AddReportEntry("ssh-kubelet-restart-failed", sshErr.Error())
				}

				Expect(helpers.WaitForNodeReady(ctx, APIClient, targetNode.Name,
					nhcparams.DestructivePollInterval, nhcparams.NodeReadyTimeout, GinkgoWriter.Printf)).To(Succeed())

				By("Waiting for NHC to return to Enabled phase")

				Expect(waitForNHCPhase(ctx, nhcName, nhcparams.NHCPhaseEnabled,
					nhcparams.RemediationCompletionTimeout)).To(Succeed(),
					"NHC should return to Enabled after node recovery")
			})
	})
