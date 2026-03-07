package bot

// RenderEventType represents a normalized event used by message renderers.
type RenderEventType string

const (
	RenderEventTextDelta  RenderEventType = "text_delta"
	RenderEventTextSet    RenderEventType = "text_set"
	RenderEventToolStart  RenderEventType = "tool_start"
	RenderEventToolUpdate RenderEventType = "tool_update"
	RenderEventToolFinish RenderEventType = "tool_finish"
	RenderEventToolError  RenderEventType = "tool_error"
	RenderEventDone       RenderEventType = "done"
)

// RenderEvent is a generator-agnostic, structured event for progressive
// message rendering.
type RenderEvent struct {
	Type RenderEventType

	MessageID string
	PartID    string
	CallID    string

	ToolName string
	Status   string
	Title    string

	Text string

	Command string
	Path    string
	Pattern string
	Query   string

	ExitCode *int
	ErrText  string
}
