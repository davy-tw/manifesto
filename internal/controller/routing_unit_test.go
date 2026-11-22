/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package controller

import (
	"context"
	"fmt"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
)

func routingGazette() *v1.Gazette {
	return &v1.Gazette{
		Namespace: "blog-alice", Name: "notes",
		Spec: v1.GazetteSpec{Domain: "alice.example.io"},
	}
}

// k3sRouting is press's configuration for the Traefik bundled with k3s.
var k3sRouting = Routing{
	Mode:           ModeGateway,
	Gateway:        types.NamespacedName{Namespace: "kube-system", Name: "traefik-gateway"},
	GatewaySection: "websecure",
	Herald:         types.NamespacedName{Namespace: "manifesto-system", Name: "manifesto-herald"},
	HeraldPort:     80,
}

func TestGatewayModeRoute(t *testing.T) {
	r := &GazetteReconciler{Routing: k3sRouting}
	route := r.httpRoute(routingGazette())
	if len(route.Spec.ParentRefs) != 1 {
		t.Fatalf("parentRefs = %v", route.Spec.ParentRefs)
	}
	p := route.Spec.ParentRefs[0]
	if *p.Kind != "Gateway" || p.Name != "traefik-gateway" || *p.Namespace != "kube-system" || *p.SectionName != "websecure" {
		t.Errorf("parentRef = %+v", p)
	}
	if route.Spec.Hostnames[0] != "alice.example.io" || route.Spec.Rules[0].BackendRefs[0].Name != "manifesto-herald" {
		t.Errorf("route spec = %+v", route.Spec)
	}

	noSection := k3sRouting
	noSection.GatewaySection = ""
	if p := (&GazetteReconciler{Routing: noSection}).httpRoute(routingGazette()).Spec.ParentRefs[0]; p.SectionName != nil {
		t.Errorf("sectionName = %v, want unset", *p.SectionName)
	}
}

func TestListenerSetPorts(t *testing.T) {
	tests := []struct {
		name            string
		routing         Routing
		wantHTTPS, want int32
	}{
		{"defaults", Routing{}, 443, 80},
		{"traefik entrypoints", Routing{HTTPSPort: 8443, HTTPPort: 8000}, 8443, 8000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ls := (&GazetteReconciler{Routing: tt.routing}).listenerSet(routingGazette())
			if ls.Spec.Listeners[0].Port != tt.wantHTTPS || ls.Spec.Listeners[1].Port != tt.want {
				t.Errorf("ports = %d/%d, want %d/%d", ls.Spec.Listeners[0].Port, ls.Spec.Listeners[1].Port, tt.wantHTTPS, tt.want)
			}
		})
	}
}

func TestRoutedCondition(t *testing.T) {
	accepted := func(kind, name string, status metav1.ConditionStatus) gatewayv1.RouteParentStatus {
		return gatewayv1.RouteParentStatus{
			ParentRef:  gatewayv1.ParentReference{Kind: new(gatewayv1.Kind(kind)), Name: gatewayv1.ObjectName(name)},
			Conditions: []metav1.Condition{{Type: "Accepted", Status: status, Reason: "R", Message: "m"}},
		}
	}
	route := func(parents ...gatewayv1.RouteParentStatus) *gatewayv1.HTTPRoute {
		return &gatewayv1.HTTPRoute{Name: "manifesto-notes",
			Status: gatewayv1.HTTPRouteStatus{RouteStatus: gatewayv1.RouteStatus{Parents: parents}}}
	}
	programmed := &gatewayv1.ListenerSet{Name: "manifesto-notes",
		Status: gatewayv1.ListenerSetStatus{Conditions: []metav1.Condition{{Type: "Programmed", Status: metav1.ConditionTrue}}}}
	pending := &gatewayv1.ListenerSet{Name: "manifesto-notes"}

	tests := []struct {
		name   string
		ls     *gatewayv1.ListenerSet
		route  *gatewayv1.HTTPRoute
		kind   string
		parent string
		want   string
	}{
		{"gateway mode accepted", nil, route(accepted("Gateway", "traefik-gateway", "True")), "Gateway", "traefik-gateway", v1.ReasonProgrammed},
		{"gateway mode not yet", nil, route(), "Gateway", "traefik-gateway", v1.ReasonRoutePending},
		{"gateway mode rejected", nil, route(accepted("Gateway", "traefik-gateway", "False")), "Gateway", "traefik-gateway", v1.ReasonRoutePending},
		{"another parent's status is ignored", nil, route(accepted("Gateway", "other", "True")), "Gateway", "traefik-gateway", v1.ReasonRoutePending},
		{"listenerset ready", programmed, route(accepted("ListenerSet", "manifesto-notes", "True")), "ListenerSet", "manifesto-notes", v1.ReasonProgrammed},
		{"listenerset not programmed", pending, route(accepted("ListenerSet", "manifesto-notes", "True")), "ListenerSet", "manifesto-notes", v1.ReasonRoutePending},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := routedCondition(metav1.Condition{Type: v1.ConditionRouted}, tt.ls, tt.route, tt.kind, tt.parent)
			if got.Reason != tt.want {
				t.Errorf("reason = %q (%s), want %q", got.Reason, got.Message, tt.want)
			}
		})
	}
}

