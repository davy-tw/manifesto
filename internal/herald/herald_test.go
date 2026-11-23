/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package herald

import (
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
	"go.pkgs.davy.tw/manifesto/internal/render"
)

const host = "alice.example.io"

var t0 = time.Date(2026, 11, 20, 1, 0, 0, 0, time.UTC)

func gazette() v1.Gazette {
	return v1.Gazette{
		Namespace: "blog-alice", Name: "notes", UID: "g-uid", ResourceVersion: "1",
		CreationTimestamp: metav1.NewTime(t0),
		Spec:              v1.GazetteSpec{Title: "Alice's Notes", Domain: host},
	}
}

func dispatch(name string, phase v1.DispatchPhase, publishedAt time.Time) v1.Dispatch {
	d := v1.Dispatch{
		Namespace: "blog-alice", Name: name, UID: types.UID(name + "-uid"), ResourceVersion: "1",
		Spec: v1.DispatchSpec{GazetteRef: "notes", Title: "Title of " + name,
			Content: v1.DispatchContent{Body: "body of " + name}},
		Status: v1.DispatchStatus{Phase: phase},
	}
	if !publishedAt.IsZero() {
		at := metav1.NewTime(publishedAt)
		d.Status.PublishedAt = &at
	}
	return d
}

func newBuilder(th *render.Theme) *Builder { return &Builder{Theme: th, Log: logr.Discard()} }

// get serves one request against snap and returns the response.
func get(t *testing.T, snap *Snapshot, h, path string, header http.Header) *http.Response {
	t.Helper()
	srv := &Server{}
	srv.Store(snap)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Host = h
	maps.Copy(req.Header, header)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec.Result()
}

