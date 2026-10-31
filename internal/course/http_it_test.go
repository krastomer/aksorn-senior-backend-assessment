package course_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aksornproject/aksorn-senior-backend-assessment/internal/course"
	"github.com/aksornproject/aksorn-senior-backend-assessment/internal/testutil"
)

func TestHTTPHealth(t *testing.T) {
	ctr := testutil.NewContainer(t)

	status, body := ctr.Do(http.MethodGet, "/healthz", "", "")
	if status != http.StatusOK || strings.TrimSpace(body) != `{"status":"ok"}` {
		t.Fatalf("got %d %s", status, body)
	}
}

func TestHTTPRecordAttemptEchoesAttempt(t *testing.T) {
	ctr := testutil.NewContainer(t)
	learner := ctr.Learner("echo")

	status, body := ctr.Post(learner, "introduction", "60")
	if status != http.StatusCreated {
		t.Fatalf("got %d %s", status, body)
	}

	var got course.Attempt
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}

	if got.LearnerID != learner || got.LessonID != "introduction" || got.Score != 60 {
		t.Fatalf("echo = %+v", got)
	}
}

func TestHTTPRecordAttemptRejections(t *testing.T) {
	ctr := testutil.NewContainer(t)
	learner := ctr.Learner("reject")
	valid := fmt.Sprintf(
		`{"attempt_id":"%sx","learner_id":"%s","course_id":"course-001","lesson_id":"practice","score":50}`,
		ctr.Prefix(), learner,
	)

	cases := []struct {
		name        string
		contentType string
		body        string
		want        int
	}{
		{"wrong content type", "text/plain", valid, http.StatusUnsupportedMediaType},
		{"missing content type", "", valid, http.StatusUnsupportedMediaType},
		{"malformed json", "application/json", `{`, http.StatusBadRequest},
		{"unknown field", "application/json", strings.Replace(valid, `"score":50`, `"score":50,"extra":1`, 1), http.StatusBadRequest},
		{"trailing data", "application/json", valid + valid, http.StatusBadRequest},
		{"missing score", "application/json", strings.Replace(valid, `,"score":50`, ``, 1), http.StatusBadRequest},
		{"null score", "application/json", strings.Replace(valid, `"score":50`, `"score":null`, 1), http.StatusBadRequest},
		{"fractional score", "application/json", strings.Replace(valid, `"score":50`, `"score":50.5`, 1), http.StatusBadRequest},
		{"score above 100", "application/json", strings.Replace(valid, `"score":50`, `"score":101`, 1), http.StatusBadRequest},
		{"negative score", "application/json", strings.Replace(valid, `"score":50`, `"score":-1`, 1), http.StatusBadRequest},
		{"blank learner", "application/json", strings.Replace(valid, learner, " ", 1), http.StatusBadRequest},
		{"unknown course", "application/json", strings.Replace(valid, `"course-001"`, `"other"`, 1), http.StatusNotFound},
		{"unknown lesson", "application/json", strings.Replace(valid, `"practice"`, `"missing"`, 1), http.StatusNotFound},
		{"body over 64 KiB", "application/json", `{"attempt_id":"` + strings.Repeat("a", 70<<10) + `"}`, http.StatusRequestEntityTooLarge},
	}
	for _, c := range cases {
		if status, body := ctr.Do(http.MethodPost, "/attempts", c.contentType, c.body); status != c.want {
			t.Errorf("%s: got %d %s, want %d", c.name, status, body, c.want)
		}
	}

	// Rejected requests must not have been stored.
	if p := ctr.Progress(learner); p.ProgressPercent != 0 || lessonOf(t, p, "practice").BestScore != nil {
		t.Fatalf("rejected attempt was stored: %+v", p)
	}
}

func TestHTTPProgressForLearnerWithoutAttempts(t *testing.T) {
	ctr := testutil.NewContainer(t)
	learner := ctr.Learner("none")

	status, body := ctr.Do(http.MethodGet, "/learners/"+learner+"/courses/course-001/progress", "", "")
	if status != http.StatusOK {
		t.Fatalf("got %d %s", status, body)
	}

	// best_score must be an explicit null, not omitted.
	if !strings.Contains(body, `"best_score":null`) {
		t.Fatalf("expected null best_score in %s", body)
	}

	p := ctr.Progress(learner)
	if len(p.Lessons) != 3 || p.ProgressPercent != 0 || p.Completed {
		t.Fatalf("got %+v", p)
	}
}

func TestHTTPProgressUnknownCourse(t *testing.T) {
	ctr := testutil.NewContainer(t)

	if status, body := ctr.Do(http.MethodGet, "/learners/"+ctr.Learner("x")+"/courses/other/progress", "", ""); status != http.StatusNotFound {
		t.Fatalf("got %d %s", status, body)
	}
}