// TestApplyBeforeCacheSync covers the first apply of a route: the cache has
// not seen it yet, so reading it back is NotFound. That is no error, and the
// route counts as not accepted yet.
func TestApplyBeforeCacheSync(t *testing.T) {
	s := runtime.NewScheme()
	if err := gatewayv1.Install(s); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(s).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return apierrors.NewNotFound(gatewayv1.Resource("httproutes"), "manifesto-notes")
		},
	}).Build()

	r := &GazetteReconciler{Client: c, Scheme: s, Routing: k3sRouting}
	route := r.httpRoute(routingGazette())
	if err := r.apply(t.Context(), routingGazette(), route); err != nil {
		t.Fatalf("apply = %v, want no error before the cache has the route", err)
	}
	cond := routedCondition(metav1.Condition{}, nil, route, "Gateway", k3sRouting.Gateway.Name)
	if cond.Status != metav1.ConditionFalse || cond.Reason != v1.ReasonRoutePending {
		t.Errorf("Routed = %s/%s, want False/%s", cond.Status, cond.Reason, v1.ReasonRoutePending)
	}

	failing := fake.NewClientBuilder().WithScheme(s).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			return apierrors.NewServiceUnavailable("down")
		},
	}).Build()
	r.Client = failing
	if err := r.apply(t.Context(), routingGazette(), r.httpRoute(routingGazette())); !apierrors.IsServiceUnavailable(err) {
		t.Errorf("apply = %v, want the read error", err)
	}
}

func TestListenerTLS(t *testing.T) {
	wildcard := types.NamespacedName{Namespace: "infra", Name: "wildcard-tls"}
	opts := map[string]string{"example.io/certificates": "managed"}
	tests := []struct {
		name     string
		tls      ListenerTLS
		wantRefs string // namespace/name of the only certificateRef, empty for none
		wantOpts int
	}{
		{"default is per Gazette", ListenerTLS{}, "/manifesto-notes-tls", 0},
		{"per Gazette with options", ListenerTLS{Certificates: TLSPerGazette, Options: opts}, "/manifesto-notes-tls", 1},
		{"shared", ListenerTLS{Certificates: TLSShared, SharedSecret: wildcard}, "infra/wildcard-tls", 0},
		{"none", ListenerTLS{Certificates: TLSNone, Options: opts}, "", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tls := (&GazetteReconciler{Routing: Routing{TLS: tt.tls}}).listenerSet(routingGazette()).Spec.Listeners[0].TLS
			var refs string
			for _, r := range tls.CertificateRefs {
				if r.Namespace != nil {
					refs += string(*r.Namespace)
				}
				refs += "/" + string(r.Name)
			}
			if refs != tt.wantRefs || len(tls.Options) != tt.wantOpts {
				t.Errorf("certificateRefs = %q, options = %v; want %q and %d options", refs, tls.Options, tt.wantRefs, tt.wantOpts)
			}
			if *tls.Mode != gatewayv1.TLSModeTerminate {
				t.Errorf("mode = %s", *tls.Mode)
			}
		})
	}
}

func TestNoHTTP(t *testing.T) {
	rt := Routing{Gateway: types.NamespacedName{Namespace: "infra", Name: "public"}, NoHTTP: true, ClusterIssuer: "le"}
	ls := (&GazetteReconciler{Routing: rt}).listenerSet(routingGazette())
	if len(ls.Spec.Listeners) != 1 || ls.Spec.Listeners[0].Name != listenerHTTPS {
		t.Errorf("listeners = %+v, want only https", ls.Spec.Listeners)
	}
	for _, o := range RoutingObjects(rt, routingGazette()) {
		if o.GetName() == redirectName("notes") {
			t.Error("RoutingObjects has a redirect HTTPRoute without the HTTP listener")
		}
	}
}

