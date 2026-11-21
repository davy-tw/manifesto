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

	"strings"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
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

// PrintScheme prefixes references to Prints in content: print:<name>.
const PrintScheme = "print:"

// PrintResolver returns the public URL of a Print by name, or "" if it does
// not exist or is not Ready.
type PrintResolver func(name string) string

var printResolverKey = parser.NewContextKey()

// printLinks rewrites print:<name> link and image destinations with the
// resolver passed in the parser context. An unresolved image is removed and
// an unresolved link is replaced by its text, so pages never show a broken
// image or link while a Print is missing or still uploading.
type printLinks struct{}

func (printLinks) Transform(doc *ast.Document, _ text.Reader, pc parser.Context) {
	resolve, _ := pc.Get(printResolverKey).(PrintResolver)
	var unresolved []ast.Node
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		var dest *[]byte
		switch v := n.(type) {
		case *ast.Image:
			dest = &v.Destination
		case *ast.Link:
			dest = &v.Destination
		default:
			return ast.WalkContinue, nil
		}
		if name, ok := strings.CutPrefix(string(*dest), PrintScheme); ok {
			u := ""
			if resolve != nil {
				u = resolve(name)
			}
			if u == "" {
				unresolved = append(unresolved, n)
			}
			*dest = []byte(u)
		}
		return ast.WalkContinue, nil
	})
	for _, n := range unresolved {
		parent := n.Parent()
		if parent == nil {
			continue
		}
		if _, isLink := n.(*ast.Link); isLink {
			for c := n.FirstChild(); c != nil; {
				next := c.NextSibling()
				parent.InsertBefore(parent, n, c)
				c = next
			}
		}
		parent.RemoveChild(parent, n)
	}
}

var (
	md = goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithParserOptions(parser.WithASTTransformers(util.Prioritized(printLinks{}, 100))),
	)
	// policy is the HTML allow-list applied after Markdown conversion.
	policy = bluemonday.UGCPolicy()
)

// Markdown converts Markdown source to sanitized HTML. print:<name>
// references are resolved with resolve, which may be nil.
func Markdown(src string, resolve PrintResolver) (template.HTML, error) {
	var buf bytes.Buffer
	pc := parser.NewContext()
	pc.Set(printResolverKey, resolve)
	if err := md.Convert([]byte(src), &buf, parser.WithContext(pc)); err != nil {
		return "", &Error{Reason: ReasonMarkdownError, Err: err}
	}
	// #nosec G203 -- the output has been sanitized by bluemonday.
	return template.HTML(policy.SanitizeBytes(buf.Bytes())), nil
}
