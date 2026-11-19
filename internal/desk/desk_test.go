/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package desk

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
)

// These tests run desk against a real API server. ServiceAccount tokens
// stand in for users' ID tokens: desk forwards either the same way.

const ns = "blog-alice"

var (
	admin  client.Client
	desk   *httptest.Server
	kube   *Kube
	envCfg *rest.Config
	issuer *fakeIssuer
	scheme = runtime.NewScheme()
)

// trustIssuer writes a Structured Authentication Configuration that makes
// the API server accept the fake issuer's ID tokens, as a real cluster
// would trust the OIDC provider desk signs users in with.
func trustIssuer(dir string) (string, error) {
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: issuer.Certificate().Raw})
	cfg := fmt.Sprintf(`apiVersion: apiserver.config.k8s.io/v1
kind: AuthenticationConfiguration
jwt:
- issuer:
    url: %s
    audiences: [manifesto-desk]
    certificateAuthority: |
%s
  claimMappings:
    username: {claim: email, prefix: "oidc:"}
    groups: {claim: groups, prefix: "oidc:"}
`, issuer.URL, indent(string(ca), "      "))
	path := filepath.Join(dir, "authentication-config.yaml")
	return path, os.WriteFile(path, []byte(cfg), 0o600)
}

func indent(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

func TestMain(m *testing.M) {
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, v1.AddToScheme} {
		if err := add(scheme); err != nil {
			panic(err)
		}
	}
	var err error
	if issuer, err = startFakeIssuer(); err != nil {
		panic(err)
	}
	dir, err := os.MkdirTemp("", "desk-test")
	if err != nil {
		panic(err)
	}
	authConfig, err := trustIssuer(dir)
	if err != nil {
		panic(err)
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	env.ControlPlane.GetAPIServer().Configure().Set("authentication-config", authConfig)
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
	envCfg = cfg
	if kube, err = NewKube(cfg, scheme); err != nil {
		panic(err)
	}
	public, _ := url.Parse("http://desk.example.io")
	desk = httptest.NewServer((&Server{Kube: kube, PublicURL: public, Log: logr.Discard()}).Handler())
	code := m.Run()
	desk.Close()
	issuer.Close()
	_ = env.Stop()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// userToken returns a token for a new ServiceAccount bound to role in ns
// ("" for no binding).
func userToken(t *testing.T, name, role string) string {
	t.Helper()
	ctx := context.Background()
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}
	if err := admin.Create(ctx, sa); err != nil {
		t.Fatal(err)
	}
	if role != "" {
		if err := admin.Create(ctx, &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: role},
			Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: name, Namespace: ns}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	tr := &authenticationv1.TokenRequest{Spec: authenticationv1.TokenRequestSpec{ExpirationSeconds: new(int64(3600))}}
	if err := admin.SubResource("token").Create(ctx, sa, tr); err != nil {
		t.Fatal(err)
	}
	return tr.Status.Token
}

func call(t *testing.T, method, path, token, contentType string, body []byte) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, desk.URL+path, bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func installRoles(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	all := []string{"gazettes", "dispatches", "sections", "stamps", "scribes", "typesets", "prints", "archives"}
	for name, rules := range map[string][]rbacv1.PolicyRule{
		"proofreader": {{APIGroups: []string{v1.GroupName}, Resources: all, Verbs: []string{"get", "list", "watch"}}},
		"editor": {
			{APIGroups: []string{v1.GroupName}, Resources: all, Verbs: []string{"get", "list", "watch"}},
			{APIGroups: []string{v1.GroupName}, Resources: []string{"dispatches"}, Verbs: []string{"create", "update", "patch", "delete"}},
		},
	} {
		if err := admin.Create(ctx, &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "test-" + name}, Rules: rules}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDeskAgainstAPIServer(t *testing.T) {
	ctx := context.Background()
	if err := admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
		t.Fatal(err)
	}
	installRoles(t)
	editor := userToken(t, "eve", "test-editor")
	proofreader := userToken(t, "pat", "test-proofreader")
	outsider := userToken(t, "otto", "")
	if err := admin.Create(ctx, &v1.Gazette{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "notes"},
		Spec: v1.GazetteSpec{Title: "Alice's Notes", Domain: "alice.example.io"}}); err != nil {
		t.Fatal(err)
	}
	const dispatches = "/k8s/apis/manifesto.davy.tw/v1alpha1/namespaces/" + ns + "/dispatches"
	apply := func(token, name, rv, title string) (int, []byte) {
		obj := map[string]any{
			"apiVersion": "manifesto.davy.tw/v1alpha1", "kind": "Dispatch",
			"metadata": map[string]any{"name": name, "namespace": ns},
			"spec":     map[string]any{"gazetteRef": "notes", "title": title},
		}
		if rv != "" {
			obj["metadata"].(map[string]any)["resourceVersion"] = rv
		}
		b, _ := json.Marshal(obj)
		return call(t, http.MethodPatch, dispatches+"/"+name+"?fieldManager=manifesto-desk", token, "application/apply-patch+yaml", b)
	}

	t.Run("who am I", func(t *testing.T) {
		code, body := call(t, http.MethodGet, "/api/session", editor, "", nil)
		var info SessionInfo
		_ = json.Unmarshal(body, &info)
		if code != http.StatusOK || info.Username != "system:serviceaccount:"+ns+":eve" {
			t.Errorf("session = %d %s", code, body)
		}
		if code, _ := call(t, http.MethodGet, "/api/session", "", "", nil); code != http.StatusUnauthorized {
			t.Errorf("without a token: %d, want 401", code)
		}
	})

	t.Run("writes with server-side apply as manifesto-desk, as the user", func(t *testing.T) {
		code, body := apply(editor, "hello", "", "Hello")
		if code != http.StatusCreated {
			t.Fatalf("apply = %d %s", code, body)
		}
		var d v1.Dispatch
		if err := admin.Get(ctx, client.ObjectKey{Namespace: ns, Name: "hello"}, &d); err != nil {
			t.Fatal(err)
		}
		if len(d.ManagedFields) == 0 || d.ManagedFields[0].Manager != "manifesto-desk" {
			t.Errorf("managedFields = %+v", d.ManagedFields)
		}

		// A stale resourceVersion is refused with 409, so edits never
		// overwrite each other silently.
		stale := d.ResourceVersion
		if code, _ := apply(editor, "hello", d.ResourceVersion, "Edited once"); code != http.StatusOK {
			t.Fatalf("first edit = %d", code)
		}
		if code, _ := apply(editor, "hello", stale, "Edited from an old copy"); code != http.StatusConflict {
			t.Errorf("stale edit = %d, want 409", code)
		}
	})

	t.Run("RBAC decides, not desk", func(t *testing.T) {
		if code, _ := call(t, http.MethodGet, dispatches, proofreader, "", nil); code != http.StatusOK {
			t.Errorf("proofreader list = %d", code)
		}
		if code, _ := apply(proofreader, "nope", "", "No"); code != http.StatusForbidden {
			t.Errorf("proofreader write = %d, want 403", code)
		}
		if code, _ := call(t, http.MethodGet, dispatches, outsider, "", nil); code != http.StatusForbidden {
			t.Errorf("outsider list = %d, want 403", code)
		}
	})

	t.Run("relays only Manifesto's APIs", func(t *testing.T) {
		for _, p := range []string{
			"/k8s/api/v1/namespaces/" + ns + "/secrets",
			"/k8s/api/v1/namespaces",
			"/k8s/apis/apps/v1/deployments",
			"/k8s/apis/manifesto.davy.tw/v1alpha1/namespaces/" + ns + "/dispatches/../../../../api/v1/secrets",
		} {
			if code, _ := call(t, http.MethodGet, p, editor, "", nil); code != http.StatusNotFound && code != http.StatusMovedPermanently {
				t.Errorf("GET %s = %d, want 404", p, code)
			}
		}
	})

	t.Run("streams watches", func(t *testing.T) {
		code, body := call(t, http.MethodGet, dispatches+"?watch=true&timeoutSeconds=1", editor, "", nil)
		if code != http.StatusOK || !bytes.Contains(body, []byte(`"type":"ADDED"`)) {
			t.Errorf("watch = %d %s", code, body)
		}
	})

	t.Run("previews with the Gazette's theme and the user's permissions", func(t *testing.T) {
		d := v1.Dispatch{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "draft"}, Spec: v1.DispatchSpec{
			GazetteRef: "notes", Title: "Draft title", Content: v1.DispatchContent{Body: "# Heading\n\n<script>x</script>body"},
		}}
		b, _ := json.Marshal(PreviewRequest{Dispatch: d})
		code, page := call(t, http.MethodPost, "/api/preview", editor, "application/json", b)
		if code != http.StatusOK {
			t.Fatalf("preview = %d %s", code, page)
		}
		for _, s := range []string{"<h1>Draft title</h1>", "Alice&#39;s Notes", "<h1>Heading</h1>"} {
			if !strings.Contains(string(page), s) {
				t.Errorf("preview does not contain %q", s)
			}
		}
		if strings.Contains(string(page), "<script>x") {
			t.Error("preview is not sanitized")
		}
		if code, _ := call(t, http.MethodPost, "/api/preview", outsider, "application/json", b); code != http.StatusForbidden {
			t.Errorf("outsider preview = %d, want 403", code)
		}
	})
}

