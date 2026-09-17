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
	"github.com/medik8s/system-tests/tests/snr-operator/internal/snrparams"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// nhcTimedOutAnnotationKey is the annotation that signals NHC timed out.
	nhcTimedOutAnnotationKey = "remediation.medik8s.io/nhc-timed-out"

	// nhcTimedOutAnnotationValue is a valid RFC3339 timestamp.
	// The SNR controller currently checks only for key presence, not value format.
	nhcTimedOutAnnotationValue = "2026-01-01T00:00:00Z"

	reasonNHCTimedOut    = "RemediationTimeoutByNHC"
	reasonNodeNotFound   = "RemediationSkippedNodeNotFound"
	conditionProcessing  = "Processing"
	conditionSucceeded   = "Succeeded"
	conditionStatusFalse = "False"
)

var _ = Describe(
	"SNR Processing Condition tests",
	Ordered,
	ContinueOnFailure,
	Label(labels.OperatorSNR), func() {
		BeforeAll(func() {
			By("Verify SNR deployment is ready")

			snrDeployment, err := deployment.Pull(
				APIClient, snrparams.OperatorDeploymentName, medik8sparams.OperatorNs)
			Expect(err).ToNot(HaveOccurred(), "Failed to get SNR deployment")
			Expect(snrDeployment.IsReady(medik8sparams.DefaultTimeout)).To(BeTrue(),
				"SNR deployment is not Ready")
		})

		Context("when SNR has nhc-timed-out annotation", func() {
			It("Verify SNR conditions with nhc-timed-out annotation",
				reportxml.ID("60881"),
				Label(labels.TierAcceptance, labels.DisruptionNonDestructive,
					labels.PlatformAny, labels.FrequencyWeekly,
					labels.ComponentController), func() {
					snrName := "snr-test-nhc-timed-out"

					By("Creating SNR with nhc-timed-out annotation")

					snrCR := buildSNRWithAnnotations(snrName, map[string]string{
						nhcTimedOutAnnotationKey: nhcTimedOutAnnotationValue,
					})

					err := APIClient.Create(context.TODO(), snrCR)
					Expect(err).ToNot(HaveOccurred(),
						"Failed to create SNR with nhc-timed-out annotation")

					deferDeleteCR(snrCR)

					By("Waiting for Processing and Succeeded conditions to reflect NHC timed-out state")

					Eventually(func() error {
						liveSNR := &unstructured.Unstructured{}
						liveSNR.SetGroupVersionKind(snrGVK)

						getErr := APIClient.Get(context.TODO(),
							client.ObjectKey{
								Name:      snrName,
								Namespace: medik8sparams.OperatorNs,
							},
							liveSNR)
						if getErr != nil {
							return getErr
						}

						return verifyConditionsByType(liveSNR,
							expectedCondition{
								conditionType: conditionProcessing,
								status:        conditionStatusFalse,
								reason:        reasonNHCTimedOut,
							},
							expectedCondition{
								conditionType: conditionSucceeded,
								status:        conditionStatusFalse,
								reason:        reasonNHCTimedOut,
							},
						)
					}, medik8sparams.DefaultTimeout, snrparams.DefaultPollInterval).Should(Succeed(),
						"SNR conditions should reflect NHC timed-out state")
				})
		})

		Context("when SNR targets a non-existent node", func() {
			It("Verify SNR conditions with non-existent node name",
				reportxml.ID("70584"),
				Label(labels.TierAcceptance, labels.DisruptionNonDestructive,
					labels.PlatformAny, labels.FrequencyWeekly,
					labels.ComponentController), func() {
					snrName := "snr-test-nonexistent-node"

					By("Verifying test node name does not exist in the cluster")

					nodeObj := &corev1.Node{}
					nodeErr := APIClient.Get(context.TODO(),
						client.ObjectKey{Name: snrName},
						nodeObj)
					Expect(k8serrors.IsNotFound(nodeErr)).To(BeTrue(),
						"Node %q must not exist in the cluster to avoid triggering real fencing",
						snrName)

					By("Creating SNR with non-existent node name")

					snrCR := buildSNRWithAnnotations(snrName, nil)

					err := APIClient.Create(context.TODO(), snrCR)
					Expect(err).ToNot(HaveOccurred(),
						"Failed to create SNR with non-existent node name")

					deferDeleteCR(snrCR)

					By("Waiting for Processing and Succeeded conditions to reflect node-not-found state")

					Eventually(func() error {
						liveSNR := &unstructured.Unstructured{}
						liveSNR.SetGroupVersionKind(snrGVK)

						getErr := APIClient.Get(context.TODO(),
							client.ObjectKey{
								Name:      snrName,
								Namespace: medik8sparams.OperatorNs,
							},
							liveSNR)
						if getErr != nil {
							return getErr
						}

						return verifyConditionsByType(liveSNR,
							expectedCondition{
								conditionType: conditionProcessing,
								status:        conditionStatusFalse,
								reason:        reasonNodeNotFound,
							},
							expectedCondition{
								conditionType: conditionSucceeded,
								status:        conditionStatusFalse,
								reason:        reasonNodeNotFound,
							},
						)
					}, medik8sparams.DefaultTimeout, snrparams.DefaultPollInterval).Should(Succeed(),
						"SNR conditions should reflect node-not-found state")
				})
		})
	})

// expectedCondition defines the expected values for a status condition.
type expectedCondition struct {
	conditionType string
	status        string
	reason        string
}

// verifyConditionsByType checks SNR conditions by looking up each expected
// condition by its type field, not by positional index.
func verifyConditionsByType(
	snrObj *unstructured.Unstructured, expected ...expectedCondition,
) error {
	conditions, found, err := unstructured.NestedSlice(
		snrObj.Object, "status", "conditions")
	if err != nil {
		return fmt.Errorf("failed to get status.conditions: %w", err)
	}

	if !found || len(conditions) == 0 {
		return fmt.Errorf("no status.conditions found")
	}

	for _, exp := range expected {
		condMap, findErr := findConditionByType(conditions, exp.conditionType)
		if findErr != nil {
			return findErr
		}

		condStatus, statusFound, statusErr := unstructured.NestedString(condMap, "status")
		if statusErr != nil {
			return fmt.Errorf("condition %q status field error: %w",
				exp.conditionType, statusErr)
		}

		if !statusFound {
			return fmt.Errorf("condition %q status field not yet written by controller",
				exp.conditionType)
		}

		condReason, reasonFound, reasonErr := unstructured.NestedString(condMap, "reason")
		if reasonErr != nil {
			return fmt.Errorf("condition %q reason field error: %w",
				exp.conditionType, reasonErr)
		}

		if !reasonFound {
			return fmt.Errorf("condition %q reason field not yet written by controller",
				exp.conditionType)
		}

		if condStatus != exp.status {
			return fmt.Errorf("condition %q status: expected %q, got %q",
				exp.conditionType, exp.status, condStatus)
		}

		if condReason != exp.reason {
			return fmt.Errorf("condition %q reason: expected %q, got %q",
				exp.conditionType, exp.reason, condReason)
		}
	}

	return nil
}

// findConditionByType finds a condition map by its type field.
func findConditionByType(
	conditions []interface{}, condType string,
) (map[string]interface{}, error) {
	for _, cond := range conditions {
		condMap, ok := cond.(map[string]interface{})
		if !ok {
			continue
		}

		typeName, _, _ := unstructured.NestedString(condMap, "type")
		if typeName == condType {
			return condMap, nil
		}
	}

	return nil, fmt.Errorf("condition with type %q not found", condType)
}
