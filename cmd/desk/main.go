/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

// Command desk serves the authors' web UI. It signs users in with OIDC and
// acts on the API server with each user's own token; it needs no RBAC of
// its own.
package main

import (
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
	"go.pkgs.davy.tw/manifesto/internal/desk"
)

func main() {
	var addr, publicURL, issuer, clientID, scopes, keyFile string
	flag.StringVar(&addr, "bind-address", ":8080", "The address desk listens on.")
	flag.StringVar(&publicURL, "public-url", "", "desk's public URL, e.g. https://desk.example.io (required).")
	flag.StringVar(&issuer, "oidc-issuer", "", "OIDC issuer URL; the API server must trust the same issuer (required).")
	flag.StringVar(&clientID, "oidc-client-id", "manifesto-desk",
		"OIDC client ID; the API server's audience for desk's tokens.")
	flag.StringVar(&scopes, "oidc-scopes", "openid,email,profile",
		"Comma-separated scopes. Add offline_access (or your provider's equivalent) for refresh tokens, "+
			"and groups if your provider needs it.")
	flag.StringVar(&keyFile, "session-key-file", "/etc/desk/session/key",
		"File with the 32-byte key (or 64 hex characters) that encrypts session cookies.")
	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	log := zap.New(zap.UseFlagOptions(&opts)).WithName("desk")
	ctrl.SetLogger(log)

	fail := func(err error, msg string) {
		log.Error(err, msg)
		os.Exit(1)
	}
	public, err := url.Parse(publicURL)
	if err != nil || public.Host == "" {
		fail(errors.New("--public-url is required"), "Invalid configuration")
	}
	if issuer == "" {
		fail(errors.New("--oidc-issuer is required"), "Invalid configuration")
	}
	key, err := readKey(keyFile)
	if err != nil {
		fail(err, "Cannot read the session key")
	}
	sealer, err := desk.NewSealer(key)
	if err != nil {
		fail(err, "Invalid session key")
	}

	ctx := ctrl.SetupSignalHandler()
	auth, err := desk.NewAuth(ctx, desk.AuthConfig{
		Issuer: issuer, ClientID: clientID,
		// The secret comes from the environment so it never shows in the process list.
		ClientSecret: os.Getenv("OIDC_CLIENT_SECRET"),
		RedirectURL:  strings.TrimSuffix(public.String(), "/") + "/auth/callback",
		Scopes:       strings.Split(scopes, ","),
		Secure:       public.Scheme == "https",
	}, sealer)
	if err != nil {
		fail(err, "Cannot set up OIDC")
	}

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		fail(err, "Scheme")
	}
	if err := v1.AddToScheme(scheme); err != nil {
		fail(err, "Scheme")
	}
	kube, err := desk.NewKube(ctrl.GetConfigOrDie(), scheme)
	if err != nil {
		fail(err, "Cannot reach the API server")
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           (&desk.Server{Auth: auth, Kube: kube, PublicURL: public, UI: desk.UI(), Log: log}).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	log.Info("Starting desk", "addr", addr, "publicURL", public.String())
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fail(err, "desk stopped")
	}
}

// readKey accepts 32 raw bytes or 64 hex characters.
func readKey(path string) ([]byte, error) {
	b, err := os.ReadFile(path) // #nosec G304 -- an operator-supplied path
	if err != nil {
		return nil, err
	}
	if s := strings.TrimSpace(string(b)); len(s) == 64 {
		if k, err := hex.DecodeString(s); err == nil {
			return k, nil
		}
	}
	return b, nil
}
