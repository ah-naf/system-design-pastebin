package id

import (
	"context"

	"github.com/redis/go-redis/v9"
)

type RedisCounterSource struct {
	client *redis.Client
	key    string
}

func NewRedisCounterSource(client *redis.Client) *RedisCounterSource {
	return newRedisCounterSourceWithKey(client, "pastebin:id:counter")
}

func newRedisCounterSourceWithKey(client *redis.Client, key string) *RedisCounterSource {
	return &RedisCounterSource{
		client: client,
		key:    key,
	}
}

func (r *RedisCounterSource) Next() (uint64, error) {
	result, err := r.client.Incr(context.Background(), r.key).Result()
	if err != nil {
		return 0, err
	}

	return uint64(result), nil
}
