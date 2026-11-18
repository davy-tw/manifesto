/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package herald

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
	"go.pkgs.davy.tw/manifesto/internal/render"
)

func labelled(d v1.Dispatch, section string, stamps ...string) v1.Dispatch {
	d.Labels = map[string]string{}
	if section != "" {
		d.Labels[v1.SectionLabel] = section
	}
	for _, s := range stamps {
		d.Labels[v1.StampLabelPrefix+"/"+s] = ""
	}
	return d
}

func meta(name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Namespace: "blog-alice", Name: name, ResourceVersion: "1", UID: types.UID("uid-" + name)}
}

func TestPagination(t *testing.T) {
	g := gazette()
	g.Spec.PageSize = 2
	ds := make([]v1.Dispatch, 0, 5)
	for i := range 5 {
		ds = append(ds, dispatch(fmt.Sprintf("post-%d", i), v1.PhasePublished, t0.Add(time.Duration(i)*time.Hour)))
	}
	snap := newBuilder(render.DefaultTheme()).Build(Inputs{Gazettes: []v1.Gazette{g}, Dispatches: ds}, nil)

	for path, want := range map[string]int{"/": 200, "/page/2": 200, "/page/3": 200, "/page/4": 404, "/page/1": 404} {
		if got := get(t, snap, host, path, nil).StatusCode; got != want {
			t.Errorf("%s: status = %d, want %d", path, got, want)
		}
	}
	first := body(t, get(t, snap, host, "/", nil))
	if !strings.Contains(first, "/posts/post-4") || !strings.Contains(first, "/posts/post-3") || strings.Contains(first, "/posts/post-2") {
		t.Errorf("page 1 must hold the two newest Dispatches:\n%s", first)
	}
	if !strings.Contains(first, `href="/page/2"`) || !strings.Contains(first, "1 / 3") {
		t.Errorf("page 1 must link to page 2")
	}
	last := body(t, get(t, snap, host, "/page/3", nil))
	if !strings.Contains(last, "/posts/post-0") || !strings.Contains(last, `href="/page/2"`) || strings.Contains(last, `rel="next"`) {
		t.Errorf("page 3 must hold the oldest Dispatch and link back only:\n%s", last)
	}
}

func TestTaxonomyPages(t *testing.T) {
	in := Inputs{
		Gazettes: []v1.Gazette{gazette()},
		Dispatches: []v1.Dispatch{
			labelled(dispatch("go-generics", v1.PhasePublished, t0), "go", "golang"),
			labelled(dispatch("k8s-intro", v1.PhasePublished, t0.Add(time.Hour)), "tech", "kubernetes"),
			labelled(dispatch("secret", v1.PhaseUnlisted, t0), "tech", "kubernetes"),
			labelled(dispatch("cooking", v1.PhasePublished, t0), "life"),
		},
		Sections: []v1.Section{
			{ObjectMeta: meta("tech"), Spec: v1.SectionSpec{DisplayName: "技術"}},
			{ObjectMeta: meta("go"), Spec: v1.SectionSpec{DisplayName: "Go", Parent: "tech"}},
			{ObjectMeta: meta("empty"), Spec: v1.SectionSpec{DisplayName: "Empty"}},
		},
		Stamps:  []v1.Stamp{{ObjectMeta: meta("kubernetes"), Spec: v1.StampSpec{DisplayName: "Kubernetes"}}},
		Scribes: []v1.Scribe{{ObjectMeta: meta("alice"), Spec: v1.ScribeSpec{DisplayName: "Alice", Bio: "Writes about K8s"}}},
	}
	in.Dispatches[0].Spec.ScribeRef = "alice"
	snap := newBuilder(render.DefaultTheme()).Build(in, nil)

	tests := []struct {
		path        string
		status      int
		contains    []string
		notContains []string
	}{
		{"/sections/tech", 200, []string{"<h1>技術</h1>", "/posts/k8s-intro", "/posts/go-generics"}, []string{"/posts/cooking", "/posts/secret"}},
		{"/sections/go", 200, []string{"<h1>Go</h1>", "/posts/go-generics"}, []string{"/posts/k8s-intro"}},
		{"/sections/life", 200, []string{"<h1>life</h1>", "/posts/cooking"}, nil},
		{"/sections/empty", 200, []string{"No dispatches yet."}, nil},
		{"/sections/nowhere", 404, nil, nil},
		{"/stamps/kubernetes", 200, []string{"<h1>Kubernetes</h1>", "/posts/k8s-intro"}, []string{"/posts/secret"}},
		{"/stamps/golang", 200, []string{"<h1>golang</h1>"}, nil},
		{"/scribes/alice", 200, []string{"<h1>Alice</h1>", "Writes about K8s", "/posts/go-generics"}, nil},
		{"/posts/go-generics", 200, []string{`href="/sections/go">Go</a>`, `href="/scribes/alice">Alice</a>`, `href="/stamps/golang">golang</a>`}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			r := get(t, snap, host, tt.path, nil)
			if r.StatusCode != tt.status {
				t.Fatalf("status = %d, want %d", r.StatusCode, tt.status)
			}
			b := body(t, r)
			for _, s := range tt.contains {
				if !strings.Contains(b, s) {
					t.Errorf("does not contain %q\n%s", s, b)
				}
			}
			for _, s := range tt.notContains {
				if strings.Contains(b, s) {
					t.Errorf("must not contain %q", s)
				}
			}
		})
	}
}

