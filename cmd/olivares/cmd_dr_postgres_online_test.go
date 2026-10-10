// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/hex"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/core/dr"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// THE DOCUMENTED ONLINE POSTGRES BACKUP FAILED WHILE THE ENGINE RAN (#496).
//
// `olivares dr backup --engine postgres` boots its own engine, and that boot joins
// the cluster leader election. With `serve` already holding the lock the backup
// process is a standby, and every tenant's chain tip was then read through
// Store.Custody, a read-write door behind the standby write gate: rc 1,
// "dr: tip for tenant ...: not the leader". The runbook has no stop step, the
// upgrade guide asks for a pre-upgrade bundle and the Helm backup CronJob runs
// beside the serving pods, so all three hit it.
//
// The serving node here is a second real boot on the same database: it holds the
// advisory lock exactly as `serve` does, and the test asserts that before it runs
// the backup, so the cell cannot go green by accident of the backup winning the
// election. Both postures run, because the split posture adds an owner pool to the
// path the fix touches.
func TestDRBackupPostgresWhileAnEngineHoldsTheLeaderLock(t *testing.T) {
	for _, split := range []bool{false, true} {
		t.Run(postureName(split), func(t *testing.T) {
			stampVersion(t)
			src := newPGSplitFixture(t, "live", split)
			dataDir := t.TempDir()
			src.seed(t, dataDir)

			serving, err := boot(t.Context(), bootConfig{
				DataDir: dataDir, Engine: "postgres",
				DSN: src.appDSN, OwnerDSN: src.ownerDSN, AdminDSN: src.adminDSN,
				Version: "test", NoIngest: true,
			})
			if err != nil {
				t.Fatalf("boot the serving node: %v", err)
			}
			defer func() { _ = serving.Close() }()
			if !serving.store.Leader().IsLeader() {
				t.Fatal("the serving node did not take the leader lock: the backup below would not be a standby, so this cell would prove nothing")
			}

			bundle := filepath.Join(t.TempDir(), "live.drbundle")
			args := append([]string{"backup", "--data-dir", dataDir}, src.drArgs()...)
			args = append(args, "--pg-dump", src.bin("pg_dump"), "--out", bundle, "--passphrase-file", passphraseFile(t))
			errs := captureErrorLogs(t)
			if out, err := runDR(args...); err != nil {
				t.Fatalf("dr backup while another engine holds the leader lock failed: %v\n%s", err, out)
			}
			// A standby refusing a leader-only write is the expected outcome of this
			// backup, not a failure of it: a scheduled backup must not print ERROR
			// lines for it, because operators alarm on them.
			if got := errs(); len(got) > 0 {
				t.Errorf("a successful backup beside a serving node logged %d error(s): %s", len(got), strings.Join(got, " | "))
			}

			f, err := os.Open(bundle)
			if err != nil {
				t.Fatalf("the backup reported success but wrote no bundle: %v", err)
			}
			defer func() { _ = f.Close() }()
			m, _, err := dr.ExtractBundle(f, t.TempDir())
			if err != nil {
				t.Fatalf("extract the bundle: %v", err)
			}

			// The tips are the live chains', read by the leader itself: a manifest
			// that listed tenants with empty or invented tips would pass a count.
			// seed wrote four events into each of three tenants.
			seeded := 0
			for _, tip := range m.Tenants {
				if !tip.VerifiedAtBackup {
					t.Errorf("tenant %s was not certified at backup: %s", tip.Tenant, tip.VerifyReason)
				}
				tenant, err := model.ParseTenantID(tip.Tenant)
				if err != nil {
					t.Fatalf("manifest tenant %q: %v", tip.Tenant, err)
				}
				var head store.HeadRef
				if err := serving.store.Custody(t.Context(), tenant, func(sc store.CustodyScope) error {
					var herr error
					head, _, herr = sc.Audit().Head(t.Context())
					return herr
				}); err != nil {
					t.Fatalf("read the live head of %s through the leader: %v", tip.Tenant, err)
				}
				if tip.HeadSeq != head.Seq || tip.HeadHash != hex.EncodeToString(head.Hash) {
					t.Errorf("tenant %s: bundle tip is seq %d %s, the live ledger is at seq %d %s",
						tip.Tenant, tip.HeadSeq, tip.HeadHash, head.Seq, hex.EncodeToString(head.Hash))
				}
				if tip.HeadSeq >= 4 && !tip.System {
					seeded++
				}
			}
			if seeded != 3 {
				t.Fatalf("the manifest carries %d seeded tenants with a real chain, want 3: %s", seeded, tenantList(m))
			}
		})
	}
}

// captureErrorLogs routes slog's default logger into a recorder for the rest of the
// test and returns the messages logged at ERROR so far (message and attributes).
func captureErrorLogs(t *testing.T) func() []string {
	t.Helper()
	prev := slog.Default()
	rec := &errorRecorder{}
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return rec.messages
}

type errorRecorder struct {
	mu   sync.Mutex
	msgs []string
}

func (r *errorRecorder) Enabled(context.Context, slog.Level) bool { return true }
func (r *errorRecorder) WithAttrs([]slog.Attr) slog.Handler       { return r }
func (r *errorRecorder) WithGroup(string) slog.Handler            { return r }
func (r *errorRecorder) Handle(_ context.Context, rec slog.Record) error {
	if rec.Level < slog.LevelError {
		return nil
	}
	msg := rec.Message
	rec.Attrs(func(a slog.Attr) bool { msg += " " + a.Key + "=" + a.Value.String(); return true })
	r.mu.Lock()
	r.msgs = append(r.msgs, msg)
	r.mu.Unlock()
	return nil
}

func (r *errorRecorder) messages() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.msgs...)
}

func tenantList(m *dr.Manifest) string {
	ids := make([]string, 0, len(m.Tenants))
	for _, t := range m.Tenants {
		ids = append(ids, t.Tenant)
	}
	return strings.Join(ids, ",")
}
