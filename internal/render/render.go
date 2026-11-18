/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

// Package render turns Dispatch content into safe HTML. It is shared by
// press (which trial-renders to set the Rendered condition) and herald
// (which renders pages for visitors), so both always agree on the result.
package render

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// Reason values used in the Rendered condition when rendering fails.
const (
	ReasonMarkdownError = "MarkdownError"
	ReasonTemplateError = "TemplateError"
)

// MaxMessageBytes caps the error message written into a condition.
const MaxMessageBytes = 1024

// Error is a rendering failure, tagged with the condition reason it maps to.
type Error struct {
	Reason string
	Err    error
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %v", e.Reason, e.Err) }

func (e *Error) Unwrap() error { return e.Err }

// ReasonOf returns the condition reason for err: the Reason of a wrapped
// *Error, or ReasonMarkdownError for anything else.
func ReasonOf(err error) string {
	if re, ok := errors.AsType[*Error](err); ok {
		return re.Reason
	}
	return ReasonMarkdownError
}

// TruncateMessage shortens s to at most MaxMessageBytes without splitting a
// UTF-8 sequence.
func TruncateMessage(s string) string {
	if len(s) <= MaxMessageBytes {
		return s
	}
	cut := MaxMessageBytes
	// Step back to the start of a UTF-8 sequence (continuation bytes are 10xxxxxx).
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut]
}

var (
	md = goldmark.New(goldmark.WithExtensions(extension.GFM))
	// policy is the HTML allow-list applied after Markdown conversion.
	policy = bluemonday.UGCPolicy()
)

// Markdown converts Markdown source to sanitized HTML.
func Markdown(src string) (template.HTML, error) {
	var buf bytes.Buffer
	if err := md.Convert([]byte(src), &buf); err != nil {
		return "", &Error{Reason: ReasonMarkdownError, Err: err}
	}
	// #nosec G203 -- the output has been sanitized by bluemonday.
	return template.HTML(policy.SanitizeBytes(buf.Bytes())), nil
}
