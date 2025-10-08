package bot

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
)

// N8nParser parses n8n streaming webhook format
// Expects JSON lines with format: {"type":"item","content":"text"}
type N8nParser struct {
	logger *slog.Logger
}

// NewN8nParser creates a new n8n stream parser
func NewN8nParser(logger *slog.Logger) *N8nParser {
	return &N8nParser{
		logger: logger,
	}
}

// send attempts to send an event to the channel, respecting context cancellation
func (p *N8nParser) send(ctx context.Context, ch chan<- StreamEvent, event StreamEvent) bool {
	select {
	case ch <- event:
		return true
	case <-ctx.Done():
		return false
	}
}

// Parse implements StreamParser for n8n streaming format
// Expects newline-delimited JSON: {"type":"item","content":"text"}
func (p *N8nParser) Parse(ctx context.Context, body io.Reader) (<-chan StreamEvent, error) {
	ch := make(chan StreamEvent, 10) // Buffered channel for backpressure

	go func() {
		defer close(ch)

		scanner := bufio.NewScanner(body)
		// Increase buffer size to handle large events (default is 64KB, increase to 1MB)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)

		lineCount := 0
		var fullMessage strings.Builder

		for scanner.Scan() {
			select {
			case <-ctx.Done():
				p.logger.Info("n8n parsing cancelled")
				return
			default:
			}

			line := scanner.Text()
			lineCount++
			p.logger.Debug("n8n line received", "line_num", lineCount, "line", line)

			// Skip empty lines
			if strings.TrimSpace(line) == "" {
				continue
			}

			// Parse JSON line
			var data map[string]interface{}
			if err := json.Unmarshal([]byte(line), &data); err != nil {
				p.logger.Warn("failed to parse JSON line", "line_num", lineCount, "error", err)
				continue
			}

			// Process based on type field
			eventType, ok := data["type"].(string)
			if !ok {
				p.logger.Debug("no type field in JSON", "line_num", lineCount)
				continue
			}

			switch eventType {
			case "begin":
				// Reset accumulator for new message
				fullMessage.Reset()
				p.logger.Debug("stream begin marker received")

			case "item":
				// Extract content and emit as chunk
				content, ok := data["content"].(string)
				if !ok || content == "" {
					break
				}
				fullMessage.WriteString(content)
				if !p.send(ctx, ch, StreamEvent{Type: EventMessageChunk, Content: content}) {
					return
				}

			case "end":
				// Emit full accumulated message
				final := fullMessage.String()
				if final == "" {
					p.logger.Debug("stream end marker received", "total_length", 0)
					break
				}
				if !p.send(ctx, ch, StreamEvent{Type: EventMessage, Content: final}) {
					return
				}
				p.logger.Debug("stream end marker received", "total_length", len(final))

			default:
				p.logger.Debug("unknown event type", "type", eventType)
			}
		}

		if err := scanner.Err(); err != nil {
			p.logger.Error("scanner error", "error", err)
		}

		p.logger.Debug("n8n parsing complete", "total_lines", lineCount)
	}()

	return ch, nil
}

// Compile-time interface check
var _ StreamParser = (*N8nParser)(nil)
