/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package controller

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
)

// RoutingMode is how a Gazette's HTTPRoute reaches the shared Gateway.
type RoutingMode string

const (
	// ModeListenerSet gives each Gazette a ListenerSet with its own HTTPS
	// and HTTP listeners (and, with ClusterIssuer, its own certificate).
	// The Gateway implementation must support ListenerSet.
	ModeListenerSet RoutingMode = "listenerset"
	// ModeGateway attaches each Gazette's HTTPRoute directly to the shared
	// Gateway, whose listeners (and TLS certificates) the cluster operator
	// manages, e.g. one HTTPS listener with a wildcard certificate. Works
	// with any Gateway implementation, such as the Traefik bundled with k3s.
	ModeGateway RoutingMode = "gateway"
)

// Routing configures how press exposes Gazettes through Gateway API. It is
// disabled when Gateway.Name is empty.
type Routing struct {
	// Mode selects ModeListenerSet (the default) or ModeGateway.
	Mode RoutingMode
	// Gateway is the shared Gateway. In ModeListenerSet it must allow
	// ListenerSets from the Gazettes' namespaces (spec.allowedListeners); in
	// ModeGateway its listeners must allow HTTPRoutes from them.
	Gateway types.NamespacedName
	// GatewaySection optionally names the Gateway listener HTTPRoutes attach
	// to in ModeGateway (parentRef sectionName).
	GatewaySection string
	// HTTPSPort and HTTPPort are the ListenerSet listener ports (default 443
	// and 80). Some implementations require the ports of their entrypoints;
	// the Traefik bundled with k3s uses 8443 and 8000.
	HTTPSPort, HTTPPort int32
	// Herald is herald's Service, which every HTTPRoute sends traffic to.
	Herald     types.NamespacedName
	HeraldPort int32
	// ClusterIssuer is the cert-manager ClusterIssuer that signs each
	// domain's certificate. When empty, no Certificate is created and the
	// TLS Secret must be provided some other way.
	ClusterIssuer string
}

// Enabled reports whether press manages routing.
func (r Routing) Enabled() bool { return r.Gateway.Name != "" }

func (r Routing) usesListenerSets() bool { return r.Mode != ModeGateway }

func (r Routing) ports() (https, http int32) {
	https, http = r.HTTPSPort, r.HTTPPort
	if https == 0 {
		https = 443
	}
	if http == 0 {
		http = 80
	}
	return https, http
}

// Names of the per-Gazette routing objects, all in the Gazette's namespace.
func routingName(g string) string         { return "manifesto-" + g }
func redirectName(g string) string        { return "manifesto-" + g + "-redirect" }
func tlsSecretName(g string) string       { return "manifesto-" + g + "-tls" }
func referenceGrantName(ns string) string { return "manifesto-" + ns }

const (
	listenerHTTPS = "https"
	listenerHTTP  = "http"

	// labelNamespace marks a ReferenceGrant with the namespace it admits.
	labelNamespace = v1.GroupName + "/namespace"
	labelManagedBy = "app.kubernetes.io/managed-by"
)

var certificateGVK = schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"}

// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=listenersets;httproutes;referencegrants,verbs=get;list;watch;create;patch;delete
// +kubebuilder:rbac:groups=cert-manager.io,resources=certificates,verbs=get;list;watch;create;patch;delete

// reconcileRouting applies or removes the routing objects of g, and reports
// them in the Routed condition. Only the Gazette that owns its domain is
// routed.
func (r *GazetteReconciler) reconcileRouting(ctx context.Context, g *v1.Gazette, ownsDomain bool) (metav1.Condition, error) {
	cond := metav1.Condition{Type: v1.ConditionRouted, ObservedGeneration: g.Generation}
	if !ownsDomain {
		if err := r.deleteRouting(ctx, g); err != nil {
			return cond, err
		}
		cond.Status, cond.Reason = metav1.ConditionFalse, v1.ReasonDomainConflict
		cond.Message = "Not routed: another Gazette serves this domain"
		return cond, nil
	}
	route := r.httpRoute(g)
	if !r.Routing.usesListenerSets() {
		// Remove what ModeListenerSet may have created before a mode change.
		if err := r.deleteObjects(ctx, r.listenerSetObjects(g)...); err != nil {
			return cond, err
		}
		if err := r.apply(ctx, g, route); err != nil {
			return cond, err
		}
		return routedCondition(cond, nil, route, "Gateway", r.Routing.Gateway.Name), nil
	}

	if r.Routing.ClusterIssuer != "" {
		if err := r.apply(ctx, g, r.certificate(g)); err != nil {
			return cond, err
		}
	}
	ls, redirect := r.listenerSet(g), r.redirectRoute(g)
	for _, obj := range []client.Object{ls, route, redirect} {
		if err := r.apply(ctx, g, obj); err != nil {
			return cond, err
		}
	}
	return routedCondition(cond, ls, route, "ListenerSet", ls.Name), nil
}

