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

// Server serves the latest Snapshot.
type Server struct {
	snap atomic.Pointer[Snapshot]
}

// Store replaces the snapshot being served.
func (s *Server) Store(snap *Snapshot) { s.snap.Store(snap) }

// Snapshot returns the snapshot being served, or nil before the first build.
func (s *Server) Snapshot() *Snapshot { return s.snap.Load() }

// ServeHTTP answers /healthz, /readyz, and the pages of the Gazette whose
// domain matches the Host header: / and /posts/{name}.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	st := snap.sites[domain.Normalize(r.Host)]
	if st == nil {
		http.NotFound(w, r)
		return
	}

	var e entry
	switch {
	case r.URL.Path == "/":
		e = st.index
	case strings.HasPrefix(r.URL.Path, "/posts/"):
		name := strings.TrimPrefix(r.URL.Path, "/posts/")
		var ok bool
		if e, ok = st.posts[name]; !ok {
			e = entry{status: http.StatusNotFound}
		}
	default:
		e = entry{status: http.StatusNotFound}
	}
	serveEntry(w, r, e)
}

func serveEntry(w http.ResponseWriter, r *http.Request, e entry) {
	if e.status != http.StatusOK || e.page == nil {
		status := e.status
		if status == http.StatusOK {
			status = http.StatusServiceUnavailable
		}
		http.Error(w, http.StatusText(status), status)
		return
	}
	h := w.Header()
	h.Set("ETag", e.page.ETag)
	h.Set("Cache-Control", "no-cache")
	if match := r.Header.Get("If-None-Match"); match != "" && etagMatches(match, e.page.ETag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Type", "text/html; charset=utf-8")
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
