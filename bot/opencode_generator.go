package bot

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
)

// OpenCodeGenerator generates responses via an OpenCode server.
//
// This is a stub used to prepare the codebase for a clean implementation.
// The real implementation will stream events from the server and emit text
// chunks compatible with StreamingGenerator.
type OpenCodeGenerator struct {
	client *http.Client
	logger *slog.Logger
}

func NewOpenCodeGenerator(client *http.Client, logger *slog.Logger) *OpenCodeGenerator {
	if client == nil {
		client = http.DefaultClient
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &OpenCodeGenerator{client: client, logger: logger}
}

func (g *OpenCodeGenerator) GenerateResponse(
	ctx context.Context,
	message Message,
	history []Message,
) (string, error) {
	_ = ctx
	_ = message
	_ = history
	return "", errors.New("opencode generator not implemented")
}

func (g *OpenCodeGenerator) GenerateResponseStream(
	ctx context.Context,
	message Message,
	history []Message,
) (<-chan string, error) {
	_ = ctx
	_ = message
	_ = history

	ch := make(chan string)
	close(ch)
	return ch, errors.New("opencode generator not implemented")
}

var _ StreamingGenerator = (*OpenCodeGenerator)(nil)
