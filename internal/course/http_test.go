package course_test

import (
	"context"
	"errors"
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
