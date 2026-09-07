package tests

import (
	"context"
	"errors"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/deployment"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/olm"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/reportxml"
	olmV1alpha1 "github.com/rh-ecosystem-edge/eco-goinfra/pkg/schemes/olm/operators/v1alpha1"

	"github.com/medik8s/system-tests/tests/far-operator/internal/farparams"
	"github.com/medik8s/system-tests/tests/far-operator/internal/farutils"
	"github.com/medik8s/system-tests/tests/internal/helpers"
	"github.com/medik8s/system-tests/tests/internal/labels"
	. "github.com/medik8s/system-tests/tests/internal/medik8sinittools"
	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
)

var errFARCSVNotSucceeded = errors.New("FAR CSV not yet Succeeded")

var _ = Describe("FAR Operator Upgrade",
	Serial, Ordered,
	Label(labels.OperatorFAR, farparams.Label,
		labels.TierUpgrade, labels.DisruptionDestructive,
		labels.PlatformAWS, labels.FrequencyWeekly,
		labels.ComponentOLM),
	func() {
		var (
			ctx              context.Context
			previousCSV      *olm.ClusterServiceVersionBuilder
			preUpgradeImage  string
			platform         configv1.PlatformType
			region           string
			fenceAgent       string
			sharedParams     map[string]interface{}
			nodeParams       map[string]interface{}
			leaderNode       string
			currentFARName   string
			operatorUpgraded bool
		)

		BeforeAll(func() {
			ctx = context.Background()

			Expect(medik8sparams.TargetOCPImage).NotTo(BeEmpty(),
				"OPENSHIFT_UPGRADE_RELEASE_IMAGE_OVERRIDE or RELEASE_IMAGE_LATEST must be set")

			By("Detecting cluster platform")

			var err error

			platform, region, err = helpers.DetectPlatform(ctx, APIClient)
			Expect(err).ToNot(HaveOccurred())
			Expect(platform).To(Equal(configv1.AWSPlatformType),
				"Upgrade tests require AWS for fence agent remediation")

			By("Verifying at least 3 Ready worker nodes")

			workerCount, err := helpers.CountReadyWorkerNodes(ctx, APIClient)
			Expect(err).ToNot(HaveOccurred())
			Expect(workerCount).To(BeNumerically(">=", 3),
				"Upgrade tests require at least 3 Ready worker nodes")

			By("Creating shared credentials Secret for remediation")

			awsAccessKey, awsSecretKey, credErr := farutils.GetAWSCredentials(
				ctx, APIClient, medik8sparams.OperatorNs)
			Expect(credErr).ToNot(HaveOccurred(), "Failed to get AWS credentials")

			credentialsSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      farparams.SharedCredentialsSecretName,
					Namespace: medik8sparams.OperatorNs,
				},
				StringData: map[string]string{
					"--access-key": awsAccessKey,
					"--secret-key": awsSecretKey,
				},
			}

			Expect(APIClient.Create(ctx, credentialsSecret)).
				To(Succeed(), "Failed to create credentials Secret")

			DeferCleanup(func() {
				if delErr := APIClient.Delete(ctx, credentialsSecret); delErr != nil &&
					!k8serrors.IsNotFound(delErr) {
					GinkgoWriter.Printf("WARNING: failed to delete credentials Secret: %v\n", delErr)
				}
			})
		})

		AfterAll(func() {
			farutils.CleanupUpgradeResources(APIClient, GinkgoWriter.Printf)
		})

		JustAfterEach(func() {
			specReport := CurrentSpecReport()
			if specReport.Failed() {
				GinkgoWriter.Println("Upgrade test failed - collecting FAR controller logs")
				helpers.LogControllerState(ctx, APIClient,
					medik8sparams.OperatorNs, farparams.OperatorControllerPodLabels,
					GinkgoWriter.Printf)
			}

			if currentFARName != "" {
				farNodeName := currentFARName

				farutils.CleanupFARRemediation(ctx, APIClient, farGVK, currentFARName,
					medik8sparams.OperatorNs, GinkgoWriter.Printf)
				currentFARName = ""

				By("Safety net: waiting for node " + farNodeName + " to become Ready")

				if err := farutils.WaitForNodeReady(
					ctx, APIClient, farNodeName, farparams.NodeReadyTimeout,
					GinkgoWriter.Printf); err != nil {
					GinkgoWriter.Printf(
						"WARNING: node %s did not become Ready within %s: %v\n",
						farNodeName, farparams.NodeReadyTimeout, err)
					AddReportEntry("upgrade-recovery-failed",
						fmt.Sprintf("node %s did not recover: %v", farNodeName, err))
				}
			}
		})

		It("should survive OCP upgrade and operator upgrade with working remediation",
			Label(labels.ComponentRemediation),
			reportxml.ID("89717"),
			func() {
				By("Step 1: Install FAR operator GA version from redhat-operators on OCP N-1")

				sub, err := farutils.InstallGAOperator(APIClient)
				Expect(err).NotTo(HaveOccurred(), "Failed to install GA FAR operator")

				GinkgoWriter.Printf("GA Subscription created: %s (catalog: %s, channel: %s, package: %s)\n",
					sub.Object.Name,
					sub.Object.Spec.CatalogSource,
					sub.Object.Spec.Channel,
					sub.Object.Spec.Package)

				helpers.LogOLMDiagnostics(ctx, APIClient, medik8sparams.OperatorNs,
					medik8sparams.GAOperatorCatalog, GinkgoWriter.Printf)

				By("Step 2: Deploy FAR controller and verify it is running")

				previousCSV = verifyFAROperatorReady(
					medik8sparams.OperatorUpgradeTimeout,
					medik8sparams.DefaultTimeout, "on OCP N-1")

				preUpgradeImage, err = farutils.GetFARControllerImage(APIClient)
				Expect(err).NotTo(HaveOccurred())
				GinkgoWriter.Printf("GA operator image: %s\n", preUpgradeImage)

				By("Step 3: Verify GA FAR installation on OCP N-1 (install checkpoint)")

				Expect(previousCSV).NotTo(BeNil(), "No FAR CSV in Succeeded phase")
				GinkgoWriter.Printf("GA FAR CSV: %s\n", previousCSV.Object.Name)

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
					medik8sparams.OCPUpgradeStartTimeout, farparams.DefaultPollInterval,
				)).To(Succeed(), "OCP upgrade did not start progressing")

				Expect(helpers.WaitForClusterVersionCondition(ctx, APIClient,
					"Progressing", configv1.ConditionFalse,
					medik8sparams.OCPUpgradeTimeout, farparams.DefaultPollInterval,
				)).To(Succeed(), "OCP upgrade did not complete (still Progressing)")

				Expect(helpers.WaitForClusterVersionCondition(ctx, APIClient,
					"Available", configv1.ConditionTrue,
					medik8sparams.PostUpgradeRecoveryTimeout, farparams.DefaultPollInterval,
				)).To(Succeed(), "Cluster not Available after OCP upgrade")

				Expect(helpers.WaitForClusterVersionCondition(ctx, APIClient,
					"Failing", configv1.ConditionFalse,
					medik8sparams.PostUpgradeRecoveryTimeout, farparams.DefaultPollInterval,
				)).To(Succeed(), "Cluster is Failing after OCP upgrade")

				GinkgoWriter.Println("OCP upgrade completed and cluster is healthy")

				By("Step 5: Verify FAR operator pod survived OCP upgrade and CSV is Succeeded")

				previousCSV = verifyFAROperatorReady(
					medik8sparams.PostUpgradeRecoveryTimeout,
					medik8sparams.PostUpgradeRecoveryTimeout, "after OCP upgrade")

				preUpgradeImage, err = farutils.GetFARControllerImage(APIClient)
				Expect(err).NotTo(HaveOccurred())
				GinkgoWriter.Printf("Post-OCP-upgrade baseline for FBC upgrade: CSV=%s image=%s\n",
					previousCSV.Object.Name, preUpgradeImage)

				By("Step 6: Validate GA FAR on OCP N (post-OCP-upgrade remediation)")

				fenceAgent, sharedParams, nodeParams, leaderNode, err =
					upgradeProvisionRemediationResources(ctx, platform, region)
				Expect(err).NotTo(HaveOccurred(), "Failed to set up remediation resources")

				currentFARName, err = upgradeRunRemediationCycle(
					ctx, fenceAgent, sharedParams, nodeParams, leaderNode, "post-ocp-upgrade")
				Expect(err).NotTo(HaveOccurred(),
					"Post-OCP-upgrade remediation failed with GA operator")

				cleanupPostRemediation(ctx, &currentFARName, "post-ocp-upgrade")

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

				By("Step 8: Switch operator Subscription to Konflux CatalogSource")

				_, err = farutils.SwitchSubscriptionCatalog(
					APIClient, medik8sparams.UpgradeCatalogName)
				Expect(err).NotTo(HaveOccurred(),
					"Failed to switch Subscription to target catalog")

				By("Step 9: Wait for operator upgrade or version parity after catalog switch")

				Eventually(func() error {
					sub, subErr := olm.PullSubscription(
						APIClient, farparams.UpgradeSubName, medik8sparams.OperatorNs)
					if subErr != nil {
						return fmt.Errorf("pulling subscription %s/%s: %w",
							medik8sparams.OperatorNs, farparams.UpgradeSubName, subErr)
					}

					if sub == nil || sub.Object == nil {
						return fmt.Errorf(
							"subscription %s/%s returned without error but Object is nil",
							medik8sparams.OperatorNs, farparams.UpgradeSubName)
					}

					if sub.Object.Spec.CatalogSource != medik8sparams.UpgradeCatalogName {
						return fmt.Errorf("subscription source not yet updated to %s",
							medik8sparams.UpgradeCatalogName)
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
							"Version parity: Konflux catalog offers same "+
								"version %s as GA; subscription healthy on "+
								"new catalog\n", currentCSV)
					}

					return nil
				}, medik8sparams.OperatorUpgradeTimeout, farparams.DefaultPollInterval).Should(Succeed(),
					"Operator upgrade or catalog switch verification failed")

				if operatorUpgraded {
					By("Step 10: Verify FAR controller pods restarted with new image")

					Eventually(func() error {
						currentImage, imgErr := farutils.GetFARControllerImage(APIClient)
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
						farparams.DefaultPollInterval).Should(Succeed(),
						"FAR controller pods did not restart with new image")
				} else {
					GinkgoWriter.Println(
						"Step 10: Skipped (no operator upgrade occurred, " +
							"Konflux and GA catalogs at same version)")
				}

				By("Step 11: Validate FAR on OCP N (post-catalog-switch remediation)")

				fenceAgent, sharedParams, nodeParams, leaderNode, err =
					upgradeProvisionRemediationResources(ctx, platform, region)
				Expect(err).NotTo(HaveOccurred(), "Failed to set up remediation resources")

				currentFARName, err = upgradeRunRemediationCycle(
					ctx, fenceAgent, sharedParams, nodeParams, leaderNode, "post-catalog-switch")
				Expect(err).NotTo(HaveOccurred(),
					"Post-catalog-switch remediation failed")

				cleanupPostRemediation(ctx, &currentFARName, "post-catalog-switch")
			})
	})

