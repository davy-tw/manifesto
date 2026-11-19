/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

// Package darkroom stores and serves media files on a local directory,
// usually a PersistentVolume. It never talks to the Kubernetes API: press
// hands out signed URLs, darkroom checks the signatures.
//
// Public port:
//
//	GET|HEAD /<objectKey>          the file, cached forever (the key is its hash)
//	PUT      /upload/<objectKey>   signed upload; the body must match the
//	                               signed size and the hash in the key
//
// Internal port (cluster only, for press):
//
//	HEAD|DELETE /objects/<objectKey>   signed existence check and deletion
//	GET         /healthz
package darkroom

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"

	"go.pkgs.davy.tw/manifesto/internal/signing"
)

const (
	tmpDir = ".tmp"
	// maxSize caps any upload, whatever its signature says.
	maxSize = 100 << 20
)

// Store is a directory of content-addressed files.
type Store struct {
	// Dir is the root of the store.
	Dir string
	// Keys returns the accepted signing keys.
	Keys func() [][]byte
	// Now returns the current time; defaults to time.Now.
	Now func() time.Time
	Log logr.Logger
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// filePath maps "<uid>/sha256/<hash>.<ext>" to <Dir>/<uid>/sha256/ab/cd/<hash>.<ext>.
func (s *Store) filePath(objectKey string) (string, error) {
	hash, err := signing.ParseObjectKey(objectKey)
	if err != nil {
		return "", err
	}
	dir, file := path.Split(objectKey)
	return filepath.Join(s.Dir, filepath.FromSlash(dir), hash[0:2], hash[2:4], file), nil
}

// PublicHandler serves files and accepts signed uploads.
func (s *Store) PublicHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /upload/", s.upload)
	mux.HandleFunc("OPTIONS /upload/", preflight)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("GET /", s.serve)
	return mux
}

// InternalHandler answers press's signed existence checks and deletions.
func (s *Store) InternalHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("HEAD /objects/", s.exists)
	mux.HandleFunc("DELETE /objects/", s.delete)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	return mux
}

func (s *Store) serve(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/")
	p, err := s.filePath(key)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(p) // #nosec G304 -- p is built from a validated object key
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		http.Error(w, "unreadable", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	ct := mime.TypeByExtension(path.Ext(key))
	if ct == "" {
		ct = "application/octet-stream"
	}
	h.Set("Content-Type", ct)
	h.Set("Cache-Control", "public, max-age=31536000, immutable")
	h.Set("ETag", `"`+path.Base(strings.TrimSuffix(key, path.Ext(key)))+`"`)
	h.Set("Access-Control-Allow-Origin", "*")
	// Media is user content: never let it run as a page of this origin.
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; sandbox")
	http.ServeContent(w, r, "", info.ModTime(), f)
}

func preflight(w http.ResponseWriter, _ *http.Request) {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Methods", "PUT")
	h.Set("Access-Control-Allow-Headers", "Content-Type")
	h.Set("Access-Control-Max-Age", "600")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Store) upload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	key := strings.TrimPrefix(r.URL.Path, "/upload/")
	size, err := signing.Verify(s.Keys(), http.MethodPut, key, r.URL.Query(), s.now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	p, err := s.filePath(key)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if size <= 0 || size > maxSize {
		http.Error(w, "invalid size", http.StatusBadRequest)
		return
	}
	if _, err := os.Stat(p); err == nil {
		// Content-addressed: the file is already here.
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.ContentLength >= 0 && r.ContentLength != size {
		http.Error(w, fmt.Sprintf("Content-Length %d does not match the signed size %d", r.ContentLength, size), http.StatusBadRequest)
		return
	}
	if err := s.write(p, key, size, r.Body); err != nil {
		if bad, ok := errors.AsType[*badUpload](err); ok {
			http.Error(w, bad.Error(), http.StatusBadRequest)
			return
		}
		s.Log.Error(err, "Upload failed", "key", key)
		http.Error(w, "storing the file failed", http.StatusInternalServerError)
		return
	}
	s.Log.Info("Stored file", "key", key, "size", size)
	w.WriteHeader(http.StatusCreated)
}

type badUpload struct{ msg string }

func (e *badUpload) Error() string { return e.msg }

// write streams body into a temporary file while hashing it, and moves it
// into place only if size and hash match.
func (s *Store) write(dst, key string, size int64, body io.Reader) error {
	wantHash, _ := signing.ParseObjectKey(key)
	tmp := filepath.Join(s.Dir, tmpDir)
	if err := os.MkdirAll(tmp, 0o750); err != nil {
		return err
	}
	f, err := os.CreateTemp(tmp, "upload-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()

	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(body, size+1))
	if err != nil {
		_ = f.Close()
		return &badUpload{"reading the upload failed: " + err.Error()}
	}
	if n != size {
		_ = f.Close()
		return &badUpload{fmt.Sprintf("got %d bytes, want %d", n, size)}
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != wantHash {
		_ = f.Close()
		return &badUpload{"sha256 " + got + " does not match the object key"}
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	return os.Rename(f.Name(), dst)
}

func (s *Store) internalKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := strings.TrimPrefix(r.URL.Path, "/objects/")
	if _, err := signing.Verify(s.Keys(), r.Method, key, r.URL.Query(), s.now()); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return "", false
	}
	p, err := s.filePath(key)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return "", false
	}
	return p, true
}

func (s *Store) exists(w http.ResponseWriter, r *http.Request) {
	p, ok := s.internalKey(w, r)
	if !ok {
		return
	}
	info, err := os.Stat(p)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.WriteHeader(http.StatusOK)
}

func (s *Store) delete(w http.ResponseWriter, r *http.Request) {
	p, ok := s.internalKey(w, r)
	if !ok {
		return
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.Log.Error(err, "Delete failed", "path", p)
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// CleanTemp removes unfinished uploads older than age.
func (s *Store) CleanTemp(age time.Duration) {
	entries, err := os.ReadDir(filepath.Join(s.Dir, tmpDir))
	if err != nil {
		return
	}
	for _, e := range entries {
		info, err := e.Info()
		if err == nil && s.now().Sub(info.ModTime()) > age {
			_ = os.Remove(filepath.Join(s.Dir, tmpDir, e.Name()))
		}
	}
}
