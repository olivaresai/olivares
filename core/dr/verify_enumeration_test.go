// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package dr_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/dr"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// enumBlindStore is a deployment whose cross-tenant enumeration fails closed:
// Postgres with no BYPASSRLS admin pool. Every other read keeps the REAL sqlstore
// behavior — only ListOrgs is replaced, and with the sentinel WRAPPED in the
// operator remedy, which is the shape sqlstore actually returns (system.go). A
// naked sentinel would be a double producing something production never does.
type enumBlindStore struct {
	store.Store
	wrapped error
}

func (s enumBlindStore) System(ctx context.Context, fn func(store.SystemScope) error) error {
	return s.Store.System(ctx, func(sys store.SystemScope) error {
		return fn(enumBlindSystem{SystemScope: sys, err: s.wrapped})
	})
}

type enumBlindSystem struct {
	store.SystemScope
	err error
}

func (e enumBlindSystem) ListOrgs(context.Context) ([]model.Org, error) { return nil, e.err }

// verifiableEstate builds a small signed estate and the manifest that describes
// it, so RestoreVerify has a genuine, self-consistent subject to certify.
func verifiableEstate(t *testing.T) (*estate, *dr.Manifest, *audit.CheckpointVerifier) {
	t.Helper()
	e := newEstate(t)
	ta := e.newTenant(t)
	e.appendN(t, ta, 3)
	e.checkpointAll(t)

	m, err := dr.BuildManifest(context.Background(), e.st, e.pub(), e.cpVerifier(t), dr.BuildOptions{
		EngineKind: "sqlite",
		Version:    "test",
		Store:      dr.StoreSnapshot{Method: dr.MethodVacuumInto, File: "store/olivares.db"},
		Keys: []dr.KeyRef{{
			File: "keys/audit-signing.key.enc", Name: "audit-signing.key", Role: dr.RoleAudit,
			PubSHA256: dr.PubFingerprint(e.pub()),
		}},
		TipMatch: dr.TipExact,
		Now:      time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("build manifest: %v", err)
	}
	return e, m, e.cpVerifier(t)
}

// A CEREMONY MUST NOT CERTIFY A CHECK IT COULD NOT RUN (raised by the
// external contrast).
//
// The extra-tenant check was guarded by `if eerr == nil`, with no else.
// RestoreReport.OK starts true, so on Postgres without an admin pool — where the
// enumeration fails closed since — the check was skipped and the report still
// said OK. That OK is what authorizes resuming writes, handed out over a
// foreign-bundle check that never happened.
//
// The wrap matters twice: the fix must recognize the sentinel THROUGH it, and must
// not let its text out. rep.Problems is joined into the console's failure message
// (core/api/dr_handler.go), so this surface needs the same non-leaking property the
// API envelope has — and had no test for it.
func TestRestoreVerifyNeverCertifiesAnUnauthoritativeEnumeration(t *testing.T) {
	const secretDSN = "postgres://olivares_app:hunter2@db.internal:5432/olivares"
	wrapped := fmt.Errorf("open store at %s: %w: engine %q holds no BYPASSRLS admin pool, so this System read is RLS-limited",
		secretDSN, store.ErrEnumerationNotAuthoritative, "postgres")

	e, m, cpv := verifiableEstate(t)

	rep, err := dr.RestoreVerify(context.Background(), enumBlindStore{Store: e.st, wrapped: wrapped}, m, e.pub(), cpv)
	if err != nil {
		t.Fatalf("RestoreVerify returned an error rather than a report: %v", err)
	}
	if rep.OK {
		t.Fatalf("a restore was CERTIFIED while the foreign-bundle check could not run: %+v", rep)
	}

	joined := strings.Join(rep.Problems, " | ")
	if !strings.Contains(joined, "could not run") {
		t.Errorf("the report does not say the check was skipped: %q", joined)
	}
	if !strings.Contains(joined, "--admin-dsn") {
		t.Errorf("the report does not name the remedy: %q", joined)
	}
	for _, leak := range []string{"hunter2", "db.internal", secretDSN} {
		if strings.Contains(joined, leak) {
			t.Fatalf("the wrapped store error reached the report (%q): %q", leak, joined)
		}
	}
}

// The control. Without it the fix above could be "always report NOT OK", which
// would pass the test above and refuse every real restore.
func TestRestoreVerifyStillCertifiesWhenEnumerationIsAuthoritative(t *testing.T) {
	e, m, cpv := verifiableEstate(t)

	rep, err := dr.RestoreVerify(context.Background(), e.st, m, e.pub(), cpv)
	if err != nil {
		t.Fatalf("RestoreVerify: %v", err)
	}
	if !rep.OK {
		t.Fatalf("a clean restore was refused: %+v", rep.Problems)
	}
}
