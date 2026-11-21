/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
)

// newFakeS3 starts an in-memory S3 server with one bucket.
func newFakeS3(t *testing.T, bucket string) *S3 {
	t.Helper()
	backend := s3mem.New()
	if err := backend.CreateBucket(bucket); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(gofakes3.New(backend).Server())
	t.Cleanup(srv.Close)
	return NewS3(S3Config{
		Endpoint: srv.URL, Bucket: bucket, PathStyle: true,
		AccessKeyID: "test", SecretAccessKey: "test",
	})
}

func put(t *testing.T, target, contentType, body string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, target, strings.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func keyFor(body string) string {
	sum := sha256.Sum256([]byte(body))
	return ObjectKey("3f1c9a2e-0000-4000-8000-000000000001", hex.EncodeToString(sum[:]), "image/svg+xml")
}

func TestS3(t *testing.T) {
	ctx := context.Background()
	d := newFakeS3(t, "media")
	if err := d.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if err := NewS3(S3Config{Endpoint: d.cfg.Endpoint, Bucket: "missing", PathStyle: true}).Check(ctx); err == nil {
		t.Error("Check must fail for a missing bucket")
	}

	const body = `<svg xmlns="http://www.w3.org/2000/svg"/>`
	key := keyFor(body)
	size := int64(len(body))
	if ok, err := d.Stored(ctx, key, size); err != nil || ok {
		t.Fatalf("Stored before upload = %v, %v", ok, err)
	}

	u, expires, err := d.SignUpload(ctx, key, "image/svg+xml", size, 5*time.Minute)
	if err != nil || time.Until(expires) < 4*time.Minute {
		t.Fatalf("SignUpload = %v, %v", expires, err)
	}
	if !strings.Contains(u, "X-Amz-Signature=") || !strings.Contains(u, "/media/"+key) {
		t.Errorf("not a presigned path-style URL: %s", u)
	}
	// gofakes3 does not check signatures, so check what the uploader must
	// send: exactly the Content-Type, nothing else.
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	if got := parsed.Query().Get("X-Amz-SignedHeaders"); got != "content-type;host" {
		t.Errorf("signed headers = %q, want only content-type and host", got)
	}
	if code := put(t, u, "image/svg+xml", body); code != http.StatusOK {
		t.Fatalf("PUT = %d", code)
	}
	if ok, err := d.Stored(ctx, key, size); err != nil || !ok {
		t.Fatalf("Stored after upload = %v, %v", ok, err)
	}
	// Verified objects are not downloaded again, but must still exist.
	if ok, _ := d.Stored(ctx, key, size); !ok {
		t.Error("second Stored = false")
	}

	resp, err := http.Get(d.PublicURL(key))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(got) != body || resp.Header.Get("Content-Type") != "image/svg+xml" {
		t.Errorf("public GET = %q, %q", got, resp.Header.Get("Content-Type"))
	}

	if err := d.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if ok, _ := d.Stored(ctx, key, size); ok {
		t.Error("object still stored after Delete")
	}
	if err := d.Delete(ctx, key); err != nil {
		t.Errorf("deleting a missing object: %v", err)
	}
}

func TestS3RejectsWrongUploads(t *testing.T) {
	ctx := context.Background()
	const body = "the real file"
	tests := []struct {
		name     string
		uploaded string
	}{
		{"different content of the same size", "the fake file"},
		{"different size", "short"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := newFakeS3(t, "media")
			key := keyFor(body)
			u, _, _ := d.SignUpload(ctx, key, "text/plain", int64(len(body)), time.Minute)
			put(t, u, "text/plain", tt.uploaded)
			ok, err := d.Stored(ctx, key, int64(len(body)))
			if ok || !IsRejected(err) {
				t.Fatalf("Stored = %v, %v; want a rejection", ok, err)
			}
			if ok, err := d.Stored(ctx, key, int64(len(body))); ok || err != nil {
				t.Errorf("the rejected object must be gone: %v, %v", ok, err)
			}
		})
	}
}

func TestS3PublicURL(t *testing.T) {
	d := NewS3(S3Config{Endpoint: "http://seaweedfs:8333/", Bucket: "media"})
	if got := d.PublicURL("k"); got != "http://seaweedfs:8333/media/k" {
		t.Errorf("default PublicURL = %q", got)
	}
	d = NewS3(S3Config{Endpoint: "http://seaweedfs:8333", Bucket: "media", PublicURL: "https://cdn.example.io/"})
	if got := d.PublicURL("k"); got != "https://cdn.example.io/k" {
		t.Errorf("PublicURL = %q", got)
	}
}
