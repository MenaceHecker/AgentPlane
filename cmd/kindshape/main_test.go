package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

const cfg = "../../deploy/kind/config.yaml"

func runWith(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var out, errw bytes.Buffer
	rc := run(args, strings.NewReader(stdin), &out, &errw)
	return rc, out.String(), errw.String()
}

// The fixture is the real `kubectl get nodes -o json` output of a cluster that
// `make kind-up` created with kind v0.31.0 (kind "List", not "NodeList"),
// trimmed to the fields the check reads. The first version of this tool only
// accepted "NodeList" and rejected a healthy cluster; this pins the real shape.
func TestRealKubectlOutputIsCompatible(t *testing.T) {
	raw, err := os.ReadFile("../../internal/kindshape/testdata/kubectl-get-nodes.json")
	if err != nil {
		t.Fatal(err)
	}
	rc, out, errw := runWith(t, string(raw), "check-nodes", "--config", cfg)
	if rc != 0 || !strings.Contains(out, "matches the config (3 nodes)") {
		t.Fatalf("rc=%d out=%q err=%q", rc, out, errw)
	}
}

func TestExitCodes(t *testing.T) {
	list := func(items string) string { return `{"apiVersion":"v1","kind":"List","items":[` + items + `]}` }
	tests := []struct {
		name  string
		args  []string
		stdin string
		want  int
		text  string
	}{
		{"no subcommand", nil, "", 2, "usage"},
		{"unknown subcommand", []string{"nope"}, "", 2, "usage"},
		{"config check passes", []string{"check-config", "--config", cfg}, "", 0, "config OK"},
		{"missing config file", []string{"check-config", "--config", "/nonexistent/x.yaml"}, "", 2, "no such file"},
		{"empty stdin", []string{"check-nodes", "--config", cfg}, "", 2, "not a kubectl node list"},
		{"not JSON", []string{"check-nodes", "--config", cfg}, "{nope", 2, "not a kubectl node list"},
		{"another kind of document", []string{"check-nodes", "--config", cfg}, `{"kind":"PodList","items":[]}`, 2, `kind "PodList"`},
		{"an item that is not a Node", []string{"check-nodes", "--config", cfg}, list(`{"kind":"Pod","metadata":{"name":"p"}}`), 2, "not Node"},
		{"an empty List is incompatible, not an error", []string{"check-nodes", "--config", cfg}, list(``), 1, "expected 3 nodes, found 0"},
		{"a legacy NodeList kind is still read", []string{"check-nodes", "--config", cfg}, `{"kind":"NodeList","items":[]}`, 1, "expected 3 nodes"},
		{"unknown flag", []string{"check-nodes", "--bogus"}, "", 2, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rc, out, errw := runWith(t, tt.stdin, tt.args...)
			if rc != tt.want || !strings.Contains(out+errw, tt.text) {
				t.Fatalf("rc=%d (want %d); out=%q err=%q (want text %q)", rc, tt.want, out, errw, tt.text)
			}
		})
	}
}