// routedCondition reads what the Gateway implementation reported on the
// ListenerSet (nil in ModeGateway) and on the HTTPRoute for the given parent
// (as returned by the apply).
func routedCondition(cond metav1.Condition, ls *gatewayv1.ListenerSet, route *gatewayv1.HTTPRoute, parentKind, parentName string) metav1.Condition {
	var problems []string
	if ls != nil {
		if c := meta.FindStatusCondition(ls.Status.Conditions, string(gatewayv1.ListenerSetConditionProgrammed)); c == nil || c.Status != metav1.ConditionTrue {
			problems = append(problems, "ListenerSet "+ls.Name+" is not programmed"+describe(c))
		}
	}
	routeProblem := "HTTPRoute " + route.Name + " is not accepted yet"
	for _, p := range route.Status.Parents {
		if p.ParentRef.Kind == nil || string(*p.ParentRef.Kind) != parentKind || string(p.ParentRef.Name) != parentName {
			continue
		}
		c := meta.FindStatusCondition(p.Conditions, string(gatewayv1.RouteConditionAccepted))
		if c != nil && c.Status == metav1.ConditionTrue {
			routeProblem = ""
		} else {
			routeProblem = "HTTPRoute " + route.Name + " is not accepted" + describe(c)
		}
	}
	if routeProblem != "" {
		problems = append(problems, routeProblem)
	}
	if len(problems) == 0 {
		cond.Status, cond.Reason, cond.Message = metav1.ConditionTrue, v1.ReasonProgrammed, "Gateway serves the domain"
		return cond
	}
	cond.Status, cond.Reason, cond.Message = metav1.ConditionFalse, v1.ReasonRoutePending, strings.Join(problems, "; ")
	return cond
}

func describe(c *metav1.Condition) string {
	if c == nil {
		return ""
	}
	return fmt.Sprintf(" (%s: %s)", c.Reason, c.Message)
}

func (r *GazetteReconciler) listenerSet(g *v1.Gazette) *gatewayv1.ListenerSet {
	host := gatewayv1.Hostname(g.Spec.Domain)
	httpsPort, httpPort := r.Routing.ports()
	same := &gatewayv1.AllowedRoutes{Namespaces: &gatewayv1.RouteNamespaces{From: new(gatewayv1.NamespacesFromSame)}}
	return &gatewayv1.ListenerSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: g.Namespace, Name: routingName(g.Name)},
		Spec: gatewayv1.ListenerSetSpec{
			ParentRef: gatewayv1.ParentGatewayReference{
				Name:      gatewayv1.ObjectName(r.Routing.Gateway.Name),
				Namespace: new(gatewayv1.Namespace(r.Routing.Gateway.Namespace)),
			},
			Listeners: []gatewayv1.ListenerEntry{
				{
					Name: listenerHTTPS, Hostname: &host, Port: httpsPort, Protocol: gatewayv1.HTTPSProtocolType,
					TLS: &gatewayv1.ListenerTLSConfig{
						Mode:            new(gatewayv1.TLSModeTerminate),
						CertificateRefs: []gatewayv1.SecretObjectReference{{Name: gatewayv1.ObjectName(tlsSecretName(g.Name))}},
					},
					AllowedRoutes: same,
				},
				{Name: listenerHTTP, Hostname: &host, Port: httpPort, Protocol: gatewayv1.HTTPProtocolType, AllowedRoutes: same},
			},
		},
	}
}

