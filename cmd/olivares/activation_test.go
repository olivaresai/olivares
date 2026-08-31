// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestActivationManifestRoundtripAndOverlay(t *testing.T) {
	dir := t.TempDir()

	// A missing manifest is an empty manifest, not an error.
	m, err := LoadActivationManifest(dir)
	if err != nil {
		t.Fatalf("load missing: %v", err)
	}
	if len(m.Entries) != 0 {
		t.Fatalf("missing manifest should be empty, got %+v", m.Entries)
	}

	m.Preset = "regulated"
	m.Entries = []ActivationEntry{
		{Addon: "reporting", Env: "OLIVARES_REPORTING_CONFIG", Value: filepath.Join(dir, "reporting.json"), State: ActivationActive},
		{Addon: "audit-worm-archive", Env: "OLIVARES_AUDIT_ARCHIVE_CONFIG", Value: filepath.Join(dir, "worm.json"), State: ActivationPending, NeedsSecret: true},
	}
	if err := SaveActivationManifest(dir, m, time.Unix(1_800_000_000, 0)); err != nil {
		t.Fatalf("save: %v", err)
	}
	// The file is 0600 and round-trips.
	info, err := os.Stat(ActivationManifestPath(dir))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("manifest mode = %v (%v), want 0600", info.Mode().Perm(), err)
	}
	got, err := LoadActivationManifest(dir)
	if err != nil || got.Preset != "regulated" || len(got.Entries) != 2 {
		t.Fatalf("roundtrip = %+v (%v)", got, err)
	}

	// Only ACTIVE entries overlay; PENDING (needs-secret) ones do not.
	overlay := got.activeOverlay()
	if overlay["OLIVARES_REPORTING_CONFIG"] == "" {
		t.Fatal("active reporting entry must overlay its env")
	}
	if _, ok := overlay["OLIVARES_AUDIT_ARCHIVE_CONFIG"]; ok {
		t.Fatal("pending (needs-secret) entry must NOT overlay — a bought-but-unconfigured control never pretends to run")
	}
}

func TestOsGetenvOverlayRespectsRealEnvAndScope(t *testing.T) {
	setActivationOverlayForTest(map[string]string{
		"OLIVARES_REPORTING_CONFIG": "/data/reporting.json",
	})
	t.Cleanup(func() { setActivationOverlayForTest(nil) })

	// Unset env + active manifest entry ⇒ the manifest value is supplied.
	if got := osGetenv("OLIVARES_REPORTING_CONFIG"); got != "/data/reporting.json" {
		t.Fatalf("overlay lookup = %q, want the manifest value", got)
	}
	// A real env value ALWAYS wins (operator override / break-glass).
	t.Setenv("OLIVARES_REPORTING_CONFIG", "/override.json")
	if got := osGetenv("OLIVARES_REPORTING_CONFIG"); got != "/override.json" {
		t.Fatalf("real env must win over the manifest, got %q", got)
	}
	// A key not in the manifest is unaffected.
	if got := osGetenv("OLIVARES_DEFINITELY_UNSET_KEY_XYZ"); got != "" {
		t.Fatalf("non-manifest key = %q, want empty", got)
	}
}

func TestAdmitActivationModules_RefusesUnpurchased(t *testing.T) {
	err := AdmitActivationModules([]string{"reporting"}, []string{"iso42001"})
	if !errors.Is(err, ErrModuleNotPurchased) {
		t.Fatalf("unpurchased module: %v, want ErrModuleNotPurchased", err)
	}
}

func TestAdmitActivationModules_PaidModuleAllowed(t *testing.T) {
	if err := AdmitActivationModules([]string{"Reporting", "rtbf-depth"}, []string{"reporting"}); err != nil {
		t.Fatalf("purchased module must admit: %v", err)
	}
}

func TestAdmitActivationModules_EmptyOwnedRefusesRequest(t *testing.T) {
	err := AdmitActivationModules(nil, []string{"reporting"})
	if !errors.Is(err, ErrModuleNotPurchased) {
		t.Fatalf("no purchase set: %v, want refuse", err)
	}
}

