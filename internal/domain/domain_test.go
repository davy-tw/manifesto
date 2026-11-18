/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package domain

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
)

func gazette(ns, name, dom string, created time.Time) v1.Gazette {
	return v1.Gazette{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, CreationTimestamp: metav1.NewTime(created)},
		Spec:       v1.GazetteSpec{Domain: dom},
	}
}

func TestOwners(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	deleting := gazette("a", "deleting", "x.io", t0.Add(-time.Hour))
	now := metav1.Now()
	deleting.DeletionTimestamp = &now

	tests := []struct {
		name     string
		gazettes []v1.Gazette
		want     map[string]string // domain -> namespace/name
	}{
		{"oldest wins", []v1.Gazette{gazette("b", "new", "x.io", t0.Add(time.Minute)), gazette("a", "old", "x.io", t0)},
			map[string]string{"x.io": "a/old"}},
		{"tie broken by namespace", []v1.Gazette{gazette("z", "g", "x.io", t0), gazette("a", "g", "x.io", t0)},
			map[string]string{"x.io": "a/g"}},
		{"tie broken by name", []v1.Gazette{gazette("a", "b", "x.io", t0), gazette("a", "a", "x.io", t0)},
			map[string]string{"x.io": "a/a"}},
		{"separate domains", []v1.Gazette{gazette("a", "one", "one.io", t0), gazette("b", "two", "two.io", t0)},
			map[string]string{"one.io": "a/one", "two.io": "b/two"}},
		{"deleting gazette gives up its domain", []v1.Gazette{deleting, gazette("b", "next", "x.io", t0)},
			map[string]string{"x.io": "b/next"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Owners(tt.gazettes)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d domains, want %d", len(got), len(tt.want))
			}
			for d, want := range tt.want {
				g := got[d]
				if g == nil || g.Namespace+"/"+g.Name != want {
					t.Errorf("owner of %s = %v, want %s", d, g, want)
				}
			}
		})
	}
}

func TestNormalize(t *testing.T) {
	tests := map[string]string{
		"alice.example.io":      "alice.example.io",
		"Alice.Example.IO":      "alice.example.io",
		"alice.example.io:8080": "alice.example.io",
		"alice.example.io.":     "alice.example.io",
		"localhost:8080":        "localhost",
		"[::1]:8080":            "[::1]",
		"[::1]":                 "[::1]",
	}
	for in, want := range tests {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}