func TestSectionCycleDoesNotHang(t *testing.T) {
	in := Inputs{
		Gazettes:   []v1.Gazette{gazette()},
		Dispatches: []v1.Dispatch{labelled(dispatch("a-post", v1.PhasePublished, t0), "a")},
		Sections: []v1.Section{
			{ObjectMeta: meta("a"), Spec: v1.SectionSpec{Parent: "b"}},
			{ObjectMeta: meta("b"), Spec: v1.SectionSpec{Parent: "a"}},
		},
	}
	snap := newBuilder(render.DefaultTheme()).Build(in, nil)
	for _, p := range []string{"/sections/a", "/sections/b"} {
		if !strings.Contains(body(t, get(t, snap, host, p, nil)), "/posts/a-post") {
			t.Errorf("%s must list a-post", p)
		}
	}
}

func TestWire(t *testing.T) {
	in := Inputs{
		Gazettes: []v1.Gazette{gazette()},
		Dispatches: []v1.Dispatch{
			labelled(dispatch("hello", v1.PhasePublished, t0), "tech", "kubernetes"),
			dispatch("hidden", v1.PhaseUnlisted, t0),
			dispatch("draft", v1.PhaseDraft, time.Time{}),
		},
		Sections: []v1.Section{{ObjectMeta: meta("tech"), Spec: v1.SectionSpec{DisplayName: "技術"}}},
		Scribes:  []v1.Scribe{{ObjectMeta: meta("alice"), Spec: v1.ScribeSpec{DisplayName: "Alice"}}},
	}
	in.Dispatches[0].Spec.ScribeRef = "alice"
	for i := range wireItems + 5 {
		in.Dispatches = append(in.Dispatches, dispatch(fmt.Sprintf("old-%02d", i), v1.PhasePublished, t0.Add(-time.Duration(i+1)*time.Hour)))
	}
	snap := newBuilder(render.DefaultTheme()).Build(in, nil)
	r := get(t, snap, host, "/wire.xml", nil)
	if r.StatusCode != http.StatusOK || r.Header.Get("Content-Type") != contentTypeRSS {
		t.Fatalf("status %d, Content-Type %q", r.StatusCode, r.Header.Get("Content-Type"))
	}
	var feed rss
	if err := xml.Unmarshal([]byte(body(t, r)), &feed); err != nil {
		t.Fatal(err)
	}
	if feed.Channel.Title != "Alice's Notes" || feed.Channel.Link != "https://"+host+"/" {
		t.Errorf("channel = %+v", feed.Channel)
	}
	if len(feed.Channel.Items) != wireItems {
		t.Fatalf("%d items, want %d", len(feed.Channel.Items), wireItems)
	}
	first := feed.Channel.Items[0]
	if first.Link != "https://"+host+"/posts/hello" || first.PubDate != "Fri, 20 Nov 2026 01:00:00 +0000" {
		t.Errorf("first item = %+v", first)
	}
	if raw := body(t, get(t, snap, host, "/wire.xml", nil)); !strings.Contains(raw, "<dc:creator>Alice</dc:creator>") ||
		!strings.Contains(raw, `xmlns:dc="http://purl.org/dc/elements/1.1/"`) {
		t.Errorf("feed must credit the author with dc:creator:\n%s", raw)
	}
	if strings.Join(first.Categories, ",") != "技術,kubernetes" {
		t.Errorf("categories = %v", first.Categories)
	}
	for _, it := range feed.Channel.Items {
		if strings.Contains(it.Link, "hidden") || strings.Contains(it.Link, "draft") {
			t.Errorf("feed must not include %s", it.Link)
		}
	}
}

func TestSitemap(t *testing.T) {
	in := Inputs{
		Gazettes: []v1.Gazette{gazette()},
		Dispatches: []v1.Dispatch{
			labelled(dispatch("hello", v1.PhasePublished, t0), "tech"),
			dispatch("hidden", v1.PhaseUnlisted, t0),
		},
	}
	snap := newBuilder(render.DefaultTheme()).Build(in, nil)
	r := get(t, snap, host, "/sitemap.xml", nil)
	if r.Header.Get("Content-Type") != contentTypeXML {
		t.Errorf("Content-Type = %q", r.Header.Get("Content-Type"))
	}
	var m urlset
	if err := xml.Unmarshal([]byte(body(t, r)), &m); err != nil {
		t.Fatal(err)
	}
	locs := make([]string, 0, len(m.URLs))
	for _, u := range m.URLs {
		locs = append(locs, u.Loc)
	}
	want := "https://alice.example.io/,https://alice.example.io/sections/tech,https://alice.example.io/posts/hello"
	if strings.Join(locs, ",") != want {
		t.Errorf("sitemap = %v, want %s", locs, want)
	}
}

