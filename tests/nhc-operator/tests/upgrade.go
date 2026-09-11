package tests

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/deployment"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/olm"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/reportxml"
	olmV1alpha1 "github.com/rh-ecosystem-edge/eco-goinfra/pkg/schemes/olm/operators/v1alpha1"

	"github.com/medik8s/system-tests/tests/internal/helpers"
	"github.com/medik8s/system-tests/tests/internal/labels"
	. "github.com/medik8s/system-tests/tests/internal/medik8sinittools"
	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
	"github.com/medik8s/system-tests/tests/nhc-operator/internal/nhcparams"
	"github.com/medik8s/system-tests/tests/nhc-operator/internal/nhcutils"
)

// NHC works with any operator providing a remediation template CRD. This test
// installs and uses SNR as the remediator because it
// self-remediates via kubelet stop -> reboot, requiring no fence agents or
// cloud credentials. This keeps the upgrade test platform-agnostic, unlike
// the FAR upgrade test which requires AWS for fence agent remediation.
var _ = Describe("NHC Operator Upgrade",
	Serial, Ordered,
	Label(labels.OperatorNHC, nhcparams.Label,
		labels.TierUpgrade, labels.TierUpgradeCluster, labels.DisruptionDestructive,
		labels.PlatformAny, labels.FrequencyWeekly,
		labels.ComponentOLM),
	func() {
		var (
			ctx                context.Context
			candidateInputs    nhcparams.CandidateInputs
			previousCSV        *olm.ClusterServiceVersionBuilder
			preUpgradeImage    string
			preOCPUpgradeCSV   string
			preOCPUpgradeImage string
			currentTargetNode  string
			operatorUpgraded   bool
			namespaceCreated   bool
		)

		BeforeAll(func() {
			ctx = context.Background()

			if medik8sparams.SkipUpgradeIDMS {
				Expect(medik8sparams.CandidateVersion).NotTo(BeEmpty(),
					"NHC_UPGRADE_CANDIDATE_VERSION must identify the direct-catalog candidate")
				Expect(medik8sparams.CandidateImage).NotTo(BeEmpty(),
					"NHC_UPGRADE_CANDIDATE_IMAGE must identify the direct-catalog candidate")
			}

			if medik8sparams.UpgradeToPRBundle {
				var err error

				candidateInputs, err = nhcparams.LoadCandidateInputs()
				Expect(err).NotTo(HaveOccurred())
				candidateInputs, err = nhcutils.ResolveAndVerifyCandidateInputs(ctx, candidateInputs)
				Expect(err).NotTo(HaveOccurred())
				Expect(candidateInputs.Namespace).To(Equal(medik8sparams.OperatorNs))
				AddReportEntry("nhc-cluster-candidate-inputs", candidateInputs)
			}

			if medik8sparams.SkipOCPUpgrade {
				GinkgoWriter.Println(
					"MEDIK8S_SKIP_OCP_UPGRADE=true: OCP upgrade step will be skipped")
			} else {
				Expect(medik8sparams.TargetOCPImage).NotTo(BeEmpty(),
					"OPENSHIFT_UPGRADE_RELEASE_IMAGE_OVERRIDE or RELEASE_IMAGE_LATEST must be set")

				clusterVersion := &configv1.ClusterVersion{}
				Expect(APIClient.Get(ctx, client.ObjectKey{Name: "version"}, clusterVersion)).To(Succeed())
				Expect(clusterVersion.Status.Desired.Version).To(HavePrefix("4.22."),
					"full cluster-upgrade test must begin on OpenShift 4.22")
				AddReportEntry("nhc-cluster-upgrade-start", map[string]string{
					"version":     clusterVersion.Status.Desired.Version,
					"image":       clusterVersion.Status.Desired.Image,
					"targetImage": medik8sparams.TargetOCPImage,
				})
			}

			By("Checking a kubelet-stop trigger mechanism is available")

			if medik8sparams.KubeletStopViaOCDebug {
				GinkgoWriter.Println(
					"MEDIK8S_KUBELET_STOP_OCDEBUG=true: using oc debug to " +
						"trigger remediation instead of SSH. Recovery still relies on SSH")
			} else if !isSSHAvailable() {
				Skip("SSH not available -- NHC upgrade test requires SSH access to worker nodes " +
					"unless MEDIK8S_KUBELET_STOP_OCDEBUG=true is set")
			}

			By("Verifying at least 2 Ready worker nodes")

			workerCount, err := helpers.CountReadyWorkerNodes(ctx, APIClient)
			Expect(err).ToNot(HaveOccurred())
			Expect(workerCount).To(BeNumerically(">=", 2),
				"Upgrade test requires at least 2 Ready worker nodes")

			By("Creating the operator namespace when the clean cluster does not have it")

			namespace := &corev1.Namespace{}

			err = APIClient.Get(ctx, client.ObjectKey{Name: medik8sparams.OperatorNs}, namespace)
			if apierrors.IsNotFound(err) {
				namespace = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
					Name: medik8sparams.OperatorNs,
					Labels: map[string]string{
						"security.openshift.io/scc.podSecurityLabelSync": "false",
						"pod-security.kubernetes.io/enforce":             "privileged",
					},
				}}
				Expect(APIClient.Create(ctx, namespace)).To(Succeed())

				namespaceCreated = true
			} else {
				Expect(err).NotTo(HaveOccurred())
			}

			By("Clearing any leftover NHC Subscription/CSV/InstallPlan from a prior run")

			// A prior run that was interrupted before AfterAll ran (or an
			// earlier manual test) can leave an ownerless CSV behind. OLM
			// refuses to resolve a fresh Subscription against the same
			// package while one exists ("clusterserviceversion ... exists
			// and is not referenced by a subscription"), so start clean.
			nhcutils.CleanupUpgradeResources(APIClient, GinkgoWriter.Printf)
		})

		AfterAll(func() {
			if medik8sparams.UpgradeToPRBundle && candidateInputs.OperatorSDK != "" {
				output, err := nhcutils.CleanupBundle(
					ctx, candidateInputs.OperatorSDK, candidateInputs.Namespace, candidateInputs.Package)
				if err != nil {
					GinkgoWriter.Printf("WARNING: candidate bundle cleanup failed: %v\n%s\n", err, output)
				}
			}

			nhcutils.CleanupUpgradeResources(APIClient, GinkgoWriter.Printf)

			if namespaceCreated {
				namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: medik8sparams.OperatorNs}}
				if err := APIClient.Delete(ctx, namespace); err != nil && !apierrors.IsNotFound(err) {
					GinkgoWriter.Printf("WARNING: failed to delete test-created namespace %s: %v\n",
						medik8sparams.OperatorNs, err)
				}
			}
		})

		JustAfterEach(func() {
			if CurrentSpecReport().Failed() {
				GinkgoWriter.Println("Upgrade test failed - collecting NHC controller logs")
				logNHCControllerState()
			}

			cleanupNHCCR(ctx, nhcparams.ClusterUpgradeTestName)

			if currentTargetNode != "" {
				nodeName := currentTargetNode
				currentTargetNode = ""

				cleanupSNRCR(ctx, nodeName)

				if isSSHAvailable() {
					if sshErr := startKubeletForRemediation(ctx, nodeName); sshErr != nil {
						GinkgoWriter.Printf(
							"WARNING: SSH kubelet restart failed for %s (best-effort): %v\n",
							nodeName, sshErr)
						AddReportEntry("ssh-kubelet-restart-failed",
							fmt.Sprintf("node %s: %v", nodeName, sshErr))
					}
				}

				By("Safety net: waiting for node " + nodeName + " to become Ready")

				if err := helpers.WaitForNodeReady(
					ctx, APIClient, nodeName,
					nhcparams.DefaultPollInterval, nhcparams.NodeReadyTimeout,
					GinkgoWriter.Printf); err != nil {
					GinkgoWriter.Printf(
						"WARNING: node %s did not become Ready within %s: %v\n",
						nodeName, nhcparams.NodeReadyTimeout, err)
					AddReportEntry("upgrade-recovery-failed",
						fmt.Sprintf("node %s did not recover: %v", nodeName, err))
				}

				if medik8sparams.KubeletStopViaOCDebug {
					if guardErr := helpers.RemoveKubeletStopGuard(
						ctx, nodeName, nhcparams.OCDebugKubeletStopTimeout); guardErr != nil {
						GinkgoWriter.Printf(
							"WARNING: failed to remove kubelet-stop guard on %s (best-effort): %v\n",
							nodeName, guardErr)
					}
				}
			}
		})

		It("should survive OCP upgrade and operator upgrade with working remediation",
			Label(labels.ComponentRemediation),
			reportxml.ID("REPLACE_WITH_POLARION_ID"),
			func() {
				By("Step 1: Install NHC operator GA version from redhat-operators on OCP N-1")

				sub, err := nhcutils.InstallGAOperator(APIClient)
				Expect(err).NotTo(HaveOccurred(), "Failed to install GA NHC operator")

				GinkgoWriter.Printf("GA Subscription created: %s (catalog: %s, channel: %s, package: %s)\n",
					sub.Object.Name,
					sub.Object.Spec.CatalogSource,
					sub.Object.Spec.Channel,
					sub.Object.Spec.Package)

				helpers.LogOLMDiagnostics(ctx, APIClient, medik8sparams.OperatorNs,
					nhcparams.ClusterUpgradeSubName, medik8sparams.GAOperatorCatalog, GinkgoWriter.Printf)

				By("Step 2: Deploy NHC controller and verify it is running")

				previousCSV = verifyNHCOperatorReady(
					medik8sparams.OperatorUpgradeTimeout,
					medik8sparams.DefaultTimeout, "on OCP N-1")

				preUpgradeImage, err = nhcutils.GetNHCControllerImage(APIClient)
				Expect(err).NotTo(HaveOccurred())
				GinkgoWriter.Printf("GA operator image: %s\n", preUpgradeImage)

				By("Step 3: Verify GA NHC installation on OCP N-1 (install checkpoint)")

				Expect(previousCSV).NotTo(BeNil(), "No NHC CSV in Succeeded phase")
				GinkgoWriter.Printf("GA NHC CSV: %s\n", previousCSV.Object.Name)
				preOCPUpgradeCSV = previousCSV.Object.Name
				preOCPUpgradeImage = preUpgradeImage

				By("Step 3b: Install the released SNR prerequisite from redhat-operators")

				snrSub, snrErr := nhcutils.InstallGASNR(APIClient)
				Expect(snrErr).NotTo(HaveOccurred(), "Failed to install the SNR prerequisite")
				GinkgoWriter.Printf("SNR Subscription created: %s (catalog: %s, channel: %s, package: %s)\n",
					snrSub.Object.Name,
					snrSub.Object.Spec.CatalogSource,
					snrSub.Object.Spec.Channel,
					snrSub.Object.Spec.Package)

				Eventually(func() error {
					_, csvErr := helpers.FindSucceededCSV(APIClient,
						nhcparams.ClusterUpgradeSNRCSVPattern, medik8sparams.OperatorNs)

					return csvErr
				}, medik8sparams.OperatorUpgradeTimeout, nhcparams.DefaultPollInterval).
					Should(Succeed(), "SNR prerequisite CSV did not become ready")
				Eventually(func() bool { return isSNRCRDInstalled(ctx) },
					medik8sparams.OperatorUpgradeTimeout, nhcparams.DefaultPollInterval).
					Should(BeTrue(), "SNR prerequisite CRD was not established")
				Eventually(func() error {
					snrDaemonSet := &appsv1.DaemonSet{}
					if getErr := APIClient.Get(ctx, client.ObjectKey{
						Name: nhcparams.SNRDaemonSetName, Namespace: medik8sparams.OperatorNs,
					}, snrDaemonSet); getErr != nil {
						return getErr
					}

					desired := snrDaemonSet.Status.DesiredNumberScheduled
					if desired == 0 || snrDaemonSet.Status.NumberReady != desired ||
						snrDaemonSet.Status.UpdatedNumberScheduled != desired {
						return fmt.Errorf("SNR node agents not ready: desired=%d updated=%d ready=%d",
							desired, snrDaemonSet.Status.UpdatedNumberScheduled,
							snrDaemonSet.Status.NumberReady)
					}

					return nil
				}, medik8sparams.OperatorUpgradeTimeout, nhcparams.DefaultPollInterval).
					Should(Succeed(), "SNR node agents were not ready on every scheduled node")

				if medik8sparams.SkipOCPUpgrade {
					By("Step 4: Skipped (MEDIK8S_SKIP_OCP_UPGRADE=true) - OCP upgrade not performed")
				} else {
					By("Step 4: Upgrade OCP from N-1 to N")

					clusterVersion := &configv1.ClusterVersion{}
					Expect(APIClient.Get(ctx, client.ObjectKey{Name: "version"}, clusterVersion)).
						To(Succeed(), "Failed to get ClusterVersion")

					clusterVersion.Spec.DesiredUpdate = &configv1.Update{
						Image: medik8sparams.TargetOCPImage,
						Force: true, // CI release images lack signed update graph metadata
					}

					Expect(APIClient.Update(ctx, clusterVersion)).
						To(Succeed(), "Failed to set desired OCP update")

					GinkgoWriter.Printf("OCP upgrade initiated to image: %s\n",
						medik8sparams.TargetOCPImage)

					Expect(helpers.WaitForClusterVersionCondition(ctx, APIClient,
						"Progressing", configv1.ConditionTrue,
						medik8sparams.OCPUpgradeStartTimeout, nhcparams.DefaultPollInterval,
					)).To(Succeed(), "OCP upgrade did not start progressing")

					Expect(helpers.WaitForClusterVersionCondition(ctx, APIClient,
						"Progressing", configv1.ConditionFalse,
						medik8sparams.OCPUpgradeTimeout, nhcparams.DefaultPollInterval,
					)).To(Succeed(), "OCP upgrade did not complete (still Progressing)")

					Expect(helpers.WaitForClusterVersionCondition(ctx, APIClient,
						"Available", configv1.ConditionTrue,
						medik8sparams.PostUpgradeRecoveryTimeout, nhcparams.DefaultPollInterval,
					)).To(Succeed(), "Cluster not Available after OCP upgrade")

					Expect(helpers.WaitForClusterVersionCondition(ctx, APIClient,
						"Failing", configv1.ConditionFalse,
						medik8sparams.PostUpgradeRecoveryTimeout, nhcparams.DefaultPollInterval,
					)).To(Succeed(), "Cluster is Failing after OCP upgrade")

					Expect(APIClient.Get(ctx, client.ObjectKey{Name: "version"}, clusterVersion)).To(Succeed())
					Expect(clusterVersion.Status.Desired.Version).To(HavePrefix("5.0."),
						"cluster upgrade must finish on OpenShift 5.0")
					Expect(clusterVersion.Status.Desired.Image).To(Equal(medik8sparams.TargetOCPImage),
						"cluster must finish on the requested 5.0 payload")
					AddReportEntry("nhc-cluster-upgrade-finish", map[string]string{
						"version": clusterVersion.Status.Desired.Version,
						"image":   clusterVersion.Status.Desired.Image,
					})

					GinkgoWriter.Println("OCP upgrade completed and cluster is healthy")
				}

				By("Step 5: Verify NHC operator pod survived OCP upgrade and CSV is Succeeded")

				postOCPUpgradeCSV := verifyNHCOperatorReady(
					medik8sparams.PostUpgradeRecoveryTimeout,
					medik8sparams.PostUpgradeRecoveryTimeout, "after OCP upgrade")
				Expect(postOCPUpgradeCSV.Object.Name).To(Equal(preOCPUpgradeCSV),
					"NHC must not silently change versions before the explicit catalog switch")
				previousCSV = postOCPUpgradeCSV

				preUpgradeImage, err = nhcutils.GetNHCControllerImage(APIClient)
				Expect(err).NotTo(HaveOccurred())
				Expect(preUpgradeImage).To(Equal(preOCPUpgradeImage),
					"NHC image must remain unchanged across the OpenShift upgrade")
				GinkgoWriter.Printf("Post-OCP-upgrade baseline for FBC upgrade: CSV=%s image=%s\n",
					previousCSV.Object.Name, preUpgradeImage)

				By("Step 6: Validate GA NHC on OCP N (post-OCP-upgrade remediation)")

				currentTargetNode, err = upgradeRunRemediationCycle(ctx, "post-ocp-upgrade")
				Expect(err).NotTo(HaveOccurred(),
					"Post-OCP-upgrade remediation failed with GA operator")

				cleanupPostRemediationNHC(ctx, &currentTargetNode, "post-ocp-upgrade")

				if medik8sparams.UpgradeToPRBundle {
					By("Step 7: Upgrade the surviving installation directly to the PR-built bundle")

					output, upgradeErr := nhcutils.UpgradeBundle(ctx, candidateInputs.OperatorSDK,
						candidateInputs.Namespace, candidateInputs.Bundle)
					GinkgoWriter.Printf("operator-sdk run bundle-upgrade output:\n%s\n", output)
					Expect(upgradeErr).NotTo(HaveOccurred(),
						"the released operator must be upgraded in place, not uninstalled")

					candidateCSV := waitForNHCUpgradeCSV(candidateInputs.Namespace,
						candidateInputs.Version, candidateInputs.Image, "post-OCP-upgrade candidate")
					Expect(candidateCSV.Object.Name).NotTo(Equal(previousCSV.Object.Name),
						"the PR bundle must produce a new CSV")

					By("Step 8: Validate NHC remediation with the PR-built operator")

					currentTargetNode, err = upgradeRunRemediationCycle(ctx, "post-candidate-upgrade")
					Expect(err).NotTo(HaveOccurred(), "PR-built NHC remediation failed")
					cleanupPostRemediationNHC(ctx, &currentTargetNode, "post-candidate-upgrade")
					AddReportEntry("nhc-cluster-candidate-result", map[string]string{
						"ocpPath": "4.22-to-5.0", "oldCSV": previousCSV.Object.Name,
						"candidateCSV": candidateCSV.Object.Name, "candidateBundle": candidateInputs.Bundle,
						"candidateImage":  candidateInputs.Image,
						"functionalCheck": "remediation completed before and after the PR-bundle upgrade",
					})

					return
				}

				if medik8sparams.SkipDownstreamOperatorUpgrade {
					AddReportEntry("nhc-cluster-upgrade-result", map[string]string{
						"ocpPath":                "4.22-to-5.0",
						"nhcCSV":                 previousCSV.Object.Name,
						"nhcImage":               preUpgradeImage,
						"functionalCheck":        "NHC and SNR remediation completed after the OpenShift upgrade",
						"downstreamCatalogPhase": "deferred by MEDIK8S_SKIP_DOWNSTREAM_OPERATOR_UPGRADE=true",
					})

					return
				}

				if medik8sparams.SkipUpgradeIDMS {
					By("Step 7: Use directly pullable catalog images; no IDMS is required")
				} else {
					By("Step 7: Apply deferred IDMS for Konflux catalog images")

					Expect(medik8sparams.SharedDir).NotTo(BeEmpty(),
						"SHARED_DIR must be set (provided by ci-operator)")

					preIDMSGens, genErr := helpers.GetMCPGenerations(ctx)
					Expect(genErr).NotTo(HaveOccurred(),
						"Failed to capture MCP generations before IDMS apply")

					idmsChanged, applyErr := helpers.ApplyIDMSFromSharedDir(ctx,
						medik8sparams.SharedDir, GinkgoWriter.Printf)
					Expect(applyErr).NotTo(HaveOccurred(),
						"Failed to apply IDMS from SHARED_DIR")

					if idmsChanged {
						By("Waiting for MCP rollout after IDMS change")

						Expect(helpers.WaitForMCPRollout(ctx, preIDMSGens,
							medik8sparams.MCPDetectionTimeout,
							medik8sparams.MCPRolloutTimeout,
							10*time.Second, GinkgoWriter.Printf,
						)).To(Succeed(), "MCP rollout failed after IDMS apply")
					} else {
						GinkgoWriter.Println("IDMS unchanged, skipping MCP rollout wait")
					}
				}

				By("Step 8: Switch operator Subscription to candidate CatalogSource")

				switchTime := time.Now()

				_, err = nhcutils.SwitchSubscriptionCatalog(
					APIClient, medik8sparams.UpgradeCatalogName)
				Expect(err).NotTo(HaveOccurred(),
					"Failed to switch Subscription to target catalog")

				By("Step 9: Wait for operator upgrade or version parity after catalog switch")

				Eventually(func() error {
					sub, subErr := olm.PullSubscription(
						APIClient, nhcparams.ClusterUpgradeSubName, medik8sparams.OperatorNs)
					if subErr != nil {
						return fmt.Errorf("pulling subscription %s/%s: %w",
							medik8sparams.OperatorNs, nhcparams.ClusterUpgradeSubName, subErr)
					}

					if sub == nil || sub.Object == nil {
						return fmt.Errorf(
							"subscription %s/%s returned without error but Object is nil",
							medik8sparams.OperatorNs, nhcparams.ClusterUpgradeSubName)
					}

					if sub.Object.Spec.CatalogSource != medik8sparams.UpgradeCatalogName {
						return fmt.Errorf("subscription source not yet updated to %s",
							medik8sparams.UpgradeCatalogName)
					}

					// Guard against a race where status still reflects the
					// pre-switch snapshot (stale currentCSV == installedCSV
					// from the old catalog) before OLM's catalog-operator has
					// re-resolved against the new catalog source. Without
					// this, that stale-but-internally-consistent status can
					// be misread as "already at target version".
					if sub.Object.Status.LastUpdated.Time.Before(switchTime) {
						return fmt.Errorf(
							"subscription status not yet re-resolved since catalog switch " +
								"(status.lastUpdated predates the switch)")
					}

					currentCSV := sub.Object.Status.CurrentCSV
					if currentCSV == "" {
						return fmt.Errorf("subscription has no currentCSV yet")
					}

					installedCSV := sub.Object.Status.InstalledCSV
					if installedCSV != currentCSV {
						return fmt.Errorf(
							"OLM still reconciling (installed: %s, current: %s)",
							installedCSV, currentCSV)
					}

					for _, cond := range sub.Object.Status.Conditions {
						if cond.Type == olmV1alpha1.SubscriptionCatalogSourcesUnhealthy &&
							cond.Status == corev1.ConditionTrue {
							return fmt.Errorf(
								"catalog unhealthy: %s", cond.Message)
						}
					}

					catalogHealthy := false

					for _, ch := range sub.Object.Status.CatalogHealth {
						if ch.CatalogSourceRef != nil &&
							ch.CatalogSourceRef.Name == medik8sparams.UpgradeCatalogName &&
							ch.Healthy {
							catalogHealthy = true

							break
						}
					}

					if !catalogHealthy {
						return fmt.Errorf(
							"catalog %s not yet healthy in subscription CatalogHealth",
							medik8sparams.UpgradeCatalogName)
					}

					csv, csvErr := olm.PullClusterServiceVersion(
						APIClient, currentCSV, medik8sparams.OperatorNs)
					if csvErr != nil {
						return fmt.Errorf("CSV %s not found: %w", currentCSV, csvErr)
					}

					csvPhase, phaseErr := csv.GetPhase()
					if phaseErr != nil {
						return fmt.Errorf("failed to get phase for CSV %s: %w",
							currentCSV, phaseErr)
					}

					if csvPhase != olmV1alpha1.CSVPhaseSucceeded {
						return fmt.Errorf("CSV %s in phase %s, waiting for Succeeded",
							currentCSV, csvPhase)
					}

					if currentCSV != previousCSV.Object.Name {
						GinkgoWriter.Printf(
							"Operator upgraded: new CSV %s (was: %s)\n",
							currentCSV, previousCSV.Object.Name)

						operatorUpgraded = true
					} else {
						GinkgoWriter.Printf(
							"Version parity: candidate catalog offers same "+
								"version %s as GA; subscription healthy on "+
								"new catalog\n", currentCSV)
					}

					return nil
				}, medik8sparams.OperatorUpgradeTimeout, nhcparams.DefaultPollInterval).Should(Succeed(),
					"Operator upgrade or catalog switch verification failed")

				if medik8sparams.SkipUpgradeIDMS {
					candidateCSV := waitForNHCUpgradeCSV(medik8sparams.OperatorNs,
						medik8sparams.CandidateVersion, medik8sparams.CandidateImage,
						"post-public-catalog-switch candidate")
					Expect(candidateCSV.Object.Name).NotTo(Equal(previousCSV.Object.Name),
						"the public catalog must produce a new candidate CSV")
				}

				if operatorUpgraded {
					By("Step 10: Verify NHC controller pods restarted with new image")

					Eventually(func() error {
						currentImage, imgErr := nhcutils.GetNHCControllerImage(APIClient)
						if imgErr != nil {
							return imgErr
						}

						if currentImage == preUpgradeImage {
							return fmt.Errorf("controller still running old image %s",
								preUpgradeImage)
						}

						GinkgoWriter.Printf("Controller image updated: %s\n", currentImage)

						return nil
					}, medik8sparams.OperatorUpgradeTimeout,
						nhcparams.DefaultPollInterval).Should(Succeed(),
						"NHC controller pods did not restart with new image")
				} else {
					GinkgoWriter.Println(
						"Step 10: Skipped (no operator upgrade occurred, " +
							"Konflux and GA catalogs at same version)")
				}

				By("Step 11: Validate NHC on OCP N (post-catalog-switch remediation)")

				currentTargetNode, err = upgradeRunRemediationCycle(ctx, "post-catalog-switch")
				Expect(err).NotTo(HaveOccurred(),
					"Post-catalog-switch remediation failed")

				cleanupPostRemediationNHC(ctx, &currentTargetNode, "post-catalog-switch")
				AddReportEntry("nhc-cluster-catalog-result", map[string]string{
					"ocpPath": "4.22-to-5.0", "oldCSV": previousCSV.Object.Name,
					"catalog":         medik8sparams.UpgradeCatalogName,
					"functionalCheck": "remediation completed before and after the catalog switch",
				})
			})
	})

