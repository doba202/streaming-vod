package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

var ErrIncompleteUpload = errors.New("multipart upload incomplete")

func (s *Store) CreateMultipartUpload(ctx context.Context, bucket, key, contentType string) (string, error) {
	out, err := s.client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket: aws.String(bucket), Key: aws.String(key), ContentType: aws.String(contentType),
	})
	if err != nil {
		return "", fmt.Errorf("create multipart %s: %w", key, err)
	}
	return aws.ToString(out.UploadId), nil
}

// PresignUploadPart trả URL để client PUT thẳng một part lên storage, hết hạn sau ttl.
func (s *Store) PresignUploadPart(ctx context.Context, bucket, key, uploadID string, partNumber int32, ttl time.Duration) (string, error) {
	req, err := s.presign.PresignUploadPart(ctx, &s3.UploadPartInput{
		Bucket: aws.String(bucket), Key: aws.String(key),
		UploadId: aws.String(uploadID), PartNumber: aws.Int32(partNumber),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("presign part %d of %s: %w", partNumber, key, err)
	}
	return req.URL, nil
}

// CompleteMultipartUpload tự liệt kê các part đã upload (lấy ETag từ server) rồi ghép object.
// Trả ErrIncompleteUpload nếu không đủ đúng các part 1..wantParts, hoặc kích thước thực tế sai:
// tổng các part phải == wantSize (và <= maxSize), mọi part trừ part cuối phải đúng partSize.
func (s *Store) CompleteMultipartUpload(ctx context.Context, bucket, key, uploadID string, wantParts int, wantSize, partSize, maxSize int64) error {
	var parts []types.CompletedPart
	var sizes []int64
	var marker *string
	for {
		out, err := s.client.ListParts(ctx, &s3.ListPartsInput{
			Bucket: aws.String(bucket), Key: aws.String(key), UploadId: aws.String(uploadID),
			PartNumberMarker: marker,
		})
		if err != nil {
			return fmt.Errorf("list parts %s: %w", key, err)
		}
		for _, p := range out.Parts {
			parts = append(parts, types.CompletedPart{ETag: p.ETag, PartNumber: p.PartNumber})
			sizes = append(sizes, aws.ToInt64(p.Size))
		}
		if !aws.ToBool(out.IsTruncated) {
			break
		}
		marker = out.NextPartNumberMarker
	}
	if len(parts) != wantParts {
		return fmt.Errorf("%w: got %d parts, want %d", ErrIncompleteUpload, len(parts), wantParts)
	}
	for i, p := range parts {
		if aws.ToInt32(p.PartNumber) != int32(i+1) {
			return fmt.Errorf("%w: missing part %d", ErrIncompleteUpload, i+1)
		}
	}
	var total int64
	for i, sz := range sizes {
		if i < len(sizes)-1 && sz != partSize {
			return fmt.Errorf("%w: part %d has %d bytes, want %d", ErrIncompleteUpload, i+1, sz, partSize)
		}
		total += sz
	}
	if total != wantSize {
		return fmt.Errorf("%w: uploaded %d bytes, declared %d", ErrIncompleteUpload, total, wantSize)
	}
	if total > maxSize {
		return fmt.Errorf("%w: uploaded %d bytes, max %d", ErrIncompleteUpload, total, maxSize)
	}
	_, err := s.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket: aws.String(bucket), Key: aws.String(key), UploadId: aws.String(uploadID),
		MultipartUpload: &types.CompletedMultipartUpload{Parts: parts},
	})
	if err != nil {
		return fmt.Errorf("complete multipart %s: %w", key, err)
	}
	return nil
}

// AbortMultipartUpload huỷ upload dở; upload không còn tồn tại thì coi như xong.
func (s *Store) AbortMultipartUpload(ctx context.Context, bucket, key, uploadID string) error {
	_, err := s.client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket: aws.String(bucket), Key: aws.String(key), UploadId: aws.String(uploadID),
	})
	var nsu *types.NoSuchUpload
	if err != nil && !errors.As(err, &nsu) {
		return fmt.Errorf("abort multipart %s: %w", key, err)
	}
	return nil
}

func (s *Store) Exists(ctx context.Context, bucket, key string) (bool, error) {
	_, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err == nil {
		return true, nil
	}
	var nf *types.NotFound
	if errors.As(err, &nf) {
		return false, nil
	}
	return false, fmt.Errorf("head %s: %w", key, err)
}

// DeletePrefix xoá mọi object dưới prefix (từng object một; số object ở raw/{id}/ rất ít).
func (s *Store) DeletePrefix(ctx context.Context, bucket, prefix string) (int, error) {
	n := 0
	p := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket), Prefix: aws.String(prefix),
	})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return n, fmt.Errorf("list %s: %w", prefix, err)
		}
		for _, obj := range page.Contents {
			if _, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: obj.Key}); err != nil {
				return n, fmt.Errorf("delete %s: %w", aws.ToString(obj.Key), err)
			}
			n++
		}
	}
	return n, nil
}
