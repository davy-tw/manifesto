/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// SectionSpec defines a category.
type SectionSpec struct {
	// displayName is the name shown to visitors. Without it, the Section's
	// metadata.name is shown.
	// +kubebuilder:validation:MaxLength=256
	// +optional
	DisplayName string `json:"displayName,omitempty"`

	// description is shown on the Section's page.
	// +kubebuilder:validation:MaxLength=4096
	// +optional
	Description string `json:"description,omitempty"`

	// parent is the name of the enclosing Section, in the same namespace.
	// A Section's page also lists the Dispatches of its descendants.
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +optional
	Parent string `json:"parent,omitempty"`
}

// SectionStatus is the state of a Section as observed by press.
type SectionStatus struct {
	// observedGeneration is the metadata.generation that the status reflects.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// conditions describe the Section's current state. "Ready" is False when
	// the parent does not exist or the parents form a cycle.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=category,categories=manifesto
// +kubebuilder:printcolumn:name="Display Name",type=string,JSONPath=`.spec.displayName`
// +kubebuilder:printcolumn:name="Parent",type=string,JSONPath=`.spec.parent`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="self.metadata.name.matches('^[a-z0-9]([-a-z0-9]*[a-z0-9])?$') && size(self.metadata.name) <= 63",message="metadata.name is used in label values and URLs and must be a DNS label"
// +kubebuilder:validation:XValidation:rule="!has(self.spec) || !has(self.spec.parent) || self.spec.parent != self.metadata.name",message="a Section cannot be its own parent"

// Section is a category. A Dispatch belongs to at most one Section, named by
// the label section.manifesto.davy.tw/name. The Section resource is
// optional: it adds a display name, a description and a parent.
type Section struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the category
	// +optional
	Spec SectionSpec `json:"spec,omitzero"`

	// status is the state of the Section as observed by press
	// +optional
	Status SectionStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// SectionList contains a list of Section
type SectionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Section `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &Section{}, &SectionList{})
		return nil
	})
}
