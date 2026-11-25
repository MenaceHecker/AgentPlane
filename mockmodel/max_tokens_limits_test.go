package mockmodel

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

// Review r3, finding 1: ParseInt accepts exponents up to the int64 limits, and
// the arithmetic that followed (len(frac) - exp, negating the scale, a repeat
// count) overflowed there, panicking in makeslice or strings.Repeat. These
// tests use the exact int64 limits and their neighbours, with mantissas and
// fractions of different lengths, and require a plain error: no panic, no
// recovery, no work proportional to the exponent.

// limitExponents are the exponent spellings around every boundary that matters:
// the int64 limits, one step inside and outside them, and the 2^40 clamp.
func limitExponents() []string {
	var out []string
	add := func(v int64) { out = append(out, fmt.Sprint(v)) }
	for _, d := range []int64{0, 1, 2, 3, 16, 17} {
		add(math.MaxInt64 - d)
		add(math.MinInt64 + d)
	}
	for _, d := range []int64{-2, -1, 0, 1, 2} {
		add(1<<40 + d)
		add(-(1 << 40) + d)
		add(1<<31 + d)
		add(-(1 << 31) + d)
		add(1<<20 + d)
		add(-(1 << 20) + d)
	}
	// one step beyond int64 (ParseInt fails): both signs, with and without "+"
	out = append(out, "9223372036854775808", "+9223372036854775808", "-9223372036854775809", "99999999999999999999")
	return out
}

func limitMantissas() []string {
	return []string{
		"1", "5", "12", "9007199254740991", "10000000000000000",
		"0.5", "1.0", "12.5", "1.000000000000000", "123456789.123456789",
		"0." + strings.Repeat("0", 1000) + "1", "1." + strings.Repeat("0", 1000),
		strings.Repeat("9", 400), strings.Repeat("1", 40) + "." + strings.Repeat("2", 40),
		"0", "0.0", "0." + strings.Repeat("0", 50),
	}
}

func TestParseMaxTokensNeverPanicsAtIntegerLimits(t *testing.T) {
	for _, m := range limitMantissas() {
		for _, e := range limitExponents() {
			for _, mark := range []string{"e", "E"} {
				text := m + mark + e
				t.Run(shorten(text), func(t *testing.T) {
					var before, after runtime.MemStats
					runtime.GC()
					runtime.ReadMemStats(&before)
					n, err := parseMaxTokens(json.RawMessage(text))
					runtime.ReadMemStats(&after)
					if err == nil {
						t.Fatalf("%s must be refused, got %d", shorten(text), n)
					}
					if !strings.Contains(err.Error(), "max_tokens") {
						t.Errorf("the error must name max_tokens: %v", err)
					}
					if grew := after.TotalAlloc - before.TotalAlloc; grew > 1<<20 {
						t.Errorf("refusing %s allocated %d bytes", shorten(text), grew)
					}
				})
			}
		}
	}
}

// The clamp must not change any answer: a huge exponent is refused for the same
// reason a merely large one is, and a mantissa that cancels a small exponent is
// still accepted exactly.
func TestSmallExponentsStillCancelExactlyNextToTheLimits(t *testing.T) {
	ok := []string{"5e0", "50e-1", "0.5e1", "5000e-3", "1" + strings.Repeat("0", 1000) + "e-1000", "1." + strings.Repeat("0", 1000) + "e0"}
	for _, text := range ok {
		n, err := parseMaxTokens(json.RawMessage(text))
		wantOne := strings.HasPrefix(text, "1")
		if err != nil {
			t.Errorf("%s must be accepted: %v", shorten(text), err)
		} else if wantOne && n != 1 || !wantOne && n != 5 {
			t.Errorf("%s parsed as %d", shorten(text), n)
		}
	}
}

func TestMalformedHugeExponentsGetA400OverRealHTTP(t *testing.T) {
	doc := loadContract(t)
	srv := httptest.NewServer(NewHandler(DefaultConfig()))
	defer srv.Close()
	const u = `"messages":[{"role":"user","content":"hi"}]`
	nums := []string{
		"1e9223372036854775807", "1e-9223372036854775808", "1.0e-9223372036854775808",
		"12e9223372036854775806", "5e9223372036854775806", "0.5e-9223372036854775807",
		"123456789012345678e-9223372036854775808", "1e9223372036854775808", "1e-9223372036854775809",
	}
	for _, num := range nums {
		for _, form := range []string{"plain", "streamed"} {
			t.Run(num+"/"+form, func(t *testing.T) {
				extra := ""
				if form == "streamed" {
					extra = `,"stream":true`
				}
				body := `{"model":"m",` + u + `,"max_tokens":` + num + extra + `}`
				resp, err := http.Post(srv.URL+ChatPath, "application/json", strings.NewReader(body))
				if err != nil {
					t.Fatalf("the server gave no response (a handler panic closes the connection): %v", err)
				}
				defer func() { _ = resp.Body.Close() }()
				raw, _ := io.ReadAll(resp.Body)
				if resp.StatusCode != http.StatusBadRequest {
					t.Fatalf("want 400, got %d: %s", resp.StatusCode, raw)
				}
				if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
					t.Errorf("an invalid request must get a JSON error, got %q", ct)
				}
				if err := validateSchema(t, doc, "InvalidRequestError", raw); err != nil {
					t.Fatalf("the 400 body does not satisfy InvalidRequestError: %v\n%s", err, raw)
				}
				if !strings.Contains(string(raw), "max_tokens") {
					t.Errorf("the error must name max_tokens: %s", raw)
				}
				// the server is still serving after the bad request
				r, err := http.Get(srv.URL + ReadyPath)
				if err != nil {
					t.Fatalf("the server stopped serving after the request: %v", err)
				}
				_ = r.Body.Close()
				if r.StatusCode != http.StatusOK {
					t.Fatalf("readiness after the request: %d", r.StatusCode)
				}
			})
		}
	}
}

func shorten(s string) string {
	if len(s) > 60 {
		return s[:25] + "..." + s[len(s)-30:]
	}
	return s
}
