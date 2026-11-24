package mockmodel

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
)

// ---- helpers ----

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test directory")
		}
		dir = parent
	}
}

func do(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rd *strings.Reader
	if body != "" {
		rd = strings.NewReader(body)
	} else {
		rd = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, rd)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

const minimalReq = `{"model":"mock-large","messages":[{"role":"user","content":"Summarize this document please."}]}`

func newTestHandler(cfg Config) http.Handler { return NewHandler(cfg) }

func decodeResp(t *testing.T, rec *httptest.ResponseRecorder) ChatResponse {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var r ChatResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatalf("response is not JSON: %v\n%s", err, rec.Body.String())
	}
	return r
}

// ---- contract (P0-T3 OpenAPI) ----

func loadContract(t *testing.T) *openapi3.T {
	t.Helper()
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromFile(filepath.Join(repoRoot(t), "docs", "gateway", "openapi.yaml"))
	if err != nil {
		t.Fatalf("load openapi.yaml: %v", err)
	}
	return doc
}

func validateSchema(t *testing.T, doc *openapi3.T, name string, raw []byte) error {
	t.Helper()
	ref, ok := doc.Components.Schemas[name]
	if !ok {
		t.Fatalf("schema %s not in the contract", name)
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	return ref.Value.VisitJSON(v, openapi3.EnableFormatValidation())
}

// ---- determinism ----

func TestChatIsDeterministic(t *testing.T) {
	h := newTestHandler(DefaultConfig())
	first := do(t, h, http.MethodPost, ChatPath, minimalReq).Body.String()
	for i := 0; i < 5; i++ {
		if got := do(t, h, http.MethodPost, ChatPath, minimalReq).Body.String(); got != first {
			t.Fatalf("repeat %d differs:\n%s\n%s", i, first, got)
		}
	}
	// A new handler instance with the same configuration answers identically
	// too: nothing depends on process state, arrival order or the clock.
	if got := do(t, newTestHandler(DefaultConfig()), http.MethodPost, ChatPath, minimalReq).Body.String(); got != first {
		t.Fatalf("a fresh handler answered differently:\n%s\n%s", first, got)
	}
}

func TestChatIsDeterministicUnderConcurrency(t *testing.T) {
	h := newTestHandler(DefaultConfig())
	want := do(t, h, http.MethodPost, ChatPath, minimalReq).Body.String()
	var wg sync.WaitGroup
	errs := make(chan string, 64)
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, ChatPath, strings.NewReader(minimalReq)))
			if rec.Body.String() != want {
				errs <- rec.Body.String()
			}
		}()
	}
	wg.Wait()
	close(errs)
	for got := range errs {
		t.Errorf("concurrent response differs:\n%s\n%s", want, got)
	}
}

func TestChatIgnoresJSONFormatting(t *testing.T) {
	h := newTestHandler(DefaultConfig())
	a := do(t, h, http.MethodPost, ChatPath, minimalReq).Body.String()
	reordered := "{\n  \"messages\": [ {\"content\": \"Summarize this document please.\", \"role\": \"user\"} ],\n  \"model\": \"mock-large\"\n}\n"
	if b := do(t, h, http.MethodPost, ChatPath, reordered).Body.String(); a != b {
		t.Fatalf("key order and whitespace changed the response:\n%s\n%s", a, b)
	}
}

func TestSeedAndRequestChangeTheOutput(t *testing.T) {
	base := decodeResp(t, do(t, newTestHandler(DefaultConfig()), http.MethodPost, ChatPath, minimalReq))
	other := DefaultConfig()
	other.Seed = 2
	seeded := decodeResp(t, do(t, newTestHandler(other), http.MethodPost, ChatPath, minimalReq))
	if seeded.ID == base.ID || seeded.Choices[0].Message.Content == base.Choices[0].Message.Content {
		t.Errorf("a different seed must change id and content: %q vs %q", base.ID, seeded.ID)
	}
	differ := decodeResp(t, do(t, newTestHandler(DefaultConfig()), http.MethodPost, ChatPath,
		`{"model":"mock-large","messages":[{"role":"user","content":"A different question."}]}`))
	if differ.ID == base.ID || differ.Choices[0].Message.Content == base.Choices[0].Message.Content {
		t.Errorf("a different request must change id and content")
	}
}

