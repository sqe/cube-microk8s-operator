package main

import (
	"context"
	"flag"
	"os"

	platformv1alpha1 "github.com/akurbanov/cube-microk8s-operator/api/v1alpha1"
	"github.com/akurbanov/cube-microk8s-operator/internal/controller"
	"github.com/akurbanov/cube-microk8s-operator/internal/telemetry"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "telemetry-collector" {
		collector, err := telemetry.New(context.Background())
		if err != nil {
			panic(err)
		}
		if err := collector.Run(ctrl.SetupSignalHandler()); err != nil {
			panic(err)
		}
		return
	}

	var metricsAddress, probeAddress string
	var leaderElection bool
	flag.StringVar(&metricsAddress, "metrics-bind-address", ":8080", "Address for metrics.")
	flag.StringVar(&probeAddress, "health-probe-bind-address", ":8081", "Address for probes.")
	flag.BoolVar(&leaderElection, "leader-elect", true, "Enable leader election.")
	options := zap.Options{Development: false}
	options.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&options)))

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		panic(err)
	}
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		panic(err)
	}
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{Scheme: scheme, Metrics: metricsserver.Options{BindAddress: metricsAddress}, HealthProbeBindAddress: probeAddress, LeaderElection: leaderElection, LeaderElectionID: "cube-operator.platform.cube.dev"})
	if err != nil {
		panic(err)
	}
	if err := (&controller.CubeClusterReconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme()}).SetupWithManager(mgr); err != nil {
		panic(err)
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		panic(err)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		panic(err)
	}
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		panic(err)
	}
}
