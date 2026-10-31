package course_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/aksornproject/aksorn-senior-backend-assessment/internal/course"
)

// memStore is an in-memory attemptStore. Set addErr/bestErr to simulate storage failures.
type memStore struct {
	attempts  []course.Attempt
	addErr    error
	bestErr   error
	bestCalls int
}

func (m *memStore) Add(_ context.Context, a course.Attempt) error {
	if m.addErr != nil {
		return m.addErr
	}

	m.attempts = append(m.attempts, a)

	return nil
}

func (m *memStore) BestScores(_ context.Context, learnerID, courseID string) (map[string]int, error) {
	m.bestCalls++

	if m.bestErr != nil {
		return nil, m.bestErr
	}

	best := map[string]int{}

	for _, a := range m.attempts {
		if a.LearnerID != learnerID || a.CourseID != courseID {
			continue
		}

		if cur, ok := best[a.LessonID]; !ok || a.Score > cur {
			best[a.LessonID] = a.Score
		}
	}

	return best, nil
}

var attemptSeq atomic.Int64

// hasBest reports whether the lesson has a best score equal to want, without dereferencing nil.
func hasBest(l course.LessonProgress, want int) bool {
	return l.BestScore != nil && *l.BestScore == want
}

func record(t *testing.T, s *course.Service, learner, lesson string, score int) {
	t.Helper()

	a := course.Attempt{
		AttemptID: fmt.Sprintf("a-%d", attemptSeq.Add(1)),
		LearnerID: learner,
		CourseID:  course.CourseID,
		LessonID:  lesson,
		Score:     score,
	}
	if err := s.RecordAttempt(context.Background(), a); err != nil {
		t.Fatalf("RecordAttempt: %v", err)
	}
}

func progress(t *testing.T, s *course.Service, learner string) course.Progress {
	t.Helper()

	p, err := s.Progress(context.Background(), learner, course.CourseID)
	if err != nil {
		t.Fatalf("Progress: %v", err)
	}

	return p
}

func lessonOf(t *testing.T, p course.Progress, id string) course.LessonProgress {
	t.Helper()

	for _, l := range p.Lessons {
		if l.LessonID == id {
			return l
		}
	}

	t.Fatalf("lesson %q missing from %+v", id, p.Lessons)

	return course.LessonProgress{}
}

func TestRecordAttemptRejectsUnknownLesson(t *testing.T) {
	// Invalid input is rejected before storage is accessed.
	service := course.NewService(nil)

	attempt := course.Attempt{
		AttemptID: "example-001",
		LearnerID: "learner-001",
		CourseID:  course.CourseID,
		LessonID:  "missing-lesson",
		Score:     75,
	}
	if err := service.RecordAttempt(
		context.Background(),
		attempt,
	); !errors.Is(
		err,
		course.ErrUnknownLesson,
	) {
		t.Fatalf("RecordAttempt error = %v, want ErrUnknownLesson", err)
	}
}

func TestRecordAttemptStoresAndKeepsMultipleAttempts(t *testing.T) {
	store := &memStore{}
	s := course.NewService(store)
	record(t, s, "l1", "introduction", 40)
	record(t, s, "l1", "introduction", 80)

	if len(store.attempts) != 2 {
		t.Fatalf("stored %d attempts, want 2", len(store.attempts))
	}
}

func TestRecordAttemptDoesNotStoreInvalid(t *testing.T) {
	store := &memStore{}
	s := course.NewService(store)

	cases := map[string]course.Attempt{
		"blank id":       {AttemptID: " ", LearnerID: "l1", CourseID: course.CourseID, LessonID: "practice", Score: 50},
		"score too high": {AttemptID: "x", LearnerID: "l1", CourseID: course.CourseID, LessonID: "practice", Score: 101},
		"score negative": {AttemptID: "x", LearnerID: "l1", CourseID: course.CourseID, LessonID: "practice", Score: -1},
		"unknown course": {AttemptID: "x", LearnerID: "l1", CourseID: "other", LessonID: "practice", Score: 50},
	}
	for name, a := range cases {
		if err := s.RecordAttempt(context.Background(), a); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}

	if len(store.attempts) != 0 {
		t.Fatalf("invalid attempts were stored: %+v", store.attempts)
	}
}

