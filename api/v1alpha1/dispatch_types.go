/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// DispatchState is the state an author asks a Dispatch to be in.
// +kubebuilder:validation:Enum=Draft;Published;Unlisted;Retracted
type DispatchState string

const (
	// StateDraft keeps the Dispatch private to the namespace.
	StateDraft DispatchState = "Draft"
	// StatePublished shows the Dispatch everywhere once publishAt has passed.
	StatePublished DispatchState = "Published"
	// StateUnlisted serves the Dispatch at its URL once publishAt has passed,
	// but leaves it out of listings, the Wire feed and the sitemap.
	StateUnlisted DispatchState = "Unlisted"
	// StateRetracted withdraws the Dispatch; herald answers 410 Gone.
	StateRetracted DispatchState = "Retracted"
)

// DispatchPhase is the state of a Dispatch as decided by press.
// +kubebuilder:validation:Enum=Draft;Embargoed;Published;Unlisted;Retracted
type DispatchPhase string

const (
	PhaseDraft DispatchPhase = "Draft"
	// PhaseEmbargoed means the Dispatch is Published or Unlisted but has never
	// been visible and its publishAt is still in the future.
	PhaseEmbargoed DispatchPhase = "Embargoed"
	PhasePublished DispatchPhase = "Published"
	PhaseUnlisted  DispatchPhase = "Unlisted"
	PhaseRetracted DispatchPhase = "Retracted"
)

// ContentFormat is the markup language of a Dispatch body.
// +kubebuilder:validation:Enum=markdown
type ContentFormat string

const (
	// FormatMarkdown is CommonMark with GitHub Flavored Markdown extensions.
	FormatMarkdown ContentFormat = "markdown"
)

// MaxBodyBytes is the largest Dispatch body accepted, well below etcd's
// ~1.5MB object limit.
const MaxBodyBytes = 512 * 1024

// DispatchContent is the body of a Dispatch, stored inline.
type DispatchContent struct {
	// format is the markup language of body.
	// +kubebuilder:default=markdown
	// +optional
	Format ContentFormat `json:"format,omitempty"`

	// body is the article text. Media is referenced as "print:<name>", for
	// example ![cover](print:hero-image).
	// +kubebuilder:validation:MaxLength=524288
	// +optional
	Body string `json:"body,omitempty"`
}

// DispatchSpec defines the desired state of a Dispatch.
type DispatchSpec struct {
	// gazetteRef is the name of the Gazette, in the same namespace, that this
	// Dispatch is published in. A Dispatch belongs to exactly one Gazette.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	// +required
	GazetteRef string `json:"gazetteRef"`

	// scribeRef is the name of the Scribe (author), in the same namespace,
	// who wrote this Dispatch.
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	// +optional
	ScribeRef string `json:"scribeRef,omitempty"`

	// title is the headline of the Dispatch.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	// +required
	Title string `json:"title"`

	// summary is a short description used in listings and the Wire feed.
	// +kubebuilder:validation:MaxLength=1024
	// +optional
	Summary string `json:"summary,omitempty"`

	// cover is the cover image, as a reference to a Print: "print:<name>".
	// +optional
	Cover PrintRef `json:"cover,omitempty"`

	// state is the state the author asks for: Draft, Published, Unlisted or
	// Retracted. press reports the resulting status.phase, which is Embargoed
	// while a Published or Unlisted Dispatch waits for publishAt.
	// +kubebuilder:default=Draft
	// +optional
	State DispatchState `json:"state,omitempty"`

	// publishAt is when a Published or Unlisted Dispatch first becomes
	// visible. Unset or in the past means immediately. It is ignored for Draft
	// and Retracted, and once the Dispatch has been visible (status.publishedAt
	// is set) moving it into the future does not hide the Dispatch again.
	// +optional
	PublishAt *metav1.Time `json:"publishAt,omitempty"`

	// content is the body of the Dispatch.
	// +optional
	Content DispatchContent `json:"content,omitzero"`
}

// DispatchStatus is the state of a Dispatch as observed by press.
type DispatchStatus struct {
	// phase is the state press decided on: Draft, Embargoed, Published,
	// Unlisted or Retracted.
	// +optional
	Phase DispatchPhase `json:"phase,omitempty"`

	// publishedAt is when the Dispatch first became visible. It never changes
	// once set.
	// +optional
	PublishedAt *metav1.Time `json:"publishedAt,omitempty"`

	// url is the public address of the Dispatch.
	// +optional
	URL string `json:"url,omitempty"`

	// observedGeneration is the metadata.generation that the status reflects.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// conditions describe the Dispatch's current state. "Ready" is False when
	// the Gazette or Scribe it refers to does not exist; "Rendered" is False
	// when its content or theme fails to render.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=post,categories=manifesto
// +kubebuilder:printcolumn:name="Title",type=string,JSONPath=`.spec.title`
// +kubebuilder:printcolumn:name="Gazette",type=string,JSONPath=`.spec.gazetteRef`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Published",type=string,JSONPath=`.status.publishedAt`
// +kubebuilder:printcolumn:name="PublishAt",type=string,JSONPath=`.spec.publishAt`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="self.metadata.name.matches('^[a-z0-9]([-a-z0-9]*[a-z0-9])?$') && size(self.metadata.name) <= 63",message="metadata.name is the URL slug and must be a DNS label: lowercase letters, digits and '-', at most 63 characters"

// Dispatch is an article, published in exactly one Gazette. Its
// metadata.name is the URL slug and its body is stored inline.
type Dispatch struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of the Dispatch
	// +required
	Spec DispatchSpec `json:"spec"`

	// status is the state of the Dispatch as observed by press
	// +optional
	Status DispatchStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// DispatchList contains a list of Dispatch
type DispatchList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Dispatch `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &Dispatch{}, &DispatchList{})
		return nil
	})
}
