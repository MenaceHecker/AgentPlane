package mockmodel

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// withStream returns the body with "stream": true added when the body has no
// stream member, so each case is also exercised as a streamed request. A body
// that already sets stream (including null) is returned unchanged.
func withStream(t *testing.T, body string) string {
	t.Helper()
	var members map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &members); err != nil || members == nil {
		return body // not a JSON object: nothing to add
	}
	if _, ok := members["stream"]; ok {
		return body
	}
	members["stream"] = json.RawMessage("true")
	out, err := json.Marshal(members)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// TestMockAgreesWithTheContractSchema is a differential test: for every body in
// the corpus the mock must accept exactly what the UNCHANGED OpenAPI
// ChatCompletionRequest schema accepts, in both plain and streamed form. A
// valid body must yield a response that satisfies the response (or chunk)
// schema; an invalid body must yield a JSON 400 that satisfies
// InvalidRequestError, never a partial stream. It exists because Go struct
// decoding cannot tell a missing field from a zero value or a null from an
// omitted field (review r1, finding 1).
func TestMockAgreesWithTheContractSchema(t *testing.T) {
	doc := loadContract(t)
	h := NewHandler(DefaultConfig())
	u := `"messages":[{"role":"user","content":"hi"}]`
	tests := []struct {
		name string
		body string
	}{
		// valid, including values that look suspicious but are allowed
		{"minimal", `{"model":"m",` + u + `}`},
		{"empty content is valid", `{"model":"m","messages":[{"role":"user","content":""}]}`},
		{"every optional field present and non-null", `{"model":"m",` + u + `,"max_tokens":1,"temperature":0,"top_p":0,"stop":[],"stream":false}`},
		{"optional fields at their upper bounds", `{"model":"m",` + u + `,"temperature":2,"top_p":1,"stop":["a","b","c","d"]}`},
		{"system, user and assistant roles", `{"model":"m","messages":[{"role":"system","content":"s"},{"role":"user","content":"u"},{"role":"assistant","content":"a"}]}`},
		{"unicode content", `{"model":"m","messages":[{"role":"user","content":"héllo é 😀"}]}`},
		{"stream false is valid", `{"model":"m",` + u + `,"stream":false}`},
		{"stream true is valid", `{"model":"m",` + u + `,"stream":true}`},
		// invalid: required members missing
		{"missing model", `{` + u + `}`},
		{"missing messages", `{"model":"m"}`},
		{"missing content", `{"model":"m","messages":[{"role":"user"}]}`},
		{"missing role", `{"model":"m","messages":[{"content":"hi"}]}`},
		{"empty object", `{}`},
		// invalid: nulls (no request field is nullable in the contract)
		{"null model", `{"model":null,` + u + `}`},
		{"null messages", `{"model":"m","messages":null}`},
		{"null message element", `{"model":"m","messages":[null]}`},
		{"null role", `{"model":"m","messages":[{"role":null,"content":"hi"}]}`},
		{"null content", `{"model":"m","messages":[{"role":"user","content":null}]}`},
		{"null temperature", `{"model":"m",` + u + `,"temperature":null}`},
		{"null top_p", `{"model":"m",` + u + `,"top_p":null}`},
		{"null max_tokens", `{"model":"m",` + u + `,"max_tokens":null}`},
		{"null stream", `{"model":"m",` + u + `,"stream":null}`},
		{"null stop", `{"model":"m",` + u + `,"stop":null}`},
		{"null inside stop", `{"model":"m",` + u + `,"stop":[null]}`},
		{"the request itself is null", `null`},
		// invalid: wrong types and shapes
		{"messages element is a string", `{"model":"m","messages":["hi"]}`},
		{"messages element is an array", `{"model":"m","messages":[[]]}`},
		{"messages is an object", `{"model":"m","messages":{}}`},
		{"model is a number", `{"model":5,` + u + `}`},
		{"temperature is a string", `{"model":"m",` + u + `,"temperature":"hot"}`},
		{"stop is a string", `{"model":"m",` + u + `,"stop":"END"}`},
		{"stop element is a number", `{"model":"m",` + u + `,"stop":[1]}`},
		{"stream is a string", `{"model":"m",` + u + `,"stream":"yes"}`},
		{"message has an extra field", `{"model":"m","messages":[{"role":"user","content":"hi","name":"n"}]}`},
		{"unsupported top-level field", `{"model":"m",` + u + `,"user":"u"}`},
		{"identity field", `{"model":"m",` + u + `,"tenant":"acme"}`},
		// invalid: ranges
		{"max_tokens zero", `{"model":"m",` + u + `,"max_tokens":0}`},
		{"temperature above 2", `{"model":"m",` + u + `,"temperature":2.5}`},
		{"top_p below 0", `{"model":"m",` + u + `,"top_p":-0.5}`},
		{"five stop strings", `{"model":"m",` + u + `,"stop":["a","b","c","d","e"]}`},
		{"empty stop string", `{"model":"m",` + u + `,"stop":[""]}`},
		{"unknown role", `{"model":"m","messages":[{"role":"tool","content":"hi"}]}`},
		{"empty model", `{"model":"",` + u + `}`},
	}
	for _, tt := range tests {
		for _, form := range []string{"plain", "streamed"} {
			body := tt.body
			if form == "streamed" {
				body = withStream(t, body)
			}
			t.Run(tt.name+"/"+form, func(t *testing.T) {
				var v any
				if err := json.Unmarshal([]byte(body), &v); err != nil {
					t.Fatalf("corpus body is not JSON: %v", err)
				}
				contract := validateSchema(t, doc, "ChatCompletionRequest", []byte(body))
				rec := do(t, h, http.MethodPost, ChatPath, body)
				if contract == nil {
					if rec.Code != http.StatusOK {
						t.Fatalf("the contract accepts this body but the mock answered %d: %s", rec.Code, rec.Body.String())
					}
					if strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") {
						if err := checkStreamBody(t, doc, rec.Body.String()); err != nil {
							t.Fatalf("stream does not satisfy the contract: %v", err)
						}
					} else if err := validateSchema(t, doc, "ChatCompletionResponse", rec.Body.Bytes()); err != nil {
						t.Fatalf("response does not satisfy the contract: %v", err)
					}
					return
				}
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("the contract rejects this body (%v) but the mock answered %d: %s", contract, rec.Code, rec.Body.String())
				}
				if !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") || strings.Contains(rec.Body.String(), "data:") {
					t.Fatalf("a rejected request must be a plain JSON 400, got %q %s", rec.Header().Get("Content-Type"), rec.Body.String())
				}
				if err := validateSchema(t, doc, "InvalidRequestError", rec.Body.Bytes()); err != nil {
					t.Fatalf("the 400 body does not satisfy InvalidRequestError: %v", err)
				}
			})
		}
	}
}

// Valid empty content and omitted optional fields must still produce the same
// deterministic response as before the stricter decoding.
func TestStrictDecodingKeepsValidInputsUnchanged(t *testing.T) {
	h := NewHandler(DefaultConfig())
	a := decodeResp(t, do(t, h, http.MethodPost, ChatPath, `{"model":"m","messages":[{"role":"user","content":""}]}`))
	if a.Usage.PromptTokens != 1 || a.Choices[0].Message.Role != "assistant" {
		t.Errorf("an empty content must still be served: %+v", a)
	}
	plain := do(t, h, http.MethodPost, ChatPath, `{"model":"m","messages":[{"role":"user","content":"a b"}]}`).Body.String()
	explicit := do(t, h, http.MethodPost, ChatPath, `{"model":"m","messages":[{"role":"user","content":"a b"}],"stop":[],"stream":false}`).Body.String()
	if plain != explicit {
		t.Errorf("an explicit empty stop list and stream:false must not change the response:\n%s\n%s", plain, explicit)
	}
}
