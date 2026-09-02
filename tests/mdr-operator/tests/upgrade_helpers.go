package tests

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"

	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/pod"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/medik8s/system-tests/tests/internal/helpers"
	. "github.com/medik8s/system-tests/tests/internal/medik8sinittools"
	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
	"github.com/medik8s/system-tests/tests/mdr-operator/internal/mdrparams"
)

// setupMDRUpgradePrereqs validates cluster prerequisites shared by both the
// MDR operator-upgrade and cluster-upgrade specs (NHC CRD installed, cloud
// platform with MachineAPI, at least 2 Ready workers), and records the
// initial worker node names. Skips the calling spec if a prerequisite isn't met.
func setupMDRUpgradePrereqs(ctx context.Context) (int, map[string]bool) {
	By("Checking NHC CRD is installed")

	if !isNHCCRDInstalled() {
		Skip("NodeHealthCheck CRD not found; NHC operator not installed -- " +
			"skipping MDR upgrade test (MDR remediation is always NHC-triggered)")
	}

	By("Detecting cluster platform")

	platform, _, err := helpers.DetectPlatform(ctx, APIClient)
	Expect(err).ToNot(HaveOccurred())

	switch platform {
	case configv1.AWSPlatformType,
		configv1.AzurePlatformType,
		configv1.GCPPlatformType,
		configv1.VSpherePlatformType:
		GinkgoWriter.Printf("Platform: %s -- MachineAPI available\n", platform)
	default:
		Skip(fmt.Sprintf(
			"MDR upgrade test requires cloud platform with MachineAPI, got %s", platform))
	}

	By("Verifying at least 2 Ready worker nodes")

	workerCount, err := helpers.CountReadyWorkerNodes(ctx, APIClient)
	Expect(err).ToNot(HaveOccurred())
	Expect(workerCount).To(BeNumerically(">=", 2),
		"MDR upgrade test requires at least 2 Ready worker nodes")

	By("Recording initial worker node names")

	workerNodes := &corev1.NodeList{}
	Expect(APIClient.List(ctx, workerNodes,
		client.MatchingLabels{"node-role.kubernetes.io/worker": ""})).To(Succeed())

	workerNames := make(map[string]bool, len(workerNodes.Items))
	for i := range workerNodes.Items {
		workerNames[workerNodes.Items[i].Name] = true
	}

	return workerCount, workerNames
}

// createMDRUpgradeCRs creates the shared MDRT and NHC CR used to trigger
// NHC-driven MDR remediation cycles during an upgrade spec.
func createMDRUpgradeCRs(ctx context.Context) {
	By("Creating shared MDRT and NHC CR for upgrade remediation cycles")

	mdrt := buildMDRT(mdrparams.UpgradeMDRTName)
	Expect(APIClient.Create(ctx, mdrt)).To(Succeed(),
		"Failed to create MDRT %s", mdrparams.UpgradeMDRTName)

	nhcCR := buildNHCForMDR(mdrparams.UpgradeNHCName, mdrparams.UpgradeMDRTName)
	Expect(APIClient.Create(ctx, nhcCR)).To(Succeed(),
		"Failed to create NHC CR %s", mdrparams.UpgradeNHCName)
}

// mdrUpgradeSafetyNet is used as an upgrade spec's JustAfterEach: dumps
// controller state on failure, then cleans up any MDR CR left over from a
// remediation cycle that didn't complete its own inline cleanup, and waits
// for the worker count to recover before the spec finishes.
func mdrUpgradeSafetyNet(ctx context.Context, getTargetWorkerName func() string, initialWorkerCount int) {
	if CurrentSpecReport().Failed() {
		logMDRControllerState()
	}

	targetWorkerName := getTargetWorkerName()
	if targetWorkerName == "" {
		return
	}

	By("Safety net: deleting any leftover MDR CR for " + targetWorkerName)
	cleanupMDRCR(targetWorkerName)

	By("Safety net: waiting for worker count to recover")

	Eventually(func() (int, error) {
		return helpers.CountReadyWorkerNodes(ctx, APIClient)
	}, mdrparams.NodeReadyTimeout, mdrparams.DefaultPollInterval).Should(
		BeNumerically(">=", initialWorkerCount),
		"Worker count did not recover to %d after MDR remediation", initialWorkerCount)
}

