package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// APIClient handles all REST API interactions with Rocket.Chat
type APIClient struct {
	baseURL string
	userID  string
	token   string
	logger  *slog.Logger
	client  *http.Client
}

// NewAPIClient creates a new Rocket.Chat REST API client
func NewAPIClient(baseURL, userID, token string, httpClient *http.Client, logger *slog.Logger) *APIClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &APIClient{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		userID:  userID,
		token:   token,
		logger:  logger,
		client:  httpClient,
	}
}

// doRequest executes an HTTP request with authentication headers
func (a *APIClient) doRequest(req *http.Request) (*http.Response, error) {
	req.Header.Set("X-Auth-Token", a.token)
	req.Header.Set("X-User-Id", a.userID)
	return a.client.Do(req)
}

// FetchUsername retrieves the username for the authenticated user
func (a *APIClient) FetchUsername() (string, error) {
	url := fmt.Sprintf("%s/api/v1/me", a.baseURL)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}

	resp, err := a.doRequest(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var result struct {
		Username string `json:"username"`
		Success  bool   `json:"success"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	if !result.Success {
		return "", fmt.Errorf("me API call failed")
	}

	return result.Username, nil
}

// SetStatusOnline sets the user's status to online with an optional custom message
func (a *APIClient) SetStatusOnline(message string) error {
	url := fmt.Sprintf("%s/api/v1/users.setStatus", a.baseURL)

	// Build payload - only include message if provided
	var payload string
	if message == "" {
		payload = `{"status":"online"}`
	} else {
		payload = fmt.Sprintf(`{"status":"online","message":%q}`, message)
	}

	req, err := http.NewRequest("POST", url, strings.NewReader(payload))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	resp, err := a.doRequest(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	return nil
}

// GetSubscriptions retrieves all room IDs and identifies DMs
func (a *APIClient) GetSubscriptions() ([]string, map[string]bool, error) {
	url := fmt.Sprintf("%s/api/v1/subscriptions.get", a.baseURL)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, nil, err
	}

	resp, err := a.doRequest(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var result struct {
		Update []struct {
			RID string `json:"rid"`
			T   string `json:"t"`
		} `json:"update"`
		Success bool `json:"success"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, nil, err
	}

	if !result.Success {
		return nil, nil, fmt.Errorf("subscriptions.get failed")
	}

	rooms := make([]string, 0, len(result.Update))
	dmRooms := make(map[string]bool)

	for _, sub := range result.Update {
		rooms = append(rooms, sub.RID)
		if sub.T == "d" {
			dmRooms[sub.RID] = true
		}
	}

	return rooms, dmRooms, nil
}