// waitForActiveControllerNode polls helpers.GetActiveControllerNode until it
// resolves to a pod that actually exists. Needed because this is called right
// after a fresh operator install/rollout: the leader-election Lease can
// briefly still reference a pod from a prior CSV/Deployment generation
// (stale holder that hasn't been overwritten yet, or hasn't expired) until
// the newly started pods renew or acquire it.
func waitForActiveControllerNode(ctx context.Context) (string, error) {
	var (
		leaderNode string
		lastErr    error
	)

	waitErr := wait.PollUntilContextTimeout(
		ctx, nhcparams.DefaultPollInterval, nhcparams.LeaderAcquisitionTimeout, true,
		func(ctx context.Context) (bool, error) {
			node, err := helpers.GetActiveControllerNode(
				ctx, APIClient, nhcparams.ControllerLeaseName, medik8sparams.OperatorNs)
			if err != nil {
				lastErr = err

				return false, nil
			}

			leaderNode = node

			return true, nil
		},
	)
	if waitErr != nil {
		return "", fmt.Errorf("timed out waiting for a resolvable leader pod: %w", lastErr)
	}

	return leaderNode, nil
}

// upgradeSelectRemediationTarget returns a Ready worker node to remediate,
// excluding the node currently hosting the active NHC controller leader so
// the remediation checkpoint doesn't overlap with a controller failover.
func upgradeSelectRemediationTarget(ctx context.Context) (string, error) {
	leaderNode, err := waitForActiveControllerNode(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to find active NHC controller node: %w", err)
	}

	selectedNode, err := helpers.SelectWorkerNode(ctx, APIClient, leaderNode)
	if err != nil {
		return "", fmt.Errorf(
			"failed to select target worker node (excluding controller node %s): %w",
			leaderNode, err)
	}

	return selectedNode.Name, nil
}

