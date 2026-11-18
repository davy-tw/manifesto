/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package v1alpha1_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
)

// These tests run the generated CRDs against a real API server (envtest) to
// check the OpenAPI and CEL validation rules.

var k8sClient client.Client

func TestMain(m *testing.M) {
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		if dirs, _ := filepath.Glob(filepath.Join("..", "..", "bin", "k8s", "*")); len(dirs) > 0 {
			env.BinaryAssetsDirectory = dirs[0]
		}
	}
	cfg, err := env.Start()
	if err != nil {
		panic(err)
	}
	scheme := runtime.NewScheme()
	if err := v1.AddToScheme(scheme); err != nil {
		panic(err)
	}
	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		panic(err)
	}
	code := m.Run()
	_ = env.Stop()
	os.Exit(code)
}

func validGazette(name string) *v1.Gazette {
	return &v1.Gazette{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: v1.GazetteSpec{
			Title:    "Alice's Notes",
			Domain:   "alice.example.io",
			Logo:     "print:alice-logo",
			Language: "zh-TW",
		},
	}
}

func validDispatch(name string) *v1.Dispatch {
	return &v1.Dispatch{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: v1.DispatchSpec{
			GazetteRef: "alice-notes",
			Title:      "Hello K8s",
			Content:    v1.DispatchContent{Body: "# hi"},
		},
	}
}

// expectCreate creates obj and checks it is accepted, or rejected as invalid
// with a message containing wantErr.
func expectCreate(t *testing.T, obj client.Object, wantErr string) {
	t.Helper()
	ctx := context.Background()
	err := k8sClient.Create(ctx, obj)
	if wantErr == "" {
		if err != nil {
			t.Fatalf("Create() error = %v, want success", err)
		}
		t.Cleanup(func() { _ = k8sClient.Delete(ctx, obj) })
		return
	}
	if err == nil {
		_ = k8sClient.Delete(ctx, obj)
		t.Fatalf("Create() succeeded, want error containing %q", wantErr)
	}
	if !apierrors.IsInvalid(err) {
		t.Fatalf("Create() error = %v, want an Invalid error", err)
	}
	if !strings.Contains(err.Error(), wantErr) {
		t.Fatalf("Create() error = %v, want it to contain %q", err, wantErr)
	}
}

func TestGazetteValidation(t *testing.T) {
	const domainField = "spec.domain"
	tests := []struct {
		name    string
		mutate  func(*v1.Gazette)
		wantErr string
	}{
		{"valid", func(*v1.Gazette) {}, ""},
		{"single-label domain for local dev", func(g *v1.Gazette) { g.Spec.Domain = "localhost" }, ""},
		{"missing title", func(g *v1.Gazette) { g.Spec.Title = "" }, "spec.title"},
		{"missing domain", func(g *v1.Gazette) { g.Spec.Domain = "" }, domainField},
		{"domain with scheme", func(g *v1.Gazette) { g.Spec.Domain = "https://alice.example.io" }, domainField},
		{"uppercase domain", func(g *v1.Gazette) { g.Spec.Domain = "Alice.example.io" }, domainField},
		{"logo without print prefix", func(g *v1.Gazette) { g.Spec.Logo = "alice-logo" }, "spec.logo"},
		{"bad language tag", func(g *v1.Gazette) { g.Spec.Language = "zh_TW" }, "spec.language"},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := validGazette("gazette-" + string(rune('a'+i)))
			tt.mutate(g)
			expectCreate(t, g, tt.wantErr)
		})
	}
}

func TestDispatchValidation(t *testing.T) {
	tests := []struct {
		name     string
		dispatch string
		mutate   func(*v1.Dispatch)
		wantErr  string
	}{
		{"valid", "hello-k8s", func(*v1.Dispatch) {}, ""},
		{"scheduled", "scheduled", func(d *v1.Dispatch) {
			d.Spec.State = v1.StatePublished
			at := metav1.Now()
			d.Spec.PublishAt = &at
		}, ""},
		{"name with a dot is not a slug", "hello.k8s", func(*v1.Dispatch) {}, "metadata.name is the URL slug"},
		{"missing gazetteRef", "no-gazette", func(d *v1.Dispatch) { d.Spec.GazetteRef = "" }, "spec.gazetteRef"},
		{"missing title", "no-title", func(d *v1.Dispatch) { d.Spec.Title = "" }, "spec.title"},
		{"unknown state", "bad-state", func(d *v1.Dispatch) { d.Spec.State = "Embargoed" }, "spec.state"},
		{"unknown format", "bad-format", func(d *v1.Dispatch) { d.Spec.Content.Format = "html" }, "spec.content.format"},
		{"cover without print prefix", "bad-cover", func(d *v1.Dispatch) { d.Spec.Cover = "https://x/y.png" }, "spec.cover"},
		{"body too large", "too-large", func(d *v1.Dispatch) {
			d.Spec.Content.Body = strings.Repeat("a", v1.MaxBodyBytes+1)
		}, "spec.content.body"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := validDispatch(tt.dispatch)
			tt.mutate(d)
			expectCreate(t, d, tt.wantErr)
		})
	}
}

