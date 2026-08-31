// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/internal/store/canon"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type fixedGapClock struct {
	now model.Timestamp
}

func (c fixedGapClock) Now() model.Timestamp { return c.now }

func gapTestClock() model.Clock {
	return fixedGapClock{now: model.NewTimestamp(time.Date(2026, 7, 10, 12, 0, 0, 123, time.UTC))}
}

// fakeGapSigner is deterministic and binds enough input to catch accidental
// signer bypasses in store tests. Cryptographic verification belongs to
// core/audit and is deliberately outside this assignment.
func fakeGapSigner(_ string, seq int64, hash []byte) []byte {
	sig := make([]byte, 12)
	binary.BigEndian.PutUint64(sig[:8], uint64(seq))
	copy(sig[8:], hash)
	return sig
}

func readPendingAuditGap(t *testing.T, st store.Store, tenant model.TenantID) (auditGap, bool) {
	t.Helper()
	var gap auditGap
	err := st.(*sqlStore).db.QueryRow(
		"SELECT dropped, first_dropped_at FROM audit_spool_gaps WHERE tenant_id = ?", tenant.String(),
	).Scan(&gap.dropped, &gap.firstDroppedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return auditGap{}, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return gap, true
}

type storedAuditRow struct {
	ev    model.AuditEvent
	meta  string
	blind []byte
}

func readTenantAuditRows(t *testing.T, st store.Store, tenant model.TenantID) []storedAuditRow {
	t.Helper()
	q := "SELECT " + columnList(auditColumns) + " FROM audit_events WHERE tenant_id = ? ORDER BY seq"
	rows, err := st.(*sqlStore).db.Query(q, tenant.String())
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []storedAuditRow
	for rows.Next() {
		ev, meta, blind, err := scanAudit(rows)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, storedAuditRow{ev: ev, meta: meta, blind: blind})
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func decodeGapMeta(t *testing.T, canonical string) map[string]any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewBufferString(canonical))
	dec.UseNumber()
	var meta map[string]any
	if err := dec.Decode(&meta); err != nil {
		t.Fatal(err)
	}
	return meta
}

func requireGapInt(t *testing.T, meta map[string]any, key string, want int64) {
	t.Helper()
	n, ok := meta[key].(json.Number)
	if !ok {
		t.Fatalf("gap meta %q = %#v, want JSON number", key, meta[key])
	}
	got, err := n.Int64()
	if err != nil || got != want {
		t.Fatalf("gap meta %q = %q (%v), want %d", key, n, err, want)
	}
}

