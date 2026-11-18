/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// ScribeSpec defines an author.
type ScribeSpec struct {
	// displayName is the author's name as shown to visitors.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	// +required
	DisplayName string `json:"displayName"`

	// bio is a short introduction shown on the author's page.
	// +kubebuilder:validation:MaxLength=4096
	// +optional
	Bio string `json:"bio,omitempty"`

	// avatar is the author's picture, as a reference to a Print: "print:<name>".
	// +optional
	Avatar PrintRef `json:"avatar,omitempty"`

	// identities are the Kubernetes user names this author signs in as, for
	// example "oidc:alice@example.io". desk uses them to pick the Scribe of
	// the signed-in user; they grant no permissions.
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:items:MaxLength=512
	// +listType=set
	// +optional
	Identities []string `json:"identities,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=author,categories=manifesto
// +kubebuilder:printcolumn:name="Display Name",type=string,JSONPath=`.spec.displayName`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Scribe is an author. A Dispatch names its author with spec.scribeRef, and
// herald lists an author's Dispatches at /scribes/<name>.
type Scribe struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the author
	// +required
	Spec ScribeSpec `json:"spec"`
}

// +kubebuilder:object:root=true

// ScribeList contains a list of Scribe
type ScribeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Scribe `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &Scribe{}, &ScribeList{})
		return nil
	})
}
