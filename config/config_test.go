package config

import (
	"strings"
	"testing"
)

func TestLoad(t *testing.T) {
	tests := []struct {
		name        string
		envVars     map[string]string
		wantBots    int
		wantErr     bool
		errContains string
		validate    func(*testing.T, *Config)
	}{
		{
			name: "single_bot_with_defaults",
			envVars: map[string]string{
				"BOT1_URL":         "https://chat.example.com",
				"BOT1_USER_ID":     "user123",
				"BOT1_TOKEN":       "token456",
				"BOT1_PARSER_TYPE": "n8n",
			},
			wantBots: 1,
			wantErr:  false,
			validate: func(t *testing.T, cfg *Config) {
				t.Helper()
				bot, _ := cfg.Get(0)
				if bot.URL != "https://chat.example.com" {
					t.Errorf("URL = %q, want %q", bot.URL, "https://chat.example.com")
				}
				if bot.ParserType != "n8n" {
					t.Errorf("ParserType = %q, want %q", bot.ParserType, "n8n")
				}
				if !bot.StreamedOutput {
					t.Error("StreamedOutput = false, want true")
				}
			},
		},
		{
			name: "multiple_bots",
			envVars: map[string]string{
				"BOT1_URL":         "https://chat1.example.com",
				"BOT1_USER_ID":     "user1",
				"BOT1_TOKEN":       "token1",
				"BOT1_PARSER_TYPE": "n8n",
				"BOT2_URL":         "https://chat2.example.com",
				"BOT2_USER_ID":     "user2",
				"BOT2_TOKEN":       "token2",
				"BOT2_PARSER_TYPE": "n8n",
				"BOT3_URL":         "https://chat3.example.com",
				"BOT3_USER_ID":     "user3",
				"BOT3_TOKEN":       "token3",
				"BOT3_PARSER_TYPE": "n8n",
			},
			wantBots: 3,
			wantErr:  false,
			validate: func(t *testing.T, cfg *Config) {
				t.Helper()
				if cfg.Count() != 3 {
					t.Errorf("Count() = %d, want 3", cfg.Count())
				}
				bot2, _ := cfg.Get(1)
				if bot2.UserID != "user2" {
					t.Errorf("Bot 2 UserID = %q, want %q", bot2.UserID, "user2")
				}
			},
		},
		{
			name: "url_trailing_slash_removed",
			envVars: map[string]string{
				"BOT1_URL":         "https://chat.example.com/",
				"BOT1_USER_ID":     "user123",
				"BOT1_TOKEN":       "token456",
				"BOT1_PARSER_TYPE": "n8n",
			},
			wantBots: 1,
			wantErr:  false,
			validate: func(t *testing.T, cfg *Config) {
				t.Helper()
				bot, _ := cfg.Get(0)
				if bot.URL != "https://chat.example.com" {
					t.Errorf("URL = %q, want %q (trailing slash should be removed)", bot.URL, "https://chat.example.com")
				}
			},
		},
		{
			name: "streamed_output_disabled",
			envVars: map[string]string{
				"BOT1_URL":             "https://chat.example.com",
				"BOT1_USER_ID":         "user123",
				"BOT1_TOKEN":           "token456",
				"BOT1_PARSER_TYPE":     "n8n",
				"BOT1_STREAMED_OUTPUT": "false",
			},
			wantBots: 1,
			wantErr:  false,
			validate: func(t *testing.T, cfg *Config) {
				t.Helper()
				bot, _ := cfg.Get(0)
				if bot.StreamedOutput {
					t.Error("StreamedOutput = true, want false")
				}
			},
		},
		{
			name: "streamed_output_zero",
			envVars: map[string]string{
				"BOT1_URL":             "https://chat.example.com",
				"BOT1_USER_ID":         "user123",
				"BOT1_TOKEN":           "token456",
				"BOT1_PARSER_TYPE":     "n8n",
				"BOT1_STREAMED_OUTPUT": "0",
			},
			wantBots: 1,
			wantErr:  false,
			validate: func(t *testing.T, cfg *Config) {
				t.Helper()
				bot, _ := cfg.Get(0)
				if bot.StreamedOutput {
					t.Error("StreamedOutput = true, want false")
				}
			},
		},
		{
			name: "custom_parser_type",
			envVars: map[string]string{
				"BOT1_URL":         "https://chat.example.com",
				"BOT1_USER_ID":     "user123",
				"BOT1_TOKEN":       "token456",
				"BOT1_PARSER_TYPE": "openai",
			},
			wantBots: 1,
			wantErr:  false,
			validate: func(t *testing.T, cfg *Config) {
				t.Helper()
				bot, _ := cfg.Get(0)
				if bot.ParserType != "openai" {
					t.Errorf("ParserType = %q, want %q", bot.ParserType, "openai")
				}
			},
		},
		{
			name: "webhook_url_and_auth",
			envVars: map[string]string{
				"BOT1_URL":          "https://chat.example.com",
				"BOT1_USER_ID":      "user123",
				"BOT1_TOKEN":        "token456",
				"BOT1_PARSER_TYPE":  "n8n",
				"BOT1_WEBHOOK_URL":  "https://webhook.example.com/api",
				"BOT1_WEBHOOK_AUTH": "Bearer secret123",
			},
			wantBots: 1,
			wantErr:  false,
			validate: func(t *testing.T, cfg *Config) {
				t.Helper()
				bot, _ := cfg.Get(0)
				if bot.WebhookURL != "https://webhook.example.com/api" {
					t.Errorf("WebhookURL = %q, want %q", bot.WebhookURL, "https://webhook.example.com/api")
				}
				if bot.WebhookAuth != "Bearer secret123" {
					t.Errorf("WebhookAuth = %q, want %q", bot.WebhookAuth, "Bearer secret123")
				}
			},
		},
		{
			name: "thread_default_enabled",
			envVars: map[string]string{
				"BOT1_URL":            "https://chat.example.com",
				"BOT1_USER_ID":        "user123",
				"BOT1_TOKEN":          "token456",
				"BOT1_PARSER_TYPE":    "n8n",
				"BOT1_THREAD_DEFAULT": "true",
			},
			wantBots: 1,
			wantErr:  false,
			validate: func(t *testing.T, cfg *Config) {
				t.Helper()
				bot, _ := cfg.Get(0)
				if !bot.ThreadDefault {
					t.Error("ThreadDefault = false, want true")
				}
			},
		},
		{
			name: "thread_default_enabled_with_one",
			envVars: map[string]string{
				"BOT1_URL":            "https://chat.example.com",
				"BOT1_USER_ID":        "user123",
				"BOT1_TOKEN":          "token456",
				"BOT1_PARSER_TYPE":    "n8n",
				"BOT1_THREAD_DEFAULT": "1",
			},
			wantBots: 1,
			wantErr:  false,
			validate: func(t *testing.T, cfg *Config) {
				t.Helper()
				bot, _ := cfg.Get(0)
				if !bot.ThreadDefault {
					t.Error("ThreadDefault = false, want true")
				}
			},
		},
		{
			name: "thread_default_disabled",
			envVars: map[string]string{
				"BOT1_URL":            "https://chat.example.com",
				"BOT1_USER_ID":        "user123",
				"BOT1_TOKEN":          "token456",
				"BOT1_PARSER_TYPE":    "n8n",
				"BOT1_THREAD_DEFAULT": "false",
			},
			wantBots: 1,
			wantErr:  false,
			validate: func(t *testing.T, cfg *Config) {
				t.Helper()
				bot, _ := cfg.Get(0)
				if bot.ThreadDefault {
					t.Error("ThreadDefault = true, want false")
				}
			},
		},
		{
			name: "thread_default_not_set",
			envVars: map[string]string{
				"BOT1_URL":         "https://chat.example.com",
				"BOT1_USER_ID":     "user123",
				"BOT1_TOKEN":       "token456",
				"BOT1_PARSER_TYPE": "n8n",
			},
			wantBots: 1,
			wantErr:  false,
			validate: func(t *testing.T, cfg *Config) {
				t.Helper()
				bot, _ := cfg.Get(0)
				if bot.ThreadDefault {
					t.Error("ThreadDefault = true, want false (default)")
				}
			},
		},
		{
			name:        "missing_user_id",
			envVars:     map[string]string{"BOT1_URL": "https://chat.example.com", "BOT1_TOKEN": "token456", "BOT1_PARSER_TYPE": "n8n"},
			wantErr:     true,
			errContains: "BOT1_USER_ID is required",
		},
		{
			name:        "missing_token",
			envVars:     map[string]string{"BOT1_URL": "https://chat.example.com", "BOT1_USER_ID": "user123", "BOT1_PARSER_TYPE": "n8n"},
			wantErr:     true,
			errContains: "BOT1_TOKEN is required",
		},
		{
			name:        "missing_parser_type",
			envVars:     map[string]string{"BOT1_URL": "https://chat.example.com", "BOT1_USER_ID": "user123", "BOT1_TOKEN": "token456"},
			wantErr:     true,
			errContains: "BOT1_PARSER_TYPE is required",
		},
		{
			name:        "no_bots_configured",
			envVars:     map[string]string{},
			wantErr:     true,
			errContains: "no bots configured",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear environment and set test vars
			for k, v := range tt.envVars {
				t.Setenv(k, v)
			}

			cfg, err := Load()

			if (err != nil) != tt.wantErr {
				t.Fatalf("Load() error = %v, wantErr %v", err, tt.wantErr)
			}

			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("Load() error = %v, want error containing %q", err, tt.errContains)
				}
				return
			}

			if cfg == nil {
				t.Fatal("Load() returned nil config")
			}

			if cfg.Count() != tt.wantBots {
				t.Errorf("Count() = %d, want %d", cfg.Count(), tt.wantBots)
			}

			if tt.validate != nil {
				tt.validate(t, cfg)
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
