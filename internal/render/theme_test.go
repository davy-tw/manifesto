/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package render

import (
	"maps"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
)

func testGazette() *v1.Gazette {
	return &v1.Gazette{
		Name: "alice-notes",
		Spec: v1.GazetteSpec{Title: "Alice's Notes", Tagline: "寫給未來的自己", Domain: "alice.example.io", Language: "zh-TW"},
	}
}

func testDispatch(name, title, body string) *v1.Dispatch {
	at := metav1.NewTime(time.Date(2026, 11, 20, 1, 0, 0, 0, time.UTC))
	return &v1.Dispatch{
		Name:   name,
		Spec:   v1.DispatchSpec{Title: title, Summary: "summary of " + name, Content: v1.DispatchContent{Body: body}},
		Status: v1.DispatchStatus{PublishedAt: &at},
	}
}

func TestDispatchPage(t *testing.T) {
	tests := []struct {
		name        string
		dispatch    *v1.Dispatch
		contains    []string
		notContains []string
	}{
		{
			name:     "title, body, date and site chrome",
			dispatch: testDispatch("hello-k8s", "Hello K8s", "# Heading\n\ntext"),
			contains: []string{
				`<html lang="zh-TW">`, "<title>Hello K8s · Alice&#39;s Notes</title>",
				"<h1>Hello K8s</h1>", "<h1>Heading</h1>", "<p>text</p>",
				`datetime="2026-11-20T01:00:00Z"`, "寫給未來的自己", "max-width: 42rem",
			},
		},
		{
			name:        "title is escaped",
			dispatch:    testDispatch("xss", `<script>alert(1)</script>`, ""),
			contains:    []string{"&lt;script&gt;alert(1)&lt;/script&gt;"},
			notContains: []string{"<script>alert(1)"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := NewSite(DefaultTheme(), testGazette(), nil, Taxonomy{}).DispatchPage(tt.dispatch)
			if err != nil {
				t.Fatalf("DispatchPage() error = %v", err)
			}
			for _, s := range tt.contains {
				if !strings.Contains(string(out), s) {
					t.Errorf("page does not contain %q\n%s", s, out)
				}
			}
			for _, s := range tt.notContains {
				if strings.Contains(string(out), s) {
					t.Errorf("page must not contain %q", s)
				}
			}
		})
	}
}

func TestDispatchPageWithoutGazette(t *testing.T) {
	if _, err := NewSite(DefaultTheme(), nil, nil, Taxonomy{}).DispatchPage(testDispatch("a", "A", "x")); err != nil {
		t.Fatalf("DispatchPage(nil gazette) error = %v", err)
	}
}

func TestIndexPage(t *testing.T) {
	ds := []*v1.Dispatch{testDispatch("second", "Second", ""), testDispatch("first", "First", "")}
	out, err := NewSite(DefaultTheme(), testGazette(), nil, Taxonomy{}).ListPage(ds, nil, nil)
	if err != nil {
		t.Fatalf("IndexPage() error = %v", err)
	}
	s := string(out)
	i, j := strings.Index(s, `href="/posts/second"`), strings.Index(s, `href="/posts/first"`)
	if i < 0 || j < 0 || i > j {
		t.Errorf("index must list both dispatches in the given order\n%s", s)
	}
	empty, err := NewSite(DefaultTheme(), testGazette(), nil, Taxonomy{}).ListPage(nil, nil, nil)
	if err != nil || !strings.Contains(string(empty), "No dispatches yet.") {
		t.Errorf("empty index = %q, %v", empty, err)
	}
}

func TestParseThemeErrors(t *testing.T) {
	valid := map[string]string{
		LayoutTemplate:   `{{ block "content" . }}{{ end }}`,
		DispatchTemplate: `{{ define "content" }}{{ .Dispatch.Title }}{{ end }}`,
		IndexTemplate:    `{{ define "content" }}index{{ end }}`,
	}
	with := func(name, src string) map[string]string {
		m := maps.Clone(valid)
		if src == "" {
			delete(m, name)
		} else {
			m[name] = src
		}
		return m
	}
	tests := []struct {
		name      string
		templates map[string]string
	}{
		{"missing layout", with(LayoutTemplate, "")},
		{"missing page", with(IndexTemplate, "")},
		{"syntax error", with(DispatchTemplate, `{{ define "content" }}{{ .Dispatch.Title }`)},
		{"function outside the FuncMap", with(DispatchTemplate, `{{ define "content" }}{{ exec "ls" }}{{ end }}`)},
	}
	if _, err := ParseTheme(valid, nil); err != nil {
		t.Fatalf("ParseTheme(valid) error = %v", err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseTheme(tt.templates, nil)
			if err == nil {
				t.Fatal("ParseTheme() succeeded, want error")
			}
			if ReasonOf(err) != ReasonTemplateError {
				t.Errorf("reason = %q, want %q", ReasonOf(err), ReasonTemplateError)
			}
		})
	}
}

func TestRenderRuntimeError(t *testing.T) {
	th, err := ParseTheme(map[string]string{
		LayoutTemplate:   `<style>{{ asset "missing.css" }}</style>{{ block "content" . }}{{ end }}`,
		DispatchTemplate: `{{ define "content" }}x{{ end }}`,
		IndexTemplate:    `{{ define "content" }}x{{ end }}`,
	}, nil)
	if err != nil {
		t.Fatalf("ParseTheme() error = %v", err)
	}
	_, err = NewSite(th, testGazette(), nil, Taxonomy{}).DispatchPage(testDispatch("a", "A", ""))
	if ReasonOf(err) != ReasonTemplateError || !strings.Contains(err.Error(), "missing.css") {
		t.Errorf("error = %v, want a TemplateError about missing.css", err)
	}
}

