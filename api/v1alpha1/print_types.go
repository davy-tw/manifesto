/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// ArchiveKind is the kind of storage a Print uses.
// +kubebuilder:validation:Enum=Archive;ClusterArchive
type ArchiveKind string

const (
	KindArchive        ArchiveKind = "Archive"
	KindClusterArchive ArchiveKind = "ClusterArchive"
)

// ArchiveRef refers to an Archive in the Print's namespace or to a
// ClusterArchive.
type ArchiveRef struct {
	// kind is Archive (same namespace) or ClusterArchive.
	// +kubebuilder:default=ClusterArchive
	// +optional
	Kind ArchiveKind `json:"kind,omitempty"`

	// name of the storage.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +required
	Name string `json:"name"`
}

// MaxPrintBytes is the largest file a Print accepts (100 MiB).
const MaxPrintBytes = 100 << 20

// PrintSpec describes a media file. Every field is fixed once the Print is
// created: a different file is a different Print.
type PrintSpec struct {
	// archiveRef is where the file is stored.
	// +required
	ArchiveRef ArchiveRef `json:"archiveRef"`

	// contentType is the file's media type, e.g. image/png.
	// +kubebuilder:validation:MaxLength=127
	// +kubebuilder:validation:Pattern=`^[a-z0-9][a-z0-9!#$&^_.+-]*/[a-z0-9][a-z0-9!#$&^_.+-]*$`
	// +required
	ContentType string `json:"contentType"`

	// size is the file's length in bytes; the upload must match it.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=104857600
	// +required
	Size int64 `json:"size"`

	// sha256 is the hex SHA-256 of the file; the upload must match it. The
	// file is stored and served under this hash, so its URL never changes
	// and identical files in a namespace share one object.
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{64}$`
	// +required
	SHA256 string `json:"sha256"`

	// alt is the text alternative for images.
	// +kubebuilder:validation:MaxLength=1024
	// +optional
	Alt string `json:"alt,omitempty"`
}

// PrintPhase is the state of a Print's file.
// +kubebuilder:validation:Enum=Pending;Ready;Failed
type PrintPhase string

const (
	// PrintPending waits for the file to be uploaded to status.uploadURL.
	PrintPending PrintPhase = "Pending"
	// PrintReady means the file is stored and served at status.url.
	PrintReady PrintPhase = "Ready"
	// PrintFailed means the upload did not arrive before the upload URL
	// expired. Delete the Print and create it again to retry.
	PrintFailed PrintPhase = "Failed"
)

// PrintStatus is the state of a Print as observed by press.
type PrintStatus struct {
	// phase is Pending, Ready or Failed.
	// +optional
	Phase PrintPhase `json:"phase,omitempty"`

	// uploadURL is a short-lived signed URL to PUT the file to. It is only
	// set while the Print is Pending.
	// +optional
	UploadURL string `json:"uploadURL,omitempty"`

	// uploadHeaders are the HTTP headers the PUT to uploadURL must send,
	// such as Content-Type; they are signed into uploadURL. Only set while
	// the Print is Pending.
	// +optional
	UploadHeaders map[string]string `json:"uploadHeaders,omitempty"`

	// uploadExpiresAt is when uploadURL stops working; a Print still Pending
	// then becomes Failed.
	// +optional
	UploadExpiresAt *metav1.Time `json:"uploadExpiresAt,omitempty"`

	// objectKey is where the file is stored: <namespace UID>/sha256/<hash>.<ext>.
	// +optional
	ObjectKey string `json:"objectKey,omitempty"`

	// url is the public address of the file.
	// +optional
	URL string `json:"url,omitempty"`

	// observedGeneration is the metadata.generation that the status reflects.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// conditions describe the Print's current state. "Ready" is False when
	// its Archive is missing or not usable.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=media,categories=manifesto
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.contentType`
// +kubebuilder:printcolumn:name="Size",type=integer,JSONPath=`.spec.size`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.status.url`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="self.metadata.name.matches('^[a-z0-9]([-a-z0-9]*[a-z0-9])?$') && size(self.metadata.name) <= 63",message="metadata.name is referenced as print:<name> and must be a DNS label"

// Print is a media file. It records the file's metadata; the file itself is
// uploaded to its Archive and referenced from content as print:<name>.
type Print struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec describes the file
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec is immutable; create a new Print for a different file"
	// +required
	Spec PrintSpec `json:"spec"`

	// status is the state of the Print as observed by press
	// +optional
	Status PrintStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// PrintList contains a list of Print
type PrintList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Print `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &Print{}, &PrintList{})
		return nil
	})
}
