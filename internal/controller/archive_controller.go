/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package controller

import (
	"context"
	"errors"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
)

// ArchiveReconciler checks that each Archive (or, with Cluster, each
// ClusterArchive) has a usable backend.
type ArchiveReconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	Media   Media
	Cluster bool
}

// +kubebuilder:rbac:groups=manifesto.davy.tw,resources=archives;clusterarchives,verbs=get;list;watch
// +kubebuilder:rbac:groups=manifesto.davy.tw,resources=archives/status;clusterarchives/status,verbs=get;patch

// Reconcile checks one Archive. An unreachable backend is checked again
// every minute.
func (r *ArchiveReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var obj client.Object
	var spec *v1.ArchiveSpec
	var current *v1.ArchiveStatus
	kind := "Archive"
	if r.Cluster {
		a := &v1.ClusterArchive{}
		obj, spec, current, kind = a, &a.Spec, &a.Status, "ClusterArchive"
	} else {
		a := &v1.Archive{}
		obj, spec, current = a, &a.Spec, &a.Status
	}
	if err := r.Get(ctx, req.NamespacedName, obj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	ready := metav1.Condition{
		Type: v1.ConditionReady, Status: metav1.ConditionTrue, Reason: v1.ReasonBackendReady,
		Message: "Backend reachable", ObservedGeneration: obj.GetGeneration(),
	}
	var result ctrl.Result
	driver, err := r.Media.driverFor(spec)
	if err == nil {
		err = driver.Check(ctx)
		result.RequeueAfter = time.Minute
	}
	if err != nil {
		ready.Status, ready.Reason, ready.Message = metav1.ConditionFalse, v1.ReasonBackendError, err.Error()
		if ea, ok := errors.AsType[*errArchive](err); ok {
			ready.Reason = ea.reason
		}
	}
	status := v1.ArchiveStatus{
		ObservedGeneration: obj.GetGeneration(),
		Conditions:         append([]metav1.Condition(nil), current.Conditions...),
	}
	meta.SetStatusCondition(&status.Conditions, ready)
	if !equality.Semantic.DeepEqual(&status, current) {
		if err := applyStatus(ctx, r.Client, kind, obj, &status); err != nil {
			return ctrl.Result{}, err
		}
	}
	return result, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ArchiveReconciler) SetupWithManager(mgr ctrl.Manager) error {
	b := ctrl.NewControllerManagedBy(mgr)
	if r.Cluster {
		b = b.For(&v1.ClusterArchive{}).Named("clusterarchive")
	} else {
		b = b.For(&v1.Archive{}).Named("archive")
	}
	return b.Complete(r)
}
