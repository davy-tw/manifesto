/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package herald

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/types"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
	"go.pkgs.davy.tw/manifesto/internal/domain"
	"go.pkgs.davy.tw/manifesto/internal/render"
)

const (
	contentTypeHTML = "text/html; charset=utf-8"
	contentTypeRSS  = "application/rss+xml; charset=utf-8"
	contentTypeXML  = "application/xml; charset=utf-8"

	// wireItems is the number of Dispatches in the Wire feed.
	wireItems = 20
)

// Page is a pre-rendered response body.
type Page struct {
	Body        []byte
	ETag        string
	ContentType string
}

// entry is what herald answers for one path.
type entry struct {
	status int
	page   *Page
	// key identifies the inputs the page was rendered from. When the inputs
	// are unchanged the page is reused without rendering.
	key string
}

// site holds the pre-rendered pages of one Gazette, keyed by path. Paths
// that are absent answer 404.
type site struct {
	gazette types.UID
	pages   map[string]entry
}

// themeEntry is a parsed theme, kept across builds so a theme that stops
// parsing keeps serving its last good version.
type themeEntry struct {
	key    string // UID/resourceVersion of the Typeset it was parsed from
	theme  *render.Theme
	params map[string]string
}

// Snapshot is an immutable set of pre-rendered sites, keyed by domain.
type Snapshot struct {
	sites  map[string]*site
	themes map[string]themeEntry
}

// Inputs are the resources a Snapshot is built from.
type Inputs struct {
	Gazettes        []v1.Gazette
	Dispatches      []v1.Dispatch
	Scribes         []v1.Scribe
	Sections        []v1.Section
	Stamps          []v1.Stamp
	Typesets        []v1.Typeset
	ClusterTypesets []v1.ClusterTypeset
	Prints          []v1.Print
}

// Builder renders Snapshots.
type Builder struct {
	// Theme is used by Gazettes without a typesetRef, or whose theme does
	// not exist.
	Theme *render.Theme
	Log   logr.Logger
}

// Build renders every site. Pages whose inputs are unchanged since prev are
// reused; a page that fails to render keeps serving its previous version,
// and answers 503 if it never rendered.
//
// A page's cache key covers everything it is rendered from: the Gazette's
// resourceVersion, the theme's resourceVersion, the namespace's Sections,
// Stamps and Scribes, and the Dispatches shown on it.
func (b *Builder) Build(in Inputs, prev *Snapshot) *Snapshot {
	if prev == nil {
		prev = &Snapshot{}
	}
	snap := &Snapshot{sites: map[string]*site{}, themes: map[string]themeEntry{}}

	byGazette := map[types.NamespacedName][]*v1.Dispatch{}
	for i := range in.Dispatches {
		d := &in.Dispatches[i]
		k := types.NamespacedName{Namespace: d.Namespace, Name: d.Spec.GazetteRef}
		byGazette[k] = append(byGazette[k], d)
	}
	taxonomies := buildTaxonomies(in)
	typesets := map[string]*v1.TypesetSpec{}
	typesetKeys := map[string]string{}
	for i := range in.Typesets {
		t := &in.Typesets[i]
		id := themeID(v1.KindTypeset, t.Namespace, t.Name)
		typesets[id], typesetKeys[id] = &t.Spec, string(t.UID)+"/"+t.ResourceVersion
	}
	for i := range in.ClusterTypesets {
		t := &in.ClusterTypesets[i]
		id := themeID(v1.KindClusterTypeset, "", t.Name)
		typesets[id], typesetKeys[id] = &t.Spec, string(t.UID)+"/"+t.ResourceVersion
	}

	for dom, g := range domain.Owners(in.Gazettes) {
		log := b.Log.WithValues("gazette", types.NamespacedName{Namespace: g.Namespace, Name: g.Name})

		themeKey := "builtin"
		te := themeEntry{theme: b.Theme}
		if ref := g.Spec.TypesetRef; ref != nil {
			id := themeID(ref.Kind, g.Namespace, ref.Name)
			if spec, ok := typesets[id]; ok {
				te = b.theme(id, typesetKeys[id], spec, prev, snap, log)
				themeKey = id + "@" + te.key
			}
		}

		var old *site
		if s := prev.sites[dom]; s != nil && s.gazette == g.UID {
			old = s
		}
		tax := taxonomies[g.Namespace]
		sb := &siteBuilder{
			b:       b,
			log:     log,
			site:    render.NewSite(te.theme, g, te.params, tax.Taxonomy),
			out:     &site{gazette: g.UID, pages: map[string]entry{}},
			old:     old,
			baseKey: strings.Join([]string{g.ResourceVersion, themeKey, tax.key}, "|"),
		}
		sb.build(byGazette[types.NamespacedName{Namespace: g.Namespace, Name: g.Name}])
		snap.sites[dom] = sb.out
	}
	return snap
}

