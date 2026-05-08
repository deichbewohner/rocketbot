package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/gorilla/websocket"
	"go.opentelemetry.io/otel"
)

// wsConn abstracts WebSocket connection for testing
type wsConn interface {
	ReadJSON(v interface{}) error
	WriteJSON(v interface{}) error
	Close() error
}

// wsDialer abstracts WebSocket dialing for testing
type wsDialer interface {
	Dial(urlStr string, requestHeader map[string][]string) (wsConn, error)
}

// defaultWSDialer adapts websocket.Dialer to wsDialer interface
type defaultWSDialer struct {
	dialer *websocket.Dialer
}

func (d *defaultWSDialer) Dial(urlStr string, requestHeader map[string][]string) (wsConn, error) {
	conn, _, err := d.dialer.Dial(urlStr, requestHeader)
	return conn, err
}

// Client represents a Rocket.Chat bot client
type Client struct {
	api                 *APIClient
	username            string
	name                string
	generator           ResponseGenerator
	logger              *slog.Logger
	streamedOutput      bool
	renderMode          string
	threadDefault       bool
	activeThreadTrigger string
	statusMessage       string
	bootstrapPrompt     string
	roomPolicies        map[string]RoomPolicy

	ws                  wsConn
	wsDialer            wsDialer
	wsMu                sync.Mutex
	pending             map[string]string
	pendingMu           sync.Mutex // protects pending map
	rooms               map[string]bool
	dmRooms             map[string]bool
	activeRoomThreads   map[string]activeThreadState
	activeRoomThreadsMu sync.RWMutex
	stopChan            chan struct{}
	stopOnce            sync.Once
}

type RoomPolicy struct {
	Enabled             bool
	OpenCodeSessionDir  string
	BootstrapPrompt     string
	RenderMode          string
	ActiveThreadTrigger string
	ThreadDefault       *bool
	StreamedOutput      *bool
}

type resolvedPolicy struct {
	Scope               string
	RoomID              string
	SessionDir          string
	BootstrapPrompt     string
	RenderMode          string
	ActiveThreadTrigger string
	ThreadDefault       bool
	StreamedOutput      bool
}

type activeThreadState struct {
	LastIngestedMessageID string
}

var errStopRequested = errors.New("bot stop requested")

const stableConnectionReset = time.Minute

// NewClient creates a new Rocket.Chat bot client
func NewClient(
	baseURL, userID, token, name string,
	generator ResponseGenerator,
	streamedOutput bool,
	renderMode string,
	threadDefault bool,
	logger *slog.Logger,
	statusMessage string,
) *Client {
	api := NewAPIClient(baseURL, userID, token, nil, logger)
	return NewClientWithAPI(
		api,
		name,
		generator,
		streamedOutput,
		renderMode,
		threadDefault,
		"auto",
		logger,
		statusMessage,
		"",
		nil,
	)
}

// NewClientWithAPI creates a Client with a pre-configured APIClient.
// This constructor is primarily for testing, allowing injection of mocked HTTP clients.
func NewClientWithAPI(
	api *APIClient,
	name string,
	generator ResponseGenerator,
	streamedOutput bool,
	renderMode string,
	threadDefault bool,
	activeThreadTrigger string,
	logger *slog.Logger,
	statusMessage string,
	bootstrapPrompt string,
	roomPolicies map[string]RoomPolicy,
) *Client {
	clonedPolicies := make(map[string]RoomPolicy, len(roomPolicies))
	for roomID, policy := range roomPolicies {
		clonedPolicies[roomID] = policy
	}
	return &Client{
		api:                 api,
		name:                name,
		generator:           generator,
		logger:              logger,
		streamedOutput:      streamedOutput,
		renderMode:          normalizeRenderMode(renderMode),
		threadDefault:       threadDefault,
		activeThreadTrigger: normalizeActiveThreadTrigger(activeThreadTrigger),
		statusMessage:       statusMessage,
		bootstrapPrompt:     strings.TrimSpace(bootstrapPrompt),
		roomPolicies:        clonedPolicies,
		wsDialer:            &defaultWSDialer{dialer: websocket.DefaultDialer},
		pending:             make(map[string]string),
		rooms:               make(map[string]bool),
		dmRooms:             make(map[string]bool),
		activeRoomThreads:   make(map[string]activeThreadState),
		stopChan:            make(chan struct{}),
	}
}

func normalizeRenderMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "concise":
		return "concise"
	default:
		return "detailed"
	}
}

func roomThreadKey(roomID, threadID string) string {
	return roomID + ":" + threadID
}

func normalizeActiveThreadTrigger(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "mention_only":
		return "mention_only"
	default:
		return "auto"
	}
}

func (c *Client) isActiveRoomThread(roomID, threadID string) bool {
	if roomID == "" || threadID == "" {
		return false
	}
	c.activeRoomThreadsMu.RLock()
	defer c.activeRoomThreadsMu.RUnlock()
	_, ok := c.activeRoomThreads[roomThreadKey(roomID, threadID)]
	return ok
}

