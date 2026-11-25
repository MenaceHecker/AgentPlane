package mockmodel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// The gateway contract limits every token count, and total_tokens, to the
// lossless range 0..2^53-1 (docs/gateway/openapi.yaml, Usage). Prompt and
// completion are validated independently, so an accepted configuration could
// add up past that limit and make every response contract-invalid (review r1,
// finding 2). These tests pin the aggregate rule at its exact boundary.

func usageRaw(t *testing.T, body []byte) map[string]json.Number {
	t.Helper()
	var r struct {
		Usage map[string]json.Number `json:"usage"`
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	if err := d.Decode(&r); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	return r.Usage
}

// streamUsageRaw returns the usage member of the terminal usage chunk, with
// numbers kept as exact decimal text.
func streamUsageRaw(t *testing.T, sse string) map[string]json.Number {
	t.Helper()
	events, err := parseEvents(sse)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if ev == "[DONE]" {
			continue
		}
		if u := usageRaw(t, []byte(ev)); len(u) > 0 {
			return u
		}
	}
	t.Fatal("no usage chunk in the stream")
	return nil
}

func TestAggregateUsageBoundaryInTheConfiguration(t *testing.T) {
	const max = maxSafeInteger
	tests := []struct {
		name               string
		prompt, completion int64
		wantErr            bool
	}{
		{"exact boundary: prompt plus completion equals 2^53-1", max - 16, 16, false},
		{"one over the boundary", max - 15, 16, true},
		{"zero completion at the maximum prompt", max, 0, false},
		{"maximum prompt with one completion token", max, 1, true},
		{"maximum completion with the largest allowed prompt", max - MaxCompletionTokens, MaxCompletionTokens, false},
		{"maximum completion with one more prompt token", max - MaxCompletionTokens + 1, MaxCompletionTokens, true},
		{"small values", 4, 16, false},
		{"derived prompt (0) with the default completion", 0, 16, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.PromptTokens, cfg.CompletionTokens = tt.prompt, tt.completion
			err := cfg.Validate()
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), "prompt-tokens") || !strings.Contains(err.Error(), "completion-tokens") {
					t.Fatalf("expected an error naming both flags, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestAggregateUsageBoundaryThroughFlags(t *testing.T) {
	var out bytes.Buffer
	if _, err := ParseFlags([]string{fmt.Sprintf("--prompt-tokens=%d", maxSafeInteger-15), "--completion-tokens=16"}, &out); err == nil ||
		!strings.Contains(err.Error(), "prompt-tokens") || !strings.Contains(err.Error(), "completion-tokens") {
		t.Fatalf("one over the boundary must be a configuration error naming both flags, got %v", err)
	}
	cfg, err := ParseFlags([]string{fmt.Sprintf("--prompt-tokens=%d", maxSafeInteger-16), "--completion-tokens=16"}, &out)
	if err != nil {
		t.Fatalf("the exact boundary must be accepted: %v", err)
	}
	if cfg.PromptTokens+cfg.CompletionTokens != maxSafeInteger {
		t.Fatalf("sum = %d", cfg.PromptTokens+cfg.CompletionTokens)
	}
}

// Every accepted configuration must produce usage the contract accepts, in the
// JSON response and in the terminal stream chunk, with the exact decimal sum.
func TestAcceptedConfigurationsKeepUsageInsideTheContract(t *testing.T) {
	doc := loadContract(t)
	const max = maxSafeInteger
	tests := []struct {
		name               string
		prompt, completion int64
		extra              string // extra request members
		wantPrompt         int64
		wantCompletion     int64
	}{
		{"exact boundary", max - 16, 16, "", max - 16, 16},
		{"zero completion at the maximum prompt", max, 0, "", max, 0},
		{"max_tokens caps the completion below the configured value", max - 16, 16, `,"max_tokens":3`, max - 16, 3},
		{"max_tokens larger than the configured completion", max - 16, 16, `,"max_tokens":1000`, max - 16, 16},
		{"one completion token", max - 1, 1, "", max - 1, 1},
		{"largest completion", max - MaxCompletionTokens, MaxCompletionTokens, "", max - MaxCompletionTokens, MaxCompletionTokens},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.PromptTokens, cfg.CompletionTokens = tt.prompt, tt.completion
			if err := cfg.Validate(); err != nil {
				t.Fatalf("this configuration must be accepted: %v", err)
			}
			h := NewHandler(cfg)
			plain := `{"model":"m","messages":[{"role":"user","content":"a b c"}]` + tt.extra + `}`
			streamed := `{"model":"m","stream":true,"messages":[{"role":"user","content":"a b c"}]` + tt.extra + `}`
			wantTotal := fmt.Sprint(tt.wantPrompt + tt.wantCompletion)

			rec := do(t, h, http.MethodPost, ChatPath, plain)
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			if err := validateSchema(t, doc, "ChatCompletionResponse", rec.Body.Bytes()); err != nil {
				t.Fatalf("JSON response violates the contract: %v", err)
			}
			u := usageRaw(t, rec.Body.Bytes())
			if u["prompt_tokens"].String() != fmt.Sprint(tt.wantPrompt) || u["completion_tokens"].String() != fmt.Sprint(tt.wantCompletion) || u["total_tokens"].String() != wantTotal {
				t.Fatalf("JSON usage = %v, want %d + %d = %s as exact decimals", u, tt.wantPrompt, tt.wantCompletion, wantTotal)
			}

			srec := do(t, h, http.MethodPost, ChatPath, streamed)
			if err := checkStreamBody(t, doc, srec.Body.String()); err != nil {
				t.Fatalf("stream violates the contract: %v", err)
			}
			su := streamUsageRaw(t, srec.Body.String())
			if su["prompt_tokens"] != u["prompt_tokens"] || su["completion_tokens"] != u["completion_tokens"] || su["total_tokens"] != u["total_tokens"] {
				t.Fatalf("stream usage %v differs from JSON usage %v", su, u)
			}
		})
	}
}

// A sweep around the boundary: for every (prompt, completion) pair either the
// configuration is rejected, or it is accepted AND every response stays inside
// the contract. No accepted configuration may emit an out-of-domain total.
func TestNoAcceptedConfigurationEmitsAnOutOfDomainTotal(t *testing.T) {
	doc := loadContract(t)
	accepted, rejected := 0, 0
	for dp := int64(0); dp <= 20; dp++ {
		for c := int64(0); c <= 20; c++ {
			cfg := DefaultConfig()
			cfg.PromptTokens, cfg.CompletionTokens = maxSafeInteger-dp, c
			if err := cfg.Validate(); err != nil {
				rejected++
				if dp >= c {
					t.Errorf("prompt max-%d completion %d sums to at most the maximum and must be accepted: %v", dp, c, err)
				}
				continue
			}
			accepted++
			if dp < c {
				t.Errorf("prompt max-%d completion %d exceeds the maximum yet was accepted", dp, c)
			}
			rec := do(t, NewHandler(cfg), http.MethodPost, ChatPath, minimalReq)
			if err := validateSchema(t, doc, "ChatCompletionResponse", rec.Body.Bytes()); err != nil {
				t.Errorf("accepted prompt max-%d completion %d produced a contract-invalid response: %v", dp, c, err)
			}
		}
	}
	if accepted == 0 || rejected == 0 {
		t.Fatalf("the sweep did not cross the boundary (accepted %d, rejected %d)", accepted, rejected)
	}
}
