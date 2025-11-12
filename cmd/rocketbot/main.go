package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/deichbewohner/rocketbot/bot"
	"github.com/deichbewohner/rocketbot/config"
	"github.com/deichbewohner/rocketbot/internal/httpapi"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// otelHandlerWrapper wraps an slog.Handler to add OpenTelemetry trace context
type otelHandlerWrapper struct {
	slog.Handler
}

func (h *otelHandlerWrapper) Handle(ctx context.Context, r slog.Record) error {
	// Extract trace and span IDs from context if present
	span := oteltrace.SpanFromContext(ctx)
	spanCtx := span.SpanContext()

	if spanCtx.IsValid() {
		// Clone record to avoid modifying original
		r = r.Clone()
		r.AddAttrs(
			slog.String("trace_id", spanCtx.TraceID().String()),
			slog.String("span_id", spanCtx.SpanID().String()),
		)
	}
	return h.Handler.Handle(ctx, r)
}

// WithAttrs returns a new handler with additional attributes
func (h *otelHandlerWrapper) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &otelHandlerWrapper{Handler: h.Handler.WithAttrs(attrs)}
}

// WithGroup returns a new handler with a group name
func (h *otelHandlerWrapper) WithGroup(name string) slog.Handler {
	return &otelHandlerWrapper{Handler: h.Handler.WithGroup(name)}
}

// getLogLevel returns the configured log level from environment
func getLogLevel() slog.Level {
	if os.Getenv("LOG_LEVEL") == "DEBUG" {
		return slog.LevelDebug
	}
	return slog.LevelInfo
}

// createBot creates and configures a bot client from config
func createBot(
	botCfg config.BotConfig,
	slug string,
	httpClient *http.Client,
	logger *slog.Logger,
) (*bot.Client, error) {
	botLogger := logger.With("bot", slug)

	// Validate webhook URL
	if botCfg.WebhookURL == "" {
		return nil, fmt.Errorf("missing webhook url for bot %s", slug)
	}

	// Create stream parser based on configuration
	var parser bot.StreamParser
	switch botCfg.ParserType {
	case "n8n":
		parser = bot.NewN8nParser(botLogger)
	default:
		return nil, fmt.Errorf("unknown parser type %q for bot %s", botCfg.ParserType, slug)
	}

	// Create webhook generator with injected parser
	generator := bot.NewWebhookGenerator(
		botCfg.WebhookURL,
		botCfg.WebhookAuth,
		parser,
		httpClient,
		botLogger,
	)

	botLogger.Info(
		"using webhook generator",
		"parser_type",
		botCfg.ParserType,
		"streamed_output",
		botCfg.StreamedOutput,
	)

	client := bot.NewClient(
		botCfg.URL,
		botCfg.UserID,
		botCfg.Token,
		slug,
		generator,
		botCfg.StreamedOutput,
		botCfg.ThreadDefault,
		botLogger,
		botCfg.StatusMessage,
	)

	return client, nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Setup OpenTelemetry trace provider
	// If OTEL_EXPORTER_OTLP_ENDPOINT is set, traces are exported to collector
	// Otherwise, traces are generated locally but not exported (no-op exporter)
	var provider *trace.TracerProvider
	if otelEndpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); otelEndpoint != "" {
		// Export traces to OTEL collector with timeout
		setupCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()

		exporter, err := otlptracehttp.New(setupCtx)
		if err != nil {
			slog.Warn("OTEL exporter setup failed, traces will not be exported", "error", err)
			provider = trace.NewTracerProvider()
		} else {
			provider = trace.NewTracerProvider(
				trace.WithBatcher(exporter),
			)
		}
	} else {
		// No exporter configured - traces generated but not exported
		provider = trace.NewTracerProvider()
	}
	otel.SetTracerProvider(provider)

	// Set global propagator for trace context and baggage injection into HTTP headers
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	defer func() {
		// Shutdown with timeout to prevent hanging on exit
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := provider.Shutdown(shutdownCtx); err != nil {
			slog.Error("failed to shutdown OTEL provider", "error", err)
		}
	}()

	// Create logger with custom wrapper that adds trace IDs to JSON logs
	baseHandler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: getLogLevel(),
	})
	logger := slog.New(&otelHandlerWrapper{Handler: baseHandler})

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		logger.Error("failed to load configuration", "error", err)
		os.Exit(1)
	}

	logger.Info("starting bots", "count", cfg.Count())

	// Create instrumented HTTP client for automatic trace propagation
	httpClient := &http.Client{
		Timeout:   0, // No timeout for streaming connections
		Transport: otelhttp.NewTransport(http.DefaultTransport),
	}

	// Create and start all bots
	var wg sync.WaitGroup
	clients := make([]*bot.Client, 0, cfg.Count())

	for i := 0; i < cfg.Count(); i++ {
		botCfg, _ := cfg.Get(i)

		client, err := createBot(botCfg, botCfg.Slug, httpClient, logger)
		if err != nil {
			logger.Error("failed to create bot", "bot", botCfg.Slug, "error", err)
			os.Exit(1)
		}
		clients = append(clients, client)

		wg.Add(1)
		go func(c *bot.Client, slug string) {
			defer wg.Done()
			if err := c.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				logger.Error("bot exited with error", "bot", slug, "error", err)
				os.Exit(1)
			}
		}(client, botCfg.Slug)
	}

	logger.Info("all bots started successfully")

	// Start HTTP API server if configured
	apiAddr := os.Getenv("API_ADDR")
	if apiAddr != "" {
		// Build bot map and token map using slugs - only include bots with API tokens configured
		botMap := make(map[string]*bot.Client)
		tokenMap := make(map[string]string)

		for i := 0; i < cfg.Count(); i++ {
			botCfg, _ := cfg.Get(i)

			// Only expose bots via HTTP API if they have an API token configured
			if botCfg.APIToken != "" {
				botMap[botCfg.Slug] = clients[i]
				tokenMap[botCfg.Slug] = botCfg.APIToken
			}
		}

		if len(botMap) > 0 {
			// Create and start HTTP server in goroutine
			httpSrv := httpapi.NewServer(botMap, tokenMap, logger)
			go func() {
				if err := httpSrv.Start(apiAddr); err != nil && err != http.ErrServerClosed {
					logger.Error("HTTP server failed", "error", err)
				}
			}()
			logger.Info("HTTP API server enabled", "bots", len(botMap))
		} else {
			logger.Warn("API_ADDR set but no bots have API_TOKEN configured - HTTP server not started")
		}
	}

	// Wait for interrupt signal
	<-ctx.Done()
	logger.Info("shutting down")

	// Stop all bots
	for _, client := range clients {
		client.Stop()
	}

	wg.Wait()
	logger.Info("all bots stopped")
}
