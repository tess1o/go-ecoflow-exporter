package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"
)

var _ Shutdownable = (*HTTPServer)(nil)

// HTTPServer serves /healthz and, when Prometheus is enabled, /metrics
type HTTPServer struct {
	server *http.Server

	healthMu sync.RWMutex
	health   func() error
}

// NewHTTPServer starts listening on port. metrics is mounted on /metrics unless it is nil.
func NewHTTPServer(port string, metrics http.Handler) (*HTTPServer, error) {
	s := &HTTPServer{}
	mux := http.NewServeMux()
	if metrics != nil {
		mux.Handle("/metrics", metrics)
	}
	mux.HandleFunc("/healthz", s.serveHealth)

	listener, err := net.Listen("tcp", ":"+port)
	if err != nil {
		return nil, fmt.Errorf("cannot listen on port %s: %w", port, err)
	}
	s.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		slog.Debug("Starting HTTP server", "port", port)
		if err := s.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("HTTP server error", "error", err)
		}
	}()
	return s, nil
}

// SetHealthCheck sets the function used by /healthz. A nil error means healthy.
func (s *HTTPServer) SetHealthCheck(check func() error) {
	s.healthMu.Lock()
	defer s.healthMu.Unlock()
	s.health = check
}

func (s *HTTPServer) serveHealth(w http.ResponseWriter, _ *http.Request) {
	s.healthMu.RLock()
	check := s.health
	s.healthMu.RUnlock()

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if check != nil {
		if err := check(); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprintf(w, "unhealthy: %v\n", err)
			return
		}
	}
	_, _ = fmt.Fprintln(w, "ok")
}

func (s *HTTPServer) Close(ctx context.Context) {
	slog.Debug("Shutting down HTTP server...")
	if err := s.server.Shutdown(ctx); err != nil {
		slog.Error("HTTP server shutdown error", "error", err)
	} else {
		slog.Debug("HTTP server gracefully stopped")
	}
}
