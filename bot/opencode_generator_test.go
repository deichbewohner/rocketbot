package bot

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOpenCodeGenerator_ConsumeEvents_EmitsAssistantTextOnly(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{}))
	g := NewOpenCodeGenerator("http://127.0.0.1:4096", "", "deny", nil, logger)

	// Assistant part arrives before we learn the message role; it should be buffered
	// and flushed once message.updated marks it as assistant.
	sse := strings.Join([]string{
		"data: {\"type\":\"server.connected\",\"properties\":{}}\n",
		"data: {\"type\":\"message.updated\",\"properties\":{\"info\":{\"id\":\"msg_user\",\"sessionID\":\"ses1\",\"role\":\"user\"}}}\n",
		"data: {\"type\":\"message.part.updated\",\"properties\":{\"part\":{\"id\":\"part_u\",\"sessionID\":\"ses1\",\"messageID\":\"msg_user\",\"type\":\"text\",\"text\":\"hi\"}}}\n",
		"data: {\"type\":\"message.part.updated\",\"properties\":{\"part\":{\"id\":\"part_a\",\"sessionID\":\"ses1\",\"messageID\":\"msg_assistant\",\"type\":\"text\",\"text\":\"Hello\"}}}\n",
		"data: {\"type\":\"message.updated\",\"properties\":{\"info\":{\"id\":\"msg_assistant\",\"sessionID\":\"ses1\",\"role\":\"assistant\"}}}\n",
		"data: {\"type\":\"message.part.updated\",\"properties\":{\"part\":{\"id\":\"part_a\",\"sessionID\":\"ses1\",\"messageID\":\"msg_assistant\",\"type\":\"text\",\"text\":\"Hello world\"}}}\n",
		"data: {\"type\":\"session.idle\",\"properties\":{\"sessionID\":\"ses1\"}}\n",
	}, "")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	outCh := make(chan string, 10)
	state := &openCodeStreamState{
		assistantMsgIDs: make(map[string]struct{}),
		partText:        make(map[string]string),
		pendingParts:    make(map[string]openCodePendingPart),
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- g.consumeEvents(ctx, strings.NewReader(sse), "ses1", state, outCh)
		close(outCh)
	}()

	var chunks []string
	for c := range outCh {
		chunks = append(chunks, c)
	}

	if err := <-errCh; err != nil {
		t.Fatalf("consumeEvents() error = %v", err)
	}

	want := []string{"Hello", " world"}
	if len(chunks) != len(want) {
		t.Fatalf("got %d chunks, want %d: %v", len(chunks), len(want), chunks)
	}
	for i := range want {
		if chunks[i] != want[i] {
			t.Fatalf("chunk[%d] = %q, want %q", i, chunks[i], want[i])
		}
	}
}

func TestBuildPrompt_IncludeHistoryToggle(t *testing.T) {
	message := Message{
		Text: "What now?",
		User: MessageUser{Username: "alice"},
	}
	history := []Message{
		{Text: "Hello", User: MessageUser{Username: "bob"}},
	}

	withHistory := buildPrompt(message, history, true)
	if !strings.Contains(withHistory, "Conversation history (oldest first):") {
		t.Fatalf("expected history header in prompt: %q", withHistory)
	}
	if !strings.Contains(withHistory, "- bob: Hello") {
		t.Fatalf("expected history content in prompt: %q", withHistory)
	}

	withoutHistory := buildPrompt(message, history, false)
	if strings.Contains(withoutHistory, "Conversation history (oldest first):") {
		t.Fatalf("did not expect history header in prompt: %q", withoutHistory)
	}
	if withoutHistory != "alice: What now?" {
		t.Fatalf("prompt = %q, want %q", withoutHistory, "alice: What now?")
	}
}

func TestOpenCodeGenerator_HistoryLimit_TracksSessionBootstrap(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{}))
	g := NewOpenCodeGenerator("http://127.0.0.1:4096", "", "deny", nil, logger)

	msg := Message{RoomID: "room1", ThreadID: "thread1"}
	if got := g.HistoryLimit(msg); got != 10 {
		t.Fatalf("HistoryLimit() = %d, want 10 before session", got)
	}

	key := openCodeConversationKey(msg)
	conv := g.conversation(key)
	conv.setSessionID("session-123")

	if got := g.HistoryLimit(msg); got != 0 {
		t.Fatalf("HistoryLimit() = %d, want 0 after session", got)
	}
}

func TestOpenCodeGenerator_HistoryLimit_DoesNotBlockOnPromptLock(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{}))
	g := NewOpenCodeGenerator("http://127.0.0.1:4096", "", "deny", nil, logger)

	msg := Message{RoomID: "room1", ThreadID: "thread1"}
	conv := g.conversation(openCodeConversationKey(msg))
	conv.setSessionID("session-123")

	conv.promptMu.Lock()
	defer conv.promptMu.Unlock()

	done := make(chan int, 1)
	go func() {
		done <- g.HistoryLimit(msg)
	}()

	select {
	case got := <-done:
		if got != 0 {
			t.Fatalf("HistoryLimit() = %d, want 0", got)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("HistoryLimit() blocked while prompt lock was held")
	}
}

