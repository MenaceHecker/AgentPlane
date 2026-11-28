package main

import (
	"strings"
	"testing"
)

func TestRefusesToGuessACluster(t *testing.T) {
	// No --kubeconfig and not in a cluster: it must not fall back to $KUBECONFIG or ~/.kube/config.
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")
	t.Setenv("KUBECONFIG", t.TempDir()+"/does-not-matter")
	err := run(nil)
	if err == nil || !strings.Contains(err.Error(), "refusing to guess a cluster") {
		t.Fatalf("run without --kubeconfig = %v, want a refusal to guess a cluster", err)
	}
}

func TestRefusesNonLoopbackAndConflictingAddressesBeforeLoadingAnyConfig(t *testing.T) {
	for _, args := range [][]string{
		{"--metrics-bind-address", "0.0.0.0:8082"},
		{"--health-probe-bind-address", ":8083"},
		{"--metrics-bind-address", "127.0.0.1:9000", "--health-probe-bind-address", "127.0.0.1:9000"},
		{"--health-probe-bind-address", "127.0.0.1:99999"},
	} {
		// A nonexistent --kubeconfig proves the address check happens first: that error would differ.
		err := run(append(args, "--kubeconfig", "/nonexistent/kubeconfig"))
		if err == nil || strings.Contains(err.Error(), "/nonexistent/kubeconfig") {
			t.Errorf("run(%v) = %v, want an address error before any kubeconfig is read", args, err)
		}
	}
}

func TestAMissingKubeconfigFileIsAnError(t *testing.T) {
	err := run([]string{"--kubeconfig", "/nonexistent/kubeconfig"})
	if err == nil || !strings.Contains(err.Error(), "/nonexistent/kubeconfig") {
		t.Fatalf("run = %v, want an error naming the missing file", err)
	}
}
