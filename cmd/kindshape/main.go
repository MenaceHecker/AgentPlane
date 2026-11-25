// Command kindshape checks a kind cluster's nodes against deploy/kind/config.yaml.
//
//	kindshape check-config --config deploy/kind/config.yaml
//	kubectl --context kind-NAME get nodes -o json | kindshape check-nodes --config deploy/kind/config.yaml
//
// Exit status: 0 compatible, 1 incompatible (every difference printed), 2 usage
// or input error. It never contacts a cluster and never changes anything.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	corev1 "k8s.io/api/core/v1"

	"github.com/MenaceHecker/AgentPlane/internal/kindshape"
)

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

func run(args []string, in io.Reader, out, errw io.Writer) int {
	if len(args) == 0 || (args[0] != "check-config" && args[0] != "check-nodes") {
		_, _ = fmt.Fprintln(errw, "usage: kindshape check-config|check-nodes --config FILE")
		return 2
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(errw)
	cfgPath := fs.String("config", "deploy/kind/config.yaml", "kind cluster config")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	data, err := os.ReadFile(*cfgPath)
	if err != nil {
		_, _ = fmt.Fprintf(errw, "kindshape: %v\n", err)
		return 2
	}
	exp, err := kindshape.ParseConfig(data)
	if err != nil {
		_, _ = fmt.Fprintf(errw, "kindshape: %v\n", err)
		return 1
	}
	if args[0] == "check-config" {
		_, _ = fmt.Fprintf(out, "kindshape: config OK (Kubernetes %s, %d control plane, pools %v)\n", exp.KubernetesVersion, exp.ControlPlanes, exp.Pools)
		return 0
	}
	raw, err := io.ReadAll(in)
	if err != nil {
		_, _ = fmt.Fprintf(errw, "kindshape: reading nodes: %v\n", err)
		return 2
	}
	var list corev1.NodeList
	// `kubectl get nodes -o json` prints kind "List" (a NodeList carries
	// "NodeList"); every item must be a Node.
	if err := json.Unmarshal(raw, &list); err != nil || (list.Kind != "List" && list.Kind != "NodeList") {
		_, _ = fmt.Fprintf(errw, "kindshape: stdin is not a kubectl node list (kind %q, error %v)\n", list.Kind, err)
		return 2
	}
	for i, n := range list.Items {
		if n.Kind != "Node" {
			_, _ = fmt.Fprintf(errw, "kindshape: item %d is kind %q, not Node\n", i, n.Kind)
			return 2
		}
	}
	if problems := kindshape.CheckNodes(exp, list.Items); len(problems) > 0 {
		for _, p := range problems {
			_, _ = fmt.Fprintf(errw, "kindshape: incompatible: %s\n", p)
		}
		return 1
	}
	_, _ = fmt.Fprintf(out, "kindshape: cluster matches the config (%d nodes)\n", len(list.Items))
	return 0
}