func TestRoutingValidate(t *testing.T) {
	gw := types.NamespacedName{Namespace: "infra", Name: "public"}
	secret := types.NamespacedName{Namespace: "infra", Name: "wildcard-tls"}
	opts := map[string]string{"example.io/certificates": "managed"}
	tests := []struct {
		name    string
		routing Routing
		wantErr bool
	}{
		{"routing off", Routing{TLS: ListenerTLS{Certificates: TLSNone}}, false},
		{"gateway mode ignores TLS", Routing{Mode: ModeGateway, Gateway: gw, TLS: ListenerTLS{Certificates: TLSNone}}, false},
		{"per Gazette", Routing{Gateway: gw, ClusterIssuer: "le"}, false},
		{"shared", Routing{Gateway: gw, TLS: ListenerTLS{Certificates: TLSShared, SharedSecret: secret}}, false},
		{"shared without a Secret", Routing{Gateway: gw, TLS: ListenerTLS{Certificates: TLSShared}}, true},
		{"none", Routing{Gateway: gw, TLS: ListenerTLS{Certificates: TLSNone, Options: opts}}, false},
		{"none without options", Routing{Gateway: gw, TLS: ListenerTLS{Certificates: TLSNone}}, true},
		{"issuer with shared", Routing{Gateway: gw, ClusterIssuer: "le",
			TLS: ListenerTLS{Certificates: TLSShared, SharedSecret: secret}}, true},
		{"unknown", Routing{Gateway: gw, TLS: ListenerTLS{Certificates: "wildcard"}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.routing.Validate(); (err != nil) != tt.wantErr {
				t.Errorf("Validate() = %v, want error %v", err, tt.wantErr)
			}
		})
	}
}

// TestRoutingCleanup changes press's configuration under existing objects:
// the Certificate goes with the ClusterIssuer, the redirect with the HTTP
// listener, and the shared Secret's grants follow the Secret.
func TestRoutingCleanup(t *testing.T) {
	s := runtime.NewScheme()
	if err := gatewayv1.Install(s); err != nil {
		t.Fatal(err)
	}
	if err := v1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	s.AddKnownTypeWithName(certificateGVK, &unstructured.Unstructured{})
	g := routingGazette()
	g.UID = "uid"
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(g).Build()
	ctx := t.Context()
	gw := types.NamespacedName{Namespace: "infra", Name: "public"}
	herald := types.NamespacedName{Namespace: "manifesto-system", Name: "manifesto-herald"}
	r := &GazetteReconciler{Client: c, Scheme: s, Routing: Routing{Gateway: gw, Herald: herald, ClusterIssuer: "le"}}
	if _, err := r.reconcileRouting(ctx, g, ""); err != nil {
		t.Fatal(err)
	}
	present := func(o client.Object) bool {
		err := c.Get(ctx, client.ObjectKeyFromObject(o), o)
		if err != nil && !apierrors.IsNotFound(err) {
			t.Fatal(err)
		}
		return err == nil
	}
	redirect := &gatewayv1.HTTPRoute{Namespace: g.Namespace, Name: redirectName(g.Name)}
	if !present(certificateRef(g)) || !present(redirect) {
		t.Fatal("the Certificate and redirect were not created")
	}

	opts := map[string]string{"example.io/certificates": "managed"}
	r.Routing = Routing{Gateway: gw, Herald: herald, NoHTTP: true, TLS: ListenerTLS{Certificates: TLSNone, Options: opts}}
	if _, err := r.reconcileRouting(ctx, g, ""); err != nil {
		t.Fatal(err)
	}
	if present(certificateRef(g)) || present(redirect) {
		t.Error("the Certificate or redirect outlived the configuration that created them")
	}

	grants := func() []string {
		var list gatewayv1.ReferenceGrantList
		if err := c.List(ctx, &list, client.MatchingLabels{labelGrant: "tls"}); err != nil {
			t.Fatal(err)
		}
		names := make([]string, 0, len(list.Items))
		for _, gr := range list.Items {
			names = append(names, gr.Namespace+"/"+gr.Name)
		}
		return names
	}
	for _, step := range []struct {
		tls  ListenerTLS
		want string
	}{
		{ListenerTLS{Certificates: TLSShared, SharedSecret: types.NamespacedName{Namespace: "infra", Name: "wildcard"}}, "[infra/manifesto-blog-alice.tls]"},
		{ListenerTLS{Certificates: TLSShared, SharedSecret: types.NamespacedName{Namespace: "certs", Name: "wildcard"}}, "[certs/manifesto-blog-alice.tls]"},
		{ListenerTLS{}, "[]"},
	} {
		r.Routing.TLS = step.tls
		if err := r.reconcileReferenceGrant(ctx, g.Namespace); err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprint(grants()); got != step.want {
			t.Errorf("with %+v, grants = %s, want %s", step.tls, got, step.want)
		}
	}
}
