// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
)

// the console log redaction, tested where the two modules actually meet.
//
// core/api owns a STRUCTURAL floor (userinfo, key=value, Bearer, PEM, JWT) and
// deliberately owns no vendor prefixes; the vendor catalog is single-owner in
// modules/security and the composition root injects it. Those unit-level
// properties are each covered in their own package. What only THIS package can
// answer is whether boot() actually performs the injection — and the witness has
// to be a shape the floor does not know, or the test would pass with the wire cut.
const (
	// An AWS access-key SHAPE. Not a key: the first two characters after AKIA are
	// what the catalog matches on, and nothing here was ever issued.
	wiringVendorKeyWitness = "AKIAZZZXXXCCCVVVBBBN"
	// A DSN password, which the FLOOR catches on its own. It is here to prove the
	// test's own instrument works — if this one survived, the failure would be in
	// the harness, not in the wiring.
	wiringDSNPasswordWitness = "d0nt-l00k-at-me-nunca-real"
)

func bootedEngineForLogWiring(t *testing.T) *engine {
	t.Helper()
	eng, err := boot(context.Background(), bootConfig{
		DataDir: t.TempDir(), Engine: "sqlite", DSN: ":memory:", Version: "test",
		Logger: slog.New(slog.NewTextHandler(discardWriter{}, &slog.HandlerOptions{Level: slog.LevelError})),
	})
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	t.Cleanup(func() { _ = eng.store.Close() })
	if eng.logBroker == nil {
		t.Fatal("boot did not build a log broker; the console log surface is unwired")
	}
	return eng
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// consoleRingText returns everything the console ring currently holds, as the
// JSON the /v1/console/logs/* handlers serialize — i.e. the exact bytes a
// system:admin would receive.
func consoleRingText(t *testing.T, eng *engine) string {
	t.Helper()
	entries, _ := eng.logBroker.Buffer(api.LogFilter{}, 0)
	raw, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("marshal ring: %v", err)
	}
	return string(raw)
}

// TestBootWiresTheCanonicalCredentialCatalogIntoTheConsoleLogSurface fails if the
// api.WithLogRedactor(securitymodule.RedactCredentials) injection in boot() is
// removed, because the vendor-prefixed witness is a shape core's floor does not
// own.
func TestBootWiresTheCanonicalCredentialCatalogIntoTheConsoleLogSurface(t *testing.T) {
	eng := bootedEngineForLogWiring(t)

	eng.log.Error("connector: role sync rejected",
		"err", "upstream refused credential "+wiringVendorKeyWitness+" for arn:aws:iam::role/sync")

	got := consoleRingText(t, eng)
	if strings.Contains(got, wiringVendorKeyWitness) {
		t.Errorf("the canonical catalog is NOT wired into the log broker: a vendor-shaped "+
			"credential reached the console ring verbatim:\n%s", got)
	}
	if !strings.Contains(got, "[REDACTED:aws-access-key]") {
		t.Errorf("expected the catalog's own rule name in the marker; got:\n%s", got)
	}
	// The diagnosis has to survive the redaction, or the log stops being worth
	// reading and someone turns it off.
	for _, keep := range []string{"connector: role sync rejected", "upstream refused credential", "arn:aws:iam::role/sync"} {
		if !strings.Contains(got, keep) {
			t.Errorf("redaction destroyed the diagnosis: %q is gone:\n%s", keep, got)
		}
	}
}

// TestBootedLogSurfaceRedactsTheStructuralFloorToo is the control: it passes with
// or without the injection, so a green here plus a red above localizes the break
// to the wire and not to the harness.
func TestBootedLogSurfaceRedactsTheStructuralFloorToo(t *testing.T) {
	eng := bootedEngineForLogWiring(t)

	eng.log.Error("store: connection failed",
		"err", "parse \"postgres://olivares:"+wiringDSNPasswordWitness+"@db.internal.corp:5432/olivares\": FATAL 28P01")

	got := consoleRingText(t, eng)
	if strings.Contains(got, wiringDSNPasswordWitness) {
		t.Errorf("a DSN password reached the console ring verbatim:\n%s", got)
	}
	if !strings.Contains(got, "db.internal.corp") || !strings.Contains(got, "28P01") {
		t.Errorf("the operator lost the host or the SQLSTATE — redaction became silence:\n%s", got)
	}
}
