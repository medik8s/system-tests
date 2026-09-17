package helpers

import (
	"context"
	"fmt"
	"strings"

	configv1 "github.com/openshift/api/config/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// DetectPlatform returns the cluster platform type and AWS region (if applicable).
func DetectPlatform(ctx context.Context, k8sClient client.Client) (configv1.PlatformType, string, error) {
	infra := &configv1.Infrastructure{}
	if err := k8sClient.Get(ctx, client.ObjectKey{Name: "cluster"}, infra); err != nil {
		return "", "", fmt.Errorf("failed to get Infrastructure/cluster: %w", err)
	}

	if infra.Status.PlatformStatus == nil {
		return "", "", fmt.Errorf("Infrastructure.status.platformStatus is nil")
	}

	platform := infra.Status.PlatformStatus.Type

	var region string
	if platform == configv1.AWSPlatformType && infra.Status.PlatformStatus.AWS != nil {
		region = infra.Status.PlatformStatus.AWS.Region
	}

	return platform, region, nil
}

// ExtractAWSInstanceID parses the EC2 instance ID from a node's spec.providerID.
// Provider ID format: aws:///us-east-1a/i-0abc123def456
func ExtractAWSInstanceID(node *corev1.Node) (string, error) {
	providerID := node.Spec.ProviderID
	if providerID == "" {
		return "", fmt.Errorf("node %s has no providerID", node.Name)
	}

	if !strings.HasPrefix(providerID, "aws://") {
		return "", fmt.Errorf("node %s providerID is not AWS: %s", node.Name, providerID)
	}

	parts := strings.Split(providerID, "/")
	instanceID := parts[len(parts)-1]

	if instanceID == "" || !strings.HasPrefix(instanceID, "i-") {
		return "", fmt.Errorf("failed to parse instance ID from providerID: %s", providerID)
	}

	return instanceID, nil
}
