/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
	"go.pkgs.davy.tw/manifesto/internal/render"
	"go.pkgs.davy.tw/manifesto/internal/sitedata"
)

// DispatchReconciler decides the phase of each Dispatch, schedules embargoed
// ones, checks their references and trial-renders them.
type DispatchReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// Clock is the source of "now"; defaults to the real clock.
	Clock clock.PassiveClock
}

// +kubebuilder:rbac:groups=manifesto.davy.tw,resources=dispatches,verbs=get;list;watch
// +kubebuilder:rbac:groups=manifesto.davy.tw,resources=dispatches/status,verbs=get;patch
// +kubebuilder:rbac:groups=manifesto.davy.tw,resources=scribes;sections;stamps,verbs=get;list;watch

// Reconcile computes the status of one Dispatch. It is idempotent: it only
// writes when the status actually changes, and an Embargoed Dispatch is
// requeued until publishAt (at most an hour at a time).
func (r *DispatchReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var d v1.Dispatch
	if err := r.Get(ctx, req.NamespacedName, &d); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	gazette, err := getOptional[v1.Gazette](ctx, r.Client, d.Namespace, d.Spec.GazetteRef)
	if err != nil {
		return ctrl.Result{}, err
	}
	var scribe *v1.Scribe
	if d.Spec.ScribeRef != "" {
		if scribe, err = getOptional[v1.Scribe](ctx, r.Client, d.Namespace, d.Spec.ScribeRef); err != nil {
			return ctrl.Result{}, err
		}
	}
	tax, err := sitedata.Taxonomy(ctx, r.Client, &d, scribe)
	if err != nil {
		return ctrl.Result{}, err
	}
	rendered, err := r.renderedCondition(ctx, &d, gazette, tax)
	if err != nil {
		return ctrl.Result{}, err
	}

	dec := decidePhase(d.Spec, d.Status, r.now())
	status := v1.DispatchStatus{
		Phase:              dec.Phase,
		PublishedAt:        dec.PublishedAt,
		ObservedGeneration: d.Generation,
		Conditions:         append([]metav1.Condition(nil), d.Status.Conditions...),
	}
	if gazette != nil {
		status.URL = render.DispatchURL(gazette.Spec.Domain, d.Name)
	}
	meta.SetStatusCondition(&status.Conditions, readyCondition(&d, gazette, scribe, tax.Prints))
	meta.SetStatusCondition(&status.Conditions, rendered)

	if !equality.Semantic.DeepEqual(status, d.Status) {
		if err := applyStatus(ctx, r.Client, "Dispatch", &d, &status); err != nil {
			return ctrl.Result{}, err
		}
		if status.Phase != d.Status.Phase {
			log.Info("Dispatch phase changed", "from", d.Status.Phase, "to", status.Phase)
		}
	}
	return ctrl.Result{RequeueAfter: dec.RequeueAfter}, nil
}

func (r *DispatchReconciler) now() time.Time {
	if r.Clock == nil {
		return time.Now()
	}
	return r.Clock.Now()
}

// getOptional gets a namespaced object, returning nil if it does not exist.
func getOptional[T any, PT interface {
	*T
	client.Object
}](ctx context.Context, c client.Client, namespace, name string) (*T, error) {
	obj := PT(new(T))
	err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, obj)
	switch {
	case err == nil:
		return (*T)(obj), nil
	case apierrors.IsNotFound(err):
		return nil, nil
	default:
		return nil, err
	}
}

// readyCondition reports whether the Gazette, Scribe and Prints the Dispatch
// refers to exist (and the Prints are Ready).
func readyCondition(d *v1.Dispatch, gazette *v1.Gazette, scribe *v1.Scribe, prints map[string]*v1.Print) metav1.Condition {
	var notReady []string
	for _, name := range render.PrintRefs(d) {
		if p := prints[name]; p == nil || p.Status.Phase != v1.PrintReady {
			notReady = append(notReady, name)
		}
	}
	c := metav1.Condition{
		Type:               v1.ConditionReady,
		Status:             metav1.ConditionTrue,
		Reason:             v1.ReasonResolved,
		Message:            "All references resolved",
		ObservedGeneration: d.Generation,
	}
	switch {
	case gazette == nil:
		c.Status, c.Reason = metav1.ConditionFalse, v1.ReasonGazetteNotFound
		c.Message = fmt.Sprintf("Gazette %q not found in namespace %q", d.Spec.GazetteRef, d.Namespace)
	case d.Spec.ScribeRef != "" && scribe == nil:
		c.Status, c.Reason = metav1.ConditionFalse, v1.ReasonScribeNotFound
		c.Message = fmt.Sprintf("Scribe %q not found in namespace %q", d.Spec.ScribeRef, d.Namespace)
	case len(notReady) > 0:
		c.Status, c.Reason = metav1.ConditionFalse, v1.ReasonPrintNotReady
		c.Message = fmt.Sprintf("Prints not found or not Ready: %s", strings.Join(notReady, ", "))
	}
	return c
}

