// Package mockmodel is a deterministic, local-only stand-in for a chat model
// backend. It implements the OpenAI-compatible subset of the AgentPlane
// gateway contract (docs/gateway/openapi.yaml, POST /v1/chat/completions) so
// the platform can be developed and tested without any real model provider,
// credential or network access.
//
// Everything it returns is a pure function of the request and the
// configuration: identical input yields byte-identical output. Token counts
// are SIMULATED, not measured by any tokenizer. It is not an authenticated or
// production server: it listens on loopback by default and has no
// authentication.
package mockmodel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	// ChatPath is the only model endpoint.
	ChatPath = "/v1/chat/completions"
	// ReadyPath answers 200 once the server is serving.
	ReadyPath = "/readyz"

	// maxSafeInteger is 2^53-1, the integer range of the gateway contract.
	maxSafeInteger int64 = 9007199254740991
	// fixedCreated is the constant "created" timestamp: deterministic output
	// must not depend on the wall clock.
	fixedCreated int64 = 1700000000
)

// Message is one chat message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest is the supported OpenAI-compatible subset. Unknown fields are
// rejected, including any identity-like field (tenant, agent, user).
type ChatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	MaxTokens   *int64    `json:"max_tokens,omitempty"`
	Temperature *float64  `json:"temperature,omitempty"`
	TopP        *float64  `json:"top_p,omitempty"`
	Stop        []string  `json:"stop,omitempty"`
	Stream      bool      `json:"stream,omitempty"`
}

// Usage is simulated token usage.
type Usage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

type chatChoice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// ChatResponse is the non-streaming response (contract: ChatCompletionResponse).
type ChatResponse struct {
	ID      string       `json:"id"`
	Object  string       `json:"object"`
	Created int64        `json:"created"`
	Model   string       `json:"model"`
	Choices []chatChoice `json:"choices"`
	Usage   Usage        `json:"usage"`
}

type errorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

type errorResponse struct {
	Error errorBody `json:"error"`
}

// WaitFunc waits for d or until ctx is done, returning ctx's error in the
// latter case. It is injected so tests never sleep for real.
type WaitFunc func(ctx context.Context, d time.Duration) error

// Option customizes a handler (used by tests).
type Option func(*Server)

// WithWait replaces the function that simulates latency.
func WithWait(w WaitFunc) Option { return func(s *Server) { s.wait = w } }

// Server is the mock's HTTP handler.
type Server struct {
	cfg  Config
	mux  *http.ServeMux
	wait WaitFunc
}

// RealWait is the default WaitFunc: a timer that respects cancellation.
func RealWait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// NewHandler returns the mock's http.Handler.
func NewHandler(cfg Config, opts ...Option) http.Handler {
	s := &Server{cfg: cfg, mux: http.NewServeMux(), wait: RealWait}
	for _, o := range opts {
		o(s)
	}
	s.mux.HandleFunc(ChatPath, s.handleChat)
	s.mux.HandleFunc(ReadyPath, s.handleReady)
	return s
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

func write(w io.Writer, b []byte) { _, _ = w.Write(b) }

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, "GET, HEAD")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	write(w, []byte(`{"status":"ok"}`+"\n"))
}

func methodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	writeError(w, http.StatusMethodNotAllowed, "invalid_request", "method not allowed", fallbackID("method"))
}

// fallbackID is the request id used when no usable body exists.
func fallbackID(label string) string {
	sum := sha256.Sum256([]byte("mockmodel-" + label))
	return "mock-" + hex.EncodeToString(sum[:8])
}

func writeError(w http.ResponseWriter, status int, code, message, id string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Request-Id", id)
	w.WriteHeader(status)
	b, _ := json.Marshal(errorResponse{Error: errorBody{Code: code, Message: message, RequestID: id}})
	write(w, append(b, '\n'))
}

// requestKey is the deterministic identity of a request: SHA-256 over the seed
// and the canonical re-encoding of the parsed request, so whitespace and key
// order in the incoming JSON do not matter. The stream flag is deliberately
// NOT part of the identity: a request and its streamed twin must produce the
// same id, content and usage, only delivered differently.
func (s *Server) requestKey(req *ChatRequest) [32]byte {
	identity := *req
	identity.Stream = false
	canon, _ := json.Marshal(&identity)
	var seed [8]byte
	binary.BigEndian.PutUint64(seed[:], uint64(s.cfg.Seed))
	h := sha256.New()
	h.Write([]byte("agentplane-mockmodel-v1\x00"))
	h.Write(seed[:])
	h.Write(canon)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// failureScore maps a request key to [0, 1). A request fails when its score is
// below the configured error rate. It is a pure function of seed and request:
// no shared random generator, so arrival order and concurrency cannot change
// which requests fail, and a request that fails at one rate fails at every
// higher rate.
func failureScore(key [32]byte) float64 {
	sum := sha256.Sum256(append([]byte("agentplane-mockmodel-failure\x00"), key[:]...))
	return float64(binary.BigEndian.Uint64(sum[:8])>>11) / float64(1<<53)
}

func (s *Server) shouldFail(key [32]byte) bool {
	return s.cfg.ErrorRate > 0 && failureScore(key) < s.cfg.ErrorRate
}

func idFromKey(key [32]byte) string { return "chatcmpl-" + hex.EncodeToString(key[:8]) }

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, "POST")
		return
	}
	body, err := readBounded(r, s.cfg.MaxBodyBytes)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error(), fallbackID("body"))
		return
	}
	bodySum := sha256.Sum256(body)
	fail := func(msg string) {
		writeError(w, http.StatusBadRequest, "invalid_request", msg, "mock-"+hex.EncodeToString(bodySum[:8]))
	}
	req, err := decodeRequest(body)
	if err != nil {
		fail(err.Error())
		return
	}
	key := s.requestKey(req)
	// Simulated processing time. A cancelled request (the client went away)
	// is abandoned without writing anything.
	if s.cfg.Latency > 0 {
		if err := s.wait(r.Context(), s.cfg.Latency); err != nil {
			return
		}
	}
	if s.shouldFail(key) {
		writeError(w, http.StatusInternalServerError, "internal_error",
			"simulated failure (deterministic for this seed and request)", idFromKey(key))
		return
	}
	if req.Stream {
		s.streamResponse(w, r, req, key)
		return
	}
	resp := s.buildResponse(req, key)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Request-Id", resp.ID)
	b, _ := json.Marshal(resp)
	write(w, append(b, '\n'))
}

