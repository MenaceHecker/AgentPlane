package mockmodel

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDefaultConfigIsValid(t *testing.T) {
	if err := DefaultConfig().Validate(); err != nil {
		t.Fatalf("the default configuration must validate: %v", err)
	}
}

func TestValidateRanges(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string // empty means valid
	}{
		{"error rate 0 is valid", func(c *Config) { c.ErrorRate = 0 }, ""},
		{"error rate 1 is valid", func(c *Config) { c.ErrorRate = 1 }, ""},
		{"error rate above 1", func(c *Config) { c.ErrorRate = 1.0001 }, "error-rate"},
		{"error rate 2", func(c *Config) { c.ErrorRate = 2 }, "error-rate"},
		{"negative error rate", func(c *Config) { c.ErrorRate = -0.1 }, "error-rate"},
		{"NaN error rate", func(c *Config) { c.ErrorRate = nan() }, "error-rate"},
		{"completion tokens 0 is valid", func(c *Config) { c.CompletionTokens = 0 }, ""},
		{"completion tokens at the maximum", func(c *Config) { c.CompletionTokens = MaxCompletionTokens }, ""},
		{"completion tokens above the maximum", func(c *Config) { c.CompletionTokens = MaxCompletionTokens + 1 }, "completion-tokens"},
		{"negative completion tokens", func(c *Config) { c.CompletionTokens = -1 }, "completion-tokens"},
		{"negative prompt tokens", func(c *Config) { c.PromptTokens = -1 }, "prompt-tokens"},
		{"prompt tokens above the safe range", func(c *Config) { c.PromptTokens = maxSafeInteger + 1 }, "prompt-tokens"},
		{"latency at the maximum", func(c *Config) { c.Latency = MaxLatency }, ""},
		{"latency above the maximum", func(c *Config) { c.Latency = MaxLatency + time.Nanosecond }, "latency"},
		{"negative latency", func(c *Config) { c.Latency = -time.Second }, "latency"},
		{"listen without a port", func(c *Config) { c.Listen = "127.0.0.1" }, "listen"},
		{"listen port out of range", func(c *Config) { c.Listen = "127.0.0.1:70000" }, "listen port"},
		{"listen port not a number", func(c *Config) { c.Listen = "127.0.0.1:http" }, "listen port"},
		{"port 0 is valid (an ephemeral port)", func(c *Config) { c.Listen = "127.0.0.1:0" }, ""},
		{"a non-loopback listen address is allowed as an explicit choice", func(c *Config) { c.Listen = "0.0.0.0:9000" }, ""},
		{"zero body limit", func(c *Config) { c.MaxBodyBytes = 0 }, "max body bytes"},
		{"body limit above the cap", func(c *Config) { c.MaxBodyBytes = maxBodyLimit + 1 }, "max body bytes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			tt.mutate(&cfg)
			err := cfg.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want one mentioning %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidateReportsEveryProblem(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ErrorRate, cfg.CompletionTokens, cfg.Latency = 2, -1, -time.Second
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{"error-rate", "completion-tokens", "latency"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}

func nan() float64 { var z float64; return z / z }

func TestParseFlags(t *testing.T) {
	good := []struct {
		name string
		args []string
		want func(Config) bool
	}{
		{"no arguments give the loopback defaults", nil, func(c Config) bool {
			return c.Listen == DefaultListen && c.Seed == 1 && c.CompletionTokens == DefaultCompletionTokens && c.ErrorRate == 0 && c.Latency == 0
		}},
		{"every flag", []string{"--listen=127.0.0.1:0", "--latency=250ms", "--prompt-tokens=7", "--completion-tokens=9", "--error-rate=0.25", "--seed=42"}, func(c Config) bool {
			return c.Listen == "127.0.0.1:0" && c.Latency == 250*time.Millisecond && c.PromptTokens == 7 &&
				c.CompletionTokens == 9 && c.ErrorRate == 0.25 && c.Seed == 42
		}},
		{"a negative seed is allowed", []string{"--seed=-5"}, func(c Config) bool { return c.Seed == -5 }},
		{"single-dash flags work too", []string{"-error-rate", "1"}, func(c Config) bool { return c.ErrorRate == 1 }},
	}
	for _, tt := range good {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			cfg, err := ParseFlags(tt.args, &out)
			if err != nil {
				t.Fatalf("unexpected error: %v (output %q)", err, out.String())
			}
			if !tt.want(cfg) {
				t.Fatalf("unexpected config %+v", cfg)
			}
		})
	}
	bad := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"error rate 2 (acceptance case)", []string{"--error-rate=2"}, "error-rate"},
		{"negative completion tokens (acceptance case)", []string{"--completion-tokens=-1"}, "completion-tokens"},
		{"negative error rate", []string{"--error-rate=-0.5"}, "error-rate"},
		{"latency over the cap", []string{"--latency=2m"}, "latency"},
		{"latency that is not a duration", []string{"--latency=fast"}, "latency"},
		{"error rate that is not a number", []string{"--error-rate=often"}, "error-rate"},
		{"unknown flag", []string{"--tenant=acme"}, "tenant"},
		{"positional argument", []string{"stray"}, "unexpected argument"},
		{"bad listen address", []string{"--listen=nowhere"}, "listen"},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			cfg, err := ParseFlags(tt.args, &out)
			if err == nil {
				t.Fatalf("expected an error, got config %+v", cfg)
			}
			if !strings.Contains(err.Error()+out.String(), tt.wantErr) {
				t.Fatalf("error %q / output %q do not mention %q", err, out.String(), tt.wantErr)
			}
			if cfg != (Config{}) {
				t.Errorf("a failed parse must not return a usable config: %+v", cfg)
			}
		})
	}
}

