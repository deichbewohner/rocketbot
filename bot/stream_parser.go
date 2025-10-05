package bot

import (
	"context"
	"io"
)

// StreamEventType represents the type of event in a stream
type StreamEventType string

const (
	// EventMessageChunk represents a partial chunk of a streaming message
	EventMessageChunk StreamEventType = "message_chunk"
	// EventMessage represents a complete message
	EventMessage StreamEventType = "message"
	// Future: EventError, EventMetadata, EventToolUse, etc.
)

// StreamEvent represents an event from a streaming response
type StreamEvent struct {
	Type    StreamEventType
	Content string
}

// StreamParser parses streaming HTTP responses into structured events
type StreamParser interface {
	// Parse reads from the response body and emits StreamEvents
	// The channel will be closed when parsing is complete or an error occurs
	Parse(ctx context.Context, body io.Reader) (<-chan StreamEvent, error)
}
