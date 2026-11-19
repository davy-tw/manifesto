/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package desk

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newSealer(t *testing.T) *Sealer {
	t.Helper()
	s, err := NewSealer([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// roundTrip sets v as cookie name and reads it back from a new request.
func roundTrip(t *testing.T, s *Sealer, name string, v any) (*http.Request, []*http.Cookie) {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := s.SetCookie(rec, httptest.NewRequest(http.MethodGet, "/", nil), name, v, cookieOptions{Secure: true}); err != nil {
		t.Fatal(err)
	}
	cookies := rec.Result().Cookies()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	return req, cookies
}

func TestSealerRoundTrip(t *testing.T) {
	s := newSealer(t)
	tests := []struct {
		name       string
		value      Session
		wantChunks int
	}{
		{"small", Session{IDToken: "abc", Name: "Alice"}, 1},
		{"larger than one cookie", Session{IDToken: strings.Repeat("x", 3000), RefreshToken: strings.Repeat("y", 2000)}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, cookies := roundTrip(t, s, sessionCookie, tt.value)
			if len(cookies) != tt.wantChunks {
				t.Errorf("%d cookies, want %d", len(cookies), tt.wantChunks)
			}
			for _, c := range cookies {
				if !c.HttpOnly || !c.Secure || len(c.Value) > cookieChunk {
					t.Errorf("cookie %s: HttpOnly=%v Secure=%v len=%d", c.Name, c.HttpOnly, c.Secure, len(c.Value))
				}
			}
			var got Session
			if err := s.ReadCookie(req, sessionCookie, &got); err != nil {
				t.Fatal(err)
			}
			if got != tt.value {
				t.Error("value changed in the round trip")
			}
		})
	}
}

func TestSealerRejectsTamperingAndReplay(t *testing.T) {
	s := newSealer(t)
	_, cookies := roundTrip(t, s, sessionCookie, Session{IDToken: "abc"})
	sealed := cookies[0].Value

	tampered := httptest.NewRequest(http.MethodGet, "/", nil)
	tampered.AddCookie(&http.Cookie{Name: sessionCookie, Value: sealed[:len(sealed)-2] + "AA"})
	if err := s.ReadCookie(tampered, sessionCookie, &Session{}); err == nil {
		t.Error("a tampered cookie was accepted")
	}

	// A value sealed for one cookie must not open under another name.
	replayed := httptest.NewRequest(http.MethodGet, "/", nil)
	replayed.AddCookie(&http.Cookie{Name: loginCookie, Value: sealed})
	if err := s.ReadCookie(replayed, loginCookie, &loginState{}); err == nil {
		t.Error("a session cookie opened as the login cookie")
	}

	other, _ := NewSealer([]byte("ffffffffffffffffffffffffffffffff"))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: sealed})
	if err := other.ReadCookie(req, sessionCookie, &Session{}); err == nil {
		t.Error("a cookie opened with another key")
	}
	if _, err := NewSealer([]byte("short")); err == nil {
		t.Error("a short key was accepted")
	}
}