// PostMessage sends a message to a room and returns the message ID
func (a *APIClient) PostMessage(ctx context.Context, roomID, text, tmid string) (string, error) {
	url := fmt.Sprintf("%s/api/v1/chat.postMessage", a.baseURL)

	payload := map[string]interface{}{
		"roomId": roomID,
		"text":   text,
	}

	// Include tmid if this is a thread reply
	if tmid != "" {
		payload["tmid"] = tmid
	}

	payloadBytes, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(
		ctx,
		"POST",
		url,
		strings.NewReader(string(payloadBytes)),
	)
	if err != nil {
		a.logger.ErrorContext(ctx, "error creating message request", "error", err)
		return "", err
	}

	req.Header.Set("Content-Type", "application/json")
	resp, err := a.doRequest(req)
	if err != nil {
		a.logger.ErrorContext(ctx, "error sending message", "error", err)
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errResp map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&errResp)
		a.logger.ErrorContext(
			ctx,
			"failed to send message",
			"status",
			resp.StatusCode,
			"response",
			errResp,
		)
		return "", fmt.Errorf("failed to send message: status %d", resp.StatusCode)
	}

	var result struct {
		Message struct {
			ID string `json:"_id"`
		} `json:"message"`
		Success bool `json:"success"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		a.logger.ErrorContext(ctx, "error decoding response", "error", err)
		return "", err
	}

	a.logger.DebugContext(ctx, "sent message", "text", text, "msgId", result.Message.ID)
	return result.Message.ID, nil
}

// UpdateMessage updates an existing message
func (a *APIClient) UpdateMessage(ctx context.Context, roomID, msgID, text string) error {
	url := fmt.Sprintf("%s/api/v1/chat.update", a.baseURL)

	payload := map[string]interface{}{
		"roomId": roomID,
		"msgId":  msgID,
		"text":   text,
	}

	payloadBytes, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(
		ctx,
		"POST",
		url,
		strings.NewReader(string(payloadBytes)),
	)
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	resp, err := a.doRequest(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errResp map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&errResp)
		return fmt.Errorf(
			"failed to update message: status %d, response %v",
			resp.StatusCode,
			errResp,
		)
	}

	return nil
}

// FetchHistory retrieves the last N messages from a room in chronological order
func (a *APIClient) FetchHistory(ctx context.Context, roomID string, count int) []Message {
	params := url.Values{}
	params.Set("roomId", roomID)
	params.Set("count", strconv.Itoa(count))

	apiURL := fmt.Sprintf("%s/api/v1/im.history?%s", a.baseURL, params.Encode())

	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		a.logger.ErrorContext(ctx, "error creating history request", "error", err)
		return nil
	}

	resp, err := a.doRequest(req)
	if err != nil {
		a.logger.ErrorContext(ctx, "error fetching history", "error", err)
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		a.logger.ErrorContext(ctx, "failed to fetch history", "status", resp.StatusCode)
		return nil
	}

	var result struct {
		Messages []struct {
			ID  string `json:"_id"`
			Msg string `json:"msg"`
			Rid string `json:"rid"`
			Ts  string `json:"ts"`
			U   struct {
				ID       string `json:"_id"`
				Username string `json:"username"`
				Name     string `json:"name"`
			} `json:"u"`
		} `json:"messages"`
		Success bool `json:"success"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		a.logger.ErrorContext(ctx, "error decoding history", "error", err)
		return nil
	}

	if !result.Success {
		a.logger.ErrorContext(ctx, "history API call failed")
		return nil
	}

	messages := parseAPIMessages(result.Messages)
	a.logger.DebugContext(ctx, "fetched history", "roomId", roomID, "count", len(messages))
	return messages
}

// FetchMessage retrieves a single message by ID
func (a *APIClient) FetchMessage(ctx context.Context, msgID string) *Message {
	url := fmt.Sprintf("%s/api/v1/chat.getMessage?msgId=%s", a.baseURL, msgID)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		a.logger.ErrorContext(ctx, "error creating message request", "error", err)
		return nil
	}

	resp, err := a.doRequest(req)
	if err != nil {
		a.logger.ErrorContext(ctx, "error fetching message", "error", err)
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		a.logger.ErrorContext(ctx, "failed to fetch message", "status", resp.StatusCode)
		return nil
	}

	var result struct {
		Message struct {
			ID  string `json:"_id"`
			Msg string `json:"msg"`
			Rid string `json:"rid"`
			Ts  string `json:"ts"`
			U   struct {
				ID       string `json:"_id"`
				Username string `json:"username"`
				Name     string `json:"name"`
			} `json:"u"`
		} `json:"message"`
		Success bool `json:"success"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		a.logger.ErrorContext(ctx, "error decoding message", "error", err)
		return nil
	}

	if !result.Success {
		a.logger.ErrorContext(ctx, "message API call failed")
		return nil
	}

	timestamp, _ := parseDDPDate(result.Message.Ts)

	return &Message{
		ID:        result.Message.ID,
		Text:      result.Message.Msg,
		RoomID:    result.Message.Rid,
		Timestamp: timestamp,
		User: MessageUser{
			ID:       result.Message.U.ID,
			Username: result.Message.U.Username,
			Name:     result.Message.U.Name,
		},
	}
}

// FetchThreadHistory retrieves messages from a specific thread in chronological order
func (a *APIClient) FetchThreadHistory(ctx context.Context, tmid string, count int) []Message {
	// First, fetch the original message that started the thread
	originalMsg := a.FetchMessage(ctx, tmid)
	if originalMsg == nil {
		a.logger.WarnContext(ctx, "could not fetch thread starter message", "tmid", tmid)
		return nil
	}

	params := url.Values{}
	params.Set("tmid", tmid)
	params.Set("count", strconv.Itoa(count))

	apiURL := fmt.Sprintf("%s/api/v1/chat.getThreadMessages?%s", a.baseURL, params.Encode())

	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		a.logger.ErrorContext(ctx, "error creating thread history request", "error", err)
		return nil
	}

	resp, err := a.doRequest(req)
	if err != nil {
		a.logger.ErrorContext(ctx, "error fetching thread history", "error", err)
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		a.logger.ErrorContext(ctx, "failed to fetch thread history", "status", resp.StatusCode)
		return nil
	}

	var result struct {
		Messages []struct {
			ID  string `json:"_id"`
			Msg string `json:"msg"`
			Rid string `json:"rid"`
			Ts  string `json:"ts"`
			U   struct {
				ID       string `json:"_id"`
				Username string `json:"username"`
				Name     string `json:"name"`
			} `json:"u"`
		} `json:"messages"`
		Success bool `json:"success"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		a.logger.ErrorContext(ctx, "error decoding thread history", "error", err)
		return nil
	}

	if !result.Success {
		a.logger.ErrorContext(ctx, "thread history API call failed")
		return nil
	}

	// Start with the original message
	messages := []Message{*originalMsg}

	// Add thread replies
	threadMessages := parseAPIMessages(result.Messages)
	for i := range threadMessages {
		threadMessages[i].ThreadID = tmid
	}
	messages = append(messages, threadMessages...)

	a.logger.DebugContext(ctx, "fetched thread history", "tmid", tmid, "count", len(messages))
	return messages
}

