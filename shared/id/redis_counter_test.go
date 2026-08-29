package id

import (
	"testing"

	"github.com/redis/go-redis/v9"
)

// requireRedis skips the test if the local Phase 0 Redis isn't reachable,
// so `go test ./...` doesn't hard-fail on a machine without Docker running.
func requireRedis(t *testing.T) *redis.Client {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	if err := client.Ping(t.Context()).Err(); err != nil {
		t.Skipf("local Redis not reachable at localhost:6379 (start it with `cd infra && docker compose up -d`): %v", err)
	}
	return client
}

func TestRedisCounterSourceIncrements(t *testing.T) {
	client := requireRedis(t)
	defer client.Close()

	testKey := "pastebin:id:counter:test:" + t.Name()
	defer client.Del(t.Context(), testKey)

	counter := newRedisCounterSourceWithKey(client, testKey)

	first, err := counter.Next()
	if err != nil {
		t.Fatalf("Next() returned error: %v", err)
	}
	second, err := counter.Next()
	if err != nil {
		t.Fatalf("Next() returned error: %v", err)
	}
	if second != first+1 {
		t.Errorf("Next() sequence = %d, %d — want strictly incrementing by 1", first, second)
	}
}

func TestRedisCounterSourceUsesProductionKey(t *testing.T) {
	client := requireRedis(t)
	defer client.Close()

	// pastebin:id:counter is the real production key — other processes
	// (write-service, other test runs) may have already incremented it,
	// so assert a relative increment rather than an absolute starting
	// value.
	before, err := client.Get(t.Context(), "pastebin:id:counter").Int64()
	if err != nil && err != redis.Nil {
		t.Fatalf("could not read starting value of \"pastebin:id:counter\": %v", err)
	}

	counter := NewRedisCounterSource(client)
	if _, err := counter.Next(); err != nil {
		t.Fatalf("Next() returned error: %v", err)
	}

	after, err := client.Get(t.Context(), "pastebin:id:counter").Int64()
	if err != nil {
		t.Fatalf("could not read key \"pastebin:id:counter\" that NewRedisCounterSource should have incremented: %v", err)
	}
	if after != before+1 {
		t.Errorf("pastebin:id:counter went from %d to %d, want exactly +1", before, after)
	}
}
