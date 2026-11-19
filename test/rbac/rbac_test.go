/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

// Package rbac tests Manifesto's roles (config/rbac/manifesto_roles.yaml)
// against a real API server: RBAC is the only access control Manifesto has.
package rbac

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/yaml"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
)

// namePrefix is what config/default's kustomization adds to every name.
const namePrefix = "manifesto-"

const testNamespace = "blog-alice"

var (
	env    *envtest.Environment
	admin  client.Client
	scheme = runtime.NewScheme()
)

func TestMain(m *testing.M) {
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, v1.AddToScheme} {
		if err := add(scheme); err != nil {
			panic(err)
		}
	}
	env = &envtest.Environment{
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
	if admin, err = client.New(cfg, client.Options{Scheme: scheme}); err != nil {
		panic(err)
	}
	if err := installRoles(context.Background()); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = env.Stop()
	os.Exit(code)
}

// installRoles creates the roles from config/rbac/manifesto_roles.yaml with
// the kustomize name prefix applied.
func installRoles(ctx context.Context) error {
	b, err := os.ReadFile(filepath.Join("..", "..", "config", "rbac", "manifesto_roles.yaml"))
	if err != nil {
		return err
	}
	dec := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(b), 4096)
	for {
		var u map[string]any
		if err := dec.Decode(&u); errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			return err
		}
		if u == nil {
			continue
		}
		raw := mustJSON(u)
		switch u["kind"] {
		case "ClusterRole":
			var r rbacv1.ClusterRole
			if err := decodeJSON(raw, &r); err != nil {
				return err
			}
			r.Name = namePrefix + r.Name
			if err := admin.Create(ctx, &r); err != nil {
				return err
			}
		case "ClusterRoleBinding":
			var rb rbacv1.ClusterRoleBinding
			if err := decodeJSON(raw, &rb); err != nil {
				return err
			}
			rb.Name, rb.RoleRef.Name = namePrefix+rb.Name, namePrefix+rb.RoleRef.Name
			if err := admin.Create(ctx, &rb); err != nil {
				return err
			}
		}
	}
}

// user returns a client authenticated as name, member of groups.
func user(t *testing.T, name string, groups ...string) client.Client {
	t.Helper()
	u, err := env.AddUser(envtest.User{Name: name, Groups: groups}, nil)
	if err != nil {
		t.Fatal(err)
	}
	c, err := client.New(u.Config(), client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func bind(t *testing.T, ns, role, group string) {
	t.Helper()
	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: role + "-" + group},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: namePrefix + role},
		Subjects:   []rbacv1.Subject{{APIGroup: rbacv1.GroupName, Kind: "Group", Name: group}},
	}
	if err := admin.Create(context.Background(), rb); err != nil {
		t.Fatal(err)
	}
}

func namespace(t *testing.T, name string) {
	t.Helper()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if err := admin.Create(context.Background(), ns); err != nil {
		t.Fatal(err)
	}
}

// expect checks that err is nil when allowed, and Forbidden otherwise.
func expect(t *testing.T, what string, allowed bool, err error) {
	t.Helper()
	switch {
	case allowed && err != nil:
		t.Errorf("%s: want allowed, got %v", what, err)
	case !allowed && !apierrors.IsForbidden(err):
		t.Errorf("%s: want Forbidden, got %v", what, err)
	}
}

func dispatch(ns, name string) *v1.Dispatch {
	return &v1.Dispatch{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: v1.DispatchSpec{GazetteRef: "notes", Title: "T"}}
}

func print(ns, name string) *v1.Print {
	return &v1.Print{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}, Spec: v1.PrintSpec{
		ArchiveRef: v1.ArchiveRef{Name: "local"}, ContentType: "image/png", Size: 1,
		SHA256: "0000000000000000000000000000000000000000000000000000000000000000",
	}}
}

// edition returns an Edition in the test namespace.
func edition(name string) *v1.Edition {
	return &v1.Edition{ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace, Name: name},
		Spec: v1.EditionSpec{DispatchRef: "existing", Number: 1, Snapshot: v1.EditionSnapshot{Title: "T"}}}
}

func gazette(ns, name string) *v1.Gazette {
	return &v1.Gazette{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: v1.GazetteSpec{Title: "T", Domain: name + ".example.io"}}
}

// member returns a RoleBinding in the test namespace giving the user
// "newcomer" a role.
func member(role string) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Namespace: testNamespace, Name: "newcomer-" + role},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: role},
		Subjects:   []rbacv1.Subject{{APIGroup: rbacv1.GroupName, Kind: "User", Name: "newcomer"}},
	}
}

