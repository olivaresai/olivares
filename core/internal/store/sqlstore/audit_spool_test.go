// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/canon"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const largeAuditSpoolBudget = int64(1 << 60)

// openSQLiteSpoolTest is the config-aware variant of openSQLiteTest used by the
// spool tests. Fields supplied by the caller are preserved; only SQLite, an
// in-memory DSN and Debug receive test defaults.
func openSQLiteSpoolTest(t *testing.T, cfg store.Config) store.Store {
	t.Helper()
	if cfg.Engine == "" {
		cfg.Engine = store.EngineSQLite
	}
	if cfg.DSN == "" {
		cfg.DSN = ":memory:"
	}
	cfg.Debug = true
	st, err := Open(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("open spool store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestAuditSpoolUnconfiguredIsInert(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteSpoolTest(t, store.Config{})
	tenant := provisionTenant(t, st, "spool-off")
	appendN(t, st, tenant, 4)

	if got := readAuditSpoolUsage(t, st); got != 0 {
		t.Fatalf("usage with budget disabled = %d, want 0", got)
	}
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		report, err := sc.Audit().Verify(ctx, 1)
		if err == nil && (!report.OK || report.Checked != 5) {
			t.Fatalf("verify = %+v, want 5 intact events", report)
		}
		return err
	}); err != nil {
		t.Fatalf("verify unconfigured spool: %v", err)
	}
}

func TestAuditSpoolBlockDenyClosed(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "block.db")
	initial := openSQLiteSpoolTest(t, store.Config{DSN: dsn})
	tenant := provisionTenant(t, initial, "spool-block")
	appendN(t, initial, tenant, 1)
	if err := initial.Close(); err != nil {
		t.Fatalf("close initial store: %v", err)
	}

	st := openSQLiteSpoolTest(t, store.Config{DSN: dsn, AuditSpoolMaxBytes: 1})
	before := readAuditSpoolState(t, st, tenant)
	err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, err := sc.Audit().Append(ctx, model.AuditDraft{
			Actor: "user:block", ActorKind: model.ActorUser, Action: "agent.update",
			Meta: map[string]any{"attempt": true},
		})
		return err
	})
	if !errors.Is(err, store.ErrAuditSpoolFull) {
		t.Fatalf("append error = %v, want ErrAuditSpoolFull", err)
	}
	after := readAuditSpoolState(t, st, tenant)
	if before.rows != after.rows || before.tailSeq != after.tailSeq || before.headSeq != after.headSeq ||
		before.usage != after.usage || !bytes.Equal(before.headHash, after.headHash) {
		t.Fatalf("state changed on rejected append: before=%+v after=%+v", before, after)
	}

	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		var walked int
		if err := sc.Audit().Walk(ctx, 1, func(model.AuditEvent) error { walked++; return nil }); err != nil {
			return err
		}
		report, err := sc.Audit().Verify(ctx, 1)
		if err == nil && (!report.OK || int64(walked) != report.Checked) {
			t.Fatalf("post-refusal read: walked=%d verify=%+v", walked, report)
		}
		return err
	}); err != nil {
		t.Fatalf("reads after full spool: %v", err)
	}
}

func TestAuditSpoolExemptEventsAppendAndAccountOverBudget(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "exempt.db")
	initial := openSQLiteSpoolTest(t, store.Config{DSN: dsn})
	tenant := provisionTenant(t, initial, "spool-exempt")
	if err := initial.Close(); err != nil {
		t.Fatalf("close initial store: %v", err)
	}

	st := openSQLiteSpoolTest(t, store.Config{DSN: dsn, AuditSpoolMaxBytes: 1})
	before := readAuditSpoolUsage(t, st)
	signedDraft := model.AuditDraft{
		Actor: "system", ActorKind: model.ActorSystem, Action: store.ActionAuditCheckpoint,
		Meta: map[string]any{"checkpoint": int64(1)}, Sig: []byte("checkpoint-signature"),
	}
	archiveDraft := model.AuditDraft{
		Actor: "system", ActorKind: model.ActorSystem, Action: store.ActionAuditArchiveSegment,
		Meta: map[string]any{"from": int64(1), "to": int64(2)},
	}
	var signed, archive model.AuditEvent
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var err error
		signed, err = sc.Audit().Append(ctx, signedDraft)
		if err != nil {
			return err
		}
		archive, err = sc.Audit().Append(ctx, archiveDraft)
		return err
	}); err != nil {
		t.Fatalf("append exempt events over budget: %v", err)
	}
	signedMeta, err := canon.CanonicalMeta(signedDraft.Meta)
	if err != nil {
		t.Fatal(err)
	}
	archiveMeta, err := canon.CanonicalMeta(archiveDraft.Meta)
	if err != nil {
		t.Fatal(err)
	}
	wantDelta := auditEventSpoolBytes(signed, signedMeta, storedBlind(t, st, signed)) +
		auditEventSpoolBytes(archive, archiveMeta, storedBlind(t, st, archive))
	if got := readAuditSpoolUsage(t, st); got != before+wantDelta {
		t.Fatalf("usage after exempt events = %d, want %d", got, before+wantDelta)
	}
}

