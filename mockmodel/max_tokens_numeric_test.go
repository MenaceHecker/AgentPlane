package mockmodel

import (
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"runtime"
	"strings"
	"testing"
)

// Review r2, finding 2: the contract's max_tokens is an integer, and JSON
// Schema accepts every spelling of an integral number (1.0, 1e3). The mock must
// parse the exact decimal text, so equivalent spellings give byte-identical
// responses and fractional or out-of-range values are rejected without rounding.
func TestMaxTokensAcceptsEveryIntegralSpelling(t *testing.T) {
	doc := loadContract(t)
	h := NewHandler(DefaultConfig())
	const u = `"messages":[{"role":"user","content":"hi"}]`
	tests := []struct {
		name, num   string
		want        bool
		checkSchema bool // float64 can represent the text exactly, so the schema oracle is meaningful
	}{
		{"plain integer", "5", true, true},
		{"trailing zero fraction", "5.0", true, true},
		{"many zero fraction digits", "5.000000000000000000000", true, true},
		{"exponent zero", "5e0", true, true},
		{"upper-case exponent", "5E0", true, true},
		{"positive exponent sign", "5e+0", true, true},
		{"mantissa shifted by exponent", "0.5e1", true, true},
		{"negative exponent", "50e-1", true, true},
		{"large mantissa negative exponent", "500e-2", true, true},
		{"thousand as exponent", "1e3", true, true},
		{"thousand with fraction", "1000.0", true, true},
		{"maximum safe integer", "9007199254740991", true, true},
		{"maximum safe integer with fraction", "9007199254740991.0", true, true},
		{"maximum safe integer as exponent form", "9007199254740991e0", true, true},
		{"one", "1", true, true},
		{"one as exponent", "1e0", true, true},
		{"one over the maximum", "9007199254740992", false, true},
		{"one over as exponent", "9007199254740992e0", false, true},
		{"one hundred times the maximum", "9.007199254740991e17", false, true},
		{"zero", "0", false, true},
		{"zero with fraction", "0.0", false, true},
		{"zero as exponent", "0e5", false, true},
		{"negative zero", "-0", false, true},
		{"negative", "-1", false, true},
		{"negative integral spelling", "-1.0", false, true},
		{"fraction", "1.5", false, true},
		{"small fraction", "0.1", false, true},
		{"exponent making a fraction", "15e-1", false, true},
		{"tiny fraction", "1e-3", false, true},
		// float64 rounds these onto an integer, so the schema accepts them; the
		// mock must not round, so it rejects. Not compared with the schema.
		{"one plus 1e-19, exact fraction", "5.0000000000000000001", false, false},
		{"maximum safe integer plus a half", "9007199254740991.5", false, false},
		{"maximum safe integer minus a tiny fraction", "9007199254740990.9999999999999999999", false, false},
		// huge exponents must be refused quickly, not expanded.
		{"huge positive exponent", "1e999999999", false, false},
		{"huge negative exponent", "1e-999999999", false, false},
		{"huge exponent on a zero mantissa", "0e999999999", false, false},
		// exponents beyond int64 must still be refused, not wrapped to a small one
		{"exponent beyond int64", "5e99999999999999999999", false, false},
		{"negative exponent beyond int64", "5e-99999999999999999999", false, false},
		{"zero mantissa with an exponent beyond int64", "0e99999999999999999999", false, false},
		{"integral spelling needing an exponent beyond int64", "5000e-99999999999999999999", false, false},
	}
	for _, tt := range tests {
		for _, form := range []string{"plain", "streamed"} {
			t.Run(tt.name+"/"+form, func(t *testing.T) {
				extra := ""
				if form == "streamed" {
					extra = `,"stream":true`
				}
				body := `{"model":"m",` + u + `,"max_tokens":` + tt.num + extra + `}`
				if tt.checkSchema {
					if got := validateSchema(t, doc, "ChatCompletionRequest", []byte(body)) == nil; got != tt.want {
						t.Fatalf("test table error: the contract's verdict is %v, the table says %v", got, tt.want)
					}
				}
				rec := do(t, h, http.MethodPost, ChatPath, body)
				if tt.want && rec.Code != http.StatusOK {
					t.Fatalf("a schema-valid max_tokens %s must be accepted, got %d: %s", tt.num, rec.Code, rec.Body.String())
				}
				if !tt.want && (rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "max_tokens")) {
					t.Fatalf("max_tokens %s must be a 400 naming max_tokens, got %d: %s", tt.num, rec.Code, rec.Body.String())
				}
			})
		}
	}
}