func TestResponseShapeAndStableMetadata(t *testing.T) {
	rec := do(t, newTestHandler(DefaultConfig()), http.MethodPost, ChatPath, minimalReq)
	r := decodeResp(t, rec)
	if r.Object != "chat.completion" || r.Created != fixedCreated || r.Model != "mock-large" {
		t.Errorf("metadata = %q %d %q", r.Object, r.Created, r.Model)
	}
	if !strings.HasPrefix(r.ID, "chatcmpl-") || len(r.ID) != len("chatcmpl-")+16 {
		t.Errorf("id = %q", r.ID)
	}
	if rec.Header().Get("X-Request-Id") != r.ID {
		t.Errorf("X-Request-Id %q != id %q", rec.Header().Get("X-Request-Id"), r.ID)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content type = %q", ct)
	}
	if len(r.Choices) != 1 || r.Choices[0].Message.Role != "assistant" || r.Choices[0].Index != 0 {
		t.Errorf("choices = %+v", r.Choices)
	}
	if got := len(strings.Fields(r.Choices[0].Message.Content)); int64(got) != r.Usage.CompletionTokens {
		t.Errorf("content has %d words but usage says %d completion tokens", got, r.Usage.CompletionTokens)
	}
}

func TestSimulatedUsageAndFinishReason(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*Config)
		body       string
		wantPrompt int64
		wantComp   int64
		wantFinish string
	}{
		{"defaults derive the prompt from the request words", func(*Config) {}, minimalReq, 4, 16, "stop"},
		{"configured prompt tokens override the derived count", func(c *Config) { c.PromptTokens = 99 }, minimalReq, 99, 16, "stop"},
		{"max_tokens caps the completion and reports length",
			func(*Config) {}, `{"model":"m","messages":[{"role":"user","content":"a b"}],"max_tokens":3}`, 2, 3, "length"},
		{"max_tokens above the simulated length does not cap",
			func(*Config) {}, `{"model":"m","messages":[{"role":"user","content":"a b"}],"max_tokens":1000}`, 2, 16, "stop"},
		{"max_tokens equal to the simulated length is not truncated",
			func(*Config) {}, `{"model":"m","messages":[{"role":"user","content":"a"}],"max_tokens":16}`, 1, 16, "stop"},
		{"an empty prompt still reports one simulated token",
			func(*Config) {}, `{"model":"m","messages":[{"role":"user","content":""}]}`, 1, 16, "stop"},
		{"a configured completion of zero is an empty completion",
			func(c *Config) { c.CompletionTokens = 0 }, minimalReq, 4, 0, "stop"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			tt.mutate(&cfg)
			r := decodeResp(t, do(t, newTestHandler(cfg), http.MethodPost, ChatPath, tt.body))
			if r.Usage.PromptTokens != tt.wantPrompt || r.Usage.CompletionTokens != tt.wantComp ||
				r.Usage.TotalTokens != tt.wantPrompt+tt.wantComp {
				t.Errorf("usage = %+v, want prompt %d completion %d", r.Usage, tt.wantPrompt, tt.wantComp)
			}
			if r.Choices[0].FinishReason != tt.wantFinish {
				t.Errorf("finish_reason = %q, want %q", r.Choices[0].FinishReason, tt.wantFinish)
			}
		})
	}
}

// ---- strict errors ----

