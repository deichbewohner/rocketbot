package config

import (
	"strings"
	"testing"
)

func TestLoad_SingleBot(t *testing.T) {
	t.Setenv("BOT1_SLUG", "my-bot")
	t.Setenv("BOT1_URL", "https://chat.example.com")
	t.Setenv("BOT1_USER_ID", "user123")
	t.Setenv("BOT1_TOKEN", "token456")
	t.Setenv("BOT1_WEBHOOK_URL", "https://webhook.example.com/api")
	t.Setenv("BOT1_PARSER_TYPE", "n8n")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Count() != 1 {
		t.Errorf("Count() = %d, want 1", cfg.Count())
	}

	bot, _ := cfg.Get(0)
	if bot.Slug != "my-bot" {
		t.Errorf("Slug = %q, want %q", bot.Slug, "my-bot")
	}
	if bot.URL != "https://chat.example.com" {
		t.Errorf("URL = %q, want %q", bot.URL, "https://chat.example.com")
	}
	if bot.GeneratorType != "webhook" {
		t.Errorf("GeneratorType = %q, want %q", bot.GeneratorType, "webhook")
	}
	if bot.ParserType != "n8n" {
		t.Errorf("ParserType = %q, want %q", bot.ParserType, "n8n")
	}
	if !bot.StreamedOutput {
		t.Error("StreamedOutput = false, want true (default)")
	}
	if bot.RenderMode != "detailed" {
		t.Errorf("RenderMode = %q, want %q", bot.RenderMode, "detailed")
	}
}

func TestLoad_MultipleBots(t *testing.T) {
	t.Setenv("BOT1_SLUG", "alerts")
	t.Setenv("BOT1_URL", "https://chat1.example.com")
	t.Setenv("BOT1_USER_ID", "user1")
	t.Setenv("BOT1_TOKEN", "token1")
	t.Setenv("BOT1_WEBHOOK_URL", "https://webhook.example.com/bot1")
	t.Setenv("BOT1_PARSER_TYPE", "n8n")
	t.Setenv("BOT2_SLUG", "notifications")
	t.Setenv("BOT2_URL", "https://chat2.example.com")
	t.Setenv("BOT2_USER_ID", "user2")
	t.Setenv("BOT2_TOKEN", "token2")
	t.Setenv("BOT2_WEBHOOK_URL", "https://webhook.example.com/bot2")
	t.Setenv("BOT2_PARSER_TYPE", "n8n")
	t.Setenv("BOT3_SLUG", "support-bot")
	t.Setenv("BOT3_URL", "https://chat3.example.com")
	t.Setenv("BOT3_USER_ID", "user3")
	t.Setenv("BOT3_TOKEN", "token3")
	t.Setenv("BOT3_WEBHOOK_URL", "https://webhook.example.com/bot3")
	t.Setenv("BOT3_PARSER_TYPE", "n8n")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Count() != 3 {
		t.Errorf("Count() = %d, want 3", cfg.Count())
	}

	bot2, _ := cfg.Get(1)
	if bot2.Slug != "notifications" {
		t.Errorf("Bot 2 Slug = %q, want %q", bot2.Slug, "notifications")
	}
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
			t.Setenv("BOT1_SLUG", "test-bot")
			t.Setenv("BOT1_URL", "https://chat.example.com")
			t.Setenv("BOT1_USER_ID", "user123")
			t.Setenv("BOT1_TOKEN", "token456")
			t.Setenv("BOT1_WEBHOOK_URL", "https://webhook.example.com/api")
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
	t.Setenv("BOT1_SLUG", "test-bot")
	t.Setenv("BOT1_URL", "https://chat.example.com/")
	t.Setenv("BOT1_USER_ID", "user123")
	t.Setenv("BOT1_TOKEN", "token456")
	t.Setenv("BOT1_WEBHOOK_URL", "https://webhook.example.com/api")
	t.Setenv("BOT1_PARSER_TYPE", "n8n")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	bot, _ := cfg.Get(0)
	if bot.URL != "https://chat.example.com" {
		t.Errorf(
			"URL = %q, want %q (trailing slash should be removed)",
			bot.URL,
			"https://chat.example.com",
		)
	}
}

