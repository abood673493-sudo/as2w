// Package httpx contains the small HTTP helpers shared by every handler:
// JSON encoding, the structured error envelope and the middleware chain.
package httpx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

// MaxBodyBytes is the default request body limit for JSON endpoints.
const MaxBodyBytes int64 = 1 << 20 // 1 MiB

// ErrorResponse is the structured error envelope returned by the API. Clients
// can rely on `error.code` being stable and machine readable.
type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

// ErrorBody describes a single failure.
type ErrorBody struct {
	Code      string            `json:"code"`
	Message   string            `json:"message"`
	Details   map[string]string `json:"details,omitempty"`
	RequestID string            `json:"request_id,omitempty"`
}

// Common error codes.
const (
	CodeBadRequest    = "bad_request"
	CodeUnauthorized  = "unauthorized"
	CodeForbidden     = "forbidden"
	CodeNotFound      = "not_found"
	CodeConflict      = "conflict"
	CodeValidation    = "validation_failed"
	CodeRateLimited   = "rate_limited"
	CodeInternal      = "internal_error"
	CodeUnavailable   = "unavailable"
	CodeNotConfigured = "not_configured"
)

// WriteJSON encodes v as JSON with the given status code.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	payload, err := json.Marshal(v)
	if err != nil {
		slog.Error("encode JSON response", "error", err)
		WriteError(w, http.StatusInternalServerError, CodeInternal, "failed to encode response", nil)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if _, err := w.Write(payload); err != nil {
		slog.Debug("write response body", "error", err)
	}
}

// WriteError writes the structured error envelope. Use WriteErrorRequest when
// the *http.Request is available so that the correlation ID is included.
func WriteError(w http.ResponseWriter, status int, code, message string, details map[string]string) {
	WriteJSON(w, status, ErrorResponse{Error: ErrorBody{
		Code:    code,
		Message: message,
		Details: details,
	}})
}

// WriteErrorRequest writes the structured error envelope including the request
// correlation ID taken from the request context.
func WriteErrorRequest(w http.ResponseWriter, r *http.Request, status int, code, message string, details map[string]string) {
	WriteJSON(w, status, ErrorResponse{Error: ErrorBody{
		Code:      code,
		Message:   message,
		Details:   details,
		RequestID: RequestIDFromContext(r.Context()),
	}})
}
// DecodeJSON reads a JSON request body into dst, rejecting unknown fields and
// oversized payloads.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	if ct := r.Header.Get("Content-Type"); ct != "" {
		if mediaType := strings.TrimSpace(strings.Split(ct, ";")[0]); mediaType != "application/json" {
			return fmt.Errorf("unsupported content type %q, expected application/json", mediaType)
		}
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var syntaxErr *json.SyntaxError
		var typeErr *json.UnmarshalTypeError
		switch {
		case errors.As(err, &syntaxErr):
			return fmt.Errorf("malformed JSON at byte %d", syntaxErr.Offset)
		case errors.As(err, &typeErr):
			return fmt.Errorf("field %q has the wrong type", typeErr.Field)
		case errors.Is(err, io.EOF):
			return errors.New("request body is empty")
		case strings.HasPrefix(err.Error(), "json: unknown field "):
			return fmt.Errorf("unknown field %s", strings.TrimPrefix(err.Error(), "json: unknown field "))
		default:
			return err
		}
	}
	if dec.More() {
		return errors.New("request body must contain a single JSON object")
	}
	return nil
}

// ---- request IDs -----------------------------------------------------------

type contextKey string

const requestIDContextKey contextKey = "request_id"

// RequestIDHeader is the header used to propagate correlation IDs.
const RequestIDHeader = "X-Request-ID"

// RequestIDFromContext returns the correlation ID stored on the request.
func RequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if id, ok := ctx.Value(requestIDContextKey).(string); ok {
		return id
	}
	return ""
}