func listenerParent(g *v1.Gazette, section string) gatewayv1.ParentReference {
	return gatewayv1.ParentReference{
		Group:       new(gatewayv1.Group(gatewayv1.GroupName)),
		Kind:        new(gatewayv1.Kind("ListenerSet")),
		Name:        gatewayv1.ObjectName(routingName(g.Name)),
		SectionName: new(gatewayv1.SectionName(section)),
	}
}

// httpRoute sends the domain's traffic to herald: the HTTPS listener of the
// Gazette's ListenerSet, or the shared Gateway in ModeGateway.
func (r *GazetteReconciler) httpRoute(g *v1.Gazette) *gatewayv1.HTTPRoute {
	parent := listenerParent(g, listenerHTTPS)
	if !r.Routing.usesListenerSets() {
		parent = gatewayv1.ParentReference{
			Group:     new(gatewayv1.Group(gatewayv1.GroupName)),
			Kind:      new(gatewayv1.Kind("Gateway")),
			Name:      gatewayv1.ObjectName(r.Routing.Gateway.Name),
			Namespace: new(gatewayv1.Namespace(r.Routing.Gateway.Namespace)),
		}
		if r.Routing.GatewaySection != "" {
			parent.SectionName = new(gatewayv1.SectionName(r.Routing.GatewaySection))
		}
	}
	return &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Namespace: g.Namespace, Name: routingName(g.Name)},
		Spec: gatewayv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{ParentRefs: []gatewayv1.ParentReference{parent}},
			Hostnames:       []gatewayv1.Hostname{gatewayv1.Hostname(g.Spec.Domain)},
			Rules: []gatewayv1.HTTPRouteRule{{
				BackendRefs: []gatewayv1.HTTPBackendRef{{BackendRef: gatewayv1.BackendRef{
					BackendObjectReference: gatewayv1.BackendObjectReference{
						Name:      gatewayv1.ObjectName(r.Routing.Herald.Name),
						Namespace: new(gatewayv1.Namespace(r.Routing.Herald.Namespace)),
						Port:      new(r.Routing.HeraldPort),
					},
				}}},
			}},
		},
	}
}

// redirectRoute redirects the domain's plain HTTP traffic to HTTPS.
func (r *GazetteReconciler) redirectRoute(g *v1.Gazette) *gatewayv1.HTTPRoute {
	return &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Namespace: g.Namespace, Name: redirectName(g.Name)},
		Spec: gatewayv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{ParentRefs: []gatewayv1.ParentReference{listenerParent(g, listenerHTTP)}},
			Hostnames:       []gatewayv1.Hostname{gatewayv1.Hostname(g.Spec.Domain)},
			Rules: []gatewayv1.HTTPRouteRule{{
				Filters: []gatewayv1.HTTPRouteFilter{{
					Type: gatewayv1.HTTPRouteFilterRequestRedirect,
					RequestRedirect: &gatewayv1.HTTPRequestRedirectFilter{
						Scheme: new("https"), StatusCode: new(301),
					},
				}},
			}},
		},
	}
}

// certificate asks cert-manager for the domain's TLS certificate.
func (r *GazetteReconciler) certificate(g *v1.Gazette) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{
			"secretName": tlsSecretName(g.Name),
			"dnsNames":   []any{g.Spec.Domain},
			"issuerRef":  map[string]any{"group": "cert-manager.io", "kind": "ClusterIssuer", "name": r.Routing.ClusterIssuer},
		},
	}}
	u.SetGroupVersionKind(certificateGVK)
	u.SetNamespace(g.Namespace)
	u.SetName(routingName(g.Name))
	return u
}

// apply writes obj with Server-Side Apply as press, owned by g so that
// deleting the Gazette garbage-collects it. obj is updated with the result.
func (r *GazetteReconciler) apply(ctx context.Context, g *v1.Gazette, obj client.Object) error {
	if g != nil {
		obj.SetOwnerReferences([]metav1.OwnerReference{{
			APIVersion: v1.GroupVersion.String(), Kind: "Gazette", Name: g.Name, UID: g.UID,
			Controller: new(true), BlockOwnerDeletion: new(true),
		}})
	}
	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[labelManagedBy] = v1.FieldManagerPress
	obj.SetLabels(labels)
	return applyObject(ctx, r.Client, r.Scheme, obj)
}

