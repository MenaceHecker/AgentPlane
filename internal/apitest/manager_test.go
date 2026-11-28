package apitest

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/MenaceHecker/AgentPlane/api/v1alpha1"
	"github.com/MenaceHecker/AgentPlane/internal/manager"
)

// Manager startup tests: they use only the rest.Config of the disposable envtest API server.

func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

// startManager runs manager.Run in a goroutine. Cancellation and the wait for Run to return are
// registered before the manager starts, and a Run error after a clean cancel fails the test.
func (s *testSuite) startManager(t *testing.T, opts manager.Options) (cancel func() error) {
	t.Helper()
	ctrl.SetLogger(logr.Discard())
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	stopped := false
	cancel = func() error {
		if stopped {
			return nil
		}
		stopped = true
		stop()
		select {
		case err := <-done:
			return err
		case <-time.After(30 * time.Second):
			return errors.New("the manager did not stop within 30s of cancellation")
		}
	}
	t.Cleanup(func() {
		if err := cancel(); err != nil {
			t.Errorf("stopping the manager: %v", err)
		}
	})
	go func() { done <- manager.Run(ctx, s.cfg, opts) }()
	return cancel
}

func httpGetStatus(addr, path string) (int, error) {
	c := &http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get("http://" + addr + path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

func waitForStatus(t *testing.T, addr, path string, want int) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		code, err := httpGetStatus(addr, path)
		if err == nil && code == want {
			return
		}
		last = "error " + errString(err) + " code " + strconv.Itoa(code)
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("GET http://%s%s never returned %d within 20s (last: %s)", addr, path, want, last)
}

func errString(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}

func TestManagerServesHealthAndReadinessOnLoopbackAndStopsCleanly(t *testing.T) {
	defer guard(t)
	s := need(t)
	opts := manager.Options{MetricsAddr: freeLoopbackAddr(t), HealthAddr: freeLoopbackAddr(t)}
	cancel := s.startManager(t, opts)

	waitForStatus(t, opts.HealthAddr, "/healthz", http.StatusOK)
	waitForStatus(t, opts.HealthAddr, "/readyz", http.StatusOK)
	waitForStatus(t, opts.MetricsAddr, "/metrics", http.StatusOK)

	if err := cancel(); err != nil {
		t.Fatalf("a cancelled manager must return nil, got: %v", err)
	}
	for _, a := range []string{opts.HealthAddr, opts.MetricsAddr} {
		if c, err := net.DialTimeout("tcp", a, time.Second); err == nil {
			_ = c.Close()
			t.Errorf("%s still accepts connections after the manager stopped", a)
		}
	}
}

func TestManagerReconcilesNothingAndAddsNoLeaseOrOwnedObjects(t *testing.T) {
	defer guard(t)
	s := need(t)
	opts := manager.Options{MetricsAddr: freeLoopbackAddr(t), HealthAddr: freeLoopbackAddr(t)}
	s.startManager(t, opts)
	waitForStatus(t, opts.HealthAddr, "/readyz", http.StatusOK)

	name := s.unique("tenant-untouched")
	created := requireCreated(t, s.createTenant(t, validTenant(name)))
	rv, _ := dig(created, "metadata", "resourceVersion")
	time.Sleep(3 * time.Second) // long enough for any reconciler to have acted

	got := decode(t, s.get(t, tenantsPath+"/"+name).Body)
	if v, _ := dig(got, "metadata", "resourceVersion"); toString(v) != toString(rv) {
		t.Errorf("the Tenant changed after creation (resourceVersion %v -> %v): something reconciled it", rv, v)
	}
	if _, ok := dig(got, "status"); ok {
		t.Errorf("a status appeared on the Tenant: %v", got["status"])
	}
	if f, ok := dig(got, "metadata", "finalizers"); ok {
		t.Errorf("finalizers appeared on the Tenant: %v", f)
	}
	// Leader election is off: no Lease other than the API server's own identity leases (those carry
	// the apiserver.kubernetes.io/identity label and exist with or without a manager).
	leases := decode(t, s.get(t, "/apis/coordination.k8s.io/v1/leases").Body)
	items, _ := leases["items"].([]any)
	for _, it := range items {
		m := it.(map[string]any)
		if id, _ := dig(m, "metadata", "labels", "apiserver.kubernetes.io/identity"); id != "kube-apiserver" {
			n, _ := dig(m, "metadata", "name")
			t.Errorf("a Lease that is not an API-server identity lease exists (%v) although leader election is off", n)
		}
	}
	if r := s.get(t, "/api/v1/namespaces/"+name); r.Code != http.StatusNotFound {
		t.Errorf("a namespace named after the Tenant exists (%d); nothing may create owned objects", r.Code)
	}
}

func TestManagerFailsWhenAnEndpointPortIsAlreadyInUse(t *testing.T) {
	defer guard(t)
	s := need(t)
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = busy.Close() }()
	ctrl.SetLogger(logr.Discard())
	ctx, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	done := make(chan error, 1)
	go func() {
		done <- manager.Run(ctx, s.cfg, manager.Options{MetricsAddr: freeLoopbackAddr(t), HealthAddr: busy.Addr().String()})
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Run returned nil although the health port was already in use")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Run neither failed nor returned within 30s with a busy health port")
	}
}

func TestManagerRefusesBadAddressesWithoutStarting(t *testing.T) {
	defer guard(t)
	s := need(t)
	for _, o := range []manager.Options{
		{MetricsAddr: "0.0.0.0:8082", HealthAddr: freeLoopbackAddr(t)},
		{MetricsAddr: freeLoopbackAddr(t), HealthAddr: "not an address"},
	} {
		if err := manager.Run(context.Background(), s.cfg, o); err == nil {
			t.Errorf("Run accepted %+v", o)
		}
	}
	same := freeLoopbackAddr(t)
	if err := manager.Run(context.Background(), s.cfg, manager.Options{MetricsAddr: same, HealthAddr: same}); err == nil {
		t.Error("Run accepted identical metrics and health addresses")
	}
}

func TestManagerSchemeRecognizesTheServedCRDKinds(t *testing.T) {
	defer guard(t)
	s := need(t)
	mgr, err := manager.New(s.cfg, manager.Options{MetricsAddr: freeLoopbackAddr(t), HealthAddr: freeLoopbackAddr(t)})
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"Tenant", "Agent"} {
		gvks, _, err := mgr.GetScheme().ObjectKinds(mustObject(kind))
		if err != nil || len(gvks) != 1 || gvks[0].Group != "agentplane.io" || gvks[0].Version != "v1alpha1" {
			t.Errorf("%s: %v %v", kind, gvks, err)
		}
	}
}

func mustObject(kind string) runtime.Object {
	switch kind {
	case "Tenant":
		return &v1alpha1.Tenant{}
	default:
		return &v1alpha1.Agent{}
	}
}