func (c *Client) getActiveRoomThreadState(roomID, threadID string) (activeThreadState, bool) {
	if roomID == "" || threadID == "" {
		return activeThreadState{}, false
	}
	c.activeRoomThreadsMu.RLock()
	defer c.activeRoomThreadsMu.RUnlock()
	state, ok := c.activeRoomThreads[roomThreadKey(roomID, threadID)]
	return state, ok
}

func (c *Client) markActiveRoomThread(roomID, threadID, lastIngestedMessageID string) {
	if roomID == "" || threadID == "" {
		return
	}
	c.activeRoomThreadsMu.Lock()
	c.activeRoomThreads[roomThreadKey(roomID, threadID)] = activeThreadState{
		LastIngestedMessageID: lastIngestedMessageID,
	}
	c.activeRoomThreadsMu.Unlock()
}

func textMentionsUsername(text, username string) bool {
	username = strings.TrimSpace(strings.ToLower(username))
	if username == "" {
		return false
	}
	for _, field := range strings.Fields(strings.ToLower(text)) {
		token := strings.Trim(field, "()[]{}<>:;,.!?\"'`")
		if token == "@"+username {
			return true
		}
	}
	return false
}

func messageMentionsUsername(msgData map[string]interface{}, username string) bool {
	username = strings.TrimSpace(strings.ToLower(username))
	if username == "" {
		return false
	}
	if mentions, ok := msgData["mentions"].([]interface{}); ok {
		for _, raw := range mentions {
			m, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			mentioned, _ := m["username"].(string)
			if strings.EqualFold(strings.TrimSpace(mentioned), username) {
				return true
			}
		}
	}
	text, _ := msgData["msg"].(string)
	return textMentionsUsername(text, username)
}

func stripLeadingUsernameMention(text, username string) string {
	username = strings.TrimSpace(strings.ToLower(username))
	if username == "" {
		return strings.TrimSpace(text)
	}

	remaining := strings.TrimLeft(text, " \t\r\n")
	mention := "@" + username
	for {
		lowerRemaining := strings.ToLower(remaining)
		if !strings.HasPrefix(lowerRemaining, mention) {
			break
		}
		remaining = remaining[len(mention):]
		remaining = strings.TrimLeft(remaining, " \t\r\n:;,-")
	}
	return strings.TrimSpace(remaining)
}

func (c *Client) shouldRespondToRoomMessage(
	message Message,
	msgData map[string]interface{},
	policy resolvedPolicy,
) bool {
	if policy.Scope != "room" {
		return true
	}
	mentioned := messageMentionsUsername(msgData, c.username)
	if message.ThreadID != "" {
		if !c.isActiveRoomThread(message.RoomID, message.ThreadID) {
			return mentioned
		}
		if policy.ActiveThreadTrigger == "mention_only" {
			return mentioned
		}
		return true
	}
	return mentioned
}

func (c *Client) resolvePolicy(roomID string) (resolvedPolicy, bool) {
	base := resolvedPolicy{
		RoomID:              roomID,
		SessionDir:          "",
		BootstrapPrompt:     c.bootstrapPrompt,
		RenderMode:          c.renderMode,
		ActiveThreadTrigger: c.activeThreadTrigger,
		ThreadDefault:       c.threadDefault,
		StreamedOutput:      c.streamedOutput,
	}

	if c.dmRooms[roomID] {
		base.Scope = "dm"
		return base, true
	}

	policy, ok := c.roomPolicies[roomID]
	if !ok || !policy.Enabled {
		return resolvedPolicy{}, false
	}

	base.Scope = "room"
	if policy.OpenCodeSessionDir != "" {
		base.SessionDir = policy.OpenCodeSessionDir
	}
	if policy.BootstrapPrompt != "" {
		base.BootstrapPrompt = policy.BootstrapPrompt
	}
	if policy.RenderMode != "" {
		base.RenderMode = normalizeRenderMode(policy.RenderMode)
	}
	if policy.ActiveThreadTrigger != "" {
		base.ActiveThreadTrigger = normalizeActiveThreadTrigger(policy.ActiveThreadTrigger)
	}
	if policy.ThreadDefault != nil {
		base.ThreadDefault = *policy.ThreadDefault
	}
	if policy.StreamedOutput != nil {
		base.StreamedOutput = *policy.StreamedOutput
	}
	return base, true
}

func (c *Client) prepareMessageForGenerator(
	ctx context.Context,
	message Message,
	policy resolvedPolicy,
) Message {
	if policy.Scope != "room" || message.ThreadID == "" || policy.ActiveThreadTrigger != "mention_only" {
		return message
	}

	state, ok := c.getActiveRoomThreadState(message.RoomID, message.ThreadID)
	if !ok {
		return message
	}

	threadHistory := c.api.FetchThreadHistory(ctx, message.ThreadID, 100)
	if len(threadHistory) == 0 {
		return message
	}

	missed := collectMissedThreadMessages(threadHistory, state.LastIngestedMessageID, c.api.userID)
	if len(missed) == 0 {
		return message
	}

	synthesized := message
	synthesized.Text = synthesizeMissedThreadMessagesPrompt(missed)
	synthesized.PreformattedPrompt = true
	return synthesized
}

