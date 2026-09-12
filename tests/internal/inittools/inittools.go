package inittools

import (
	"flag"
	"os"

	"github.com/go-logr/logr"
	"github.com/medik8s/system-tests/tests/internal/config"
	"github.com/rh-ecosystem-edge/eco-goinfra/pkg/clients"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// Event-verification polling (helpers.WaitForEvents) can exhaust client-go's
// default client-side rate limiter (QPS 5 / Burst 10) under heavy suites,
// surfacing as "client rate limiter Wait returned an error: context deadline
// exceeded". EventsClient below is built with higher limits for those callers.
const (
	eventsClientQPS   = 50
	eventsClientBurst = 100
)

var (
	// APIClient provides access to cluster.
	APIClient *clients.Settings
	// EventsClient is a high-QPS clientset for event verification and other
	// poll-heavy reads that the default-throttled APIClient.K8sClient starves.
	// It is nil when APIClient is nil (e.g. dry-run).
	EventsClient kubernetes.Interface
	// GeneralConfig provides access to general configuration parameters.
	GeneralConfig *config.GeneralConfig
)

// init loads all variables automatically when this package is imported. Once package is imported a user has full
// access to all vars within init function. It is recommended to import this package using dot import.
func init() {
	klog.InitFlags(nil)
	klog.EnableContextualLogging(true)
	logf.SetLogger(logr.Discard())

	_ = flag.Set("logtostderr", "true")

	// Skip loading config if running unit tests
	if os.Getenv("UNIT_TEST") == "true" {
		return
	}

	if GeneralConfig = config.NewConfig(); GeneralConfig == nil {
		klog.Fatalf("error to load general config")
	}

	_ = flag.Set("v", GeneralConfig.VerboseLevel)

	if APIClient = clients.New(""); APIClient == nil {
		if GeneralConfig.DryRun {
			return
		}

		klog.Exitf("can not load ApiClient. Please check your KUBECONFIG env var")
	}

	if EventsClient = newEventsClient(APIClient.Config); EventsClient == nil {
		klog.Exitf("can not build high-QPS events client from ApiClient config")
	}
}

// newEventsClient builds a clientset from a copy of cfg with raised QPS/Burst.
// Returns nil on error so init can fail loudly rather than hand back a client
// that silently keeps the default limits.
func newEventsClient(cfg *rest.Config) kubernetes.Interface {
	if cfg == nil {
		return nil
	}

	throttled := rest.CopyConfig(cfg)
	throttled.QPS = eventsClientQPS
	throttled.Burst = eventsClientBurst

	clientset, err := kubernetes.NewForConfig(throttled)
	if err != nil {
		klog.V(100).Infof("failed to build events client: %v", err)

		return nil
	}

	return clientset
}
