// Package testutil holds helpers shared by integration tests.
package testutil

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aksornproject/aksorn-senior-backend-assessment/internal/course"
	"github.com/aksornproject/aksorn-senior-backend-assessment/internal/infra"
)

// Container runs the real HTTP handler against the supplied PostgreSQL.
type Container struct {
	t      testing.TB
	server *httptest.Server
	prefix string
	seq    int
}

// NewContainer starts the handler and removes the data it created when the test ends.
// The test fails when PostgreSQL or Redis is unreachable (run make infra-up);
// set SKIP_INTEGRATION=1 to skip instead.
func NewContainer(t testing.TB) *Container {
	t.Helper()

	conns, err := infra.Open(context.Background())
	if err != nil {
		// Fail by default so a missing database cannot make the suite look green.
		if os.Getenv("SKIP_INTEGRATION") != "" {
			t.Skipf("SKIP_INTEGRATION set; PostgreSQL and Redis unavailable: %v", err)
		}

		t.Fatalf("integration test needs PostgreSQL and Redis (run make infra-up, or set SKIP_INTEGRATION=1): %v", err)
	}

	service := course.NewService(course.NewRepository(conns.Postgres))
	server := httptest.NewServer(course.NewHandler(service))
	prefix := fmt.Sprintf("it-%d-", time.Now().UnixNano())

	t.Cleanup(func() {
		server.Close()
		_, _ = conns.Postgres.Exec(context.Background(), "DELETE FROM attempts WHERE learner_id LIKE $1", prefix+"%")
		conns.Close()
	})

	return &Container{t: t, server: server, prefix: prefix}
}

// Prefix is prepended to every learner and attempt ID so runs stay independent.
func (a *Container) Prefix() string { return a.prefix }

// Learner returns a learner ID unique to this test run.
func (a *Container) Learner(name string) string { return a.prefix + name }

// Do sends a request and returns the status code and raw response body.
func (a *Container) Do(method, path, contentType, body string) (int, string) {
	a.t.Helper()

	req, err := http.NewRequest(method, a.server.URL+path, strings.NewReader(body))
	if err != nil {
		a.t.Fatal(err)
	}

	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		a.t.Fatal(err)
	}

	return resp.StatusCode, string(raw)
}

// Post sends an attempt with a fresh attempt_id and the given raw score JSON.
func (a *Container) Post(learner, lesson, score string) (int, string) {
	a.t.Helper()

	a.seq++
	body := fmt.Sprintf(
		`{"attempt_id":"%sa%d","learner_id":"%s","course_id":"course-001","lesson_id":"%s","score":%s}`,
		a.prefix, a.seq, learner, lesson, score,
	)

	return a.Do(http.MethodPost, "/attempts", "application/json", body)
}

// MustPost records an attempt and fails the test unless the server returns 201.
func (a *Container) MustPost(learner, lesson string, score int) {
	a.t.Helper()

	if status, body := a.Post(learner, lesson, fmt.Sprint(score)); status != http.StatusCreated {
		a.t.Fatalf("POST %s %d = %d %s", lesson, score, status, body)
	}
}

// Progress fetches and decodes a learner's progress, failing the test unless the server returns 200.
func (a *Container) Progress(learner string) course.Progress {
	a.t.Helper()

	status, body := a.Do(http.MethodGet, "/learners/"+learner+"/courses/course-001/progress", "", "")
	if status != http.StatusOK {
		a.t.Fatalf("GET progress = %d %s", status, body)
	}

	var p course.Progress
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		a.t.Fatalf("decode %q: %v", body, err)
	}

	return p
}
