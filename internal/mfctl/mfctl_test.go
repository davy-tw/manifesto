/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/
package mfctl

import (
	"bytes"
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
	"go.pkgs.davy.tw/manifesto/internal/controller"
	"go.pkgs.davy.tw/manifesto/internal/darkroom"
)

var c client.Client

// TestMain runs envtest with press and an in-process darkroom, as in a
// real installation.
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
	scheme, err := Scheme()
	if err != nil {
		panic(err)
	}
	if c, err = client.New(cfg, client.Options{Scheme: scheme}); err != nil {
		panic(err)
	}
	dir, err := os.MkdirTemp("", "mfctl-darkroom")
	if err != nil {
		panic(err)
	}
	key := []byte("mfctl-test-signing-key")
	store := &darkroom.Store{Dir: dir, Keys: func() [][]byte { return [][]byte{key} }, Log: logr.Discard()}
	media := httptest.NewServer(store.PublicHandler())
	internal := httptest.NewServer(store.InternalHandler())
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: scheme, Metrics: metricsserver.Options{BindAddress: "0"}})
	if err != nil {
		panic(err)
	}
	if err := controller.Setup(mgr, controller.Options{Media: controller.Media{
		DarkroomURL: internal.URL, DarkroomClaim: "media", SigningKey: key, Namespace: "default",
	}}); err != nil {
		panic(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = mgr.Start(ctx) }()
	if err := c.Create(ctx, &v1.ClusterArchive{ObjectMeta: metav1.ObjectMeta{Name: "local"},
		Spec: v1.ArchiveSpec{PVC: &v1.PVCArchiveSource{ClaimName: "media", PublicURL: media.URL}}}); err != nil {
		panic(err)
	}
	code := m.Run()
	cancel()
	media.Close()
	internal.Close()
	_ = env.Stop()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// run runs mfctl with args in namespace ns and returns its output.
func run(t *testing.T, ns string, args ...string) (string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	env := &Env{
		Out: &out, Err: &errOut, Poll: 50 * time.Millisecond,
		Client:    func() (client.Client, error) { return c, nil },
		Namespace: func() (string, error) { return ns, nil },
	}
	cmd := NewCommand(env)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return out.String(), err
}

func namespace(t *testing.T, name string) string {
	t.Helper()
	if err := c.Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}); err != nil {
		t.Fatal(err)
	}
	return name
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestUpload(t *testing.T) {
	ns := namespace(t, "upload")
	file := filepath.Join(t.TempDir(), "My Photo.PNG")
	png := []byte("\x89PNG\r\n\x1a\n not really a picture")
	if err := os.WriteFile(file, png, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, ns, "print", "upload", file, "--alt", "A photo")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "print:my-photo\thttp") {
		t.Errorf("output = %q, want print:my-photo and its URL", out)
	}
	var p v1.Print
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "my-photo"}, &p); err != nil {
		t.Fatal(err)
	}
	if p.Status.Phase != v1.PrintReady || p.Spec.ContentType != "image/png" || p.Spec.Alt != "A photo" ||
		p.Spec.ArchiveRef != (v1.ArchiveRef{Kind: v1.KindClusterArchive, Name: "local"}) {
		t.Errorf("print = %+v %+v", p.Spec, p.Status)
	}

	// The same file under another name is stored already.
	if _, err := run(t, ns, "print", "upload", file, "--name", "again"); err != nil {
		t.Fatal(err)
	}
	// Uploading the same name with another file is refused by the API server.
	if err := os.WriteFile(file, append(png, '!'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, ns, "print", "upload", file); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Errorf("re-uploading a different file as my-photo: err = %v, want immutable", err)
	}
	if _, err := run(t, ns, "print", "upload", filepath.Join(t.TempDir(), "x.unknown-ext")); err == nil {
		t.Error("a missing file was accepted")
	}
}

