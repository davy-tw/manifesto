/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package desk

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
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
// desk forwards IDToken to the API server, which authenticates it.
type Session struct {
	IDToken      string    `json:"id"`
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

// Auth signs users in with OpenID Connect. The API server must trust the
// same issuer with desk's client ID as audience (Structured Authentication
// Configuration), since the ID token is what desk sends it.
type Auth struct {
	oauth    oauth2.Config
	verifier *oidc.IDTokenVerifier
	sealer   *Sealer
	secure   bool
	// SessionMaxAge bounds how long a session cookie lives (default 12h).
	SessionMaxAge time.Duration
}

// AuthConfig configures Auth.
type AuthConfig struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	// RedirectURL is desk's public URL followed by /auth/callback.
	RedirectURL string
	Scopes      []string
	// Secure marks cookies Secure; true unless desk is served over plain HTTP.
	Secure bool
}

// NewAuth discovers the issuer and returns an Auth.
func NewAuth(ctx context.Context, cfg AuthConfig, sealer *Sealer) (*Auth, error) {
	provider, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("discovering OIDC issuer %s: %w", cfg.Issuer, err)
	}
	scopes := cfg.Scopes
	if len(scopes) == 0 {
		scopes = []string{oidc.ScopeOpenID, "email", "profile"}
	}
	return &Auth{
		oauth: oauth2.Config{
			ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, RedirectURL: cfg.RedirectURL,
			Endpoint: provider.Endpoint(), Scopes: scopes,
		},
		verifier: provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
		sealer:   sealer,
		secure:   cfg.Secure,
	}, nil
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
	tok, err := a.oauth.Exchange(r.Context(), r.URL.Query().Get("code"), oauth2.VerifierOption(st.Verifier))
	if err != nil {
		http.Error(w, "sign-in failed", http.StatusUnauthorized)
		return
	}
	sess, err := a.session(r.Context(), tok, st.Nonce)
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
	return &Session{IDToken: raw, RefreshToken: tok.RefreshToken, Expiry: id.Expiry, Name: name}, nil
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
	if s.RefreshToken == "" {
		return nil, errNoSession
	}
	tok, err := a.oauth.TokenSource(r.Context(), &oauth2.Token{RefreshToken: s.RefreshToken}).Token()
	if err != nil {
		return nil, errNoSession
	}
	fresh, err := a.session(r.Context(), tok, "")
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
