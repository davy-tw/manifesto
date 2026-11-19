/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
)

// DefaultEditionLimit is how many Editions press keeps per Dispatch.
const DefaultEditionLimit = 20

// EditionReconciler records an Edition each time the content of a Dispatch
// changes, and keeps the most recent Limit of them.
type EditionReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// Limit is how many Editions to keep per Dispatch; 0 or less turns
	// Editions off (existing ones are left alone).
	Limit int
}

// +kubebuilder:rbac:groups=manifesto.davy.tw,resources=editions,verbs=get;list;watch;create;patch;delete

// Reconcile is idempotent: an Edition is only written when the Dispatch's
// content differs from its latest Edition, and its name follows from its
// number, so a repeated attempt cannot record the same change twice.
func (r *EditionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if r.Limit <= 0 {
		return ctrl.Result{}, nil
	}
	var d v1.Dispatch
	if err := r.Get(ctx, req.NamespacedName, &d); err != nil {
		// Deleting a Dispatch garbage-collects its Editions.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !d.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	editions, err := r.editionsOf(ctx, &d)
	if err != nil {
		return ctrl.Result{}, err
	}
	snapshot := SnapshotOf(&d.Spec)
	digest := Digest(snapshot)
	if n := len(editions); n == 0 || editions[n-1].Spec.Digest != digest {
		number := int64(1)
		if n > 0 {
			number = editions[n-1].Spec.Number + 1
		}
		e := &v1.Edition{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: d.Namespace,
				Name:      EditionName(d.Name, number),
				Labels:    map[string]string{v1.EditionDispatchLabel: d.Name, labelManagedBy: v1.FieldManagerPress},
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: v1.GroupVersion.String(), Kind: "Dispatch", Name: d.Name, UID: d.UID, Controller: new(true),
				}},
			},
			Spec: v1.EditionSpec{
				DispatchRef: d.Name, Number: number, DispatchGeneration: d.Generation,
				Digest: digest, Snapshot: snapshot,
			},
		}
		// Create, not apply: an Edition never changes, and if the name is
		// taken the cache was behind; the retry will see it.
		if err := r.Create(ctx, e); err != nil {
			if apierrors.IsAlreadyExists(err) {
				return ctrl.Result{RequeueAfter: time.Second}, nil
			}
			return ctrl.Result{}, err
		}
		logf.FromContext(ctx).Info("Recorded an Edition", "edition", e.Name)
		editions = append(editions, *e)
	}
	for i := 0; i < len(editions)-r.Limit; i++ {
		old := &editions[i]
		if err := r.Delete(ctx, old, client.Preconditions{UID: &old.UID}); client.IgnoreNotFound(err) != nil &&
			!apierrors.IsConflict(err) {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

// editionsOf lists the Editions of d, oldest first. Editions of an earlier
// Dispatch with the same name are left to the garbage collector.
func (r *EditionReconciler) editionsOf(ctx context.Context, d *v1.Dispatch) ([]v1.Edition, error) {
	var list v1.EditionList
	if err := r.List(ctx, &list, client.InNamespace(d.Namespace),
		client.MatchingLabels{v1.EditionDispatchLabel: d.Name}); err != nil {
		return nil, err
	}
	editions := slices.DeleteFunc(list.Items, func(e v1.Edition) bool {
		owner := metav1.GetControllerOf(&e)
		return owner == nil || owner.UID != d.UID || !e.DeletionTimestamp.IsZero()
	})
	slices.SortFunc(editions, func(a, b v1.Edition) int { return int(a.Spec.Number - b.Spec.Number) })
	return editions, nil
}

// SnapshotOf is the part of a Dispatch an Edition records.
func SnapshotOf(s *v1.DispatchSpec) v1.EditionSnapshot {
	return v1.EditionSnapshot{
		Title: s.Title, Summary: s.Summary, Cover: s.Cover, ScribeRef: s.ScribeRef, Content: s.Content,
	}
}

// Digest identifies a snapshot's content.
func Digest(s v1.EditionSnapshot) string {
	b, _ := json.Marshal(s)
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// EditionName is the name of a Dispatch's Edition number n.
func EditionName(dispatch string, n int64) string {
	return fmt.Sprintf("%s-%d", dispatch, n)
}

// SetupWithManager sets up the controller with the Manager.
func (r *EditionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1.Dispatch{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		// Someone deleting an Edition is no reason to record one, but its
		// Dispatch is checked again so the history stays within the limit.
		Owns(&v1.Edition{}, builder.WithPredicates(predicate.Funcs{
			CreateFunc: func(event.CreateEvent) bool { return false },
			UpdateFunc: func(event.UpdateEvent) bool { return false },
		})).
		Named("edition").
		Complete(r)
}
