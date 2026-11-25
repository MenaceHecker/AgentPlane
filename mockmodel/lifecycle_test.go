package mockmodel

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The golden body freezes the exact bytes the default configuration returns
// for minimalReq. Any change to the id derivation, content words, created
// timestamp, field order or usage simulation shows up here as a visible diff,
// which is the point: other components may depend on the mock's stability.
func TestGoldenResponseIsStable(t *testing.T) {
	const want = "{\"id\":\"chatcmpl-e7e90785d6c07af3\",\"object\":\"chat.completion\",\"created\":1700000000,\"model\":\"mock-large\"," +
		"\"choices\":[{\"index\":0,\"message\":{\"role\":\"assistant\",\"content\":\"meadow xenon iris lumen anchor river prism yarrow xenon yarrow onyx sable sable river prism alpha\"},\"finish_reason\":\"stop\"}]," +
		"\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":16,\"total_tokens\":20}}\n"
	if got := do(t, NewHandler(DefaultConfig()), http.MethodPost, ChatPath, minimalReq).Body.String(); got != want {
		t.Fatalf("the default response changed:\n got: %q\nwant: %q", got, want)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

func TestRequestBodyReadErrors(t *testing.T) {
	h := NewHandler(DefaultConfig())
	t.Run("a body that fails while being read", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, ChatPath, errReader{}))
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "cannot read request body") {
			t.Fatalf("got %d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("a request with no body at all", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, ChatPath, nil)
		req.Body = nil
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "empty request body") {
			t.Fatalf("got %d %s", rec.Code, rec.Body.String())
		}
	})
}

func TestOtherMethodsAreRefused(t *testing.T) {
	h := NewHandler(DefaultConfig())
	for _, m := range []string{http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodHead, http.MethodOptions} {
		rec := do(t, h, m, ChatPath, "")
		if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "POST" {
			t.Errorf("%s %s = %d allow %q", m, ChatPath, rec.Code, rec.Header().Get("Allow"))
		}
	}
	if rec := do(t, h, http.MethodHead, ReadyPath, ""); rec.Code != http.StatusOK {
		t.Errorf("HEAD readyz = %d", rec.Code)
	}
}

func TestServeReturnsListenerFailures(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := ln.Close(); err != nil { // a closed listener cannot accept
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	got := Serve(context.Background(), NewHTTPServer(cfg, NewHandler(cfg)), ln)
	if got == nil {
		t.Fatal("Serve must report a listener failure, not return nil")
	}
}

// Graceful shutdown: a request that is in flight when shutdown starts is
// allowed to finish, and only then does Serve return.
func TestGracefulShutdownLetsAnInFlightRequestFinish(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	cfg := DefaultConfig()
	cfg.Latency = time.Second // any positive latency routes through the injected wait
	wait := func(ctx context.Context, _ time.Duration) error {
		close(started)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- Serve(ctx, NewHTTPServer(cfg, NewHandler(cfg, WithWait(wait))), ln) }()

	type result struct {
		code int
		body string
		err  error
	}
	got := make(chan result, 1)
	go func() {
		resp, err := http.Post("http://"+ln.Addr().String()+ChatPath, "application/json", strings.NewReader(minimalReq))
		if err != nil {
			got <- result{err: err}
			return
		}
		b, rerr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		got <- result{code: resp.StatusCode, body: string(b), err: rerr}
	}()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the request never reached the handler")
	}
	cancel() // begin graceful shutdown while the request is in flight
	select {
	case err := <-served:
		t.Fatalf("Serve returned (%v) while a request was still in flight", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	select {
	case r := <-got:
		if r.err != nil || r.code != http.StatusOK || !strings.Contains(r.body, "chat.completion") {
			t.Fatalf("the in-flight request must complete normally: %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the in-flight request never completed")
	}
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("Serve returned %v after a clean shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after the in-flight request finished")
	}
}
