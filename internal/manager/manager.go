// Package manager builds the controller-runtime manager scaffold for the AgentPlane API (P1-T1).
//
// This is a startup scaffold only. It registers the core and agentplane.io/v1alpha1 types in the
// scheme, serves loopback-only metrics and health endpoints, and runs until its context ends. It
// adds NO controller, reconciler, watch, webhook, certificate, finalizer or owned object, and it
// asks the API server for no RBAC beyond what the supplied rest.Config already has. Leader
// election is off because nothing here needs a single active instance; P1-T7 turns it on and tests
// it with a deployed manager.
//
// Readiness reports only that the manager process and its cache have started. It says nothing
// about whether any Tenant or Agent was reconciled, because nothing reconciles them yet.
package manager

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	agentplanev1alpha1 "github.com/MenaceHecker/AgentPlane/api/v1alpha1"
)

const (
	// DefaultMetricsAddr and DefaultHealthAddr are loopback-only. 8081 is the mock model's default
	// port, so neither default uses it.
	DefaultMetricsAddr = "127.0.0.1:8082"
	DefaultHealthAddr  = "127.0.0.1:8083"

	// ShutdownTimeout bounds how long the manager waits for its runnables after the context ends.
	ShutdownTimeout = 10 * time.Second
	// readyProbeTimeout bounds one readiness evaluation.
	readyProbeTimeout = 500 * time.Millisecond
)

// Options are the manager's endpoint settings.
type Options struct {
	// MetricsAddr is the host:port of the metrics endpoint (plain HTTP, loopback only).
	MetricsAddr string
	// HealthAddr is the host:port of the /healthz and /readyz endpoints (loopback only).
	HealthAddr string
}

// DefaultOptions returns the loopback defaults.
func DefaultOptions() Options {
	return Options{MetricsAddr: DefaultMetricsAddr, HealthAddr: DefaultHealthAddr}
}

// Validate checks both addresses before anything binds a port. Each must be host:port with a
// loopback IP literal or "localhost" and a port from 1 to 65535, and the two must differ. Binding
// problems that can only be seen at bind time (a port already in use) are returned by Run.
func (o Options) Validate() error {
	if err := validateLoopbackAddr("metrics", o.MetricsAddr); err != nil {
		return err
	}
	if err := validateLoopbackAddr("health", o.HealthAddr); err != nil {
		return err
	}
	if o.MetricsAddr == o.HealthAddr {
		return fmt.Errorf("the metrics and health addresses must differ, both are %q", o.MetricsAddr)
	}
	return nil
}

func validateLoopbackAddr(what, addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid %s address %q: %w", what, addr, err)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("invalid %s address %q: the port must be a number from 1 to 65535", what, addr)
	}
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return fmt.Errorf("invalid %s address %q: only a loopback address is allowed (127.0.0.1, ::1 or localhost)", what, addr)
		}
	}
	return nil
}

// NewScheme returns a scheme with the Kubernetes core types and agentplane.io/v1alpha1.
func NewScheme() (*runtime.Scheme, error) {
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		return nil, fmt.Errorf("adding the core types to the scheme: %w", err)
	}
	if err := agentplanev1alpha1.AddToScheme(s); err != nil {
		return nil, fmt.Errorf("adding agentplane.io/v1alpha1 to the scheme: %w", err)
	}
	return s, nil
}

// New builds the manager for an explicitly supplied rest.Config. It never discovers a kubeconfig.
// It validates the options first and registers only health and readiness checks.
func New(cfg *rest.Config, opts Options) (ctrl.Manager, error) {
	if cfg == nil {
		return nil, errors.New("a rest.Config is required; the manager never discovers one")
	}
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	scheme, err := NewScheme()
	if err != nil {
		return nil, err
	}
	shutdown := ShutdownTimeout
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                  scheme,
		Metrics:                 metricsserver.Options{BindAddress: opts.MetricsAddr},
		HealthProbeBindAddress:  opts.HealthAddr,
		LeaderElection:          false,
		GracefulShutdownTimeout: &shutdown,
	})
	if err != nil {
		return nil, fmt.Errorf("creating the manager: %w", err)
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return nil, fmt.Errorf("adding the health check: %w", err)
	}
	if err := mgr.AddReadyzCheck("readyz", cacheStarted(mgr)); err != nil {
		return nil, fmt.Errorf("adding the readiness check: %w", err)
	}
	return mgr, nil
}

// cacheStarted is the readiness check: it succeeds once the manager's cache has started. It does
// not look at any Tenant or Agent.
func cacheStarted(mgr ctrl.Manager) healthz.Checker {
	return func(_ *http.Request) error {
		ctx, cancel := context.WithTimeout(context.Background(), readyProbeTimeout)
		defer cancel()
		if !mgr.GetCache().WaitForCacheSync(ctx) {
			return errors.New("the manager cache has not started")
		}
		return nil
	}
}

// Run builds the manager and runs it until ctx is cancelled. A clean cancellation returns nil; a
// bind error or any other failure is returned.
func Run(ctx context.Context, cfg *rest.Config, opts Options) error {
	mgr, err := New(cfg, opts)
	if err != nil {
		return err
	}
	if err := mgr.Start(ctx); err != nil {
		return fmt.Errorf("running the manager: %w", err)
	}
	return nil
}
