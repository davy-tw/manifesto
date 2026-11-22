/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/
// Package chart checks the Helm chart against the kustomize install
// (config/default): the same objects, the same CRDs and RBAC, and pods that
// run the same way. It needs bin/helm and bin/kustomize (make helm kustomize).
package chart

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	"sigs.k8s.io/yaml"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
	"go.pkgs.davy.tw/manifesto/internal/controller"
)

const ns = "manifesto-system"

var root = filepath.Join("..", "..")

func tool(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(root, "bin", name)
	if _, err := os.Stat(path); err != nil {
		t.Skipf("%s not found; run make %s", path, name)
	}
	return path
}

func objects(t *testing.T, out []byte) map[string]*unstructured.Unstructured {
	t.Helper()
	objs := map[string]*unstructured.Unstructured{}
	r := utilyaml.NewYAMLReader(bufioReader(out))
	for {
		doc, err := r.Read()
		if errors.Is(err, io.EOF) {
			return objs
		}
		if err != nil {
			t.Fatal(err)
		}
		u := &unstructured.Unstructured{}
		if err := yaml.Unmarshal(doc, &u.Object); err != nil {
			t.Fatal(err)
		}
		if u.Object == nil {
			continue
		}
		objs[key(u)] = u
	}
}

func key(u *unstructured.Unstructured) string {
	return u.GetKind() + "/" + u.GetNamespace() + "/" + u.GetName()
}

func helmTemplate(t *testing.T, set ...string) (map[string]*unstructured.Unstructured, error) {
	t.Helper()
	args := make([]string, 0, 5+2*len(set))
	args = append(args, "template", "m", filepath.Join(root, "charts", "manifesto"), "-n", ns)
	for _, s := range set {
		args = append(args, "--set", s)
	}
	var stderr bytes.Buffer
	cmd := exec.Command(tool(t, "helm"), args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, errors.New(stderr.String())
	}
	return objects(t, out), nil
}

func convert[T any](t *testing.T, u *unstructured.Unstructured) *T {
	t.Helper()
	var v T
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &v); err != nil {
		t.Fatal(err)
	}
	return &v
}

func TestChartMatchesKustomize(t *testing.T) {
	out, err := exec.Command(tool(t, "kustomize"), "build", filepath.Join(root, "config", "default")).Output()
	if err != nil {
		t.Fatal(err)
	}
	want := objects(t, out)
	delete(want, "Namespace//"+ns) // Helm installs into an existing namespace.
	got, err := helmTemplate(t, "desk.publicURL=https://desk.example.io")
	if err != nil {
		t.Fatal(err)
	}

	keys := func(m map[string]*unstructured.Unstructured) []string {
		k := make([]string, 0, len(m))
		for key := range m {
			k = append(k, key)
		}
		slices.Sort(k)
		return k
	}
	if !slices.Equal(keys(got), keys(want)) {
		t.Fatalf("objects differ\nchart:     %v\nkustomize: %v", keys(got), keys(want))
	}

	for k, w := range want {
		g := got[k]
		switch w.GetKind() {
		case "CustomResourceDefinition", "Service", "PersistentVolumeClaim", "NetworkPolicy":
			if !reflect.DeepEqual(g.Object["spec"], w.Object["spec"]) {
				t.Errorf("%s: spec differs", k)
			}
		case "ClusterRole", "Role":
			if !reflect.DeepEqual(g.Object["rules"], w.Object["rules"]) {
				t.Errorf("%s: rules differ", k)
			}
		case "ClusterRoleBinding", "RoleBinding":
			if !reflect.DeepEqual(g.Object["roleRef"], w.Object["roleRef"]) ||
				!reflect.DeepEqual(g.Object["subjects"], w.Object["subjects"]) {
				t.Errorf("%s: binding differs", k)
			}
		case "Deployment":
			comparePods(t, k, convert[appsv1.Deployment](t, g), convert[appsv1.Deployment](t, w))
		}
	}
	crd := got["CustomResourceDefinition//dispatches.manifesto.davy.tw"]
	if crd.GetAnnotations()["helm.sh/resource-policy"] != "keep" {
		t.Error("CRDs must be kept on uninstall: deleting one deletes every post")
	}
}