// EnsureDMRoom creates or retrieves a DM room with the given username
// Returns the room ID for the DM conversation
func (a *APIClient) EnsureDMRoom(ctx context.Context, username string) (string, error) {
	url := fmt.Sprintf("%s/api/v1/im.create", a.baseURL)
	payload := fmt.Sprintf(`{"username":"%s"}`, username)

	req, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(payload))
	if err != nil {
		return "", err
	}

	req.Header.Set("Content-Type", "application/json")
	resp, err := a.doRequest(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to create/get DM: status %d", resp.StatusCode)
	}

	var result struct {
		Room struct {
			ID string `json:"_id"`
		} `json:"room"`
		Success bool `json:"success"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	if !result.Success {
		return "", fmt.Errorf("im.create failed")
	}

	a.logger.DebugContext(ctx, "ensured DM room", "username", username, "roomId", result.Room.ID)
	return result.Room.ID, nil
}

// ResolveChannel resolves a channel name (with or without #) to a room ID
// Supports both public channels and private groups
func (a *APIClient) ResolveChannel(ctx context.Context, channelName string) (string, error) {
	// Strip leading # if present
	channelName = strings.TrimPrefix(channelName, "#")

	// Try public channel first
	params := url.Values{}
	params.Set("roomName", channelName)
	url := fmt.Sprintf("%s/api/v1/channels.info?%s", a.baseURL, params.Encode())

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}

	resp, err := a.doRequest(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		var result struct {
			Channel struct {
				ID string `json:"_id"`
			} `json:"channel"`
			Success bool `json:"success"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return "", err
		}
		if result.Success {
			a.logger.DebugContext(ctx, "resolved channel", "name", channelName, "roomId", result.Channel.ID)
			return result.Channel.ID, nil
		}
	}

	// Try private group
	url = fmt.Sprintf("%s/api/v1/groups.info?%s", a.baseURL, params.Encode())
	req, err = http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}

	resp, err = a.doRequest(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		var result struct {
			Group struct {
				ID string `json:"_id"`
			} `json:"group"`
			Success bool `json:"success"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return "", err
		}
		if result.Success {
			a.logger.DebugContext(ctx, "resolved group", "name", channelName, "roomId", result.Group.ID)
			return result.Group.ID, nil
		}
	}

	return "", fmt.Errorf("channel not found: %s", channelName)
}
