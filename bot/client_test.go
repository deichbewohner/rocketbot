package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/deichbewohner/rocketbot/bot/testutil"
)

func waitForCondition(t *testing.T, cond func() bool) {
	t.Helper()
	timeout := time.After(2 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		if cond() {
			return
		}
		select {
		case <-ticker.C:
		case <-timeout:
			t.Fatal("condition not met before timeout")
		}
	}
}

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
		{
			name:      "numberLong",
			dateField: map[string]interface{}{"$numberLong": fmt.Sprintf("%d", ms)},
		},
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
			if got.User.ID != "USERID" || got.User.Username != "user" ||
				got.User.Name != "User Name" {
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
		{
			name:      "rfc3339nano",
			input:     "2021-01-01T00:00:00.000000000Z",
			wantTime:  expected,
			wantValid: true,
		},
		{
			name:      "numberLong",
			input:     map[string]interface{}{"$numberLong": fmt.Sprintf("%d", ms)},
			wantTime:  expected,
			wantValid: true,
		},
		{name: "invalid_string", input: "not a date", wantTime: time.Time{}, wantValid: false},
		{
			name:      "invalid_map",
			input:     map[string]interface{}{"foo": "bar"},
			wantTime:  time.Time{},
			wantValid: false,
		},
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

func TestParseBotCommand(t *testing.T) {
	tests := []struct {
		name string
		text string
		want botCommand
		ok   bool
	}{
		{name: "reset", text: "!rb reset", want: botCommandReset, ok: true},
		{name: "reset uppercase", text: "!RB RESET", want: botCommandReset, ok: true},
		{name: "reset with args", text: "!rb reset now", want: botCommandReset, ok: true},
		{name: "unknown", text: "!rb foo", ok: false},
		{name: "missing prefix", text: "reset", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseBotCommand(tt.text)
			if ok != tt.ok {
				t.Fatalf("parseBotCommand() ok = %v, want %v", ok, tt.ok)
			}
			if got != tt.want {
				t.Fatalf("parseBotCommand() command = %q, want %q", got, tt.want)
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
		{
			ID:  "msg2",
			Msg: "Second",
			Rid: "room1",
			Ts:  fmt.Sprintf("%d", now.Add(1*time.Second).UnixMilli()),
		},
		{ID: "msg1", Msg: "First", Rid: "room1", Ts: fmt.Sprintf("%d", now.UnixMilli())},
		{
			ID:  "msg3",
			Msg: "Third",
			Rid: "room1",
			Ts:  fmt.Sprintf("%d", now.Add(2*time.Second).UnixMilli()),
		},
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

func (m *mockResponseGenerator) GenerateResponse(
	ctx context.Context,
	message Message,
	history []Message,
) (string, error) {
	return m.response, m.err
}

type mockResettableGenerator struct {
	mockResponseGenerator
	resetErr    error
	resetCalled bool
}

func (m *mockResettableGenerator) ResetSession(ctx context.Context, message Message) error {
	m.resetCalled = true
	return m.resetErr
}

func TestClient_HandleDMResponse_ResetCommand(t *testing.T) {
	tests := []struct {
		name              string
		generator         ResponseGenerator
		expectResetCalled bool
		expectReply       string
	}{
		{
			name:              "supported and successful",
			generator:         &mockResettableGenerator{},
			expectResetCalled: true,
			expectReply:       "Session reset. Starting fresh.",
		},
		{
			name:              "supported and failing",
			generator:         &mockResettableGenerator{resetErr: errors.New("boom")},
			expectResetCalled: true,
			expectReply:       "Reset failed. Please try again.",
		},
		{
			name:              "unsupported generator",
			generator:         &mockResponseGenerator{},
			expectResetCalled: false,
			expectReply:       "Session reset is not supported for this bot.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var postedText string
			postCalls := 0
			rt := testutil.RoundTripperFunc(func(r *http.Request) (*http.Response, error) {
				if !strings.Contains(r.URL.Path, "/api/v1/chat.postMessage") {
					t.Fatalf("unexpected request path: %s", r.URL.Path)
				}
				postCalls++
				var payload map[string]interface{}
				_ = json.NewDecoder(r.Body).Decode(&payload)
				postedText, _ = payload["text"].(string)

				resp := map[string]interface{}{
					"message": map[string]interface{}{"_id": "cmd-reply-id"},
					"success": true,
				}
				body, _ := json.Marshal(resp)
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader(body)),
				}, nil
			})

			logger := testutil.NewTestLogger(t)
			httpClient := &http.Client{Transport: rt}
			apiClient := NewAPIClient("https://test.com", "user", "token", httpClient, logger)

			client := &Client{api: apiClient, generator: tt.generator, logger: logger}
			msg := Message{RoomID: "room1", Text: "!rb reset", User: MessageUser{Username: "alice"}}
			client.handleDMResponse(msg)

			if postCalls != 1 {
				t.Fatalf("post calls = %d, want 1", postCalls)
			}
			if postedText != tt.expectReply {
				t.Fatalf("posted text = %q, want %q", postedText, tt.expectReply)
			}

			if rg, ok := tt.generator.(*mockResettableGenerator); ok {
				if rg.resetCalled != tt.expectResetCalled {
					t.Fatalf("resetCalled = %v, want %v", rg.resetCalled, tt.expectResetCalled)
				}
			}
		})
	}
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
		name        string
		msg         *ddpMessage
		setupClient func(*Client)
		validate    func(*testing.T, *Client, []interface{})
	}{
		{
			name: "connected_sends_login",
			msg: &ddpMessage{
				Msg: "connected",
			},
			setupClient: func(c *Client) {},
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
			setupClient: func(c *Client) {
				c.pending["login-id"] = "login"
				c.rooms["room1"] = true
				c.rooms["room2"] = true
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
			name: "changed_calls_handleChangedMessage",
			msg: &ddpMessage{
				Msg:        "changed",
				Collection: "stream-room-messages",
				Fields:     map[string]interface{}{},
			},
			setupClient: func(c *Client) {},
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
			client.processMessage(tt.msg)
			tt.validate(t, client, mockWs.GetWrites())
		})
	}
}

func TestClient_HandleMessages_PingSendsPong(t *testing.T) {
	logger := testutil.NewTestLogger(t)
	client := &Client{
		logger:   logger,
		pending:  make(map[string]string),
		rooms:    make(map[string]bool),
		dmRooms:  make(map[string]bool),
		stopChan: make(chan struct{}),
	}

	mockWs := testutil.NewMockWsConn()
	mockWs.QueueRead(ddpMessage{Msg: "ping"})
	client.wsMu.Lock()
	client.ws = mockWs
	client.wsMu.Unlock()

	err := client.handleMessages(context.Background())
	if err == nil {
		t.Fatal("expected handleMessages to exit with error after queue drained")
	}

	writes := mockWs.GetWrites()
	if len(writes) == 0 {
		t.Fatal("expected pong message to be sent")
	}
	pong, ok := writes[0].(ddpMessage)
	if !ok || pong.Msg != "pong" {
		t.Fatalf("expected pong message, got %+v", writes[0])
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
					if msg, ok := w.(ddpMessage); ok && msg.Msg == "sub" &&
						msg.Name == "stream-room-messages" {
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

func TestClient_ResolvePolicy_UsesRoomOverride(t *testing.T) {
	logger := testutil.NewTestLogger(t)
	streamedOutput := false
	threadDefault := true
	client := &Client{
		streamedOutput:  true,
		threadDefault:   false,
		bootstrapPrompt: "default prompt",
		roomPolicies: map[string]RoomPolicy{
			"room-123": {
				Enabled:            true,
				OpenCodeSessionDir: "/tmp/room-123",
				BootstrapPrompt:    "room prompt",
				StreamedOutput:     &streamedOutput,
				ThreadDefault:      &threadDefault,
			},
		},
		logger: logger,
	}

	policy, ok := client.resolvePolicy("room-123")
	if !ok {
		t.Fatal("expected room policy to resolve")
	}
	if policy.Scope != "room" {
		t.Fatalf("Scope = %q, want room", policy.Scope)
	}
	if policy.SessionDir != "/tmp/room-123" {
		t.Fatalf("SessionDir = %q, want %q", policy.SessionDir, "/tmp/room-123")
	}
	if policy.BootstrapPrompt != "room prompt" {
		t.Fatalf("BootstrapPrompt = %q, want %q", policy.BootstrapPrompt, "room prompt")
	}
	if !policy.ThreadDefault {
		t.Fatal("expected ThreadDefault override to be true")
	}
	if policy.StreamedOutput {
		t.Fatal("expected StreamedOutput override to be false")
	}
}

func TestMessageMentionsUsername(t *testing.T) {
	tests := []struct {
		name     string
		msgData  map[string]interface{}
		username string
		want     bool
	}{
		{
			name: "matches_mentions_array",
			msgData: map[string]interface{}{
				"mentions": []interface{}{map[string]interface{}{"username": "franziska"}},
			},
			username: "franziska",
			want:     true,
		},
		{
			name: "matches_text_mention",
			msgData: map[string]interface{}{
				"msg": "hello @franziska, can you help?",
			},
			username: "franziska",
			want:     true,
		},
		{
			name: "ignores_plain_name_without_at",
			msgData: map[string]interface{}{
				"msg": "hello franziska",
			},
			username: "franziska",
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := messageMentionsUsername(tt.msgData, tt.username); got != tt.want {
				t.Fatalf("messageMentionsUsername() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestClient_ShouldRespondToRoomMessage(t *testing.T) {
	client := &Client{
		username:          "franziska",
		activeRoomThreads: make(map[string]activeThreadState),
	}
	roomPolicy := resolvedPolicy{Scope: "room", RoomID: "room-1", ActiveThreadTrigger: "auto"}

	if client.shouldRespondToRoomMessage(
		Message{RoomID: "room-1", Text: "hello"},
		map[string]interface{}{"msg": "hello"},
		roomPolicy,
	) {
		t.Fatal("expected unmentioned room root message to be ignored")
	}

	if !client.shouldRespondToRoomMessage(
		Message{RoomID: "room-1", Text: "@franziska hello"},
		map[string]interface{}{"msg": "@franziska hello"},
		roomPolicy,
	) {
		t.Fatal("expected mentioned room root message to be handled")
	}

	if client.shouldRespondToRoomMessage(
		Message{RoomID: "room-1", ThreadID: "thread-1", Text: "follow up"},
		map[string]interface{}{"msg": "follow up"},
		roomPolicy,
	) {
		t.Fatal("expected inactive room thread without mention to be ignored")
	}

	client.markActiveRoomThread("room-1", "thread-1", "thread-1")
	if !client.shouldRespondToRoomMessage(
		Message{RoomID: "room-1", ThreadID: "thread-1", Text: "follow up"},
		map[string]interface{}{"msg": "follow up"},
		roomPolicy,
	) {
		t.Fatal("expected active room thread follow-up to be handled")
	}

	mentionOnlyPolicy := resolvedPolicy{
		Scope:               "room",
		RoomID:              "room-1",
		ActiveThreadTrigger: "mention_only",
	}
	if client.shouldRespondToRoomMessage(
		Message{RoomID: "room-1", ThreadID: "thread-1", Text: "follow up"},
		map[string]interface{}{"msg": "follow up"},
		mentionOnlyPolicy,
	) {
		t.Fatal("expected active mention_only thread follow-up without mention to be ignored")
	}
	if !client.shouldRespondToRoomMessage(
		Message{RoomID: "room-1", ThreadID: "thread-1", Text: "@franziska follow up"},
		map[string]interface{}{"msg": "@franziska follow up"},
		mentionOnlyPolicy,
	) {
		t.Fatal("expected active mention_only thread follow-up with mention to be handled")
	}
}

func TestCollectMissedThreadMessages(t *testing.T) {
	threadHistory := []Message{
		{ID: "root", User: MessageUser{ID: "user-root", Username: "alice"}, Text: "root"},
		{ID: "m1", User: MessageUser{ID: "user-1", Username: "alice"}, Text: "first"},
		{ID: "bot-1", User: MessageUser{ID: "bot-user", Username: "bot"}, Text: "reply"},
		{ID: "m2", User: MessageUser{ID: "user-2", Username: "bob"}, Text: "second"},
		{ID: "m3", User: MessageUser{ID: "user-1", Username: "alice"}, Text: "third"},
		{ID: "m3", User: MessageUser{ID: "user-1", Username: "alice"}, Text: "third"},
	}

	missed := collectMissedThreadMessages(threadHistory, "bot-1", "bot-user")
	if len(missed) != 2 {
		t.Fatalf("len(missed) = %d, want 2", len(missed))
	}
	if missed[0].ID != "m2" || missed[1].ID != "m3" {
		t.Fatalf("missed IDs = [%s %s], want [m2 m3]", missed[0].ID, missed[1].ID)
	}
}

func TestSynthesizeMissedThreadMessagesPrompt(t *testing.T) {
	prompt := synthesizeMissedThreadMessagesPrompt([]Message{
		{ID: "m1", User: MessageUser{Username: "alice"}, Text: "first"},
		{ID: "m2", User: MessageUser{Username: "bob"}, Text: "second"},
	})

	if !strings.Contains(prompt, "Messages in the thread since your last response:") {
		t.Fatalf("prompt = %q, want synthesized header", prompt)
	}
	if !strings.Contains(prompt, "- alice: first") || !strings.Contains(prompt, "- bob: second") {
		t.Fatalf("prompt = %q, want both missed messages", prompt)
	}
	if strings.Count(prompt, "- bob: second") != 2 {
		t.Fatalf("prompt = %q, want latest message included once in each section", prompt)
	}
}

func TestStripLeadingUsernameMention(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		username string
		want     string
	}{
		{
			name:     "strip simple leading mention",
			text:     "@franziska hello there",
			username: "franziska",
			want:     "hello there",
		},
		{
			name:     "strip leading mention with punctuation",
			text:     " @franziska: hello there",
			username: "franziska",
			want:     "hello there",
		},
		{
			name:     "strip repeated leading mention",
			text:     "@franziska @franziska hello",
			username: "franziska",
			want:     "hello",
		},
		{
			name:     "keep middle mention",
			text:     "hello @franziska there",
			username: "franziska",
			want:     "hello @franziska there",
		},
		{
			name:     "keep trailing mention",
			text:     "hello there @franziska",
			username: "franziska",
			want:     "hello there @franziska",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stripLeadingUsernameMention(tt.text, tt.username); got != tt.want {
				t.Fatalf("stripLeadingUsernameMention() = %q, want %q", got, tt.want)
			}
		})
	}
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
		"detailed",
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

type recordingGenerator struct {
	response    string
	err         error
	lastMessage Message
	lastHistory []Message
}

func (g *recordingGenerator) GenerateResponse(
	ctx context.Context,
	message Message,
	history []Message,
) (string, error) {
	g.lastMessage = message
	g.lastHistory = append([]Message(nil), history...)
	return g.response, g.err
}

type recordingStreamingGenerator struct {
	chunks         []string
	lastMessage    Message
	lastHistory    []Message
	generateCalled bool
}

func (g *recordingStreamingGenerator) GenerateResponse(
	ctx context.Context,
	message Message,
	history []Message,
) (string, error) {
	g.generateCalled = true
	return "", nil
}

func (g *recordingStreamingGenerator) GenerateResponseStream(
	ctx context.Context,
	message Message,
	history []Message,
) (<-chan string, error) {
	g.lastMessage = message
	g.lastHistory = append([]Message(nil), history...)

	ch := make(chan string)
	go func() {
		defer close(ch)
		for _, chunk := range g.chunks {
			select {
			case <-ctx.Done():
				return
			case ch <- chunk:
			}
		}
	}()
	return ch, nil
}

func TestClient_HandleDMResponse_NonStreaming(t *testing.T) {
	logger := testutil.NewTestLogger(t)
	ws := testutil.NewMockWsConn()
	gen := &recordingGenerator{response: "generated response"}

	var historyCalled bool
	var postPayload map[string]interface{}
	var updatePayload map[string]interface{}
	var updateCount int

	transport := testutil.RoundTripperFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.Contains(r.URL.Path, "/api/v1/im.history"):
			historyCalled = true
			resp := map[string]interface{}{
				"messages": []map[string]interface{}{
					{
						"_id": "other-msg",
						"msg": "previous message",
						"rid": "room123",
						"ts":  "2024-01-01T00:00:00Z",
						"u": map[string]interface{}{
							"_id":      "user2",
							"username": "user2",
							"name":     "User Two",
						},
					},
					{
						"_id": "current-msg",
						"msg": "hello there",
						"rid": "room123",
						"ts":  "2024-01-01T00:00:05Z",
						"u": map[string]interface{}{
							"_id":      "user1",
							"username": "alice",
							"name":     "Alice",
						},
					},
				},
				"success": true,
			}
			body, _ := json.Marshal(resp)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader(body)),
			}, nil
		case strings.Contains(r.URL.Path, "/api/v1/chat.postMessage"):
			var payload map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&payload)
			postPayload = payload

			resp := map[string]interface{}{
				"message": map[string]interface{}{"_id": "reply-msg-id"},
				"success": true,
			}
			body, _ := json.Marshal(resp)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader(body)),
			}, nil
		case strings.Contains(r.URL.Path, "/api/v1/chat.update"):
			var payload map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&payload)
			updatePayload = payload
			updateCount++
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader([]byte(`{"success":true}`))),
			}, nil
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
		return nil, nil
	})

	httpClient := &http.Client{Transport: transport}
	apiClient := NewAPIClient("https://test.com", "user", "token", httpClient, logger)

	client := &Client{
		api:            apiClient,
		generator:      gen,
		logger:         logger,
		streamedOutput: false,
		pending:        make(map[string]string),
		ws:             ws,
		username:       "botuser",
	}

	message := Message{
		ID:     "current-msg",
		Text:   "hello there",
		RoomID: "room123",
		User: MessageUser{
			ID:       "user1",
			Username: "alice",
			Name:     "Alice",
		},
	}

	client.handleDMResponse(message)

	if !historyCalled {
		t.Fatal("expected FetchHistory to be called")
	}
	if postPayload == nil {
		t.Fatal("expected PostMessage payload to be captured")
	}
	if updatePayload == nil {
		t.Fatal("expected UpdateMessage payload to be captured")
	}
	if updateCount == 0 {
		t.Fatal("expected at least one UpdateMessage call")
	}

	if postRoom, _ := postPayload["roomId"].(string); postRoom != "room123" {
		t.Fatalf("post roomId = %q, want room123", postRoom)
	}
	if postText, _ := postPayload["text"].(string); postText != "..." {
		t.Fatalf("post text = %q, want ...", postText)
	}
	if _, ok := postPayload["tmid"]; ok {
		t.Fatal("expected no thread ID for DM response")
	}

	if updateRoom, _ := updatePayload["roomId"].(string); updateRoom != "room123" {
		t.Fatalf("update roomId = %q, want room123", updateRoom)
	}
	if updateMsgID, _ := updatePayload["msgId"].(string); updateMsgID != "reply-msg-id" {
		t.Fatalf("update msgId = %q, want reply-msg-id", updateMsgID)
	}
	if updateText, _ := updatePayload["text"].(string); updateText != "generated response" {
		t.Fatalf("update text = %q, want generated response", updateText)
	}

	if gen.lastMessage.ID != message.ID {
		t.Fatalf("generator received message %q, want %q", gen.lastMessage.ID, message.ID)
	}
	if len(gen.lastHistory) != 1 || gen.lastHistory[0].ID != "other-msg" {
		t.Fatalf("generator history = %+v, want only other-msg", gen.lastHistory)
	}

	writes := ws.GetWrites()
	if len(writes) != 2 {
		t.Fatalf("expected 2 typing indicator writes, got %d", len(writes))
	}

	first, ok := writes[0].(ddpMessage)
	if !ok {
		t.Fatalf("unexpected first write type: %T", writes[0])
	}
	if first.Method != "stream-notify-room" {
		t.Fatalf("first write method = %q, want stream-notify-room", first.Method)
	}
	if len(first.Params) != 3 {
		t.Fatalf("first write params length = %d, want 3", len(first.Params))
	}
	if activities, ok := first.Params[2].([]string); !ok || len(activities) != 1 ||
		activities[0] != "user-typing" {
		t.Fatalf("first write activities = %#v, want [user-typing]", first.Params[2])
	}

	second, ok := writes[1].(ddpMessage)
	if !ok {
		t.Fatalf("unexpected second write type: %T", writes[1])
	}
	if activities, ok := second.Params[2].([]string); !ok || len(activities) != 0 {
		t.Fatalf("second write activities = %#v, want empty slice", second.Params[2])
	}
}