func TestRecordAttemptAcceptsScoreBounds(t *testing.T) {
	s := course.NewService(&memStore{})
	record(t, s, "l1", "practice", 0)
	record(t, s, "l1", "practice", 100)
}

func TestRecordAttemptPropagatesStoreError(t *testing.T) {
	boom := errors.New("boom")
	s := course.NewService(&memStore{addErr: boom})
	a := course.Attempt{AttemptID: "x", LearnerID: "l1", CourseID: course.CourseID, LessonID: "practice", Score: 50}

	if err := s.RecordAttempt(context.Background(), a); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
}

func TestCancelledContext(t *testing.T) {
	s := course.NewService(&memStore{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	a := course.Attempt{AttemptID: "x", LearnerID: "l1", CourseID: course.CourseID, LessonID: "practice", Score: 50}
	if err := s.RecordAttempt(ctx, a); !errors.Is(err, context.Canceled) {
		t.Fatalf("RecordAttempt err = %v", err)
	}

	if _, err := s.Progress(ctx, "l1", course.CourseID); !errors.Is(err, context.Canceled) {
		t.Fatalf("Progress err = %v", err)
	}
}

func TestProgressNoAttempts(t *testing.T) {
	p := progress(t, course.NewService(&memStore{}), "l1")
	if len(p.Lessons) != 3 || p.ProgressPercent != 0 || p.Completed {
		t.Fatalf("unexpected progress: %+v", p)
	}

	for _, l := range p.Lessons {
		if l.BestScore != nil || l.Passed {
			t.Fatalf("lesson %s should be empty: %+v", l.LessonID, l)
		}
	}
}

func TestPassingThresholds(t *testing.T) {
	cases := []struct {
		lesson string
		score  int
		passed bool
	}{
		{"introduction", 59, false}, {"introduction", 60, true},
		{"practice", 69, false}, {"practice", 70, true},
		{"challenge", 79, false}, {"challenge", 80, true},
	}
	for _, c := range cases {
		s := course.NewService(&memStore{})
		record(t, s, "l1", c.lesson, c.score)

		got := lessonOf(t, progress(t, s, "l1"), c.lesson)
		if got.Passed != c.passed || got.BestScore == nil || *got.BestScore != c.score {
			t.Errorf("%s score %d: got %+v, want passed=%v", c.lesson, c.score, got, c.passed)
		}
	}
}

func TestZeroScoreIsNotNull(t *testing.T) {
	s := course.NewService(&memStore{})
	record(t, s, "l1", "introduction", 0)

	l := lessonOf(t, progress(t, s, "l1"), "introduction")
	if l.BestScore == nil || *l.BestScore != 0 || l.Passed {
		t.Fatalf("got %+v", l)
	}
}

func TestBestScoreIgnoresInsertionOrder(t *testing.T) {
	s := course.NewService(&memStore{})
	record(t, s, "l1", "introduction", 50)
	record(t, s, "l1", "introduction", 90)
	record(t, s, "l1", "introduction", 10)

	if got := lessonOf(t, progress(t, s, "l1"), "introduction"); !hasBest(got, 90) {
		t.Fatalf("got %+v, want best score 90", got)
	}
}

func TestLowerLaterScoreKeepsPass(t *testing.T) {
	s := course.NewService(&memStore{})
	record(t, s, "l1", "introduction", 60)
	record(t, s, "l1", "introduction", 0)

	p := progress(t, s, "l1")

	l := lessonOf(t, p, "introduction")
	if !l.Passed || !hasBest(l, 60) {
		t.Fatalf("got %+v", l)
	}

	if p.ProgressPercent != 50 {
		t.Fatalf("percent = %d, want 50", p.ProgressPercent)
	}
}

func TestRequiredVersusOptionalAndCompletion(t *testing.T) {
	s := course.NewService(&memStore{})
	record(t, s, "l1", "challenge", 100)

	if p := progress(t, s, "l1"); p.ProgressPercent != 0 || p.Completed {
		t.Fatalf("optional lesson must not count: %+v", p)
	}

	record(t, s, "l1", "introduction", 60)

	if p := progress(t, s, "l1"); p.ProgressPercent != 50 || p.Completed {
		t.Fatalf("one required passed: %+v", p)
	}

	record(t, s, "l1", "practice", 69)

	if p := progress(t, s, "l1"); p.ProgressPercent != 50 || p.Completed {
		t.Fatalf("practice just below threshold: %+v", p)
	}

	record(t, s, "l1", "practice", 70)

	if p := progress(t, s, "l1"); p.ProgressPercent != 100 || !p.Completed {
		t.Fatalf("both required passed: %+v", p)
	}
}

func TestCompletionDoesNotNeedOptionalLesson(t *testing.T) {
	s := course.NewService(&memStore{})
	record(t, s, "l1", "introduction", 100)
	record(t, s, "l1", "practice", 100)

	p := progress(t, s, "l1")
	if !p.Completed || p.ProgressPercent != 100 {
		t.Fatalf("got %+v", p)
	}

	if l := lessonOf(t, p, "challenge"); l.BestScore != nil || l.Passed {
		t.Fatalf("challenge should be untouched: %+v", l)
	}
}

func TestLearnerIsolation(t *testing.T) {
	s := course.NewService(&memStore{})
	record(t, s, "l1", "introduction", 100)
	record(t, s, "l1", "practice", 100)

	p := progress(t, s, "l2")
	if p.ProgressPercent != 0 || p.Completed || lessonOf(t, p, "introduction").BestScore != nil {
		t.Fatalf("l2 saw l1 data: %+v", p)
	}

	if p := progress(t, s, "l1"); !p.Completed {
		t.Fatalf("l1 should be complete: %+v", p)
	}
}

func TestProgressResponseIdentity(t *testing.T) {
	p := progress(t, course.NewService(&memStore{}), "learner-x")
	if p.LearnerID != "learner-x" || p.CourseID != course.CourseID {
		t.Fatalf("got %+v", p)
	}
}

func TestProgressErrors(t *testing.T) {
	boom := errors.New("boom")
	ctx := context.Background()

	if _, err := course.NewService(&memStore{}).Progress(ctx, " ", course.CourseID); !errors.Is(err, course.ErrInvalidInput) {
		t.Errorf("blank learner: %v", err)
	}

	if _, err := course.NewService(&memStore{}).Progress(ctx, "l1", "nope"); !errors.Is(err, course.ErrUnknownCourse) {
		t.Errorf("unknown course: %v", err)
	}

	if _, err := course.NewService(&memStore{bestErr: boom}).Progress(ctx, "l1", course.CourseID); !errors.Is(err, boom) {
		t.Errorf("list error: %v", err)
	}
}

// memCache is an in-memory progressCache. Set the *Err fields to simulate Redis failures.
type memCache struct {
	entries                map[string]course.Progress
	getErr, setErr, delErr error
}

func newMemCache() *memCache { return &memCache{entries: map[string]course.Progress{}} }

func (c *memCache) Get(_ context.Context, learnerID, courseID string) (course.Progress, bool, error) {
	if c.getErr != nil {
		return course.Progress{}, false, c.getErr
	}

	p, ok := c.entries[learnerID+"/"+courseID]

	return p, ok, nil
}

func (c *memCache) Set(_ context.Context, learnerID, courseID string, p course.Progress) error {
	if c.setErr != nil {
		return c.setErr
	}

	c.entries[learnerID+"/"+courseID] = p

	return nil
}

func (c *memCache) Delete(_ context.Context, learnerID, courseID string) error {
	if c.delErr != nil {
		return c.delErr
	}

	delete(c.entries, learnerID+"/"+courseID)

	return nil
}

func TestProgressIsServedFromCache(t *testing.T) {
	store, cache := &memStore{}, newMemCache()
	s := course.NewService(store, course.WithProgressCache(cache))
	record(t, s, "l1", "introduction", 60)

	first := progress(t, s, "l1")
	second := progress(t, s, "l1")

	if store.bestCalls != 1 {
		t.Fatalf("store read %d times, want 1", store.bestCalls)
	}

	if !hasBest(lessonOf(t, second, "introduction"), 60) || second.ProgressPercent != first.ProgressPercent {
		t.Fatalf("cached progress differs: %+v vs %+v", first, second)
	}
}

func TestRecordAttemptInvalidatesCache(t *testing.T) {
	store, cache := &memStore{}, newMemCache()
	s := course.NewService(store, course.WithProgressCache(cache))
	record(t, s, "l1", "introduction", 40)

	if l := lessonOf(t, progress(t, s, "l1"), "introduction"); !hasBest(l, 40) {
		t.Fatalf("got %+v", l)
	}

	record(t, s, "l1", "introduction", 90)

	if l := lessonOf(t, progress(t, s, "l1"), "introduction"); !hasBest(l, 90) || !l.Passed {
		t.Fatalf("stale progress after new attempt: %+v", l)
	}
}

func TestCacheIsPerLearner(t *testing.T) {
	s := course.NewService(&memStore{}, course.WithProgressCache(newMemCache()))
	record(t, s, "l1", "introduction", 100)
	_ = progress(t, s, "l1")

	if l := lessonOf(t, progress(t, s, "l2"), "introduction"); l.BestScore != nil {
		t.Fatalf("l2 saw l1 cache: %+v", l)
	}
}

func TestCacheFailuresFallBackToStore(t *testing.T) {
	boom := errors.New("redis down")
	store := &memStore{}
	cache := &memCache{entries: map[string]course.Progress{}, getErr: boom, setErr: boom, delErr: boom}
	s := course.NewService(store, course.WithProgressCache(cache))

	// A failing delete must not fail the write, and failing get/set must not fail the read.
	record(t, s, "l1", "introduction", 70)

	if l := lessonOf(t, progress(t, s, "l1"), "introduction"); !hasBest(l, 70) {
		t.Fatalf("got %+v", l)
	}

	progress(t, s, "l1")

	if store.bestCalls != 2 {
		t.Fatalf("store read %d times, want 2 (no cache help while Redis is down)", store.bestCalls)
	}
}

func TestCacheNotUpdatedWhenStoreFails(t *testing.T) {
	boom := errors.New("boom")
	cache := newMemCache()
	s := course.NewService(&memStore{bestErr: boom}, course.WithProgressCache(cache))

	if _, err := s.Progress(context.Background(), "l1", course.CourseID); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}

	if len(cache.entries) != 0 {
		t.Fatalf("failed read was cached: %+v", cache.entries)
	}
}

func TestFailedAddKeepsCache(t *testing.T) {
	boom := errors.New("boom")
	store, cache := &memStore{}, newMemCache()
	s := course.NewService(store, course.WithProgressCache(cache))
	record(t, s, "l1", "introduction", 40)
	_ = progress(t, s, "l1")

	store.addErr = boom
	a := course.Attempt{AttemptID: "x", LearnerID: "l1", CourseID: course.CourseID, LessonID: "introduction", Score: 90}

	if err := s.RecordAttempt(context.Background(), a); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}

	if len(cache.entries) != 1 {
		t.Fatal("cache was invalidated although nothing was stored")
	}
}
