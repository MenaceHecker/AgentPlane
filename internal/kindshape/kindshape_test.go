package kindshape

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const configPath = "../../deploy/kind/config.yaml"

func loadConfig(t *testing.T) *Expected {
	t.Helper()
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	exp, err := ParseConfig(data)
	if err != nil {
		t.Fatalf("the committed kind config is not the supported shape: %v", err)
	}
	return exp
}

// The committed topology is the one the plan approved: if someone changes the
// node count, pools, taint or version, this test fails and the change must be
// made deliberately (and re-approved), never silently.
func TestCommittedConfigIsTheApprovedTopology(t *testing.T) {
	exp := loadConfig(t)
	if exp.KubernetesVersion != "v1.35.0" || exp.ControlPlanes != 1 || exp.Pools["system"] != 1 || exp.Pools["tenant"] != 1 || len(exp.Pools) != 2 {
		t.Fatalf("unexpected topology: %+v", exp)
	}
	tt := exp.PoolTaints["tenant"]
	if len(tt) != 1 || tt[0].Key != "agentplane.io/pool" || tt[0].Value != "tenant" || tt[0].Effect != corev1.TaintEffectNoSchedule {
		t.Fatalf("tenant taint = %+v", tt)
	}
	if len(exp.PoolTaints["system"]) != 0 {
		t.Fatalf("system pool must be untainted: %+v", exp.PoolTaints["system"])
	}
}

func node(name, version string, labels map[string]string, taints ...corev1.Taint) corev1.Node {
	return corev1.Node{
		TypeMeta:   metav1.TypeMeta{Kind: "Node", APIVersion: "v1"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		Spec:       corev1.NodeSpec{Taints: taints},
		Status:     corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{KubeletVersion: version}},
	}
}

var (
	cpTaint     = corev1.Taint{Key: "node-role.kubernetes.io/control-plane", Effect: corev1.TaintEffectNoSchedule}
	tenantTaint = corev1.Taint{Key: "agentplane.io/pool", Value: "tenant", Effect: corev1.TaintEffectNoSchedule}
)

func good() []corev1.Node {
	return []corev1.Node{
		node("c-control-plane", "v1.35.0", map[string]string{"node-role.kubernetes.io/control-plane": ""}, cpTaint),
		node("c-worker", "v1.35.0", map[string]string{"agentplane.io/pool": "system"}),
		node("c-worker2", "v1.35.0", map[string]string{"agentplane.io/pool": "tenant"}, tenantTaint),
	}
}

func TestCheckNodes(t *testing.T) {
	exp := loadConfig(t)
	tests := []struct {
		name   string
		mutate func(n []corev1.Node) []corev1.Node
		want   string // substring of a reported problem; empty means compatible
	}{
		{"compatible cluster", func(n []corev1.Node) []corev1.Node { return n }, ""},
		{"default single-node cluster (an unlabeled default kind cluster)", func(n []corev1.Node) []corev1.Node { return n[:1] }, "expected 3 nodes, found 1"},
		{"missing tenant worker", func(n []corev1.Node) []corev1.Node { return n[:2] }, "expected 3 nodes, found 2"},
		{"extra worker", func(n []corev1.Node) []corev1.Node {
			return append(n, node("c-worker3", "v1.35.0", map[string]string{"agentplane.io/pool": "system"}))
		}, "expected 3 nodes, found 4"},
		{"tenant taint missing", func(n []corev1.Node) []corev1.Node { n[2].Spec.Taints = nil; return n }, "has agentplane.io/pool taints [], want [agentplane.io/pool=tenant:NoSchedule]"},
		{"tenant taint with the wrong effect", func(n []corev1.Node) []corev1.Node {
			n[2].Spec.Taints = []corev1.Taint{{Key: "agentplane.io/pool", Value: "tenant", Effect: corev1.TaintEffectPreferNoSchedule}}
			return n
		}, "PreferNoSchedule"},
		{"tenant taint with the wrong value", func(n []corev1.Node) []corev1.Node {
			n[2].Spec.Taints = []corev1.Taint{{Key: "agentplane.io/pool", Value: "system", Effect: corev1.TaintEffectNoSchedule}}
			return n
		}, "agentplane.io/pool=system:NoSchedule"},
		{"system worker wrongly tainted", func(n []corev1.Node) []corev1.Node { n[1].Spec.Taints = []corev1.Taint{tenantTaint}; return n }, "worker node c-worker (pool system)"},
		{"pool labels swapped", func(n []corev1.Node) []corev1.Node {
			n[1].Labels["agentplane.io/pool"], n[2].Labels["agentplane.io/pool"] = "tenant", "system"
			return n
		}, "worker node c-worker (pool tenant)"},
		{"both workers in the tenant pool", func(n []corev1.Node) []corev1.Node { n[1].Labels["agentplane.io/pool"] = "tenant"; return n }, "expected 1 worker(s) in pool system, found 0"},
		{"worker without a pool label", func(n []corev1.Node) []corev1.Node { n[1].Labels = map[string]string{}; return n }, "worker node c-worker has no agentplane.io/pool label"},
		{"unknown pool", func(n []corev1.Node) []corev1.Node { n[1].Labels["agentplane.io/pool"] = "gpu"; return n }, `unexpected pool "gpu"`},
		{"control plane carries a pool label", func(n []corev1.Node) []corev1.Node { n[0].Labels["agentplane.io/pool"] = "system"; return n }, "control-plane node c-control-plane must not carry agentplane.io/pool"},
		{"no control plane", func(n []corev1.Node) []corev1.Node { n[0].Labels = map[string]string{}; return n }, "expected 1 control-plane node(s), found 0"},
		{"wrong kubelet version", func(n []corev1.Node) []corev1.Node { n[1].Status.NodeInfo.KubeletVersion = "v1.34.3"; return n }, `runs kubelet "v1.34.3", config pins v1.35.0`},
		{"no nodes at all", func(n []corev1.Node) []corev1.Node { return nil }, "expected 3 nodes, found 0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			problems := CheckNodes(exp, tt.mutate(good()))
			if tt.want == "" {
				if len(problems) != 0 {
					t.Fatalf("a compatible cluster was reported incompatible: %v", problems)
				}
				return
			}
			if len(problems) == 0 {
				t.Fatalf("an incompatible cluster was accepted (want a problem containing %q)", tt.want)
			}
			if !strings.Contains(strings.Join(problems, "\n"), tt.want) {
				t.Fatalf("want a problem containing %q, got %v", tt.want, problems)
			}
		})
	}
}

