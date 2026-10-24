// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// recordingVault is a vault that only remembers what it was asked to open.
type recordingVault struct {
	tenant   model.TenantID
	locators []string
}

func (v *recordingVault) Seal(context.Context, auth.Principal, model.TenantID, string, []byte) (string, error) {
	panic("the broker never seals")
}

func (v *recordingVault) Open(_ context.Context, tenant model.TenantID, locator string) ([]byte, error) {
	v.tenant = tenant
	v.locators = append(v.locators, locator)
	return []byte("document"), nil
}

func (v *recordingVault) Revoke(context.Context, auth.Principal, model.TenantID, string) error {
	panic("the broker never revokes")
}

// The broker reaches one thing in the vault: the document sealed as credential/<name> for
// the launch's own tenant. A name that could point anywhere else (a session secret under
// env/, an MCP credential, a person's vault item) never reaches the vault, and no vault
// wired on the node is an error, not an empty document.
func TestSessionCredentialOpenReadsOnlyTheCredentialNamespace(t *testing.T) {
	ctx := context.Background()
	const tenant = model.TenantID("tenant-a")

	vault := &recordingVault{}
	deps := newSessionCredentialDeps("/data", vault)
	if got, err := deps.Open(ctx, tenant, "prod-db"); err != nil || string(got) != "document" {
		t.Fatalf("Open(prod-db) = %q, %v; want the sealed document", got, err)
	}
	if len(vault.locators) != 1 || vault.locators[0] != "credential/prod-db" || vault.tenant != tenant {
		t.Fatalf("the vault was asked for %v in tenant %q; want credential/prod-db in %q", vault.locators, vault.tenant, tenant)
	}

	for _, name := range []string{"", ".", "..", "../env/x", "env/x", "a/b", `a\b`} {
		vault.locators = nil
		if _, err := deps.Open(ctx, tenant, name); err == nil {
			t.Errorf("Open(%q) was not refused", name)
		}
		if len(vault.locators) != 0 {
			t.Errorf("Open(%q) reached the vault as %v", name, vault.locators)
		}
	}

	if _, err := newSessionCredentialDeps("/data", nil).Open(ctx, tenant, "prod-db"); err == nil {
		t.Error("Open with no vault wired answered without an error")
	}
}
