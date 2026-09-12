package tests

import (
	"context"
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/medik8s/system-tests/tests/internal/labels"
	. "github.com/medik8s/system-tests/tests/internal/medik8sinittools"
	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
	"github.com/medik8s/system-tests/tests/sbr-operator/internal/sbrparams"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

// stalePVCName returns the PVC name that testRWXSupport would create for the given SBRC name.
func stalePVCName(sbrcName string) string {
	return sbrcName + "-rwx-test"
}

// releasedRetainPVsForSC lists PVs in Released phase with Retain reclaim policy for the given
// StorageClass. PVs in Released with Delete policy are expected to be reclaimed by Kubernetes
// and are not counted — RHWA-1046 is about Retain-backed orphans from testRWXSupport.
func releasedRetainPVsForSC(scName string) ([]string, error) {
	pvList, err := APIClient.CoreV1Interface.PersistentVolumes().List(
		context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	var stuck []string

	for i := range pvList.Items {
		persistentVolume := &pvList.Items[i]
		if persistentVolume.Spec.StorageClassName != scName {
			continue
		}

		if persistentVolume.Status.Phase == corev1.VolumeReleased &&
			persistentVolume.Spec.PersistentVolumeReclaimPolicy == corev1.PersistentVolumeReclaimRetain {
			stuck = append(stuck, persistentVolume.Name)
		}
	}

	return stuck, nil
}

// describeReleasedRetainPVs returns a single-line debug summary for Released+Retain PV names.
func describeReleasedRetainPVs(pvNames []string) string {
	if len(pvNames) == 0 {
		return "none"
	}

	var parts []string

	for _, pvName := range pvNames {
		persistentVolume, getErr := APIClient.CoreV1Interface.PersistentVolumes().Get(
			context.TODO(), pvName, metav1.GetOptions{})
		if getErr != nil {
			parts = append(parts, fmt.Sprintf("%s (get failed: %v)", pvName, getErr))

			continue
		}

		claimRef := "none"
		if persistentVolume.Spec.ClaimRef != nil {
			claimRef = fmt.Sprintf("%s/%s", persistentVolume.Spec.ClaimRef.Namespace, persistentVolume.Spec.ClaimRef.Name)
		}

		parts = append(parts, fmt.Sprintf("%s phase=%s reclaimPolicy=%s claimRef=%s",
			persistentVolume.Name, persistentVolume.Status.Phase, persistentVolume.Spec.PersistentVolumeReclaimPolicy, claimRef))
	}

	return strings.Join(parts, "; ")
}

func rwxTestPVCAbsent(pvcName string) error {
	_, getErr := APIClient.CoreV1Interface.PersistentVolumeClaims(medik8sparams.OperatorNs).Get(
		context.TODO(), pvcName, metav1.GetOptions{})
	if k8serrors.IsNotFound(getErr) {
		return nil
	}

	if getErr != nil {
		return getErr
	}

	return fmt.Errorf("RWX test PVC %q exists in namespace %s", pvcName, medik8sparams.OperatorNs)
}

// logSBRCRWXConditions prints any SBRC status condition whose type mentions RWX (best-effort).
func logSBRCRWXConditions(sbrcName string) {
	obj := buildSBRC(sbrcName, map[string]interface{}{})

	getErr := APIClient.Get(context.TODO(),
		types.NamespacedName{Name: sbrcName, Namespace: medik8sparams.OperatorNs}, obj)
	if getErr != nil {
		GinkgoWriter.Printf("SBRC %q status: could not read conditions: %v\n", sbrcName, getErr)

		return
	}

	conditions, found, nestedErr := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if nestedErr != nil || !found {
		GinkgoWriter.Printf("SBRC %q status: no conditions in status\n", sbrcName)

		return
	}

	for _, raw := range conditions {
		condMap, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}

		condType, _, _ := unstructured.NestedString(condMap, "type")
		if !strings.Contains(strings.ToLower(condType), "rwx") {
			continue
		}

		condStatus, _, _ := unstructured.NestedString(condMap, "status")
		condReason, _, _ := unstructured.NestedString(condMap, "reason")
		condMessage, _, _ := unstructured.NestedString(condMap, "message")
		GinkgoWriter.Printf("SBRC %q condition %s=%s reason=%s message=%s\n",
			sbrcName, condType, condStatus, condReason, condMessage)
	}
}

// waitForUnknownProvStorageReconciled waits until the operator completes the storage-related
// reconciliation for an unknown-provisioner SBRC: SharedStorageReady=True, shared-storage PVC
// bound to nfs-sbr-dynamic, and the transient rwx-test PVC absent. This gate matches the
// RHWA-1017/1046/1047 scope without requiring agent pods Ready (watchdog may be unavailable on VMs).
func waitForUnknownProvStorageReconciled(sbrcName string) {
	sharedPVCName := sbrcName + "-shared-storage"
	testPVCName := stalePVCName(sbrcName)

	Eventually(func() error {
		obj := buildSBRC(sbrcName, map[string]interface{}{})

		getErr := APIClient.Get(context.TODO(),
			types.NamespacedName{Name: sbrcName, Namespace: medik8sparams.OperatorNs}, obj)
		if getErr != nil {
			return fmt.Errorf("SBRC %q: %w", sbrcName, getErr)
		}

		cond := getSBRCRCondition(obj, "SharedStorageReady")
		if cond == nil {
			return fmt.Errorf("SBRC %q: SharedStorageReady condition not yet present", sbrcName)
		}

		if cond["status"] != string(corev1.ConditionTrue) {
			msg, _ := cond["message"].(string)

			return fmt.Errorf("SBRC %q: SharedStorageReady=%v (%s)", sbrcName, cond["status"], msg)
		}

		pvc, pvcErr := APIClient.CoreV1Interface.PersistentVolumeClaims(medik8sparams.OperatorNs).Get(
			context.TODO(), sharedPVCName, metav1.GetOptions{})
		if pvcErr != nil {
			return fmt.Errorf("SBRC %q: shared-storage PVC %q: %w", sbrcName, sharedPVCName, pvcErr)
		}

		if pvc.Status.Phase != corev1.ClaimBound {
			return fmt.Errorf("SBRC %q: shared-storage PVC %q phase=%s (expected Bound)",
				sbrcName, sharedPVCName, pvc.Status.Phase)
		}

		if pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName != sbrparams.UnknownProvSCName {
			storageClass := "<nil>"
			if pvc.Spec.StorageClassName != nil {
				storageClass = *pvc.Spec.StorageClassName
			}

			return fmt.Errorf("SBRC %q: shared-storage PVC %q storageClass=%q (expected %q)",
				sbrcName, sharedPVCName, storageClass, sbrparams.UnknownProvSCName)
		}

		if err := rwxTestPVCAbsent(testPVCName); err != nil {
			return fmt.Errorf("SBRC %q: %w", sbrcName, err)
		}

		GinkgoWriter.Printf("SBRC %q storage reconciliation OK: SharedStorageReady=True, "+
			"PVC %q Bound, rwx-test PVC absent\n", sbrcName, sharedPVCName)

		return nil
	}, sbrparams.UnknownProvReconcileTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
		"SBRC %q storage reconciliation should complete (SharedStorageReady, shared-storage Bound, rwx-test absent)",
		sbrcName)
}

var _ = Describe(
	"SBR Unknown Provisioner Tests",
	Ordered,
	ContinueOnFailure,
	Label(
		labels.OperatorSBR,
		labels.TierAcceptance,
		labels.FrequencyWeekly,
		labels.DisruptionNonDestructive,
		labels.PlatformAny,
		labels.ComponentPostDeploy,
	), func() {
		var sbrcReconciled bool

		BeforeAll(func() {
			By(fmt.Sprintf("Checking StorageClass %q exists",
				sbrparams.UnknownProvSCName))

			storageClass, scErr := APIClient.StorageV1Interface.StorageClasses().Get(
				context.TODO(), sbrparams.UnknownProvSCName, metav1.GetOptions{})
			if k8serrors.IsNotFound(scErr) {
				Skip(fmt.Sprintf("StorageClass %q not found — NFS dynamic provisioner not deployed, "+
					"skipping unknown provisioner tests", sbrparams.UnknownProvSCName))
			}

			Expect(scErr).ToNot(HaveOccurred(),
				"Failed to get StorageClass %q", sbrparams.UnknownProvSCName)

			GinkgoWriter.Printf("StorageClass %q found: provisioner=%s reclaimPolicy=%s\n",
				storageClass.Name, storageClass.Provisioner, *storageClass.ReclaimPolicy)

			By("Cleaning up any stale test SBRC from a prior run")

			staleRef := buildSBRC(sbrparams.UnknownProvSBRCName, map[string]interface{}{})

			deleteErr := APIClient.Delete(context.TODO(), staleRef)
			if deleteErr != nil && !k8serrors.IsNotFound(deleteErr) {
				GinkgoT().Logf("Warning: pre-test cleanup of stale SBRC %s failed: %v",
					sbrparams.UnknownProvSBRCName, deleteErr)
			}

			if deleteErr == nil {
				By("Waiting for stale test SBRC to be fully deleted")

				Eventually(func() bool {
					getErr := APIClient.Get(context.TODO(),
						types.NamespacedName{Name: sbrparams.UnknownProvSBRCName, Namespace: medik8sparams.OperatorNs},
						buildSBRC(sbrparams.UnknownProvSBRCName, map[string]interface{}{}))

					return k8serrors.IsNotFound(getErr)
				}, medik8sparams.DefaultTimeout, sbrparams.DefaultPollInterval).Should(BeTrue(),
					"Stale SBRC %s must be fully deleted", sbrparams.UnknownProvSBRCName)
			}

			By("Cleaning up any stuck Released PVs from a prior run")

			stuck, listErr := releasedRetainPVsForSC(sbrparams.UnknownProvSCName)
			Expect(listErr).ToNot(HaveOccurred(), "Failed to list PVs")

			for _, pvName := range stuck {
				GinkgoWriter.Printf("Deleting leftover Released+Retain PV %q (%s)\n",
					pvName, describeReleasedRetainPVs([]string{pvName}))
				_ = APIClient.CoreV1Interface.PersistentVolumes().Delete(
					context.TODO(), pvName, metav1.DeleteOptions{})
			}
		})

		AfterAll(func() {
			By("Cleaning up test SBRC if still present")

			sbrcRef := buildSBRC(sbrparams.UnknownProvSBRCName, map[string]interface{}{})

			deleteErr := APIClient.Delete(context.TODO(), sbrcRef)
			if deleteErr != nil && !k8serrors.IsNotFound(deleteErr) {
				GinkgoT().Logf("Warning: AfterAll cleanup of SBRC %s failed: %v",
					sbrparams.UnknownProvSBRCName, deleteErr)
			}

			if deleteErr == nil {
				Eventually(func() bool {
					getErr := APIClient.Get(context.TODO(),
						types.NamespacedName{Name: sbrparams.UnknownProvSBRCName, Namespace: medik8sparams.OperatorNs},
						buildSBRC(sbrparams.UnknownProvSBRCName, map[string]interface{}{}))

					return k8serrors.IsNotFound(getErr)
				}, sbrparams.UnknownProvDeletionTimeout, sbrparams.DefaultPollInterval).Should(BeTrue(),
					"Test SBRC %s must be fully deleted in AfterAll", sbrparams.UnknownProvSBRCName)
			}

			By("Cleaning up any leftover stuck Released PVs")

			stuck, _ := releasedRetainPVsForSC(sbrparams.UnknownProvSCName)
			for _, pvName := range stuck {
				_ = APIClient.CoreV1Interface.PersistentVolumes().Delete(
					context.TODO(), pvName, metav1.DeleteOptions{})
			}
		})

		It("Verify operator handles a stale RWX test PVC and reconciles successfully (RHWA-1017)",
			func() {
				testPVCName := stalePVCName(sbrparams.UnknownProvSBRCName)
				fakeSC := sbrparams.UnknownProvStalePVCSC

				By(fmt.Sprintf("Pre-creating stale PVC %q with non-matching SC %q to simulate leftover from crashed operator",
					testPVCName, fakeSC))

				stalePVC := &corev1.PersistentVolumeClaim{
					ObjectMeta: metav1.ObjectMeta{
						Name:      testPVCName,
						Namespace: medik8sparams.OperatorNs,
					},
					Spec: corev1.PersistentVolumeClaimSpec{
						AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
						StorageClassName: &fakeSC,
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceStorage: resource.MustParse("1Gi"),
							},
						},
					},
				}

				_, createErr := APIClient.CoreV1Interface.PersistentVolumeClaims(medik8sparams.OperatorNs).Create(
					context.TODO(), stalePVC, metav1.CreateOptions{})
				Expect(createErr).ToNot(HaveOccurred(),
					"Failed to pre-create stale PVC %q", testPVCName)

				DeferCleanup(func() {
					_ = APIClient.CoreV1Interface.PersistentVolumeClaims(medik8sparams.OperatorNs).Delete(
						context.TODO(), testPVCName, metav1.DeleteOptions{})
				})

				By(fmt.Sprintf("Creating SBRC %q with detectOnlyMode and SC %q (unknown provisioner triggers testRWXSupport)",
					sbrparams.UnknownProvSBRCName, sbrparams.UnknownProvSCName))

				sbrc := buildSBRC(sbrparams.UnknownProvSBRCName, map[string]interface{}{
					"detectOnlyMode":     "Enabled",
					"sharedStorageClass": sbrparams.UnknownProvSCName,
					"nodeSelector": map[string]interface{}{
						"node-role.kubernetes.io/worker": "",
					},
				})
				Expect(APIClient.Create(context.TODO(), sbrc)).To(Succeed(),
					"SBRC %q must be created successfully", sbrparams.UnknownProvSBRCName)

				By(fmt.Sprintf("Verifying stale PVC %q no longer references fake StorageClass %q",
					testPVCName, fakeSC))

				Eventually(func() error {
					pvc, getErr := APIClient.CoreV1Interface.PersistentVolumeClaims(medik8sparams.OperatorNs).Get(
						context.TODO(), testPVCName, metav1.GetOptions{})
					if k8serrors.IsNotFound(getErr) {
						return nil
					}

					if getErr != nil {
						return getErr
					}

					if pvc.Spec.StorageClassName != nil && *pvc.Spec.StorageClassName == fakeSC {
						return fmt.Errorf("stale PVC %q still references fake SC %q — operator likely blocked "+
							"by AlreadyExists on testRWXSupport",
							testPVCName, fakeSC)
					}

					return nil
				}, sbrparams.UnknownProvReconcileTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
					"Operator should handle stale PVC %q before proceeding with testRWXSupport",
					testPVCName)

				By(fmt.Sprintf("Verifying RWX test PVC %q was deleted after testRWXSupport completed",
					testPVCName))

				Eventually(func() error {
					return rwxTestPVCAbsent(testPVCName)
				}, sbrparams.UnknownProvReconcileTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
					"testRWXSupport should delete transient PVC %q after validating RWX access", testPVCName)

				By("Waiting for SBRC storage reconciliation (SharedStorageReady, shared-storage Bound, rwx-test absent)")

				waitForUnknownProvStorageReconciled(sbrparams.UnknownProvSBRCName)

				sbrcReconciled = true
			})

		It("Verify Released PVs from testRWXSupport do not accumulate while SBRC is alive (RHWA-1046)",
			func() {
				if !sbrcReconciled {
					Skip("SBRC did not reconcile in RHWA-1017 test — cannot observe testRWXSupport PV churn")
				}

				scName := sbrparams.UnknownProvSCName

				By(fmt.Sprintf("Waiting for no Released+Retain PVs on StorageClass %q after initial testRWXSupport",
					scName))

				Eventually(func() error {
					stuck, listErr := releasedRetainPVsForSC(scName)
					if listErr != nil {
						return listErr
					}

					if len(stuck) > 0 {
						return fmt.Errorf("RHWA-1046: %d Released+Retain PV(s) on SC %q after testRWXSupport — "+
							"backing PVs should be deleted, not left Released: %s",
							len(stuck), scName, describeReleasedRetainPVs(stuck))
					}

					return nil
				}, sbrparams.UnknownProvReconcileTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
					"Released+Retain PV count for SC %q should return to zero after testRWXSupport", scName)

				By(fmt.Sprintf("Verifying Released+Retain PV count does not accumulate on SC %q "+
					"while SBRC %q remains (RHWA-1046 accumulation regression; a brief count of 1 "+
					"during testRWXSupport cleanup is expected)",
					scName, sbrparams.UnknownProvSBRCName))

				Consistently(func() error {
					stuck, listErr := releasedRetainPVsForSC(scName)
					if listErr != nil {
						return listErr
					}

					// The bug leaves every backing PV in Released+Retain (count grows 1, 2, 3…).
					// With the fix, testRWXSupport may briefly leave one Released PV while the PVC
					// is deleted and the backing PV is reclaimed — count returns to 0. Tolerate that
					// transient; fail only when multiple orphans accumulate.
					if len(stuck) > 1 {
						return fmt.Errorf("RHWA-1046: Released+Retain PV count reached %d on SC %q — "+
							"testRWXSupport likely re-runs every reconcile without deleting backing PVs: %s",
							len(stuck), scName, describeReleasedRetainPVs(stuck))
					}

					return nil
				}, sbrparams.UnknownProvChurnCheckDuration, sbrparams.UnknownProvChurnCheckInterval).Should(Succeed(),
					"Released+Retain PV count should not accumulate while SBRC %q is alive", sbrparams.UnknownProvSBRCName)
			})

		It("Verify testRWXSupport does not recreate the RWX test PVC on every reconcile (RHWA-1047)",
			func() {
				if !sbrcReconciled {
					Skip("SBRC did not reconcile in RHWA-1017 test — cannot observe testRWXSupport PVC churn")
				}

				testPVCName := stalePVCName(sbrparams.UnknownProvSBRCName)

				By(fmt.Sprintf("Recording SBRC status conditions related to RWX verification for %q",
					sbrparams.UnknownProvSBRCName))
				logSBRCRWXConditions(sbrparams.UnknownProvSBRCName)

				By(fmt.Sprintf("Consistently verifying RWX test PVC %q stays absent while SBRC %q remains "+
					"(RHWA-1047 testRWXSupport churn regression)",
					testPVCName, sbrparams.UnknownProvSBRCName))

				Consistently(func() error {
					if err := rwxTestPVCAbsent(testPVCName); err != nil {
						return fmt.Errorf("RHWA-1047: %w — testRWXSupport likely runs on every reconcile "+
							"instead of caching the RWX result", err)
					}

					return nil
				}, sbrparams.UnknownProvChurnCheckDuration, sbrparams.UnknownProvChurnCheckInterval).Should(Succeed(),
					"RWX test PVC %q should not be recreated while SBRC %q is alive", testPVCName,
					sbrparams.UnknownProvSBRCName)
			})

		It("Verify shared-storage PV is cleaned up after SBRC deletion (RHWA-1046)",
			func() {
				if !sbrcReconciled {
					Skip("SBRC did not reconcile in RHWA-1017 test — shared-storage PVC was never created")
				}

				sharedPVCName := sbrparams.UnknownProvSBRCName + "-shared-storage"

				By(fmt.Sprintf("Recording the shared-storage PV name from PVC %q", sharedPVCName))

				pvc, pvcErr := APIClient.CoreV1Interface.PersistentVolumeClaims(medik8sparams.OperatorNs).Get(
					context.TODO(), sharedPVCName, metav1.GetOptions{})
				Expect(pvcErr).ToNot(HaveOccurred(), "Failed to get shared-storage PVC")
				Expect(pvc.Spec.VolumeName).ToNot(BeEmpty(), "Shared-storage PVC has no bound PV")

				sharedPVName := pvc.Spec.VolumeName
				GinkgoWriter.Printf("Shared-storage PV: %s\n", sharedPVName)

				By(fmt.Sprintf("Deleting SBRC %q to trigger handleDeletion PV cleanup",
					sbrparams.UnknownProvSBRCName))

				sbrcRef := buildSBRC(sbrparams.UnknownProvSBRCName, map[string]interface{}{})
				Expect(APIClient.Delete(context.TODO(), sbrcRef)).To(Succeed(),
					"Failed to delete SBRC %q", sbrparams.UnknownProvSBRCName)

				Eventually(func() bool {
					getErr := APIClient.Get(context.TODO(),
						types.NamespacedName{Name: sbrparams.UnknownProvSBRCName, Namespace: medik8sparams.OperatorNs},
						buildSBRC(sbrparams.UnknownProvSBRCName, map[string]interface{}{}))

					return k8serrors.IsNotFound(getErr)
				}, sbrparams.UnknownProvDeletionTimeout, sbrparams.DefaultPollInterval).Should(BeTrue(),
					"SBRC %s must be fully deleted", sbrparams.UnknownProvSBRCName)

				By(fmt.Sprintf("Checking shared-storage PV %q is not stuck in Released+Retain "+
					"(handleDeletion should patch reclaimPolicy to Delete)", sharedPVName))

				Eventually(func() error {
					persistentVolume, getErr := APIClient.CoreV1Interface.PersistentVolumes().Get(
						context.TODO(), sharedPVName, metav1.GetOptions{})
					if k8serrors.IsNotFound(getErr) {
						GinkgoWriter.Printf("Shared-storage PV %q deleted after SBRC removal (reclaimPolicy patched to Delete)\n",
							sharedPVName)

						return nil
					}

					if getErr != nil {
						return getErr
					}

					if persistentVolume.Status.Phase == corev1.VolumeReleased &&
						persistentVolume.Spec.PersistentVolumeReclaimPolicy == corev1.PersistentVolumeReclaimRetain {
						return fmt.Errorf("RHWA-1046: shared-storage PV %q is Released+Retain after SBRC delete — "+
							"handleDeletion should patch reclaimPolicy to Delete: %s",
							sharedPVName, describeReleasedRetainPVs([]string{sharedPVName}))
					}

					GinkgoWriter.Printf("Shared-storage PV %s: phase=%s reclaimPolicy=%s (ok)\n",
						persistentVolume.Name, persistentVolume.Status.Phase, persistentVolume.Spec.PersistentVolumeReclaimPolicy)

					return nil
				}, sbrparams.UnknownProvPVCleanupTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
					"Shared-storage PV should not remain Released+Retain after SBRC deletion")
			})
	})
