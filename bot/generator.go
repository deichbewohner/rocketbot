package bot

import (
	"context"
	"time"
)

type contextKey string

const (
	// ReplyMessageIDKey is the context key for the bot's reply message ID
	ReplyMessageIDKey contextKey = "replyMessageID"
	// ReplyRoomIDKey is the context key for the room ID where the bot is replying
	ReplyRoomIDKey contextKey = "replyRoomID"
)

// MessageUser represents the user who sent a message
type MessageUser struct {
	ID       string
	Username string
	Name     string
}

// Message represents an incoming message with full context
type Message struct {
	ID        string
	Text      string
	RoomID    string
	ThreadID  string // tmid - thread message ID (empty if not in a thread)
	Timestamp time.Time
	User      MessageUser
}

// ResponseGenerator generates responses to messages
type ResponseGenerator interface {
	// GenerateResponse generates a response for the given message
	// history contains previous messages in chronological order (oldest first)
	// ctx may contain ReplyMessageIDKey and ReplyRoomIDKey for the bot's reply message
	// Returns the response text or an error
	GenerateResponse(ctx context.Context, message Message, history []Message) (string, error)
}

// StreamingGenerator is an optional interface for generators that support streaming responses
type StreamingGenerator interface {
	ResponseGenerator
	// GenerateResponseStream generates a response as a stream of chunks
	// history contains previous messages in chronological order (oldest first)
	// ctx may contain ReplyMessageIDKey and ReplyRoomIDKey for the bot's reply message
	// The channel will be closed when generation is complete
	GenerateResponseStream(
		ctx context.Context,
		message Message,
		history []Message,
	) (<-chan string, error)
}

// HistoryAwareGenerator can hint how much Rocket.Chat history it needs.
// Returning 0 means no history fetch is needed for the current message.
type HistoryAwareGenerator interface {
	HistoryLimit(message Message) int
}