// upgradeRunRemediationCycle drives one full NHC+SNR remediation checkpoint:
// create an NHC CR targeting a single node, stop its kubelet, and verify NHC
// enters Remediating, SNR reboots the node, and NHC returns to Enabled.
// The selected node name is always returned (even on error) so the caller can
// run safety-net recovery regardless of which step failed.
func upgradeRunRemediationCycle(ctx context.Context, phase string) (string, error) {
	nodeName, err := upgradeSelectRemediationTarget(ctx)
	if err != nil {
		return "", fmt.Errorf("[%s] failed to select target node: %w", phase, err)
	}

	GinkgoWriter.Printf("[%s] Target node: %s\n", phase, nodeName)

	originalBootID, err := helpers.GetNodeBootIDFromAPI(ctx, APIClient, nodeName)
	if err != nil {
		return nodeName, fmt.Errorf("[%s] failed to get boot ID for %s: %w", phase, nodeName, err)
	}

	By(fmt.Sprintf("[%s] Deleting any stale NHC/SNR CRs from a prior run", phase))

	cleanupSNRCR(ctx, nodeName)
	cleanupNHCCR(ctx, nhcparams.ClusterUpgradeTestName)

	By(fmt.Sprintf("[%s] Creating NHC CR targeting %s", phase, nodeName))

	nhcCR := buildNHCWithHostnameSelector(nhcparams.ClusterUpgradeTestName, nodeName)
	if createErr := APIClient.Create(ctx, nhcCR); createErr != nil {
		return nodeName, fmt.Errorf("[%s] failed to create NHC CR: %w", phase, createErr)
	}

	By(fmt.Sprintf("[%s] Waiting for NHC to reach Enabled phase", phase))

	if waitErr := waitForNHCPhase(ctx, nhcparams.ClusterUpgradeTestName,
		nhcparams.NHCPhaseEnabled, medik8sparams.DefaultTimeout); waitErr != nil {
		return nodeName, fmt.Errorf("[%s] NHC did not reach Enabled phase: %w", phase, waitErr)
	}

	By(fmt.Sprintf("[%s] Stopping kubelet on %s to trigger remediation", phase, nodeName))

	if sshErr := stopKubeletForRemediation(ctx, nodeName); sshErr != nil {
		return nodeName, fmt.Errorf("[%s] failed to stop kubelet on %s: %w", phase, nodeName, sshErr)
	}

	By(fmt.Sprintf("[%s] Waiting for NHC to enter Remediating phase", phase))

	if waitErr := waitForNHCPhase(ctx, nhcparams.ClusterUpgradeTestName,
		nhcparams.NHCPhaseRemediating, nhcparams.NodeNotReadyTimeout); waitErr != nil {
		return nodeName, fmt.Errorf("[%s] NHC did not enter Remediating phase: %w", phase, waitErr)
	}

	By(fmt.Sprintf("[%s] Waiting for SNR remediation to complete on %s", phase, nodeName))

	if waitErr := waitForSNRRemediationComplete(ctx, nodeName, originalBootID); waitErr != nil {
		return nodeName, fmt.Errorf(
			"[%s] SNR remediation did not complete for %s: %w", phase, nodeName, waitErr)
	}

	By(fmt.Sprintf("[%s] Waiting for node %s to become Ready", phase, nodeName))

	if waitErr := helpers.WaitForNodeReady(ctx, APIClient, nodeName,
		nhcparams.DefaultPollInterval, nhcparams.NodeReadyTimeout, GinkgoWriter.Printf); waitErr != nil {
		return nodeName, fmt.Errorf("[%s] node %s did not become Ready: %w", phase, nodeName, waitErr)
	}

	if medik8sparams.KubeletStopViaOCDebug {
		// Reset the guard now that the node has recovered, so the next
		// remediation cycle's stopKubeletForRemediation call on this node
		// (e.g. the post-catalog-switch phase) isn't silently skipped.
		if guardErr := helpers.RemoveKubeletStopGuard(
			ctx, nodeName, nhcparams.OCDebugKubeletStopTimeout); guardErr != nil {
			GinkgoWriter.Printf(
				"[%s] WARNING: failed to remove kubelet-stop guard on %s (best-effort): %v\n",
				phase, nodeName, guardErr)
		}
	}

	By(fmt.Sprintf("[%s] Waiting for NHC to return to Enabled phase", phase))

	if waitErr := waitForNHCPhase(ctx, nhcparams.ClusterUpgradeTestName,
		nhcparams.NHCPhaseEnabled, nhcparams.UpgradeRemediationCompletionTimeout); waitErr != nil {
		return nodeName, fmt.Errorf("[%s] NHC did not return to Enabled phase: %w", phase, waitErr)
	}

	GinkgoWriter.Printf("[%s] Remediation cycle completed for node %s\n", phase, nodeName)

	return nodeName, nil
}

