// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/store"
)

// A Community operator who sets OLIVARES_DECOY_CREDENTIALS_CONFIG gets a boot line that
// names the key and the edition. Nothing is planted, and boot is not refused.
func TestCommunityDecoyKeyNamesItselfAtBoot(t *testing.T) {
	if thisEdition.sessionDecoys == nil {
		t.Fatal("the community edition has no sessionDecoys port: a set OLIVARES_DECOY_CREDENTIALS_CONFIG is silent at boot")
	}
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	env := map[string]string{"OLIVARES_DECOY_CREDENTIALS_CONFIG": "/etc/olivares/decoys.json"}
	if engine := thisEdition.sessionDecoys(getenvFrom(env), sessionDecoyDeps{}, log); engine != nil {
		t.Fatalf("community binary planted decoys: %T", engine)
	}
	out := logs.String()
	if !strings.Contains(out, "OLIVARES_DECOY_CREDENTIALS_CONFIG") || !strings.Contains(out, "community") {
		t.Fatalf("boot log must name the key and the community edition, got %q", out)
	}
}

// An unset key keeps every Community boot quiet: no line and no engine.
func TestCommunityDecoyKeyUnsetIsSilent(t *testing.T) {
	if thisEdition.sessionDecoys == nil {
		t.Fatal("the community edition has no sessionDecoys port")
	}
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	if engine := thisEdition.sessionDecoys(getenvFrom(nil), sessionDecoyDeps{}, log); engine != nil {
		t.Fatalf("community binary planted decoys with no key: %T", engine)
	}
	if logs.Len() != 0 {
		t.Fatalf("an unset key must not log, got %q", logs.String())
	}
}

// The composition root reaches the decoy seam on a real boot: the engine boots with the
// key set, and its boot log names the key and the edition.
func TestCommunityBootNamesASetDecoyKey(t *testing.T) {
	t.Setenv("OLIVARES_DECOY_CREDENTIALS_CONFIG", "/etc/olivares/decoys.json")
	cfg := store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}
	sink := &syncBuffer{}
	eng, err := boot(context.Background(), bootConfigOn(t, cfg, slog.New(slog.NewTextHandler(sink, nil))))
	if err != nil {
		t.Fatalf("a Community boot with the key set must still boot: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	if out := sink.String(); !strings.Contains(out, "OLIVARES_DECOY_CREDENTIALS_CONFIG") || !strings.Contains(out, "edition=community") {
		t.Fatalf("boot log must name the key and edition=community, got:\n%s", out)
	}
}
