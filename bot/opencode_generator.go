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
	"net/url"
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

	activeMu      sync.Mutex
	activeCancel  context.CancelFunc
	activeCancelN uint64
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

func (c *openCodeConversation) setActiveCancel(cancel context.CancelFunc) uint64 {
	c.activeMu.Lock()
	c.activeCancelN++
	seq := c.activeCancelN
	c.activeCancel = cancel
	c.activeMu.Unlock()
	return seq
}

func (c *openCodeConversation) clearActiveCancel(seq uint64) {
	c.activeMu.Lock()
	if c.activeCancelN == seq {
		c.activeCancel = nil
	}
	c.activeMu.Unlock()
}

func (c *openCodeConversation) cancelActive() {
	c.activeMu.Lock()
	cancel := c.activeCancel
	c.activeCancel = nil
	c.activeMu.Unlock()
	if cancel != nil {
		cancel()
	}
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

func (g *OpenCodeGenerator) ResetSession(ctx context.Context, message Message) error {
	if ctx == nil {
		ctx = context.Background()
	}

	conversationKey := openCodeConversationKey(message)
	conv := g.conversation(conversationKey)
	sessionID := conv.getSessionID()
	conv.setSessionID("")
	conv.cancelActive()

	if sessionID == "" {
		g.logger.InfoContext(
			ctx,
			"opencode: session reset requested with no active session",
			"conversation_key",
			conversationKey,
		)
		return nil
	}

	g.logger.InfoContext(
		ctx,
		"opencode: resetting session",
		"conversation_key",
		conversationKey,
		"session_id",
		sessionID,
	)

	cleanupCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	if err := g.abortAndDeleteSessionTree(cleanupCtx, sessionID); err != nil {
		return fmt.Errorf("reset session %s: %w", sessionID, err)
	}
	return nil
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
	renderCh, err := g.GenerateRenderStream(ctx, message, history)
	if err != nil {
		return nil, err
	}
	outCh := make(chan string, 10)
	go func() {
		defer close(outCh)
		partText := make(map[string]string)
		for ev := range renderCh {
			switch ev.Type {
			case RenderEventTextDelta:
				if ev.Text == "" {
					continue
				}
				partText[ev.PartID] = partText[ev.PartID] + ev.Text
				if !sendChunk(ctx, outCh, ev.Text) {
					return
				}
			case RenderEventTextSet:
				prev := partText[ev.PartID]
				next := ev.Text
				partText[ev.PartID] = next
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
					return
				}
			}
		}
	}()
	return outCh, nil
}

