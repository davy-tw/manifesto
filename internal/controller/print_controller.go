/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package controller

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
	"go.pkgs.davy.tw/manifesto/internal/storage"
)

// pendingPoll is how often press checks whether a Pending Print's file has
// arrived. darkroom and S3 cannot notify press, so it polls until the upload
// URL expires.
const pendingPoll = 2 * time.Second

// Condition reasons specific to Prints.
const (
	reasonStored         = "Stored"
	reasonAwaitingUpload = "AwaitingUpload"
	reasonUploadRejected = "UploadRejected"
)

// PrintReconciler tracks each Print's file: it hands out a signed upload
// URL, notices when the file is stored, and deletes the stored file once no
// Print in the namespace uses it.
type PrintReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Media  Media
	Clock  clock.PassiveClock
}

// +kubebuilder:rbac:groups=manifesto.davy.tw,resources=prints,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=manifesto.davy.tw,resources=prints/status,verbs=get;patch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch

// Reconcile moves one Print through Pending, Ready and Failed.
func (r *PrintReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var p v1.Print
	if err := r.Get(ctx, req.NamespacedName, &p); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	var ns corev1.Namespace
	if err := r.Get(ctx, types.NamespacedName{Name: p.Namespace}, &ns); err != nil {
		return ctrl.Result{}, err
	}
	objectKey := storage.ObjectKey(string(ns.UID), p.Spec.SHA256, p.Spec.ContentType)

	var driver storage.Driver
	spec, err := getArchive(ctx, r.Client, p.Namespace, p.Spec.ArchiveRef)
	if err == nil {
		archiveNS := p.Namespace
		if p.Spec.ArchiveRef.Kind != v1.KindArchive {
			archiveNS = ""
		}
		driver, err = r.Media.driverFor(ctx, spec, archiveNS)
	}

	if p.DeletionTimestamp != nil {
		return ctrl.Result{}, r.finalize(ctx, &p, objectKey, driver, err)
	}
	if !slices.Contains(p.Finalizers, v1.PrintFinalizer) {
		if err := r.setFinalizer(ctx, &p, true); err != nil {
			return ctrl.Result{}, err
		}
	}

	status := v1.PrintStatus{
		Phase:              p.Status.Phase,
		UploadURL:          p.Status.UploadURL,
		UploadHeaders:      p.Status.UploadHeaders,
		UploadExpiresAt:    p.Status.UploadExpiresAt,
		ObjectKey:          objectKey,
		URL:                p.Status.URL,
		ObservedGeneration: p.Generation,
		Conditions:         append([]metav1.Condition(nil), p.Status.Conditions...),
	}
	if status.Phase == "" {
		status.Phase = v1.PrintPending
	}
	ready := metav1.Condition{Type: v1.ConditionReady, ObservedGeneration: p.Generation}
	var result ctrl.Result

	switch {
	case err != nil:
		ready.Status, ready.Reason, ready.Message = metav1.ConditionFalse, v1.ReasonArchiveNotReady, err.Error()
		if ea, ok := errors.AsType[*errArchive](err); ok && ea.reason == v1.ReasonArchiveNotFound {
			ready.Reason = v1.ReasonArchiveNotFound
		}
	case status.Phase == v1.PrintFailed:
		ready.Status, ready.Reason = metav1.ConditionFalse, v1.ReasonUploadExpired
		ready.Message = "The file was not uploaded in time; delete this Print and create it again"
	default:
		// A Ready Print's file was checked when it became Ready.
		verified := p.Status.Phase == v1.PrintReady && p.Status.ObjectKey == objectKey
		result, err = r.track(ctx, &status, &ready, driver, objectKey, p.Spec.ContentType, p.Spec.Size, verified)
		if err != nil {
			return ctrl.Result{}, err
		}
	}
	meta.SetStatusCondition(&status.Conditions, ready)
	if !equality.Semantic.DeepEqual(status, p.Status) {
		if err := applyStatus(ctx, r.Client, "Print", &p, &status); err != nil {
			return ctrl.Result{}, err
		}
	}
	return result, nil
}