func TestClient_HandleDMResponse_ThreadHistory(t *testing.T) {
	logger := testutil.NewTestLogger(t)
	ws := testutil.NewMockWsConn()
	gen := &recordingGenerator{response: "thread response"}

	var fetchMessageCalled bool
	var threadMessagesCalled bool
	var postPayload map[string]interface{}
	var updatePayload map[string]interface{}

	transport := testutil.RoundTripperFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.Contains(r.URL.Path, "/api/v1/chat.getMessage"):
			fetchMessageCalled = true
			resp := map[string]interface{}{
				"message": map[string]interface{}{
					"_id": "thread-root",
					"msg": "root message",
					"rid": "room123",
					"ts":  "2024-01-01T00:00:00Z",
					"u": map[string]interface{}{
						"_id":      "user-root",
						"username": "root",
						"name":     "Root",
					},
				},
				"success": true,
			}
			body, _ := json.Marshal(resp)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader(body)),
			}, nil
		case strings.Contains(r.URL.Path, "/api/v1/chat.getThreadMessages"):
			threadMessagesCalled = true
			resp := map[string]interface{}{
				"messages": []map[string]interface{}{
					{
						"_id": "other-reply",
						"msg": "previous reply",
						"rid": "room123",
						"ts":  "2024-01-01T00:00:05Z",
						"u": map[string]interface{}{
							"_id":      "user2",
							"username": "bob",
							"name":     "Bob",
						},
					},
					{
						"_id": "current-msg",
						"msg": "current reply",
						"rid": "room123",
						"ts":  "2024-01-01T00:00:06Z",
						"u": map[string]interface{}{
							"_id":      "user1",
							"username": "alice",
							"name":     "Alice",
						},
					},
				},
				"success": true,
			}
			body, _ := json.Marshal(resp)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader(body)),
			}, nil
		case strings.Contains(r.URL.Path, "/api/v1/chat.postMessage"):
			var payload map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&payload)
			postPayload = payload
			resp := map[string]interface{}{
				"message": map[string]interface{}{"_id": "reply-msg-id"},
				"success": true,
			}
			body, _ := json.Marshal(resp)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader(body)),
			}, nil
		case strings.Contains(r.URL.Path, "/api/v1/chat.update"):
			var payload map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&payload)
			updatePayload = payload
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader([]byte(`{"success":true}`))),
			}, nil
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
		return nil, nil
	})

	httpClient := &http.Client{Transport: transport}
	apiClient := NewAPIClient("https://test.com", "user", "token", httpClient, logger)

	client := &Client{
		api:            apiClient,
		generator:      gen,
		logger:         logger,
		streamedOutput: false,
		pending:        make(map[string]string),
		ws:             ws,
		username:       "botuser",
	}

	message := Message{
		ID:       "current-msg",
		Text:     "current reply",
		RoomID:   "room123",
		ThreadID: "thread-root",
		User: MessageUser{
			ID:       "user1",
			Username: "alice",
			Name:     "Alice",
		},
	}

	client.handleDMResponse(message)

	if !fetchMessageCalled {
		t.Fatal("expected FetchMessage to be called for thread history")
	}
	if !threadMessagesCalled {
		t.Fatal("expected chat.getThreadMessages to be invoked")
	}
	if postPayload == nil || updatePayload == nil {
		t.Fatal("expected PostMessage and UpdateMessage to be called")
	}

	if len(gen.lastHistory) != 2 {
		t.Fatalf("generator history length = %d, want 2", len(gen.lastHistory))
	}
	if gen.lastHistory[0].ID != "thread-root" {
		t.Fatalf("first history message = %q, want thread-root", gen.lastHistory[0].ID)
	}
	if gen.lastHistory[1].ID != "other-reply" {
		t.Fatalf("second history message = %q, want other-reply", gen.lastHistory[1].ID)
	}

	writes := ws.GetWrites()
	if len(writes) != 2 {
		t.Fatalf("expected 2 typing indicator writes, got %d", len(writes))
	}

	for i, w := range writes {
		msg, ok := w.(ddpMessage)
		if !ok {
			t.Fatalf("write %d has unexpected type %T", i, w)
		}
		if msg.Method != "stream-notify-room" {
			t.Fatalf("write %d method = %q, want stream-notify-room", i, msg.Method)
		}
	}
}