func (g *OpenCodeGenerator) GenerateRenderStream(
	ctx context.Context,
	message Message,
	history []Message,
) (<-chan RenderEvent, error) {
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

	postPromptWithStream := func(sessionID, prompt string) (<-chan RenderEvent, error) {
		g.logger.DebugContext(ctx, "opencode: opening event stream", "session_id", sessionID)
		resp, err := g.openEventStream(ctx)
		if err != nil {
			return nil, err
		}
		g.logger.DebugContext(ctx, "opencode: event stream opened", "session_id", sessionID)

		outCh := make(chan RenderEvent, 32)
		done := make(chan struct{})
		streamCtx, cancel := context.WithCancel(ctx)
		cancelSeq := conv.setActiveCancel(cancel)

		state := &openCodeStreamState{
			assistantMsgIDs:  make(map[string]struct{}),
			partText:         make(map[string]string),
			pendingParts:     make(map[string]openCodePendingPart),
			pendingToolParts: make(map[string]openCodeToolPartUpdate),
			seenToolCalls:    make(map[string]struct{}),
		}

		go func() {
			defer close(done)
			defer close(outCh)
			defer cancel()
			defer conv.clearActiveCancel(cancelSeq)
			defer unlockConv()
			defer func() {
				// Event stream can stay open indefinitely; close directly.
				if err := resp.Body.Close(); err != nil {
					g.logger.Warn("failed to close opencode event stream", "error", err)
				}
			}()

			if err := g.consumeRenderEvents(streamCtx, resp.Body, sessionID, state, outCh); err != nil {
				if !errors.Is(err, context.Canceled) {
					g.logger.Error("opencode stream ended with error", "error", err)
				}
			}
		}()

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

	prompt := buildPrompt(message, history, includeHistory)
	outCh, err := postPromptWithStream(sessionID, prompt)
	if err == nil {
		return outCh, nil
	}
	if !isStaleSessionPromptError(err) {
		unlockConv()
		return nil, err
	}

	g.logger.WarnContext(
		ctx,
		"opencode: stale session detected, recreating and retrying",
		"session_id",
		sessionID,
		"conversation_key",
		conversationKey,
	)
	conv.clearSessionIDIfMatch(sessionID)

	sessionTitle := titleForMessage(message.Text)
	newSessionID, createErr := g.createSession(ctx, sessionTitle)
	if createErr != nil {
		unlockConv()
		return nil, createErr
	}
	conv.setSessionID(newSessionID)
	includeHistory = true
	retryPrompt := buildPrompt(message, history, includeHistory)

	outCh, err = postPromptWithStream(newSessionID, retryPrompt)
	if err != nil {
		if isStaleSessionPromptError(err) {
			conv.clearSessionIDIfMatch(newSessionID)
		}
		unlockConv()
		return nil, err
	}

	return outCh, nil
}

func (g *OpenCodeGenerator) hasSession(message Message) bool {
	key := openCodeConversationKey(message)
	conv := g.conversation(key)
	return conv.getSessionID() != ""
}

func (g *OpenCodeGenerator) abortAndDeleteSessionTree(ctx context.Context, sessionID string) error {
	ids, err := g.collectSessionTreeIDs(ctx, sessionID, map[string]struct{}{})
	if err != nil {
		return err
	}

	var errs []error
	for _, id := range ids {
		if err := g.abortSession(ctx, id); err != nil {
			errs = append(errs, err)
		}
	}

	if err := g.waitForSessionNotBusy(ctx, sessionID, 2*time.Second); err != nil {
		errs = append(errs, err)
	}

	for i := len(ids) - 1; i >= 0; i-- {
		if err := g.deleteSession(ctx, ids[i]); err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func (g *OpenCodeGenerator) collectSessionTreeIDs(
	ctx context.Context,
	sessionID string,
	seen map[string]struct{},
) ([]string, error) {
	if _, ok := seen[sessionID]; ok {
		return nil, nil
	}
	seen[sessionID] = struct{}{}

	ids := []string{sessionID}
	children, err := g.listChildSessions(ctx, sessionID)
	if err != nil {
		return ids, err
	}
	for _, childID := range children {
		childIDs, err := g.collectSessionTreeIDs(ctx, childID, seen)
		ids = append(ids, childIDs...)
		if err != nil {
			return ids, err
		}
	}
	return ids, nil
}

func (g *OpenCodeGenerator) abortSession(ctx context.Context, sessionID string) error {
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		g.baseURL+"/session/"+url.PathEscape(sessionID)+"/abort",
		nil,
	)
	if err != nil {
		return err
	}
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

	if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		return nil
	}
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	return &openCodeHTTPError{op: "session abort", status: resp.StatusCode, body: string(b)}
}

func (g *OpenCodeGenerator) deleteSession(ctx context.Context, sessionID string) error {
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodDelete,
		g.baseURL+"/session/"+url.PathEscape(sessionID),
		nil,
	)
	if err != nil {
		return err
	}
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

	if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		return nil
	}
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	return &openCodeHTTPError{op: "session delete", status: resp.StatusCode, body: string(b)}
}

func (g *OpenCodeGenerator) listChildSessions(
	ctx context.Context,
	sessionID string,
) ([]string, error) {
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		g.baseURL+"/session/"+url.PathEscape(sessionID)+"/children",
		nil,
	)
	if err != nil {
		return nil, err
	}
	if g.auth != "" {
		req.Header.Set("Authorization", g.auth)
	}

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &openCodeHTTPError{
			op:     "session children",
			status: resp.StatusCode,
			body:   string(b),
		}
	}

	var decoded any
	if err := json.Unmarshal(b, &decoded); err != nil {
		return nil, err
	}
	childSet := map[string]struct{}{}
	collectSessionIDs(decoded, childSet)
	delete(childSet, sessionID)
	children := make([]string, 0, len(childSet))
	for id := range childSet {
		children = append(children, id)
	}
	return children, nil
}