func clusterTypeset(name, rv, dispatchTemplate string) v1.ClusterTypeset {
	return v1.ClusterTypeset{
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: "ts-uid", ResourceVersion: rv},
		Spec: v1.TypesetSpec{
			Params: map[string]string{"color": "blue", "font": "serif"},
			Source: v1.TypesetSource{Inline: &v1.InlineTypesetSource{Templates: map[string]string{
				render.LayoutTemplate:   `<main>{{ block "content" . }}{{ end }}</main>`,
				render.DispatchTemplate: dispatchTemplate,
				render.IndexTemplate:    `{{ define "content" }}index{{ end }}`,
			}}},
		},
	}
}

func TestTypeset(t *testing.T) {
	g := gazette()
	g.Spec.TypesetRef = &v1.TypesetRef{Kind: v1.KindClusterTypeset, Name: "fancy"}
	g.Spec.TypesetParams = map[string]string{"color": "red"}
	ds := []v1.Dispatch{dispatch("hello", v1.PhasePublished, t0)}
	b := newBuilder(render.DefaultTheme())

	good := clusterTypeset("fancy", "1", `{{ define "content" }}v1 {{ .Dispatch.Title }} {{ .Params.color }} {{ .Params.font }}{{ end }}`)
	snap := b.Build(Inputs{Gazettes: []v1.Gazette{g}, Dispatches: ds, ClusterTypesets: []v1.ClusterTypeset{good}}, nil)
	if got := body(t, get(t, snap, host, "/posts/hello", nil)); got != "<main>v1 Title of hello red serif</main>" {
		t.Fatalf("page = %q", got)
	}

	By := func(string) {}
	By("breaking the theme keeps its last good version")
	broken := clusterTypeset("fancy", "2", `{{ define "content" }}{{ .Dispatch.Title }`)
	ds[0].ResourceVersion = "2"
	snap2 := b.Build(Inputs{Gazettes: []v1.Gazette{g}, Dispatches: ds, ClusterTypesets: []v1.ClusterTypeset{broken}}, snap)
	if got := body(t, get(t, snap2, host, "/posts/hello", nil)); !strings.HasPrefix(got, "<main>v1 ") {
		t.Errorf("page = %q, want the last good theme", got)
	}

	By("a theme that never parsed answers 503 for pages, but feeds still work")
	fresh := b.Build(Inputs{Gazettes: []v1.Gazette{g}, Dispatches: ds, ClusterTypesets: []v1.ClusterTypeset{broken}}, nil)
	for path, want := range map[string]int{"/posts/hello": 503, "/": 503, "/wire.xml": 200, "/sitemap.xml": 200} {
		if got := get(t, fresh, host, path, nil).StatusCode; got != want {
			t.Errorf("%s: status = %d, want %d", path, got, want)
		}
	}

	By("a missing theme falls back to the built-in one")
	missing := b.Build(Inputs{Gazettes: []v1.Gazette{g}, Dispatches: ds}, nil)
	if got := body(t, get(t, missing, host, "/posts/hello", nil)); !strings.Contains(got, "<!doctype html>") {
		t.Errorf("want the built-in theme, got %q", got)
	}
}

func TestTaxonomyChangeInvalidatesPages(t *testing.T) {
	b := newBuilder(render.DefaultTheme())
	ds := []v1.Dispatch{labelled(dispatch("hello", v1.PhasePublished, t0), "tech")}
	sec := v1.Section{ObjectMeta: meta("tech"), Spec: v1.SectionSpec{DisplayName: "Tech"}}
	snap := b.Build(Inputs{Gazettes: []v1.Gazette{gazette()}, Dispatches: ds, Sections: []v1.Section{sec}}, nil)

	sec.ResourceVersion, sec.Spec.DisplayName = "2", "技術"
	snap2 := b.Build(Inputs{Gazettes: []v1.Gazette{gazette()}, Dispatches: ds, Sections: []v1.Section{sec}}, snap)
	if !strings.Contains(body(t, get(t, snap2, host, "/posts/hello", nil)), ">技術</a>") {
		t.Error("renaming a Section did not re-render the Dispatch page")
	}
	if get(t, snap, host, "/posts/hello", nil).Header.Get("ETag") == get(t, snap2, host, "/posts/hello", nil).Header.Get("ETag") {
		t.Error("ETag did not change")
	}
}
