package apitest

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	tenantsPath     = "/apis/agentplane.io/v1alpha1/tenants"
	tenantsNSPath   = "/apis/agentplane.io/v1alpha1/namespaces/default/tenants"
	tableAcceptJSON = "application/json;as=Table;g=meta.k8s.io;v=v1"
)

func validTenant(name string) map[string]any {
	return map[string]any{
		"apiVersion": "agentplane.io/v1alpha1",
		"kind":       "Tenant",
		"metadata":   map[string]any{"name": name},
		"spec": map[string]any{
			"displayName": "Team A",
			"quota":       map[string]any{"cpu": "4", "memory": "8Gi", "pods": 10},
			"budget":      map[string]any{"tokensPerDay": 100000, "requestsPerMinute": 60},
			"egress":      map[string]any{"allowedTools": []any{"search"}},
		},
	}
}

// createTenant posts the object and registers its deletion when it was created.
func (s *testSuite) createTenant(t *testing.T, obj map[string]any) response {
	t.Helper()
	r := s.postJSON(t, tenantsPath, obj)
	if r.Code == http.StatusCreated {
		name := obj["metadata"].(map[string]any)["name"].(string)
		t.Cleanup(func() { s.delete(t, tenantsPath+"/"+name) })
	}
	return r
}

func TestTenantCRDIsStructuralClusterScopedAndEstablished(t *testing.T) {
	s := need(t)
	crd, err := s.ext.ApiextensionsV1().CustomResourceDefinitions().Get(context.Background(), "tenants.agentplane.io", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if crd.Spec.Scope != apiextensionsv1.ClusterScoped {
		t.Errorf("scope = %s, want Cluster", crd.Spec.Scope)
	}
	n := crd.Spec.Names
	if n.Plural != "tenants" || n.Singular != "tenant" || n.Kind != "Tenant" || n.ListKind != "TenantList" || crd.Spec.Group != "agentplane.io" {
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
	if strings.Join(cols, ",") != "DisplayName,Ready,Age" {
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

func TestTenantValidObjectIsAcceptedAndStoredExactly(t *testing.T) {
	s := need(t)
	name := s.unique("tenant-valid")
	created := requireCreated(t, s.createTenant(t, validTenant(name)))
	for path, want := range map[string]string{
		"displayName":              "Team A",
		"quota.cpu":                "4",
		"quota.memory":             "8Gi",
		"quota.pods":               "10",
		"budget.tokensPerDay":      "100000",
		"budget.requestsPerMinute": "60",
	} {
		got, ok := dig(created, append([]string{"spec"}, strings.Split(path, ".")...)...)
		if !ok || toString(got) != want {
			t.Errorf("spec.%s = %v (present %v), want %s", path, got, ok, want)
		}
	}
}

func toString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func TestTenantZeroValuesAreAcceptedAndKeptNotTreatedAsAbsent(t *testing.T) {
	s := need(t)
	obj := validTenant(s.unique("tenant-zero"))
	obj = with(obj, 0, "spec", "budget", "tokensPerDay")
	obj = with(obj, 0, "spec", "budget", "requestsPerMinute")
	obj = with(obj, 0, "spec", "quota", "pods")
	created := requireCreated(t, s.createTenant(t, obj))
	for _, p := range [][]string{{"spec", "budget", "tokensPerDay"}, {"spec", "budget", "requestsPerMinute"}, {"spec", "quota", "pods"}} {
		v, ok := dig(created, p...)
		if !ok || toString(v) != "0" {
			t.Errorf("%s = %v (present %v), want an explicit 0", strings.Join(p, "."), v, ok)
		}
	}
	// Read back through a separate GET: stored, not just echoed.
	got := decode(t, s.get(t, tenantsPath+"/"+obj["metadata"].(map[string]any)["name"].(string)).Body)
	if v, ok := dig(got, "spec", "budget", "tokensPerDay"); !ok || toString(v) != "0" {
		t.Errorf("stored tokensPerDay = %v (present %v)", v, ok)
	}
}

func TestTenantEgressAllowedToolsMayBeAbsentOrEmptyAndIsKeptAsGiven(t *testing.T) {
	s := need(t)
	absent := without(validTenant(s.unique("tenant-noallow")), "spec", "egress", "allowedTools")
	created := requireCreated(t, s.createTenant(t, absent))
	if _, ok := dig(created, "spec", "egress", "allowedTools"); ok {
		t.Error("absent allowedTools must stay absent")
	}
	empty := with(validTenant(s.unique("tenant-emptyallow")), []any{}, "spec", "egress", "allowedTools")
	created = requireCreated(t, s.createTenant(t, empty))
	v, ok := dig(created, "spec", "egress", "allowedTools")
	if l, isList := v.([]any); !ok || !isList || len(l) != 0 {
		t.Errorf("empty allowedTools = %v (present %v), want []", v, ok)
	}
}

func TestTenantMissingRequiredFieldsAreRejectedWithNamedCauses(t *testing.T) {
	s := need(t)
	for _, path := range [][]string{
		{"spec"},
		{"spec", "displayName"},
		{"spec", "quota"},
		{"spec", "budget"},
		{"spec", "egress"},
		{"spec", "quota", "cpu"},
		{"spec", "quota", "memory"},
		{"spec", "quota", "pods"},
		{"spec", "budget", "tokensPerDay"},
		{"spec", "budget", "requestsPerMinute"},
	} {
		field := strings.Join(path, ".")
		t.Run("omit "+field, func(t *testing.T) {
			obj := without(validTenant(s.unique("tenant-missing")), path...)
			requireRejected(t, s.createTenant(t, obj), field)
		})
	}
}

func TestTenantOutOfRangeAndWrongTypedValuesAreRejected(t *testing.T) {
	s := need(t)
	big := func(v string) json.Number { return json.Number(v) }
	cases := []struct {
		name  string
		path  []string
		value any
		field string // the field path the server names in its cause, when it differs from the path
	}{
		{"empty displayName", []string{"spec", "displayName"}, "", ""},
		{"negative tokensPerDay", []string{"spec", "budget", "tokensPerDay"}, -1, ""},
		{"negative requestsPerMinute", []string{"spec", "budget", "requestsPerMinute"}, -1, ""},
		{"negative pods", []string{"spec", "quota", "pods"}, -1, ""},
		{"tokensPerDay above int64", []string{"spec", "budget", "tokensPerDay"}, big("9223372036854775808"), ""},
		{"requestsPerMinute above int32", []string{"spec", "budget", "requestsPerMinute"}, big("2147483648"), ""},
		{"pods above int32", []string{"spec", "quota", "pods"}, big("2147483648"), ""},
		{"string tokensPerDay", []string{"spec", "budget", "tokensPerDay"}, "5", ""},
		{"fractional tokensPerDay", []string{"spec", "budget", "tokensPerDay"}, big("1.5"), ""},
		{"fractional pods", []string{"spec", "quota", "pods"}, big("1.5"), ""},
		{"numeric displayName", []string{"spec", "displayName"}, 5, ""},
		{"boolean cpu", []string{"spec", "quota", "cpu"}, true, ""},
		{"cpu that is not a quantity", []string{"spec", "quota", "cpu"}, "lots", ""},
		{"memory that is not a quantity", []string{"spec", "quota", "memory"}, "8 gigs", ""},
		{"object allowedTools", []string{"spec", "egress", "allowedTools"}, map[string]any{"a": 1}, ""},
		{"numeric allowedTools item", []string{"spec", "egress", "allowedTools"}, []any{1}, "spec.egress.allowedTools[0]"},
		{"string budget", []string{"spec", "budget"}, "none", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			obj := with(validTenant(s.unique("tenant-bad")), c.value, c.path...)
			field := c.field
			if field == "" {
				field = strings.Join(c.path, ".")
			}
			requireRejected(t, s.createTenant(t, obj), field)
		})
	}
}

// The schema checks that a quantity is shaped like one; it does not judge its value. This test pins
// that documented boundary so a later policy change is a deliberate one. It is NOT a statement that
// negative or absurd compute quotas are acceptable: that policy belongs to later validation work.
func TestTenantQuantityPolicyIsDeferredSchemaOnlyChecksTheShape(t *testing.T) {
	s := need(t)
	obj := with(validTenant(s.unique("tenant-qty")), "-4", "spec", "quota", "cpu")
	requireCreated(t, s.createTenant(t, obj))
}

func TestTenantUnknownFieldsArePrunedNotRejected(t *testing.T) {
	s := need(t)
	obj := with(validTenant(s.unique("tenant-prune")), "x", "spec", "notInTheSchema")
	obj["extraTopLevel"] = "y"
	created := requireCreated(t, s.createTenant(t, obj))
	if _, ok := dig(created, "spec", "notInTheSchema"); ok {
		t.Error("an unknown spec field was stored; the structural schema should prune it")
	}
	if _, ok := created["extraTopLevel"]; ok {
		t.Error("an unknown top-level field was stored")
	}
}

func TestTenantIsClusterScopedNotNamespaced(t *testing.T) {
	s := need(t)
	r := s.postJSON(t, tenantsNSPath, validTenant(s.unique("tenant-ns")))
	if r.Code != http.StatusNotFound {
		t.Errorf("creating a Tenant under a namespace returned %d, want 404 (cluster-scoped): %s", r.Code, r.Body)
	}
	list := decode(t, s.get(t, "/apis/agentplane.io/v1alpha1").Body)
	found := false
	for _, res := range list["resources"].([]any) {
		m := res.(map[string]any)
		if m["name"] == "tenants" {
			found = true
			if m["namespaced"] != false || m["kind"] != "Tenant" {
				t.Errorf("tenants resource = %v", m)
			}
		}
	}
	if !found {
		t.Error("the tenants resource is not served")
	}
}

func TestTenantStatusIsWrittenOnlyThroughTheStatusSubresource(t *testing.T) {
	s := need(t)
	name := s.unique("tenant-status")
	requireCreated(t, s.createTenant(t, validTenant(name)))
	path := tenantsPath + "/" + name
	cond := func(typ, st string) map[string]any {
		return map[string]any{"type": typ, "status": st, "reason": "Test", "message": "", "lastTransitionTime": time.Now().UTC().Format(time.RFC3339)}
	}

	// 1. An ordinary update that carries a status is not allowed to write it.
	cur := decode(t, s.get(t, path).Body)
	cur["status"] = map[string]any{"conditions": []any{cond("Ready", "True")}}
	cur = with(cur, "Renamed", "spec", "displayName")
	r := s.put(t, path, cur)
	if r.Code != http.StatusOK {
		t.Fatalf("ordinary update: %d %s", r.Code, r.Body)
	}
	after := decode(t, r.Body)
	if _, ok := dig(after, "status", "conditions"); ok {
		t.Error("an ordinary update wrote status.conditions; only the status subresource may")
	}
	gen1, _ := dig(after, "metadata", "generation")

	// 2. A status update writes status and cannot change spec; generation does not move.
	cur = decode(t, s.get(t, path).Body)
	cur["status"] = map[string]any{"observedGeneration": 1, "conditions": []any{cond("Ready", "True")}}
	cur = with(cur, "Sneaky", "spec", "displayName")
	r = s.put(t, path+"/status", cur)
	if r.Code != http.StatusOK {
		t.Fatalf("status update: %d %s", r.Code, r.Body)
	}
	after = decode(t, r.Body)
	if v, _ := dig(after, "status", "conditions"); len(v.([]any)) != 1 {
		t.Errorf("the status update did not store the condition: %s", r.Body)
	}
	if v, _ := dig(after, "spec", "displayName"); v != "Renamed" {
		t.Errorf("the status update changed spec.displayName to %v", v)
	}
	if gen2, _ := dig(after, "metadata", "generation"); toString(gen2) != toString(gen1) {
		t.Errorf("generation moved from %v to %v on a status-only update", gen1, gen2)
	}

	// 3. A real spec change moves generation.
	cur = decode(t, s.get(t, path).Body)
	cur = with(cur, "Renamed again", "spec", "displayName")
	after = decode(t, s.put(t, path, cur).Body)
	if gen3, _ := dig(after, "metadata", "generation"); toString(gen3) == toString(gen1) {
		t.Errorf("generation did not move on a spec change (still %v)", gen3)
	}
}

func TestTenantConditionsAreAListMapKeyedByType(t *testing.T) {
	s := need(t)
	name := s.unique("tenant-cond")
	requireCreated(t, s.createTenant(t, validTenant(name)))
	path := tenantsPath + "/" + name + "/status"
	cond := func(typ, st string) map[string]any {
		return map[string]any{"type": typ, "status": st, "reason": "Test", "message": "m", "lastTransitionTime": time.Now().UTC().Format(time.RFC3339)}
	}
	put := func(conds ...any) response {
		cur := decode(t, s.get(t, strings.TrimSuffix(path, "/status")).Body)
		cur["status"] = map[string]any{"conditions": conds}
		return s.put(t, path, cur)
	}
	requireRejected(t, put(cond("Ready", "True"), cond("Ready", "False")), "status.conditions[1]")
	if r := put(cond("Ready", "True"), cond("Reconciled", "False")); r.Code != http.StatusOK {
		t.Errorf("two different condition types were refused: %d %s", r.Code, r.Body)
	}
	requireRejected(t, put(cond("Ready", "Maybe")), "status.conditions[0].status")
	bad := cond("Ready", "True")
	delete(bad, "reason")
	requireRejected(t, put(bad), "status.conditions[0].reason")
}

func TestTenantTableShowsDisplayNameAndAnUnknownReadyIsBlankNotSuccess(t *testing.T) {
	s := need(t)
	name := s.unique("tenant-table")
	requireCreated(t, s.createTenant(t, validTenant(name)))
	r := s.do(t, http.MethodGet, tenantsPath+"/"+name, "", tableAcceptJSON, nil)
	if r.Code != http.StatusOK {
		t.Fatalf("table request: %d %s", r.Code, r.Body)
	}
	tbl := decode(t, r.Body)
	var cols []string
	for _, c := range tbl["columnDefinitions"].([]any) {
		cols = append(cols, c.(map[string]any)["name"].(string))
	}
	if strings.Join(cols, ",") != "Name,DisplayName,Ready,Age" {
		t.Fatalf("columns = %v", cols)
	}
	cells := tbl["rows"].([]any)[0].(map[string]any)["cells"].([]any)
	if cells[1] != "Team A" {
		t.Errorf("DisplayName cell = %v", cells[1])
	}
	if cells[2] != nil && cells[2] != "" {
		t.Errorf("with no Ready condition the Ready cell is %v; it must stay blank, not a synthetic success", cells[2])
	}
	// Once a Ready condition exists the column shows its status.
	cur := decode(t, s.get(t, tenantsPath+"/"+name).Body)
	cur["status"] = map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True", "reason": "Test", "message": "", "lastTransitionTime": time.Now().UTC().Format(time.RFC3339)}}}
	if r := s.put(t, tenantsPath+"/"+name+"/status", cur); r.Code != http.StatusOK {
		t.Fatalf("status update: %d %s", r.Code, r.Body)
	}
	tbl = decode(t, s.do(t, http.MethodGet, tenantsPath+"/"+name, "", tableAcceptJSON, nil).Body)
	if got := tbl["rows"].([]any)[0].(map[string]any)["cells"].([]any)[2]; got != "True" {
		t.Errorf("Ready cell = %v, want True once the condition exists", got)
	}
}