// upgradeProvisionRemediationResources resolves the fence agent, builds node
// parameters, and waits for leader election to settle. The credentials Secret
// is created once in BeforeAll.
func upgradeProvisionRemediationResources(
	ctx context.Context,
	platform configv1.PlatformType,
	region string,
) (string, map[string]interface{}, map[string]interface{}, string, error) {
	fenceAgent, _, err := farutils.FenceAgentForPlatform(platform)
	if err != nil {
		return "", nil, nil, "", fmt.Errorf("failed to resolve fence agent: %w", err)
	}

	GinkgoWriter.Printf("Fence agent: %s, Region: %s\n", fenceAgent, region)

	sharedParams := map[string]interface{}{
		"--region":          region,
		"--action":          "reboot",
		"--skip-race-check": "",
	}

	awsNodeParams, err := farutils.BuildAWSNodeParameters(ctx, APIClient)
	if err != nil {
		return "", nil, nil, "", fmt.Errorf("failed to build AWS node parameters: %w", err)
	}

	nodeParams := make(map[string]interface{})

	for paramName, nodeMap := range awsNodeParams {
		inner := make(map[string]interface{}, len(nodeMap))
		for nodeName, val := range nodeMap {
			inner[nodeName] = val
		}

		nodeParams[paramName] = inner
	}

	var leader string

	Eventually(func() error {
		var leaderErr error

		leader, leaderErr = farutils.GetActiveFARControllerNode(ctx, APIClient)

		return leaderErr
	}, farparams.ControllerHandoverTimeout, farparams.DefaultPollInterval).Should(Succeed(),
		"FAR leader election did not settle")

	return fenceAgent, sharedParams, nodeParams, leader, nil
}

