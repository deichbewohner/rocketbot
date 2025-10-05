package bot_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/deichbewohner/rocketbot/bot"
	"github.com/deichbewohner/rocketbot/bot/testutil"
)

func TestN8nParser_Parse(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantEvents   []bot.StreamEvent
		wantEventCnt int // For cases where we check count only
	}{
		{
			name: "happy_path_begin_items_end",
			input: `{"type":"begin"}
{"type":"item","content":"Hello"}
{"type":"item","content":" world"}
{"type":"item","content":"!"}
{"type":"end"}
`,
			wantEvents: []bot.StreamEvent{
				{Type: bot.EventMessageChunk, Content: "Hello"},
				{Type: bot.EventMessageChunk, Content: " world"},
				{Type: bot.EventMessageChunk, Content: "!"},
				{Type: bot.EventMessage, Content: "Hello world!"},
			},
		},
		{
			name: "multiple_items_no_explicit_markers",
			input: `{"type":"item","content":"First"}
{"type":"item","content":"Second"}
{"type":"item","content":"Third"}
`,
			wantEvents: []bot.StreamEvent{
				{Type: bot.EventMessageChunk, Content: "First"},
				{Type: bot.EventMessageChunk, Content: "Second"},
				{Type: bot.EventMessageChunk, Content: "Third"},
			},
		},
		{
			name:         "empty_input",
			input:        "",
			wantEventCnt: 0,
		},
		{
			name: "empty_lines_skipped",
			input: `{"type":"item","content":"First"}

{"type":"item","content":"Second"}

`,
			wantEvents: []bot.StreamEvent{
				{Type: bot.EventMessageChunk, Content: "First"},
				{Type: bot.EventMessageChunk, Content: "Second"},
			},
		},
		{
			name: "malformed_json_skipped",
			input: `{"type":"item","content":"Valid"}
{bad json here}
{"type":"item","content":"Also valid"}
`,
			wantEvents: []bot.StreamEvent{
				{Type: bot.EventMessageChunk, Content: "Valid"},
				{Type: bot.EventMessageChunk, Content: "Also valid"},
			},
		},
		{
			name: "missing_type_field_skipped",
			input: `{"content":"No type field"}
{"type":"item","content":"Has type"}
`,
			wantEvents: []bot.StreamEvent{
				{Type: bot.EventMessageChunk, Content: "Has type"},
			},
		},
		{
			name: "empty_content_not_emitted",
			input: `{"type":"item","content":""}
{"type":"item","content":"Valid"}
{"type":"item","content":""}
`,
			wantEvents: []bot.StreamEvent{
				{Type: bot.EventMessageChunk, Content: "Valid"},
			},
		},
		{
			name: "begin_resets_accumulator",
			input: `{"type":"item","content":"Old"}
{"type":"begin"}
{"type":"item","content":"New"}
{"type":"end"}
`,
			wantEvents: []bot.StreamEvent{
				{Type: bot.EventMessageChunk, Content: "Old"},
				{Type: bot.EventMessageChunk, Content: "New"},
				{Type: bot.EventMessage, Content: "New"},
			},
		},
		{
			name: "unknown_event_types_ignored",
			input: `{"type":"unknown","content":"Ignored"}
{"type":"item","content":"Valid"}
{"type":"weird","data":"Also ignored"}
`,
			wantEvents: []bot.StreamEvent{
				{Type: bot.EventMessageChunk, Content: "Valid"},
			},
		},
		{
			name: "large_content",
			input: `{"type":"item","content":"` + strings.Repeat("A", 100000) + `"}
`,
			wantEvents: []bot.StreamEvent{
				{Type: bot.EventMessageChunk, Content: strings.Repeat("A", 100000)},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parser := bot.NewN8nParser(testutil.NewTestLogger(t))
			reader := strings.NewReader(tt.input)

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			eventCh, err := parser.Parse(ctx, reader)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}

			// Collect all events
			var events []bot.StreamEvent
			for event := range eventCh {
				events = append(events, event)
			}

			// Check count if specified
			if tt.wantEventCnt > 0 {
				if len(events) != tt.wantEventCnt {
					t.Errorf("got %d events, want %d", len(events), tt.wantEventCnt)
				}
				return
			}

			// Check exact events
			if len(events) != len(tt.wantEvents) {
				t.Fatalf("got %d events, want %d\nGot: %+v\nWant: %+v",
					len(events), len(tt.wantEvents), events, tt.wantEvents)
			}

			for i, want := range tt.wantEvents {
				got := events[i]
				if got.Type != want.Type {
					t.Errorf("event[%d].Type = %v, want %v", i, got.Type, want.Type)
				}
				if got.Content != want.Content {
					t.Errorf("event[%d].Content = %q, want %q", i, got.Content, want.Content)
				}
			}
		})
	}
}

func TestN8nParser_ContextCancellation(t *testing.T) {
	parser := bot.NewN8nParser(testutil.NewTestLogger(t))

	// Create a slow-reading stream
	input := `{"type":"item","content":"First"}
{"type":"item","content":"Second"}
{"type":"item","content":"Third"}
`
	reader := strings.NewReader(input)

	ctx, cancel := context.WithCancel(context.Background())

	eventCh, err := parser.Parse(ctx, reader)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	// Read first event
	event := <-eventCh
	if event.Content != "First" {
		t.Errorf("first event.Content = %q, want %q", event.Content, "First")
	}

	// Cancel context
	cancel()

	// Channel should close soon (parser respects context)
	timeout := time.After(1 * time.Second)
	eventCount := 1
	for {
		select {
		case _, ok := <-eventCh:
			if !ok {
				// Channel closed - good!
				if eventCount > 3 {
					t.Errorf("got %d events after cancel, expected <= 3", eventCount)
				}
				return
			}
			eventCount++
		case <-timeout:
			t.Fatal("parser did not stop after context cancellation")
		}
	}
}