func TestTruncate(t *testing.T) {
	trunc := funcMap(nil)["truncate"].(func(int, string) string)
	tests := []struct {
		n    int
		in   string
		want string
	}{
		{5, "hello", "hello"},
		{3, "hello", "hel…"},
		{2, "中文字", "中文…"},
	}
	for _, tt := range tests {
		if got := trunc(tt.n, tt.in); got != tt.want {
			t.Errorf("truncate(%d, %q) = %q, want %q", tt.n, tt.in, got, tt.want)
		}
	}
}

func TestTaxonomyAndParams(t *testing.T) {
	g := testGazette()
	g.Spec.TypesetParams = map[string]string{"accent": "red"}
	tax := Taxonomy{
		Sections: map[string]*v1.Section{"tech": {Spec: v1.SectionSpec{DisplayName: "技術"}}},
		Stamps:   map[string]*v1.Stamp{"k8s": {Spec: v1.StampSpec{DisplayName: "Kubernetes"}}},
		Scribes:  map[string]*v1.Scribe{"alice": {Spec: v1.ScribeSpec{DisplayName: "Alice"}}},
	}
	th, err := ParseTheme(map[string]string{
		LayoutTemplate: `{{ block "content" . }}{{ end }}`,
		DispatchTemplate: `{{ define "content" }}{{ .Params.accent }}|{{ .Params.font }}|` +
			`{{ .Dispatch.Section.DisplayName }}|{{ range .Dispatch.Stamps }}{{ .DisplayName }},{{ end }}|` +
			`{{ .Dispatch.Scribe.DisplayName }}{{ end }}`,
		IndexTemplate: `{{ define "content" }}{{ .Filter.DisplayName }} {{ .Pagination.Page }}/{{ .Pagination.Pages }}{{ end }}`,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	site := NewSite(th, g, map[string]string{"accent": "blue", "font": "serif"}, tax)

	d := testDispatch("hello", "Hello", "")
	d.Labels = map[string]string{v1.SectionLabel: "tech", v1.StampLabelPrefix + "/k8s": "", v1.StampLabelPrefix + "/plain": ""}
	d.Spec.ScribeRef = "alice"
	out, err := site.DispatchPage(d)
	if err != nil {
		t.Fatal(err)
	}
	if want := "red|serif|技術|Kubernetes,plain,|Alice"; string(out) != want {
		t.Errorf("page = %q, want %q", out, want)
	}

	out, err = site.ListPage(nil, &FilterView{DisplayName: "技術"}, &PaginationView{Page: 2, Pages: 3})
	if err != nil || string(out) != "技術 2/3" {
		t.Errorf("list page = %q, %v", out, err)
	}
}

func TestDefaultThemeShowsTaxonomyAndPagination(t *testing.T) {
	tax := Taxonomy{Sections: map[string]*v1.Section{"tech": {Spec: v1.SectionSpec{DisplayName: "技術", Description: "關於技術"}}}}
	site := NewSite(DefaultTheme(), testGazette(), nil, tax)
	d := testDispatch("hello", "Hello", "")
	d.Labels = map[string]string{v1.SectionLabel: "tech", v1.StampLabelPrefix + "/k8s": ""}
	page, err := site.DispatchPage(d)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{`href="/sections/tech">技術</a>`, `href="/stamps/k8s">k8s</a>`} {
		if !strings.Contains(string(page), s) {
			t.Errorf("dispatch page does not contain %q", s)
		}
	}
	filter := &FilterView{Kind: "section", Name: "tech", DisplayName: "技術", Description: "關於技術"}
	list, err := site.ListPage([]*v1.Dispatch{d}, filter, &PaginationView{Page: 2, Pages: 3, PrevURL: "/sections/tech", NextURL: "/sections/tech/page/3"})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"<h1>技術</h1>", "關於技術", `href="/sections/tech"`, `href="/sections/tech/page/3"`, "2 / 3"} {
		if !strings.Contains(string(list), s) {
			t.Errorf("list page does not contain %q", s)
		}
	}
}

func TestPagePath(t *testing.T) {
	tests := []struct {
		base string
		n    int
		want string
	}{
		{"/", 1, "/"},
		{"/", 2, "/page/2"},
		{"/sections/tech", 1, "/sections/tech"},
		{"/sections/tech", 3, "/sections/tech/page/3"},
	}
	for _, tt := range tests {
		if got := PagePath(tt.base, tt.n); got != tt.want {
			t.Errorf("PagePath(%q, %d) = %q, want %q", tt.base, tt.n, got, tt.want)
		}
	}
}

func TestParseTypeset(t *testing.T) {
	valid := map[string]string{
		LayoutTemplate:   `{{ block "content" . }}{{ end }}`,
		DispatchTemplate: `{{ define "content" }}d{{ end }}`,
		IndexTemplate:    `{{ define "content" }}i{{ end }}`,
	}
	inline := v1.TypesetSource{Inline: &v1.InlineTypesetSource{Templates: valid}}
	tests := []struct {
		name    string
		spec    v1.TypesetSpec
		wantErr bool
	}{
		{"v1", v1.TypesetSpec{ContractVersion: v1.ContractV1, Source: inline}, false},
		{"unset contract", v1.TypesetSpec{Source: inline}, false},
		{"unknown contract", v1.TypesetSpec{ContractVersion: "v2", Source: inline}, true},
		{"no source", v1.TypesetSpec{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseTypeset(&tt.spec)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && ReasonOf(err) != ReasonTemplateError {
				t.Errorf("reason = %q", ReasonOf(err))
			}
		})
	}
}