// theme returns the parsed theme id at version key, reusing prev's parse
// when unchanged and its last good parse when the new version fails.
func (b *Builder) theme(id, key string, spec *v1.TypesetSpec, prev, snap *Snapshot, log logr.Logger) themeEntry {
	if te, ok := snap.themes[id]; ok {
		return te
	}
	te, ok := prev.themes[id]
	if !ok || te.key != key {
		theme, err := render.ParseTypeset(spec)
		switch {
		case err == nil:
			te = themeEntry{key: key, theme: theme, params: spec.Params}
		case ok && te.theme != nil:
			log.Error(err, "Theme does not parse; keeping its last good version", "typeset", id)
		default:
			log.Error(err, "Theme does not parse", "typeset", id)
			te = themeEntry{key: key}
		}
	}
	snap.themes[id] = te
	return te
}

func themeID(kind v1.TypesetKind, namespace, name string) string {
	if kind == "" {
		kind = v1.KindClusterTypeset
	}
	if kind == v1.KindClusterTypeset {
		namespace = ""
	}
	return string(kind) + "/" + namespace + "/" + name
}

type namespaceTaxonomy struct {
	render.Taxonomy
	key string
}

// buildTaxonomies groups Sections, Stamps, Scribes and Prints by namespace,
// with a key that changes whenever any of them does.
func buildTaxonomies(in Inputs) map[string]namespaceTaxonomy {
	out := map[string]namespaceTaxonomy{}
	parts := map[string][]string{}
	get := func(ns string) namespaceTaxonomy {
		t, ok := out[ns]
		if !ok {
			t.Taxonomy = render.Taxonomy{
				Sections: map[string]*v1.Section{}, Stamps: map[string]*v1.Stamp{},
				Scribes: map[string]*v1.Scribe{}, Prints: map[string]*v1.Print{},
			}
		}
		return t
	}
	for i := range in.Sections {
		s := &in.Sections[i]
		t := get(s.Namespace)
		t.Sections[s.Name] = s
		out[s.Namespace] = t
		parts[s.Namespace] = append(parts[s.Namespace], "section/"+s.Name+"/"+s.ResourceVersion)
	}
	for i := range in.Stamps {
		s := &in.Stamps[i]
		t := get(s.Namespace)
		t.Stamps[s.Name] = s
		out[s.Namespace] = t
		parts[s.Namespace] = append(parts[s.Namespace], "stamp/"+s.Name+"/"+s.ResourceVersion)
	}
	for i := range in.Scribes {
		s := &in.Scribes[i]
		t := get(s.Namespace)
		t.Scribes[s.Name] = s
		out[s.Namespace] = t
		parts[s.Namespace] = append(parts[s.Namespace], "scribe/"+s.Name+"/"+s.ResourceVersion)
	}
	for i := range in.Prints {
		p := &in.Prints[i]
		t := get(p.Namespace)
		t.Prints[p.Name] = p
		out[p.Namespace] = t
		parts[p.Namespace] = append(parts[p.Namespace], "print/"+p.Name+"/"+p.ResourceVersion)
	}
	for ns, p := range parts {
		slices.Sort(p)
		t := out[ns]
		t.key = hash(strings.Join(p, ","))
		out[ns] = t
	}
	return out
}

// siteBuilder renders the pages of one Gazette.
type siteBuilder struct {
	b       *Builder
	log     logr.Logger
	site    *render.Site
	out     *site
	old     *site
	baseKey string
}

func (sb *siteBuilder) build(dispatches []*v1.Dispatch) {
	var listed []*v1.Dispatch
	for _, d := range dispatches {
		switch d.Status.Phase {
		case v1.PhasePublished, v1.PhaseUnlisted:
			path := render.DispatchPath(d.Name)
			sb.page(path, contentTypeHTML, sb.baseKey+"|"+string(d.UID)+"/"+d.ResourceVersion, func() ([]byte, error) {
				return sb.site.DispatchPage(d)
			})
			if d.Status.Phase == v1.PhasePublished {
				listed = append(listed, d)
			}
		case v1.PhaseRetracted:
			sb.out.pages[render.DispatchPath(d.Name)] = entry{status: http.StatusGone}
		}
	}
	// Newest first; name breaks ties so the order is stable.
	slices.SortFunc(listed, func(a, c *v1.Dispatch) int {
		if r := c.Status.PublishedAt.Compare(a.Status.PublishedAt.Time); r != 0 {
			return r
		}
		return cmp.Compare(a.Name, c.Name)
	})

	sb.listing("/", nil, listed)
	sb.listings(listed)
	sb.page("/wire.xml", contentTypeRSS, sb.listKey("wire", listed[:min(len(listed), wireItems)]), func() ([]byte, error) {
		return wire(sb.site, listed[:min(len(listed), wireItems)])
	})
	sb.page("/sitemap.xml", contentTypeXML, sb.listKey("sitemap", listed), func() ([]byte, error) {
		return sitemap(sb.site, listed, sb.listingPaths())
	})
}

