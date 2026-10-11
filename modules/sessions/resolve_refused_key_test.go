// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"testing"
)

// Root on FH 099: the automatic choice skips a key whose last provider test was refused
// when anything else can run the tool (the readiness the console shows). When the
// refused key is the only option it is still the answer, and the launch says the key
// was refused.
func TestResolve_ARefusedKeyIsChosenOnlyWhenNothingElseCanRun(t *testing.T) {
	m, tenant, _ := resolveHarness(t)
	probe := &fakeProbe{err: ErrProviderRefused}
	WithProviderProbe(probe)(m)
	ctx := context.Background()
	bad := addRecord(t, m, tenant, ProviderKindOpenAI, "Old OpenAI", "", "sk-proj-fixture-old-0123456789")
	if _, err := m.TestProviderRecord(ctx, tenant, bad.Ref); err != nil {
		t.Fatal(err)
	}
	only, err := m.PreviewProfile(ctx, tenant, "codex")
	if err != nil || only.Provider == nil || only.Provider.Ref != bad.Ref {
		t.Fatalf("with only the refused key = %+v %v, want it (the launch then says it was refused)", only, err)
	}
	got, err := m.ResolveProfile(ctx, tenant, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.mintFromProviderRecord(ctx, tenant, providerDriverCodex, got.Profile.ProviderRecordRef); statusOf(err) != http.StatusConflict {
		t.Fatalf("a launch on the only, refused key = %v, want 409", err)
	}
	probe.mu.Lock()
	probe.err = nil
	probe.mu.Unlock()
	good := addRecord(t, m, tenant, ProviderKindOpenAI, "New OpenAI", "", "sk-proj-fixture-new-0123456789")
	if _, err := m.TestProviderRecord(ctx, tenant, good.Ref); err != nil {
		t.Fatal(err)
	}
	next, err := m.PreviewProfile(ctx, tenant, "codex")
	if err != nil || next.Provider == nil || next.Provider.Ref != good.Ref {
		t.Fatalf("with a refused and a good key = %+v %v, want the good key", next, err)
	}
}
