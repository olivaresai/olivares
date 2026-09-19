// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/ed25519"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"sort"
	"testing"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
)

// schema_census_test.go measures the APPEND-ONLY census of the EXACT product boot
// callback, and it exists because nothing else in this repository does.
//
// WHY NOT `migrate manifest`. The existing recorder registers the module set and stops
// there: it omits the two calls boot.go makes after rt.RegisterSchema, so its output is
// not the callback's output. Those two calls create three MUTABLE relations, which add
// nothing to the append-only census — and that is a result, not an assumption, so it is
// measured here rather than argued. The three tables are `mcp_tool_pins`,
// `governance_cb_rule` and `governance_cb_state`.
//
// WHAT THE CENSUS IS FOR. The guard manifest's identity, and therefore every upgrade edge
// between two builds, is derived from the sorted append-only relations of the CLOSED
// registry. Comparing two releases' upgrade compatibility means comparing these lists;
// comparing their version numbers, module lists or migration counts does not.

// productBootCallbackCensus runs the exact callback cmd/olivares hands coreengine.Open:
// rt.RegisterSchema over the built module set, then registerToolPinSchema, then
// registerCircuitBreakerSchema, in that order.
func productBootCallbackCensus(t *testing.T) (appendOnly, mutable []string) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	signer, err := audit.NewSigner(ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)))
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	set, err := buildModules(signer,
		ed25519.NewKeyFromSeed(fixedSeed(1)), ed25519.NewKeyFromSeed(fixedSeed(2)),
		nil, nil, sourcesConfig{}, EditionConfig{}, t.TempDir(), log)
	if err != nil {
		t.Fatalf("build the module set: %v", err)
	}
	rt := runtime.New(runtime.Options{Logger: log})
	for _, m := range set.all {
		sm, ok := m.(sdk.Module)
		if !ok {
			t.Fatalf("module %q does not satisfy sdk.Module", m.APINamespace())
		}
		if aerr := rt.AddModule(sm, sdk.Config{}); aerr != nil {
			t.Fatalf("register module %q: %v", m.APINamespace(), aerr)
		}
	}
	reg := &schemaManifestRegistry{}
	if rerr := rt.RegisterSchema(reg); rerr != nil {
		t.Fatalf("rt.RegisterSchema: %v", rerr)
	}
	if terr := registerToolPinSchema(reg); terr != nil {
		t.Fatalf("registerToolPinSchema: %v", terr)
	}
	if cerr := registerCircuitBreakerSchema(reg); cerr != nil {
		t.Fatalf("registerCircuitBreakerSchema: %v", cerr)
	}
	var _ store.ExtensionRegistry = reg
	for _, e := range reg.entities {
		if e.AppendOnly {
			appendOnly = append(appendOnly, e.Table)
		} else {
			mutable = append(mutable, e.Table)
		}
	}
	sort.Strings(appendOnly)
	sort.Strings(mutable)
	return appendOnly, mutable
}

// TestProductBootCallbackAppendOnlyCensus records the census and proves the two calls the
// existing recorder omits do not change it.
//
// The census is LOGGED rather than frozen as a literal. A frozen list would go red every
// time a module gained an evidence relation, which is ordinary work; what must not happen
// silently is the list changing between two releases that claim an upgrade path, and that
// comparison is made against a recorded census of the other release, not against a
// constant in this file. The log line is the artifact the comparison uses.
func TestProductBootCallbackAppendOnlyCensus(t *testing.T) {
	appendOnly, mutable := productBootCallbackCensus(t)
	if len(appendOnly) == 0 {
		t.Fatal("the product callback declares no append-only relations at all")
	}
	seen := map[string]bool{}
	for _, table := range appendOnly {
		if seen[table] {
			t.Fatalf("the product callback registers %s twice", table)
		}
		seen[table] = true
	}
	blob, err := json.Marshal(appendOnly)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("PRODUCT_CALLBACK_APPEND_ONLY|count=%d|tables=%s", len(appendOnly), blob)

	// The three relations the two extra calls create are MUTABLE, so the append-only
	// census is the same with and without them. Measured rather than assumed, because
	// the difference decides whether `migrate manifest` output could stand in for the
	// callback when comparing two releases' upgrade compatibility.
	mutableSet := map[string]bool{}
	for _, table := range mutable {
		mutableSet[table] = true
	}
	for _, table := range []string{"mcp_tool_pins", "governance_cb_rule", "governance_cb_state"} {
		if seen[table] {
			t.Fatalf("%s is registered append-only; the callback's extra calls do change the census", table)
		}
		if !mutableSet[table] {
			t.Fatalf("%s was not registered at all by the complete callback", table)
		}
	}
	if path := os.Getenv("OLIVARES_TEST_CENSUS_OUT"); path != "" {
		out, merr := json.MarshalIndent(map[string]any{
			"append_only": appendOnly, "mutable": mutable,
		}, "", " ")
		if merr != nil {
			t.Fatal(merr)
		}
		if werr := os.WriteFile(path, out, 0o600); werr != nil {
			t.Fatalf("write the census artifact: %v", werr)
		}
		t.Logf("PRODUCT_CALLBACK_CENSUS_WRITTEN|path=%s", path)
	}
}
