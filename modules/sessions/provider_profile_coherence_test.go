// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// Step 3: a key bound under the tool's own login was silently unused at launch
// (HU 030). Such a profile is refused at create and at patch, with the remedy.
func TestAKeyUnderTheToolsOwnLoginIsRefused(t *testing.T) {
	m, tenant, _ := resolveHarness(t)
	key := addRecord(t, m, tenant, ProviderKindAnthropic, "Team key", "", "sk-ant-fixture-0123456789")
	_, err := m.CreateProfile(context.Background(), tenant, CreateProfileInput{
		Driver: "claude", AuthSource: AuthSourceAccountHome, ProviderRecordRef: key.Ref,
	})
	if statusOf(err) != http.StatusUnprocessableEntity || !strings.Contains(err.Error(), "Managed provider credential") {
		t.Fatalf("create = %v, want 422 naming Managed provider credential", err)
	}
	own, err := m.CreateProfile(context.Background(), tenant, CreateProfileInput{Driver: "claude", AuthSource: AuthSourceAccountHome})
	if err != nil {
		t.Fatalf("own-login profile: %v", err)
	}
	ref := key.Ref
	if _, err := m.PatchProfile(context.Background(), tenant, own.Ref, ProfilePatch{ProviderRecordRef: &ref}); statusOf(err) != http.StatusUnprocessableEntity ||
		!strings.Contains(err.Error(), "Managed provider credential") {
		t.Fatalf("patch = %v, want 422 naming Managed provider credential", err)
	}
	managed := AuthSourceManagedInjection
	if _, err := m.PatchProfile(context.Background(), tenant, own.Ref, ProfilePatch{AuthSource: &managed, ProviderRecordRef: &ref}); err != nil {
		t.Fatalf("switching to a managed credential with the key: %v", err)
	}
}
