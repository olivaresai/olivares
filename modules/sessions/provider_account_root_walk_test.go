// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// The engine may pass through the directories above its data directory but
// need not list them: on the appliance its policy lets it search /var and
// /var/lib, not read them. Creating the first account home therefore starts
// from the nearest directory that exists, never from /.
func TestProviderAccount_CreatePassesThroughAnAncestorItCannotList(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a directory whatever its mode")
	}
	be := profileBackends(t)[0]
	m, st := openProfileModule(t, be, nil)
	tenant := ensureTenant(t, st, "pass-through")
	a := newAcctAPI(m, st, tenant)

	passThrough := filepath.Join(t.TempDir(), "var")
	data := filepath.Join(passThrough, "data")
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(passThrough, 0o111); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(passThrough, 0o700) })
	acctUseAccountsRoot(t, m, filepath.Join(data, "accounts"))

	if r := a.create(acctHomeDriver, "claude-p1"); r.code != http.StatusCreated {
		t.Fatalf("create under a search-only ancestor = %d %s, want 201", r.code, r.raw)
	}
	if _, err := os.Stat(filepath.Join(data, "accounts", tenant.String())); err != nil {
		t.Fatalf("the account home is not under the accounts root: %v", err)
	}
}
