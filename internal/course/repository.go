package course

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Repository stores attempts and enforces unique attempt IDs.
// Each operation is one SQL statement, except Add, which reads back only when the ID already exists.
type Repository struct{ pool *pgxpool.Pool }

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// Add stores the attempt. Replaying an identical attempt succeeds without storing another row;
// reusing an attempt ID with different content returns ErrConflict.
func (r *Repository) Add(ctx context.Context, attempt Attempt) error {
	tag, err := r.pool.Exec(
		ctx,
		`INSERT INTO attempts (attempt_id, learner_id, course_id, lesson_id, score)
 VALUES ($1, $2, $3, $4, $5)
 ON CONFLICT (attempt_id) DO NOTHING`,
		attempt.AttemptID,
		attempt.LearnerID,
		attempt.CourseID,
		attempt.LessonID,
		attempt.Score,
	)
	if err != nil {
		return fmt.Errorf("insert attempt: %w", err)
	}

	if tag.RowsAffected() == 1 {
		return nil
	}

	var stored Attempt

	err = r.pool.QueryRow(
		ctx,
		`SELECT attempt_id, learner_id, course_id, lesson_id, score FROM attempts WHERE attempt_id = $1`,
		attempt.AttemptID,
	).Scan(&stored.AttemptID, &stored.LearnerID, &stored.CourseID, &stored.LessonID, &stored.Score)
	if err != nil {
		return fmt.Errorf("load existing attempt: %w", err)
	}

	if stored != attempt {
		return ErrConflict
	}

	return nil
}

// BestScores returns the highest score per lesson for the learner and course.
// Lessons without attempts are absent from the map.
func (r *Repository) BestScores(ctx context.Context, learnerID, courseID string) (map[string]int, error) {
	rows, err := r.pool.Query(ctx, `SELECT lesson_id, MAX(score)
 FROM attempts WHERE learner_id = $1 AND course_id = $2 GROUP BY lesson_id`, learnerID, courseID)
	if err != nil {
		return nil, fmt.Errorf("best scores: %w", err)
	}
	defer rows.Close()

	best := map[string]int{}

	for rows.Next() {
		var (
			lessonID string
			score    int
		)
		if err := rows.Scan(&lessonID, &score); err != nil {
			return nil, fmt.Errorf("scan best score: %w", err)
		}

		best[lessonID] = score
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read best scores: %w", err)
	}

	return best, nil
}

func (r *Repository) List(ctx context.Context, learnerID, courseID string) ([]Attempt, error) {
	rows, err := r.pool.Query(ctx, `SELECT attempt_id, learner_id, course_id, lesson_id, score
 FROM attempts WHERE learner_id = $1 AND course_id = $2 ORDER BY id`, learnerID, courseID)
	if err != nil {
		return nil, fmt.Errorf("list attempts: %w", err)
	}
	defer rows.Close()

	attempts := []Attempt{}

	for rows.Next() {
		var attempt Attempt
		if err := rows.Scan(
			&attempt.AttemptID,
			&attempt.LearnerID,
			&attempt.CourseID,
			&attempt.LessonID,
			&attempt.Score,
		); err != nil {
			return nil, fmt.Errorf("scan attempt: %w", err)
		}

		attempts = append(attempts, attempt)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read attempts: %w", err)
	}

	return attempts, nil
}
