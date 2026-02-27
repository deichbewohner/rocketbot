package bot

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

const defaultOpenCodeBaseURL = "http://127.0.0.1:4096"

// OpenCodeGenerator generates responses via an OpenCode server.
//
// It uses the OpenCode HTTP API:
// - POST /session
// - GET  /event (SSE)
// - POST /session/<id>/prompt_async
//
// Streaming output is derived from SSE events for the created session.
type OpenCodeGenerator struct {
	baseURL        string
	auth           string
	permissionMode string
	client         *http.Client
	logger         *slog.Logger

	conversationsMu sync.Mutex
	conversations   map[string]*openCodeConversation
}

type openCodeConversation struct {
	promptMu  sync.Mutex
	sessionMu sync.RWMutex
	sessionID string
}

func (c *openCodeConversation) getSessionID() string {
	c.sessionMu.RLock()
	defer c.sessionMu.RUnlock()
	return c.sessionID
}

func (c *openCodeConversation) setSessionID(sessionID string) {
	c.sessionMu.Lock()
	c.sessionID = sessionID
	c.sessionMu.Unlock()
}

func (c *openCodeConversation) clearSessionIDIfMatch(sessionID string) {
	c.sessionMu.Lock()
	if c.sessionID == sessionID {
		c.sessionID = ""
	}
	c.sessionMu.Unlock()
}

func NewOpenCodeGenerator(
	baseURL string,
	auth string,
	permissionMode string,
	client *http.Client,
	logger *slog.Logger,
) *OpenCodeGenerator {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = defaultOpenCodeBaseURL
	}
	baseURL = strings.TrimSuffix(baseURL, "/")

	permissionMode = strings.TrimSpace(permissionMode)
	if permissionMode == "" {
		permissionMode = "deny"
	}
	if permissionMode != "allow" && permissionMode != "deny" {
		permissionMode = "deny"
	}

	if client == nil {
		client = http.DefaultClient
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &OpenCodeGenerator{
		baseURL:        baseURL,
		auth:           auth,
		permissionMode: permissionMode,
		client:         client,
		logger:         logger,
		conversations:  make(map[string]*openCodeConversation),
	}
}

func (g *OpenCodeGenerator) HistoryLimit(message Message) int {
	if g.hasSession(message) {
		return 0
	}
	return 10
}

func (g *OpenCodeGenerator) GenerateResponse(
	ctx context.Context,
	message Message,
	history []Message,
) (string, error) {
	ch, err := g.GenerateResponseStream(ctx, message, history)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for chunk := range ch {
		b.WriteString(chunk)
	}
	return b.String(), nil
}

