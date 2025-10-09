package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/deichbewohner/rocketbot/bot/testutil"
)

func TestParseMessage_TimestampVariants(t *testing.T) {
	ms := int64(1609459200000) // 2021-01-01T00:00:00Z
	expected := time.Unix(0, ms*int64(time.Millisecond))

	cases := []struct {
		name      string
		dateField interface{}
	}{
		{name: "float64_millis", dateField: float64(ms)},
		{name: "int64_millis", dateField: int64(ms)},
		{name: "int_millis", dateField: int(ms)},
		{name: "string_millis", dateField: fmt.Sprintf("%d", ms)},
		{name: "rfc3339", dateField: "2021-01-01T00:00:00Z"},
		{name: "numberLong", dateField: map[string]interface{}{"$numberLong": fmt.Sprintf("%d", ms)}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msgData := map[string]interface{}{
				"rid": "ROOMID",
				"msg": "hello",
				"_id": "MSGID",
				"ts": map[string]interface{}{
					"$date": tc.dateField,
				},
				"u": map[string]interface{}{
					"_id":      "USERID",
					"username": "user",
					"name":     "User Name",
				},
			}

			got := parseMessage(msgData)

			if got.RoomID != "ROOMID" || got.Text != "hello" || got.ID != "MSGID" {
				t.Fatalf("unexpected basic fields: %+v", got)
			}
			if got.User.ID != "USERID" || got.User.Username != "user" || got.User.Name != "User Name" {
				t.Fatalf("unexpected user fields: %+v", got.User)
			}
			if !got.Timestamp.Equal(expected) {
				t.Fatalf("timestamp mismatch: got %v want %v", got.Timestamp, expected)
			}
		})
	}
}

func TestParseMessage_MissingFieldsDoesNotPanic(t *testing.T) {
	msgData := map[string]interface{}{
		// intentionally missing rid, msg, u, ts
		"_id": "only-id",
	}

	got := parseMessage(msgData)

	if got.RoomID != "" || got.Text != "" {
		t.Fatalf("expected empty rid/msg, got: rid=%q msg=%q", got.RoomID, got.Text)
	}
	if got.ID != "only-id" {
		t.Fatalf("expected id to be preserved, got %q", got.ID)
	}
	if !got.Timestamp.IsZero() {
		t.Fatalf("expected zero timestamp, got %v", got.Timestamp)
	}
	if got.User.ID != "" || got.User.Username != "" || got.User.Name != "" {
		t.Fatalf("expected zero user fields, got %+v", got.User)
	}
}

func TestMessageSortingOrder(t *testing.T) {
	// Test that messages are sorted in ascending chronological order
	now := time.Now()

	tests := []struct {
		name     string
		input    []Message
		expected []string // Expected order by ID
	}{
		{
			name: "already_sorted",
			input: []Message{
				{ID: "msg1", Timestamp: now},
				{ID: "msg2", Timestamp: now.Add(1 * time.Second)},
				{ID: "msg3", Timestamp: now.Add(2 * time.Second)},
			},
			expected: []string{"msg1", "msg2", "msg3"},
		},
		{
			name: "reverse_order",
			input: []Message{
				{ID: "msg3", Timestamp: now.Add(2 * time.Second)},
				{ID: "msg2", Timestamp: now.Add(1 * time.Second)},
				{ID: "msg1", Timestamp: now},
			},
			expected: []string{"msg1", "msg2", "msg3"},
		},
		{
			name: "random_order",
			input: []Message{
				{ID: "msg2", Timestamp: now.Add(1 * time.Second)},
				{ID: "msg4", Timestamp: now.Add(3 * time.Second)},
				{ID: "msg1", Timestamp: now},
				{ID: "msg3", Timestamp: now.Add(2 * time.Second)},
			},
			expected: []string{"msg1", "msg2", "msg3", "msg4"},
		},
		{
			name: "same_timestamp",
			input: []Message{
				{ID: "msg1", Timestamp: now},
				{ID: "msg2", Timestamp: now},
				{ID: "msg3", Timestamp: now},
			},
			expected: []string{"msg1", "msg2", "msg3"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Make a copy to avoid modifying test data
			messages := make([]Message, len(tt.input))
			copy(messages, tt.input)

			// Apply the same sorting logic used in fetchHistory/fetchThreadHistory
			sortMessagesByTimestamp(messages)

			// Verify order
			if len(messages) != len(tt.expected) {
				t.Fatalf("length mismatch: got %d, want %d", len(messages), len(tt.expected))
			}

			for i, expectedID := range tt.expected {
				if messages[i].ID != expectedID {
					t.Errorf("position %d: got ID %q, want %q", i, messages[i].ID, expectedID)
				}
			}

			// Verify ascending timestamp order
			for i := 1; i < len(messages); i++ {
				if messages[i].Timestamp.Before(messages[i-1].Timestamp) {
					t.Errorf("timestamps not in ascending order at position %d: %v comes before %v",
						i, messages[i].Timestamp, messages[i-1].Timestamp)
				}
			}
		})
	}
}

