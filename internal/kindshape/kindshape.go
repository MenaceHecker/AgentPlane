// Package kindshape checks that a kind cluster has the topology that
// deploy/kind/config.yaml declares: one control plane and two workers carrying
// the agentplane.io/pool label (system and tenant), the tenant worker tainted
// NoSchedule, all at the configured Kubernetes version. The expectations are
// derived from the config file itself, so the config and the check cannot
// drift apart. It reads data only: it never talks to a cluster.
package kindshape

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
)

// Placement labels and roles used by the topology.
const (
	PoolLabel        = "agentplane.io/pool"
	controlPlaneRole = "node-role.kubernetes.io/control-plane"
)

type clusterConfig struct {
	Kind       string       `json:"kind"`
	APIVersion string       `json:"apiVersion"`
	Nodes      []nodeConfig `json:"nodes"`
}

type nodeConfig struct {
	Role                 string            `json:"role"`
	Image                string            `json:"image"`
	Labels               map[string]string `json:"labels"`
	KubeadmConfigPatches []string          `json:"kubeadmConfigPatches"`
}

type kubeadmPatch struct {
	Kind             string `json:"kind"`
	NodeRegistration struct {
		Taints []corev1.Taint `json:"taints"`
	} `json:"nodeRegistration"`
}

// Expected is the topology derived from a kind config.
type Expected struct {
	KubernetesVersion string                    // for example v1.35.0
	ControlPlanes     int                       // number of control-plane nodes
	Pools             map[string]int            // worker count per pool label value
	PoolTaints        map[string][]corev1.Taint // taints each pool's workers must carry (key PoolLabel)
}

var imageRE = regexp.MustCompile(`^kindest/node:(v[0-9]+\.[0-9]+\.[0-9]+)@sha256:[0-9a-f]{64}$`)