// readBounded reads at most max bytes; a larger body is an error rather than a
// truncation.
func readBounded(r *http.Request, max int64) ([]byte, error) {
	if r.Body == nil {
		return nil, errors.New("empty request body")
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, max+1))
	if err != nil {
		return nil, fmt.Errorf("cannot read request body: %v", err)
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("request body exceeds the %d byte limit", max)
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return nil, errors.New("empty request body")
	}
	return b, nil
}

// decodeRequest parses and validates a body strictly (unknown fields, trailing
// data and out-of-range values are errors).
func decodeRequest(body []byte) (*ChatRequest, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var req ChatRequest
	if err := dec.Decode(&req); err != nil {
		return nil, fmt.Errorf("invalid JSON request: %v", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("invalid JSON request: unexpected data after the JSON object")
	}
	if err := req.validate(); err != nil {
		return nil, err
	}
	return &req, nil
}

func (r *ChatRequest) validate() error {
	if r.Model == "" || len(r.Model) > 128 {
		return errors.New("model is required and at most 128 characters")
	}
	if len(r.Messages) == 0 {
		return errors.New("messages must contain at least one message")
	}
	for i, m := range r.Messages {
		switch m.Role {
		case "system", "user", "assistant":
		default:
			return fmt.Errorf("messages[%d].role must be system, user or assistant", i)
		}
	}
	if r.MaxTokens != nil && (*r.MaxTokens < 1 || *r.MaxTokens > maxSafeInteger) {
		return fmt.Errorf("max_tokens must be between 1 and %d", maxSafeInteger)
	}
	if r.Temperature != nil && (*r.Temperature < 0 || *r.Temperature > 2) {
		return errors.New("temperature must be between 0 and 2")
	}
	if r.TopP != nil && (*r.TopP < 0 || *r.TopP > 1) {
		return errors.New("top_p must be between 0 and 1")
	}
	if len(r.Stop) > 4 {
		return errors.New("stop accepts at most 4 strings")
	}
	for _, s := range r.Stop {
		if s == "" {
			return errors.New("stop strings must not be empty")
		}
	}
	return nil
}

var words = [...]string{
	"alpha", "bravo", "cedar", "delta", "ember", "fjord", "gamma", "harbor",
	"iris", "jade", "kernel", "lumen", "meadow", "nova", "onyx", "prism",
	"quartz", "river", "sable", "tundra", "umber", "vertex", "willow", "xenon",
	"yarrow", "zephyr", "anchor", "beacon", "canyon", "dune", "echo", "forge",
}

// completionWords derives n words from the request key. Word i depends only on
// (key, i), so any prefix is stable.
func completionWords(key [32]byte, n int64) []string {
	out := make([]string, 0, n)
	for i := int64(0); i < n; i++ {
		var ix [8]byte
		binary.BigEndian.PutUint64(ix[:], uint64(i))
		sum := sha256.Sum256(append(key[:], ix[:]...))
		out = append(out, words[int(sum[0])%len(words)])
	}
	return out
}

func (s *Server) simulatedUsage(req *ChatRequest) (prompt, completion int64, finish string) {
	prompt = s.cfg.PromptTokens
	if prompt <= 0 {
		for _, m := range req.Messages {
			prompt += int64(len(strings.Fields(m.Content)))
		}
		if prompt < 1 {
			prompt = 1
		}
	}
	completion, finish = s.cfg.CompletionTokens, "stop"
	if req.MaxTokens != nil && completion > *req.MaxTokens {
		completion, finish = *req.MaxTokens, "length"
	}
	return prompt, completion, finish
}

func (s *Server) buildResponse(req *ChatRequest, key [32]byte) ChatResponse {
	prompt, completion, finish := s.simulatedUsage(req)
	content := strings.Join(completionWords(key, completion), " ")
	return ChatResponse{
		ID:      idFromKey(key),
		Object:  "chat.completion",
		Created: fixedCreated,
		Model:   req.Model,
		Choices: []chatChoice{{Index: 0, Message: Message{Role: "assistant", Content: content}, FinishReason: finish}},
		Usage:   Usage{PromptTokens: prompt, CompletionTokens: completion, TotalTokens: prompt + completion},
	}
}

// NewHTTPServer wraps the handler with bounded timeouts. Header and body reads
// are limited so a slow client cannot hold a connection open indefinitely.
func NewHTTPServer(cfg Config, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              cfg.Listen,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
}

// Serve serves on ln until ctx is cancelled, then shuts down gracefully
// (in-flight requests get a bounded time to finish).
func Serve(ctx context.Context, srv *http.Server, ln net.Listener) error {
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("graceful shutdown: %w", err)
		}
		if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}