func TestStrictRequestErrors(t *testing.T) {
	user := `"messages":[{"role":"user","content":"hi"}]`
	tests := []struct {
		name, body, wantMsg string
	}{
		{"not JSON", `not json`, "invalid JSON"},
		{"empty body", ``, "empty request body"},
		{"whitespace only", "  \n", "empty request body"},
		{"array instead of object", `[]`, "invalid JSON"},
		{"trailing data", minimalReq + ` {}`, "unexpected data"},
		{"missing model", `{` + user + `}`, "model"},
		{"empty model", `{"model":"",` + user + `}`, "model"},
		{"overlong model", `{"model":"` + strings.Repeat("m", 129) + `",` + user + `}`, "model"},
		{"model of the wrong type", `{"model":5,` + user + `}`, "invalid JSON"},
		{"no messages", `{"model":"m","messages":[]}`, "messages"},
		{"unknown role", `{"model":"m","messages":[{"role":"tool","content":"x"}]}`, "role"},
		{"content of the wrong type", `{"model":"m","messages":[{"role":"user","content":5}]}`, "invalid JSON"},
		{"unknown message field", `{"model":"m","messages":[{"role":"user","content":"x","name":"n"}]}`, "invalid JSON"},
		{"identity field tenant", `{"model":"m",` + user + `,"tenant":"acme"}`, "invalid JSON"},
		{"identity field agent", `{"model":"m",` + user + `,"agent":"a"}`, "invalid JSON"},
		{"unsupported field user", `{"model":"m",` + user + `,"user":"u"}`, "invalid JSON"},
		{"unsupported field tools", `{"model":"m",` + user + `,"tools":[]}`, "invalid JSON"},
		{"max_tokens zero", `{"model":"m",` + user + `,"max_tokens":0}`, "max_tokens"},
		{"max_tokens negative", `{"model":"m",` + user + `,"max_tokens":-1}`, "max_tokens"},
		{"max_tokens above the safe range", `{"model":"m",` + user + `,"max_tokens":9007199254740992}`, "max_tokens"},
		{"max_tokens fractional", `{"model":"m",` + user + `,"max_tokens":1.5}`, "invalid JSON"},
		{"temperature too high", `{"model":"m",` + user + `,"temperature":3}`, "temperature"},
		{"top_p negative", `{"model":"m",` + user + `,"top_p":-0.1}`, "top_p"},
		{"too many stop strings", `{"model":"m",` + user + `,"stop":["a","b","c","d","e"]}`, "stop"},
		{"empty stop string", `{"model":"m",` + user + `,"stop":[""]}`, "stop"},
		{"stream of the wrong type", `{"model":"m",` + user + `,"stream":"yes"}`, "invalid JSON"},
	}
	h := newTestHandler(DefaultConfig())
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, h, http.MethodPost, ChatPath, tt.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body %s", rec.Code, rec.Body.String())
			}
			var e errorResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
				t.Fatalf("error body is not JSON: %v", err)
			}
			if e.Error.Code != "invalid_request" || e.Error.RequestID == "" || !strings.Contains(e.Error.Message, tt.wantMsg) {
				t.Errorf("error = %+v, want code invalid_request and a message containing %q", e.Error, tt.wantMsg)
			}
			if rec.Header().Get("X-Request-Id") != e.Error.RequestID {
				t.Errorf("X-Request-Id %q != request_id %q", rec.Header().Get("X-Request-Id"), e.Error.RequestID)
			}
		})
	}
}

func TestBodyLimitIsEnforced(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MaxBodyBytes = int64(len(minimalReq))
	h := newTestHandler(cfg)
	if rec := do(t, h, http.MethodPost, ChatPath, minimalReq); rec.Code != http.StatusOK {
		t.Fatalf("a body of exactly the limit must be accepted, got %d: %s", rec.Code, rec.Body.String())
	}
	rec := do(t, h, http.MethodPost, ChatPath, minimalReq+" ")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "exceeds") {
		t.Fatalf("one byte over the limit must be rejected, got %d: %s", rec.Code, rec.Body.String())
	}
	huge := `{"model":"m","messages":[{"role":"user","content":"` + strings.Repeat("x", 4096) + `"}]}`
	if rec := do(t, h, http.MethodPost, ChatPath, huge); rec.Code != http.StatusBadRequest {
		t.Fatalf("an oversized body must be rejected, got %d", rec.Code)
	}
}

func TestMethodsAndPaths(t *testing.T) {
	h := newTestHandler(DefaultConfig())
	if rec := do(t, h, http.MethodGet, ChatPath, ""); rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "POST" {
		t.Errorf("GET chat = %d allow %q", rec.Code, rec.Header().Get("Allow"))
	}
	if rec := do(t, h, http.MethodPost, ReadyPath, ""); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST readyz = %d", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, "/v1/tools/web-search", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown path = %d, want 404 (the mock does not implement tools)", rec.Code)
	}
}

