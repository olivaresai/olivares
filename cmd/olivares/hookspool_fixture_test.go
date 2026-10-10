// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/core/audit"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The signed audit-spool store fixtures the hook PEP F9 tests (modules/sessions/hookpep) and the
// MCP gateway evidence tests share.

func openHookSpoolStore(t *testing.T, dsn string, signer *audit.Signer, budget int64, mode store.AuditSpoolMode) store.Store {
	t.Helper()
	st, err := coreengine.Open(context.Background(), store.Config{
		Engine: store.EngineSQLite, DSN: dsn, SignEvent: signer.SignEvent,
		AuditSpoolMaxBytes: budget, AuditSpoolOnFull: mode,
	}, nil)
	if err != nil {
		t.Fatalf("open spool store (budget=%d mode=%s): %v", budget, mode, err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// hookPendingDrops reads the durable degrade-mode loss counter (audit_spool_gaps, surfaced
// as AuditSpoolStatus.PendingDrops). This is the accounting the rollback bug lost.
func hookPendingDrops(t *testing.T, st store.Store) int64 {
	t.Helper()
	// The local is named after the interface it holds. misspell reads "statuser"
	// as a misspelling of "stature"; it is our own agent noun, and note the linter
	// does NOT flag the exported AuditSpoolStatuser one line down — renaming the
	// local to satisfy it would only break the tie to the name it mirrors.
	//nolint:misspell // agent noun of store.AuditSpoolStatuser, not "stature".
	statuser, ok := st.(store.AuditSpoolStatuser)
	if !ok {
		t.Fatal("store does not expose AuditSpoolStatuser")
	}
	//nolint:misspell // same local as above.
	status, configured, err := statuser.AuditSpoolStatus(context.Background())
	if err != nil {
		t.Fatalf("audit spool status: %v", err)
	}
	if !configured {
		t.Fatal("audit spool budget is not configured on this store")
	}
	return status.PendingDrops
}

// notLeaderHookStore simulates a standby node: every write transaction is refused with
// store.ErrNotLeader before the callback runs. Reads pass through.
type notLeaderHookStore struct{ store.Store }

func (s notLeaderHookStore) Mutate(context.Context, model.TenantID, func(store.Scope) error) error {
	return store.ErrNotLeader
}