func TestHTTPProgressAcrossAttempts(t *testing.T) {
	ctr := testutil.NewContainer(t)
	learner := ctr.Learner("flow")

	ctr.MustPost(learner, "introduction", 40)
	ctr.MustPost(learner, "introduction", 60)
	ctr.MustPost(learner, "introduction", 10) // later lower score must not undo the pass

	p := ctr.Progress(learner)
	if l := lessonOf(t, p, "introduction"); !l.Passed || !hasBest(l, 60) || p.ProgressPercent != 50 || p.Completed {
		t.Fatalf("after introduction: %+v", p)
	}

	ctr.MustPost(learner, "challenge", 100) // optional lesson must not change progress

	if p := ctr.Progress(learner); p.ProgressPercent != 50 || p.Completed {
		t.Fatalf("after challenge: %+v", p)
	}

	ctr.MustPost(learner, "practice", 0)

	p = ctr.Progress(learner)
	if l := lessonOf(t, p, "practice"); l.BestScore == nil || *l.BestScore != 0 || l.Passed {
		t.Fatalf("zero score: %+v", l)
	}

	ctr.MustPost(learner, "practice", 70)

	if p := ctr.Progress(learner); p.ProgressPercent != 100 || !p.Completed {
		t.Fatalf("after practice: %+v", p)
	}
}

func TestHTTPLearnerIsolation(t *testing.T) {
	ctr := testutil.NewContainer(t)
	a, b := ctr.Learner("a"), ctr.Learner("b")

	ctr.MustPost(a, "introduction", 100)
	ctr.MustPost(a, "practice", 100)

	if p := ctr.Progress(b); p.ProgressPercent != 0 || p.Completed || lessonOf(t, p, "introduction").BestScore != nil {
		t.Fatalf("learner b saw learner a data: %+v", p)
	}

	if p := ctr.Progress(a); !p.Completed {
		t.Fatalf("learner a: %+v", p)
	}
}

func TestHTTPRoutingErrors(t *testing.T) {
	ctr := testutil.NewContainer(t)

	if status, _ := ctr.Do(http.MethodGet, "/nope", "", ""); status != http.StatusNotFound {
		t.Errorf("unknown route = %d", status)
	}

	if status, _ := ctr.Do(http.MethodGet, "/attempts", "", ""); status != http.StatusMethodNotAllowed {
		t.Errorf("GET /attempts = %d", status)
	}
}

func TestHTTPStorageFailureReturns500(t *testing.T) {
	ctr := testutil.NewBrokenContainer(t)
	learner := ctr.Learner("down")

	status, body := ctr.Post(learner, "introduction", "60")
	if status != http.StatusInternalServerError || strings.TrimSpace(body) != `{"error":"internal error"}` {
		t.Errorf("POST /attempts = %d %s", status, body)
	}

	status, body = ctr.Do(http.MethodGet, "/learners/"+learner+"/courses/course-001/progress", "", "")
	if status != http.StatusInternalServerError || strings.TrimSpace(body) != `{"error":"internal error"}` {
		t.Errorf("GET progress = %d %s", status, body)
	}
}

func TestRepositoryReturnsErrorsWhenStorageIsDown(t *testing.T) {
	repo := testutil.NewBrokenContainer(t).Repo()
	ctx := context.Background()

	if _, err := repo.List(ctx, "l1", course.CourseID); err == nil {
		t.Error("List: expected error")
	}

	if _, err := repo.BestScores(ctx, "l1", course.CourseID); err == nil {
		t.Error("BestScores: expected error")
	}

	a := course.Attempt{AttemptID: "x", LearnerID: "l1", CourseID: course.CourseID, LessonID: "practice", Score: 1}
	if err := repo.Add(ctx, a); err == nil {
		t.Error("Add: expected error")
	}
}

func TestHTTPSimultaneousAttemptsKeepHigherScore(t *testing.T) {
	ctr := testutil.NewContainer(t)

	for round := range 20 {
		learner := ctr.Learner(fmt.Sprintf("race-%d", round))
		scores := []int{90, 70}
		statuses := make([]int, len(scores))

		var wg sync.WaitGroup

		for i, score := range scores {
			wg.Add(1)

			go func() {
				defer wg.Done()

				statuses[i], _ = ctr.Post(learner, "introduction", fmt.Sprint(score))
			}()
		}

		wg.Wait()

		for i, status := range statuses {
			if status != http.StatusCreated {
				t.Fatalf("round %d: POST score %d = %d", round, scores[i], status)
			}
		}

		if l := lessonOf(t, ctr.Progress(learner), "introduction"); !hasBest(l, 90) {
			t.Fatalf("round %d: got %+v, want best score 90", round, l)
		}
	}
}

func storedAttempts(t *testing.T, ctr *testutil.Container, learner string) []course.Attempt {
	t.Helper()

	attempts, err := ctr.Repo().List(context.Background(), learner, course.CourseID)
	if err != nil {
		t.Fatal(err)
	}

	return attempts
}