// sortMessagesByTimestamp uses the exact same sorting logic as fetchHistory/fetchThreadHistory
func sortMessagesByTimestamp(messages []Message) {
	// Defensive sort: ensure ascending timestamp order (same as in client.go)
	sort.Slice(messages, func(i, j int) bool {
		return messages[i].Timestamp.Before(messages[j].Timestamp)
	})
}

func TestParseDDPDate_AllFormats(t *testing.T) {
	ms := int64(1609459200000) // 2021-01-01T00:00:00Z
	expected := time.Unix(0, ms*int64(time.Millisecond))

	tests := []struct {
		name      string
		input     interface{}
		wantTime  time.Time
		wantValid bool
	}{
		{name: "float64", input: float64(ms), wantTime: expected, wantValid: true},
		{name: "int64", input: int64(ms), wantTime: expected, wantValid: true},
		{name: "int", input: int(ms), wantTime: expected, wantValid: true},
		{name: "string_numeric", input: fmt.Sprintf("%d", ms), wantTime: expected, wantValid: true},
		{name: "rfc3339", input: "2021-01-01T00:00:00Z", wantTime: expected, wantValid: true},
		{name: "rfc3339nano", input: "2021-01-01T00:00:00.000000000Z", wantTime: expected, wantValid: true},
		{
			name:      "numberLong",
			input:     map[string]interface{}{"$numberLong": fmt.Sprintf("%d", ms)},
			wantTime:  expected,
			wantValid: true,
		},
		{name: "invalid_string", input: "not a date", wantTime: time.Time{}, wantValid: false},
		{name: "invalid_map", input: map[string]interface{}{"foo": "bar"}, wantTime: time.Time{}, wantValid: false},
		{name: "nil", input: nil, wantTime: time.Time{}, wantValid: false},
		{name: "bool", input: true, wantTime: time.Time{}, wantValid: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, valid := parseDDPDate(tt.input)
			if valid != tt.wantValid {
				t.Fatalf("parseDDPDate() valid = %v, want %v", valid, tt.wantValid)
			}
			if tt.wantValid && !got.Equal(tt.wantTime) {
				t.Errorf("parseDDPDate() = %v, want %v", got, tt.wantTime)
			}
		})
	}
}

