/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package storage

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
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
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

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
// API: presigned PutObject for uploads, HeadObject (with its sha256
// checksum) or GetObject to verify them, CopyObject to publish them,
// DeleteObject, and HeadBucket as a health check.
//
// A presigned PUT cannot be revoked and can be replayed until it expires, so
// uploads never go to the object key visitors read. They land under
// StagingPrefix, and press copies an upload to its object key only after
// checking its size and hash. A replayed upload URL can therefore only
// overwrite the staging copy, never a verified file.
type S3 struct {
	cfg     S3Config
	client  *s3.Client
	presign *s3.PresignClient
}

// StagingPrefix is prepended to an object key to get the key uploads are
// signed for. A bucket lifecycle rule may expire objects under it after a
// day; press deletes the ones it handles itself.
const StagingPrefix = "staging/"

var _ Driver = (*S3)(nil)

// checksumChecks remembers, by endpoint and bucket, whether the service
// rejects a PUT whose x-amz-checksum-sha256 does not match its body.
var checksumChecks sync.Map

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

// SignUpload implements Driver. The URL is for the staging key of
// objectKey. The PUT must send the returned headers, which are signed:
// contentType, so the stored object is served with it (CopyObject keeps
// it), and the file's sha256 as x-amz-checksum-sha256, so the service
// refuses other content and press can read the hash it computed instead of
// downloading the file. Long-lived caching of the immutable objects belongs
// to whatever serves publicURL (a CDN or a route in front of the bucket).
func (d *S3) SignUpload(ctx context.Context, objectKey, contentType string, _ int64, ttl time.Duration) (Upload, error) {
	want, err := signing.ParseObjectKey(objectKey)
	if err != nil {
		return Upload{}, err
	}
	sum, err := checksum(want)
	if err != nil {
		return Upload{}, err
	}
	req, err := d.presign.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:         aws.String(d.cfg.Bucket),
		Key:            aws.String(StagingPrefix + objectKey),
		ContentType:    aws.String(contentType),
		ChecksumSHA256: aws.String(sum),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return Upload{}, err
	}
	headers := map[string]string{}
	for name, values := range req.SignedHeader {
		if !strings.EqualFold(name, "Host") {
			headers[name] = strings.Join(values, ",")
		}
	}
	return Upload{URL: req.URL, Headers: headers, Expires: time.Now().Add(ttl).Truncate(time.Second)}, nil
}

// checksum turns a hex sha256 into the base64 form S3 checksums use.
func checksum(hexSum string) (string, error) {
	b, err := hex.DecodeString(hexSum)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

// Stored implements Driver. Only press writes objectKey, after checking the
// file, so an object there that press reported before (verified) is trusted
// without reading it again. Otherwise an object at objectKey is checked
// once (it may predate staged uploads), and a staged upload is checked and
// copied to objectKey. An upload of the wrong size or hash is deleted so
// the uploader can try again while the upload URL is valid.
func (d *S3) Stored(ctx context.Context, objectKey string, size int64, verified bool) (bool, error) {
	want, err := signing.ParseObjectKey(objectKey)
	if err != nil {
		return false, err
	}
	head, err := d.head(ctx, objectKey)
	if err != nil {
		return false, err
	}
	if head == nil {
		return d.publish(ctx, objectKey, want, size)
	}
	if verified && aws.ToInt64(head.ContentLength) == size {
		return true, nil
	}
	if err := d.verify(ctx, objectKey, want, size, head); err != nil {
		return false, err
	}
	// Clean up a staged upload left behind, e.g. when the copy only showed
	// up after publish gave up.
	if err := d.deleteKey(ctx, StagingPrefix+objectKey); err != nil {
		return false, err
	}
	return true, nil
}

// publish verifies the staged upload of objectKey and copies it into place.
func (d *S3) publish(ctx context.Context, objectKey, want string, size int64) (bool, error) {
	staging := StagingPrefix + objectKey
	head, err := d.head(ctx, staging)
	if err != nil || head == nil {
		return false, err
	}
	if err := d.verify(ctx, staging, want, size, head); err != nil {
		return false, err
	}
	// The upload URL may still be valid: only copy the version that was
	// just checked. Services that ignore the condition are caught below.
	etag := aws.ToString(head.ETag)
	if _, err := d.client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:            aws.String(d.cfg.Bucket),
		Key:               aws.String(objectKey),
		CopySource:        aws.String(d.cfg.Bucket + "/" + staging),
		CopySourceIfMatch: aws.String(etag),
	}); err != nil {
		return false, fmt.Errorf("copying the verified upload into place: %w", err)
	}
	// The staged upload is deleted only once the copy is known to be good:
	// a service that is slow to show the copy is asked again next time.
	copied, err := d.head(ctx, objectKey)
	if err != nil || copied == nil {
		return false, err
	}
	if etag == "" || aws.ToString(copied.ETag) != etag {
		// Either the staged upload changed after it was checked, or the
		// service gives copies their own ETag: check the copy itself.
		if err := d.verify(ctx, objectKey, want, size, copied); err != nil {
			if IsRejected(err) {
				// The staging key holds the same wrong content.
				if derr := d.deleteKey(ctx, staging); derr != nil {
					return false, derr
				}
			}
			return false, err
		}
	}
	if err := d.deleteKey(ctx, staging); err != nil {
		return false, err
	}
	return true, nil
}

