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

func TestAPIClient_FetchUsername(t *testing.T) {
	tests := []struct {
		name         string
		roundTripper testutil.RoundTripperFunc
		wantUsername string
		wantErr      bool
	}{
		{
			name: "success",
			roundTripper: func(r *http.Request) (*http.Response, error) {
				// Verify auth headers
				if r.Header.Get("X-Auth-Token") != "test-token" {
					t.Error("missing X-Auth-Token header")
				}
				if r.Header.Get("X-User-Id") != "test-user" {
					t.Error("missing X-User-Id header")
				}
				if !strings.HasSuffix(r.URL.Path, "/api/v1/me") {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}

				resp := map[string]interface{}{
					"username": "testbot",
					"success":  true,
				}
				body, _ := json.Marshal(resp)
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader(body)),
					Header:     make(http.Header),
				}, nil
			},
			wantUsername: "testbot",
			wantErr:      false,
		},
		{
			name: "non_200_status",
			roundTripper: func(r *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusUnauthorized,
					Body:       io.NopCloser(bytes.NewReader([]byte("{}"))),
					Header:     make(http.Header),
				}, nil
			},
			wantUsername: "",
			wantErr:      true,
		},
		{
			name: "invalid_json",
			roundTripper: func(r *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader([]byte("invalid json"))),
					Header:     make(http.Header),
				}, nil
			},
			wantUsername: "",
			wantErr:      true,
		},
		{
			name: "success_false",
			roundTripper: func(r *http.Request) (*http.Response, error) {
				resp := map[string]interface{}{
					"username": "testbot",
					"success":  false,
				}
				body, _ := json.Marshal(resp)
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader(body)),
					Header:     make(http.Header),
				}, nil
			},
			wantUsername: "",
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &http.Client{Transport: tt.roundTripper}
			api := bot.NewAPIClient("https://test.example.com", "test-user", "test-token", client, testutil.NewTestLogger(t))

			got, err := api.FetchUsername()

			if (err != nil) != tt.wantErr {
				t.Fatalf("FetchUsername() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.wantUsername {
				t.Errorf("FetchUsername() = %q, want %q", got, tt.wantUsername)
			}
		})
	}
}