// listings renders the Section, Stamp and Scribe pages. A term has a page
// when its resource exists or a listed Dispatch uses it. A Section's page
// also lists the Dispatches of its descendants.
func (sb *siteBuilder) listings(listed []*v1.Dispatch) {
	tax := sb.site.Taxonomy
	sections, stamps, scribes := map[string][]*v1.Dispatch{}, map[string][]*v1.Dispatch{}, map[string][]*v1.Dispatch{}
	for name := range tax.Sections {
		sections[name] = nil
	}
	for name := range tax.Stamps {
		stamps[name] = nil
	}
	for name := range tax.Scribes {
		scribes[name] = nil
	}
	for _, d := range listed {
		for _, s := range sectionChain(render.SectionOf(d), tax.Sections) {
			sections[s] = append(sections[s], d)
		}
		for _, s := range render.StampsOf(d) {
			stamps[s] = append(stamps[s], d)
		}
		if s := d.Spec.ScribeRef; s != "" {
			scribes[s] = append(scribes[s], d)
		}
	}
	for name, ds := range sections {
		v := sb.site.SectionView(name)
		sb.listing(v.Path, &render.FilterView{Kind: "section", Name: name, DisplayName: v.DisplayName, Description: v.Description, Path: v.Path}, ds)
	}
	for name, ds := range stamps {
		v := sb.site.StampView(name)
		sb.listing(v.Path, &render.FilterView{Kind: "stamp", Name: name, DisplayName: v.DisplayName, Description: v.Description, Path: v.Path}, ds)
	}
	for name, ds := range scribes {
		f := &render.FilterView{Kind: "scribe", Name: name, DisplayName: name, Path: render.ScribePath(name)}
		if v := sb.site.ScribeView(name); v != nil {
			f.DisplayName, f.Description = v.DisplayName, v.Bio
		}
		sb.listing(f.Path, f, ds)
	}
}

// sectionChain returns name and its ancestors, stopping at a missing parent
// or a cycle.
func sectionChain(name string, sections map[string]*v1.Section) []string {
	var chain []string
	seen := map[string]bool{}
	for cur := name; cur != "" && !seen[cur]; {
		chain = append(chain, cur)
		seen[cur] = true
		s := sections[cur]
		if s == nil {
			break
		}
		cur = s.Spec.Parent
	}
	return chain
}

// listing renders base and its further pages.
func (sb *siteBuilder) listing(base string, filter *render.FilterView, ds []*v1.Dispatch) {
	size := int(sb.site.Gazette.Spec.PageSize)
	if size <= 0 {
		size = v1.DefaultPageSize
	}
	pages := max(1, (len(ds)+size-1)/size)
	for p := 1; p <= pages; p++ {
		items := ds[(p-1)*size : min(p*size, len(ds))]
		pg := &render.PaginationView{Page: p, Pages: pages}
		if p > 1 {
			pg.PrevURL = render.PagePath(base, p-1)
		}
		if p < pages {
			pg.NextURL = render.PagePath(base, p+1)
		}
		key := sb.listKey(base+"#"+strconv.Itoa(p)+"/"+strconv.Itoa(pages), items)
		sb.page(render.PagePath(base, p), contentTypeHTML, key, func() ([]byte, error) {
			return sb.site.ListPage(items, filter, pg)
		})
	}
}

// listingPaths returns the first page of every listing, for the sitemap.
func (sb *siteBuilder) listingPaths() []string {
	var paths []string
	for p := range sb.out.pages {
		if p != "/" && !strings.HasPrefix(p, "/posts/") && !strings.Contains(p, "/page/") && !strings.HasSuffix(p, ".xml") {
			paths = append(paths, p)
		}
	}
	slices.Sort(paths)
	return paths
}

func (sb *siteBuilder) listKey(name string, ds []*v1.Dispatch) string {
	parts := make([]string, 0, 2+2*len(ds))
	parts = append(parts, sb.baseKey, name)
	for _, d := range ds {
		parts = append(parts, d.Name, d.ResourceVersion)
	}
	return strings.Join(parts, "|")
}

// page reuses the previous entry for path when its key matches, and
// otherwise renders. On failure it falls back to the previous good page, or
// 503.
func (sb *siteBuilder) page(path, contentType, key string, renderFn func() ([]byte, error)) {
	var prev *entry
	if sb.old != nil {
		if e, ok := sb.old.pages[path]; ok {
			prev = &e
		}
	}
	if prev != nil && prev.key == key && prev.page != nil {
		sb.out.pages[path] = *prev
		return
	}
	var body []byte
	var err error
	if sb.site.Theme == nil && contentType == contentTypeHTML {
		err = errNoTheme
	} else {
		body, err = renderFn()
	}
	if err != nil {
		sb.log.Error(err, "Render failed", "path", path)
		if prev != nil && prev.page != nil && prev.status == http.StatusOK {
			sb.out.pages[path] = *prev
		} else {
			sb.out.pages[path] = entry{status: http.StatusServiceUnavailable, key: key}
		}
		return
	}
	sb.out.pages[path] = entry{
		status: http.StatusOK,
		page:   &Page{Body: body, ETag: `"` + hash(key) + `"`, ContentType: contentType},
		key:    key,
	}
}

func hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:12])
}
