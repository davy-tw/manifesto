/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

// Package herald implements the visitor-facing site. It reads Gazettes and
// Dispatches through informers, pre-renders every visible page in memory and
// serves them by Host header. It never writes to the Kubernetes API.
package herald

import (
	"net/http"
	"strings"
	"sync/atomic"

	"go.pkgs.davy.tw/manifesto/internal/domain"
)

// DefaultCacheControl lets browsers and shared caches store pages but
// revalidate them on every request; the ETag makes that a cheap 304.
const DefaultCacheControl = "no-cache"

// Server serves the latest Snapshot.
type Server struct {
	// CacheControl is sent with every page, 404 and 410; empty means
	// DefaultCacheControl. Pages carry an ETag computed from their content,
	// so every replica answers conditional requests alike and a CDN can
	// revalidate against any of them.
	CacheControl string

	snap atomic.Pointer[Snapshot]
}

func (s *Server) cacheControl() string {
	if s.CacheControl == "" {
		return DefaultCacheControl
	}
	return s.CacheControl
}

// Store replaces the snapshot being served.
func (s *Server) Store(snap *Snapshot) { s.snap.Store(snap) }

// Snapshot returns the snapshot being served, or nil before the first build.
func (s *Server) Snapshot() *Snapshot { return s.snap.Load() }

// ServeHTTP answers /healthz, /readyz, and the pre-rendered pages of the
// Gazette whose domain matches the Host header: /, /posts/{name},
// /sections/{name}, /stamps/{name}, /scribes/{name} (each with /page/{n}),
// /wire.xml and /sitemap.xml.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/healthz", "/readyz":
		w.Header().Set("Cache-Control", "no-store")
	}
	switch r.URL.Path {
	case "/healthz":
		_, _ = w.Write([]byte("ok"))
		return
	case "/readyz":
		if s.Snapshot() == nil {
			http.Error(w, "not synced yet", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok"))
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}

	snap := s.Snapshot()
	if snap == nil {
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	st := snap.sites[domain.Normalize(r.Host)]
	if st == nil {
		// Not cached: the domain may get a Gazette any moment.
		w.Header().Set("Cache-Control", "no-store")
		http.NotFound(w, r)
		return
	}

	e, ok := st.pages[r.URL.Path]
	if !ok {
		e = entry{status: http.StatusNotFound}
	}
	serveEntry(w, r, e, s.cacheControl())
}

func serveEntry(w http.ResponseWriter, r *http.Request, e entry, cacheControl string) {
	h := w.Header()
	if e.status != http.StatusOK || e.page == nil {
		status := e.status
		if status == http.StatusOK {
			status = http.StatusServiceUnavailable
		}
		if status == http.StatusServiceUnavailable {
			// A failed render may succeed on the next build.
			h.Set("Cache-Control", "no-store")
		} else {
			h.Set("Cache-Control", cacheControl)
		}
		http.Error(w, http.StatusText(status), status)
		return
	}
	h.Set("ETag", e.page.ETag)
	h.Set("Cache-Control", cacheControl)
	if match := r.Header.Get("If-None-Match"); match != "" && etagMatches(match, e.page.ETag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Type", e.page.ContentType)
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(e.page.Body)
	}
}

// etagMatches implements If-None-Match's weak comparison over a list of tags.
func etagMatches(header, etag string) bool {
	for t := range strings.SplitSeq(header, ",") {
		t = strings.TrimPrefix(strings.TrimSpace(t), "W/")
		if t == "*" || t == etag {
			return true
		}
	}
	return false
}
