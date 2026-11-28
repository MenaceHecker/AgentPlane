package apitest

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/MenaceHecker/AgentPlane/api/v1alpha1"
)

func agentsPath(ns string) string { return "/apis/agentplane.io/v1alpha1/namespaces/" + ns + "/agents" }

func validAgent(name string) map[string]any {
	return map[string]any{
		"apiVersion": "agentplane.io/v1alpha1",
		"kind":       "Agent",
		"metadata":   map[string]any{"name": name},
		"spec": map[string]any{
			"image":     "registry.example.invalid/team-a/agent:1.0.0",
			"model":     "mock-large",
			"budget":    map[string]any{"tokensPerDay": 5000, "requestsPerMinute": 10},
			"resources": map[string]any{"requests": map[string]any{"cpu": "100m", "memory": "128Mi"}, "limits": map[string]any{"cpu": "500m", "memory": "256Mi"}},
		},
	}
}

func (s *testSuite) createAgent(t *testing.T, ns string, obj map[string]any) response {
	t.Helper()
	r := s.postJSON(t, agentsPath(ns), obj)
	if r.Code == http.StatusCreated {
		name := obj["metadata"].(map[string]any)["name"].(string)
		t.Cleanup(func() { s.delete(t, agentsPath(ns)+"/"+name) })
	}
	return r
}

