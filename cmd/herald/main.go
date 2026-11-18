/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

// Command herald serves Gazettes to visitors. It only reads from the
// Kubernetes API (get/list/watch) and never writes any resource.
package main

import (
	"context"
	"errors"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"go.pkgs.davy.tw/manifesto/internal/herald"
)

func main() {
	var addr string
	flag.StringVar(&addr, "bind-address", ":8080", "The address the visitor site listens on.")
	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	log := zap.New(zap.UseFlagOptions(&opts)).WithName("herald")

	srv := &http.Server{
		Addr:              addr,
		Handler:           herald.NewHandler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Info("starting herald", "addr", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error(err, "herald stopped")
		os.Exit(1)
	}
}
