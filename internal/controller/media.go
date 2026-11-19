/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
	"go.pkgs.davy.tw/manifesto/internal/storage"
)

// Media configures how press reaches media storage.
type Media struct {
	// DarkroomURL is darkroom's internal address, used by pvc Archives.
	DarkroomURL string
	// DarkroomClaim is the PersistentVolumeClaim darkroom serves; a pvc
	// Archive must name it.
	DarkroomClaim string
	// SigningKey signs darkroom requests. Without it pvc Archives are not usable.
	SigningKey []byte
	// UploadTTL is how long an upload URL stays valid (default 5 minutes).
	UploadTTL time.Duration
	// Namespace is press's namespace, where a ClusterArchive's credentials
	// Secret is looked up unless it names another namespace.
	Namespace string
	// Secrets reads S3 credentials. It should bypass the cache so press does
	// not watch every Secret in the cluster; Setup uses the manager's
	// API reader.
	Secrets client.Reader
}

func (m Media) uploadTTL() time.Duration {
	if m.UploadTTL > 0 {
		return m.UploadTTL
	}
	return 5 * time.Minute
}

// errArchive is a problem with an Archive, reported with a condition reason.
type errArchive struct {
	reason string
	err    error
}

func (e *errArchive) Error() string { return e.err.Error() }

// +kubebuilder:rbac:groups="",resources=secrets,verbs=get

// driverFor returns the storage driver of an Archive or ClusterArchive spec.
// archiveNamespace is the Archive's namespace, or "" for a ClusterArchive.
func (m Media) driverFor(ctx context.Context, spec *v1.ArchiveSpec, archiveNamespace string) (storage.Driver, error) {
	switch {
	case spec.PVC != nil:
		if len(m.SigningKey) == 0 || m.DarkroomURL == "" {
			return nil, &errArchive{v1.ReasonBackendError, errors.New("press is not configured with darkroom (--darkroom-url and its signing key)")}
		}
		if spec.PVC.ClaimName != m.DarkroomClaim {
			return nil, &errArchive{v1.ReasonBackendError,
				fmt.Errorf("darkroom serves claim %q, not %q", m.DarkroomClaim, spec.PVC.ClaimName)}
		}
		return &storage.PVC{InternalURL: m.DarkroomURL, PublicBase: spec.PVC.PublicURL, Key: m.SigningKey}, nil
	case spec.S3 != nil:
		return m.s3Driver(ctx, spec.S3, archiveNamespace)
	}
	return nil, &errArchive{v1.ReasonBackendError, errors.New("no backend set")}
}

// archiveKey identifies an Archive in printArchiveIndex: "Archive/<ns>/<name>"
// or "ClusterArchive//<name>".
func archiveKey(kind v1.ArchiveKind, namespace, name string) string {
	if kind == "" {
		kind = v1.KindClusterArchive
	}
	if kind == v1.KindClusterArchive {
		namespace = ""
	}
	return string(kind) + "/" + namespace + "/" + name
}

// getArchive returns the spec of the Archive a Print refers to, or an
// *errArchive with ArchiveNotFound.
func getArchive(ctx context.Context, c client.Client, namespace string, ref v1.ArchiveRef) (*v1.ArchiveSpec, error) {
	var spec *v1.ArchiveSpec
	var err error
	if ref.Kind == v1.KindArchive {
		var a v1.Archive
		err = c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: ref.Name}, &a)
		spec = &a.Spec
	} else {
		var a v1.ClusterArchive
		err = c.Get(ctx, types.NamespacedName{Name: ref.Name}, &a)
		spec = &a.Spec
	}
	if apierrors.IsNotFound(err) {
		kind := ref.Kind
		if kind == "" {
			kind = v1.KindClusterArchive
		}
		return nil, &errArchive{v1.ReasonArchiveNotFound, fmt.Errorf("%s %q not found", kind, ref.Name)}
	}
	return spec, err
}

// Keys of an S3 credentials Secret.
const (
	secretAccessKeyID     = "accessKeyID"
	secretSecretAccessKey = "secretAccessKey"
)

func (m Media) s3Driver(ctx context.Context, src *v1.S3ArchiveSource, archiveNamespace string) (storage.Driver, error) {
	ns := archiveNamespace
	if ns == "" {
		ns = src.CredentialsSecretRef.Namespace
		if ns == "" {
			ns = m.Namespace
		}
	}
	if m.Secrets == nil {
		return nil, &errArchive{v1.ReasonBackendError, errors.New("press cannot read S3 credentials")}
	}
	var secret corev1.Secret
	ref := types.NamespacedName{Namespace: ns, Name: src.CredentialsSecretRef.Name}
	if err := m.Secrets.Get(ctx, ref, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, &errArchive{v1.ReasonBackendError, fmt.Errorf("credentials Secret %s not found", ref)}
		}
		return nil, err
	}
	id, key := string(secret.Data[secretAccessKeyID]), string(secret.Data[secretSecretAccessKey])
	if id == "" || key == "" {
		return nil, &errArchive{v1.ReasonBackendError,
			fmt.Errorf("credentials Secret %s needs %q and %q", ref, secretAccessKeyID, secretSecretAccessKey)}
	}
	return storage.NewS3(storage.S3Config{
		Endpoint: src.Endpoint, Region: src.Region, Bucket: src.Bucket,
		PathStyle:   src.PathStyle == nil || *src.PathStyle,
		AccessKeyID: id, SecretAccessKey: key, PublicURL: src.PublicURL,
	}), nil
}