func TestAdmitActivationModules_EmptySliceRefusesRequest(t *testing.T) {
	err := AdmitActivationModules([]string{}, []string{"reporting"})
	if !errors.Is(err, ErrModuleNotPurchased) {
		t.Fatalf("empty purchase slice: %v, want refuse", err)
	}
}

func TestAdmitActivationModules_NothingRequestedOK(t *testing.T) {
	if err := AdmitActivationModules(nil, nil); err != nil {
		t.Fatalf("empty request must admit: %v", err)
	}
}

func TestSaveActivationManifestOwned_RefusesUnpurchased(t *testing.T) {
	dir := t.TempDir()
	m := &ActivationManifest{
		Modules: []string{"iso42001"},
		Entries: []ActivationEntry{{Addon: "iso42001", State: ActivationActive}},
	}
	err := SaveActivationManifestOwned(dir, m, []string{"reporting"}, time.Unix(1_800_000_000, 0))
	if !errors.Is(err, ErrModuleNotPurchased) {
		t.Fatalf("save: %v, want ErrModuleNotPurchased", err)
	}
	if _, err := os.Stat(ActivationManifestPath(dir)); !os.IsNotExist(err) {
		t.Fatal("refused save must not write the manifest")
	}
}

func TestSaveActivationManifestOwned_EmptyOwnedSliceRefuses(t *testing.T) {
	dir := t.TempDir()
	m := &ActivationManifest{
		Modules: []string{"reporting"},
		Entries: []ActivationEntry{{Addon: "reporting", State: ActivationActive}},
	}
	err := SaveActivationManifestOwned(dir, m, []string{}, time.Unix(1_800_000_000, 0))
	if !errors.Is(err, ErrModuleNotPurchased) {
		t.Fatalf("empty owned slice: %v, want refuse", err)
	}
	if _, err := os.Stat(ActivationManifestPath(dir)); !os.IsNotExist(err) {
		t.Fatal("refused save must not write the manifest")
	}
}

func TestSaveActivationManifestOwned_PaidModuleWrites(t *testing.T) {
	dir := t.TempDir()
	m := &ActivationManifest{
		Modules: []string{"reporting"},
		Entries: []ActivationEntry{{Addon: "reporting", Env: "OLIVARES_REPORTING_CONFIG", Value: "x", State: ActivationActive}},
	}
	if err := SaveActivationManifestOwned(dir, m, []string{"reporting"}, time.Unix(1_800_000_000, 0)); err != nil {
		t.Fatalf("save purchased: %v", err)
	}
	got, err := LoadActivationManifest(dir)
	if err != nil || len(got.Modules) != 1 || got.Modules[0] != "reporting" {
		t.Fatalf("roundtrip modules = %+v (%v)", got, err)
	}
}

func TestAdmitActivationModules_EmptyOwnedIsNotAGrantMutant(t *testing.T) {
	// Direction of no-fire: treating nil/empty owned as "admit everything"
	// is the lie this lote exists to kill. A mutant that drops the
	// empty-owned miss would make this request succeed.
	if err := AdmitActivationModules(nil, []string{"iso42001"}); err == nil {
		t.Fatal("mutant survived: empty owned admitted an unpurchased module")
	}
	if err := AdmitActivationModules([]string{}, []string{"iso42001"}); err == nil {
		t.Fatal("mutant survived: empty owned slice admitted an unpurchased module")
	}
}

func TestInitActivationManifestMissingIsNoop(t *testing.T) {
	setActivationOverlayForTest(nil)
	dir := t.TempDir()
	if err := initActivationManifest(dir); err != nil {
		t.Fatalf("init on empty dir: %v", err)
	}
	if got := osGetenv("OLIVARES_REPORTING_CONFIG"); got != "" && os.Getenv("OLIVARES_REPORTING_CONFIG") == "" {
		t.Fatalf("no manifest ⇒ no overlay, got %q", got)
	}
}
