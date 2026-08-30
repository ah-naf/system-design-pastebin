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

func TestAcquireLockMutualExclusion(t *testing.T) {
	client := requireRedis(t)
	defer client.Close()
	id := "test-lock-" + t.Name()
	t.Cleanup(func() {
		client.Del(context.Background(), "paste:lock:"+id)
	})

	c := NewCache(client)

	first, token, err := c.AcquireLock(context.Background(), id)
	if err != nil {
		t.Fatalf("AcquireLock() first call error: %v", err)
	}
	if !first {
		t.Fatal("AcquireLock() first call = false, want true")
	}
	if token == "" {
		t.Error("AcquireLock() first call returned empty token, want non-empty")
	}

	second, secondToken, err := c.AcquireLock(context.Background(), id)
	if err != nil {
		t.Fatalf("AcquireLock() second call error: %v", err)
	}
	if second {
		t.Error("AcquireLock() second call = true, want false (lock already held)")
	}
	if secondToken != "" {
		t.Error("AcquireLock() second call returned non-empty token despite not acquiring the lock")
	}
}

func TestReleaseLockClearsKey(t *testing.T) {
	client := requireRedis(t)
	defer client.Close()
	id := "test-lock-release-" + t.Name()
	t.Cleanup(func() {
		client.Del(context.Background(), "paste:lock:"+id)
	})

	c := NewCache(client)

	acquired, token, err := c.AcquireLock(context.Background(), id)
	if err != nil || !acquired {
		t.Fatalf("AcquireLock() = (%v, %q, %v), want (true, non-empty, nil)", acquired, token, err)
	}

	if err := c.ReleaseLock(context.Background(), id, token); err != nil {
		t.Fatalf("ReleaseLock() error: %v", err)
	}

	reacquired, _, err := c.AcquireLock(context.Background(), id)
	if err != nil {
		t.Fatalf("AcquireLock() after release error: %v", err)
	}
	if !reacquired {
		t.Error("AcquireLock() after ReleaseLock = false, want true (lock was cleared)")
	}
}

func TestReleaseLockDoesNotDeleteMismatchedToken(t *testing.T) {
	client := requireRedis(t)
	defer client.Close()
	id := "test-lock-mismatch-" + t.Name()
	key := "paste:lock:" + id
	t.Cleanup(func() {
		client.Del(context.Background(), key)
	})

	c := NewCache(client)

	_, staleToken, err := c.AcquireLock(context.Background(), id)
	if err != nil {
		t.Fatalf("AcquireLock() error: %v", err)
	}

	// Simulate the lock expiring and a different requester acquiring it,
	// as if this holder's fetch took longer than the lock TTL.
	if err := client.Set(context.Background(), key, "someone-elses-token", 5*time.Second).Err(); err != nil {
		t.Fatalf("failed to simulate a new lock holder: %v", err)
	}

	if err := c.ReleaseLock(context.Background(), id, staleToken); err != nil {
		t.Fatalf("ReleaseLock() error: %v", err)
	}

	val, err := client.Get(context.Background(), key).Result()
	if err != nil {
		t.Fatalf("expected the new holder's lock to still exist, Get() error: %v", err)
	}
	if val != "someone-elses-token" {
		t.Errorf("lock value = %q, want %q (a stale ReleaseLock must not delete a different holder's lock)", val, "someone-elses-token")
	}
}
