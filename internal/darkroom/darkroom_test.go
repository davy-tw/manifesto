/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package darkroom

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"

	"go.pkgs.davy.tw/manifesto/internal/signing"
)

const nsUID = "3f1c9a2e-0000-4000-8000-000000000001"

var (
	signKey = []byte("test-key")
	now     = time.Unix(1_800_000_000, 0)
	content = []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)
)

func objectKey(b []byte, ext string) string {
	sum := sha256.Sum256(b)
	return nsUID + "/sha256/" + hex.EncodeToString(sum[:]) + ext
}

func newStore(t *testing.T) *Store {
	return &Store{Dir: t.TempDir(), Keys: func() [][]byte { return [][]byte{signKey} }, Now: func() time.Time { return now }, Log: logr.Discard()}
}

func do(t *testing.T, h http.Handler, method, target string, body []byte) *http.Response {
	t.Helper()
	var r io.Reader
	if body != nil {
		r = strings.NewReader(string(body))
	}
	req := httptest.NewRequest(method, target, r)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Result()
}

func signed(method, prefix, key string, size int64) string {
	return prefix + key + "?" + signing.Query(signKey, method, key, size, now.Add(5*time.Minute)).Encode()
}

func TestUploadAndServe(t *testing.T) {
	s := newStore(t)
	pub := s.PublicHandler()
	key := objectKey(content, ".svg")

	r := do(t, pub, http.MethodPut, signed(http.MethodPut, "/upload/", key, int64(len(content))), content)
	if r.StatusCode != http.StatusCreated {
		t.Fatalf("upload status = %d", r.StatusCode)
	}
	hash := strings.TrimSuffix(key[len(nsUID)+len("/sha256/"):], ".svg")
	if _, err := os.Stat(filepath.Join(s.Dir, nsUID, "sha256", hash[:2], hash[2:4], hash+".svg")); err != nil {
		t.Errorf("file not stored in the two-level layout: %v", err)
	}

	r = do(t, pub, http.MethodGet, "/"+key, nil)
	b, _ := io.ReadAll(r.Body)
	if r.StatusCode != http.StatusOK || string(b) != string(content) {
		t.Fatalf("GET = %d %q", r.StatusCode, b)
	}
	for k, want := range map[string]string{
		"Content-Type":           "image/svg+xml",
		"Cache-Control":          "public, max-age=31536000, immutable",
		"X-Content-Type-Options": "nosniff",
	} {
		if got := r.Header.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if !strings.Contains(r.Header.Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("CSP = %q", r.Header.Get("Content-Security-Policy"))
	}

	// Uploading the same content again is a no-op.
	if r := do(t, pub, http.MethodPut, signed(http.MethodPut, "/upload/", key, int64(len(content))), content); r.StatusCode != http.StatusOK {
		t.Errorf("repeat upload status = %d, want 200", r.StatusCode)
	}
}

func TestUploadRejections(t *testing.T) {
	key := objectKey(content, ".svg")
	size := int64(len(content))
	tests := []struct {
		name   string
		target string
		body   []byte
		want   int
	}{
		{"unsigned", "/upload/" + key, content, http.StatusForbidden},
		{"signed for another method", signed(http.MethodDelete, "/upload/", key, size), content, http.StatusForbidden},
		{"expired", "/upload/" + key + "?" + signing.Query(signKey, http.MethodPut, key, size, now.Add(-time.Second)).Encode(), content, http.StatusForbidden},
		{"body does not match the hash", signed(http.MethodPut, "/upload/", key, size), []byte(strings.Repeat("x", len(content))), http.StatusBadRequest},
		{"body shorter than signed", signed(http.MethodPut, "/upload/", key, size+5), content, http.StatusBadRequest},
		// ServeMux redirects to the cleaned path before any handler runs.
		{"path traversal", signed(http.MethodPut, "/upload/", "../../etc/passwd", size), content, http.StatusTemporaryRedirect},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newStore(t)
			if r := do(t, s.PublicHandler(), http.MethodPut, tt.target, tt.body); r.StatusCode != tt.want {
				b, _ := io.ReadAll(r.Body)
				t.Errorf("status = %d (%s), want %d", r.StatusCode, b, tt.want)
			}
			if r := do(t, s.PublicHandler(), http.MethodGet, "/"+key, nil); r.StatusCode != http.StatusNotFound {
				t.Errorf("a rejected upload must not be served")
			}
			if entries, _ := os.ReadDir(filepath.Join(s.Dir, tmpDir)); len(entries) != 0 {
				t.Errorf("temporary files left behind: %v", entries)
			}
		})
	}
}

