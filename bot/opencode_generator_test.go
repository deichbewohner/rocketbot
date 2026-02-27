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
