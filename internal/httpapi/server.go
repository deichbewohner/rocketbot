package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/deichbewohner/rocketbot/bot"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// Server serves the HTTP API for triggering bot messages
type Server struct {
	bots   map[string]*bot.Client
	tokens map[string]string
	logger *slog.Logger
}

// NewServer creates a new HTTP API server
func NewServer(bots map[string]*bot.Client, tokens map[string]string, logger *slog.Logger) *Server {
	return &Server{
		bots:   bots,
		tokens: tokens,
		logger: logger,
	}
}

// Handler constructs the HTTP handler with routes and middleware
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/bots/{slug}/send", s.handleSend)
	mux.HandleFunc("GET /api/v1/health", s.handleHealth)
	return s.loggingMiddleware(mux)
}

// Start starts the HTTP server on the given address
func (s *Server) Start(addr string) error {
	s.logger.Info("HTTP API server starting", "addr", addr)
	handler := otelhttp.NewHandler(
		s.Handler(),
		"httpapi",
		otelhttp.WithSpanNameFormatter(func(operation string, r *http.Request) string {
			return r.Method + " " + r.URL.Path
		}),
	)
	return http.ListenAndServe(addr, handler)
}

// Target represents a structured message destination
type Target struct {
	Username string `json:"username,omitempty"` // DM to username
	Channel  string `json:"channel,omitempty"`  // Post to channel (with or without #)
	RoomID   string `json:"roomId,omitempty"`   // Post to room ID directly
}

// sendRequest represents the request payload for sending messages
type sendRequest struct {
	Target Target `json:"target"`
	Text   string `json:"text"`
}

// sendResponse represents the response after sending a message
type sendResponse struct {
	Success bool   `json:"success"`
	RoomID  string `json:"roomId"`
	Error   string `json:"error,omitempty"`
}

// validateTarget validates that exactly one target field is set
func validateTarget(t Target) error {
	set := 0
	if t.Username != "" {
		set++
	}
	if t.Channel != "" {
		set++
	}
	if t.RoomID != "" {
		set++
	}

	if set == 0 {
		return errors.New("target must specify username, channel, or roomId")
	}
	if set > 1 {
		return errors.New("target must specify only one of username, channel, or roomId")
	}
	return nil
}

// handleSend handles POST /api/v1/bots/{slug}/send
func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	slug := r.PathValue("slug")

	// Authenticate request
	expectedToken, ok := s.tokens[slug]
	if !ok || expectedToken == "" {
		s.writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		s.writeError(w, http.StatusUnauthorized, "missing authorization header")
		return
	}

	token := strings.TrimPrefix(authHeader, "Bearer ")
	if token == authHeader || token != expectedToken {
		s.writeError(w, http.StatusUnauthorized, "invalid token")
		return
	}

	// Get bot client
	client, ok := s.bots[slug]
	if !ok {
		s.writeError(w, http.StatusNotFound, "bot not found")
		return
	}

	// Parse request
	var req sendRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)) // 1MB limit
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Validate
	if req.Text == "" {
		s.writeError(w, http.StatusBadRequest, "text is required")
		return
	}

	// Validate target - exactly one field must be set
	if err := validateTarget(req.Target); err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Resolve target to room ID
	var roomID string
	var err error
	if req.Target.Username != "" {
		roomID, err = client.API().EnsureDMRoom(ctx, req.Target.Username)
	} else if req.Target.Channel != "" {
		roomID, err = client.API().ResolveChannel(ctx, req.Target.Channel)
	} else {
		roomID = req.Target.RoomID
	}

	if err != nil {
		s.logger.ErrorContext(ctx, "failed to resolve target", "target", req.Target, "error", err)
		s.writeError(w, http.StatusUnprocessableEntity, "failed to resolve target: "+err.Error())
		return
	}

	// Post message
	_, err = client.API().PostMessage(ctx, roomID, req.Text, "")
	if err != nil {
		s.logger.ErrorContext(ctx, "failed to post message", "roomId", roomID, "error", err)
		s.writeError(w, http.StatusInternalServerError, "failed to post message")
		return
	}

	s.logger.InfoContext(ctx, "message sent via API", "bot", slug, "roomId", roomID)
	s.writeJSON(w, http.StatusOK, sendResponse{
		Success: true,
		RoomID:  roomID,
	})
}

// handleHealth handles GET /api/v1/health
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]string{
		"status": "ok",
	})
}

// loggingMiddleware logs HTTP requests
func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.logger.Debug("HTTP request", "method", r.Method, "path", r.URL.Path)
		next.ServeHTTP(w, r)
	})
}

// writeJSON writes a JSON response
func (s *Server) writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// writeError writes an error response
func (s *Server) writeError(w http.ResponseWriter, status int, message string) {
	s.writeJSON(w, status, sendResponse{
		Success: false,
		Error:   message,
	})
}

// no additional helpers
