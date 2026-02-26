package config

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// BotConfig holds configuration for a single bot instance
type BotConfig struct {
	Slug           string // URL-safe identifier used in HTTP API endpoints
	URL            string
	UserID         string
	Token          string
	GeneratorType  string // Response generator type (e.g., "webhook", "opencode")
	WebhookURL     string
	WebhookAuth    string
	StreamedOutput bool
	ParserType     string // Stream parser type for webhook generator (e.g., "n8n")
	APIToken       string // HTTP API authentication token
	ThreadDefault  bool   // Always reply in threads (default: false)
	StatusMessage  string // Custom status message (optional, defaults to "Bot is active")
}

// Config holds all bot configurations
type Config struct {
	Bots []BotConfig
}

var slugRegex = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// validateSlug checks if a slug is URL-safe (lowercase alphanumeric with hyphens)
func validateSlug(slug string) error {
	if slug == "" {
		return fmt.Errorf("slug cannot be empty")
	}
	if !slugRegex.MatchString(slug) {
		return fmt.Errorf(
			"slug must be lowercase alphanumeric with hyphens (e.g., 'alerts', 'my-bot-1')",
		)
	}
	return nil
}

// Load reads bot configurations from environment variables
// Supports multiple bots with pattern: BOTn_URL, BOTn_USER_ID, BOTn_TOKEN, BOTn_SLUG
func Load() (*Config, error) {
	cfg := &Config{
		Bots: make([]BotConfig, 0),
	}

	seenSlugs := make(map[string]string) // slug -> BOTn prefix

	// Scan for bot configurations (BOT1, BOT2, BOT3, ...)
	for i := 1; ; i++ {
		prefix := fmt.Sprintf("BOT%d_", i)
		url := os.Getenv(prefix + "URL")

		// Stop when we don't find the next bot
		if url == "" {
			break
		}

		slug := os.Getenv(prefix + "SLUG")
		userID := os.Getenv(prefix + "USER_ID")
		token := os.Getenv(prefix + "TOKEN")
		webhookURL := os.Getenv(prefix + "WEBHOOK_URL")
		webhookAuth := os.Getenv(prefix + "WEBHOOK_AUTH")
		streamedOutput := os.Getenv(prefix + "STREAMED_OUTPUT")
		parserType := os.Getenv(prefix + "PARSER_TYPE")
		generatorType := os.Getenv(prefix + "GENERATOR_TYPE")
		apiToken := os.Getenv(prefix + "API_TOKEN")
		threadDefault := os.Getenv(prefix + "THREAD_DEFAULT")
		statusMessage := os.Getenv(prefix + "STATUS_MESSAGE")

		// Validate required fields
		if slug == "" {
			return nil, fmt.Errorf("%sSLUG is required", prefix)
		}
		if err := validateSlug(slug); err != nil {
			return nil, fmt.Errorf("%sSLUG invalid: %w", prefix, err)
		}
		if existingPrefix, exists := seenSlugs[slug]; exists {
			return nil, fmt.Errorf(
				"%sSLUG %q conflicts with %sSLUG (slugs must be unique)",
				prefix,
				slug,
				existingPrefix,
			)
		}
		seenSlugs[slug] = prefix

		if userID == "" {
			return nil, fmt.Errorf("%sUSER_ID is required", prefix)
		}
		if token == "" {
			return nil, fmt.Errorf("%sTOKEN is required", prefix)
		}

		// Normalize URL (remove trailing slash)
		url = strings.TrimSuffix(url, "/")

		// Generator type selection (default: webhook)
		if generatorType == "" {
			generatorType = "webhook"
		}
		switch generatorType {
		case "webhook":
			// Webhook generator requires webhook URL and a parser
			if webhookURL == "" {
				return nil, fmt.Errorf(
					"%sWEBHOOK_URL is required when %sGENERATOR_TYPE=webhook",
					prefix,
					prefix,
				)
			}
			if parserType == "" {
				return nil, fmt.Errorf(
					"%sPARSER_TYPE is required when %sGENERATOR_TYPE=webhook",
					prefix,
					prefix,
				)
			}
			if parserType != "n8n" {
				return nil, fmt.Errorf(
					"%sPARSER_TYPE must be %q (got %q)",
					prefix,
					"n8n",
					parserType,
				)
			}
		case "opencode":
			// OpenCode generator must not mix with webhook-only config.
			if parserType != "" {
				return nil, fmt.Errorf(
					"%sPARSER_TYPE must not be set when %sGENERATOR_TYPE=opencode",
					prefix,
					prefix,
				)
			}
			if webhookURL != "" {
				return nil, fmt.Errorf(
					"%sWEBHOOK_URL must not be set when %sGENERATOR_TYPE=opencode",
					prefix,
					prefix,
				)
			}
			if webhookAuth != "" {
				return nil, fmt.Errorf(
					"%sWEBHOOK_AUTH must not be set when %sGENERATOR_TYPE=opencode",
					prefix,
					prefix,
				)
			}
		default:
			return nil, fmt.Errorf("%sGENERATOR_TYPE unknown: %q", prefix, generatorType)
		}

		// Parse streamed output (default: true - enables live message updates as responses are generated)
		enableStreamedOutput := true
		if streamedOutput != "" {
			enableStreamedOutput = streamedOutput != "false" && streamedOutput != "0"
		}

		// Parse thread default (default: false)
		enableThreadDefault := false
		if threadDefault != "" {
			enableThreadDefault = threadDefault == "true" || threadDefault == "1"
		}

		cfg.Bots = append(cfg.Bots, BotConfig{
			Slug:           slug,
			URL:            url,
			UserID:         userID,
			Token:          token,
			GeneratorType:  generatorType,
			WebhookURL:     webhookURL,
			WebhookAuth:    webhookAuth,
			StreamedOutput: enableStreamedOutput,
			ParserType:     parserType,
			APIToken:       apiToken,
			ThreadDefault:  enableThreadDefault,
			StatusMessage:  statusMessage,
		})
	}

	if len(cfg.Bots) == 0 {
		return nil, fmt.Errorf(
			"no bots configured (expected BOT1_URL, BOT1_USER_ID, BOT1_TOKEN, BOT1_SLUG)",
		)
	}

	return cfg, nil
}

// Count returns the number of configured bots
func (c *Config) Count() int {
	return len(c.Bots)
}

// Get returns the bot configuration at the given index
func (c *Config) Get(index int) (BotConfig, error) {
	if index < 0 || index >= len(c.Bots) {
		return BotConfig{}, fmt.Errorf("bot index %d out of range [0-%d]", index, len(c.Bots)-1)
	}
	return c.Bots[index], nil
}

// BotName returns a human-readable name for the bot at the given index
func (c *Config) BotName(index int) string {
	return "BOT" + strconv.Itoa(index+1)
}