func TestLoad_OpenCodeGenerator_Succeeds(t *testing.T) {
	t.Setenv("BOT1_SLUG", "test-bot")
	t.Setenv("BOT1_URL", "https://chat.example.com")
	t.Setenv("BOT1_USER_ID", "user123")
	t.Setenv("BOT1_TOKEN", "token456")
	t.Setenv("BOT1_GENERATOR_TYPE", "opencode")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	bot, _ := cfg.Get(0)
	if bot.GeneratorType != "opencode" {
		t.Errorf("GeneratorType = %q, want %q", bot.GeneratorType, "opencode")
	}
	if bot.OpenCodeBaseURL != "http://127.0.0.1:4096" {
		t.Errorf("OpenCodeBaseURL = %q, want default", bot.OpenCodeBaseURL)
	}
	if bot.OpenCodePermissionMode != "deny" {
		t.Errorf("OpenCodePermissionMode = %q, want default", bot.OpenCodePermissionMode)
	}
	if bot.OpenCodeSessionDir != "" {
		t.Errorf("OpenCodeSessionDir = %q, want empty", bot.OpenCodeSessionDir)
	}
	if bot.ParserType != "" {
		t.Errorf("ParserType = %q, want empty", bot.ParserType)
	}
	if bot.WebhookURL != "" {
		t.Errorf("WebhookURL = %q, want empty", bot.WebhookURL)
	}
}

func TestLoad_OpenCodeGenerator_AllowsSessionDir(t *testing.T) {
	t.Setenv("BOT1_SLUG", "test-bot")
	t.Setenv("BOT1_URL", "https://chat.example.com")
	t.Setenv("BOT1_USER_ID", "user123")
	t.Setenv("BOT1_TOKEN", "token456")
	t.Setenv("BOT1_GENERATOR_TYPE", "opencode")
	t.Setenv("BOT1_OPENCODE_SESSION_DIR", " /tmp/foo ")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	bot, _ := cfg.Get(0)
	if bot.OpenCodeSessionDir != "/tmp/foo" {
		t.Fatalf("OpenCodeSessionDir = %q, want %q", bot.OpenCodeSessionDir, "/tmp/foo")
	}
}

func TestLoad_BootstrapPromptAndRoomPolicies(t *testing.T) {
	t.Setenv("BOT1_SLUG", "test-bot")
	t.Setenv("BOT1_URL", "https://chat.example.com")
	t.Setenv("BOT1_USER_ID", "user123")
	t.Setenv("BOT1_TOKEN", "token456")
	t.Setenv("BOT1_GENERATOR_TYPE", "opencode")
	t.Setenv("BOT1_BOOTSTRAP_PROMPT", "You are Franziska.")
	t.Setenv("BOT1_RENDER_MODE", "concise")
	t.Setenv(
		"BOT1_ROOM_POLICIES_JSON",
		`{"rooms":{"room-123":{"enabled":true,"opencodeSessionDir":"/tmp/room-123","bootstrapPrompt":"Room-specific prompt","renderMode":"detailed","threadDefault":true,"streamedOutput":false}}}`,
	)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	bot, _ := cfg.Get(0)
	if bot.BootstrapPrompt != "You are Franziska." {
		t.Fatalf("BootstrapPrompt = %q, want %q", bot.BootstrapPrompt, "You are Franziska.")
	}
	if bot.RenderMode != "concise" {
		t.Fatalf("RenderMode = %q, want %q", bot.RenderMode, "concise")
	}
	policy, ok := bot.RoomPolicies["room-123"]
	if !ok {
		t.Fatal("expected room policy for room-123")
	}
	if !policy.Enabled {
		t.Fatal("expected room policy to be enabled")
	}
	if policy.OpenCodeSessionDir != "/tmp/room-123" {
		t.Fatalf("OpenCodeSessionDir = %q, want %q", policy.OpenCodeSessionDir, "/tmp/room-123")
	}
	if policy.BootstrapPrompt != "Room-specific prompt" {
		t.Fatalf("BootstrapPrompt = %q, want %q", policy.BootstrapPrompt, "Room-specific prompt")
	}
	if policy.RenderMode != "detailed" {
		t.Fatalf("RenderMode = %q, want %q", policy.RenderMode, "detailed")
	}
	if policy.ThreadDefault == nil || !*policy.ThreadDefault {
		t.Fatal("expected ThreadDefault override to be true")
	}
	if policy.StreamedOutput == nil || *policy.StreamedOutput {
		t.Fatal("expected StreamedOutput override to be false")
	}
}