// verify checks the size and sha256 of key, whose HeadObject is head, and
// deletes it if they do not match. The sha256 the service computed is used
// when it has one and is known to check the ones uploaders send; otherwise
// the object is downloaded, and only if its ETag still matches head, so
// what was hashed is what head describes.
func (d *S3) verify(ctx context.Context, key, want string, size int64, head *s3.HeadObjectOutput) error {
	if got := aws.ToInt64(head.ContentLength); got != size {
		return d.reject(ctx, key, fmt.Sprintf("size %d, want %d", got, size))
	}
	if sum := aws.ToString(head.ChecksumSHA256); sum != "" && head.ChecksumType != types.ChecksumTypeComposite {
		checked, err := d.checksChecksums(ctx)
		if err != nil {
			return err
		}
		if checked {
			if wantSum, _ := checksum(want); sum != wantSum {
				return d.reject(ctx, key, "sha256 checksum "+sum)
			}
			return nil
		}
	}
	in := &s3.GetObjectInput{Bucket: aws.String(d.cfg.Bucket), Key: aws.String(key)}
	if head.ETag != nil {
		in.IfMatch = head.ETag
	}
	obj, err := d.client.GetObject(ctx, in)
	if err != nil {
		return err
	}
	defer func() { _ = obj.Body.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(obj.Body, size+1))
	if err != nil {
		return err
	}
	if n != size {
		return d.reject(ctx, key, fmt.Sprintf("size %d, want %d", n, size))
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return d.reject(ctx, key, "sha256 "+got)
	}
	return nil
}

// checksumProbe is the staging key used to find out whether the service
// checks x-amz-checksum-sha256.
const checksumProbe = StagingPrefix + "manifesto-checksum-probe"

// checksChecksums reports whether the service refuses a PUT whose
// x-amz-checksum-sha256 does not match its body. Only then does the
// checksum it reports say what the object holds: a service that stored the
// uploader's claim unchecked would report the signed, expected hash for any
// content. It is asked once per process and bucket, with a tiny PUT.
func (d *S3) checksChecksums(ctx context.Context) (bool, error) {
	if v, ok := checksumChecks.Load(d.bucketID()); ok {
		return v.(bool), nil
	}
	empty := sha256.Sum256(nil)
	_, err := d.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:         aws.String(d.cfg.Bucket),
		Key:            aws.String(checksumProbe),
		Body:           strings.NewReader("not empty"),
		ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(empty[:])),
	})
	var re *awshttp.ResponseError
	switch {
	case err == nil:
		if err := d.deleteKey(ctx, checksumProbe); err != nil {
			return false, err
		}
		checksumChecks.Store(d.bucketID(), false)
		return false, nil
	case errors.As(err, &re) && re.HTTPStatusCode() == http.StatusBadRequest:
		checksumChecks.Store(d.bucketID(), true)
		return true, nil
	}
	return false, fmt.Errorf("checking whether the service verifies checksums: %w", err)
}

func (d *S3) head(ctx context.Context, key string) (*s3.HeadObjectOutput, error) {
	head, err := d.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(d.cfg.Bucket), Key: aws.String(key), ChecksumMode: types.ChecksumModeEnabled,
	})
	if isNotFound(err) {
		return nil, nil
	}
	return head, err
}

// errRejected reports an upload that did not match its Print and was deleted.
var errRejected = errors.New("uploaded object does not match the Print")

func (d *S3) reject(ctx context.Context, key, why string) error {
	if err := d.deleteKey(ctx, key); err != nil {
		return err
	}
	return fmt.Errorf("%w (%s); it was deleted, upload the right file again", errRejected, why)
}

// IsRejected reports whether err is a deleted upload that did not match.
func IsRejected(err error) bool { return errors.Is(err, errRejected) }

// Delete implements Driver. It also deletes an upload still waiting in
// staging, for example one replayed after the file was published.
func (d *S3) Delete(ctx context.Context, objectKey string) error {
	if err := d.deleteKey(ctx, StagingPrefix+objectKey); err != nil {
		return err
	}
	return d.deleteKey(ctx, objectKey)
}

func (d *S3) deleteKey(ctx context.Context, key string) error {
	_, err := d.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(d.cfg.Bucket), Key: aws.String(key)})
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

func (d *S3) bucketID() string {
	return d.cfg.Endpoint + "|" + d.cfg.Bucket
}

func isNotFound(err error) bool {
	var re *awshttp.ResponseError
	return errors.As(err, &re) && re.HTTPStatusCode() == http.StatusNotFound
}
