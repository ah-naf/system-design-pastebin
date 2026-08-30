package storage

import (
	"bytes"
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const (
	testEndpoint  = "localhost:9000"
	testAccessKey = "pastebin_minio"
	testSecretKey = "pastebin_minio_password"
	testBucket    = "pastebin"
)

func rawClient(t *testing.T) *s3.Client {
	t.Helper()
	return s3.New(s3.Options{
		Region:       "ap-southeast-1",
		BaseEndpoint: aws.String("http://" + testEndpoint),
		UsePathStyle: true,
		Credentials:  credentials.NewStaticCredentialsProvider(testAccessKey, testSecretKey, ""),
	})
}

func uploadFixture(t *testing.T, key string, content []byte) {
	t.Helper()
	client := rawClient(t)
	ctx := context.Background()
	if _, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(testBucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(content),
	}); err != nil {
		t.Skipf("local MinIO not reachable/writable at %s (start it with `cd infra && docker compose up -d`): %v", testEndpoint, err)
	}
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(context.Background(), testEndpoint, testAccessKey, testSecretKey, testBucket, false)
	if err != nil {
		t.Skipf("local MinIO not reachable at %s: %v", testEndpoint, err)
	}
	return store
}

func TestDeleteRemovesObject(t *testing.T) {
	key := "test-delete-" + t.Name()
	uploadFixture(t, key, []byte("goodbye"))

	store := newTestStore(t)
	if err := store.Delete(context.Background(), key); err != nil {
		t.Fatalf("Delete() returned error: %v", err)
	}

	client := rawClient(t)
	_, err := client.GetObject(context.Background(), &s3.GetObjectInput{
		Bucket: aws.String(testBucket),
		Key:    aws.String(key),
	})
	if err == nil {
		t.Error("object still exists after Delete()")
	}
}

func TestDeleteOnMissingKeyDoesNotError(t *testing.T) {
	store := newTestStore(t)
	if err := store.Delete(context.Background(), "does-not-exist-"+t.Name()); err != nil {
		t.Errorf("Delete() on missing key returned error: %v, want nil (S3 DeleteObject is idempotent)", err)
	}
}