// comparePods checks that the chart runs a component as kustomize does.
// Images and arguments are the chart's to choose, and checked separately.
func comparePods(t *testing.T, k string, g, w *appsv1.Deployment) {
	t.Helper()
	strip := func(d *appsv1.Deployment) appsv1.DeploymentSpec {
		s := *d.Spec.DeepCopy()
		for i := range s.Template.Spec.Containers {
			c := &s.Template.Spec.Containers[i]
			c.Image, c.ImagePullPolicy, c.Args = "", "", nil
			if len(c.VolumeMounts) == 0 {
				c.VolumeMounts = nil
			}
		}
		if len(s.Template.Spec.Volumes) == 0 {
			s.Template.Spec.Volumes = nil
		}
		if s.Replicas == nil {
			s.Replicas = new(int32(1))
		}
		return s
	}
	gs, ws := strip(g), strip(w)
	if !reflect.DeepEqual(gs, ws) {
		a, _ := yaml.Marshal(gs)
		b, _ := yaml.Marshal(ws)
		t.Errorf("%s: pod spec differs\nchart:\n%s\nkustomize:\n%s", k, a, b)
	}
}

func args(t *testing.T, objs map[string]*unstructured.Unstructured, deployment string) []string {
	t.Helper()
	u := objs["Deployment/"+ns+"/"+deployment]
	if u == nil {
		t.Fatalf("no Deployment %s", deployment)
	}
	return convert[appsv1.Deployment](t, u).Spec.Template.Spec.Containers[0].Args
}

