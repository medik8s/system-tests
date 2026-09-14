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

// transientNodeHasCondition returns nil when the named node condition matches wantTrue; error otherwise.
// A missing condition is treated as False.
func transientNodeHasCondition(nodeName, condType string, wantTrue bool) error {
	node, err := APIClient.CoreV1Interface.Nodes().Get(context.TODO(), nodeName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get node %s: %w", nodeName, err)
	}

	for _, cond := range node.Status.Conditions {
		if string(cond.Type) != condType {
			continue
		}

		gotTrue := cond.Status == corev1.ConditionTrue
		if gotTrue == wantTrue {
			return nil
		}

		return fmt.Errorf("node %s condition %s status=%s; want true=%v",
			nodeName, condType, cond.Status, wantTrue)
	}

	if !wantTrue {
		return nil
	}

	return fmt.Errorf("node %s: condition %s not present", nodeName, condType)
}

// transientSBRCRCount returns the number of StorageBasedRemediation CRs in the cluster.
func transientSBRCRCount() (int, error) {
	sbrList := &unstructured.UnstructuredList{}
	sbrList.SetAPIVersion(sbrparams.CRDGroup + "/" + sbrparams.CRDVersion)
	sbrList.SetKind("StorageBasedRemediationList")

	if err := APIClient.List(context.TODO(), sbrList); err != nil {
		return 0, fmt.Errorf("failed to list StorageBasedRemediation CRs: %w", err)
	}

	return len(sbrList.Items), nil
}

