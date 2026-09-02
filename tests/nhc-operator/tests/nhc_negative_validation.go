package tests

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/deployment"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/reportxml"

	"github.com/medik8s/system-tests/tests/internal/labels"
	. "github.com/medik8s/system-tests/tests/internal/medik8sinittools"
	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
	"github.com/medik8s/system-tests/tests/nhc-operator/internal/nhcparams"

	"github.com/medik8s/system-tests/tests/internal/helpers"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("NHC Negative -- Validation and Webhook",
	Serial, Ordered, ContinueOnFailure,
	Label(labels.OperatorNHC, nhcparams.Label,
		labels.DisruptionNonDestructive, labels.FrequencyWeekly),
	func() {
		var ctx context.Context

		// allNegativeTestNames lists every NHC CR name used by tests in this
		// Describe so AfterEach can sweep-clean in a single place.
		allNegativeTestNames := []string{
			nhcparams.NHCDuplicateTestName,
			nhcparams.NHCIncorrectTemplateTestName,
			nhcparams.NHCInvalidValuesTestName,
			nhcparams.NHCMissingNsTestName,
			nhcparams.NHCEmptySelectorTestName,
		}

		BeforeAll(func() {
			ctx = context.Background()

			By("Verifying NHC controller deployment is ready")

			nhcDeployment, err := deployment.Pull(
				APIClient, nhcparams.OperatorDeploymentName, medik8sparams.OperatorNs)
			Expect(err).ToNot(HaveOccurred(), "Failed to get NHC deployment")
			Expect(nhcDeployment.IsReady(medik8sparams.DefaultTimeout)).To(BeTrue(),
				"NHC deployment is not Ready")

			By("Checking SNR CRD is installed (needed for template namespace tests)")

			if !isSNRCRDInstalled(ctx) {
				Skip("SelfNodeRemediation CRD not found -- skipping negative validation tests that use SNRT")
			}

			By("Pre-cleaning stale NHC CRs from previous interrupted runs")

			for _, name := range allNegativeTestNames {
				cleanupNHCCR(ctx, name)
			}
		})

		AfterEach(func() {
			for _, name := range allNegativeTestNames {
				cleanupNHCCR(ctx, name)
			}
		})

		Context("webhook rejection", func() {
			// OCP-53769 tests standard K8s AlreadyExists behavior for NHC CRs.
			// This is a Polarion requirement, not NHC-specific webhook validation.
			It("Verifying duplicate NHC name creation is rejected",
				reportxml.ID("53769"),
				Label(labels.TierAcceptance, labels.PlatformAny,
					labels.ComponentWebhook), func() {
					nhcName := nhcparams.NHCDuplicateTestName

					By("Creating first NHC CR")

					nhc := buildNHCForWorkers(nhcName)
					Expect(APIClient.Create(ctx, nhc)).To(Succeed(),
						"Failed to create first NHC CR %q", nhcName)

					By("Verifying NHC was created")

					created := &unstructured.Unstructured{}
					created.SetGroupVersionKind(nhcGVK)
					Expect(APIClient.Get(ctx, client.ObjectKey{Name: nhcName}, created)).To(Succeed(),
						"NHC CR %q should exist after creation", nhcName)

					By("Attempting to create second NHC with the same name")

					duplicate := buildNHCForWorkers(nhcName)
					err := APIClient.Create(ctx, duplicate)
					Expect(err).To(HaveOccurred(), "Duplicate NHC creation should fail")
					Expect(k8serrors.IsAlreadyExists(err)).To(BeTrue(),
						"Expected AlreadyExists error, got: %v", err)

					By("Verifying only one NHC with this name exists")

					nhcList := &unstructured.UnstructuredList{}
					nhcList.SetGroupVersionKind(nhcGVK)
					Expect(APIClient.List(ctx, nhcList)).To(Succeed(),
						"Failed to list NHC CRs")

					count := 0

					for i := range nhcList.Items {
						if nhcList.Items[i].GetName() == nhcName {
							count++
						}
					}

					Expect(count).To(Equal(1),
						"Expected exactly 1 NHC CR named %q, found %d", nhcName, count)
				})

			// OCP-51626 checks both minHealthy and unhealthyConditions in the same
			// assertion. The NHC webhook currently aggregates all validation errors
			// into a single response. If it switches to fail-fast, split into
			// per-field tests.
			It("Verifying NHC creation with invalid values is rejected",
				reportxml.ID("51626"),
				Label(labels.TierAcceptance, labels.PlatformAny,
					labels.ComponentWebhook), func() {
					nhcName := nhcparams.NHCInvalidValuesTestName

					By("Creating NHC with negative minHealthy and duration values")

					nhc := buildNHCForWorkers(nhcName)
					spec := nhcSpec(nhc)
					spec["minHealthy"] = "-30%"

					conditions := nhcUnhealthyConditions(spec)
					cond, isMap := conditions[0].(map[string]interface{})
					Expect(isMap).To(BeTrue(), "unhealthyConditions[0] is not a map")

					cond["duration"] = "-30s"

					err := APIClient.Create(ctx, nhc)
					Expect(err).To(HaveOccurred(), "NHC creation with negative values should fail")
					Expect(err).To(MatchError(ContainSubstring("spec.minHealthy")),
						"Error should mention invalid minHealthy")
					Expect(err).To(MatchError(ContainSubstring("spec.unhealthyConditions")),
						"Error should mention invalid unhealthyConditions duration")

					By("Verifying NHC was not created")

					verifyNHCNotCreated(ctx, nhcName)

					By("Creating NHC with string minHealthy and duration values")

					nhcStr := buildNHCForWorkers(nhcName)
					specStr := nhcSpec(nhcStr)
					specStr["minHealthy"] = "string"

					conditionsStr := nhcUnhealthyConditions(specStr)
					condStr, ok := conditionsStr[0].(map[string]interface{})
					Expect(ok).To(BeTrue(), "unhealthyConditions[0] is not a map")

					condStr["duration"] = "string"

					err = APIClient.Create(ctx, nhcStr)
					Expect(err).To(HaveOccurred(), "NHC creation with string values should fail")
					Expect(err).To(MatchError(ContainSubstring("spec.minHealthy")),
						"Error should mention invalid minHealthy")
					Expect(err).To(MatchError(ContainSubstring("spec.unhealthyConditions")),
						"Error should mention invalid unhealthyConditions duration")

					By("Verifying NHC was not created after string values attempt")

					verifyNHCNotCreated(ctx, nhcName)
				})

			It("Verifying NHC creation with empty selector is rejected",
				reportxml.ID("61591"),
				Label(labels.TierAcceptance, labels.PlatformAny,
					labels.ComponentWebhook), func() {
					nhcName := nhcparams.NHCEmptySelectorTestName

					By("Creating NHC with empty matchExpressions selector")

					// buildNHC with nil matchLabels creates an intermediate selector that
					// is immediately overwritten below -- we only need the base NHC spec.
					nhc := buildNHC(nhcName, "", "", nil)
					nhcSpec(nhc)["selector"] = map[string]interface{}{
						"matchExpressions": []interface{}{},
					}

					err := APIClient.Create(ctx, nhc)
					Expect(err).To(HaveOccurred(), "NHC creation with empty selector should fail")
					Expect(err).To(MatchError(ContainSubstring("Selector is mandatory")),
						"Error should indicate selector is mandatory")

					By("Verifying NHC was not created")

					verifyNHCNotCreated(ctx, nhcName)
				})
		})

		Context("controller behavior", func() {
			// OCP-51625 covers two scenarios (wrong template name + wrong API group)
			// under a single Polarion ID. Both are kept in one It block to avoid
			// duplicating the reportxml.ID -- duplicate IDs cause one Polarion
			// result to overwrite the other.
			It("Verifying NHC reports Disabled phase for non-existent remediation template",
				reportxml.ID("51625"),
				Label(labels.TierAcceptance, labels.PlatformAny,
					labels.ComponentController), func() {
					nhcName := nhcparams.NHCIncorrectTemplateTestName

					By("Creating NHC with non-existent SNR template name")

					nhc := buildNHCForWorkers(nhcName)
					spec := nhcSpec(nhc)
					tmpl, ok := spec["remediationTemplate"].(map[string]interface{})
					Expect(ok).To(BeTrue(), "NHC spec has no remediationTemplate map")

					tmpl["name"] = "non-existent-template"

					Expect(APIClient.Create(ctx, nhc)).To(Succeed(),
						"NHC creation should succeed even with a non-existent template")

					By("Verifying NHC is Disabled with RemediationTemplateNotFound")

					verifyNHCDisabledWithReason(ctx, nhcName)

					// Must delete and confirm gone before reusing the same name.
					By("Deleting NHC with wrong SNR template before next scenario")

					cleanupNHCCR(ctx, nhcName)
					waitForNHCGone(ctx, nhcName)

					By("Creating NHC with poison-pill remediation template (non-existent API group)")

					nhcPP := buildNHCForWorkers(nhcName)
					specPP := nhcSpec(nhcPP)
					specPP["remediationTemplate"] = map[string]interface{}{
						"apiVersion": "poison-pill-remediation.medik8s.io/v1alpha1",
						"kind":       "PoisonPillRemediationTemplate",
						"name":       "poison-pill-default-template",
						"namespace":  medik8sparams.OperatorNs,
					}

					Expect(APIClient.Create(ctx, nhcPP)).To(Succeed(),
						"NHC creation should succeed even with a non-existent API group")

					By("Verifying NHC is Disabled with RemediationTemplateNotFound for poison-pill template")

					verifyNHCDisabledWithReason(ctx, nhcName)
				})

			It("Verifying NHC handles missing template namespace correctly",
				reportxml.ID("71184"),
				Label(labels.TierAcceptance, labels.PlatformAny,
					labels.ComponentController), func() {
					nhcName := nhcparams.NHCMissingNsTestName

					if !isSNRCRDInstalled(ctx) {
						Skip("SelfNodeRemediation CRD not found -- OCP-71184 Part 1 requires SNRT")
					}

					By("Part 1: Namespaced template (SNRT) without namespace in remediationTemplate")

					By("Creating NHC with SNRT but no namespace in remediationTemplate ref")

					nhc := buildNHCForWorkers(nhcName)
					spec := nhcSpec(nhc)
					tmpl, ok := spec["remediationTemplate"].(map[string]interface{})
					Expect(ok).To(BeTrue(), "NHC spec has no remediationTemplate map")
					delete(tmpl, "namespace")

					Expect(APIClient.Create(ctx, nhc)).To(Succeed(),
						"NHC creation should succeed without namespace in template ref")

					By("Verifying NHC is Disabled due to missing namespace")

					verifyNHCDisabledWithReason(ctx, nhcName)

					By("Adding namespace to remediationTemplate via patch")

					patchBytes := []byte(fmt.Sprintf(
						`{"spec":{"remediationTemplate":{"namespace":%q}}}`,
						medik8sparams.OperatorNs))
					patchedNHC := &unstructured.Unstructured{}
					patchedNHC.SetGroupVersionKind(nhcGVK)
					patchedNHC.SetName(nhcName)
					Expect(APIClient.Patch(ctx, patchedNHC,
						client.RawPatch(types.MergePatchType, patchBytes))).To(Succeed(),
						"Failed to patch namespace into NHC remediationTemplate")

					By("Verifying NHC transitions to Enabled after namespace added")

					Expect(waitForNHCPhase(ctx, nhcName, nhcparams.NHCPhaseEnabled,
						nhcparams.NodeNotReadyTimeout)).To(Succeed(),
						"NHC %q should become Enabled after adding namespace", nhcName)

					By("Removing namespace from remediationTemplate via patch")

					// JSON merge patch with null removes the key entirely (RFC 7396),
					// matching the Python reference which uses del on the namespace field.
					removePatch := []byte(`{"spec":{"remediationTemplate":{"namespace":null}}}`)
					Expect(APIClient.Patch(ctx, patchedNHC,
						client.RawPatch(types.MergePatchType, removePatch))).To(Succeed(),
						"Failed to remove namespace from NHC remediationTemplate")

					By("Verifying NHC transitions back to Disabled after namespace removed")

					verifyNHCDisabledWithReason(ctx, nhcName)

					By("Cleaning up NHC before Part 2")

					cleanupNHCCR(ctx, nhcName)
					waitForNHCGone(ctx, nhcName)

					By("Part 2: Cluster-scoped TestRemediationTemplate (TRT) without namespace")

					By("Setting up TestRemediation CRDs and RBAC")

					setupTestRemediationResources(ctx)
					DeferCleanup(func() { cleanupTestRemediationResources(ctx) })

					By("Creating NHC with cluster-scoped TestRemediationTemplate and no namespace in ref")

					nhcTRT := buildNHCForWorkers(nhcName)
					specTRT := nhcSpec(nhcTRT)
					specTRT["remediationTemplate"] = map[string]interface{}{
						"apiVersion": nhcparams.TestRemediationGroup + "/" + nhcparams.TestRemediationVersion,
						"kind":       "TestRemediationTemplate",
						"name":       nhcparams.TestRemediationTemplateName,
					}

					Expect(APIClient.Create(ctx, nhcTRT)).To(Succeed(),
						"NHC creation with cluster-scoped TestRemediationTemplate should succeed without namespace")

					By("Verifying NHC with cluster-scoped TestRemediationTemplate is Enabled")

					Expect(waitForNHCPhase(ctx, nhcName, nhcparams.NHCPhaseEnabled,
						nhcparams.NodeNotReadyTimeout)).To(Succeed(),
						"NHC %q should be Enabled with cluster-scoped TestRemediationTemplate (no namespace needed)", nhcName)
				})
		})
	})

