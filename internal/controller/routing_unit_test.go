/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package controller

import (
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
