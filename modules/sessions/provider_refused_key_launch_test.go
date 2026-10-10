// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

// Codex launched on an OpenAI key that the provider test had refused never
// started its first turn (80 s and more, the process alive). The launch is refused
// with the console's readiness words; a key the test could not reach, or never ran
// on, still launches, and a key that passes again launches again.
func TestALaunchOnAKeyTheProviderRefusedIsRefused(t *testing.T) {
	m, _, tenant, _, probe := providerHarness(t)
	ctx := context.Background()
	rec := mustCreateRecord(t, m, tenant, CreateProviderRecordInput{Kind: ProviderKindOpenAI, DisplayName: "Team OpenAI", APIKey: testProviderKey})
	if _, _, err := m.mintFromProviderRecord(ctx, tenant, providerDriverCodex, rec.Ref); err != nil {
		t.Fatalf("a never-tested key = %v, want a launch", err)
	}
	probe.result, probe.err = ProviderProbeResult{}, ErrProviderRefused
	if _, err := m.TestProviderRecord(ctx, tenant, rec.Ref); err != nil {
		t.Fatal(err)
	}
	_, _, err := m.mintFromProviderRecord(ctx, tenant, providerDriverCodex, rec.Ref)
	if statusOf(err) != http.StatusConflict || err.Error() != "The API key Team OpenAI was refused. Replace it under API keys." {
		t.Fatalf("a refused key = %v, want 409 with the readiness words", err)
	}
	probe.result, probe.err = ProviderProbeResult{}, errors.New("dial tcp: no route to host")
	if _, err := m.TestProviderRecord(ctx, tenant, rec.Ref); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.mintFromProviderRecord(ctx, tenant, providerDriverCodex, rec.Ref); err != nil {
		t.Fatalf("an unreachable test is no verdict on the key = %v, want a launch", err)
	}
	probe.result, probe.err = ProviderProbeResult{Models: []string{"gpt-6"}, Detail: "1 models listed"}, nil
	if _, err := m.TestProviderRecord(ctx, tenant, rec.Ref); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.mintFromProviderRecord(ctx, tenant, providerDriverCodex, rec.Ref); err != nil {
		t.Fatalf("a key that passed again = %v, want a launch", err)
	}
}