func TestHelpDocumentsEveryFlag(t *testing.T) {
	var out bytes.Buffer
	_, err := ParseFlags([]string{"-h"}, &out)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("-h must return flag.ErrHelp, got %v", err)
	}
	for _, f := range []string{"--listen", "-listen", "latency", "prompt-tokens", "completion-tokens", "error-rate", "seed"} {
		if !strings.Contains(out.String(), strings.TrimPrefix(f, "--")) {
			t.Errorf("usage does not document %s:\n%s", f, out.String())
		}
	}
}

// ---- simulated failures ----

func failureReq(i int) string {
	return fmt.Sprintf(`{"model":"m","messages":[{"role":"user","content":"request number %d"}]}`, i)
}

func outcomes(t *testing.T, cfg Config, n int) string {
	t.Helper()
	h := NewHandler(cfg)
	var b strings.Builder
	for i := 0; i < n; i++ {
		switch rec := do(t, h, http.MethodPost, ChatPath, failureReq(i)); rec.Code {
		case http.StatusOK:
			b.WriteByte('S')
		case http.StatusInternalServerError:
			b.WriteByte('F')
		default:
			t.Fatalf("request %d: unexpected status %d", i, rec.Code)
		}
	}
	return b.String()
}

func TestErrorRateZeroNeverFailsAndOneAlwaysFails(t *testing.T) {
	cfg := DefaultConfig()
	if got := outcomes(t, cfg, 40); strings.Contains(got, "F") {
		t.Errorf("error rate 0 produced failures: %s", got)
	}
	cfg.ErrorRate = 1
	if got := outcomes(t, cfg, 40); strings.Contains(got, "S") {
		t.Errorf("error rate 1 produced successes: %s", got)
	}
}

// The golden patterns freeze the deterministic selection for a seed. They are
// not statistical assertions: the same seed and requests must always give
// exactly these outcomes, and a different seed must select differently.
func TestIntermediateErrorRateIsStableAndSeedDependent(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ErrorRate = 0.5
	const wantSeed1 = "SFFSFFSFSSSFSFSF"
	const wantSeed2 = "SFFFSFSFSFSSSFSF"
	for round := 0; round < 3; round++ {
		if got := outcomes(t, cfg, 16); got != wantSeed1 {
			t.Fatalf("round %d seed 1 outcomes = %s, want %s", round, got, wantSeed1)
		}
	}
	cfg.Seed = 2
	if got := outcomes(t, cfg, 16); got != wantSeed2 {
		t.Fatalf("seed 2 outcomes = %s, want %s", got, wantSeed2)
	}
	if wantSeed1 == wantSeed2 {
		t.Fatal("the golden patterns must differ so the test proves seed dependence")
	}
}

func TestFailureSelectionIgnoresArrivalOrder(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ErrorRate = 0.5
	h := NewHandler(cfg)
	forward := make([]int, 16)
	for i := 0; i < 16; i++ {
		forward[i] = do(t, h, http.MethodPost, ChatPath, failureReq(i)).Code
	}
	for i := 15; i >= 0; i-- { // reverse order, on the same handler
		if got := do(t, h, http.MethodPost, ChatPath, failureReq(i)).Code; got != forward[i] {
			t.Fatalf("request %d changed outcome with arrival order: %d vs %d", i, forward[i], got)
		}
	}
	var wg sync.WaitGroup
	mismatch := make(chan int, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, ChatPath, strings.NewReader(failureReq(i))))
			if rec.Code != forward[i] {
				mismatch <- i
			}
		}(i)
	}
	wg.Wait()
	close(mismatch)
	for i := range mismatch {
		t.Errorf("request %d changed outcome under concurrency", i)
	}
}