// applyObject applies the spec and metadata of obj with Server-Side Apply as
// the press field manager, and reads the result back into obj.
func applyObject(ctx context.Context, c client.Client, scheme *runtime.Scheme, obj client.Object) error {
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		gvk, err := apiutil.GVKForObject(obj, scheme)
		if err != nil {
			return err
		}
		m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
		if err != nil {
			return err
		}
		delete(m, "status")
		unstructured.RemoveNestedField(m, "metadata", "creationTimestamp")
		u = &unstructured.Unstructured{Object: m}
		u.SetGroupVersionKind(gvk)
	}
	if err := c.Apply(ctx, client.ApplyConfigurationFromUnstructured(u),
		client.FieldOwner(v1.FieldManagerPress), client.ForceOwnership); err != nil {
		return err
	}
	if _, ok := obj.(*unstructured.Unstructured); ok {
		return nil
	}
	return c.Get(ctx, client.ObjectKeyFromObject(obj), obj)
}

// listenerSetObjects are the objects only ModeListenerSet creates for g.
func (r *GazetteReconciler) listenerSetObjects(g *v1.Gazette) []client.Object {
	cert := &unstructured.Unstructured{}
	cert.SetGroupVersionKind(certificateGVK)
	cert.SetNamespace(g.Namespace)
	cert.SetName(routingName(g.Name))
	return []client.Object{
		&gatewayv1.HTTPRoute{ObjectMeta: metav1.ObjectMeta{Namespace: g.Namespace, Name: redirectName(g.Name)}},
		&gatewayv1.ListenerSet{ObjectMeta: metav1.ObjectMeta{Namespace: g.Namespace, Name: routingName(g.Name)}},
		cert,
	}
}

// deleteRouting removes the routing objects of g, if any.
func (r *GazetteReconciler) deleteRouting(ctx context.Context, g *v1.Gazette) error {
	route := &gatewayv1.HTTPRoute{ObjectMeta: metav1.ObjectMeta{Namespace: g.Namespace, Name: routingName(g.Name)}}
	return r.deleteObjects(ctx, append(r.listenerSetObjects(g), route)...)
}

// deleteObjects deletes objs that exist and press manages. A missing kind
// (cert-manager or ListenerSet CRDs not installed) is treated as absent.
func (r *GazetteReconciler) deleteObjects(ctx context.Context, objs ...client.Object) error {
	for _, o := range objs {
		err := r.Delete(ctx, o)
		if err != nil && !apierrors.IsNotFound(err) && !meta.IsNoMatchError(err) {
			return err
		}
	}
	return nil
}

// reconcileReferenceGrant lets the HTTPRoutes of namespace reach herald's
// Service while the namespace has Gazettes, and removes the grant after.
func (r *GazetteReconciler) reconcileReferenceGrant(ctx context.Context, namespace string) error {
	var gazettes v1.GazetteList
	if err := r.List(ctx, &gazettes, client.InNamespace(namespace)); err != nil {
		return err
	}
	needed := false
	for _, g := range gazettes.Items {
		if g.DeletionTimestamp == nil {
			needed = true
			break
		}
	}
	grant := &gatewayv1.ReferenceGrant{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: r.Routing.Herald.Namespace,
			Name:      referenceGrantName(namespace),
			Labels:    map[string]string{labelNamespace: namespace},
		},
	}
	if !needed {
		if err := r.Delete(ctx, grant); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		return nil
	}
	grant.Spec = gatewayv1.ReferenceGrantSpec{
		From: []gatewayv1.ReferenceGrantFrom{{
			Group: gatewayv1.GroupName, Kind: "HTTPRoute", Namespace: gatewayv1.Namespace(namespace),
		}},
		To: []gatewayv1.ReferenceGrantTo{{
			Group: "", Kind: "Service", Name: new(gatewayv1.ObjectName(r.Routing.Herald.Name)),
		}},
	}
	return r.apply(ctx, nil, grant)
}
