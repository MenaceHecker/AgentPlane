package contractcheck

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

type gatewayFixture struct {
	File        string `json:"file"`
	Kind        string `json:"kind"`
	Schema      string `json:"schema"`
	Operation   string `json:"operation"`
	Status      int    `json:"status"`
	Valid       bool   `json:"valid"`
	Why         string `json:"why"`
	ExpectError string `json:"expectError"`
}

type gatewayManifest struct {
	Fixtures []gatewayFixture `json:"fixtures"`
}

func gatewayDir(t *testing.T) string {
	return filepath.Join(repoRoot(t), "docs", "gateway")
}

func loadGatewayDoc(t *testing.T) *openapi3.T {
	t.Helper()
	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = false
	doc, err := loader.LoadFromFile(filepath.Join(gatewayDir(t), "openapi.yaml"))
	if err != nil {
		t.Fatalf("load openapi.yaml: %v", err)
	}
	if err := doc.Validate(context.Background()); err != nil {
		t.Fatalf("openapi.yaml is not a valid OpenAPI document: %v", err)
	}
	return doc
}

func loadGatewayManifest(t *testing.T) gatewayManifest {
	t.Helper()
	var m gatewayManifest
	if err := json.Unmarshal(readFile(t, filepath.Join(gatewayDir(t), "examples", "manifest.json")), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func visit(s *openapi3.Schema, v any) error {
	return s.VisitJSON(v, openapi3.EnableFormatValidation())
}

func schemaByName(t *testing.T, doc *openapi3.T, name string) *openapi3.Schema {
	t.Helper()
	ref, ok := doc.Components.Schemas[name]
	if !ok || ref.Value == nil {
		t.Fatalf("schema %q not found in components", name)
	}
	return ref.Value
}

// responseSchema returns the JSON schema the operation declares for a status.
func responseSchema(t *testing.T, doc *openapi3.T, operation string, status int) *openapi3.Schema {
	t.Helper()
	parts := strings.SplitN(operation, " ", 2)
	if len(parts) != 2 {
		t.Fatalf("bad operation %q", operation)
	}
	item := doc.Paths.Find(parts[1])
	if item == nil {
		t.Fatalf("path %q not in document", parts[1])
	}
	op := item.GetOperation(parts[0])
	if op == nil {
		t.Fatalf("operation %q not in document", operation)
	}
	resp := op.Responses.Status(status)
	if resp == nil || resp.Value == nil {
		t.Fatalf("%s declares no %d response", operation, status)
	}
	mt := resp.Value.Content.Get("application/json")
	if mt == nil || mt.Schema == nil || mt.Schema.Value == nil {
		t.Fatalf("%s %d declares no application/json schema", operation, status)
	}
	return mt.Schema.Value
}

func TestGatewayOpenAPIDocument(t *testing.T) {
	doc := loadGatewayDoc(t)
	if !strings.HasPrefix(doc.OpenAPI, "3.0") {
		t.Errorf("openapi version = %q, want 3.0.x", doc.OpenAPI)
	}
	for _, p := range []string{"/v1/chat/completions", "/v1/tools/{name}"} {
		item := doc.Paths.Find(p)
		if item == nil || item.Post == nil {
			t.Errorf("FR-GW-1: POST %s missing", p)
			continue
		}
		for _, st := range []int{200, 400, 401, 403, 429, 500, 502, 503, 504} {
			if item.Post.Responses.Status(st) == nil {
				t.Errorf("POST %s lacks a %d response", p, st)
			}
		}
	}
	// FR-GW-2: bearer token only, no API-key scheme.
	if len(doc.Components.SecuritySchemes) != 1 {
		t.Errorf("security schemes = %d, want exactly bearerAuth", len(doc.Components.SecuritySchemes))
	}
	b := doc.Components.SecuritySchemes["bearerAuth"]
	if b == nil || b.Value.Type != "http" || b.Value.Scheme != "bearer" {
		t.Errorf("bearerAuth must be an http bearer scheme: %+v", b)
	}
	// FR-GW-9: traceparent accepted on both operations.
	for _, p := range []string{"/v1/chat/completions", "/v1/tools/{name}"} {
		found := false
		for _, par := range doc.Paths.Find(p).Post.Parameters {
			if par.Value != nil && par.Value.Name == "traceparent" && par.Value.In == "header" {
				found = true
			}
		}
		if !found {
			t.Errorf("POST %s does not declare the traceparent header", p)
		}
	}
}

// ADR-005: request bodies never carry identity. The request schemas must
// forbid unknown properties and declare none that look like identity.
func TestGatewayRequestSchemasCarryNoIdentity(t *testing.T) {
	doc := loadGatewayDoc(t)
	banned := []string{"tenant", "tenant_id", "agent", "agent_id", "user", "identity", "namespace", "serviceaccount"}
	for _, name := range []string{"ChatCompletionRequest", "ToolCallRequest", "ChatMessage"} {
		s := schemaByName(t, doc, name)
		if s.AdditionalProperties.Has == nil || *s.AdditionalProperties.Has {
			t.Errorf("%s must set additionalProperties: false", name)
		}
		for _, b := range banned {
			if _, ok := s.Properties[b]; ok {
				t.Errorf("%s declares identity-like property %q", name, b)
			}
		}
	}
}

func TestGatewayFixtureManifestIsComplete(t *testing.T) {
	root := filepath.Join(gatewayDir(t), "examples")
	m := loadGatewayManifest(t)
	listed := map[string]bool{}
	for _, f := range m.Fixtures {
		if listed[f.File] {
			t.Errorf("fixture %s listed twice", f.File)
		}
		listed[f.File] = true
		if f.Kind == "" {
			t.Errorf("fixture %s has no kind", f.File)
		}
		if !f.Valid && f.Why == "" {
			t.Errorf("invalid fixture %s must say why it is invalid", f.File)
		}
	}
	var onDisk []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Base(p) == "manifest.json" {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		onDisk = append(onDisk, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(onDisk)
	for _, f := range onDisk {
		if !listed[f] {
			t.Errorf("fixture file %s is not in manifest.json", f)
		}
		delete(listed, f)
	}
	for f := range listed {
		t.Errorf("manifest lists %s but the file does not exist", f)
	}
}

func TestGatewayFixtures(t *testing.T) {
	doc := loadGatewayDoc(t)
	root := filepath.Join(gatewayDir(t), "examples")
	m := loadGatewayManifest(t)
	for _, f := range m.Fixtures {
		t.Run(f.File, func(t *testing.T) {
			path := filepath.Join(root, f.File)
			var err error
			switch f.Kind {
			case "schema":
				err = visit(schemaByName(t, doc, f.Schema), readJSON(t, path))
			case "response":
				err = visit(responseSchema(t, doc, f.Operation, f.Status), readJSON(t, path))
			case "traceparent":
				param := doc.Components.Parameters["Traceparent"]
				if param == nil {
					t.Fatal("Traceparent parameter missing")
				}
				val := readJSON(t, path).(map[string]any)["value"].(string)
				err = visit(param.Value.Schema.Value, val)
			case "stream":
				err = checkStream(t, doc, string(readFile(t, path)))
			case "budget-error":
				err = checkBudgetError(t, doc, readJSON(t, path), f.Valid)
			default:
				t.Fatalf("unknown fixture kind %q", f.Kind)
			}
			if f.Valid {
				if err != nil {
					t.Fatalf("valid fixture rejected: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("invalid fixture was ACCEPTED (%s)", f.Why)
			}
			if !strings.Contains(err.Error(), f.ExpectError) {
				t.Fatalf("rejected for the wrong reason: want error containing %q, got: %v", f.ExpectError, err)
			}
		})
	}
}

// Every error code and every declared status must be demonstrated by at least
// one valid fixture, so the status-to-code contract is exercised, not assumed.
func TestGatewayFixturesCoverStatusesAndCodes(t *testing.T) {
	doc := loadGatewayDoc(t)
	m := loadGatewayManifest(t)
	root := filepath.Join(gatewayDir(t), "examples")
	statuses := map[string]map[int]bool{}
	codes := map[string]bool{}
	for _, f := range m.Fixtures {
		if f.Kind != "response" || !f.Valid {
			continue
		}
		if statuses[f.Operation] == nil {
			statuses[f.Operation] = map[int]bool{}
		}
		statuses[f.Operation][f.Status] = true
		if f.Status != 200 {
			body := readJSON(t, filepath.Join(root, f.File)).(map[string]any)
			codes[body["error"].(map[string]any)["code"].(string)] = true
		}
	}
	for _, op := range []string{"POST /v1/chat/completions", "POST /v1/tools/{name}"} {
		for _, st := range []int{200, 400, 401, 403, 429, 500, 502, 503, 504} {
			if !statuses[op][st] {
				t.Errorf("no valid response fixture for %s %d", op, st)
			}
		}
	}
	// Every code the document can emit: the generic detail's codes plus the
	// budget detail's single code.
	var want []string
	for _, c := range schemaByName(t, doc, "ErrorDetail").Properties["code"].Value.Enum {
		want = append(want, c.(string))
	}
	for _, c := range schemaByName(t, doc, "BudgetExhaustedDetail").Properties["code"].Value.Enum {
		want = append(want, c.(string))
	}
	for _, c := range want {
		if !codes[c] {
			t.Errorf("error code %q has no valid response fixture", c)
		}
	}
	if len(codes) != len(want) {
		t.Errorf("fixtures use %d codes, enum has %d", len(codes), len(want))
	}
}

// checkBudgetError applies the budget-error contract to a {"error": {...}}
// object (review finding: budget_exhausted must never match the generic
// branch). It requires, in one place:
//   - the envelope schema (ErrorResponse) to agree with the expectation;
//   - the inner object to match BudgetExhaustedDetail exactly when valid, and
//     to be rejected by it, naming the offending property, when invalid;
//   - the generic detail (ErrorDetail) to reject EVERY budget_exhausted object,
//     valid or not, so the two ErrorResponse branches stay disjoint.
//
// It returns the first reason the object is rejected, or nil if it is valid.
func checkBudgetError(t *testing.T, doc *openapi3.T, v any, wantValid bool) error {
	t.Helper()
	body, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("budget-error fixture is not an object")
	}
	inner := body["error"]
	envelope := visit(schemaByName(t, doc, "ErrorResponse"), v)
	detail := visit(schemaByName(t, doc, "BudgetExhaustedDetail"), inner)
	generic := visit(schemaByName(t, doc, "ErrorDetail"), inner)
	if generic == nil {
		t.Errorf("the generic ErrorDetail accepted a budget_exhausted object; the branches are not disjoint")
	}
	if wantValid {
		if envelope != nil || detail != nil {
			return fmt.Errorf("valid budget error rejected: envelope=%v detail=%v", envelope, detail)
		}
		return nil
	}
	if envelope == nil {
		t.Errorf("ErrorResponse accepted an invalid budget error (the generic branch must not match it)")
	}
	if detail == nil {
		t.Errorf("BudgetExhaustedDetail accepted an invalid budget error")
		return nil
	}
	return detail
}

// --- SSE stream checks (ADR-005 section 5 and 6) ---

type sseEvent struct{ data string }

func parseSSE(text string) ([]sseEvent, error) {
	var events []sseEvent
	for _, block := range strings.Split(strings.TrimSuffix(text, "\n\n"), "\n\n") {
		if block == "" {
			return nil, fmt.Errorf("empty event block")
		}
		lines := strings.Split(block, "\n")
		if len(lines) != 1 || !strings.HasPrefix(lines[0], "data: ") {
			return nil, fmt.Errorf("malformed SSE block %q (want a single `data: ` line)", block)
		}
		events = append(events, sseEvent{data: strings.TrimPrefix(lines[0], "data: ")})
	}
	if !strings.HasSuffix(text, "\n\n") {
		return nil, fmt.Errorf("stream does not end with a blank line")
	}
	return events, nil
}

// checkStream applies the stream contract: content chunks, exactly one
// terminal usage chunk (empty choices) and then [DONE] on normal completion;
// or one terminal error event with no [DONE]. Nothing may follow either.
func checkStream(t *testing.T, doc *openapi3.T, text string) error {
	t.Helper()
	events, err := parseSSE(text)
	if err != nil {
		return err
	}
	chunk := schemaByName(t, doc, "ChatCompletionChunk")
	errResp := schemaByName(t, doc, "ErrorResponse")
	usageSeen := 0
	for i, ev := range events {
		last := i == len(events)-1
		if ev.data == "[DONE]" {
			if !last {
				return fmt.Errorf("events after [DONE]")
			}
			if usageSeen != 1 {
				return fmt.Errorf("normal completion needs exactly one terminal usage chunk before [DONE], found %d", usageSeen)
			}
			return nil
		}
		var v map[string]any
		if err := json.Unmarshal([]byte(ev.data), &v); err != nil {
			return fmt.Errorf("event %d is not JSON: %v", i, err)
		}
		if _, isErr := v["error"]; isErr {
			if err := visit(errResp, v); err != nil {
				return fmt.Errorf("error event %d invalid: %v", i, err)
			}
			if !last {
				return fmt.Errorf("events after the terminal error event (an error ends the stream, with no [DONE])")
			}
			if usageSeen > 0 {
				return fmt.Errorf("error event after the usage chunk")
			}
			return nil
		}
		if err := visit(chunk, v); err != nil {
			return fmt.Errorf("event %d matches neither a chunk nor an error: %v", i, err)
		}
		_, hasUsage := v["usage"]
		choices, _ := v["choices"].([]any)
		if hasUsage {
			if len(choices) != 0 {
				return fmt.Errorf("terminal usage chunk must have empty choices, event %d has %d", i, len(choices))
			}
			usageSeen++
			if usageSeen > 1 {
				return fmt.Errorf("more than one terminal usage chunk")
			}
		} else if len(choices) == 0 {
			return fmt.Errorf("event %d has empty choices but no usage", i)
		}
	}
	return fmt.Errorf("stream ended without [DONE] or an error event; normal completion must end with [DONE]")
}

// Structural guard for the review finding: the generic error detail must not
// be able to carry budget_exhausted, and the budget detail must require both
// scope and reset_at, so ErrorResponse's two branches are disjoint.
func TestGatewayErrorBranchesAreDisjoint(t *testing.T) {
	doc := loadGatewayDoc(t)
	for _, c := range schemaByName(t, doc, "ErrorDetail").Properties["code"].Value.Enum {
		if c == "budget_exhausted" {
			t.Fatal("ErrorDetail still allows budget_exhausted, which would bypass the required scope and reset_at")
		}
	}
	budget := schemaByName(t, doc, "BudgetExhaustedDetail")
	req := map[string]bool{}
	for _, r := range budget.Required {
		req[r] = true
	}
	for _, f := range []string{"code", "message", "request_id", "scope", "reset_at"} {
		if !req[f] {
			t.Errorf("BudgetExhaustedDetail does not require %q", f)
		}
	}
	if len(budget.Properties["code"].Value.Enum) != 1 || budget.Properties["code"].Value.Enum[0] != "budget_exhausted" {
		t.Error("BudgetExhaustedDetail must be fixed to the single code budget_exhausted")
	}
	branches := schemaByName(t, doc, "ErrorResponse").Properties["error"].Value.OneOf
	if len(branches) != 2 {
		t.Fatalf("ErrorResponse.error has %d branches, want exactly the generic and the budget detail", len(branches))
	}
}
