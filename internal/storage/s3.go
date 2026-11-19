/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"go.pkgs.davy.tw/manifesto/internal/signing"
)

// S3Config describes an S3-compatible bucket.
type S3Config struct {
	Endpoint        string
	Region          string
	Bucket          string
	PathStyle       bool
	AccessKeyID     string
	SecretAccessKey string
	// PublicURL is where visitors read objects; defaults to the bucket URL.
	PublicURL string
}

// S3 stores files in an S3-compatible bucket using only the standard S3
// API: presigned PutObject for uploads, HeadObject/GetObject to verify them,
// DeleteObject, and HeadBucket as a health check.
type S3 struct {
	cfg     S3Config
	client  *s3.Client
	presign *s3.PresignClient
}

var _ Driver = (*S3)(nil)

// verified remembers objects whose content press has already hashed, keyed
// by endpoint, bucket and object key, so each upload is downloaded once.
var verified sync.Map

// NewS3 returns a driver for cfg.
func NewS3(cfg S3Config) *S3 {
	region := cfg.Region
	if region == "" {
		region = "us-east-1"
	}
	client := s3.New(s3.Options{
		BaseEndpoint: aws.String(cfg.Endpoint),
		Region:       region,
		UsePathStyle: cfg.PathStyle,
		Credentials:  credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		// Only send checksums when the API requires them: optional
		// checksum headers are not understood by every S3-compatible service.
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
	return &S3{cfg: cfg, client: client, presign: s3.NewPresignClient(client)}
}

// SignUpload implements Driver. The PUT must send contentType as its
// Content-Type header, which is signed so the stored object is served with it.
func (d *S3) SignUpload(ctx context.Context, objectKey, contentType string, _ int64, ttl time.Duration) (string, time.Time, error) {
	req, err := d.presign.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(d.cfg.Bucket),
		Key:         aws.String(objectKey),
		ContentType: aws.String(contentType),
		// Content-addressed objects never change.
		CacheControl: aws.String("public, max-age=31536000, immutable"),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", time.Time{}, err
	}
	return req.URL, time.Now().Add(ttl).Truncate(time.Second), nil
}

// Stored implements Driver. The first time an object is seen it is
// downloaded and hashed; an object of the wrong size or hash is deleted so
// the uploader can try again while the upload URL is valid.
func (d *S3) Stored(ctx context.Context, objectKey string, size int64) (bool, error) {
	head, err := d.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(d.cfg.Bucket), Key: aws.String(objectKey)})
	if isNotFound(err) {
		verified.Delete(d.cacheKey(objectKey))
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if aws.ToInt64(head.ContentLength) != size {
		return false, d.reject(ctx, objectKey, fmt.Sprintf("size %d, want %d", aws.ToInt64(head.ContentLength), size))
	}
	if _, ok := verified.Load(d.cacheKey(objectKey)); ok {
		return true, nil
	}
	want, err := signing.ParseObjectKey(objectKey)
	if err != nil {
		return false, err
	}
	obj, err := d.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(d.cfg.Bucket), Key: aws.String(objectKey)})
	if err != nil {
		return false, err
	}
	defer func() { _ = obj.Body.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(obj.Body, size+1)); err != nil {
		return false, err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return false, d.reject(ctx, objectKey, "sha256 "+got)
	}
	verified.Store(d.cacheKey(objectKey), struct{}{})
	return true, nil
}

// errRejected reports an upload that did not match its Print and was deleted.
var errRejected = errors.New("uploaded object does not match the Print")

func (d *S3) reject(ctx context.Context, objectKey, why string) error {
	if err := d.Delete(ctx, objectKey); err != nil {
		return err
	}
	return fmt.Errorf("%w (%s); it was deleted, upload the right file again", errRejected, why)
}

// IsRejected reports whether err is a deleted upload that did not match.
func IsRejected(err error) bool { return errors.Is(err, errRejected) }

// Delete implements Driver.
func (d *S3) Delete(ctx context.Context, objectKey string) error {
	verified.Delete(d.cacheKey(objectKey))
	_, err := d.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(d.cfg.Bucket), Key: aws.String(objectKey)})
	if isNotFound(err) {
		return nil
	}
	return err
}

// Check implements Driver.
func (d *S3) Check(ctx context.Context) error {
	if _, err := d.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(d.cfg.Bucket)}); err != nil {
		return fmt.Errorf("bucket %q is not reachable: %w", d.cfg.Bucket, err)
	}
	return nil
}

// PublicURL implements Driver.
func (d *S3) PublicURL(objectKey string) string {
	base := d.cfg.PublicURL
	if base == "" {
		base = strings.TrimSuffix(d.cfg.Endpoint, "/") + "/" + d.cfg.Bucket
	}
	return strings.TrimSuffix(base, "/") + "/" + objectKey
}

func (d *S3) cacheKey(objectKey string) string {
	return d.cfg.Endpoint + "|" + d.cfg.Bucket + "|" + objectKey
}

func isNotFound(err error) bool {
	var re *awshttp.ResponseError
	return errors.As(err, &re) && re.HTTPStatusCode() == http.StatusNotFound
}
