/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/
package mfctl

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
)

// UploadOptions describe one file to store as a Print.
type UploadOptions struct {
	File        string
	Name        string
	Alt         string
	ContentType string
	Archive     v1.ArchiveRef
	Timeout     time.Duration
}

func printCommand(env *Env) *cobra.Command {
	cmd := &cobra.Command{Use: "print", Aliases: []string{"media"}, Short: "Work with media files"}
	var o UploadOptions
	var archiveKind string
	upload := &cobra.Command{
		Use:   "upload FILE",
		Short: "Store a file as a Print and wait until it is Ready",
		Long: `Store a file as a Print: create the Print, upload the file to the URL press
hands out, and wait until press has checked it. The file is then
referenced as print:<name>, e.g. ![alt](print:<name>).

Without --archive, the only Archive in the namespace or the only
ClusterArchive is used.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			o.File = args[0]
			o.Archive.Kind = v1.ArchiveKind(archiveKind)
			ns, err := env.Namespace()
			if err != nil {
				return err
			}
			c, err := env.Client()
			if err != nil {
				return err
			}
			p, err := Upload(cmd.Context(), env, c, ns, o)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "print:%s\t%s\n", p.Name, p.Status.URL)
			return err
		},
	}
	f := upload.Flags()
	f.StringVar(&o.Name, "name", "", "The Print's name (default: from the file name).")
	f.StringVar(&o.Alt, "alt", "", "Alternative text for images.")
	f.StringVar(&o.ContentType, "content-type", "", "The media type (default: from the file extension).")
	f.StringVar(&o.Archive.Name, "archive", "", "The Archive or ClusterArchive to store the file in.")
	f.StringVar(&archiveKind, "archive-kind", string(v1.KindClusterArchive), "Archive or ClusterArchive.")
	f.DurationVar(&o.Timeout, "timeout", 5*time.Minute, "How long to wait for the upload and press's check.")
	cmd.AddCommand(upload)
	return cmd
}

var notInName = regexp.MustCompile(`[^a-z0-9]+`)

// PrintName derives a Print name from a file name: "My Photo.JPG" gives
// "my-photo".
func PrintName(file string) string {
	base := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
	name := strings.Trim(notInName.ReplaceAllString(strings.ToLower(base), "-"), "-")
	if len(name) > 63 {
		name = strings.TrimRight(name[:63], "-")
	}
	return name
}

// Upload stores a file as a Print in ns and returns it once Ready.
func Upload(ctx context.Context, env *Env, c client.Client, ns string, o UploadOptions) (*v1.Print, error) {
	data, err := os.ReadFile(o.File)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data) > v1.MaxPrintBytes {
		return nil, fmt.Errorf("%s: a Print holds 1 byte to %d MiB", o.File, v1.MaxPrintBytes>>20)
	}
	if o.Name == "" {
		if o.Name = PrintName(o.File); o.Name == "" {
			return nil, fmt.Errorf("cannot derive a name from %s; use --name", o.File)
		}
	}
	if o.ContentType == "" {
		if o.ContentType, _, err = mime.ParseMediaType(mime.TypeByExtension(strings.ToLower(filepath.Ext(o.File)))); err != nil {
			return nil, fmt.Errorf("unknown media type for %s; use --content-type", o.File)
		}
	}
	if o.Archive.Name == "" {
		if o.Archive, err = onlyArchive(ctx, c, ns); err != nil {
			return nil, err
		}
	}
	sum := sha256.Sum256(data)
	spec := v1.PrintSpec{
		ArchiveRef: o.Archive, ContentType: o.ContentType, Size: int64(len(data)),
		SHA256: hex.EncodeToString(sum[:]), Alt: o.Alt,
	}
	if err := apply(ctx, c, "Print", ns, o.Name, &spec); err != nil {
		return nil, err
	}
	if o.Timeout <= 0 {
		o.Timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	p, err := waitForPrint(ctx, env, c, ns, o.Name, func(p *v1.Print) bool {
		return p.Status.Phase == v1.PrintReady || p.Status.UploadURL != ""
	})
	if err != nil || p.Status.Phase == v1.PrintReady {
		// Ready already: the namespace had the same file.
		return p, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, p.Status.UploadURL, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", o.ContentType)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("uploading: %w", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("uploading: the storage refused the file (%s)", resp.Status)
	}
	return waitForPrint(ctx, env, c, ns, o.Name, func(p *v1.Print) bool { return p.Status.Phase == v1.PrintReady })
}

// onlyArchive returns the one storage a Print in ns can use, if there is
// exactly one.
func onlyArchive(ctx context.Context, c client.Client, ns string) (v1.ArchiveRef, error) {
	var found []v1.ArchiveRef
	var local v1.ArchiveList
	if err := c.List(ctx, &local, client.InNamespace(ns)); err != nil && !apierrors.IsForbidden(err) {
		return v1.ArchiveRef{}, err
	}
	for _, a := range local.Items {
		found = append(found, v1.ArchiveRef{Kind: v1.KindArchive, Name: a.Name})
	}
	var shared v1.ClusterArchiveList
	if err := c.List(ctx, &shared); err != nil && !apierrors.IsForbidden(err) {
		return v1.ArchiveRef{}, err
	}
	for _, a := range shared.Items {
		found = append(found, v1.ArchiveRef{Kind: v1.KindClusterArchive, Name: a.Name})
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return v1.ArchiveRef{}, errors.New("no Archive or ClusterArchive to store the file in; ask a publisher to create one")
	}
	names := make([]string, 0, len(found))
	for _, a := range found {
		names = append(names, string(a.Kind)+"/"+a.Name)
	}
	return v1.ArchiveRef{}, fmt.Errorf("choose a storage with --archive (and --archive-kind): %s", strings.Join(names, ", "))
}

func waitForPrint(ctx context.Context, env *Env, c client.Client, ns, name string, done func(*v1.Print) bool) (*v1.Print, error) {
	for {
		var p v1.Print
		if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &p); err != nil {
			return nil, err
		}
		if p.Status.Phase == v1.PrintFailed {
			msg := "press rejected the file"
			if cond := meta.FindStatusCondition(p.Status.Conditions, v1.ConditionReady); cond != nil {
				msg = cond.Message
			}
			return nil, fmt.Errorf("print:%s failed: %s", name, msg)
		}
		if done(&p) {
			return &p, nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("print:%s: gave up waiting (phase %q): %w", name, p.Status.Phase, ctx.Err())
		case <-time.After(env.Poll):
		}
	}
}

// apply writes the spec of a Manifesto object with Server-Side Apply as mfctl.
func apply(ctx context.Context, c client.Client, kind, ns, name string, spec any) error {
	s, err := runtime.DefaultUnstructuredConverter.ToUnstructured(spec)
	if err != nil {
		return err
	}
	u := &unstructured.Unstructured{Object: map[string]any{"spec": s}}
	u.SetAPIVersion(v1.GroupVersion.String())
	u.SetKind(kind)
	u.SetNamespace(ns)
	u.SetName(name)
	return c.Apply(ctx, client.ApplyConfigurationFromUnstructured(u), client.FieldOwner(FieldManager))
}
