// Package storage bọc API S3 (MinIO local, R2/S3 khi lên cloud).
package storage

import (
	"bytes"
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
	// PublicEndpoint là địa chỉ client bên ngoài dùng với presigned URL (chữ ký gắn với Host).
	// Rỗng thì dùng Endpoint.
	PublicEndpoint string
}

type Store struct {
	client  *s3.Client
	presign *s3.PresignClient
}

func newClient(endpoint string, cfg Config) *s3.Client {
	return s3.New(s3.Options{
		Region:                     cfg.Region,
		BaseEndpoint:               aws.String(endpoint),
		UsePathStyle:               true,
		Credentials:                credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
}

func New(cfg Config) *Store {
	public := cfg.PublicEndpoint
	if public == "" {
		public = cfg.Endpoint
	}
	// Presign chạy offline (không gọi mạng), nên client presign trỏ vào địa chỉ public là đủ.
	return &Store{client: newClient(cfg.Endpoint, cfg), presign: s3.NewPresignClient(newClient(public, cfg))}
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

// PutObject ghi một object nhỏ (ví dụ phụ đề .vtt) từ bộ nhớ, ghi đè nếu đã có.
func (s *Store) PutObject(ctx context.Context, bucket, key, contentType string, body []byte) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(body),
		ContentType: aws.String(contentType),
	})
	if err != nil {
		return fmt.Errorf("put %s: %w", key, err)
	}
	return nil
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