func appendDroppedEvents(t *testing.T, st store.Store, tenant model.TenantID, count int64) {
	t.Helper()
	ctx := context.Background()
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		for i := int64(0); i < count; i++ {
			ev, err := sc.Audit().Append(ctx, model.AuditDraft{
				Actor: "user:drop", ActorKind: model.ActorUser, Action: "agent.update",
				Meta: map[string]any{"drop": i},
			})
			if err != nil {
				return err
			}
			if ev.Seq != 0 {
				t.Fatalf("drop %d returned seq %d", i+1, ev.Seq)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("append %d dropped events: %v", count, err)
	}
}

func TestAuditGapSealsOnBudgetRaise(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "raise.db")
	initial := openSQLiteSpoolTest(t, store.Config{DSN: dsn})
	tenant := provisionTenant(t, initial, "gap-raise")
	appendN(t, initial, tenant, 2)
	if err := initial.Close(); err != nil {
		t.Fatal(err)
	}

	degraded := openSQLiteSpoolTest(t, store.Config{
		DSN: dsn, Clock: gapTestClock(), SignEvent: fakeGapSigner,
		AuditSpoolMaxBytes: 1, AuditSpoolOnFull: store.AuditSpoolDegrade,
	})
	before := readAuditSpoolState(t, degraded, tenant)
	const drops = int64(3)
	appendDroppedEvents(t, degraded, tenant, drops)
	pending, ok := readPendingAuditGap(t, degraded, tenant)
	if !ok || pending.dropped != drops {
		t.Fatalf("pending gap = %+v, ok=%v", pending, ok)
	}
	if want := gapTestClock().Now().String(); pending.firstDroppedAt != want {
		t.Fatalf("first_dropped_at = %q, want clock value %q", pending.firstDroppedAt, want)
	}
	if err := degraded.Close(); err != nil {
		t.Fatal(err)
	}

	st := openSQLiteSpoolTest(t, store.Config{
		DSN: dsn, Clock: gapTestClock(), SignEvent: fakeGapSigner,
		AuditSpoolMaxBytes: largeAuditSpoolBudget, AuditSpoolOnFull: store.AuditSpoolDegrade,
	})
	usageBefore := readAuditSpoolUsage(t, st)
	draft := model.AuditDraft{
		Actor: "user:resume", ActorKind: model.ActorUser, Action: "agent.update",
		Meta: map[string]any{"resumed": true},
	}
	var incoming model.AuditEvent
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var err error
		incoming, err = sc.Audit().Append(ctx, draft)
		return err
	}); err != nil {
		t.Fatalf("append after budget raise: %v", err)
	}

	rows := readTenantAuditRows(t, st, tenant)
	markerRow, incomingRow := rows[len(rows)-2], rows[len(rows)-1]
	wantMarkerSeq := before.tailSeq + drops + 1
	if markerRow.ev.Action != store.ActionAuditGap || markerRow.ev.Seq != wantMarkerSeq {
		t.Fatalf("marker = %+v, want audit.gap at %d", markerRow.ev, wantMarkerSeq)
	}
	if !bytes.Equal(markerRow.ev.PrevHash, before.headHash) {
		t.Fatal("marker did not link to the pre-gap chain tail")
	}
	if len(markerRow.ev.Sig) == 0 {
		t.Fatal("gap marker bypassed the configured event signer")
	}
	meta := decodeGapMeta(t, markerRow.meta)
	requireGapInt(t, meta, store.GapMetaFromSeq, before.tailSeq+1)
	requireGapInt(t, meta, store.GapMetaToSeq, before.tailSeq+drops)
	requireGapInt(t, meta, store.GapMetaCount, drops)
	if meta[store.GapMetaReason] != store.GapReasonSpoolFull || meta[store.GapMetaAt] != pending.firstDroppedAt {
		t.Fatalf("marker vocabulary = %#v, want reason=%q at=%q", meta, store.GapReasonSpoolFull, pending.firstDroppedAt)
	}
	if incoming.Seq != wantMarkerSeq+1 || incomingRow.ev.Seq != incoming.Seq ||
		!bytes.Equal(incoming.PrevHash, markerRow.ev.Hash) {
		t.Fatalf("incoming event = %+v, marker seq/hash = %d/%x", incoming, markerRow.ev.Seq, markerRow.ev.Hash)
	}
	if _, ok := readPendingAuditGap(t, st, tenant); ok {
		t.Fatal("pending gap survived its marker")
	}
	wantDelta := auditEventSpoolBytes(markerRow.ev, markerRow.meta, markerRow.blind) +
		auditEventSpoolBytes(incomingRow.ev, incomingRow.meta, incomingRow.blind)
	if got := readAuditSpoolUsage(t, st); got != usageBefore+wantDelta {
		t.Fatalf("usage after seal = %d, want %d", got, usageBefore+wantDelta)
	}
}

