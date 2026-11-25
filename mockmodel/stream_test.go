package mockmodel

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
)

const streamReq = `{"model":"mock-large","stream":true,"messages":[{"role":"user","content":"Summarize this document please."}]}`

// parseEvents splits an SSE body into its data payloads, enforcing the
// "data: <payload>\n\n" framing.
func parseEvents(body string) ([]string, error) {
	if !strings.HasSuffix(body, "\n\n") {
		return nil, errors.New("stream does not end with a blank line")
	}
	var out []string
	for _, block := range strings.Split(strings.TrimSuffix(body, "\n\n"), "\n\n") {
		if !strings.HasPrefix(block, "data: ") || strings.Contains(block, "\n") {
			return nil, fmt.Errorf("malformed SSE block %q", block)
		}
		out = append(out, strings.TrimPrefix(block, "data: "))
	}
	return out, nil
}

// checkStreamBody applies the P0-T3 stream contract: content chunks that
// satisfy ChatCompletionChunk, exactly one terminal usage chunk (empty
// choices), then [DONE], nothing after.
func checkStreamBody(t *testing.T, doc *openapi3.T, body string) error {
	t.Helper()
	events, err := parseEvents(body)
	if err != nil {
		return err
	}
	usage := 0
	for i, ev := range events {
		if ev == "[DONE]" {
			if i != len(events)-1 {
				return errors.New("events after [DONE]")
			}
			if usage != 1 {
				return fmt.Errorf("want exactly one terminal usage chunk before [DONE], got %d", usage)
			}
			return nil
		}
		if err := validateSchema(t, doc, "ChatCompletionChunk", []byte(ev)); err != nil {
			return fmt.Errorf("event %d: %v", i, err)
		}
		var c struct {
			Choices []json.RawMessage `json:"choices"`
			Usage   *Usage            `json:"usage"`
		}
		if err := json.Unmarshal([]byte(ev), &c); err != nil {
			return err
		}
		if c.Usage != nil {
			if len(c.Choices) != 0 {
				return fmt.Errorf("event %d: usage chunk must have empty choices", i)
			}
			usage++
		} else if len(c.Choices) == 0 {
			return fmt.Errorf("event %d: empty choices without usage", i)
		}
	}
	return errors.New("no [DONE] at the end of the stream")
}

type assembled struct {
	content, id, model, finish string
	usage                      Usage
	roles                      int
}

func assemble(t *testing.T, body string) assembled {
	t.Helper()
	events, err := parseEvents(body)
	if err != nil {
		t.Fatal(err)
	}
	var a assembled
	for _, ev := range events {
		if ev == "[DONE]" {
			continue
		}
		var c ChatChunk
		if err := json.Unmarshal([]byte(ev), &c); err != nil {
			t.Fatalf("chunk %q: %v", ev, err)
		}
		a.id, a.model = c.ID, c.Model
		if c.Usage != nil {
			a.usage = *c.Usage
		}
		for _, ch := range c.Choices {
			if ch.Delta.Role != "" {
				a.roles++
			}
			if ch.Delta.Content != nil {
				a.content += *ch.Delta.Content
			}
			if ch.FinishReason != nil {
				a.finish = *ch.FinishReason
			}
		}
	}
	return a
}

func TestStreamSequenceAndFraming(t *testing.T) {
	doc := loadContract(t)
	rec := do(t, NewHandler(DefaultConfig()), http.MethodPost, ChatPath, streamReq)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("status %d content-type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if rec.Header().Get("Cache-Control") != "no-cache" || rec.Header().Get("X-Request-Id") == "" {
		t.Errorf("headers = %v", rec.Header())
	}
	if err := checkStreamBody(t, doc, rec.Body.String()); err != nil {
		t.Fatalf("%v\n%s", err, rec.Body.String())
	}
	events, _ := parseEvents(rec.Body.String())
	// 16 content chunks + the finish chunk + the usage chunk + [DONE]
	if want := DefaultCompletionTokens + 3; len(events) != want {
		t.Fatalf("got %d events, want %d", len(events), want)
	}
	if events[len(events)-1] != "[DONE]" {
		t.Errorf("last event = %q", events[len(events)-1])
	}
	for i, ev := range events[:DefaultCompletionTokens] {
		var raw map[string]any
		if err := json.Unmarshal([]byte(ev), &raw); err != nil {
			t.Fatal(err)
		}
		choice := raw["choices"].([]any)[0].(map[string]any)
		if fr, present := choice["finish_reason"]; !present || fr != nil {
			t.Errorf("content chunk %d must carry finish_reason null, got %v (present %v)", i, fr, present)
		}
		delta := choice["delta"].(map[string]any)
		if _, hasRole := delta["role"]; hasRole != (i == 0) {
			t.Errorf("chunk %d: role present = %v, only the first chunk carries the role", i, hasRole)
		}
	}
	final := assemble(t, rec.Body.String())
	if final.finish != "stop" || final.roles != 1 {
		t.Errorf("finish %q roles %d", final.finish, final.roles)
	}
}

