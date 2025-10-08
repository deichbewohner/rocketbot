package config

import (
	"strings"
	"testing"
)

func TestLoad_SingleBot(t *testing.T) {
	t.Setenv("BOT1_URL", "https://chat.example.com")
	t.Setenv("BOT1_USER_ID", "user123")
	t.Setenv("BOT1_TOKEN", "token456")
	t.Setenv("BOT1_PARSER_TYPE", "n8n")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Count() != 1 {
		t.Errorf("Count() = %d, want 1", cfg.Count())
	}

	bot, _ := cfg.Get(0)
	if bot.URL != "https://chat.example.com" {
		t.Errorf("URL = %q, want %q", bot.URL, "https://chat.example.com")
	}
	if bot.ParserType != "n8n" {
		t.Errorf("ParserType = %q, want %q", bot.ParserType, "n8n")
	}
	if !bot.StreamedOutput {
		t.Error("StreamedOutput = false, want true (default)")
	}
}

func TestLoad_MultipleBots(t *testing.T) {
	t.Setenv("BOT1_URL", "https://chat1.example.com")
	t.Setenv("BOT1_USER_ID", "user1")
	t.Setenv("BOT1_TOKEN", "token1")
	t.Setenv("BOT1_PARSER_TYPE", "n8n")
	t.Setenv("BOT2_URL", "https://chat2.example.com")
	t.Setenv("BOT2_USER_ID", "user2")
	t.Setenv("BOT2_TOKEN", "token2")
	t.Setenv("BOT2_PARSER_TYPE", "n8n")
	t.Setenv("BOT3_URL", "https://chat3.example.com")
	t.Setenv("BOT3_USER_ID", "user3")
	t.Setenv("BOT3_TOKEN", "token3")
	t.Setenv("BOT3_PARSER_TYPE", "n8n")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Count() != 3 {
		t.Errorf("Count() = %d, want 3", cfg.Count())
	}

	bot2, _ := cfg.Get(1)
	if bot2.UserID != "user2" {
		t.Errorf("Bot 2 UserID = %q, want %q", bot2.UserID, "user2")
	}
}

func TestLoad_BooleanFlags(t *testing.T) {
	tests := []struct {
		name              string
		streamedOutput    string // empty = not set
		threadDefault     string // empty = not set
		wantStreamedOut   bool
		wantThreadDefault bool
	}{
		{
			name:              "defaults",
			streamedOutput:    "",
			threadDefault:     "",
			wantStreamedOut:   true,
			wantThreadDefault: false,
		},
		{
			name:              "streamed_output_false",
			streamedOutput:    "false",
			threadDefault:     "",
			wantStreamedOut:   false,
			wantThreadDefault: false,
		},
		{
			name:              "streamed_output_zero",
			streamedOutput:    "0",
			threadDefault:     "",
			wantStreamedOut:   false,
			wantThreadDefault: false,
		},
		{
			name:              "thread_default_true",
			streamedOutput:    "",
			threadDefault:     "true",
			wantStreamedOut:   true,
			wantThreadDefault: true,
		},
		{
			name:              "thread_default_one",
			streamedOutput:    "",
			threadDefault:     "1",
			wantStreamedOut:   true,
			wantThreadDefault: true,
		},
		{
			name:              "both_custom",
			streamedOutput:    "false",
			threadDefault:     "true",
			wantStreamedOut:   false,
			wantThreadDefault: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("BOT1_URL", "https://chat.example.com")
			t.Setenv("BOT1_USER_ID", "user123")
			t.Setenv("BOT1_TOKEN", "token456")
			t.Setenv("BOT1_PARSER_TYPE", "n8n")
			if tt.streamedOutput != "" {
				t.Setenv("BOT1_STREAMED_OUTPUT", tt.streamedOutput)
			}
			if tt.threadDefault != "" {
				t.Setenv("BOT1_THREAD_DEFAULT", tt.threadDefault)
			}

			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}

			bot, _ := cfg.Get(0)
			if bot.StreamedOutput != tt.wantStreamedOut {
				t.Errorf("StreamedOutput = %v, want %v", bot.StreamedOutput, tt.wantStreamedOut)
			}
			if bot.ThreadDefault != tt.wantThreadDefault {
				t.Errorf("ThreadDefault = %v, want %v", bot.ThreadDefault, tt.wantThreadDefault)
			}
		})
	}
}

func TestLoad_URLProcessing(t *testing.T) {
	t.Setenv("BOT1_URL", "https://chat.example.com/")
	t.Setenv("BOT1_USER_ID", "user123")
	t.Setenv("BOT1_TOKEN", "token456")
	t.Setenv("BOT1_PARSER_TYPE", "n8n")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	bot, _ := cfg.Get(0)
	if bot.URL != "https://chat.example.com" {
		t.Errorf("URL = %q, want %q (trailing slash should be removed)", bot.URL, "https://chat.example.com")
	}
}

