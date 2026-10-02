// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"strings"
	"testing"
	"time"

	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// A data dir whose org with the demo slug the seed did not create is an ordinary
// operator mistake. The engine refuses it, and it says so with the remedy, never in
// the store's language (on 2026-08-24 it printed "UNIQUE constraint failed:
// orgs.slug (2067)"). A directory the seed DID make is not a mistake any more: a
// restart with --seed-demo starts on it (demo_seed_restart_test.go).
func TestSeedDemoOverAForeignOrgSaysWhatToDo(t *testing.T) {
	ctx := context.Background()
	st, err := coreengine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}, nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, e := sys.EnsureSystemTenant(ctx); e != nil {
			return e
		}
		_, e := sys.CreateOrg(ctx, model.Org{Name: "Operations", Slug: demoOrgSlug, Status: model.StatusActive})
		return e
	}); err != nil {
		t.Fatalf("fixture: %v", err)
	}

	_, err = seedDemoEstate(ctx, st, nil, time.Now().UTC())
	if err == nil {
		t.Fatal("--seed-demo seeded over an organization it did not create")
	}
	msg := err.Error()
	for _, want := range []string{"--seed-demo did not create", "--data-dir"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("the refusal does not say %q, so the operator is not told what to do.\n  got: %s", want, msg)
		}
	}
	if strings.Contains(msg, "constraint") {
		t.Fatalf("the refusal speaks the store's language: %s", msg)
	}
}