func TestLoad_InvalidRenderModeErrors(t *testing.T) {
	t.Setenv("BOT1_SLUG", "test-bot")
	t.Setenv("BOT1_URL", "https://chat.example.com")
	t.Setenv("BOT1_USER_ID", "user123")
	t.Setenv("BOT1_TOKEN", "token456")
	t.Setenv("BOT1_GENERATOR_TYPE", "opencode")
	t.Setenv("BOT1_RENDER_MODE", "verbose")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want render mode validation error")
	}
	if !strings.Contains(err.Error(), "BOT1_RENDER_MODE must be") {
		t.Fatalf("Load() error = %v, want render mode validation error", err)
	}
}

func TestLoad_RoomPoliciesJSON_Invalid(t *testing.T) {
	t.Setenv("BOT1_SLUG", "test-bot")
	t.Setenv("BOT1_URL", "https://chat.example.com")
	t.Setenv("BOT1_USER_ID", "user123")
	t.Setenv("BOT1_TOKEN", "token456")
	t.Setenv("BOT1_GENERATOR_TYPE", "opencode")
	t.Setenv("BOT1_ROOM_POLICIES_JSON", `{"rooms":{"room-123":{"enabled":true,"unknown":1}}}`)

	_, err := Load()
	if err == nil {
		t.Fatal("expected Load() to fail")
	}
	if !strings.Contains(err.Error(), "BOT1_ROOM_POLICIES_JSON invalid") {
		t.Fatalf("error = %q, want invalid room policies message", err)
	}
}

func TestLoad_OpenCodeGenerator_PermissionModeValidation(t *testing.T) {
	t.Setenv("BOT1_SLUG", "test-bot")
	t.Setenv("BOT1_URL", "https://chat.example.com")
	t.Setenv("BOT1_USER_ID", "user123")
	t.Setenv("BOT1_TOKEN", "token456")
	t.Setenv("BOT1_GENERATOR_TYPE", "opencode")
	t.Setenv("BOT1_OPENCODE_PERMISSION_MODE", "nope")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "OPENCODE_PERMISSION_MODE") {
		t.Fatalf("Load() error = %v, want permission mode error", err)
	}
}

func TestLoad_WebhookGenerator_RejectsOpenCodeVars(t *testing.T) {
	t.Setenv("BOT1_SLUG", "test-bot")
	t.Setenv("BOT1_URL", "https://chat.example.com")
	t.Setenv("BOT1_USER_ID", "user123")
	t.Setenv("BOT1_TOKEN", "token456")
	t.Setenv("BOT1_WEBHOOK_URL", "https://webhook.example.com/api")
	t.Setenv("BOT1_PARSER_TYPE", "n8n")
	t.Setenv("BOT1_OPENCODE_BASE_URL", "http://127.0.0.1:4096")
	t.Setenv("BOT1_OPENCODE_PERMISSION_MODE", "allow")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "OPENCODE_BASE_URL must not be set") {
		t.Fatalf("Load() error = %v, want opencode vars forbidden error", err)
	}
}

func TestLoad_WebhookGenerator_RejectsOpenCodeSessionDir(t *testing.T) {
	t.Setenv("BOT1_SLUG", "test-bot")
	t.Setenv("BOT1_URL", "https://chat.example.com")
	t.Setenv("BOT1_USER_ID", "user123")
	t.Setenv("BOT1_TOKEN", "token456")
	t.Setenv("BOT1_WEBHOOK_URL", "https://webhook.example.com/api")
	t.Setenv("BOT1_PARSER_TYPE", "n8n")
	t.Setenv("BOT1_OPENCODE_SESSION_DIR", "/tmp/foo")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "OPENCODE_SESSION_DIR must not be set") {
		t.Fatalf("Load() error = %v, want opencode session dir forbidden error", err)
	}
}

func TestLoad_OpenCodeGenerator_RejectsParserType(t *testing.T) {
	t.Setenv("BOT1_SLUG", "test-bot")
	t.Setenv("BOT1_URL", "https://chat.example.com")
	t.Setenv("BOT1_USER_ID", "user123")
	t.Setenv("BOT1_TOKEN", "token456")
	t.Setenv("BOT1_GENERATOR_TYPE", "opencode")
	t.Setenv("BOT1_PARSER_TYPE", "n8n")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "PARSER_TYPE must not be set") {
		t.Fatalf("Load() error = %v, want parser type forbidden error", err)
	}
}

