package course_test

import (
	"encoding/json"
	"fmt"
	"net/http"
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
