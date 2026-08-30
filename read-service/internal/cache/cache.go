package cache

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"time"

	"github.com/redis/go-redis/v9"
)

type Result int

const (
	Miss Result = iota
	Hit
	Negative
)

type Cache struct {
	client *redis.Client
}

func NewCache(client *redis.Client) *Cache {
	return &Cache{
		client: client,
	}
}

func (c *Cache) Get(ctx context.Context, id string) ([]byte, Result) {
	contentKey := "paste:content:" + id

	content, err := c.client.Get(ctx, contentKey).Bytes()
	if err == nil {
		return content, Hit
	}

	missingKey := "paste:missing:" + id
	_, err = c.client.Get(ctx, missingKey).Result()
	if err == nil {
		return nil, Negative
	}

	return nil, Miss
}

func (c *Cache) SetPositive(ctx context.Context, id string, content []byte, ttl time.Duration) {
	key := "paste:content:" + id

	if err := c.client.Set(ctx, key, content, ttl).Err(); err != nil {
		log.Printf("cache SetPositive failed for %q: %v", id, err)
	}
}

func (c *Cache) SetNegative(ctx context.Context, id string, ttl time.Duration) {
	key := "paste:missing:" + id

	if err := c.client.Set(ctx, key, "1", ttl).Err(); err != nil {
		log.Printf("cache SetNegative failed for %q: %v", id, err)
	}
}

func (c *Cache) AcquireLock(ctx context.Context, id string) (bool, string, error) {
	tokenBytes := make([]byte, 16)

	if _, err := rand.Read(tokenBytes); err != nil {
		return false, "", fmt.Errorf("generate lock token: %w", err)
	}

	token := hex.EncodeToString(tokenBytes)

	ok, err := c.client.SetNX(ctx, "paste:lock:"+id, token, 5*time.Second).Result()
	if err != nil {
		return false, "", err
	}

	if !ok {
		return false, "", nil
	}
	return ok, token, nil
}

func (c *Cache) ReleaseLock(ctx context.Context, id, token string) error {
	const script = `
		if redis.call("GET", KEYS[1]) == ARGV[1] then
			return redis.call("DEL", KEYS[1])
		end
		return 0
	`

	key := "paste:lock:" + id

	_, err := c.client.Eval(
		ctx,
		script,
		[]string{key},
		token,
	).Result()

	return err
}
