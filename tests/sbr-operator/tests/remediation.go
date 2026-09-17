package tests

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/pod"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/reportxml"

	"github.com/medik8s/system-tests/tests/internal/labels"
	. "github.com/medik8s/system-tests/tests/internal/medik8sinittools"
	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
	"github.com/medik8s/system-tests/tests/sbr-operator/internal/sbrparams"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

// buildSBR returns an unstructured StorageBasedRemediation CR named after nodeName.
// The SBR operator identifies the target node by metadata.name; spec is intentionally empty.
func buildSBR(nodeName string) *unstructured.Unstructured {
	return buildSBRUnstructured("StorageBasedRemediation", nodeName, map[string]interface{}{})
}

// pullSBRCR fetches the named StorageBasedRemediation CR from the cluster.
func pullSBRCR(nodeName string) (*unstructured.Unstructured, error) {
	sbrObject := &unstructured.Unstructured{}
	sbrObject.SetAPIVersion(sbrparams.CRDGroup + "/" + sbrparams.CRDVersion)
	sbrObject.SetKind("StorageBasedRemediation")

	err := APIClient.Get(context.TODO(),
		types.NamespacedName{Name: nodeName, Namespace: medik8sparams.OperatorNs}, sbrObject)
	if err != nil {
		return nil, err
	}

	return sbrObject, nil
}

// cleanupSBRCR force-removes a StorageBasedRemediation CR by clearing finalizers first.
// Safe to call when the CR may already be gone.
func cleanupSBRCR(nodeName string) {
	sbrObject, err := pullSBRCR(nodeName)

	if k8serrors.IsNotFound(err) {
		return
	}

	if err != nil {
		GinkgoT().Logf("Warning: cleanup get StorageBasedRemediation/%s: %v", nodeName, err)

		return
	}

	if len(sbrObject.GetFinalizers()) > 0 {
		sbrObject.SetFinalizers(nil)

		if updateErr := APIClient.Update(context.TODO(), sbrObject); updateErr != nil &&
			!k8serrors.IsNotFound(updateErr) {
			GinkgoT().Logf("Warning: cleanup clear finalizers on StorageBasedRemediation/%s: %v",
				nodeName, updateErr)
		}
	}

	if deleteErr := APIClient.Delete(context.TODO(), sbrObject); deleteErr != nil &&
		!k8serrors.IsNotFound(deleteErr) {
		GinkgoT().Logf("Warning: cleanup delete StorageBasedRemediation/%s: %v", nodeName, deleteErr)
	}
}

// controllerPodNodes returns the set of node names that currently host SBR controller pods.
// The SBR reconciler skips fencing its own node (the node the controller pod runs on),
// so a CR targeting one of these nodes exercises a different code path.
func controllerPodNodes() map[string]bool {
	nodeSet := make(map[string]bool)

	pods, err := pod.List(APIClient, medik8sparams.OperatorNs,
		metav1.ListOptions{LabelSelector: sbrparams.OperatorControllerPodLabelSelector})
	if err != nil {
		Fail(fmt.Sprintf("failed to list SBR controller pods: %v", err))

		return nodeSet // unreachable — Fail panics
	}

	for _, controllerPod := range pods {
		if controllerPod.Object.Spec.NodeName != "" {
			nodeSet[controllerPod.Object.Spec.NodeName] = true
		}
	}

	return nodeSet
}