func TestClient_HandleResponse_ActiveRoomThreadMentionOnlySynthesizesMissedMessages(t *testing.T) {
	logger := testutil.NewTestLogger(t)
	ws := testutil.NewMockWsConn()
	gen := &recordingGenerator{response: "thread response"}

	var fetchMessageCalled bool
	var threadMessagesCalled bool

	transport := testutil.RoundTripperFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.Contains(r.URL.Path, "/api/v1/chat.getMessage"):
			fetchMessageCalled = true
			resp := map[string]interface{}{
				"message": map[string]interface{}{
					"_id": "thread-root",
					"msg": "root message",
					"rid": "room123",
					"ts":  "2024-01-01T00:00:00Z",
					"u": map[string]interface{}{
						"_id":      "user-root",
						"username": "root",
						"name":     "Root",
					},
				},
				"success": true,
			}
			body, _ := json.Marshal(resp)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader(body)),
			}, nil
		case strings.Contains(r.URL.Path, "/api/v1/chat.getThreadMessages"):
			threadMessagesCalled = true
			resp := map[string]interface{}{
				"messages": []map[string]interface{}{
					{
						"_id": "bot-reply-1",
						"msg": "earlier bot reply",
						"rid": "room123",
						"ts":  "2024-01-01T00:00:05Z",
						"u": map[string]interface{}{
							"_id":      "bot-user",
							"username": "botuser",
							"name":     "Bot",
						},
					},
					{
						"_id": "missed-1",
						"msg": "first missed",
						"rid": "room123",
						"ts":  "2024-01-01T00:00:06Z",
						"u": map[string]interface{}{
							"_id":      "user1",
							"username": "alice",
							"name":     "Alice",
						},
					},
					{
						"_id": "current-msg",
						"msg": "current mention",
						"rid": "room123",
						"ts":  "2024-01-01T00:00:07Z",
						"u": map[string]interface{}{
							"_id":      "user2",
							"username": "bob",
							"name":     "Bob",
						},
					},
				},
				"success": true,
			}
			body, _ := json.Marshal(resp)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader(body)),
			}, nil
		case strings.Contains(r.URL.Path, "/api/v1/chat.postMessage"):
			resp := map[string]interface{}{
				"message": map[string]interface{}{"_id": "reply-msg-id"},
				"success": true,
			}
			body, _ := json.Marshal(resp)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader(body)),
			}, nil
		case strings.Contains(r.URL.Path, "/api/v1/chat.update"):
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader([]byte(`{"success":true}`))),
			}, nil
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
		return nil, nil
	})

	httpClient := &http.Client{Transport: transport}
	apiClient := NewAPIClient("https://test.com", "bot-user", "token", httpClient, logger)

	client := &Client{
		api:                 apiClient,
		generator:           gen,
		logger:              logger,
		streamedOutput:      false,
		pending:             make(map[string]string),
		ws:                  ws,
		username:            "botuser",
		activeRoomThreads:   map[string]activeThreadState{"room123:thread-root": {LastIngestedMessageID: "bot-reply-1"}},
		activeThreadTrigger: "auto",
	}

	message := Message{
		ID:       "current-msg",
		Text:     "current mention",
		RoomID:   "room123",
		ThreadID: "thread-root",
		User: MessageUser{
			ID:       "user2",
			Username: "bob",
			Name:     "Bob",
		},
	}

	client.handleResponse(message, resolvedPolicy{
		Scope:               "room",
		RoomID:              "room123",
		ActiveThreadTrigger: "mention_only",
	})

	if !fetchMessageCalled || !threadMessagesCalled {
		t.Fatal("expected thread history to be fetched for mention_only synthesis")
	}
	if !strings.Contains(gen.lastMessage.Text, "- alice: first missed") {
		t.Fatalf("generator message = %q, want missed message context", gen.lastMessage.Text)
	}
	if strings.Count(gen.lastMessage.Text, "- bob: current mention") != 2 {
		t.Fatalf("generator message = %q, want current message in both synthesized sections", gen.lastMessage.Text)
	}

	state, ok := client.getActiveRoomThreadState("room123", "thread-root")
	if !ok {
		t.Fatal("expected active thread state to be preserved")
	}
	if state.LastIngestedMessageID != "current-msg" {
		t.Fatalf("LastIngestedMessageID = %q, want current-msg", state.LastIngestedMessageID)
	}
}

