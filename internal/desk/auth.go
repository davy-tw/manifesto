/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package desk

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-logr/logr"
	"golang.org/x/oauth2"
	"golang.org/x/sync/singleflight"
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

	// Refreshing: one refresh grant per refresh token, see Refresh.
	refreshes singleflight.Group
	mu        sync.Mutex
	refreshed map[[sha256.Size]byte]refreshResult
	// retriedFrom maps a refresh token kept by a retry result to the
	// refresh token it replaced, so the outcome of the retry also answers
	// requests still carrying the older one.
	retriedFrom map[[sha256.Size]byte][sha256.Size]byte
	log         logr.Logger
}

// refreshResult is the session a refresh token was exchanged for, kept for
// requests that still carry the old cookie.
type refreshResult struct {
	session *Session
	until   time.Time
	// retry: the ID token could not be verified this time, and session is
	// the old one carrying the refresh token to try again with.
	retry bool
}

// Timing of sessions.
const (
	// refreshBefore is how long before the ID token expires Refresh renews it.
	refreshBefore = 30 * time.Second
	// expiryMargin: a token this close to expiry is not forwarded any more,
	// so it does not run out on its way to the API server.
	expiryMargin = 5 * time.Second
	// refreshGrace is how long the outcome of a refresh is reused for
	// requests that still carry the refresh token it consumed.
	refreshGrace = 2 * time.Minute
)

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
	// Log records failed refreshes; the zero value discards.
	Log logr.Logger
}

