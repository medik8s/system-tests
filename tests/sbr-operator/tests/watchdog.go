package tests

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"regexp"
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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

var validWatchdogDevice = regexp.MustCompile(`^/dev/watchdog[0-9]*$`)

var _ = Describe(
	"SBR Functional — Watchdog Integration",
	Ordered,
	ContinueOnFailure,
	Label(labels.OperatorSBR), func() {
		// nodeWatchdogDevices maps node name → discovered /dev/watchdog* paths.
		// Populated in BeforeAll from the shared inventory (if it already ran)
		// or from an independent per-node probe so this suite is self-contained.
		// Only Ready, schedulable nodes are included.
		var nodeWatchdogDevices map[string][]string

		BeforeAll(func() {
			nodeList, err := APIClient.CoreV1Interface.Nodes().List(context.TODO(), metav1.ListOptions{})
			Expect(err).ToNot(HaveOccurred(), "Failed to list cluster nodes for watchdog integration checks")
			Expect(nodeList.Items).ToNot(BeEmpty(), "No nodes found in the cluster")

			// Fast path: the 'SBR Debug — Cluster Watchdog Inventory' suite already
			// populated the shared map. Copy only schedulable nodes' entries so we
			// don't attempt to probe NotReady nodes and get misleading errors.
			if WatchdogDevicesByNode != nil {
				nodeWatchdogDevices = make(map[string][]string)

				for i := range nodeList.Items {
					node := &nodeList.Items[i]
					if !isNodeSchedulable(node) {
						GinkgoWriter.Printf("Skipping unschedulable/NotReady node %s\n", node.Name)

						continue
					}

					devs, ok := WatchdogDevicesByNode[node.Name]
					if !ok || devs == nil {
						// !ok: node missing from inventory entirely.
						// devs == nil: inventory probe failed (e.g. ExecCommand failed in disconnected
						// cluster before the slow-path fix applied to this suite).
						// Preserve nil so the test case can distinguish "probe failed" from
						// "no hardware watchdog" and skip accordingly.
						GinkgoWriter.Printf(
							"Warning: node %s missing from or probe-failed in watchdog inventory; skipping\n",
							node.Name)

						nodeWatchdogDevices[node.Name] = nil

						continue
					}

					nodeWatchdogDevices[node.Name] = devs
				}

				if len(nodeWatchdogDevices) == 0 {
					Skip("No schedulable nodes found in watchdog inventory; skipping test")
				}

				return
			}

			// Slow path: the inventory suite did not run (e.g. nightly label filter
			// excludes FrequencyPresubmit). Discover watchdog devices independently.
			nodeWatchdogDevices = make(map[string][]string)

			for i := range nodeList.Items {
				node := &nodeList.Items[i]
				nodeName := node.Name

				if !isNodeSchedulable(node) {
					GinkgoWriter.Printf("Skipping unschedulable/NotReady node %s\n", nodeName)

					continue
				}

				podName := watchdogDebugPodName(nodeName)

				// Run a pod to completion instead of exec-ing into a long-running one.
				// ExecCommand uses SPDY/WebSocket to the kube API exec endpoint, which
				// requires resolving the external API hostname. In disconnected clusters the
				// test binary runs inside the cluster and uses in-cluster DNS (172.30.x.x),
				// which has no entry for the external hostname — causing every exec to fail.
				// A run-to-completion pod sidesteps this: pod creation uses standard REST,
				// and logs are read via watchdogProbeLogs which uses kubernetes.default.svc.
				probePod, createErr := pod.NewBuilder(
					APIClient, podName, medik8sparams.OperatorNs, sbrparams.WatchdogDebugImage).
					DefineOnNode(nodeName).
					WithHostPid(true).
					WithPrivilegedFlag().
					WithRestartPolicy(corev1.RestartPolicyNever).
					RedefineDefaultCMD([]string{
						"sh", "-c", "ls -1 /proc/1/root/dev/watchdog* 2>/dev/null || true",
					}).
					Create()
				if createErr != nil {
					GinkgoWriter.Printf("Warning: could not create watchdog probe pod for node %s: %v\n",
						nodeName, createErr)
					nodeWatchdogDevices[nodeName] = nil

					continue
				}

				DeferCleanup(func(name string) {
					if existing, pullErr := pod.Pull(APIClient, name, medik8sparams.OperatorNs); pullErr == nil {
						if _, delErr := existing.Delete(); delErr != nil {
							GinkgoWriter.Printf("DeferCleanup: failed to delete probe pod %s: %v\n",
								name, delErr)
						}
					}
				}, podName)

				if waitErr := probePod.WaitUntilInStatus(corev1.PodSucceeded, medik8sparams.DefaultTimeout); waitErr != nil {
					GinkgoWriter.Printf("Warning: watchdog probe pod for node %s did not complete: %v\n",
						nodeName, waitErr)
					nodeWatchdogDevices[nodeName] = nil

					if _, delErr := probePod.Delete(); delErr != nil {
						GinkgoWriter.Printf("Warning: failed to delete watchdog probe pod for node %s: %v\n",
							nodeName, delErr)
					}

					continue
				}

				logContent, logErr := watchdogProbeLogs(podName, medik8sparams.OperatorNs, probePod)

				if _, delErr := probePod.Delete(); delErr != nil {
					GinkgoWriter.Printf("Warning: failed to delete watchdog probe pod for node %s: %v\n",
						nodeName, delErr)
				}

				if logErr != nil {
					GinkgoWriter.Printf("Warning: could not read watchdog probe pod logs for node %s: %v\n",
						nodeName, logErr)
					nodeWatchdogDevices[nodeName] = nil

					continue
				}

				// Use a non-nil empty slice so that "no devices found" is
				// distinguishable from "probe failed" (which leaves nil in the map).
				devices := make([]string, 0)

				for _, line := range strings.Split(strings.TrimSpace(logContent), "\n") {
					line = strings.TrimSpace(line)
					if line == "" {
						continue
					}

					if !strings.HasPrefix(line, "/proc/1/root/dev/watchdog") {
						GinkgoWriter.Printf("Warning: ignoring unexpected watchdog probe output on node %s: %q\n",
							nodeName, line)

						continue
					}

					device := strings.TrimPrefix(line, "/proc/1/root")
					if device != "" && validWatchdogDevice.MatchString(device) {
						devices = append(devices, device)
					}
				}

				nodeWatchdogDevices[nodeName] = devices
			}

			if len(nodeWatchdogDevices) == 0 {
				Skip("No schedulable nodes found; skipping watchdog integration test")
			}

			GinkgoWriter.Println("=== Watchdog inventory (self-discovered) ===")

			for nodeName, devs := range nodeWatchdogDevices {
				switch {
				case devs == nil:
					GinkgoWriter.Printf("  %s: probe-failed\n", nodeName)
				case len(devs) == 0:
					GinkgoWriter.Printf("  %s: none\n", nodeName)
				default:
					GinkgoWriter.Printf("  %s: %v\n", nodeName, devs)
				}
			}
		})

		It("Verify watchdog device accessibility and softdog module availability",
			reportxml.ID("88878"),
			Label(
				labels.OperatorSBR,
				labels.DisruptionNonDestructive,
				labels.TierAcceptance,
				labels.PlatformAny,
				labels.ComponentController,
				labels.FrequencyNightly,
			), func() {
				Expect(nodeWatchdogDevices).ToNot(BeEmpty(),
					"watchdog inventory is empty — no schedulable nodes were checked")

				var hwNodes, noWatchdogNodes []string

				for nodeName, devs := range nodeWatchdogDevices {
					switch {
					case devs == nil:
						// Probe pod failed during BeforeAll; reported there as a warning.
						// Don't run softdog check — we can't distinguish "no hw watchdog" from "probe error".
						GinkgoWriter.Printf("Skipping node %s: watchdog probe failed during setup\n", nodeName)
					case len(devs) > 0:
						hwNodes = append(hwNodes, nodeName)
					default:
						noWatchdogNodes = append(noWatchdogNodes, nodeName)
					}
				}

				if len(hwNodes) == 0 && len(noWatchdogNodes) == 0 {
					Fail("watchdog integration test did not successfully inventory any schedulable nodes; " +
						"all probe pods likely failed during setup")
				}

				var errorMessages []string

				// Part 1: nodes with hardware watchdog — verify each device is a character device.
				By(fmt.Sprintf("Part 1: verifying hardware watchdog devices on %d node(s)", len(hwNodes)))

				for _, nodeName := range hwNodes {
					devices := nodeWatchdogDevices[nodeName]

					By(fmt.Sprintf("Checking hardware watchdog on node %s: %v", nodeName, devices))

					if len(devices) == 0 {
						continue
					}

					podName := watchdogDebugPodName("hwchk-" + nodeName)

					var testCmds []string
					for _, device := range devices {
						testCmds = append(testCmds,
							fmt.Sprintf("test -c '/proc/1/root%s' && echo '%s OK' || echo '%s FAIL'",
								device, device, device))
					}

					hwCheckCmd := []string{"sh", "-c", strings.Join(testCmds, "; ")}

					hwPod, createErr := pod.NewBuilder(
						APIClient, podName, medik8sparams.OperatorNs, sbrparams.WatchdogDebugImage).
						DefineOnNode(nodeName).
						WithHostPid(true).
						WithPrivilegedFlag().
						WithRestartPolicy(corev1.RestartPolicyNever).
						RedefineDefaultCMD(hwCheckCmd).
						Create()
					if createErr != nil {
						errorMessages = append(errorMessages,
							fmt.Sprintf("node %s: failed to create debug pod: %v", nodeName, createErr))

						continue
					}

					DeferCleanup(func(name string) {
						if existing, pullErr := pod.Pull(APIClient, name, medik8sparams.OperatorNs); pullErr == nil {
							if _, delErr := existing.Delete(); delErr != nil {
								GinkgoWriter.Printf("Warning: failed to delete debug pod %s: %v\n", name, delErr)
							}
						}
					}, podName)

					if waitErr := hwPod.WaitUntilInStatus(corev1.PodSucceeded, medik8sparams.DefaultTimeout); waitErr != nil {
						errorMessages = append(errorMessages,
							fmt.Sprintf("node %s: hardware watchdog check pod did not complete: %v", nodeName, waitErr))

						if _, delErr := hwPod.Delete(); delErr != nil {
							GinkgoWriter.Printf("Warning: failed to delete debug pod %s: %v\n", podName, delErr)
						}

						continue
					}

					hwLog, hwLogErr := watchdogProbeLogs(podName, medik8sparams.OperatorNs, hwPod)
					if _, delErr := hwPod.Delete(); delErr != nil {
						GinkgoWriter.Printf("Warning: failed to delete debug pod %s: %v\n", podName, delErr)
					}

					if hwLogErr != nil {
						errorMessages = append(errorMessages,
							fmt.Sprintf("node %s: failed to read hardware watchdog check pod logs: %v", nodeName, hwLogErr))

						continue
					}

					seenDevices := make(map[string]bool, len(devices))

					for _, line := range strings.Split(strings.TrimSpace(hwLog), "\n") {
						line = strings.TrimSpace(line)

						switch {
						case line == "":
							continue
						case strings.HasSuffix(line, " OK"):
							seenDevices[strings.TrimSuffix(line, " OK")] = true
						case strings.HasSuffix(line, " FAIL"):
							device := strings.TrimSuffix(line, " FAIL")
							seenDevices[device] = true
							errorMessages = append(errorMessages,
								fmt.Sprintf("node %s: watchdog device %s is not a character device", nodeName, device))
						default:
							errorMessages = append(errorMessages,
								fmt.Sprintf("node %s: unexpected hardware watchdog check output: %q", nodeName, line))
						}
					}

					for _, device := range devices {
						if !seenDevices[device] {
							errorMessages = append(errorMessages,
								fmt.Sprintf("node %s: no result for watchdog device %s — log may be truncated", nodeName, device))
						}
					}
				}

				// Part 2: nodes without hardware watchdog — verify softdog module is present in the
				// kernel so the SBR agent can load it as a fallback (/dev/watchdog backup path).
				// On RHCOS, modules live under /usr/lib/modules (not /lib/modules which is a symlink).
				By(fmt.Sprintf("Part 2: verifying softdog fallback on %d node(s) with no hardware watchdog",
					len(noWatchdogNodes)))

				for _, nodeName := range noWatchdogNodes {
					By(fmt.Sprintf("Checking softdog availability on node %s (no hardware watchdog found)", nodeName))

					podName := watchdogDebugPodName("softdog-" + nodeName)

					// Three-step check (first match wins, always exits 0):
					// 1. A watchdog device exists already (softdog or hw not in inventory).
					// 2. softdog.ko* is present under /usr/lib/modules (RHCOS standard location).
					// 3. softdog.ko* is present under /lib/modules (fallback for other layouts).
					softdogCmd := []string{
						"sh", "-c",
						"ls /proc/1/root/dev/watchdog* 2>/dev/null | head -1 | grep -q . && echo loaded && exit 0;" +
							"ls -R /proc/1/root/usr/lib/modules 2>/dev/null | grep -q 'softdog\\.ko' " +
							"&& echo available && exit 0;" +
							"ls -R /proc/1/root/lib/modules 2>/dev/null | grep -q 'softdog\\.ko' " +
							"&& echo available && exit 0;" +
							"echo missing",
					}

					softdogPod, createErr := pod.NewBuilder(
						APIClient, podName, medik8sparams.OperatorNs, sbrparams.WatchdogDebugImage).
						DefineOnNode(nodeName).
						WithHostPid(true).
						WithPrivilegedFlag().
						WithRestartPolicy(corev1.RestartPolicyNever).
						RedefineDefaultCMD(softdogCmd).
						Create()
					if createErr != nil {
						errorMessages = append(errorMessages,
							fmt.Sprintf("node %s: failed to create softdog check pod: %v", nodeName, createErr))

						continue
					}

					DeferCleanup(func(name string) {
						if existing, pullErr := pod.Pull(APIClient, name, medik8sparams.OperatorNs); pullErr == nil {
							if _, delErr := existing.Delete(); delErr != nil {
								GinkgoWriter.Printf("Warning: failed to delete debug pod %s: %v\n", name, delErr)
							}
						}
					}, podName)

					if waitErr := softdogPod.WaitUntilInStatus(corev1.PodSucceeded, medik8sparams.DefaultTimeout); waitErr != nil {
						errorMessages = append(errorMessages,
							fmt.Sprintf("node %s: failed to check softdog status: %v", nodeName, waitErr))

						if _, delErr := softdogPod.Delete(); delErr != nil {
							GinkgoWriter.Printf("Warning: failed to delete softdog check pod %s: %v\n", podName, delErr)
						}

						continue
					}

					softdogLog, softdogLogErr := watchdogProbeLogs(podName, medik8sparams.OperatorNs, softdogPod)
					if _, delErr := softdogPod.Delete(); delErr != nil {
						GinkgoWriter.Printf("Warning: failed to delete softdog check pod %s: %v\n", podName, delErr)
					}

					if softdogLogErr != nil {
						errorMessages = append(errorMessages,
							fmt.Sprintf("node %s: failed to read softdog check pod logs: %v", nodeName, softdogLogErr))

						continue
					}

					switch result := strings.TrimSpace(softdogLog); result {
					case "loaded":
						GinkgoWriter.Printf("  node %s: watchdog device already present\n", nodeName)
					case "available":
						GinkgoWriter.Printf("  node %s: softdog.ko found in kernel module tree\n", nodeName)
					default:
						errorMessages = append(errorMessages,
							fmt.Sprintf("node %s: no hardware watchdog and softdog module not found in kernel (got=%q)",
								nodeName, result))
					}
				}

				if len(errorMessages) > 0 {
					errMsg := "Watchdog integration check failures:\n"
					for _, msg := range errorMessages {
						errMsg += fmt.Sprintf("- %s\n", msg)
					}

					Fail(errMsg)
				}
			})
	})