func TestAuditGapCheckpointNeverSealsAnchorDoes(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "exempt-seal.db")
	initial := openSQLiteSpoolTest(t, store.Config{DSN: dsn})
	tenant := provisionTenant(t, initial, "gap-exempt")
	if err := initial.Close(); err != nil {
		t.Fatal(err)
	}
	st := openSQLiteSpoolTest(t, store.Config{
		DSN: dsn, Clock: gapTestClock(), SignEvent: fakeGapSigner,
		AuditSpoolMaxBytes: 1, AuditSpoolOnFull: store.AuditSpoolDegrade,
	})
	rowsBefore := readTenantAuditRows(t, st, tenant)
	tailBefore := rowsBefore[len(rowsBefore)-1]
	appendDroppedEvents(t, st, tenant, 2)
	pendingBefore, _ := readPendingAuditGap(t, st, tenant)
	usageBefore := readAuditSpoolUsage(t, st)

	// A draft-carried signature (a checkpoint) was signed over the current head
	// BEFORE Append ran; sealing a marker in front of it would re-link its
	// PrevHash and break the archive verifier's attested-head binding
	// (checkpointPreimage over line.Seq-1 + prev_hash). It must append
	// contiguously and leave the episode untouched.
	cpDraft := model.AuditDraft{
		Actor: model.ActorSystem, ActorKind: model.ActorSystem, Action: store.ActionAuditCheckpoint,
		Meta: map[string]any{"checkpoint": int64(1)}, Sig: []byte("checkpoint-signature"),
	}
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, err := sc.Audit().Append(ctx, cpDraft)
		return err
	}); err != nil {
		t.Fatalf("append checkpoint during episode: %v", err)
	}
	rows := readTenantAuditRows(t, st, tenant)
	cp := rows[len(rows)-1]
	if cp.ev.Action != store.ActionAuditCheckpoint || cp.ev.Seq != tailBefore.ev.Seq+1 ||
		!bytes.Equal(cp.ev.PrevHash, tailBefore.ev.Hash) || !bytes.Equal(cp.ev.Sig, cpDraft.Sig) {
		t.Fatalf("checkpoint re-linked or sealed: tail=%+v cp=%+v", tailBefore.ev, cp.ev)
	}
	pendingAfterCp, ok := readPendingAuditGap(t, st, tenant)
	if !ok || pendingAfterCp != pendingBefore {
		t.Fatalf("checkpoint disturbed the pending episode: before=%+v after=%+v", pendingBefore, pendingAfterCp)
	}

	// An archive anchor is exempt but UNSIGNED at draft time (its per-event
	// signature is computed after sequencing), so it seals: marker first — now
	// positioned after the checkpoint — then the anchor.
	anchorDraft := model.AuditDraft{
		Actor: model.ActorSystem, ActorKind: model.ActorSystem, Action: store.ActionAuditArchiveSegment,
		Meta: map[string]any{"archive.from_seq": int64(1)},
	}
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, err := sc.Audit().Append(ctx, anchorDraft)
		return err
	}); err != nil {
		t.Fatalf("append anchor after episode: %v", err)
	}
	rows = readTenantAuditRows(t, st, tenant)
	marker, anchor := rows[len(rows)-2], rows[len(rows)-1]
	if marker.ev.Action != store.ActionAuditGap || anchor.ev.Action != store.ActionAuditArchiveSegment ||
		!bytes.Equal(marker.ev.PrevHash, cp.ev.Hash) || anchor.ev.Seq != marker.ev.Seq+1 ||
		len(marker.ev.Sig) == 0 {
		t.Fatalf("anchor seal shape: cp=%+v marker=%+v anchor=%+v", cp.ev, marker.ev, anchor.ev)
	}
	meta := decodeGapMeta(t, marker.meta)
	requireGapInt(t, meta, store.GapMetaFromSeq, cp.ev.Seq+1)
	requireGapInt(t, meta, store.GapMetaToSeq, cp.ev.Seq+2)
	requireGapInt(t, meta, store.GapMetaCount, 2)
	wantDelta := auditEventSpoolBytes(cp.ev, cp.meta, cp.blind) +
		auditEventSpoolBytes(marker.ev, marker.meta, marker.blind) + auditEventSpoolBytes(anchor.ev, anchor.meta, anchor.blind)
	if got := readAuditSpoolUsage(t, st); got != usageBefore+wantDelta {
		t.Fatalf("usage after checkpoint+seal = %d, want %d", got, usageBefore+wantDelta)
	}
	if _, ok := readPendingAuditGap(t, st, tenant); ok {
		t.Fatal("pending gap survived the anchor seal")
	}
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		report, err := sc.Audit().Verify(ctx, 1)
		if err == nil && (!report.OK || report.DeclaredGaps != 1) {
			t.Fatalf("verify after mid-episode checkpoint = %+v", report)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAuditGapThresholdSeal(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "threshold.db")
	initial := openSQLiteSpoolTest(t, store.Config{DSN: dsn})
	tenant := provisionTenant(t, initial, "gap-threshold")
	if err := initial.Close(); err != nil {
		t.Fatal(err)
	}
	st := openSQLiteSpoolTest(t, store.Config{
		DSN: dsn, Clock: gapTestClock(), SignEvent: fakeGapSigner,
		AuditSpoolMaxBytes: 1, AuditSpoolOnFull: store.AuditSpoolDegrade,
	})
	before := readAuditSpoolState(t, st, tenant)
	appendDroppedEvents(t, st, tenant, gapSealEvery)
	after := readAuditSpoolState(t, st, tenant)
	if after.rows != before.rows+1 || after.tailSeq != before.tailSeq+gapSealEvery+1 {
		t.Fatalf("threshold state = %+v, before=%+v", after, before)
	}
	if _, ok := readPendingAuditGap(t, st, tenant); ok {
		t.Fatal("threshold marker did not clear pending state")
	}
	rows := readTenantAuditRows(t, st, tenant)
	marker := rows[len(rows)-1]
	if marker.ev.Action != store.ActionAuditGap {
		t.Fatalf("threshold row action = %q", marker.ev.Action)
	}
	meta := decodeGapMeta(t, marker.meta)
	requireGapInt(t, meta, store.GapMetaCount, gapSealEvery)
	if got, want := after.usage-before.usage, auditEventSpoolBytes(marker.ev, marker.meta, marker.blind); got != want {
		t.Fatalf("threshold usage delta = %d, want marker bytes %d", got, want)
	}
}

