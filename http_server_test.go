package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deichbewohner/rocketbot/bot"
	"github.com/deichbewohner/rocketbot/bot/testutil"
)

func TestValidateTarget(t *testing.T) {
	tests := []struct {
		name    string
		target  Target
		wantErr bool
		errMsg  string
	}{
		{
			name:    "valid_username",
			target:  Target{Username: "alice"},
			wantErr: false,
		},
		{
			name:    "valid_channel",
			target:  Target{Channel: "general"},
			wantErr: false,
		},
		{
			name:    "valid_roomId",
			target:  Target{RoomID: "abc123"},
			wantErr: false,
		},
		{
			name:    "empty_target",
			target:  Target{},
			wantErr: true,
			errMsg:  "must specify username, channel, or roomId",
		},
		{
			name:    "username_and_channel",
			target:  Target{Username: "alice", Channel: "general"},
			wantErr: true,
			errMsg:  "only one",
		},
		{
			name:    "username_and_roomId",
			target:  Target{Username: "alice", RoomID: "abc123"},
			wantErr: true,
			errMsg:  "only one",
		},
		{
			name:    "channel_and_roomId",
			target:  Target{Channel: "general", RoomID: "abc123"},
			wantErr: true,
			errMsg:  "only one",
		},
		{
			name:    "all_three_fields",
			target:  Target{Username: "alice", Channel: "general", RoomID: "abc123"},
			wantErr: true,
			errMsg:  "only one",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTarget(tt.target)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				} else if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("error = %q, want to contain %q", err.Error(), tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestHTTPServer_HandleSend_ValidationErrors(t *testing.T) {
	tests := []struct {
		name           string
		payload        interface{}
		wantStatusCode int
		wantErrMsg     string
	}{
		{
			name:           "missing_text",
			payload:        map[string]interface{}{"target": map[string]string{"username": "alice"}},
			wantStatusCode: http.StatusBadRequest,
			wantErrMsg:     "text is required",
		},
		{
			name:           "empty_text",
			payload:        map[string]interface{}{"target": map[string]string{"username": "alice"}, "text": ""},
			wantStatusCode: http.StatusBadRequest,
			wantErrMsg:     "text is required",
		},
		{
			name:           "empty_target_object",
			payload:        map[string]interface{}{"target": map[string]string{}, "text": "Hello"},
			wantStatusCode: http.StatusBadRequest,
			wantErrMsg:     "target must specify username, channel, or roomId",
		},
		{
			name:           "target_with_multiple_fields",
			payload:        map[string]interface{}{"target": map[string]string{"username": "alice", "channel": "general"}, "text": "Hello"},
			wantStatusCode: http.StatusBadRequest,
			wantErrMsg:     "target must specify only one",
		},
		{
			name:           "invalid_json",
			payload:        "not json",
			wantStatusCode: http.StatusBadRequest,
			wantErrMsg:     "invalid request body",
		},
		{
			name:           "unknown_fields",
			payload:        map[string]interface{}{"target": map[string]string{"username": "alice"}, "text": "hi", "unknown": "field"},
			wantStatusCode: http.StatusBadRequest,
			wantErrMsg:     "invalid request body",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create minimal server with slug
			server := &HTTPServer{
				bots:   make(map[string]*bot.Client),
				tokens: map[string]string{"alerts": "secret"},
				logger: testutil.NewTestLogger(t),
			}

			// Create a fake client (will be looked up but not used for validation errors)
			client := bot.NewClient("https://test.local", "user1", "token1", "BOT1",
				nil, false, false, testutil.NewTestLogger(t), "")
			server.bots["alerts"] = client

			// Marshal payload
			var payloadBytes []byte
			if str, ok := tt.payload.(string); ok {
				payloadBytes = []byte(str)
			} else {
				payloadBytes, _ = json.Marshal(tt.payload)
			}

			req := httptest.NewRequest("POST", "/api/v1/bots/alerts/send", bytes.NewReader(payloadBytes))
			req.SetPathValue("slug", "alerts")
			req.Header.Set("Authorization", "Bearer secret")
			w := httptest.NewRecorder()

			server.handleSend(w, req)

			if w.Code != tt.wantStatusCode {
				t.Errorf("status code = %d, want %d", w.Code, tt.wantStatusCode)
			}

			// Check error message
			var resp sendResponse
			json.NewDecoder(w.Body).Decode(&resp)
			if !strings.Contains(resp.Error, tt.wantErrMsg) {
				t.Errorf("error message = %q, want to contain %q", resp.Error, tt.wantErrMsg)
			}
		})
	}
}

func TestHTTPServer_HandleSend_BotNotFound(t *testing.T) {
	server := &HTTPServer{
		bots:   make(map[string]*bot.Client),
		tokens: map[string]string{"alerts": "secret", "missing-bot": "secret99"},
		logger: testutil.NewTestLogger(t),
	}

	payload := map[string]interface{}{"target": map[string]string{"username": "alice"}, "text": "Hello"}
	payloadBytes, _ := json.Marshal(payload)

	req := httptest.NewRequest("POST", "/api/v1/bots/missing-bot/send", bytes.NewReader(payloadBytes))
	req.SetPathValue("slug", "missing-bot")
	req.Header.Set("Authorization", "Bearer secret99") // Use correct token for missing-bot
	w := httptest.NewRecorder()

	server.handleSend(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status code = %d, want %d", w.Code, http.StatusNotFound)
	}

	var resp sendResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if !strings.Contains(resp.Error, "bot not found") {
		t.Errorf("error = %q, want to contain 'bot not found'", resp.Error)
	}
}

func TestHTTPServer_Authentication(t *testing.T) {
	tests := []struct {
		name           string
		slug           string
		authHeader     string
		configuredAuth string
		wantStatusCode int
		wantErrMsg     string
	}{
		{
			name:           "missing_authorization_header",
			slug:           "alerts",
			authHeader:     "",
			configuredAuth: "secret123",
			wantStatusCode: http.StatusUnauthorized,
			wantErrMsg:     "missing authorization header",
		},
		{
			name:           "invalid_token",
			slug:           "alerts",
			authHeader:     "Bearer wrongtoken",
			configuredAuth: "secret123",
			wantStatusCode: http.StatusUnauthorized,
			wantErrMsg:     "invalid token",
		},
		{
			name:           "malformed_auth_header",
			slug:           "alerts",
			authHeader:     "NotBearer secret123",
			configuredAuth: "secret123",
			wantStatusCode: http.StatusUnauthorized,
			wantErrMsg:     "invalid token",
		},
		{
			name:           "bot_has_no_token_configured",
			slug:           "notifications",
			authHeader:     "Bearer anything",
			configuredAuth: "",
			wantStatusCode: http.StatusUnauthorized,
			wantErrMsg:     "unauthorized",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := &HTTPServer{
				bots:   make(map[string]*bot.Client),
				tokens: map[string]string{tt.slug: tt.configuredAuth},
				logger: testutil.NewTestLogger(t),
			}

			// Add a bot client if token is configured
			if tt.configuredAuth != "" {
				client := bot.NewClient("https://test.local", "user1", "token1", "BOT1",
					nil, false, false, testutil.NewTestLogger(t), "")
				server.bots[tt.slug] = client
			}

			payload := map[string]interface{}{"target": map[string]string{"username": "alice"}, "text": "Hello"}
			payloadBytes, _ := json.Marshal(payload)

			req := httptest.NewRequest("POST", "/api/v1/bots/"+tt.slug+"/send", bytes.NewReader(payloadBytes))
			req.SetPathValue("slug", tt.slug)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			w := httptest.NewRecorder()

			server.handleSend(w, req)

			if w.Code != tt.wantStatusCode {
				t.Errorf("status code = %d, want %d", w.Code, tt.wantStatusCode)
			}

			if tt.wantErrMsg != "" {
				var resp sendResponse
				json.NewDecoder(w.Body).Decode(&resp)
				if !strings.Contains(resp.Error, tt.wantErrMsg) {
					t.Errorf("error = %q, want to contain %q", resp.Error, tt.wantErrMsg)
				}
			}
		})
	}
}

func TestHTTPServer_HandleHealth(t *testing.T) {
	server := &HTTPServer{
		logger: testutil.NewTestLogger(t),
	}

	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	w := httptest.NewRecorder()

	server.handleHealth(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status code = %d, want %d", w.Code, http.StatusOK)
	}

	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "ok" {
		t.Errorf("status = %q, want %q", resp["status"], "ok")
	}
}

func TestHTTPServer_LoggingMiddleware(t *testing.T) {
	logger, buf := testutil.NewCaptureLogger(t)
	server := &HTTPServer{
		logger: logger,
	}

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := server.loggingMiddleware(nextHandler)

	req := httptest.NewRequest("POST", "/api/v1/bots/BOT1/send", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	// Check that something was logged
	logOutput := buf.String()
	if !strings.Contains(logOutput, "HTTP request") {
		t.Errorf("expected log output to contain 'HTTP request', got: %s", logOutput)
	}
	if !strings.Contains(logOutput, "POST") {
		t.Errorf("expected log output to contain 'POST', got: %s", logOutput)
	}
}

func TestHTTPServer_MaxBytesReader(t *testing.T) {
	server := &HTTPServer{
		bots:   make(map[string]*bot.Client),
		tokens: map[string]string{"alerts": "secret"},
		logger: testutil.NewTestLogger(t),
	}

	client := bot.NewClient("https://test.local", "user1", "token1", "BOT1",
		nil, false, false, testutil.NewTestLogger(t), "")
	server.bots["alerts"] = client

	// Create a payload larger than 1MB
	largePayload := map[string]interface{}{
		"target": map[string]string{"username": "alice"},
		"text":   strings.Repeat("x", 2*1024*1024), // 2MB of text
	}
	payloadBytes, _ := json.Marshal(largePayload)

	req := httptest.NewRequest("POST", "/api/v1/bots/alerts/send", bytes.NewReader(payloadBytes))
	req.SetPathValue("slug", "alerts")
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()

	server.handleSend(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status code = %d, want %d (should reject large payload)", w.Code, http.StatusBadRequest)
	}
}

func TestSendResponse_JSON(t *testing.T) {
	tests := []struct {
		name     string
		response sendResponse
		want     string
	}{
		{
			name: "success_response",
			response: sendResponse{
				Success: true,
				RoomID:  "room123",
			},
			want: `{"success":true,"roomId":"room123"}`,
		},
		{
			name: "error_response",
			response: sendResponse{
				Success: false,
				Error:   "something went wrong",
			},
			want: `{"success":false,"roomId":"","error":"something went wrong"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := json.Marshal(tt.response)
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}

			// Parse both to compare structures (order-independent)
			var wantMap, gotMap map[string]interface{}
			json.Unmarshal([]byte(tt.want), &wantMap)
			json.Unmarshal(got, &gotMap)

			if wantMap["success"] != gotMap["success"] {
				t.Errorf("success mismatch: got %v, want %v", gotMap["success"], wantMap["success"])
			}
		})
	}
}

func TestHTTPServer_WriteJSON(t *testing.T) {
	server := &HTTPServer{
		logger: slog.Default(),
	}

	w := httptest.NewRecorder()
	data := map[string]string{"foo": "bar"}

	server.writeJSON(w, http.StatusCreated, data)

	if w.Code != http.StatusCreated {
		t.Errorf("status code = %d, want %d", w.Code, http.StatusCreated)
	}

	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want %q", ct, "application/json")
	}

	var result map[string]string
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode JSON: %v", err)
	}

	if result["foo"] != "bar" {
		t.Errorf("result[foo] = %q, want %q", result["foo"], "bar")
	}
}

func TestHTTPServer_WriteError(t *testing.T) {
	server := &HTTPServer{
		logger: slog.Default(),
	}

	w := httptest.NewRecorder()
	server.writeError(w, http.StatusForbidden, "access denied")

	if w.Code != http.StatusForbidden {
		t.Errorf("status code = %d, want %d", w.Code, http.StatusForbidden)
	}

	var resp sendResponse
	json.NewDecoder(w.Body).Decode(&resp)

	if resp.Success != false {
		t.Error("expected success to be false")
	}
	if resp.Error != "access denied" {
		t.Errorf("error = %q, want %q", resp.Error, "access denied")
	}
}

// Test that multiple bots can be configured
func TestHTTPServer_MultipleBots(t *testing.T) {
	server := &HTTPServer{
		bots: map[string]*bot.Client{
			"alerts": bot.NewClient("https://chat1.local", "user1", "token1", "BOT1",
				nil, false, false, testutil.NewTestLogger(t), ""),
			"notifications": bot.NewClient("https://chat2.local", "user2", "token2", "BOT2",
				nil, false, false, testutil.NewTestLogger(t), ""),
		},
		tokens: map[string]string{
			"alerts":        "secret1",
			"notifications": "secret2",
		},
		logger: testutil.NewTestLogger(t),
	}

	// Test that alerts token doesn't work for notifications
	payload := map[string]interface{}{"target": map[string]string{"username": "alice"}, "text": "Hello"}
	payloadBytes, _ := json.Marshal(payload)

	req := httptest.NewRequest("POST", "/api/v1/bots/notifications/send", bytes.NewReader(payloadBytes))
	req.SetPathValue("slug", "notifications")
	req.Header.Set("Authorization", "Bearer secret1") // Wrong token
	w := httptest.NewRecorder()

	server.handleSend(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status code = %d, want %d (alerts token should not work for notifications)", w.Code, http.StatusUnauthorized)
	}

	// Now try with correct token
	req = httptest.NewRequest("POST", "/api/v1/bots/notifications/send", bytes.NewReader(payloadBytes))
	req.SetPathValue("slug", "notifications")
	req.Header.Set("Authorization", "Bearer secret2") // Correct token
	w = httptest.NewRecorder()

	server.handleSend(w, req)

	// Should pass auth (but will fail at bot operation since we don't have full mock setup)
	if w.Code == http.StatusUnauthorized {
		t.Error("status should not be Unauthorized with correct token")
	}
}

// Test that bots without API tokens are not accessible via HTTP API
func TestHTTPServer_BotWithoutAPIToken(t *testing.T) {
	// support-bot is created but deliberately not added to the server (simulating no API token)
	server := &HTTPServer{
		bots: map[string]*bot.Client{
			"alerts": bot.NewClient("https://chat1.local", "user1", "token1", "BOT1",
				nil, false, false, testutil.NewTestLogger(t), ""),
			// support-bot intentionally not in the map
		},
		tokens: map[string]string{
			"alerts": "secret1",
			// support-bot has no token configured
		},
		logger: testutil.NewTestLogger(t),
	}

	payload := map[string]interface{}{"target": map[string]string{"username": "alice"}, "text": "Hello"}
	payloadBytes, _ := json.Marshal(payload)

	req := httptest.NewRequest("POST", "/api/v1/bots/support-bot/send", bytes.NewReader(payloadBytes))
	req.SetPathValue("slug", "support-bot")
	req.Header.Set("Authorization", "Bearer some-token")
	w := httptest.NewRecorder()

	server.handleSend(w, req)

	// Should be rejected with unauthorized (no token configured for this bot)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status code = %d, want %d (support-bot should not be accessible without API token)", w.Code, http.StatusUnauthorized)
	}

	var resp sendResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if !strings.Contains(resp.Error, "unauthorized") {
		t.Errorf("error = %q, want to contain 'unauthorized'", resp.Error)
	}
}
