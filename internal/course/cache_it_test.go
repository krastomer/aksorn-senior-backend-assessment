package course_test

import (
	"context"
	"testing"
	"time"

	"github.com/aksornproject/aksorn-senior-backend-assessment/internal/course"
	"github.com/aksornproject/aksorn-senior-backend-assessment/internal/infra"
	"github.com/aksornproject/aksorn-senior-backend-assessment/internal/testutil"
)

func TestRedisCacheRoundTrip(t *testing.T) {
	ctr := testutil.NewContainer(t)
	cache := ctr.Cache()
	learner := ctr.Learner("cache")
	ctx := context.Background()

	if _, ok, err := cache.Get(ctx, learner, course.CourseID); ok || err != nil {
		t.Fatalf("empty cache = %v, %v", ok, err)
	}

	zero := 0
	want := course.Progress{
		LearnerID: learner,
		CourseID:  course.CourseID,
		Lessons: []course.LessonProgress{
			{LessonID: "introduction", BestScore: &zero},
			{LessonID: "practice"},
		},
	}

	if err := cache.Set(ctx, learner, course.CourseID, want); err != nil {
		t.Fatal(err)
	}

	got, ok, err := cache.Get(ctx, learner, course.CourseID)
	if err != nil || !ok {
		t.Fatalf("get = %v, %v", ok, err)
	}

	// A real zero and a missing score must both survive the JSON round trip.
	if !hasBest(got.Lessons[0], 0) || got.Lessons[1].BestScore != nil {
		t.Fatalf("round trip changed scores: %+v", got.Lessons)
	}

	if err := cache.Delete(ctx, learner, course.CourseID); err != nil {
		t.Fatal(err)
	}

	if _, ok, _ := cache.Get(ctx, learner, course.CourseID); ok {
		t.Fatal("entry still present after delete")
	}
}

func TestRedisCacheEntriesExpire(t *testing.T) {
	ctr := testutil.NewContainer(t)
	conns, err := infra.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(conns.Close)

	short := course.NewRedisCache(conns.Redis, 100*time.Millisecond)
	learner := ctr.Learner("ttl")
	ctx := context.Background()

	if err := short.Set(ctx, learner, course.CourseID, course.Progress{LearnerID: learner}); err != nil {
		t.Fatal(err)
	}

	time.Sleep(300 * time.Millisecond)

	if _, ok, _ := short.Get(ctx, learner, course.CourseID); ok {
		t.Fatal("entry did not expire")
	}
}

func TestHTTPProgressRefreshesAfterNewAttempt(t *testing.T) {
	ctr := testutil.NewContainer(t)
	learner := ctr.Learner("fresh")

	ctr.MustPost(learner, "introduction", 40)

	if l := lessonOf(t, ctr.Progress(learner), "introduction"); !hasBest(l, 40) || l.Passed {
		t.Fatalf("first read: %+v", l)
	}

	// The first read cached 40; a new attempt must invalidate it.
	ctr.MustPost(learner, "introduction", 90)

	if l := lessonOf(t, ctr.Progress(learner), "introduction"); !hasBest(l, 90) || !l.Passed {
		t.Fatalf("stale read after new attempt: %+v", l)
	}
}

func TestHTTPProgressRecoversWhenCacheIsLost(t *testing.T) {
	ctr := testutil.NewContainer(t)
	learner := ctr.Learner("lost")

	ctr.MustPost(learner, "practice", 80)
	_ = ctr.Progress(learner)

	if err := ctr.Cache().Delete(context.Background(), learner, course.CourseID); err != nil {
		t.Fatal(err)
	}

	if l := lessonOf(t, ctr.Progress(learner), "practice"); !hasBest(l, 80) || !l.Passed {
		t.Fatalf("rebuild from PostgreSQL failed: %+v", l)
	}
}

func TestProgressWorksWhenRedisIsDown(t *testing.T) {
	conns, err := infra.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(conns.Close)

	repo := course.NewRepository(conns.Postgres)
	// A closed client fails every call, like an unreachable Redis.
	_ = conns.Redis.Close()

	service := course.NewService(repo, course.WithProgressCache(course.NewRedisCache(conns.Redis, time.Minute)))
	learner := "it-redisdown-" + time.Now().Format("150405.000000000")
	ctx := context.Background()

	t.Cleanup(func() {
		_, _ = conns.Postgres.Exec(ctx, "DELETE FROM attempts WHERE learner_id = $1", learner)
	})

	a := course.Attempt{AttemptID: learner + "-a", LearnerID: learner, CourseID: course.CourseID, LessonID: "introduction", Score: 75}
	if err := service.RecordAttempt(ctx, a); err != nil {
		t.Fatalf("RecordAttempt with Redis down: %v", err)
	}

	p, err := service.Progress(ctx, learner, course.CourseID)
	if err != nil {
		t.Fatalf("Progress with Redis down: %v", err)
	}

	if l := lessonOf(t, p, "introduction"); !hasBest(l, 75) || !l.Passed {
		t.Fatalf("got %+v", l)
	}
}

func TestCorruptCacheEntryFallsBackToPostgres(t *testing.T) {
	ctr := testutil.NewContainer(t)
	learner := ctr.Learner("corrupt")
	ctx := context.Background()

	conns, err := infra.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(conns.Close)

	ctr.MustPost(learner, "introduction", 65)

	if err := conns.Redis.Set(ctx, "progress:"+course.CourseID+":"+learner, "not json", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}

	if _, ok, err := ctr.Cache().Get(ctx, learner, course.CourseID); ok || err == nil {
		t.Fatalf("corrupt entry = %v, %v; want a decode error", ok, err)
	}

	// The read still succeeds from PostgreSQL and replaces the bad entry.
	if l := lessonOf(t, ctr.Progress(learner), "introduction"); !hasBest(l, 65) {
		t.Fatalf("got %+v", l)
	}

	if _, ok, err := ctr.Cache().Get(ctx, learner, course.CourseID); !ok || err != nil {
		t.Fatalf("entry not rebuilt: %v, %v", ok, err)
	}
}