func body(t *testing.T, r *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestStatusCodes(t *testing.T) {
	ds := []v1.Dispatch{
		dispatch("published", v1.PhasePublished, t0),
		dispatch("unlisted", v1.PhaseUnlisted, t0),
		dispatch("draft", v1.PhaseDraft, time.Time{}),
		dispatch("embargoed", v1.PhaseEmbargoed, time.Time{}),
		dispatch("retracted", v1.PhaseRetracted, t0),
		dispatch("unreconciled", "", time.Time{}),
	}
	snap := newBuilder(render.DefaultTheme()).Build(Inputs{Gazettes: []v1.Gazette{gazette()}, Dispatches: ds}, nil)

	tests := []struct {
		host, path string
		want       int
	}{
		{host, "/", http.StatusOK},
		{host, "/posts/published", http.StatusOK},
		{host, "/posts/unlisted", http.StatusOK},
		{host, "/posts/draft", http.StatusNotFound},
		{host, "/posts/embargoed", http.StatusNotFound},
		{host, "/posts/unreconciled", http.StatusNotFound},
		{host, "/posts/missing", http.StatusNotFound},
		{host, "/posts/retracted", http.StatusGone},
		{host, "/elsewhere", http.StatusNotFound},
		{"ALICE.example.io:8080", "/posts/published", http.StatusOK},
		{"other.example.io", "/", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.host+tt.path, func(t *testing.T) {
			if got := get(t, snap, tt.host, tt.path, nil).StatusCode; got != tt.want {
				t.Errorf("status = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestIndexListsOnlyPublishedNewestFirst(t *testing.T) {
	ds := []v1.Dispatch{
		dispatch("old", v1.PhasePublished, t0),
		dispatch("new", v1.PhasePublished, t0.Add(time.Hour)),
		dispatch("unlisted", v1.PhaseUnlisted, t0.Add(2*time.Hour)),
		dispatch("draft", v1.PhaseDraft, time.Time{}),
	}
	snap := newBuilder(render.DefaultTheme()).Build(Inputs{Gazettes: []v1.Gazette{gazette()}, Dispatches: ds}, nil)
	page := body(t, get(t, snap, host, "/", nil))
	iNew, iOld := strings.Index(page, "/posts/new"), strings.Index(page, "/posts/old")
	if iNew < 0 || iOld < 0 || iNew > iOld {
		t.Errorf("index must list new before old:\n%s", page)
	}
	for _, hidden := range []string{"/posts/unlisted", "/posts/draft"} {
		if strings.Contains(page, hidden) {
			t.Errorf("index must not list %s", hidden)
		}
	}
}

func TestDispatchPageContent(t *testing.T) {
	snap := newBuilder(render.DefaultTheme()).Build(Inputs{Gazettes: []v1.Gazette{gazette()},
		Dispatches: []v1.Dispatch{dispatch("hello", v1.PhasePublished, t0)}}, nil)
	r := get(t, snap, host, "/posts/hello", nil)
	if ct := r.Header.Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	page := body(t, r)
	for _, s := range []string{"Title of hello", "<p>body of hello</p>", "Alice&#39;s Notes"} {
		if !strings.Contains(page, s) {
			t.Errorf("page does not contain %q", s)
		}
	}
}

func TestDispatchOfAnotherGazetteIsNotServed(t *testing.T) {
	d := dispatch("elsewhere", v1.PhasePublished, t0)
	d.Spec.GazetteRef = "other"
	snap := newBuilder(render.DefaultTheme()).Build(Inputs{Gazettes: []v1.Gazette{gazette()}, Dispatches: []v1.Dispatch{d}}, nil)
	if got := get(t, snap, host, "/posts/elsewhere", nil).StatusCode; got != http.StatusNotFound {
		t.Errorf("status = %d, want 404", got)
	}
}

func TestDomainConflictServesOldestGazette(t *testing.T) {
	older := gazette()
	newer := gazette()
	newer.Namespace, newer.UID = "blog-mallory", "g2-uid"
	newer.CreationTimestamp = metav1.NewTime(t0.Add(time.Minute))
	d := dispatch("mallory", v1.PhasePublished, t0)
	d.Namespace = "blog-mallory"
	snap := newBuilder(render.DefaultTheme()).Build(Inputs{Gazettes: []v1.Gazette{newer, older}, Dispatches: []v1.Dispatch{d}}, nil)
	if got := get(t, snap, host, "/posts/mallory", nil).StatusCode; got != http.StatusNotFound {
		t.Errorf("newer Gazette's Dispatch status = %d, want 404", got)
	}
}

func TestETag(t *testing.T) {
	b := newBuilder(render.DefaultTheme())
	g := gazette()
	d := dispatch("hello", v1.PhasePublished, t0)
	snap := b.Build(Inputs{Gazettes: []v1.Gazette{g}, Dispatches: []v1.Dispatch{d}}, nil)

	r := get(t, snap, host, "/posts/hello", nil)
	etag := r.Header.Get("ETag")
	if etag == "" || r.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("ETag = %q, Cache-Control = %q", etag, r.Header.Get("Cache-Control"))
	}
	for _, inm := range []string{etag, `"x", ` + etag, "W/" + etag, "*"} {
		if got := get(t, snap, host, "/posts/hello", http.Header{"If-None-Match": {inm}}).StatusCode; got != http.StatusNotModified {
			t.Errorf("If-None-Match %s: status = %d, want 304", inm, got)
		}
	}
	if got := get(t, snap, host, "/posts/hello", http.Header{"If-None-Match": {`"stale"`}}).StatusCode; got != http.StatusOK {
		t.Errorf("stale If-None-Match: status = %d, want 200", got)
	}

	// Changing the Gazette (its title is on every page) changes the ETag.
	g.ResourceVersion = "2"
	g.Spec.Title = "Renamed"
	snap2 := b.Build(Inputs{Gazettes: []v1.Gazette{g}, Dispatches: []v1.Dispatch{d}}, snap)
	r2 := get(t, snap2, host, "/posts/hello", nil)
	if r2.Header.Get("ETag") == etag || !strings.Contains(body(t, r2), "Renamed") {
		t.Errorf("Gazette change did not produce a new page")
	}
}

func TestCacheControl(t *testing.T) {
	const cdn = "public, max-age=0, s-maxage=300, stale-if-error=86400"
	snap := newBuilder(render.DefaultTheme()).Build(Inputs{Gazettes: []v1.Gazette{gazette()}, Dispatches: []v1.Dispatch{
		dispatch("hello", v1.PhasePublished, t0), dispatch("gone", v1.PhaseRetracted, t0),
	}}, nil)
	srv := &Server{CacheControl: cdn}
	do := func(h, path string) *http.Response {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = h
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec.Result()
	}
	if got := do(host, "/").Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("before the first build: Cache-Control = %q, want no-store", got)
	}
	srv.Store(snap)
	tests := []struct {
		host, path string
		status     int
		want       string
	}{
		{host, "/", http.StatusOK, cdn},
		{host, "/posts/hello", http.StatusOK, cdn},
		{host, "/wire.xml", http.StatusOK, cdn},
		{host, "/posts/missing", http.StatusNotFound, cdn},
		{host, "/posts/gone", http.StatusGone, cdn},
		{"unknown.example.io", "/", http.StatusNotFound, "no-store"},
		{host, "/healthz", http.StatusOK, "no-store"},
	}
	for _, tt := range tests {
		r := do(tt.host, tt.path)
		if r.StatusCode != tt.status || r.Header.Get("Cache-Control") != tt.want {
			t.Errorf("%s%s = %d, Cache-Control %q; want %d, %q", tt.host, tt.path, r.StatusCode, r.Header.Get("Cache-Control"), tt.status, tt.want)
		}
	}
	if got := get(t, snap, host, "/", nil).Header.Get("Cache-Control"); got != DefaultCacheControl {
		t.Errorf("default Cache-Control = %q", got)
	}

	// A 304 repeats the caching headers.
	etag := do(host, "/posts/hello").Header.Get("ETag")
	req := httptest.NewRequest(http.MethodGet, "/posts/hello", nil)
	req.Host, req.Header["If-None-Match"] = host, []string{etag}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if r := rec.Result(); r.StatusCode != http.StatusNotModified || r.Header.Get("Cache-Control") != cdn || r.Header.Get("ETag") != etag {
		t.Errorf("304 = %d, Cache-Control %q, ETag %q", r.StatusCode, r.Header.Get("Cache-Control"), r.Header.Get("ETag"))
	}

	// A page that never rendered (no theme) must not stick in a cache.
	srv.Store(newBuilder(nil).Build(Inputs{Gazettes: []v1.Gazette{gazette()}, Dispatches: []v1.Dispatch{
		dispatch("hello", v1.PhasePublished, t0),
	}}, nil))
	if r := do(host, "/posts/hello"); r.StatusCode != http.StatusServiceUnavailable || r.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("never rendered = %d, Cache-Control %q; want 503, no-store", r.StatusCode, r.Header.Get("Cache-Control"))
	}
}

// Replicas build their snapshots independently; a CDN revalidating against
// any of them must get the same ETag for the same page.
func TestETagIsTheSameOnEveryReplica(t *testing.T) {
	in := Inputs{Gazettes: []v1.Gazette{gazette()}, Dispatches: []v1.Dispatch{dispatch("hello", v1.PhasePublished, t0)}}
	a := newBuilder(render.DefaultTheme()).Build(in, nil)
	// The other replica saw an earlier version first.
	older := in
	older.Dispatches = []v1.Dispatch{dispatch("hello", v1.PhaseDraft, time.Time{})}
	b := newBuilder(render.DefaultTheme())
	bSnap := b.Build(in, b.Build(older, nil))
	for _, path := range []string{"/", "/posts/hello", "/wire.xml", "/sitemap.xml"} {
		ea, eb := get(t, a, host, path, nil).Header.Get("ETag"), get(t, bSnap, host, path, nil).Header.Get("ETag")
		if ea == "" || ea != eb {
			t.Errorf("%s: ETags %q and %q", path, ea, eb)
		}
	}
}

func TestUnchangedPagesAreReused(t *testing.T) {
	b := newBuilder(render.DefaultTheme())
	ds := []v1.Dispatch{dispatch("a", v1.PhasePublished, t0), dispatch("b", v1.PhasePublished, t0)}
	snap := b.Build(Inputs{Gazettes: []v1.Gazette{gazette()}, Dispatches: ds}, nil)

	ds[1].ResourceVersion = "2"
	ds[1].Spec.Title = "B, edited"
	snap2 := b.Build(Inputs{Gazettes: []v1.Gazette{gazette()}, Dispatches: ds}, snap)

	site1, site2 := snap.sites[host], snap2.sites[host]
	if site1.pages["/posts/a"].page != site2.pages["/posts/a"].page {
		t.Error("unchanged Dispatch was re-rendered")
	}
	if site1.pages["/posts/b"].page == site2.pages["/posts/b"].page {
		t.Error("changed Dispatch was not re-rendered")
	}
	if site1.pages["/"].page == site2.pages["/"].page {
		t.Error("index was not re-rendered after a listed Dispatch changed")
	}
}

func TestRenderFailureKeepsLastGoodPage(t *testing.T) {
	broken, err := render.ParseTheme(map[string]string{
		render.LayoutTemplate:   `{{ asset "missing.css" }}{{ block "content" . }}{{ end }}`,
		render.DispatchTemplate: `{{ define "content" }}x{{ end }}`,
		render.IndexTemplate:    `{{ define "content" }}x{{ end }}`,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	g := gazette()
	d := dispatch("hello", v1.PhasePublished, t0)
	good := newBuilder(render.DefaultTheme()).Build(Inputs{Gazettes: []v1.Gazette{g}, Dispatches: []v1.Dispatch{d}}, nil)

	// The Dispatch changes, but rendering now fails: the old page stays.
	d.ResourceVersion = "2"
	snap := newBuilder(broken).Build(Inputs{Gazettes: []v1.Gazette{g}, Dispatches: []v1.Dispatch{d}}, good)
	if r := get(t, snap, host, "/posts/hello", nil); r.StatusCode != http.StatusOK || !strings.Contains(body(t, r), "Title of hello") {
		t.Errorf("want the last good page, got status %d", r.StatusCode)
	}

	// A page that never rendered answers 503.
	fresh := newBuilder(broken).Build(Inputs{Gazettes: []v1.Gazette{g}, Dispatches: []v1.Dispatch{d}}, nil)
	for _, p := range []string{"/", "/posts/hello"} {
		if got := get(t, fresh, host, p, nil).StatusCode; got != http.StatusServiceUnavailable {
			t.Errorf("%s: status = %d, want 503", p, got)
		}
	}
}

func TestHealthAndMethods(t *testing.T) {
	srv := &Server{}
	do := func(method, path string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, nil)
		req.Host = host
		srv.ServeHTTP(rec, req)
		return rec.Code
	}
	if got := do(http.MethodGet, "/healthz"); got != http.StatusOK {
		t.Errorf("/healthz = %d", got)
	}
	if got := do(http.MethodGet, "/readyz"); got != http.StatusServiceUnavailable {
		t.Errorf("/readyz before first build = %d, want 503", got)
	}
	if got := do(http.MethodGet, "/"); got != http.StatusServiceUnavailable {
		t.Errorf("/ before first build = %d, want 503", got)
	}
	srv.Store(newBuilder(render.DefaultTheme()).Build(Inputs{Gazettes: []v1.Gazette{gazette()}}, nil))
	if got := do(http.MethodGet, "/readyz"); got != http.StatusOK {
		t.Errorf("/readyz after first build = %d", got)
	}
	if got := do(http.MethodPost, "/"); got != http.StatusMethodNotAllowed {
		t.Errorf("POST / = %d, want 405", got)
	}
	if got := do(http.MethodHead, "/"); got != http.StatusOK {
		t.Errorf("HEAD / = %d, want 200", got)
	}
}