func TestLoad_CustomParserType(t *testing.T) {
	t.Setenv("BOT1_URL", "https://chat.example.com")
	t.Setenv("BOT1_USER_ID", "user123")
	t.Setenv("BOT1_TOKEN", "token456")
	t.Setenv("BOT1_PARSER_TYPE", "openai")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	bot, _ := cfg.Get(0)
	if bot.ParserType != "openai" {
		t.Errorf("ParserType = %q, want %q", bot.ParserType, "openai")
	}
}

func TestLoad_WebhookConfig(t *testing.T) {
	t.Setenv("BOT1_URL", "https://chat.example.com")
	t.Setenv("BOT1_USER_ID", "user123")
	t.Setenv("BOT1_TOKEN", "token456")
	t.Setenv("BOT1_PARSER_TYPE", "n8n")
	t.Setenv("BOT1_WEBHOOK_URL", "https://webhook.example.com/api")
	t.Setenv("BOT1_WEBHOOK_AUTH", "Bearer secret123")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	bot, _ := cfg.Get(0)
	if bot.WebhookURL != "https://webhook.example.com/api" {
		t.Errorf("WebhookURL = %q, want %q", bot.WebhookURL, "https://webhook.example.com/api")
	}
	if bot.WebhookAuth != "Bearer secret123" {
		t.Errorf("WebhookAuth = %q, want %q", bot.WebhookAuth, "Bearer secret123")
	}
}

func TestLoad_ValidationErrors(t *testing.T) {
	tests := []struct {
		name        string
		envVars     map[string]string
		errContains string
	}{
		{
			name: "missing_user_id",
			envVars: map[string]string{
				"BOT1_URL":         "https://chat.example.com",
				"BOT1_TOKEN":       "token456",
				"BOT1_PARSER_TYPE": "n8n",
			},
			errContains: "BOT1_USER_ID is required",
		},
		{
			name: "missing_token",
			envVars: map[string]string{
				"BOT1_URL":         "https://chat.example.com",
				"BOT1_USER_ID":     "user123",
				"BOT1_PARSER_TYPE": "n8n",
			},
			errContains: "BOT1_TOKEN is required",
		},
		{
			name: "missing_parser_type",
			envVars: map[string]string{
				"BOT1_URL":     "https://chat.example.com",
				"BOT1_USER_ID": "user123",
				"BOT1_TOKEN":   "token456",
			},
			errContains: "BOT1_PARSER_TYPE is required",
		},
		{
			name:        "no_bots_configured",
			envVars:     map[string]string{},
			errContains: "no bots configured",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.envVars {
				t.Setenv(k, v)
			}

			_, err := Load()
			if err == nil {
				t.Fatal("Load() error = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.errContains) {
				t.Errorf("Load() error = %v, want error containing %q", err, tt.errContains)
			}
		})
	}
}

func TestConfig_Get(t *testing.T) {
	cfg := &Config{
		Bots: []BotConfig{
			{URL: "https://bot1.com", UserID: "user1", Token: "token1"},
			{URL: "https://bot2.com", UserID: "user2", Token: "token2"},
		},
	}

	tests := []struct {
		name    string
		index   int
		wantErr bool
		wantURL string
	}{
		{name: "valid_index_0", index: 0, wantErr: false, wantURL: "https://bot1.com"},
		{name: "valid_index_1", index: 1, wantErr: false, wantURL: "https://bot2.com"},
		{name: "negative_index", index: -1, wantErr: true},
		{name: "out_of_bounds", index: 2, wantErr: true},
		{name: "way_out_of_bounds", index: 999, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bot, err := cfg.Get(tt.index)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Get(%d) error = %v, wantErr %v", tt.index, err, tt.wantErr)
			}
			if !tt.wantErr && bot.URL != tt.wantURL {
				t.Errorf("Get(%d).URL = %q, want %q", tt.index, bot.URL, tt.wantURL)
			}
		})
	}
}

func TestConfig_BotName(t *testing.T) {
	cfg := &Config{
		Bots: []BotConfig{
			{URL: "https://bot1.com"},
			{URL: "https://bot2.com"},
			{URL: "https://bot3.com"},
		},
	}

	tests := []struct {
		index int
		want  string
	}{
		{index: 0, want: "BOT1"},
		{index: 1, want: "BOT2"},
		{index: 2, want: "BOT3"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := cfg.BotName(tt.index)
			if got != tt.want {
				t.Errorf("BotName(%d) = %q, want %q", tt.index, got, tt.want)
			}
		})
	}
}