func TestLoad_OpenCodeGenerator_RejectsWebhookURL(t *testing.T) {
	t.Setenv("BOT1_SLUG", "test-bot")
	t.Setenv("BOT1_URL", "https://chat.example.com")
	t.Setenv("BOT1_USER_ID", "user123")
	t.Setenv("BOT1_TOKEN", "token456")
	t.Setenv("BOT1_GENERATOR_TYPE", "opencode")
	t.Setenv("BOT1_WEBHOOK_URL", "https://webhook.example.com/api")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "WEBHOOK_URL must not be set") {
		t.Fatalf("Load() error = %v, want webhook url forbidden error", err)
	}
}

func TestLoad_InvalidParserTypeErrors(t *testing.T) {
	t.Setenv("BOT1_SLUG", "test-bot")
	t.Setenv("BOT1_URL", "https://chat.example.com")
	t.Setenv("BOT1_USER_ID", "user123")
	t.Setenv("BOT1_TOKEN", "token456")
	t.Setenv("BOT1_WEBHOOK_URL", "https://webhook.example.com/api")
	t.Setenv("BOT1_PARSER_TYPE", "openai")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "PARSER_TYPE must be") {
		t.Fatalf("Load() error = %v, want invalid parser type error", err)
	}
}

func TestLoad_InvalidGeneratorTypeErrors(t *testing.T) {
	t.Setenv("BOT1_SLUG", "test-bot")
	t.Setenv("BOT1_URL", "https://chat.example.com")
	t.Setenv("BOT1_USER_ID", "user123")
	t.Setenv("BOT1_TOKEN", "token456")
	t.Setenv("BOT1_GENERATOR_TYPE", "nope")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "GENERATOR_TYPE unknown") {
		t.Fatalf("Load() error = %v, want invalid generator type error", err)
	}
}