func TestRoles(t *testing.T) {
	ctx := context.Background()
	const ns, other = testNamespace, "blog-bob"
	namespace(t, ns)
	namespace(t, other)
	bind(t, ns, "proofreader", "oidc:proofreaders")
	bind(t, ns, "editor", "oidc:editors")
	bind(t, ns, "publisher", "oidc:publishers")
	if err := admin.Create(ctx, dispatch(ns, "existing")); err != nil {
		t.Fatal(err)
	}
	if err := admin.Create(ctx, edition("existing-1")); err != nil {
		t.Fatal(err)
	}

	proofreader := user(t, "pat", "oidc:proofreaders")
	editor := user(t, "eve", "oidc:editors")
	publisher := user(t, "pia", "oidc:publishers")
	outsider := user(t, "otto")

	t.Run("proofreader reads everything, drafts included, and writes nothing", func(t *testing.T) {
		var list v1.DispatchList
		expect(t, "list dispatches", true, proofreader.List(ctx, &list, client.InNamespace(ns)))
		if len(list.Items) != 1 {
			t.Errorf("listed %d dispatches, want 1", len(list.Items))
		}
		expect(t, "list prints", true, proofreader.List(ctx, &v1.PrintList{}, client.InNamespace(ns)))
		expect(t, "create dispatch", false, proofreader.Create(ctx, dispatch(ns, "p1")))
		expect(t, "list editions", true, proofreader.List(ctx, &v1.EditionList{}, client.InNamespace(ns)))
	})

	t.Run("editor writes content but not the site", func(t *testing.T) {
		expect(t, "create dispatch", true, editor.Create(ctx, dispatch(ns, "e1")))
		expect(t, "create print", true, editor.Create(ctx, print(ns, "e1")))
		section := &v1.Section{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "tech"}}
		expect(t, "create section", true, editor.Create(ctx, section))
		expect(t, "create gazette", false, editor.Create(ctx, gazette(ns, "e1")))
		expect(t, "create scribe", false, editor.Create(ctx, &v1.Scribe{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "eve"}, Spec: v1.ScribeSpec{DisplayName: "Eve"}}))
		expect(t, "add a member", false, editor.Create(ctx, member(namePrefix+"proofreader")))
		expect(t, "create dispatch in another namespace", false, editor.Create(ctx, dispatch(other, "e1")))
		expect(t, "write print status", false, editor.Status().Update(ctx, print(ns, "e1")))
		expect(t, "read editions", true, editor.List(ctx, &v1.EditionList{}, client.InNamespace(ns)))
		expect(t, "create edition", false, editor.Create(ctx, edition("e1-1")))
		expect(t, "delete edition", false, editor.Delete(ctx, edition("existing-1")))
	})

	t.Run("publisher runs the site and its members", func(t *testing.T) {
		expect(t, "create gazette", true, publisher.Create(ctx, gazette(ns, "pia")))
		expect(t, "create archive", true, publisher.Create(ctx, &v1.Archive{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "bucket"},
			Spec:       v1.ArchiveSpec{PVC: &v1.PVCArchiveSource{ClaimName: "c", PublicURL: "https://m.example.io"}}}))
		expect(t, "make someone an editor", true, publisher.Create(ctx, member(namePrefix+"editor")))
		expect(t, "make someone a publisher", true, publisher.Create(ctx, member(namePrefix+"publisher")))
		expect(t, "make someone cluster-admin", false, publisher.Create(ctx, member("cluster-admin")))
		expect(t, "read secrets", false, publisher.List(ctx, &corev1.SecretList{}, client.InNamespace(ns)))
		expect(t, "create gazette in another namespace", false, publisher.Create(ctx, gazette(other, "pia")))
		expect(t, "create edition", false, publisher.Create(ctx, edition("pia-1")))
		expect(t, "delete edition", true, publisher.Delete(ctx, edition("existing-1")))
	})

	t.Run("everyone signed in reads the shared catalog, nothing else", func(t *testing.T) {
		expect(t, "list cluster typesets", true, outsider.List(ctx, &v1.ClusterTypesetList{}))
		expect(t, "list cluster archives", true, outsider.List(ctx, &v1.ClusterArchiveList{}))
		inline := &v1.InlineTypesetSource{Templates: map[string]string{"layout.html": "x"}}
		expect(t, "create cluster typeset", false, publisher.Create(ctx, &v1.ClusterTypeset{
			ObjectMeta: metav1.ObjectMeta{Name: "mine"}, Spec: v1.TypesetSpec{Source: v1.TypesetSource{Inline: inline}}}))
		expect(t, "list dispatches", false, outsider.List(ctx, &v1.DispatchList{}, client.InNamespace(ns)))
	})
}

func mustJSON(v map[string]any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func decodeJSON(b []byte, into any) error { return json.Unmarshal(b, into) }
