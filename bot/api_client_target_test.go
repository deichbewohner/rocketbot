package bot_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/deichbewohner/rocketbot/bot"
	"github.com/deichbewohner/rocketbot/bot/testutil"
)

func TestAPIClient_EnsureDMRoom(t *testing.T) {
	tests := []struct {
		name         string
		username     string
		roundTripper testutil.RoundTripperFunc
		wantRoomID   string
		wantErr      bool
	}{
		{
			name:     "success",
			username: "alice",
			roundTripper: func(r *http.Request) (*http.Response, error) {
				// Verify request
				if r.Method != "POST" {
					t.Errorf("Method = %s, want POST", r.Method)
				}
				if !strings.HasSuffix(r.URL.Path, "/api/v1/im.create") {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}

				var payload map[string]string
				json.NewDecoder(r.Body).Decode(&payload)
				if payload["username"] != "alice" {
					t.Errorf("username = %q, want %q", payload["username"], "alice")
				}

				resp := map[string]interface{}{
					"room": map[string]interface{}{
						"_id": "dm-alice-123",
					},
					"success": true,
				}
				body, _ := json.Marshal(resp)
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader(body)),
					Header:     make(http.Header),
				}, nil
			},
			wantRoomID: "dm-alice-123",
			wantErr:    false,
		},
		{
			name:     "non_200_status",
			username: "nonexistent",
			roundTripper: func(r *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusNotFound,
					Body:       io.NopCloser(bytes.NewReader([]byte("{}"))),
					Header:     make(http.Header),
				}, nil
			},
			wantErr: true,
		},
		{
			name:     "success_false",
			username: "bob",
			roundTripper: func(r *http.Request) (*http.Response, error) {
				resp := map[string]interface{}{
					"success": false,
				}
				body, _ := json.Marshal(resp)
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader(body)),
					Header:     make(http.Header),
				}, nil
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &http.Client{Transport: tt.roundTripper}
			api := bot.NewAPIClient("https://test.example.com", "test-user", "test-token", client, testutil.NewTestLogger(t))

			roomID, err := api.EnsureDMRoom(context.Background(), tt.username)

			if (err != nil) != tt.wantErr {
				t.Fatalf("EnsureDMRoom() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && roomID != tt.wantRoomID {
				t.Errorf("EnsureDMRoom() = %q, want %q", roomID, tt.wantRoomID)
			}
		})
	}
}

func TestAPIClient_ResolveChannel(t *testing.T) {
	tests := []struct {
		name         string
		channelName  string
		roundTripper testutil.RoundTripperFunc
		wantRoomID   string
		wantErr      bool
	}{
		{
			name:        "public_channel_with_hash",
			channelName: "#general",
			roundTripper: func(r *http.Request) (*http.Response, error) {
				// Should strip # before querying
				if !strings.Contains(r.URL.RawQuery, "roomName=general") {
					t.Errorf("query should contain roomName=general, got: %s", r.URL.RawQuery)
				}

				if strings.Contains(r.URL.Path, "/api/v1/channels.info") {
					resp := map[string]interface{}{
						"channel": map[string]interface{}{
							"_id": "channel-general-123",
						},
						"success": true,
					}
					body, _ := json.Marshal(resp)
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(bytes.NewReader(body)),
						Header:     make(http.Header),
					}, nil
				}

				return &http.Response{
					StatusCode: http.StatusNotFound,
					Body:       io.NopCloser(bytes.NewReader([]byte("{}"))),
					Header:     make(http.Header),
				}, nil
			},
			wantRoomID: "channel-general-123",
			wantErr:    false,
		},
		{
			name:        "public_channel_without_hash",
			channelName: "general",
			roundTripper: func(r *http.Request) (*http.Response, error) {
				if strings.Contains(r.URL.Path, "/api/v1/channels.info") {
					resp := map[string]interface{}{
						"channel": map[string]interface{}{
							"_id": "channel-general-123",
						},
						"success": true,
					}
					body, _ := json.Marshal(resp)
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(bytes.NewReader(body)),
						Header:     make(http.Header),
					}, nil
				}
				return &http.Response{
					StatusCode: http.StatusNotFound,
					Body:       io.NopCloser(bytes.NewReader([]byte("{}"))),
					Header:     make(http.Header),
				}, nil
			},
			wantRoomID: "channel-general-123",
			wantErr:    false,
		},
		{
			name:        "private_group",
			channelName: "#team-private",
			roundTripper: func(r *http.Request) (*http.Response, error) {
				// Fail on channels.info
				if strings.Contains(r.URL.Path, "/api/v1/channels.info") {
					return &http.Response{
						StatusCode: http.StatusNotFound,
						Body:       io.NopCloser(bytes.NewReader([]byte("{}"))),
						Header:     make(http.Header),
					}, nil
				}

				// Succeed on groups.info
				if strings.Contains(r.URL.Path, "/api/v1/groups.info") {
					resp := map[string]interface{}{
						"group": map[string]interface{}{
							"_id": "group-team-private-456",
						},
						"success": true,
					}
					body, _ := json.Marshal(resp)
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(bytes.NewReader(body)),
						Header:     make(http.Header),
					}, nil
				}

				return &http.Response{
					StatusCode: http.StatusNotFound,
					Body:       io.NopCloser(bytes.NewReader([]byte("{}"))),
					Header:     make(http.Header),
				}, nil
			},
			wantRoomID: "group-team-private-456",
			wantErr:    false,
		},
		{
			name:        "channel_not_found",
			channelName: "#nonexistent",
			roundTripper: func(r *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusNotFound,
					Body:       io.NopCloser(bytes.NewReader([]byte("{}"))),
					Header:     make(http.Header),
				}, nil
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &http.Client{Transport: tt.roundTripper}
			api := bot.NewAPIClient("https://test.example.com", "test-user", "test-token", client, testutil.NewTestLogger(t))

			roomID, err := api.ResolveChannel(context.Background(), tt.channelName)

			if (err != nil) != tt.wantErr {
				t.Fatalf("ResolveChannel() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && roomID != tt.wantRoomID {
				t.Errorf("ResolveChannel() = %q, want %q", roomID, tt.wantRoomID)
			}
		})
	}
}