func TestAuditGapTwoEpisodesVerify(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "episodes.db")
	initial := openSQLiteSpoolTest(t, store.Config{DSN: dsn})
	tenant := provisionTenant(t, initial, "gap-episodes")
	if err := initial.Close(); err != nil {
		t.Fatal(err)
	}
	st := openSQLiteSpoolTest(t, store.Config{
		DSN: dsn, Clock: gapTestClock(), SignEvent: fakeGapSigner,
		AuditSpoolMaxBytes: 1, AuditSpoolOnFull: store.AuditSpoolDegrade,
	})
	appendDroppedEvents(t, st, tenant, 2)
	// Both episodes seal via UNSIGNED exempt writes (archive anchors): a
	// draft-carried checkpoint signature never seals by design (see
	// TestAuditGapCheckpointNeverSealsAnchorDoes).
	for episode := 0; episode < 2; episode++ {
		draft := model.AuditDraft{
			Actor: model.ActorSystem, ActorKind: model.ActorSystem, Action: store.ActionAuditArchiveSegment,
			Meta: map[string]any{"episode": int64(episode + 1)},
		}
		if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
			_, err := sc.Audit().Append(ctx, draft)
			return err
		}); err != nil {
			t.Fatalf("seal episode %d: %v", episode+1, err)
		}
		if episode == 0 {
			appendDroppedEvents(t, st, tenant, 3)
		}
	}
	rows := readTenantAuditRows(t, st, tenant)
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		report, err := sc.Audit().Verify(ctx, 1)
		if err == nil && (!report.OK || report.DeclaredGaps != 2 || report.Checked != int64(len(rows))) {
			t.Fatalf("two-episode verify = %+v, rows=%d", report, len(rows))
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAuditGapBlockPreservesInheritedPending(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "block-inherited.db")
	initial := openSQLiteSpoolTest(t, store.Config{DSN: dsn})
	tenant := provisionTenant(t, initial, "gap-block")
	if err := initial.Close(); err != nil {
		t.Fatal(err)
	}
	degraded := openSQLiteSpoolTest(t, store.Config{
		DSN: dsn, Clock: gapTestClock(), SignEvent: fakeGapSigner,
		AuditSpoolMaxBytes: 1, AuditSpoolOnFull: store.AuditSpoolDegrade,
	})
	appendDroppedEvents(t, degraded, tenant, 2)
	pending, _ := readPendingAuditGap(t, degraded, tenant)
	if err := degraded.Close(); err != nil {
		t.Fatal(err)
	}

	st := openSQLiteSpoolTest(t, store.Config{
		DSN: dsn, Clock: gapTestClock(), SignEvent: fakeGapSigner,
		AuditSpoolMaxBytes: 1, AuditSpoolOnFull: store.AuditSpoolBlock,
	})
	before := readAuditSpoolState(t, st, tenant)
	err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, err := sc.Audit().Append(ctx, model.AuditDraft{
			Actor: "user:block", ActorKind: model.ActorUser, Action: "agent.update",
		})
		return err
	})
	if !errors.Is(err, store.ErrAuditSpoolFull) {
		t.Fatalf("block append error = %v", err)
	}
	after := readAuditSpoolState(t, st, tenant)
	stillPending, ok := readPendingAuditGap(t, st, tenant)
	if before.rows != after.rows || before.tailSeq != after.tailSeq || before.headSeq != after.headSeq ||
		before.usage != after.usage || !bytes.Equal(before.headHash, after.headHash) ||
		!ok || stillPending != pending {
		t.Fatalf("block changed inherited state: before=%+v after=%+v pending=%+v/%+v", before, after, pending, stillPending)
	}
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, err := sc.Audit().Append(ctx, model.AuditDraft{
			Actor: model.ActorSystem, ActorKind: model.ActorSystem, Action: store.ActionAuditArchiveSegment,
			Meta: map[string]any{"archive.from_seq": int64(1)},
		})
		return err
	}); err != nil {
		t.Fatalf("exempt seal after block: %v", err)
	}
	if _, ok := readPendingAuditGap(t, st, tenant); ok {
		t.Fatal("exempt append did not seal inherited gap")
	}
}

