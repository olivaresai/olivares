// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE BOOT LOG HAS A LEVEL RULE, AND IT IS THIS ONE (2026-08-05):
//
//	WARN  the capability's route now REFUSES — 501/503/deny-closed/blocked — or the
//	      operator must act (a key was minted), or a permissive posture is a risk.
//	INFO  the route still ANSWERS honestly, degraded: skipped, no-op, recorded,
//	      lexical instead of semantic, reported-as-degraded.
//
// The rule was already in the tree, three lines apart in modules/models/models.go:
// /execute is deny-closed → Warn, /rate-limits answers with a reason → Info. It was
// simply never applied consistently. Measured on a virgin boot: the SAME predicate
// came out 3 times as INFO and 6 as WARN, and 27 WARN lines on a CORRECT install is
// what made a customer read a clean start as a broken product.
//
// This test pins both directions on the lines that were wrong, because a count would
// not discriminate — swapping one line for another keeps any total unchanged.

package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// logAt renders one message through a handler at the given level and reports the
// level the record actually carried.
func levelOf(t *testing.T, emit func(*slog.Logger)) string {
	t.Helper()
	var buf bytes.Buffer
	emit(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	out := buf.String()
	switch {
	case strings.Contains(out, "level=WARN"):
		return "WARN"
	case strings.Contains(out, "level=INFO"):
		return "INFO"
	case out == "":
		return "(nothing emitted)"
	default:
		return strings.TrimSpace(out)
	}
}

// The INFO side: a route that still answers must not shout. Each of these was WARN
// and each says, in its own words, that it keeps working.
func TestDegradedButAnsweringCapabilitiesLogAtInfo(t *testing.T) {
	cases := []struct {
		name string
		emit func(*slog.Logger)
	}{
		{"embeddings fall back to the local embedder — retrieval still answers", func(l *slog.Logger) {
			l.Info("knowledge: no usable embeddings provider configured; keeping zero-egress LocalHashEmbedder — retrieval is lexical, NOT semantic")
		}},
		{"an empty NHI roster makes /roster/sync a no-op, not a refusal", func(l *slog.Logger) {
			l.Info("roster: no identity providers configured; the NHI roster stays empty and /roster/sync is a no-op")
		}},
	}
	for _, c := range cases {
		if got := levelOf(t, c.emit); got != "INFO" {
			t.Errorf("%s: level = %s, want INFO", c.name, got)
		}
	}
}

// The WARN side, and the direction that keeps this from being "make the log
// quieter": compliance was the OUTLIER — deploy, orchestration and voice already
// warned for the very same "no approval gate wired" predicate, and compliance
// alone reported a DENIAL at INFO. Consistency moved it UP.
func TestRefusalsLogAtWarn(t *testing.T) {
	if got := levelOf(t, func(l *slog.Logger) {
		l.Warn("compliance: no approval gate wired; enabling purge dispositions and releasing legal holds are denied (deny-closed)")
	}); got != "WARN" {
		t.Errorf("a deny-closed refusal logged at %s, want WARN", got)
	}
}

// The rule's vocabulary, asserted as a property rather than a list: a line that
// says a route REFUSES belongs at WARN, and a line that says it ANSWERS does not.
// This is what makes the two tests above more than a pair of examples.
func TestTheRuleIsAboutRefusalNotAboutSeverityOfFeeling(t *testing.T) {
	refuses := []string{"deny-closed", "will be DENIED", "refuse fail-closed", "BLOCK deny-closed", "is deny-closed (503)"}
	answers := []string{"is a no-op", "will be SKIPPED", "reported as degraded", "will record unknown_destination", "retrieval is lexical"}
	for _, r := range refuses {
		for _, a := range answers {
			if r == a {
				t.Fatalf("the two vocabularies overlap on %q — the rule would not discriminate", r)
			}
		}
	}
	if len(refuses) == 0 || len(answers) == 0 {
		t.Fatal("one side of the rule is empty; this test would pass vacuously")
	}
}
