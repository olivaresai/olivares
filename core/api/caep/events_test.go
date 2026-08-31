// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package caep_test

import (
	"testing"

	"github.com/olivaresai/olivares/core/api/caep"
)

func TestActionForCAEPEvent(t *testing.T) {
	cases := []struct {
		uri  string
		want caep.CAEPEventAction
	}{
		{caep.EventSessionRevoked, caep.ActionSessionRevoke},
		{caep.EventTokenClaimsChange, caep.ActionTokenRevoke},
		{caep.EventCredentialChange, caep.ActionCredentialRevoke},
		{caep.EventDeviceComplianceChange, caep.ActionDeviceNonCompliant},
		{caep.EventAccountDisabled, caep.ActionAccountDisable},
		{caep.EventCredentialCompromise, caep.ActionCredentialCompromise},
		{"https://unknown.example.com/event", caep.ActionIgnore},
	}
	for _, c := range cases {
		if got := caep.ActionForCAEPEvent(c.uri); got != c.want {
			t.Errorf("ActionForCAEPEvent(%q) = %q, want %q", c.uri, got, c.want)
		}
	}
}

func TestSubjectIdentifierFormats(t *testing.T) {
	email := caep.SubjectIdentifier{Format: "email", Email: "user@example.com"}
	if email.Email != "user@example.com" {
		t.Fatalf("email = %q", email.Email)
	}
	issSub := caep.SubjectIdentifier{Format: "iss_sub", Issuer: "https://idp.test", Subject: "user123"}
	if issSub.Issuer != "https://idp.test" || issSub.Subject != "user123" {
		t.Fatalf("iss_sub = %+v", issSub)
	}
}

func TestResolveSubjectEmail(t *testing.T) {
	s := caep.SubjectIdentifier{Format: "email", Email: "user@example.com"}
	if got := s.ResolveSubjectEmail(); got != "user@example.com" {
		t.Fatalf("got %q", got)
	}
	wrong := caep.SubjectIdentifier{Format: "iss_sub", Email: "user@example.com"}
	if got := wrong.ResolveSubjectEmail(); got != "" {
		t.Fatalf("wrong format should return empty, got %q", got)
	}
}

func TestResolveSubjectIssSub(t *testing.T) {
	s := caep.SubjectIdentifier{Format: "iss_sub", Issuer: "https://idp.test", Subject: "user123"}
	iss, sub := s.ResolveSubjectIssSub()
	if iss != "https://idp.test" || sub != "user123" {
		t.Fatalf("got (%q, %q)", iss, sub)
	}
	wrong := caep.SubjectIdentifier{Format: "email", Issuer: "https://idp.test", Subject: "user123"}
	iss, sub = wrong.ResolveSubjectIssSub()
	if iss != "" || sub != "" {
		t.Fatalf("wrong format should return empty, got (%q, %q)", iss, sub)
	}
}
