package manager

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
)

func TestDefaultsAreLoopbackOnlyAndValid(t *testing.T) {
	o := DefaultOptions()
	if err := o.Validate(); err != nil {
		t.Fatalf("the defaults are invalid: %v", err)
	}
	for _, a := range []string{o.MetricsAddr, o.HealthAddr} {
		if !strings.HasPrefix(a, "127.0.0.1:") {
			t.Errorf("default address %q is not loopback 127.0.0.1", a)
		}
	}
	if strings.HasSuffix(o.MetricsAddr, ":8081") || strings.HasSuffix(o.HealthAddr, ":8081") {
		t.Error("a default uses 8081, the mock model's default port")
	}
}

func TestInvalidOrNonLoopbackOrConflictingAddressesAreRefused(t *testing.T) {
	for _, c := range []struct {
		name            string
		metrics, health string
	}{
		{"all interfaces", "0.0.0.0:8082", "127.0.0.1:8083"},
		{"empty host", ":8082", "127.0.0.1:8083"},
		{"public address", "192.0.2.10:8082", "127.0.0.1:8083"},
		{"health on all interfaces", "127.0.0.1:8082", "[::]:8083"},
		{"hostname that is not localhost", "example.invalid:8082", "127.0.0.1:8083"},
		{"no port", "127.0.0.1", "127.0.0.1:8083"},
		{"port zero", "127.0.0.1:0", "127.0.0.1:8083"},
		{"port too large", "127.0.0.1:70000", "127.0.0.1:8083"},
		{"port not a number", "127.0.0.1:http", "127.0.0.1:8083"},
		{"empty", "", "127.0.0.1:8083"},
		{"controller-runtime disable value", "0", "127.0.0.1:8083"},
		{"same address twice", "127.0.0.1:8082", "127.0.0.1:8082"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if err := (Options{MetricsAddr: c.metrics, HealthAddr: c.health}).Validate(); err == nil {
				t.Errorf("Validate accepted metrics=%q health=%q", c.metrics, c.health)
			}
		})
	}
}

func TestLoopbackSpellingsAreAccepted(t *testing.T) {
	for _, m := range []string{"127.0.0.1:9001", "localhost:9001", "[::1]:9001", "127.0.0.2:9001"} {
		if err := (Options{MetricsAddr: m, HealthAddr: "127.0.0.1:9002"}).Validate(); err != nil {
			t.Errorf("%q refused: %v", m, err)
		}
	}
}

func TestNewRefusesAMissingConfigAndBadOptionsBeforeBindingAnything(t *testing.T) {
	if _, err := New(nil, DefaultOptions()); err == nil {
		t.Error("New accepted a nil rest.Config; it must never discover one")
	}
	if _, err := New(&rest.Config{Host: "https://127.0.0.1:1"}, Options{MetricsAddr: "0.0.0.0:8082", HealthAddr: "127.0.0.1:8083"}); err == nil {
		t.Error("New accepted a non-loopback metrics address")
	}
}

func TestSchemeRegistersCoreAndAgentPlaneTypesOnly(t *testing.T) {
	s, err := NewScheme()
	if err != nil {
		t.Fatal(err)
	}
	for _, gvk := range []schema.GroupVersionKind{
		{Group: "agentplane.io", Version: "v1alpha1", Kind: "Tenant"},
		{Group: "agentplane.io", Version: "v1alpha1", Kind: "TenantList"},
		{Group: "agentplane.io", Version: "v1alpha1", Kind: "Agent"},
		{Group: "agentplane.io", Version: "v1alpha1", Kind: "AgentList"},
		{Group: "", Version: "v1", Kind: "Namespace"},
		{Group: "", Version: "v1", Kind: "Pod"},
	} {
		if !s.Recognizes(gvk) {
			t.Errorf("the scheme does not recognize %s", gvk)
		}
	}
	if s.Recognizes(schema.GroupVersionKind{Group: "agentplane.io", Version: "v1beta1", Kind: "Tenant"}) {
		t.Error("the scheme recognizes an agentplane.io version that does not exist")
	}
}
