package tests

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/clients"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/deployment"

	"github.com/medik8s/system-tests/tests/far-operator/internal/farparams"
	"github.com/medik8s/system-tests/tests/far-operator/internal/farutils"
	"github.com/medik8s/system-tests/tests/internal/helpers"
	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
)

type awsFARPrerequisites struct {
	fenceAgent   string
	leaderNode   string
	sharedParams map[string]interface{}
	nodeParams   map[string]interface{}
}

func setupAWSFARPrerequisites(ctx context.Context, apiClient *clients.Settings) awsFARPrerequisites {
	GinkgoHelper()

	By("Detecting cluster platform")

	platform, region, err := helpers.DetectPlatform(ctx, apiClient)
	Expect(err).ToNot(HaveOccurred())

	if platform != configv1.AWSPlatformType {
		Skip(fmt.Sprintf("FAR tests require AWS, got %s", platform))
	}

	fenceAgent := resolveAndVerifyFAR(ctx, apiClient, platform, region)
	createCredentialsSecret(ctx, apiClient)
	sharedParams, nodeParams := buildFenceParams(ctx, apiClient, region)
	leaderNode := waitForLeaderElection(ctx, apiClient)

	return awsFARPrerequisites{
		fenceAgent:   fenceAgent,
		leaderNode:   leaderNode,
		sharedParams: sharedParams,
		nodeParams:   nodeParams,
	}
}

func resolveAndVerifyFAR(
	ctx context.Context, apiClient *clients.Settings,
	platform configv1.PlatformType, region string,
) string {
	GinkgoHelper()

	By("Resolving fence agent for platform")

	fenceAgent, _, err := farutils.FenceAgentForPlatform(platform)
	Expect(err).ToNot(HaveOccurred())
	GinkgoWriter.Printf("Platform: %s, Agent: %s, Region: %s\n",
		platform, fenceAgent, region)

	By("Verifying FAR deployment is Ready")

	farDeploy, err := deployment.Pull(
		apiClient, farparams.OperatorDeploymentName, medik8sparams.OperatorNs)
	Expect(err).ToNot(HaveOccurred(), "FAR operator not installed")
	Expect(farDeploy.IsReady(medik8sparams.DefaultTimeout)).To(BeTrue(),
		"FAR deployment is not Ready")

	By(fmt.Sprintf("Verifying at least %d Ready worker nodes", farparams.MinWorkersForDestructiveTests))

	workerCount, err := helpers.CountReadyWorkerNodes(ctx, apiClient)
	Expect(err).ToNot(HaveOccurred())

	if workerCount < farparams.MinWorkersForDestructiveTests {
		Skip(fmt.Sprintf("FAR tests require at least %d Ready workers, found %d",
			farparams.MinWorkersForDestructiveTests, workerCount))
	}

	return fenceAgent
}

func createCredentialsSecret(ctx context.Context, apiClient client.Client) {
	GinkgoHelper()

	By("Reading AWS credentials from CCO Secret")

	awsAccessKey, awsSecretKey, err := farutils.GetAWSCredentials(
		ctx, apiClient, medik8sparams.OperatorNs)
	Expect(err).ToNot(HaveOccurred())

	By("Creating shared credentials Secret")

	credSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      farparams.SharedCredentialsSecretName,
			Namespace: medik8sparams.OperatorNs,
		},
		StringData: map[string]string{
			"--access-key": awsAccessKey,
			"--secret-key": awsSecretKey,
		},
	}

	err = apiClient.Create(ctx, credSecret)
	if k8serrors.IsAlreadyExists(err) {
		err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
			existing := &corev1.Secret{}
			if getErr := apiClient.Get(ctx, client.ObjectKey{
				Name:      farparams.SharedCredentialsSecretName,
				Namespace: medik8sparams.OperatorNs,
			}, existing); getErr != nil {
				return getErr
			}

			existing.StringData = credSecret.StringData

			return apiClient.Update(ctx, existing)
		})
		Expect(err).ToNot(HaveOccurred(),
			"Failed to reconcile shared credentials Secret")

		return
	}

	Expect(err).ToNot(HaveOccurred(),
		"Failed to create shared credentials Secret")
}

func buildFenceParams(
	ctx context.Context, apiClient client.Client, region string,
) (map[string]interface{}, map[string]interface{}) {
	GinkgoHelper()

	By("Building fence_aws shared parameters")

	sharedParams := map[string]interface{}{
		"--region":          region,
		"--action":          "reboot",
		"--skip-race-check": "",
	}

	By("Building node parameters (--plug = EC2 instance ID)")

	awsNodeParams, err := farutils.BuildAWSNodeParameters(ctx, apiClient)
	Expect(err).ToNot(HaveOccurred())

	nodeParams := make(map[string]interface{})

	for paramName, nodeMap := range awsNodeParams {
		inner := make(map[string]interface{}, len(nodeMap))
		for nodeName, val := range nodeMap {
			inner[nodeName] = val
		}

		nodeParams[paramName] = inner
	}

	return sharedParams, nodeParams
}

func waitForLeaderElection(ctx context.Context, apiClient client.Client) string {
	GinkgoHelper()

	By("Identifying active FAR controller node")

	var leaderNode string

	Eventually(func() error {
		var leaderErr error

		leaderNode, leaderErr = farutils.GetActiveFARControllerNode(ctx, apiClient)

		return leaderErr
	}, farparams.ControllerHandoverTimeout, farparams.DefaultPollInterval).Should(Succeed(),
		"FAR leader election did not settle")
	GinkgoWriter.Printf("FAR leader is on node: %s\n", leaderNode)

	return leaderNode
}
