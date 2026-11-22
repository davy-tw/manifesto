/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package desk

import (
	"context"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-logr/logr"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
	"go.pkgs.davy.tw/manifesto/internal/controller"
	"go.pkgs.davy.tw/manifesto/internal/darkroom"
)

// TestUI drives the real UI in Chromium: sign in through OIDC, write and
// publish a Dispatch with live preview, resolve an edit conflict and upload
// media. press and darkroom run in-process, the API server is envtest's.
// It needs node and playwright, so it only runs with DESK_UI_TEST=1;
// screenshots go to DESK_UI_SHOTS (default: a temporary directory).
func TestUI(t *testing.T) {
	if os.Getenv("DESK_UI_TEST") == "" {
		t.Skip("set DESK_UI_TEST=1 to drive the UI in a browser (needs node and playwright)")
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	const uiNS = "blog-ui"

	// press, with an in-process darkroom.
	key := []byte("ui-test-signing-key")
	store := &darkroom.Store{Dir: t.TempDir(), Keys: func() [][]byte { return [][]byte{key} }, Log: logr.Discard()}
	media := httptest.NewServer(store.PublicHandler())
	t.Cleanup(media.Close)
	internal := httptest.NewServer(store.InternalHandler())
	t.Cleanup(internal.Close)
	mgr, err := ctrl.NewManager(envCfg, ctrl.Options{Scheme: scheme, Metrics: metricsserver.Options{BindAddress: "0"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.Setup(mgr, controller.Options{Media: controller.Media{
		DarkroomURL: internal.URL, DarkroomClaim: "manifesto-media-data", SigningKey: key, Namespace: "default",
	}}); err != nil {
		t.Fatal(err)
	}
	go func() { _ = mgr.Start(ctx) }()

	// The publication: alice's group may edit, everyone may read the catalog.
	all := []string{"gazettes", "dispatches", "editions", "sections", "stamps", "scribes", "typesets", "prints", "archives"}
	mustCreate := func(obj interface {
		GetName() string
	}, create func() error) {
		if err := create(); err != nil {
			t.Fatalf("creating %s: %v", obj.GetName(), err)
		}
	}
	ns := &corev1.Namespace{Name: uiNS}
	mustCreate(ns, func() error { return admin.Create(ctx, ns) })
	role := &rbacv1.ClusterRole{Name: "ui-editor", Rules: []rbacv1.PolicyRule{
		{APIGroups: []string{v1.GroupName}, Resources: all, Verbs: []string{"get", "list", "watch"}},
		{APIGroups: []string{v1.GroupName}, Resources: []string{"dispatches", "sections", "stamps", "prints"}, Verbs: []string{"create", "update", "patch", "delete"}},
	}}
	mustCreate(role, func() error { return admin.Create(ctx, role) })
	catalog := &rbacv1.ClusterRole{Name: "ui-catalog", Rules: []rbacv1.PolicyRule{
		{APIGroups: []string{v1.GroupName}, Resources: []string{"clustertypesets", "clusterarchives"}, Verbs: []string{"get", "list", "watch"}},
	}}
	mustCreate(catalog, func() error { return admin.Create(ctx, catalog) })
	group := rbacv1.Subject{APIGroup: rbacv1.GroupName, Kind: "Group", Name: "oidc:blog-alice-editors"}
	rb := &rbacv1.RoleBinding{Namespace: uiNS, Name: "editors",
		RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "ui-editor"}, Subjects: []rbacv1.Subject{group}}
	mustCreate(rb, func() error { return admin.Create(ctx, rb) })
	crb := &rbacv1.ClusterRoleBinding{Name: "ui-catalog",
		RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "ui-catalog"}, Subjects: []rbacv1.Subject{group}}
	mustCreate(crb, func() error { return admin.Create(ctx, crb) })
	g := &v1.Gazette{Namespace: uiNS, Name: "notes", Spec: v1.GazetteSpec{Title: "Alice's Notes", Domain: "ui.example.io"}}
	mustCreate(g, func() error { return admin.Create(ctx, g) })
	ca := &v1.ClusterArchive{Name: "local",
		Spec: v1.ArchiveSpec{PVC: &v1.PVCArchiveSource{ClaimName: "manifesto-media-data", PublicURL: media.URL}}}
	mustCreate(ca, func() error { return admin.Create(ctx, ca) })

	// desk, signing in with the fake issuer the API server trusts. Its ID
	// tokens live for 10s, so desk asks the browser to refresh every few
	// seconds while the UI makes parallel requests and watches; the issuer
	// revokes the session if a refresh token is ever used twice.
	issuer.mu.Lock()
	issuer.lifetime = 10 * time.Second
	issuer.mu.Unlock()
	t.Cleanup(func() {
		issuer.mu.Lock()
		issuer.lifetime = time.Hour
		issuer.mu.Unlock()
	})
	srv := httptest.NewUnstartedServer(nil)
	srv.Start()
	t.Cleanup(srv.Close)
	public, _ := url.Parse(srv.URL)
	a, err := NewAuth(ctx, AuthConfig{
		Issuer: issuer.URL, ClientID: "manifesto-desk", ClientSecret: "s",
		RedirectURL: srv.URL + "/auth/callback", HTTPClient: issuer.Client(), TokenSignIn: true,
	}, newSealer(t))
	if err != nil {
		t.Fatal(err)
	}
	srv.Config.Handler = (&Server{Auth: a, Kube: kube, PublicURL: public, UI: UI(), Log: logr.Discard()}).Handler()

	// A ServiceAccount for signing in with a pasted token.
	sa := &corev1.ServiceAccount{Namespace: uiNS, Name: "token-user"}
	mustCreate(sa, func() error { return admin.Create(ctx, sa) })
	saBinding := &rbacv1.RoleBinding{Namespace: uiNS, Name: "token-user",
		RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "ui-editor"},
		Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: sa.Name, Namespace: uiNS}}}
	mustCreate(saBinding, func() error { return admin.Create(ctx, saBinding) })
	// It may also list Gazettes in every namespace, unlike alice.
	listAll := &rbacv1.ClusterRole{Name: "ui-list-gazettes", Rules: []rbacv1.PolicyRule{
		{APIGroups: []string{v1.GroupName}, Resources: []string{"gazettes"}, Verbs: []string{"list"}},
	}}
	mustCreate(listAll, func() error { return admin.Create(ctx, listAll) })
	listAllBinding := &rbacv1.ClusterRoleBinding{Name: "ui-list-gazettes",
		RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: listAll.Name},
		Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: sa.Name, Namespace: uiNS}}}
	mustCreate(listAllBinding, func() error { return admin.Create(ctx, listAllBinding) })
	tr := &authenticationv1.TokenRequest{Spec: authenticationv1.TokenRequestSpec{ExpirationSeconds: new(int64(3600))}}
	if err := admin.SubResource("token").Create(ctx, sa, tr); err != nil {
		t.Fatal(err)
	}

	shots := os.Getenv("DESK_UI_SHOTS")
	if shots == "" {
		shots = t.TempDir()
	}
	cmd := exec.CommandContext(ctx, "node", filepath.Join("uitest", "desk.cjs"), srv.URL, shots)
	cmd.Env = append(os.Environ(), "DESK_UI_TOKEN="+tr.Status.Token)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	done := make(chan error, 1)
	go func() { done <- cmd.Run() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("UI script failed: %v (screenshots in %s)", err, shots)
		}
	case <-time.After(3 * time.Minute):
		t.Fatal("UI script timed out")
	}
	issuer.mu.Lock()
	defer issuer.mu.Unlock()
	if issuer.reused != 0 || issuer.refresh == 0 {
		t.Errorf("refresh grants = %d, reused refresh tokens = %d; want refreshes and no reuse", issuer.refresh, issuer.reused)
	}
}