func upgradeRunRemediationCycle(
	ctx context.Context,
	fenceAgent string,
	sharedParams, nodeParams map[string]interface{},
	leaderNode, phase string,
) (string, error) {
	selectedNode, err := helpers.SelectWorkerNode(ctx, APIClient, leaderNode)
	if err != nil {
		return "", fmt.Errorf("[%s] failed to select target node: %w", phase, err)
	}

	nodeName := selectedNode.Name
	GinkgoWriter.Printf("[%s] Target node: %s (leader: %s)\n", phase, nodeName, leaderNode)

	originalBootID, err := farutils.GetNodeBootIDFromAPI(ctx, APIClient, nodeName)
	if err != nil {
		return "", fmt.Errorf("[%s] failed to get boot ID: %w", phase, err)
	}

	farCRName := nodeName

	By(fmt.Sprintf("[%s] Deleting any stale FAR CR from a prior run", phase))

	helpers.DeleteRemediationCR(ctx, APIClient, farGVK, farCRName,
		medik8sparams.OperatorNs, farparams.DefaultPollInterval,
		farparams.RemediationCRDeletionTimeout, GinkgoWriter.Printf)

	By(fmt.Sprintf("[%s] Cleaning CRI-O overlay storage on %s", phase, nodeName))

	helpers.RemoveWorkloadImage(ctx, nodeName, farparams.WorkloadTestImage,
		farparams.CrioCleanupTimeout, GinkgoWriter.Printf)

	By(fmt.Sprintf("[%s] Creating workload pod pinned to %s", phase, nodeName))

	workloadPodName, createErr := helpers.CreateWorkloadPod(ctx, APIClient,
		nodeName, medik8sparams.OperatorNs, farparams.WorkloadTestImage,
		"far-upgrade-workload-", farparams.WorkloadPodReadyTimeout,
		farparams.DefaultPollInterval)
	Expect(createErr).NotTo(HaveOccurred(),
		"[%s] Failed to create workload pod on %s", phase, nodeName)

	DeferCleanup(func() {
		helpers.DeleteWorkloadPod(ctx, APIClient, workloadPodName,
			medik8sparams.OperatorNs, GinkgoWriter.Printf)
	})

	GinkgoWriter.Printf("[%s] Workload pod %s running on %s\n",
		phase, workloadPodName, nodeName)

	farObj := buildFARUnstructured(farCRName, fenceAgent, sharedParams, nodeParams)

	GinkgoWriter.Printf("[%s] Creating FAR CR %s\n", phase, farCRName)

	Eventually(func() error {
		createErr := APIClient.Create(ctx, farObj)
		if createErr != nil && k8serrors.IsAlreadyExists(createErr) {
			GinkgoWriter.Printf("[%s] FAR CR %s already exists, treating as success\n",
				phase, farCRName)

			return nil
		}

		return createErr
	}, farparams.WorkloadPodReadyTimeout, farparams.DefaultPollInterval).Should(Succeed(),
		"[%s] Failed to create FAR CR %s", phase, farCRName)

	GinkgoWriter.Printf("[%s] Waiting for node %s remediation\n", phase, nodeName)

	waitForRemediation(ctx, APIClient, nodeName, originalBootID)

	By(fmt.Sprintf("[%s] Verifying workload pod %s was evicted", phase, workloadPodName))

	Eventually(func() bool {
		pod := &corev1.Pod{}
		getErr := APIClient.Get(ctx, client.ObjectKey{
			Name:      workloadPodName,
			Namespace: medik8sparams.OperatorNs,
		}, pod)

		return k8serrors.IsNotFound(getErr) || pod.DeletionTimestamp != nil
	}, farparams.WorkloadEvictionTimeout, farparams.DefaultPollInterval).Should(BeTrue(),
		"[%s] Workload pod %s was not evicted after remediation", phase, workloadPodName)

	GinkgoWriter.Printf("[%s] Remediation cycle completed for node %s (workload evicted)\n",
		phase, nodeName)

	return farCRName, nil
}

