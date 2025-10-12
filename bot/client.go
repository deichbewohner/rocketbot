package bot

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"strings"
	"sync"
	"time"

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
	api            *APIClient
	username       string
	name           string
	generator      ResponseGenerator
	logger         *slog.Logger
	streamedOutput bool
	threadDefault  bool
	statusMessage  string

	ws        wsConn
	wsDialer  wsDialer
	wsMu      sync.Mutex
	pending   map[string]string
	pendingMu sync.Mutex // protects pending map
	rooms     map[string]bool
	dmRooms   map[string]bool
	stopChan  chan struct{}
}

// NewClient creates a new Rocket.Chat bot client
func NewClient(
	baseURL, userID, token, name string,
	generator ResponseGenerator,
	streamedOutput bool,
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
		threadDefault,
		logger,
		statusMessage,
	)
}

// NewClientWithAPI creates a Client with a pre-configured APIClient.
// This constructor is primarily for testing, allowing injection of mocked HTTP clients.
func NewClientWithAPI(
	api *APIClient,
	name string,
	generator ResponseGenerator,
	streamedOutput bool,
	threadDefault bool,
	logger *slog.Logger,
	statusMessage string,
) *Client {
	return &Client{
		api:            api,
		name:           name,
		generator:      generator,
		logger:         logger,
		streamedOutput: streamedOutput,
		threadDefault:  threadDefault,
		statusMessage:  statusMessage,
		wsDialer:       &defaultWSDialer{dialer: websocket.DefaultDialer},
		pending:        make(map[string]string),
		rooms:          make(map[string]bool),
		dmRooms:        make(map[string]bool),
		stopChan:       make(chan struct{}),
	}
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

// Start connects the bot and begins listening for messages
func (c *Client) Start() error {
	c.logger.Info("starting bot")

	// Get username from API
	username, err := c.api.FetchUsername()
	if err != nil {
		return fmt.Errorf("failed to fetch username: %w", err)
	}
	c.username = username

	// Set status to online via REST
	if err := c.api.SetStatusOnline(c.statusMessage); err != nil {
		return fmt.Errorf("failed to set status online: %w", err)
	}
	c.logger.Info("status set to online")

	// Get all subscriptions (rooms) via REST
	rooms, dmRooms, err := c.api.GetSubscriptions()
	if err != nil {
		return fmt.Errorf("failed to get subscriptions: %w", err)
	}
	c.dmRooms = dmRooms
	c.logger.Info("found rooms", "total", len(rooms), "dms", len(dmRooms))

	// Connect to WebSocket
	wsURL := strings.Replace(c.api.baseURL, "https://", "wss://", 1)
	wsURL = strings.Replace(wsURL, "http://", "ws://", 1)
	wsURL += "/websocket"

	ws, err := c.wsDialer.Dial(wsURL, nil)
	if err != nil {
		return fmt.Errorf("failed to connect to WebSocket: %w", err)
	}
	c.ws = ws
	c.logger.Info("connected to websocket")

	// Send connect message
	if err := c.sendMessage(ddpMessage{
		Msg:     "connect",
		Version: "1",
		Support: []string{"1"},
	}); err != nil {
		return fmt.Errorf("failed to send connect message: %w", err)
	}

	// Start message handler
	go c.handleMessages(rooms)

	return nil
}

// Stop gracefully stops the bot
func (c *Client) Stop() {
	close(c.stopChan)
	if c.ws != nil {
		if err := c.ws.Close(); err != nil {
			c.logger.Warn("websocket close failed", "error", err)
		}
	}
}

// sendMessage sends a DDP message to the WebSocket
func (c *Client) sendMessage(msg ddpMessage) error {
	c.wsMu.Lock()
	defer c.wsMu.Unlock()
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
func (c *Client) handleMessages(initialRooms []string) {
	for {
		select {
		case <-c.stopChan:
			return
		default:
			var msg ddpMessage
			if err := c.ws.ReadJSON(&msg); err != nil {
				c.logger.Error("error reading message", "error", err)
				return
			}

			c.processMessage(&msg, initialRooms)
		}
	}
}

// processMessage handles a single DDP message
func (c *Client) processMessage(msg *ddpMessage, initialRooms []string) {
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

			// Subscribe to all rooms
			for _, rid := range initialRooms {
				c.rooms[rid] = true
				c.subscribe("stream-room-messages", rid, false)
			}
			c.logger.Info("subscribed to rooms", "count", len(initialRooms))

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

	// Check if this is a DM by looking up the room ID
	if c.dmRooms[roomID] {
		// Extract message metadata
		message := parseMessage(msgData)

		// Handle response generation in a goroutine to avoid blocking
		go c.handleDMResponse(message)
	} else {
		c.logger.Debug("ignoring non-DM message", "room_id", roomID)
	}
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

// handleDMResponse handles generating and sending a response to a DM
func (c *Client) handleDMResponse(message Message) {
	// Create trace span for this request
	tracer := otel.Tracer("rocketbot")
	ctx, span := tracer.Start(context.Background(), "handle-dm")
	defer span.End()

	// Log the received DM with trace context
	if message.ThreadID != "" {
		c.logger.InfoContext(
			ctx,
			"received dm in thread",
			"room_id",
			message.RoomID,
			"thread_id",
			message.ThreadID,
			"user",
			message.User.Username,
		)
	} else {
		c.logger.InfoContext(ctx, "received dm", "room_id", message.RoomID, "user", message.User.Username)
	}

	// Fetch conversation history based on whether this is a thread
	var history []Message
	if message.ThreadID != "" {
		// Fetch thread messages
		history = c.api.FetchThreadHistory(ctx, message.ThreadID, 10)
	} else {
		// Fetch room messages
		history = c.api.FetchHistory(ctx, message.RoomID, 10)
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
	if c.streamedOutput {
		if sg, ok := c.generator.(StreamingGenerator); ok {
			c.logger.DebugContext(ctx, "using streaming response mode")
			c.handleStreamingResponse(ctx, message, filteredHistory, sg)
			return
		}
	}

	// Use non-streaming response (either no streaming support or streamed output disabled)
	c.logger.DebugContext(
		ctx,
		"using non-streaming response mode",
		"streamed_output_enabled",
		c.streamedOutput,
	)
	c.handleNonStreamingResponse(ctx, message, filteredHistory)
}

// handleNonStreamingResponse handles the traditional non-streaming response
func (c *Client) handleNonStreamingResponse(
	ctx context.Context,
	message Message,
	history []Message,
) {
	// Determine thread ID: use /thread command logic or threadDefault config
	threadID := message.ThreadID
	if shouldCreateThread(message, c.threadDefault) {
		threadID = message.ID
	}

	// Create placeholder message first
	msgID, err := c.api.PostMessage(ctx, message.RoomID, "...", threadID)
	if err != nil {
		c.logger.ErrorContext(ctx, "error posting initial message", "error", err)
		return
	}

	// Add reply message ID and room ID to context
	ctx = context.WithValue(ctx, ReplyMessageIDKey, msgID)
	ctx = context.WithValue(ctx, ReplyRoomIDKey, message.RoomID)

	// Generate response (this may take time)
	response, err := c.generator.GenerateResponse(ctx, message, history)
	if err != nil {
		c.logger.ErrorContext(ctx, "error generating response", "error", err)
		return
	}

	// Update the message with the actual response
	if err := c.api.UpdateMessage(ctx, message.RoomID, msgID, response); err != nil {
		c.logger.ErrorContext(ctx, "error updating message", "error", err)
		return
	}

	c.logger.InfoContext(ctx, "response sent", "length", len(response))
}

// handleStreamingResponse handles streaming response with 1-second updates
func (c *Client) handleStreamingResponse(
	ctx context.Context,
	message Message,
	history []Message,
	sg StreamingGenerator,
) {
	// Determine thread ID: use /thread command logic or threadDefault config
	threadID := message.ThreadID
	if shouldCreateThread(message, c.threadDefault) {
		threadID = message.ID
	}

	// Post initial message
	msgID, err := c.api.PostMessage(ctx, message.RoomID, "...", threadID)
	if err != nil {
		c.logger.ErrorContext(ctx, "error posting initial message", "error", err)
		return
	}

	// Add reply message ID and room ID to context
	ctx = context.WithValue(ctx, ReplyMessageIDKey, msgID)
	ctx = context.WithValue(ctx, ReplyRoomIDKey, message.RoomID)

	// Start streaming
	chunkCh, err := sg.GenerateResponseStream(ctx, message, history)
	if err != nil {
		c.logger.ErrorContext(ctx, "error starting stream", "error", err)
		return
	}

	// Buffer for accumulating chunks
	var buffer strings.Builder
	lastSent := ""
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	// Process chunks and update periodically
	for {
		select {
		case chunk, ok := <-chunkCh:
			if !ok {
				// Stream finished - do final update
				final := buffer.String()
				if final != lastSent && final != "" {
					if err := c.api.UpdateMessage(ctx, message.RoomID, msgID, final); err != nil {
						c.logger.ErrorContext(ctx, "error updating final message", "error", err)
					} else {
						c.logger.InfoContext(ctx, "response sent", "length", len(final))
					}
				}
				return
			}
			buffer.WriteString(chunk)

		case <-ticker.C:
			// Update if buffer has changed
			current := buffer.String()
			if current != lastSent && current != "" {
				if err := c.api.UpdateMessage(ctx, message.RoomID, msgID, current); err != nil {
					c.logger.ErrorContext(ctx, "error updating message", "error", err)
				} else {
					c.logger.DebugContext(ctx, "message updated", "length", len(current))
					lastSent = current
				}
			}
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
