package bot_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/deichbewohner/rocketbot/bot"
	"github.com/deichbewohner/rocketbot/bot/testutil"
)

func TestWebhookGenerator_GenerateResponse(t *testing.T) {
	tests := []struct {
		name         string
		serverFunc   http.HandlerFunc
		webhookAuth  string
		wantResponse string
		wantErr      bool
	}{
		{
			name: "success_accumulates_chunks",
			serverFunc: func(w http.ResponseWriter, r *http.Request) {
				// Return n8n-style streaming response
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				flusher := w.(http.Flusher)

				io.WriteString(w, `{"type":"begin"}`+"\n")
				flusher.Flush()
				io.WriteString(w, `{"type":"item","content":"Hello"}`+"\n")
				flusher.Flush()
				io.WriteString(w, `{"type":"item","content":" world"}`+"\n")
				flusher.Flush()
				io.WriteString(w, `{"type":"end"}`+"\n")
				flusher.Flush()
			},
			wantResponse: "Hello world",
			wantErr:      false,
		},
		{
			name: "non_200_status",
			serverFunc: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			},
			wantErr: true,
		},
		{
			name: "empty_stream",
			serverFunc: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			},
			wantResponse: "",
			wantErr:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(tt.serverFunc)
			defer server.Close()

			parser := bot.NewN8nParser(testutil.NewTestLogger(t))
			generator := bot.NewWebhookGenerator(
				server.URL,
				tt.webhookAuth,
				parser,
				server.Client(),
				testutil.NewTestLogger(t),
			)

			msg := bot.NewMessage(t).WithText("Test message").Build()
			history := []bot.Message{}

			response, err := generator.GenerateResponse(context.Background(), msg, history)

			if (err != nil) != tt.wantErr {
				t.Fatalf("GenerateResponse() error = %v, wantErr %v", err, tt.wantErr)
			}

			if !tt.wantErr && response != tt.wantResponse {
				t.Errorf("GenerateResponse() = %q, want %q", response, tt.wantResponse)
			}
		})
	}
}

func TestWebhookGenerator_GenerateResponseStream(t *testing.T) {
	tests := []struct {
		name       string
		serverFunc http.HandlerFunc
		wantChunks []string
		wantErr    bool
	}{
		{
			name: "success_streams_chunks",
			serverFunc: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				flusher := w.(http.Flusher)

				io.WriteString(w, `{"type":"begin"}`+"\n")
				flusher.Flush()
				io.WriteString(w, `{"type":"item","content":"First"}`+"\n")
				flusher.Flush()
				io.WriteString(w, `{"type":"item","content":"Second"}`+"\n")
				flusher.Flush()
				io.WriteString(w, `{"type":"item","content":"Third"}`+"\n")
				flusher.Flush()
				io.WriteString(w, `{"type":"end"}`+"\n")
				flusher.Flush()
			},
			wantChunks: []string{"First", "Second", "Third"},
			wantErr:    false,
		},
		{
			name: "non_200_status",
			serverFunc: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(tt.serverFunc)
			defer server.Close()

			parser := bot.NewN8nParser(testutil.NewTestLogger(t))
			generator := bot.NewWebhookGenerator(
				server.URL,
				"",
				parser,
				server.Client(),
				testutil.NewTestLogger(t),
			)

			msg := bot.NewMessage(t).WithText("Test message").Build()
			history := []bot.Message{}

			chunkCh, err := generator.GenerateResponseStream(context.Background(), msg, history)

			if (err != nil) != tt.wantErr {
				t.Fatalf("GenerateResponseStream() error = %v, wantErr %v", err, tt.wantErr)
			}

			if tt.wantErr {
				return
			}

			// Collect chunks
			var chunks []string
			for chunk := range chunkCh {
				chunks = append(chunks, chunk)
			}

			if len(chunks) != len(tt.wantChunks) {
				t.Fatalf("got %d chunks, want %d\nGot: %v\nWant: %v",
					len(chunks), len(tt.wantChunks), chunks, tt.wantChunks)
			}

			for i, want := range tt.wantChunks {
				if chunks[i] != want {
					t.Errorf("chunk[%d] = %q, want %q", i, chunks[i], want)
				}
			}
		})
	}
}

