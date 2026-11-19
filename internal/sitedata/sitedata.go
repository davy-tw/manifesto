/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

// Package sitedata looks up what a page is rendered from: the Gazette's
// theme and the Sections, Stamps, Scribe and Prints a Dispatch refers to.
// press uses it to trial-render and desk to preview, so both see exactly
// what herald will show.
package sitedata

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
	"go.pkgs.davy.tw/manifesto/internal/render"
)

// Theme is the theme a Gazette renders with.
type Theme struct {
	Theme  *render.Theme
	Params map[string]string
	// Err explains why the Gazette's typesetRef could not be used, with the
	// condition Reason. Theme is then the built-in one (TypesetNotFound) or
	// nil (TemplateError).
	Err    error
	Reason string
}

// ResolveTheme looks up and parses the theme of g. A nil Gazette, one
// without typesetRef, or one whose theme does not exist uses the built-in
// theme.
func ResolveTheme(ctx context.Context, c client.Reader, g *v1.Gazette) (Theme, error) {
	if g == nil || g.Spec.TypesetRef == nil {
		return Theme{Theme: render.DefaultTheme()}, nil
	}
	ref := g.Spec.TypesetRef
	var spec *v1.TypesetSpec
	var err error
	if ref.Kind == v1.KindTypeset {
		var ts v1.Typeset
		err = c.Get(ctx, types.NamespacedName{Namespace: g.Namespace, Name: ref.Name}, &ts)
		spec = &ts.Spec
	} else {
		var ts v1.ClusterTypeset
		err = c.Get(ctx, types.NamespacedName{Name: ref.Name}, &ts)
		spec = &ts.Spec
	}
	if apierrors.IsNotFound(err) {
		return Theme{
			Theme:  render.DefaultTheme(),
			Reason: v1.ReasonTypesetNotFound,
			Err:    fmt.Errorf("%s %q not found; using the built-in theme", kindOf(ref), ref.Name),
		}, nil
	}
	if err != nil {
		return Theme{}, err
	}
	theme, err := render.ParseTypeset(spec)
	if err != nil {
		return Theme{Reason: v1.ReasonTemplateError, Err: fmt.Errorf("%s %q: %w", kindOf(ref), ref.Name, err)}, nil
	}
	return Theme{Theme: theme, Params: spec.Params}, nil
}

func kindOf(ref *v1.TypesetRef) v1.TypesetKind {
	if ref.Kind == "" {
		return v1.KindClusterTypeset
	}
	return ref.Kind
}

// Taxonomy fetches the optional Section, Stamps, Scribe and Prints a
// Dispatch refers to. A known scribe may be passed to avoid fetching it.
func Taxonomy(ctx context.Context, c client.Reader, d *v1.Dispatch, scribe *v1.Scribe) (render.Taxonomy, error) {
	tax := render.Taxonomy{
		Sections: map[string]*v1.Section{},
		Stamps:   map[string]*v1.Stamp{},
		Scribes:  map[string]*v1.Scribe{},
		Prints:   map[string]*v1.Print{},
	}
	get := func(name string, obj client.Object) (bool, error) {
		switch err := c.Get(ctx, types.NamespacedName{Namespace: d.Namespace, Name: name}, obj); {
		case err == nil:
			return true, nil
		case apierrors.IsNotFound(err):
			return false, nil
		default:
			return false, err
		}
	}
	for _, name := range render.PrintRefs(d) {
		var p v1.Print
		if ok, err := get(name, &p); err != nil {
			return tax, err
		} else if ok {
			tax.Prints[name] = &p
		}
	}
	if scribe == nil && d.Spec.ScribeRef != "" {
		var s v1.Scribe
		if ok, err := get(d.Spec.ScribeRef, &s); err != nil {
			return tax, err
		} else if ok {
			scribe = &s
		}
	}
	if scribe != nil {
		tax.Scribes[scribe.Name] = scribe
	}
	if name := render.SectionOf(d); name != "" {
		var s v1.Section
		if ok, err := get(name, &s); err != nil {
			return tax, err
		} else if ok {
			tax.Sections[name] = &s
		}
	}
	for _, name := range render.StampsOf(d) {
		var s v1.Stamp
		if ok, err := get(name, &s); err != nil {
			return tax, err
		} else if ok {
			tax.Stamps[name] = &s
		}
	}
	return tax, nil
}