func collectMissedThreadMessages(
	threadHistory []Message,
	lastIngestedMessageID string,
	botUserID string,
) []Message {
	missed := make([]Message, 0)
	ready := lastIngestedMessageID == ""
	seen := make(map[string]bool, len(threadHistory))

	for _, msg := range threadHistory {
		if msg.ID == "" || seen[msg.ID] {
			continue
		}
		seen[msg.ID] = true
		if !ready {
			if msg.ID == lastIngestedMessageID {
				ready = true
			}
			continue
		}
		if msg.ID == lastIngestedMessageID || msg.User.ID == botUserID {
			continue
		}
		missed = append(missed, msg)
	}

	return missed
}

func synthesizeMissedThreadMessagesPrompt(messages []Message) string {
	var b strings.Builder
	b.WriteString("Thread context since your last response:\n\n")
	for _, msg := range messages {
		username := strings.TrimSpace(msg.User.Username)
		if username == "" {
			username = "user"
		}
		b.WriteString("- ")
		b.WriteString(username)
		b.WriteString(": ")
		b.WriteString(strings.TrimSpace(msg.Text))
		b.WriteString("\n")
	}
	if len(messages) > 1 {
		last := messages[len(messages)-1]
		username := strings.TrimSpace(last.User.Username)
		if username == "" {
			username = "user"
		}
		b.WriteString("\nReply to the latest message:\n")
		b.WriteString(username)
		b.WriteString(": ")
		b.WriteString(strings.TrimSpace(last.Text))
	}
	return b.String()
}

// ddpMessage represents a DDP protocol message
type ddpMessage struct {
	Msg        string                 `json:"msg,omitempty"`
	Version    string                 `json:"version,omitempty"`
	Support    []string               `json:"support,omitempty"`
	Method     string                 `json:"method,omitempty"`
	ID         string                 `json:"id,omitempty"`
	Params     []interface{}          `json:"params,omitempty"`
	Name       string                 `json:"name,omitempty"`
	Collection string                 `json:"collection,omitempty"`
	Fields     map[string]interface{} `json:"fields,omitempty"`
}

// generateID creates a random ID for DDP messages
func generateID() string {
	return fmt.Sprintf("%x", rand.Uint64())
}

// Run connects the bot and keeps it running until the context is canceled or Stop is called.
func (c *Client) Run(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	c.logger.Info("starting bot")

	if err := c.initialize(); err != nil {
		return err
	}

	return c.runWithReconnect(ctx)
}

func (c *Client) initialize() error {
	username, err := c.api.FetchUsername()
	if err != nil {
		return fmt.Errorf("failed to fetch username: %w", err)
	}
	c.username = username

	if err := c.api.SetStatusOnline(c.statusMessage); err != nil {
		return fmt.Errorf("failed to set status online: %w", err)
	}
	c.logger.Info("status set to online")

	rooms, dmRooms, err := c.api.GetSubscriptions()
	if err != nil {
		return fmt.Errorf("failed to get subscriptions: %w", err)
	}

	c.rooms = make(map[string]bool, len(rooms))
	for _, rid := range rooms {
		c.rooms[rid] = true
	}

	c.dmRooms = make(map[string]bool, len(dmRooms))
	for rid, isDM := range dmRooms {
		if isDM {
			c.dmRooms[rid] = true
		}
	}

	c.logger.Info("found rooms", "total", len(rooms), "dms", len(c.dmRooms))
	return nil
}

func (c *Client) runWithReconnect(ctx context.Context) error {
	backoffCfg := backoff.NewExponentialBackOff()
	backoffCfg.InitialInterval = time.Second
	backoffCfg.MaxInterval = 30 * time.Second
	backoffCfg.MaxElapsedTime = 0 // retry indefinitely

	for {
		if err := c.checkForStop(ctx); err != nil {
			return err
		}

		sessionStart := time.Now()
		if err := c.runSession(ctx); err != nil {
			if errors.Is(err, errStopRequested) {
				return nil
			}
			if errors.Is(err, context.Canceled) {
				return ctx.Err()
			}

			c.logger.Error("websocket session ended", "error", err)

			// If we stayed connected for a decent period, reset backoff
			if time.Since(sessionStart) >= stableConnectionReset {
				backoffCfg.Reset()
			}

			wait := backoffCfg.NextBackOff()
			c.logger.Warn("retrying websocket connection", "retry_in", wait)

			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return ctx.Err()
			case <-c.stopChan:
				return nil
			}
			continue
		}

		// runSession only returns nil when stop has been requested
		return nil
	}
}

