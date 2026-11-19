/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package render

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
)

func readyPrint(url, alt string) *v1.Print {
	return &v1.Print{
		Spec:   v1.PrintSpec{Alt: alt},
		Status: v1.PrintStatus{Phase: v1.PrintReady, URL: url},
	}
}

func TestMarkdownPrintRefs(t *testing.T) {
	resolve := func(name string) string {
		if name == "hero" {
			return "https://media.example.io/u/sha256/abc.png"
		}
		return ""
	}
	tests := []struct {
		name        string
		src         string
		contains    []string
		notContains []string
	}{
		{"image", "![封面](print:hero)", []string{`<img src="https://media.example.io/u/sha256/abc.png" alt="封面"`}, []string{"print:"}},
		{"link", "[download](print:hero)", []string{`href="https://media.example.io/u/sha256/abc.png"`}, nil},
		{"unknown image is dropped", "before ![x](print:missing) after", []string{"<p>before  after</p>"}, []string{"print:", "<img"}},
		{"unknown link keeps its text", "see [the file](print:missing).", []string{"<p>see the file.</p>"}, []string{"<a", "print:"}},
		{"plain text untouched", "write print:hero in code", []string{"print:hero"}, nil},
		{"other links untouched", "[a](https://example.io/print:hero)", []string{`href="https://example.io/print:hero"`}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Markdown(tt.src, resolve)
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range tt.contains {
				if !strings.Contains(string(got), s) {
					t.Errorf("%q does not contain %q", got, s)
				}
			}
			for _, s := range tt.notContains {
				if strings.Contains(string(got), s) {
					t.Errorf("%q must not contain %q", got, s)
				}
			}
		})
	}
	if got, _ := Markdown("![x](print:hero)", nil); strings.Contains(string(got), "<img") {
		t.Errorf("without a resolver print refs must be dropped: %q", got)
	}
}

func TestPrintsInViewsAndTemplates(t *testing.T) {
	g := testGazette()
	g.Spec.Logo = "print:logo"
	tax := Taxonomy{
		Prints: map[string]*v1.Print{
			"logo":    readyPrint("https://m/logo.svg", ""),
			"cover":   readyPrint("https://m/cover.png", "A cover"),
			"avatar":  readyPrint("https://m/avatar.png", ""),
			"pending": {Status: v1.PrintStatus{Phase: v1.PrintPending}},
		},
		Scribes: map[string]*v1.Scribe{"alice": {Spec: v1.ScribeSpec{DisplayName: "Alice", Avatar: "print:avatar"}}},
	}
	th, err := ParseTheme(map[string]string{
		LayoutTemplate: `{{ block "content" . }}{{ end }}`,
		DispatchTemplate: `{{ define "content" }}{{ .Gazette.Logo }}|{{ .Dispatch.Cover }}|{{ .Dispatch.CoverAlt }}|` +
			`{{ .Dispatch.Scribe.Avatar }}|{{ printurl "cover" }}|{{ printurl "pending" }}|{{ printurl "nope" }}{{ end }}`,
		IndexTemplate: `{{ define "content" }}{{ end }}`,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	d := testDispatch("hello", "Hello", "")
	d.Spec.Cover = "print:cover"
	d.Spec.ScribeRef = "alice"
	out, err := NewSite(th, g, nil, tax).DispatchPage(d)
	if err != nil {
		t.Fatal(err)
	}
	want := "https://m/logo.svg|https://m/cover.png|A cover|https://m/avatar.png|https://m/cover.png||"
	if string(out) != want {
		t.Errorf("page = %q, want %q", out, want)
	}

	// The same theme renders again with other Prints: printurl is rebound.
	tax.Prints = map[string]*v1.Print{"cover": readyPrint("https://m/other.png", "")}
	out, _ = NewSite(th, g, nil, tax).DispatchPage(d)
	if !strings.Contains(string(out), "|https://m/other.png|") {
		t.Errorf("second render = %q", out)
	}
}

func TestDefaultThemeShowsCoverAndLogo(t *testing.T) {
	g := testGazette()
	g.Spec.Logo = "print:logo"
	tax := Taxonomy{Prints: map[string]*v1.Print{
		"logo": readyPrint("https://m/logo.svg", ""), "cover": readyPrint("https://m/cover.png", "封面"),
	}}
	d := testDispatch("hello", "Hello", "![inline](print:cover)")
	d.Spec.Cover = "print:cover"
	out, err := NewSite(DefaultTheme(), g, nil, tax).DispatchPage(d)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{`class="logo" src="https://m/logo.svg"`, `class="cover" src="https://m/cover.png" alt="封面"`, `<img src="https://m/cover.png" alt="inline"`} {
		if !strings.Contains(string(out), s) {
			t.Errorf("page does not contain %q", s)
		}
	}
}

func TestPrintRefs(t *testing.T) {
	d := &v1.Dispatch{ObjectMeta: metav1.ObjectMeta{Name: "d"}, Spec: v1.DispatchSpec{
		Cover: "print:cover",
		Content: v1.DispatchContent{Body: "![a](print:one) [b](print:two) ![again](print:one) " +
			"![c](<print:three>) text print:not-a-link ![d](https://x/print:four)"},
	}}
	if got := strings.Join(PrintRefs(d), ","); got != "cover,one,three,two" {
		t.Errorf("PrintRefs = %q", got)
	}
}