func TestShouldCreateThread(t *testing.T) {
	tests := []struct {
		name          string
		msg           Message
		threadDefault bool
		want          bool
	}{
		// threadDefault = false (command-based mode)
		{
			name:          "command_mode_thread_command_no_existing_thread",
			msg:           Message{Text: "Hello /thread", ThreadID: ""},
			threadDefault: false,
			want:          true,
		},
		{
			name:          "command_mode_thread_command_already_in_thread",
			msg:           Message{Text: "Hello /thread", ThreadID: "existing-thread-123"},
			threadDefault: false,
			want:          false,
		},
		{
			name:          "command_mode_no_thread_command",
			msg:           Message{Text: "Hello", ThreadID: ""},
			threadDefault: false,
			want:          false,
		},
		{
			name:          "command_mode_thread_command_in_middle",
			msg:           Message{Text: "Hello /thread world", ThreadID: ""},
			threadDefault: false,
			want:          true,
		},
		{
			name:          "command_mode_empty_text",
			msg:           Message{Text: "", ThreadID: ""},
			threadDefault: false,
			want:          false,
		},
		// threadDefault = true (always thread mode)
		{
			name:          "always_mode_no_existing_thread",
			msg:           Message{Text: "Hello", ThreadID: ""},
			threadDefault: true,
			want:          true,
		},
		{
			name:          "always_mode_already_in_thread",
			msg:           Message{Text: "Hello", ThreadID: "existing-thread-123"},
			threadDefault: true,
			want:          false,
		},
		{
			name:          "always_mode_with_thread_command",
			msg:           Message{Text: "Hello /thread", ThreadID: ""},
			threadDefault: true,
			want:          true,
		},
		{
			name:          "always_mode_empty_text",
			msg:           Message{Text: "", ThreadID: ""},
			threadDefault: true,
			want:          true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldCreateThread(tt.msg, tt.threadDefault)
			if got != tt.want {
				t.Errorf("shouldCreateThread() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseAPIMessages_Ordering(t *testing.T) {
	now := time.Now()

	apiMessages := []struct {
		ID  string `json:"_id"`
		Msg string `json:"msg"`
		Rid string `json:"rid"`
		Ts  string `json:"ts"`
		U   struct {
			ID       string `json:"_id"`
			Username string `json:"username"`
			Name     string `json:"name"`
		} `json:"u"`
	}{
		// Out of order input
		{ID: "msg2", Msg: "Second", Rid: "room1", Ts: fmt.Sprintf("%d", now.Add(1*time.Second).UnixMilli())},
		{ID: "msg1", Msg: "First", Rid: "room1", Ts: fmt.Sprintf("%d", now.UnixMilli())},
		{ID: "msg3", Msg: "Third", Rid: "room1", Ts: fmt.Sprintf("%d", now.Add(2*time.Second).UnixMilli())},
	}

	// Set user data
	for i := range apiMessages {
		apiMessages[i].U.ID = "user1"
		apiMessages[i].U.Username = "testuser"
		apiMessages[i].U.Name = "Test User"
	}

	messages := parseAPIMessages(apiMessages)

	// Verify chronological order
	if len(messages) != 3 {
		t.Fatalf("got %d messages, want 3", len(messages))
	}

	if messages[0].ID != "msg1" {
		t.Errorf("messages[0].ID = %q, want msg1", messages[0].ID)
	}
	if messages[1].ID != "msg2" {
		t.Errorf("messages[1].ID = %q, want msg2", messages[1].ID)
	}
	if messages[2].ID != "msg3" {
		t.Errorf("messages[2].ID = %q, want msg3", messages[2].ID)
	}

	// Verify timestamps are in order
	for i := 1; i < len(messages); i++ {
		if messages[i].Timestamp.Before(messages[i-1].Timestamp) {
			t.Errorf("messages not in chronological order at index %d", i)
		}
	}
}

// Local test helpers for WebSocket testing (no testutil import for white-box tests)

// mockResponseGenerator is a simple mock for ResponseGenerator
type mockResponseGenerator struct {
	response string
	err      error
}

func (m *mockResponseGenerator) GenerateResponse(ctx context.Context, message Message, history []Message) (string, error) {
	return m.response, m.err
}

func TestClient_HandleRoomMessage_IgnoreOwnMessages(t *testing.T) {
	logger := testutil.NewTestLogger(t)

	// Create a client
	client := &Client{
		api: &APIClient{
			userID: "bot-user-123",
		},
		dmRooms: map[string]bool{"dm-room-456": true},
		logger:  logger,
	}

	// Message from the bot itself
	msgData := map[string]interface{}{
		"rid": "dm-room-456",
		"msg": "Hello",
		"_id": "msg123",
		"u": map[string]interface{}{
			"_id":      "bot-user-123", // Same as bot
			"username": "botuser",
		},
	}

	msg := &ddpMessage{
		Msg:        "changed",
		Collection: "stream-room-messages",
		Fields: map[string]interface{}{
			"args": []interface{}{msgData},
		},
	}

	// This should not trigger handleDMResponse (would panic if it did due to nil generator)
	client.handleRoomMessage(msg)
	// If we get here without panic, the message was correctly ignored
}

func TestClient_HandleRoomMessage_IgnoreEdits(t *testing.T) {
	logger := testutil.NewTestLogger(t)

	client := &Client{
		api: &APIClient{
			userID: "bot-user-123",
		},
		dmRooms:   map[string]bool{"dm-room-456": true},
		generator: &mockResponseGenerator{response: "test"},
		logger:    logger,
	}

	// Message with editedAt field
	msgData := map[string]interface{}{
		"rid": "dm-room-456",
		"msg": "Hello",
		"_id": "msg123",
		"u": map[string]interface{}{
			"_id":      "user-789",
			"username": "testuser",
		},
		"editedAt": map[string]interface{}{
			"$date": float64(time.Now().UnixMilli()),
		},
	}

	msg := &ddpMessage{
		Msg:        "changed",
		Collection: "stream-room-messages",
		Fields: map[string]interface{}{
			"args": []interface{}{msgData},
		},
	}

	// This should not trigger handleDMResponse
	client.handleRoomMessage(msg)
}

func TestClient_HandleRoomMessage_IgnoreThreadMetadataUpdates(t *testing.T) {
	logger := testutil.NewTestLogger(t)

	client := &Client{
		api: &APIClient{
			userID: "bot-user-123",
		},
		dmRooms:   map[string]bool{"dm-room-456": true},
		generator: &mockResponseGenerator{response: "test"},
		logger:    logger,
	}

	// Message with tcount field (thread metadata update)
	msgData := map[string]interface{}{
		"rid": "dm-room-456",
		"msg": "Original message",
		"_id": "msg123",
		"u": map[string]interface{}{
			"_id":      "user-789",
			"username": "testuser",
		},
		"tcount": 5, // Thread reply count
	}

	msg := &ddpMessage{
		Msg:        "changed",
		Collection: "stream-room-messages",
		Fields: map[string]interface{}{
			"args": []interface{}{msgData},
		},
	}

	// This should not trigger handleDMResponse
	client.handleRoomMessage(msg)
}

func TestClient_ProcessMessage(t *testing.T) {
	tests := []struct {
		name         string
		msg          *ddpMessage
		initialRooms []string
		setupClient  func(*Client)
		validate     func(*testing.T, *Client, []interface{})
	}{
		{
			name: "connected_sends_login",
			msg: &ddpMessage{
				Msg: "connected",
			},
			initialRooms: []string{},
			setupClient:  func(c *Client) {},
			validate: func(t *testing.T, c *Client, writes []interface{}) {
				t.Helper()
				if len(writes) == 0 {
					t.Fatal("expected login message")
				}
				loginMsg, ok := writes[0].(ddpMessage)
				if !ok || loginMsg.Method != "login" {
					t.Errorf("expected login message, got %+v", writes[0])
				}
			},
		},
		{
			name: "result_login_subscribes_to_rooms",
			msg: &ddpMessage{
				Msg: "result",
				ID:  "login-id",
			},
			initialRooms: []string{"room1", "room2"},
			setupClient: func(c *Client) {
				c.pending["login-id"] = "login"
			},
			validate: func(t *testing.T, c *Client, writes []interface{}) {
				t.Helper()
				// Should have: UserPresence:online + 2 subscriptions + subscribe to notify-user
				if len(writes) < 3 {
					t.Errorf("expected at least 3 messages, got %d", len(writes))
				}
				// Check for UserPresence call
				foundPresence := false
				for _, w := range writes {
					if msg, ok := w.(ddpMessage); ok && msg.Method == "UserPresence:online" {
						foundPresence = true
						break
					}
				}
				if !foundPresence {
					t.Error("expected UserPresence:online call")
				}
			},
		},
		{
			name: "ping_sends_pong",
			msg: &ddpMessage{
				Msg: "ping",
			},
			initialRooms: []string{},
			setupClient:  func(c *Client) {},
			validate: func(t *testing.T, c *Client, writes []interface{}) {
				t.Helper()
				if len(writes) == 0 {
					t.Fatal("expected pong message")
				}
				pongMsg, ok := writes[0].(ddpMessage)
				if !ok || pongMsg.Msg != "pong" {
					t.Errorf("expected pong message, got %+v", writes[0])
				}
			},
		},
		{
			name: "changed_calls_handleChangedMessage",
			msg: &ddpMessage{
				Msg:        "changed",
				Collection: "stream-room-messages",
				Fields:     map[string]interface{}{},
			},
			initialRooms: []string{},
			setupClient:  func(c *Client) {},
			validate: func(t *testing.T, c *Client, writes []interface{}) {
				t.Helper()
				// No writes expected, just routing check
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := testutil.NewTestLogger(t)
			client := &Client{
				api: &APIClient{
					token:  "test-token",
					userID: "test-user",
				},
				pending: make(map[string]string),
				rooms:   make(map[string]bool),
				dmRooms: make(map[string]bool),
				logger:  logger,
			}

			mockWs := testutil.NewMockWsConn()
			client.wsMu.Lock()
			client.ws = mockWs
			client.wsMu.Unlock()

			tt.setupClient(client)
			client.processMessage(tt.msg, tt.initialRooms)
			tt.validate(t, client, mockWs.GetWrites())
		})
	}
}

func TestClient_HandleChangedMessage(t *testing.T) {
	tests := []struct {
		name       string
		msg        *ddpMessage
		dmRooms    map[string]bool
		expectCall bool
	}{
		{
			name: "routes_user_notifications",
			msg: &ddpMessage{
				Collection: "stream-notify-user",
				Fields: map[string]interface{}{
					"args": []interface{}{"inserted", map[string]interface{}{}},
				},
			},
			dmRooms:    map[string]bool{},
			expectCall: false,
		},
		{
			name: "ignores_unknown_collection",
			msg: &ddpMessage{
				Collection: "unknown-collection",
				Fields:     map[string]interface{}{},
			},
			dmRooms:    map[string]bool{},
			expectCall: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := testutil.NewTestLogger(t)
			client := &Client{
				api:     &APIClient{userID: "bot-user"},
				dmRooms: tt.dmRooms,
				logger:  logger,
			}

			// Just ensure it doesn't panic
			client.handleChangedMessage(tt.msg)
		})
	}
}

func TestClient_HandleUserNotification(t *testing.T) {
	tests := []struct {
		name          string
		msg           *ddpMessage
		expectSubRoom string
		expectDM      bool
	}{
		{
			name: "inserted_dm_room",
			msg: &ddpMessage{
				Fields: map[string]interface{}{
					"args": []interface{}{
						"inserted",
						map[string]interface{}{
							"rid": "new-dm-room",
							"t":   "d",
						},
					},
				},
			},
			expectSubRoom: "new-dm-room",
			expectDM:      true,
		},
		{
			name: "updated_channel_room",
			msg: &ddpMessage{
				Fields: map[string]interface{}{
					"args": []interface{}{
						"updated",
						map[string]interface{}{
							"rid": "channel-room",
							"t":   "c",
						},
					},
				},
			},
			expectSubRoom: "channel-room",
			expectDM:      false,
		},
		{
			name: "removed_event_ignored",
			msg: &ddpMessage{
				Fields: map[string]interface{}{
					"args": []interface{}{
						"removed",
						map[string]interface{}{
							"rid": "old-room",
						},
					},
				},
			},
			expectSubRoom: "",
			expectDM:      false,
		},
		{
			name: "missing_args",
			msg: &ddpMessage{
				Fields: map[string]interface{}{},
			},
			expectSubRoom: "",
			expectDM:      false,
		},
		{
			name: "insufficient_args",
			msg: &ddpMessage{
				Fields: map[string]interface{}{
					"args": []interface{}{"inserted"},
				},
			},
			expectSubRoom: "",
			expectDM:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := testutil.NewTestLogger(t)
			client := &Client{
				api:     &APIClient{userID: "bot-user"},
				rooms:   make(map[string]bool),
				dmRooms: make(map[string]bool),
				logger:  logger,
			}

			mockWs := testutil.NewMockWsConn()
			client.wsMu.Lock()
			client.ws = mockWs
			client.wsMu.Unlock()

			client.handleUserNotification(tt.msg)

			if tt.expectSubRoom != "" {
				if !client.rooms[tt.expectSubRoom] {
					t.Errorf("expected room %q to be tracked", tt.expectSubRoom)
				}
				if tt.expectDM && !client.dmRooms[tt.expectSubRoom] {
					t.Errorf("expected DM room %q to be tracked", tt.expectSubRoom)
				}

				// Check subscription was sent
				writes := mockWs.GetWrites()
				foundSub := false
				for _, w := range writes {
					if msg, ok := w.(ddpMessage); ok && msg.Msg == "sub" && msg.Name == "stream-room-messages" {
						foundSub = true
						break
					}
				}
				if !foundSub {
					t.Error("expected subscription message to be sent")
				}
			}
		})
	}
}

func TestClient_HandleRoomMessage_NonDMIgnored(t *testing.T) {
	logger := testutil.NewTestLogger(t)

	client := &Client{
		api: &APIClient{
			userID: "bot-user-123",
		},
		dmRooms:   map[string]bool{"dm-room": true},
		generator: &mockResponseGenerator{response: "test"},
		logger:    logger,
	}

	// Message in a non-DM room
	msgData := map[string]interface{}{
		"rid": "channel-room", // Not in dmRooms
		"msg": "Hello",
		"_id": "msg123",
		"u": map[string]interface{}{
			"_id":      "user-789",
			"username": "testuser",
		},
	}

	msg := &ddpMessage{
		Msg:        "changed",
		Collection: "stream-room-messages",
		Fields: map[string]interface{}{
			"args": []interface{}{msgData},
		},
	}

	// Should not trigger handleDMResponse (would panic if it did due to nil API methods)
	client.handleRoomMessage(msg)
}

func TestClient_SetTypingIndicator(t *testing.T) {
	logger := testutil.NewTestLogger(t)

	tests := []struct {
		name   string
		typing bool
	}{
		{name: "typing_true", typing: true},
		{name: "typing_false", typing: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &Client{
				api:      &APIClient{userID: "bot-user"},
				username: "botuser",
				pending:  make(map[string]string),
				logger:   logger,
			}

			mockWs := testutil.NewMockWsConn()
			client.wsMu.Lock()
			client.ws = mockWs
			client.wsMu.Unlock()

			err := client.setTypingIndicator("room-123", tt.typing)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			writes := mockWs.GetWrites()
			if len(writes) == 0 {
				t.Fatal("expected typing indicator message")
			}

			msg, ok := writes[0].(ddpMessage)
			if !ok || msg.Method != "stream-notify-room" {
				t.Errorf("expected stream-notify-room call, got %+v", writes[0])
			}
		})
	}
}

func TestClient_NewClient(t *testing.T) {
	logger := testutil.NewTestLogger(t)
	generator := &mockResponseGenerator{response: "test"}

	client := NewClient(
		"https://chat.example.com",
		"user-123",
		"token-456",
		"testbot",
		generator,
		true,
		false,
		logger,
		"",
	)

	if client == nil {
		t.Fatal("expected non-nil client")
	}
	if client.api == nil {
		t.Error("expected API client to be initialized")
	}
	if client.name != "testbot" {
		t.Errorf("name = %q, want testbot", client.name)
	}
	if !client.streamedOutput {
		t.Error("expected streamedOutput to be true")
	}
	if client.threadDefault {
		t.Error("expected threadDefault to be false")
	}
	if client.pending == nil || client.rooms == nil || client.dmRooms == nil {
		t.Error("expected maps to be initialized")
	}
}

func TestClient_Stop(t *testing.T) {
	logger := testutil.NewTestLogger(t)
	client := &Client{
		stopChan: make(chan struct{}),
		logger:   logger,
	}

	mockWs := testutil.NewMockWsConn()
	client.ws = mockWs

	client.Stop()

	// Check stopChan is closed
	select {
	case <-client.stopChan:
		// Expected
	default:
		t.Error("expected stopChan to be closed")
	}

	// Verify WebSocket was closed
	if !mockWs.IsClosed() {
		t.Error("expected WebSocket to be closed")
	}
}

func TestClient_API(t *testing.T) {
	logger := testutil.NewTestLogger(t)
	apiClient := NewAPIClient("https://test.com", "user", "token", nil, logger)
	client := &Client{
		api:    apiClient,
		logger: logger,
	}

	if client.API() != apiClient {
		t.Error("API() should return the underlying APIClient")
	}
}

func TestClient_GenerateID(t *testing.T) {
	// Test that generateID produces non-empty hex strings
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := generateID()
		if id == "" {
			t.Error("generateID returned empty string")
		}
		if seen[id] {
			t.Errorf("generateID produced duplicate: %s", id)
		}
		seen[id] = true
	}
}

