package course

import (
	"context"
	"log/slog"
	"strings"
	"time"
)

// cacheTimeout bounds each cache call so a slow Redis cannot hold up requests that PostgreSQL can serve.
const cacheTimeout = 200 * time.Millisecond

type attemptStore interface {
	Add(ctx context.Context, attempt Attempt) error
	BestScores(ctx context.Context, learnerID, courseID string) (map[string]int, error)
}

type Service struct {
	repo  attemptStore
	cache progressCache
}

// Option configures optional Service behavior.
type Option func(*Service)

// WithProgressCache serves repeated Progress reads from cache. PostgreSQL stays the source of truth.
func WithProgressCache(cache progressCache) Option {
	return func(s *Service) { s.cache = cache }
}

func NewService(repo attemptStore, opts ...Option) *Service {
	s := &Service{repo: repo}
	for _, opt := range opts {
		opt(s)
	}

	return s
}

func (s *Service) RecordAttempt(ctx context.Context, attempt Attempt) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := ValidateAttempt(attempt); err != nil {
		return err
	}

	if err := s.repo.Add(ctx, attempt); err != nil {
		return err
	}

	// Invalidate only after the attempt is stored. A failed delete is not an error: the entry expires by TTL.
	if s.cache != nil {
		cacheCtx, cancel := context.WithTimeout(ctx, cacheTimeout)
		defer cancel()

		if err := s.cache.Delete(cacheCtx, attempt.LearnerID, attempt.CourseID); err != nil {
			slog.Warn("progress cache invalidation failed", "error", err)
		}
	}

	return nil
}

func (s *Service) Progress(ctx context.Context, learnerID, courseID string) (Progress, error) {
	if err := ctx.Err(); err != nil {
		return Progress{}, err
	}

	if strings.TrimSpace(learnerID) == "" {
		return Progress{}, ErrInvalidInput
	}

	if courseID != CourseID {
		return Progress{}, ErrUnknownCourse
	}

	if s.cache != nil {
		if cached, ok := s.cachedProgress(ctx, learnerID, courseID); ok {
			return cached, nil
		}
	}

	progress, err := s.computeProgress(ctx, learnerID, courseID)
	if err != nil {
		return Progress{}, err
	}

	if s.cache != nil {
		cacheCtx, cancel := context.WithTimeout(ctx, cacheTimeout)
		defer cancel()

		if err := s.cache.Set(cacheCtx, learnerID, courseID, progress); err != nil {
			slog.Warn("progress cache write failed", "error", err)
		}
	}

	return progress, nil
}

// cachedProgress treats a cache error like a miss so reads fall back to PostgreSQL.
func (s *Service) cachedProgress(ctx context.Context, learnerID, courseID string) (Progress, bool) {
	cacheCtx, cancel := context.WithTimeout(ctx, cacheTimeout)
	defer cancel()

	cached, ok, err := s.cache.Get(cacheCtx, learnerID, courseID)
	if err != nil {
		slog.Warn("progress cache read failed", "error", err)
		return Progress{}, false
	}

	return cached, ok
}

func (s *Service) computeProgress(ctx context.Context, learnerID, courseID string) (Progress, error) {
	best, err := s.repo.BestScores(ctx, learnerID, courseID)
	if err != nil {
		return Progress{}, err
	}

	course := FixedCourse()
	lessons := make([]LessonProgress, 0, len(course.Lessons))
	required, passedRequired := 0, 0

	for _, l := range course.Lessons {
		lp := LessonProgress{LessonID: l.ID}

		if score, ok := best[l.ID]; ok {
			lp.BestScore = &score
			lp.Passed = score >= l.PassingScore
		}

		if l.Required {
			required++

			if lp.Passed {
				passedRequired++
			}
		}

		lessons = append(lessons, lp)
	}

	// A course without required lessons cannot be completed or have progress.
	percent := 0
	if required > 0 {
		percent = passedRequired * 100 / required
	}

	return Progress{
		LearnerID:       learnerID,
		CourseID:        courseID,
		Lessons:         lessons,
		ProgressPercent: percent,
		Completed:       required > 0 && passedRequired == required,
	}, nil
}
