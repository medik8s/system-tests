package tests

import (
	"context"
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/deployment"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/pod"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/reportxml"

	"github.com/medik8s/system-tests/tests/internal/helpers"
	"github.com/medik8s/system-tests/tests/internal/labels"
	. "github.com/medik8s/system-tests/tests/internal/medik8sinittools"
	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
	"github.com/medik8s/system-tests/tests/mdr-operator/internal/mdrparams"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe(
	"MDR Negative Validation tests",
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

			cleanupMDRT(mdrparams.MDRTNegativeTestName)
			cleanupMDRT(mdrparams.MDRTInvalidTestName)
		})

		AfterAll(func() {
			By("Cleaning up test MDRTs if unexpectedly created")

			cleanupMDRT(mdrparams.MDRTNegativeTestName)
			cleanupMDRT(mdrparams.MDRTInvalidTestName)

			By("Verifying MDR controller pod is running")

			Eventually(verifyMDRControllerRunning,
				medik8sparams.DefaultTimeout, mdrparams.DefaultPollInterval).Should(Succeed(),
				"MDR controller pod should be running after negative tests")
		})

		It("Verify MDRT with invalid values is rejected by API server",
			reportxml.ID("60889"),
			Label(labels.TierAcceptance,
				labels.DisruptionNonDestructive, labels.PlatformAny,
				labels.FrequencyPresubmit), func() {
				var validationErrors []string

				By("Creating MDRT with non-existent namespace")

				mdrtInvalidNs := buildMDRT(mdrparams.MDRTNegativeTestName)
				mdrtInvalidNs.SetNamespace(mdrparams.MDRTInvalidTestNamespace)

				err := APIClient.Create(context.Background(), mdrtInvalidNs)
				if err == nil {
					DeferCleanup(func() {
						By("Cleaning up unexpectedly created MDRT in non-existent namespace")

						if delErr := APIClient.Delete(context.Background(), mdrtInvalidNs); delErr != nil &&
							!k8serrors.IsNotFound(delErr) {
							GinkgoWriter.Printf("Warning: failed to delete MDRT %s/%s: %v\n",
								mdrtInvalidNs.GetNamespace(), mdrtInvalidNs.GetName(), delErr)
						}
					})

					validationErrors = append(validationErrors,
						fmt.Sprintf("MDRT with namespace %q was unexpectedly created",
							mdrparams.MDRTInvalidTestNamespace))
				} else if !k8serrors.IsNotFound(err) {
					validationErrors = append(validationErrors,
						fmt.Sprintf("MDRT with namespace %q: expected NotFound error, got: %v",
							mdrparams.MDRTInvalidTestNamespace, err))
				}

				By("Creating MDRT with name violating RFC 1123")

				mdrtInvalidName := buildMDRT(mdrparams.MDRTInvalidTestName)

				err = APIClient.Create(context.Background(), mdrtInvalidName)
				if err == nil {
					DeferCleanup(func() { cleanupMDRT(mdrtInvalidName.GetName()) })

					validationErrors = append(validationErrors,
						fmt.Sprintf("MDRT with name %q was unexpectedly created",
							mdrparams.MDRTInvalidTestName))
				} else if !k8serrors.IsInvalid(err) {
					validationErrors = append(validationErrors,
						fmt.Sprintf("MDRT with name %q: expected Invalid error, got: %v",
							mdrparams.MDRTInvalidTestName, err))
				}

				if len(validationErrors) > 0 {
					Fail("MDRT negative validation failures:\n- " +
						strings.Join(validationErrors, "\n- "))
				}
			})
	})

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
