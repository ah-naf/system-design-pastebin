package storage

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Matches infra/.env.example's MinIO defaults from Phase 0.
const (
	testEndpoint  = "localhost:9000"
	testAccessKey = "pastebin_minio"
	testSecretKey = "pastebin_minio_password"
	testBucket    = "pastebin"
)

// uploadFixture puts an object directly via a raw S3 client — read-service's
// own Store never writes, so tests can't use it to set up fixtures.
func uploadFixture(t *testing.T, key string, content []byte) {
	t.Helper()
	client := s3.New(s3.Options{
		Region:       "ap-southeast-1",
		BaseEndpoint: aws.String("http://" + testEndpoint),
		UsePathStyle: true,
		Credentials:  credentials.NewStaticCredentialsProvider(testAccessKey, testSecretKey, ""),
	})
	ctx := context.Background()
	if _, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(testBucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(content),
	}); err != nil {
		t.Skipf("local MinIO not reachable/writable at %s (start it with `cd infra && docker compose up -d`): %v", testEndpoint, err)
	}
	t.Cleanup(func() {
		client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(testBucket), Key: aws.String(key)})
	})
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(context.Background(), testEndpoint, testAccessKey, testSecretKey, testBucket, false)
	if err != nil {
		t.Skipf("local MinIO not reachable at %s: %v", testEndpoint, err)
	}
	return store
}

func TestGetFetchesUploadedObject(t *testing.T) {
	key := "test-get-" + t.Name()
	content := []byte("hello from the read-service test suite")
	uploadFixture(t, key, content)

	store := newTestStore(t)
	body, size, err := store.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("Get() returned error: %v", err)
	}
	defer body.Close()

	if size != int64(len(content)) {
		t.Errorf("size = %d, want %d", size, len(content))
	}
	got, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("reading body returned error: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("content = %q, want %q", got, content)
	}
}

func TestGetMissingKeyReturnsError(t *testing.T) {
	store := newTestStore(t)
	_, _, err := store.Get(context.Background(), "does-not-exist-"+t.Name())
	if err == nil {
		t.Error("Get() on missing key: expected error, got nil")
	}
}

func TestStorePing(t *testing.T) {
	store := newTestStore(t)
	if err := store.Ping(context.Background()); err != nil {
		t.Errorf("Ping() returned error: %v", err)
	}
}
