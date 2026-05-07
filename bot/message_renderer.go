package bot

import (
	"strconv"
	"strings"
)

// MessageRenderer renders a full Rocket.Chat message from structured events.
type MessageRenderer interface {
	Apply(event RenderEvent) bool
	Render() string
}

type toolCallView struct {
	ID       string
	Summary  string
	Status   string
	Failed   bool
	Category string
}

// ProgressiveRenderer keeps tool lines plus assistant answer text.
type ProgressiveRenderer struct {
	tools     []toolCallView
	toolIndex map[string]int
	answer    string
	done      bool
	mode      string
}

func NewProgressiveRenderer(mode string) *ProgressiveRenderer {
	return &ProgressiveRenderer{
		toolIndex: make(map[string]int),
		mode:      normalizeRenderMode(mode),
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
	category := classifyRenderEventTool(event)

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
		if category != "" && r.tools[idx].Category != category {
			r.tools[idx].Category = category
			changed = true
		}
		return changed
	}

	r.toolIndex[id] = len(r.tools)
	r.tools = append(r.tools, toolCallView{
		ID:       id,
		Summary:  summary,
		Status:   event.Status,
		Failed:   failed,
		Category: category,
	})
	return true
}

func (r *ProgressiveRenderer) Render() string {
	var b strings.Builder
	if r.mode == "concise" {
		if summary := r.renderConciseSummary(); summary != "" {
			b.WriteString("> `")
			b.WriteString(summary)
			b.WriteString("`\n")
		}
	} else {
		for _, t := range r.tools {
			if strings.TrimSpace(t.Summary) == "" {
				continue
			}
			b.WriteString("> `")
			b.WriteString(t.Summary)
			b.WriteString("`\n")
		}
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

func (r *ProgressiveRenderer) renderConciseSummary() string {
	skills := 0
	reads := 0
	tools := 0
	for _, t := range r.tools {
		switch t.Category {
		case "skill":
			skills++
		case "read":
			reads++
		default:
			tools++
		}
	}

	parts := make([]string, 0, 3)
	if skills > 0 {
		parts = append(parts, formatCount(skills, "skill", "skills"))
	}
	if reads > 0 {
		parts = append(parts, formatCount(reads, "read", "reads"))
	}
	if tools > 0 {
		parts = append(parts, formatCount(tools, "tool", "tools"))
	}
	return strings.Join(parts, ", ")
}

func classifyRenderEventTool(event RenderEvent) string {
	if strings.EqualFold(strings.TrimSpace(event.ToolName), "read") {
		path := strings.ToLower(strings.TrimSpace(event.Path))
		title := strings.ToLower(strings.TrimSpace(event.Title))
		if strings.HasSuffix(path, "/skill.md") || path == "skill.md" || strings.Contains(path, "/skills/") ||
			strings.Contains(title, "skill") {
			return "skill"
		}
		return "read"
	}
	return "tool"
}

func formatCount(n int, singular, plural string) string {
	if n == 1 {
		return "1 " + singular
	}
	return strconv.Itoa(n) + " " + plural
}
