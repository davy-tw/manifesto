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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
)

// SectionReconciler checks that each Section's parent exists and that the
// parents do not form a cycle.
type SectionReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=manifesto.davy.tw,resources=sections/status,verbs=get;patch

// Reconcile checks one Section.
func (r *SectionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var s v1.Section
	if err := r.Get(ctx, req.NamespacedName, &s); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	var list v1.SectionList
	if err := r.List(ctx, &list, client.InNamespace(s.Namespace)); err != nil {
		return ctrl.Result{}, err
	}
	parents := map[string]string{}
	for _, o := range list.Items {
		parents[o.Name] = o.Spec.Parent
	}

	ready := metav1.Condition{
		Type:               v1.ConditionReady,
		Status:             metav1.ConditionTrue,
		Reason:             v1.ReasonResolved,
		Message:            "Parent resolved",
		ObservedGeneration: s.Generation,
	}
	if reason, msg := checkParents(s.Name, parents); reason != "" {
		ready.Status, ready.Reason, ready.Message = metav1.ConditionFalse, reason, msg
	}
	status := v1.SectionStatus{
		ObservedGeneration: s.Generation,
		Conditions:         append([]metav1.Condition(nil), s.Status.Conditions...),
	}
	meta.SetStatusCondition(&status.Conditions, ready)
	if equality.Semantic.DeepEqual(status, s.Status) {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{}, applyStatus(ctx, r.Client, "Section", &s, &status)
}

// checkParents walks the parent chain of name. parents maps every existing
// Section to its parent ("" for none).
func checkParents(name string, parents map[string]string) (reason, message string) {
	seen := map[string]bool{name: true}
	for cur := parents[name]; cur != ""; cur = parents[cur] {
		if _, ok := parents[cur]; !ok {
			return v1.ReasonParentNotFound, fmt.Sprintf("Parent Section %q not found", cur)
		}
		if seen[cur] {
			return v1.ReasonParentCycle, fmt.Sprintf("Parents form a cycle through %q", cur)
		}
		seen[cur] = true
	}
	return "", ""
}

// SetupWithManager sets up the controller with the Manager. Any change to a
// Section can affect the chains of the others in its namespace, so all of
// them are requeued.
func (r *SectionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1.Section{}).
		Watches(&v1.Section{}, handler.EnqueueRequestsFromMapFunc(r.sectionsInNamespace)).
		Named("section").
		Complete(r)
}

func (r *SectionReconciler) sectionsInNamespace(ctx context.Context, o client.Object) []reconcile.Request {
	var list v1.SectionList
	if err := r.List(ctx, &list, client.InNamespace(o.GetNamespace())); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to list Sections")
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
	}
	return reqs
}
