// Command manager is the AgentPlane controller manager scaffold (P1-T1). It starts a
// controller-runtime manager with loopback-only metrics and health endpoints and no controllers.
//
// It never discovers a kubeconfig on its own: it needs an explicit --kubeconfig path, or it runs
// in-cluster. $KUBECONFIG and ~/.kube/config are not read, so starting it by accident cannot reach
// whatever cluster your current context points at.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/MenaceHecker/AgentPlane/internal/manager"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "manager:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("manager", flag.ContinueOnError)
	defaults := manager.DefaultOptions()
	metrics := fs.String("metrics-bind-address", defaults.MetricsAddr, "loopback host:port for the metrics endpoint")
	health := fs.String("health-probe-bind-address", defaults.HealthAddr, "loopback host:port for /healthz and /readyz")
	kubeconfig := fs.String("kubeconfig", "", "path to a kubeconfig file; required unless running in-cluster ($KUBECONFIG and ~/.kube/config are never read)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	opts := manager.Options{MetricsAddr: *metrics, HealthAddr: *health}
	if err := opts.Validate(); err != nil {
		return err
	}
	cfg, err := restConfig(*kubeconfig)
	if err != nil {
		return err
	}
	ctrl.SetLogger(zap.New())
	return manager.Run(ctrl.SetupSignalHandler(), cfg, opts)
}

func restConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig != "" {
		cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, fmt.Errorf("loading %s: %w", kubeconfig, err)
		}
		return cfg, nil
	}
	cfg, err := rest.InClusterConfig()
	if err != nil {
		if errors.Is(err, rest.ErrNotInCluster) {
			return nil, errors.New("not running in a cluster and no --kubeconfig was given; refusing to guess a cluster")
		}
		return nil, err
	}
	return cfg, nil
}
