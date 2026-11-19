/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package signing

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const key = "3f1c9a2e-0000-4000-8000-000000000001/sha256/c4275d038e3ba866777b1e00323afa2f0ac18b07a8f98b9087720a19f313a398.svg"

func TestParseObjectKey(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		{key, true},
		{"3f1c9a2e-0000-4000-8000-000000000001/sha256/c4275d038e3ba866777b1e00323afa2f0ac18b07a8f98b9087720a19f313a398", true},
		{"../etc/passwd", false},
		{"3f1c9a2e-0000-4000-8000-000000000001/sha256/C4275D038E3BA866777B1E00323AFA2F0AC18B07A8F98B9087720A19F313A398.svg", false},
		{"3f1c9a2e-0000-4000-8000-000000000001/sha256/c4275d038e3ba866777b1e00323afa2f0ac18b07a8f98b9087720a19f313a398.svg/../x", false},
		{"3f1c9a2e-0000-4000-8000-000000000001/md5/c4275d038e3ba866777b1e00323afa2f.svg", false},
	}
	for _, tt := range tests {
		if _, err := ParseObjectKey(tt.key); (err == nil) != tt.want {
			t.Errorf("ParseObjectKey(%q) err = %v, want ok=%v", tt.key, err, tt.want)
		}
	}
}

func TestVerify(t *testing.T) {
	k1, k2 := []byte("current-key"), []byte("old-key")
	now := time.Unix(1_800_000_000, 0)
	exp := now.Add(5 * time.Minute)
	q := Query(k1, http.MethodPut, key, 255, exp)

	if size, err := Verify([][]byte{k1}, http.MethodPut, key, q, now); err != nil || size != 255 {
		t.Fatalf("Verify() = %d, %v", size, err)
	}
	if _, err := Verify([][]byte{k2, k1}, http.MethodPut, key, q, now); err != nil {
		t.Errorf("any accepted key must verify: %v", err)
	}
	tests := []struct {
		name   string
		keys   [][]byte
		method string
		key    string
		mutate func(q map[string][]string)
		now    time.Time
	}{
		{"wrong key", [][]byte{k2}, http.MethodPut, key, nil, now},
		{"other method", [][]byte{k1}, http.MethodDelete, key, nil, now},
		{"other object", [][]byte{k1}, http.MethodPut, key[:len(key)-4], nil, now},
		{"tampered size", [][]byte{k1}, http.MethodPut, key, func(q map[string][]string) { q[ParamSize] = []string{"999"} }, now},
		{"extended expiry", [][]byte{k1}, http.MethodPut, key, func(q map[string][]string) { q[ParamExpires] = []string{"9999999999"} }, now},
		{"expired", [][]byte{k1}, http.MethodPut, key, nil, exp.Add(time.Second)},
		{"no sig", [][]byte{k1}, http.MethodPut, key, func(q map[string][]string) { delete(q, ParamSig) }, now},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := Query(k1, http.MethodPut, key, 255, exp)
			if tt.mutate != nil {
				tt.mutate(q)
			}
			if _, err := Verify(tt.keys, tt.method, tt.key, q, tt.now); err == nil {
				t.Error("Verify() succeeded, want error")
			}
		})
	}
}

func TestLoadKeys(t *testing.T) {
	dir := t.TempDir()
	for name, v := range map[string]string{"previous": "old", CurrentKey: "new", "..data": "x"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(v), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	keys, err := LoadKeys(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || string(keys[0]) != "new" || string(keys[1]) != "old" {
		t.Errorf("keys = %q", keys)
	}
	if _, err := LoadKeys(t.TempDir()); err == nil {
		t.Error("empty dir must be an error")
	}
}