// The assembled stream and the non-streaming response must agree on content,
// usage, id, model and finish reason.
func TestStreamMatchesNonStream(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
		extra  string
	}{
		{"defaults", func(*Config) {}, ""},
		{"max_tokens truncates and reports length", func(*Config) {}, `,"max_tokens":3`},
		{"a configured prompt count", func(c *Config) { c.PromptTokens = 41 }, ""},
		{"a different seed", func(c *Config) { c.Seed = 7 }, ""},
		{"an empty completion", func(c *Config) { c.CompletionTokens = 0 }, ""},
		{"one completion token", func(c *Config) { c.CompletionTokens = 1 }, ""},
		{"a long completion", func(c *Config) { c.CompletionTokens = 500 }, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			tt.mutate(&cfg)
			h := NewHandler(cfg)
			plain := `{"model":"mock-large","messages":[{"role":"user","content":"Summarize this document please."}]` + tt.extra + `}`
			streamed := `{"model":"mock-large","stream":true,"messages":[{"role":"user","content":"Summarize this document please."}]` + tt.extra + `}`
			non := decodeResp(t, do(t, h, http.MethodPost, ChatPath, plain))
			a := assemble(t, do(t, h, http.MethodPost, ChatPath, streamed).Body.String())
			if a.content != non.Choices[0].Message.Content {
				t.Errorf("content differs:\n stream %q\n plain  %q", a.content, non.Choices[0].Message.Content)
			}
			if a.usage != non.Usage {
				t.Errorf("usage differs: stream %+v plain %+v", a.usage, non.Usage)
			}
			if a.finish != non.Choices[0].FinishReason || a.model != non.Model {
				t.Errorf("finish/model differ: %q/%q vs %q/%q", a.finish, a.model, non.Choices[0].FinishReason, non.Model)
			}
			if a.id == "" || strings.HasPrefix(a.id, "chatcmpl-") == false {
				t.Errorf("stream id = %q", a.id)
			}
		})
	}
}

func TestStreamIsDeterministicAndConcurrencySafe(t *testing.T) {
	h := NewHandler(DefaultConfig())
	want := do(t, h, http.MethodPost, ChatPath, streamReq).Body.String()
	for i := 0; i < 3; i++ {
		if got := do(t, h, http.MethodPost, ChatPath, streamReq).Body.String(); got != want {
			t.Fatalf("repeat %d differs", i)
		}
	}
	var wg sync.WaitGroup
	bad := make(chan string, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, ChatPath, strings.NewReader(streamReq)))
			if rec.Body.String() != want {
				bad <- rec.Body.String()
			}
		}()
	}
	wg.Wait()
	close(bad)
	for range bad {
		t.Error("a concurrent stream differs from the reference")
	}
}

func TestStreamWithEmptyCompletion(t *testing.T) {
	cfg := DefaultConfig()
	cfg.CompletionTokens = 0
	doc := loadContract(t)
	body := do(t, NewHandler(cfg), http.MethodPost, ChatPath, streamReq).Body.String()
	if err := checkStreamBody(t, doc, body); err != nil {
		t.Fatal(err)
	}
	events, _ := parseEvents(body)
	// an empty role chunk, the finish chunk, the usage chunk, [DONE]
	if len(events) != 4 {
		t.Fatalf("got %d events: %v", len(events), events)
	}
}

