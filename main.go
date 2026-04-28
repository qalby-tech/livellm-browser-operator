package main

import (
	"encoding/json"
	"flag"
	"os"

	corev1 "k8s.io/api/core/v1"
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

// parseEnvVars reads a JSON array of {"name":"X","value":"Y"} from the given
// environment variable and returns the corresponding []corev1.EnvVar slice.
// Returns nil if the env var is empty or invalid.
func parseEnvVars(envName string) []corev1.EnvVar {
	raw := os.Getenv(envName)
	if raw == "" {
		return nil
	}
	var envVars []corev1.EnvVar
	if err := json.Unmarshal([]byte(raw), &envVars); err != nil {
		return nil
	}
	return envVars
}

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(browserv1.AddToScheme(scheme))
}

func main() {
	var probeAddr string
	var leaderElect bool

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
	defaultBrowserPullPolicy := os.Getenv("DEFAULT_BROWSER_PULL_POLICY")
	defaultControllerPullPolicy := os.Getenv("DEFAULT_CONTROLLER_PULL_POLICY")
	redisURL := os.Getenv("REDIS_URL")
	if defaultBrowserImage != "" {
		setupLog.Info("default browser image overridden", "image", defaultBrowserImage)
	}
	if defaultControllerImage != "" {
		setupLog.Info("default controller image overridden", "image", defaultControllerImage)
	}
	if defaultBrowserPullPolicy != "" {
		setupLog.Info("default browser pull policy configured", "pullPolicy", defaultBrowserPullPolicy)
	}
	if defaultControllerPullPolicy != "" {
		setupLog.Info("default controller pull policy configured", "pullPolicy", defaultControllerPullPolicy)
	}
	if redisURL != "" {
		setupLog.Info("redis URL configured", "url", redisURL)
	}

	redisState, err := controller.NewRedisState(redisURL)
	if err != nil {
		setupLog.Error(err, "unable to connect to Redis")
		os.Exit(1)
	}

	defaultBrowserEnv := parseEnvVars("DEFAULT_BROWSER_ENV")
	defaultControllerEnv := parseEnvVars("DEFAULT_CONTROLLER_ENV")

	browserReconciler := &controller.BrowserReconciler{
		Client:                   mgr.GetClient(),
		Scheme:                   mgr.GetScheme(),
		RedisState:               redisState,
		DefaultBrowserImage:      defaultBrowserImage,
		DefaultBrowserPullPolicy: defaultBrowserPullPolicy,
		DefaultBrowserEnv:        defaultBrowserEnv,
		RedisURL:                 redisURL,
	}
	if err := browserReconciler.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Browser")
		os.Exit(1)
	}

	controllerReconciler := &controller.ControllerReconciler{
		Client:                      mgr.GetClient(),
		Scheme:                      mgr.GetScheme(),
		RedisState:                  redisState,
		DefaultControllerImage:      defaultControllerImage,
		DefaultControllerPullPolicy: defaultControllerPullPolicy,
		DefaultControllerEnv:        defaultControllerEnv,
		RedisURL:                    redisURL,
		DefaultBrowserImage:         defaultBrowserImage,
		DefaultBrowserPullPolicy:    defaultBrowserPullPolicy,
		DefaultBrowserEnv:           defaultBrowserEnv,
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

	setupLog.Info("starting manager", "defaultBrowserEnv", defaultBrowserEnv, "defaultControllerEnv", defaultControllerEnv)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