func (c *Client) runSession(ctx context.Context) error {
	wsURL := strings.Replace(c.api.baseURL, "https://", "wss://", 1)
	wsURL = strings.Replace(wsURL, "http://", "ws://", 1)
	wsURL += "/websocket"

	ws, err := c.wsDialer.Dial(wsURL, nil)
	if err != nil {
		return fmt.Errorf("failed to connect to WebSocket: %w", err)
	}

	c.wsMu.Lock()
	c.ws = ws
	c.wsMu.Unlock()
	c.logger.Info("connected to websocket")

	defer c.closeWebSocket()

	// Reset pending map for the new session
	c.pendingMu.Lock()
	c.pending = make(map[string]string)
	c.pendingMu.Unlock()

	if err := c.sendMessage(ddpMessage{
		Msg:     "connect",
		Version: "1",
		Support: []string{"1"},
	}); err != nil {
		return fmt.Errorf("failed to send connect message: %w", err)
	}

	sessionCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Ensure the websocket is closed when context or stop is triggered
	go func() {
		select {
		case <-sessionCtx.Done():
			c.closeWebSocket()
		case <-c.stopChan:
			c.closeWebSocket()
		}
	}()

	return c.handleMessages(sessionCtx)
}

func (c *Client) checkForStop(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.stopChan:
		return errStopRequested
	default:
		return nil
	}
}

// Stop gracefully stops the bot
func (c *Client) Stop() {
	c.stopOnce.Do(func() {
		close(c.stopChan)
		c.closeWebSocket()
	})
}

func (c *Client) closeWebSocket() {
	c.wsMu.Lock()
	defer c.wsMu.Unlock()
	if c.ws != nil {
		if err := c.ws.Close(); err != nil {
			c.logger.Warn("websocket close failed", "error", err)
		}
		c.ws = nil
	}
}

// sendMessage sends a DDP message to the WebSocket
func (c *Client) sendMessage(msg ddpMessage) error {
	c.wsMu.Lock()
	defer c.wsMu.Unlock()
	if c.ws == nil {
		return errors.New("websocket not connected")
	}
	return c.ws.WriteJSON(msg)
}

// callMethod calls a DDP method and tracks it
func (c *Client) callMethod(method string, params ...interface{}) string {
	id := generateID()

	c.pendingMu.Lock()
	c.pending[id] = method
	c.pendingMu.Unlock()

	if err := c.sendMessage(ddpMessage{
		Msg:    "method",
		Method: method,
		ID:     id,
		Params: params,
	}); err != nil {
		c.pendingMu.Lock()
		delete(c.pending, id) // Keep state consistent
		c.pendingMu.Unlock()
		c.logger.Error("failed to send DDP method call", "method", method, "id", id, "error", err)
	}
	return id
}

// subscribe subscribes to a DDP stream
func (c *Client) subscribe(name string, params ...interface{}) string {
	id := generateID()
	if err := c.sendMessage(ddpMessage{
		Msg:    "sub",
		ID:     id,
		Name:   name,
		Params: params,
	}); err != nil {
		c.logger.Error("failed to send DDP subscription", "name", name, "id", id, "error", err)
	}
	return id
}

// handleMessages processes incoming WebSocket messages
func (c *Client) handleMessages(ctx context.Context) error {
	for {
		var msg ddpMessage
		if err := c.ws.ReadJSON(&msg); err != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-c.stopChan:
				return errStopRequested
			default:
				c.logger.Error("error reading message", "error", err)
				return err
			}
		}

		if msg.Msg == "ping" {
			if err := c.sendMessage(ddpMessage{Msg: "pong"}); err != nil {
				c.logger.Warn("failed to send pong", "error", err)
			}
			continue
		}

		c.processMessage(&msg)
	}
}

// processMessage handles a single DDP message
func (c *Client) processMessage(msg *ddpMessage) {
	switch msg.Msg {
	case "connected":
		c.logger.Debug("websocket connected, logging in")
		c.callMethod("login", map[string]string{"resume": c.api.token})

	case "result":
		c.pendingMu.Lock()
		method, ok := c.pending[msg.ID]
		if ok {
			delete(c.pending, msg.ID)
		}
		c.pendingMu.Unlock()

		if ok && method == "login" {
			c.logger.Info("logged in via websocket")
			c.callMethod("UserPresence:online")

			// Subscribe to all rooms the bot knows about
			count := 0
			for rid := range c.rooms {
				c.subscribe("stream-room-messages", rid, false)
				count++
			}
			c.logger.Info("subscribed to rooms", "count", count)

			// Subscribe to membership changes
			c.subscribe(
				"stream-notify-user",
				fmt.Sprintf("%s/subscriptions-changed", c.api.userID),
				false,
			)
		}

	case "changed":
		c.handleChangedMessage(msg)

	case "ping":
		if err := c.sendMessage(ddpMessage{Msg: "pong"}); err != nil {
			c.logger.Warn("failed to send pong", "error", err)
		}
	}
}

// handleChangedMessage processes "changed" messages (new messages, subscriptions, etc.)
func (c *Client) handleChangedMessage(msg *ddpMessage) {
	switch msg.Collection {
	case "stream-room-messages":
		c.handleRoomMessage(msg)

	case "stream-notify-user":
		c.handleUserNotification(msg)
	}
}

