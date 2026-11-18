/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package render

import (
	"html/template"
	"time"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
)

// GazetteView is the Gazette as seen by theme templates (.Gazette).
type GazetteView struct {
	Name     string
	Title    string
	Tagline  string
	Domain   string
	Language string
	URL      string
}

// DispatchView is a Dispatch as seen by theme templates (.Dispatch and the
// items of .Dispatches).
type DispatchView struct {
	Name        string
	Title       string
	Summary     string
	URL         string
	PublishedAt *time.Time
	// HTML is the sanitized body. It is empty in .Dispatches.
	HTML template.HTML
}

// PageData is the view model passed to every theme template.
type PageData struct {
	Gazette    GazetteView
	Dispatch   *DispatchView
	Dispatches []DispatchView
	// Pagination is filled in from M2.
	Pagination any
	// Params are the theme parameters; filled in from M2 with Typeset.
	Params map[string]string
}

// GazetteURL is the public address of a Gazette's front page.
func GazetteURL(domain string) string { return "https://" + domain }

// DispatchURL is the public address of a Dispatch.
func DispatchURL(domain, name string) string { return GazetteURL(domain) + DispatchPath(name) }

// NewGazetteView builds the template view of g. A nil g gives an empty view,
// which lets press trial-render a Dispatch whose Gazette is missing.
func NewGazetteView(g *v1.Gazette) GazetteView {
	if g == nil {
		return GazetteView{}
	}
	return GazetteView{
		Name:     g.Name,
		Title:    g.Spec.Title,
		Tagline:  g.Spec.Tagline,
		Domain:   g.Spec.Domain,
		Language: g.Spec.Language,
		URL:      GazetteURL(g.Spec.Domain),
	}
}

// NewDispatchView builds the listing view of d, without its body.
func NewDispatchView(d *v1.Dispatch, domain string) DispatchView {
	v := DispatchView{
		Name:    d.Name,
		Title:   d.Spec.Title,
		Summary: d.Spec.Summary,
		URL:     DispatchURL(domain, d.Name),
	}
	if d.Status.PublishedAt != nil {
		t := d.Status.PublishedAt.Time
		v.PublishedAt = &t
	}
	return v
}

// DispatchPage renders the page of d in g with theme t.
func DispatchPage(t *Theme, g *v1.Gazette, d *v1.Dispatch) ([]byte, error) {
	gv := NewGazetteView(g)
	dv := NewDispatchView(d, gv.Domain)
	html, err := Markdown(d.Spec.Content.Body)
	if err != nil {
		return nil, err
	}
	dv.HTML = html
	return t.Render(DispatchTemplate, &PageData{Gazette: gv, Dispatch: &dv})
}

// IndexPage renders the front page of g listing ds, in the given order.
func IndexPage(t *Theme, g *v1.Gazette, ds []*v1.Dispatch) ([]byte, error) {
	gv := NewGazetteView(g)
	views := make([]DispatchView, 0, len(ds))
	for _, d := range ds {
		views = append(views, NewDispatchView(d, gv.Domain))
	}
	return t.Render(IndexTemplate, &PageData{Gazette: gv, Dispatches: views})
}
