/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package controller

import (
	"slices"
	"strings"
	"testing"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
)

func TestEditionsToPrune(t *testing.T) {
	// edition returns an Edition whose content is about kb KiB.
	edition := func(n int64, kb int) v1.Edition {
		return v1.Edition{Spec: v1.EditionSpec{Number: n, Snapshot: v1.EditionSnapshot{
			Content: v1.DispatchContent{Body: strings.Repeat("x", kb<<10)}}}}
	}
	history := func(kbs ...int) []v1.Edition {
		es := make([]v1.Edition, 0, len(kbs))
		for i, kb := range kbs {
			es = append(es, edition(int64(i+1), kb))
		}
		return es
	}
	tests := []struct {
		name     string
		editions []v1.Edition
		limit    int
		maxBytes int64
		want     []int
	}{
		{"within both", history(1, 1, 1), 5, 1 << 20, nil},
		{"over the count", history(1, 1, 1, 1), 2, 1 << 20, []int{0, 1}},
		{"over the size, counted from the newest", history(10, 10, 10, 10), 20, 25 << 10, []int{0, 1}},
		{"the newest is kept even when too large", history(10, 600), 20, 512 << 10, []int{0}},
		{"no size bound", history(500, 500, 500), 20, 0, nil},
		{"a large old version goes first", history(400, 1, 1), 20, 100 << 10, []int{0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := editionsToPrune(tt.editions, tt.limit, tt.maxBytes); !slices.Equal(got, tt.want) {
				t.Errorf("editionsToPrune() = %v, want %v", got, tt.want)
			}
		})
	}
}
