/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package controller

import (
	"context"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
	"go.pkgs.davy.tw/manifesto/internal/domain"
	"go.pkgs.davy.tw/manifesto/internal/render"
)

// Field indexes shared by press's controllers.
const (
	// gazetteRefIndex indexes Dispatches by spec.gazetteRef.
	gazetteRefIndex = "spec.gazetteRef"
	// scribeRefIndex indexes Dispatches by spec.scribeRef.
	scribeRefIndex = "spec.scribeRef"
	// domainIndex indexes Gazettes (across namespaces) by normalized spec.domain.
	domainIndex = "spec.domain"
	// typesetIndex indexes Gazettes by the theme they use, see typesetKey.
	typesetIndex = "spec.typesetRef"
	// printRefIndex indexes Dispatches by the Prints they refer to.
	printRefIndex = "printRefs"
	// printObjectIndex indexes Prints by status.objectKey.
	printObjectIndex = "status.objectKey"
	// printArchiveIndex indexes Prints by their Archive, see archiveKey.
	printArchiveIndex = "spec.archiveRef"
)

// Options configure press's controllers. The zero value works: real clock,
// no routing, no media backends.
type Options struct {
	Clock   clock.PassiveClock
	Routing Routing
	Media   Media
	// Editions is how many Editions to keep per Dispatch: 0 means
	// DefaultEditionLimit, a negative number turns Editions off.
	Editions int
	// EditionBytes bounds the content kept in one Dispatch's Editions: 0
	// means DefaultEditionBytes, a negative number no bound.
	EditionBytes int64
}

// Setup registers press's field indexes and controllers with mgr.
func Setup(mgr ctrl.Manager, opts Options) error {
	ctx := context.Background()
	idx := mgr.GetFieldIndexer()
	if err := idx.IndexField(ctx, &v1.Dispatch{}, gazetteRefIndex, func(o client.Object) []string {
		return []string{o.(*v1.Dispatch).Spec.GazetteRef}
	}); err != nil {
		return err
	}
	if err := idx.IndexField(ctx, &v1.Dispatch{}, scribeRefIndex, func(o client.Object) []string {
		if ref := o.(*v1.Dispatch).Spec.ScribeRef; ref != "" {
			return []string{ref}
		}
		return nil
	}); err != nil {
		return err
	}
	if err := idx.IndexField(ctx, &v1.Dispatch{}, printRefIndex, func(o client.Object) []string {
		return render.PrintRefs(o.(*v1.Dispatch))
	}); err != nil {
		return err
	}
	if err := idx.IndexField(ctx, &v1.Gazette{}, domainIndex, func(o client.Object) []string {
		return []string{domain.Normalize(o.(*v1.Gazette).Spec.Domain)}
	}); err != nil {
		return err
	}
	if err := idx.IndexField(ctx, &v1.Gazette{}, typesetIndex, func(o client.Object) []string {
		if key := typesetKeyOf(o.(*v1.Gazette)); key != "" {
			return []string{key}
		}
		return nil
	}); err != nil {
		return err
	}

	if err := idx.IndexField(ctx, &v1.Print{}, printObjectIndex, func(o client.Object) []string {
		if k := o.(*v1.Print).Status.ObjectKey; k != "" {
			return []string{k}
		}
		return nil
	}); err != nil {
		return err
	}
	if err := idx.IndexField(ctx, &v1.Print{}, printArchiveIndex, func(o client.Object) []string {
		ref := o.(*v1.Print).Spec.ArchiveRef
		return []string{archiveKey(ref.Kind, o.GetNamespace(), ref.Name)}
	}); err != nil {
		return err
	}

	c, scheme := mgr.GetClient(), mgr.GetScheme()
	if opts.Media.Secrets == nil {
		opts.Media.Secrets = mgr.GetAPIReader()
	}
	for _, r := range []interface{ SetupWithManager(ctrl.Manager) error }{
		&GazetteReconciler{Client: c, Scheme: scheme, Routing: opts.Routing},
		&DispatchReconciler{Client: c, Scheme: scheme, Clock: opts.Clock},
		&PrintReconciler{Client: c, Scheme: scheme, Media: opts.Media, Clock: opts.Clock},
		&ArchiveReconciler{Client: c, Scheme: scheme, Media: opts.Media},
		&ArchiveReconciler{Client: c, Scheme: scheme, Media: opts.Media, Cluster: true},
		&EditionReconciler{Client: c, Scheme: scheme, Limit: editionLimit(opts.Editions),
			MaxBytes: editionBytes(opts.EditionBytes)},
		&SectionReconciler{Client: c, Scheme: scheme},
		&TypesetReconciler{Client: c, Scheme: scheme},
		&TypesetReconciler{Client: c, Scheme: scheme, Cluster: true},
	} {
		if err := r.SetupWithManager(mgr); err != nil {
			return err
		}
	}
	return nil
}

// typesetKey identifies a theme in typesetIndex: "Typeset/<ns>/<name>" or
// "ClusterTypeset//<name>".
func typesetKey(kind v1.TypesetKind, namespace, name string) string {
	if kind == v1.KindClusterTypeset {
		namespace = ""
	}
	return string(kind) + "/" + namespace + "/" + name
}

func typesetKeyOf(g *v1.Gazette) string {
	ref := g.Spec.TypesetRef
	if ref == nil {
		return ""
	}
	kind := ref.Kind
	if kind == "" {
		kind = v1.KindClusterTypeset
	}
	return typesetKey(kind, g.Namespace, ref.Name)
}

// gazettesUsingTypeset lists the Gazettes whose theme has the given key.
func gazettesUsingTypeset(ctx context.Context, c client.Client, key string) []v1.Gazette {
	var list v1.GazetteList
	if err := c.List(ctx, &list, client.MatchingFields{typesetIndex: key}); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to list Gazettes using theme", "typeset", key)
		return nil
	}
	return list.Items
}

// typesetObjectKey returns the theme key of a Typeset or ClusterTypeset object.
func typesetObjectKey(o client.Object) string {
	if _, ok := o.(*v1.ClusterTypeset); ok {
		return typesetKey(v1.KindClusterTypeset, "", o.GetName())
	}
	return typesetKey(v1.KindTypeset, o.GetNamespace(), o.GetName())
}

func requestsFor[T client.Object](items []T) []reconcile.Request {
	reqs := make([]reconcile.Request, 0, len(items))
	for _, o := range items {
		reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: o.GetNamespace(), Name: o.GetName()}})
	}
	return reqs
}

func editionLimit(n int) int {
	if n == 0 {
		return DefaultEditionLimit
	}
	return n
}

func editionBytes(n int64) int64 {
	if n == 0 {
		return DefaultEditionBytes
	}
	return n
}
