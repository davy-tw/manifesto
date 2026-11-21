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
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
	"go.pkgs.davy.tw/manifesto/internal/herald"
	"go.pkgs.davy.tw/manifesto/internal/render"
)

func main() {
	var addr string
	flag.StringVar(&addr, "bind-address", ":8080", "The address the visitor site listens on.")
	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	log := zap.New(zap.UseFlagOptions(&opts)).WithName("herald")
	ctrl.SetLogger(log)

	scheme := runtime.NewScheme()
	if err := v1.AddToScheme(scheme); err != nil {
		log.Error(err, "Failed to build scheme")
		os.Exit(1)
	}
	c, err := cache.New(ctrl.GetConfigOrDie(), cache.Options{Scheme: scheme})
	if err != nil {
		log.Error(err, "Failed to create cache")
		os.Exit(1)
	}

	ctx := ctrl.SetupSignalHandler()
	srv := &herald.Server{}
	builder := &herald.Builder{Theme: render.DefaultTheme(), Log: log}

	go func() {
		if err := c.Start(ctx); err != nil {
			log.Error(err, "Cache stopped")
			os.Exit(1)
		}
	}()
	go func() {
		if err := herald.Watch(ctx, c, builder, srv); err != nil {
			log.Error(err, "Watch stopped")
			os.Exit(1)
		}
	}()

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	}()

	log.Info("Starting herald", "addr", addr)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error(err, "herald stopped")
		os.Exit(1)
	}
}