func TestWebhookGenerator_AuthHeaderPropagation(t *testing.T) {
	authHeaderReceived := ""

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeaderReceived = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"type":"item","content":"test"}`+"\n")
	}))
	defer server.Close()

	parser := bot.NewN8nParser(testutil.NewTestLogger(t))
	generator := bot.NewWebhookGenerator(
		server.URL,
		"Bearer secret-token-123",
		parser,
		server.Client(),
		testutil.NewTestLogger(t),
	)

	msg := bot.NewMessage(t).Build()
	_, err := generator.GenerateResponse(context.Background(), msg, []bot.Message{})
	if err != nil {
		t.Fatalf("GenerateResponse() error = %v", err)
	}

	if authHeaderReceived != "Bearer secret-token-123" {
		t.Errorf("Authorization header = %q, want %q", authHeaderReceived, "Bearer secret-token-123")
	}
}

func TestWebhookGenerator_ContextMetadata(t *testing.T) {
	var receivedPayload map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Capture payload
		if err := json.NewDecoder(r.Body).Decode(&receivedPayload); err != nil {
			t.Errorf("failed to decode payload: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"type":"item","content":"test"}`+"\n")
	}))
	defer server.Close()

	parser := bot.NewN8nParser(testutil.NewTestLogger(t))
	generator := bot.NewWebhookGenerator(
		server.URL,
		"",
		parser,
		server.Client(),
		testutil.NewTestLogger(t),
	)

	// Create context with reply metadata
	ctx := context.Background()
	ctx = context.WithValue(ctx, bot.ReplyMessageIDKey, "reply-msg-123")
	ctx = context.WithValue(ctx, bot.ReplyRoomIDKey, "reply-room-456")

	msg := bot.NewMessage(t).Build()
	_, err := generator.GenerateResponse(ctx, msg, []bot.Message{})
	if err != nil {
		t.Fatalf("GenerateResponse() error = %v", err)
	}

	// Verify metadata was included in payload
	if replyMsgID, ok := receivedPayload["replyMessageId"].(string); !ok || replyMsgID != "reply-msg-123" {
		t.Errorf("replyMessageId = %v, want %q", receivedPayload["replyMessageId"], "reply-msg-123")
	}
	if replyRoomID, ok := receivedPayload["replyRoomId"].(string); !ok || replyRoomID != "reply-room-456" {
		t.Errorf("replyRoomId = %v, want %q", receivedPayload["replyRoomId"], "reply-room-456")
	}
}

func TestWebhookGenerator_HistoryPayload(t *testing.T) {
	var receivedPayload map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Capture payload
		if err := json.NewDecoder(r.Body).Decode(&receivedPayload); err != nil {
			t.Errorf("failed to decode payload: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"type":"item","content":"response"}`+"\n")
	}))
	defer server.Close()

	parser := bot.NewN8nParser(testutil.NewTestLogger(t))
	generator := bot.NewWebhookGenerator(
		server.URL,
		"",
		parser,
		server.Client(),
		testutil.NewTestLogger(t),
	)

	msg := bot.NewMessage(t).WithText("current message").WithID("msg-3").Build()
	history := []bot.Message{
		bot.NewMessage(t).WithID("msg-1").WithText("first message").WithRoomID("room-123").Build(),
		bot.NewMessage(t).WithID("msg-2").WithText("second message").WithRoomID("room-123").Build(),
	}

	_, err := generator.GenerateResponse(context.Background(), msg, history)
	if err != nil {
		t.Fatalf("GenerateResponse() error = %v", err)
	}

	// Verify history array in payload
	historyArray, ok := receivedPayload["history"].([]interface{})
	if !ok {
		t.Fatalf("history field missing or wrong type: %T", receivedPayload["history"])
	}
	if len(historyArray) != 2 {
		t.Fatalf("history length = %d, want 2", len(historyArray))
	}

	// Check first history message
	msg1, ok := historyArray[0].(map[string]interface{})
	if !ok {
		t.Fatalf("history[0] not a map: %T", historyArray[0])
	}
	if msg1["id"] != "msg-1" {
		t.Errorf("history[0].id = %v, want msg-1", msg1["id"])
	}
	if msg1["text"] != "first message" {
		t.Errorf("history[0].text = %v, want 'first message'", msg1["text"])
	}

	// Check second history message
	msg2, ok := historyArray[1].(map[string]interface{})
	if !ok {
		t.Fatalf("history[1] not a map: %T", historyArray[1])
	}
	if msg2["id"] != "msg-2" {
		t.Errorf("history[1].id = %v, want msg-2", msg2["id"])
	}
	if msg2["text"] != "second message" {
		t.Errorf("history[1].text = %v, want 'second message'", msg2["text"])
	}
}

func TestWebhookGenerator_ContextCancellation(t *testing.T) {
	// Server that streams slowly
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)

		for i := 0; i < 10; i++ {
			io.WriteString(w, `{"type":"item","content":"chunk"}`+"\n")
			flusher.Flush()
			time.Sleep(100 * time.Millisecond)
		}
	}))
	defer server.Close()

	parser := bot.NewN8nParser(testutil.NewTestLogger(t))
	generator := bot.NewWebhookGenerator(
		server.URL,
		"",
		parser,
		server.Client(),
		testutil.NewTestLogger(t),
	)

	ctx, cancel := context.WithCancel(context.Background())

	msg := bot.NewMessage(t).Build()
	chunkCh, err := generator.GenerateResponseStream(ctx, msg, []bot.Message{})
	if err != nil {
		t.Fatalf("GenerateResponseStream() error = %v", err)
	}

	// Read a few chunks
	<-chunkCh
	<-chunkCh

	// Cancel context
	cancel()

	// Channel should close soon
	timeout := time.After(500 * time.Millisecond)
	for {
		select {
		case _, ok := <-chunkCh:
			if !ok {
				// Channel closed - good!
				return
			}
		case <-timeout:
			t.Fatal("stream did not stop after context cancellation")
		}
	}
}
