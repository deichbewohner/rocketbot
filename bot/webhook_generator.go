package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
)

// WebhookGenerator generates responses by calling a streaming webhook endpoint
type WebhookGenerator struct {
	webhookURL  string
	webhookAuth string
	parser      StreamParser
	logger      *slog.Logger
	client      *http.Client
}

// NewWebhookGenerator creates a new webhook-based response generator
func NewWebhookGenerator(
	webhookURL, webhookAuth string,
	parser StreamParser,
	client *http.Client,
	logger *slog.Logger,
) *WebhookGenerator {
	return &WebhookGenerator{
		webhookURL:  webhookURL,
		webhookAuth: webhookAuth,
		parser:      parser,
		logger:      logger,
		client:      client,
	}
}

// GenerateResponse implements the non-streaming ResponseGenerator interface
// It accumulates the entire streaming response before returning
func (g *WebhookGenerator) GenerateResponse(
	ctx context.Context,
	message Message,
	history []Message,
) (string, error) {
	ch, err := g.GenerateResponseStream(ctx, message, history)
	if err != nil {
		return "", err
	}

	// Accumulate all chunks
	var result strings.Builder
	for chunk := range ch {
		result.WriteString(chunk)
	}

	return result.String(), nil
}

// GenerateResponseStream implements the StreamingGenerator interface
// It streams the response as chunks via a channel
func (g *WebhookGenerator) GenerateResponseStream(
	ctx context.Context,
	message Message,
	history []Message,
) (<-chan string, error) {
	outCh := make(chan string, 10) // Output channel for text chunks

	// Build history array for payload
	historyPayload := make([]map[string]interface{}, 0, len(history))
	for _, msg := range history {
		historyPayload = append(historyPayload, map[string]interface{}{
			"id":        msg.ID,
			"text":      msg.Text,
			"roomId":    msg.RoomID,
			"timestamp": msg.Timestamp,
			"user": map[string]string{
				"id":       msg.User.ID,
				"username": msg.User.Username,
				"name":     msg.User.Name,
			},
		})
	}

	// Build request payload
	payload := map[string]interface{}{
		"message": map[string]interface{}{
			"id":        message.ID,
			"text":      message.Text,
			"roomId":    message.RoomID,
			"timestamp": message.Timestamp,
			"user": map[string]string{
				"id":       message.User.ID,
				"username": message.User.Username,
				"name":     message.User.Name,
			},
		},
		"history": historyPayload,
	}

	// Add reply message metadata from context if available
	if replyMsgID, ok := ctx.Value(ReplyMessageIDKey).(string); ok {
		payload["replyMessageId"] = replyMsgID
	}
	if replyRoomID, ok := ctx.Value(ReplyRoomIDKey).(string); ok {
		payload["replyRoomId"] = replyRoomID
	}
	if opts := GenerationOptionsFromContext(ctx); opts != (GenerationOptions{}) {
		payload["options"] = map[string]interface{}{
			"scope":           opts.Scope,
			"roomId":          opts.RoomID,
			"sessionDir":      opts.SessionDir,
			"bootstrapPrompt": opts.BootstrapPrompt,
		}
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		close(outCh)
		return outCh, fmt.Errorf("failed to marshal payload: %w", err)
	}

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, "POST", g.webhookURL, bytes.NewReader(payloadBytes))
	if err != nil {
		close(outCh)
		return outCh, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if g.webhookAuth != "" {
		req.Header.Set("Authorization", g.webhookAuth)
	}

	g.logger.Debug("sending webhook request", "message_text", message.Text)

	// Make request
	resp, err := g.client.Do(req)
	if err != nil {
		close(outCh)
		return outCh, fmt.Errorf("failed to send request: %w", err)
	}

	g.logger.Debug(
		"webhook response received",
		"status",
		resp.StatusCode,
		"content_type",
		resp.Header.Get("Content-Type"),
	)

	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close() // explicitly ignore close error in error path
		close(outCh)
		return outCh, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	// Start goroutine to parse stream and convert events to text chunks
	go func() {
		defer close(outCh)
		defer func() {
			if err := resp.Body.Close(); err != nil {
				g.logger.Warn("failed to close webhook response body", "error", err)
			}
		}()

		// Delegate parsing to injected parser
		eventCh, err := g.parser.Parse(ctx, resp.Body)
		if err != nil {
			g.logger.Error("error starting stream parser", "error", err)
			return
		}

		// Convert StreamEvents to text chunks
		for event := range eventCh {
			if event.Type == EventMessageChunk {
				select {
				case outCh <- event.Content:
				case <-ctx.Done():
					return
				}
			}
			// Ignore EventMessage (full messages) in streaming mode
		}
	}()

	return outCh, nil
}
