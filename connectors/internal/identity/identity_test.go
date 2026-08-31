// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package identity_test

import (
	"testing"

	"github.com/olivaresai/olivares/connectors/internal/identity"
	"github.com/olivaresai/olivares/sdk/model"
)

func TestOriginKindIsIdentity(t *testing.T) {
	if identity.OriginKind != "identity" {
		t.Fatalf("OriginKind = %q, want %q (store audit attributes to a non-human identity, never a resolved agent)", identity.OriginKind, "identity")
	}
}

func TestParseSharedAccounts(t *testing.T) {
	cases := []struct {
		name string
		csv  string
		in   string
		want bool
	}{
		{"plain match", "app_rw,etl_pool", "app_rw", true},
		{"case insensitive", "App_RW", "app_rw", true},
		{"whitespace trimmed", "  etl_pool  , app_rw ", "etl_pool", true},
		{"non-member", "app_rw", "reporting", false},
		{"empty config has no members", "", "app_rw", false},
		{"blank entries ignored", ",, ,", "app_rw", false},
		{"user@host form", "svc@10.0.0.5", "SVC@10.0.0.5", true},
		{"role arn", "arn:aws:iam::123:role/shared", "arn:aws:iam::123:role/shared", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := identity.ParseSharedAccounts(c.csv)
			if got := s.Has(c.in); got != c.want {
				t.Errorf("ParseSharedAccounts(%q).Has(%q) = %v, want %v", c.csv, c.in, got, c.want)
			}
		})
	}
}

func TestHasBlankRefNeverMember(t *testing.T) {
	s := identity.ParseSharedAccounts("app_rw")
	if s.Has("") || s.Has("   ") {
		t.Error("a blank ref must never be a shared-account member")
	}
}

func TestLen(t *testing.T) {
	if got := identity.ParseSharedAccounts("a, b ,a,,c").Len(); got != 3 {
		t.Errorf("Len() = %d, want 3 (dedup + blank removal)", got)
	}
}

func TestConfidenceFor(t *testing.T) {
	shared := identity.ParseSharedAccounts("pgbouncer, svc@10.0.0.5")
	cases := []struct {
		name string
		refs []string
		want model.Confidence
	}{
		{"none shared -> attributed", []string{"reporting", "claude-agent-7"}, model.ConfidenceAttributed},
		{"one ref shared -> approximate", []string{"reporting", "pgbouncer"}, model.ConfidenceApproximate},
		{"single shared ref -> approximate", []string{"svc@10.0.0.5"}, model.ConfidenceApproximate},
		{"empty refs -> attributed", nil, model.ConfidenceAttributed},
		{"blank ref -> attributed", []string{""}, model.ConfidenceAttributed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shared.ConfidenceFor(c.refs...); got != c.want {
				t.Errorf("ConfidenceFor(%v) = %q, want %q", c.refs, got, c.want)
			}
		})
	}
}

func TestEmptySetIsAttributed(t *testing.T) {
	var empty identity.SharedSet // zero value
	if got := empty.ConfidenceFor("anyone"); got != model.ConfidenceAttributed {
		t.Errorf("zero-value SharedSet.ConfidenceFor = %q, want attributed", got)
	}
	if empty.Has("anyone") {
		t.Error("zero-value SharedSet must have no members")
	}
}
