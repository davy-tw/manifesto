/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

// Package signing signs and verifies the short-lived URLs press hands out
// for darkroom: uploads (PUT) on the public port, and existence checks
// (HEAD) and deletions (DELETE) on the internal one.
//
// A signature is HMAC-SHA256 over the method, object key, size and expiry,
// so a URL is only good for that one operation on that one object.
package signing

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"time"
)

// Query parameters of a signed URL.
const (
	ParamSize    = "size"
	ParamExpires = "expires"
	ParamSig     = "sig"
)

// CurrentKey is the Secret key press signs with. darkroom also accepts every
// other key in the Secret, so a rotated-out key keeps working for a while.
const CurrentKey = "current"

var objectKeyRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/sha256/([0-9a-f]{64})(\.[a-z0-9]{1,10})?$`)

// ParseObjectKey validates "<namespace UID>/sha256/<hash>[.ext]" and returns
// the hash.
func ParseObjectKey(key string) (hash string, err error) {
	m := objectKeyRE.FindStringSubmatch(key)
	if m == nil {
		return "", fmt.Errorf("invalid object key %q", key)
	}
	return m[1], nil
}

// NewKey returns a random signing key.
func NewKey() ([]byte, error) {
	k := make([]byte, 32)
	_, err := rand.Read(k)
	return k, err
}

// Sign returns the signature of method on objectKey with size, valid until expires.
func Sign(key []byte, method, objectKey string, size int64, expires time.Time) string {
	mac := hmac.New(sha256.New, key)
	_, _ = fmt.Fprintf(mac, "%s\n%s\n%d\n%d", method, objectKey, size, expires.Unix())
	return hex.EncodeToString(mac.Sum(nil))
}

// Query returns the signed query parameters for method on objectKey.
func Query(key []byte, method, objectKey string, size int64, expires time.Time) url.Values {
	return url.Values{
		ParamSize:    {strconv.FormatInt(size, 10)},
		ParamExpires: {strconv.FormatInt(expires.Unix(), 10)},
		ParamSig:     {Sign(key, method, objectKey, size, expires)},
	}
}

// ErrExpired is returned for a correctly signed URL past its expiry.
var ErrExpired = errors.New("signed URL expired")

// Verify checks the signed query q for method on objectKey against any of
// keys, and returns the signed size.
func Verify(keys [][]byte, method, objectKey string, q url.Values, now time.Time) (int64, error) {
	size, err := strconv.ParseInt(q.Get(ParamSize), 10, 64)
	if err != nil {
		return 0, errors.New("missing or invalid size")
	}
	exp, err := strconv.ParseInt(q.Get(ParamExpires), 10, 64)
	if err != nil {
		return 0, errors.New("missing or invalid expires")
	}
	got, err := hex.DecodeString(q.Get(ParamSig))
	if err != nil || len(got) == 0 {
		return 0, errors.New("missing or invalid sig")
	}
	expires := time.Unix(exp, 0)
	for _, k := range keys {
		want, _ := hex.DecodeString(Sign(k, method, objectKey, size, expires))
		if hmac.Equal(got, want) {
			if now.After(expires) {
				return 0, ErrExpired
			}
			return size, nil
		}
	}
	return 0, errors.New("signature does not match")
}

// LoadKeys reads the keys of a mounted Secret: every regular file in dir,
// CurrentKey first.
func LoadKeys(dir string) ([][]byte, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		// Secret volumes hold dot-prefixed bookkeeping entries and symlinks.
		if e.Name()[0] != '.' {
			names = append(names, e.Name())
		}
	}
	slices.SortFunc(names, func(a, b string) int {
		switch {
		case a == CurrentKey:
			return -1
		case b == CurrentKey:
			return 1
		}
		return 0
	})
	var keys [][]byte
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			continue // a directory or a dangling link
		}
		if len(b) > 0 {
			keys = append(keys, b)
		}
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("no signing keys in %s", dir)
	}
	return keys, nil
}
