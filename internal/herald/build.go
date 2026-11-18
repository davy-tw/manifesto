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
	"strings"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/types"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
	"go.pkgs.davy.tw/manifesto/internal/domain"
	"go.pkgs.davy.tw/manifesto/internal/render"
)

// Page is a pre-rendered response body.
type Page struct {
	Body []byte
	ETag string
}

// entry is what herald answers for one path.
type entry struct {
	status int
	page   *Page
	// key identifies the inputs the page was rendered from. When the inputs
	// are unchanged the page is reused without rendering.
	key string
}

// site holds the pre-rendered pages of one Gazette.
type site struct {
	gazette types.UID
	index   entry
	// posts maps a Dispatch name to its entry. Dispatches that are not
	// visible (Draft, Embargoed) are absent and answer 404.
	posts map[string]entry
}

// Snapshot is an immutable set of pre-rendered sites, keyed by domain.
type Snapshot struct {
	sites map[string]*site
}

// Builder renders Snapshots from Gazettes and Dispatches.
type Builder struct {
	Theme *render.Theme
	Log   logr.Logger
}

// Build renders every site. Pages whose inputs are unchanged since prev are
// reused; a page that fails to render keeps serving its previous version,
// and answers 503 if it never rendered.
//
// The cache key of a Dispatch page is its UID and resourceVersion plus the
// Gazette's resourceVersion (the title and tagline appear on every page).
// TODO(M2): add the Typeset's resourceVersion.
func (b *Builder) Build(gazettes []v1.Gazette, dispatches []v1.Dispatch, prev *Snapshot) *Snapshot {
	byGazette := map[types.NamespacedName][]*v1.Dispatch{}
	for i := range dispatches {
		d := &dispatches[i]
		k := types.NamespacedName{Namespace: d.Namespace, Name: d.Spec.GazetteRef}
		byGazette[k] = append(byGazette[k], d)
	}

	snap := &Snapshot{sites: map[string]*site{}}
	for dom, g := range domain.Owners(gazettes) {
		var old *site
		if prev != nil && prev.sites[dom] != nil && prev.sites[dom].gazette == g.UID {
			old = prev.sites[dom]
		}
		snap.sites[dom] = b.buildSite(g, byGazette[types.NamespacedName{Namespace: g.Namespace, Name: g.Name}], old)
	}
	return snap
}

func (b *Builder) buildSite(g *v1.Gazette, dispatches []*v1.Dispatch, old *site) *site {
	s := &site{gazette: g.UID, posts: map[string]entry{}}
	log := b.Log.WithValues("gazette", types.NamespacedName{Namespace: g.Namespace, Name: g.Name})

	var listed []*v1.Dispatch
	for _, d := range dispatches {
		switch d.Status.Phase {
		case v1.PhasePublished, v1.PhaseUnlisted:
			key := strings.Join([]string{string(d.UID), d.ResourceVersion, g.ResourceVersion}, "/")
			var prevEntry *entry
			if old != nil {
				if e, ok := old.posts[d.Name]; ok {
					prevEntry = &e
				}
			}
			s.posts[d.Name] = b.renderEntry(key, prevEntry, func() ([]byte, error) {
				return render.DispatchPage(b.Theme, g, d)
			}, log.WithValues("dispatch", d.Name))
			if d.Status.Phase == v1.PhasePublished {
				listed = append(listed, d)
			}
		case v1.PhaseRetracted:
			s.posts[d.Name] = entry{status: http.StatusGone}
		}
	}

	// Newest first; name breaks ties so the order is stable.
	slices.SortFunc(listed, func(a, c *v1.Dispatch) int {
		if r := c.Status.PublishedAt.Compare(a.Status.PublishedAt.Time); r != 0 {
			return r
		}
		return cmp.Compare(a.Name, c.Name)
	})
	parts := make([]string, 0, 2+2*len(listed))
	parts = append(parts, string(g.UID), g.ResourceVersion)
	for _, d := range listed {
		parts = append(parts, d.Name, d.ResourceVersion)
	}
	var prevIndex *entry
	if old != nil {
		prevIndex = &old.index
	}
	s.index = b.renderEntry(strings.Join(parts, "/"), prevIndex, func() ([]byte, error) {
		return render.IndexPage(b.Theme, g, listed)
	}, log.WithValues("page", "index"))
	return s
}

// renderEntry reuses prev when its key matches, and otherwise renders. On
// failure it falls back to prev's last good page, or 503.
func (b *Builder) renderEntry(key string, prev *entry, renderFn func() ([]byte, error), log logr.Logger) entry {
	if prev != nil && prev.key == key && prev.page != nil {
		return *prev
	}
	body, err := renderFn()
	if err != nil {
		log.Error(err, "Render failed")
		if prev != nil && prev.page != nil {
			return *prev
		}
		return entry{status: http.StatusServiceUnavailable, key: key}
	}
	sum := sha256.Sum256([]byte(key))
	return entry{
		status: http.StatusOK,
		page:   &Page{Body: body, ETag: `"` + hex.EncodeToString(sum[:12]) + `"`},
		key:    key,
	}
}
