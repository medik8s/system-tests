package tests

import (
	"context"
	"fmt"
	"math/rand"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/deployment"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/pod"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/reportxml"

	"github.com/medik8s/system-tests/tests/internal/helpers"
	"github.com/medik8s/system-tests/tests/internal/labels"
	. "github.com/medik8s/system-tests/tests/internal/medik8sinittools"
	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
	"github.com/medik8s/system-tests/tests/mdr-operator/internal/mdrparams"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe(
	"MDR Condition tests",
	Ordered,
	ContinueOnFailure,
	Label(labels.OperatorMDR, mdrparams.Label), func() {
		BeforeAll(func() {
			By("Verify MDR deployment is ready")

			mdrDeployment, err := deployment.Pull(
				APIClient, mdrparams.OperatorDeploymentName, medik8sparams.OperatorNs)
			Expect(err).ToNot(HaveOccurred(), "Failed to get MDR deployment")
			Expect(mdrDeployment.IsReady(medik8sparams.DefaultTimeout)).To(BeTrue(),
				"MDR deployment is not Ready")

			By("Pre-cleaning stale test resources from previous runs")

			cleanupMDRCR(mdrparams.MDRConditionTestName)
			cleanupMDRCR(mdrparams.MDRNonExistentNodeTestName)
		})

		AfterAll(func() {
			By("Cleaning up condition test MDR CRs")

			cleanupMDRCR(mdrparams.MDRConditionTestName)
			cleanupMDRCR(mdrparams.MDRNonExistentNodeTestName)

			By("Verifying MDR controller pod is running")

			Eventually(verifyMDRControllerRunning,
				medik8sparams.DefaultTimeout, mdrparams.DefaultPollInterval).Should(Succeed(),
				"MDR controller pod should be running after condition tests")
		})

		Context("when MDR has nhc-timed-out annotation", func() {
			It("Verify MDR conditions with nhc-timed-out annotation",
				reportxml.ID("65763"),
				Label(labels.TierAcceptance, labels.ComponentController,
					labels.DisruptionNonDestructive, labels.PlatformAny,
					labels.FrequencyWeekly), func() {
					By("Creating MDR with nhc-timed-out annotation")

					mdrCR := buildMDRWithAnnotations(mdrparams.MDRConditionTestName,
						map[string]string{
							mdrparams.NHCTimedOutAnnotationKey: mdrparams.NHCTimedOutAnnotationValue,
						})

					err := APIClient.Create(context.Background(), mdrCR)
					Expect(err).ToNot(HaveOccurred(),
						"Failed to create MDR with nhc-timed-out annotation")

					deferDeleteMDRCR(mdrparams.MDRConditionTestName)

					By("Waiting for Processing and Succeeded conditions to reflect NHC timed-out state")

					Eventually(func() error {
						liveMDR := &unstructured.Unstructured{}
						liveMDR.SetGroupVersionKind(mdrGVK)

						getErr := APIClient.Get(context.Background(),
							client.ObjectKey{
								Name:      mdrparams.MDRConditionTestName,
								Namespace: medik8sparams.OperatorNs,
							},
							liveMDR)
						if getErr != nil {
							return getErr
						}

						return verifyMDRConditionsByType(liveMDR,
							expectedCondition{
								conditionType: mdrparams.ProcessingConditionType,
								status:        mdrparams.ConditionStatusFalse,
								reason:        mdrparams.ConditionReasonStoppedByNHC,
							},
							expectedCondition{
								conditionType: mdrparams.SucceededConditionType,
								status:        mdrparams.ConditionStatusFalse,
								reason:        mdrparams.ConditionReasonStoppedByNHC,
							},
						)
					}, medik8sparams.DefaultTimeout, mdrparams.DefaultPollInterval).Should(Succeed(),
						"MDR conditions should reflect NHC timed-out state")

					By("Verifying controller log contains remediation-stopped message")

					Eventually(func() error {
						return findMessageInControllerLogs(
							mdrparams.MDRRemStoppedLogMsg, mdrparams.ControllerLogWindow)
					}, medik8sparams.DefaultTimeout, mdrparams.DefaultPollInterval).Should(Succeed(),
						"MDR controller log should contain %q", mdrparams.MDRRemStoppedLogMsg)
				})
		})

		Context("when MDR targets a non-existent node", func() {
			It("Verify MDR conditions with non-existent node name",
				reportxml.ID("66137"),
				Label(labels.TierAcceptance, labels.ComponentController,
					labels.DisruptionNonDestructive, labels.PlatformAny,
					labels.FrequencyWeekly), func() {
					By("Verifying test node name does not exist in the cluster")

					nodeObj := &corev1.Node{}
					nodeErr := APIClient.Get(context.Background(),
						client.ObjectKey{Name: mdrparams.MDRNonExistentNodeTestName},
						nodeObj)
					Expect(k8serrors.IsNotFound(nodeErr)).To(BeTrue(),
						"Node %q must not exist in the cluster to avoid triggering real fencing",
						mdrparams.MDRNonExistentNodeTestName)

					By("Creating MDR with non-existent node name")

					mdrCR := buildMDR(mdrparams.MDRNonExistentNodeTestName)

					err := APIClient.Create(context.Background(), mdrCR)
					Expect(err).ToNot(HaveOccurred(),
						"Failed to create MDR with non-existent node name")

					deferDeleteMDRCR(mdrparams.MDRNonExistentNodeTestName)

					By("Waiting for Processing and Succeeded conditions to reflect node-not-found state")

					Eventually(func() error {
						liveMDR := &unstructured.Unstructured{}
						liveMDR.SetGroupVersionKind(mdrGVK)

						getErr := APIClient.Get(context.Background(),
							client.ObjectKey{
								Name:      mdrparams.MDRNonExistentNodeTestName,
								Namespace: medik8sparams.OperatorNs,
							},
							liveMDR)
						if getErr != nil {
							return getErr
						}

						return verifyMDRConditionsByType(liveMDR,
							expectedCondition{
								conditionType: mdrparams.ProcessingConditionType,
								status:        mdrparams.ConditionStatusFalse,
								reason:        mdrparams.ConditionReasonNodeNotFound,
							},
							expectedCondition{
								conditionType: mdrparams.SucceededConditionType,
								status:        mdrparams.ConditionStatusFalse,
								reason:        mdrparams.ConditionReasonNodeNotFound,
							},
						)
					}, medik8sparams.DefaultTimeout, mdrparams.DefaultPollInterval).Should(Succeed(),
						"MDR conditions should reflect node-not-found state")
				})
		})

		Context("when MDR targets a control-plane node", func() {
			var (
				controlPlaneNodeName string
				platform             configv1.PlatformType
			)

			BeforeAll(func() {
				By("Detecting cluster platform")

				var platformErr error

				platform, _, platformErr = helpers.DetectPlatform(context.Background(), APIClient)
				Expect(platformErr).ToNot(HaveOccurred(), "Failed to detect cluster platform")

				switch platform {
				case configv1.BareMetalPlatformType, configv1.NonePlatformType:
					GinkgoWriter.Printf("Platform %s: control-plane nodes have no controller owner\n", platform)
				default:
					Skip(fmt.Sprintf("Skipping control-plane tests on %s: "+
						"CPMS gives control-plane Machines a controller owner, "+
						"causing MDR to start Machine deletion (destructive)", platform))
				}

				By("Selecting a random control-plane node")

				cpNodes, err := listControlPlaneNodes(context.Background(), APIClient)
				Expect(err).ToNot(HaveOccurred(), "Failed to list control-plane nodes")
				Expect(cpNodes.Items).ToNot(BeEmpty(), "No control-plane nodes found")

				controlPlaneNodeName = cpNodes.Items[rand.Intn(len(cpNodes.Items))].Name
				GinkgoWriter.Printf("Selected control-plane node: %s\n", controlPlaneNodeName)

				By("Pre-cleaning stale control-plane MDR CR from previous runs")

				cleanupMDRCR(controlPlaneNodeName)

				DeferCleanup(func() {
					By("Safety-net cleanup of control-plane MDR CR")
					cleanupMDRCR(controlPlaneNodeName)
				})
			})

			AfterEach(func() {
				if controlPlaneNodeName != "" {
					cleanupMDRCR(controlPlaneNodeName)
				}
			})

			It("Verify MDR conditions with control-plane node name",
				reportxml.ID("66351"),
				Label(labels.TierAcceptance, labels.ComponentController,
					labels.DisruptionNonDestructive, labels.PlatformBareMetal,
					labels.FrequencyWeekly), func() {
					By(fmt.Sprintf("Creating MDR for control-plane node %s", controlPlaneNodeName))

					mdrCR := buildMDR(controlPlaneNodeName)

					err := APIClient.Create(context.Background(), mdrCR)
					Expect(err).ToNot(HaveOccurred(),
						"Failed to create MDR for control-plane node %s", controlPlaneNodeName)

					By("Waiting for Processing and Succeeded conditions to reflect no-controller-owner state")

					Eventually(func() error {
						liveMDR := &unstructured.Unstructured{}
						liveMDR.SetGroupVersionKind(mdrGVK)

						getErr := APIClient.Get(context.Background(),
							client.ObjectKey{
								Name:      controlPlaneNodeName,
								Namespace: medik8sparams.OperatorNs,
							},
							liveMDR)
						if getErr != nil {
							return getErr
						}

						return verifyMDRConditionsByType(liveMDR,
							expectedCondition{
								conditionType: mdrparams.ProcessingConditionType,
								status:        mdrparams.ConditionStatusFalse,
								reason:        mdrparams.ConditionReasonNoControllerOwner,
							},
							expectedCondition{
								conditionType: mdrparams.SucceededConditionType,
								status:        mdrparams.ConditionStatusFalse,
								reason:        mdrparams.ConditionReasonNoControllerOwner,
							},
						)
					}, medik8sparams.DefaultTimeout, mdrparams.DefaultPollInterval).Should(Succeed(),
						"MDR conditions should reflect no-controller-owner state")
				})

			It("Verify PermanentNodeDeletionExpected condition with control-plane node",
				reportxml.ID("66317"),
				Label(labels.TierAcceptance, labels.ComponentController,
					labels.DisruptionNonDestructive, labels.PlatformBareMetal,
					labels.FrequencyWeekly), func() {
					By(fmt.Sprintf("Creating MDR for control-plane node %s", controlPlaneNodeName))

					mdrCR := buildMDR(controlPlaneNodeName)

					err := APIClient.Create(context.Background(), mdrCR)
					Expect(err).ToNot(HaveOccurred(),
						"Failed to create MDR for control-plane node %s", controlPlaneNodeName)

					GinkgoWriter.Printf("Expecting PermanentNodeDeletionExpected "+
						"status=%s reason=%s\n",
						mdrparams.ConditionStatusFalse, mdrparams.ConditionReasonKeepsNodeName)

					By("Waiting for PermanentNodeDeletionExpected condition")

					Eventually(func() error {
						liveMDR := &unstructured.Unstructured{}
						liveMDR.SetGroupVersionKind(mdrGVK)

						getErr := APIClient.Get(context.Background(),
							client.ObjectKey{
								Name:      controlPlaneNodeName,
								Namespace: medik8sparams.OperatorNs,
							},
							liveMDR)
						if getErr != nil {
							return getErr
						}

						return verifyMDRConditionsByType(liveMDR,
							expectedCondition{
								conditionType: mdrparams.PermanentNodeDeletionExpectedConditionType,
								status:        mdrparams.ConditionStatusFalse,
								reason:        mdrparams.ConditionReasonKeepsNodeName,
								message:       mdrparams.ConditionMessageKeepsNodeName,
							},
						)
					}, medik8sparams.DefaultTimeout, mdrparams.DefaultPollInterval).Should(Succeed(),
						"PermanentNodeDeletionExpected condition should reflect baremetal expectations")
				})
		})
	})

// verifyMDRControllerRunning checks the MDR controller pod is running
// with the expected replica count.
func verifyMDRControllerRunning() error {
	listOptions := metav1.ListOptions{
		LabelSelector: mdrparams.OperatorControllerPodLabelSelector,
	}

	allPods, listErr := pod.List(APIClient, medik8sparams.OperatorNs, listOptions)
	if listErr != nil {
		return fmt.Errorf("failed to list MDR pods: %w", listErr)
	}

	mdrPods := helpers.FilterPodsByDeployment(allPods, mdrparams.OperatorDeploymentName)
	runningCount := int32(len(helpers.FilterRunningPods(mdrPods)))

	if runningCount != mdrparams.ExpectedReplicas {
		return fmt.Errorf("expected %d running MDR pod(s), found %d",
			mdrparams.ExpectedReplicas, runningCount)
	}

	return nil
}