// Equivalent spellings of one value are one request: the response, plain or
// streamed, must be byte-identical (identity is the parsed value, not the text).
func TestEquivalentMaxTokensSpellingsGiveIdenticalResponses(t *testing.T) {
	h := NewHandler(DefaultConfig())
	const u = `"messages":[{"role":"user","content":"hi"}]`
	spellings := []string{"5", "5.0", "5e0", "5E0", "5e+0", "0.5e1", "50e-1", "500e-2", "0.005e3", "5.000000000000000000000"}
	for _, form := range []string{"", `,"stream":true`} {
		want := do(t, h, http.MethodPost, ChatPath, `{"model":"m",`+u+`,"max_tokens":5`+form+`}`)
		if want.Code != http.StatusOK {
			t.Fatalf("baseline failed: %d %s", want.Code, want.Body.String())
		}
		for _, s := range spellings {
			got := do(t, h, http.MethodPost, ChatPath, `{"model":"m",`+u+`,"max_tokens":`+s+form+`}`)
			if got.Code != want.Code || got.Body.String() != want.Body.String() {
				t.Errorf("max_tokens %s%s differs from 5:\nwant %s\n got %s", s, form, want.Body.String(), got.Body.String())
			}
		}
	}
	// and a different value really does change the response (the test can fail)
	other := do(t, h, http.MethodPost, ChatPath, `{"model":"m",`+u+`,"max_tokens":3}`)
	base := do(t, h, http.MethodPost, ChatPath, `{"model":"m",`+u+`,"max_tokens":5}`)
	if other.Body.String() == base.Body.String() {
		t.Fatal("max_tokens 3 and 5 must produce different responses, or this test proves nothing")
	}
}

// parseMaxTokens is cross-checked against math/big, an independent exact
// implementation, over every combination of integer part, fraction and
// exponent below: same accept/reject decision and the same value.
func TestParseMaxTokensMatchesExactRationalArithmetic(t *testing.T) {
	ints := []string{"0", "1", "5", "10", "100", "9007199254740991", "9007199254740992", "12345678901234567890"}
	fracs := []string{"", ".0", ".00", ".5", ".25", ".000000000000000000001", ".9999999999999999999"}
	exps := []string{"", "e0", "E0", "e1", "e+1", "e2", "e-1", "e-2", "e-3", "e15", "e16", "e17", "e-16", "e-17", "e30", "e-30"}
	for _, sign := range []string{"", "-"} {
		for _, i := range ints {
			for _, f := range fracs {
				for _, e := range exps {
					text := sign + i + f + e
					r, ok := new(big.Rat).SetString(text)
					if !ok {
						t.Fatalf("test error: math/big cannot parse %s", text)
					}
					wantOK := r.IsInt() && r.Sign() > 0 && r.Num().Cmp(big.NewInt(maxSafeInteger)) <= 0
					got, err := parseMaxTokens(json.RawMessage(text))
					if (err == nil) != wantOK {
						t.Fatalf("%s: accepted=%v, exact arithmetic says %v", text, err == nil, wantOK)
					}
					if wantOK && fmt.Sprint(got) != r.Num().String() {
						t.Fatalf("%s: got %d, exact value %s", text, got, r.Num())
					}
				}
			}
		}
	}
}

// A huge exponent must be refused without expanding it: the parser is linear in
// the text length. The memory allocated while refusing 1e99999999 (a hundred
// million zeros if it were expanded) must stay tiny.
func TestHugeExponentIsRefusedWithoutExpansion(t *testing.T) {
	for _, text := range []string{"1e99999999", "9e99999999", "1.5e99999999", "100000e-99999999"} {
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		_, err := parseMaxTokens(json.RawMessage(text))
		runtime.ReadMemStats(&after)
		if err == nil {
			t.Fatalf("%s must be refused", text)
		}
		if grew := after.TotalAlloc - before.TotalAlloc; grew > 1<<20 {
			t.Errorf("refusing %s allocated %d bytes: the exponent was expanded", text, grew)
		}
	}
}
