/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

// Command mfctl is Manifesto's command-line tool.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"go.pkgs.davy.tw/manifesto/internal/mfctl"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := mfctl.NewCommand(&mfctl.Env{Out: os.Stdout, Err: os.Stderr}).ExecuteContext(ctx); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "error:", err)
		stop()
		os.Exit(1)
	}
}
