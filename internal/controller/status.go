/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package controller

import (
	"context"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
)

// applyStatus writes status to the status subresource of the named object
// with Server-Side Apply, as the press field manager. Fields press owned but
// left out of status are removed.
func applyStatus(ctx context.Context, c client.Client, kind string, obj client.Object, status any) error {
	s, err := runtime.DefaultUnstructuredConverter.ToUnstructured(status)
	if err != nil {
		return err
	}
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": v1.GroupVersion.String(),
		"kind":       kind,
		"metadata": map[string]any{
			"name":      obj.GetName(),
			"namespace": obj.GetNamespace(),
		},
		"status": s,
	}}
	return c.Status().Apply(ctx, client.ApplyConfigurationFromUnstructured(u),
		client.FieldOwner(v1.FieldManagerPress), client.ForceOwnership)
}
