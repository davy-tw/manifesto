/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

// Package desk is the authors' web UI: a single-page app and the backend it
// talks to. The backend signs users in with OIDC and forwards their requests
// to the API server with their own token; it makes no access decisions.
package desk

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-logr/logr"
	authenticationv1 "k8s.io/api/authentication/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
	"go.pkgs.davy.tw/manifesto/internal/render"
	"go.pkgs.davy.tw/manifesto/internal/sitedata"
)

// CSRFHeader must accompany every state-changing request made with the
// session cookie. Browsers do not let other sites set custom headers on
// cross-origin requests without a CORS preflight, which desk never grants.
const CSRFHeader = "X-Manifesto-Desk"

// Server is desk's HTTP handler.
type Server struct {
	// Auth signs users in; nil allows only bearer tokens (no browser sign-in).
	Auth *Auth
	Kube *Kube
	// PublicURL is desk's own URL; cross-origin state-changing requests are refused.
	PublicURL *url.URL
	// UI is the single-page app.
	UI  fs.FS
	Log logr.Logger
}

// Handler returns the routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	methods := s.Auth.Methods()
	mux.HandleFunc("GET /auth/methods", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, methods) })
	if methods.OIDC {
		mux.HandleFunc("GET /auth/login", s.Auth.Login)
		mux.HandleFunc("GET /auth/callback", s.Auth.Callback)
	}
	if methods.Token {
		mux.Handle("POST /auth/token", s.guard(http.HandlerFunc(s.tokenSignIn)))
	}
	if s.Auth != nil {
		mux.Handle("POST /auth/logout", s.guard(http.HandlerFunc(s.Auth.Logout)))
	}
	mux.Handle("GET /api/session", s.withToken(s.session))
	mux.Handle("POST /api/preview", s.withToken(s.preview))
	mux.Handle(proxyPrefix+"/", s.withToken(func(w http.ResponseWriter, r *http.Request, _ string) {
		s.Kube.Proxy(tokenOf).ServeHTTP(w, r)
	}))
	if s.UI != nil {
		// Not "GET /": that would conflict with the method-less /k8s/.
		mux.Handle("/", s.ui())
	}
	return securityHeaders(mux)
}

type tokenKey struct{}

func tokenOf(r *http.Request) string {
	t, _ := r.Context().Value(tokenKey{}).(string)
	return t
}

// withToken resolves the user's token, from an Authorization: Bearer header
// (API clients) or the session cookie (the browser), and rejects
// cross-site state changes on cookie sessions.
func (s *Server) withToken(h func(http.ResponseWriter, *http.Request, string)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var token string
		if t, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && t != "" {
			token = t
		} else {
			if !s.sameOrigin(r) {
				http.Error(w, "cross-site request refused", http.StatusForbidden)
				return
			}
			if s.Auth != nil {
				if sess, err := s.Auth.Session(w, r); err == nil {
					token = sess.Token
				}
			}
		}
		if token == "" {
			http.Error(w, "not signed in", http.StatusUnauthorized)
			return
		}
		r = r.WithContext(contextWithToken(r, token))
		h(w, r, token)
	})
}

// guard applies the cross-site checks to a handler that does not need a token.
func (s *Server) guard(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.sameOrigin(r) {
			http.Error(w, "cross-site request refused", http.StatusForbidden)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// sameOrigin accepts safe methods, and state-changing ones that carry the
// CSRF header and, when the browser sends one, desk's own Origin.
func (s *Server) sameOrigin(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	if r.Header.Get(CSRFHeader) == "" {
		return false
	}
	if o := r.Header.Get("Origin"); o != "" && s.PublicURL != nil {
		return o == s.PublicURL.Scheme+"://"+s.PublicURL.Host
	}
	return true
}

// SessionInfo is what /api/session returns.
type SessionInfo struct {
	// Name is from the ID token, for display.
	Name string `json:"name,omitempty"`
	// Username and Groups are who Kubernetes says the user is.
	Username string   `json:"username"`
	Groups   []string `json:"groups,omitempty"`
}

func (s *Server) session(w http.ResponseWriter, r *http.Request, token string) {
	c, err := s.Kube.Client(token)
	if err != nil {
		s.fail(w, err)
		return
	}
	review := &authenticationv1.SelfSubjectReview{}
	if err := c.Create(r.Context(), review); err != nil {
		s.fail(w, err)
		return
	}
	info := SessionInfo{Username: review.Status.UserInfo.Username, Groups: review.Status.UserInfo.Groups}
	if s.Auth != nil {
		if sess, err := s.Auth.Session(w, r); err == nil {
			info.Name = sess.Name
		}
	}
	writeJSON(w, info)
}

// TokenSignIn is the body of POST /auth/token.
type TokenSignIn struct {
	Token string `json:"token"`
}

// tokenSignIn starts a session with a token the user pasted, once the API
// server has said whose it is. Like every state change made from the
// browser it needs the CSRF header, so no other site can sign a visitor in
// as someone else.
func (s *Server) tokenSignIn(w http.ResponseWriter, r *http.Request) {
	var req TokenSignIn
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		http.Error(w, "invalid sign-in request", http.StatusBadRequest)
		return
	}
	token := strings.TrimSpace(req.Token)
	if token == "" {
		http.Error(w, "token is required", http.StatusBadRequest)
		return
	}
	c, err := s.Kube.Client(token)
	if err != nil {
		s.fail(w, err)
		return
	}
	review := &authenticationv1.SelfSubjectReview{}
	if err := c.Create(r.Context(), review); err != nil {
		if apierrors.IsUnauthorized(err) {
			http.Error(w, "the API server did not accept this token", http.StatusUnauthorized)
			return
		}
		s.fail(w, err)
		return
	}
	user := review.Status.UserInfo
	if err := s.Auth.SignInWithToken(w, r, token, user.Username); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, SessionInfo{Name: user.Username, Username: user.Username, Groups: user.Groups})
}