func TestAPIClient_SetStatusOnline(t *testing.T) {
	tests := []struct {
		name         string
		roundTripper testutil.RoundTripperFunc
		wantErr      bool
	}{
		{
			name: "success",
			roundTripper: func(r *http.Request) (*http.Response, error) {
				if r.Method != "POST" {
					t.Errorf("Method = %s, want POST", r.Method)
				}
				if !strings.HasSuffix(r.URL.Path, "/api/v1/users.setStatus") {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				if r.Header.Get("Content-Type") != "application/json" {
					t.Error("missing Content-Type: application/json")
				}

				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader([]byte("{}"))),
					Header:     make(http.Header),
				}, nil
			},
			wantErr: false,
		},
		{
			name: "non_200_status",
			roundTripper: func(r *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusBadRequest,
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

			err := api.SetStatusOnline()

			if (err != nil) != tt.wantErr {
				t.Fatalf("SetStatusOnline() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestAPIClient_GetSubscriptions(t *testing.T) {
	tests := []struct {
		name         string
		roundTripper testutil.RoundTripperFunc
		wantRooms    int
		wantDMs      int
		wantErr      bool
	}{
		{
			name: "success_with_dms",
			roundTripper: func(r *http.Request) (*http.Response, error) {
				resp := map[string]interface{}{
					"update": []map[string]interface{}{
						{"rid": "room1", "t": "p"},
						{"rid": "room2", "t": "d"},
						{"rid": "room3", "t": "c"},
						{"rid": "room4", "t": "d"},
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
			wantRooms: 4,
			wantDMs:   2,
			wantErr:   false,
		},
		{
			name: "empty_subscriptions",
			roundTripper: func(r *http.Request) (*http.Response, error) {
				resp := map[string]interface{}{
					"update":  []map[string]interface{}{},
					"success": true,
				}
				body, _ := json.Marshal(resp)
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader(body)),
					Header:     make(http.Header),
				}, nil
			},
			wantRooms: 0,
			wantDMs:   0,
			wantErr:   false,
		},
		{
			name: "non_200_status",
			roundTripper: func(r *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusInternalServerError,
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

			rooms, dmRooms, err := api.GetSubscriptions()

			if (err != nil) != tt.wantErr {
				t.Fatalf("GetSubscriptions() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if len(rooms) != tt.wantRooms {
				t.Errorf("got %d rooms, want %d", len(rooms), tt.wantRooms)
			}
			if len(dmRooms) != tt.wantDMs {
				t.Errorf("got %d DM rooms, want %d", len(dmRooms), tt.wantDMs)
			}
		})
	}
}

func TestAPIClient_PostMessage(t *testing.T) {
	tests := []struct {
		name         string
		roomID       string
		text         string
		tmid         string
		roundTripper testutil.RoundTripperFunc
		wantMsgID    string
		wantErr      bool
	}{
		{
			name:   "success_without_thread",
			roomID: "room123",
			text:   "Hello world",
			tmid:   "",
			roundTripper: func(r *http.Request) (*http.Response, error) {
				// Verify request
				var payload map[string]interface{}
				json.NewDecoder(r.Body).Decode(&payload)
				if payload["roomId"] != "room123" {
					t.Errorf("roomId = %v, want room123", payload["roomId"])
				}
				if payload["text"] != "Hello world" {
					t.Errorf("text = %v, want Hello world", payload["text"])
				}
				if _, hasTmid := payload["tmid"]; hasTmid {
					t.Error("tmid should not be present")
				}

				resp := map[string]interface{}{
					"message": map[string]interface{}{
						"_id": "msg789",
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
			wantMsgID: "msg789",
			wantErr:   false,
		},
		{
			name:   "success_with_thread",
			roomID: "room123",
			text:   "Reply",
			tmid:   "thread456",
			roundTripper: func(r *http.Request) (*http.Response, error) {
				var payload map[string]interface{}
				json.NewDecoder(r.Body).Decode(&payload)
				if payload["tmid"] != "thread456" {
					t.Errorf("tmid = %v, want thread456", payload["tmid"])
				}

				resp := map[string]interface{}{
					"message": map[string]interface{}{
						"_id": "msg999",
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
			wantMsgID: "msg999",
			wantErr:   false,
		},
		{
			name:   "non_200_status",
			roomID: "room123",
			text:   "test",
			tmid:   "",
			roundTripper: func(r *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusBadRequest,
					Body:       io.NopCloser(bytes.NewReader([]byte(`{"error":"bad request"}`))),
					Header:     make(http.Header),
				}, nil
			},
			wantMsgID: "",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &http.Client{Transport: tt.roundTripper}
			api := bot.NewAPIClient("https://test.example.com", "test-user", "test-token", client, testutil.NewTestLogger(t))

			msgID, err := api.PostMessage(context.Background(), tt.roomID, tt.text, tt.tmid)

			if (err != nil) != tt.wantErr {
				t.Fatalf("PostMessage() error = %v, wantErr %v", err, tt.wantErr)
			}
			if msgID != tt.wantMsgID {
				t.Errorf("PostMessage() = %q, want %q", msgID, tt.wantMsgID)
			}
		})
	}
}

func TestAPIClient_UpdateMessage(t *testing.T) {
	tests := []struct {
		name         string
		roundTripper testutil.RoundTripperFunc
		wantErr      bool
	}{
		{
			name: "success",
			roundTripper: func(r *http.Request) (*http.Response, error) {
				// Verify payload
				var payload map[string]interface{}
				json.NewDecoder(r.Body).Decode(&payload)
				if payload["msgId"] != "msg123" {
					t.Errorf("msgId = %v, want msg123", payload["msgId"])
				}
				if payload["text"] != "Updated text" {
					t.Errorf("text = %v, want Updated text", payload["text"])
				}

				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader([]byte(`{"success":true}`))),
					Header:     make(http.Header),
				}, nil
			},
			wantErr: false,
		},
		{
			name: "non_200_status",
			roundTripper: func(r *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusForbidden,
					Body:       io.NopCloser(bytes.NewReader([]byte(`{"error":"forbidden"}`))),
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

			err := api.UpdateMessage(context.Background(), "room456", "msg123", "Updated text")

			if (err != nil) != tt.wantErr {
				t.Fatalf("UpdateMessage() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestAPIClient_FetchHistory(t *testing.T) {
	tests := []struct {
		name         string
		roundTripper testutil.RoundTripperFunc
		wantCount    int
	}{
		{
			name: "success_with_messages",
			roundTripper: func(r *http.Request) (*http.Response, error) {
				resp := map[string]interface{}{
					"messages": []map[string]interface{}{
						{
							"_id": "msg1",
							"msg": "Hello",
							"rid": "room1",
							"ts":  "1609459200000",
							"u": map[string]interface{}{
								"_id":      "user1",
								"username": "user1",
								"name":     "User One",
							},
						},
						{
							"_id": "msg2",
							"msg": "World",
							"rid": "room1",
							"ts":  "1609459300000",
							"u": map[string]interface{}{
								"_id":      "user2",
								"username": "user2",
								"name":     "User Two",
							},
						},
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
			wantCount: 2,
		},
		{
			name: "empty_history",
			roundTripper: func(r *http.Request) (*http.Response, error) {
				resp := map[string]interface{}{
					"messages": []map[string]interface{}{},
					"success":  true,
				}
				body, _ := json.Marshal(resp)
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader(body)),
					Header:     make(http.Header),
				}, nil
			},
			wantCount: 0,
		},
		{
			name: "non_200_status",
			roundTripper: func(r *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusNotFound,
					Body:       io.NopCloser(bytes.NewReader([]byte("{}"))),
					Header:     make(http.Header),
				}, nil
			},
			wantCount: 0, // Returns nil on error
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &http.Client{Transport: tt.roundTripper}
			api := bot.NewAPIClient("https://test.example.com", "test-user", "test-token", client, testutil.NewTestLogger(t))

			messages := api.FetchHistory(context.Background(), "room1", 10)

			if len(messages) != tt.wantCount {
				t.Errorf("FetchHistory() returned %d messages, want %d", len(messages), tt.wantCount)
			}

			// Verify chronological order
			for i := 1; i < len(messages); i++ {
				if messages[i].Timestamp.Before(messages[i-1].Timestamp) {
					t.Errorf("messages not in chronological order at index %d", i)
				}
			}
		})
	}
}

func TestAPIClient_FetchMessage(t *testing.T) {
	tests := []struct {
		name         string
		roundTripper testutil.RoundTripperFunc
		wantMessage  *bot.Message
		wantNil      bool
	}{
		{
			name: "success",
			roundTripper: func(r *http.Request) (*http.Response, error) {
				if !strings.Contains(r.URL.Path, "/api/v1/chat.getMessage") {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				if !strings.Contains(r.URL.RawQuery, "msgId=msg123") {
					t.Errorf("missing msgId query param: %s", r.URL.RawQuery)
				}

				resp := map[string]interface{}{
					"message": map[string]interface{}{
						"_id": "msg123",
						"msg": "Hello",
						"rid": "room1",
						"ts":  "1609459200000",
						"u": map[string]interface{}{
							"_id":      "user1",
							"username": "user1",
							"name":     "User One",
						},
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
			wantMessage: &bot.Message{
				ID:     "msg123",
				Text:   "Hello",
				RoomID: "room1",
			},
			wantNil: false,
		},
		{
			name: "non_200_status",
			roundTripper: func(r *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusNotFound,
					Body:       io.NopCloser(bytes.NewReader([]byte("{}"))),
					Header:     make(http.Header),
				}, nil
			},
			wantNil: true,
		},
		{
			name: "malformed_json",
			roundTripper: func(r *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader([]byte("invalid json"))),
					Header:     make(http.Header),
				}, nil
			},
			wantNil: true,
		},
		{
			name: "success_false",
			roundTripper: func(r *http.Request) (*http.Response, error) {
				resp := map[string]interface{}{
					"message": map[string]interface{}{
						"_id": "msg123",
					},
					"success": false,
				}
				body, _ := json.Marshal(resp)
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader(body)),
					Header:     make(http.Header),
				}, nil
			},
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &http.Client{Transport: tt.roundTripper}
			api := bot.NewAPIClient("https://test.example.com", "test-user", "test-token", client, testutil.NewTestLogger(t))

			msg := api.FetchMessage(context.Background(), "msg123")

			if tt.wantNil {
				if msg != nil {
					t.Errorf("FetchMessage() = %+v, want nil", msg)
				}
				return
			}

			if msg == nil {
				t.Fatal("FetchMessage() = nil, want message")
			}

			if msg.ID != tt.wantMessage.ID {
				t.Errorf("ID = %q, want %q", msg.ID, tt.wantMessage.ID)
			}
			if msg.Text != tt.wantMessage.Text {
				t.Errorf("Text = %q, want %q", msg.Text, tt.wantMessage.Text)
			}
			if msg.RoomID != tt.wantMessage.RoomID {
				t.Errorf("RoomID = %q, want %q", msg.RoomID, tt.wantMessage.RoomID)
			}
		})
	}
}

func TestAPIClient_FetchThreadHistory(t *testing.T) {
	tests := []struct {
		name         string
		roundTripper testutil.RoundTripperFunc
		wantCount    int
		wantNil      bool
		validate     func(*testing.T, []bot.Message)
	}{
		{
			name: "success_with_starter_and_replies",
			roundTripper: func(r *http.Request) (*http.Response, error) {
				// First call: FetchMessage for starter
				if strings.Contains(r.URL.Path, "/api/v1/chat.getMessage") {
					resp := map[string]interface{}{
						"message": map[string]interface{}{
							"_id": "thread-start",
							"msg": "Thread starter",
							"rid": "room1",
							"ts":  "1609459200000",
							"u": map[string]interface{}{
								"_id":      "user1",
								"username": "user1",
								"name":     "User One",
							},
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

				// Second call: getThreadMessages for replies
				if strings.Contains(r.URL.Path, "/api/v1/chat.getThreadMessages") {
					resp := map[string]interface{}{
						"messages": []map[string]interface{}{
							{
								"_id": "reply1",
								"msg": "First reply",
								"rid": "room1",
								"ts":  "1609459300000",
								"u": map[string]interface{}{
									"_id":      "user2",
									"username": "user2",
									"name":     "User Two",
								},
							},
							{
								"_id": "reply2",
								"msg": "Second reply",
								"rid": "room1",
								"ts":  "1609459400000",
								"u": map[string]interface{}{
									"_id":      "user3",
									"username": "user3",
									"name":     "User Three",
								},
							},
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
			wantCount: 3, // starter + 2 replies
			validate: func(t *testing.T, messages []bot.Message) {
				t.Helper()
				// Verify starter is first
				if messages[0].ID != "thread-start" {
					t.Errorf("first message ID = %q, want thread-start", messages[0].ID)
				}
				if messages[0].ThreadID != "" {
					t.Errorf("starter ThreadID = %q, want empty", messages[0].ThreadID)
				}

				// Verify replies have ThreadID set
				for i := 1; i < len(messages); i++ {
					if messages[i].ThreadID != "thread-start" {
						t.Errorf("reply[%d] ThreadID = %q, want thread-start", i-1, messages[i].ThreadID)
					}
				}

				// Verify chronological order
				for i := 1; i < len(messages); i++ {
					if messages[i].Timestamp.Before(messages[i-1].Timestamp) {
						t.Errorf("messages not in chronological order at index %d", i)
					}
				}
			},
		},
		{
			name: "starter_fetch_fails",
			roundTripper: func(r *http.Request) (*http.Response, error) {
				// Fail on starter message fetch
				if strings.Contains(r.URL.Path, "/api/v1/chat.getMessage") {
					return &http.Response{
						StatusCode: http.StatusNotFound,
						Body:       io.NopCloser(bytes.NewReader([]byte("{}"))),
						Header:     make(http.Header),
					}, nil
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader([]byte(`{"messages":[],"success":true}`))),
					Header:     make(http.Header),
				}, nil
			},
			wantNil: true,
		},
		{
			name: "thread_replies_fetch_fails",
			roundTripper: func(r *http.Request) (*http.Response, error) {
				// Starter succeeds
				if strings.Contains(r.URL.Path, "/api/v1/chat.getMessage") {
					resp := map[string]interface{}{
						"message": map[string]interface{}{
							"_id": "thread-start",
							"msg": "Thread starter",
							"rid": "room1",
							"ts":  "1609459200000",
							"u": map[string]interface{}{
								"_id":      "user1",
								"username": "user1",
								"name":     "User One",
							},
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

				// Thread replies fetch fails
				return &http.Response{
					StatusCode: http.StatusInternalServerError,
					Body:       io.NopCloser(bytes.NewReader([]byte("{}"))),
					Header:     make(http.Header),
				}, nil
			},
			wantNil: true,
		},
		{
			name: "empty_thread",
			roundTripper: func(r *http.Request) (*http.Response, error) {
				// Starter succeeds
				if strings.Contains(r.URL.Path, "/api/v1/chat.getMessage") {
					resp := map[string]interface{}{
						"message": map[string]interface{}{
							"_id": "thread-start",
							"msg": "Thread starter",
							"rid": "room1",
							"ts":  "1609459200000",
							"u": map[string]interface{}{
								"_id":      "user1",
								"username": "user1",
								"name":     "User One",
							},
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

				// No replies
				resp := map[string]interface{}{
					"messages": []map[string]interface{}{},
					"success":  true,
				}
				body, _ := json.Marshal(resp)
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader(body)),
					Header:     make(http.Header),
				}, nil
			},
			wantCount: 1, // Just the starter
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &http.Client{Transport: tt.roundTripper}
			api := bot.NewAPIClient("https://test.example.com", "test-user", "test-token", client, testutil.NewTestLogger(t))

			messages := api.FetchThreadHistory(context.Background(), "thread-start", 10)

			if tt.wantNil {
				if messages != nil {
					t.Errorf("FetchThreadHistory() = %+v, want nil", messages)
				}
				return
			}

			if len(messages) != tt.wantCount {
				t.Errorf("FetchThreadHistory() returned %d messages, want %d", len(messages), tt.wantCount)
			}

			if tt.validate != nil {
				tt.validate(t, messages)
			}
		})
	}
}
