-- Attempt IDs are globally unique: Add relies on this index for ON CONFLICT.
-- Fails if the table already holds duplicate attempt_id values.
CREATE UNIQUE INDEX attempts_attempt_id_key ON attempts (attempt_id);

-- Covering index so best-score aggregation is an index-only scan.
CREATE INDEX attempts_best_score_idx ON attempts (learner_id, course_id, lesson_id, score);