// track checks whether the file is stored and otherwise keeps an upload
// URL available until it expires.
func (r *PrintReconciler) track(ctx context.Context, status *v1.PrintStatus, ready *metav1.Condition,
	driver storage.Driver, objectKey, contentType string, size int64, verified bool) (ctrl.Result, error) {
	stored, err := driver.Stored(ctx, objectKey, size, verified)
	if err != nil {
		ready.Status, ready.Reason, ready.Message = metav1.ConditionFalse, v1.ReasonBackendError, err.Error()
		if storage.IsRejected(err) {
			// The wrong file was uploaded and removed; the URL still works.
			ready.Reason = reasonUploadRejected
			return ctrl.Result{RequeueAfter: pendingPoll}, nil
		}
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}
	if stored {
		// Also covers a file another Print of the namespace already stored.
		status.Phase, status.URL = v1.PrintReady, driver.PublicURL(objectKey)
		status.UploadURL, status.UploadHeaders, status.UploadExpiresAt = "", nil, nil
		ready.Status, ready.Reason, ready.Message = metav1.ConditionTrue, reasonStored, "File stored at status.url"
		return ctrl.Result{}, nil
	}

	now := r.now()
	status.Phase, status.URL = v1.PrintPending, ""
	switch {
	case status.UploadExpiresAt != nil && !now.Before(status.UploadExpiresAt.Time):
		status.Phase, status.UploadURL, status.UploadHeaders, status.UploadExpiresAt = v1.PrintFailed, "", nil, nil
		ready.Status, ready.Reason = metav1.ConditionFalse, v1.ReasonUploadExpired
		ready.Message = "The file was not uploaded in time; delete this Print and create it again"
		return ctrl.Result{}, nil
	case status.UploadURL == "":
		up, err := driver.SignUpload(ctx, objectKey, contentType, size, r.Media.uploadTTL())
		if err != nil {
			return ctrl.Result{}, err
		}
		at := metav1.NewTime(up.Expires)
		status.UploadURL, status.UploadHeaders, status.UploadExpiresAt = up.URL, up.Headers, &at
	}
	ready.Status, ready.Reason = metav1.ConditionFalse, reasonAwaitingUpload
	ready.Message = fmt.Sprintf("PUT the file to status.uploadURL with the headers in status.uploadHeaders before %s",
		status.UploadExpiresAt.UTC().Format(time.RFC3339))
	if prev := meta.FindStatusCondition(status.Conditions, v1.ConditionReady); prev != nil && prev.Reason == reasonUploadRejected {
		// Keep telling the uploader why the last attempt was removed.
		ready.Reason, ready.Message = prev.Reason, prev.Message
	}
	wait := min(pendingPoll, status.UploadExpiresAt.Sub(now))
	return ctrl.Result{RequeueAfter: max(wait, time.Second)}, nil
}

// finalize deletes the stored file unless another Print of the namespace
// still uses it, then releases the Print.
func (r *PrintReconciler) finalize(ctx context.Context, p *v1.Print, objectKey string, driver storage.Driver, archiveErr error) error {
	if !slices.Contains(p.Finalizers, v1.PrintFinalizer) {
		return nil
	}
	log := logf.FromContext(ctx)
	var others v1.PrintList
	if err := r.List(ctx, &others, client.InNamespace(p.Namespace), client.MatchingFields{printObjectIndex: objectKey}); err != nil {
		return err
	}
	shared := false
	for _, o := range others.Items {
		if o.Name != p.Name && o.DeletionTimestamp == nil {
			shared = true
			break
		}
	}
	switch {
	case shared:
		log.Info("Keeping the stored file; another Print uses it", "objectKey", objectKey)
	case archiveErr != nil:
		// Without a usable Archive there is nothing press can delete.
		log.Info("Cannot delete the stored file; releasing the Print anyway", "objectKey", objectKey, "reason", archiveErr.Error())
	default:
		if err := driver.Delete(ctx, objectKey); err != nil {
			return fmt.Errorf("deleting %s: %w", objectKey, err)
		}
		log.Info("Deleted the stored file", "objectKey", objectKey)
	}
	return r.setFinalizer(ctx, p, false)
}

// setFinalizer adds or removes press's finalizer with Server-Side Apply.
func (r *PrintReconciler) setFinalizer(ctx context.Context, p *v1.Print, present bool) error {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(v1.GroupVersion.WithKind("Print"))
	u.SetNamespace(p.Namespace)
	u.SetName(p.Name)
	if present {
		u.SetFinalizers([]string{v1.PrintFinalizer})
	}
	return r.Apply(ctx, client.ApplyConfigurationFromUnstructured(u), client.FieldOwner(v1.FieldManagerPress), client.ForceOwnership)
}

func (r *PrintReconciler) now() time.Time {
	if r.Clock == nil {
		return time.Now()
	}
	return r.Clock.Now()
}

// SetupWithManager sets up the controller with the Manager. The field
// indexes it relies on are registered by Setup.
func (r *PrintReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1.Print{}).
		Watches(&v1.Archive{}, handler.EnqueueRequestsFromMapFunc(r.printsOfArchive)).
		Watches(&v1.ClusterArchive{}, handler.EnqueueRequestsFromMapFunc(r.printsOfArchive)).
		Named("print").
		Complete(r)
}

func (r *PrintReconciler) printsOfArchive(ctx context.Context, o client.Object) []reconcile.Request {
	key := archiveKey(v1.KindArchive, o.GetNamespace(), o.GetName())
	if _, ok := o.(*v1.ClusterArchive); ok {
		key = archiveKey(v1.KindClusterArchive, "", o.GetName())
	}
	var list v1.PrintList
	if err := r.List(ctx, &list, client.MatchingFields{printArchiveIndex: key}); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to list Prints of Archive", "archive", key)
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
	}
	return reqs
}
