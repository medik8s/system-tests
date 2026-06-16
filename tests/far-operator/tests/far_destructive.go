package tests

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/medik8s/system-tests/tests/far-operator/internal/farparams"
	"github.com/medik8s/system-tests/tests/far-operator/internal/farutils"
	"github.com/medik8s/system-tests/tests/internal/labels"
	. "github.com/medik8s/system-tests/tests/internal/medik8sinittools"
	"github.com/medik8s/system-tests/tests/internal/medik8sparams"
)

var farGVR = schema.GroupVersionResource{
	Group:    "fence-agents-remediation.medik8s.io",
	Version:  "v1alpha1",
	Resource: "fenceagentsremediations",
}

var fartGVR = schema.GroupVersionResource{
	Group:    "fence-agents-remediation.medik8s.io",
	Version:  "v1alpha1",
	Resource: "fenceagentsremediationtemplates",
}

var _ = Describe("FAR Destructive Tests",
	Serial, Ordered, ContinueOnFailure,
	Label(farparams.Label, labels.DisruptionDestructive,
		labels.PlatformAWS, labels.FrequencyWeekly),
	func() {
		var (
			ctx             context.Context
			platform        configv1.PlatformType
			region          string
			fenceAgent      string
			nodeIDParam     string
			awsAccessKey    string
			awsSecretKey    string
			leaderNode      string
			targetNode      *corev1.Node
			sharedParams    map[string]interface{}
			nodeParams      map[string]interface{}
			currentFARTName string
			currentFARName  string
		)

		BeforeAll(func() {
			ctx = context.Background()

			By("Detecting cluster platform")

			var err error

			platform, region, err = farutils.DetectPlatform(ctx, APIClient)
			Expect(err).ToNot(HaveOccurred())

			if platform != configv1.AWSPlatformType {
				Skip(fmt.Sprintf(
					"FAR destructive tests require AWS, got %s", platform))
			}

			By("Resolving fence agent for platform")

			fenceAgent, nodeIDParam, err = farutils.FenceAgentForPlatform(platform)
			Expect(err).ToNot(HaveOccurred())
			GinkgoWriter.Printf(
				"Platform: %s, Agent: %s, Region: %s\n",
				platform, fenceAgent, region)

			By("Verifying FAR operator has at least 1 running pod")

			farPods, err := farutils.GetFARControllerPods(ctx, APIClient)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(farPods)).To(
				BeNumerically(">=", 1),
				"FAR controller must have at least 1 running pod")

			By("Verifying at least 3 Ready worker nodes")

			workerCount, err := farutils.CountReadyWorkerNodes(ctx, APIClient)
			Expect(err).ToNot(HaveOccurred())
			Expect(workerCount).To(
				BeNumerically(">=", 3),
				"Destructive tests require at least 3 Ready worker nodes")

			By("Reading AWS credentials from CCO Secret")

			awsAccessKey, awsSecretKey, err = farutils.GetAWSCredentials(
				ctx, APIClient, medik8sparams.OperatorNs)
			Expect(err).ToNot(HaveOccurred(),
				"AWS credentials must be provisioned by the "+
					"medik8s-aws-credentials CI step")

			By("Building fence_aws shared parameters")

			sharedParams = map[string]interface{}{
				"--region":          region,
				"--action":          "reboot",
				"--skip-race-check": "",
				"--access-key":      awsAccessKey,
				"--secret-key":      awsSecretKey,
			}

			By("Building node parameters (--plug = EC2 instance ID)")

			awsNodeParams, err := farutils.BuildAWSNodeParameters(
				ctx, APIClient)
			Expect(err).ToNot(HaveOccurred())

			nodeParams = make(map[string]interface{})

			for paramName, nodeMap := range awsNodeParams {
				inner := make(map[string]interface{}, len(nodeMap))
				for nodeName, val := range nodeMap {
					inner[nodeName] = val
				}

				nodeParams[paramName] = inner
			}

			By("Identifying active FAR controller node")

			leaderNode, err = farutils.GetActiveFARControllerNode(
				ctx, APIClient)
			Expect(err).ToNot(HaveOccurred())
			GinkgoWriter.Printf("FAR leader is on node: %s\n", leaderNode)

			_ = nodeIDParam
			_ = sharedParams
			_ = nodeParams
		})

		JustAfterEach(func() {
			spec := CurrentSpecReport()
			if spec.Failed() {
				GinkgoWriter.Println(
					"Test failed - running safety net cleanup")
			}

			if targetNode != nil {
				By("Safety net: ensuring kubelet is running on " +
					targetNode.Name)
				_ = farutils.StartKubelet(ctx, targetNode.Name)

				By("Safety net: waiting for node to become Ready")

				_ = farutils.WaitForNodeReady(
					ctx, APIClient, targetNode.Name,
					farparams.NodeReadyTimeout)

				targetNode = nil
			}

			if currentFARName != "" {
				By("Safety net: deleting FAR CR " + currentFARName)
				deleteFARCR(ctx, APIClient, currentFARName)
				currentFARName = ""
			}

			if currentFARTName != "" {
				By("Safety net: deleting FART " + currentFARTName)
				deleteFARTCR(ctx, APIClient, currentFARTName)
				currentFARTName = ""
			}
		})

		Context("Standalone FAR remediation", func() {
			// RHWA-963: 7 standalone destructive tests will be added here.
			// Each test follows this flow:
			//   1. Select target worker (exclude leader node)
			//   2. Record boot ID
			//   3. Create FART + deploy workload pod
			//   4. Stop kubelet (simulate unhealthy node)
			//   5. Create FAR CR (trigger remediation)
			//   6. Verify: taint applied, node rebooted, pod evicted
			//   7. Cleanup via DeferCleanup + JustAfterEach safety net
		})

		Context("NHC+FAR interop", func() {
			// RHWA-1035: 4 NHC+FAR interop tests will be added here.
			// These tests install both NHC and FAR, configure NHC to use
			// FAR as the remediator, then trigger remediation via NHC by
			// stopping kubelet and waiting for NHC to detect the unhealthy
			// node and create a FAR CR automatically.
		})
	})

