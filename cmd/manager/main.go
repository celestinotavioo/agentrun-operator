// Command manager runs the AgentRun controller.
package main

import (
	"flag"
	"os"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	agentsv1 "github.com/celestinotavioo/agentrun-operator/api/v1alpha1"
	"github.com/celestinotavioo/agentrun-operator/internal/controller"
)

func main() {
	var (
		metricsAddr, probeAddr, kubeContext string
		agentImage, pullPolicy, mockSpeed   string
		defaultRuntimeClass                 string
		leaderElect                         bool
	)
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "Address of the Prometheus /metrics endpoint.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "Address of the health probes.")
	flag.StringVar(&kubeContext, "context", "", "Kubeconfig context to use when running outside the cluster.")
	flag.StringVar(&agentImage, "agent-image", envOr("AGENT_IMAGE", "agentrun-agent:dev"), "Image for attempt Jobs.")
	flag.StringVar(&pullPolicy, "agent-image-pull-policy", envOr("AGENT_IMAGE_PULL_POLICY", "IfNotPresent"), "Pull policy for attempt Jobs.")
	flag.StringVar(&mockSpeed, "mock-speed", envOr("MOCK_SPEED", "1"), "Speed multiplier for the mock provider (2 = twice as fast).")
	flag.StringVar(&defaultRuntimeClass, "default-runtime-class", os.Getenv("DEFAULT_RUNTIME_CLASS"), "RuntimeClass for attempt Jobs that do not set spec.sandbox.runtimeClassName (e.g. gvisor).")
	flag.BoolVar(&leaderElect, "leader-elect", false, "Enable leader election.")
	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	setupLog := ctrl.Log.WithName("setup")

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(agentsv1.AddToScheme(scheme))

	cfg, err := config.GetConfigWithContext(kubeContext)
	if err != nil {
		setupLog.Error(err, "unable to load kubeconfig")
		os.Exit(1)
	}
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         leaderElect,
		LeaderElectionID:       "agentrun-operator.agents.devfest.dev",
	})
	if err != nil {
		setupLog.Error(err, "unable to create manager")
		os.Exit(1)
	}

	r := &controller.AgentRunReconciler{
		Client:    mgr.GetClient(),
		APIReader: mgr.GetAPIReader(),
		Scheme:    mgr.GetScheme(),
		Recorder:  mgr.GetEventRecorder("agentrun-controller"),
		Jobs: controller.JobOptions{
			AgentImage:          agentImage,
			ImagePullPolicy:     corev1.PullPolicy(pullPolicy),
			MockSpeed:           mockSpeed,
			DefaultRuntimeClass: defaultRuntimeClass,
		},
	}
	if err := r.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller")
		os.Exit(1)
	}
	utilruntime.Must(mgr.AddHealthzCheck("healthz", healthz.Ping))
	utilruntime.Must(mgr.AddReadyzCheck("readyz", healthz.Ping))

	setupLog.Info("starting AgentRun controller", "agentImage", agentImage, "mockSpeed", mockSpeed, "defaultRuntimeClass", defaultRuntimeClass)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "manager exited")
		os.Exit(1)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
