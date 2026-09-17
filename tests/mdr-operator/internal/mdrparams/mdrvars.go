package mdrparams

import (
	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
	"github.com/openshift-kni/k8sreporter"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
)

var (
	// Labels represents the range of labels that can be used for test cases selection.
	Labels = []string{medik8sparams.Label, Label}

	// OperatorDeploymentName represents MDR deployment name.
	OperatorDeploymentName = "machine-deletion-remediation-controller-manager"

	// OperatorControllerPodLabelSelector selects MDR controller-manager pods by label.
	OperatorControllerPodLabelSelector = "control-plane=controller-manager"

	// ReporterNamespacesToDump tells the reporter from where to collect logs.
	ReporterNamespacesToDump = map[string]string{
		medik8sparams.OperatorNs: medik8sparams.OperatorNs,
		"openshift-machine-api":  "openshift-machine-api",
	}

	operatorNs = medik8sparams.OperatorNs

	// ReporterCRDsToDump tells the reporter what CRs to dump.
	ReporterCRDsToDump = []k8sreporter.CRData{
		{Cr: &corev1.PodList{}},
		{Cr: medik8sparams.NewUnstructuredList(CRDGroup, CRDVersion, "MachineDeletionRemediationList")},
		{Cr: medik8sparams.NewUnstructuredList(CRDGroup, CRDVersion, "MachineDeletionRemediationTemplateList")},
		{Cr: medik8sparams.NewUnstructuredList(NHCAPIGroup, NHCAPIVersion, "NodeHealthCheckList")},
		{Cr: &coordinationv1.LeaseList{}, Namespace: &operatorNs},
	}

	// RequiredAnnotations defines the required annotations and expected values for MDR CSV.
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
