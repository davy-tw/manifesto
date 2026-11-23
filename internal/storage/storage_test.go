/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"

	"go.pkgs.davy.tw/manifesto/internal/darkroom"
)

func TestExtension(t *testing.T) {
	for ct, want := range map[string]string{
		"image/png": ".png", "image/jpeg": ".jpg", "image/svg+xml": ".svg", "image/webp": ".webp",
		"application/pdf": ".pdf", "application/x-unknown-thing": "",
	} {
		if got := Extension(ct); got != want {
			t.Errorf("Extension(%q) = %q, want %q", ct, got, want)
		}
	}
}

// TestPVCAgainstDarkroom runs the driver against a real darkroom.
func TestPVCAgainstDarkroom(t *testing.T) {
	key := []byte("k")
	store := &darkroom.Store{Dir: t.TempDir(), Keys: func() [][]byte { return [][]byte{key} }, Log: logr.Discard()}
	public := httptest.NewServer(store.PublicHandler())
	defer public.Close()
	internal := httptest.NewServer(store.InternalHandler())
	defer internal.Close()

	d := &PVC{InternalURL: internal.URL, PublicBase: public.URL + "/", Key: key}
	ctx := context.Background()
	if err := d.Check(ctx); err != nil {
		t.Fatal(err)
	}

	body := "hello"
	sum := sha256.Sum256([]byte(body))
	objectKey := ObjectKey("3f1c9a2e-0000-4000-8000-000000000001", hex.EncodeToString(sum[:]), "text/plain")

	if ok, err := d.Stored(ctx, objectKey, int64(len(body)), false); err != nil || ok {
		t.Fatalf("Exists before upload = %v, %v", ok, err)
	}
	up, err := d.SignUpload(ctx, objectKey, "text/plain", int64(len(body)), 5*time.Minute)
	if err != nil || time.Until(up.Expires) < 4*time.Minute {
		t.Fatalf("SignUpload = %v, %v", up.Expires, err)
	}
	req, _ := http.NewRequest(http.MethodPut, up.URL, strings.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload = %v, %v", resp, err)
	}
	if ok, err := d.Stored(ctx, objectKey, int64(len(body)), false); err != nil || !ok {
		t.Fatalf("Exists after upload = %v, %v", ok, err)
	}
	if got := d.PublicURL(objectKey); got != public.URL+"/"+objectKey {
		t.Errorf("PublicURL = %q", got)
	}
	if err := d.Delete(ctx, objectKey); err != nil {
		t.Fatal(err)
	}
	if ok, _ := d.Stored(ctx, objectKey, int64(len(body)), false); ok {
		t.Error("object still exists after Delete")
	}
	if err := d.Delete(ctx, objectKey); err != nil {
		t.Errorf("deleting a missing object: %v", err)
	}

	bad := &PVC{InternalURL: internal.URL, Key: []byte("wrong")}
	if _, err := bad.Stored(ctx, objectKey, int64(len(body)), false); err == nil {
		t.Error("a wrong key must be rejected")
	}
	if err := (&PVC{InternalURL: "http://127.0.0.1:1"}).Check(ctx); err == nil {
		t.Error("Check must fail when darkroom is unreachable")
	}
}
