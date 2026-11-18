/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// ContractVersion is the version of the template contract: the template
// names, the view model and the FuncMap a theme can rely on.
// +kubebuilder:validation:Enum=v1
type ContractVersion string

// ContractV1 requires layout.html, dispatch.html and index.html.
const ContractV1 ContractVersion = "v1"

// InlineTypesetSource holds a whole theme in the resource, so an update is
// atomic.
type InlineTypesetSource struct {
	// templates are html/template files keyed by file name. Contract v1
	// requires layout.html, dispatch.html and index.html.
	// +kubebuilder:validation:MaxProperties=32
	// +required
	Templates map[string]string `json:"templates"`

	// assets are static files keyed by file name, inlined with
	// {{ asset "style.css" }}.
	// +kubebuilder:validation:MaxProperties=32
	// +optional
	Assets map[string]string `json:"assets,omitempty"`
}

// TypesetSource is where a theme's files come from. Exactly one source is set.
// +kubebuilder:validation:XValidation:rule="has(self.inline)",message="exactly one source must be set"
type TypesetSource struct {
	// inline holds the theme's files in the resource.
	// +optional
	Inline *InlineTypesetSource `json:"inline,omitempty"`
}

// TypesetSpec defines a theme.
type TypesetSpec struct {
	// contractVersion is the template contract the theme is written for.
	// +kubebuilder:default=v1
	// +optional
	ContractVersion ContractVersion `json:"contractVersion,omitempty"`

	// source holds the theme's templates and assets.
	// +required
	Source TypesetSource `json:"source"`

	// params are the theme's default parameters, available as .Params.
	// A Gazette's typesetParams override them.
	// +kubebuilder:validation:MaxProperties=64
	// +optional
	Params map[string]string `json:"params,omitempty"`
}

// TypesetStatus is the state of a theme as observed by press.
type TypesetStatus struct {
	// observedGeneration is the metadata.generation that the status reflects.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// conditions describe the theme's current state. "Ready" is False with
	// reason TemplateError when the templates do not parse; herald then keeps
	// using the last version that did.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=theme,categories=manifesto
// +kubebuilder:printcolumn:name="Contract",type=string,JSONPath=`.spec.contractVersion`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Typeset is a theme available to the Gazettes of its namespace. Use a
// ClusterTypeset for a theme shared by every namespace.
type Typeset struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the theme
	// +required
	Spec TypesetSpec `json:"spec"`

	// status is the state of the theme as observed by press
	// +optional
	Status TypesetStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// TypesetList contains a list of Typeset
type TypesetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Typeset `json:"items"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=ctheme,categories=manifesto
// +kubebuilder:printcolumn:name="Contract",type=string,JSONPath=`.spec.contractVersion`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ClusterTypeset is a theme available to the Gazettes of every namespace.
type ClusterTypeset struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the theme
	// +required
	Spec TypesetSpec `json:"spec"`

	// status is the state of the theme as observed by press
	// +optional
	Status TypesetStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// ClusterTypesetList contains a list of ClusterTypeset
type ClusterTypesetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []ClusterTypeset `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &Typeset{}, &TypesetList{}, &ClusterTypeset{}, &ClusterTypesetList{})
		return nil
	})
}
