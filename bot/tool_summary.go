package bot

import "strings"

func capitalizeASCII(s string) string {
	if s == "" {
		return s
	}
	b := []byte(s)
	if b[0] >= 'a' && b[0] <= 'z' {
		b[0] = b[0] - ('a' - 'A')
	}
	return string(b)
}

func summarizeToolEvent(ev RenderEvent) string {
	tool := strings.TrimSpace(ev.ToolName)
	status := strings.ToLower(strings.TrimSpace(ev.Status))

	if ev.Type == RenderEventToolError || status == "error" {
		switch tool {
		case "read":
			if ev.Path != "" {
				return "Read failed: " + ev.Path
			}
			return "Read failed"
		case "bash":
			if ev.Command != "" {
				return "Bash failed: " + ev.Command
			}
			return "Bash failed"
		default:
			if tool != "" {
				return capitalizeASCII(tool) + " failed"
			}
			return "Tool failed"
		}
	}

	switch tool {
	case "read":
		if ev.Path != "" {
			return "Reading " + ev.Path
		}
		if ev.Title != "" {
			return ev.Title
		}
		return "Reading file"
	case "bash":
		if ev.Command != "" {
			return "Running bash: " + ev.Command
		}
		if ev.Title != "" {
			return ev.Title
		}
		return "Running bash"
	case "grep":
		if ev.Query != "" {
			return "Searching for " + ev.Query
		}
		if ev.Pattern != "" {
			return "Searching for " + ev.Pattern
		}
		return "Searching"
	case "glob":
		if ev.Pattern != "" {
			return "Matching files: " + ev.Pattern
		}
		return "Looking for matching files"
	case "apply_patch":
		return "Editing files"
	default:
		if ev.Title != "" {
			return ev.Title
		}
		if tool != "" {
			return "Using " + tool
		}
		return "Using tool"
	}
}
