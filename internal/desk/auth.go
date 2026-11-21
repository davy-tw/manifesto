/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package desk

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// Cookie names.
const (
	sessionCookie = "manifesto_session"
	loginCookie   = "manifesto_login"
)

// Session is what desk keeps about a signed-in user, sealed in a cookie.
// desk forwards Token to the API server, which authenticates it.
type Session struct {
	// Token is an OIDC ID token, or a token pasted at sign-in.
	Token        string    `json:"id"`
	RefreshToken string    `json:"rt,omitempty"`
	Expiry       time.Time `json:"exp"`
	// Name is shown in the UI only; Kubernetes decides who the user is.
	Name string `json:"n,omitempty"`
}

type loginState struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
}

// Auth signs users in and keeps their session in an encrypted cookie. It
// offers OpenID Connect, signing in with a pasted token, or both. With OIDC
// the API server must trust the same issuer with desk's client ID as
// audience (Structured Authentication Configuration), since the ID token is
// what desk sends it.
type Auth struct {
	oauth    oauth2.Config
	verifier *oidc.IDTokenVerifier // nil without OIDC
	sealer   *Sealer
	secure   bool
	client   *http.Client
	tokens   bool
	// SessionMaxAge bounds how long a session cookie lives (default 12h).
	SessionMaxAge time.Duration
}

// AuthConfig configures Auth.
type AuthConfig struct {
	// Issuer is the OIDC issuer; empty turns OIDC sign-in off.
	Issuer       string
	ClientID     string
	ClientSecret string
	// RedirectURL is desk's public URL followed by /auth/callback.
	RedirectURL string
	Scopes      []string
	// TokenSignIn lets users sign in by pasting a token the API server
	// accepts, such as one from "kubectl create token".
	TokenSignIn bool
	// Secure marks cookies Secure; true unless desk is served over plain HTTP.
	Secure bool
	// HTTPClient talks to the provider; nil uses the default client.
	HTTPClient *http.Client
}

// NewAuth discovers the issuer, if any, and returns an Auth.
func NewAuth(ctx context.Context, cfg AuthConfig, sealer *Sealer) (*Auth, error) {
	if cfg.Issuer == "" && !cfg.TokenSignIn {
		return nil, errors.New("no sign-in method: set an OIDC issuer or allow token sign-in")
	}
	a := &Auth{sealer: sealer, secure: cfg.Secure, client: cfg.HTTPClient, tokens: cfg.TokenSignIn}
	if cfg.Issuer == "" {
		return a, nil
	}
	if cfg.HTTPClient != nil {
		ctx = oidc.ClientContext(ctx, cfg.HTTPClient)
	}
	provider, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("discovering OIDC issuer %s: %w", cfg.Issuer, err)
	}
	scopes := cfg.Scopes
	if len(scopes) == 0 {
		scopes = []string{oidc.ScopeOpenID, "email", "profile"}
	}
	a.oauth = oauth2.Config{
		ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, RedirectURL: cfg.RedirectURL,
		Endpoint: provider.Endpoint(), Scopes: scopes,
	}
	a.verifier = provider.Verifier(&oidc.Config{ClientID: cfg.ClientID})
	return a, nil
}

// Methods are the sign-in methods desk offers, for the sign-in page.
type Methods struct {
	OIDC  bool `json:"oidc"`
	Token bool `json:"token"`
}

// Methods returns the sign-in methods a offers; a may be nil.
func (a *Auth) Methods() Methods {
	if a == nil {
		return Methods{}
	}
	return Methods{OIDC: a.verifier != nil, Token: a.tokens}
}

// SignInWithToken starts a session for token, which the caller has checked
// the API server accepts. The session ends when the token expires, if it
// says when, and after SessionMaxAge at the latest; it cannot be refreshed.
func (a *Auth) SignInWithToken(w http.ResponseWriter, r *http.Request, token, name string) error {
	expiry := time.Now().Add(a.maxAge())
	if exp, ok := tokenExpiry(token); ok && exp.Before(expiry) {
		expiry = exp
	}
	return a.sealer.SetCookie(w, r, sessionCookie, &Session{Token: token, Expiry: expiry, Name: name}, a.sessionOptions())
}