func registerTransientTests(cfg StorageConfig) {
	Describe(
		"SBR Functional — Transient Storage Failure Self-Healing ("+cfg.Name+")",
		Ordered,
		ContinueOnFailure,
		Label(labels.OperatorSBR), func() {
			var (
				transientSBRC    *unstructured.Unstructured
				targetNodeName   string
				injectorPod      *pod.Builder
				storageClassName string
			)

			sbrcName := sbrparams.SBRCTransientTestName + cfg.NameSuffix
			injectorPodName := sbrparams.TransientInjectorPodName + cfg.NameSuffix

			BeforeAll(func() {
				By("Discovering storage class for the transient storage test")

				storageClassName = discoverStorageClass(cfg)

				GinkgoWriter.Printf("Using StorageClass %q for transient storage test\n", storageClassName)

				By(fmt.Sprintf("Pre-cleaning stale SBRC %q if present", sbrcName))

				staleObj := &unstructured.Unstructured{}
				staleObj.SetAPIVersion(sbrparams.CRDGroup + "/" + sbrparams.CRDVersion)
				staleObj.SetKind("StorageBasedRemediationConfig")
				staleObj.SetName(sbrcName)
				staleObj.SetNamespace(medik8sparams.OperatorNs)

				if delErr := APIClient.Delete(context.TODO(), staleObj); delErr != nil && !k8serrors.IsNotFound(delErr) {
					GinkgoWriter.Printf("Pre-cleanup: warning deleting stale SBRC %s: %v\n",
						sbrcName, delErr)
				} else if delErr == nil {
					Eventually(func() bool {
						chk := &unstructured.Unstructured{}
						chk.SetAPIVersion(sbrparams.CRDGroup + "/" + sbrparams.CRDVersion)
						chk.SetKind("StorageBasedRemediationConfig")

						getErr := APIClient.Get(context.TODO(),
							types.NamespacedName{Name: sbrcName, Namespace: medik8sparams.OperatorNs}, chk)

						return k8serrors.IsNotFound(getErr)
					}, medik8sparams.DefaultTimeout, sbrparams.DefaultPollInterval).Should(BeTrue(),
						"Stale SBRC %q must be gone before creating a fresh one", sbrcName)
				}

				By(fmt.Sprintf("Pre-cleaning stale injector pod %q if present", injectorPodName))

				if existing, pullErr := pod.Pull(
					APIClient, injectorPodName, medik8sparams.OperatorNs,
				); pullErr == nil {
					if _, delErr := existing.Delete(); delErr != nil {
						GinkgoWriter.Printf("Pre-cleanup: warning deleting stale injector pod %s: %v\n",
							injectorPodName, delErr)
					}
				}

				By(fmt.Sprintf("Creating StorageBasedRemediationConfig %q with sharedStorageClass %q",
					sbrcName, storageClassName))

				transientSBRC = buildSBRCWithMode(sbrcName, storageClassName, cfg, nil)

				createErr := APIClient.Create(context.TODO(), transientSBRC)
				Expect(createErr).ToNot(HaveOccurred(),
					"StorageBasedRemediationConfig %q must be created for the transient storage test",
					sbrcName)

				By(fmt.Sprintf("Waiting for SBRC %q agent DaemonSet to be ready", sbrcName))

				waitForSBRCReady(sbrcName)

				By("Selecting a schedulable worker node that does not host an SBR controller pod")

				controllerNodes := controllerPodNodes()

				nodeList, nodeListErr := APIClient.CoreV1Interface.Nodes().List(
					context.TODO(),
					metav1.ListOptions{LabelSelector: "node-role.kubernetes.io/worker"})
				Expect(nodeListErr).ToNot(HaveOccurred(), "Failed to list worker nodes")

				for nodeIdx := range nodeList.Items {
					node := &nodeList.Items[nodeIdx]

					if controllerNodes[node.Name] {
						GinkgoWriter.Printf("Skipping node %s (SBR controller runs there)\n", node.Name)

						continue
					}

					if isNodeSchedulable(node) {
						targetNodeName = node.Name

						break
					}
				}

				if targetNodeName == "" {
					Skip("No schedulable worker node found that does not host an SBR controller pod")
				}

				GinkgoWriter.Printf("Target node for transient storage injection: %q\n", targetNodeName)
			})

			AfterAll(func() {
				if transientSBRC != nil {
					By(fmt.Sprintf("Removing StorageBasedRemediationConfig %q", sbrcName))

					if deleteErr := APIClient.Delete(context.TODO(), transientSBRC); deleteErr != nil &&
						!k8serrors.IsNotFound(deleteErr) {
						GinkgoT().Logf("Warning: cleanup delete SBRC %s: %v", sbrcName, deleteErr)
					} else {
						Eventually(func() error {
							getErr := APIClient.Get(context.TODO(),
								types.NamespacedName{Name: sbrcName, Namespace: medik8sparams.OperatorNs},
								transientSBRC.DeepCopy())

							if k8serrors.IsNotFound(getErr) {
								return nil
							}

							if getErr != nil {
								return getErr
							}

							return fmt.Errorf("SBRC %s still present", sbrcName)
						}, medik8sparams.DefaultTimeout, sbrparams.DefaultPollInterval).Should(Succeed())
					}
				}

				if injectorPod != nil {
					if existing, pullErr := pod.Pull(
						APIClient, injectorPodName, medik8sparams.OperatorNs,
					); pullErr == nil {
						if _, delErr := existing.Delete(); delErr != nil {
							GinkgoWriter.Printf("AfterAll: failed to delete injector pod: %v\n", delErr)
						} else {
							Eventually(func() error {
								_, err := pod.Pull(APIClient, injectorPodName, medik8sparams.OperatorNs)
								if err != nil {
									return nil
								}

								return fmt.Errorf("injector pod %s still present", injectorPodName)
							}, medik8sparams.DefaultTimeout, sbrparams.DefaultPollInterval).Should(Succeed())
						}
					}
				}
			})

			It("Verify transient storage failure clears without fencing",
				reportxml.ID("88735"),
				Label(
					labels.OperatorSBR,
					labels.TierAcceptance,
					labels.FrequencyNightly,
					labels.DisruptionDestructive,
					labels.PlatformAny,
					labels.ComponentRemediation,
				), func() {
					verifyTransientStorageSelfHealing(injectorPodName, &targetNodeName, &injectorPod)
				})
		})
}

