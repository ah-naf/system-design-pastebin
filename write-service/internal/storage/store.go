package storage

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type Store struct {
	client *s3.Client
	bucket string
}

func NewStore(ctx context.Context, endpoint, accessKey, secretKey, bucket string, useSSL bool) (*Store, error) {
	scheme := "http"
	if useSSL {
		scheme = "https"
	}

	baseEndpoint := scheme + "://" + endpoint

	cfg := aws.Config{
		Credentials: credentials.NewStaticCredentialsProvider(
			accessKey, secretKey, "",
		),
		Region: "ap-southeast-1",
	}

	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(baseEndpoint)
		o.UsePathStyle = true
	})

	store := &Store{
		client: client,
		bucket: bucket,
	}

	_, err := store.client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(bucket),
	})

	if err != nil {
		var alreadyExists *s3types.BucketAlreadyExists
		var alreadyOwned *s3types.BucketAlreadyOwnedByYou
		if !errors.As(err, &alreadyExists) && !errors.As(err, &alreadyOwned) {
			return nil, fmt.Errorf("create bucket: %w", err)
		}
	}

	return store, nil
}

func (s *Store) Put(ctx context.Context, key string, content io.Reader, size int64) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &s.bucket,
		Key:    aws.String(key),
		Body:   content,
	})
	if err != nil {
		return fmt.Errorf("put object: %w", err)
	}
	return nil
}

func (s *Store) Ping(ctx context.Context) error {
	_, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: &s.bucket,
	})
	if err != nil {
		return fmt.Errorf("ping bucket: %w", err)
	}
	return nil
}
