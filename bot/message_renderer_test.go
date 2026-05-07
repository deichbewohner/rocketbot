package bot

import (
	"strings"
	"testing"
)

func TestProgressiveRenderer_InitialRenderIsPlaceholder(t *testing.T) {
	r := NewProgressiveRenderer("detailed")
	if got := r.Render(); got != "..." {
		t.Fatalf("Render() = %q, want %q", got, "...")
	}
}

func TestProgressiveRenderer_ToolLineAndAnswerFlow(t *testing.T) {
	r := NewProgressiveRenderer("detailed")

	changed := r.Apply(RenderEvent{
		Type:     RenderEventToolStart,
		CallID:   "call_1",
		ToolName: "read",
		Path:     "bot/client.go",
		Status:   "running",
	})
	if !changed {
		t.Fatal("expected tool start to change renderer")
	}

	got := r.Render()
	if !strings.Contains(got, "> `Reading bot/client.go`") {
		t.Fatalf("Render() missing tool line: %q", got)
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("Render() should end with placeholder: %q", got)
	}

	changed = r.Apply(RenderEvent{Type: RenderEventTextDelta, PartID: "p1", Text: "Hello"})
	if !changed {
		t.Fatal("expected text delta to change renderer")
	}

	got = r.Render()
	if strings.Contains(got, "\n\n...") {
		t.Fatalf("placeholder should be replaced by answer: %q", got)
	}
	if !strings.HasSuffix(got, "Hello") {
		t.Fatalf("Render() should contain answer text: %q", got)
	}
}

func TestProgressiveRenderer_OneLinePerToolCall(t *testing.T) {
	r := NewProgressiveRenderer("detailed")

	_ = r.Apply(RenderEvent{
		Type:     RenderEventToolStart,
		CallID:   "call_1",
		ToolName: "read",
		Path:     "bot/client.go",
		Status:   "running",
	})
	_ = r.Apply(RenderEvent{
		Type:     RenderEventToolUpdate,
		CallID:   "call_1",
		ToolName: "read",
		Path:     "bot/opencode_generator.go",
		Status:   "running",
	})
	_ = r.Apply(RenderEvent{Type: RenderEventTextDelta, PartID: "p1", Text: "Done."})

	got := r.Render()
	if count := strings.Count(got, "> `"); count != 1 {
		t.Fatalf("expected one rendered tool line, got %d in %q", count, got)
	}
	if !strings.Contains(got, "Reading bot/opencode_generator.go") {
		t.Fatalf("expected tool summary update in-place, got %q", got)
	}
}

func TestProgressiveRenderer_ErrorLineIsShort(t *testing.T) {
	r := NewProgressiveRenderer("detailed")

	_ = r.Apply(RenderEvent{
		Type:     RenderEventToolStart,
		CallID:   "call_bash",
		ToolName: "bash",
		Command:  "go test ./...",
		Status:   "running",
	})
	changed := r.Apply(RenderEvent{
		Type:     RenderEventToolError,
		CallID:   "call_bash",
		ToolName: "bash",
		Command:  "go test ./...",
		Status:   "error",
		ErrText:  "very long stack trace that should not appear",
	})
	if !changed {
		t.Fatal("expected tool error to change renderer")
	}

	got := r.Render()
	if !strings.Contains(got, "Bash failed: go test ./...") {
		t.Fatalf("expected short error summary, got %q", got)
	}
	if strings.Contains(got, "stack trace") {
		t.Fatalf("render should not include full error payload: %q", got)
	}
}

func TestProgressiveRenderer_CompletedToolRemainsVisible(t *testing.T) {
	r := NewProgressiveRenderer("detailed")

	_ = r.Apply(RenderEvent{
		Type:     RenderEventToolStart,
		CallID:   "call_1",
		ToolName: "read",
		Path:     "bot/client.go",
		Status:   "running",
	})
	_ = r.Apply(RenderEvent{
		Type:     RenderEventToolFinish,
		CallID:   "call_1",
		ToolName: "read",
		Path:     "bot/client.go",
		Status:   "completed",
	})
	_ = r.Apply(RenderEvent{Type: RenderEventTextDelta, PartID: "p1", Text: "Final answer."})

	got := r.Render()
	if !strings.Contains(got, "> `Reading bot/client.go`") {
		t.Fatalf("expected completed tool line to remain visible: %q", got)
	}
	if !strings.Contains(got, "Final answer.") {
		t.Fatalf("expected final answer text: %q", got)
	}
}

func TestProgressiveRenderer_ConciseModeCountsCategories(t *testing.T) {
	r := NewProgressiveRenderer("concise")

	_ = r.Apply(RenderEvent{
		Type:     RenderEventToolStart,
		CallID:   "skill-1",
		ToolName: "read",
		Path:     "/tmp/skills/openai-docs/SKILL.md",
		Status:   "running",
	})
	_ = r.Apply(RenderEvent{
		Type:     RenderEventToolStart,
		CallID:   "read-1",
		ToolName: "read",
		Path:     "bot/client.go",
		Status:   "running",
	})
	_ = r.Apply(RenderEvent{
		Type:     RenderEventToolStart,
		CallID:   "read-2",
		ToolName: "read",
		Path:     "config/config.go",
		Status:   "running",
	})
	_ = r.Apply(RenderEvent{
		Type:     RenderEventToolStart,
		CallID:   "tool-1",
		ToolName: "bash",
		Command:  "go test ./...",
		Status:   "running",
	})
	_ = r.Apply(RenderEvent{
		Type:     RenderEventToolStart,
		CallID:   "tool-2",
		ToolName: "grep",
		Query:    "render",
		Status:   "running",
	})
	_ = r.Apply(RenderEvent{Type: RenderEventTextDelta, PartID: "p1", Text: "Answer"})

	got := r.Render()
	if !strings.Contains(got, "> `1 skill, 2 reads, 2 tools`") {
		t.Fatalf("expected concise category summary, got %q", got)
	}
	if !strings.Contains(got, "Answer") {
		t.Fatalf("expected final answer text, got %q", got)
	}
}