func TestHTTPIdenticalReplayStoresOnce(t *testing.T) {
	ctr := testutil.NewContainer(t)
	learner := ctr.Learner("replay")

	for range 3 {
		if status, body := ctr.PostWithID("same", learner, "introduction", "60"); status != http.StatusCreated {
			t.Fatalf("replay = %d %s", status, body)
		}
	}

	if got := storedAttempts(t, ctr, learner); len(got) != 1 {
		t.Fatalf("stored %d rows, want 1: %+v", len(got), got)
	}
}

func TestHTTPSameIDWithDifferentContentConflicts(t *testing.T) {
	ctr := testutil.NewContainer(t)
	learner := ctr.Learner("conflict")

	if status, body := ctr.PostWithID("same", learner, "introduction", "60"); status != http.StatusCreated {
		t.Fatalf("first = %d %s", status, body)
	}

	for name, c := range map[string]struct{ learner, lesson, score string }{
		"score":   {learner, "introduction", "90"},
		"lesson":  {learner, "practice", "60"},
		"learner": {ctr.Learner("other"), "introduction", "60"},
	} {
		if status, body := ctr.PostWithID("same", c.learner, c.lesson, c.score); status != http.StatusConflict {
			t.Errorf("different %s = %d %s, want 409", name, status, body)
		}
	}

	got := storedAttempts(t, ctr, learner)
	if len(got) != 1 || got[0].Score != 60 || got[0].LessonID != "introduction" {
		t.Fatalf("data changed: %+v", got)
	}

	if len(storedAttempts(t, ctr, ctr.Learner("other"))) != 0 {
		t.Fatal("conflicting attempt was stored for another learner")
	}
}

func TestHTTPSimultaneousSameIDStoresOnce(t *testing.T) {
	ctr := testutil.NewContainer(t)

	for round := range 10 {
		learner := ctr.Learner(fmt.Sprintf("dup-%d", round))
		id := fmt.Sprintf("dup-%d", round)
		scores := []string{"60", "60", "60", "90", "90"} // three identical, two conflicting
		statuses := make([]int, len(scores))

		var wg sync.WaitGroup

		for i, score := range scores {
			wg.Add(1)

			go func() {
				defer wg.Done()

				statuses[i], _ = ctr.PostWithID(id, learner, "introduction", score)
			}()
		}

		wg.Wait()

		got := storedAttempts(t, ctr, learner)
		if len(got) != 1 {
			t.Fatalf("round %d: stored %d rows, want 1", round, len(got))
		}

		// Whichever request won decides which content is the original; the rest must match 201 or 409.
		for i, status := range statuses {
			want := http.StatusConflict
			if fmt.Sprint(got[0].Score) == scores[i] {
				want = http.StatusCreated
			}

			if status != want {
				t.Fatalf("round %d: score %s = %d, want %d (stored %d)", round, scores[i], status, want, got[0].Score)
			}
		}
	}
}

func TestRepositoryBestScoresAndList(t *testing.T) {
	ctr := testutil.NewContainer(t)
	learner := ctr.Learner("repo")
	ctx := context.Background()

	best, err := ctr.Repo().BestScores(ctx, learner, course.CourseID)
	if err != nil || len(best) != 0 {
		t.Fatalf("empty = %v, %v", best, err)
	}

	ctr.MustPost(learner, "introduction", 40)
	ctr.MustPost(learner, "introduction", 80)
	ctr.MustPost(learner, "introduction", 10)
	ctr.MustPost(learner, "practice", 0)
	ctr.MustPost(ctr.Learner("repo-other"), "practice", 100)

	best, err = ctr.Repo().BestScores(ctx, learner, course.CourseID)
	if err != nil {
		t.Fatal(err)
	}

	if len(best) != 2 || best["introduction"] != 80 || best["practice"] != 0 {
		t.Fatalf("best = %v", best)
	}

	if got := storedAttempts(t, ctr, learner); len(got) != 4 || got[0].Score != 40 || got[3].LessonID != "practice" {
		t.Fatalf("list = %+v", got)
	}
}

func TestHTTPErrorsAreJSON(t *testing.T) {
	ctr := testutil.NewContainer(t)

	for name, c := range map[string]struct {
		method, path string
		want         int
	}{
		"unknown route":  {http.MethodGet, "/nope", http.StatusNotFound},
		"wrong method":   {http.MethodGet, "/attempts", http.StatusMethodNotAllowed},
		"wrong method 2": {http.MethodPost, "/healthz", http.StatusMethodNotAllowed},
	} {
		status, body := ctr.Do(c.method, c.path, "", "")
		if status != c.want || !strings.HasPrefix(body, `{"error":`) {
			t.Errorf("%s: got %d %q", name, status, body)
		}
	}
}

func TestHTTPPanicReturns500(t *testing.T) {
	// A nil service panics on first use; the recoverer must turn that into a JSON 500.
	server := httptest.NewServer(course.NewHandler(nil))
	defer server.Close()

	resp, err := http.Get(server.URL + "/learners/l1/courses/course-001/progress")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusInternalServerError || strings.TrimSpace(string(body)) != `{"error":"internal error"}` {
		t.Fatalf("got %d %s", resp.StatusCode, body)
	}
}
