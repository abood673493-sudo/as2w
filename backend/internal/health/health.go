// Package health exposes liveness and readiness reporting.
package health

import (
	"context"
	"net/http"
	"time"

	"github.com/sshmp/ssh-manager-panel/internal/httpx"
)

// Checker is anything that can report whether a dependency is usable.
type Checker interface {
	// Name is a short stable identifier used in the JSON output.
	Name() string
	// Check returns nil when the dependency is healthy.
	Check(ctx context.Context) error
}

type checkerFunc struct {
	name string
	fn   func(ctx context.Context) error
}

// Check adapts a plain function into a Checker.
func Check(name string, fn func(ctx context.Context) error) Checker {
	return checkerFunc{name: name, fn: fn}
}

func (c checkerFunc) Name() string                     { return c.name }
func (c checkerFunc) Check(ctx context.Context) error   { return c.fn(ctx) }

// Result is the outcome of a single dependency check.
type Result struct {
	Name       string `json:"name"`
	OK         bool   `json:"ok"`
	Error      string `json:"error,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}

// Report is the document returned by GET /api/v1/health.
type Report struct {
	Status  string    `json:"status"`
	Version string    `json:"version"`
	Time    time.Time `json:"time"`
	Checks  []Result  `json:"checks"`
}

// Handler serves the health endpoints.
type Handler struct {
	version  string
	timeout  time.Duration
	checkers []Checker
}

// NewHandler builds a health handler. A zero or negative timeout becomes 3s.
func NewHandler(version string, timeout time.Duration, checkers ...Checker) *Handler {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	return &Handler{version: version, timeout: timeout, checkers: checkers}
}

// Liveness always answers 200 while the process is running. It must not touch
// the database: a restarting database should not cause the process to be
// killed by the supervisor.
func (h *Handler) Liveness(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"version": h.version,
	})
}

// Readiness runs every checker and answers 503 when one of them fails.
func (h *Handler) Readiness(w http.ResponseWriter, r *http.Request) {
	report := h.run(r.Context())
	status := http.StatusOK
	if report.Status != "ok" {
		status = http.StatusServiceUnavailable
	}
	httpx.WriteJSON(w, status, report)
}

// Report always answers 200 and is meant for the dashboard.
func (h *Handler) Report(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, h.run(r.Context()))
}

func (h *Handler) run(parent context.Context) Report {
	ctx, cancel := context.WithTimeout(parent, h.timeout)
	defer cancel()

	report := Report{
		Status:  "ok",
		Version: h.version,
		Time:    time.Now().UTC(),
		Checks:  make([]Result, 0, len(h.checkers)),
	}
	for _, c := range h.checkers {
		start := time.Now()
		err := c.Check(ctx)
		res := Result{
			Name:       c.Name(),
			OK:         err == nil,
			DurationMS: time.Since(start).Milliseconds(),
		}
		if err != nil {
			res.Error = err.Error()
			report.Status = "degraded"
		}
		report.Checks = append(report.Checks, res)
	}
	return report
}