var _ = Describe("NHC Negative -- Zero Healthy Nodes",
	Serial, Ordered, ContinueOnFailure,
	Label(labels.OperatorNHC, nhcparams.Label,
		labels.DisruptionDestructive, labels.FrequencyWeekly),
	func() {
		var (
			ctx              context.Context
			targetWorkerName string
			oldBootID        string
		)

		BeforeAll(func() {
			ctx = context.Background()

			By("Checking SSH access is available")

			if !isSSHAvailable() {
				Skip("SSH not available -- zero-healthy-nodes test requires SSH to stop kubelet")
			}

			By("Checking SNR CRD is installed")

			if !isSNRCRDInstalled(ctx) {
				Skip("SelfNodeRemediation CRD not found -- skipping zero-healthy-nodes test")
			}

			By("Verifying NHC controller deployment is ready")

			nhcDeployment, err := deployment.Pull(
				APIClient, nhcparams.OperatorDeploymentName, medik8sparams.OperatorNs)
			Expect(err).ToNot(HaveOccurred(), "Failed to get NHC deployment")
			Expect(nhcDeployment.IsReady(medik8sparams.DefaultTimeout)).To(BeTrue(),
				"NHC deployment is not Ready")

			By("Verifying at least 1 Ready worker node")

			workerCount, err := helpers.CountReadyWorkerNodes(ctx, APIClient)
			Expect(err).ToNot(HaveOccurred())
			Expect(workerCount).To(BeNumerically(">=", 1),
				"Zero-healthy-nodes test requires at least 1 Ready worker node")

			By("Selecting target worker node")

			targetNode, err := helpers.SelectWorkerNode(ctx, APIClient)
			Expect(err).ToNot(HaveOccurred(), "Failed to select worker node")

			targetWorkerName = targetNode.Name
			GinkgoWriter.Printf("Target worker node: %s\n", targetWorkerName)
		})

		BeforeEach(func() {
			By("Verifying NHC controller deployment is ready")

			nhcDeployment, err := deployment.Pull(
				APIClient, nhcparams.OperatorDeploymentName, medik8sparams.OperatorNs)
			Expect(err).ToNot(HaveOccurred())
			Expect(nhcDeployment.IsReady(medik8sparams.DefaultTimeout)).To(BeTrue(),
				"NHC deployment is not Ready")

			By("Verifying target node is Ready")

			node := &corev1.Node{}
			Expect(APIClient.Get(ctx, client.ObjectKey{Name: targetWorkerName}, node)).To(Succeed())
			Expect(helpers.IsNodeReady(node)).To(BeTrue(),
				"Target node %s is not Ready before test", targetWorkerName)

			By("Recording boot ID")

			oldBootID, err = helpers.GetNodeBootIDFromAPI(ctx, APIClient, targetWorkerName)
			Expect(err).ToNot(HaveOccurred(),
				"Must read boot ID from node %s", targetWorkerName)

			By("Pre-cleaning stale CRs")

			cleanupNHCCR(ctx, nhcparams.NHCZeroHealthyTestName)
			cleanupSNRCR(ctx, targetWorkerName)

			GinkgoWriter.Printf("Pre-remediation boot ID: %s\n", oldBootID)
		})

		JustAfterEach(func() {
			if CurrentSpecReport().Failed() {
				logNHCControllerState()
			}

			cleanupNHCCR(ctx, nhcparams.NHCZeroHealthyTestName)
			cleanupSNRCR(ctx, targetWorkerName)

			if isSSHAvailable() {
				if sshErr := startKubeletForRemediation(ctx, targetWorkerName); sshErr != nil {
					GinkgoWriter.Printf(
						"WARNING: SSH kubelet restart failed for %s: %v\n",
						targetWorkerName, sshErr)
					AddReportEntry("ssh-kubelet-restart-failed",
						fmt.Sprintf("node %s: %v", targetWorkerName, sshErr))
				}
			}

			By("Safety net: waiting for node " + targetWorkerName + " to become Ready")

			if err := helpers.WaitForNodeReady(ctx, APIClient,
				targetWorkerName,
				nhcparams.DefaultPollInterval, nhcparams.NodeReadyTimeout,
				GinkgoWriter.Printf,
			); err != nil {
				GinkgoWriter.Printf(
					"WARNING: node %s did not become Ready within %s: %v\n",
					targetWorkerName, nhcparams.NodeReadyTimeout, err)
				AddReportEntry("safety-net-recovery-failed",
					fmt.Sprintf("node %s did not recover: %v", targetWorkerName, err))
			}
		})

		It("Verifying healthyNodes drops to zero during remediation",
			reportxml.ID("56599"),
			Label(labels.TierAcceptance, labels.PlatformAny,
				labels.ComponentRemediation), func() {
				nhcName := nhcparams.NHCZeroHealthyTestName

				By("Creating NHC CR targeting single worker node")

				nhcCR := buildNHCWithHostnameSelector(nhcName, targetWorkerName)
				Expect(APIClient.Create(ctx, nhcCR)).To(Succeed(),
					"Failed to create NHC CR %q for node %s", nhcName, targetWorkerName)

				By("Verifying pre-remediation status: healthyNodes=1, observedNodes=1, phase=Enabled")

				Expect(waitForNHCPhase(ctx, nhcName, nhcparams.NHCPhaseEnabled,
					nhcparams.NodeNotReadyTimeout)).To(Succeed(),
					"NHC %q should be Enabled before remediation", nhcName)

				Eventually(func(g Gomega) {
					healthy, err := getNHCHealthyNodes(ctx, nhcName)
					g.Expect(err).ToNot(HaveOccurred())
					g.Expect(healthy).To(Equal(int64(1)),
						"healthyNodes should be 1 before remediation")
				}).WithPolling(nhcparams.DefaultPollInterval).
					WithTimeout(nhcparams.NodeNotReadyTimeout).Should(Succeed())

				Eventually(func(g Gomega) {
					observed, err := getNHCObservedNodes(ctx, nhcName)
					g.Expect(err).ToNot(HaveOccurred())
					g.Expect(observed).To(Equal(int64(1)),
						"observedNodes should be 1 (single-node selector)")
				}).WithPolling(nhcparams.DefaultPollInterval).
					WithTimeout(nhcparams.NodeNotReadyTimeout).Should(Succeed())

				By("Stopping kubelet on target node to trigger remediation")

				Expect(stopKubeletForRemediation(ctx, targetWorkerName)).To(Succeed(),
					"Failed to stop kubelet on %s", targetWorkerName)

				By("Waiting for NHC to enter Remediating phase")

				Expect(waitForNHCPhase(ctx, nhcName, nhcparams.NHCPhaseRemediating,
					nhcparams.NodeNotReadyTimeout)).To(Succeed(),
					"NHC %q should enter Remediating after kubelet stop", nhcName)

				By("Verifying healthyNodes=0 during remediation")

				Eventually(func(g Gomega) {
					healthy, err := getNHCHealthyNodes(ctx, nhcName)
					g.Expect(err).ToNot(HaveOccurred())
					g.Expect(healthy).To(Equal(int64(0)),
						"healthyNodes should be 0 during remediation")
				}).WithPolling(nhcparams.DefaultPollInterval).
					WithTimeout(nhcparams.NodeNotReadyTimeout).Should(Succeed())

				By("Verifying observedNodes=1 during remediation")

				Eventually(func(g Gomega) {
					observed, err := getNHCObservedNodes(ctx, nhcName)
					g.Expect(err).ToNot(HaveOccurred())
					g.Expect(observed).To(Equal(int64(1)),
						"observedNodes should remain 1 during remediation")
				}).WithPolling(nhcparams.DefaultPollInterval).
					WithTimeout(nhcparams.NodeNotReadyTimeout).Should(Succeed())

				By("Waiting for SNR remediation to complete (node reboot)")

				Expect(waitForSNRRemediationComplete(ctx, targetWorkerName, oldBootID,
					nhcparams.RemediationCompletionTimeout)).To(Succeed(),
					"SNR remediation should complete for node %s", targetWorkerName)

				By("Waiting for NHC to return to Enabled after recovery")

				Expect(waitForNHCPhase(ctx, nhcName, nhcparams.NHCPhaseEnabled,
					nhcparams.RemediationCompletionTimeout)).To(Succeed(),
					"NHC %q should return to Enabled after remediation", nhcName)

				By("Verifying post-recovery status: healthyNodes=1, observedNodes=1")

				Eventually(func(g Gomega) {
					healthy, err := getNHCHealthyNodes(ctx, nhcName)
					g.Expect(err).ToNot(HaveOccurred())
					g.Expect(healthy).To(Equal(int64(1)),
						"healthyNodes should be 1 after recovery")
				}).WithPolling(nhcparams.DefaultPollInterval).
					WithTimeout(nhcparams.NodeNotReadyTimeout).Should(Succeed())

				Eventually(func(g Gomega) {
					observed, err := getNHCObservedNodes(ctx, nhcName)
					g.Expect(err).ToNot(HaveOccurred())
					g.Expect(observed).To(Equal(int64(1)),
						"observedNodes should be 1 after recovery")
				}).WithPolling(nhcparams.DefaultPollInterval).
					WithTimeout(nhcparams.NodeNotReadyTimeout).Should(Succeed())
			})
	})
