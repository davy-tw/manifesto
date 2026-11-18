/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package controller

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
	"go.pkgs.davy.tw/manifesto/internal/domain"
	"go.pkgs.davy.tw/manifesto/internal/render"
)

// domainIndex indexes Gazettes (across namespaces) by normalized spec.domain.
const domainIndex = "spec.domain"

// GazetteReconciler reports whether each Gazette owns its domain.
//
// TODO(M2): create the HTTPRoute and cert-manager Certificate for the domain.
type GazetteReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=manifesto.davy.tw,resources=gazettes,verbs=get;list;watch
// +kubebuilder:rbac:groups=manifesto.davy.tw,resources=gazettes/status,verbs=get;patch

// Reconcile computes the status of one Gazette. When Gazettes in different
// namespaces claim the same domain, the oldest one is Ready and the others
// report DomainConflict.
func (r *GazetteReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var g v1.Gazette
	if err := r.Get(ctx, req.NamespacedName, &g); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	var peers v1.GazetteList
	if err := r.List(ctx, &peers, client.MatchingFields{domainIndex: domain.Normalize(g.Spec.Domain)}); err != nil {
		return ctrl.Result{}, err
	}
	owner := domain.Owners(peers.Items)[domain.Normalize(g.Spec.Domain)]

	ready := metav1.Condition{
		Type:               v1.ConditionReady,
		Status:             metav1.ConditionTrue,
		Reason:             v1.ReasonResolved,
		Message:            fmt.Sprintf("Serving %s", g.Spec.Domain),
		ObservedGeneration: g.Generation,
	}
	if owner != nil && (owner.Namespace != g.Namespace || owner.Name != g.Name) {
		ready.Status = metav1.ConditionFalse
		ready.Reason = v1.ReasonDomainConflict
		ready.Message = fmt.Sprintf("Domain %s is already served by Gazette %s/%s", g.Spec.Domain, owner.Namespace, owner.Name)
	}

	status := v1.GazetteStatus{
		URL:                render.GazetteURL(g.Spec.Domain),
		ObservedGeneration: g.Generation,
		Conditions:         append([]metav1.Condition(nil), g.Status.Conditions...),
	}
	meta.SetStatusCondition(&status.Conditions, ready)
	if equality.Semantic.DeepEqual(status, g.Status) {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{}, applyStatus(ctx, r.Client, "Gazette", &g, &status)
}

// SetupWithManager sets up the controller with the Manager.
func (r *GazetteReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &v1.Gazette{}, domainIndex,
		func(o client.Object) []string { return []string{domain.Normalize(o.(*v1.Gazette).Spec.Domain)} }); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).
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
		Named("gazette").
		Complete(r)
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