func TestClient_HandleDMResponse_Streaming(t *testing.T) {
	logger := testutil.NewTestLogger(t)
	ws := testutil.NewMockWsConn()

	gen := &recordingStreamingGenerator{
		chunks: []string{"chunk1 ", "chunk2"},
	}

	var historyCalled bool
	var postPayload map[string]interface{}
	var updateCount int

	transport := testutil.RoundTripperFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.Contains(r.URL.Path, "/api/v1/im.history"):
			historyCalled = true
			resp := map[string]interface{}{
				"messages": []map[string]interface{}{
					{
						"_id": "other-msg",
						"msg": "previous message",
						"rid": "room123",
						"ts":  "2024-01-01T00:00:00Z",
						"u": map[string]interface{}{
							"_id":      "user2",
							"username": "user2",
							"name":     "User Two",
						},
					},
					{
						"_id": "current-msg",
						"msg": "hello there",
						"rid": "room123",
						"ts":  "2024-01-01T00:00:05Z",
						"u": map[string]interface{}{
							"_id":      "user1",
							"username": "alice",
							"name":     "Alice",
						},
					},
				},
				"success": true,
			}
			body, _ := json.Marshal(resp)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader(body)),
			}, nil
		case strings.Contains(r.URL.Path, "/api/v1/chat.postMessage"):
			var payload map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&payload)
			postPayload = payload
			resp := map[string]interface{}{
				"message": map[string]interface{}{"_id": "reply-msg-id"},
				"success": true,
			}
			body, _ := json.Marshal(resp)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader(body)),
			}, nil
		case strings.Contains(r.URL.Path, "/api/v1/chat.update"):
			updateCount++
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader([]byte(`{"success":true}`))),
			}, nil
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
		return nil, nil
	})

	httpClient := &http.Client{Transport: transport}
	apiClient := NewAPIClient("https://test.com", "user", "token", httpClient, logger)

	client := &Client{
		api:            apiClient,
		generator:      gen,
		logger:         logger,
		streamedOutput: true,
		pending:        make(map[string]string),
		ws:             ws,
		username:       "botuser",
	}

	message := Message{
		ID:     "current-msg",
		Text:   "hello there",
		RoomID: "room123",
		User: MessageUser{
			ID:       "user1",
			Username: "alice",
			Name:     "Alice",
		},
	}

	client.handleDMResponse(message)

	if !historyCalled {
		t.Fatal("expected FetchHistory to be called")
	}
	if postPayload == nil {
		t.Fatal("expected PostMessage to be called")
	}
	if updateCount == 0 {
		t.Fatal("expected at least one UpdateMessage call during streaming")
	}
	if gen.generateCalled {
		t.Fatal("GenerateResponse should not be called in streaming mode")
	}
	if len(gen.lastHistory) != 1 || gen.lastHistory[0].ID != "other-msg" {
		t.Fatalf("streaming generator history = %+v, want only other-msg", gen.lastHistory)
	}

	writes := ws.GetWrites()
	if len(writes) != 2 {
		t.Fatalf("expected 2 typing indicator writes, got %d", len(writes))
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
					_ = json.NewDecoder(r.Body).Decode(&payload)
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
				return &http.Response{
					StatusCode: 404,
					Body:       io.NopCloser(bytes.NewReader([]byte(""))),
				}, nil
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
			client.handleNonStreamingResponse(
				ctx,
				tt.message,
				[]Message{},
				resolvedPolicy{ThreadDefault: tt.threadDefault},
			)

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
					_ = json.NewDecoder(r.Body).Decode(&payload)
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
				return &http.Response{
					StatusCode: 404,
					Body:       io.NopCloser(bytes.NewReader([]byte(""))),
				}, nil
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
			client.handleStreamingResponse(
				ctx,
				tt.message,
				[]Message{},
				generator,
				resolvedPolicy{ThreadDefault: tt.threadDefault},
			)

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

func (m *mockStreamingGenerator) GenerateResponse(
	ctx context.Context,
	message Message,
	history []Message,
) (string, error) {
	return strings.Join(m.chunks, ""), m.err
}

func (m *mockStreamingGenerator) GenerateResponseStream(
	ctx context.Context,
	message Message,
	history []Message,
) (<-chan string, error) {
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

// mockWSDialer is a mock WebSocket dialer for testing Start()
type mockWSDialer struct {
	conn      wsConn
	err       error
	mu        sync.RWMutex
	dialedURL string
}

func (m *mockWSDialer) Dial(urlStr string, requestHeader map[string][]string) (wsConn, error) {
	m.mu.Lock()
	m.dialedURL = urlStr
	m.mu.Unlock()
	if m.err != nil {
		return nil, m.err
	}
	return m.conn, nil
}

func (m *mockWSDialer) DialedURL() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.dialedURL
}

func TestClient_Start(t *testing.T) {
	logger := testutil.NewTestLogger(t)
	mockWs := testutil.NewMockWsConn()

	// Track API calls in order
	var usernameCalled, statusCalled, subscriptionsCalled bool

	// Mock HTTP API calls (same pattern as handleDMResponse tests)
	transport := testutil.RoundTripperFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.Contains(r.URL.Path, "/api/v1/me"):
			usernameCalled = true
			resp := map[string]interface{}{
				"username": "testbot",
				"success":  true,
			}
			body, _ := json.Marshal(resp)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader(body)),
			}, nil

		case strings.Contains(r.URL.Path, "/api/v1/users.setStatus"):
			statusCalled = true
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader([]byte(`{"success":true}`))),
			}, nil

		case strings.Contains(r.URL.Path, "/api/v1/subscriptions.get"):
			subscriptionsCalled = true
			resp := map[string]interface{}{
				"update": []map[string]interface{}{
					{"rid": "room1", "t": "c"},
					{"rid": "dm1", "t": "d"},
					{"rid": "dm2", "t": "d"},
				},
				"success": true,
			}
			body, _ := json.Marshal(resp)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader(body)),
			}, nil

		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
		return nil, nil
	})

	// Mock WebSocket dialer
	mockDialer := &mockWSDialer{
		conn: mockWs,
	}

	httpClient := &http.Client{Transport: transport}
	apiClient := NewAPIClient(
		"https://test.example.com",
		"user-123",
		"token-456",
		httpClient,
		logger,
	)

	client := NewClientWithAPI(
		apiClient,
		"TestBot",
		&mockResponseGenerator{response: "test"},
		false,
		"detailed",
		false,
		"auto",
		logger,
		"online",
		"",
		nil,
	)
	client.wsDialer = mockDialer

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- client.Run(ctx)
	}()

	waitForCondition(t, func() bool {
		return len(mockWs.GetWrites()) > 0
	})

	cancel()
	err := <-errCh
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() returned unexpected error: %v", err)
	}

	// Verify API orchestration sequence
	if !usernameCalled {
		t.Error("expected FetchUsername to be called")
	}
	if !statusCalled {
		t.Error("expected SetStatusOnline to be called")
	}
	if !subscriptionsCalled {
		t.Error("expected GetSubscriptions to be called")
	}

	// Verify state mutations
	if client.username != "testbot" {
		t.Errorf("client.username = %q, want testbot", client.username)
	}
	if len(client.dmRooms) != 2 {
		t.Errorf("len(client.dmRooms) = %d, want 2", len(client.dmRooms))
	}
	if !client.dmRooms["dm1"] || !client.dmRooms["dm2"] {
		t.Errorf("dmRooms = %+v, expected dm1 and dm2", client.dmRooms)
	}

	// Verify WebSocket dialer was called with correct URL
	expectedURL := "wss://test.example.com/websocket"
	if mockDialer.DialedURL() != expectedURL {
		t.Errorf("dialed URL = %q, want %q", mockDialer.DialedURL(), expectedURL)
	}

	// Verify DDP connect message was sent
	writes := mockWs.GetWrites()
	if len(writes) == 0 {
		t.Fatal("expected DDP connect message to be sent")
	}

	connectMsg, ok := writes[0].(ddpMessage)
	if !ok {
		t.Fatalf("first write has unexpected type: %T", writes[0])
	}
	if connectMsg.Msg != "connect" {
		t.Errorf("first message.Msg = %q, want connect", connectMsg.Msg)
	}
	if connectMsg.Version != "1" {
		t.Errorf("connect message Version = %q, want 1", connectMsg.Version)
	}
	if len(connectMsg.Support) == 0 || connectMsg.Support[0] != "1" {
		t.Errorf("connect message Support = %v, want [1]", connectMsg.Support)
	}
}

