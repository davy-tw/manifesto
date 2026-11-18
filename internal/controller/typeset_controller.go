/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package controller

import (
	"context"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
	"go.pkgs.davy.tw/manifesto/internal/render"
)

// TypesetReconciler parses each Typeset (or, with Cluster, each
// ClusterTypeset) and reports template errors in its Ready condition.
type TypesetReconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	Cluster bool
}

// +kubebuilder:rbac:groups=manifesto.davy.tw,resources=typesets;clustertypesets,verbs=get;list;watch
// +kubebuilder:rbac:groups=manifesto.davy.tw,resources=typesets/status;clustertypesets/status,verbs=get;patch

// Reconcile parses one theme.
func (r *TypesetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var obj client.Object
	var spec *v1.TypesetSpec
	var current *v1.TypesetStatus
	kind := string(v1.KindTypeset)
	if r.Cluster {
		ts := &v1.ClusterTypeset{}
		obj, spec, current, kind = ts, &ts.Spec, &ts.Status, string(v1.KindClusterTypeset)
	} else {
		ts := &v1.Typeset{}
		obj, spec, current = ts, &ts.Spec, &ts.Status
	}
	if err := r.Get(ctx, req.NamespacedName, obj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	ready := metav1.Condition{
		Type:               v1.ConditionReady,
		Status:             metav1.ConditionTrue,
		Reason:             v1.ReasonTemplatesParsed,
		Message:            "Templates parsed",
		ObservedGeneration: obj.GetGeneration(),
	}
	if _, err := render.ParseTypeset(spec); err != nil {
		ready.Status = metav1.ConditionFalse
		ready.Reason = v1.ReasonTemplateError
		ready.Message = render.TruncateMessage(err.Error())
	}
	status := v1.TypesetStatus{
		ObservedGeneration: obj.GetGeneration(),
		Conditions:         append([]metav1.Condition(nil), current.Conditions...),
	}
	meta.SetStatusCondition(&status.Conditions, ready)
	if equality.Semantic.DeepEqual(&status, current) {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{}, applyStatus(ctx, r.Client, kind, obj, &status)
}

// SetupWithManager sets up the controller with the Manager.
func (r *TypesetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	b := ctrl.NewControllerManagedBy(mgr)
	if r.Cluster {
		b = b.For(&v1.ClusterTypeset{}).Named("clustertypeset")
	} else {
		b = b.For(&v1.Typeset{}).Named("typeset")
	}
	return b.Complete(r)
}
