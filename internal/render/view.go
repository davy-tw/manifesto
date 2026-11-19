/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package render

import (
	"fmt"
	"html/template"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
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
	// Logo is the URL of the logo Print, or "".
	Logo string
}

// TermView is a Section or Stamp as seen by templates. DisplayName falls
// back to the name when the optional resource does not exist.
type TermView struct {
	Name        string
	DisplayName string
	Description string
	// Path is the term's listing page, e.g. /sections/tech.
	Path string
}

// ScribeView is an author as seen by templates.
type ScribeView struct {
	Name        string
	DisplayName string
	Bio         string
	Path        string
	// Avatar is the URL of the avatar Print, or "".
	Avatar string
}

// DispatchView is a Dispatch as seen by theme templates (.Dispatch and the
// items of .Dispatches).
type DispatchView struct {
	Name        string
	Title       string
	Summary     string
	URL         string
	Path        string
	PublishedAt *time.Time
	// Cover is the URL of the cover Print, or "", and CoverAlt its alt text.
	Cover    string
	CoverAlt string
	// Section is nil when the Dispatch has no section label.
	Section *TermView
	Stamps  []TermView
	// Scribe is nil when the Dispatch has no scribeRef or the Scribe does
	// not exist.
	Scribe *ScribeView
	// HTML is the sanitized body. It is empty in .Dispatches.
	HTML template.HTML
}

// FilterView describes the term a listing page is filtered by (.Filter).
type FilterView struct {
	// Kind is "section", "stamp" or "scribe".
	Kind        string
	Name        string
	DisplayName string
	Description string
	Path        string
}

// PaginationView describes a listing page's position (.Pagination).
type PaginationView struct {
	Page  int
	Pages int
	// PrevURL and NextURL are paths, empty on the first and last page.
	PrevURL string
	NextURL string
}

// PageData is the view model passed to every theme template.
type PageData struct {
	Gazette    GazetteView
	Dispatch   *DispatchView
	Dispatches []DispatchView
	// Filter is nil on the front page.
	Filter     *FilterView
	Pagination *PaginationView
	// Params are the theme's params overridden by the Gazette's typesetParams.
	Params map[string]string
}

// Taxonomy holds the optional Sections, Stamps and Scribes of a namespace,
// and its Prints, keyed by name.
type Taxonomy struct {
	Sections map[string]*v1.Section
	Stamps   map[string]*v1.Stamp
	Scribes  map[string]*v1.Scribe
	Prints   map[string]*v1.Print
}

// Site is everything needed to render the pages of one Gazette.
type Site struct {
	Theme    *Theme
	Gazette  *v1.Gazette
	Params   map[string]string
	Taxonomy Taxonomy
}

// NewSite returns the Site of g with theme t. typesetParams are the theme's
// default params; the Gazette's typesetParams override them. A nil g gives
// an empty Gazette, which lets press trial-render a Dispatch whose Gazette
// is missing.
func NewSite(t *Theme, g *v1.Gazette, typesetParams map[string]string, tax Taxonomy) *Site {
	if g == nil {
		g = &v1.Gazette{}
	}
	params := maps.Clone(typesetParams)
	if params == nil {
		params = map[string]string{}
	}
	maps.Copy(params, g.Spec.TypesetParams)
	return &Site{Theme: t, Gazette: g, Params: params, Taxonomy: tax}
}

// GazetteURL is the public address of a Gazette's front page.
func GazetteURL(domain string) string { return "https://" + domain }

// DispatchURL is the public address of a Dispatch.
func DispatchURL(domain, name string) string { return GazetteURL(domain) + DispatchPath(name) }

// SectionPath, StampPath and ScribePath are the listing pages of a term.
func SectionPath(name string) string { return "/sections/" + url.PathEscape(name) }
func StampPath(name string) string   { return "/stamps/" + url.PathEscape(name) }
func ScribePath(name string) string  { return "/scribes/" + url.PathEscape(name) }

// PagePath is page n (from 1) of the listing at base: base itself for the
// first page, then base/page/n.
func PagePath(base string, n int) string {
	if n <= 1 {
		return base
	}
	return strings.TrimSuffix(base, "/") + "/page/" + strconv.Itoa(n)
}

// PrintURL resolves a Print of the Site's namespace by name. Prints that do
// not exist or are not Ready resolve to "".
func (s *Site) PrintURL(name string) string {
	p := s.Taxonomy.Prints[name]
	if p == nil || p.Status.Phase != v1.PrintReady {
		return ""
	}
	return p.Status.URL
}

// printRef resolves a "print:<name>" field.
func (s *Site) printRef(ref v1.PrintRef) (link, alt string) {
	name, ok := strings.CutPrefix(string(ref), PrintScheme)
	if !ok {
		return "", ""
	}
	if p := s.Taxonomy.Prints[name]; p != nil {
		alt = p.Spec.Alt
	}
	return s.PrintURL(name), alt
}

// GazetteView returns the template view of the Site's Gazette.
func (s *Site) GazetteView() GazetteView {
	g := s.Gazette
	logo, _ := s.printRef(g.Spec.Logo)
	return GazetteView{
		Name:     g.Name,
		Title:    g.Spec.Title,
		Tagline:  g.Spec.Tagline,
		Domain:   g.Spec.Domain,
		Language: g.Spec.Language,
		URL:      GazetteURL(g.Spec.Domain),
		Logo:     logo,
	}
}

