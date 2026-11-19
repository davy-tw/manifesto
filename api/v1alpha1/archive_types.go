/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// S3ArchiveSource stores media in an S3-compatible bucket, using only the
// standard S3 API.
type S3ArchiveSource struct {
	// endpoint is the S3 API endpoint, e.g. http://seaweedfs-s3.storage.svc:8333.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=2048
	// +required
	Endpoint string `json:"endpoint"`

	// region of the bucket. Many S3-compatible services accept any value.
	// +kubebuilder:default=us-east-1
	// +kubebuilder:validation:MaxLength=64
	// +optional
	Region string `json:"region,omitempty"`

	// bucket holds the objects.
	// +kubebuilder:validation:MinLength=3
	// +kubebuilder:validation:MaxLength=63
	// +required
	Bucket string `json:"bucket"`

	// pathStyle addresses the bucket as endpoint/bucket instead of
	// bucket.endpoint, which most self-hosted services need.
	// +kubebuilder:default=true
	// +optional
	PathStyle *bool `json:"pathStyle,omitempty"`

	// credentialsSecretRef is a Secret with accessKeyID and secretAccessKey.
	// For an Archive it must be in the Archive's namespace; a ClusterArchive
	// names its namespace.
	// +required
	CredentialsSecretRef SecretReference `json:"credentialsSecretRef"`

	// publicURL is where visitors read objects, e.g. a CDN in front of the
	// bucket. Defaults to endpoint/bucket.
	// +kubebuilder:validation:MaxLength=2048
	// +kubebuilder:validation:Pattern=`^https?://`
	// +optional
	PublicURL string `json:"publicURL,omitempty"`
}

// SecretReference names a Secret.
type SecretReference struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +required
	Name string `json:"name"`

	// namespace of the Secret; only used by ClusterArchive.
	// +kubebuilder:validation:MaxLength=63
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// PVCArchiveSource stores media on the PersistentVolume darkroom serves.
type PVCArchiveSource struct {
	// claimName is the PersistentVolumeClaim darkroom mounts. press checks
	// that it is the claim its darkroom serves.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +required
	ClaimName string `json:"claimName"`

	// publicURL is where visitors reach darkroom, e.g. https://media.example.io.
	// Upload URLs are signed against it as well.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=2048
	// +kubebuilder:validation:Pattern=`^https?://`
	// +required
	PublicURL string `json:"publicURL"`
}

// ArchiveSpec is where media files are stored. Exactly one backend is set.
// +kubebuilder:validation:XValidation:rule="has(self.s3) != has(self.pvc)",message="exactly one of s3 or pvc must be set"
type ArchiveSpec struct {
	// s3 stores files in an S3-compatible bucket.
	// +optional
	S3 *S3ArchiveSource `json:"s3,omitempty"`

	// pvc stores files on darkroom's PersistentVolume.
	// +optional
	PVC *PVCArchiveSource `json:"pvc,omitempty"`
}

// ArchiveStatus is the state of an Archive as observed by press.
type ArchiveStatus struct {
	// observedGeneration is the metadata.generation that the status reflects.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// conditions describe the Archive's current state. "Ready" is True when
	// press can sign uploads and check objects in the backend.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=store,categories=manifesto
// +kubebuilder:printcolumn:name="Backend",type=string,JSONPath=`.spec.pvc.claimName`,description="PVC claim (empty for S3)"
// +kubebuilder:printcolumn:name="Bucket",type=string,JSONPath=`.spec.s3.bucket`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Archive is media storage for the Prints of its namespace. Use a
// ClusterArchive for storage shared by every namespace; objects are still
// kept apart per namespace.
type Archive struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec is where files are stored
	// +required
	Spec ArchiveSpec `json:"spec"`

	// status is the state of the Archive as observed by press
	// +optional
	Status ArchiveStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// ArchiveList contains a list of Archive
type ArchiveList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Archive `json:"items"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=cstore,categories=manifesto
// +kubebuilder:printcolumn:name="Backend",type=string,JSONPath=`.spec.pvc.claimName`,description="PVC claim (empty for S3)"
// +kubebuilder:printcolumn:name="Bucket",type=string,JSONPath=`.spec.s3.bucket`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ClusterArchive is media storage available to the Prints of every namespace.
type ClusterArchive struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec is where files are stored
	// +required
	Spec ArchiveSpec `json:"spec"`

	// status is the state of the ClusterArchive as observed by press
	// +optional
	Status ArchiveStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// ClusterArchiveList contains a list of ClusterArchive
type ClusterArchiveList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []ClusterArchive `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &Archive{}, &ArchiveList{}, &ClusterArchive{}, &ClusterArchiveList{})
		return nil
	})
}