// handleRoomMessage processes incoming room messages
func (c *Client) handleRoomMessage(msg *ddpMessage) {
	args, ok := msg.Fields["args"].([]interface{})
	if !ok || len(args) == 0 {
		c.logger.Debug("handleRoomMessage: no args")
		return
	}

	msgData, ok := args[0].(map[string]interface{})
	if !ok {
		c.logger.Debug("handleRoomMessage: args[0] not map")
		return
	}

	roomID, _ := msgData["rid"].(string)

	// Ignore our own messages
	if u, ok := msgData["u"].(map[string]interface{}); ok {
		if userID, ok := u["_id"].(string); ok && userID == c.api.userID {
			c.logger.Debug("ignoring own message")
			return
		}
	}

	// Ignore message edits (messages with editedAt field)
	if _, hasEditedAt := msgData["editedAt"]; hasEditedAt {
		c.logger.Debug("ignoring edited message")
		return
	}

	// Ignore thread metadata updates (messages with tcount/tlm but no actual new content)
	// When someone replies to a thread, the parent message gets updated with tcount/tlm
	if _, hasTcount := msgData["tcount"]; hasTcount {
		c.logger.Debug("ignoring thread metadata update (tcount)")
		return
	}
	if _, hasTlm := msgData["tlm"]; hasTlm {
		c.logger.Debug("ignoring thread metadata update (tlm)")
		return
	}

	policy, ok := c.resolvePolicy(roomID)
	if !ok {
		c.logger.Debug("ignoring inactive room message", "room_id", roomID)
		return
	}

	message := parseMessage(msgData)
	if !c.shouldRespondToRoomMessage(message, msgData, policy) {
		if message.ThreadID != "" {
			c.logger.Debug(
				"ignoring room thread message without mention or active thread",
				"room_id",
				message.RoomID,
				"thread_id",
				message.ThreadID,
			)
		} else {
			c.logger.Debug("ignoring room root message without mention", "room_id", message.RoomID)
		}
		return
	}
	if policy.Scope == "room" {
		message.Text = stripLeadingUsernameMention(message.Text, c.username)
	}
	go c.handleResponse(message, policy)
}

// handleUserNotification processes user notifications (like subscription changes)
func (c *Client) handleUserNotification(msg *ddpMessage) {
	args, ok := msg.Fields["args"].([]interface{})
	if !ok {
		c.logger.Debug("handleUserNotification: args not array")
		return
	}
	if len(args) < 2 {
		c.logger.Debug("handleUserNotification: insufficient args", "len", len(args))
		return
	}

	event, _ := args[0].(string)
	c.logger.Debug("handleUserNotification: event received", "event", event)

	// Check for subscription change events (inserted = new subscription, updated = modified)
	if event != "inserted" && event != "updated" {
		c.logger.Debug("handleUserNotification: not a subscription event, ignoring", "event", event)
		return
	}

	payload, ok := args[1].(map[string]interface{})
	if !ok {
		c.logger.Debug("handleUserNotification: payload not map")
		return
	}

	rid, _ := payload["rid"].(string)
	roomType, _ := payload["t"].(string)

	// Log new subscriptions for troubleshooting
	if event == "inserted" {
		c.logger.Debug("new subscription created", "room_id", rid, "type", roomType)
	}

	if rid != "" && !c.rooms[rid] {
		c.rooms[rid] = true

		// Track DM rooms (type "d")
		if roomType == "d" {
			c.dmRooms[rid] = true
			c.logger.Info("subscribed to new DM room", "room_id", rid)
		} else {
			c.logger.Info("subscribed to new room", "room_id", rid, "type", roomType)
		}

		c.subscribe("stream-room-messages", rid, false)
	}
}

