package mockmodel

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"
)

// OpenAPI maxLength counts characters (Unicode code points), not UTF-8 bytes.
// The mock used len(model), so a schema-valid 128-character name made of
// two-byte characters was refused (review r1, finding 3). These tests pin the
// boundary in code points for multibyte, four-byte, combining and escaped
// names, and compare every verdict with the unchanged contract schema.
func TestModelNameLengthIsCountedInCodePoints(t *testing.T) {
	doc := loadContract(t)
	h := NewHandler(DefaultConfig())
	const messages = `"messages":[{"role":"user","content":"hi"}]`
	jsonString := func(s string) string {
		b, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	tests := []struct {
		name       string
		modelJSON  string // the JSON text of the model value
		codePoints int
		wantOK     bool
	}{
		{"128 ASCII characters", jsonString(strings.Repeat("a", 128)), 128, true},
		{"129 ASCII characters", jsonString(strings.Repeat("a", 129)), 129, false},
		{"128 two-byte characters (256 bytes)", jsonString(strings.Repeat("é", 128)), 128, true},
		{"129 two-byte characters", jsonString(strings.Repeat("é", 129)), 129, false},
		{"128 three-byte characters (384 bytes)", jsonString(strings.Repeat("日", 128)), 128, true},
		{"129 three-byte characters", jsonString(strings.Repeat("日", 129)), 129, false},
		{"128 four-byte characters (512 bytes)", jsonString(strings.Repeat("😀", 128)), 128, true},
		{"129 four-byte characters", jsonString(strings.Repeat("😀", 129)), 129, false},
		{"64 base plus combining pairs (128 code points)", jsonString(strings.Repeat("é", 64)), 128, true},
		{"65 base plus combining pairs (130 code points)", jsonString(strings.Repeat("é", 65)), 130, false},
		{"128 escaped surrogate pairs", `"` + strings.Repeat(`😀`, 128) + `"`, 128, true},
		{"129 escaped surrogate pairs", `"` + strings.Repeat(`😀`, 129) + `"`, 129, false},
		{"mixed widths totalling exactly 128", jsonString(strings.Repeat("a", 32) + strings.Repeat("é", 32) + strings.Repeat("日", 32) + strings.Repeat("😀", 32)), 128, true},
		{"mixed widths totalling 129", jsonString(strings.Repeat("a", 33) + strings.Repeat("é", 32) + strings.Repeat("日", 32) + strings.Repeat("😀", 32)), 129, false},
		{"one multibyte character", jsonString("é"), 1, true},
		{"empty name", `""`, 0, false},
	}
	for _, tt := range tests {
		for _, form := range []string{"plain", "streamed"} {
			t.Run(tt.name+"/"+form, func(t *testing.T) {
				stream := ""
				if form == "streamed" {
					stream = `,"stream":true`
				}
				body := `{"model":` + tt.modelJSON + `,` + messages + stream + `}`
				var decoded struct{ Model string }
				if err := json.Unmarshal([]byte(body), &decoded); err != nil {
					t.Fatal(err)
				}
				if got := utf8.RuneCountInString(decoded.Model); got != tt.codePoints {
					t.Fatalf("test table error: the name has %d code points, not %d", got, tt.codePoints)
				}
				rec := do(t, h, http.MethodPost, ChatPath, body)
				contract := validateSchema(t, doc, "ChatCompletionRequest", []byte(body))
				if (contract == nil) != tt.wantOK {
					t.Fatalf("test table error: the contract's verdict (valid=%v) differs from the expectation (%v)", contract == nil, tt.wantOK)
				}
				if tt.wantOK {
					if rec.Code != http.StatusOK {
						t.Fatalf("a %d-code-point name is valid but the mock answered %d: %s", tt.codePoints, rec.Code, rec.Body.String())
					}
					if form == "plain" {
						if r := decodeResp(t, rec); r.Model != decoded.Model {
							t.Errorf("the response must echo the model unchanged")
						}
					} else if err := checkStreamBody(t, doc, rec.Body.String()); err != nil {
						t.Fatalf("stream does not satisfy the contract: %v", err)
					}
					return
				}
				if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "model") {
					t.Fatalf("a %d-code-point name must be a 400 naming the model, got %d: %s", tt.codePoints, rec.Code, rec.Body.String())
				}
			})
		}
	}
}
