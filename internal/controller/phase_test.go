/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package controller

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
)

func TestDecidePhase(t *testing.T) {
	now := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *metav1.Time { t := metav1.NewTime(now.Add(d)); return &t }

	tests := []struct {
		name            string
		state           v1.DispatchState
		publishAt       *metav1.Time
		publishedAt     *metav1.Time
		wantPhase       v1.DispatchPhase
		wantPublishedAt *metav1.Time
		wantRequeue     time.Duration
	}{
		{name: "draft", state: v1.StateDraft, wantPhase: v1.PhaseDraft},
		{name: "unset state is draft", state: "", wantPhase: v1.PhaseDraft},
		{name: "draft ignores publishAt", state: v1.StateDraft, publishAt: at(-time.Hour), wantPhase: v1.PhaseDraft},
		{name: "draft keeps publishedAt", state: v1.StateDraft, publishedAt: at(-48 * time.Hour),
			wantPhase: v1.PhaseDraft, wantPublishedAt: at(-48 * time.Hour)},
		{name: "retracted", state: v1.StateRetracted, publishAt: at(time.Hour), wantPhase: v1.PhaseRetracted},
		{name: "published now", state: v1.StatePublished, wantPhase: v1.PhasePublished, wantPublishedAt: at(0)},
		{name: "unlisted now", state: v1.StateUnlisted, wantPhase: v1.PhaseUnlisted, wantPublishedAt: at(0)},
		{name: "published, publishAt passed, uses publishAt", state: v1.StatePublished, publishAt: at(-24 * time.Hour),
			wantPhase: v1.PhasePublished, wantPublishedAt: at(-24 * time.Hour)},
		{name: "published, publishAt exactly now", state: v1.StatePublished, publishAt: at(0),
			wantPhase: v1.PhasePublished, wantPublishedAt: at(0)},
		{name: "embargoed soon", state: v1.StatePublished, publishAt: at(10 * time.Minute),
			wantPhase: v1.PhaseEmbargoed, wantRequeue: 10 * time.Minute},
		{name: "embargoed far away waits at most an hour", state: v1.StatePublished, publishAt: at(72 * time.Hour),
			wantPhase: v1.PhaseEmbargoed, wantRequeue: MaxEmbargoWait},
		{name: "unlisted is embargoed too", state: v1.StateUnlisted, publishAt: at(time.Minute),
			wantPhase: v1.PhaseEmbargoed, wantRequeue: time.Minute},
		{name: "already published is not embargoed again", state: v1.StatePublished, publishAt: at(time.Hour),
			publishedAt: at(-time.Hour), wantPhase: v1.PhasePublished, wantPublishedAt: at(-time.Hour)},
		{name: "republish after draft keeps first publishedAt", state: v1.StatePublished,
			publishedAt: at(-48 * time.Hour), wantPhase: v1.PhasePublished, wantPublishedAt: at(-48 * time.Hour)},
		{name: "published to unlisted keeps publishedAt", state: v1.StateUnlisted,
			publishedAt: at(-time.Hour), wantPhase: v1.PhaseUnlisted, wantPublishedAt: at(-time.Hour)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decidePhase(
				v1.DispatchSpec{State: tt.state, PublishAt: tt.publishAt},
				v1.DispatchStatus{PublishedAt: tt.publishedAt},
				now,
			)
			if got.Phase != tt.wantPhase {
				t.Errorf("phase = %q, want %q", got.Phase, tt.wantPhase)
			}
			if got.RequeueAfter != tt.wantRequeue {
				t.Errorf("requeueAfter = %v, want %v", got.RequeueAfter, tt.wantRequeue)
			}
			switch {
			case tt.wantPublishedAt == nil && got.PublishedAt != nil:
				t.Errorf("publishedAt = %v, want nil", got.PublishedAt)
			case tt.wantPublishedAt != nil && (got.PublishedAt == nil || !got.PublishedAt.Equal(tt.wantPublishedAt)):
				t.Errorf("publishedAt = %v, want %v", got.PublishedAt, tt.wantPublishedAt)
			}
		})
	}
}