// handleResponse handles generating and sending a response within a resolved scope.
func (c *Client) handleResponse(message Message, policy resolvedPolicy) {
	// Create trace span for this request
	tracer := otel.Tracer("rocketbot")
	ctx, span := tracer.Start(context.Background(), "handle-message")
	defer span.End()
	ctx = WithGenerationOptions(ctx, GenerationOptions{
		Scope:           policy.Scope,
		RoomID:          policy.RoomID,
		SessionDir:      policy.SessionDir,
		BootstrapPrompt: policy.BootstrapPrompt,
	})

	// Log the received message with trace context
	scopeLabel := policy.Scope
	if scopeLabel == "" {
		scopeLabel = "unknown"
	}
	if message.ThreadID != "" {
		c.logger.InfoContext(
			ctx,
			"received message in thread",
			"room_id",
			message.RoomID,
			"thread_id",
			message.ThreadID,
			"scope",
			scopeLabel,
			"user",
			message.User.Username,
		)
	} else {
		c.logger.InfoContext(
			ctx,
			"received message",
			"room_id",
			message.RoomID,
			"scope",
			scopeLabel,
			"user",
			message.User.Username,
		)
	}

	if cmd, ok := parseBotCommand(message.Text); ok {
		c.handleBotCommand(ctx, message, cmd)
		return
	}

	// Determine thread target once so history fetch, context keying, and replies align.
	effectiveThreadID := message.ThreadID
	if shouldCreateThread(message, policy.ThreadDefault) {
		effectiveThreadID = message.ID
	}

	messageForGenerator := message
	messageForGenerator.ThreadID = effectiveThreadID
	messageForGenerator = c.prepareMessageForGenerator(ctx, messageForGenerator, policy)

	historyLimit := 10
	if hg, ok := c.generator.(HistoryAwareGenerator); ok {
		if limit := hg.HistoryLimit(messageForGenerator); limit >= 0 {
			historyLimit = limit
		}
	}
	c.logger.DebugContext(
		ctx,
		"history strategy selected",
		"room_id",
		message.RoomID,
		"thread_id",
		effectiveThreadID,
		"history_limit",
		historyLimit,
	)

	// Fetch conversation history only when requested by generator.
	var history []Message
	if historyLimit > 0 {
		if effectiveThreadID != "" {
			// Fetch thread messages
			history = c.api.FetchThreadHistory(ctx, effectiveThreadID, historyLimit)
		} else {
			// Fetch room messages
			history = c.api.FetchHistory(ctx, message.RoomID, historyLimit)
		}
	}

	// Remove current message if it appears in history
	// (API returns newest messages, current one may be included)
	filteredHistory := make([]Message, 0, len(history))
	for _, msg := range history {
		if msg.ID != message.ID {
			filteredHistory = append(filteredHistory, msg)
		}
	}

	// Show typing indicator and ensure cleanup
	if err := c.setTypingIndicator(message.RoomID, true); err != nil {
		c.logger.ErrorContext(ctx, "error setting typing indicator", "error", err)
	}
	defer func() { _ = c.setTypingIndicator(message.RoomID, false) }()

	// Check if generator supports streaming AND streamed output is enabled
	if policy.StreamedOutput {
		if rg, ok := c.generator.(RenderStreamingGenerator); ok {
			c.logger.DebugContext(ctx, "using rich render streaming response mode")
			if c.handleRenderStreamingResponse(ctx, messageForGenerator, filteredHistory, rg, policy) &&
				policy.Scope == "room" && effectiveThreadID != "" {
				c.markActiveRoomThread(message.RoomID, effectiveThreadID, message.ID)
			}
			return
		}
		if sg, ok := c.generator.(StreamingGenerator); ok {
			c.logger.DebugContext(ctx, "using streaming response mode")
			if c.handleStreamingResponse(ctx, messageForGenerator, filteredHistory, sg, policy) &&
				policy.Scope == "room" && effectiveThreadID != "" {
				c.markActiveRoomThread(message.RoomID, effectiveThreadID, message.ID)
			}
			return
		}
	}

	// Use non-streaming response (either no streaming support or streamed output disabled)
	c.logger.DebugContext(
		ctx,
		"using non-streaming response mode",
		"streamed_output_enabled",
		policy.StreamedOutput,
	)
	if c.handleNonStreamingResponse(ctx, messageForGenerator, filteredHistory, policy) &&
		policy.Scope == "room" && effectiveThreadID != "" {
		c.markActiveRoomThread(message.RoomID, effectiveThreadID, message.ID)
	}
}

func (c *Client) handleDMResponse(message Message) {
	policy, ok := c.resolvePolicy(message.RoomID)
	if !ok {
		policy = resolvedPolicy{
			Scope:           "dm",
			RoomID:          message.RoomID,
			BootstrapPrompt: c.bootstrapPrompt,
			RenderMode:      c.renderMode,
			ThreadDefault:   c.threadDefault,
			StreamedOutput:  c.streamedOutput,
		}
	}
	c.handleResponse(message, policy)
}