// tokenExpiry reads the exp claim of a JWT without verifying it: the API
// server verifies the token on every request, this only ends the session
// when the token can no longer work. Tokens that are not JWTs have none.
func tokenExpiry(token string) (time.Time, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, false
	}
	var claims struct {
		Exp json.Number `json:"exp"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return time.Time{}, false
	}
	exp, err := claims.Exp.Int64()
	if err != nil || exp <= 0 {
		return time.Time{}, false
	}
	return time.Unix(exp, 0), true
}

// ctx carries the provider HTTP client, for the token exchange and refresh.
func (a *Auth) ctx(r *http.Request) context.Context {
	if a.client == nil {
		return r.Context()
	}
	return oidc.ClientContext(r.Context(), a.client)
}

func (a *Auth) maxAge() time.Duration {
	if a.SessionMaxAge > 0 {
		return a.SessionMaxAge
	}
	return 12 * time.Hour
}

// sessionOptions: Strict, so the session never rides on a cross-site request.
func (a *Auth) sessionOptions() cookieOptions {
	return cookieOptions{Secure: a.secure, SameSite: http.SameSiteStrictMode, MaxAge: a.maxAge()}
}

// loginOptions: Lax, so it comes back with the provider's redirect.
func (a *Auth) loginOptions() cookieOptions {
	return cookieOptions{Secure: a.secure, SameSite: http.SameSiteLaxMode, MaxAge: 10 * time.Minute}
}

func random() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// Login starts the authorization code flow with PKCE.
func (a *Auth) Login(w http.ResponseWriter, r *http.Request) {
	st := loginState{State: random(), Nonce: random(), Verifier: oauth2.GenerateVerifier()}
	if err := a.sealer.SetCookie(w, r, loginCookie, st, a.loginOptions()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, a.oauth.AuthCodeURL(st.State, oidc.Nonce(st.Nonce), oauth2.S256ChallengeOption(st.Verifier)), http.StatusFound)
}

// Callback finishes the flow and starts the session.
func (a *Auth) Callback(w http.ResponseWriter, r *http.Request) {
	var st loginState
	if err := a.sealer.ReadCookie(r, loginCookie, &st); err != nil {
		http.Error(w, "the sign-in expired; try again", http.StatusBadRequest)
		return
	}
	a.sealer.ClearCookie(w, r, loginCookie, a.loginOptions())
	if e := r.URL.Query().Get("error"); e != "" {
		http.Error(w, "sign-in failed: "+e, http.StatusUnauthorized)
		return
	}
	if r.URL.Query().Get("state") != st.State {
		http.Error(w, "sign-in state mismatch", http.StatusBadRequest)
		return
	}
	tok, err := a.oauth.Exchange(a.ctx(r), r.URL.Query().Get("code"), oauth2.VerifierOption(st.Verifier))
	if err != nil {
		http.Error(w, "sign-in failed", http.StatusUnauthorized)
		return
	}
	sess, err := a.session(a.ctx(r), tok, st.Nonce)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	if err := a.sealer.SetCookie(w, r, sessionCookie, sess, a.sessionOptions()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

// session verifies the ID token of tok; nonce is checked when not empty.
func (a *Auth) session(ctx context.Context, tok *oauth2.Token, nonce string) (*Session, error) {
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		return nil, errors.New("the provider returned no ID token")
	}
	id, err := a.verifier.Verify(ctx, raw)
	if err != nil {
		return nil, fmt.Errorf("invalid ID token: %w", err)
	}
	if nonce != "" && id.Nonce != nonce {
		return nil, errors.New("ID token nonce mismatch")
	}
	var claims struct {
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	_ = id.Claims(&claims)
	name := claims.Name
	if name == "" {
		name = claims.Email
	}
	return &Session{Token: raw, RefreshToken: tok.RefreshToken, Expiry: id.Expiry, Name: name}, nil
}

// Logout ends the session. It is a POST so a link elsewhere cannot sign
// users out.
func (a *Auth) Logout(w http.ResponseWriter, r *http.Request) {
	a.sealer.ClearCookie(w, r, sessionCookie, a.sessionOptions())
	w.WriteHeader(http.StatusNoContent)
}

// errNoSession means the request carries no usable session.
var errNoSession = errors.New("not signed in")

// Session returns the request's session, refreshing an expired ID token
// with the refresh token when there is one.
func (a *Auth) Session(w http.ResponseWriter, r *http.Request) (*Session, error) {
	var s Session
	if err := a.sealer.ReadCookie(r, sessionCookie, &s); err != nil {
		return nil, errNoSession
	}
	if time.Until(s.Expiry) > 30*time.Second {
		return &s, nil
	}
	if s.RefreshToken == "" || a.verifier == nil {
		return nil, errNoSession
	}
	tok, err := a.oauth.TokenSource(a.ctx(r), &oauth2.Token{RefreshToken: s.RefreshToken}).Token()
	if err != nil {
		return nil, errNoSession
	}
	fresh, err := a.session(a.ctx(r), tok, "")
	if err != nil {
		return nil, errNoSession
	}
	if fresh.RefreshToken == "" {
		fresh.RefreshToken = s.RefreshToken
	}
	if err := a.sealer.SetCookie(w, r, sessionCookie, fresh, a.sessionOptions()); err != nil {
		return nil, err
	}
	return fresh, nil
}
