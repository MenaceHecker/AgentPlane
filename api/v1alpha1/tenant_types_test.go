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

// tenantSample is the Tenant example from requirements section 4.1.
const tenantSample = `
apiVersion: agentplane.io/v1alpha1
kind: Tenant
metadata:
  name: acme
spec:
  displayName: Acme Research
  quota:
    cpu: "4"
    memory: 8Gi
    pods: 20
  budget:
    tokensPerDay: 2000000
    requestsPerMinute: 600
  egress:
    allowedTools: ["web-search", "calculator"]
`

func mustTenant(t *testing.T) *Tenant {
	t.Helper()
	var tn Tenant
	if err := yaml.UnmarshalStrict([]byte(tenantSample), &tn); err != nil {
		t.Fatalf("section 4.1 sample does not parse into Tenant: %v", err)
	}
	return &tn
}

func TestTenantSampleFields(t *testing.T) {
	tn := mustTenant(t)
	if tn.APIVersion != GroupVersion.String() || tn.Kind != "Tenant" {
		t.Errorf("type meta = %q/%q, want %q/Tenant", tn.APIVersion, tn.Kind, GroupVersion.String())
	}
	if tn.Name != "acme" || tn.Namespace != "" {
		t.Errorf("metadata = %q/%q, want cluster-scoped name acme", tn.Namespace, tn.Name)
	}
	s := tn.Spec
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"displayName", s.DisplayName, "Acme Research"},
		{"quota.cpu", s.Quota.CPU.String(), "4"},
		{"quota.memory", s.Quota.Memory.String(), "8Gi"},
		{"quota.pods", s.Quota.Pods, int32(20)},
		{"budget.tokensPerDay", s.Budget.TokensPerDay, int64(2000000)},
		{"budget.requestsPerMinute", s.Budget.RequestsPerMinute, int32(600)},
		{"egress.allowedTools", s.Egress.AllowedTools, []string{"web-search", "calculator"}},
	}
	for _, c := range checks {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	// Resource quantities are real quantities, not strings.
	if got := tn.Spec.Quota.Memory.Value(); got != 8*1024*1024*1024 {
		t.Errorf("memory bytes = %d, want 8Gi", got)
	}
	if got := tn.Spec.Quota.CPU.MilliValue(); got != 4000 {
		t.Errorf("cpu millicores = %d, want 4000", got)
	}
}

func TestTenantJSONNames(t *testing.T) {
	b, err := json.Marshal(mustTenant(t))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	spec := m["spec"].(map[string]any)
	for _, k := range []string{"displayName", "quota", "budget", "egress"} {
		if _, ok := spec[k]; !ok {
			t.Errorf("spec.%s missing from JSON: %s", k, b)
		}
	}
	budget := spec["budget"].(map[string]any)
	for _, k := range []string{"tokensPerDay", "requestsPerMinute"} {
		if _, ok := budget[k]; !ok {
			t.Errorf("spec.budget.%s missing from JSON: %s", k, b)
		}
	}
	if _, ok := spec["egress"].(map[string]any)["allowedTools"]; !ok {
		t.Errorf("spec.egress.allowedTools missing from JSON: %s", b)
	}
}

func TestTenantRoundTrip(t *testing.T) {
	in := mustTenant(t)
	in.Status = TenantStatus{
		ObservedGeneration: 3,
		Conditions: []metav1.Condition{{
			Type: ConditionReady, Status: metav1.ConditionTrue,
			Reason: "Reconciled", Message: "ok", ObservedGeneration: 3,
			LastTransitionTime: metav1.Unix(1700000000, 0),
		}},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Tenant
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in.Spec, out.Spec) {
		t.Errorf("spec changed in round trip:\n in=%+v\nout=%+v", in.Spec, out.Spec)
	}
	if out.Status.ObservedGeneration != 3 || len(out.Status.Conditions) != 1 ||
		out.Status.Conditions[0].Type != "Ready" || out.Status.Conditions[0].Status != metav1.ConditionTrue {
		t.Errorf("status changed in round trip: %+v", out.Status)
	}
}

// ADR-005: zero is a real limit and must never be dropped from serialization,
// otherwise an explicit 0 could be mistaken for an absent field.
func TestBudgetZeroIsSerialized(t *testing.T) {
	b, err := json.Marshal(Budget{})
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{`"tokensPerDay":0`, `"requestsPerMinute":0`} {
		if !strings.Contains(got, want) {
			t.Errorf("zero Budget JSON %s lacks %s", got, want)
		}
	}
}

func TestTenantUnknownFieldIsRejectedByStrictDecoding(t *testing.T) {
	bad := strings.Replace(tenantSample, "displayName: Acme Research", "displayName: Acme Research\n  unitPriceUsd: 1", 1)
	var tn Tenant
	if err := yaml.UnmarshalStrict([]byte(bad), &tn); err == nil {
		t.Fatal("strict decoding accepted an unknown spec field")
	}
}

func TestTenantRegistersInScheme(t *testing.T) {
	s := runtime.NewScheme()
	if err := AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	for _, obj := range []runtime.Object{&Tenant{}, &TenantList{}} {
		gvks, _, err := s.ObjectKinds(obj)
		if err != nil || len(gvks) != 1 {
			t.Fatalf("ObjectKinds(%T) = %v, %v", obj, gvks, err)
		}
		if gvks[0].Group != "agentplane.io" || gvks[0].Version != "v1alpha1" {
			t.Errorf("%T registered as %v", obj, gvks[0])
		}
	}
	if !s.Recognizes(GroupVersion.WithKind("Tenant")) || !s.Recognizes(GroupVersion.WithKind("TenantList")) {
		t.Error("scheme does not recognize Tenant and TenantList")
	}
}

func TestTenantDeepCopyIsIndependent(t *testing.T) {
	in := mustTenant(t)
	in.Status.Conditions = []metav1.Condition{{Type: ConditionReady}}
	cp := in.DeepCopy()
	cp.Spec.Egress.AllowedTools[0] = "changed"
	cp.Spec.Quota.Memory.Add(cp.Spec.Quota.Memory)
	cp.Status.Conditions[0].Type = "Changed"
	if in.Spec.Egress.AllowedTools[0] != "web-search" {
		t.Error("DeepCopy shares AllowedTools backing array")
	}
	if in.Spec.Quota.Memory.String() != "8Gi" {
		t.Errorf("DeepCopy shares quota memory: %s", in.Spec.Quota.Memory.String())
	}
	if in.Status.Conditions[0].Type != ConditionReady {
		t.Error("DeepCopy shares conditions")
	}
	if _, ok := in.DeepCopyObject().(*Tenant); !ok {
		t.Error("DeepCopyObject did not return *Tenant")
	}
}

func TestTenantListDeepCopy(t *testing.T) {
	l := &TenantList{Items: []Tenant{*mustTenant(t)}}
	cp := l.DeepCopy()
	cp.Items[0].Name = "other"
	if l.Items[0].Name != "acme" {
		t.Error("TenantList.DeepCopy shares items")
	}
}

func TestConditionTypeNames(t *testing.T) {
	// Names are fixed by requirements 4.3.
	want := map[string]string{
		ConditionReady: "Ready", ConditionReconciled: "Reconciled",
		ConditionBudgetExhausted: "BudgetExhausted", ConditionPolicyViolation: "PolicyViolation",
	}
	for got, w := range want {
		if got != w {
			t.Errorf("condition constant = %q, want %q", got, w)
		}
	}
}