func (g *OpenCodeGenerator) waitForSessionNotBusy(
	ctx context.Context,
	sessionID string,
	maxWait time.Duration,
) error {
	if maxWait <= 0 {
		return nil
	}
	deadline := time.Now().Add(maxWait)
	for {
		busy, known, err := g.sessionBusy(ctx, sessionID)
		if err != nil {
			return err
		}
		if !known || !busy {
			return nil
		}
		if time.Now().After(deadline) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (g *OpenCodeGenerator) sessionBusy(ctx context.Context, sessionID string) (bool, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.baseURL+"/session/status", nil)
	if err != nil {
		return false, false, err
	}
	if g.auth != "" {
		req.Header.Set("Authorization", g.auth)
	}

	resp, err := g.client.Do(req)
	if err != nil {
		return false, false, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return false, false, &openCodeHTTPError{
			op:     "session status",
			status: resp.StatusCode,
			body:   string(b),
		}
	}

	var decoded any
	if err := json.Unmarshal(b, &decoded); err != nil {
		return false, false, err
	}
	status, ok := findSessionStatus(decoded, sessionID)
	if !ok {
		return false, false, nil
	}
	return strings.EqualFold(status, "busy"), true, nil
}

func collectSessionIDs(v any, out map[string]struct{}) {
	switch x := v.(type) {
	case map[string]any:
		if id, ok := x["id"].(string); ok && id != "" {
			out[id] = struct{}{}
		}
		for _, val := range x {
			collectSessionIDs(val, out)
		}
	case []any:
		for _, item := range x {
			collectSessionIDs(item, out)
		}
	}
}

func findSessionStatus(v any, sessionID string) (string, bool) {
	switch x := v.(type) {
	case map[string]any:
		if id, ok := x["id"].(string); ok && id == sessionID {
			if status, ok := x["status"].(string); ok {
				return status, true
			}
		}
		if statusVal, ok := x[sessionID]; ok {
			switch s := statusVal.(type) {
			case string:
				return s, true
			case map[string]any:
				if status, ok := s["status"].(string); ok {
					return status, true
				}
			}
		}
		for _, val := range x {
			if status, ok := findSessionStatus(val, sessionID); ok {
				return status, true
			}
		}
	case []any:
		for _, item := range x {
			if status, ok := findSessionStatus(item, sessionID); ok {
				return status, true
			}
		}
	}
	return "", false
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
	assistantMsgIDs  map[string]struct{}
	partText         map[string]string              // partID -> last full text
	pendingParts     map[string]openCodePendingPart // partID -> last seen data for unknown message role
	pendingToolParts map[string]openCodeToolPartUpdate
	seenToolCalls    map[string]struct{}
}

type openCodePendingPart struct {
	MessageID string
	Text      string
}

type openCodeHTTPError struct {
	op     string
	status int
	body   string
}

func (e *openCodeHTTPError) Error() string {
	return fmt.Sprintf("opencode %s: http %d: %s", e.op, e.status, strings.TrimSpace(e.body))
}

func isStaleSessionPromptError(err error) bool {
	var httpErr *openCodeHTTPError
	if !errors.As(err, &httpErr) {
		return false
	}
	if httpErr.op != "prompt_async" {
		return false
	}
	if httpErr.status == http.StatusNotFound || httpErr.status == http.StatusGone {
		return true
	}
	if httpErr.status != http.StatusBadRequest {
		return false
	}
	body := strings.ToLower(httpErr.body)
	if !strings.Contains(body, "session") {
		return false
	}
	if strings.Contains(body, "not found") {
		return true
	}
	if strings.Contains(body, "unknown") {
		return true
	}
	if strings.Contains(body, "invalid") {
		return true
	}
	if strings.Contains(body, "does not exist") {
		return true
	}
	return false
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
		return "", &openCodeHTTPError{
			op:     "create session",
			status: resp.StatusCode,
			body:   string(respBody),
		}
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
		return nil, &openCodeHTTPError{op: "event stream", status: resp.StatusCode, body: string(b)}
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
		return &openCodeHTTPError{op: "prompt_async", status: resp.StatusCode, body: string(b)}
	}
	return nil
}

