package tests

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/deployment"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/reportxml"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/medik8s/system-tests/tests/internal/labels"
	. "github.com/medik8s/system-tests/tests/internal/medik8sinittools"
	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
	"github.com/medik8s/system-tests/tests/sbr-operator/internal/sbrparams"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

var _ = Describe(
	"SBR Functional — detectOnlyMode",
	Ordered,
	ContinueOnFailure,
	Label(sbrparams.Label), func() {
		var (
			detectOnlySBRC     *unstructured.Unstructured
			rwxStorageClass    string
			existingSBRCRNames map[string]bool
		)

		BeforeAll(func() {
			By("Verifying SBR operator deployment is ready")

			sbrDeployment, pullErr := deployment.Pull(
				APIClient, sbrparams.OperatorDeploymentName, medik8sparams.OperatorNs)
			Expect(pullErr).ToNot(HaveOccurred(), "Failed to get SBR operator deployment")
			Expect(sbrDeployment.IsReady(medik8sparams.DefaultTimeout)).To(BeTrue(),
				"SBR operator deployment must be Ready before running detectOnlyMode tests")

			By("Discovering RWX storage class")

			rwxStorageClass = discoverRWXStorageClass()
			if rwxStorageClass == "" {
				Skip("No RWX storage class found; set SBR_STORAGE_CLASS env or deploy ODF/CephFS before running")
			}

			GinkgoWriter.Printf("Using storage class: %s\n", rwxStorageClass)

			By("Cleaning up any stale detectOnly StorageBasedRemediationConfig from a previous run")

			stale := buildSBRC(sbrparams.SBRCDetectOnlyTestName, map[string]interface{}{})

			deleteErr := APIClient.Delete(context.TODO(), stale)
			if deleteErr != nil && !k8serrors.IsNotFound(deleteErr) {
				GinkgoT().Logf("Warning: pre-test cleanup of %s failed: %v",
					sbrparams.SBRCDetectOnlyTestName, deleteErr)
			}

			By("Waiting for stale StorageBasedRemediationConfig to be fully removed before recreating")

			Eventually(func() error {
				staleCheck := buildSBRC(sbrparams.SBRCDetectOnlyTestName, map[string]interface{}{})

				getErr := APIClient.Get(context.TODO(),
					types.NamespacedName{Name: sbrparams.SBRCDetectOnlyTestName, Namespace: medik8sparams.OperatorNs},
					staleCheck)

				if k8serrors.IsNotFound(getErr) {
					return nil
				}

				if getErr != nil {
					return getErr
				}

				return fmt.Errorf("SBRC %s still terminating", sbrparams.SBRCDetectOnlyTestName)
			}, sbrparams.SBRCReadyTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
				"Stale SBRC must be fully gone before recreating")

			By("Snapshotting existing StorageBasedRemediation CRs to baseline the Consistently check")

			sbrListGVK := schema.GroupVersionKind{
				Group:   sbrparams.CRDGroup,
				Version: sbrparams.CRDVersion,
				Kind:    "StorageBasedRemediationList",
			}

			existingList := &unstructured.UnstructuredList{}
			existingList.SetGroupVersionKind(sbrListGVK)

			existingSBRCRNames = make(map[string]bool)

			if listErr := APIClient.List(context.TODO(), existingList,
				&client.ListOptions{Namespace: medik8sparams.OperatorNs}); listErr == nil {
				for idx := range existingList.Items {
					existingSBRCRNames[existingList.Items[idx].GetName()] = true
				}
			}

			By("Creating StorageBasedRemediationConfig with detectOnlyMode: Enabled")

			detectOnlySBRC = buildSBRC(sbrparams.SBRCDetectOnlyTestName, map[string]interface{}{
				"detectOnlyMode":     "Enabled",
				"sharedStorageClass": rwxStorageClass,
			})

			createErr := APIClient.Create(context.TODO(), detectOnlySBRC)
			Expect(createErr).ToNot(HaveOccurred(),
				"StorageBasedRemediationConfig with detectOnlyMode: Enabled must be admitted by the API server")

			By("Waiting for agent DaemonSet to become ready with detectOnlyMode: Enabled")

			waitForSBRCReady(sbrparams.SBRCDetectOnlyTestName)
		})

		AfterAll(func() {
			if detectOnlySBRC == nil {
				return
			}

			By("Removing detectOnly StorageBasedRemediationConfig")

			if deleteErr := APIClient.Delete(context.TODO(), detectOnlySBRC); deleteErr != nil &&
				!k8serrors.IsNotFound(deleteErr) {
				GinkgoT().Logf("Warning: AfterAll cleanup delete %s failed: %v",
					sbrparams.SBRCDetectOnlyTestName, deleteErr)
			} else {
				Eventually(func() error {
					getErr := APIClient.Get(context.TODO(),
						types.NamespacedName{Name: sbrparams.SBRCDetectOnlyTestName,
							Namespace: medik8sparams.OperatorNs},
						detectOnlySBRC.DeepCopy())

					if k8serrors.IsNotFound(getErr) {
						return nil
					}

					if getErr != nil {
						return getErr
					}

					return fmt.Errorf("SBRC %s still present after delete", sbrparams.SBRCDetectOnlyTestName)
				}, medik8sparams.DefaultTimeout, sbrparams.DefaultPollInterval).Should(Succeed())
			}
		})

		It("Verify SBRC detectOnlyMode field: spec reflection, no remediation CRs, toggle, and DaemonSet GC",
			reportxml.ID("88876"),
			Label(
				labels.OperatorSBR,
				labels.TierAcceptance,
				labels.FrequencyNightly,
				labels.DisruptionNonDestructive,
				labels.PlatformAny,
				labels.ComponentController,
			), func() {
				By("Fetching the StorageBasedRemediationConfig from the cluster")

				liveSBRC := &unstructured.Unstructured{}
				liveSBRC.SetGroupVersionKind(detectOnlySBRC.GroupVersionKind())

				getErr := APIClient.Get(context.TODO(),
					types.NamespacedName{Name: sbrparams.SBRCDetectOnlyTestName, Namespace: medik8sparams.OperatorNs},
					liveSBRC)
				Expect(getErr).ToNot(HaveOccurred(),
					"StorageBasedRemediationConfig %q must exist on the cluster", sbrparams.SBRCDetectOnlyTestName)

				mode, found, nestedErr := unstructured.NestedString(liveSBRC.Object, "spec", "detectOnlyMode")
				Expect(nestedErr).ToNot(HaveOccurred(),
					"detectOnlyMode field must be readable from StorageBasedRemediationConfig spec")
				Expect(found).To(BeTrue(),
					"detectOnlyMode must be present in StorageBasedRemediationConfig spec")
				Expect(mode).To(Equal("Enabled"),
					"detectOnlyMode must be Enabled in the StorageBasedRemediationConfig spec")

				By("Confirming no new StorageBasedRemediation CRs are auto-created while detectOnlyMode is Enabled")

				listGVK := schema.GroupVersionKind{
					Group:   sbrparams.CRDGroup,
					Version: sbrparams.CRDVersion,
					Kind:    "StorageBasedRemediationList",
				}

				sbrList := &unstructured.UnstructuredList{}
				sbrList.SetGroupVersionKind(listGVK)

				Consistently(func() error {
					listErr := APIClient.List(context.TODO(), sbrList,
						&client.ListOptions{Namespace: medik8sparams.OperatorNs})
					if listErr != nil {
						return fmt.Errorf("failed to list StorageBasedRemediation CRs: %w", listErr)
					}

					var newNames []string

					for idx := range sbrList.Items {
						name := sbrList.Items[idx].GetName()
						if !existingSBRCRNames[name] {
							newNames = append(newNames, name)
						}
					}

					if len(newNames) > 0 {
						return fmt.Errorf(
							"new StorageBasedRemediation CR(s) appeared while detectOnlyMode is Enabled: %v",
							newNames)
					}

					return nil
				}, sbrparams.NoNewDaemonSetCheckDuration, sbrparams.NoNewDaemonSetCheckInterval).Should(Succeed(),
					"No new StorageBasedRemediation CRs must be auto-created while detectOnlyMode is Enabled")

				By("Patching StorageBasedRemediationConfig to detectOnlyMode: Disabled")

				patchTarget := &unstructured.Unstructured{}
				patchTarget.SetGroupVersionKind(detectOnlySBRC.GroupVersionKind())
				patchTarget.SetName(sbrparams.SBRCDetectOnlyTestName)
				patchTarget.SetNamespace(medik8sparams.OperatorNs)

				patchJSON := []byte(`{"spec":{"detectOnlyMode":"Disabled"}}`)

				patchErr := APIClient.Patch(context.TODO(), patchTarget,
					client.RawPatch(types.MergePatchType, patchJSON))
				Expect(patchErr).ToNot(HaveOccurred(),
					"Patching StorageBasedRemediationConfig detectOnlyMode to Disabled must succeed")

				By("Verifying detectOnlyMode is now Disabled in the StorageBasedRemediationConfig spec")

				Eventually(func() error {
					freshSBRC := &unstructured.Unstructured{}
					freshSBRC.SetGroupVersionKind(detectOnlySBRC.GroupVersionKind())

					getErr := APIClient.Get(context.TODO(),
						types.NamespacedName{Name: sbrparams.SBRCDetectOnlyTestName, Namespace: medik8sparams.OperatorNs},
						freshSBRC)
					if getErr != nil {
						return fmt.Errorf("failed to fetch StorageBasedRemediationConfig: %w", getErr)
					}

					mode, found, nestedErr := unstructured.NestedString(freshSBRC.Object, "spec", "detectOnlyMode")
					if nestedErr != nil {
						return fmt.Errorf("reading spec.detectOnlyMode: %w", nestedErr)
					}

					if !found {
						return fmt.Errorf("spec.detectOnlyMode field absent from StorageBasedRemediationConfig")
					}

					if mode != "Disabled" {
						return fmt.Errorf("detectOnlyMode is %q, want Disabled", mode)
					}

					return nil
				}, medik8sparams.DefaultTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
					"StorageBasedRemediationConfig detectOnlyMode must reflect Disabled after patch")

				By("Verifying agent DaemonSet remains ready after toggling detectOnlyMode to Disabled")

				waitForSBRCReady(sbrparams.SBRCDetectOnlyTestName)

				By("Deleting the StorageBasedRemediationConfig")

				liveRef := detectOnlySBRC.DeepCopy()

				deleteErr := APIClient.Delete(context.TODO(), liveRef)
				Expect(deleteErr).ToNot(HaveOccurred(),
					"StorageBasedRemediationConfig %q must be deletable", sbrparams.SBRCDetectOnlyTestName)

				// Nil out so AfterAll does not attempt a second delete.
				detectOnlySBRC = nil

				By("Verifying agent DaemonSet is garbage-collected after StorageBasedRemediationConfig deletion")

				dsName := sbrparams.SBRAgentDaemonSetPrefix + sbrparams.SBRCDetectOnlyTestName

				Eventually(func() error {
					_, getErr := APIClient.DaemonSets(medik8sparams.OperatorNs).Get(
						context.TODO(), dsName, metav1.GetOptions{})

					if k8serrors.IsNotFound(getErr) {
						return nil
					}

					if getErr != nil {
						return fmt.Errorf("unexpected error checking DaemonSet %s: %w", dsName, getErr)
					}

					return fmt.Errorf("DaemonSet %q still exists after StorageBasedRemediationConfig deletion",
						dsName)
				}, sbrparams.SBRCDaemonSetGCTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
					"Agent DaemonSet %q must be garbage-collected after StorageBasedRemediationConfig deletion",
					dsName)
			})
	})
