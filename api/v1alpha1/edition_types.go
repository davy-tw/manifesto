/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// EditionDispatchLabel holds the name of the Dispatch an Edition belongs to.
const EditionDispatchLabel = GroupName + "/dispatch"

// EditionSnapshot is the content of a Dispatch at one point in time: what an
// author writes. Publishing (state, publishAt), the Gazette, and the Section
// and Stamp labels are not part of an Edition.
type EditionSnapshot struct {
	// title is the headline.
	// +kubebuilder:validation:MaxLength=256
	// +optional
	Title string `json:"title,omitempty"`

	// summary is the short description.
	// +kubebuilder:validation:MaxLength=1024
	// +optional
	Summary string `json:"summary,omitempty"`

	// cover is the cover image, "print:<name>".
	// +optional
	Cover PrintRef `json:"cover,omitempty"`

	// scribeRef is the author.
	// +kubebuilder:validation:MaxLength=253
	// +optional
	ScribeRef string `json:"scribeRef,omitempty"`

	// content is the body.
	// +optional
	Content DispatchContent `json:"content,omitzero"`
}

// EditionSpec is one revision of a Dispatch. press writes it; it never
// changes afterwards.
type EditionSpec struct {
	// dispatchRef is the name of the Dispatch, in the same namespace.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +required
	DispatchRef string `json:"dispatchRef"`

	// number counts the Dispatch's Editions from 1, in the order press saw
	// the changes.
	// +kubebuilder:validation:Minimum=1
	// +required
	Number int64 `json:"number"`

	// dispatchGeneration is the Dispatch's metadata.generation the snapshot
	// was taken from.
	// +optional
	DispatchGeneration int64 `json:"dispatchGeneration,omitempty"`

	// digest identifies the snapshot's content: "sha256:<hex>".
	// +kubebuilder:validation:MaxLength=71
	// +optional
	Digest string `json:"digest,omitempty"`

	// snapshot is the content of the Dispatch.
	// +required
	Snapshot EditionSnapshot `json:"snapshot"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=revision,categories=manifesto
// +kubebuilder:printcolumn:name="Dispatch",type=string,JSONPath=`.spec.dispatchRef`
// +kubebuilder:printcolumn:name="Number",type=integer,JSONPath=`.spec.number`
// +kubebuilder:printcolumn:name="Title",type=string,JSONPath=`.spec.snapshot.title`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="self.spec == oldSelf.spec",message="an Edition is a record of the past and cannot be changed"

// Edition is a revision of a Dispatch. press creates one each time the
// content of a Dispatch changes and keeps the most recent ones; deleting the
// Dispatch deletes its Editions. Restoring an Edition means copying its
// snapshot back into the Dispatch, which in turn becomes a new Edition.
type Edition struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec is the recorded revision
	// +required
	Spec EditionSpec `json:"spec"`
}

// +kubebuilder:object:root=true

// EditionList contains a list of Edition
type EditionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Edition `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &Edition{}, &EditionList{})
		return nil
	})
}
