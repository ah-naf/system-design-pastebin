package cache

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func requireRedis(t *testing.T) *redis.Client {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Skipf("local Redis not reachable at localhost:6379 (start it with `cd infra && docker compose up -d`): %v", err)
	}
	return client
}

func cleanupKeys(t *testing.T, client *redis.Client, id string) {
	t.Helper()
	t.Cleanup(func() {
		client.Del(context.Background(), "paste:content:"+id, "paste:missing:"+id)
	})
}

func TestGetOnUnsetKeyIsMiss(t *testing.T) {
	client := requireRedis(t)
	defer client.Close()
	id := "test-miss-" + t.Name()
	cleanupKeys(t, client, id)

	c := NewCache(client)
	_, result := c.Get(context.Background(), id)
	if result != Miss {
		t.Errorf("Get() on unset key = %v, want Miss", result)
	}
}

func TestSetPositiveThenGetRoundTrips(t *testing.T) {
	client := requireRedis(t)
	defer client.Close()
	id := "test-positive-" + t.Name()
	cleanupKeys(t, client, id)

	c := NewCache(client)
	content := []byte("cached content")
	c.SetPositive(context.Background(), id, content, time.Minute)

	got, result := c.Get(context.Background(), id)
	if result != Hit {
		t.Fatalf("Get() after SetPositive = %v, want Hit", result)
	}
	if string(got) != string(content) {
		t.Errorf("content = %q, want %q", got, content)
	}
}

func TestSetNegativeThenGetReturnsNegative(t *testing.T) {
	client := requireRedis(t)
	defer client.Close()
	id := "test-negative-" + t.Name()
	cleanupKeys(t, client, id)

	c := NewCache(client)
	c.SetNegative(context.Background(), id, time.Minute)

	_, result := c.Get(context.Background(), id)
	if result != Negative {
		t.Errorf("Get() after SetNegative = %v, want Negative", result)
	}
}

func TestPositiveTTLExpires(t *testing.T) {
	client := requireRedis(t)
	defer client.Close()
	id := "test-ttl-" + t.Name()
	cleanupKeys(t, client, id)

	c := NewCache(client)
	c.SetPositive(context.Background(), id, []byte("x"), 50*time.Millisecond)
	time.Sleep(150 * time.Millisecond)

	_, result := c.Get(context.Background(), id)
	if result != Miss {
		t.Errorf("Get() after TTL expiry = %v, want Miss", result)
	}
}

func TestCacheDegradesGracefullyWhenRedisUnavailable(t *testing.T) {
	client := requireRedis(t)
	id := "test-degrade-" + t.Name()
	client.Close() // simulate Redis being unreachable for every call below

	c := NewCache(client)

	// None of these must panic or block despite the closed client.
	if _, result := c.Get(context.Background(), id); result != Miss {
		t.Errorf("Get() with closed client = %v, want Miss (fail open)", result)
	}
	c.SetPositive(context.Background(), id, []byte("x"), time.Minute)
	c.SetNegative(context.Background(), id, time.Minute)
}
