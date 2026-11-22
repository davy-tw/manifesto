/*
Copyright 2026.

SPDX-License-Identifier: MIT
*/

// Package domain decides which Gazette serves a domain. press uses it to set
// DomainConflict and herald uses it to route requests, so both always agree.
package domain

import (
	"strings"

	v1 "go.pkgs.davy.tw/manifesto/api/v1alpha1"
)

// Older reports whether a claimed its domain before b: the earlier
// creationTimestamp wins, ties are broken by namespace and then name so the
// result never depends on list order.
func Older(a, b *v1.Gazette) bool {
	if !a.CreationTimestamp.Equal(&b.CreationTimestamp) {
		return a.CreationTimestamp.Before(&b.CreationTimestamp)
	}
	if a.Namespace != b.Namespace {
		return a.Namespace < b.Namespace
	}
	return a.Name < b.Name
}

// Owners maps each domain to the Gazette that serves it. Gazettes being
// deleted do not claim their domain.
func Owners(gazettes []v1.Gazette) map[string]*v1.Gazette {
	owners := map[string]*v1.Gazette{}
	for i := range gazettes {
		g := &gazettes[i]
		if g.DeletionTimestamp != nil {
			continue
		}
		d := Normalize(g.Spec.Domain)
		if cur, ok := owners[d]; !ok || Older(g, cur) {
			owners[d] = g
		}
	}
	return owners
}

// Normalize turns a Host header or domain into the form used as a map key:
// lower case, without port or trailing dot.
func Normalize(host string) string {
	host = strings.ToLower(host)
	if i := strings.LastIndexByte(host, ':'); i >= 0 && !strings.Contains(host[i:], "]") {
		host = host[:i]
	}
	return strings.TrimSuffix(host, ".")
}

// Set is a set of normalized domains.
type Set map[string]struct{}

// NewSet returns the set of the given domains, normalized; empty ones are
// skipped.
func NewSet(domains []string) Set {
	s := Set{}
	for _, d := range domains {
		if d = Normalize(strings.TrimSpace(d)); d != "" {
			s[d] = struct{}{}
		}
	}
	return s
}

// Has reports whether domain, normalized, is in s.
func (s Set) Has(domain string) bool {
	_, ok := s[Normalize(domain)]
	return ok
}