func TestClient_Start_FetchUsernameError(t *testing.T) {
	logger := testutil.NewTestLogger(t)

	transport := testutil.RoundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "/api/v1/me") {
			return &http.Response{
				StatusCode: http.StatusUnauthorized,
				Body:       io.NopCloser(bytes.NewReader([]byte(`{"success":false}`))),
			}, nil
		}
		t.Fatalf("unexpected request path: %s", r.URL.Path)
		return nil, nil
	})

	httpClient := &http.Client{Transport: transport}
	apiClient := NewAPIClient("https://test.com", "user", "token", httpClient, logger)

	client := NewClientWithAPI(
		apiClient,
		"TestBot",
		&mockResponseGenerator{},
		false,
		"detailed",
		false,
		"auto",
		logger,
		"",
		"",
		nil,
	)

	err := client.Run(context.Background())
	if err == nil {
		t.Fatal("expected error when FetchUsername fails")
	}
	if !strings.Contains(err.Error(), "failed to fetch username") {
		t.Errorf("error message = %q, want it to contain 'failed to fetch username'", err.Error())
	}
}

func TestClient_Start_SetStatusError(t *testing.T) {
	logger := testutil.NewTestLogger(t)

	transport := testutil.RoundTripperFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.Contains(r.URL.Path, "/api/v1/me"):
			resp := map[string]interface{}{"username": "testbot", "success": true}
			body, _ := json.Marshal(resp)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader(body)),
			}, nil
		case strings.Contains(r.URL.Path, "/api/v1/users.setStatus"):
			return &http.Response{
				StatusCode: http.StatusInternalServerError,
				Body:       io.NopCloser(bytes.NewReader([]byte(`{"success":false}`))),
			}, nil
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
		return nil, nil
	})

	httpClient := &http.Client{Transport: transport}
	apiClient := NewAPIClient("https://test.com", "user", "token", httpClient, logger)

	client := NewClientWithAPI(
		apiClient,
		"TestBot",
		&mockResponseGenerator{},
		false,
		"detailed",
		false,
		"auto",
		logger,
		"",
		"",
		nil,
	)

	err := client.Run(context.Background())
	if err == nil {
		t.Fatal("expected error when SetStatusOnline fails")
	}
	if !strings.Contains(err.Error(), "failed to set status online") {
		t.Errorf(
			"error message = %q, want it to contain 'failed to set status online'",
			err.Error(),
		)
	}
}