func (g *OpenCodeGenerator) GenerateResponseStream(
	ctx context.Context,
	message Message,
	history []Message,
) (<-chan string, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	start := time.Now()
	conversationKey := openCodeConversationKey(message)
	conv := g.conversation(conversationKey)
	g.logger.DebugContext(
		ctx,
		"opencode: waiting for conversation prompt lock",
		"conversation_key",
		conversationKey,
	)
	lockStart := time.Now()
	conv.promptMu.Lock()
	g.logger.DebugContext(
		ctx,
		"opencode: acquired conversation prompt lock",
		"conversation_key",
		conversationKey,
		"wait",
		time.Since(lockStart),
	)
	var unlockOnce sync.Once
	unlockConv := func() {
		unlockOnce.Do(func() {
			g.logger.DebugContext(
				ctx,
				"opencode: released conversation prompt lock",
				"conversation_key",
				conversationKey,
			)
			conv.promptMu.Unlock()
		})
	}

	sessionID := conv.getSessionID()
	includeHistory := false
	if sessionID == "" {
		sessionTitle := titleForMessage(message.Text)
		g.logger.DebugContext(
			ctx,
			"opencode: creating session",
			"base_url",
			g.baseURL,
			"title",
			sessionTitle,
			"conversation_key",
			conversationKey,
		)
		var err error
		sessionID, err = g.createSession(ctx, sessionTitle)
		if err != nil {
			unlockConv()
			return nil, err
		}
		conv.setSessionID(sessionID)
		includeHistory = true
		g.logger.DebugContext(
			ctx,
			"opencode: session created",
			"session_id",
			sessionID,
			"conversation_key",
			conversationKey,
			"took",
			time.Since(start),
		)
	} else {
		g.logger.DebugContext(
			ctx,
			"opencode: reusing session",
			"session_id",
			sessionID,
			"conversation_key",
			conversationKey,
		)
	}

	g.logger.DebugContext(ctx, "opencode: opening event stream", "session_id", sessionID)
	resp, err := g.openEventStream(ctx)
	if err != nil {
		unlockConv()
		return nil, err
	}
	g.logger.DebugContext(ctx, "opencode: event stream opened", "session_id", sessionID)

	outCh := make(chan string, 10)
	done := make(chan struct{})
	streamCtx, cancel := context.WithCancel(ctx)

	state := &openCodeStreamState{
		assistantMsgIDs: make(map[string]struct{}),
		partText:        make(map[string]string),
		pendingParts:    make(map[string]openCodePendingPart),
	}

	go func() {
		defer close(done)
		defer close(outCh)
		defer cancel()
		defer unlockConv()
		defer func() {
			// Event stream can stay open indefinitely; close directly.
			if err := resp.Body.Close(); err != nil {
				g.logger.Warn("failed to close opencode event stream", "error", err)
			}
		}()

		if err := g.consumeEvents(streamCtx, resp.Body, sessionID, state, outCh); err != nil {
			if !errors.Is(err, context.Canceled) {
				g.logger.Error("opencode stream ended with error", "error", err)
			}
		}
	}()

	prompt := buildPrompt(message, history, includeHistory)
	g.logger.DebugContext(
		ctx,
		"opencode: posting prompt_async",
		"session_id",
		sessionID,
		"conversation_key",
		conversationKey,
		"include_history",
		includeHistory,
		"prompt_len",
		len(prompt),
	)
	if err := g.promptAsync(ctx, sessionID, prompt); err != nil {
		conv.clearSessionIDIfMatch(sessionID)
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			unlockConv()
		}
		return nil, err
	}
	g.logger.DebugContext(ctx, "opencode: prompt_async accepted", "session_id", sessionID)

	return outCh, nil
}

func (g *OpenCodeGenerator) hasSession(message Message) bool {
	key := openCodeConversationKey(message)
	conv := g.conversation(key)
	return conv.getSessionID() != ""
}

func (g *OpenCodeGenerator) conversation(key string) *openCodeConversation {
	g.conversationsMu.Lock()
	defer g.conversationsMu.Unlock()
	if conv, ok := g.conversations[key]; ok {
		return conv
	}
	conv := &openCodeConversation{}
	g.conversations[key] = conv
	return conv
}

type openCodeStreamState struct {
	assistantMsgIDs map[string]struct{}
	partText        map[string]string              // partID -> last full text
	pendingParts    map[string]openCodePendingPart // partID -> last seen data for unknown message role
}

type openCodePendingPart struct {
	MessageID string
	Text      string
}

func (g *OpenCodeGenerator) createSession(ctx context.Context, title string) (string, error) {
	url := g.baseURL + "/session"
	body, _ := json.Marshal(map[string]string{"title": title})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if g.auth != "" {
		req.Header.Set("Authorization", g.auth)
	}

	resp, err := g.client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf(
			"opencode create session: http %d: %s",
			resp.StatusCode,
			strings.TrimSpace(string(respBody)),
		)
	}

	var decoded struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return "", err
	}
	if decoded.ID == "" {
		return "", errors.New("opencode create session: missing id")
	}
	return decoded.ID, nil
}

func (g *OpenCodeGenerator) openEventStream(ctx context.Context) (*http.Response, error) {
	url := g.baseURL + "/event"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	if g.auth != "" {
		req.Header.Set("Authorization", g.auth)
	}

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf(
			"opencode event stream: http %d: %s",
			resp.StatusCode,
			strings.TrimSpace(string(b)),
		)
	}
	return resp, nil
}

