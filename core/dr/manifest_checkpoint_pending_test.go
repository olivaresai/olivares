// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package dr_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/dr"
	"github.com/olivaresai/olivares/core/store"
)

// manifestFor builds a backup manifest exactly as makeBundle does, without sealing a bundle.
func manifestFor(t *testing.T, e *estate) *dr.Manifest {
	t.Helper()
	snap := e.snapshotInto(t, filepath.Join(t.TempDir(), "olivares.db"))
	sum, size, err := dr.FileSHA256(snap)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	keyRef := dr.KeyRef{
		File: "keys/audit-signing.key.enc", Name: "audit-signing.key", Role: dr.RoleAudit,
		PubSHA256: dr.PubFingerprint(e.pub()),
	}
	m, err := dr.BuildManifest(context.Background(), e.st, e.pub(), e.cpVerifier(t), dr.BuildOptions{
		EngineKind: string(store.EngineSQLite),
		Version:    "test",
		Store:      dr.StoreSnapshot{Method: dr.MethodVacuumInto, File: "store/olivares.db", SizeBytes: size, SHA256: sum},
		Keys:       []dr.KeyRef{keyRef},
		TipMatch:   dr.TipExact,
		Now:        time.Unix(1_700_000_000, 0),
	})
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	return m
}

func tipFor(t *testing.T, m *dr.Manifest, tenant string) dr.TenantTip {
	t.Helper()
	for _, tip := range m.Tenants {
		if tip.Tenant == tenant {
			return tip
		}
	}
	t.Fatalf("tenant %s missing from the manifest", tenant)
	return dr.TenantTip{}
}

// A young estate whose checkpoint scheduler has not fired yet is PENDING, not failed
// (audit.CheckpointStatusPending). Restore already treats it as advisory; backup
// must not refuse to capture it, or `dr backup` fails on every fresh installation until
// the first checkpoint interval elapses.
func TestBuildManifestYoungEstateCheckpointsPendingIsVerified(t *testing.T) {
	e := newEstate(t)
	tn := e.newTenant(t)
	e.appendN(t, tn, 3)
	tip := tipFor(t, manifestFor(t, e), tn.String())
	if tip.Checkpoints != 0 || !tip.VerifiedAtBackup || tip.VerifyReason != "" {
		t.Fatalf("young tenant tip = %+v; want verified, zero checkpoints, no failure reason", tip)
	}
}

// A checkpoint that EXISTS and does not verify is still a refusal.
func TestBuildManifestTamperedCheckpointStaysUnverified(t *testing.T) {
	e := newEstate(t)
	tn := e.newTenant(t)
	e.appendN(t, tn, 3)
	e.checkpointAll(t)
	rawExec(t, e.dbPath,
		"DROP TRIGGER audit_events_no_update",
		"UPDATE audit_events SET sig = randomblob(64) WHERE tenant_id = '"+tn.String()+"' AND action = 'audit.checkpoint'",
	)
	tip := tipFor(t, manifestFor(t, e), tn.String())
	if tip.VerifiedAtBackup || !strings.HasPrefix(tip.VerifyReason, "checkpoints:") {
		t.Fatalf("tampered tenant tip = %+v; want unverified with a checkpoints reason", tip)
	}
}
