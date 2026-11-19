/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

// Package storage abstracts where Print files live. press uses a Driver to
// hand out upload URLs, notice finished uploads and delete files; visitors
// fetch files from PublicURL directly.
package storage

import (
	"context"
	"fmt"
	"mime"
	"net/http"
	"strings"
	"time"

	"go.pkgs.davy.tw/manifesto/internal/signing"
)

// Driver is one storage backend.
type Driver interface {
	// SignUpload returns a URL the file of size bytes and contentType can be
	// PUT to until expires. Some backends require the PUT to send
	// contentType as its Content-Type header.
	SignUpload(ctx context.Context, objectKey, contentType string, size int64, ttl time.Duration) (url string, expires time.Time, err error)
	// Stored reports whether the object is stored with the given size and
	// the hash in its key. A backend that cannot verify uploads as they
	// arrive verifies them here and deletes objects that do not match.
	Stored(ctx context.Context, objectKey string, size int64) (bool, error)
	// Delete removes the object; deleting a missing object is not an error.
	Delete(ctx context.Context, objectKey string) error
	// Check reports whether the backend is reachable.
	Check(ctx context.Context) error
	// PublicURL is where visitors read the object.
	PublicURL(objectKey string) string
}

// ObjectKey is where a file with the given hash and content type is stored
// for a namespace: <namespace UID>/sha256/<hash>.<ext>.
func ObjectKey(namespaceUID, sha256, contentType string) string {
	return namespaceUID + "/sha256/" + sha256 + Extension(contentType)
}

// Extension returns the file extension for a content type, or "".
func Extension(contentType string) string {
	switch contentType {
	case "image/jpeg":
		return ".jpg"
	case "image/svg+xml":
		return ".svg"
	case "text/plain":
		return ".txt"
	}
	exts, err := mime.ExtensionsByType(contentType)
	if err != nil || len(exts) == 0 {
		return ""
	}
	// Prefer the shortest, most common extension.
	best := exts[0]
	for _, e := range exts[1:] {
		if len(e) < len(best) {
			best = e
		}
	}
	return best
}

// PVC stores files with darkroom on a PersistentVolume.
type PVC struct {
	// InternalURL is darkroom's internal port, e.g. http://manifesto-darkroom.manifesto-system.svc:8081.
	InternalURL string
	// PublicBase is the Archive's publicURL.
	PublicBase string
	// Key signs requests to darkroom.
	Key    []byte
	Client *http.Client
	Now    func() time.Time
}

var _ Driver = (*PVC)(nil)

func (p *PVC) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *PVC) client() *http.Client {
	if p.Client != nil {
		return p.Client
	}
	return &http.Client{Timeout: 10 * time.Second}
}

// SignUpload implements Driver. darkroom ignores the Content-Type header and
// serves files by their extension.
func (p *PVC) SignUpload(_ context.Context, objectKey, _ string, size int64, ttl time.Duration) (string, time.Time, error) {
	expires := p.now().Add(ttl).Truncate(time.Second)
	q := signing.Query(p.Key, http.MethodPut, objectKey, size, expires)
	return strings.TrimSuffix(p.PublicBase, "/") + "/upload/" + objectKey + "?" + q.Encode(), expires, nil
}

// PublicURL implements Driver.
func (p *PVC) PublicURL(objectKey string) string {
	return strings.TrimSuffix(p.PublicBase, "/") + "/" + objectKey
}

func (p *PVC) internal(ctx context.Context, method, objectKey string) (int, error) {
	q := signing.Query(p.Key, method, objectKey, 0, p.now().Add(time.Minute))
	u := strings.TrimSuffix(p.InternalURL, "/") + "/objects/" + objectKey + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return 0, err
	}
	resp, err := p.client().Do(req)
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}

// Stored implements Driver. darkroom verifies size and hash while it
// receives an upload, so a stored file is a verified file.
func (p *PVC) Stored(ctx context.Context, objectKey string, _ int64) (bool, error) {
	code, err := p.internal(ctx, http.MethodHead, objectKey)
	switch {
	case err != nil:
		return false, err
	case code == http.StatusOK:
		return true, nil
	case code == http.StatusNotFound:
		return false, nil
	}
	return false, fmt.Errorf("darkroom answered %d", code)
}

// Delete implements Driver.
func (p *PVC) Delete(ctx context.Context, objectKey string) error {
	code, err := p.internal(ctx, http.MethodDelete, objectKey)
	if err != nil {
		return err
	}
	if code != http.StatusNoContent && code != http.StatusNotFound {
		return fmt.Errorf("darkroom answered %d", code)
	}
	return nil
}

// Check implements Driver.
func (p *PVC) Check(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(p.InternalURL, "/")+"/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := p.client().Do(req)
	if err != nil {
		return fmt.Errorf("darkroom is not reachable: %w", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("darkroom health check answered %d", resp.StatusCode)
	}
	return nil
}
