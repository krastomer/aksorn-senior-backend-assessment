package course

import (
	"context"
	"strings"
)

type attemptStore interface {
	Add(ctx context.Context, attempt Attempt) error
	List(ctx context.Context, learnerID, courseID string) ([]Attempt, error)
}

type Service struct {
	repo attemptStore
}

func NewService(repo attemptStore) *Service {
	return &Service{repo: repo}
}

func (s *Service) RecordAttempt(ctx context.Context, attempt Attempt) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := ValidateAttempt(attempt); err != nil {
		return err
	}

	return s.repo.Add(ctx, attempt)
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

	attempts, err := s.repo.List(ctx, learnerID, courseID)
	if err != nil {
		return Progress{}, err
	}

	best := map[string]int{}

	for _, a := range attempts {
		if cur, ok := best[a.LessonID]; !ok || a.Score > cur {
			best[a.LessonID] = a.Score
		}
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