// verifyTransientStorageSelfHealing is the test body extracted to satisfy funlen constraints.
func verifyTransientStorageSelfHealing(injectorPodName string, targetNodeName *string, injectorPod **pod.Builder) {
	By(fmt.Sprintf("Creating privileged injector pod on node %q", *targetNodeName))

	built, createErr := pod.NewBuilder(
		APIClient, injectorPodName, medik8sparams.OperatorNs,
		sbrparams.WatchdogDebugImage,
	).
		DefineOnNode(*targetNodeName).
		WithHostPid(true).
		WithPrivilegedFlag().
		WithRestartPolicy(corev1.RestartPolicyNever).
		RedefineDefaultCMD([]string{"sleep", "3600"}).
		CreateAndWaitUntilRunning(medik8sparams.DefaultTimeout)

	Expect(createErr).ToNot(HaveOccurred(),
		"Injector pod must start running on node %q", *targetNodeName)

	*injectorPod = built

	cleanupPod := built

	DeferCleanup(func() {
		if cleanupPod == nil {
			return
		}

		By("DeferCleanup: removing Ceph iptables OUTPUT REJECT rules")

		removeCephFSRejectOutput(cleanupPod)
	})

	By("Injecting Ceph port REJECT rules via iptables OUTPUT (nsenter --net)")

	injectCephFSRejectOutput(*injectorPod, *targetNodeName)

	By(fmt.Sprintf("Waiting for node condition %q=True on node %q",
		sbrparams.SBRStorageUnhealthyCondition, *targetNodeName))

	Eventually(func() error {
		return transientNodeHasCondition(*targetNodeName, sbrparams.SBRStorageUnhealthyCondition, true)
	}, sbrparams.StorageInjectionTimeout, sbrparams.StorageInjectionPollInterval).Should(Succeed(),
		"Node %q must report %q=True when Ceph ports are blocked",
		*targetNodeName, sbrparams.SBRStorageUnhealthyCondition)

	By("Asserting no new StorageBasedRemediation CR was created over the injection window (no fencing triggered)")

	baselineCount, baselineErr := transientSBRCRCount()
	Expect(baselineErr).ToNot(HaveOccurred(), "Failed to get baseline SBR CR count")

	Consistently(transientSBRCRCount,
		sbrparams.NoNewDaemonSetCheckDuration, sbrparams.NoNewDaemonSetCheckInterval).Should(
		Equal(baselineCount), "No new StorageBasedRemediation CR must appear while storage is transiently lost")

	By("Removing Ceph iptables OUTPUT REJECT rules to restore storage")

	removeCephFSRejectOutput(*injectorPod)

	By(fmt.Sprintf("Waiting for node condition %q to clear on node %q",
		sbrparams.SBRStorageUnhealthyCondition, *targetNodeName))

	Eventually(func() error {
		return transientNodeHasCondition(*targetNodeName, sbrparams.SBRStorageUnhealthyCondition, false)
	}, sbrparams.StorageInjectionTimeout, sbrparams.StorageInjectionPollInterval).Should(Succeed(),
		"Node %q condition %q must clear after storage is restored",
		*targetNodeName, sbrparams.SBRStorageUnhealthyCondition)

	By("Confirming no StorageBasedRemediation CR was created throughout")

	finalCount, finalCountErr := transientSBRCRCount()
	Expect(finalCountErr).ToNot(HaveOccurred(), "Failed to count StorageBasedRemediation CRs")
	Expect(finalCount).To(BeZero(),
		"No StorageBasedRemediation CR must have been created during the transient storage test")

	By("Deleting injector pod")

	if _, delErr := (*injectorPod).Delete(); delErr != nil {
		GinkgoWriter.Printf("Warning: failed to delete injector pod: %v\n", delErr)
	}

	*injectorPod = nil
}
