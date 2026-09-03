package tests

import (
	"context"
	"fmt"

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
	"k8s.io/apimachinery/pkg/types"
)

// stalePVCName returns the PVC name that testRWXSupport would create for the given SBRC name.
func stalePVCName(sbrcName string) string {
	return sbrcName + "-rwx-test"
}

// stuckReleasedPVsForSC returns PVs stuck in Released state with Retain policy
// for the given StorageClass. A PV in Released with Delete policy is expected
// to be cleaned up by Kubernetes and is not considered "stuck".
func stuckReleasedPVsForSC(scName string) ([]string, error) {
	pvList, err := APIClient.CoreV1Interface.PersistentVolumes().List(
		context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	var stuck []string

	for i := range pvList.Items {
		pv := &pvList.Items[i]
		if pv.Spec.StorageClassName != scName {
			continue
		}

		if pv.Status.Phase == corev1.VolumeReleased {
			claimRef := "none"
			if pv.Spec.ClaimRef != nil {
				claimRef = fmt.Sprintf("%s/%s", pv.Spec.ClaimRef.Namespace, pv.Spec.ClaimRef.Name)
			}

			GinkgoWriter.Printf("PV %s: phase=%s reclaimPolicy=%s claimRef=%s\n",
				pv.Name, pv.Status.Phase, pv.Spec.PersistentVolumeReclaimPolicy, claimRef)

			if pv.Spec.PersistentVolumeReclaimPolicy == corev1.PersistentVolumeReclaimRetain {
				stuck = append(stuck, pv.Name)
			}
		}
	}

	return stuck, nil
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

			sc, scErr := APIClient.StorageV1Interface.StorageClasses().Get(
				context.TODO(), sbrparams.UnknownProvSCName, metav1.GetOptions{})
			if k8serrors.IsNotFound(scErr) {
				Skip(fmt.Sprintf("StorageClass %q not found — NFS dynamic provisioner not deployed, "+
					"skipping unknown provisioner tests", sbrparams.UnknownProvSCName))
			}

			Expect(scErr).ToNot(HaveOccurred(),
				"Failed to get StorageClass %q", sbrparams.UnknownProvSCName)

			GinkgoWriter.Printf("StorageClass %q found: provisioner=%s reclaimPolicy=%s\n",
				sc.Name, sc.Provisioner, *sc.ReclaimPolicy)

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

			stuck, listErr := stuckReleasedPVsForSC(sbrparams.UnknownProvSCName)
			Expect(listErr).ToNot(HaveOccurred(), "Failed to list PVs")

			for _, pvName := range stuck {
				GinkgoWriter.Printf("Deleting leftover stuck Released PV %q\n", pvName)
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

			stuck, _ := stuckReleasedPVsForSC(sbrparams.UnknownProvSCName)
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

				By(fmt.Sprintf("Creating SBRC %q pointing to SC %q (unknown provisioner triggers testRWXSupport)",
					sbrparams.UnknownProvSBRCName, sbrparams.UnknownProvSCName))

				sbrc := buildSBRC(sbrparams.UnknownProvSBRCName, map[string]interface{}{
					"sharedStorageClass": sbrparams.UnknownProvSCName,
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
					_, getErr := APIClient.CoreV1Interface.PersistentVolumeClaims(medik8sparams.OperatorNs).Get(
						context.TODO(), testPVCName, metav1.GetOptions{})
					if k8serrors.IsNotFound(getErr) {
						return nil
					}

					if getErr != nil {
						return getErr
					}

					return fmt.Errorf("RWX test PVC %q still present — testRWXSupport cleanup likely did not run",
						testPVCName)
				}, sbrparams.UnknownProvReconcileTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
					"testRWXSupport should delete transient PVC %q after validating RWX access", testPVCName)

				By("Waiting for SBRC agent DaemonSet pods to reach Ready")

				waitForSBRCReady(sbrparams.UnknownProvSBRCName)

				sbrcReconciled = true
			})

		It("Verify shared-storage PV is not left in Released state after SBRC deletion (RHWA-1046 & RHWA-1047)",
			func() {
				if !sbrcReconciled {
					Skip("SBRC did not reconcile in the previous test (RHWA-1017 failed) — PVs were never consumed")
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
					pv, getErr := APIClient.CoreV1Interface.PersistentVolumes().Get(
						context.TODO(), sharedPVName, metav1.GetOptions{})
					if k8serrors.IsNotFound(getErr) {
						GinkgoWriter.Println("Shared-storage PV deleted (reclaimPolicy was patched to Delete) — fix verified")

						return nil
					}

					if getErr != nil {
						return getErr
					}

					if pv.Status.Phase == corev1.VolumeReleased &&
						pv.Spec.PersistentVolumeReclaimPolicy == corev1.PersistentVolumeReclaimRetain {
						return fmt.Errorf("shared-storage PV %q is in Released state with Retain policy — "+
							"reclaimPolicy was NOT patched to Delete (bug present, see PR #62)", sharedPVName)
					}

					GinkgoWriter.Printf("Shared-storage PV %s: phase=%s reclaimPolicy=%s (ok)\n",
						pv.Name, pv.Status.Phase, pv.Spec.PersistentVolumeReclaimPolicy)

					return nil
				}, sbrparams.UnknownProvPVCleanupTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
					"Shared-storage PV should not remain in Released+Retain state after SBRC deletion")
			})
	})