// runMDRRemediationCycle selects a worker node not running the MDR controller,
// stops its kubelet to trigger an NHC-driven MDR remediation, and waits for
// the Machine to be deleted and a replacement node to join Ready.
// Returns the name of the node that was targeted (the caller is responsible
// for tracking the replacement in initialWorkerNames for subsequent cycles).
func runMDRRemediationCycle(
	ctx context.Context, expectedWorkerCount int,
	initialWorkerNames map[string]bool, phase string,
) (string, error) {
	controllerNodes, err := getMDRControllerNodes(ctx)
	if err != nil {
		return "", fmt.Errorf("[%s] failed to get controller nodes: %w", phase, err)
	}

	selectedNode, err := helpers.SelectWorkerNode(ctx, APIClient, controllerNodes...)
	if err != nil {
		return "", fmt.Errorf("[%s] failed to select target node: %w", phase, err)
	}

	nodeName := selectedNode.Name
	GinkgoWriter.Printf("[%s] Target node: %s (excluding controller nodes: %v)\n",
		phase, nodeName, controllerNodes)

	testStartTime := time.Now()

	By(fmt.Sprintf("[%s] Stopping kubelet on worker node %s", phase, nodeName))

	if stopErr := stopKubeletForRemediation(ctx, nodeName); stopErr != nil {
		return "", fmt.Errorf("[%s] failed to stop kubelet on %s: %w", phase, nodeName, stopErr)
	}

	By(fmt.Sprintf("[%s] Waiting for MDR CR to be created by NHC", phase))

	Eventually(func() error {
		_, condErr := getMDRCRCondition(nodeName, mdrparams.ProcessingConditionType)

		return condErr
	}, mdrparams.NodeNotReadyTimeout, mdrparams.DefaultPollInterval).Should(Succeed(),
		"[%s] MDR CR with Processing condition not found for node %s", phase, nodeName)

	By(fmt.Sprintf("[%s] Waiting for MDR remediation to complete", phase))

	newNodeName, waitErr := waitForMDRRemediationComplete(
		ctx, nodeName, expectedWorkerCount, initialWorkerNames,
		testStartTime, mdrparams.RemediationCompleteTimeout,
	)
	if waitErr != nil {
		return nodeName, fmt.Errorf("[%s] MDR remediation did not complete for node %s: %w",
			phase, nodeName, waitErr)
	}

	By(fmt.Sprintf("[%s] Waiting for replacement node %s to become Ready", phase, newNodeName))

	if readyErr := helpers.WaitForNodeReady(
		ctx, APIClient, newNodeName,
		mdrparams.DefaultPollInterval, mdrparams.NodeReadyTimeout,
		GinkgoWriter.Printf,
	); readyErr != nil {
		return newNodeName, fmt.Errorf("[%s] replacement node %s did not become Ready: %w",
			phase, newNodeName, readyErr)
	}

	GinkgoWriter.Printf("[%s] Remediation cycle completed, replacement node: %s\n",
		phase, newNodeName)

	return newNodeName, nil
}

// getMDRControllerNodes returns the node names where MDR controller-manager
// pods are running, so remediation cycles can exclude them from targets.
func getMDRControllerNodes(ctx context.Context) ([]string, error) {
	_ = ctx

	listOptions := metav1.ListOptions{
		LabelSelector: mdrparams.OperatorControllerPodLabelSelector,
	}

	controllerPods, err := pod.List(APIClient, medik8sparams.OperatorNs, listOptions)
	if err != nil {
		return nil, fmt.Errorf("failed to list MDR controller pods: %w", err)
	}

	runningPods := helpers.FilterRunningPods(controllerPods)
	nodes := make([]string, 0, len(runningPods))

	for _, p := range runningPods {
		if p.Object.Spec.NodeName != "" {
			nodes = append(nodes, p.Object.Spec.NodeName)
		}
	}

	return nodes, nil
}
