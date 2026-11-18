/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package herald

import (
	"context"
	"fmt"

	toolscache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
)

// Watch keeps srv up to date: every informer event schedules a rebuild in
// the background, and bursts of events are coalesced into one rebuild. It
// returns when ctx is done. The cache must be started separately.
func Watch(ctx context.Context, c cache.Cache, b *Builder, srv *Server) error {
	trigger := make(chan struct{}, 1)
	notify := func() {
		select {
		case trigger <- struct{}{}:
		default:
		}
	}
	handler := toolscache.ResourceEventHandlerFuncs{
		AddFunc:    func(any) { notify() },
		UpdateFunc: func(any, any) { notify() },
		DeleteFunc: func(any) { notify() },
	}
	for _, obj := range []client.Object{&v1.Gazette{}, &v1.Dispatch{}} {
		inf, err := c.GetInformer(ctx, obj)
		if err != nil {
			return fmt.Errorf("getting informer for %T: %w", obj, err)
		}
		if _, err := inf.AddEventHandler(handler); err != nil {
			return err
		}
	}
	if !c.WaitForCacheSync(ctx) {
		return ctx.Err()
	}
	notify()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-trigger:
			var gazettes v1.GazetteList
			var dispatches v1.DispatchList
			if err := c.List(ctx, &gazettes); err != nil {
				b.Log.Error(err, "Failed to list Gazettes")
				continue
			}
			if err := c.List(ctx, &dispatches); err != nil {
				b.Log.Error(err, "Failed to list Dispatches")
				continue
			}
			srv.Store(b.Build(gazettes.Items, dispatches.Items, srv.Snapshot()))
		}
	}
}
