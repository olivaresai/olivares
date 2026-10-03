// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import "testing"

// A profile bound to an API key saved in Providers is ready on the credential
// dimension for every driver, Claude included: the launch opens that key from
// the vault and needs no WIF, token file or adapter (HU on R1 refresh 01). The
// same profile with no key bound still reports what is missing.
func TestCredentialSourceCheck_AKeyBoundProfileIsReady(t *testing.T) {
	t.Parallel()
	m := &Module{rt: &runtimeState{}}
	sel := LaunchReadinessSelection{Transport: string(TransportStreamJSON), Isolation: string(IsolationNative)}
	for _, driver := range []string{"claude", "codex", "grok"} {
		got := m.credentialSourceCheck(ProviderProfile{
			Driver: driver, AuthSource: AuthSourceManagedInjection, ProviderRecordRef: "prv_key",
		}, sel, true)
		if got.State != ReadinessReady || got.Code != codeProviderRecordBound {
			t.Fatalf("%s bound to a key = %+v, want ready/%s", driver, got, codeProviderRecordBound)
		}
	}
	unbound := m.credentialSourceCheck(ProviderProfile{Driver: "codex", AuthSource: AuthSourceManagedInjection}, sel, true)
	if unbound.Code == codeProviderRecordBound || unbound.State == ReadinessReady {
		t.Fatalf("an unbound managed profile reads as key-bound: %+v", unbound)
	}
}