// PreviewRequest is the body of /api/preview: the Dispatch as edited.
type PreviewRequest struct {
	Dispatch v1.Dispatch `json:"dispatch"`
}

// maxPreviewBytes bounds a preview request: a Dispatch body plus metadata.
const maxPreviewBytes = v1.MaxBodyBytes + 64<<10

// preview renders a Dispatch with its Gazette's theme exactly as herald
// would, reading everything with the user's own permissions.
func (s *Server) preview(w http.ResponseWriter, r *http.Request, token string) {
	var req PreviewRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxPreviewBytes)).Decode(&req); err != nil {
		http.Error(w, "invalid preview request", http.StatusBadRequest)
		return
	}
	d := &req.Dispatch
	if d.Namespace == "" {
		http.Error(w, "metadata.namespace is required", http.StatusBadRequest)
		return
	}
	c, err := s.Kube.Client(token)
	if err != nil {
		s.fail(w, err)
		return
	}
	var gazette *v1.Gazette
	if d.Spec.GazetteRef != "" {
		var g v1.Gazette
		switch err := c.Get(r.Context(), types.NamespacedName{Namespace: d.Namespace, Name: d.Spec.GazetteRef}, &g); {
		case err == nil:
			gazette = &g
		case !apierrors.IsNotFound(err):
			s.fail(w, err)
			return
		}
	}
	theme, err := sitedata.ResolveTheme(r.Context(), c, gazette)
	if err != nil {
		s.fail(w, err)
		return
	}
	if theme.Theme == nil {
		http.Error(w, theme.Err.Error(), http.StatusUnprocessableEntity)
		return
	}
	tax, err := sitedata.Taxonomy(r.Context(), c, d, nil)
	if err != nil {
		s.fail(w, err)
		return
	}
	page, err := render.NewSite(theme.Theme, gazette, theme.Params, tax).DispatchPage(d)
	if err != nil {
		http.Error(w, render.TruncateMessage(err.Error()), http.StatusUnprocessableEntity)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(page)
}

// fail maps API errors to responses: the API server's verdict is final.
func (s *Server) fail(w http.ResponseWriter, err error) {
	var status apierrors.APIStatus
	if errors.As(err, &status) {
		code := int(status.Status().Code)
		if code == 0 {
			code = http.StatusBadGateway
		}
		http.Error(w, status.Status().Message, code)
		return
	}
	s.Log.Error(err, "Request failed")
	http.Error(w, "the API server could not be reached", http.StatusBadGateway)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// ui serves the single-page app; unknown paths get index.html so client-side
// routes work on reload.
func (s *Server) ui() http.Handler {
	files := http.FileServerFS(s.UI)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p != "" {
			if _, err := fs.Stat(s.UI, p); err == nil {
				files.ServeHTTP(w, r)
				return
			}
		}
		http.ServeFileFS(w, r, s.UI, "index.html")
	})
}

// securityHeaders: the app loads only its own code; media may come from
// anywhere (Prints live on darkroom or S3); previews are sandboxed frames.
func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Content-Security-Policy", "default-src 'self'; img-src * data: blob:; style-src 'self' 'unsafe-inline'; "+
			"frame-src 'self' blob:; connect-src 'self' *; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "same-origin")
		h.ServeHTTP(w, r)
	})
}