// handleRenderStreamingResponse handles structured streaming responses with
// minimal tool rendering plus assistant answer text.
func (c *Client) handleRenderStreamingResponse(
	ctx context.Context,
	message Message,
	history []Message,
	rg RenderStreamingGenerator,
	policy resolvedPolicy,
) bool {
	const noProgressNoticeAfter = 10 * time.Second
	const noProgressNoticeEvery = 20 * time.Second

	threadID := message.ThreadID
	if shouldCreateThread(message, policy.ThreadDefault) {
		threadID = message.ID
	}

	msgID, err := c.api.PostMessage(ctx, message.RoomID, "...", threadID)
	if err != nil {
		c.logger.ErrorContext(ctx, "error posting initial message", "error", err)
		return false
	}

	ctx = context.WithValue(ctx, ReplyMessageIDKey, msgID)
	ctx = context.WithValue(ctx, ReplyRoomIDKey, message.RoomID)

	c.logger.DebugContext(ctx, "starting generator render stream")
	eventCh, err := rg.GenerateRenderStream(ctx, message, history)
	if err != nil {
		c.logger.ErrorContext(ctx, "error starting render stream", "error", err)
		return false
	}
	c.logger.DebugContext(ctx, "generator render stream started")

	renderer := NewProgressiveRenderer(policy.RenderMode)
	lastSent := "..."
	dirty := false
	lastProgressAt := time.Now()
	nextNoProgressNoticeAt := lastProgressAt.Add(noProgressNoticeAfter)

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	flush := func(force bool) bool {
		base := renderer.Render()
		noticeDue := false
		if renderHasPlaceholder(base) {
			elapsed := time.Since(lastProgressAt)
			noticeDue = elapsed >= noProgressNoticeAfter && time.Now().After(nextNoProgressNoticeAt)
		}

		if !dirty && !force && !noticeDue {
			c.logger.DebugContext(ctx, "render update skipped", "reason", "unchanged")
			return true
		}

		current := base
		if noticeDue {
			elapsed := time.Since(lastProgressAt)
			noticeText := fmt.Sprintf(
				"_Still working... (%ds)_",
				int(elapsed.Round(time.Second).Seconds()),
			)
			current = renderReplacePlaceholder(current, noticeText)
			nextNoProgressNoticeAt = time.Now().Add(noProgressNoticeEvery)
		}
		if current == "" {
			current = "..."
		}
		if !force && current == lastSent {
			dirty = false
			c.logger.DebugContext(ctx, "render update skipped", "reason", "same_output")
			return true
		}
		if err := c.api.UpdateMessage(ctx, message.RoomID, msgID, current); err != nil {
			c.logger.ErrorContext(ctx, "error updating rendered message", "error", err)
			return false
		}
		lastSent = current
		dirty = false
		c.logger.DebugContext(ctx, "rendered message updated", "length", len(current))
		return true
	}

	for {
		select {
		case ev, ok := <-eventCh:
			if !ok {
				if !flush(true) {
					return false
				}
				c.logger.InfoContext(ctx, "response sent", "length", len(lastSent))
				return true
			}
			changed := renderer.Apply(ev)
			if changed {
				dirty = true
				lastProgressAt = time.Now()
				nextNoProgressNoticeAt = lastProgressAt.Add(noProgressNoticeAfter)
			}
			c.logger.DebugContext(
				ctx,
				"render event applied",
				"type",
				string(ev.Type),
				"changed",
				changed,
				"tool",
				ev.ToolName,
				"status",
				ev.Status,
			)

		case <-ticker.C:
			if !flush(false) {
				return false
			}

		case <-ctx.Done():
			return false
		}
	}
}

func renderHasPlaceholder(rendered string) bool {
	trimmed := strings.TrimSpace(rendered)
	if trimmed == "..." {
		return true
	}
	return strings.HasSuffix(trimmed, "\n\n...")
}

func renderReplacePlaceholder(rendered string, replacement string) string {
	trimmed := strings.TrimSpace(rendered)
	if trimmed == "..." {
		return replacement
	}
	if strings.HasSuffix(trimmed, "\n\n...") {
		return strings.TrimSuffix(trimmed, "\n\n...") + "\n\n" + replacement
	}
	return rendered
}

type botCommand string

const botCommandReset botCommand = "reset"

func parseBotCommand(text string) (botCommand, bool) {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) < 2 {
		return "", false
	}
	if strings.ToLower(fields[0]) != "!rb" {
		return "", false
	}
	switch strings.ToLower(fields[1]) {
	case string(botCommandReset):
		return botCommandReset, true
	default:
		return "", false
	}
}

func (c *Client) handleBotCommand(ctx context.Context, message Message, cmd botCommand) {
	threadID := message.ThreadID
	respond := func(text string) {
		if _, err := c.api.PostMessage(ctx, message.RoomID, text, threadID); err != nil {
			c.logger.ErrorContext(
				ctx,
				"error posting command response",
				"command",
				string(cmd),
				"error",
				err,
			)
		}
	}

	if cmd != botCommandReset {
		respond("Unknown command.")
		return
	}

	resetter, ok := c.generator.(SessionResetter)
	if !ok {
		respond("Session reset is not supported for this bot.")
		return
	}

	if err := resetter.ResetSession(ctx, message); err != nil {
		c.logger.ErrorContext(ctx, "session reset failed", "error", err)
		respond("Reset failed. Please try again.")
		return
	}
	respond("Session reset. Starting fresh.")
}

// handleNonStreamingResponse handles the traditional non-streaming response
func (c *Client) handleNonStreamingResponse(
	ctx context.Context,
	message Message,
	history []Message,
	policy resolvedPolicy,
) bool {
	// Determine thread ID: use /thread command logic or threadDefault config
	threadID := message.ThreadID
	if shouldCreateThread(message, policy.ThreadDefault) {
		threadID = message.ID
	}

	// Create placeholder message first
	msgID, err := c.api.PostMessage(ctx, message.RoomID, "...", threadID)
	if err != nil {
		c.logger.ErrorContext(ctx, "error posting initial message", "error", err)
		return false
	}

	// Add reply message ID and room ID to context
	ctx = context.WithValue(ctx, ReplyMessageIDKey, msgID)
	ctx = context.WithValue(ctx, ReplyRoomIDKey, message.RoomID)

	// Generate response (this may take time)
	response, err := c.generator.GenerateResponse(ctx, message, history)
	if err != nil {
		c.logger.ErrorContext(ctx, "error generating response", "error", err)
		return false
	}

	// Update the message with the actual response
	if err := c.api.UpdateMessage(ctx, message.RoomID, msgID, response); err != nil {
		c.logger.ErrorContext(ctx, "error updating message", "error", err)
		return false
	}

	c.logger.InfoContext(ctx, "response sent", "length", len(response))
	return true
}

