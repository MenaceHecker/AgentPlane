package v1alpha1

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/yaml"
)

// agentSample is the Agent example from requirements section 4.2.
const agentSample = `
apiVersion: agentplane.io/v1alpha1
kind: Agent
metadata:
  name: summarizer
  namespace: tenant-acme
spec:
  image: myacr.azurecr.io/acme/summarizer:1.2.0
  replicas: 2
  model: mock-large
  tools: ["web-search"]
  budget:
    tokensPerDay: 500000
    requestsPerMinute: 120
  resources:
    requests: { cpu: 100m, memory: 128Mi }
    limits: { cpu: 500m, memory: 256Mi }
  sandboxClass: hardened
  rollout:
    strategy: canary
    steps: [10, 50, 100]
    slo:
      errorRate: "<1%"
      p95LatencyMs: 800
`

func mustAgent(t *testing.T) *Agent {
	t.Helper()
	var a Agent
	if err := yaml.UnmarshalStrict([]byte(agentSample), &a); err != nil {
		t.Fatalf("section 4.2 sample does not parse into Agent: %v", err)
	}
	return &a
}

func TestAgentSampleFields(t *testing.T) {
	a := mustAgent(t)
	if a.APIVersion != GroupVersion.String() || a.Kind != "Agent" {
		t.Errorf("type meta = %q/%q", a.APIVersion, a.Kind)
	}
	if a.Name != "summarizer" || a.Namespace != "tenant-acme" {
		t.Errorf("metadata = %q/%q, want tenant-acme/summarizer", a.Namespace, a.Name)
	}
	s := a.Spec
	if s.Replicas == nil || *s.Replicas != 2 {
		t.Errorf("replicas = %v, want 2", s.Replicas)
	}
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"image", s.Image, "myacr.azurecr.io/acme/summarizer:1.2.0"},
		{"model", s.Model, "mock-large"},
		{"tools", s.Tools, []string{"web-search"}},
		{"budget.tokensPerDay", s.Budget.TokensPerDay, int64(500000)},
		{"budget.requestsPerMinute", s.Budget.RequestsPerMinute, int32(120)},
		{"sandboxClass", s.SandboxClass, SandboxClassHardened},
		{"rollout.strategy", s.Rollout.Strategy, RolloutStrategyCanary},
		{"rollout.steps", s.Rollout.Steps, []int32{10, 50, 100}},
		{"rollout.slo.errorRate", s.Rollout.SLO.ErrorRate, "<1%"},
		{"rollout.slo.p95LatencyMs", s.Rollout.SLO.P95LatencyMs, int32(800)},
	}
	for _, c := range checks {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

func TestAgentResourceQuantities(t *testing.T) {
	r := mustAgent(t).Spec.Resources
	cases := []struct {
		name  string
		milli int64
		bytes int64
		cpu   bool
	}{
		{"requests.cpu", 100, 0, true},
		{"limits.cpu", 500, 0, true},
		{"requests.memory", 0, 128 * 1024 * 1024, false},
		{"limits.memory", 0, 256 * 1024 * 1024, false},
	}
	for _, c := range cases {
		list := r.Requests
		if strings.HasPrefix(c.name, "limits") {
			list = r.Limits
		}
		if c.cpu {
			q := list["cpu"]
			if q.MilliValue() != c.milli {
				t.Errorf("%s = %dm, want %dm", c.name, q.MilliValue(), c.milli)
			}
		} else {
			q := list["memory"]
			if q.Value() != c.bytes {
				t.Errorf("%s = %d, want %d", c.name, q.Value(), c.bytes)
			}
		}
	}
}

func TestAgentJSONNames(t *testing.T) {
	b, err := json.Marshal(mustAgent(t))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	spec := m["spec"].(map[string]any)
	for _, k := range []string{"image", "replicas", "model", "tools", "budget", "resources", "sandboxClass", "rollout"} {
		if _, ok := spec[k]; !ok {
			t.Errorf("spec.%s missing from JSON: %s", k, b)
		}
	}
	rollout := spec["rollout"].(map[string]any)
	for _, k := range []string{"strategy", "steps", "slo"} {
		if _, ok := rollout[k]; !ok {
			t.Errorf("spec.rollout.%s missing from JSON: %s", k, b)
		}
	}
	slo := rollout["slo"].(map[string]any)
	for _, k := range []string{"errorRate", "p95LatencyMs"} {
		if _, ok := slo[k]; !ok {
			t.Errorf("spec.rollout.slo.%s missing from JSON: %s", k, b)
		}
	}
}

func TestAgentRoundTrip(t *testing.T) {
	in := mustAgent(t)
	in.Status = AgentStatus{
		ObservedGeneration: 7,
		Conditions: []metav1.Condition{
			{Type: ConditionReady, Status: metav1.ConditionFalse, Reason: "Progressing", ObservedGeneration: 7, LastTransitionTime: metav1.Unix(1700000000, 0)},
			{Type: ConditionBudgetExhausted, Status: metav1.ConditionTrue, Reason: "DailyBudget", ObservedGeneration: 7, LastTransitionTime: metav1.Unix(1700000100, 0)},
		},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Agent
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in.Spec, out.Spec) {
		t.Errorf("spec changed in round trip:\n in=%+v\nout=%+v", in.Spec, out.Spec)
	}
	if out.Status.ObservedGeneration != 7 || len(out.Status.Conditions) != 2 {
		t.Fatalf("status changed in round trip: %+v", out.Status)
	}
	got := []string{out.Status.Conditions[0].Type, out.Status.Conditions[1].Type}
	if !reflect.DeepEqual(got, []string{ConditionReady, ConditionBudgetExhausted}) {
		t.Errorf("condition types = %v", got)
	}
}

func TestAgentOptionalFieldsAreOmittedButBudgetZeroIsNot(t *testing.T) {
	a := &Agent{Spec: AgentSpec{Image: "i", Model: "m"}}
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, absent := range []string{`"replicas"`, `"tools"`, `"sandboxClass"`, `"rollout"`} {
		if strings.Contains(s, absent) {
			t.Errorf("optional field %s serialized when unset: %s", absent, s)
		}
	}
	for _, present := range []string{`"tokensPerDay":0`, `"requestsPerMinute":0`} {
		if !strings.Contains(s, present) {
			t.Errorf("zero budget must be serialized, missing %s: %s", present, s)
		}
	}
	// An explicit replicas of zero is a real value and must survive.
	zero := int32(0)
	a.Spec.Replicas = &zero
	b, _ = json.Marshal(a)
	if !strings.Contains(string(b), `"replicas":0`) {
		t.Errorf("explicit replicas 0 was dropped: %s", b)
	}
}

func TestAgentUnknownFieldIsRejectedByStrictDecoding(t *testing.T) {
	bad := strings.Replace(agentSample, "model: mock-large", "model: mock-large\n  apiKey: nope", 1)
	var a Agent
	if err := yaml.UnmarshalStrict([]byte(bad), &a); err == nil {
		t.Fatal("strict decoding accepted an unknown spec field")
	}
}

func TestAgentRegistersInScheme(t *testing.T) {
	s := runtime.NewScheme()
	if err := AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	for _, obj := range []runtime.Object{&Agent{}, &AgentList{}, &Tenant{}, &TenantList{}} {
		gvks, _, err := s.ObjectKinds(obj)
		if err != nil || len(gvks) != 1 || gvks[0].GroupVersion() != GroupVersion {
			t.Fatalf("ObjectKinds(%T) = %v, %v", obj, gvks, err)
		}
	}
	for _, k := range []string{"Agent", "AgentList", "Tenant", "TenantList"} {
		if !s.Recognizes(GroupVersion.WithKind(k)) {
			t.Errorf("scheme does not recognize %s", k)
		}
	}
}

func TestAgentDeepCopyIsIndependent(t *testing.T) {
	in := mustAgent(t)
	in.Status.Conditions = []metav1.Condition{{Type: ConditionReady}}
	cp := in.DeepCopy()
	*cp.Spec.Replicas = 99
	cp.Spec.Tools[0] = "changed"
	cp.Spec.Rollout.Steps[0] = 99
	cp.Spec.Resources.Limits["memory"] = cp.Spec.Resources.Requests["memory"]
	cp.Status.Conditions[0].Type = "Changed"
	if *in.Spec.Replicas != 2 || in.Spec.Tools[0] != "web-search" || in.Spec.Rollout.Steps[0] != 10 {
		t.Error("DeepCopy shares replicas, tools or rollout steps")
	}
	if got := in.Spec.Resources.Limits.Memory().String(); got != "256Mi" {
		t.Errorf("DeepCopy shares resource limits: %s", got)
	}
	if in.Status.Conditions[0].Type != ConditionReady {
		t.Error("DeepCopy shares conditions")
	}
	l := &AgentList{Items: []Agent{*in}}
	lc := l.DeepCopy()
	lc.Items[0].Name = "other"
	if l.Items[0].Name != "summarizer" {
		t.Error("AgentList.DeepCopy shares items")
	}
	if _, ok := in.DeepCopyObject().(*Agent); !ok {
		t.Error("DeepCopyObject did not return *Agent")
	}
}
