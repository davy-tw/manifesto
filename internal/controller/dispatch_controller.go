/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package controller

import (
	"context"
	"fmt"
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
)

// gazetteRefIndex indexes Dispatches by spec.gazetteRef.
const gazetteRefIndex = "spec.gazetteRef"

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

// Reconcile computes the status of one Dispatch. It is idempotent: it only
// writes when the status actually changes, and an Embargoed Dispatch is
// requeued until publishAt (at most an hour at a time).
func (r *DispatchReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var d v1.Dispatch
	if err := r.Get(ctx, req.NamespacedName, &d); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	var gazette *v1.Gazette
	var g v1.Gazette
	err := r.Get(ctx, types.NamespacedName{Namespace: d.Namespace, Name: d.Spec.GazetteRef}, &g)
	switch {
	case err == nil:
		gazette = &g
	case !apierrors.IsNotFound(err):
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
	meta.SetStatusCondition(&status.Conditions, readyCondition(&d, gazette))
	meta.SetStatusCondition(&status.Conditions, renderedCondition(&d, gazette))

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

// readyCondition reports whether the Gazette and Scribe the Dispatch refers
// to exist.
func readyCondition(d *v1.Dispatch, gazette *v1.Gazette) metav1.Condition {
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
	case d.Spec.ScribeRef != "":
		// Scribe resources arrive in M2; until then no scribeRef can resolve.
		c.Status, c.Reason = metav1.ConditionFalse, v1.ReasonScribeNotFound
		c.Message = fmt.Sprintf("Scribe %q not found in namespace %q", d.Spec.ScribeRef, d.Namespace)
	}
	return c
}

// renderedCondition trial-renders the Dispatch with its Gazette's theme and
// discards the output, so authors see errors before visitors do.
func renderedCondition(d *v1.Dispatch, gazette *v1.Gazette) metav1.Condition {
	c := metav1.Condition{
		Type:               v1.ConditionRendered,
		Status:             metav1.ConditionTrue,
		Reason:             v1.ReasonRenderSucceeded,
		Message:            "Content renders with the Gazette's theme",
		ObservedGeneration: d.Generation,
	}
	// M1 has only the built-in theme; Typeset arrives in M2.
	if _, err := render.DispatchPage(render.DefaultTheme(), gazette, d); err != nil {
		c.Status = metav1.ConditionFalse
		c.Reason = render.ReasonOf(err)
		c.Message = render.TruncateMessage(err.Error())
	}
	return c
}

// SetupWithManager sets up the controller with the Manager.
func (r *DispatchReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &v1.Dispatch{}, gazetteRefIndex,
		func(o client.Object) []string { return []string{o.(*v1.Dispatch).Spec.GazetteRef} }); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1.Dispatch{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&v1.Gazette{}, handler.EnqueueRequestsFromMapFunc(r.dispatchesOfGazette),
			builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Named("dispatch").
		Complete(r)
}

// dispatchesOfGazette enqueues every Dispatch that refers to a Gazette, so
// their URL and conditions follow when it is created, changed or deleted.
func (r *DispatchReconciler) dispatchesOfGazette(ctx context.Context, o client.Object) []reconcile.Request {
	var list v1.DispatchList
	if err := r.List(ctx, &list, client.InNamespace(o.GetNamespace()),
		client.MatchingFields{gazetteRefIndex: o.GetName()}); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to list Dispatches of Gazette", "gazette", o.GetName())
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for _, d := range list.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&d)})
	}
	return reqs
}
