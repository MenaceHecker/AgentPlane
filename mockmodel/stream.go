package mockmodel

import (
	"encoding/json"
	"net/http"
)

// chunkDelta is the incremental part of a streamed choice.
type chunkDelta struct {
	Role    string  `json:"role,omitempty"`
	Content *string `json:"content,omitempty"`
}

type chunkChoice struct {
	Index        int        `json:"index"`
	Delta        chunkDelta `json:"delta"`
	FinishReason *string    `json:"finish_reason"`
}

// ChatChunk is one SSE event (contract: ChatCompletionChunk). The terminal
// usage chunk has Choices == [] and Usage set.
type ChatChunk struct {
	ID      string        `json:"id"`
	Object  string        `json:"object"`
	Created int64         `json:"created"`
	Model   string        `json:"model"`
	Choices []chunkChoice `json:"choices"`
	Usage   *Usage        `json:"usage,omitempty"`
}

func strp(s string) *string { return &s }

// streamEvents builds the full event sequence for a request: one chunk per
// simulated completion token (the first carries the assistant role), a final
// chunk with the finish reason, the terminal usage chunk and then [DONE].
// Concatenating the delta contents gives exactly the non-streaming content.
func (s *Server) streamEvents(req *ChatRequest, key [32]byte) []string {
	prompt, completion, finish := s.simulatedUsage(req)
	id := idFromKey(key)
	base := func() ChatChunk {
		return ChatChunk{ID: id, Object: "chat.completion.chunk", Created: fixedCreated, Model: req.Model}
	}
	var events []string
	emit := func(c ChatChunk) {
		b, _ := json.Marshal(c)
		events = append(events, string(b))
	}
	words := completionWords(key, completion)
	if len(words) == 0 {
		c := base()
		c.Choices = []chunkChoice{{Delta: chunkDelta{Role: "assistant", Content: strp("")}}}
		emit(c)
	}
	for i, w := range words {
		text := w
		if i > 0 {
			text = " " + w
		}
		c := base()
		d := chunkDelta{Content: strp(text)}
		if i == 0 {
			d.Role = "assistant"
		}
		c.Choices = []chunkChoice{{Delta: d}}
		emit(c)
	}
	last := base()
	last.Choices = []chunkChoice{{Delta: chunkDelta{}, FinishReason: strp(finish)}}
	emit(last)
	usage := base()
	usage.Choices = []chunkChoice{}
	usage.Usage = &Usage{PromptTokens: prompt, CompletionTokens: completion, TotalTokens: prompt + completion}
	emit(usage)
	return append(events, "[DONE]")
}

// streamResponse writes the events as server-sent events, flushing after each.
// It stops as soon as the client is gone (request context cancelled) or a
// write fails, so a disconnected client cannot keep the handler busy.
func (s *Server) streamResponse(w http.ResponseWriter, r *http.Request, req *ChatRequest, key [32]byte) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal_error", "streaming is not supported by this connection", idFromKey(key))
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Request-Id", idFromKey(key))
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	for _, ev := range s.streamEvents(req, key) {
		if r.Context().Err() != nil {
			return
		}
		if _, err := w.Write([]byte("data: " + ev + "\n\n")); err != nil {
			return
		}
		flusher.Flush()
	}
}