func TestOpenCodeGenerator_GenerateResponseStream_RetriesOnStaleSession(t *testing.T) {
	var createCalls int
	var stalePromptCalls int
	var newPromptCalls int
	var retryPromptBody string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/event":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"type\":\"server.connected\",\"properties\":{}}\n"))
			return

		case r.Method == http.MethodPost && r.URL.Path == "/session":
			createCalls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"new-session"}`))
			return

		case r.Method == http.MethodPost && r.URL.Path == "/session/stale-session/prompt_async":
			stalePromptCalls++
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("session not found"))
			return

		case r.Method == http.MethodPost && r.URL.Path == "/session/new-session/prompt_async":
			newPromptCalls++
			body, _ := io.ReadAll(r.Body)
			retryPromptBody = string(body)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
			return
		}

		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{}))
	g := NewOpenCodeGenerator(srv.URL, "", "deny", srv.Client(), logger)

	message := Message{RoomID: "room1", Text: "hello", User: MessageUser{Username: "alice"}}
	history := []Message{{Text: "prev", User: MessageUser{Username: "bob"}}}

	conv := g.conversation(openCodeConversationKey(message))
	conv.setSessionID("stale-session")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	ch, err := g.GenerateResponseStream(ctx, message, history)
	if err != nil {
		t.Fatalf("GenerateResponseStream() error = %v", err)
	}
	for range ch {
	}

	if createCalls != 1 {
		t.Fatalf("create session calls = %d, want 1", createCalls)
	}
	if stalePromptCalls != 1 {
		t.Fatalf("stale prompt calls = %d, want 1", stalePromptCalls)
	}
	if newPromptCalls != 1 {
		t.Fatalf("new prompt calls = %d, want 1", newPromptCalls)
	}
	if !strings.Contains(retryPromptBody, "Conversation history (oldest first):") {
		t.Fatalf("retry prompt missing history bootstrap: %q", retryPromptBody)
	}
	if got := conv.getSessionID(); got != "new-session" {
		t.Fatalf("cached session id = %q, want %q", got, "new-session")
	}
}

func TestOpenCodeGenerator_GenerateResponseStream_DoesNotRetryOnNonStalePromptError(t *testing.T) {
	var createCalls int
	var promptCalls int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/event":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"type\":\"server.connected\",\"properties\":{}}\n"))
			return

		case r.Method == http.MethodPost && r.URL.Path == "/session":
			createCalls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"new-session"}`))
			return

		case r.Method == http.MethodPost && r.URL.Path == "/session/live-session/prompt_async":
			promptCalls++
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("upstream failure"))
			return
		}

		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{}))
	g := NewOpenCodeGenerator(srv.URL, "", "deny", srv.Client(), logger)

	message := Message{RoomID: "room1", Text: "hello", User: MessageUser{Username: "alice"}}
	conv := g.conversation(openCodeConversationKey(message))
	conv.setSessionID("live-session")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err := g.GenerateResponseStream(ctx, message, nil)
	if err == nil {
		t.Fatal("GenerateResponseStream() error = nil, want error")
	}

	if createCalls != 0 {
		t.Fatalf("create session calls = %d, want 0", createCalls)
	}
	if promptCalls != 1 {
		t.Fatalf("prompt calls = %d, want 1", promptCalls)
	}
	if got := conv.getSessionID(); got != "live-session" {
		t.Fatalf("cached session id = %q, want %q", got, "live-session")
	}
}

func TestIsStaleSessionPromptError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "404 prompt async",
			err: &openCodeHTTPError{
				op:     "prompt_async",
				status: http.StatusNotFound,
				body:   "session not found",
			},
			want: true,
		},
		{
			name: "400 prompt async with invalid session text",
			err: &openCodeHTTPError{
				op:     "prompt_async",
				status: http.StatusBadRequest,
				body:   "invalid session id",
			},
			want: true,
		},
		{
			name: "400 prompt async unrelated validation",
			err: &openCodeHTTPError{
				op:     "prompt_async",
				status: http.StatusBadRequest,
				body:   "missing parts",
			},
			want: false,
		},
		{
			name: "500 prompt async",
			err: &openCodeHTTPError{
				op:     "prompt_async",
				status: http.StatusInternalServerError,
				body:   "boom",
			},
			want: false,
		},
		{
			name: "not prompt async",
			err: &openCodeHTTPError{
				op:     "create session",
				status: http.StatusNotFound,
				body:   "session not found",
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isStaleSessionPromptError(tt.err); got != tt.want {
				t.Fatalf("isStaleSessionPromptError() = %v, want %v", got, tt.want)
			}
		})
	}
}