func TestClient_Start_GetSubscriptionsError(t *testing.T) {
	logger := testutil.NewTestLogger(t)

	transport := testutil.RoundTripperFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.Contains(r.URL.Path, "/api/v1/me"):
			resp := map[string]interface{}{"username": "testbot", "success": true}
			body, _ := json.Marshal(resp)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader(body)),
			}, nil
		case strings.Contains(r.URL.Path, "/api/v1/users.setStatus"):
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader([]byte(`{"success":true}`))),
			}, nil
		case strings.Contains(r.URL.Path, "/api/v1/subscriptions.get"):
			return &http.Response{
				StatusCode: http.StatusInternalServerError,
				Body:       io.NopCloser(bytes.NewReader([]byte(`{"success":false}`))),
			}, nil
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
		return nil, nil
	})

	httpClient := &http.Client{Transport: transport}
	apiClient := NewAPIClient("https://test.com", "user", "token", httpClient, logger)

	client := NewClientWithAPI(
		apiClient,
		"TestBot",
		&mockResponseGenerator{},
		false,
		"detailed",
		false,
		"auto",
		logger,
		"",
		"",
		nil,
	)

	err := client.Run(context.Background())
	if err == nil {
		t.Fatal("expected error when GetSubscriptions fails")
	}
	if !strings.Contains(err.Error(), "failed to get subscriptions") {
		t.Errorf(
			"error message = %q, want it to contain 'failed to get subscriptions'",
			err.Error(),
		)
	}
}

