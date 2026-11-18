/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package herald_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
	"go.pkgs.davy.tw/manifesto/internal/controller"
	"go.pkgs.davy.tw/manifesto/internal/herald"
	"go.pkgs.davy.tw/manifesto/internal/render"
)

// TestMilestone1 runs press and herald against a real API server: a
// Dispatch applied with a future publishAt is 404 until press publishes it,
// then herald serves it.
func TestMilestone1(t *testing.T) {
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	if dirs, _ := filepath.Glob(filepath.Join("..", "..", "bin", "k8s", "*")); len(dirs) > 0 {
		env.BinaryAssetsDirectory = dirs[0]
	}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = env.Stop() })

	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, v1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// press
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: scheme, Metrics: metricsserver.Options{BindAddress: "0"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := (&controller.GazetteReconciler{Client: mgr.GetClient(), Scheme: scheme}).SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	if err := (&controller.DispatchReconciler{Client: mgr.GetClient(), Scheme: scheme}).SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	go func() { _ = mgr.Start(ctx) }()

	// herald
	c, err := cache.New(cfg, cache.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = c.Start(ctx) }()
	srv := &herald.Server{}
	go func() {
		_ = herald.Watch(ctx, c, &herald.Builder{Theme: render.DefaultTheme(), Log: logr.Discard()}, srv)
	}()
	site := httptest.NewServer(srv)
	t.Cleanup(site.Close)

	// The author applies a Gazette and a Dispatch scheduled a few seconds ahead.
	k8s, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	const ns, dom = "blog-alice", "alice.example.io"
	mustCreate(t, k8s, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
	mustCreate(t, k8s, &v1.Gazette{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "alice-notes"},
		Spec:       v1.GazetteSpec{Title: "Alice's Notes", Domain: dom},
	})
	// The API server stores times at second precision.
	publishAt := metav1.NewTime(time.Now().Add(3 * time.Second).Truncate(time.Second))
	mustCreate(t, k8s, &v1.Dispatch{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "hello-k8s"},
		Spec: v1.DispatchSpec{
			GazetteRef: "alice-notes", Title: "Hello K8s", State: v1.StatePublished, PublishAt: &publishAt,
			Content: v1.DispatchContent{Body: "# Hello\n\nfrom a CRD"},
		},
	})

	fetch := func(path string) (int, string) {
		req, _ := http.NewRequest(http.MethodGet, site.URL+path, nil)
		req.Host = dom
		resp, err := site.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	// Embargoed: the site is up but the Dispatch is not visible yet.
	eventually(t, 10*time.Second, func() bool { code, _ := fetch("/"); return code == http.StatusOK })
	if time.Now().Before(publishAt.Time) {
		if code, _ := fetch("/posts/hello-k8s"); code != http.StatusNotFound {
			t.Errorf("before publishAt: status = %d, want 404", code)
		}
	}

	// After publishAt, press publishes it and herald serves it.
	eventually(t, 15*time.Second, func() bool { code, _ := fetch("/posts/hello-k8s"); return code == http.StatusOK })
	if time.Now().Before(publishAt.Time) {
		t.Error("served before publishAt")
	}
	_, page := fetch("/posts/hello-k8s")
	if !strings.Contains(page, "<p>from a CRD</p>") {
		t.Errorf("page does not contain the body:\n%s", page)
	}
	if _, index := fetch("/"); !strings.Contains(index, `href="/posts/hello-k8s"`) {
		t.Errorf("index does not list the Dispatch:\n%s", index)
	}
}

func mustCreate(t *testing.T, c client.Client, obj client.Object) {
	t.Helper()
	if err := c.Create(context.Background(), obj); err != nil {
		t.Fatal(err)
	}
}

func eventually(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(100 * time.Millisecond)
	}
}
