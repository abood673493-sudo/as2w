// Package server wires the HTTP API, the WebSocket gateway and the health
// endpoints into a single runnable unit.
//
// Note on timeouts: the admin API sets a finite WriteTimeout. The SSH-over-WS
// gateway is a long-lived streaming service and therefore runs on its own
// listener (WS_LISTEN_ADDR) without a request write deadline.
package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"runtime"
	"time"

	"github.com/sshmp/ssh-manager-panel/internal/config"
	"github.com/sshmp/ssh-manager-panel/internal/database"
	"github.com/sshmp/ssh-manager-panel/internal/health"
	"github.com/sshmp/ssh-manager-panel/internal/httpx"
	"github.com/sshmp/ssh-manager-panel/internal/version"
)

// Server owns the HTTP listener and its dependencies.
type Server struct {
	cfg    *config.Config
	db     *database.DB
	logger *slog.Logger
	http   *http.Server
}

// New constructs a Server.
func New(cfg *config.Config, db *database.DB, logger *slog.Logger) *Server {
	return &Server{cfg: cfg, db: db, logger: logger}
}

// Handler builds the complete routing tree for the admin API.
func (s *Server) Handler() http.Handler {
	healthHandler := health.NewHandler(version.Version, 5*time.Second,
		health.Check("database", func(ctx context.Context) error {
			return s.db.Ping(ctx)
		}),
	)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthHandler.Liveness)
	mux.HandleFunc("GET /readyz", healthHandler.Readiness)
	mux.HandleFunc("GET /api/v1/health", healthHandler.Report)
	mux.HandleFunc("GET /api/v1/version", s.handleVersion)

	realIP, err := httpx.RealIP(s.cfg.App.TrustedProxies)
	if err != nil {
		// Trusted proxy CIDRs are validated by config.Load, so this can only
		// happen if the configuration was built by hand.
		panic("server: invalid trusted proxy configuration: " + err.Error())
	}

	return httpx.Chain(mux,
		httpx.RequestID(),
		httpx.Recoverer(s.logger),
		httpx.SecurityHeaders(),
		realIP,
		httpx.AccessLog(s.logger),
	)
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]string{
		"version": version.Version,
		"commit":  version.Commit,
		"date":    version.Date,
		"runtime": runtime.Version(),
	})
}

// ListenAndServe blocks until ctx is cancelled or the listener fails.
func (s *Server) ListenAndServe(ctx context.Context) error {
	s.http = &http.Server{
		Addr:              s.cfg.App.HTTPAddr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(s.logger.Handler(), slog.LevelError),
	}

	errCh := make(chan error, 1)
	go func() {
		s.logger.Info("http server listening",
			"addr", s.cfg.App.HTTPAddr,
			"env", s.cfg.App.Env,
			"version", version.Version,
		)
		err := s.http.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		return s.Shutdown(context.WithoutCancel(ctx))
	case err := <-errCh:
		return err
	}
}

// Shutdown gracefully stops the HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.http == nil {
		return nil
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, s.cfg.App.ShutdownTimeout)
	defer cancel()

	s.logger.Info("shutting down http server", "timeout", s.cfg.App.ShutdownTimeout.String())
	return s.http.Shutdown(shutdownCtx)
}