// watchdogProbeLogs reads the completed probe pod's logs.
// When running inside a cluster pod (in-cluster config available), it uses the
// kubernetes.default.svc endpoint to avoid external API hostname DNS failures in
// disconnected clusters. Otherwise it falls back to the eco-goinfra GetFullLog.
func watchdogProbeLogs(podName, namespace string, builder *pod.Builder) (string, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return builder.GetFullLog("test")
	}

	client, clientErr := kubernetes.NewForConfig(cfg)
	if clientErr != nil {
		return builder.GetFullLog("test")
	}

	req := client.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{Container: "test"})

	streamCtx, streamCancel := context.WithTimeout(context.Background(), sbrparams.WatchdogProbeLogTimeout)
	defer streamCancel()

	stream, streamErr := req.Stream(streamCtx)
	if streamErr != nil {
		// Fall back to eco-goinfra client when the in-cluster service account
		// lacks pods/log RBAC (common in CI where the test pod's SA is scoped
		// differently from the kubeconfig used by eco-goinfra).
		GinkgoWriter.Printf("Warning: in-cluster log stream for pod %s failed: %v; falling back to eco-goinfra\n",
			podName, streamErr)

		return builder.GetFullLog("test")
	}

	defer stream.Close()

	var buf bytes.Buffer
	if _, copyErr := io.Copy(&buf, io.LimitReader(stream, 64*1024)); copyErr != nil {
		return "", copyErr
	}

	return buf.String(), nil
}