func (g *OpenCodeGenerator) consumeRenderEvents(
	ctx context.Context,
	body io.Reader,
	sessionID string,
	state *openCodeStreamState,
	outCh chan<- RenderEvent,
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

		typ, sid := eventTypeAndSessionID(payload)
		if typ == "" {
			continue
		}
		if typ != "server.connected" && sid != sessionID {
			continue
		}

		g.logger.DebugContext(
			ctx,
			"opencode: render event received",
			"type",
			typ,
			"session_id",
			sid,
		)

		if typ == "session.idle" && sid == sessionID {
			_ = sendRenderEvent(ctx, outCh, RenderEvent{Type: RenderEventDone})
			g.logger.DebugContext(ctx, "opencode: session idle", "session_id", sessionID)
			return nil
		}

		if typ == "permission.asked" {
			reqID, ok := permissionRequestIDFromAsked(payload)
			if !ok {
				continue
			}
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

				for partID, pp := range state.pendingParts {
					if pp.MessageID != assistantID || pp.Text == "" {
						continue
					}
					state.partText[partID] = pp.Text
					if !sendRenderEvent(ctx, outCh, RenderEvent{
						Type:      RenderEventTextSet,
						MessageID: pp.MessageID,
						PartID:    partID,
						Text:      pp.Text,
					}) {
						return ctx.Err()
					}
					delete(state.pendingParts, partID)
				}

				for partID, tu := range state.pendingToolParts {
					if tu.MessageID != assistantID {
						continue
					}
					ev := mapToolUpdateToRenderEvent(state, tu)
					if !sendRenderEvent(ctx, outCh, ev) {
						return ctx.Err()
					}
					delete(state.pendingToolParts, partID)
				}
			}
			continue
		}

		if typ == "message.part.updated" {
			upd, ok := toolPartUpdateFromEvent(payload)
			if ok && upd.SessionID == sessionID {
				if _, isAssistant := state.assistantMsgIDs[upd.MessageID]; !isAssistant {
					state.pendingToolParts[upd.PartID] = upd
					continue
				}
				ev := mapToolUpdateToRenderEvent(state, upd)
				g.logger.DebugContext(
					ctx,
					"opencode: tool mapped",
					"tool",
					upd.Tool,
					"status",
					upd.Status,
					"call_id",
					upd.CallID,
				)
				if !sendRenderEvent(ctx, outCh, ev) {
					return ctx.Err()
				}
				continue
			}
		}

		if typ == "message.part.updated" || typ == "message.part.delta" {
			txt, ok := textPartFromEvent(payload)
			if !ok || txt.SessionID != sessionID {
				continue
			}

			if _, isAssistant := state.assistantMsgIDs[txt.MessageID]; !isAssistant {
				pp := state.pendingParts[txt.PartID]
				pp.MessageID = txt.MessageID
				if txt.IsDelta {
					pp.Text += txt.Text
				} else {
					pp.Text = txt.Text
				}
				state.pendingParts[txt.PartID] = pp
				continue
			}

			if txt.IsDelta {
				state.partText[txt.PartID] = state.partText[txt.PartID] + txt.Text
				if txt.Text == "" {
					continue
				}
				if !sendRenderEvent(ctx, outCh, RenderEvent{
					Type:      RenderEventTextDelta,
					MessageID: txt.MessageID,
					PartID:    txt.PartID,
					Text:      txt.Text,
				}) {
					return ctx.Err()
				}
				continue
			}

			prev := state.partText[txt.PartID]
			next := txt.Text
			state.partText[txt.PartID] = next
			if next == "" {
				continue
			}
			if prev != "" && strings.HasPrefix(next, prev) {
				delta := next[len(prev):]
				if delta == "" {
					continue
				}
				if !sendRenderEvent(ctx, outCh, RenderEvent{
					Type:      RenderEventTextDelta,
					MessageID: txt.MessageID,
					PartID:    txt.PartID,
					Text:      delta,
				}) {
					return ctx.Err()
				}
				continue
			}

			if !sendRenderEvent(ctx, outCh, RenderEvent{
				Type:      RenderEventTextSet,
				MessageID: txt.MessageID,
				PartID:    txt.PartID,
				Text:      next,
			}) {
				return ctx.Err()
			}
		}
	}
}

func sendRenderEvent(ctx context.Context, ch chan<- RenderEvent, event RenderEvent) bool {
	select {
	case ch <- event:
		return true
	case <-ctx.Done():
		return false
	}
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
	IsDelta   bool
}

type openCodeToolPartUpdate struct {
	SessionID string
	MessageID string
	PartID    string
	CallID    string
	Tool      string
	Status    string
	Title     string
	Command   string
	Path      string
	Pattern   string
	Query     string
	OutputLen int
	ExitCode  *int
	Truncated *bool
	ErrText   string
}

