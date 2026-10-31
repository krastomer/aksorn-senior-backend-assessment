package course_test

import (
	"bytes"
	"context"
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

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()

	var buf bytes.Buffer

	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	return &buf
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

	for _, want := range []string{"request_id=abc-123", "method=GET", "path=/nope", "status=404"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log missing %q: %s", want, logs)
		}
	}
}

func TestAccessLogAndPanicLogShareRequestID(t *testing.T) {
	logs := captureLogs(t)
	h := course.NewHandler(course.NewService(&panicStore{}))

	req := httptest.NewRequest(http.MethodGet, "/learners/l1/courses/course-001/progress", nil)
	req.Header.Set("X-Request-Id", "boom-1")
	h.ServeHTTP(httptest.NewRecorder(), req)

	out := logs.String()
	if strings.Count(out, "request_id=boom-1") != 2 || !strings.Contains(out, "status=500") {
		t.Fatalf("want panic log and access log (status 500) with the same id:\n%s", out)
	}
}

// panicStore panics with an ordinary value, which the recoverer must handle.
type panicStore struct{ memStore }

func (panicStore) BestScores(context.Context, string, string) (map[string]int, error) {
	panic("boom")
}