func TestLoad_WebhookConfig(t *testing.T) {
	t.Setenv("BOT1_SLUG", "test-bot")
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
			name: "missing_slug",
			envVars: map[string]string{
				"BOT1_URL":         "https://chat.example.com",
				"BOT1_USER_ID":     "user123",
				"BOT1_TOKEN":       "token456",
				"BOT1_PARSER_TYPE": "n8n",
			},
			errContains: "BOT1_SLUG is required",
		},
		{
			name: "invalid_slug_uppercase",
			envVars: map[string]string{
				"BOT1_SLUG":        "MyBot",
				"BOT1_URL":         "https://chat.example.com",
				"BOT1_USER_ID":     "user123",
				"BOT1_TOKEN":       "token456",
				"BOT1_PARSER_TYPE": "n8n",
			},
			errContains: "BOT1_SLUG invalid",
		},
		{
			name: "invalid_slug_special_chars",
			envVars: map[string]string{
				"BOT1_SLUG":        "my_bot!",
				"BOT1_URL":         "https://chat.example.com",
				"BOT1_USER_ID":     "user123",
				"BOT1_TOKEN":       "token456",
				"BOT1_PARSER_TYPE": "n8n",
			},
			errContains: "BOT1_SLUG invalid",
		},
		{
			name: "invalid_slug_spaces",
			envVars: map[string]string{
				"BOT1_SLUG":        "my bot",
				"BOT1_URL":         "https://chat.example.com",
				"BOT1_USER_ID":     "user123",
				"BOT1_TOKEN":       "token456",
				"BOT1_PARSER_TYPE": "n8n",
			},
			errContains: "BOT1_SLUG invalid",
		},
		{
			name: "duplicate_slug",
			envVars: map[string]string{
				"BOT1_SLUG":        "alerts",
				"BOT1_URL":         "https://chat1.example.com",
				"BOT1_USER_ID":     "user1",
				"BOT1_TOKEN":       "token1",
				"BOT1_WEBHOOK_URL": "https://webhook.example.com/bot1",
				"BOT1_PARSER_TYPE": "n8n",
				"BOT2_SLUG":        "alerts",
				"BOT2_URL":         "https://chat2.example.com",
				"BOT2_USER_ID":     "user2",
				"BOT2_TOKEN":       "token2",
				"BOT2_WEBHOOK_URL": "https://webhook.example.com/bot2",
				"BOT2_PARSER_TYPE": "n8n",
			},
			errContains: "conflicts",
		},
		{
			name: "missing_user_id",
			envVars: map[string]string{
				"BOT1_SLUG":        "alerts",
				"BOT1_URL":         "https://chat.example.com",
				"BOT1_TOKEN":       "token456",
				"BOT1_PARSER_TYPE": "n8n",
			},
			errContains: "BOT1_USER_ID is required",
		},
		{
			name: "missing_token",
			envVars: map[string]string{
				"BOT1_SLUG":        "alerts",
				"BOT1_URL":         "https://chat.example.com",
				"BOT1_USER_ID":     "user123",
				"BOT1_PARSER_TYPE": "n8n",
			},
			errContains: "BOT1_TOKEN is required",
		},
		{
			name: "missing_parser_type",
			envVars: map[string]string{
				"BOT1_SLUG":        "alerts",
				"BOT1_URL":         "https://chat.example.com",
				"BOT1_USER_ID":     "user123",
				"BOT1_TOKEN":       "token456",
				"BOT1_WEBHOOK_URL": "https://webhook.example.com/api",
			},
			errContains: "BOT1_PARSER_TYPE is required",
		},
		{
			name: "missing_webhook_url",
			envVars: map[string]string{
				"BOT1_SLUG":        "alerts",
				"BOT1_URL":         "https://chat.example.com",
				"BOT1_USER_ID":     "user123",
				"BOT1_TOKEN":       "token456",
				"BOT1_PARSER_TYPE": "n8n",
			},
			errContains: "BOT1_WEBHOOK_URL is required",
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

func TestLoad_StatusMessage(t *testing.T) {
	tests := []struct {
		name              string
		statusMessage     string // empty = not set
		wantStatusMessage string
	}{
		{
			name:              "status_message_not_set",
			statusMessage:     "",
			wantStatusMessage: "",
		},
		{
			name:              "status_message_custom",
			statusMessage:     "Alerts Bot Ready",
			wantStatusMessage: "Alerts Bot Ready",
		},
		{
			name:              "status_message_with_emoji",
			statusMessage:     "🤖 Bot Active",
			wantStatusMessage: "🤖 Bot Active",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("BOT1_SLUG", "test-bot")
			t.Setenv("BOT1_URL", "https://chat.example.com")
			t.Setenv("BOT1_USER_ID", "user123")
			t.Setenv("BOT1_TOKEN", "token456")
			t.Setenv("BOT1_WEBHOOK_URL", "https://webhook.example.com/api")
			t.Setenv("BOT1_PARSER_TYPE", "n8n")
			if tt.statusMessage != "" {
				t.Setenv("BOT1_STATUS_MESSAGE", tt.statusMessage)
			}

			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}

			bot, _ := cfg.Get(0)
			if bot.StatusMessage != tt.wantStatusMessage {
				t.Errorf("StatusMessage = %q, want %q", bot.StatusMessage, tt.wantStatusMessage)
			}
		})
	}
}

func TestLoad_ValidSlugs(t *testing.T) {
	tests := []struct {
		name string
		slug string
	}{
		{name: "simple", slug: "alerts"},
		{name: "with_number", slug: "bot1"},
		{name: "with_hyphen", slug: "my-bot"},
		{name: "multiple_hyphens", slug: "my-support-bot-1"},
		{name: "all_numbers", slug: "123"},
		{name: "hyphen_and_numbers", slug: "bot-2024"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("BOT1_SLUG", tt.slug)
			t.Setenv("BOT1_URL", "https://chat.example.com")
			t.Setenv("BOT1_USER_ID", "user123")
			t.Setenv("BOT1_TOKEN", "token456")
			t.Setenv("BOT1_WEBHOOK_URL", "https://webhook.example.com/api")
			t.Setenv("BOT1_PARSER_TYPE", "n8n")

			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load() error = %v, want nil", err)
			}

			bot, _ := cfg.Get(0)
			if bot.Slug != tt.slug {
				t.Errorf("Slug = %q, want %q", bot.Slug, tt.slug)
			}
		})
	}
}
