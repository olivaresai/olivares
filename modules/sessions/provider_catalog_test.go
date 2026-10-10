// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"testing"
)

func TestAutomaticProviderCatalogDoesNotWriteTestReceipts(t *testing.T) {
	probe := &fakeProbe{result: ProviderProbeResult{Models: []string{"model-fixture"}}}
	m, _, tenant, _ := newRuntimeHarness(t, WithProviderSecretVault(newFakeVault()), WithProviderProbe(probe))
	ctx := context.Background()
	rec, err := m.CreateProviderRecord(ctx, tenant, CreateProviderRecordInput{Kind: ProviderKindAnthropic, DisplayName: "Catalog fixture", APIKey: testProviderKey})
	if err != nil {
		t.Fatal(err)
	}
	ids, err := m.ReadProviderModels(ctx, tenant, rec)
	if err != nil || len(ids) != 1 || ids[0] != "model-fixture" {
		t.Fatalf("read catalog: %v %v", ids, err)
	}
	after, err := m.GetProviderRecord(ctx, tenant, rec.Ref)
	if err != nil || after.Version != rec.Version || after.ProbedAt != "" || after.ProbeState != "" {
		t.Fatalf("automatic discovery changed manual receipt: %+v %v", after, err)
	}
	base := "https://other.example"
	_, err = m.PatchProviderRecord(ctx, tenant, rec.Ref, ProviderRecordPatch{BaseURL: &base})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.ReadProviderModels(ctx, tenant, rec); err != ErrProviderRecordChanged {
		t.Fatalf("old config retained: %v", err)
	}
}
