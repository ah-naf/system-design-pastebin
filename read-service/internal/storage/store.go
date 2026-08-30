package storage

import (
	"context"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type Store struct {
	client *s3.Client
	bucket string
}

func NewStore(ctx context.Context, endpoint, accessKey, secretKey, bucket string, useSSL bool) (*Store, error) {
	schema := "http"
	if useSSL {
		schema = "https"
	}

	baseEndpoint := schema + "://" + endpoint

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

	return &Store{
		client: client,
		bucket: bucket,
	}, nil
}

func (s *Store) Get(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	obj, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &s.bucket,
		Key:    &key,
	})

	if err != nil {
		return nil, 0, err
	}

	return obj.Body, *obj.ContentLength, nil
}

func (s *Store) Ping(ctx context.Context) error {
	_, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: &s.bucket,
	})

	return err
}
