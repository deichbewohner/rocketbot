package bot_test

import (
	"encoding/json"
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

	// Start bot (this will connect to our mock servers)
	if err := client.Start(); err != nil {
		t.Fatalf("bot.Start() failed: %v", err)
	}
	defer client.Stop()

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
