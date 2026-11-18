/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// StampSpec defines a tag.
type StampSpec struct {
	// displayName is the name shown to visitors. Without it, the Stamp's
	// metadata.name is shown.
	// +kubebuilder:validation:MaxLength=256
	// +optional
	DisplayName string `json:"displayName,omitempty"`

	// description is shown on the Stamp's page.
	// +kubebuilder:validation:MaxLength=4096
	// +optional
	Description string `json:"description,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=tag,categories=manifesto
// +kubebuilder:printcolumn:name="Display Name",type=string,JSONPath=`.spec.displayName`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="self.metadata.name.matches('^[a-z0-9]([-a-z0-9]*[a-z0-9])?$') && size(self.metadata.name) <= 63",message="metadata.name is used in label keys and URLs and must be a DNS label"

// Stamp is a tag. A Dispatch carries any number of Stamps as labels
// stamp.manifesto.davy.tw/<name>: "". The Stamp resource is optional: it
// adds a display name and a description.
type Stamp struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the tag
	// +optional
	Spec StampSpec `json:"spec,omitzero"`
}

// +kubebuilder:object:root=true

// StampList contains a list of Stamp
type StampList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Stamp `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &Stamp{}, &StampList{})
		return nil
	})
}