func TestDispatchDefaults(t *testing.T) {
	d := validDispatch("defaults")
	expectCreate(t, d, "")
	if d.Spec.State != v1.StateDraft {
		t.Errorf("spec.state = %q, want %q", d.Spec.State, v1.StateDraft)
	}
	if d.Spec.Content.Format != v1.FormatMarkdown {
		t.Errorf("spec.content.format = %q, want %q", d.Spec.Content.Format, v1.FormatMarkdown)
	}
}

func TestSectionValidation(t *testing.T) {
	tests := []struct {
		name    string
		section v1.Section
		wantErr string
	}{
		{"minimal", v1.Section{ObjectMeta: metav1.ObjectMeta{Name: "tech"}}, ""},
		{"with parent", v1.Section{ObjectMeta: metav1.ObjectMeta{Name: "go"}, Spec: v1.SectionSpec{Parent: "tech"}}, ""},
		{"own parent", v1.Section{ObjectMeta: metav1.ObjectMeta{Name: "loop"}, Spec: v1.SectionSpec{Parent: "loop"}}, "cannot be its own parent"},
		{"dotted name", v1.Section{ObjectMeta: metav1.ObjectMeta{Name: "a.b"}}, "must be a DNS label"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := tt.section
			s.Namespace = "default"
			expectCreate(t, &s, tt.wantErr)
		})
	}
}

func TestStampAndScribeValidation(t *testing.T) {
	expectCreate(t, &v1.Stamp{ObjectMeta: metav1.ObjectMeta{Name: "kubernetes", Namespace: "default"}}, "")
	expectCreate(t, &v1.Stamp{ObjectMeta: metav1.ObjectMeta{Name: "k8s.io", Namespace: "default"}}, "must be a DNS label")
	expectCreate(t, &v1.Scribe{ObjectMeta: metav1.ObjectMeta{Name: "alice", Namespace: "default"},
		Spec: v1.ScribeSpec{DisplayName: "Alice", Avatar: "print:alice", Identities: []string{"oidc:alice@example.io"}}}, "")
	expectCreate(t, &v1.Scribe{ObjectMeta: metav1.ObjectMeta{Name: "nameless", Namespace: "default"}}, "spec.displayName")
}

func TestTypesetValidation(t *testing.T) {
	inline := v1.TypesetSource{Inline: &v1.InlineTypesetSource{Templates: map[string]string{"layout.html": "x"}}}
	ts := &v1.Typeset{ObjectMeta: metav1.ObjectMeta{Name: "mine", Namespace: "default"}, Spec: v1.TypesetSpec{Source: inline}}
	expectCreate(t, ts, "")
	if ts.Spec.ContractVersion != v1.ContractV1 {
		t.Errorf("contractVersion = %q, want default %q", ts.Spec.ContractVersion, v1.ContractV1)
	}
	expectCreate(t, &v1.ClusterTypeset{ObjectMeta: metav1.ObjectMeta{Name: "shared"}, Spec: v1.TypesetSpec{Source: inline}}, "")
	expectCreate(t, &v1.Typeset{ObjectMeta: metav1.ObjectMeta{Name: "empty", Namespace: "default"}}, "exactly one source")
	expectCreate(t, &v1.Typeset{ObjectMeta: metav1.ObjectMeta{Name: "v9", Namespace: "default"},
		Spec: v1.TypesetSpec{ContractVersion: "v9", Source: inline}}, "spec.contractVersion")
}

func TestGazetteTypesetFields(t *testing.T) {
	g := validGazette("themed")
	g.Spec.TypesetRef = &v1.TypesetRef{Name: "minimal"}
	g.Spec.PageSize = 20
	expectCreate(t, g, "")
	if g.Spec.TypesetRef.Kind != v1.KindClusterTypeset {
		t.Errorf("typesetRef.kind = %q, want default %q", g.Spec.TypesetRef.Kind, v1.KindClusterTypeset)
	}
	bad := validGazette("bad-page-size")
	bad.Spec.PageSize = 1000
	expectCreate(t, bad, "spec.pageSize")
}