func TestClient_Start_WebSocketDialError(t *testing.T) {
	logger := testutil.NewTestLogger(t)

	transport := testutil.RoundTripperFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.Contains(r.URL.Path, "/api/v1/me"):
			resp := map[string]interface{}{"username": "testbot", "success": true}
			body, _ := json.Marshal(resp)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader(body)),
			}, nil
		case strings.Contains(r.URL.Path, "/api/v1/users.setStatus"):
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader([]byte(`{"success":true}`))),
			}, nil
		case strings.Contains(r.URL.Path, "/api/v1/subscriptions.get"):
			resp := map[string]interface{}{
				"update":  []map[string]interface{}{},
				"success": true,
			}
			body, _ := json.Marshal(resp)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewReader(body)),
			}, nil
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
		return nil, nil
	})

	// Mock dialer that returns an error
	mockDialer := &mockWSDialer{
		err: fmt.Errorf("connection refused"),
	}

	httpClient := &http.Client{Transport: transport}
	apiClient := NewAPIClient("https://test.com", "user", "token", httpClient, logger)

	client := NewClientWithAPI(
		apiClient,
		"TestBot",
		&mockResponseGenerator{},
		false,
		"detailed",
		false,
		"auto",
		logger,
		"",
		"",
		nil,
	)
	client.wsDialer = mockDialer

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- client.Run(ctx)
	}()

	waitForCondition(t, func() bool {
		return mockDialer.DialedURL() != ""
	})

	cancel()
	if err := <-errCh; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() returned unexpected error: %v", err)
	}

	expectedURL := "wss://test.com/websocket"
	if mockDialer.DialedURL() != expectedURL {
		t.Errorf("dialed URL = %q, want %q", mockDialer.DialedURL(), expectedURL)
	}
}

