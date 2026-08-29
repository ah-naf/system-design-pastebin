package cache

import "github.com/redis/go-redis/v9"

func NewClient(addr string) *redis.Client {
	return redis.NewClient(&redis.Options{Addr: addr})
}
