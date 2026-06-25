package tests

import (
	"context"
	"fmt"
	"os"
	"strings"

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

// findTransientRWXStorageClass returns a CephFS StorageClass name for the transient storage test.
// Checks the SBR_STORAGE_CLASS env var first; falls back to listing StorageClasses by provisioner.
// Skips the test when no CephFS class is found because the port-based injection rules are
// CephFS-specific and would not block NFS or other storage traffic correctly.
func findTransientRWXStorageClass() string {
	if storageClassName := os.Getenv("SBR_STORAGE_CLASS"); storageClassName != "" {
		obj, getErr := APIClient.StorageV1Interface.StorageClasses().Get(
			context.TODO(), storageClassName, metav1.GetOptions{})
		Expect(getErr).ToNot(HaveOccurred(), "SBR_STORAGE_CLASS=%q not found", storageClassName)

		if !strings.Contains(obj.Provisioner, "cephfs") {
			Skip(fmt.Sprintf("SBR_STORAGE_CLASS=%q uses provisioner %q (not CephFS); "+
				"transient port injection requires a CephFS-backed class", storageClassName, obj.Provisioner))
		}

		return storageClassName
	}

	scList, err := APIClient.StorageV1Interface.StorageClasses().List(context.TODO(), metav1.ListOptions{})
	Expect(err).ToNot(HaveOccurred(), "Failed to list StorageClasses")

	for idx := range scList.Items {
		if strings.Contains(scList.Items[idx].Provisioner, "cephfs") {
			GinkgoWriter.Printf("Auto-discovered CephFS StorageClass: %s\n", scList.Items[idx].Name)

			return scList.Items[idx].Name
		}
	}

	Skip("No CephFS StorageClass found; set SBR_STORAGE_CLASS env var to override")

	return ""
}

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

// sbrCRCount returns the number of StorageBasedRemediation CRs in the cluster.
func transientSBRCRCount() (int, error) {
	sbrList := &unstructured.UnstructuredList{}
	sbrList.SetAPIVersion(sbrparams.CRDGroup + "/" + sbrparams.CRDVersion)
	sbrList.SetKind("StorageBasedRemediationList")

	if err := APIClient.List(context.TODO(), sbrList); err != nil {
		return 0, fmt.Errorf("failed to list StorageBasedRemediation CRs: %w", err)
	}

	return len(sbrList.Items), nil
}

// transientInjectCephFSBlock inserts iptables OUTPUT REJECT rules to cut CephFS write traffic.
// Blocking OUTPUT only is sufficient: the agent cannot write heartbeats, peers detect a stale
// heartbeat and set SBRStorageUnhealthy=True. Uses nsenter --target 1 --net --mount to enter
// the host network and mount namespaces so the host's iptables binary is used.
//
// Calls Skip when iptables returns a non-zero exit code so that CI runs on nodes where the
// OUTPUT chain is not writable do not hard-fail.
func transientInjectCephFSBlock(injectorPod *pod.Builder, nodeName string) {
	By(fmt.Sprintf("Injecting CephFS iptables OUTPUT REJECT rules on node %s", nodeName))

	rejectRules := [][]string{
		{"nsenter", "--target", "1", "--net", "--mount", "--", "iptables", "-I", "OUTPUT",
			"-p", "tcp", "--dport", "3300", "-j", "REJECT"},
		{"nsenter", "--target", "1", "--net", "--mount", "--", "iptables", "-I", "OUTPUT",
			"-p", "tcp", "--dport", "6789", "-j", "REJECT"},
		{"nsenter", "--target", "1", "--net", "--mount", "--", "iptables", "-I", "OUTPUT",
			"-p", "tcp", "--match", "multiport", "--dports", "6800:7300", "-j", "REJECT"},
	}

	for _, rule := range rejectRules {
		if _, execErr := injectorPod.ExecCommand(rule); execErr != nil {
			Skip(fmt.Sprintf("iptables OUTPUT injection not available on node %s (exit code 1): %v; "+
				"skipping transient storage test", nodeName, execErr))
		}
	}
}

// transientRemoveCephFSBlock deletes the iptables OUTPUT rules added by transientInjectCephFSBlock.
// Failures are logged as warnings; cleanup must be best-effort so the node is not left blocked.
func transientRemoveCephFSBlock(injectorPod *pod.Builder) {
	cleanupRules := [][]string{
		{"nsenter", "--target", "1", "--net", "--mount", "--", "iptables", "-D", "OUTPUT",
			"-p", "tcp", "--dport", "3300", "-j", "REJECT"},
		{"nsenter", "--target", "1", "--net", "--mount", "--", "iptables", "-D", "OUTPUT",
			"-p", "tcp", "--dport", "6789", "-j", "REJECT"},
		{"nsenter", "--target", "1", "--net", "--mount", "--", "iptables", "-D", "OUTPUT",
			"-p", "tcp", "--match", "multiport", "--dports", "6800:7300", "-j", "REJECT"},
	}

	for _, rule := range cleanupRules {
		if _, flushErr := injectorPod.ExecCommand(rule); flushErr != nil {
			GinkgoWriter.Printf("Warning: iptables cleanup (cmd %v): %v\n", rule, flushErr)
		}
	}
}

var _ = Describe(
	"SBR Functional — Transient Storage Failure Self-Healing",
	Ordered,
	ContinueOnFailure,
	Label(sbrparams.Label), func() {
		var (
			transientSBRC    *unstructured.Unstructured
			targetNodeName   string
			injectorPod      *pod.Builder
			storageClassName string
		)

		BeforeAll(func() {
			By("Discovering a CephFS StorageClass for the transient storage test")

			storageClassName = findTransientRWXStorageClass()

			GinkgoWriter.Printf("Using StorageClass %q for transient storage test\n", storageClassName)

			By(fmt.Sprintf("Pre-cleaning stale SBRC %q if present", sbrparams.SBRCTransientTestName))

			staleObj := &unstructured.Unstructured{}
			staleObj.SetAPIVersion(sbrparams.CRDGroup + "/" + sbrparams.CRDVersion)
			staleObj.SetKind("StorageBasedRemediationConfig")
			staleObj.SetName(sbrparams.SBRCTransientTestName)
			staleObj.SetNamespace(medik8sparams.OperatorNs)

			if delErr := APIClient.Delete(context.TODO(), staleObj); delErr != nil && !k8serrors.IsNotFound(delErr) {
				GinkgoWriter.Printf("Pre-cleanup: warning deleting stale SBRC %s: %v\n",
					sbrparams.SBRCTransientTestName, delErr)
			} else if delErr == nil {
				Eventually(func() bool {
					chk := &unstructured.Unstructured{}
					chk.SetAPIVersion(sbrparams.CRDGroup + "/" + sbrparams.CRDVersion)
					chk.SetKind("StorageBasedRemediationConfig")

					getErr := APIClient.Get(context.TODO(),
						types.NamespacedName{Name: sbrparams.SBRCTransientTestName,
							Namespace: medik8sparams.OperatorNs}, chk)

					return k8serrors.IsNotFound(getErr)
				}, medik8sparams.DefaultTimeout, sbrparams.DefaultPollInterval).Should(BeTrue(),
					"Stale SBRC %q must be gone before creating a fresh one",
					sbrparams.SBRCTransientTestName)
			}

			By(fmt.Sprintf("Pre-cleaning stale injector pod %q if present", sbrparams.TransientInjectorPodName))

			if existing, pullErr := pod.Pull(
				APIClient, sbrparams.TransientInjectorPodName, medik8sparams.OperatorNs,
			); pullErr == nil {
				if _, delErr := existing.Delete(); delErr != nil {
					GinkgoWriter.Printf("Pre-cleanup: warning deleting stale injector pod %s: %v\n",
						sbrparams.TransientInjectorPodName, delErr)
				}
			}

			By(fmt.Sprintf("Creating StorageBasedRemediationConfig %q with sharedStorageClass %q",
				sbrparams.SBRCTransientTestName, storageClassName))

			transientSBRC = buildSBRC(sbrparams.SBRCTransientTestName, map[string]interface{}{
				"sharedStorageClass": storageClassName,
			})

			createErr := APIClient.Create(context.TODO(), transientSBRC)
			Expect(createErr).ToNot(HaveOccurred(),
				"StorageBasedRemediationConfig %q must be created for the transient storage test",
				sbrparams.SBRCTransientTestName)

			By(fmt.Sprintf("Waiting for SBRC %q agent DaemonSet to be ready",
				sbrparams.SBRCTransientTestName))

			waitForSBRCReady(sbrparams.SBRCTransientTestName)

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
				By(fmt.Sprintf("Removing StorageBasedRemediationConfig %q",
					sbrparams.SBRCTransientTestName))

				if deleteErr := APIClient.Delete(context.TODO(), transientSBRC); deleteErr != nil &&
					!k8serrors.IsNotFound(deleteErr) {
					GinkgoT().Logf("Warning: cleanup delete SBRC %s: %v",
						sbrparams.SBRCTransientTestName, deleteErr)
				} else {
					Eventually(func() error {
						getErr := APIClient.Get(context.TODO(),
							types.NamespacedName{Name: sbrparams.SBRCTransientTestName,
								Namespace: medik8sparams.OperatorNs},
							transientSBRC.DeepCopy())

						if k8serrors.IsNotFound(getErr) {
							return nil
						}

						if getErr != nil {
							return getErr
						}

						return fmt.Errorf("SBRC %s still present", sbrparams.SBRCTransientTestName)
					}, medik8sparams.DefaultTimeout, sbrparams.DefaultPollInterval).Should(Succeed())
				}
			}

			if injectorPod != nil {
				if existing, pullErr := pod.Pull(
					APIClient, sbrparams.TransientInjectorPodName, medik8sparams.OperatorNs,
				); pullErr == nil {
					if _, delErr := existing.Delete(); delErr != nil {
						GinkgoWriter.Printf("AfterAll: failed to delete injector pod: %v\n", delErr)
					} else {
						Eventually(func() error {
							_, err := pod.Pull(APIClient, sbrparams.TransientInjectorPodName,
								medik8sparams.OperatorNs)
							if err != nil {
								return nil // not found = gone
							}

							return fmt.Errorf("injector pod %s still present",
								sbrparams.TransientInjectorPodName)
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
				verifyTransientStorageSelfHealing(&targetNodeName, &injectorPod)
			})
	})

// verifyTransientStorageSelfHealing is the test body extracted to satisfy funlen constraints.
func verifyTransientStorageSelfHealing(targetNodeName *string, injectorPod **pod.Builder) {
	By(fmt.Sprintf("Creating privileged injector pod on node %q", *targetNodeName))

	built, createErr := pod.NewBuilder(
		APIClient, sbrparams.TransientInjectorPodName, medik8sparams.OperatorNs,
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

	// Capture the pod value now; the test body may nil *injectorPod before DeferCleanup fires.
	cleanupPod := built

	DeferCleanup(func() {
		if cleanupPod == nil {
			return
		}

		By("DeferCleanup: removing CephFS iptables OUTPUT REJECT rules")

		transientRemoveCephFSBlock(cleanupPod)
	})

	By("Injecting CephFS port REJECT rules via iptables OUTPUT (nsenter --net)")

	transientInjectCephFSBlock(*injectorPod, *targetNodeName)

	By(fmt.Sprintf("Waiting for node condition %q=True on node %q",
		sbrparams.SBRStorageUnhealthyCondition, *targetNodeName))

	Eventually(func() error {
		return transientNodeHasCondition(*targetNodeName, sbrparams.SBRStorageUnhealthyCondition, true)
	}, sbrparams.StorageInjectionTimeout, sbrparams.StorageInjectionPollInterval).Should(Succeed(),
		"Node %q must report %q=True when CephFS ports are blocked",
		*targetNodeName, sbrparams.SBRStorageUnhealthyCondition)

	By("Asserting no new StorageBasedRemediation CR was created over the injection window (no fencing triggered)")

	baselineCount, baselineErr := transientSBRCRCount()
	Expect(baselineErr).ToNot(HaveOccurred(), "Failed to get baseline SBR CR count")

	Consistently(transientSBRCRCount,
		sbrparams.NoNewDaemonSetCheckDuration, sbrparams.NoNewDaemonSetCheckInterval).Should(
		Equal(baselineCount), "No new StorageBasedRemediation CR must appear while storage is transiently lost")

	By("Removing CephFS iptables OUTPUT REJECT rules to restore storage")

	transientRemoveCephFSBlock(*injectorPod)

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