// ParseConfig derives the expected topology from a kind v1alpha4 Cluster config
// and rejects anything that is not the supported shape.
func ParseConfig(data []byte) (*Expected, error) {
	var cfg clusterConfig
	if err := yaml.UnmarshalStrict(data, &cfg); err != nil {
		return nil, fmt.Errorf("kind config is not valid: %w", err)
	}
	if cfg.Kind != "Cluster" || cfg.APIVersion != "kind.x-k8s.io/v1alpha4" {
		return nil, fmt.Errorf("kind config must be kind.x-k8s.io/v1alpha4 Cluster, got %s %s", cfg.APIVersion, cfg.Kind)
	}
	exp := &Expected{Pools: map[string]int{}, PoolTaints: map[string][]corev1.Taint{}}
	var errs []error
	for i, n := range cfg.Nodes {
		m := imageRE.FindStringSubmatch(n.Image)
		if m == nil {
			errs = append(errs, fmt.Errorf("nodes[%d].image %q must be kindest/node:vX.Y.Z pinned by an @sha256 digest", i, n.Image))
			continue
		}
		if exp.KubernetesVersion == "" {
			exp.KubernetesVersion = m[1]
		} else if exp.KubernetesVersion != m[1] {
			errs = append(errs, fmt.Errorf("nodes[%d].image is %s but earlier nodes use %s: every node must use one version", i, m[1], exp.KubernetesVersion))
		}
		switch n.Role {
		case "control-plane":
			exp.ControlPlanes++
			if _, ok := n.Labels[PoolLabel]; ok {
				errs = append(errs, fmt.Errorf("nodes[%d]: the control plane must not carry %s", i, PoolLabel))
			}
		case "worker":
			pool := n.Labels[PoolLabel]
			if pool != "system" && pool != "tenant" {
				errs = append(errs, fmt.Errorf("nodes[%d]: a worker must carry %s=system or tenant, got %q", i, PoolLabel, pool))
				continue
			}
			exp.Pools[pool]++
			for _, raw := range n.KubeadmConfigPatches {
				var p kubeadmPatch
				if err := yaml.Unmarshal([]byte(raw), &p); err != nil {
					errs = append(errs, fmt.Errorf("nodes[%d]: kubeadm patch is not valid YAML: %w", i, err))
					continue
				}
				for _, t := range p.NodeRegistration.Taints {
					if t.Key == PoolLabel {
						exp.PoolTaints[pool] = append(exp.PoolTaints[pool], t)
					}
				}
			}
		default:
			errs = append(errs, fmt.Errorf("nodes[%d].role %q must be control-plane or worker", i, n.Role))
		}
	}
	if exp.ControlPlanes != 1 {
		errs = append(errs, fmt.Errorf("want exactly one control-plane node, config has %d", exp.ControlPlanes))
	}
	if exp.Pools["system"] != 1 || exp.Pools["tenant"] != 1 || len(exp.Pools) != 2 {
		errs = append(errs, fmt.Errorf("want exactly one system and one tenant worker, config has %v", exp.Pools))
	}
	if t := exp.PoolTaints["tenant"]; len(t) != 1 || t[0].Value != "tenant" || t[0].Effect != corev1.TaintEffectNoSchedule {
		errs = append(errs, fmt.Errorf("the tenant worker must carry exactly the taint %s=tenant:NoSchedule, config has %v", PoolLabel, t))
	}
	if len(exp.PoolTaints["system"]) != 0 {
		errs = append(errs, fmt.Errorf("the system worker must not carry a %s taint", PoolLabel))
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return exp, nil
}

// CheckNodes compares the nodes of a running cluster with the expected shape and
// returns every difference (empty when the cluster is compatible).
func CheckNodes(exp *Expected, nodes []corev1.Node) []string {
	var problems []string
	want := exp.ControlPlanes
	for _, c := range exp.Pools {
		want += c
	}
	if len(nodes) != want {
		problems = append(problems, fmt.Sprintf("expected %d nodes, found %d", want, len(nodes)))
	}
	cp := 0
	pools := map[string]int{}
	for _, n := range nodes {
		name := n.Name
		if v := n.Status.NodeInfo.KubeletVersion; v != exp.KubernetesVersion {
			problems = append(problems, fmt.Sprintf("node %s runs kubelet %q, config pins %s", name, v, exp.KubernetesVersion))
		}
		_, isCP := n.Labels[controlPlaneRole]
		pool, hasPool := n.Labels[PoolLabel]
		var poolTaints []corev1.Taint
		for _, t := range n.Spec.Taints {
			if t.Key == PoolLabel {
				poolTaints = append(poolTaints, t)
			}
		}
		switch {
		case isCP:
			cp++
			if hasPool {
				problems = append(problems, fmt.Sprintf("control-plane node %s must not carry %s (has %q)", name, PoolLabel, pool))
			}
			if len(poolTaints) > 0 {
				problems = append(problems, fmt.Sprintf("control-plane node %s must not carry a %s taint", name, PoolLabel))
			}
		case !hasPool:
			problems = append(problems, fmt.Sprintf("worker node %s has no %s label", name, PoolLabel))
		default:
			pools[pool]++
			if _, inPlan := exp.Pools[pool]; !inPlan {
				problems = append(problems, fmt.Sprintf("worker node %s has unexpected pool %q", name, pool))
				continue
			}
			if wantTaints := exp.PoolTaints[pool]; !sameTaints(wantTaints, poolTaints) {
				problems = append(problems, fmt.Sprintf("worker node %s (pool %s) has %s taints %s, want %s", name, pool, PoolLabel, fmtTaints(poolTaints), fmtTaints(wantTaints)))
			}
		}
	}
	if cp != exp.ControlPlanes {
		problems = append(problems, fmt.Sprintf("expected %d control-plane node(s), found %d", exp.ControlPlanes, cp))
	}
	for pool, c := range exp.Pools {
		if pools[pool] != c {
			problems = append(problems, fmt.Sprintf("expected %d worker(s) in pool %s, found %d", c, pool, pools[pool]))
		}
	}
	sort.Strings(problems)
	return problems
}

func sameTaints(a, b []corev1.Taint) bool {
	if len(a) != len(b) {
		return false
	}
	ka, kb := taintKeys(a), taintKeys(b)
	for i := range ka {
		if ka[i] != kb[i] {
			return false
		}
	}
	return true
}

func taintKeys(ts []corev1.Taint) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Key + "=" + t.Value + ":" + string(t.Effect)
	}
	sort.Strings(out)
	return out
}

func fmtTaints(ts []corev1.Taint) string {
	if len(ts) == 0 {
		return "[]"
	}
	return "[" + strings.Join(taintKeys(ts), ", ") + "]"
}
