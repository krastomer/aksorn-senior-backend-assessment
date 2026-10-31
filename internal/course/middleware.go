package course

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"time"
)

const (
	requestIDHeader = "X-Request-Id"
	maxRequestIDLen = 64
)

type requestIDKey struct{}

func requestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// validRequestID accepts only short, log-safe IDs so a caller cannot inject arbitrary text into logs.
func validRequestID(id string) bool {
	if id == "" || len(id) > maxRequestIDLen {
		return false
	}

	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == '-':
		default:
			return false
		}
	}

	return true
}

// requestID keeps the caller's X-Request-Id when it is valid, otherwise generates one,
// and makes it available to later middleware, handlers and the response header.
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(requestIDHeader)
		if !validRequestID(id) {
			id = rand.Text()
		}

		w.Header().Set(requestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
	})
}

// maxLoggedBody caps how much of each body is copied into a log line.
const maxLoggedBody = 2 << 10

// bodyCapture keeps the first maxLoggedBody bytes written to it and notes if more was dropped.
type bodyCapture struct {
	buf       bytes.Buffer
	truncated bool
}

func (c *bodyCapture) Write(p []byte) (int, error) {
	room := maxLoggedBody - c.buf.Len()

	switch {
	case len(p) <= room:
		c.buf.Write(p)
	case room > 0:
		c.buf.Write(p[:room])

		c.truncated = true
	default:
		c.truncated = true
	}

	return len(p), nil
}

func (c *bodyCapture) String() string {
	if c.truncated {
		return c.buf.String() + "...(truncated)"
	}

	return c.buf.String()
}

// statusRecorder remembers the status code and body a handler wrote.
type statusRecorder struct {
	http.ResponseWriter
	status int
	body   bodyCapture
}

func (s *statusRecorder) WriteHeader(status int) {
	s.status = status
	s.ResponseWriter.WriteHeader(status)
}

func (s *statusRecorder) Write(p []byte) (int, error) {
	n, err := s.ResponseWriter.Write(p)
	_, _ = s.body.Write(p[:n])

	return n, err
}

// teeBody copies bytes into capture as the handler reads them, so reading behavior is unchanged.
type teeBody struct {
	io.Reader
	io.Closer
}

// accessLog logs one line per request after it finishes, including the first bytes of both bodies.
// Only bytes the handler actually read are logged, so a request rejected before reading shows an empty body.
func accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		var requestBody bodyCapture

		r.Body = teeBody{Reader: io.TeeReader(r.Body, &requestBody), Closer: r.Body}

		next.ServeHTTP(rec, r)

		slog.Info("request",
			"request_id", requestIDFrom(r.Context()),
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration", time.Since(start),
			"request_body", requestBody.String(),
			"response_body", rec.body.String(),
		)
	})
}

// recoverer turns a panic in any handler into a logged JSON 500 instead of a dropped connection.
func recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}

			// net/http uses this value to abort a response on purpose; it must keep propagating.
			if rec == http.ErrAbortHandler {
				panic(rec)
			}

			slog.Error("panic while handling request", "panic", rec, "request_id", requestIDFrom(r.Context()), "method", r.Method, "path", r.URL.Path)
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: "internal error"})
		}()

		next.ServeHTTP(w, r)
	})
}

// requireJSON rejects requests whose Content-Type is not application/json.
func requireJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			writeJSON(w, http.StatusUnsupportedMediaType, errorResponse{Error: "use application/json"})
			return
		}

		next.ServeHTTP(w, r)
	})
}

// limitBody makes reads past max bytes fail with *http.MaxBytesError.
func limitBody(max int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, max)
			next.ServeHTTP(w, r)
		})
	}
}