// SectionView returns the view of a Section by name.
func (s *Site) SectionView(name string) TermView {
	v := TermView{Name: name, DisplayName: name, Path: SectionPath(name)}
	if sec := s.Taxonomy.Sections[name]; sec != nil {
		v.Description = sec.Spec.Description
		if sec.Spec.DisplayName != "" {
			v.DisplayName = sec.Spec.DisplayName
		}
	}
	return v
}

// StampView returns the view of a Stamp by name.
func (s *Site) StampView(name string) TermView {
	v := TermView{Name: name, DisplayName: name, Path: StampPath(name)}
	if st := s.Taxonomy.Stamps[name]; st != nil {
		v.Description = st.Spec.Description
		if st.Spec.DisplayName != "" {
			v.DisplayName = st.Spec.DisplayName
		}
	}
	return v
}

// ScribeView returns the view of a Scribe by name, or nil if it does not exist.
func (s *Site) ScribeView(name string) *ScribeView {
	sc := s.Taxonomy.Scribes[name]
	if sc == nil {
		return nil
	}
	avatar, _ := s.printRef(sc.Spec.Avatar)
	return &ScribeView{Name: name, DisplayName: sc.Spec.DisplayName, Bio: sc.Spec.Bio, Path: ScribePath(name), Avatar: avatar}
}

// DispatchView builds the listing view of d, without its body.
func (s *Site) DispatchView(d *v1.Dispatch) DispatchView {
	v := DispatchView{
		Name:    d.Name,
		Title:   d.Spec.Title,
		Summary: d.Spec.Summary,
		URL:     DispatchURL(s.Gazette.Spec.Domain, d.Name),
		Path:    DispatchPath(d.Name),
	}
	if d.Status.PublishedAt != nil {
		t := d.Status.PublishedAt.Time
		v.PublishedAt = &t
	}
	v.Cover, v.CoverAlt = s.printRef(d.Spec.Cover)
	if name := SectionOf(d); name != "" {
		sec := s.SectionView(name)
		v.Section = &sec
	}
	for _, name := range StampsOf(d) {
		v.Stamps = append(v.Stamps, s.StampView(name))
	}
	if d.Spec.ScribeRef != "" {
		v.Scribe = s.ScribeView(d.Spec.ScribeRef)
	}
	return v
}

// DispatchPage renders the page of d.
func (s *Site) DispatchPage(d *v1.Dispatch) ([]byte, error) {
	dv := s.DispatchView(d)
	html, err := Markdown(d.Spec.Content.Body, s.PrintURL)
	if err != nil {
		return nil, err
	}
	dv.HTML = html
	return s.Theme.Render(DispatchTemplate, &PageData{
		Gazette: s.GazetteView(), Dispatch: &dv, Params: s.Params,
	}, s.PrintURL)
}

// ListPage renders a listing of ds, in the given order. filter is nil on
// the front page.
func (s *Site) ListPage(ds []*v1.Dispatch, filter *FilterView, pagination *PaginationView) ([]byte, error) {
	views := make([]DispatchView, 0, len(ds))
	for _, d := range ds {
		views = append(views, s.DispatchView(d))
	}
	return s.Theme.Render(IndexTemplate, &PageData{
		Gazette: s.GazetteView(), Dispatches: views, Filter: filter, Pagination: pagination, Params: s.Params,
	}, s.PrintURL)
}

// SectionOf returns the Section label of d, or "".
func SectionOf(d *v1.Dispatch) string { return d.Labels[v1.SectionLabel] }

// StampsOf returns the Stamp names labelled on d, sorted.
func StampsOf(d *v1.Dispatch) []string {
	var names []string
	prefix := v1.StampLabelPrefix + "/"
	for k := range d.Labels {
		if name, ok := strings.CutPrefix(k, prefix); ok && name != "" {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

// ParseTypeset parses the theme defined by a Typeset or ClusterTypeset spec.
func ParseTypeset(spec *v1.TypesetSpec) (*Theme, error) {
	if spec.ContractVersion != "" && spec.ContractVersion != v1.ContractV1 {
		return nil, templateError(fmt.Errorf("unsupported contractVersion %q", spec.ContractVersion))
	}
	if spec.Source.Inline == nil {
		return nil, templateError(fmt.Errorf("no inline source"))
	}
	return ParseTheme(spec.Source.Inline.Templates, spec.Source.Inline.Assets)
}

// PrintRefs returns the names of the Prints d refers to: its cover and the
// print:<name> references in its body, sorted and without duplicates.
func PrintRefs(d *v1.Dispatch) []string {
	seen := map[string]bool{}
	if name, ok := strings.CutPrefix(string(d.Spec.Cover), PrintScheme); ok && name != "" {
		seen[name] = true
	}
	for _, m := range printRefRE.FindAllStringSubmatch(d.Spec.Content.Body, -1) {
		seen[m[1]] = true
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

var printRefRE = regexp.MustCompile(`\(\s*<?print:([a-z0-9]([-a-z0-9]*[a-z0-9])?)`)