func TestServeRejectsBadKeys(t *testing.T) {
	s := newStore(t)
	for _, p := range []string{"/", "/etc/passwd", "/" + nsUID + "/sha256/../../x", "/.tmp/upload-1"} {
		if r := do(t, s.PublicHandler(), http.MethodGet, p, nil); r.StatusCode < 300 {
			t.Errorf("GET %s = %d, want no content", p, r.StatusCode)
		}
	}
}

func TestInternalExistsAndDelete(t *testing.T) {
	s := newStore(t)
	internal := s.InternalHandler()
	key := objectKey(content, ".svg")
	size := int64(len(content))

	if r := do(t, internal, http.MethodHead, signed(http.MethodHead, "/objects/", key, 0), nil); r.StatusCode != http.StatusNotFound {
		t.Errorf("HEAD before upload = %d, want 404", r.StatusCode)
	}
	do(t, s.PublicHandler(), http.MethodPut, signed(http.MethodPut, "/upload/", key, size), content)
	r := do(t, internal, http.MethodHead, signed(http.MethodHead, "/objects/", key, 0), nil)
	if r.StatusCode != http.StatusOK || r.Header.Get("Content-Length") != strconv.Itoa(len(content)) {
		t.Errorf("HEAD after upload = %d, length %q", r.StatusCode, r.Header.Get("Content-Length"))
	}
	if r := do(t, internal, http.MethodHead, "/objects/"+key, nil); r.StatusCode != http.StatusForbidden {
		t.Errorf("unsigned HEAD = %d, want 403", r.StatusCode)
	}
	if r := do(t, internal, http.MethodDelete, signed(http.MethodHead, "/objects/", key, 0), nil); r.StatusCode != http.StatusForbidden {
		t.Errorf("DELETE signed for HEAD = %d, want 403", r.StatusCode)
	}
	if r := do(t, internal, http.MethodDelete, signed(http.MethodDelete, "/objects/", key, 0), nil); r.StatusCode != http.StatusNoContent {
		t.Errorf("DELETE = %d, want 204", r.StatusCode)
	}
	if r := do(t, s.PublicHandler(), http.MethodGet, "/"+key, nil); r.StatusCode != http.StatusNotFound {
		t.Errorf("GET after delete = %d, want 404", r.StatusCode)
	}
	if r := do(t, internal, http.MethodDelete, signed(http.MethodDelete, "/objects/", key, 0), nil); r.StatusCode != http.StatusNoContent {
		t.Errorf("repeated DELETE = %d, want 204", r.StatusCode)
	}
	if r := do(t, s.PublicHandler(), http.MethodDelete, signed(http.MethodDelete, "/objects/", key, 0), nil); r.StatusCode == http.StatusNoContent {
		t.Error("the public port must not accept deletions")
	}
}

func TestCleanTemp(t *testing.T) {
	s := newStore(t)
	dir := filepath.Join(s.Dir, tmpDir)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	old, fresh := filepath.Join(dir, "upload-old"), filepath.Join(dir, "upload-fresh")
	for _, p := range []string{old, fresh} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(old, now.Add(-2*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(fresh, now, now); err != nil {
		t.Fatal(err)
	}
	s.CleanTemp(time.Hour)
	if _, err := os.Stat(old); err == nil {
		t.Error("old temporary file was kept")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("fresh temporary file was removed")
	}
}