func TestClient_HandleNonStreamingResponse(t *testing.T) {
	tests := []struct {
		name           string
		message        Message
		threadDefault  bool
		postErr        bool
		generateErr    bool
		updateErr      bool
		expectThreadID string
	}{
		{
			name: "success_no_thread",
			message: Message{
				ID:     "msg123",
				RoomID: "room456",
				Text:   "hello",
			},
			threadDefault:  false,
			expectThreadID: "",
		},
		{
			name: "success_with_thread_command",
			message: Message{
				ID:     "msg123",
				RoomID: "room456",
				Text:   "hello /thread",
			},
			threadDefault:  false,
			expectThreadID: "msg123",
		},
		{
			name: "success_thread_default",
			message: Message{
				ID:     "msg123",
				RoomID: "room456",
				Text:   "hello",
			},
			threadDefault:  true,
			expectThreadID: "msg123",
		},
		{
			name: "post_message_error",
			message: Message{
				ID:     "msg123",
				RoomID: "room456",
				Text:   "hello",
			},
			postErr: true,
		},
		{
			name: "generate_response_error",
			message: Message{
				ID:     "msg123",
				RoomID: "room456",
				Text:   "hello",
			},
			generateErr: true,
		},
		{
			name: "update_message_error",
			message: Message{
				ID:     "msg123",
				RoomID: "room456",
				Text:   "hello",
			},
			updateErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var postCalled, updateCalled bool
			var postedThreadID string

			rt := testutil.RoundTripperFunc(func(r *http.Request) (*http.Response, error) {
				if strings.Contains(r.URL.Path, "/api/v1/chat.postMessage") {
					postCalled = true
					if tt.postErr {
						return &http.Response{
							StatusCode: http.StatusInternalServerError,
							Body:       io.NopCloser(bytes.NewReader([]byte("{}"))),
						}, nil
					}
					// Decode body to check threadID
					var payload map[string]interface{}
					json.NewDecoder(r.Body).Decode(&payload)
					if tmid, ok := payload["tmid"].(string); ok {
						postedThreadID = tmid
					}
					resp := map[string]interface{}{
						"message": map[string]interface{}{"_id": "posted-msg-id"},
						"success": true,
					}
					body, _ := json.Marshal(resp)
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(bytes.NewReader(body)),
					}, nil
				}
				if strings.Contains(r.URL.Path, "/api/v1/chat.update") {
					updateCalled = true
					if tt.updateErr {
						return &http.Response{
							StatusCode: http.StatusInternalServerError,
							Body:       io.NopCloser(bytes.NewReader([]byte("{}"))),
						}, nil
					}
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(bytes.NewReader([]byte(`{"success":true}`))),
					}, nil
				}
				return &http.Response{StatusCode: 404, Body: io.NopCloser(bytes.NewReader([]byte("")))}, nil
			})

			logger := testutil.NewTestLogger(t)
			httpClient := &http.Client{Transport: rt}
			apiClient := NewAPIClient("https://test.com", "user", "token", httpClient, logger)

			var genErr error
			if tt.generateErr {
				genErr = fmt.Errorf("generation failed")
			}
			generator := &mockResponseGenerator{response: "generated response", err: genErr}

			client := &Client{
				api:           apiClient,
				generator:     generator,
				threadDefault: tt.threadDefault,
				logger:        logger,
			}

			ctx := context.Background()
			client.handleNonStreamingResponse(ctx, tt.message, []Message{})

			if !tt.postErr && !postCalled {
				t.Error("expected PostMessage to be called")
			}
			if !tt.postErr && !tt.generateErr && !updateCalled {
				t.Error("expected UpdateMessage to be called")
			}
			if tt.expectThreadID != "" && postedThreadID != tt.expectThreadID {
				t.Errorf("posted threadID = %q, want %q", postedThreadID, tt.expectThreadID)
			}
		})
	}
}

