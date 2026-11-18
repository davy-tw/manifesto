/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package controller

import (
	"testing"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
)

func TestCheckParents(t *testing.T) {
	tests := []struct {
		name    string
		section string
		parents map[string]string
		want    string
	}{
		{"no parent", "tech", map[string]string{"tech": ""}, ""},
		{"parent exists", "go", map[string]string{"go": "tech", "tech": ""}, ""},
		{"deep chain", "a", map[string]string{"a": "b", "b": "c", "c": ""}, ""},
		{"missing parent", "go", map[string]string{"go": "tech"}, v1.ReasonParentNotFound},
		{"missing grandparent", "go", map[string]string{"go": "tech", "tech": "root"}, v1.ReasonParentNotFound},
		{"two-cycle", "a", map[string]string{"a": "b", "b": "a"}, v1.ReasonParentCycle},
		{"cycle above", "a", map[string]string{"a": "b", "b": "c", "c": "b"}, v1.ReasonParentCycle},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, _ := checkParents(tt.section, tt.parents); got != tt.want {
				t.Errorf("checkParents() = %q, want %q", got, tt.want)
			}
		})
	}
}
