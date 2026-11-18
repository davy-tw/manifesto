/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// PrintRef refers to a Print (media file) in the same namespace, written as
// "print:<name>".
// +kubebuilder:validation:Pattern=`^print:[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
// +kubebuilder:validation:MaxLength=259
type PrintRef string

// GazetteSpec defines the desired state of a Gazette.
type GazetteSpec struct {
	// title is the site name shown in page titles, headers and the Wire feed.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	// +required
	Title string `json:"title"`

	// tagline is a short line shown under the title.
	// +kubebuilder:validation:MaxLength=512
	// +optional
	Tagline string `json:"tagline,omitempty"`

	// domain is the host name visitors use to reach this Gazette, for example
	// "alice.example.io". herald picks the Gazette by the request's Host header.
	// When Gazettes in different namespaces claim the same domain, the oldest
	// one wins and the others report DomainConflict.
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	// +required
	Domain string `json:"domain"`

	// logo is the site logo, as a reference to a Print: "print:<name>".
	// +optional
	Logo PrintRef `json:"logo,omitempty"`

	// language is the BCP 47 language tag of the site's content, for example
	// "zh-TW". It is written to the html lang attribute and the Wire feed.
	// +kubebuilder:validation:MaxLength=35
	// +kubebuilder:validation:Pattern=`^[A-Za-z]{2,3}(-[A-Za-z0-9]{1,8})*$`
	// +optional
	Language string `json:"language,omitempty"`
}

// GazetteStatus is the state of a Gazette as observed by press.
type GazetteStatus struct {
	// url is the public address of the Gazette's front page.
	// +optional
	URL string `json:"url,omitempty"`

	// observedGeneration is the metadata.generation that the status reflects.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// conditions describe the Gazette's current state. "Ready" is True when the
	// Gazette owns its domain and can be served.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=blog,categories=manifesto
// +kubebuilder:printcolumn:name="Title",type=string,JSONPath=`.spec.title`
// +kubebuilder:printcolumn:name="Domain",type=string,JSONPath=`.spec.domain`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Gazette is a blog site: a publication with its own domain, title and
// branding. A namespace (the publisher) may run several Gazettes, and every
// Dispatch belongs to exactly one of them.
type Gazette struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of the Gazette
	// +required
	Spec GazetteSpec `json:"spec"`

	// status is the state of the Gazette as observed by press
	// +optional
	Status GazetteStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// GazetteList contains a list of Gazette
type GazetteList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Gazette `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &Gazette{}, &GazetteList{})
		return nil
	})
}