func TestClient_HandleStreamingResponse(t *testing.T) {
	tests := []struct {
		name           string
		message        Message
		chunks         []string
		threadDefault  bool
		postErr        bool
		expectThreadID string
	}{
		{
			name: "success_streams_chunks",
			message: Message{
				ID:     "msg123",
				RoomID: "room456",
				Text:   "hello",
			},
			chunks:         []string{"chunk1", "chunk2", "chunk3"},
			threadDefault:  false,
			expectThreadID: "",
		},
		{
			name: "thread_command",
			message: Message{
				ID:     "msg123",
				RoomID: "room456",
				Text:   "hello /thread",
			},
			chunks:         []string{"response"},
			threadDefault:  false,
			expectThreadID: "msg123",
		},
		{
			name: "post_error",
			message: Message{
				ID:     "msg123",
				RoomID: "room456",
				Text:   "hello",
			},
			chunks:  []string{"chunk1"},
			postErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var postCalled bool
			var updateCount int
			var postedThreadID string

			rt := testutil.RoundTripperFunc(func(r *http.Request) (*http.Response, error) {
				if strings.Contains(r.URL.Path, "/api/v1/chat.postMessage") {
					postCalled = true
					if tt.postErr {
						return &http.Response{
							StatusCode: http.StatusInternalServerError,
							Body:       io.NopCloser(bytes.NewReader([]byte("{}"))),
						}, nil
					}
					var payload map[string]interface{}
					json.NewDecoder(r.Body).Decode(&payload)
					if tmid, ok := payload["tmid"].(string); ok {
						postedThreadID = tmid
					}
					resp := map[string]interface{}{
						"message": map[string]interface{}{"_id": "posted-msg-id"},
						"success": true,
					}
					body, _ := json.Marshal(resp)
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(bytes.NewReader(body)),
					}, nil
				}
				if strings.Contains(r.URL.Path, "/api/v1/chat.update") {
					updateCount++
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(bytes.NewReader([]byte(`{"success":true}`))),
					}, nil
				}
				return &http.Response{StatusCode: 404, Body: io.NopCloser(bytes.NewReader([]byte("")))}, nil
			})

			logger := testutil.NewTestLogger(t)
			httpClient := &http.Client{Transport: rt}
			apiClient := NewAPIClient("https://test.com", "user", "token", httpClient, logger)

			generator := &mockStreamingGenerator{chunks: tt.chunks}

			client := &Client{
				api:           apiClient,
				generator:     generator,
				threadDefault: tt.threadDefault,
				logger:        logger,
			}

			ctx := context.Background()
			client.handleStreamingResponse(ctx, tt.message, []Message{}, generator)

			if !tt.postErr && !postCalled {
				t.Error("expected PostMessage to be called")
			}
			if !tt.postErr && updateCount == 0 {
				t.Error("expected UpdateMessage to be called at least once")
			}
			if tt.expectThreadID != "" && postedThreadID != tt.expectThreadID {
				t.Errorf("posted threadID = %q, want %q", postedThreadID, tt.expectThreadID)
			}
		})
	}
}

// mockStreamingGenerator implements StreamingGenerator for tests
type mockStreamingGenerator struct {
	chunks []string
	err    error
}

func (m *mockStreamingGenerator) GenerateResponse(ctx context.Context, message Message, history []Message) (string, error) {
	return strings.Join(m.chunks, ""), m.err
}

func (m *mockStreamingGenerator) GenerateResponseStream(ctx context.Context, message Message, history []Message) (<-chan string, error) {
	if m.err != nil {
		return nil, m.err
	}
	ch := make(chan string)
	go func() {
		defer close(ch)
		for _, chunk := range m.chunks {
			select {
			case <-ctx.Done():
				return
			case ch <- chunk:
			}
		}
	}()
	return ch, nil
}