func TestAuditSpoolAccountingExactness(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "exact.db")
	initial := openSQLiteSpoolTest(t, store.Config{DSN: dsn})
	tenant := provisionTenant(t, initial, "spool-exact")
	if err := initial.Close(); err != nil {
		t.Fatalf("close initial store: %v", err)
	}
	st := openSQLiteSpoolTest(t, store.Config{DSN: dsn, AuditSpoolMaxBytes: largeAuditSpoolBudget})
	drafts := []model.AuditDraft{
		{
			Actor: "user:á", ActorKind: model.ActorUser, Action: "agent.signed",
			TargetKind: "core.agent", TargetID: "agent-一", Sig: []byte{0, 1, 2, 3},
			Meta: map[string]any{"nested": map[string]any{"city": "Málaga", "ok": true}},
		},
		{
			Actor: "agent:2", ActorKind: model.ActorAgent, Action: "agent.unsigned",
			Meta: map[string]any{"array": []any{"雪", float64(3), map[string]any{"x": "ñ"}}},
		},
		{
			Actor: "connector:3", ActorKind: model.ActorConnector, Action: "connector.sync",
			// A payload hash is a SHA-256: full width, or nil for "no payload". The
			// append boundary rejects anything else now, instead of zero-padding a
			// stub into the preimage where nobody could explain the result later.
			PayloadHash: append([]byte{0xff, 0x00, 0x7f}, make([]byte, 29)...),
			Meta:        map[string]any{"empty": map[string]any{}},
		},
	}
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		for _, draft := range drafts {
			if _, err := sc.Audit().Append(ctx, draft); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("append exactness fixtures: %v", err)
	}

	usage := readAuditSpoolUsage(t, st)
	sqlSum := readSQLiteAuditSpoolSum(t, st)
	var goSum int64
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		return sc.Audit().(store.CanonicalWalker).WalkCanonical(ctx, 1, func(ev model.AuditEvent, meta string, blind []byte) error {
			goSum += auditEventSpoolBytes(ev, meta, blind)
			return nil
		})
	}); err != nil {
		t.Fatalf("walk exactness fixtures: %v", err)
	}
	if usage != sqlSum || usage != goSum {
		t.Fatalf("logical-byte accounting diverged: usage=%d sql=%d go=%d", usage, sqlSum, goSum)
	}
}

func TestAuditSpoolBootRecomputeAndIncrement(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "recompute.db")
	initial := openSQLiteSpoolTest(t, store.Config{DSN: dsn})
	tenant := provisionTenant(t, initial, "spool-recompute")
	appendN(t, initial, tenant, 3)
	if got := readAuditSpoolUsage(t, initial); got != 0 {
		t.Fatalf("disabled usage before reopen = %d, want 0", got)
	}
	if err := initial.Close(); err != nil {
		t.Fatalf("close initial store: %v", err)
	}

	st := openSQLiteSpoolTest(t, store.Config{DSN: dsn, AuditSpoolMaxBytes: largeAuditSpoolBudget})
	recomputed := readSQLiteAuditSpoolSum(t, st)
	if got := readAuditSpoolUsage(t, st); got != recomputed {
		t.Fatalf("boot recompute usage = %d, want %d", got, recomputed)
	}
	draft := model.AuditDraft{
		Actor: "user:next", ActorKind: model.ActorUser, Action: "agent.update",
		Meta: map[string]any{"unicode": "á雪", "nested": map[string]any{"n": float64(4)}},
	}
	var ev model.AuditEvent
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var err error
		ev, err = sc.Audit().Append(ctx, draft)
		return err
	}); err != nil {
		t.Fatalf("append after recompute: %v", err)
	}
	meta, err := canon.CanonicalMeta(draft.Meta)
	if err != nil {
		t.Fatal(err)
	}
	want := recomputed + auditEventSpoolBytes(ev, meta, storedBlind(t, st, ev))
	if got := readAuditSpoolUsage(t, st); got != want {
		t.Fatalf("usage after post-recompute append = %d, want %d", got, want)
	}
}