func TestReadiness(t *testing.T) {
	rec := do(t, newTestHandler(DefaultConfig()), http.MethodGet, ReadyPath, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok"`) {
		t.Fatalf("readyz = %d %s", rec.Code, rec.Body.String())
	}
}

// ---- contract conformance against the published P0-T3 fixtures ----

type gatewayManifest struct {
	Fixtures []struct {
		File   string `json:"file"`
		Kind   string `json:"kind"`
		Schema string `json:"schema"`
		Valid  bool   `json:"valid"`
	} `json:"fixtures"`
}

// TestContractFixtures replays every published ChatCompletionRequest fixture
// (docs/gateway/examples) against the mock and validates the responses with
// the OpenAPI schemas, so the mock cannot drift from the gateway contract.
func TestContractFixtures(t *testing.T) {
	doc := loadContract(t)
	dir := filepath.Join(repoRoot(t), "docs", "gateway", "examples")
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m gatewayManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	h := newTestHandler(DefaultConfig())
	var valid, invalid int
	for _, f := range m.Fixtures {
		if f.Kind != "schema" || f.Schema != "ChatCompletionRequest" {
			continue
		}
		t.Run(f.File, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join(dir, f.File))
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(body, []byte(`"stream": true`)) {
				t.Skip("streaming fixtures are exercised once streaming is implemented")
			}
			rec := do(t, h, http.MethodPost, ChatPath, string(body))
			if f.Valid {
				valid++
				if rec.Code != http.StatusOK {
					t.Fatalf("a contract-valid request was refused: %d %s", rec.Code, rec.Body.String())
				}
				if err := validateSchema(t, doc, "ChatCompletionResponse", rec.Body.Bytes()); err != nil {
					t.Fatalf("response does not satisfy ChatCompletionResponse: %v\n%s", err, rec.Body.String())
				}
				return
			}
			invalid++
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("a contract-invalid request was accepted: %d %s", rec.Code, rec.Body.String())
			}
			if err := validateSchema(t, doc, "InvalidRequestError", rec.Body.Bytes()); err != nil {
				t.Fatalf("error does not satisfy InvalidRequestError: %v\n%s", err, rec.Body.String())
			}
		})
	}
	if valid < 3 || invalid < 10 {
		t.Fatalf("fixture replay covered %d valid and %d invalid requests; the manifest changed or the filter is wrong", valid, invalid)
	}
}

// ---- defaults and lifecycle ----

func TestDefaultConfigListensOnLoopbackOnly(t *testing.T) {
	host, _, err := net.SplitHostPort(DefaultConfig().Listen)
	if err != nil {
		t.Fatal(err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		t.Fatalf("default listen host %q is not a loopback address", host)
	}
}

func TestHTTPServerHasBoundedTimeouts(t *testing.T) {
	srv := NewHTTPServer(DefaultConfig(), newTestHandler(DefaultConfig()))
	if srv.ReadHeaderTimeout <= 0 || srv.ReadTimeout <= 0 || srv.WriteTimeout <= 0 || srv.IdleTimeout <= 0 || srv.MaxHeaderBytes <= 0 {
		t.Fatalf("server limits must all be set: %+v", srv)
	}
}

func TestServeStartsAndShutsDownGracefully(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, NewHTTPServer(cfg, newTestHandler(cfg)), ln) }()

	url := "http://" + ln.Addr().String()
	client := &http.Client{Timeout: 2 * time.Second}
	var resp *http.Response
	for i := 0; i < 50; i++ {
		if resp, err = client.Get(url + ReadyPath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("server never became ready: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("readyz status = %d", resp.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned %v after cancellation, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return after the context was cancelled")
	}
	if c, err := net.DialTimeout("tcp", ln.Addr().String(), 500*time.Millisecond); err == nil {
		_ = c.Close()
		t.Fatal("the listener still accepts connections after shutdown")
	}
}