//nolint:unused // scaffold helper for upcoming destructive test specs
func buildFARTUnstructured(
	name, agent string,
	sharedParams, nodeParams map[string]interface{},
) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "fence-agents-remediation.medik8s.io/v1alpha1",
			"kind":       "FenceAgentsRemediationTemplate",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": medik8sparams.OperatorNs,
			},
			"spec": map[string]interface{}{
				"template": map[string]interface{}{
					"spec": map[string]interface{}{
						"agent":               agent,
						"sharedparameters":    sharedParams,
						"nodeparameters":      nodeParams,
						"retrycount":          10,
						"retryinterval":       "20s",
						"timeout":             "60s",
						"remediationStrategy": "OutOfServiceTaint",
					},
				},
			},
		},
	}
}

func deleteFARCR(
	ctx context.Context, k8sClient client.Client, name string,
) {
	far := &unstructured.Unstructured{}
	far.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   farGVR.Group,
		Version: farGVR.Version,
		Kind:    "FenceAgentsRemediation",
	})

	key := client.ObjectKey{Name: name, Namespace: medik8sparams.OperatorNs}

	err := k8sClient.Get(ctx, key, far)
	if k8serrors.IsNotFound(err) {
		return
	}

	if err != nil {
		GinkgoWriter.Printf("Warning: failed to get FAR CR %s for cleanup: %v\n", name, err)

		return
	}

	if delErr := k8sClient.Delete(ctx, far); delErr != nil {
		GinkgoWriter.Printf("Warning: failed to delete FAR CR %s: %v\n", name, delErr)

		return
	}

	_ = wait.PollUntilContextTimeout(
		ctx, farparams.DefaultPollInterval, 2*time.Minute, true,
		func(ctx context.Context) (bool, error) {
			err := k8sClient.Get(ctx, key, far)

			return k8serrors.IsNotFound(err), nil
		},
	)
}

func deleteFARTCR(
	ctx context.Context, k8sClient client.Client, name string,
) {
	fart := &unstructured.Unstructured{}
	fart.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   fartGVR.Group,
		Version: fartGVR.Version,
		Kind:    "FenceAgentsRemediationTemplate",
	})

	key := client.ObjectKey{Name: name, Namespace: medik8sparams.OperatorNs}

	err := k8sClient.Get(ctx, key, fart)
	if k8serrors.IsNotFound(err) {
		return
	}

	if err != nil {
		GinkgoWriter.Printf("Warning: failed to get FART %s for cleanup: %v\n", name, err)

		return
	}

	if delErr := k8sClient.Delete(ctx, fart); delErr != nil {
		GinkgoWriter.Printf("Warning: failed to delete FART %s: %v\n", name, delErr)

		return
	}

	_ = wait.PollUntilContextTimeout(
		ctx, farparams.DefaultPollInterval, 2*time.Minute, true,
		func(ctx context.Context) (bool, error) {
			err := k8sClient.Get(ctx, key, fart)

			return k8serrors.IsNotFound(err), nil
		},
	)
}
