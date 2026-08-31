// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package siemwire

import (
	"reflect"
	"testing"
)

// The catalog is the single source every surface derives from, so its own test
// is the one place the token lists are spelled out literally ON PURPOSE: an
// accidental edit to a set must fail loudly here, not surface as a silent
// contract change on an API, a connector or the console.

func allSurfaceSets() []FormatSet {
	return []FormatSet{
		LedgerExportFormats(),
		EventingSinkFormats(),
		NotificationConnectorFormats(),
		SyslogConnectorFormats(),
	}
}

func TestEverySurfaceSetIsPinnedExactly(t *testing.T) {
	want := map[string]struct {
		tokens []FormatToken
		def    FormatToken
	}{
		"ledger export": {
			tokens: []FormatToken{
				TokenCEF, TokenLEEF, TokenSyslog, TokenOTLP, TokenOTLPEnvelope,
				TokenOTLPLogRecord, TokenOCSF,
			},
			def: TokenCEF,
		},
		"eventing sink": {
			tokens: []FormatToken{
				TokenOCSF, TokenCEF, TokenLEEF, TokenSyslog, TokenOTLP,
				TokenOTLPEnvelope, TokenJSON,
			},
			def: TokenOCSF,
		},
		"notification connector": {
			tokens: []FormatToken{
				TokenJSON, TokenCEF, TokenLEEF, TokenSyslog, TokenOTLP,
				TokenOTLPEnvelope, TokenOCSF, TokenASIM,
			},
			def: TokenJSON,
		},
		"syslog connector": {
			tokens: []FormatToken{TokenSyslog, TokenCEF, TokenLEEF},
			def:    TokenSyslog,
		},
	}
	seen := map[string]bool{}
	for _, s := range allSurfaceSets() {
		w, ok := want[s.Surface()]
		if !ok {
			t.Fatalf("surface %q is not pinned in this test — pin it", s.Surface())
		}
		if seen[s.Surface()] {
			t.Fatalf("surface %q appears twice", s.Surface())
		}
		seen[s.Surface()] = true
		if got := s.Tokens(); !reflect.DeepEqual(got, w.tokens) {
			t.Errorf("%s tokens = %v, want %v (order matters)", s.Surface(), got, w.tokens)
		}
		if s.Default() != w.def {
			t.Errorf("%s default = %q, want %q", s.Surface(), s.Default(), w.def)
		}
	}
	if len(seen) != len(want) {
		t.Errorf("pinned %d surfaces, catalog exposes %d", len(want), len(seen))
	}
}

func TestEverySetTokenIsACatalogConstantWithoutDuplicates(t *testing.T) {
	vocabulary := map[FormatToken]bool{
		TokenJSON: true, TokenCEF: true, TokenLEEF: true, TokenSyslog: true,
		TokenOTLP: true, TokenOTLPEnvelope: true, TokenOTLPLogRecord: true,
		TokenOCSF: true, TokenASIM: true,
	}
	for _, s := range allSurfaceSets() {
		seen := map[FormatToken]bool{}
		for _, tok := range s.Tokens() {
			if !vocabulary[tok] {
				t.Errorf("%s carries %q, which is not a catalog constant", s.Surface(), tok)
			}
			if seen[tok] {
				t.Errorf("%s lists %q twice", s.Surface(), tok)
			}
			seen[tok] = true
		}
	}
}

func TestEveryDefaultAndAliasTargetIsAMember(t *testing.T) {
	for _, s := range allSurfaceSets() {
		if !s.Valid(s.Default()) {
			t.Errorf("%s default %q is not a member of the set", s.Surface(), s.Default())
		}
		// Wherever the alias spelling is accepted, its canonical target must be
		// accepted too — otherwise a valid submission resolves to an encoder key
		// the surface never advertises.
		for _, tok := range s.Tokens() {
			if c := Canonical(tok); c != tok && !s.Valid(c) {
				t.Errorf("%s accepts %q but not its canonical form %q", s.Surface(), tok, c)
			}
		}
	}
}

func TestCanonicalResolvesOnlyTheOneAlias(t *testing.T) {
	if got := Canonical(TokenOTLPEnvelope); got != TokenOTLP {
		t.Fatalf("Canonical(otlp_envelope) = %q, want %q", got, TokenOTLP)
	}
	// Every other token — members, unknown spellings, empty — passes through
	// unchanged: Canonical never validates, never defaults, never normalizes.
	for _, tok := range []FormatToken{
		TokenJSON, TokenCEF, TokenLEEF, TokenSyslog, TokenOTLP,
		TokenOTLPLogRecord, TokenOCSF, TokenASIM,
		"", "OTLP_ENVELOPE", " otlp_envelope", "weird", "otlp ",
	} {
		if got := Canonical(tok); got != tok {
			t.Errorf("Canonical(%q) = %q, want it unchanged", tok, got)
		}
	}
}

func TestValidUsesTheSubmittedSpellingOnly(t *testing.T) {
	syslogSet := SyslogConnectorFormats()
	// The syslog connector does not accept the OTLP family at all; the alias
	// must not sneak in via canonicalization, case-folding or trimming.
	for _, tok := range []FormatToken{
		TokenOTLP, TokenOTLPEnvelope, TokenOTLPLogRecord, TokenJSON, TokenOCSF,
		TokenASIM, "CEF", " cef", "cef ", "", "weird",
	} {
		if syslogSet.Valid(tok) {
			t.Errorf("syslog connector set accepted %q", tok)
		}
	}
	eventing := EventingSinkFormats()
	if eventing.Valid(TokenOTLPLogRecord) {
		t.Error("eventing sink set accepted otlp_log_record — the bare projection is not postable and is excluded by design")
	}
}

func TestListRendersTheOperatorChoiceList(t *testing.T) {
	want := map[string]string{
		"ledger export":          "cef|leef|syslog|otlp|otlp_envelope|otlp_log_record|ocsf",
		"eventing sink":          "ocsf|cef|leef|syslog|otlp|otlp_envelope|json",
		"notification connector": "json|cef|leef|syslog|otlp|otlp_envelope|ocsf|asim",
		"syslog connector":       "syslog|cef|leef",
	}
	for _, s := range allSurfaceSets() {
		if got := s.List(); got != want[s.Surface()] {
			t.Errorf("%s List() = %q, want %q", s.Surface(), got, want[s.Surface()])
		}
	}
}

func TestTokensReturnsADefensiveCopy(t *testing.T) {
	s := LedgerExportFormats()
	got := s.Tokens()
	got[0] = "mutated"
	if again := s.Tokens(); again[0] != TokenCEF {
		t.Fatalf("mutating a returned slice reached the catalog: %v", again)
	}
}