// verifyNHCOperatorReady polls OLM Subscription/CSV/InstallPlan state until the
// NHC CSV reaches Succeeded, then verifies the controller Deployment is Ready.
// The branching intentionally records detailed OLM failure states while it polls.
//
//nolint:gocognit
func verifyNHCOperatorReady(
	csvTimeout, readyTimeout time.Duration, contextMsg string,
) *olm.ClusterServiceVersionBuilder {
	var csv *olm.ClusterServiceVersionBuilder

	lastSubState := ""
	lastCSVPhase := ""
	lastIPPhase := ""

	Eventually(func() error {
		sub, subErr := olm.PullSubscription(
			APIClient, nhcparams.ClusterUpgradeSubName, medik8sparams.OperatorNs)
		if subErr == nil {
			state := string(sub.Object.Status.State)
			if state != lastSubState {
				GinkgoWriter.Printf("[OLM] Subscription: state=%s currentCSV=%s installedCSV=%s installPlanRef=%s\n",
					state, sub.Object.Status.CurrentCSV, sub.Object.Status.InstalledCSV,
					sub.Object.Status.InstallPlanRef.Name)

				for _, cond := range sub.Object.Status.Conditions {
					GinkgoWriter.Printf("[OLM]   sub-condition: %s=%s reason=%s message=%s\n",
						cond.Type, cond.Status, cond.Reason, cond.Message)
				}

				lastSubState = state
			}
		}

		csvs, csvListErr := olm.ListClusterServiceVersionWithNamePattern(
			APIClient, medik8sparams.OperatorPackage, medik8sparams.OperatorNs)
		if csvListErr == nil {
			for _, csvCandidate := range csvs {
				phase, phaseErr := csvCandidate.GetPhase()
				if phaseErr != nil {
					return fmt.Errorf("failed to get phase for CSV %s: %w",
						csvCandidate.Object.Name, phaseErr)
				}

				phaseStr := string(phase)
				if phaseStr != lastCSVPhase {
					GinkgoWriter.Printf("[OLM] CSV %s: phase=%s reason=%s message=%s\n",
						csvCandidate.Object.Name, phaseStr,
						csvCandidate.Object.Status.Reason,
						csvCandidate.Object.Status.Message)
					lastCSVPhase = phaseStr
				}

				if phase == olmV1alpha1.CSVPhaseSucceeded {
					csv = csvCandidate

					return nil
				}
			}
		}

		installPlans, ipErr := olm.ListInstallPlan(APIClient, medik8sparams.OperatorNs)
		if ipErr == nil {
			for _, installPlan := range installPlans {
				ipPhase := string(installPlan.Object.Status.Phase)
				if ipPhase != lastIPPhase {
					GinkgoWriter.Printf("[OLM] InstallPlan %s: phase=%s\n",
						installPlan.Object.Name, ipPhase)

					for _, cond := range installPlan.Object.Status.Conditions {
						GinkgoWriter.Printf("[OLM]   ip-condition: %s=%s reason=%s message=%s\n",
							cond.Type, cond.Status, cond.Reason, cond.Message)
					}

					lastIPPhase = ipPhase
				}
			}
		}

		return fmt.Errorf("CSV not yet Succeeded")
	}, csvTimeout, nhcparams.DefaultPollInterval).Should(Succeed(),
		fmt.Sprintf("NHC CSV not in Succeeded phase %s", contextMsg))

	nhcDeploy, err := deployment.Pull(
		APIClient, nhcparams.OperatorDeploymentName, medik8sparams.OperatorNs)
	Expect(err).NotTo(HaveOccurred(),
		fmt.Sprintf("Failed to get NHC deployment %s", contextMsg))
	Expect(nhcDeploy.IsReady(readyTimeout)).To(BeTrue(),
		fmt.Sprintf("NHC deployment not Ready %s", contextMsg))

	return csv
}

// cleanupPostRemediationNHC removes the NHC/SNR CRs left over from a
// remediation checkpoint and clears the tracked node name so JustAfterEach's
// safety net does not re-run for an already-recovered node.
func cleanupPostRemediationNHC(ctx context.Context, nodeName *string, phase string) {
	GinkgoHelper()

	By(fmt.Sprintf("Cleaning up NHC/SNR CRs from %s remediation", phase))

	cleanupNHCCR(ctx, nhcparams.ClusterUpgradeTestName)

	if *nodeName != "" {
		cleanupSNRCR(ctx, *nodeName)
	}

	*nodeName = ""
}
