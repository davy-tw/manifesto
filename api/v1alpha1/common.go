/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package v1alpha1

// Label keys used to classify Dispatches. Section and Stamp are stored as
// labels so they can be queried with label selectors.
const (
	// SectionLabel holds the name of the Dispatch's Section (one per Dispatch).
	SectionLabel = "section." + GroupName + "/name"
	// StampLabelPrefix is followed by "/<stamp name>" with an empty value,
	// one label per Stamp.
	StampLabelPrefix = "stamp." + GroupName
)

// Condition types shared by Manifesto resources.
const (
	// ConditionReady is True when every reference the resource depends on
	// exists and the resource can be served.
	ConditionReady = "Ready"
	// ConditionRendered reports whether press could trial-render a Dispatch.
	ConditionRendered = "Rendered"
	// ConditionRouted reports whether the Gateway serves a Gazette's domain.
	// It is only set when press manages routing.
	ConditionRouted = "Routed"
)

// Condition reasons shared by Manifesto resources.
const (
	ReasonResolved         = "Resolved"
	ReasonGazetteNotFound  = "GazetteNotFound"
	ReasonScribeNotFound   = "ScribeNotFound"
	ReasonRenderSucceeded  = "RenderSucceeded"
	ReasonDomainConflict   = "DomainConflict"
	ReasonTypesetNotFound  = "TypesetNotFound"
	ReasonTemplateError    = "TemplateError"
	ReasonTemplatesParsed  = "TemplatesParsed"
	ReasonParentNotFound   = "ParentNotFound"
	ReasonParentCycle      = "ParentCycle"
	ReasonReconcileFailing = "ReconcileFailing"
	ReasonProgrammed       = "Programmed"
	ReasonArchiveNotFound  = "ArchiveNotFound"
	ReasonArchiveNotReady  = "ArchiveNotReady"
	ReasonBackendReady     = "BackendReady"
	ReasonBackendError     = "BackendError"
	ReasonUploadExpired    = "UploadExpired"
	ReasonPrintNotReady    = "PrintNotReady"
	ReasonRoutePending     = "RoutePending"
)

// PrintFinalizer lets press delete a Print's stored object, outside the
// cluster, once no other Print in the namespace uses it.
const PrintFinalizer = GroupName + "/stored-object"

// FieldManagerPress is the Server-Side Apply field manager used by press.
const FieldManagerPress = "manifesto-press"
