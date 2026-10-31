package course_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aksornproject/aksorn-senior-backend-assessment/internal/course"
)

const validAttemptBody = `{"attempt_id":"h-1","learner_id":"l1","course_id":"course-001","lesson_id":"practice","score":50}`

func serve(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	return rec
}

func TestServiceErrorsMapToStatusCodes(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"not implemented", course.ErrNotImplemented, http.StatusNotImplemented},
		{"conflict", course.ErrConflict, http.StatusConflict},
		{"invalid input", course.ErrInvalidInput, http.StatusBadRequest},
		{"unknown lesson", course.ErrUnknownLesson, http.StatusNotFound},
		{"unexpected", errors.New("boom"), http.StatusInternalServerError},
	}
	for _, c := range cases {
		h := course.NewHandler(course.NewService(&memStore{addErr: c.err}))

		rec := serve(h, http.MethodPost, "/attempts", validAttemptBody)
		if rec.Code != c.want {
			t.Errorf("%s: got %d %s, want %d", c.name, rec.Code, rec.Body, c.want)
		}

		// An unexpected error must not leak its text to the client.
		if c.want == http.StatusInternalServerError && strings.Contains(rec.Body.String(), "boom") {
			t.Errorf("%s: response leaks error: %s", c.name, rec.Body)
		}
	}
}

// failingWriter fails every body write, like a client that disconnected mid-response.
type failingWriter struct{ header http.Header }

func (w *failingWriter) Header() http.Header        { return w.header }
func (w *failingWriter) Write([]byte) (int, error)  { return 0, errors.New("closed") }
func (w *failingWriter) WriteHeader(statusCode int) {}

func TestResponseWriteFailureDoesNotPanic(t *testing.T) {
	h := course.NewHandler(course.NewService(&memStore{}))
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)

	h.ServeHTTP(&failingWriter{header: http.Header{}}, req)
}

// abortStore panics the way net/http does to abort a response on purpose.
type abortStore struct{ memStore }

func (abortStore) BestScores(context.Context, string, string) (map[string]int, error) {
	panic(http.ErrAbortHandler)
}

func TestRecovererLeavesAbortHandlerPanicAlone(t *testing.T) {
	h := course.NewHandler(course.NewService(&abortStore{}))

	defer func() {
		if got := recover(); got != http.ErrAbortHandler {
			t.Fatalf("recovered %v, want http.ErrAbortHandler", got)
		}
	}()

	serve(h, http.MethodGet, "/learners/l1/courses/course-001/progress", "")
	t.Fatal("expected panic to propagate")
}

// captureLogs swaps in a JSON logger and returns a function that decodes every line logged so far.
func captureLogs(t *testing.T) func() []map[string]any {
	t.Helper()

	var buf bytes.Buffer

	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	return func() []map[string]any {
		var entries []map[string]any

		for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
			if line == "" {
				continue
			}

			var entry map[string]any
			if err := json.Unmarshal([]byte(line), &entry); err != nil {
				t.Fatalf("log line is not JSON: %q", line)
			}

			entries = append(entries, entry)
		}

		return entries
	}
}

func requestWithID(h http.Handler, id string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	if id != "" {
		req.Header.Set("X-Request-Id", id)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	return rec
}

func TestRequestIDIsGeneratedWhenMissing(t *testing.T) {
	h := course.NewHandler(course.NewService(&memStore{}))

	first := requestWithID(h, "").Header().Get("X-Request-Id")
	second := requestWithID(h, "").Header().Get("X-Request-Id")

	if first == "" || first == second {
		t.Fatalf("ids = %q, %q; want distinct non-empty values", first, second)
	}
}

func TestRequestIDKeepsValidCallerValue(t *testing.T) {
	h := course.NewHandler(course.NewService(&memStore{}))

	if got := requestWithID(h, "trace-1.a_B").Header().Get("X-Request-Id"); got != "trace-1.a_B" {
		t.Fatalf("got %q", got)
	}
}

func TestRequestIDReplacesUnsafeCallerValue(t *testing.T) {
	h := course.NewHandler(course.NewService(&memStore{}))

	for _, bad := range []string{"has space", "semi;colon", "quote\"", strings.Repeat("a", 65)} {
		if got := requestWithID(h, bad).Header().Get("X-Request-Id"); got == bad || got == "" {
			t.Errorf("unsafe id %q was kept or dropped: %q", bad, got)
		}
	}
}

func TestAccessLogRecordsRequest(t *testing.T) {
	logs := captureLogs(t)
	h := course.NewHandler(course.NewService(&memStore{}))

	req := httptest.NewRequest(http.MethodGet, "/nope", nil)
	req.Header.Set("X-Request-Id", "abc-123")
	h.ServeHTTP(httptest.NewRecorder(), req)

	entries := logs()
	if len(entries) != 1 {
		t.Fatalf("got %d log lines: %v", len(entries), entries)
	}

	e := entries[0]
	if e["request_id"] != "abc-123" || e["method"] != "GET" || e["path"] != "/nope" || e["status"] != float64(404) {
		t.Fatalf("unexpected entry: %v", e)
	}
}

func TestAccessLogIncludesBodies(t *testing.T) {
	logs := captureLogs(t)
	h := course.NewHandler(course.NewService(&memStore{}))

	serve(h, http.MethodPost, "/attempts", validAttemptBody)

	e := logs()[0]
	if e["request_body"] != validAttemptBody {
		t.Errorf("request_body = %v", e["request_body"])
	}

	// The echoed attempt is logged exactly as sent to the client.
	if body, _ := e["response_body"].(string); !strings.Contains(body, `"attempt_id":"h-1"`) {
		t.Errorf("response_body = %v", e["response_body"])
	}
}

func TestAccessLogTruncatesLargeBodies(t *testing.T) {
	logs := captureLogs(t)
	h := course.NewHandler(course.NewService(&memStore{}))

	big := `{"attempt_id":"` + strings.Repeat("a", 5000) + `","learner_id":"l1","course_id":"course-001","lesson_id":"practice","score":50}`
	rec := serve(h, http.MethodPost, "/attempts", big)

	// Logging must not change what the client gets: this body is valid and under the 64 KiB limit.
	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}

	body, _ := logs()[0]["request_body"].(string)
	if !strings.HasSuffix(body, "...(truncated)") || len(body) > 2<<10+len("...(truncated)") {
		t.Fatalf("request_body length %d, suffix %q", len(body), body[max(0, len(body)-20):])
	}
}

func TestAccessLogAndPanicLogShareRequestID(t *testing.T) {
	logs := captureLogs(t)
	h := course.NewHandler(course.NewService(&panicStore{}))

	req := httptest.NewRequest(http.MethodGet, "/learners/l1/courses/course-001/progress", nil)
	req.Header.Set("X-Request-Id", "boom-1")
	h.ServeHTTP(httptest.NewRecorder(), req)

	entries := logs()
	if len(entries) != 2 {
		t.Fatalf("want panic log and access log, got %v", entries)
	}

	for _, e := range entries {
		if e["request_id"] != "boom-1" {
			t.Errorf("entry without the request id: %v", e)
		}
	}

	if entries[1]["status"] != float64(500) {
		t.Errorf("access log status = %v, want 500", entries[1]["status"])
	}
}

// panicStore panics with an ordinary value, which the recoverer must handle.
type panicStore struct{ memStore }

func (panicStore) BestScores(context.Context, string, string) (map[string]int, error) {
	panic("boom")
}