func TestCookieSessionsNeedTheCSRFHeader(t *testing.T) {
	issuer := newFakeIssuer(t)
	a := newAuth(t, issuer)
	public, _ := url.Parse("http://desk")
	srv := (&Server{Auth: a, Kube: &Kube{}, PublicURL: public, Log: logr.Discard()}).Handler()
	b := signIn(t, issuer, a)
	b.handler = srv

	try := func(header http.Header) int {
		req := httptest.NewRequest(http.MethodPost, "http://desk/api/preview", strings.NewReader("{}"))
		maps.Copy(req.Header, header)
		for _, c := range b.cookies {
			req.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec.Code
	}
	if got := try(nil); got != http.StatusForbidden {
		t.Errorf("without the header: %d, want 403", got)
	}
	if got := try(http.Header{CSRFHeader: {"1"}, "Origin": {"https://evil.example"}}); got != http.StatusForbidden {
		t.Errorf("from another origin: %d, want 403", got)
	}
	// With the header from desk's own origin the request goes through (and
	// fails later only because the body is not a Dispatch).
	if got := try(http.Header{CSRFHeader: {"1"}, "Origin": {"http://desk"}}); got != http.StatusBadRequest {
		t.Errorf("same origin with the header: %d, want 400 from the handler", got)
	}
}

// TestOIDCThroughTheAPIServer signs in with the provider, then lets desk
// forward the ID token: the API server itself authenticates the user.
func TestOIDCThroughTheAPIServer(t *testing.T) {
	a := newAuth(t, issuer)
	public, _ := url.Parse("http://desk")
	srv := (&Server{Auth: a, Kube: kube, PublicURL: public, Log: logr.Discard()}).Handler()
	b := signIn(t, issuer, a)
	b.handler = srv

	resp := b.do(http.MethodGet, "http://desk/api/session", nil)
	body, _ := io.ReadAll(resp.Body)
	var info SessionInfo
	_ = json.Unmarshal(body, &info)
	if resp.StatusCode != http.StatusOK || info.Username != "oidc:alice@example.io" || info.Name != "Alice" {
		t.Fatalf("session = %d %s", resp.StatusCode, body)
	}
	if !slices.Contains(info.Groups, "oidc:blog-alice-editors") {
		t.Errorf("groups = %v, want the provider's groups mapped with the oidc: prefix", info.Groups)
	}
}