// namespace creates a disposable namespace on the envtest API server.
func (s *testSuite) namespace(t *testing.T) string {
	t.Helper()
	name := s.unique("agent-ns")
	r := s.postJSON(t, "/api/v1/namespaces", map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": name}})
	if r.Code != http.StatusCreated {
		t.Fatalf("creating namespace %s: %d %s", name, r.Code, r.Body)
	}
	return name
}

func TestAgentCRDIsStructuralNamespacedAndEstablished(t *testing.T) {
	defer guard(t)
	s := need(t)
	crd, err := s.ext.ApiextensionsV1().CustomResourceDefinitions().Get(context.Background(), "agents.agentplane.io", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if crd.Spec.Scope != apiextensionsv1.NamespaceScoped {
		t.Errorf("scope = %s, want Namespaced", crd.Spec.Scope)
	}
	n := crd.Spec.Names
	if n.Plural != "agents" || n.Singular != "agent" || n.Kind != "Agent" || n.ListKind != "AgentList" || crd.Spec.Group != "agentplane.io" {
		t.Errorf("names/group = %+v / %s", n, crd.Spec.Group)
	}
	if crd.Spec.PreserveUnknownFields {
		t.Error("preserveUnknownFields must be false for a structural v1 CRD")
	}
	if len(crd.Spec.Versions) != 1 {
		t.Fatalf("want exactly one version, got %d", len(crd.Spec.Versions))
	}
	v := crd.Spec.Versions[0]
	if v.Name != "v1alpha1" || !v.Served || !v.Storage {
		t.Errorf("version = %+v", v)
	}
	if v.Subresources == nil || v.Subresources.Status == nil {
		t.Error("the status subresource must be enabled")
	}
	var cols []string
	for _, c := range v.AdditionalPrinterColumns {
		cols = append(cols, c.Name)
	}
	if strings.Join(cols, ",") != "Replicas,Ready,Age" {
		t.Errorf("printer columns = %v", cols)
	}
	conds := map[apiextensionsv1.CustomResourceDefinitionConditionType]apiextensionsv1.ConditionStatus{}
	for _, c := range crd.Status.Conditions {
		conds[c.Type] = c.Status
	}
	if conds[apiextensionsv1.Established] != apiextensionsv1.ConditionTrue {
		t.Errorf("CRD not Established: %v", conds)
	}
	if conds[apiextensionsv1.NonStructuralSchema] == apiextensionsv1.ConditionTrue {
		t.Error("the schema was reported non-structural")
	}
}

func TestAgentValidObjectIsAcceptedAndStoredExactly(t *testing.T) {
	defer guard(t)
	s := need(t)
	created := requireCreated(t, s.createAgent(t, "default", validAgent(s.unique("agent-valid"))))
	for path, want := range map[string]string{
		"image":                    "registry.example.invalid/team-a/agent:1.0.0",
		"model":                    "mock-large",
		"budget.tokensPerDay":      "5000",
		"budget.requestsPerMinute": "10",
		"resources.requests.cpu":   "100m",
		"resources.limits.memory":  "256Mi",
	} {
		got, ok := dig(created, append([]string{"spec"}, strings.Split(path, ".")...)...)
		if !ok || toString(got) != want {
			t.Errorf("spec.%s = %v (present %v), want %s", path, got, ok, want)
		}
	}
}

func TestAgentReplicasAbsentStaysAbsentAndExplicitZeroIsKept(t *testing.T) {
	defer guard(t)
	s := need(t)
	created := requireCreated(t, s.createAgent(t, "default", validAgent(s.unique("agent-noreplicas"))))
	if v, ok := dig(created, "spec", "replicas"); ok {
		t.Errorf("replicas was defaulted to %v; the CRD must not default it", v)
	}
	zero := with(validAgent(s.unique("agent-zero")), 0, "spec", "replicas")
	name := zero["metadata"].(map[string]any)["name"].(string)
	created = requireCreated(t, s.createAgent(t, "default", zero))
	if v, ok := dig(created, "spec", "replicas"); !ok || toString(v) != "0" {
		t.Errorf("explicit replicas 0 = %v (present %v)", v, ok)
	}
	stored := decode(t, s.get(t, agentsPath("default")+"/"+name).Body)
	if v, ok := dig(stored, "spec", "replicas"); !ok || toString(v) != "0" {
		t.Errorf("stored replicas = %v (present %v), want an explicit 0", v, ok)
	}
	// Zero budgets are real limits and are kept too.
	b := with(with(validAgent(s.unique("agent-zerobudget")), 0, "spec", "budget", "tokensPerDay"), 0, "spec", "budget", "requestsPerMinute")
	created = requireCreated(t, s.createAgent(t, "default", b))
	for _, f := range []string{"tokensPerDay", "requestsPerMinute"} {
		if v, ok := dig(created, "spec", "budget", f); !ok || toString(v) != "0" {
			t.Errorf("budget.%s = %v (present %v), want an explicit 0", f, v, ok)
		}
	}
}

func TestAgentToolsAndSandboxClassAreOptional(t *testing.T) {
	defer guard(t)
	s := need(t)
	created := requireCreated(t, s.createAgent(t, "default", validAgent(s.unique("agent-opt"))))
	for _, f := range []string{"tools", "sandboxClass", "rollout", "replicas"} {
		if _, ok := dig(created, "spec", f); ok {
			t.Errorf("optional spec.%s appeared although it was omitted", f)
		}
	}
	empty := with(validAgent(s.unique("agent-emptytools")), []any{}, "spec", "tools")
	created = requireCreated(t, s.createAgent(t, "default", empty))
	if v, ok := dig(created, "spec", "tools"); !ok || len(v.([]any)) != 0 {
		t.Errorf("empty tools = %v (present %v)", v, ok)
	}
	hardened := with(validAgent(s.unique("agent-hardened")), "hardened", "spec", "sandboxClass")
	created = requireCreated(t, s.createAgent(t, "default", hardened))
	if v, _ := dig(created, "spec", "sandboxClass"); v != "hardened" {
		t.Errorf("sandboxClass = %v", v)
	}
}

func TestAgentMissingRequiredFieldsAreRejectedWithNamedCauses(t *testing.T) {
	defer guard(t)
	s := need(t)
	for _, path := range [][]string{
		{"spec"},
		{"spec", "image"},
		{"spec", "model"},
		{"spec", "budget"},
		{"spec", "resources"},
		{"spec", "budget", "tokensPerDay"},
		{"spec", "budget", "requestsPerMinute"},
	} {
		field := strings.Join(path, ".")
		t.Run("omit "+field, func(t *testing.T) {
			defer guard(t)
			obj := without(validAgent(s.unique("agent-missing")), path...)
			requireRejected(t, s.createAgent(t, "default", obj), field)
		})
	}
}

func TestAgentOutOfRangeAndWrongTypedValuesAreRejected(t *testing.T) {
	defer guard(t)
	s := need(t)
	big := func(v string) json.Number { return json.Number(v) }
	cases := []struct {
		name  string
		path  []string
		value any
		field string // the field path the server names, when it differs from the path
	}{
		{"empty image", []string{"spec", "image"}, "", ""},
		{"empty model", []string{"spec", "model"}, "", ""},
		{"numeric image", []string{"spec", "image"}, 7, ""},
		{"negative replicas", []string{"spec", "replicas"}, -1, ""},
		{"replicas above int32", []string{"spec", "replicas"}, big("2147483648"), ""},
		{"fractional replicas", []string{"spec", "replicas"}, big("1.5"), ""},
		{"string replicas", []string{"spec", "replicas"}, "3", ""},
		{"negative tokensPerDay", []string{"spec", "budget", "tokensPerDay"}, -1, ""},
		{"negative requestsPerMinute", []string{"spec", "budget", "requestsPerMinute"}, -1, ""},
		{"tokensPerDay above int64", []string{"spec", "budget", "tokensPerDay"}, big("9223372036854775808"), ""},
		{"requestsPerMinute above int32", []string{"spec", "budget", "requestsPerMinute"}, big("2147483648"), ""},
		{"string tokensPerDay", []string{"spec", "budget", "tokensPerDay"}, "5", ""},
		{"unsupported sandbox class", []string{"spec", "sandboxClass"}, "kata", ""},
		{"empty sandbox class", []string{"spec", "sandboxClass"}, "", ""},
		{"resources that is not an object", []string{"spec", "resources"}, "big", ""},
		{"cpu request that is not a quantity", []string{"spec", "resources", "requests", "cpu"}, "lots", ""},
		{"boolean memory limit", []string{"spec", "resources", "limits", "memory"}, true, ""},
		{"object tools", []string{"spec", "tools"}, map[string]any{"a": 1}, ""},
		{"numeric tools item", []string{"spec", "tools"}, []any{1}, "spec.tools[0]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			defer guard(t)
			obj := with(validAgent(s.unique("agent-bad")), c.value, c.path...)
			field := c.field
			if field == "" {
				field = strings.Join(c.path, ".")
			}
			requireRejected(t, s.createAgent(t, "default", obj), field)
		})
	}
}

func TestAgentRolloutIsOptionalTypedDataWithItsOwnRequiredFields(t *testing.T) {
	defer guard(t)
	s := need(t)
	rollout := map[string]any{"strategy": "canary", "steps": []any{10, 50, 100}, "slo": map[string]any{"errorRate": "<1%", "p95LatencyMs": 500}}
	created := requireCreated(t, s.createAgent(t, "default", with(validAgent(s.unique("agent-rollout")), rollout, "spec", "rollout")))
	if v, ok := dig(created, "spec", "rollout", "slo", "p95LatencyMs"); !ok || toString(v) != "500" {
		t.Errorf("rollout.slo.p95LatencyMs = %v (present %v)", v, ok)
	}
	for _, path := range [][]string{{"strategy"}, {"steps"}, {"slo"}, {"slo", "errorRate"}, {"slo", "p95LatencyMs"}} {
		t.Run("omit rollout."+strings.Join(path, "."), func(t *testing.T) {
			defer guard(t)
			r := with(validAgent(s.unique("agent-rollout-missing")), rollout, "spec", "rollout")
			full := append([]string{"spec", "rollout"}, path...)
			requireRejected(t, s.createAgent(t, "default", without(r, full...)), strings.Join(full, "."))
		})
	}
	wrongType := with(with(validAgent(s.unique("agent-rollout-type")), rollout, "spec", "rollout"), "x", "spec", "rollout", "steps")
	requireRejected(t, s.createAgent(t, "default", wrongType), "spec.rollout.steps")
	// The schema accepts a rollout; nothing here evaluates it (no controller, no canary).
}

// The schema checks that resources are present and quantity-shaped; it does not judge them. These
// tests pin that documented boundary so a later policy change is a deliberate one. They are NOT a
// statement that an empty or negative resource spec is acceptable: that policy is later work.
func TestAgentResourcePolicyIsDeferredSchemaOnlyChecksTheShape(t *testing.T) {
	defer guard(t)
	s := need(t)
	requireCreated(t, s.createAgent(t, "default", with(validAgent(s.unique("agent-emptyres")), map[string]any{}, "spec", "resources")))
	requireCreated(t, s.createAgent(t, "default", with(validAgent(s.unique("agent-negres")), "-1", "spec", "resources", "requests", "cpu")))
}

func TestAgentUnknownFieldsArePrunedNotRejected(t *testing.T) {
	defer guard(t)
	s := need(t)
	obj := with(validAgent(s.unique("agent-prune")), "x", "spec", "notInTheSchema")
	created := requireCreated(t, s.createAgent(t, "default", obj))
	if _, ok := dig(created, "spec", "notInTheSchema"); ok {
		t.Error("an unknown spec field was stored; the structural schema should prune it")
	}
}

func TestAgentIsNamespacedAndSameNameCanLiveInTwoNamespaces(t *testing.T) {
	defer guard(t)
	s := need(t)
	// Not creatable without a namespace.
	if r := s.postJSON(t, "/apis/agentplane.io/v1alpha1/agents", validAgent(s.unique("agent-nons"))); r.Code == http.StatusCreated {
		t.Errorf("an Agent was created without a namespace: %s", r.Body)
	}
	a, b := s.namespace(t), s.namespace(t)
	name := s.unique("agent-shared")
	requireCreated(t, s.createAgent(t, a, validAgent(name)))
	requireCreated(t, s.createAgent(t, b, validAgent(name)))
	if r := s.get(t, agentsPath(b)+"/"+name); r.Code != http.StatusOK {
		t.Errorf("the second namespace's Agent is not readable: %d", r.Code)
	}
	list := decode(t, s.get(t, "/apis/agentplane.io/v1alpha1").Body)
	for _, res := range list["resources"].([]any) {
		if m := res.(map[string]any); m["name"] == "agents" && (m["namespaced"] != true || m["kind"] != "Agent") {
			t.Errorf("agents resource = %v", m)
		}
	}
}

func TestAgentStatusIsWrittenOnlyThroughTheStatusSubresource(t *testing.T) {
	defer guard(t)
	s := need(t)
	name := s.unique("agent-status")
	requireCreated(t, s.createAgent(t, "default", validAgent(name)))
	path := agentsPath("default") + "/" + name
	cond := func(typ, st string) map[string]any {
		return map[string]any{"type": typ, "status": st, "reason": "Test", "message": "", "lastTransitionTime": time.Now().UTC().Format(time.RFC3339)}
	}
	cur := decode(t, s.get(t, path).Body)
	cur["status"] = map[string]any{"conditions": []any{cond("Ready", "True")}}
	cur = with(cur, 2, "spec", "replicas")
	r := s.put(t, path, cur)
	if r.Code != http.StatusOK {
		t.Fatalf("ordinary update: %d %s", r.Code, r.Body)
	}
	after := decode(t, r.Body)
	if _, ok := dig(after, "status", "conditions"); ok {
		t.Error("an ordinary update wrote status.conditions; only the status subresource may")
	}
	gen1, _ := dig(after, "metadata", "generation")

	cur = decode(t, s.get(t, path).Body)
	cur["status"] = map[string]any{"observedGeneration": 1, "conditions": []any{cond("Ready", "True")}}
	cur = with(cur, 9, "spec", "replicas")
	r = s.put(t, path+"/status", cur)
	if r.Code != http.StatusOK {
		t.Fatalf("status update: %d %s", r.Code, r.Body)
	}
	after = decode(t, r.Body)
	if v, _ := dig(after, "status", "conditions"); len(v.([]any)) != 1 {
		t.Errorf("the status update did not store the condition: %s", r.Body)
	}
	if v, _ := dig(after, "spec", "replicas"); toString(v) != "2" {
		t.Errorf("the status update changed spec.replicas to %v", v)
	}
	if gen2, _ := dig(after, "metadata", "generation"); toString(gen2) != toString(gen1) {
		t.Errorf("generation moved from %v to %v on a status-only update", gen1, gen2)
	}

	cur = decode(t, s.get(t, path).Body)
	cur = with(cur, 3, "spec", "replicas")
	after = decode(t, s.put(t, path, cur).Body)
	if gen3, _ := dig(after, "metadata", "generation"); toString(gen3) == toString(gen1) {
		t.Errorf("generation did not move on a spec change (still %v)", gen3)
	}
}

func TestAgentConditionsAreAListMapKeyedByType(t *testing.T) {
	defer guard(t)
	s := need(t)
	name := s.unique("agent-cond")
	requireCreated(t, s.createAgent(t, "default", validAgent(name)))
	base := agentsPath("default") + "/" + name
	cond := func(typ, st string) map[string]any {
		return map[string]any{"type": typ, "status": st, "reason": "Test", "message": "m", "lastTransitionTime": time.Now().UTC().Format(time.RFC3339)}
	}
	put := func(conds ...any) response {
		cur := decode(t, s.get(t, base).Body)
		cur["status"] = map[string]any{"conditions": conds}
		return s.put(t, base+"/status", cur)
	}
	requireRejected(t, put(cond("Ready", "True"), cond("Ready", "False")), "status.conditions[1]")
	if r := put(cond("Ready", "True"), cond("BudgetExhausted", "False")); r.Code != http.StatusOK {
		t.Errorf("two different condition types were refused: %d %s", r.Code, r.Body)
	}
	requireRejected(t, put(cond("Ready", "Maybe")), "status.conditions[0].status")
}

func TestAgentTableLeavesUnknownReplicasAndReadyBlank(t *testing.T) {
	defer guard(t)
	s := need(t)
	name := s.unique("agent-table")
	requireCreated(t, s.createAgent(t, "default", validAgent(name)))
	path := agentsPath("default") + "/" + name
	table := func() ([]string, []any) {
		tbl := decode(t, s.do(t, http.MethodGet, path, "", tableAcceptJSON, nil).Body)
		var cols []string
		for _, c := range tbl["columnDefinitions"].([]any) {
			cols = append(cols, c.(map[string]any)["name"].(string))
		}
		return cols, tbl["rows"].([]any)[0].(map[string]any)["cells"].([]any)
	}
	cols, cells := table()
	if strings.Join(cols, ",") != "Name,Replicas,Ready,Age" {
		t.Fatalf("columns = %v", cols)
	}
	for i, n := range []string{"Replicas", "Ready"} {
		if v := cells[i+1]; v != nil && v != "" {
			t.Errorf("%s cell is %v with no value to show; it must stay blank", n, v)
		}
	}
	cur := decode(t, s.get(t, path).Body)
	cur = with(cur, 4, "spec", "replicas")
	if r := s.put(t, path, cur); r.Code != http.StatusOK {
		t.Fatalf("update: %d %s", r.Code, r.Body)
	}
	_, cells = table()
	if fmt.Sprint(cells[1]) != "4" {
		t.Errorf("Replicas cell = %v, want 4 once set", cells[1])
	}
}

// TestAgentRolloutInt32FieldsCoverTheWholeGoRangeAndNothingMore pins the representability contract of
// rollout.steps items and rollout.slo.p95LatencyMs: the CRD must accept exactly what the existing Go
// int32 fields can hold, and the stored object must decode into the unchanged typed Agent. The API
// server does not enforce format int32 here, so without explicit bounds 2147483648 is admitted (201)
// and then cannot be decoded by a typed client. No value policy (nonnegative steps, positive
// latency) is imposed: negative values inside the int32 range stay accepted.
func TestAgentRolloutInt32FieldsCoverTheWholeGoRangeAndNothingMore(t *testing.T) {
	defer guard(t)
	s := need(t)
	rollout := func(steps, p95 json.Number) map[string]any {
		return map[string]any{"strategy": "canary", "steps": []any{steps}, "slo": map[string]any{"errorRate": "<1%", "p95LatencyMs": p95}}
	}
	fields := []struct {
		name  string
		field string // the cause path the server must name
		build func(v json.Number) map[string]any
		read  func(a *v1alpha1.Agent) int32
	}{
		{"steps item", "spec.rollout.steps[0]", func(v json.Number) map[string]any { return rollout(v, "500") },
			func(a *v1alpha1.Agent) int32 { return a.Spec.Rollout.Steps[0] }},
		{"p95LatencyMs", "spec.rollout.slo.p95LatencyMs", func(v json.Number) map[string]any { return rollout("10", v) },
			func(a *v1alpha1.Agent) int32 { return a.Spec.Rollout.SLO.P95LatencyMs }},
	}
	valid := []struct {
		name string
		v    string
		want int32
	}{{"int32 minimum", "-2147483648", math.MinInt32}, {"just above int32 minimum", "-2147483647", math.MinInt32 + 1}, {"zero", "0", 0}, {"int32 maximum", "2147483647", math.MaxInt32}}
	invalid := []struct{ name, v string }{{"just below int32 minimum", "-2147483649"}, {"just above int32 maximum", "2147483648"}}
	for _, f := range fields {
		for _, c := range valid {
			t.Run(f.name+" accepts "+c.name, func(t *testing.T) {
				defer guard(t)
				obj := with(validAgent(s.unique("agent-int32")), f.build(json.Number(c.v)), "spec", "rollout")
				name := obj["metadata"].(map[string]any)["name"].(string)
				requireCreated(t, s.createAgent(t, "default", obj))
				got := s.get(t, agentsPath("default")+"/"+name)
				if got.Code != http.StatusOK {
					t.Fatalf("GET %s: %d %s", name, got.Code, got.Body)
				}
				var a v1alpha1.Agent
				if err := json.Unmarshal(got.Body, &a); err != nil {
					t.Fatalf("stored Agent does not decode into the typed API: %v\n%s", err, got.Body)
				}
				if a.Spec.Rollout == nil || len(a.Spec.Rollout.Steps) != 1 {
					t.Fatalf("typed rollout lost: %+v", a.Spec.Rollout)
				}
				if v := f.read(&a); v != c.want {
					t.Errorf("typed value = %d, want %d", v, c.want)
				}
			})
		}
		for _, c := range invalid {
			t.Run(f.name+" rejects "+c.name, func(t *testing.T) {
				defer guard(t)
				obj := with(validAgent(s.unique("agent-int32-bad")), f.build(json.Number(c.v)), "spec", "rollout")
				requireRejected(t, s.createAgent(t, "default", obj), f.field)
			})
		}
	}
}