func TestChartValues(t *testing.T) {
	t.Run("desk needs its public URL", func(t *testing.T) {
		if _, err := helmTemplate(t); err == nil || !strings.Contains(err.Error(), "desk.publicURL is required") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("press finds herald and darkroom in the release namespace", func(t *testing.T) {
		objs, err := helmTemplate(t, "desk.publicURL=https://d", "imageRegistry=ghcr.io/me/", "press.image.tag=v9")
		if err != nil {
			t.Fatal(err)
		}
		a := args(t, objs, "manifesto-controller-manager")
		for _, want := range []string{"--herald-service=" + ns + "/manifesto-herald",
			"--darkroom-url=http://manifesto-darkroom." + ns + ".svc:8081", "--editions=20", "--edition-bytes=2Mi"} {
			if !slices.Contains(a, want) {
				t.Errorf("press args %v lack %s", a, want)
			}
		}
		d := convert[appsv1.Deployment](t, objs["Deployment/"+ns+"/manifesto-controller-manager"])
		if img := d.Spec.Template.Spec.Containers[0].Image; img != "ghcr.io/me/manifesto-press:v9" {
			t.Errorf("image = %s", img)
		}
		h := convert[appsv1.Deployment](t, objs["Deployment/"+ns+"/manifesto-herald"])
		if img := h.Spec.Template.Spec.Containers[0].Image; img != "ghcr.io/me/manifesto-herald:v0.1.0" {
			t.Errorf("herald image = %s, want the appVersion tag", img)
		}
	})

	t.Run("k3s: gateway routing mode", func(t *testing.T) {
		objs, err := helmTemplate(t, "desk.publicURL=https://d", "press.routing.gatewayName=manifesto",
			"press.routing.gatewayNamespace=kube-system", "press.routing.mode=gateway", "press.routing.gatewaySection=websecure")
		if err != nil {
			t.Fatal(err)
		}
		a := args(t, objs, "manifesto-controller-manager")
		for _, want := range []string{"--gateway-name=manifesto", "--gateway-namespace=kube-system",
			"--routing-mode=gateway", "--gateway-section=websecure"} {
			if !slices.Contains(a, want) {
				t.Errorf("press args %v lack %s", a, want)
			}
		}
		if slices.ContainsFunc(a, func(s string) bool { return strings.HasPrefix(s, "--listener-") }) {
			t.Errorf("gateway mode got listener flags: %v", a)
		}
	})

	t.Run("without darkroom and desk", func(t *testing.T) {
		objs, err := helmTemplate(t, "darkroom.enabled=false", "desk.enabled=false")
		if err != nil {
			t.Fatal(err)
		}
		for _, k := range []string{"Deployment/" + ns + "/manifesto-darkroom",
			"PersistentVolumeClaim/" + ns + "/manifesto-media-data",
			"Deployment/" + ns + "/manifesto-desk", "Service/" + ns + "/manifesto-desk"} {
			if objs[k] != nil {
				t.Errorf("%s rendered while disabled", k)
			}
		}
		a := args(t, objs, "manifesto-controller-manager")
		if !slices.Contains(a, "--darkroom-url=") || !slices.Contains(a, "--desk-session-secret=") {
			t.Errorf("press args %v: want the pvc backend and desk's key turned off", a)
		}
	})

	t.Run("desk sign-in and an existing media volume", func(t *testing.T) {
		objs, err := helmTemplate(t, "desk.publicURL=https://desk.blog.example.io",
			"desk.oidc.issuer=https://auth.example.io",
			"desk.tokenSignIn=true", "darkroom.persistence.existingClaim=media")
		if err != nil {
			t.Fatal(err)
		}
		a := args(t, objs, "manifesto-desk")
		for _, want := range []string{"--public-url=https://desk.blog.example.io", "--oidc-issuer=https://auth.example.io",
			"--oidc-client-id=manifesto-desk", "--oidc-scopes=openid,email,profile", "--token-sign-in"} {
			if !slices.Contains(a, want) {
				t.Errorf("desk args %v lack %s", a, want)
			}
		}
		if objs["PersistentVolumeClaim/"+ns+"/manifesto-media-data"] != nil {
			t.Error("a PVC was created next to existingClaim")
		}
		dr := convert[appsv1.Deployment](t, objs["Deployment/"+ns+"/manifesto-darkroom"])
		var claim string
		for _, v := range dr.Spec.Template.Spec.Volumes {
			if v.PersistentVolumeClaim != nil {
				claim = v.PersistentVolumeClaim.ClaimName
			}
		}
		if claim != "media" || !slices.Contains(args(t, objs, "manifesto-controller-manager"), "--darkroom-claim=media") {
			t.Errorf("darkroom claim = %q; press must name the same claim", claim)
		}
	})

	t.Run("subjects follow the release namespace", func(t *testing.T) {
		out, err := exec.Command(tool(t, "helm"), "template", "m", filepath.Join(root, "charts", "manifesto"),
			"-n", "blog-platform", "--set", "desk.publicURL=https://d").Output()
		if err != nil {
			t.Fatal(err)
		}
		objs := objects(t, out)
		rb := convert[rbacv1.ClusterRoleBinding](t, objs["ClusterRoleBinding//manifesto-manager-rolebinding"])
		if rb.Subjects[0].Namespace != "blog-platform" {
			t.Errorf("subject namespace = %s", rb.Subjects[0].Namespace)
		}
		if objs["Role/blog-platform/manifesto-manager-role"] == nil {
			t.Error("press's namespaced Role is not in the release namespace")
		}
		svc := convert[corev1.Service](t, objs["Service/blog-platform/manifesto-herald"])
		if svc.Name == "" {
			t.Error("no herald Service in the release namespace")
		}
	})
}

func bufioReader(b []byte) *bufio.Reader { return bufio.NewReader(bytes.NewReader(b)) }

// TestChartRoutes checks desk's and darkroom's routes: the objects press
// applies for a Gazette of that hostname in the release namespace, in both
// routing modes, unless parentRefs takes over.
func TestChartRoutes(t *testing.T) {
	base := []string{"desk.publicURL=https://desk.example.io", "desk.route.enabled=true",
		"darkroom.publicURL=https://media.example.io:8443", "darkroom.route.enabled=true",
		"press.routing.gatewayName=gw", "press.routing.gatewayNamespace=infra"}
	gateway := types.NamespacedName{Namespace: "infra", Name: "gw"}

	modes := []struct {
		name    string
		set     []string
		routing controller.Routing
	}{
		{"gateway mode", []string{"press.routing.mode=gateway", "press.routing.gatewaySection=websecure"},
			controller.Routing{Mode: controller.ModeGateway, Gateway: gateway, GatewaySection: "websecure"}},
		{"listenerset mode", []string{"press.routing.listenerHTTPSPort=8443", "press.routing.listenerHTTPPort=8000"},
			controller.Routing{Mode: controller.ModeListenerSet, Gateway: gateway, HTTPSPort: 8443, HTTPPort: 8000}},
		{"listenerset mode with cert-manager", []string{"press.routing.clusterIssuer=letsencrypt"},
			controller.Routing{Mode: controller.ModeListenerSet, Gateway: gateway, HTTPSPort: 443, HTTPPort: 80,
				ClusterIssuer: "letsencrypt"}},
	}
	for _, m := range modes {
		t.Run(m.name, func(t *testing.T) {
			objs, err := helmTemplate(t, append(slices.Clone(base), m.set...)...)
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]bool{}
			for component, host := range map[string]string{"desk": "desk.example.io", "darkroom": "media.example.io"} {
				rt := m.routing
				rt.Herald, rt.HeraldPort = types.NamespacedName{Namespace: ns, Name: "manifesto-" + component}, 80
				g := &v1.Gazette{Namespace: ns, Name: component, Spec: v1.GazetteSpec{Domain: host}}
				for _, o := range controller.RoutingObjects(rt, g) {
					w := toUnstructured(t, o)
					k := key(w)
					want[k] = true
					got := objs[k]
					if got == nil {
						t.Errorf("%s not rendered", k)
						continue
					}
					// Compared as YAML: the chart's numbers come back as float64.
					a, _ := yaml.Marshal(got.Object["spec"])
					b, _ := yaml.Marshal(w.Object["spec"])
					if !bytes.Equal(a, b) {
						t.Errorf("%s: spec differs from press\nchart:\n%s\npress:\n%s", k, a, b)
					}
					if got.GetLabels()["app.kubernetes.io/component"] != component {
						t.Errorf("%s labels = %v", k, got.GetLabels())
					}
				}
			}
			for k, o := range objs {
				switch o.GetKind() {
				case "HTTPRoute", "ListenerSet", "Certificate":
					if !want[k] {
						t.Errorf("%s rendered, press would not create it", k)
					}
				}
			}
		})
	}

	t.Run("parentRefs replaces the Gateway", func(t *testing.T) {
		objs, err := helmTemplate(t, "desk.publicURL=https://desk.example.io", "desk.route.enabled=true",
			"desk.route.parentRefs[0].name=other", "desk.route.parentRefs[0].sectionName=https")
		if err != nil {
			t.Fatal(err)
		}
		r := convert[gatewayv1.HTTPRoute](t, objs["HTTPRoute/"+ns+"/manifesto-desk"])
		if len(r.Spec.ParentRefs) != 1 || r.Spec.ParentRefs[0].Name != "other" || r.Spec.Hostnames[0] != "desk.example.io" {
			t.Errorf("route spec = %+v", r.Spec)
		}
		if objs["ListenerSet/"+ns+"/manifesto-desk"] != nil {
			t.Error("a ListenerSet was rendered next to parentRefs")
		}
	})

	t.Run("off by default", func(t *testing.T) {
		objs, err := helmTemplate(t, "desk.publicURL=https://desk.example.io", "press.routing.gatewayName=gw",
			"press.routing.gatewayNamespace=infra")
		if err != nil {
			t.Fatal(err)
		}
		for k, o := range objs {
			if strings.HasPrefix(o.GetAPIVersion(), "gateway.networking.k8s.io/") {
				t.Errorf("%s rendered without route.enabled", k)
			}
		}
	})

	t.Run("not without darkroom", func(t *testing.T) {
		objs, err := helmTemplate(t, "desk.publicURL=https://d", "darkroom.enabled=false",
			"darkroom.route.enabled=true", "press.routing.gatewayName=gw", "press.routing.gatewayNamespace=infra")
		if err != nil {
			t.Fatal(err)
		}
		if objs["HTTPRoute/"+ns+"/manifesto-darkroom"] != nil {
			t.Error("darkroom's route rendered while darkroom is disabled")
		}
	})

	for _, tc := range []struct {
		name string
		set  []string
		want string
	}{
		{"needs a Gateway", []string{"desk.publicURL=https://d.example.io", "desk.route.enabled=true"},
			"desk.route needs press.routing.gatewayName"},
		{"needs darkroom's publicURL", []string{"desk.publicURL=https://d", "darkroom.route.enabled=true",
			"press.routing.gatewayName=gw", "press.routing.gatewayNamespace=infra"}, "darkroom.publicURL is required"},
		{"needs a hostname", []string{"desk.publicURL=desk", "desk.route.enabled=true",
			"press.routing.gatewayName=gw", "press.routing.gatewayNamespace=infra"}, "needs a publicURL with a hostname"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := helmTemplate(t, tc.set...); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func toUnstructured(t *testing.T, o client.Object) *unstructured.Unstructured {
	t.Helper()
	if u, ok := o.(*unstructured.Unstructured); ok {
		return u
	}
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(o)
	if err != nil {
		t.Fatal(err)
	}
	u := &unstructured.Unstructured{Object: m}
	gvk, err := apiutil.GVKForObject(o, scheme())
	if err != nil {
		t.Fatal(err)
	}
	u.SetGroupVersionKind(gvk)
	return u
}

func scheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = gatewayv1.Install(s)
	return s
}