func TestClient_Start_HTTPToWS_URLConversion(t *testing.T) {
	logger := testutil.NewTestLogger(t)

	tests := []struct {
		name       string
		baseURL    string
		expectedWS string
	}{
		{
			name:       "https_to_wss",
			baseURL:    "https://chat.example.com",
			expectedWS: "wss://chat.example.com/websocket",
		},
		{
			name:       "http_to_ws",
			baseURL:    "http://localhost:3000",
			expectedWS: "ws://localhost:3000/websocket",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockWs := testutil.NewMockWsConn()
			transport := testutil.RoundTripperFunc(func(r *http.Request) (*http.Response, error) {
				switch {
				case strings.Contains(r.URL.Path, "/api/v1/me"):
					resp := map[string]interface{}{"username": "testbot", "success": true}
					body, _ := json.Marshal(resp)
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(bytes.NewReader(body)),
					}, nil
				case strings.Contains(r.URL.Path, "/api/v1/users.setStatus"):
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(bytes.NewReader([]byte(`{"success":true}`))),
					}, nil
				case strings.Contains(r.URL.Path, "/api/v1/subscriptions.get"):
					resp := map[string]interface{}{
						"update":  []map[string]interface{}{},
						"success": true,
					}
					body, _ := json.Marshal(resp)
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(bytes.NewReader(body)),
					}, nil
				default:
					t.Fatalf("unexpected request path: %s", r.URL.Path)
				}
				return nil, nil
			})

			mockDialer := &mockWSDialer{conn: mockWs}
			httpClient := &http.Client{Transport: transport}
			apiClient := NewAPIClient(tt.baseURL, "user", "token", httpClient, logger)

			client := NewClientWithAPI(
				apiClient,
				"TestBot",
				&mockResponseGenerator{},
				false,
				"detailed",
				false,
				"auto",
				logger,
				"",
				"",
				nil,
			)
			client.wsDialer = mockDialer

			ctx, cancel := context.WithCancel(context.Background())
			errCh := make(chan error, 1)
			go func() {
				errCh <- client.Run(ctx)
			}()

			waitForCondition(t, func() bool {
				return mockDialer.DialedURL() != ""
			})

			cancel()
			if err := <-errCh; err != nil && !errors.Is(err, context.Canceled) {
				t.Fatalf("Run() returned unexpected error: %v", err)
			}

			if mockDialer.DialedURL() != tt.expectedWS {
				t.Errorf("dialed URL = %q, want %q", mockDialer.DialedURL(), tt.expectedWS)
			}
		})
	}
}
