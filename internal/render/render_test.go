/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

package render

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestMarkdown(t *testing.T) {
	tests := []struct {
		name        string
		src         string
		contains    []string
		notContains []string
	}{
		{
			name:     "heading and paragraph",
			src:      "# Hello\n\nworld",
			contains: []string{"<h1>Hello</h1>", "<p>world</p>"},
		},
		{
			name:     "gfm table",
			src:      "| a | b |\n|---|---|\n| 1 | 2 |",
			contains: []string{"<table>", "<td>1</td>"},
		},
		{
			name:        "raw script is dropped",
			src:         "hi <script>alert(1)</script>",
			notContains: []string{"<script", "alert(1)</script>"},
		},
		{
			name:        "javascript link is neutralized",
			src:         "[x](javascript:alert(1))",
			notContains: []string{"javascript:"},
		},
		{
			name:        "event handler attribute is stripped",
			src:         `<img src="a.png" onerror="alert(1)">`,
			notContains: []string{"onerror"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Markdown(tt.src)
			if err != nil {
				t.Fatalf("Markdown() error = %v", err)
			}
			for _, s := range tt.contains {
				if !strings.Contains(string(got), s) {
					t.Errorf("output %q does not contain %q", got, s)
				}
			}
			for _, s := range tt.notContains {
				if strings.Contains(string(got), s) {
					t.Errorf("output %q must not contain %q", got, s)
				}
			}
		})
	}
}

func TestReasonOf(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"template error", &Error{Reason: ReasonTemplateError, Err: errors.New("x")}, ReasonTemplateError},
		{"wrapped markdown error", errors.Join(errors.New("ctx"), &Error{Reason: ReasonMarkdownError, Err: errors.New("x")}), ReasonMarkdownError},
		{"plain error", errors.New("x"), ReasonMarkdownError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ReasonOf(tt.err); got != tt.want {
				t.Errorf("ReasonOf() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTruncateMessage(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"short", "hello"},
		{"exact", strings.Repeat("a", MaxMessageBytes)},
		{"long ascii", strings.Repeat("a", MaxMessageBytes+10)},
		// 3-byte runes force the cut to land mid-sequence.
		{"long multibyte", strings.Repeat("中", MaxMessageBytes)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TruncateMessage(tt.in)
			if len(got) > MaxMessageBytes {
				t.Errorf("len = %d, want <= %d", len(got), MaxMessageBytes)
			}
			if !utf8.ValidString(got) {
				t.Errorf("result is not valid UTF-8")
			}
			if !strings.HasPrefix(tt.in, got) {
				t.Errorf("result is not a prefix of the input")
			}
			if len(tt.in) <= MaxMessageBytes && got != tt.in {
				t.Errorf("short input was changed")
			}
		})
	}
}