// A kubectl NodeList round-trips through JSON exactly as the CLI reads it.
func TestNodeListJSONRoundTrip(t *testing.T) {
	list := corev1.NodeList{TypeMeta: metav1.TypeMeta{Kind: "NodeList", APIVersion: "v1"}, Items: good()}
	raw, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	var back corev1.NodeList
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if problems := CheckNodes(loadConfig(t), back.Items); len(problems) != 0 {
		t.Fatalf("round-tripped list was reported incompatible: %v", problems)
	}
}

func TestParseConfigRejectsUnsupportedShapes(t *testing.T) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	base := string(data)
	img := "kindest/node:v1.35.0@sha256:452d707d4862f52530247495d180205e029056831160e22870e37e3f6c1ac31f"
	tests := []struct {
		name, from, to, want string
	}{
		{"tag without a digest", img, "kindest/node:v1.35.0", "pinned by an @sha256 digest"},
		{"floating latest tag", img, "kindest/node:latest", "pinned by an @sha256 digest"},
		{"another image repository", img, "evil/node:v1.35.0@sha256:452d707d4862f52530247495d180205e029056831160e22870e37e3f6c1ac31f", "pinned by an @sha256 digest"},
		{"unknown pool", "agentplane.io/pool: system", "agentplane.io/pool: gpu", "must carry agentplane.io/pool=system or tenant"},
		{"tenant taint removed", "effect: NoSchedule", "effect: PreferNoSchedule", "tenant worker must carry exactly the taint"},
		{"wrong apiVersion", "kind.x-k8s.io/v1alpha4", "kind.x-k8s.io/v1alpha3", "must be kind.x-k8s.io/v1alpha4 Cluster"},
		{"unknown field", "role: control-plane", "role: control-plane\n    surprise: true", "kind config is not valid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mutated := strings.Replace(base, tt.from, tt.to, 1)
			if mutated == base {
				t.Fatal("test error: the mutation did not change the config")
			}
			if tt.name == "tag without a digest" || tt.name == "floating latest tag" || tt.name == "another image repository" {
				mutated = strings.ReplaceAll(base, tt.from, tt.to)
			}
			_, err := ParseConfig([]byte(mutated))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want an error containing %q, got %v", tt.want, err)
			}
		})
	}
	t.Run("a mixed-version cluster", func(t *testing.T) {
		mutated := strings.Replace(base, img, "kindest/node:v1.34.3@sha256:08497ee19eace7b4b5348db5c6a1591d7752b164530a36f855cb0f2bdcbadd48", 1)
		if _, err := ParseConfig([]byte(mutated)); err == nil || !strings.Contains(err.Error(), "every node must use one version") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("an extra worker", func(t *testing.T) {
		mutated := base + "  - role: worker\n    image: " + img + "\n    labels:\n      agentplane.io/pool: system\n"
		if _, err := ParseConfig([]byte(mutated)); err == nil || !strings.Contains(err.Error(), "exactly one system and one tenant worker") {
			t.Fatalf("got %v", err)
		}
	})
}
