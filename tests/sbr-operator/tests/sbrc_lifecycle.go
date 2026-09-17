package tests

import (
	"context"
	"encoding/json"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/reportxml"

	"github.com/medik8s/system-tests/tests/internal/labels"
	. "github.com/medik8s/system-tests/tests/internal/medik8sinittools"
	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
	"github.com/medik8s/system-tests/tests/sbr-operator/internal/sbrparams"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	runtimeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// waitForDaemonSetGC polls until the named DaemonSet is gone (GC'd after SBRC deletion).
func waitForDaemonSetGC(dsName string) {
	Eventually(func() error {
		_, getErr := APIClient.DaemonSets(medik8sparams.OperatorNs).Get(
			context.TODO(), dsName, metav1.GetOptions{})
		if k8serrors.IsNotFound(getErr) {
			return nil
		}

		if getErr != nil {
			return getErr
		}

		return fmt.Errorf("DaemonSet %s still present; waiting for GC", dsName)
	}, sbrparams.SBRCDaemonSetGCTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
		"DaemonSet %s must be GC-d after its StorageBasedRemediationConfig is deleted", dsName)
}

var _ = Describe(
	"SBR Functional - StorageBasedRemediationConfig Lifecycle",
	Ordered,
	Label(
		labels.OperatorSBR,
		labels.TierAcceptance,
		labels.FrequencyPresubmit,
		labels.DisruptionNonDestructive,
		labels.PlatformAny,
		labels.ComponentController,
		labels.OperatorSBR,
	), func() {
		var rwxStorageClass string

		BeforeAll(func() {
			By("Discovering RWX-capable StorageClass (required for agent DaemonSet creation)")

			rwxStorageClass = discoverRWXStorageClass()

			By("Cleaning up any leftover lifecycle test SBRCs from prior runs")

			for _, staleName := range []string{sbrparams.SBRCLifecycleTestNameA, sbrparams.SBRCLifecycleTestNameB} {
				staleRef := buildSBRC(staleName, map[string]interface{}{})

				deleteErr := APIClient.Delete(context.TODO(), staleRef)
				if deleteErr != nil && !k8serrors.IsNotFound(deleteErr) {
					GinkgoT().Logf("Warning: pre-test cleanup of stale SBRC %s failed: %v", staleName, deleteErr)
				}
			}

			By("Waiting for stale lifecycle SBRCs to be fully deleted (finalizer drain)")

			for _, staleName := range []string{sbrparams.SBRCLifecycleTestNameA, sbrparams.SBRCLifecycleTestNameB} {
				Eventually(func() error {
					getErr := APIClient.Get(context.TODO(),
						types.NamespacedName{Name: staleName, Namespace: medik8sparams.OperatorNs},
						buildSBRC(staleName, map[string]interface{}{}))
					if k8serrors.IsNotFound(getErr) {
						return nil
					}

					if getErr != nil {
						return getErr
					}

					return fmt.Errorf("SBRC %s still terminating", staleName)
				}, medik8sparams.DefaultTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
					"Stale SBRC %s must be fully deleted before the test recreates it", staleName)
			}

			By("Waiting for any stale lifecycle DaemonSets to be garbage-collected")

			staleDS := map[string]bool{
				sbrparams.SBRAgentDaemonSetPrefix + sbrparams.SBRCLifecycleTestNameA: true,
				sbrparams.SBRAgentDaemonSetPrefix + sbrparams.SBRCLifecycleTestNameB: true,
			}

			Eventually(func() error {
				dsList, listErr := APIClient.DaemonSets(medik8sparams.OperatorNs).List(
					context.TODO(), metav1.ListOptions{})
				if listErr != nil {
					return listErr
				}

				for _, daemonSet := range dsList.Items {
					if staleDS[daemonSet.Name] {
						return fmt.Errorf("stale DaemonSet %q still present; waiting for GC", daemonSet.Name)
					}
				}

				return nil
			}, medik8sparams.DefaultTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
				"Stale lifecycle DaemonSets from prior runs must be GC-d before the test begins")
		})

		It("Verify StorageBasedRemediationConfig CR create, patch, multi-instance, and delete lifecycle",
			reportxml.ID("88734"),
			func() {
				sbrcA := buildSBRC(sbrparams.SBRCLifecycleTestNameA, map[string]interface{}{
					"sharedStorageClass": rwxStorageClass,
					"sbrTimeoutSeconds":  int64(sbrparams.SBRCTimeoutSecondsMin),
				})
				sbrcB := buildSBRC(sbrparams.SBRCLifecycleTestNameB, map[string]interface{}{
					"sharedStorageClass": rwxStorageClass,
					"sbrTimeoutSeconds":  int64(sbrparams.SBRCTimeoutSecondsMin),
				})

				DeferCleanup(func() {
					for name, obj := range map[string]runtimeclient.Object{
						sbrparams.SBRCLifecycleTestNameA: sbrcA.DeepCopy(),
						sbrparams.SBRCLifecycleTestNameB: sbrcB.DeepCopy(),
					} {
						By(fmt.Sprintf("DeferCleanup: deleting lifecycle SBRC %s if still present", name))

						_ = Eventually(func() error {
							delErr := APIClient.Delete(context.TODO(), obj)
							if delErr == nil || k8serrors.IsNotFound(delErr) {
								return nil
							}

							return delErr
						}, medik8sparams.DefaultTimeout, sbrparams.DefaultPollInterval).Should(Succeed())
					}
				})

				By("Step 1: Creating StorageBasedRemediationConfig A and waiting for its DaemonSet to be ready")

				Expect(APIClient.Create(context.TODO(), sbrcA)).To(Succeed(),
					"StorageBasedRemediationConfig %q must be created successfully", sbrparams.SBRCLifecycleTestNameA)

				waitForSBRCReady(sbrparams.SBRCLifecycleTestNameA)

				By("Step 2: Patching StorageBasedRemediationConfig A (updating sbrTimeoutSeconds) " +
					"and verifying the DaemonSet rolls out")

				patchPayload, marshalErr := json.Marshal(map[string]interface{}{
					"spec": map[string]interface{}{
						"sbrTimeoutSeconds": int64(sbrparams.SBRCTimeoutSecondsMin + 10),
					},
				})
				Expect(marshalErr).ToNot(HaveOccurred(), "Failed to marshal patch payload for SBRC A")

				dsNameA := sbrparams.SBRAgentDaemonSetPrefix + sbrparams.SBRCLifecycleTestNameA

				prePatchDS, prePatchErr := APIClient.DaemonSets(medik8sparams.OperatorNs).Get(
					context.TODO(), dsNameA, metav1.GetOptions{})
				Expect(prePatchErr).ToNot(HaveOccurred(),
					"Failed to get DaemonSet %s before patch", dsNameA)

				prePatchGen := prePatchDS.Generation

				patchErr := APIClient.Patch(
					context.TODO(),
					sbrcA.DeepCopy(),
					runtimeclient.RawPatch(types.MergePatchType, patchPayload),
				)
				Expect(patchErr).ToNot(HaveOccurred(),
					"Failed to patch StorageBasedRemediationConfig %q", sbrparams.SBRCLifecycleTestNameA)

				Eventually(func() error {
					agentDS, getErr := APIClient.DaemonSets(medik8sparams.OperatorNs).Get(
						context.TODO(), dsNameA, metav1.GetOptions{})
					if getErr != nil {
						return fmt.Errorf("DaemonSet %s not found: %w", dsNameA, getErr)
					}

					if agentDS.Generation <= prePatchGen {
						return fmt.Errorf("DaemonSet %s generation not advanced (current: %d, pre-patch: %d)",
							dsNameA, agentDS.Generation, prePatchGen)
					}

					if agentDS.Status.ObservedGeneration < agentDS.Generation {
						return fmt.Errorf("DaemonSet %s not yet reconciled (observed: %d, current: %d)",
							dsNameA, agentDS.Status.ObservedGeneration, agentDS.Generation)
					}

					if agentDS.Status.NumberReady == 0 {
						return fmt.Errorf("DaemonSet %s: 0/%d pods ready after rollout",
							dsNameA, agentDS.Status.DesiredNumberScheduled)
					}

					return nil
				}, sbrparams.SBRCLifecyclePatchedTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
					"DaemonSet %s must complete rollout after SBRC A is patched", dsNameA)

				// Step 3 & 4: Delete SBRC-A before creating SBRC-B. The operator
				// holds /dev/watchdog exclusively per agent — two SBRCs with
				// overlapping nodeSelectors cause the second DaemonSet's pods to
				// crash-loop (EBUSY on watchdog open). Run them sequentially until
				// the operator supports concurrent SBRCs on the same nodes.
				By("Step 3: Deleting StorageBasedRemediationConfig A and verifying its DaemonSet is removed")

				Expect(APIClient.Delete(context.TODO(), sbrcA.DeepCopy())).To(Succeed(),
					"StorageBasedRemediationConfig %q must be deleted successfully", sbrparams.SBRCLifecycleTestNameA)

				waitForDaemonSetGC(dsNameA)

				By("Step 4: Creating StorageBasedRemediationConfig B and verifying its DaemonSet becomes ready")

				dsNameB := sbrparams.SBRAgentDaemonSetPrefix + sbrparams.SBRCLifecycleTestNameB

				Expect(APIClient.Create(context.TODO(), sbrcB)).To(Succeed(),
					"StorageBasedRemediationConfig %q must be created successfully", sbrparams.SBRCLifecycleTestNameB)

				waitForSBRCReady(sbrparams.SBRCLifecycleTestNameB)

				By("Step 5: Deleting StorageBasedRemediationConfig B and verifying its DaemonSet is removed")

				Expect(APIClient.Delete(context.TODO(), sbrcB.DeepCopy())).To(Succeed(),
					"StorageBasedRemediationConfig %q must be deleted successfully", sbrparams.SBRCLifecycleTestNameB)

				waitForDaemonSetGC(dsNameB)
			})
	})
