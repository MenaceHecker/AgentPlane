package mockmodel

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Review r2, finding 1: an error naming one of several unknown members must not
// depend on Go's randomized map iteration. The same body must always give the
// same complete error body, sequentially and concurrently, plain and streamed.
func TestUnknownMembersGiveOneStableErrorBody(t *testing.T) {
	h := NewHandler(DefaultConfig())
	msgs := `[{"role":"user","content":"","z":0,"y":0,"x":0,"a":0,"m":0}]`
	bodies := map[string]string{
		"unknown message members":         `{"model":"m","messages":` + msgs + `}`,
		"unknown message members, stream": `{"model":"m","messages":` + msgs + `,"stream":true}`,
		"unknown top-level members":       `{"model":"m","messages":[{"role":"user","content":"hi"}],"z":0,"y":0,"x":0,"a":0}`,
		"unknown members at both levels":  `{"model":"m","messages":` + msgs + `,"z":0,"a":0,"stream":true}`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			first := do(t, h, http.MethodPost, ChatPath, body)
			if first.Code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d: %s", first.Code, first.Body.String())
			}
			want := first.Body.String()
			if !strings.Contains(want, `\"a\"`) {
				t.Errorf("the lexicographically first unknown member must be named, got %s", want)
			}
			var wg sync.WaitGroup
			errs := make(chan string, 16*40)
			for g := 0; g < 16; g++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for i := 0; i < 40; i++ {
						rec := httptest.NewRecorder()
						req := httptest.NewRequest(http.MethodPost, ChatPath, strings.NewReader(body))
						req.Header.Set("Content-Type", "application/json")
						h.ServeHTTP(rec, req)
						if rec.Code != http.StatusBadRequest || rec.Body.String() != want {
							errs <- rec.Body.String()
						}
					}
				}()
			}
			wg.Wait()
			close(errs)
			for e := range errs {
				t.Fatalf("error body differed between identical requests:\nwant %s\n got %s", want, e)
			}
		})
	}
}
