/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package controller

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
)

func routingGazette() *v1.Gazette {
	return &v1.Gazette{
		ObjectMeta: metav1.ObjectMeta{Namespace: "blog-alice", Name: "notes"},
		Spec:       v1.GazetteSpec{Domain: "alice.example.io"},
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
		return &gatewayv1.HTTPRoute{ObjectMeta: metav1.ObjectMeta{Name: "manifesto-notes"},
			Status: gatewayv1.HTTPRouteStatus{RouteStatus: gatewayv1.RouteStatus{Parents: parents}}}
	}
	programmed := &gatewayv1.ListenerSet{ObjectMeta: metav1.ObjectMeta{Name: "manifesto-notes"},
		Status: gatewayv1.ListenerSetStatus{Conditions: []metav1.Condition{{Type: "Programmed", Status: metav1.ConditionTrue}}}}
	pending := &gatewayv1.ListenerSet{ObjectMeta: metav1.ObjectMeta{Name: "manifesto-notes"}}

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
