/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package controller

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
)

// MaxEmbargoWait caps how long an Embargoed Dispatch waits before press
// checks it again, so a lost queue item or a clock change self-corrects
// within an hour.
const MaxEmbargoWait = time.Hour

// phaseDecision is what press decides for a Dispatch at a point in time.
type phaseDecision struct {
	Phase v1.DispatchPhase
	// PublishedAt is the first time the Dispatch became visible; nil if it
	// never has.
	PublishedAt *metav1.Time
	// RequeueAfter is non-zero while the Dispatch is Embargoed.
	RequeueAfter time.Duration
}

// decidePhase applies the Dispatch state rules:
//
//   - Draft and Retracted map to the same phase and ignore publishAt.
//   - Published and Unlisted are Embargoed until publishAt, unless the
//     Dispatch has been visible before (status.publishedAt is set).
//   - status.publishedAt is written the first time the Dispatch becomes
//     visible and never changes afterwards. It is publishAt when that has
//     passed (so a back-dated or late-reconciled Dispatch keeps its intended
//     date), and now otherwise.
func decidePhase(spec v1.DispatchSpec, status v1.DispatchStatus, now time.Time) phaseDecision {
	d := phaseDecision{PublishedAt: status.PublishedAt}
	var visible v1.DispatchPhase
	switch spec.State {
	case v1.StatePublished:
		visible = v1.PhasePublished
	case v1.StateUnlisted:
		visible = v1.PhaseUnlisted
	case v1.StateRetracted:
		d.Phase = v1.PhaseRetracted
		return d
	default: // Draft, or unset on objects created before defaulting.
		d.Phase = v1.PhaseDraft
		return d
	}

	if status.PublishedAt == nil && spec.PublishAt != nil && now.Before(spec.PublishAt.Time) {
		d.Phase = v1.PhaseEmbargoed
		d.RequeueAfter = min(spec.PublishAt.Sub(now), MaxEmbargoWait)
		return d
	}

	d.Phase = visible
	if d.PublishedAt == nil {
		at := metav1.NewTime(now)
		if spec.PublishAt != nil {
			at = *spec.PublishAt
		}
		// Store at second precision, as the API server does.
		at = metav1.NewTime(at.Truncate(time.Second))
		d.PublishedAt = &at
	}
	return d
}
