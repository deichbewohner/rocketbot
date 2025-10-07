package bot

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"testing"
	"time"
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
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

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
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

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
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

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

func TestClient_ProcessMessage_Connected(t *testing.T) {
	// Create a discard logger for testing
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	client := &Client{
		api: &APIClient{
			token:  "test-token",
			userID: "test-user",
		},
		pending: make(map[string]string),
		logger:  logger,
	}

	// Mock WebSocket (local implementation, not using testutil)
	writes := []interface{}{}
	client.wsMu.Lock()
	client.ws = &mockWs{writes: &writes}
	client.wsMu.Unlock()

	msg := &ddpMessage{
		Msg: "connected",
	}

	client.processMessage(msg, []string{})

	// Verify login method was called
	if len(writes) == 0 {
		t.Fatal("expected login message to be sent")
	}

	loginMsg, ok := writes[0].(ddpMessage)
	if !ok || loginMsg.Method != "login" {
		t.Errorf("expected login message, got %+v", writes[0])
	}
}

// mockWs is a minimal WebSocket mock for white-box testing
type mockWs struct {
	writes *[]interface{}
}

func (m *mockWs) ReadJSON(v interface{}) error {
	return fmt.Errorf("not implemented")
}

func (m *mockWs) WriteJSON(v interface{}) error {
	*m.writes = append(*m.writes, v)
	return nil
}

func (m *mockWs) Close() error {
	return nil
}
