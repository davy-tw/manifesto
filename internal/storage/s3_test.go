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

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
)

// newFakeS3 starts an in-memory S3 server with one bucket.
func newFakeS3(t *testing.T) *S3 {
	return newFakeS3With(t, nil)
}

// newFakeS3With starts the fake S3 behind wrap, which can tamper with
// requests the way a racing uploader or a different service would.
func newFakeS3With(t *testing.T, wrap func(next http.Handler, backend *s3mem.Backend) http.Handler) *S3 {
	const bucket = "media"
	t.Helper()
	backend := s3mem.New()
	if err := backend.CreateBucket(bucket); err != nil {
		t.Fatal(err)
	}
	h := gofakes3.New(backend).Server()
	if wrap != nil {
		h = wrap(h, backend)
	}
	srv := httptest.NewServer(h)
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

// get returns the body of a successful GET, or "".
func get(t *testing.T, target string) string {
	t.Helper()
	resp, err := http.Get(target)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	return string(b)
}

func keyFor(body string) string {
	sum := sha256.Sum256([]byte(body))
	return ObjectKey("3f1c9a2e-0000-4000-8000-000000000001", hex.EncodeToString(sum[:]), "image/svg+xml")
}

func TestS3(t *testing.T) {
	ctx := context.Background()
	d := newFakeS3(t)
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
	if !strings.Contains(u, "X-Amz-Signature=") || !strings.Contains(u, "/media/"+StagingPrefix+key+"?") {
		t.Errorf("not a presigned path-style URL for the staging key: %s", u)
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
	if got := get(t, d.PublicURL(key)); got != "" {
		t.Fatalf("an unverified upload is public: %q", got)
	}
	if ok, err := d.Stored(ctx, key, size); err != nil || !ok {
		t.Fatalf("Stored after upload = %v, %v", ok, err)
	}
	if got := get(t, d.PublicURL(StagingPrefix+key)); got != "" {
		t.Errorf("the staged upload was not deleted: %q", got)
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
			d := newFakeS3(t)
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

// A presigned PUT stays valid after the upload is verified, and anyone who
// can read the Print sees it. Replaying it must not replace the file.
func TestS3ReplayedUploadCannotReplaceTheFile(t *testing.T) {
	ctx := context.Background()
	d := newFakeS3(t)
	const body, fake = "the real file", "the fake file"
	key := keyFor(body)
	size := int64(len(body))
	u, _, _ := d.SignUpload(ctx, key, "text/plain", size, time.Minute)
	put(t, u, "text/plain", body)
	if ok, err := d.Stored(ctx, key, size); err != nil || !ok {
		t.Fatalf("Stored = %v, %v", ok, err)
	}

	if code := put(t, u, "text/plain", fake); code != http.StatusOK {
		t.Fatalf("replayed PUT = %d", code)
	}
	if ok, err := d.Stored(ctx, key, size); err != nil || !ok {
		t.Errorf("Stored after a replayed PUT = %v, %v", ok, err)
	}
	if got := get(t, d.PublicURL(key)); got != body {
		t.Errorf("public file = %q, want %q", got, body)
	}

	// After a restart press hashes the file again; it is still the real one.
	verified.Delete(d.cacheKey(key))
	if ok, err := d.Stored(ctx, key, size); err != nil || !ok {
		t.Errorf("Stored after a restart = %v, %v", ok, err)
	}

	// Deleting the file also removes the replayed upload.
	if err := d.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if got := get(t, d.PublicURL(StagingPrefix+key)); got != "" {
		t.Errorf("staged upload left after Delete: %q", got)
	}
}

// Objects uploaded straight to the object key, as before uploads were
// staged, are still verified.
func TestS3VerifiesObjectsAtTheObjectKey(t *testing.T) {
	ctx := context.Background()
	d := newFakeS3(t)
	const body = "the real file"
	key := keyFor(body)
	size := int64(len(body))
	for _, tt := range []struct {
		content string
		stored  bool
	}{{body, true}, {"the fake file", false}} {
		_, err := d.client.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String("media"), Key: aws.String(key), Body: strings.NewReader(tt.content),
		})
		if err != nil {
			t.Fatal(err)
		}
		verified.Delete(d.cacheKey(key))
		ok, err := d.Stored(ctx, key, size)
		if ok != tt.stored || (!tt.stored && !IsRejected(err)) {
			t.Errorf("Stored(%q) = %v, %v", tt.content, ok, err)
		}
	}
	if got := get(t, d.PublicURL(key)); got != "" {
		t.Errorf("rejected object still public: %q", got)
	}
}

// gofakes3 ignores x-amz-copy-source-if-match and gives copies the source's
// ETag, so these tests tamper with requests to reach the other branches.
func TestS3PublishRaces(t *testing.T) {
	ctx := context.Background()
	const body, fake = "the real file", "the fake file"
	key := keyFor(body)
	size := int64(len(body))
	isCopy := func(r *http.Request) bool {
		return r.Method == http.MethodPut && r.Header.Get("X-Amz-Copy-Source") != ""
	}

	t.Run("a replay between hashing and copying, on a service that ignores the condition", func(t *testing.T) {
		d := newFakeS3With(t, func(next http.Handler, backend *s3mem.Backend) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if isCopy(r) {
					if _, err := backend.PutObject("media", StagingPrefix+key, map[string]string{"Content-Type": "text/plain"},
						strings.NewReader(fake), int64(len(fake)), nil); err != nil {
						t.Error(err)
					}
				}
				next.ServeHTTP(w, r)
			})
		})
		u, _, _ := d.SignUpload(ctx, key, "text/plain", size, time.Minute)
		put(t, u, "text/plain", body)
		ok, err := d.Stored(ctx, key, size)
		if ok || !IsRejected(err) {
			t.Fatalf("Stored = %v, %v; want the copy rejected", ok, err)
		}
		if got := get(t, d.PublicURL(key)); got != "" {
			t.Errorf("the replayed file is public: %q", got)
		}
		if got := get(t, d.PublicURL(StagingPrefix+key)); got != "" {
			t.Errorf("the replayed upload was kept: %q", got)
		}
	})

	t.Run("a service that gives copies their own ETag", func(t *testing.T) {
		d := newFakeS3With(t, func(next http.Handler, _ *s3mem.Backend) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodHead && !strings.Contains(r.URL.Path, StagingPrefix) {
					w = etagRewriter{w}
				}
				next.ServeHTTP(w, r)
			})
		})
		u, _, _ := d.SignUpload(ctx, key, "text/plain", size, time.Minute)
		put(t, u, "text/plain", body)
		if ok, err := d.Stored(ctx, key, size); err != nil || !ok {
			t.Fatalf("Stored = %v, %v", ok, err)
		}
		if got := get(t, d.PublicURL(key)); got != body {
			t.Errorf("public file = %q", got)
		}
	})

	t.Run("a copy that shows up late", func(t *testing.T) {
		var hidden bool
		d := newFakeS3With(t, func(next http.Handler, _ *s3mem.Backend) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if isCopy(r) {
					hidden = true
				} else if hidden && r.Method == http.MethodHead && !strings.Contains(r.URL.Path, StagingPrefix) {
					hidden = false
					w.WriteHeader(http.StatusNotFound)
					return
				}
				next.ServeHTTP(w, r)
			})
		})
		u, _, _ := d.SignUpload(ctx, key, "text/plain", size, time.Minute)
		put(t, u, "text/plain", body)
		if ok, err := d.Stored(ctx, key, size); ok || err != nil {
			t.Fatalf("first Stored = %v, %v; want not yet", ok, err)
		}
		if got := get(t, d.PublicURL(StagingPrefix+key)); got != body {
			t.Fatalf("the staged upload must be kept until the copy is seen: %q", got)
		}
		if ok, err := d.Stored(ctx, key, size); !ok || err != nil {
			t.Fatalf("second Stored = %v, %v", ok, err)
		}
		if got := get(t, d.PublicURL(StagingPrefix+key)); got != "" {
			t.Errorf("staged upload left behind: %q", got)
		}
	})
}

// etagRewriter answers with a different ETag, as services that encrypt
// objects do for copies.
type etagRewriter struct{ http.ResponseWriter }

func (e etagRewriter) WriteHeader(code int) {
	e.Header().Set("ETag", `"rewritten"`)
	e.ResponseWriter.WriteHeader(code)
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