func waitForFARCSVSucceeded(
	csvTimeout time.Duration, contextMsg string,
) *olm.ClusterServiceVersionBuilder {
	var csv *olm.ClusterServiceVersionBuilder

	lastSubState := ""
	lastCSVPhase := ""
	lastIPPhase := ""

	Eventually(func() error {
		logFARSubscriptionState(&lastSubState)

		var err error

		csv, err = findSucceededFARCSV(&lastCSVPhase)
		if err == nil {
			return nil
		}

		if !errors.Is(err, errFARCSVNotSucceeded) {
			return err
		}

		logFARInstallPlanStates(&lastIPPhase)

		return fmt.Errorf("CSV not yet Succeeded")
	}, csvTimeout, farparams.DefaultPollInterval).Should(Succeed(),
		fmt.Sprintf("FAR CSV not in Succeeded phase %s", contextMsg))

	return csv
}

func logFARSubscriptionState(lastState *string) {
	sub, err := olm.PullSubscription(APIClient, farparams.UpgradeSubName, medik8sparams.OperatorNs)
	if err != nil {
		return
	}

	state := string(sub.Object.Status.State)
	if state == *lastState {
		return
	}

	installPlanName := ""
	if sub.Object.Status.InstallPlanRef != nil {
		installPlanName = sub.Object.Status.InstallPlanRef.Name
	}

	GinkgoWriter.Printf("[OLM] Subscription: state=%s currentCSV=%s installedCSV=%s installPlanRef=%s\n",
		state, sub.Object.Status.CurrentCSV, sub.Object.Status.InstalledCSV, installPlanName)

	for _, cond := range sub.Object.Status.Conditions {
		GinkgoWriter.Printf("[OLM]   sub-condition: %s=%s reason=%s message=%s\n",
			cond.Type, cond.Status, cond.Reason, cond.Message)
	}

	*lastState = state
}

