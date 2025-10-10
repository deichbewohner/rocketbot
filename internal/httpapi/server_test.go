package httpapi_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deichbewohner/rocketbot/bot"
	"github.com/deichbewohner/rocketbot/bot/testutil"
	"github.com/deichbewohner/rocketbot/internal/httpapi"
)

func TestServer_Authentication(t *testing.T) {
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
			logger := testutil.NewTestLogger(t)
			srv := httpapi.NewServer(map[string]*bot.Client{}, map[string]string{tt.slug: tt.configuredAuth}, logger)

			// if a token is configured, register a dummy bot client so that auth can pass
			if tt.configuredAuth != "" {
				client := bot.NewClient("https://test.local", "user1", "token1", "BOT1", nil, false, false, logger, "")
				// mutate internal map via a new server to include bot
				srv = httpapi.NewServer(map[string]*bot.Client{tt.slug: client}, map[string]string{tt.slug: tt.configuredAuth}, logger)
			}

			payload := map[string]any{"target": map[string]string{"username": "alice"}, "text": "Hello"}
			payloadBytes, _ := json.Marshal(payload)

			req := httptest.NewRequest("POST", "/api/v1/bots/"+tt.slug+"/send", bytes.NewReader(payloadBytes))
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			w := httptest.NewRecorder()

			httpapiServerHandler := srv.Handler()
			httpapiServerHandler.ServeHTTP(w, req)

			if w.Code != tt.wantStatusCode {
				t.Errorf("status code = %d, want %d", w.Code, tt.wantStatusCode)
			}

			if tt.wantErrMsg != "" {
				var resp map[string]any
				json.NewDecoder(w.Body).Decode(&resp)
				if !strings.Contains(resp["error"].(string), tt.wantErrMsg) {
					t.Errorf("error = %q, want to contain %q", resp["error"], tt.wantErrMsg)
				}
			}
		})
	}
}

func TestServer_Health(t *testing.T) {
	srv := httpapi.NewServer(nil, nil, slog.Default())
	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status code = %d, want %d", w.Code, http.StatusOK)
	}
	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "ok" {
		t.Errorf("status = %q, want %q", resp["status"], "ok")
	}
}

func TestServer_LoggingMiddleware(t *testing.T) {
	logger, buf := testutil.NewCaptureLogger(t)
	srv := httpapi.NewServer(nil, nil, logger)

	req := httptest.NewRequest("POST", "/api/v1/bots/BOT1/send", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	logOutput := buf.String()
	if !strings.Contains(logOutput, "HTTP request") {
		t.Errorf("expected log output to contain 'HTTP request', got: %s", logOutput)
	}
	if !strings.Contains(logOutput, "POST") {
		t.Errorf("expected log output to contain 'POST', got: %s", logOutput)
	}
}

func TestServer_MaxBytesReader(t *testing.T) {
	logger := testutil.NewTestLogger(t)
	// mock API calls to avoid network
	mockRT := testutil.RoundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Body:       io.NopCloser(strings.NewReader("{}")),
			Header:     make(http.Header),
		}, nil
	})
	mockHTTPClient := &http.Client{Transport: mockRT}

	client := bot.NewClientWithAPI(
		bot.NewAPIClient("https://test.local", "user1", "token1", mockHTTPClient, logger),
		"BOT1", nil, false, false, logger, "",
	)
	srv := httpapi.NewServer(map[string]*bot.Client{"alerts": client}, map[string]string{"alerts": "secret"}, logger)

	// Create a payload larger than 1MB
	largePayload := map[string]any{
		"target": map[string]string{"username": "alice"},
		"text":   strings.Repeat("x", 2*1024*1024), // 2MB of text
	}
	payloadBytes, _ := json.Marshal(largePayload)

	req := httptest.NewRequest("POST", "/api/v1/bots/alerts/send", bytes.NewReader(payloadBytes))
	req.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()

	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status code = %d, want %d (should reject large payload)", w.Code, http.StatusBadRequest)
	}
}

// Multiple bots and token isolation
func TestServer_MultipleBots(t *testing.T) {
	logger := testutil.NewTestLogger(t)
	mockRT := testutil.RoundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Body:       io.NopCloser(strings.NewReader("{}")),
			Header:     make(http.Header),
		}, nil
	})
	mockHTTPClient := &http.Client{Transport: mockRT}

	srv := httpapi.NewServer(
		map[string]*bot.Client{
			"alerts":        bot.NewClientWithAPI(bot.NewAPIClient("https://chat1.local", "user1", "token1", mockHTTPClient, logger), "BOT1", nil, false, false, logger, ""),
			"notifications": bot.NewClientWithAPI(bot.NewAPIClient("https://chat2.local", "user2", "token2", mockHTTPClient, logger), "BOT2", nil, false, false, logger, ""),
		},
		map[string]string{
			"alerts":        "secret1",
			"notifications": "secret2",
		},
		logger,
	)

	payload := map[string]any{"target": map[string]string{"username": "alice"}, "text": "Hello"}
	payloadBytes, _ := json.Marshal(payload)

	// Wrong token for notifications
	req := httptest.NewRequest("POST", "/api/v1/bots/notifications/send", bytes.NewReader(payloadBytes))
	req.Header.Set("Authorization", "Bearer secret1")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status code = %d, want %d (alerts token should not work for notifications)", w.Code, http.StatusUnauthorized)
	}

	// Correct token
	req = httptest.NewRequest("POST", "/api/v1/bots/notifications/send", bytes.NewReader(payloadBytes))
	req.Header.Set("Authorization", "Bearer secret2")
	w = httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code == http.StatusUnauthorized {
		t.Error("status should not be Unauthorized with correct token")
	}
}

func TestServer_BotWithoutAPIToken(t *testing.T) {
	logger := testutil.NewTestLogger(t)
	mockRT := testutil.RoundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Body:       io.NopCloser(strings.NewReader("{}")),
			Header:     make(http.Header),
		}, nil
	})
	mockHTTPClient := &http.Client{Transport: mockRT}

	// support-bot is created but deliberately not added to server tokens
	srv := httpapi.NewServer(
		map[string]*bot.Client{
			"alerts": bot.NewClientWithAPI(bot.NewAPIClient("https://chat1.local", "user1", "token1", mockHTTPClient, logger), "BOT1", nil, false, false, logger, ""),
		},
		map[string]string{
			"alerts": "secret1",
			// support-bot has no token configured
		},
		logger,
	)

	payload := map[string]any{"target": map[string]string{"username": "alice"}, "text": "Hello"}
	payloadBytes, _ := json.Marshal(payload)

	req := httptest.NewRequest("POST", "/api/v1/bots/support-bot/send", bytes.NewReader(payloadBytes))
	req.Header.Set("Authorization", "Bearer some-token")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status code = %d, want %d (support-bot should not be accessible without API token)", w.Code, http.StatusUnauthorized)
	}

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if !strings.Contains(resp["error"].(string), "unauthorized") {
		t.Errorf("error = %q, want to contain 'unauthorized'", resp["error"])
	}
}
