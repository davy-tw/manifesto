/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package controller

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
	"go.pkgs.davy.tw/manifesto/internal/domain"
	"go.pkgs.davy.tw/manifesto/internal/render"
)

// GazetteReconciler reports whether each Gazette owns its domain and has a
// usable theme, and, when Routing is enabled, exposes the domain through
// Gateway API.
type GazetteReconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	Routing Routing
}

// +kubebuilder:rbac:groups=manifesto.davy.tw,resources=gazettes,verbs=get;list;watch
// +kubebuilder:rbac:groups=manifesto.davy.tw,resources=gazettes/status,verbs=get;patch

// Reconcile computes the status of one Gazette. When Gazettes in different
// namespaces claim the same domain, the oldest one is Ready and the others
// report DomainConflict. A missing theme reports TypesetNotFound (the
// built-in theme is used meanwhile) and a broken one TemplateError.
func (r *GazetteReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var g v1.Gazette
	if err := r.Get(ctx, req.NamespacedName, &g); err != nil {
		if apierrors.IsNotFound(err) && r.Routing.Enabled() {
			// The routing objects go with the Gazette (owner references);
			// only the namespace's ReferenceGrant may need removing.
			return ctrl.Result{}, r.reconcileReferenceGrant(ctx, req.Namespace)
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	var peers v1.GazetteList
	if err := r.List(ctx, &peers, client.MatchingFields{domainIndex: domain.Normalize(g.Spec.Domain)}); err != nil {
		return ctrl.Result{}, err
	}
	owner := domain.Owners(peers.Items)[domain.Normalize(g.Spec.Domain)]
	theme, err := resolveTheme(ctx, r.Client, &g)
	if err != nil {
		return ctrl.Result{}, err
	}

	ready := metav1.Condition{
		Type:               v1.ConditionReady,
		Status:             metav1.ConditionTrue,
		Reason:             v1.ReasonResolved,
		Message:            fmt.Sprintf("Serving %s", g.Spec.Domain),
		ObservedGeneration: g.Generation,
	}
	ownsDomain := owner == nil || (owner.Namespace == g.Namespace && owner.Name == g.Name)
	switch {
	case !ownsDomain:
		ready.Status = metav1.ConditionFalse
		ready.Reason = v1.ReasonDomainConflict
		ready.Message = fmt.Sprintf("Domain %s is already served by Gazette %s/%s", g.Spec.Domain, owner.Namespace, owner.Name)
	case theme.err != nil:
		ready.Status = metav1.ConditionFalse
		ready.Reason = theme.reason
		ready.Message = render.TruncateMessage(theme.err.Error())
	}

	status := v1.GazetteStatus{
		URL:                render.GazetteURL(g.Spec.Domain),
		ObservedGeneration: g.Generation,
		Conditions:         append([]metav1.Condition(nil), g.Status.Conditions...),
	}
	meta.SetStatusCondition(&status.Conditions, ready)
	if r.Routing.Enabled() {
		if err := r.reconcileReferenceGrant(ctx, g.Namespace); err != nil {
			return ctrl.Result{}, err
		}
		routed, err := r.reconcileRouting(ctx, &g, ownsDomain)
		if err != nil {
			return ctrl.Result{}, err
		}
		meta.SetStatusCondition(&status.Conditions, routed)
	}
	if equality.Semantic.DeepEqual(status, g.Status) {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{}, applyStatus(ctx, r.Client, "Gazette", &g, &status)
}

// SetupWithManager sets up the controller with the Manager. The field
// indexes it relies on are registered by Setup.
func (r *GazetteReconciler) SetupWithManager(mgr ctrl.Manager) error {
	generation := builder.WithPredicates(predicate.GenerationChangedPredicate{})
	b := ctrl.NewControllerManagedBy(mgr)
	if r.Routing.Enabled() {
		// Follow what the Gateway implementation reports, and repair edits.
		b = b.Owns(&gatewayv1.ListenerSet{}).Owns(&gatewayv1.HTTPRoute{})
	}
	return b.
		For(&v1.Gazette{}).
		// A Gazette appearing, leaving or changing domain can change which of
		// its peers owns a domain, so requeue every Gazette on the old and new
		// domains.
		Watches(&v1.Gazette{}, handler.TypedFuncs[client.Object, reconcile.Request]{
			CreateFunc: func(ctx context.Context, e event.TypedCreateEvent[client.Object], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
				r.enqueuePeers(ctx, q, e.Object)
			},
			UpdateFunc: func(ctx context.Context, e event.TypedUpdateEvent[client.Object], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
				r.enqueuePeers(ctx, q, e.ObjectOld)
				r.enqueuePeers(ctx, q, e.ObjectNew)
			},
			DeleteFunc: func(ctx context.Context, e event.TypedDeleteEvent[client.Object], q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
				r.enqueuePeers(ctx, q, e.Object)
			},
		}).
		Watches(&v1.Typeset{}, handler.EnqueueRequestsFromMapFunc(r.gazettesOfTypeset), generation).
		Watches(&v1.ClusterTypeset{}, handler.EnqueueRequestsFromMapFunc(r.gazettesOfTypeset), generation).
		Named("gazette").
		Complete(r)
}

func (r *GazetteReconciler) gazettesOfTypeset(ctx context.Context, o client.Object) []reconcile.Request {
	gs := gazettesUsingTypeset(ctx, r.Client, typesetObjectKey(o))
	ptrs := make([]*v1.Gazette, len(gs))
	for i := range gs {
		ptrs[i] = &gs[i]
	}
	return requestsFor(ptrs)
}

func (r *GazetteReconciler) enqueuePeers(ctx context.Context, q workqueue.TypedRateLimitingInterface[reconcile.Request], o client.Object) {
	g, ok := o.(*v1.Gazette)
	if !ok {
		return
	}
	var peers v1.GazetteList
	if err := r.List(ctx, &peers, client.MatchingFields{domainIndex: domain.Normalize(g.Spec.Domain)}); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to list Gazettes for domain", "domain", g.Spec.Domain)
		return
	}
	for i := range peers.Items {
		q.Add(reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&peers.Items[i])})
	}
}
