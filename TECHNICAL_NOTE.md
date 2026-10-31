# Technical Note

## 1. Design decision

Describe one decision in your implementation, why you chose it, and its tradeoff.

I chose to calculate progress by listing all of a learner's attempts on every read. After a new attempt is added, the next read returns fresh data, and simultaneous inserts are supported, so the learner always gets up-to-date course progress. The tradeoff is that the design is not idempotent: a repeated `attempt_id` is stored as a duplicate row, so data grows faster (the best score is unaffected when the duplicate has the same score). Also, as a learner's data grows, each read gets slower and could even run out of memory when loading one person's data.

## 2. Design questions

Brief answers are sufficient. No additional implementation is required.

### Retried requests

How would you enforce globally unique attempt IDs, return the original result for an identical replay, and return 409 for different content under the same ID? Consider simultaneous requests with the same ID.

Add a unique constraint on `attempt_id` and insert with `INSERT ... ON CONFLICT (attempt_id) DO NOTHING`. If no row was inserted, read the existing row: identical content returns the original result, different content returns 409 and changes nothing. Two simultaneous requests with the same ID are serialized by the unique index, so the second one waits, hits the conflict, and takes the same replay/409 path. No application lock is needed; PostgreSQL is the source of truth.

### Concurrent requests

Two attempts with distinct IDs and scores of 90 and 70 arrive simultaneously for the same learner and lesson.

Can your implementation lose the higher score? What could a simultaneous progress read observe? What should a new read return after both writes have committed? Is any protection needed?

No, the higher score cannot be lost. Each attempt is a separate INSERT and the best score is computed at read time, so there is no read-modify-write to overwrite. A simultaneous read sees only committed rows: none, 70 only, 90 only, or both. After both commit, a new read must return 90. No extra protection is needed in this design (a stored best-score column would need an atomic `GREATEST` upsert).

### Redis

Suppose attempt history grows and progress reads become slow.

Would you use Redis? What would you store, how would it stay consistent with PostgreSQL, and what would happen if Redis were unavailable or its data were lost? An alternative approach is welcome.

Not first. I would try, in order: (1) let PostgreSQL compute `MAX(score) ... GROUP BY lesson_id` using an index, so only up to three rows are returned; (2) a best-score summary table updated in the same transaction as the insert. If reads are still slow, Redis would cache only the computed progress per learner. After a successful commit, delete that key (and set a TTL) so the next read rebuilds it from PostgreSQL. If Redis is unavailable or loses its data, reads fall back to PostgreSQL and are slower but correct, because Redis holds nothing that cannot be rebuilt.

## 3. Limitations and production readiness

List any limitations or unfinished work, and what you would verify before production use.

**Limitations**
- Retries are not handled: a repeated `attempt_id` is stored as another row (no unique constraint).
- `Progress` loads every attempt of a learner on each call, so cost grows with history.
- Integration tests need PostgreSQL and Redis running.

**Verify before production**
- Query plan with `EXPLAIN` on realistic data volumes, and a load test of `Progress`.
- Same-ID retries running simultaneously against a real database, after adding the unique constraint.
- Logging, metrics and timeouts around database calls.
- Authentication and authorization, which the assignment treats as out of scope.

## 4. Time and AI tools

Time spent: 1 hour 34 minutes

AI tools used: Claude Code. It explained the codebase, drafted the implementation and tests, reviewed them, and helped me reason about the design questions. I chose the design (small store interface, progress computed on read, spec-only scope on `main`), decided which review findings to fix, and edited the note's answers.
