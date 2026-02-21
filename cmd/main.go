package main

import (
	"flag"
	"fmt"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/vinzenz/pangolin-ingress-controller/internal/controller"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	var ingressClass string
	var pangolinBaseURL string
	var pangolinAPIKeySecret string
	var pangolinAPIKeyNamespace string
	var pangolinOrgID string
	var pangolinSiteNiceID string
	var resourcePrefix string

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.StringVar(&ingressClass, "ingress-class", "pangolin", "The ingress class this controller manages.")
	flag.StringVar(&pangolinBaseURL, "pangolin-base-url", "https://api.tunnel.tf", "The base URL for the Pangolin API.")
	flag.StringVar(&pangolinAPIKeySecret, "pangolin-api-key-secret", "pangolin-api-key", "The name of the secret containing the Pangolin API key.")
	flag.StringVar(&pangolinAPIKeyNamespace, "pangolin-api-key-namespace", "pangolin-system", "The namespace of the secret containing the Pangolin API key.")
	flag.StringVar(&pangolinOrgID, "pangolin-org-id", "", "The organization identifier in Pangolin.")
	flag.StringVar(&pangolinSiteNiceID, "pangolin-site-nice-id", "", "The Pangolin site nice ID to attach resources/targets to.")
	flag.StringVar(&resourcePrefix, "resource-prefix", "pangolin-controller", "Prefix for Pangolin resource names.")

	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "pangolin-ingress-controller.k8s.io",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if pangolinOrgID == "" {
		setupLog.Error(fmt.Errorf("missing pangolin org id"), "pangolin org id must be configured via --pangolin-org-id")
		os.Exit(1)
	}
	if pangolinSiteNiceID == "" {
		setupLog.Error(fmt.Errorf("missing pangolin site nice id"), "pangolin site nice id must be configured via --pangolin-site-nice-id")
		os.Exit(1)
	}

	if err = (&controller.IngressReconciler{
		Client:          mgr.GetClient(),
		Scheme:          mgr.GetScheme(),
		IngressClass:    ingressClass,
		ResourcePrefix:  resourcePrefix,
		PangolinBaseURL: pangolinBaseURL,
		APIKeySecret:    pangolinAPIKeySecret,
		APIKeyNamespace: pangolinAPIKeyNamespace,
		OrgID:           pangolinOrgID,
		SiteNiceID:      pangolinSiteNiceID,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Ingress")
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

	setupLog.Info("starting manager", "ingressClass", ingressClass)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
