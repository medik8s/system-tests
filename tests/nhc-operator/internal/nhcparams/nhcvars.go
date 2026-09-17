package nhcparams

import (
	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
	"github.com/openshift-kni/k8sreporter"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
)

var (
	// Labels represents the range of labels that can be used for test cases selection.
	Labels = []string{medik8sparams.Label, Label}

	// OperatorDeploymentName represents NHC deployment name.
	OperatorDeploymentName = "node-healthcheck-controller-manager"

	// OperatorControllerPodLabelSelector selects NHC controller-manager pods.
	OperatorControllerPodLabelSelector = "app.kubernetes.io/component=controller-manager," +
		"app.kubernetes.io/name=node-healthcheck-operator"

	// ReporterNamespacesToDump tells the reporter from where to collect logs.
	ReporterNamespacesToDump = map[string]string{
		medik8sparams.OperatorNs: medik8sparams.OperatorNs,
	}

	operatorNs = medik8sparams.OperatorNs

	// ReporterCRDsToDump tells the reporter what CRs to dump.
	ReporterCRDsToDump = []k8sreporter.CRData{
		{Cr: &corev1.PodList{}},
		{Cr: &coordinationv1.LeaseList{}, Namespace: &operatorNs},
		{Cr: medik8sparams.NewUnstructuredList(CRDGroup, CRDVersion, "NodeHealthCheckList")},
	}

	// RequiredAnnotations defines the required annotations and expected values for NHC CSV.
	RequiredAnnotations = map[string]string{
		"features.operators.openshift.io/tls-profiles":     "false",
		"features.operators.openshift.io/disconnected":     "true",
		"features.operators.openshift.io/fips-compliant":   "true",
		"features.operators.openshift.io/proxy-aware":      "false",
		"features.operators.openshift.io/cnf":              "false",
		"features.operators.openshift.io/cni":              "false",
		"features.operators.openshift.io/csi":              "false",
		"features.operators.openshift.io/token-auth-aws":   "false",
		"features.operators.openshift.io/token-auth-azure": "false",
		"features.operators.openshift.io/token-auth-gcp":   "false",
		"operatorframework.io/suggested-namespace":         medik8sparams.OperatorNs,
	}
)