func TestAuditGapEmptyChainEpisode(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteSpoolTest(t, store.Config{
		Clock: gapTestClock(), SignEvent: fakeGapSigner,
		AuditSpoolMaxBytes: 1, AuditSpoolOnFull: store.AuditSpoolDegrade,
	})
	tenant := model.NewTenantID()
	const drops = int64(3)
	appendDroppedEvents(t, st, tenant, drops)
	if rows := readTenantAuditRows(t, st, tenant); len(rows) != 0 {
		t.Fatalf("fresh-chain drops wrote %d rows", len(rows))
	}
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, err := sc.Audit().Append(ctx, model.AuditDraft{
			Actor: model.ActorSystem, ActorKind: model.ActorSystem, Action: store.ActionAuditArchiveSegment,
			Meta: map[string]any{"archive.from_seq": int64(1)},
		})
		return err
	}); err != nil {
		t.Fatalf("seal empty chain: %v", err)
	}
	rows := readTenantAuditRows(t, st, tenant)
	if len(rows) != 2 || rows[0].ev.Seq != drops+1 || rows[1].ev.Seq != drops+2 ||
		!bytes.Equal(rows[0].ev.PrevHash, canon.ZeroHash()) {
		t.Fatalf("empty-chain rows = %+v", rows)
	}
	meta := decodeGapMeta(t, rows[0].meta)
	requireGapInt(t, meta, store.GapMetaFromSeq, 1)
	requireGapInt(t, meta, store.GapMetaToSeq, drops)
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		report, err := sc.Audit().Verify(ctx, 1)
		if err == nil && (!report.OK || report.DeclaredGaps != 1 || report.Checked != 2) {
			t.Fatalf("empty-chain verify = %+v", report)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAuditGapReservedActionRejected(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteSpoolTest(t, store.Config{
		AuditSpoolMaxBytes: largeAuditSpoolBudget, SignEvent: fakeGapSigner,
	})
	tenant := provisionTenant(t, st, "gap-reserved")
	before := readAuditSpoolState(t, st, tenant)
	err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, err := sc.Audit().Append(ctx, model.AuditDraft{
			Actor: model.ActorSystem, ActorKind: model.ActorSystem, Action: store.ActionAuditGap,
			TargetKind: "core.audit_gap",
		})
		return err
	})
	if !errors.Is(err, store.ErrReservedAuditAction) {
		t.Fatalf("reserved action error = %v", err)
	}
	after := readAuditSpoolState(t, st, tenant)
	if before.rows != after.rows || before.tailSeq != after.tailSeq || before.headSeq != after.headSeq ||
		before.usage != after.usage || !bytes.Equal(before.headHash, after.headHash) {
		t.Fatalf("reserved action changed state: before=%+v after=%+v", before, after)
	}
	if _, ok := readPendingAuditGap(t, st, tenant); ok {
		t.Fatal("reserved action created pending state")
	}
}

