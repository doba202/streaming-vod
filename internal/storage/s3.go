// Package storage bọc API S3 (MinIO local, R2/S3 khi lên cloud).
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type Config struct {
	Endpoint, Region, AccessKey, SecretKey string
}

type Store struct {
	client *s3.Client
}

func New(cfg Config) *Store {
	client := s3.New(s3.Options{
		Region:                     cfg.Region,
		BaseEndpoint:               aws.String(cfg.Endpoint),
		UsePathStyle:               true,
		Credentials:                credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
	return &Store{client: client}
}

func (s *Store) EnsureBucket(ctx context.Context, bucket string) error {
	_, err := s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	var owned *types.BucketAlreadyOwnedByYou
	var exists *types.BucketAlreadyExists
	if err != nil && !errors.As(err, &owned) && !errors.As(err, &exists) {
		return fmt.Errorf("create bucket %s: %w", bucket, err)
	}
	return nil
}

// UploadDir upload mọi file trong dir lên bucket dưới prefix, giữ cấu trúc thư mục.
func (s *Store) UploadDir(ctx context.Context, bucket, prefix, dir string) (int, error) {
	count := 0
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		key := path.Join(prefix, filepath.ToSlash(rel))
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
			Bucket:      aws.String(bucket),
			Key:         aws.String(key),
			Body:        f,
			ContentType: aws.String(ContentTypeFor(p)),
		})
		if err != nil {
			return fmt.Errorf("put %s: %w", key, err)
		}
		count++
		return nil
	})
	return count, err
}

func (s *Store) Download(ctx context.Context, bucket, key, dest string) error {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		return fmt.Errorf("get %s: %w", key, err)
	}
	defer out.Body.Close()
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, out.Body)
	return err
}

func ContentTypeFor(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".m3u8":
		return "application/vnd.apple.mpegurl"
	case ".ts":
		return "video/mp2t"
	case ".vtt":
		return "text/vtt"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".mp4":
		return "video/mp4"
	default:
		return "application/octet-stream"
	}
}
