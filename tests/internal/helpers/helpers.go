package helpers

import (
	"fmt"
	"strings"

	oplmV1alpha1 "github.com/rh-ecosystem-edge/eco-goinfra/pkg/schemes/olm/operators/v1alpha1"

	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/olm"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/pod"

	corev1 "k8s.io/api/core/v1"
)

// FilterRunningPods returns only pods that are Running, not terminating, and have all
// containers ready. Pods with mismatched container status counts are excluded.
//
// This uses the strictest readiness definition (from NMO). Suites that previously
// used looser variants (FAR, NHC, MDR: no status count check; SBR: non-empty only)
// now inherit this stricter check. Callers should either wrap in Eventually/polling
// or ensure pods are verified ready beforehand (e.g., via WaitForAllPodsInNamespaceRunning).
func FilterRunningPods(pods []*pod.Builder) []*pod.Builder {
	var running []*pod.Builder

	for _, candidate := range pods {
		if candidate.Object.Status.Phase != corev1.PodRunning || candidate.Object.DeletionTimestamp != nil {
			continue
		}

		if len(candidate.Object.Status.ContainerStatuses) == 0 ||
			len(candidate.Object.Status.ContainerStatuses) != len(candidate.Object.Spec.Containers) {
			continue
		}

		allReady := true

		for _, cs := range candidate.Object.Status.ContainerStatuses {
			if !cs.Ready {
				allReady = false

				break
			}
		}

		if allReady {
			running = append(running, candidate)
		}
	}

	return running
}

// FilterPodsByDeployment returns only pods owned by a ReplicaSet whose name starts
// with the given deployment name prefix.
func FilterPodsByDeployment(pods []*pod.Builder, deploymentName string) []*pod.Builder {
	prefix := deploymentName + "-"

	var owned []*pod.Builder

	for _, candidate := range pods {
		for _, ref := range candidate.Object.OwnerReferences {
			if ref.Kind == "ReplicaSet" && strings.HasPrefix(ref.Name, prefix) {
				owned = append(owned, candidate)

				break
			}
		}
	}

	return owned
}

// ValidateNonRootSecurityContext checks that all pods run as non-root with restricted
// security contexts. It validates: pod-level RunAsNonRoot, per-container RunAsUser,
// AllowPrivilegeEscalation, Capabilities.Drop ALL, SeccompProfile RuntimeDefault,
// and optionally ReadOnlyRootFilesystem (SBR skips this check).
// Returns a slice of error messages; empty means all checks passed.
func ValidateNonRootSecurityContext(
	pods []*pod.Builder,
	containerName string,
	checkReadOnlyRootFS bool,
) []string {
	var errorMessages []string

	for _, targetPod := range pods {
		podName := targetPod.Object.Name

		errorMessages = append(errorMessages,
			validatePodLevelSecurity(targetPod, podName)...)
		errorMessages = append(errorMessages,
			validateContainerSecurity(targetPod, containerName, podName, checkReadOnlyRootFS)...)
	}

	return errorMessages
}

func validatePodLevelSecurity(targetPod *pod.Builder, podName string) []string {
	podSC := targetPod.Object.Spec.SecurityContext

	switch {
	case podSC == nil:
		return []string{fmt.Sprintf("Pod %s has nil SecurityContext", podName)}
	case podSC.RunAsNonRoot == nil:
		return []string{fmt.Sprintf("Pod %s has nil runAsNonRoot", podName)}
	case !*podSC.RunAsNonRoot:
		return []string{fmt.Sprintf("Incorrect runAsNonRoot for pod %s. Expected true, found: %v",
			podName, *podSC.RunAsNonRoot)}
	default:
		return nil
	}
}

func validateContainerSecurity(
	targetPod *pod.Builder,
	containerName, podName string,
	checkReadOnlyRootFS bool,
) []string {
	var errorMessages []string

	managerFound := false

	for _, container := range targetPod.Object.Spec.Containers {
		if container.Name != containerName {
			continue
		}

		managerFound = true
		containerSC := container.SecurityContext

		if containerSC == nil {
			return []string{fmt.Sprintf("Container %s in pod %s has nil SecurityContext",
				container.Name, podName)}
		}

		if containerSC.RunAsUser != nil && *containerSC.RunAsUser == 0 {
			errorMessages = append(errorMessages,
				fmt.Sprintf("Container %s in pod %s runs as root (UID 0)",
					container.Name, podName))
		}

		if containerSC.RunAsNonRoot != nil && !*containerSC.RunAsNonRoot {
			errorMessages = append(errorMessages,
				fmt.Sprintf("Container %s in pod %s explicitly sets runAsNonRoot=false",
					container.Name, podName))
		}

		if containerSC.AllowPrivilegeEscalation == nil || *containerSC.AllowPrivilegeEscalation {
			errorMessages = append(errorMessages,
				fmt.Sprintf("Container %s in pod %s: AllowPrivilegeEscalation must be explicitly false",
					container.Name, podName))
		}

		errorMessages = append(errorMessages,
			validateCapabilities(containerSC, container.Name, podName)...)

		if checkReadOnlyRootFS &&
			(containerSC.ReadOnlyRootFilesystem == nil || !*containerSC.ReadOnlyRootFilesystem) {
			errorMessages = append(errorMessages,
				fmt.Sprintf("Container %s in pod %s: ReadOnlyRootFilesystem must be explicitly true",
					container.Name, podName))
		}

		if !hasRuntimeDefaultSeccomp(containerSC, targetPod.Object.Spec.SecurityContext) {
			errorMessages = append(errorMessages,
				fmt.Sprintf("Container %s in pod %s missing RuntimeDefault seccomp profile",
					container.Name, podName))
		}
	}

	if !managerFound {
		errorMessages = append(errorMessages,
			fmt.Sprintf("Pod %s has no container named %q", podName, containerName))
	}

	return errorMessages
}

func validateCapabilities(secCtx *corev1.SecurityContext, containerName, podName string) []string {
	if secCtx.Capabilities == nil {
		return []string{fmt.Sprintf("Container %s in pod %s: Capabilities block is nil, must drop ALL",
			containerName, podName)}
	}

	for _, cap := range secCtx.Capabilities.Drop {
		if cap == "ALL" {
			return nil
		}
	}

	return []string{fmt.Sprintf("Container %s in pod %s does not drop ALL capabilities",
		containerName, podName)}
}

func hasRuntimeDefaultSeccomp(containerSC *corev1.SecurityContext, podSC *corev1.PodSecurityContext) bool {
	if containerSC.SeccompProfile != nil &&
		containerSC.SeccompProfile.Type == corev1.SeccompProfileTypeRuntimeDefault {
		return true
	}

	return podSC != nil &&
		podSC.SeccompProfile != nil &&
		podSC.SeccompProfile.Type == corev1.SeccompProfileTypeRuntimeDefault
}

// FindActiveCSV returns the first ClusterServiceVersion in Succeeded phase, or nil.
func FindActiveCSV(csvs []*olm.ClusterServiceVersionBuilder) *olm.ClusterServiceVersionBuilder {
	for _, csv := range csvs {
		phase, err := csv.GetPhase()
		if err == nil && phase == oplmV1alpha1.CSVPhaseSucceeded {
			return csv
		}
	}

	return nil
}
