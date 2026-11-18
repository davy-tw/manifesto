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
		ObjectMeta: metav1.ObjectMeta{Name: "alice-notes"},
		Spec:       v1.GazetteSpec{Title: "Alice's Notes", Tagline: "寫給未來的自己", Domain: "alice.example.io", Language: "zh-TW"},
	}
}

func testDispatch(name, title, body string) *v1.Dispatch {
	at := metav1.NewTime(time.Date(2026, 11, 20, 1, 0, 0, 0, time.UTC))
	return &v1.Dispatch{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       v1.DispatchSpec{Title: title, Summary: "summary of " + name, Content: v1.DispatchContent{Body: body}},
		Status:     v1.DispatchStatus{PublishedAt: &at},
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
			out, err := DispatchPage(DefaultTheme(), testGazette(), tt.dispatch)
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
	if _, err := DispatchPage(DefaultTheme(), nil, testDispatch("a", "A", "x")); err != nil {
		t.Fatalf("DispatchPage(nil gazette) error = %v", err)
	}
}

func TestIndexPage(t *testing.T) {
	ds := []*v1.Dispatch{testDispatch("second", "Second", ""), testDispatch("first", "First", "")}
	out, err := IndexPage(DefaultTheme(), testGazette(), ds)
	if err != nil {
		t.Fatalf("IndexPage() error = %v", err)
	}
	s := string(out)
	i, j := strings.Index(s, `href="/posts/second"`), strings.Index(s, `href="/posts/first"`)
	if i < 0 || j < 0 || i > j {
		t.Errorf("index must list both dispatches in the given order\n%s", s)
	}
	empty, err := IndexPage(DefaultTheme(), testGazette(), nil)
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
	_, err = DispatchPage(th, testGazette(), testDispatch("a", "A", ""))
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
