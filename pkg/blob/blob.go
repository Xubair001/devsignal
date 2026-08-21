// Package blob is object storage for anything too large or too sensitive to sit
// in a database row.
//
// Resumes and their extracted text live here rather than in Postgres. That keeps
// the densest PII in one place that can be locked down and deleted, and keeps it
// out of database backups and query logs.
package blob

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// ErrNotFound is returned for a missing key, so callers can distinguish "gone"
// from "broken" — which matters for erasure verification, where absence is the
// success condition.
var ErrNotFound = errors.New("blob: not found")

type Config struct {
	Endpoint  string
	Bucket    string
	AccessKey string
	SecretKey string
	// PathStyle is required for MinIO and any S3-compatible endpoint that does
	// not do virtual-host addressing.
	PathStyle bool
	Region    string
}

type Store struct {
	c      *s3.Client
	bucket string
}

func New(ctx context.Context, cfg Config) (*Store, error) {
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("blob: bucket is required")
	}
	region := cfg.Region
	if region == "" {
		region = "us-east-1" // MinIO ignores it, but the SDK requires one
	}

	opts := []func(*s3.Options){
		func(o *s3.Options) {
			o.Region = region
			o.UsePathStyle = cfg.PathStyle
			if cfg.AccessKey != "" {
				o.Credentials = credentials.NewStaticCredentialsProvider(
					cfg.AccessKey, cfg.SecretKey, "")
			}
			if cfg.Endpoint != "" {
				o.BaseEndpoint = aws.String(cfg.Endpoint)
			}
		},
	}
	st := &Store{c: s3.New(s3.Options{}, opts...), bucket: cfg.Bucket}

	if err := st.ensureBucket(ctx); err != nil {
		return nil, err
	}
	return st, nil
}

// ensureBucket creates the bucket if it is absent. Convenient locally; harmless
// in production, where the bucket already exists and the call is a no-op.
func (s *Store) ensureBucket(ctx context.Context) error {
	_, err := s.c.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(s.bucket)})
	if err == nil {
		return nil
	}
	if _, cerr := s.c.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(s.bucket),
	}); cerr != nil {
		var owned *types.BucketAlreadyOwnedByYou
		var exists *types.BucketAlreadyExists
		if errors.As(cerr, &owned) || errors.As(cerr, &exists) {
			return nil
		}
		return fmt.Errorf("blob: ensuring bucket %q: %w", s.bucket, cerr)
	}
	return nil
}

func (s *Store) Put(ctx context.Context, key string, body []byte, contentType string) error {
	_, err := s.c.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(body),
		ContentType: aws.String(contentType),
	})
	if err != nil {
		return fmt.Errorf("blob: put %q: %w", key, err)
	}
	return nil
}

func (s *Store) Get(ctx context.Context, key string) ([]byte, error) {
	out, err := s.c.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(key),
	})
	if err != nil {
		if isMissing(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("blob: get %q: %w", key, err)
	}
	defer func() { _ = out.Body.Close() }()
	return io.ReadAll(out.Body)
}

// Delete is idempotent: deleting an absent key succeeds. Erasure must be
// re-runnable, and a second run finding nothing is the success case.
func (s *Store) Delete(ctx context.Context, key string) error {
	_, err := s.c.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(key),
	})
	if err != nil && !isMissing(err) {
		return fmt.Errorf("blob: delete %q: %w", key, err)
	}
	return nil
}

// DeletePrefix removes everything under a prefix and reports how many objects it
// removed. Used by erasure: per-user keys are prefixed, so one call clears every
// object belonging to a user even if the database rows are already gone.
func (s *Store) DeletePrefix(ctx context.Context, prefix string) (int, error) {
	if strings.TrimSpace(prefix) == "" {
		// A blank prefix would delete the whole bucket. Refuse rather than trust
		// the caller not to pass an empty user id.
		return 0, fmt.Errorf("blob: refusing to delete an empty prefix")
	}

	var removed int
	var token *string
	for {
		page, err := s.c.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket: aws.String(s.bucket), Prefix: aws.String(prefix), ContinuationToken: token,
		})
		if err != nil {
			return removed, fmt.Errorf("blob: list %q: %w", prefix, err)
		}
		for _, o := range page.Contents {
			if o.Key == nil {
				continue
			}
			if err := s.Delete(ctx, *o.Key); err != nil {
				return removed, err
			}
			removed++
		}
		if page.IsTruncated == nil || !*page.IsTruncated {
			return removed, nil
		}
		token = page.NextContinuationToken
	}
}

// CountPrefix is the erasure verification primitive: after a delete, the answer
// must be zero.
func (s *Store) CountPrefix(ctx context.Context, prefix string) (int, error) {
	var n int
	var token *string
	for {
		page, err := s.c.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket: aws.String(s.bucket), Prefix: aws.String(prefix), ContinuationToken: token,
		})
		if err != nil {
			return n, fmt.Errorf("blob: list %q: %w", prefix, err)
		}
		n += len(page.Contents)
		if page.IsTruncated == nil || !*page.IsTruncated {
			return n, nil
		}
		token = page.NextContinuationToken
	}
}

func isMissing(err error) bool {
	var nk *types.NoSuchKey
	var nf *types.NotFound
	return errors.As(err, &nk) || errors.As(err, &nf)
}
