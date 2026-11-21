/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package render

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/url"
	"path"
	"time"
	"unicode/utf8"
)

// Template names that make up a theme. Every page is layout.html with the
// page's own template filling the "content" block.
const (
	LayoutTemplate   = "layout.html"
	DispatchTemplate = "dispatch.html"
	IndexTemplate    = "index.html"
)

var pageTemplates = []string{DispatchTemplate, IndexTemplate}

// Theme is a parsed set of page templates and their assets.
type Theme struct {
	// pages are never executed: html/template cannot clone an executed
	// template, and every render clones a page to bind printurl.
	pages map[string]*template.Template
}

//go:embed theme
var defaultThemeFS embed.FS

var defaultTheme = mustLoadDefaultTheme()

// DefaultTheme returns the built-in theme used when a Gazette has no Typeset.
func DefaultTheme() *Theme { return defaultTheme }

func mustLoadDefaultTheme() *Theme {
	templates := map[string]string{}
	assets := map[string]string{}
	err := fs.WalkDir(defaultThemeFS, "theme", func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		b, err := defaultThemeFS.ReadFile(p)
		if err != nil {
			return err
		}
		if path.Ext(p) == ".html" {
			templates[path.Base(p)] = string(b)
		} else {
			assets[path.Base(p)] = string(b)
		}
		return nil
	})
	if err != nil {
		panic(err)
	}
	t, err := ParseTheme(templates, assets)
	if err != nil {
		panic(fmt.Sprintf("built-in theme: %v", err))
	}
	return t
}

// ParseTheme parses a theme from its templates and assets, keyed by file
// name. Templates may only call the fixed FuncMap; failures are returned as
// an *Error with ReasonTemplateError.
func ParseTheme(templates, assets map[string]string) (*Theme, error) {
	layoutSrc, ok := templates[LayoutTemplate]
	if !ok {
		return nil, templateError(fmt.Errorf("missing %s", LayoutTemplate))
	}
	layout, err := template.New(LayoutTemplate).Funcs(funcMap(assets)).Parse(layoutSrc)
	if err != nil {
		return nil, templateError(err)
	}
	t := &Theme{pages: map[string]*template.Template{}}
	for _, name := range pageTemplates {
		src, ok := templates[name]
		if !ok {
			return nil, templateError(fmt.Errorf("missing %s", name))
		}
		page, err := template.Must(layout.Clone()).New(name).Parse(src)
		if err != nil {
			return nil, templateError(err)
		}
		t.pages[name] = page
	}
	return t, nil
}

func templateError(err error) error {
	return &Error{Reason: ReasonTemplateError, Err: err}
}

// funcMap is the only set of functions a theme can call.
func funcMap(assets map[string]string) template.FuncMap {
	return template.FuncMap{
		// asset inlines a theme asset, e.g. <style>{{ asset "style.css" }}</style>.
		"asset": func(name string) (template.CSS, error) {
			a, ok := assets[name]
			if !ok {
				return "", fmt.Errorf("asset %q not found", name)
			}
			// #nosec G203 -- assets are part of the theme, written by the publisher.
			return template.CSS(a), nil
		},
		// date formats a time with a Go layout, e.g. {{ date "2006-01-02" .PublishedAt }}.
		"date": func(layout string, v any) (string, error) {
			switch t := v.(type) {
			case time.Time:
				return t.Format(layout), nil
			case *time.Time:
				if t == nil {
					return "", nil
				}
				return t.Format(layout), nil
			}
			return "", fmt.Errorf("date: unsupported value %T", v)
		},
		// truncate shortens s to at most n characters, adding "…" when cut.
		"truncate": func(n int, s string) string {
			if utf8.RuneCountInString(s) <= n {
				return s
			}
			r := []rune(s)
			return string(r[:n]) + "…"
		},
		// url returns the path of a Dispatch by name, e.g. {{ url .Name }}.
		"url": DispatchPath,
		// printurl resolves a Print by name. Prints arrive in M3.
		// printurl resolves a Print by name, e.g. {{ printurl "logo" }}. It
		// is rebound for every render; an unknown or not Ready Print gives "".
		"printurl": func(string) string { return "" },
	}
}

// Render executes one page template (DispatchTemplate or IndexTemplate).
// resolve answers printurl and may be nil.
func (t *Theme) Render(page string, data *PageData, resolve PrintResolver) ([]byte, error) {
	pristine, ok := t.pages[page]
	if !ok {
		return nil, templateError(fmt.Errorf("unknown page %q", page))
	}
	tpl, err := pristine.Clone()
	if err != nil {
		return nil, templateError(err)
	}
	tpl.Funcs(template.FuncMap{"printurl": func(name string) string {
		if resolve == nil {
			return ""
		}
		return resolve(name)
	}})
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, LayoutTemplate, data); err != nil {
		return nil, templateError(err)
	}
	return buf.Bytes(), nil
}

// DispatchPath is the path of a Dispatch within its Gazette.
func DispatchPath(name string) string {
	return "/posts/" + url.PathEscape(name)
}
