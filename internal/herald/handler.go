/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

// Package herald implements the visitor-facing site.
package herald

import "net/http"

// NewHandler returns herald's HTTP handler.
//
// TODO(M1): watch Gazettes and Dispatches with informers, find the Gazette by
// Host, pre-render pages into an in-memory cache and serve them with ETags.
func NewHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "herald: no content yet", http.StatusServiceUnavailable)
	})
	return mux
}
