package bot

import (
	"context"
	"io"
	"log/slog"
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