func TestAuditGapVerifyAnchoringVariants(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "anchors.db")
	initial := openSQLiteSpoolTest(t, store.Config{DSN: dsn})
	tenant := provisionTenant(t, initial, "gap-anchors")
	appendN(t, initial, tenant, 1)
	if err := initial.Close(); err != nil {
		t.Fatal(err)
	}
	st := openSQLiteSpoolTest(t, store.Config{
		DSN: dsn, Clock: gapTestClock(), SignEvent: fakeGapSigner,
		AuditSpoolMaxBytes: 1, AuditSpoolOnFull: store.AuditSpoolDegrade,
	})
	before := readAuditSpoolState(t, st, tenant)
	const drops = int64(3)
	appendDroppedEvents(t, st, tenant, drops)
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, err := sc.Audit().Append(ctx, model.AuditDraft{
			Actor: model.ActorSystem, ActorKind: model.ActorSystem, Action: store.ActionAuditArchiveSegment,
			Meta: map[string]any{"archive.from_seq": int64(1)},
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	markerSeq := before.tailSeq + drops + 1
	tests := []struct {
		name         string
		from         int64
		declaredGaps int64
	}{
		{name: "chain start", from: 1, declaredGaps: 1},
		{name: "marker sequence", from: markerSeq, declaredGaps: 1},
		{name: "inside hole", from: before.tailSeq + 2, declaredGaps: 1},
		{name: "after marker", from: markerSeq + 1, declaredGaps: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := st.View(ctx, tenant, func(sc store.Scope) error {
				report, err := sc.Audit().Verify(ctx, tt.from)
				if err == nil && (!report.OK || report.DeclaredGaps != tt.declaredGaps) {
					t.Fatalf("Verify(%d) = %+v", tt.from, report)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAuditGapVerifyAdversarialMarkers(t *testing.T) {
	t.Run("wrong declared range with consistent hashes", func(t *testing.T) {
		st, tenant, markerSeq := openSealedGapFixture(t, "wrong-range")
		db := st.(*sqlStore).db
		if _, err := db.Exec("DROP TRIGGER audit_events_no_update"); err != nil {
			t.Fatal(err)
		}
		wrongMeta, err := canon.CanonicalMeta(map[string]any{
			store.GapMetaFromSeq: markerSeq - 1,
			store.GapMetaToSeq:   markerSeq - 1,
			store.GapMetaCount:   int64(1),
			store.GapMetaReason:  store.GapReasonSpoolFull,
			store.GapMetaAt:      gapTestClock().Now().String(),
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("UPDATE audit_events SET meta = ? WHERE tenant_id = ? AND seq = ?", wrongMeta, tenant.String(), markerSeq); err != nil {
			t.Fatal(err)
		}
		rewriteAuditChainFrom(t, st, tenant, markerSeq)
		requireVerifyBreak(t, st, tenant, markerSeq, "gap-mismatch")
	})

	t.Run("altered marker meta without recomputed hash", func(t *testing.T) {
		st, tenant, markerSeq := openSealedGapFixture(t, "altered-meta")
		db := st.(*sqlStore).db
		if _, err := db.Exec("DROP TRIGGER audit_events_no_update"); err != nil {
			t.Fatal(err)
		}
		var canonical string
		if err := db.QueryRow("SELECT meta FROM audit_events WHERE tenant_id = ? AND seq = ?", tenant.String(), markerSeq).Scan(&canonical); err != nil {
			t.Fatal(err)
		}
		meta := decodeGapMeta(t, canonical)
		meta[store.GapMetaReason] = "tampered"
		changed, err := canon.CanonicalMeta(meta)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("UPDATE audit_events SET meta = ? WHERE tenant_id = ? AND seq = ?", changed, tenant.String(), markerSeq); err != nil {
			t.Fatal(err)
		}
		requireVerifyBreak(t, st, tenant, markerSeq, "hash-mismatch")
	})

	t.Run("marker without hole", func(t *testing.T) {
		ctx := context.Background()
		st := openSQLiteSpoolTest(t, store.Config{Clock: gapTestClock(), SignEvent: fakeGapSigner})
		tenant := provisionTenant(t, st, "gap-no-hole")
		appendN(t, st, tenant, 1)
		const markerSeq = int64(2)
		db := st.(*sqlStore).db
		if _, err := db.Exec("DROP TRIGGER audit_events_no_update"); err != nil {
			t.Fatal(err)
		}
		meta, err := canon.CanonicalMeta(map[string]any{
			store.GapMetaFromSeq: markerSeq,
			store.GapMetaToSeq:   markerSeq,
			store.GapMetaCount:   int64(1),
			store.GapMetaReason:  store.GapReasonSpoolFull,
			store.GapMetaAt:      gapTestClock().Now().String(),
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("UPDATE audit_events SET action = ?, meta = ? WHERE tenant_id = ? AND seq = ?",
			store.ActionAuditGap, meta, tenant.String(), markerSeq); err != nil {
			t.Fatal(err)
		}
		rewriteAuditChainFrom(t, st, tenant, markerSeq)
		var report store.VerifyReport
		if err := st.View(ctx, tenant, func(sc store.Scope) error {
			var err error
			report, err = sc.Audit().Verify(ctx, 1)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if report.OK || report.BreakAt != markerSeq || report.Reason != "gap-mismatch" {
			t.Fatalf("no-hole marker verify = %+v", report)
		}
	})
}

func openSealedGapFixture(t *testing.T, slug string) (store.Store, model.TenantID, int64) {
	t.Helper()
	ctx := context.Background()
	st := openSQLiteSpoolTest(t, store.Config{
		Clock: gapTestClock(), SignEvent: fakeGapSigner,
		AuditSpoolMaxBytes: 1, AuditSpoolOnFull: store.AuditSpoolDegrade,
	})
	// A fresh tenant id gives an empty physical chain and avoids an unbudgeted
	// provisioning event in the compact adversarial fixture.
	tenant := model.NewTenantID()
	appendDroppedEvents(t, st, tenant, 2)
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, err := sc.Audit().Append(ctx, model.AuditDraft{
			Actor: model.ActorSystem, ActorKind: model.ActorSystem, Action: store.ActionAuditArchiveSegment,
			Meta: map[string]any{"archive.from_seq": int64(1)},
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return st, tenant, 3
}

// rewriteAuditChainFrom simulates a DB-only attacker that keeps all structural
// hashes and the mutable head consistent after altering a row. Per-event
// signatures intentionally remain stale because live Verify proves structure,
// while core/audit proves authenticity.
func rewriteAuditChainFrom(t *testing.T, st store.Store, tenant model.TenantID, fromSeq int64) {
	t.Helper()
	db := st.(*sqlStore).db
	prev := canon.ZeroHash()
	if err := db.QueryRow(
		"SELECT hash FROM audit_events WHERE tenant_id = ? AND seq < ? ORDER BY seq DESC LIMIT 1",
		tenant.String(), fromSeq,
	).Scan(&prev); err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	q := "SELECT " + columnList(auditColumns) + " FROM audit_events WHERE tenant_id = ? AND seq >= ? ORDER BY seq"
	rows, err := db.Query(q, tenant.String(), fromSeq)
	if err != nil {
		t.Fatal(err)
	}
	var stored []storedAuditRow
	for rows.Next() {
		ev, meta, _, err := scanAudit(rows)
		if err != nil {
			t.Fatal(err)
		}
		stored = append(stored, storedAuditRow{ev: ev, meta: meta})
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	for i := range stored {
		ev := &stored[i].ev
		ev.PrevHash = prev
		ev.Hash = mustEventHash(t, canon.Event{
			TenantID: ev.TenantID.String(), Seq: ev.Seq, OccurredAt: ev.OccurredAt.String(),
			Actor: ev.Actor, ActorKind: ev.ActorKind, Action: ev.Action,
			TargetKind: string(ev.TargetKind), TargetID: ev.TargetID.String(),
			// The row's OWN rule, as the decoder resolved it — never MetaDigest picked
			// here. A helper that re-hashed everything under the unblinded rule would
			// leave a hash-broken chain, and the adversarial tests built on it would
			// pass only because Verify checks gap semantics before the hash: they would
			// stop proving that tamper detection works at all.
			MetaCommitment: ev.MetaCommitment, PayloadHash: ev.PayloadHash, PrevHash: ev.PrevHash,
		})
		if _, err := db.Exec("UPDATE audit_events SET prev_hash = ?, hash = ? WHERE tenant_id = ? AND seq = ?",
			ev.PrevHash, ev.Hash, tenant.String(), ev.Seq); err != nil {
			t.Fatal(err)
		}
		prev = ev.Hash
	}
	if len(stored) > 0 {
		last := stored[len(stored)-1].ev
		if _, err := db.Exec("UPDATE audit_heads SET seq = ?, hash = ? WHERE tenant_id = ?", last.Seq, last.Hash, tenant.String()); err != nil {
			t.Fatal(err)
		}
	}
	// POSTCONDITION, checked ROW BY ROW rather than through Verify. This helper's
	// purpose is to hand the caller a structurally consistent chain so the caller's
	// SEMANTIC mutation is what the assertion catches. Verify cannot police that
	// here: it reports gap semantics BEFORE the hash, so a helper that silently
	// left every row hash-broken — which is exactly what re-hashing a blinded row
	// under the legacy rule does — would still let the subtests pass while proving
	// nothing about tamper detection.
	for _, row := range readTenantAuditRows(t, st, tenant) {
		if row.ev.Seq < fromSeq {
			continue
		}
		want := mustEventHash(t, canon.Event{
			TenantID: row.ev.TenantID.String(), Seq: row.ev.Seq, OccurredAt: row.ev.OccurredAt.String(),
			Actor: row.ev.Actor, ActorKind: row.ev.ActorKind, Action: row.ev.Action,
			TargetKind: string(row.ev.TargetKind), TargetID: row.ev.TargetID.String(),
			MetaCommitment: row.ev.MetaCommitment, PayloadHash: row.ev.PayloadHash, PrevHash: row.ev.PrevHash,
		})
		if !bytesEqual(want, row.ev.Hash) {
			t.Fatalf("rewriteAuditChainFrom left seq %d hash-broken: it must re-seal each row under that row's OWN commitment rule, not one picked at the call site", row.ev.Seq)
		}
	}
}

func requireVerifyBreak(t *testing.T, st store.Store, tenant model.TenantID, breakAt int64, reason string) {
	t.Helper()
	ctx := context.Background()
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		report, err := sc.Audit().Verify(ctx, 1)
		if err == nil && (report.OK || report.BreakAt != breakAt || report.Reason != reason) {
			t.Fatalf("verify = %+v, want break at %d reason %q", report, breakAt, reason)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// mustEventHash hashes a preimage the test expects to be well-formed. EventHash
// refuses a malformed event, and a test helper that swallowed that error would
// re-seal rows with an empty hash and prove nothing.
func mustEventHash(t *testing.T, e canon.Event) []byte {
	t.Helper()
	h, err := canon.EventHash(e)
	if err != nil {
		t.Fatalf("EventHash on a well-formed preimage: %v", err)
	}
	return h
}