// renderedCondition trial-renders the Dispatch with its Gazette's theme and
// discards the output, so authors see errors before visitors do.
func (r *DispatchReconciler) renderedCondition(ctx context.Context, d *v1.Dispatch, gazette *v1.Gazette, tax render.Taxonomy) (metav1.Condition, error) {
	c := metav1.Condition{
		Type:               v1.ConditionRendered,
		Status:             metav1.ConditionTrue,
		Reason:             v1.ReasonRenderSucceeded,
		Message:            "Content renders with the Gazette's theme",
		ObservedGeneration: d.Generation,
	}
	theme, err := sitedata.ResolveTheme(ctx, r.Client, gazette)
	if err != nil {
		return c, err
	}
	if theme.Theme == nil {
		c.Status, c.Reason = metav1.ConditionFalse, theme.Reason
		c.Message = render.TruncateMessage(theme.Err.Error())
		return c, nil
	}
	if _, err := render.NewSite(theme.Theme, gazette, theme.Params, tax).DispatchPage(d); err != nil {
		c.Status = metav1.ConditionFalse
		c.Reason = render.ReasonOf(err)
		c.Message = render.TruncateMessage(err.Error())
	}
	return c, nil
}

// SetupWithManager sets up the controller with the Manager. The field
// indexes it relies on are registered by Setup.
func (r *DispatchReconciler) SetupWithManager(mgr ctrl.Manager) error {
	generation := builder.WithPredicates(predicate.GenerationChangedPredicate{})
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1.Dispatch{}, builder.WithPredicates(predicate.Or[client.Object](
			predicate.GenerationChangedPredicate{}, predicate.LabelChangedPredicate{}))).
		Watches(&v1.Gazette{}, handler.EnqueueRequestsFromMapFunc(r.dispatchesOfGazette), generation).
		Watches(&v1.Scribe{}, handler.EnqueueRequestsFromMapFunc(r.dispatchesOfScribe), generation).
		// A Print becoming Ready changes its status only, so no generation filter.
		Watches(&v1.Print{}, handler.EnqueueRequestsFromMapFunc(r.dispatchesOfPrint)).
		Watches(&v1.Typeset{}, handler.EnqueueRequestsFromMapFunc(r.dispatchesOfTypeset), generation).
		Watches(&v1.ClusterTypeset{}, handler.EnqueueRequestsFromMapFunc(r.dispatchesOfTypeset), generation).
		Named("dispatch").
		Complete(r)
}

func (r *DispatchReconciler) listDispatches(ctx context.Context, namespace, field, value string) []reconcile.Request {
	var list v1.DispatchList
	if err := r.List(ctx, &list, client.InNamespace(namespace), client.MatchingFields{field: value}); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to list Dispatches", "index", field, "value", value)
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
	}
	return reqs
}

// dispatchesOfGazette enqueues every Dispatch that refers to a Gazette, so
// their URL and conditions follow when it is created, changed or deleted.
func (r *DispatchReconciler) dispatchesOfGazette(ctx context.Context, o client.Object) []reconcile.Request {
	return r.listDispatches(ctx, o.GetNamespace(), gazetteRefIndex, o.GetName())
}

func (r *DispatchReconciler) dispatchesOfPrint(ctx context.Context, o client.Object) []reconcile.Request {
	return r.listDispatches(ctx, o.GetNamespace(), printRefIndex, o.GetName())
}

func (r *DispatchReconciler) dispatchesOfScribe(ctx context.Context, o client.Object) []reconcile.Request {
	return r.listDispatches(ctx, o.GetNamespace(), scribeRefIndex, o.GetName())
}

// dispatchesOfTypeset enqueues the Dispatches of every Gazette using a theme,
// so their Rendered condition follows theme changes.
func (r *DispatchReconciler) dispatchesOfTypeset(ctx context.Context, o client.Object) []reconcile.Request {
	gazettes := gazettesUsingTypeset(ctx, r.Client, typesetObjectKey(o))
	reqs := make([]reconcile.Request, 0, len(gazettes))
	for _, g := range gazettes {
		reqs = append(reqs, r.listDispatches(ctx, g.Namespace, gazetteRefIndex, g.Name)...)
	}
	return reqs
}