// A request that fails at some error rate fails at every higher rate, because
// the decision is "score < rate" on a fixed per-request score.
func TestFailureIsMonotonicInTheErrorRate(t *testing.T) {
	rates := []float64{0.1, 0.3, 0.5, 0.7, 0.9, 1}
	fails := func(rate float64) []bool {
		cfg := DefaultConfig()
		cfg.ErrorRate = rate
		h := NewHandler(cfg)
		out := make([]bool, 50)
		for i := range out {
			out[i] = do(t, h, http.MethodPost, ChatPath, failureReq(i)).Code == http.StatusInternalServerError
		}
		return out
	}
	prev := fails(rates[0])
	for _, r := range rates[1:] {
		cur := fails(r)
		for i := range cur {
			if prev[i] && !cur[i] {
				t.Fatalf("request %d fails at a lower rate but not at %v", i, r)
			}
		}
		prev = cur
	}
}

func TestSimulatedFailureBody(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ErrorRate = 1
	rec := do(t, NewHandler(cfg), http.MethodPost, ChatPath, minimalReq)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"code":"internal_error"`) || !strings.Contains(rec.Body.String(), "simulated failure") {
		t.Errorf("body = %s", rec.Body.String())
	}
	doc := loadContract(t)
	if err := validateSchema(t, doc, "InternalErrorResponse", rec.Body.Bytes()); err != nil {
		t.Errorf("failure body does not satisfy the contract's 500 schema: %v", err)
	}
	// Invalid requests are rejected before any failure simulation.
	if rec := do(t, NewHandler(cfg), http.MethodPost, ChatPath, `{}`); rec.Code != http.StatusBadRequest {
		t.Errorf("an invalid request must be 400 even at error rate 1, got %d", rec.Code)
	}
}

// ---- simulated latency (no real sleeping) ----

type fakeWait struct {
	mu    sync.Mutex
	calls []time.Duration
	err   error
}

func (f *fakeWait) wait(_ context.Context, d time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, d)
	return f.err
}

func TestLatencyIsSimulatedThroughTheInjectedWait(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Latency = 250 * time.Millisecond
	fw := &fakeWait{}
	h := NewHandler(cfg, WithWait(fw.wait))
	start := time.Now()
	if rec := do(t, h, http.MethodPost, ChatPath, minimalReq); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if time.Since(start) > 200*time.Millisecond {
		t.Fatal("the test slept for real; latency must go through the injected wait")
	}
	if len(fw.calls) != 1 || fw.calls[0] != 250*time.Millisecond {
		t.Fatalf("wait calls = %v, want one call of 250ms", fw.calls)
	}
	// Failures also take the simulated time; invalid requests do not.
	cfg.ErrorRate = 1
	fw2 := &fakeWait{}
	h = NewHandler(cfg, WithWait(fw2.wait))
	do(t, h, http.MethodPost, ChatPath, minimalReq)
	do(t, h, http.MethodPost, ChatPath, `{}`)
	if len(fw2.calls) != 1 {
		t.Errorf("wait calls = %v, want exactly one (the failed valid request)", fw2.calls)
	}
}

func TestZeroLatencyDoesNotWait(t *testing.T) {
	fw := &fakeWait{}
	do(t, NewHandler(DefaultConfig(), WithWait(fw.wait)), http.MethodPost, ChatPath, minimalReq)
	if len(fw.calls) != 0 {
		t.Fatalf("zero latency must not wait, got %v", fw.calls)
	}
}

func TestCancelledRequestIsAbandonedDuringLatency(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Latency = time.Minute
	fw := &fakeWait{err: context.Canceled}
	h := NewHandler(cfg, WithWait(fw.wait))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, ChatPath, strings.NewReader(minimalReq)))
	if rec.Body.Len() != 0 || rec.Header().Get("X-Request-Id") != "" {
		t.Fatalf("a cancelled request must write nothing, got %q", rec.Body.String())
	}
}

func TestRealWaitHonoursCancellationAndDuration(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if err := RealWait(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("RealWait on a cancelled context = %v, want context.Canceled", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("RealWait did not return promptly after cancellation")
	}
	if err := RealWait(context.Background(), time.Millisecond); err != nil {
		t.Fatalf("a short wait must complete: %v", err)
	}
	if err := RealWait(context.Background(), 0); err != nil {
		t.Fatalf("a zero wait must return immediately: %v", err)
	}
}