func textPartFromEvent(payload string) (openCodeTextPartUpdate, bool) {
	var evt map[string]any
	if err := json.Unmarshal([]byte(payload), &evt); err != nil {
		return openCodeTextPartUpdate{}, false
	}

	typ, _ := evt["type"].(string)
	props, _ := evt["properties"].(map[string]any)
	if props == nil {
		return openCodeTextPartUpdate{}, false
	}

	if typ == "message.part.delta" {
		field, _ := props["field"].(string)
		if field != "text" {
			return openCodeTextPartUpdate{}, false
		}
		delta, _ := props["delta"].(string)
		sessionID, _ := props["sessionID"].(string)
		messageID, _ := props["messageID"].(string)
		partID, _ := props["partID"].(string)
		if partID == "" || messageID == "" {
			return openCodeTextPartUpdate{}, false
		}
		return openCodeTextPartUpdate{
			SessionID: sessionID,
			MessageID: messageID,
			PartID:    partID,
			Text:      delta,
			IsDelta:   true,
		}, true
	}

	part, _ := props["part"].(map[string]any)
	if part == nil {
		return openCodeTextPartUpdate{}, false
	}
	if partType, _ := part["type"].(string); partType != "text" {
		return openCodeTextPartUpdate{}, false
	}
	partID, _ := part["id"].(string)
	messageID, _ := part["messageID"].(string)
	if partID == "" || messageID == "" {
		return openCodeTextPartUpdate{}, false
	}
	sessionID, _ := part["sessionID"].(string)
	text, _ := part["text"].(string)
	return openCodeTextPartUpdate{
		SessionID: sessionID,
		MessageID: messageID,
		PartID:    partID,
		Text:      text,
		IsDelta:   false,
	}, true
}

func textPartUpdateFromEvent(payload string) (openCodeTextPartUpdate, bool) {
	upd, ok := textPartFromEvent(payload)
	if !ok || upd.IsDelta {
		return openCodeTextPartUpdate{}, false
	}
	return upd, true
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
	if v, _ := part["id"].(string); v != "" {
		upd.PartID = v
	}
	if v, _ := part["sessionID"].(string); v != "" {
		upd.SessionID = v
	}
	if v, _ := part["messageID"].(string); v != "" {
		upd.MessageID = v
	}
	if v, _ := part["callID"].(string); v != "" {
		upd.CallID = v
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
	if v, _ := state["title"].(string); v != "" {
		upd.Title = v
	}
	input, _ := state["input"].(map[string]any)
	if input != nil {
		if v, _ := input["command"].(string); v != "" {
			upd.Command = v
		}
		if v, _ := input["path"].(string); v != "" {
			upd.Path = v
		}
		if v, _ := input["pattern"].(string); v != "" {
			upd.Pattern = v
		}
		if v, _ := input["query"].(string); v != "" {
			upd.Query = v
		}
	}
	if v, _ := state["error"].(string); v != "" {
		upd.ErrText = v
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

func toolCallKey(upd openCodeToolPartUpdate) string {
	if upd.CallID != "" {
		return upd.CallID
	}
	if upd.PartID != "" {
		return upd.PartID
	}
	if upd.Tool != "" {
		return upd.Tool
	}
	return "tool"
}

func mapToolUpdateToRenderEvent(
	state *openCodeStreamState,
	upd openCodeToolPartUpdate,
) RenderEvent {
	key := toolCallKey(upd)
	status := strings.ToLower(strings.TrimSpace(upd.Status))
	eventType := RenderEventToolUpdate
	_, seen := state.seenToolCalls[key]

	switch status {
	case "pending", "running":
		if !seen {
			eventType = RenderEventToolStart
			state.seenToolCalls[key] = struct{}{}
		}
	case "completed":
		eventType = RenderEventToolFinish
		state.seenToolCalls[key] = struct{}{}
	case "error":
		eventType = RenderEventToolError
		state.seenToolCalls[key] = struct{}{}
	default:
		if !seen {
			eventType = RenderEventToolStart
			state.seenToolCalls[key] = struct{}{}
		}
	}

	return RenderEvent{
		Type:      eventType,
		MessageID: upd.MessageID,
		PartID:    upd.PartID,
		CallID:    upd.CallID,
		ToolName:  upd.Tool,
		Status:    upd.Status,
		Title:     upd.Title,
		Command:   upd.Command,
		Path:      upd.Path,
		Pattern:   upd.Pattern,
		Query:     upd.Query,
		ExitCode:  upd.ExitCode,
		ErrText:   upd.ErrText,
	}
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
		return &openCodeHTTPError{op: "permission reply", status: resp.StatusCode, body: string(b)}
	}
	return nil
}

var (
	_ StreamingGenerator       = (*OpenCodeGenerator)(nil)
	_ RenderStreamingGenerator = (*OpenCodeGenerator)(nil)
	_ HistoryAwareGenerator    = (*OpenCodeGenerator)(nil)
	_ SessionResetter          = (*OpenCodeGenerator)(nil)
)
