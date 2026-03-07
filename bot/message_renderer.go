package bot

import "strings"

// MessageRenderer renders a full Rocket.Chat message from structured events.
type MessageRenderer interface {
	Apply(event RenderEvent) bool
	Render() string
}

type toolCallView struct {
	ID      string
	Summary string
	Status  string
	Failed  bool
}

// ProgressiveRenderer keeps tool lines plus assistant answer text.
type ProgressiveRenderer struct {
	tools     []toolCallView
	toolIndex map[string]int
	answer    string
	done      bool
}

func NewProgressiveRenderer() *ProgressiveRenderer {
	return &ProgressiveRenderer{
		toolIndex: make(map[string]int),
	}
}

func (r *ProgressiveRenderer) Apply(event RenderEvent) bool {
	switch event.Type {
	case RenderEventTextDelta:
		if event.Text == "" {
			return false
		}
		r.answer += event.Text
		return true
	case RenderEventTextSet:
		if event.Text == r.answer {
			return false
		}
		r.answer = event.Text
		return true
	case RenderEventToolStart, RenderEventToolUpdate, RenderEventToolFinish, RenderEventToolError:
		return r.upsertTool(event)
	case RenderEventDone:
		if r.done {
			return false
		}
		r.done = true
		return false
	default:
		return false
	}
}

func (r *ProgressiveRenderer) upsertTool(event RenderEvent) bool {
	id := strings.TrimSpace(event.CallID)
	if id == "" {
		id = strings.TrimSpace(event.PartID)
	}
	if id == "" {
		id = strings.TrimSpace(event.ToolName)
	}
	if id == "" {
		id = "tool"
	}

	summary := summarizeToolEvent(event)
	failed := event.Type == RenderEventToolError || strings.EqualFold(event.Status, "error")

	if idx, ok := r.toolIndex[id]; ok {
		changed := false
		if summary != "" && r.tools[idx].Summary != summary {
			r.tools[idx].Summary = summary
			changed = true
		}
		if event.Status != "" && r.tools[idx].Status != event.Status {
			r.tools[idx].Status = event.Status
			changed = true
		}
		if r.tools[idx].Failed != failed {
			r.tools[idx].Failed = failed
			changed = true
		}
		return changed
	}

	r.toolIndex[id] = len(r.tools)
	r.tools = append(r.tools, toolCallView{
		ID:      id,
		Summary: summary,
		Status:  event.Status,
		Failed:  failed,
	})
	return true
}

func (r *ProgressiveRenderer) Render() string {
	var b strings.Builder
	for _, t := range r.tools {
		if strings.TrimSpace(t.Summary) == "" {
			continue
		}
		b.WriteString("> `")
		b.WriteString(t.Summary)
		b.WriteString("`\n")
	}

	body := strings.TrimSpace(r.answer)
	if body == "" {
		body = "..."
	}

	if b.Len() > 0 {
		b.WriteString("\n")
	}
	b.WriteString(body)
	return b.String()
}
