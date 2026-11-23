/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
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

// upload PUTs body the way press tells uploaders to.
func upload(t *testing.T, up Upload, body string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, up.URL, strings.NewReader(body))
	for name, value := range up.Headers {
		req.Header.Set(name, value)
	}
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
	if ok, err := d.Stored(ctx, key, size, false); err != nil || ok {
		t.Fatalf("Stored before upload = %v, %v", ok, err)
	}

	up, err := d.SignUpload(ctx, key, "image/svg+xml", size, 5*time.Minute)
	if err != nil || time.Until(up.Expires) < 4*time.Minute {
		t.Fatalf("SignUpload = %v, %v", up.Expires, err)
	}
	if !strings.Contains(up.URL, "X-Amz-Signature=") || !strings.Contains(up.URL, "/media/"+StagingPrefix+key+"?") {
		t.Errorf("not a presigned path-style URL for the staging key: %s", up.URL)
	}
	// gofakes3 does not check signatures, so check what the uploader must
	// send: the Content-Type and the file's sha256, nothing else.
	parsed, err := url.Parse(up.URL)
	if err != nil {
		t.Fatal(err)
	}
	if got := parsed.Query().Get("X-Amz-SignedHeaders"); got != "content-type;host;x-amz-checksum-sha256" {
		t.Errorf("signed headers = %q", got)
	}
	sum := sha256.Sum256([]byte(body))
	wantHeaders := map[string]string{
		"Content-Type":          "image/svg+xml",
		"X-Amz-Checksum-Sha256": base64.StdEncoding.EncodeToString(sum[:]),
	}
	if !maps.Equal(up.Headers, wantHeaders) {
		t.Errorf("headers = %v, want %v", up.Headers, wantHeaders)
	}
	if code := upload(t, up, body); code != http.StatusOK {
		t.Fatalf("PUT = %d", code)
	}
	if got := get(t, d.PublicURL(key)); got != "" {
		t.Fatalf("an unverified upload is public: %q", got)
	}
	if ok, err := d.Stored(ctx, key, size, false); err != nil || !ok {
		t.Fatalf("Stored after upload = %v, %v", ok, err)
	}
	if got := get(t, d.PublicURL(StagingPrefix+key)); got != "" {
		t.Errorf("the staged upload was not deleted: %q", got)
	}
	// Verified objects are not downloaded again, but must still exist.
	if ok, _ := d.Stored(ctx, key, size, true); !ok {
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
	if ok, _ := d.Stored(ctx, key, size, true); ok {
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
			up, _ := d.SignUpload(ctx, key, "text/plain", int64(len(body)), time.Minute)
			upload(t, up, tt.uploaded)
			ok, err := d.Stored(ctx, key, int64(len(body)), false)
			if ok || !IsRejected(err) {
				t.Fatalf("Stored = %v, %v; want a rejection", ok, err)
			}
			if ok, err := d.Stored(ctx, key, int64(len(body)), false); ok || err != nil {
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
	up, _ := d.SignUpload(ctx, key, "text/plain", size, time.Minute)
	upload(t, up, body)
	if ok, err := d.Stored(ctx, key, size, false); err != nil || !ok {
		t.Fatalf("Stored = %v, %v", ok, err)
	}

	// gofakes3 does not check the signed checksum, so the replay lands.
	if code := upload(t, up, fake); code != http.StatusOK {
		t.Fatalf("replayed PUT = %d", code)
	}
	if ok, err := d.Stored(ctx, key, size, true); err != nil || !ok {
		t.Errorf("Stored after a replayed PUT = %v, %v", ok, err)
	}
	if got := get(t, d.PublicURL(key)); got != body {
		t.Errorf("public file = %q, want %q", got, body)
	}

	// Checking the file again finds the real one.
	if ok, err := d.Stored(ctx, key, size, false); err != nil || !ok {
		t.Errorf("Stored checking again = %v, %v", ok, err)
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
		ok, err := d.Stored(ctx, key, size, false)
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
		up, _ := d.SignUpload(ctx, key, "text/plain", size, time.Minute)
		upload(t, up, body)
		ok, err := d.Stored(ctx, key, size, false)
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
		up, _ := d.SignUpload(ctx, key, "text/plain", size, time.Minute)
		upload(t, up, body)
		if ok, err := d.Stored(ctx, key, size, false); err != nil || !ok {
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
		up, _ := d.SignUpload(ctx, key, "text/plain", size, time.Minute)
		upload(t, up, body)
		if ok, err := d.Stored(ctx, key, size, false); ok || err != nil {
			t.Fatalf("first Stored = %v, %v; want not yet", ok, err)
		}
		if got := get(t, d.PublicURL(StagingPrefix+key)); got != body {
			t.Fatalf("the staged upload must be kept until the copy is seen: %q", got)
		}
		if ok, err := d.Stored(ctx, key, size, false); !ok || err != nil {
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

// checksumS3 wraps gofakes3 with x-amz-checksum-sha256 support, which
// gofakes3 lacks: it remembers the checksum of each PUT, keeps it on
// copies and reports it on HEAD with x-amz-checksum-mode: ENABLED. If check
// is set it refuses a PUT whose body does not match, as S3 does; otherwise
// it stores the uploader's claim unchecked. It counts object GETs.
type checksumS3 struct {
	check bool
	mu    sync.Mutex
	sums  map[string]string
	gets  int
}

func (c *checksumS3) wrap(next http.Handler, _ *s3mem.Backend) http.Handler {
	c.sums = map[string]string{}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/media/")
		c.mu.Lock()
		defer c.mu.Unlock()
		switch {
		case r.Method == http.MethodPut && r.Header.Get("X-Amz-Copy-Source") != "":
			src, _ := url.PathUnescape(strings.TrimPrefix(strings.TrimPrefix(r.Header.Get("X-Amz-Copy-Source"), "/"), "media/"))
			c.sums[key] = c.sums[src]
		case r.Method == http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			claim := r.Header.Get("X-Amz-Checksum-Sha256")
			sum := sha256.Sum256(body)
			if c.check && claim != "" && claim != base64.StdEncoding.EncodeToString(sum[:]) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `<Error><Code>BadDigest</Code></Error>`)
				return
			}
			c.sums[key] = claim
			r.Body = io.NopCloser(bytes.NewReader(body))
		case r.Method == http.MethodHead && r.Header.Get("X-Amz-Checksum-Mode") == "ENABLED":
			if sum := c.sums[key]; sum != "" {
				w.Header().Set("X-Amz-Checksum-Sha256", sum)
			}
		case r.Method == http.MethodGet && key != "":
			c.gets++
		case r.Method == http.MethodDelete:
			delete(c.sums, key)
		}
		next.ServeHTTP(w, r)
	})
}

func (c *checksumS3) downloads() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gets
}

func TestS3UsesTheChecksumTheServiceComputed(t *testing.T) {
	ctx := context.Background()
	const body, fake = "the real file", "the fake file"
	key := keyFor(body)
	size := int64(len(body))

	t.Run("a service that checks checksums", func(t *testing.T) {
		fake3 := &checksumS3{check: true}
		d := newFakeS3With(t, fake3.wrap)
		up, _ := d.SignUpload(ctx, key, "text/plain", size, time.Minute)
		if code := upload(t, up, fake); code != http.StatusBadRequest {
			t.Errorf("PUT of other content = %d, want the service to refuse it", code)
		}
		if code := upload(t, up, body); code != http.StatusOK {
			t.Fatalf("PUT = %d", code)
		}
		if ok, err := d.Stored(ctx, key, size, false); err != nil || !ok {
			t.Fatalf("Stored = %v, %v", ok, err)
		}
		// The copy keeps the checksum, so checking the object key again
		// does not download it either.
		if ok, err := d.Stored(ctx, key, size, false); err != nil || !ok {
			t.Fatalf("Stored checking again = %v, %v", ok, err)
		}
		if n := fake3.downloads(); n != 0 {
			t.Errorf("press downloaded the file %d times", n)
		}
		if got := get(t, d.PublicURL(key)); got != body {
			t.Errorf("public file = %q", got)
		}
		if got := get(t, d.PublicURL(checksumProbe)); got != "" {
			t.Errorf("the probe was stored: %q", got)
		}
	})

	t.Run("a service that stores checksums unchecked", func(t *testing.T) {
		fake3 := &checksumS3{}
		d := newFakeS3With(t, fake3.wrap)
		up, _ := d.SignUpload(ctx, key, "text/plain", size, time.Minute)
		// The checksum the service reports is the signed claim, not the
		// content: press must download the file.
		upload(t, up, fake)
		if ok, err := d.Stored(ctx, key, size, false); ok || !IsRejected(err) {
			t.Fatalf("Stored = %v, %v; want a rejection", ok, err)
		}
		upload(t, up, body)
		if ok, err := d.Stored(ctx, key, size, false); err != nil || !ok {
			t.Fatalf("Stored = %v, %v", ok, err)
		}
		if got := get(t, d.PublicURL(checksumProbe)); got != "" {
			t.Errorf("the probe was left behind: %q", got)
		}
	})
}

// A Print that is Ready was checked already: press only looks for the file.
func TestS3TrustsVerifiedObjects(t *testing.T) {
	ctx := context.Background()
	const body = "the real file"
	key := keyFor(body)
	size := int64(len(body))
	fake3 := &checksumS3{}
	d := newFakeS3With(t, fake3.wrap)
	if _, err := d.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String("media"), Key: aws.String(key), Body: strings.NewReader(body),
	}); err != nil {
		t.Fatal(err)
	}
	if ok, err := d.Stored(ctx, key, size, true); err != nil || !ok {
		t.Fatalf("Stored = %v, %v", ok, err)
	}
	if n := fake3.downloads(); n != 0 {
		t.Errorf("press downloaded a verified file %d times", n)
	}
	// A file of the wrong size is checked (and rejected) all the same.
	if ok, err := d.Stored(ctx, key, size+1, true); ok || !IsRejected(err) {
		t.Errorf("Stored with another size = %v, %v", ok, err)
	}
}