func TestAuditSpoolDegradeDropsAndCommits(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "degrade.db")
	initial := openSQLiteSpoolTest(t, store.Config{DSN: dsn})
	tenant := provisionTenant(t, initial, "spool-degrade")
	if err := initial.Close(); err != nil {
		t.Fatalf("close initial store: %v", err)
	}

	st := openSQLiteSpoolTest(t, store.Config{
		DSN: dsn, AuditSpoolMaxBytes: 1, AuditSpoolOnFull: store.AuditSpoolDegrade,
	})
	before := readAuditSpoolState(t, st, tenant)
	const drops = int64(4)
	var firstAt string
	for i := int64(0); i < drops; i++ {
		if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
			ev, err := sc.Audit().Append(ctx, model.AuditDraft{
				Actor: "user:degrade", ActorKind: model.ActorUser, Action: "agent.update",
				Meta: map[string]any{"i": i},
			})
			if err == nil && ev.Seq != 0 {
				t.Fatalf("dropped append returned persisted event: %+v", ev)
			}
			return err
		}); err != nil {
			t.Fatalf("degrade drop %d: %v", i+1, err)
		}
		gap, ok := readPendingAuditGap(t, st, tenant)
		if !ok || gap.dropped != i+1 {
			t.Fatalf("pending gap after drop %d = %+v, ok=%v", i+1, gap, ok)
		}
		if i == 0 {
			firstAt = gap.firstDroppedAt
		} else if gap.firstDroppedAt != firstAt {
			t.Fatalf("first_dropped_at changed: got %q, want %q", gap.firstDroppedAt, firstAt)
		}
	}
	after := readAuditSpoolState(t, st, tenant)
	if before.rows != after.rows || before.tailSeq != after.tailSeq || before.headSeq != after.headSeq ||
		before.usage != after.usage || !bytes.Equal(before.headHash, after.headHash) {
		t.Fatalf("ledger state changed across drops: before=%+v after=%+v", before, after)
	}
}

func TestAuditSpoolRejectsUnknownModeAtOpen(t *testing.T) {
	_, err := Open(context.Background(), store.Config{
		Engine: store.EngineSQLite, DSN: ":memory:", AuditSpoolOnFull: "discard",
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "invalid audit spool on_full mode") {
		t.Fatalf("Open unknown spool mode error = %v", err)
	}
}

type auditSpoolState struct {
	rows             int64
	tailSeq, headSeq int64
	headHash         []byte
	usage            int64
}

func readAuditSpoolState(t *testing.T, st store.Store, tenant model.TenantID) auditSpoolState {
	t.Helper()
	db := st.(*sqlStore).db
	var state auditSpoolState
	if err := db.QueryRow(
		"SELECT COUNT(*), COALESCE(MAX(seq), 0) FROM audit_events WHERE tenant_id = ?", tenant.String(),
	).Scan(&state.rows, &state.tailSeq); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(
		"SELECT seq, hash FROM audit_heads WHERE tenant_id = ?", tenant.String(),
	).Scan(&state.headSeq, &state.headHash); err != nil {
		t.Fatal(err)
	}
	state.usage = readAuditSpoolUsage(t, st)
	return state
}

func readAuditSpoolUsage(t *testing.T, st store.Store) int64 {
	t.Helper()
	var usage int64
	if err := st.(*sqlStore).db.QueryRow("SELECT bytes FROM audit_spool_usage WHERE id = 1").Scan(&usage); err != nil {
		t.Fatal(err)
	}
	return usage
}

func readSQLiteAuditSpoolSum(t *testing.T, st store.Store) int64 {
	t.Helper()
	const query = `SELECT COALESCE(SUM(
length(CAST(id AS BLOB)) + length(CAST(tenant_id AS BLOB)) + 8 +
length(CAST(occurred_at AS BLOB)) + length(CAST(actor AS BLOB)) +
length(CAST(actor_kind AS BLOB)) + length(CAST(action AS BLOB)) +
length(CAST(target_kind AS BLOB)) + length(CAST(target_id AS BLOB)) +
length(CAST(meta AS BLOB)) + COALESCE(length(meta_blind), 0) +
COALESCE(length(payload_hash), 0) +
length(prev_hash) + length(hash) + COALESCE(length(sig), 0)
), 0) FROM audit_events`
	var sum int64
	if err := st.(*sqlStore).db.QueryRow(query).Scan(&sum); err != nil {
		t.Fatal(err)
	}
	return sum
}

// storedBlind reads the blind actually persisted for one sealed event. The tests
// read it from the ROW rather than deriving it from the event, because the event
// deliberately carries only the discriminator: an expectation computed from the
// implementation's own assumption about the blind's width would still pass if the
// column and the accounting disagreed, which is the failure this asserts against.
func storedBlind(t *testing.T, st store.Store, ev model.AuditEvent) []byte {
	t.Helper()
	var blind []byte
	q := "SELECT meta_blind FROM audit_events WHERE tenant_id = ? AND seq = ?"
	if err := st.(*sqlStore).db.QueryRow(q, ev.TenantID.String(), ev.Seq).Scan(&blind); err != nil {
		t.Fatalf("read stored blind for seq %d: %v", ev.Seq, err)
	}
	return blind
}
