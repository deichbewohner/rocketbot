package bot

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// parseMessage extracts message metadata from DDP message data
func parseMessage(msgData map[string]interface{}) Message {
	var message Message

	// Extract room and text safely
	if rid, ok := msgData["rid"].(string); ok {
		message.RoomID = rid
	}
	if text, ok := msgData["msg"].(string); ok {
		message.Text = text
	}

	// Extract message ID
	if id, ok := msgData["_id"].(string); ok {
		message.ID = id
	}

	// Extract thread ID (tmid) if this message is part of a thread
	if tmid, ok := msgData["tmid"].(string); ok {
		message.ThreadID = tmid
	}

	// Extract timestamp (handle multiple encodings)
	if ts, ok := msgData["ts"].(map[string]interface{}); ok {
		if dateVal, ok := ts["$date"]; ok {
			if t, ok := parseDDPDate(dateVal); ok {
				message.Timestamp = t
			}
		}
	}

	// Extract user information
	if u, ok := msgData["u"].(map[string]interface{}); ok {
		if userID, ok := u["_id"].(string); ok {
			message.User.ID = userID
		}
		if username, ok := u["username"].(string); ok {
			message.User.Username = username
		}
		if name, ok := u["name"].(string); ok {
			message.User.Name = name
		}
	}

	return message
}

// parseDDPDate parses Rocket.Chat/Mongo-style $date values.
// Supports milliseconds since epoch as float64, int, int64, numeric strings,
// RFC3339/RFC3339Nano strings, and {"$numberLong":"<millis>"} objects.
func parseDDPDate(v interface{}) (time.Time, bool) {
	// Helper to build time from milliseconds
	toTime := func(ms int64) time.Time { return time.Unix(0, ms*int64(time.Millisecond)) }

	switch val := v.(type) {
	case float64:
		// JSON numbers default to float64
		return toTime(int64(val)), true
	case int64:
		return toTime(val), true
	case int:
		return toTime(int64(val)), true
	case string:
		// Try parse as integer milliseconds first
		if ms, err := parseInt64(val); err == nil {
			return toTime(ms), true
		}
		// Try RFC3339 and RFC3339Nano
		if t, err := time.Parse(time.RFC3339, val); err == nil {
			return t, true
		}
		if t, err := time.Parse(time.RFC3339Nano, val); err == nil {
			return t, true
		}
		return time.Time{}, false
	case map[string]interface{}:
		// e.g. {"$numberLong":"1690000000000"}
		if s, ok := val["$numberLong"].(string); ok {
			if ms, err := parseInt64(s); err == nil {
				return toTime(ms), true
			}
		}
		return time.Time{}, false
	default:
		return time.Time{}, false
	}
}

// parseInt64 parses base-10 integers safely.
func parseInt64(s string) (int64, error) {
	return strconv.ParseInt(s, 10, 64)
}

// parseAPIMessages converts REST API message structs to Message slice in chronological order
func parseAPIMessages(apiMessages []struct {
	ID  string `json:"_id"`
	Msg string `json:"msg"`
	Rid string `json:"rid"`
	Ts  string `json:"ts"`
	U   struct {
		ID       string `json:"_id"`
		Username string `json:"username"`
		Name     string `json:"name"`
	} `json:"u"`
},
) []Message {
	messages := make([]Message, 0, len(apiMessages))
	for _, msg := range apiMessages {
		timestamp, _ := parseDDPDate(msg.Ts)

		messages = append(messages, Message{
			ID:        msg.ID,
			Text:      msg.Msg,
			RoomID:    msg.Rid,
			Timestamp: timestamp,
			User: MessageUser{
				ID:       msg.U.ID,
				Username: msg.U.Username,
				Name:     msg.U.Name,
			},
		})
	}

	// Defensive sort: ensure ascending timestamp order regardless of API response
	sort.Slice(messages, func(i, j int) bool {
		return messages[i].Timestamp.Before(messages[j].Timestamp)
	})

	return messages
}

// shouldCreateThread checks if we should create a new thread based on command or configuration
func shouldCreateThread(message Message, threadDefault bool) bool {
	// If threadDefault is enabled, always create thread for root messages
	if threadDefault {
		return message.ThreadID == ""
	}
	// Otherwise, only create thread if /thread command is present and not already in thread
	return strings.Contains(message.Text, "/thread") && message.ThreadID == ""
}