func TestEditions(t *testing.T) {
	ctx := context.Background()
	ns := namespace(t, "editions")
	d := &v1.Dispatch{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "story"},
		Spec: v1.DispatchSpec{GazetteRef: "notes", Title: "First", Content: v1.DispatchContent{Body: "one"}}}
	if err := c.Create(ctx, d); err != nil {
		t.Fatal(err)
	}
	edit := func(mutate func(*v1.Dispatch)) {
		t.Helper()
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			if err := c.Get(ctx, client.ObjectKeyFromObject(d), d); err != nil {
				return err
			}
			mutate(d)
			return c.Update(ctx, d)
		}); err != nil {
			t.Fatal(err)
		}
	}
	count := func(n int) func() bool {
		return func() bool {
			e, err := Editions(ctx, c, ns, "story")
			return err == nil && len(e) == n
		}
	}
	eventually(t, "edition 1", count(1))
	edit(func(d *v1.Dispatch) {
		d.Spec.Title, d.Spec.Summary, d.Spec.Content.Body = "Second", "added later", "two"
		d.Spec.State = v1.StatePublished
	})
	eventually(t, "edition 2", count(2))

	out, err := run(t, ns, "edition", "list", "story")
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Split(strings.TrimSpace(out), "\n"); len(lines) != 3 ||
		!strings.HasPrefix(lines[1], "2 ") || !strings.HasSuffix(lines[2], "First") {
		t.Errorf("list =\n%s", out)
	}
	if out, err := run(t, ns, "edition", "show", "story", "1"); err != nil || out != "one" {
		t.Errorf("show = %q, %v", out, err)
	}
	if _, err := run(t, ns, "edition", "show", "story", "9"); err == nil {
		t.Error("showing a missing edition succeeded")
	}

	if _, err := run(t, ns, "edition", "restore", "story", "1"); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(d), d); err != nil {
		t.Fatal(err)
	}
	if d.Spec.Title != "First" || d.Spec.Summary != "" || d.Spec.Content.Body != "one" || d.Spec.State != v1.StatePublished {
		t.Errorf("restored spec = %+v; want edition 1's content and the publishing left alone", d.Spec)
	}
	eventually(t, "the restore recorded as edition 3", count(3))
}

func TestBuildTypeset(t *testing.T) {
	theme := filepath.Join("..", "render", "theme")
	obj, err := BuildTypeset(theme, "plain", "blog", false, []string{"accent=#c00"})
	if err != nil {
		t.Fatal(err)
	}
	ts := obj.(*v1.Typeset)
	if ts.Namespace != "blog" || ts.Spec.Params["accent"] != "#c00" || ts.Spec.Source.Inline.Templates["layout.html"] == "" ||
		ts.Spec.Source.Inline.Assets["style.css"] == "" {
		t.Errorf("typeset = %+v", ts)
	}
	if obj, err := BuildTypeset(theme, "plain", "", true, nil); err != nil || obj.(*v1.ClusterTypeset).Name != "plain" {
		t.Errorf("cluster typeset: %v", err)
	}

	write := func(dir string, files map[string]string) string {
		for name, body := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	broken := write(t.TempDir(), map[string]string{
		"layout.html": `{{ block "content" . }}{{ end }}`, "index.html": "x", "dispatch.html": "{{ if }}",
	})
	if _, err := BuildTypeset(broken, "b", "ns", false, nil); err == nil || !strings.Contains(err.Error(), "does not parse") {
		t.Errorf("broken template: err = %v", err)
	}
	nested := t.TempDir()
	if err := os.Mkdir(filepath.Join(nested, "img"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildTypeset(nested, "n", "ns", false, nil); err == nil || !strings.Contains(err.Error(), "flat") {
		t.Errorf("nested folder: err = %v", err)
	}
	binary := write(t.TempDir(), map[string]string{"logo.png": "\xff\xfe\x00"})
	if _, err := BuildTypeset(binary, "b", "ns", false, nil); err == nil || !strings.Contains(err.Error(), "not text") {
		t.Errorf("binary asset: err = %v", err)
	}
	if _, err := BuildTypeset(theme, "p", "ns", false, []string{"novalue"}); err == nil {
		t.Error("a --param without = was accepted")
	}

	out, err := run(t, "blog", "typeset", "build", theme, "--name", "plain")
	if err != nil || !strings.Contains(out, "kind: Typeset") || !strings.Contains(out, "namespace: blog") {
		t.Errorf("typeset build = %q, %v", out, err)
	}
}

func TestPrintName(t *testing.T) {
	for in, want := range map[string]string{
		"photo.jpg":                      "photo",
		"/tmp/My Photo.PNG":              "my-photo",
		"__weird--name__.webp":           "weird-name",
		"中文.png":                         "",
		strings.Repeat("a", 70) + ".gif": strings.Repeat("a", 63),
	} {
		if got := PrintName(in); got != want {
			t.Errorf("PrintName(%q) = %q, want %q", in, got, want)
		}
	}
}
