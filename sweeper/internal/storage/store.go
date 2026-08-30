package storage

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
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

	return &Store{
		client: client,
		bucket: bucket,
	}, nil
}

func (s *Store) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: &s.bucket,
		Key:    &key,
	})

	return err
}
