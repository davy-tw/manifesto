/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package desk

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// cookieChunk keeps each cookie well under the 4KB browsers accept; a
// session (ID token and refresh token) may need several.
const cookieChunk = 3500

// maxChunks bounds how many cookies one value may use.
const maxChunks = 8

// Sealer encrypts and authenticates cookie values with AES-GCM, so desk
// keeps no server-side state and can run any number of replicas.
type Sealer struct {
	aead cipher.AEAD
}

// NewSealer returns a Sealer for a 32-byte key.
func NewSealer(key []byte) (*Sealer, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("session key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Sealer{aead: aead}, nil
}

// seal encrypts v as JSON; name is authenticated so a value sealed for one
// cookie cannot be replayed as another.
func (s *Sealer) seal(name string, v any) (string, error) {
	plain, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(s.aead.Seal(nonce, nonce, plain, []byte(name))), nil
}

func (s *Sealer) open(name, sealed string, v any) error {
	b, err := base64.RawURLEncoding.DecodeString(sealed)
	if err != nil {
		return err
	}
	n := s.aead.NonceSize()
	if len(b) < n {
		return errors.New("sealed value too short")
	}
	plain, err := s.aead.Open(nil, b[:n], b[n:], []byte(name))
	if err != nil {
		return err
	}
	return json.Unmarshal(plain, v)
}

// cookieOptions are applied to every cookie desk sets.
type cookieOptions struct {
	Secure   bool
	SameSite http.SameSite
	MaxAge   time.Duration
}

// SetCookie seals v into one or more cookies named name, name.1, ...
func (s *Sealer) SetCookie(w http.ResponseWriter, r *http.Request, name string, v any, opts cookieOptions) error {
	sealed, err := s.seal(name, v)
	if err != nil {
		return err
	}
	var chunks []string
	for len(sealed) > cookieChunk {
		chunks, sealed = append(chunks, sealed[:cookieChunk]), sealed[cookieChunk:]
	}
	chunks = append(chunks, sealed)
	if len(chunks) > maxChunks {
		return fmt.Errorf("%s is too large for cookies", name)
	}
	for i, c := range chunks {
		http.SetCookie(w, s.cookie(chunkName(name, i), c, opts))
	}
	// Drop chunks left over from a larger earlier value.
	for i := len(chunks); i < maxChunks; i++ {
		if _, err := r.Cookie(chunkName(name, i)); err == nil {
			http.SetCookie(w, s.cookie(chunkName(name, i), "", cookieOptions{Secure: opts.Secure, SameSite: opts.SameSite, MaxAge: -1}))
		}
	}
	return nil
}

// ReadCookie reassembles and opens the cookies named name.
func (s *Sealer) ReadCookie(r *http.Request, name string, v any) error {
	var b strings.Builder
	for i := range maxChunks {
		c, err := r.Cookie(chunkName(name, i))
		if err != nil {
			break
		}
		b.WriteString(c.Value)
	}
	if b.Len() == 0 {
		return http.ErrNoCookie
	}
	return s.open(name, b.String(), v)
}

// ClearCookie removes every chunk of name.
func (s *Sealer) ClearCookie(w http.ResponseWriter, r *http.Request, name string, opts cookieOptions) {
	for i := range maxChunks {
		if _, err := r.Cookie(chunkName(name, i)); err == nil {
			http.SetCookie(w, s.cookie(chunkName(name, i), "", cookieOptions{Secure: opts.Secure, SameSite: opts.SameSite, MaxAge: -1}))
		}
	}
}

func (s *Sealer) cookie(name, value string, opts cookieOptions) *http.Cookie {
	c := &http.Cookie{
		Name: name, Value: value, Path: "/", HttpOnly: true, Secure: opts.Secure, SameSite: opts.SameSite,
	}
	switch {
	case opts.MaxAge < 0:
		c.MaxAge = -1
	case opts.MaxAge > 0:
		c.MaxAge = int(opts.MaxAge.Seconds())
	}
	return c
}

func chunkName(name string, i int) string {
	if i == 0 {
		return name
	}
	return name + "." + strconv.Itoa(i)
}
