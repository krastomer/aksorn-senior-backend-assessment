package course

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// progressCache holds computed progress per learner and course. It is only an optimization:
// every entry can be rebuilt from PostgreSQL, so losing the cache never loses data.
type progressCache interface {
	// Get reports whether an entry exists; a miss is not an error.
	Get(ctx context.Context, learnerID, courseID string) (Progress, bool, error)
	Set(ctx context.Context, learnerID, courseID string, progress Progress) error
	Delete(ctx context.Context, learnerID, courseID string) error
}

// RedisCache stores progress as JSON with a TTL so an entry that is never invalidated still expires.
type RedisCache struct {
	client *redis.Client
	ttl    time.Duration
}

func NewRedisCache(client *redis.Client, ttl time.Duration) *RedisCache {
	return &RedisCache{client: client, ttl: ttl}
}

// Course ID comes first because it is validated and never contains a colon, so keys cannot collide.
func progressKey(learnerID, courseID string) string {
	return fmt.Sprintf("progress:%s:%s", courseID, learnerID)
}

func (c *RedisCache) Get(ctx context.Context, learnerID, courseID string) (Progress, bool, error) {
	raw, err := c.client.Get(ctx, progressKey(learnerID, courseID)).Bytes()
	if errors.Is(err, redis.Nil) {
		return Progress{}, false, nil
	}

	if err != nil {
		return Progress{}, false, fmt.Errorf("get cached progress: %w", err)
	}

	var progress Progress
	if err := json.Unmarshal(raw, &progress); err != nil {
		return Progress{}, false, fmt.Errorf("decode cached progress: %w", err)
	}

	return progress, true, nil
}

func (c *RedisCache) Set(ctx context.Context, learnerID, courseID string, progress Progress) error {
	raw, err := json.Marshal(progress)
	if err != nil {
		return fmt.Errorf("encode progress: %w", err)
	}

	if err := c.client.Set(ctx, progressKey(learnerID, courseID), raw, c.ttl).Err(); err != nil {
		return fmt.Errorf("set cached progress: %w", err)
	}

	return nil
}

func (c *RedisCache) Delete(ctx context.Context, learnerID, courseID string) error {
	if err := c.client.Del(ctx, progressKey(learnerID, courseID)).Err(); err != nil {
		return fmt.Errorf("delete cached progress: %w", err)
	}

	return nil
}
