/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package desk

import (
	"embed"
	"io/fs"
)

//go:embed ui
var uiFS embed.FS

// UI returns the single-page app's files.
func UI() fs.FS {
	sub, err := fs.Sub(uiFS, "ui")
	if err != nil {
		panic(err)
	}
	return sub
}
