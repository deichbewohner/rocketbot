package bot

import "testing"

func TestSummarizeToolEvent_BashCommandTruncated(t *testing.T) {
	ev := RenderEvent{
		Type:     RenderEventToolStart,
		ToolName: "bash",
		Command:  "abcdefghijklmnopqrstuvwxyz",
	}

	got := summarizeToolEvent(ev)
	want := "Running bash: abcdefghijklmnopqrst..."
	if got != want {
		t.Fatalf("summarizeToolEvent() = %q, want %q", got, want)
	}
}

func TestSummarizeToolEvent_BashFailureCommandTruncated(t *testing.T) {
	ev := RenderEvent{
		Type:     RenderEventToolError,
		ToolName: "bash",
		Status:   "error",
		Command:  "abcdefghijklmnopqrstuvwxyz",
	}

	got := summarizeToolEvent(ev)
	want := "Bash failed: abcdefghijklmnopqrst..."
	if got != want {
		t.Fatalf("summarizeToolEvent() = %q, want %q", got, want)
	}
}