// NewAuth discovers the issuer, if any, and returns an Auth.
func NewAuth(ctx context.Context, cfg AuthConfig, sealer *Sealer) (*Auth, error) {
	if cfg.Issuer == "" && !cfg.TokenSignIn {
		return nil, errors.New("no sign-in method: set an OIDC issuer or allow token sign-in")
	}
	a := &Auth{sealer: sealer, secure: cfg.Secure, client: cfg.HTTPClient, tokens: cfg.TokenSignIn, log: cfg.Log}
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

// errSessionExpired means the session's ID token expired but it has a
// refresh token: the browser should POST /auth/refresh and try again.
var errSessionExpired = errors.New("session expired; refresh it")

// Session returns the request's session. It never refreshes: an ID token
// is used until shortly before it expires, and then errSessionExpired asks
// the browser to call Refresh. Refreshing on every request that finds the
// token expired would send the same refresh token from several requests at
// once, possibly to different desk replicas, and providers that rotate
// refresh tokens revoke the whole session when one is used twice.
func (a *Auth) Session(r *http.Request) (*Session, error) {
	var s Session
	if err := a.sealer.ReadCookie(r, sessionCookie, &s); err != nil {
		return nil, errNoSession
	}
	if time.Until(s.Expiry) > expiryMargin {
		return &s, nil
	}
	if s.RefreshToken == "" || a.verifier == nil {
		return nil, errNoSession
	}
	return nil, errSessionExpired
}

// Refresh renews the ID token with the refresh token once it is about to
// expire, and stores the new refresh token the provider returns: a
// provider that rotates refresh tokens accepts each one only once. The
// browser calls it one request at a time. Requests that still carry a
// refresh token being or just exchanged here share that exchange's result
// instead of sending the token again.
func (a *Auth) Refresh(w http.ResponseWriter, r *http.Request) {
	var s Session
	if err := a.sealer.ReadCookie(r, sessionCookie, &s); err != nil {
		http.Error(w, errNoSession.Error(), http.StatusUnauthorized)
		return
	}
	if time.Until(s.Expiry) > refreshBefore {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if s.RefreshToken == "" || a.verifier == nil {
		a.sealer.ClearCookie(w, r, sessionCookie, a.sessionOptions())
		http.Error(w, errNoSession.Error(), http.StatusUnauthorized)
		return
	}
	fresh, retry, err := a.refresh(r, &s)
	if err == nil && retry {
		// Keep the refresh token the provider just issued (the old one is
		// used up) for the next attempt.
		if err := a.sealer.SetCookie(w, r, sessionCookie, fresh, a.sessionOptions()); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.Error(w, "verifying the renewed session failed; try again", http.StatusServiceUnavailable)
		return
	}
	if err != nil {
		if !sessionIsOver(err) {
			// The provider failed or could not be reached; the refresh
			// token may still be good, so keep the session for a retry.
			http.Error(w, "renewing the session failed; try again", http.StatusServiceUnavailable)
			return
		}
		a.sealer.ClearCookie(w, r, sessionCookie, a.sessionOptions())
		http.Error(w, errNoSession.Error()+": "+err.Error(), http.StatusUnauthorized)
		return
	}
	if err := a.sealer.SetCookie(w, r, sessionCookie, fresh, a.sessionOptions()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// errNoIDToken: the provider answered a refresh without an ID token, which
// OIDC allows but desk cannot use, since the ID token is what it forwards.
var errNoIDToken = errors.New("the provider returned no ID token on refresh; " +
	"desk needs one (check the provider's client settings)")

// keysUnavailable reports whether verifying an ID token failed only
// because the provider's signing keys could not be fetched, e.g. right after
// it rotated them. go-oidc does not wrap that error, so this matches its
// message; when it no longer matches, failures end the session as any other
// invalid ID token does.
func keysUnavailable(err error) bool {
	return strings.Contains(err.Error(), "fetching keys")
}

// errInvalidIDToken: a refresh returned an ID token desk will not accept.
var errInvalidIDToken = errors.New("invalid ID token on refresh")

// sessionIsOver reports whether a failed refresh ended the session: the
// provider refused the refresh token, or answered with something desk
// cannot use. Anything else (5xx, timeouts, network errors) may pass.
func sessionIsOver(err error) bool {
	if re, ok := errors.AsType[*oauth2.RetrieveError](err); ok {
		if re.ErrorCode == "invalid_grant" {
			return true
		}
		code := 0
		if re.Response != nil {
			code = re.Response.StatusCode
		}
		return code >= 400 && code < 500 && code != http.StatusTooManyRequests
	}
	return errors.Is(err, errNoIDToken) || errors.Is(err, errInvalidIDToken)
}

// refresh exchanges the refresh token of old at most once per
// refreshGrace in this process. When the new ID token cannot be verified
// for now (fetching the provider's new signing keys failed), retry is
// true and the session returned is old with the refresh token to try again
// with: a provider that rotates refresh tokens has already used up old's.
func (a *Auth) refresh(r *http.Request, old *Session) (s *Session, retry bool, err error) {
	refreshToken := old.RefreshToken
	key := sha256.Sum256([]byte(refreshToken))
	if res, ok := a.recentRefresh(key); ok {
		return res.session, res.retry, nil
	}
	v, err, _ := a.refreshes.Do(string(key[:]), func() (any, error) {
		if res, ok := a.recentRefresh(key); ok {
			return res, nil
		}
		// Not the request's context: a browser that gives up must not
		// cancel an exchange the provider may already have completed.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(a.ctx(r)), 30*time.Second)
		defer cancel()
		tok, err := a.oauth.TokenSource(ctx, &oauth2.Token{RefreshToken: refreshToken}).Token()
		if err != nil {
			return nil, err
		}
		if raw, _ := tok.Extra("id_token").(string); raw == "" {
			return nil, errNoIDToken
		}
		res := refreshResult{}
		fresh, err := a.session(ctx, tok, "")
		switch {
		case err != nil && !keysUnavailable(err):
			// A token desk will never accept (audience, issuer, signature):
			// a setup problem, so the session ends rather than retrying.
			a.log.Error(err, "The provider's refreshed ID token does not verify; ending the session")
			return nil, fmt.Errorf("%w: %w", errInvalidIDToken, err)
		case err != nil:
			a.log.Info("Could not fetch the provider's signing keys to verify a refreshed ID token; the browser will retry",
				"error", err.Error())
			kept := *old
			if tok.RefreshToken != "" {
				kept.RefreshToken = tok.RefreshToken
			}
			res.session, res.retry = &kept, true
			if kept.RefreshToken == refreshToken {
				// Nothing was used up: the next attempt asks the provider again.
				return res, nil
			}
		default:
			if fresh.RefreshToken == "" {
				// The provider does not rotate refresh tokens.
				fresh.RefreshToken = refreshToken
			}
			res.session = fresh
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		now := time.Now()
		if a.refreshed == nil {
			a.refreshed = map[[sha256.Size]byte]refreshResult{}
			a.retriedFrom = map[[sha256.Size]byte][sha256.Size]byte{}
		}
		for k, v := range a.refreshed {
			if now.After(v.until) {
				delete(a.refreshed, k)
				delete(a.retriedFrom, sha256.Sum256([]byte(v.session.RefreshToken)))
			}
		}
		res.until = now.Add(refreshGrace)
		a.refreshed[key] = res
		// Requests still carrying the refresh token that an earlier retry
		// replaced get this outcome too, not the used-up token of the retry.
		for k, ok := key, true; ok; {
			var parent [sha256.Size]byte
			if parent, ok = a.retriedFrom[k]; ok {
				if _, live := a.refreshed[parent]; live {
					a.refreshed[parent] = res
				}
				k = parent
			}
		}
		if res.retry {
			a.retriedFrom[sha256.Sum256([]byte(res.session.RefreshToken))] = key
		}
		return res, nil
	})
	if err != nil {
		return nil, false, err
	}
	res := v.(refreshResult)
	return res.session, res.retry, nil
}

func (a *Auth) recentRefresh(key [sha256.Size]byte) (refreshResult, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	v, ok := a.refreshed[key]
	if !ok || time.Now().After(v.until) {
		return refreshResult{}, false
	}
	return v, true
}
