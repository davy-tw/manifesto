/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package desk

import (
	"context"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strings"

	authenticationv1 "k8s.io/api/authentication/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
)

// Kube talks to the API server as the signed-in user: every request carries
// the user's own token, so Kubernetes authenticates, authorizes and audits
// the user, never desk. desk uses no credentials of its own.
type Kube struct {
	host      *url.URL
	transport http.RoundTripper
	scheme    *runtime.Scheme
	mapper    meta.RESTMapper
}

// NewKube prepares access to the API server of cfg, dropping any
// credentials cfg carries (such as desk's service account token).
func NewKube(cfg *rest.Config, scheme *runtime.Scheme) (*Kube, error) {
	anon := rest.AnonymousClientConfig(cfg)
	transport, err := rest.TransportFor(anon)
	if err != nil {
		return nil, err
	}
	host, err := url.Parse(anon.Host)
	if err != nil {
		return nil, err
	}
	return &Kube{host: host, transport: transport, scheme: scheme, mapper: staticMapper()}, nil
}

// staticMapper maps the kinds desk reads itself, so building a client per
// request needs no discovery.
func staticMapper() meta.RESTMapper {
	m := meta.NewDefaultRESTMapper(nil)
	for kind, scope := range map[string]meta.RESTScope{
		"Gazette": meta.RESTScopeNamespace, "Dispatch": meta.RESTScopeNamespace, "Section": meta.RESTScopeNamespace,
		"Stamp": meta.RESTScopeNamespace, "Scribe": meta.RESTScopeNamespace, "Typeset": meta.RESTScopeNamespace,
		"Print": meta.RESTScopeNamespace, "Archive": meta.RESTScopeNamespace,
		"ClusterTypeset": meta.RESTScopeRoot, "ClusterArchive": meta.RESTScopeRoot,
	} {
		m.Add(v1.GroupVersion.WithKind(kind), scope)
	}
	m.Add(authenticationv1.SchemeGroupVersion.WithKind("SelfSubjectReview"), meta.RESTScopeRoot)
	return m
}

type bearer struct {
	next  http.RoundTripper
	token string
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.next.RoundTrip(r)
}

// Client returns a client that acts as the user with token.
func (k *Kube) Client(token string) (client.Client, error) {
	return client.New(&rest.Config{Host: k.host.String()}, client.Options{
		Scheme: k.scheme, Mapper: k.mapper,
		HTTPClient: &http.Client{Transport: bearer{k.transport, token}},
	})
}

// proxyPaths are the API paths desk forwards: Manifesto's resources, the
// events about them, a namespace's RoleBindings (publishers manage members)
// and the self-review APIs the UI uses to show what the user may do. It is
// not access control (RBAC decides), only a bound on what desk relays.
var proxyPaths = []*regexp.Regexp{
	regexp.MustCompile(`^/apis/` + regexp.QuoteMeta(v1.GroupName) + `/v1alpha1(/namespaces/[a-z0-9-]+)?/[a-z]+(/[a-z0-9.-]+(/status)?)?$`),
	regexp.MustCompile(`^/api/v1/namespaces/[a-z0-9-]+/events$`),
	regexp.MustCompile(`^/apis/rbac\.authorization\.k8s\.io/v1/namespaces/[a-z0-9-]+/rolebindings(/[a-z0-9.:-]+)?$`),
	regexp.MustCompile(`^/apis/authorization\.k8s\.io/v1/selfsubject(access|rules)reviews$`),
	regexp.MustCompile(`^/apis/authentication\.k8s\.io/v1/selfsubjectreviews$`),
}

// proxyPrefix is where desk mounts the API server.
const proxyPrefix = "/k8s"

func allowedPath(p string) bool {
	for _, re := range proxyPaths {
		if re.MatchString(p) {
			return true
		}
	}
	return false
}

// Proxy forwards /k8s/<API path> to the API server with the user's token.
// Watches stream through.
func (k *Kube) Proxy(token func(*http.Request) string) http.Handler {
	rp := &httputil.ReverseProxy{
		Transport:     k.transport,
		FlushInterval: -1,
		Rewrite: func(pr *httputil.ProxyRequest) {
			path := strings.TrimPrefix(pr.In.URL.Path, proxyPrefix)
			pr.Out.URL.Scheme, pr.Out.URL.Host = k.host.Scheme, k.host.Host
			pr.Out.URL.Path = strings.TrimSuffix(k.host.Path, "/") + path
			pr.Out.URL.RawPath = ""
			pr.Out.Host = k.host.Host
			pr.Out.Header.Del("Cookie")
			pr.Out.Header.Set("Authorization", "Bearer "+token(pr.In))
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowedPath(strings.TrimPrefix(r.URL.Path, proxyPrefix)) {
			http.Error(w, "not available through desk", http.StatusNotFound)
			return
		}
		rp.ServeHTTP(w, r)
	})
}

func contextWithToken(r *http.Request, token string) context.Context {
	return context.WithValue(r.Context(), tokenKey{}, token)
}
