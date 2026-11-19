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
}

func newFakeIssuer(t *testing.T) *fakeIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: key, KeyID: "k1"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIssuer{key: key, signer: signer, codes: map[string]string{}, lifetime: time.Hour}
	mux := http.NewServeMux()
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
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
			f.refresh++
		}
		if f.nonce != "" {
			nonce = f.nonce
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at", "token_type": "Bearer", "expires_in": 3600,
			"refresh_token": "rt", "id_token": f.idToken(t, nonce),
		})
	})
	return f
}

func (f *fakeIssuer) idToken(t *testing.T, nonce string) string {
	now := time.Now()
	claims := map[string]any{
		"iss": f.URL, "sub": "alice", "aud": "manifesto-desk", "email": "alice@example.io", "name": "Alice",
		"iat": now.Unix(), "exp": now.Add(f.lifetime).Unix(),
	}
	if nonce != "" {
		claims["nonce"] = nonce
	}
	raw, err := jwt.Signed(f.signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// browser follows redirects within the test servers and keeps cookies.
type browser struct {
	t       *testing.T
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
			resp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Get(target)
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
	}, newSealer(t))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func signIn(t *testing.T, a *Auth) *browser {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /auth/login", a.Login)
	mux.HandleFunc("GET /auth/callback", a.Callback)
	b := &browser{t: t, handler: mux, cookies: map[string]*http.Cookie{}}
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
	rec := httptest.NewRecorder()
	s, err := a.Session(rec, req)
	for _, c := range rec.Result().Cookies() {
		b.cookies[c.Name] = c
	}
	return s, err
}

func TestSignIn(t *testing.T) {
	issuer := newFakeIssuer(t)
	a := newAuth(t, issuer)
	b := signIn(t, a)

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
	if s.Name != "Alice" || s.IDToken == "" || s.RefreshToken != "rt" {
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
	b := &browser{t: t, handler: mux, cookies: map[string]*http.Cookie{}}
	if resp := b.do(http.MethodGet, "http://desk/auth/login", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
	if _, ok := b.cookies[sessionCookie]; ok {
		t.Error("a session was started")
	}
}

func TestSessionRefresh(t *testing.T) {
	issuer := newFakeIssuer(t)
	issuer.lifetime = 10 * time.Second // already inside the 30s refresh window
	a := newAuth(t, issuer)
	b := signIn(t, a)
	first := b.cookies[sessionCookie].Value

	s, err := sessionOf(t, a, b)
	if err != nil {
		t.Fatal(err)
	}
	if issuer.refresh != 1 || b.cookies[sessionCookie].Value == first || s.IDToken == "" {
		t.Errorf("refreshes = %d; the session cookie must be replaced", issuer.refresh)
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
	if _, err := sessionOf(t, a, b); err == nil {
		t.Error("an expired session without refresh token was accepted")
	}
}

func cookieRequest(b *browser) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, c := range b.cookies {
		req.AddCookie(c)
	}
	return req
}