func (g *OpenCodeGenerator) promptAsync(ctx context.Context, sessionID, prompt string) error {
	url := g.baseURL + "/session/" + sessionID + "/prompt_async"
	payload := map[string]any{
		"parts": []map[string]string{{"type": "text", "text": prompt}},
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if g.auth != "" {
		req.Header.Set("Authorization", g.auth)
	}

	resp, err := g.client.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf(
			"opencode prompt_async: http %d: %s",
			resp.StatusCode,
			strings.TrimSpace(string(b)),
		)
	}
	return nil
}

func (g *OpenCodeGenerator) consumeEvents(
	ctx context.Context,
	body io.Reader,
	sessionID string,
	state *openCodeStreamState,
	outCh chan<- string,
) error {
	reader := bufio.NewReader(body)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				g.logger.DebugContext(ctx, "opencode: event stream EOF", "session_id", sessionID)
				return nil
			}
			return err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			continue
		}
		payload, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		payload = strings.TrimSpace(payload)
		if payload == "" {
			continue
		}

		// Fast path: filter out unrelated sessions.
		typ, sid := eventTypeAndSessionID(payload)
		if typ == "" {
			continue
		}
		if typ != "server.connected" && sid != sessionID {
			continue
		}

		if typ == "session.idle" && sid == sessionID {
			g.logger.DebugContext(ctx, "opencode: session idle", "session_id", sessionID)
			return nil
		}

		if typ == "permission.asked" {
			reqID, ok := permissionRequestIDFromAsked(payload)
			if !ok {
				continue
			}
			g.logger.InfoContext(
				ctx,
				"opencode: permission asked",
				"session_id",
				sessionID,
				"request_id",
				reqID,
				"mode",
				g.permissionMode,
			)
			reply := "reject"
			msg := "Denied by rocketbot configuration"
			if g.permissionMode == "allow" {
				reply = "always"
				msg = "Approved by rocketbot configuration"
			}
			replyCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := g.replyPermission(replyCtx, reqID, reply, msg)
			cancel()
			if err != nil {
				return err
			}
			g.logger.InfoContext(
				ctx,
				"opencode: permission replied",
				"session_id",
				sessionID,
				"request_id",
				reqID,
				"reply",
				reply,
			)
			continue
		}

		if typ == "message.updated" {
			assistantID, ok := assistantMessageIDFromMessageUpdated(payload)
			if ok {
				g.logger.DebugContext(
					ctx,
					"opencode: assistant message seen",
					"session_id",
					sessionID,
					"message_id",
					assistantID,
				)
				state.assistantMsgIDs[assistantID] = struct{}{}
				// Flush any buffered parts that arrived before we knew the role.
				for partID, pp := range state.pendingParts {
					if pp.MessageID == assistantID {
						if pp.Text != "" {
							state.partText[partID] = pp.Text
							if !sendChunk(ctx, outCh, pp.Text) {
								return ctx.Err()
							}
						}
						delete(state.pendingParts, partID)
					}
				}
			}
			continue
		}

		if typ == "message.part.updated" {
			upd, ok := toolPartUpdateFromEvent(payload)
			if ok && upd.SessionID == sessionID {
				attrs := []any{
					"session_id", sessionID,
					"tool", upd.Tool,
					"status", upd.Status,
				}
				if upd.Command != "" {
					attrs = append(attrs, "command", upd.Command)
				}
				if upd.ExitCode != nil {
					attrs = append(attrs, "exit", *upd.ExitCode)
				}
				if upd.Truncated != nil {
					attrs = append(attrs, "truncated", *upd.Truncated)
				}
				if upd.OutputLen > 0 {
					attrs = append(attrs, "output_len", upd.OutputLen)
				}
				g.logger.DebugContext(ctx, "opencode: tool update", attrs...)
				continue
			}
		}

		if typ == "message.part.updated" || typ == "message.part.delta" {
			upd, ok := textPartUpdateFromEvent(payload)
			if !ok {
				continue
			}
			if upd.SessionID != sessionID {
				continue
			}

			if _, isAssistant := state.assistantMsgIDs[upd.MessageID]; !isAssistant {
				// Buffer until we know role. This avoids echoing the user's text part.
				state.pendingParts[upd.PartID] = openCodePendingPart{
					MessageID: upd.MessageID,
					Text:      upd.Text,
				}
				continue
			}

			prev := state.partText[upd.PartID]
			next := upd.Text
			state.partText[upd.PartID] = next
			if next == "" {
				continue
			}

			chunk := next
			if prev != "" && strings.HasPrefix(next, prev) {
				chunk = next[len(prev):]
			}
			if chunk == "" {
				continue
			}

			if !sendChunk(ctx, outCh, chunk) {
				return ctx.Err()
			}
			continue
		}
	}
}

