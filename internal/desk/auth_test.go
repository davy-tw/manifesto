/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package desk

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// fakeIssuer is a minimal OpenID Connect provider: discovery, keys, an
// authorize endpoint that signs the user in at once, and a token endpoint
// for authorization codes and refresh tokens.
type fakeIssuer struct {
	*httptest.Server
	key    *rsa.PrivateKey
	signer jose.Signer

	mu       sync.Mutex
	codes    map[string]string // code -> nonce
	lifetime time.Duration
	nonce    string // overrides the nonce put in ID tokens when set
	refresh  int    // refresh grants served
	// The issuer rotates refresh tokens with reuse detection: each is
	// accepted once, and using one again revokes the whole chain.
	issued  int
	current string // the refresh token that is still valid
	reused  int    // refresh tokens presented a second time
	// failNext makes the next refresh grant fail with this status, without
	// using up the refresh token, as a provider outage would.
	failNext int
	// noIDToken leaves the ID token out of refresh responses.
	noIDToken bool
	// badIDTokenNext makes the next refresh return an ID token desk cannot
	// verify (here: for another audience), after rotating the refresh token.
	badIDTokenNext bool
}

func newFakeIssuer(t *testing.T) *fakeIssuer {
	t.Helper()
	f, err := startFakeIssuer()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.Close)
	return f
}

// startFakeIssuer serves the provider over TLS, as the API server requires
// for issuers it trusts.
func startFakeIssuer() (*fakeIssuer, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: key, KeyID: "k1"}}, nil)
	if err != nil {
		return nil, err
	}
	f := &fakeIssuer{key: key, signer: signer, codes: map[string]string{}, lifetime: time.Hour}
	mux := http.NewServeMux()
	f.Server = httptest.NewTLSServer(mux)
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": f.URL, "authorization_endpoint": f.URL + "/authorize", "token_endpoint": f.URL + "/token",
			"jwks_uri": f.URL + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
			http.Error(w, "PKCE required", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		code := "code-" + q.Get("state")
		f.codes[code] = q.Get("nonce")
		f.mu.Unlock()
		http.Redirect(w, r, q.Get("redirect_uri")+"?code="+code+"&state="+url.QueryEscape(q.Get("state")), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		defer f.mu.Unlock()
		nonce := ""
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			var ok bool
			if nonce, ok = f.codes[r.Form.Get("code")]; !ok || r.Form.Get("code_verifier") == "" {
				http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
				return
			}
			delete(f.codes, r.Form.Get("code"))
		case "refresh_token":
			if f.failNext != 0 {
				code := f.failNext
				f.failNext = 0
				http.Error(w, `{"error":"server_error"}`, code)
				return
			}
			if rt := r.Form.Get("refresh_token"); rt != f.current || rt == "" {
				f.reused++
				f.current = "" // revoke the chain
				http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
				return
			}
			f.refresh++
		}
		f.issued++
		f.current = fmt.Sprintf("rt-%d", f.issued)
		if f.nonce != "" {
			nonce = f.nonce
		}
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"access_token": "at", "token_type": "Bearer", "expires_in": 3600,
			"refresh_token": f.current, "id_token": f.idToken(nonce),
		}
		if f.noIDToken && r.Form.Get("grant_type") == "refresh_token" {
			delete(resp, "id_token")
		}
		if f.badIDTokenNext && r.Form.Get("grant_type") == "refresh_token" {
			f.badIDTokenNext = false
			resp["id_token"] = f.signedIDToken(nonce, "someone-else")
		}
		_ = json.NewEncoder(w).Encode(resp)
	})
	return f, nil
}

// testGroups are the groups in every ID token the fake issuer signs.
var testGroups = []string{"blog-alice-editors"}

func (f *fakeIssuer) idToken(nonce string) string { return f.signedIDToken(nonce, "manifesto-desk") }

