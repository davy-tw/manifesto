/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package controller

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
	"go.pkgs.davy.tw/manifesto/internal/signing"
)

// +kubebuilder:rbac:groups="",namespace=manifesto-system,resources=secrets,verbs=get;create

// EnsureSigningKey returns the key press signs darkroom requests with,
// creating the Secret darkroom mounts the first time press starts. To
// rotate, move the current key to another data key (e.g. "previous") and
// put a new one under "current": darkroom accepts both until the old one is
// removed.
func EnsureSigningKey(ctx context.Context, c client.Client, secret types.NamespacedName) ([]byte, error) {
	return EnsureKey(ctx, c, secret, signing.CurrentKey)
}

// DeskSessionKey is the data key of the Secret holding desk's session key.
const DeskSessionKey = "key"

// EnsureKey returns the random 32-byte key stored under dataKey in secret,
// creating the Secret first if it does not exist. press does this for the
// components that hold no RBAC of their own: darkroom's signing key and
// desk's session key.
func EnsureKey(ctx context.Context, c client.Client, secret types.NamespacedName, dataKey string) ([]byte, error) {
	var s corev1.Secret
	err := c.Get(ctx, secret, &s)
	if apierrors.IsNotFound(err) {
		key, kerr := signing.NewKey()
		if kerr != nil {
			return nil, kerr
		}
		s = corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: secret.Namespace, Name: secret.Name,
				Labels: map[string]string{labelManagedBy: v1.FieldManagerPress},
			},
			Data: map[string][]byte{dataKey: key},
		}
		err = c.Create(ctx, &s)
		if apierrors.IsAlreadyExists(err) {
			// Another press replica created it first.
			err = c.Get(ctx, secret, &s)
		}
	}
	if err != nil {
		return nil, err
	}
	key := s.Data[dataKey]
	if len(key) == 0 {
		return nil, apierrors.NewBadRequest("Secret " + secret.String() + " has no \"" + dataKey + "\" key")
	}
	return key, nil
}
