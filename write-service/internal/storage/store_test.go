package storage

import (
	"bytes"
	"context"
	"testing"
)

// Matches infra/.env.example's MinIO defaults from Phase 0.
const (
	testEndpoint  = "localhost:9000"
	testAccessKey = "pastebin_minio"
	testSecretKey = "pastebin_minio_password"
)

func newTestStore(t *testing.T, bucket string) *Store {
	t.Helper()
	store, err := NewStore(context.Background(), testEndpoint, testAccessKey, testSecretKey, bucket, false)
	if err != nil {
		t.Skipf("local MinIO not reachable at %s (start it with `cd infra && docker compose up -d`): %v", testEndpoint, err)
	}
	return store
}

func TestNewStoreCreatesBucketIfMissing(t *testing.T) {
	// A throwaway bucket name proves auto-create works on a bucket that
	// does not exist yet.
	newTestStore(t, "pastebin-test-new-bucket")
}

func TestNewStoreSucceedsOnExistingBucket(t *testing.T) {
	// Calling NewStore twice against the same bucket must not error on
	// the second call (bucket-already-exists must be treated as success).
	newTestStore(t, "pastebin-test-existing-bucket")
	newTestStore(t, "pastebin-test-existing-bucket")
}

func TestPutUploadsObject(t *testing.T) {
	store := newTestStore(t, "pastebin-test-put")
	content := []byte("hello from the write-service test suite")

	err := store.Put(context.Background(), "test-key-put", bytes.NewReader(content), int64(len(content)))
	if err != nil {
		t.Fatalf("Put() returned error: %v", err)
	}
}

func TestStorePing(t *testing.T) {
	store := newTestStore(t, "pastebin-test-ping")
	if err := store.Ping(context.Background()); err != nil {
		t.Errorf("Ping() returned error: %v", err)
	}
}