func (f *fakeIssuer) signedIDToken(nonce, audience string) string {
	now := time.Now()
	claims := map[string]any{
		"iss": f.URL, "sub": "alice", "aud": audience, "email": "alice@example.io", "name": "Alice",
		"groups": testGroups, "iat": now.Unix(), "exp": now.Add(f.lifetime).Unix(),
	}
	if nonce != "" {
		claims["nonce"] = nonce
	}
	raw, err := jwt.Signed(f.signer).Claims(claims).Serialize()
	if err != nil {
		panic(err)
	}
	return raw
}

// browser follows redirects within the test servers and keeps cookies.
type browser struct {
	t       *testing.T
	issuer  *fakeIssuer
	handler http.Handler
	cookies map[string]*http.Cookie
}

func (b *browser) do(method, target string, header http.Header) *http.Response {
	b.t.Helper()
	for range 10 {
		req := httptest.NewRequest(method, target, nil)
		maps.Copy(req.Header, header)
		for _, c := range b.cookies {
			req.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		if strings.HasPrefix(target, "http") && !strings.HasPrefix(target, "http://desk") {
			hc := &http.Client{Transport: b.issuer.Client().Transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			resp, err := hc.Get(target)
			if err != nil {
				b.t.Fatal(err)
			}
			_ = resp.Body.Close()
			target = resp.Header.Get("Location")
			continue
		}
		b.handler.ServeHTTP(rec, req)
		resp := rec.Result()
		for _, c := range resp.Cookies() {
			if c.MaxAge < 0 {
				delete(b.cookies, c.Name)
			} else {
				b.cookies[c.Name] = c
			}
		}
		if loc := resp.Header.Get("Location"); resp.StatusCode == http.StatusFound && loc != "" && loc != "/" {
			target = loc
			continue
		}
		return resp
	}
	b.t.Fatal("too many redirects")
	return nil
}

func newAuth(t *testing.T, issuer *fakeIssuer) *Auth {
	t.Helper()
	a, err := NewAuth(context.Background(), AuthConfig{
		Issuer: issuer.URL, ClientID: "manifesto-desk", ClientSecret: "s",
		RedirectURL: "http://desk/auth/callback", Scopes: []string{"openid", "email"}, Secure: true,
		HTTPClient: issuer.Client(),
	}, newSealer(t))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func signIn(t *testing.T, issuer *fakeIssuer, a *Auth) *browser {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /auth/login", a.Login)
	mux.HandleFunc("GET /auth/callback", a.Callback)
	b := &browser{t: t, issuer: issuer, handler: mux, cookies: map[string]*http.Cookie{}}
	resp := b.do(http.MethodGet, "http://desk/auth/login", nil)
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/" {
		t.Fatalf("sign-in ended with %d, Location %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	return b
}

func sessionOf(t *testing.T, a *Auth, b *browser) (*Session, error) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "http://desk/api/session", nil)
	for _, c := range b.cookies {
		req.AddCookie(c)
	}
	return a.Session(req)
}

func TestSignIn(t *testing.T) {
	issuer := newFakeIssuer(t)
	a := newAuth(t, issuer)
	b := signIn(t, issuer, a)

	if _, ok := b.cookies[loginCookie]; ok {
		t.Error("the login cookie must be cleared after the callback")
	}
	sc := b.cookies[sessionCookie]
	if sc == nil || sc.SameSite != http.SameSiteStrictMode || !sc.HttpOnly || !sc.Secure {
		t.Fatalf("session cookie = %+v", sc)
	}
	s, err := sessionOf(t, a, b)
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "Alice" || s.Token == "" || s.RefreshToken != "rt-1" {
		t.Errorf("session = %+v", s)
	}
}

func TestSignInRejectsWrongState(t *testing.T) {
	a := newAuth(t, newFakeIssuer(t))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /auth/login", a.Login)
	mux.HandleFunc("GET /auth/callback", a.Callback)
	// Start a login but answer with someone else's state.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://desk/auth/login", nil))
	req := httptest.NewRequest(http.MethodGet, "http://desk/auth/callback?code=x&state=forged", nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("forged state: status = %d, want 400", rec.Code)
	}
	// Without the login cookie at all.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://desk/auth/callback?code=x&state=y", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("no login cookie: status = %d, want 400", rec.Code)
	}
}

func TestSignInRejectsWrongNonce(t *testing.T) {
	issuer := newFakeIssuer(t)
	issuer.nonce = "replayed"
	a := newAuth(t, issuer)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /auth/login", a.Login)
	mux.HandleFunc("GET /auth/callback", a.Callback)
	b := &browser{t: t, issuer: issuer, handler: mux, cookies: map[string]*http.Cookie{}}
	if resp := b.do(http.MethodGet, "http://desk/auth/login", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
	if _, ok := b.cookies[sessionCookie]; ok {
		t.Error("a session was started")
	}
}

// refreshWith POSTs /auth/refresh with cookies and returns the response.
func refreshWith(a *Auth, cookies map[string]*http.Cookie) *http.Response {
	req := httptest.NewRequest(http.MethodPost, "http://desk/auth/refresh", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	a.Refresh(rec, req)
	return rec.Result()
}

func (b *browser) refresh(a *Auth) int {
	resp := refreshWith(a, b.cookies)
	for _, c := range resp.Cookies() {
		if c.MaxAge < 0 {
			delete(b.cookies, c.Name)
		} else {
			b.cookies[c.Name] = c
		}
	}
	return resp.StatusCode
}

func TestSessionRefresh(t *testing.T) {
	issuer := newFakeIssuer(t)
	issuer.lifetime = 3 * time.Second // already about to expire
	a := newAuth(t, issuer)
	b := signIn(t, issuer, a)
	first := b.cookies[sessionCookie].Value

	// Session does not refresh: it asks the browser to.
	if _, err := sessionOf(t, a, b); !errors.Is(err, errSessionExpired) {
		t.Fatalf("Session of an expired ID token = %v, want errSessionExpired", err)
	}
	// Tokens from now on are still usable but inside the refresh window.
	issuer.lifetime = 20 * time.Second
	if code := b.refresh(a); code != http.StatusNoContent {
		t.Fatalf("refresh = %d", code)
	}
	s, err := sessionOf(t, a, b)
	if err != nil {
		t.Fatal(err)
	}
	if issuer.refresh != 1 || b.cookies[sessionCookie].Value == first || s.Token == "" {
		t.Errorf("refreshes = %d; the session cookie must be replaced", issuer.refresh)
	}
	// The rotated refresh token is the one kept, so the next refresh works.
	if s.RefreshToken != "rt-2" {
		t.Errorf("refresh token = %q, want the rotated rt-2", s.RefreshToken)
	}
	if code := b.refresh(a); code != http.StatusNoContent || issuer.refresh != 2 || issuer.reused != 0 {
		t.Errorf("second refresh = %d, grants %d, reused %d", code, issuer.refresh, issuer.reused)
	}

	// A session that does not expire soon is not refreshed.
	issuer.lifetime = time.Hour
	if code := b.refresh(a); code != http.StatusNoContent {
		t.Fatal(code)
	}
	if code := b.refresh(a); code != http.StatusNoContent || issuer.refresh != 3 {
		t.Errorf("refreshing a fresh session: %d, grants %d", code, issuer.refresh)
	}

	// Without a refresh token an expired session is over.
	var expired Session
	if err := a.sealer.ReadCookie(cookieRequest(b), sessionCookie, &expired); err != nil {
		t.Fatal(err)
	}
	expired.RefreshToken, expired.Expiry = "", time.Now().Add(-time.Minute)
	rec := httptest.NewRecorder()
	if err := a.sealer.SetCookie(rec, httptest.NewRequest(http.MethodGet, "/", nil), sessionCookie, expired, a.sessionOptions()); err != nil {
		t.Fatal(err)
	}
	b.cookies[sessionCookie] = rec.Result().Cookies()[0]
	if _, err := sessionOf(t, a, b); !errors.Is(err, errNoSession) {
		t.Errorf("an expired session without refresh token: %v", err)
	}
	if code := b.refresh(a); code != http.StatusUnauthorized {
		t.Errorf("refreshing it = %d, want 401", code)
	}
}

// A provider that fails for a while must not sign the user out; one that
// refuses the refresh token, or answers without an ID token, does.
func TestRefreshFailures(t *testing.T) {
	issuer := newFakeIssuer(t)
	issuer.lifetime = 3 * time.Second
	a := newAuth(t, issuer)
	b := signIn(t, issuer, a)

	for _, code := range []int{http.StatusInternalServerError, http.StatusTooManyRequests} {
		issuer.failNext = code
		if got := b.refresh(a); got != http.StatusServiceUnavailable {
			t.Errorf("provider answered %d: refresh = %d, want 503", code, got)
		}
		if _, ok := b.cookies[sessionCookie]; !ok {
			t.Fatalf("provider answered %d: the session cookie was cleared", code)
		}
	}
	if got := b.refresh(a); got != http.StatusNoContent || issuer.reused != 0 {
		t.Fatalf("retry = %d, reused %d", got, issuer.reused)
	}

	issuer.noIDToken = true
	resp := refreshWith(a, b.cookies)
	msg, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(string(msg), "no ID token") {
		t.Errorf("refresh without an ID token = %d %s", resp.StatusCode, msg)
	}
	issuer.noIDToken = false

	// An ID token that does not verify, after the provider rotated the
	// refresh token: the new refresh token is kept for the retry, also for
	// a request still carrying the old cookie.
	b = signIn(t, issuer, a)
	stale := maps.Clone(b.cookies)
	issuer.badIDTokenNext = true
	if got := b.refresh(a); got != http.StatusServiceUnavailable {
		t.Fatalf("unverifiable ID token: refresh = %d, want 503", got)
	}
	if got := refreshWith(a, stale).StatusCode; got != http.StatusServiceUnavailable {
		t.Errorf("old cookie after the failure: refresh = %d, want 503", got)
	}
	if got := b.refresh(a); got != http.StatusNoContent || issuer.reused != 0 {
		t.Fatalf("retry after an unverifiable ID token = %d, reused %d", got, issuer.reused)
	}

	// A refused refresh token ends the session.
	b2 := signIn(t, issuer, a)
	issuer.current = "revoked-elsewhere"
	if got := b2.refresh(a); got != http.StatusUnauthorized {
		t.Errorf("refused refresh token: refresh = %d, want 401", got)
	}
	if _, ok := b2.cookies[sessionCookie]; ok {
		t.Error("a refused refresh token kept the session")
	}
}

// Requests that carry the same expired cookie, at once or just after the
// refresh, must not present its refresh token to the provider twice.
func TestConcurrentRefreshUsesTheRefreshTokenOnce(t *testing.T) {
	issuer := newFakeIssuer(t)
	issuer.lifetime = 3 * time.Second
	a := newAuth(t, issuer)
	b := signIn(t, issuer, a)
	stale := maps.Clone(b.cookies)

	var wg sync.WaitGroup
	results := make([]*http.Response, 8, 9)
	for i := range results {
		wg.Go(func() { results[i] = refreshWith(a, stale) })
	}
	wg.Wait()
	// One more, after the others finished, still with the old cookie.
	results = append(results, refreshWith(a, stale))

	tokens := make([]string, 0, len(results))
	for _, resp := range results {
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("refresh = %d", resp.StatusCode)
		}
		var s Session
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		for _, c := range resp.Cookies() {
			req.AddCookie(c)
		}
		if err := a.sealer.ReadCookie(req, sessionCookie, &s); err != nil {
			t.Fatal(err)
		}
		tokens = append(tokens, s.RefreshToken)
	}
	if issuer.refresh != 1 || issuer.reused != 0 {
		t.Errorf("grants = %d, reused = %d; want one grant", issuer.refresh, issuer.reused)
	}
	for _, rt := range tokens {
		if rt != "rt-2" {
			t.Errorf("a request got refresh token %q, want rt-2", rt)
		}
	}
}

func cookieRequest(b *browser) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, c := range b.cookies {
		req.AddCookie(c)
	}
	return req
}
