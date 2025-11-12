package bot_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deichbewohner/rocketbot/bot"
	"github.com/deichbewohner/rocketbot/bot/testutil"
	"github.com/gorilla/websocket"
)

// TestIntegration_BotEndToEnd verifies the complete bot flow without external dependencies.
// This test spins up mock Rocket.Chat (WebSocket + REST) and n8n servers, then runs
// the actual bot code to verify the full message handling pipeline.
func TestIntegration_BotEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Track test state
	var (
		n8nCalled    bool
		n8nPayload   map[string]interface{}
		responseSent bool
		responseText string
		mu           sync.Mutex
	)

	// Mock n8n webhook server
	n8nServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		n8nCalled = true
		if err := json.NewDecoder(r.Body).Decode(&n8nPayload); err != nil {
			t.Errorf("failed to decode n8n payload: %v", err)
		}

		// Return simple response
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"type":"item","content":"Hello from n8n!"}`+"\n")
		_, _ = io.WriteString(w, `{"type":"end"}`+"\n")
	}))
	defer n8nServer.Close()

	// Mock unified Rocket.Chat server (REST + WebSocket on same server, like real Rocket.Chat)
	rocketServer := newMockRocketChatServer(t, &mu, &responseSent, &responseText)
	defer rocketServer.Close()

	// Create bot with injected dependencies
	logger := testutil.NewTestLogger(t)
	parser := bot.NewN8nParser(logger)
	generator := bot.NewWebhookGenerator(
		n8nServer.URL,
		"",
		parser,
		http.DefaultClient,
		logger,
	)

	apiClient := bot.NewAPIClient(rocketServer.URL, "test-user", "test-token", nil, logger)

	client := bot.NewClientWithAPI(
		apiClient,
		"TestBot",
		generator,
		false, // non-streaming for this test
		false,
		logger,
		"online",
	)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- client.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		client.Stop()
		if err := <-errCh; err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("bot.Run() returned unexpected error: %v", err)
		}
	})

	// Wait for bot to connect and subscribe
	time.Sleep(100 * time.Millisecond)

	// Send a DM message to the bot
	rocketServer.SendMessage(map[string]interface{}{
		"msg":        "changed",
		"collection": "stream-room-messages",
		"fields": map[string]interface{}{
			"args": []interface{}{
				map[string]interface{}{
					"_id": "user-msg-123",
					"rid": "dm-room-123",
					"msg": "Hello bot!",
					"u": map[string]interface{}{
						"_id":      "user-456",
						"username": "testuser",
						"name":     "Test User",
					},
					"ts": map[string]interface{}{
						"$date": float64(time.Now().UnixMilli()),
					},
				},
			},
		},
	})

	// Wait for bot to process message and respond
	time.Sleep(200 * time.Millisecond)

	// Verify n8n was called
	mu.Lock()
	defer mu.Unlock()

	if !n8nCalled {
		t.Fatal("expected n8n webhook to be called")
	}

	if n8nPayload == nil {
		t.Fatal("expected n8n payload to be captured")
	}

	// Verify message was sent to n8n
	msgText, ok := n8nPayload["message"].(map[string]interface{})
	if !ok {
		t.Fatalf("n8n payload missing message field: %+v", n8nPayload)
	}
	if text, _ := msgText["text"].(string); text != "Hello bot!" {
		t.Errorf("n8n received message text = %q, want 'Hello bot!'", text)
	}

	// Verify response was sent back to Rocket.Chat
	if !responseSent {
		t.Fatal("expected response to be posted to Rocket.Chat")
	}
	if responseText != "Hello from n8n!" {
		t.Errorf("response text = %q, want 'Hello from n8n!'", responseText)
	}
}

// mockRocketChatServer implements a unified mock Rocket.Chat server (REST + WebSocket)
type mockRocketChatServer struct {
	URL      string
	server   *httptest.Server
	upgrader websocket.Upgrader
	conn     *websocket.Conn
	connMu   sync.Mutex // Protects conn access
	writeMu  sync.Mutex // Serializes WebSocket writes
	t        *testing.T
}

func newMockRocketChatServer(
	t *testing.T,
	mu *sync.Mutex,
	responseSent *bool,
	responseText *string,
) *mockRocketChatServer {
	t.Helper()

	mock := &mockRocketChatServer{
		t: t,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}

	mock.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check if this is a WebSocket upgrade request
		if r.URL.Path == "/websocket" {
			conn, err := mock.upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Errorf("websocket upgrade failed: %v", err)
				return
			}

			mock.connMu.Lock()
			mock.conn = conn
			mock.connMu.Unlock()

			mock.handleWebSocketConnection(conn)
			return
		}

		// Otherwise, handle REST API endpoints
		switch {
		case strings.Contains(r.URL.Path, "/api/v1/me"):
			resp := map[string]interface{}{
				"username": "testbot",
				"success":  true,
			}
			json.NewEncoder(w).Encode(resp)

		case strings.Contains(r.URL.Path, "/api/v1/users.setStatus"):
			json.NewEncoder(w).Encode(map[string]interface{}{"success": true})

		case strings.Contains(r.URL.Path, "/api/v1/subscriptions.get"):
			resp := map[string]interface{}{
				"update": []map[string]interface{}{
					{"rid": "dm-room-123", "t": "d"},
				},
				"success": true,
			}
			json.NewEncoder(w).Encode(resp)

		case strings.Contains(r.URL.Path, "/api/v1/im.history"):
			resp := map[string]interface{}{
				"messages": []map[string]interface{}{},
				"success":  true,
			}
			json.NewEncoder(w).Encode(resp)

		case strings.Contains(r.URL.Path, "/api/v1/chat.postMessage"):
			resp := map[string]interface{}{
				"message": map[string]interface{}{"_id": "reply-msg-id"},
				"success": true,
			}
			json.NewEncoder(w).Encode(resp)

		case strings.Contains(r.URL.Path, "/api/v1/chat.update"):
			mu.Lock()
			defer mu.Unlock()

			var payload map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("failed to decode update payload: %v", err)
			}

			*responseSent = true
			*responseText, _ = payload["text"].(string)
			json.NewEncoder(w).Encode(map[string]interface{}{"success": true})

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	mock.URL = mock.server.URL
	return mock
}

func (m *mockRocketChatServer) handleWebSocketConnection(conn *websocket.Conn) {
	defer conn.Close()

	writeMsg := func(msg interface{}) {
		m.writeMu.Lock()
		defer m.writeMu.Unlock()
		conn.WriteJSON(msg)
	}

	for {
		var msg map[string]interface{}
		if err := conn.ReadJSON(&msg); err != nil {
			return
		}

		// Handle DDP protocol messages
		switch msg["msg"] {
		case "connect":
			// Respond with connected
			writeMsg(map[string]interface{}{
				"msg":     "connected",
				"session": "test-session-123",
			})

		case "method":
			method, _ := msg["method"].(string)
			id, _ := msg["id"].(string)

			switch method {
			case "login":
				// Send successful login result
				writeMsg(map[string]interface{}{
					"msg": "result",
					"id":  id,
				})

			case "UserPresence:online":
				// Acknowledge presence
				writeMsg(map[string]interface{}{
					"msg": "result",
					"id":  id,
				})

			case "stream-notify-room":
				// Acknowledge typing indicator
				writeMsg(map[string]interface{}{
					"msg": "result",
					"id":  id,
				})
			}

		case "sub":
			// Acknowledge subscription
			writeMsg(map[string]interface{}{
				"msg":  "ready",
				"subs": []interface{}{msg["id"]},
			})

		case "ping":
			writeMsg(map[string]interface{}{"msg": "pong"})
		}
	}
}

func (m *mockRocketChatServer) SendMessage(msg interface{}) {
	m.connMu.Lock()
	conn := m.conn
	m.connMu.Unlock()

	if conn != nil {
		m.writeMu.Lock()
		defer m.writeMu.Unlock()
		if err := conn.WriteJSON(msg); err != nil {
			m.t.Errorf("failed to send WebSocket message: %v", err)
		}
	}
}

func (m *mockRocketChatServer) Close() {
	m.server.Close()
	m.connMu.Lock()
	defer m.connMu.Unlock()
	if m.conn != nil {
		m.conn.Close()
	}
}

// eventually polls condition until it returns true or timeout expires (stdlib-only)
func eventually(t *testing.T, condition func() bool, timeout, interval time.Duration, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		if condition() {
			return
		}
		select {
		case <-ticker.C:
			if time.Now().After(deadline) {
				t.Fatal(msg)
				return
			}
		}
	}
}

// update represents a timestamped message update
type update struct {
	at   time.Time
	text string
}

// TestIntegration_BotStreaming verifies streaming response behavior with realistic chunk rates.
// Tests the 1-second ticker batching logic under high-frequency chunk arrival (20/sec over 3s).
func TestIntegration_BotStreaming(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping streaming integration test in short mode")
	}

	// Channel-based update tracking (cleaner than mutex+slice)
	updatesCh := make(chan update, 64)
	var updates []update
	done := make(chan struct{})

	// Single-writer goroutine: appends updates without mutex
	go func() {
		defer close(done)
		for u := range updatesCh {
			updates = append(updates, u)
		}
	}()

	n8nServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)

		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()

		ctx := r.Context()
		for i := 0; i < 60; i++ {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, _ = io.WriteString(
					w,
					fmt.Sprintf(`{"type":"item","content":"chunk %d"}`+"\n", i),
				)
				flusher.Flush()
			}
		}

		_, _ = io.WriteString(w, `{"type":"end"}`+"\n")
		flusher.Flush()
	}))
	t.Cleanup(n8nServer.Close)

	rocketServer := newMockRocketChatServerWithUpdates(t, updatesCh)
	t.Cleanup(rocketServer.Close)

	logger := testutil.NewTestLogger(t)
	parser := bot.NewN8nParser(logger)
	generator := bot.NewWebhookGenerator(
		n8nServer.URL,
		"",
		parser,
		http.DefaultClient,
		logger,
	)

	apiClient := bot.NewAPIClient(rocketServer.URL, "test-user", "test-token", nil, logger)

	client := bot.NewClientWithAPI(
		apiClient,
		"TestBot",
		generator,
		true, // streamedOutput: enables ticker batching
		false,
		logger,
		"online",
	)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- client.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		client.Stop()
		if err := <-errCh; err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("bot.Run() returned unexpected error: %v", err)
		}
	})

	time.Sleep(100 * time.Millisecond)

	rocketServer.SendMessage(map[string]interface{}{
		"msg":        "changed",
		"collection": "stream-room-messages",
		"fields": map[string]interface{}{
			"args": []interface{}{
				map[string]interface{}{
					"_id": "user-msg-456",
					"rid": "dm-room-123",
					"msg": "Stream test message",
					"u": map[string]interface{}{
						"_id":      "user-789",
						"username": "streamuser",
						"name":     "Stream User",
					},
					"ts": map[string]interface{}{
						"$date": float64(time.Now().UnixMilli()),
					},
				},
			},
		},
	})

	// 60 chunks at 50ms = 3s, plus buffer for ticker and final update
	time.Sleep(4500 * time.Millisecond)

	client.Stop()
	time.Sleep(100 * time.Millisecond)

	close(updatesCh)
	<-done

	if len(updates) < 2 {
		t.Fatalf("expected at least 2 batched updates, got %d", len(updates))
	}

	for i := 1; i < len(updates); i++ {
		interval := updates[i].at.Sub(updates[i-1].at)
		if interval < 800*time.Millisecond || interval > 1500*time.Millisecond {
			t.Errorf("update %d: interval %v outside 800ms-1500ms range", i, interval)
		}
	}

	final := updates[len(updates)-1].text
	for i := 0; i < 60; i++ {
		expected := fmt.Sprintf("chunk %d", i)
		if !strings.Contains(final, expected) {
			t.Errorf("final text missing %q", expected)
		}
	}

	t.Logf("streaming test completed: %d updates over %v",
		len(updates),
		updates[len(updates)-1].at.Sub(updates[0].at))
}

// newMockRocketChatServerWithUpdates creates a mock server that sends updates via channel
func newMockRocketChatServerWithUpdates(
	t *testing.T,
	updatesCh chan<- update,
) *mockRocketChatServer {
	t.Helper()

	mock := &mockRocketChatServer{
		t: t,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}

	mock.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check if this is a WebSocket upgrade request
		if r.URL.Path == "/websocket" {
			conn, err := mock.upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Errorf("websocket upgrade failed: %v", err)
				return
			}

			mock.connMu.Lock()
			mock.conn = conn
			mock.connMu.Unlock()

			mock.handleWebSocketConnection(conn)
			return
		}

		// Otherwise, handle REST API endpoints
		switch {
		case strings.Contains(r.URL.Path, "/api/v1/me"):
			resp := map[string]interface{}{
				"username": "testbot",
				"success":  true,
			}
			json.NewEncoder(w).Encode(resp)

		case strings.Contains(r.URL.Path, "/api/v1/users.setStatus"):
			json.NewEncoder(w).Encode(map[string]interface{}{"success": true})

		case strings.Contains(r.URL.Path, "/api/v1/subscriptions.get"):
			resp := map[string]interface{}{
				"update": []map[string]interface{}{
					{"rid": "dm-room-123", "t": "d"},
				},
				"success": true,
			}
			json.NewEncoder(w).Encode(resp)

		case strings.Contains(r.URL.Path, "/api/v1/im.history"):
			resp := map[string]interface{}{
				"messages": []map[string]interface{}{},
				"success":  true,
			}
			json.NewEncoder(w).Encode(resp)

		case strings.Contains(r.URL.Path, "/api/v1/chat.postMessage"):
			resp := map[string]interface{}{
				"message": map[string]interface{}{"_id": "reply-msg-id"},
				"success": true,
			}
			json.NewEncoder(w).Encode(resp)

		case strings.Contains(r.URL.Path, "/api/v1/chat.update"):
			var payload struct {
				Text string `json:"text"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("failed to decode update payload: %v", err)
			}

			updatesCh <- update{
				at:   time.Now(),
				text: payload.Text,
			}

			json.NewEncoder(w).Encode(map[string]interface{}{"success": true})

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	mock.URL = mock.server.URL
	return mock
}