// handleStreamingResponse handles streaming response with 1-second updates
func (c *Client) handleStreamingResponse(
	ctx context.Context,
	message Message,
	history []Message,
	sg StreamingGenerator,
	policy resolvedPolicy,
) bool {
	const noChunkNoticeAfter = 10 * time.Second
	const noChunkNoticeEvery = 20 * time.Second
	const noResponseText = "_No response produced._"

	// Determine thread ID: use /thread command logic or threadDefault config
	threadID := message.ThreadID
	if shouldCreateThread(message, policy.ThreadDefault) {
		threadID = message.ID
	}

	// Post initial message
	msgID, err := c.api.PostMessage(ctx, message.RoomID, "...", threadID)
	if err != nil {
		c.logger.ErrorContext(ctx, "error posting initial message", "error", err)
		return false
	}

	// Add reply message ID and room ID to context
	ctx = context.WithValue(ctx, ReplyMessageIDKey, msgID)
	ctx = context.WithValue(ctx, ReplyRoomIDKey, message.RoomID)

	// Start streaming
	c.logger.DebugContext(ctx, "starting generator stream")
	chunkCh, err := sg.GenerateResponseStream(ctx, message, history)
	if err != nil {
		c.logger.ErrorContext(ctx, "error starting stream", "error", err)
		return false
	}
	c.logger.DebugContext(ctx, "generator stream started")

	// Buffer for accumulating chunks
	var buffer strings.Builder
	lastSent := ""
	lastChunkAt := time.Now()
	nextNoChunkNoticeAt := lastChunkAt.Add(noChunkNoticeAfter)
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	// Process chunks and update periodically
	for {
		select {
		case chunk, ok := <-chunkCh:
			if !ok {
				// Stream finished - do final update
				final := buffer.String()
				if final == "" && lastSent != "" {
					// We may have sent a "still working" placeholder; replace it.
					if err := c.api.UpdateMessage(ctx, message.RoomID, msgID, noResponseText); err != nil {
						c.logger.ErrorContext(ctx, "error updating final message", "error", err)
					}
					return false
				}
				if final != lastSent && final != "" {
					if err := c.api.UpdateMessage(ctx, message.RoomID, msgID, final); err != nil {
						c.logger.ErrorContext(ctx, "error updating final message", "error", err)
						return false
					} else {
						c.logger.InfoContext(ctx, "response sent", "length", len(final))
						return true
					}
				}
				return final == lastSent && final != ""
			}
			buffer.WriteString(chunk)
			lastChunkAt = time.Now()
			nextNoChunkNoticeAt = lastChunkAt.Add(noChunkNoticeAfter)

		case <-ticker.C:
			// Update if buffer has changed
			current := buffer.String()
			if current == "" {
				elapsed := time.Since(lastChunkAt)
				if elapsed >= noChunkNoticeAfter && time.Now().After(nextNoChunkNoticeAt) {
					noticeText := fmt.Sprintf(
						"_Still working... (%ds)_",
						int(elapsed.Round(time.Second).Seconds()),
					)
					if err := c.api.UpdateMessage(ctx, message.RoomID, msgID, noticeText); err != nil {
						c.logger.ErrorContext(ctx, "error updating message", "error", err)
					} else {
						c.logger.DebugContext(ctx, "message updated (no chunks yet)", "elapsed", elapsed)
						lastSent = noticeText
						nextNoChunkNoticeAt = time.Now().Add(noChunkNoticeEvery)
					}
					continue
				}
			}
			if current != lastSent && current != "" {
				if err := c.api.UpdateMessage(ctx, message.RoomID, msgID, current); err != nil {
					c.logger.ErrorContext(ctx, "error updating message", "error", err)
				} else {
					c.logger.DebugContext(ctx, "message updated", "length", len(current))
					lastSent = current
				}
			}
		case <-ctx.Done():
			return false
		}
	}
}

// setTypingIndicator sets or clears the typing indicator for a room
func (c *Client) setTypingIndicator(roomID string, typing bool) error {
	// Send typing notification via WebSocket using user-activity
	var activity []string
	if typing {
		activity = []string{"user-typing"}
	} else {
		activity = []string{}
	}
	c.callMethod(
		"stream-notify-room",
		fmt.Sprintf("%s/user-activity", roomID),
		c.username,
		activity,
	)
	return nil
}

// API returns the underlying APIClient for external use (e.g., HTTP API server)
func (c *Client) API() *APIClient {
	return c.api
}
