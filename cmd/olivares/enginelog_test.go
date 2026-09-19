// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/store"
)

// Measured 2026-09-18: ONE first hour produced TWO log formats from one
// binary — Go's default handler without --quiet and a TextHandler with it — and the
// same line carried a LOCAL timestamp with no zone beside UTC values.

// oneLogLine renders a line through the engine's handler.
func oneLogLine(t *testing.T, level slog.Level, emit func(*slog.Logger)) string {
	t.Helper()
	var buf bytes.Buffer
	emit(slog.New(engineLogHandler(&buf, level)))
	return buf.String()
}

func TestTheEngineLogHasOneFormatWithAUTCTimestamp(t *testing.T) {
	t.Parallel()

	// The shape, and the zone. `time=<RFC3339 with Z>` is the whole assertion: a
	// local prefix with no zone is what sat beside UTC values in the same line.
	utc := regexp.MustCompile(`^time=\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z level=`)
	for _, tc := range []struct {
		name  string
		level slog.Level
		emit  func(*slog.Logger)
	}{
		{"info", slog.LevelInfo, func(l *slog.Logger) { l.Info("boot: a posture", "key", "value") }},
		{"warn", slog.LevelWarn, func(l *slog.Logger) { l.Warn("boot: a warning") }},
		{"error", slog.LevelError, func(l *slog.Logger) { l.Error("boot: a failure") }},
	} {
		line := oneLogLine(t, tc.level, tc.emit)
		if !utc.MatchString(line) {
			t.Fatalf("%s line is not the one format with a UTC timestamp: %q", tc.name, line)
		}
		if strings.Contains(line, "+0") || strings.Contains(line, "+1") {
			t.Fatalf("%s line carries a local zone offset: %q", tc.name, line)
		}
	}

	// And the QUIET form is the same format at another level, not another format.
	quiet := oneLogLine(t, slog.LevelError, func(l *slog.Logger) {
		l.Info("boot: held back")
		l.Error("boot: the only line --quiet shows")
	})
	if strings.Contains(quiet, "held back") {
		t.Fatalf("--quiet's level did not hold back an INFO line: %q", quiet)
	}
	if !utc.MatchString(quiet) {
		t.Fatalf("--quiet changed the FORMAT, which is the defect: %q", quiet)
	}
}

func TestEngineLogLevelHonoursTheDocumentedVariable(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		raw   string
		want  slog.Level
		quiet bool
	}{
		{"", slog.LevelInfo, false},
		{"debug", slog.LevelDebug, false},
		{"info", slog.LevelInfo, false},
		{"WARN", slog.LevelWarn, false},
		{"error", slog.LevelError, false},
		{"nonsense", slog.LevelInfo, false},
		// --quiet is a per-invocation instruction and wins over the deployment-wide
		// variable, which is the only order that makes the flag mean what it says.
		{"debug", slog.LevelError, true},
	} {
		got, source := engineLogLevel(func(string) string { return tc.raw }, tc.quiet)
		if got != tc.want {
			t.Fatalf("OLIVARES_LOG_LEVEL=%q quiet=%v -> %v, want %v", tc.raw, tc.quiet, got, tc.want)
		}
		if tc.quiet && source != "--quiet" {
			t.Fatalf("the source of a quiet level reads as %q", source)
		}
	}
}

// TestAnOrdinaryFirstBootIsNotAnErrorOnTheCommunicationStore pins what an
// ordinary first boot says: a clean `quickstart` logged `sessions: communication
// store proof incomplete` at ERROR, with three blockers that all said the same
// thing — nobody has run `olivares db activate-directory-writer` yet. A posture
// waiting for a ceremony is a posture.
func TestAnOrdinaryFirstBootIsNotAnErrorOnTheCommunicationStore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfg := store.Config{Engine: store.EngineSQLite, DSN: filepath.Join(t.TempDir(), "firstboot.db")}

	staged := openStoreProofEstate(t, cfg, true)
	witness := staged.proofWitness(func() bool { return true })
	if err := witness.ReconcileAndVerify(ctx); err == nil {
		t.Fatal("a staged writer passed the proof; this test measures the staged case")
	}
	proof := witness.Proof()
	if proof.Ready {
		t.Fatalf("proof = %+v", proof)
	}
	if !proof.AwaitingActivation {
		t.Fatalf("an ordinary first boot is not classified as awaiting activation: %+v", proof)
	}
	// Every blocker is one the ceremony clears — that is what the classification
	// claims, so it is checked rather than trusted.
	for _, b := range proof.Blockers {
		switch {
		case strings.Contains(b, "writer_control_not_enforced"),
			strings.Contains(b, "directory_epoch_coverage_incomplete"),
			strings.Contains(b, "expected_generation_below_activation"):
		default:
			t.Fatalf("a blocker outside the activation ceremony was folded into it: %q", b)
		}
	}

	// And the other direction: a proof with ANY other blocker is NOT awaiting
	// activation, so it keeps the ERROR it deserves. The store whose optional
	// status capability is hidden is exactly that case.
	hidden := newCommunicationStoreProofWitness(
		hiddenStatusStore{staged.st}, nil, staged.sm, staged.listOrgs, func() bool { return true }, time.Now)
	if err := hidden.ReconcileAndVerify(ctx); err == nil {
		t.Fatal("a store with no directory status passed the proof")
	}
	if hidden.Proof().AwaitingActivation {
		t.Fatalf("an unsupported directory status was classified as awaiting activation: %+v", hidden.Proof())
	}
}

// TestAwaitingActivationNeedsEVERYBlockerToBeOne is the discriminating half of the
// classification, and it exists because the integration case above cannot produce a
// MIXED proof: a store with no directory status never reaches the activation checks,
// so it counts zero of them. The mixed case is the one that decides whether an
// operator who runs the ceremony is left with a silent, still-blocked store.
func TestAwaitingActivationNeedsEVERYBlockerToBeOne(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		blockers, pending int
		want              bool
		why               string
	}{
		{3, 3, true, "an ordinary first boot: every blocker is the ceremony's"},
		{1, 1, true, "one staged control and nothing else"},
		{3, 2, false, "two of the ceremony's and ONE other — a ceremony would not clear it"},
		{1, 0, false, "a blocker that is not the ceremony's at all"},
		{0, 0, false, "nothing blocks: the proof is ready, not awaiting anything"},
	} {
		if got := awaitingActivationOnly(tc.blockers, tc.pending); got != tc.want {
			t.Fatalf("blockers=%d pending=%d -> %v, want %v (%s)",
				tc.blockers, tc.pending, got, tc.want, tc.why)
		}
	}
}
