package main

import (
	"flag"
	"net/http"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	browserv1 "github.com/livellm/browser-operator/api/v1alpha1"
	"github.com/livellm/browser-operator/internal/controller"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(browserv1.AddToScheme(scheme))
}

func main() {
	var metricsAddr string
	var probeAddr string
	var leaderElect bool

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&leaderElect, "leader-elect", false, "Enable leader election for HA.")
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))
	setupLog := ctrl.Log.WithName("setup")

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         leaderElect,
		LeaderElectionID:       "livellm-browser-operator.livellm.io",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// Read configuration from environment
	defaultBrowserImage := os.Getenv("DEFAULT_BROWSER_IMAGE")
	defaultControllerImage := os.Getenv("DEFAULT_CONTROLLER_IMAGE")
	if defaultBrowserImage != "" {
		setupLog.Info("default browser image overridden", "image", defaultBrowserImage)
	}
	if defaultControllerImage != "" {
		setupLog.Info("default controller image overridden", "image", defaultControllerImage)
	}

	httpClient := &http.Client{Timeout: 60 * time.Second}

	browserReconciler := &controller.BrowserReconciler{
		Client:              mgr.GetClient(),
		Scheme:              mgr.GetScheme(),
		HTTPClient:          httpClient,
		DefaultBrowserImage: defaultBrowserImage,
	}
	if err := browserReconciler.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Browser")
		os.Exit(1)
	}

	controllerReconciler := &controller.ControllerReconciler{
		Client:                 mgr.GetClient(),
		Scheme:                 mgr.GetScheme(),
		HTTPClient:             httpClient,
		DefaultControllerImage: defaultControllerImage,
	}
	if err := controllerReconciler.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Controller")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