func newRequestID() string {
	var buf [12]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("req-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf[:])
}

// Middleware decorates an http.Handler.
type Middleware func(http.Handler) http.Handler

// Chain applies middlewares so that the first listed runs outermost.
func Chain(h http.Handler, middlewares ...Middleware) http.Handler {
	for i := len(middlewares) - 1; i >= 0; i-- {
		h = middlewares[i](h)
	}
	return h
}

// RequestID assigns/propagates a correlation ID.
func RequestID() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := strings.TrimSpace(r.Header.Get(RequestIDHeader))
			if id == "" || len(id) > 128 {
				id = newRequestID()
			}
			w.Header().Set(RequestIDHeader, id)
			ctx := context.WithValue(r.Context(), requestIDContextKey, id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// WithRequestID stores an explicit request ID on the context (used by tests).
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDContextKey, id)
}

// Recoverer converts panics into 500 responses instead of killing the process.
func Recoverer(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					logger.Error("panic recovered",
						"error", fmt.Sprint(rec),
						"path", r.URL.Path,
						"method", r.Method,
						"request_id", RequestIDFromContext(r.Context()),
					)
					WriteErrorRequest(w, r, http.StatusInternalServerError, CodeInternal, "internal server error", nil)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// statusRecorder captures the status code for access logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

// Unwrap allows http.ResponseController to reach the underlying writer.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// AccessLog writes one structured line per request.
func AccessLog(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w}
			next.ServeHTTP(rec, r)
			if rec.status == 0 {
				rec.status = http.StatusOK
			}
			level := slog.LevelInfo
			if rec.status >= 500 {
				level = slog.LevelError
			} else if rec.status >= 400 {
				level = slog.LevelWarn
			}
			logger.Log(r.Context(), level, "http request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"bytes", rec.bytes,
				"duration_ms", time.Since(start).Milliseconds(),
				"remote_ip", RemoteIP(r),
				"request_id", RequestIDFromContext(r.Context()),
			)
		})
	}
}

// SecurityHeaders sets conservative defaults. The panel is an admin interface,
// so it is never embedded and never cached.
func SecurityHeaders() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Referrer-Policy", "no-referrer")
			h.Set("Cross-Origin-Opener-Policy", "same-origin")
			h.Set("Cache-Control", "no-store")
			next.ServeHTTP(w, r)
		})
	}
}

// Timeout bounds handler execution. Streaming endpoints (the WebSocket
// gateway) must not be wrapped with it.
func Timeout(d time.Duration) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// ---- client address --------------------------------------------------------

// RealIP replaces r.RemoteAddr with the client address reported by a trusted
// reverse proxy. X-Forwarded-For is only honoured when the direct peer is
// inside one of trustedCIDRs, which prevents header spoofing.
func RealIP(trustedCIDRs []string) (Middleware, error) {
	nets := make([]*net.IPNet, 0, len(trustedCIDRs))
	for _, cidr := range trustedCIDRs {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			return nil, fmt.Errorf("invalid trusted proxy CIDR %q: %w", cidr, err)
		}
		nets = append(nets, ipNet)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			peer := peerIP(r.RemoteAddr)
			if peer != nil && ipInAny(peer, nets) {
				if forwarded := parseForwardedFor(r.Header.Get("X-Forwarded-For")); forwarded != nil {
					r.RemoteAddr = forwarded.String()
				} else if real := net.ParseIP(strings.TrimSpace(r.Header.Get("X-Real-IP"))); real != nil {
					r.RemoteAddr = real.String()
				}
			}
			next.ServeHTTP(w, r)
		})
	}, nil
}

// RemoteIP returns the client IP of the request without the port.
func RemoteIP(r *http.Request) string {
	if ip := peerIP(r.RemoteAddr); ip != nil {
		return ip.String()
	}
	return r.RemoteAddr
}

func peerIP(remoteAddr string) net.IP {
	if remoteAddr == "" {
		return nil
	}
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return net.ParseIP(host)
	}
	return net.ParseIP(remoteAddr)
}

func parseForwardedFor(value string) net.IP {
	if value == "" {
		return nil
	}
	// The left-most entry is the original client.
	for _, part := range strings.Split(value, ",") {
		if ip := net.ParseIP(strings.TrimSpace(part)); ip != nil {
			return ip
		}
	}
	return nil
}

func ipInAny(ip net.IP, nets []*net.IPNet) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}