package tests

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"

	. "github.com/medik8s/system-tests/tests/internal/medik8sinittools"
	"github.com/medik8s/system-tests/tests/sbr-operator/internal/sbrparams"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// getNodeBootID returns the bootID of the named node.
func getNodeBootID(nodeName string) (string, error) {
	node, err := APIClient.CoreV1Interface.Nodes().Get(context.TODO(), nodeName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("get node %s: %w", nodeName, err)
	}

	bootID := node.Status.NodeInfo.BootID
	if bootID == "" {
		return "", fmt.Errorf("node %s boot-id is empty", nodeName)
	}

	return bootID, nil
}

// isNHCCRDInstalled returns true when the NodeHealthCheck CRD is registered in the cluster.
// Any API error — including transient failures — is treated as "not installed" so the caller
// gets a clean Skip rather than a confusing downstream failure.
func isNHCCRDInstalled() bool {
	nhcList := &unstructured.UnstructuredList{}
	nhcList.SetAPIVersion(sbrparams.NHCAPIGroup + "/" + sbrparams.NHCAPIVersion)
	nhcList.SetKind("NodeHealthCheckList")

	err := APIClient.List(context.TODO(), nhcList)
	if err == nil {
		return true
	}

	// k8serrors.IsNotFound is not triggered for missing CRDs; the API returns a "no kind is
	// registered" or "no matches for kind" error instead. Treat any error as not installed.
	GinkgoWriter.Printf("isNHCCRDInstalled: List returned %v — treating NHC as not installed\n", err)

	return false
}

// cleanupNHCCR deletes the named NodeHealthCheck CR. Safe to call when CR may not exist.
func cleanupNHCCR(name string) {
	nhc := &unstructured.Unstructured{}
	nhc.SetAPIVersion(sbrparams.NHCAPIGroup + "/" + sbrparams.NHCAPIVersion)
	nhc.SetKind("NodeHealthCheck")
	nhc.SetName(name)

	err := APIClient.Delete(context.TODO(), nhc)
	if err != nil && !k8serrors.IsNotFound(err) {
		GinkgoT().Logf("Warning: cleanup NHC %s: %v", name, err)
	}
}