func TestStreamErrorsAreJSONBeforeAnyStreaming(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ErrorRate = 1
	rec := do(t, NewHandler(cfg), http.MethodPost, ChatPath, streamReq)
	if rec.Code != http.StatusInternalServerError || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") ||
		strings.Contains(rec.Body.String(), "data:") {
		t.Fatalf("a simulated failure must be a plain JSON 500 with no partial stream: %d %q %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
	invalid := `{"model":"","stream":true,"messages":[{"role":"user","content":"x"}]}`
	if rec := do(t, NewHandler(DefaultConfig()), http.MethodPost, ChatPath, invalid); rec.Code != http.StatusBadRequest ||
		!strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("invalid streaming input must be a JSON 400, got %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestStreamAppliesLatencyOnceBeforeTheFirstEvent(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Latency = time.Second
	fw := &fakeWait{}
	do(t, NewHandler(cfg, WithWait(fw.wait)), http.MethodPost, ChatPath, streamReq)
	if len(fw.calls) != 1 || fw.calls[0] != time.Second {
		t.Fatalf("wait calls = %v, want a single 1s wait", fw.calls)
	}
}

// ---- fake writers for cancellation and disconnects ----

type fakeStreamWriter struct {
	header      http.Header
	status      int
	mu          sync.Mutex
	dataWrites  int
	attempts    int // every Write call, including the failing one
	flushes     int
	failAfter   int // fail data writes after this many (0 = never)
	afterWrite  func(n int)
	writeFailed bool
}

func (f *fakeStreamWriter) Header() http.Header {
	if f.header == nil {
		f.header = http.Header{}
	}
	return f.header
}
func (f *fakeStreamWriter) WriteHeader(code int) { f.status = code }
func (f *fakeStreamWriter) Write(b []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts++
	if f.failAfter > 0 && f.dataWrites >= f.failAfter {
		f.writeFailed = true
		return 0, errors.New("broken pipe")
	}
	f.dataWrites++
	if f.afterWrite != nil {
		f.afterWrite(f.dataWrites)
	}
	return len(b), nil
}
func (f *fakeStreamWriter) Flush() { f.flushes++ }

func TestStreamStopsWhenTheClientCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fw := &fakeStreamWriter{afterWrite: func(n int) {
		if n == 3 {
			cancel() // the client goes away after three events
		}
	}}
	req := httptest.NewRequest(http.MethodPost, ChatPath, strings.NewReader(streamReq)).WithContext(ctx)
	NewHandler(DefaultConfig()).ServeHTTP(fw, req)
	if fw.dataWrites != 3 {
		t.Fatalf("handler wrote %d events after cancellation at 3; it must stop immediately", fw.dataWrites)
	}
}

func TestStreamStopsOnWriteError(t *testing.T) {
	fw := &fakeStreamWriter{failAfter: 2}
	req := httptest.NewRequest(http.MethodPost, ChatPath, strings.NewReader(streamReq))
	NewHandler(DefaultConfig()).ServeHTTP(fw, req)
	if !fw.writeFailed || fw.dataWrites != 2 {
		t.Fatalf("writes = %d (failed %v); the handler must stop at the first write error", fw.dataWrites, fw.writeFailed)
	}
	if fw.attempts != 3 {
		t.Fatalf("the handler made %d write attempts, want 3 (two that succeeded and the one that failed); it kept writing after an error", fw.attempts)
	}
}

func TestStreamAlreadyCancelledWritesNoEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fw := &fakeStreamWriter{}
	req := httptest.NewRequest(http.MethodPost, ChatPath, strings.NewReader(streamReq)).WithContext(ctx)
	NewHandler(DefaultConfig()).ServeHTTP(fw, req)
	if fw.dataWrites != 0 {
		t.Fatalf("a cancelled request produced %d events", fw.dataWrites)
	}
}

type noFlushWriter struct {
	h    http.Header
	code int
	body strings.Builder
}

func (n *noFlushWriter) Header() http.Header {
	if n.h == nil {
		n.h = http.Header{}
	}
	return n.h
}
func (n *noFlushWriter) WriteHeader(c int)           { n.code = c }
func (n *noFlushWriter) Write(b []byte) (int, error) { return n.body.Write(b) }

func TestStreamRequiresAFlusher(t *testing.T) {
	w := &noFlushWriter{}
	NewHandler(DefaultConfig()).ServeHTTP(w, httptest.NewRequest(http.MethodPost, ChatPath, strings.NewReader(streamReq)))
	if w.code != http.StatusInternalServerError || strings.Contains(w.body.String(), "data:") {
		t.Fatalf("without a flusher the mock must answer a JSON 500, got %d %q", w.code, w.body.String())
	}
}

// ---- a real connection: chunks are flushed incrementally ----

// gatedWriter passes everything through unchanged, but after the mock FLUSHES
// the first data event it blocks until released. It never flushes on its own,
// so the client can only see that event if the mock itself flushed it.
type gatedWriter struct {
	http.ResponseWriter
	wroteData bool
	once      sync.Once
	reached   chan struct{}
	release   chan struct{}
}

func (g *gatedWriter) Flush() {
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
	if g.wroteData {
		g.once.Do(func() {
			close(g.reached)
			<-g.release
		})
	}
}

func (g *gatedWriter) Write(b []byte) (int, error) {
	if strings.HasPrefix(string(b), "data: ") {
		g.wroteData = true
	}
	return g.ResponseWriter.Write(b)
}

func TestStreamIsFlushedIncrementallyOverARealConnection(t *testing.T) {
	inner := NewHandler(DefaultConfig())
	reached, release := make(chan struct{}), make(chan struct{})
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		inner.ServeHTTP(&gatedWriter{ResponseWriter: w, reached: reached, release: release}, r)
	}))
	defer srv.Close()

	resp, err := http.Post(srv.URL+ChatPath, "application/json", strings.NewReader(streamReq))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type = %q", ct)
	}
	lines := bufio.NewScanner(resp.Body)
	first := make(chan string, 1)
	go func() {
		for lines.Scan() {
			if strings.HasPrefix(lines.Text(), "data: ") {
				first <- lines.Text()
				return
			}
		}
		first <- ""
	}()
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler never reached its first event")
	}
	select {
	case line := <-first:
		if !strings.Contains(line, `"role":"assistant"`) {
			t.Fatalf("first event = %q", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the first event was not delivered while the handler was still running: the mock does not flush each chunk")
	}
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler did not finish after the gate was released")
	}
}
