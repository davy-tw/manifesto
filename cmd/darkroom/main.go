/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

// Command darkroom stores and serves media files for Prints whose Archive
// uses the pvc backend. It does not talk to the Kubernetes API.
package main

import (
	"context"
	"errors"
	"flag"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"go.pkgs.davy.tw/manifesto/internal/darkroom"
	"go.pkgs.davy.tw/manifesto/internal/signing"
)

func main() {
	var dataDir, keysDir, publicAddr, internalAddr string
	flag.StringVar(&dataDir, "data-dir", "/data", "Directory (usually a PersistentVolume) holding the files.")
	flag.StringVar(&keysDir, "keys-dir", "/etc/darkroom/keys", "Mounted Secret with the signing keys press uses.")
	flag.StringVar(&publicAddr, "public-address", ":8080", "Address for serving files and accepting uploads.")
	flag.StringVar(&internalAddr, "internal-address", ":8081", "Address for press's existence checks and deletions.")
	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	log := zap.New(zap.UseFlagOptions(&opts)).WithName("darkroom")

	var keys atomic.Pointer[[][]byte]
	load := func() error {
		k, err := signing.LoadKeys(keysDir)
		if err != nil {
			return err
		}
		keys.Store(&k)
		return nil
	}
	if err := load(); err != nil {
		log.Error(err, "Cannot load signing keys")
		os.Exit(1)
	}

	store := &darkroom.Store{Dir: dataDir, Keys: func() [][]byte { return *keys.Load() }, Log: log}
	ctx := ctrl.SetupSignalHandler()
	g, ctx := errgroup.WithContext(ctx)

	// Pick up rotated keys and clear abandoned uploads.
	g.Go(func() error {
		reload, clean := time.NewTicker(30*time.Second), time.NewTicker(time.Hour)
		defer reload.Stop()
		defer clean.Stop()
		store.CleanTemp(time.Hour)
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-reload.C:
				if err := load(); err != nil {
					log.Error(err, "Reloading signing keys failed; keeping the previous ones")
				}
			case <-clean.C:
				store.CleanTemp(time.Hour)
			}
		}
	})
	handlers := map[string]http.Handler{publicAddr: store.PublicHandler(), internalAddr: store.InternalHandler()}
	for addr, h := range handlers {
		srv := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 10 * time.Second}
		g.Go(func() error {
			log.Info("Listening", "addr", addr)
			if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		})
		g.Go(func() error {
			<-ctx.Done()
			shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			return srv.Shutdown(shutdown)
		})
	}
	if err := g.Wait(); err != nil {
		log.Error(err, "darkroom stopped")
		os.Exit(1)
	}
}