func findSucceededFARCSV(lastPhase *string) (*olm.ClusterServiceVersionBuilder, error) {
	csvs, err := olm.ListClusterServiceVersionWithNamePattern(
		APIClient, medik8sparams.OperatorPackage, medik8sparams.OperatorNs)
	if err != nil {
		return nil, errFARCSVNotSucceeded
	}

	for _, csvBuilder := range csvs {
		phase, phaseErr := csvBuilder.GetPhase()
		if phaseErr != nil {
			return nil, fmt.Errorf("failed to get phase for CSV %s: %w", csvBuilder.Object.Name, phaseErr)
		}

		phaseStr := string(phase)
		if phaseStr != *lastPhase {
			GinkgoWriter.Printf("[OLM] CSV %s: phase=%s reason=%s message=%s\n",
				csvBuilder.Object.Name, phaseStr, csvBuilder.Object.Status.Reason, csvBuilder.Object.Status.Message)
			*lastPhase = phaseStr
		}

		if phase == olmV1alpha1.CSVPhaseSucceeded {
			return csvBuilder, nil
		}
	}

	return nil, errFARCSVNotSucceeded
}

func logFARInstallPlanStates(lastPhase *string) {
	installPlans, err := olm.ListInstallPlan(APIClient, medik8sparams.OperatorNs)
	if err != nil {
		return
	}

	for _, installPlan := range installPlans {
		phase := string(installPlan.Object.Status.Phase)
		if phase == *lastPhase {
			continue
		}

		GinkgoWriter.Printf("[OLM] InstallPlan %s: phase=%s\n", installPlan.Object.Name, phase)

		for _, cond := range installPlan.Object.Status.Conditions {
			GinkgoWriter.Printf("[OLM]   ip-condition: %s=%s reason=%s message=%s\n",
				cond.Type, cond.Status, cond.Reason, cond.Message)
		}

		*lastPhase = phase
	}
}

func verifyFAROperatorReady(
	csvTimeout, readyTimeout time.Duration, contextMsg string,
) *olm.ClusterServiceVersionBuilder {
	csv := waitForFARCSVSucceeded(csvTimeout, contextMsg)

	farDeploy, err := deployment.Pull(
		APIClient, farparams.OperatorDeploymentName, medik8sparams.OperatorNs)
	Expect(err).NotTo(HaveOccurred(),
		fmt.Sprintf("Failed to get FAR deployment %s", contextMsg))
	Expect(farDeploy.IsReady(readyTimeout)).To(BeTrue(),
		fmt.Sprintf("FAR deployment not Ready %s", contextMsg))

	return csv
}

func cleanupPostRemediation(ctx context.Context, farName *string, phase string) {
	GinkgoHelper()

	By(fmt.Sprintf("Cleaning up FAR CR from %s remediation", phase))

	farutils.CleanupFARRemediation(ctx, APIClient, farGVK, *farName,
		medik8sparams.OperatorNs, GinkgoWriter.Printf)
	*farName = ""
}