func sendChunk(ctx context.Context, ch chan<- string, chunk string) bool {
	select {
	case ch <- chunk:
		return true
	case <-ctx.Done():
		return false
	}
}

func titleForMessage(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return "rocketbot"
	}
	if len(text) > 60 {
		return text[:60]
	}
	return text
}

func buildPrompt(message Message, history []Message, includeHistory bool) string {
	// Keep it simple and deterministic; only include history when bootstrapping a session.
	var b strings.Builder
	if includeHistory && len(history) > 0 {
		b.WriteString("Conversation history (oldest first):\n")
		for _, m := range history {
			role := "user"
			if m.User.Username == "" {
				role = "user"
			} else {
				role = m.User.Username
			}
			b.WriteString("- ")
			b.WriteString(role)
			b.WriteString(": ")
			b.WriteString(strings.TrimSpace(m.Text))
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	if message.User.Username != "" {
		b.WriteString(message.User.Username)
		b.WriteString(": ")
	} else {
		b.WriteString("User: ")
	}
	b.WriteString(strings.TrimSpace(message.Text))
	return b.String()
}

func openCodeConversationKey(message Message) string {
	if message.ThreadID != "" {
		return "thread:" + message.RoomID + ":" + message.ThreadID
	}
	return "room:" + message.RoomID
}

func eventTypeAndSessionID(payload string) (typ string, sessionID string) {
	var m map[string]any
	if err := json.Unmarshal([]byte(payload), &m); err != nil {
		return "", ""
	}
	if v, ok := m["type"].(string); ok {
		typ = v
	}
	props, _ := m["properties"].(map[string]any)
	if props == nil {
		return typ, ""
	}
	if v, ok := props["sessionID"].(string); ok && v != "" {
		return typ, v
	}
	if info, ok := props["info"].(map[string]any); ok {
		if v, ok := info["sessionID"].(string); ok && v != "" {
			return typ, v
		}
	}
	if part, ok := props["part"].(map[string]any); ok {
		if v, ok := part["sessionID"].(string); ok && v != "" {
			return typ, v
		}
	}
	return typ, ""
}

func assistantMessageIDFromMessageUpdated(payload string) (string, bool) {
	var evt struct {
		Type       string `json:"type"`
		Properties struct {
			Info struct {
				ID   string `json:"id"`
				Role string `json:"role"`
			} `json:"info"`
		} `json:"properties"`
	}
	if err := json.Unmarshal([]byte(payload), &evt); err != nil {
		return "", false
	}
	if evt.Type != "message.updated" {
		return "", false
	}
	if evt.Properties.Info.Role != "assistant" {
		return "", false
	}
	if evt.Properties.Info.ID == "" {
		return "", false
	}
	return evt.Properties.Info.ID, true
}

type openCodeTextPartUpdate struct {
	SessionID string
	MessageID string
	PartID    string
	Text      string
}

type openCodeToolPartUpdate struct {
	SessionID string
	Tool      string
	Status    string
	Command   string
	OutputLen int
	ExitCode  *int
	Truncated *bool
}

func textPartUpdateFromEvent(payload string) (openCodeTextPartUpdate, bool) {
	var evt struct {
		Type       string `json:"type"`
		Properties struct {
			Part struct {
				ID        string `json:"id"`
				SessionID string `json:"sessionID"`
				MessageID string `json:"messageID"`
				Type      string `json:"type"`
				Text      string `json:"text"`
			} `json:"part"`
		} `json:"properties"`
	}
	if err := json.Unmarshal([]byte(payload), &evt); err != nil {
		return openCodeTextPartUpdate{}, false
	}
	if evt.Properties.Part.Type != "text" {
		return openCodeTextPartUpdate{}, false
	}
	if evt.Properties.Part.ID == "" || evt.Properties.Part.MessageID == "" {
		return openCodeTextPartUpdate{}, false
	}
	return openCodeTextPartUpdate{
		SessionID: evt.Properties.Part.SessionID,
		MessageID: evt.Properties.Part.MessageID,
		PartID:    evt.Properties.Part.ID,
		Text:      evt.Properties.Part.Text,
	}, true
}

func toolPartUpdateFromEvent(payload string) (openCodeToolPartUpdate, bool) {
	// Tool state is loosely typed; decode as map to be resilient.
	var evt map[string]any
	if err := json.Unmarshal([]byte(payload), &evt); err != nil {
		return openCodeToolPartUpdate{}, false
	}
	if evt["type"] != "message.part.updated" {
		return openCodeToolPartUpdate{}, false
	}
	props, _ := evt["properties"].(map[string]any)
	if props == nil {
		return openCodeToolPartUpdate{}, false
	}
	part, _ := props["part"].(map[string]any)
	if part == nil {
		return openCodeToolPartUpdate{}, false
	}
	if part["type"] != "tool" {
		return openCodeToolPartUpdate{}, false
	}

	upd := openCodeToolPartUpdate{}
	if v, _ := part["sessionID"].(string); v != "" {
		upd.SessionID = v
	}
	if v, _ := part["tool"].(string); v != "" {
		upd.Tool = v
	}
	state, _ := part["state"].(map[string]any)
	if state == nil {
		return upd, true
	}
	if v, _ := state["status"].(string); v != "" {
		upd.Status = v
	}
	input, _ := state["input"].(map[string]any)
	if input != nil {
		if v, _ := input["command"].(string); v != "" {
			upd.Command = v
		}
	}
	if out, ok := state["output"].(string); ok {
		upd.OutputLen = len(out)
	}
	metadata, _ := state["metadata"].(map[string]any)
	if metadata != nil {
		if out, ok := metadata["output"].(string); ok && upd.OutputLen == 0 {
			upd.OutputLen = len(out)
		}
		if v, ok := metadata["exit"].(float64); ok {
			exit := int(v)
			upd.ExitCode = &exit
		}
		if v, ok := metadata["truncated"].(bool); ok {
			upd.Truncated = &v
		}
	}
	return upd, true
}

func permissionRequestIDFromAsked(payload string) (string, bool) {
	var evt struct {
		Type       string `json:"type"`
		Properties struct {
			ID string `json:"id"`
		} `json:"properties"`
	}
	if err := json.Unmarshal([]byte(payload), &evt); err != nil {
		return "", false
	}
	if evt.Type != "permission.asked" {
		return "", false
	}
	if evt.Properties.ID == "" {
		return "", false
	}
	return evt.Properties.ID, true
}

func (g *OpenCodeGenerator) replyPermission(
	ctx context.Context,
	requestID string,
	reply string,
	message string,
) error {
	url := g.baseURL + "/permission/" + requestID + "/reply"
	payload := map[string]any{"reply": reply}
	if message != "" {
		payload["message"] = message
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if g.auth != "" {
		req.Header.Set("Authorization", g.auth)
	}

	resp, err := g.client.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf(
			"opencode permission reply: http %d: %s",
			resp.StatusCode,
			strings.TrimSpace(string(b)),
		)
	}
	return nil
}

var (
	_ StreamingGenerator    = (*OpenCodeGenerator)(nil)
	_ HistoryAwareGenerator = (*OpenCodeGenerator)(nil)
)
