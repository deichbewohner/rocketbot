package bot

import (
	"testing"
	"time"
)

// MessageBuilder provides a fluent API for creating test messages
type MessageBuilder struct {
	t   *testing.T
	msg Message
}

// NewMessage creates a new message builder with sensible defaults
func NewMessage(t *testing.T) *MessageBuilder {
	t.Helper()
	return &MessageBuilder{
		t: t,
		msg: Message{
			ID:        "msg-test-123",
			Text:      "test message",
			RoomID:    "room-test-456",
			Timestamp: time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC),
			User: MessageUser{
				ID:       "user-test-789",
				Username: "testuser",
				Name:     "Test User",
			},
		},
	}
}

// WithID sets the message ID
func (b *MessageBuilder) WithID(id string) *MessageBuilder {
	b.t.Helper()
	b.msg.ID = id
	return b
}

// WithText sets the message text
func (b *MessageBuilder) WithText(text string) *MessageBuilder {
	b.t.Helper()
	b.msg.Text = text
	return b
}

// WithRoomID sets the room ID
func (b *MessageBuilder) WithRoomID(roomID string) *MessageBuilder {
	b.t.Helper()
	b.msg.RoomID = roomID
	return b
}

// WithThreadID sets the thread ID
func (b *MessageBuilder) WithThreadID(threadID string) *MessageBuilder {
	b.t.Helper()
	b.msg.ThreadID = threadID
	return b
}

// WithTimestamp sets the timestamp
func (b *MessageBuilder) WithTimestamp(ts time.Time) *MessageBuilder {
	b.t.Helper()
	b.msg.Timestamp = ts
	return b
}

// WithUser sets the user information
func (b *MessageBuilder) WithUser(id, username, name string) *MessageBuilder {
	b.t.Helper()
	b.msg.User = MessageUser{
		ID:       id,
		Username: username,
		Name:     name,
	}
	return b
}

// Build returns the constructed message
func (b *MessageBuilder) Build() Message {
	b.t.Helper()
	return b.msg
}
