/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package controller

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
	"go.pkgs.davy.tw/manifesto/internal/render"
)

// resolvedTheme is the theme a Gazette renders with.
type resolvedTheme struct {
	theme  *render.Theme
	params map[string]string
	// err explains why the Gazette's typesetRef could not be used. The
	// theme is then the built-in one (TypesetNotFound) or nil (TemplateError).
	err    error
	reason string
}

// resolveTheme looks up and parses the theme of g. A Gazette without
// typesetRef, or whose theme does not exist, uses the built-in theme.
func resolveTheme(ctx context.Context, c client.Client, g *v1.Gazette) (resolvedTheme, error) {
	if g == nil || g.Spec.TypesetRef == nil {
		return resolvedTheme{theme: render.DefaultTheme()}, nil
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
		return resolvedTheme{
			theme:  render.DefaultTheme(),
			reason: v1.ReasonTypesetNotFound,
			err:    fmt.Errorf("%s %q not found; using the built-in theme", kindOf(ref), ref.Name),
		}, nil
	}
	if err != nil {
		return resolvedTheme{}, err
	}
	theme, err := render.ParseTypeset(spec)
	if err != nil {
		return resolvedTheme{reason: v1.ReasonTemplateError, err: fmt.Errorf("%s %q: %w", kindOf(ref), ref.Name, err)}, nil
	}
	return resolvedTheme{theme: theme, params: spec.Params}, nil
}

func kindOf(ref *v1.TypesetRef) v1.TypesetKind {
	if ref.Kind == "" {
		return v1.KindClusterTypeset
	}
	return ref.Kind
}

// taxonomyFor fetches the optional Section, Stamps and Scribe a Dispatch
// refers to, so a trial render sees the same view as herald.
func taxonomyFor(ctx context.Context, c client.Client, d *v1.Dispatch, scribe *v1.Scribe) (render.Taxonomy, error) {
	tax := render.Taxonomy{
		Sections: map[string]*v1.Section{},
		Stamps:   map[string]*v1.Stamp{},
		Scribes:  map[string]*v1.Scribe{},
	}
	if scribe != nil {
		tax.Scribes[scribe.Name] = scribe
	}
	if name := render.SectionOf(d); name != "" {
		var s v1.Section
		switch err := c.Get(ctx, types.NamespacedName{Namespace: d.Namespace, Name: name}, &s); {
		case err == nil:
			tax.Sections[name] = &s
		case !apierrors.IsNotFound(err):
			return tax, err
		}
	}
	for _, name := range render.StampsOf(d) {
		var s v1.Stamp
		switch err := c.Get(ctx, types.NamespacedName{Namespace: d.Namespace, Name: name}, &s); {
		case err == nil:
			tax.Stamps[name] = &s
		case !apierrors.IsNotFound(err):
			return tax, err
		}
	}
	return tax, nil
}