var _ = Describe(
	"SBR Functional — StorageBasedRemediation CR",
	Ordered,
	ContinueOnFailure,
	Label(labels.OperatorSBR), func() {
		var (
			targetNodeName string
			// setupSBRC is created in BeforeAll to ensure the agent DaemonSet is running.
			// The SBRRemediationReconciler runs inside agent pods (not the main operator),
			// so without an active SBRC the finalizer on StorageBasedRemediation CRs is never
			// added. An SBRC with sharedStorageClass is required: the controller only creates
			// the DaemonSet after the storage init job completes successfully.
			setupSBRC *unstructured.Unstructured
		)

		BeforeAll(func() {
			By("Pre-cleanup: removing any stale SBRC from prior runs")

			staleRef := buildSBRC(sbrparams.SBRCFunctionalTestName, map[string]interface{}{})
			if deleteErr := APIClient.Delete(context.TODO(), staleRef); deleteErr != nil &&
				!k8serrors.IsNotFound(deleteErr) {
				GinkgoT().Logf("Warning: pre-cleanup delete %s: %v", sbrparams.SBRCFunctionalTestName, deleteErr)
			}

			Eventually(func() error {
				getErr := APIClient.Get(context.TODO(),
					types.NamespacedName{Name: sbrparams.SBRCFunctionalTestName, Namespace: medik8sparams.OperatorNs},
					buildSBRC(sbrparams.SBRCFunctionalTestName, map[string]interface{}{}))
				if k8serrors.IsNotFound(getErr) {
					return nil
				}

				if getErr != nil {
					return getErr
				}

				return fmt.Errorf("SBRC %s still terminating", sbrparams.SBRCFunctionalTestName)
			}, sbrparams.SBRCReadyTimeout, sbrparams.DefaultPollInterval).Should(Succeed())

			By("Discovering RWX storage class for the functional SBRC")

			storageClass := discoverRWXStorageClass()
			Expect(storageClass).ToNot(BeEmpty(),
				"Could not discover a RWX storage class; set SBR_STORAGE_CLASS to override")

			// Pre-check: for static provisioners (e.g. NFS in disconnected clusters),
			// fail fast if no PV exists rather than waiting for the full 3-minute
			// waitForSBRCReady timeout. Only triggers for kubernetes.io/no-provisioner
			// (the provisioner used by our manually-created NFS StorageClass). All other
			// provisioners are assumed to create PVs dynamically and skip this check.
			scObj, scGetErr := APIClient.StorageV1Interface.StorageClasses().Get(
				context.TODO(), storageClass, metav1.GetOptions{})
			if scGetErr != nil {
				GinkgoWriter.Printf("Warning: could not fetch StorageClass %q: %v; skipping static PV pre-check\n",
					storageClass, scGetErr)
			} else if scObj.Provisioner == "kubernetes.io/no-provisioner" {
				By(fmt.Sprintf("Verifying an Available or Bound PV exists for static StorageClass %q", storageClass))

				Eventually(func() error {
					pvList, listErr := APIClient.CoreV1Interface.PersistentVolumes().List(
						context.TODO(), metav1.ListOptions{})
					if listErr != nil {
						return listErr
					}

					for i := range pvList.Items {
						pvItem := &pvList.Items[i]
						if pvItem.Spec.StorageClassName == storageClass &&
							(pvItem.Status.Phase == corev1.VolumeAvailable || pvItem.Status.Phase == corev1.VolumeBound) {
							return nil
						}
					}

					return fmt.Errorf("no Available or Bound PV found for StorageClass %q", storageClass)
				}, sbrparams.PVCheckTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
					"a PV for static StorageClass %q must exist before creating the SBRC", storageClass)
			} else {
				GinkgoWriter.Printf("Skipping PV pre-check for provisioner %q; PVs may be created dynamically\n",
					scObj.Provisioner)
			}

			By(fmt.Sprintf("Creating StorageBasedRemediationConfig %q with sharedStorageClass=%q so agent pods run",
				sbrparams.SBRCFunctionalTestName, storageClass))

			setupSBRC = buildSBRC(sbrparams.SBRCFunctionalTestName, map[string]interface{}{
				"sharedStorageClass": storageClass,
			})

			createErr := APIClient.Create(context.TODO(), setupSBRC)
			Expect(createErr).ToNot(HaveOccurred(),
				"StorageBasedRemediationConfig %q must be created before the remediation CR test",
				sbrparams.SBRCFunctionalTestName)

			waitForSBRCReady(sbrparams.SBRCFunctionalTestName)

			// Exclude nodes running SBR controller pods: the reconciler skips fencing its own
			// node (CR name == ownNodeName check), which would leave the CR in a state where
			// no conditions are ever set and the finalizer is never released on its own.
			controllerNodes := controllerPodNodes()

			nodeList, err := APIClient.CoreV1Interface.Nodes().List(context.TODO(), metav1.ListOptions{
				LabelSelector: "node-role.kubernetes.io/worker",
			})
			Expect(err).ToNot(HaveOccurred(), "Failed to list worker nodes")

			for nodeIdx := range nodeList.Items {
				node := &nodeList.Items[nodeIdx]
				if controllerNodes[node.Name] {
					GinkgoWriter.Printf("Skipping node %s (SBR controller pod runs there)\n", node.Name)

					continue
				}

				if isNodeSchedulable(node) {
					targetNodeName = node.Name

					break
				}
			}

			if targetNodeName == "" {
				Skip("No schedulable worker node available that does not host an SBR controller pod; " +
					"skipping StorageBasedRemediation CR lifecycle test")
			}

			GinkgoWriter.Printf("Target node for StorageBasedRemediation CR: %q\n", targetNodeName)
		})

		AfterAll(func() {
			if setupSBRC == nil {
				return
			}

			By("Removing StorageBasedRemediationConfig created for the remediation CR test")

			deleteErr := APIClient.Delete(context.TODO(), setupSBRC)
			if deleteErr != nil && !k8serrors.IsNotFound(deleteErr) {
				GinkgoT().Logf("Warning: cleanup delete StorageBasedRemediationConfig %s: %v",
					sbrparams.SBRCFunctionalTestName, deleteErr)
			}
		})

		It("Verify StorageBasedRemediation CR lifecycle: admission, finalizer, and deletion cleanup",
			reportxml.ID("88737"),
			Label(
				labels.OperatorSBR,
				labels.DisruptionDestructive,
				labels.TierAcceptance,
				labels.PlatformAny,
				labels.ComponentRemediation,
				labels.FrequencyNightly,
			), func() {
				// Register cleanup before creating: cleanupSBRCR is NotFound-safe, so
				// this is a no-op if creation fails, but ensures the CR is removed if
				// creation succeeds and a later assertion panics before any inline cleanup.
				DeferCleanup(func() {
					By("DeferCleanup: removing StorageBasedRemediation CR and restoring node schedulability")

					cleanupSBRCR(targetNodeName)

					// The operator may have cordoned the node as part of CR reconciliation.
					// If it is still cordoned after CR removal (operator race or incomplete uncordon),
					// patch it ourselves so subsequent tests start with a clean node state.
					node, nodeErr := APIClient.CoreV1Interface.Nodes().Get(
						context.TODO(), targetNodeName, metav1.GetOptions{})
					if nodeErr != nil {
						GinkgoWriter.Printf("DeferCleanup: could not get node %s: %v\n", targetNodeName, nodeErr)

						return
					}

					if !node.Spec.Unschedulable {
						return
					}

					GinkgoWriter.Printf(
						"DeferCleanup: node %s still cordoned after SBR CR removal; patching to uncordon\n",
						targetNodeName)

					patch := []byte(`{"spec":{"unschedulable":false}}`)
					if _, patchErr := APIClient.CoreV1Interface.Nodes().Patch(
						context.TODO(), targetNodeName, types.MergePatchType, patch, metav1.PatchOptions{},
					); patchErr != nil {
						GinkgoWriter.Printf("DeferCleanup: failed to uncordon node %s: %v\n", targetNodeName, patchErr)
					}

					// Give the operator one poll cycle to propagate the uncordon before we recheck.
					// Consistently is used as a non-failing, interruptible timer.
					// Do not assert on the recheck result — a stuck cordon is an operator bug and
					// must not block teardown or mask the actual test result.
					Consistently(func() bool { return true },
						sbrparams.DefaultPollInterval, sbrparams.DefaultPollInterval).Should(BeTrue())

					recheckNode, recheckErr := APIClient.CoreV1Interface.Nodes().Get(
						context.TODO(), targetNodeName, metav1.GetOptions{})
					if recheckErr != nil {
						GinkgoWriter.Printf(
							"DeferCleanup: failed to recheck node %s after uncordon patch: %v\n",
							targetNodeName, recheckErr)
					} else if recheckNode.Spec.Unschedulable {
						GinkgoWriter.Printf(
							"DeferCleanup: node %s still cordoned after patch — operator may be re-cordoning; "+
								"leaving for next test to handle\n", targetNodeName)
					}
				})

				By(fmt.Sprintf("Creating StorageBasedRemediation CR targeting node %q", targetNodeName))

				sbrCR := buildSBR(targetNodeName)

				createErr := APIClient.Create(context.TODO(), sbrCR)
				Expect(createErr).ToNot(HaveOccurred(),
					"StorageBasedRemediation CR should be admitted by the API server (spec is intentionally empty)")

				By(fmt.Sprintf(
					"Verifying controller adds finalizer %q to the StorageBasedRemediation CR",
					sbrparams.SBRRemediationFinalizer))

				var liveCR *unstructured.Unstructured

				// Wait for the controller to add any finalizer (proof of reconcile).
				Eventually(func() error {
					sbrCRObj, pullErr := pullSBRCR(targetNodeName)
					if pullErr != nil {
						return pullErr
					}

					if len(sbrCRObj.GetFinalizers()) == 0 {
						return fmt.Errorf(
							"controller has not yet added any finalizer to StorageBasedRemediation/%s",
							targetNodeName)
					}

					liveCR = sbrCRObj

					return nil
				}, medik8sparams.DefaultTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
					"Controller must add a finalizer to StorageBasedRemediation/%s", targetNodeName)

				// Verify the exact finalizer string immediately — fail fast rather than
				// waiting DefaultTimeout when the constant is out of sync with the operator.
				Expect(liveCR.GetFinalizers()).To(ContainElement(sbrparams.SBRRemediationFinalizer),
					"StorageBasedRemediation/%s has unexpected finalizer(s) %v; "+
						"sbrparams.SBRRemediationFinalizer may be out of sync with the operator repo",
					targetNodeName, liveCR.GetFinalizers())

				// Informational only — not asserted. Conditions require an active agent pool.
				// Fresh pull because the controller sets conditions in a second reconcile iteration
				// (after the finalizer-add requeue); liveCR was captured in the first.
				if freshCR, pullErr := pullSBRCR(targetNodeName); pullErr == nil {
					conditions, _, _ := unstructured.NestedSlice(freshCR.Object, "status", "conditions")
					if len(conditions) > 0 {
						GinkgoWriter.Printf("StorageBasedRemediation/%s conditions: %v\n",
							targetNodeName, conditions)
					} else {
						GinkgoWriter.Printf(
							"StorageBasedRemediation/%s: no conditions set "+
								"(node manager likely not available — expected without active DaemonSet)\n",
							targetNodeName)
					}
				}

				By(fmt.Sprintf("Deleting StorageBasedRemediation CR for node %q", targetNodeName))

				deleteErr := APIClient.Delete(context.TODO(), liveCR)
				Expect(deleteErr).ToNot(HaveOccurred(),
					"StorageBasedRemediation CR deletion must succeed")

				By("Verifying controller releases the finalizer and the CR is fully removed")

				Eventually(func() error {
					_, getErr := pullSBRCR(targetNodeName)
					if k8serrors.IsNotFound(getErr) {
						return nil
					}

					if getErr != nil {
						return getErr
					}

					return fmt.Errorf(
						"StorageBasedRemediation/%s still exists; waiting for controller to release finalizer %q",
						targetNodeName, sbrparams.SBRRemediationFinalizer)
				}, medik8sparams.DefaultTimeout, sbrparams.DefaultPollInterval).Should(Succeed(),
					"StorageBasedRemediation/%s must be fully removed after controller releases finalizer %q",
					targetNodeName, sbrparams.SBRRemediationFinalizer)
			})
	})
