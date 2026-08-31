// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/olivaresai/olivares/core/internal/store/canon"
	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// appendN appends n events to a tenant's chain inside one transaction.
func appendN(t *testing.T, st store.Store, tenant model.TenantID, n int) {
	t.Helper()
	ctx := context.Background()
	err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		for i := 0; i < n; i++ {
			if _, err := sc.Audit().Append(ctx, model.AuditDraft{
				Actor: "user:1", ActorKind: model.ActorUser, Action: "agent.update",
				Meta: map[string]any{"i": i},
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("append %d events: %v", n, err)
	}
}

func TestAuditAppendAndVerify(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "acme") // seeds seq 1
	appendN(t, st, tenant, 5)                // seq 2..6

	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		rep, err := sc.Audit().Verify(ctx, 1)
		if err != nil {
			return err
		}
		if !rep.OK || rep.BreakAt != 0 || rep.Checked != 6 {
			t.Fatalf("verify = %+v, want OK with 6 checked", rep)
		}
		// Walk yields events in sequence order.
		var seqs []int64
		if err := sc.Audit().Walk(ctx, 1, func(ev model.AuditEvent) error {
			seqs = append(seqs, ev.Seq)
			return nil
		}); err != nil {
			return err
		}
		for i, s := range seqs {
			if s != int64(i+1) {
				t.Fatalf("walk seq[%d] = %d, want %d", i, s, i+1)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("verify view: %v", err)
	}
}

// TestAuditVerifyEmptyLedgerNotVerified prevents vacuous integrity evidence: a
// verifier that examined no events has proved nothing and must never report OK.
func TestAuditVerifyEmptyLedgerNotVerified(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := model.NewTenantID() // deliberately not provisioned: no seeded event

	var rep store.VerifyReport
	if err := st.System(ctx, func(sys store.SystemScope) error {
		var err error
		rep, err = sys.Verify(ctx, tenant, 0)
		return err
	}); err != nil {
		t.Fatalf("verify empty ledger: %v", err)
	}
	if rep.OK || rep.Checked != 0 || rep.Reason != "no-events" {
		t.Fatalf("empty verify = %+v, want OK=false Checked=0 Reason=no-events", rep)
	}
}

// TestAuditWalkCanonical proves the store.CanonicalWalker capability: WalkCanonical yields the same events as Walk PLUS the stored canonical
// meta string, and that string is the authoritative MetaDigest input — for every
// event, re-deriving canon.EventHash from the yielded fields reproduces the
// stored chain hash, which is exactly what an offline archive verifier does.
func TestAuditWalkCanonical(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "acme") // seq 1 (org.create)
	appendN(t, st, tenant, 4)                // seq 2..5, meta {"i": n}

	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		cw, ok := sc.Audit().(store.CanonicalWalker)
		if !ok {
			t.Fatal("sqlstore audit log does not expose store.CanonicalWalker")
		}
		var seqs []int64
		if err := cw.WalkCanonical(ctx, 1, func(ev model.AuditEvent, metaCanonical string, metaBlind []byte) error {
			seqs = append(seqs, ev.Seq)
			if metaCanonical == "" {
				t.Fatalf("seq %d: empty canonical meta (the store always writes at least {})", ev.Seq)
			}
			if ev.Seq > 1 && !strings.Contains(metaCanonical, `"i"`) {
				t.Fatalf("seq %d: canonical meta lost the caller's key: %q", ev.Seq, metaCanonical)
			}
			commitment, cerr := canon.MetaCommitmentFor(metaBlind, metaCanonical)
			if cerr != nil {
				t.Fatalf("seq %d: the walk yielded an illegal blind: %v", ev.Seq, cerr)
			}
			want := mustEventHash(t, canon.Event{
				TenantID:   ev.TenantID.String(),
				Seq:        ev.Seq,
				OccurredAt: ev.OccurredAt.String(),
				Actor:      ev.Actor,
				ActorKind:  ev.ActorKind,
				Action:     ev.Action,
				TargetKind: string(ev.TargetKind),
				TargetID:   ev.TargetID.String(),
				// Resolved from the blind the walk hands over, not by picking a rule
				// here: that is the whole point of carrying the blind through the
				// canonical walk, and computing MetaDigest directly would silently
				// pass on a pre-blind record while failing on every new one.
				MetaCommitment: commitment,
				PayloadHash:    ev.PayloadHash,
				PrevHash:       ev.PrevHash,
			})
			if !bytesEqual(want, ev.Hash) {
				t.Fatalf("seq %d: hash does not re-derive from the canonical walk", ev.Seq)
			}
			return nil
		}); err != nil {
			return err
		}
		for i, s := range seqs {
			if s != int64(i+1) {
				t.Fatalf("walk seq[%d] = %d, want %d", i, s, i+1)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("canonical walk: %v", err)
	}
}

func TestAuditReadVerifiedAnchorIsBoundedAndCanonical(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "audit-anchor.db")
	st := openFileStore(t, dsn)
	tenant := provisionTenant(t, st, "audit-anchor")
	appendN(t, st, tenant, 5) // seq 2..6
	if err := st.Close(); err != nil {
		t.Fatalf("close audit-anchor seed store: %v", err)
	}

	tamper := func(sequence int64) {
		t.Helper()
		raw, err := sql.Open("sqlite", dsn)
		if err != nil {
			t.Fatalf("open raw audit-anchor database: %v", err)
		}
		defer raw.Close()
		if _, err := raw.Exec("DROP TRIGGER IF EXISTS audit_events_no_update"); err != nil {
			t.Fatalf("drop audit immutability trigger: %v", err)
		}
		if _, err := raw.Exec(
			"UPDATE audit_events SET action = 'tampered' WHERE tenant_id = ? AND seq = ?",
			tenant.String(), sequence,
		); err != nil {
			t.Fatalf("tamper audit seq %d: %v", sequence, err)
		}
	}
	tamper(6) // a later unrelated break must not poison an older exact receipt.

	read := func(wantError bool) {
		t.Helper()
		opened := openFileStore(t, dsn)
		defer opened.Close()
		err := opened.View(ctx, tenant, func(sc store.Scope) error {
			reader, ok := sc.Audit().(store.VerifiedAuditAnchorReader)
			if !ok {
				t.Fatal("sqlstore audit log lacks VerifiedAuditAnchorReader")
			}
			event, meta, found, readErr := reader.ReadVerifiedAuditAnchor(ctx, 3)
			if wantError {
				if readErr == nil {
					t.Fatalf("tampered exact anchor = %+v/%q/found=%v, want error", event, meta, found)
				}
				return nil
			}
			if readErr != nil || !found || event.Seq != 3 || !strings.Contains(meta, `"i":1`) {
				t.Fatalf("verified exact anchor = %+v/%q/found=%v, err=%v", event, meta, found, readErr)
			}
			_, _, missing, readErr := reader.ReadVerifiedAuditAnchor(ctx, 99)
			if readErr != nil || missing {
				t.Fatalf("missing exact anchor = found=%v, err=%v", missing, readErr)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("read exact audit anchor: %v", err)
		}
	}
	read(false)
	tamper(3)
	read(true)
}

// TestAuditMetaCarriesTraceContext is the OBS-03 ledger-correlation proof: an audit
// Append performed inside a request whose context carries a W3C span context stamps
// the trace_id/span_id into the event's minimal-data Meta (correlation, not payload),
// alongside the caller's own keys — so SIEM can join a ledger event to the
// caller's trace. It asserts at the DB level because the read path nils Meta (the
// canonical string is authoritative).
func TestAuditMetaCarriesTraceContext(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "trace.db")
	st := openFileStore(t, dsn)
	tenant := provisionTenant(t, st, "acme")

	const wantTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	traceID, _ := oteltrace.TraceIDFromHex(wantTraceID)
	spanID, _ := oteltrace.SpanIDFromHex("00f067aa0ba902b7")
	sc := oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
		TraceID: traceID, SpanID: spanID, TraceFlags: oteltrace.FlagsSampled,
	})
	tctx := oteltrace.ContextWithSpanContext(ctx, sc)

	if err := st.Mutate(tctx, tenant, func(scope store.Scope) error {
		_, err := scope.Audit().Append(tctx, model.AuditDraft{
			Actor: "user:1", ActorKind: model.ActorUser, Action: "agent.update",
			Meta: map[string]any{"k": "v"},
		})
		return err
	}); err != nil {
		t.Fatalf("append in span ctx: %v", err)
	}
	_ = st.Close()

	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	var metaStr string
	row := raw.QueryRow("SELECT meta FROM audit_events WHERE tenant_id = ? ORDER BY seq DESC LIMIT 1", tenant.String())
	if err := row.Scan(&metaStr); err != nil {
		t.Fatalf("read meta: %v", err)
	}
	if !strings.Contains(metaStr, wantTraceID) {
		t.Errorf("ledger meta missing the trace_id %q: %q", wantTraceID, metaStr)
	}
	if !strings.Contains(metaStr, `"trace_id"`) || !strings.Contains(metaStr, `"span_id"`) {
		t.Errorf("ledger meta missing trace_id/span_id keys: %q", metaStr)
	}
	if !strings.Contains(metaStr, `"v"`) {
		t.Errorf("ledger meta dropped the caller's own key (minimal-data must be additive): %q", metaStr)
	}
}

// openFileStore opens a file-backed SQLite store (needed for tamper tests, which
// modify the database through a second connection).
func openFileStore(t *testing.T, dsn string) store.Store {
	t.Helper()
	st, err := Open(context.Background(), store.Config{Engine: store.EngineSQLite, DSN: dsn}, nil)
	if err != nil {
		t.Fatalf("open file store: %v", err)
	}
	return st
}

func TestAuditTamperHashMismatch(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "audit.db")

	st := openFileStore(t, dsn)
	tenant := provisionTenant(t, st, "acme")
	appendN(t, st, tenant, 5)
	_ = st.Close()

	// Simulate an attacker with raw DB access: drop the immutability trigger and
	// silently alter a row. The hash chain must still detect it.
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec("DROP TRIGGER audit_events_no_update"); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
	if _, err := raw.Exec("UPDATE audit_events SET action = 'tampered' WHERE tenant_id = ? AND seq = 3", tenant.String()); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	_ = raw.Close()

	st2 := openFileStore(t, dsn)
	defer st2.Close()
	var rep store.VerifyReport
	if err := st2.System(ctx, func(sys store.SystemScope) error {
		var err error
		rep, err = sys.Verify(ctx, tenant, 1)
		return err
	}); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if rep.OK || rep.BreakAt != 3 || rep.Reason != "hash-mismatch" {
		t.Fatalf("tamper verify = %+v, want break at 3 hash-mismatch", rep)
	}
}

func TestAuditTamperSeqGap(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "audit.db")

	st := openFileStore(t, dsn)
	tenant := provisionTenant(t, st, "acme")
	appendN(t, st, tenant, 5)
	_ = st.Close()

	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec("DROP TRIGGER audit_events_no_delete"); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
	if _, err := raw.Exec("DELETE FROM audit_events WHERE tenant_id = ? AND seq = 3", tenant.String()); err != nil {
		t.Fatalf("delete: %v", err)
	}
	_ = raw.Close()

	st2 := openFileStore(t, dsn)
	defer st2.Close()
	var rep store.VerifyReport
	if err := st2.System(ctx, func(sys store.SystemScope) error {
		var err error
		rep, err = sys.Verify(ctx, tenant, 1)
		return err
	}); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if rep.OK || rep.BreakAt != 3 || rep.Reason != "seq-gap" {
		t.Fatalf("gap verify = %+v, want break at 3 seq-gap", rep)
	}
}

// TestAuditTailTruncationDetected proves Verify catches deletion of the most
// recent events — which leaves no sequence gap — via the per-tenant head
// checkpoint.
func TestAuditTailTruncationDetected(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "audit.db")

	st := openFileStore(t, dsn)
	tenant := provisionTenant(t, st, "acme") // seq 1
	appendN(t, st, tenant, 5)                // seq 2..6
	_ = st.Close()

	// Attacker drops the immutability trigger and deletes the tail (seq 6). No
	// gap is left, but the head still records seq 6.
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec("DROP TRIGGER audit_events_no_delete"); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
	if _, err := raw.Exec("DELETE FROM audit_events WHERE tenant_id = ? AND seq = 6", tenant.String()); err != nil {
		t.Fatalf("truncate tail: %v", err)
	}
	_ = raw.Close()

	st2 := openFileStore(t, dsn)
	defer st2.Close()
	var rep store.VerifyReport
	if err := st2.System(ctx, func(sys store.SystemScope) error {
		var err error
		rep, err = sys.Verify(ctx, tenant, 1)
		return err
	}); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if rep.OK || rep.Reason != "tail-truncated" || rep.BreakAt != 6 {
		t.Fatalf("tail verify = %+v, want break at 6 tail-truncated", rep)
	}
}

// TestAuditAppendOnlyEnforced proves the database itself rejects update/delete on
// the ledger, independent of the application layer.
func TestAuditAppendOnlyEnforced(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "acme")
	appendN(t, st, tenant, 1)

	ss := st.(*sqlStore)
	tx, err := ss.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck
	if err := ss.dia.BindTenant(ctx, tx, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE audit_events SET action='x' WHERE tenant_id=?", tenant.String()); err == nil ||
		!strings.Contains(strings.ToLower(err.Error()), "append-only") {
		t.Fatalf("update audit: err = %v, want append-only", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM audit_events WHERE tenant_id=?", tenant.String()); err == nil ||
		!strings.Contains(strings.ToLower(err.Error()), "append-only") {
		t.Fatalf("delete audit: err = %v, want append-only", err)
	}
}

// spliceLegacyRow appends one row the way a release BEFORE metadata blinding would
// have written it: no stored blind, and a chain hash computed under the unblinded
// rule. It writes through the raw handle on purpose — the point is to produce a row
// today's Append can no longer produce, which is exactly what an upgraded database
// contains.
func spliceLegacyRow(t *testing.T, st store.Store, tenant model.TenantID, meta string) {
	t.Helper()
	ctx := context.Background()
	ss := st.(*sqlStore)
	var lastSeq int64
	var prevHash []byte
	if err := ss.db.QueryRowContext(ctx,
		ss.dia.Rebind("SELECT seq, hash FROM "+auditHeadsTable+" WHERE tenant_id = ?"),
		tenant.String()).Scan(&lastSeq, &prevHash); err != nil {
		t.Fatalf("read head: %v", err)
	}
	occurred := model.NewTimestamp(time.Unix(1700000000, 0).UTC()).String()
	seq := lastSeq + 1
	hash := mustEventHash(t, canon.Event{
		TenantID: tenant.String(), Seq: seq, OccurredAt: occurred,
		Actor: "user:legacy", ActorKind: model.ActorUser, Action: "agent.update",
		TargetKind: "", TargetID: "",
		MetaCommitment: canon.MetaDigest(meta), // the pre-blinding rule
		PayloadHash:    nil, PrevHash: prevHash,
	})
	if _, err := ss.db.ExecContext(ctx, ss.dia.Rebind(
		"INSERT INTO "+auditTable+" (id, tenant_id, seq, occurred_at, actor, actor_kind, action,"+
			" target_kind, target_id, meta, meta_blind, payload_hash, prev_hash, hash, sig)"+
			" VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)"),
		model.NewID().String(), tenant.String(), seq, occurred, "user:legacy", model.ActorUser,
		"agent.update", "", "", meta, nil, nil, prevHash, hash, nil); err != nil {
		t.Fatalf("splice legacy row: %v", err)
	}
	if _, err := ss.db.ExecContext(ctx, ss.dia.Rebind(
		"UPDATE "+auditHeadsTable+" SET seq = ?, hash = ? WHERE tenant_id = ?"),
		seq, hash, tenant.String()); err != nil {
		t.Fatalf("advance head: %v", err)
	}
}

// TestChainVerifiesAcrossBothMetadataRules is the load-bearing test for keeping the
// unblinded rule alive forever. A ledger upgraded across the change contains rows
// sealed under BOTH rules, and it must verify end to end. Applying the new rule
// retroactively would make every pre-blinding row fail — a legitimate history
// reported as tampering, which is the failure this ledger exists to prevent.
func TestChainVerifiesAcrossBothMetadataRules(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "acme") // seq 1, blinded by today's Append
	appendN(t, st, tenant, 2)                // seq 2..3, blinded
	spliceLegacyRow(t, st, tenant, `{"legacy":true}`)
	appendN(t, st, tenant, 1) // one more blinded row ON TOP of the legacy one

	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		rep, verr := sc.Audit().Verify(ctx, 1)
		if verr != nil {
			return verr
		}
		if !rep.OK {
			t.Fatalf("a mixed-generation chain must verify: %+v", rep)
		}
		if rep.Checked < 5 {
			t.Fatalf("verify checked %d events, want at least 5", rep.Checked)
		}
		return nil
	}); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

// TestVerifyConsultsTheStoredBlind proves the discriminator is actually read rather
// than assumed: a row with NO stored blind whose hash was computed under the BLINDED
// rule must FAIL. Without this, a Verify that always used the legacy rule — or always
// the new one — would pass the test above for the wrong reason.
func TestVerifyConsultsTheStoredBlind(t *testing.T) {
	ctx := context.Background()
	st := openSQLiteTest(t, nil)
	tenant := provisionTenant(t, st, "acme")
	ss := st.(*sqlStore)
	tamperTx, err := ss.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tamper transaction: %v", err)
	}
	defer tamperTx.Rollback() //nolint:errcheck // no-op after the explicit commit below
	if err := ss.dia.BindTenant(ctx, tamperTx, tenant); err != nil {
		t.Fatalf("bind tamper transaction: %v", err)
	}

	var lastSeq int64
	var prevHash []byte
	if err := tamperTx.QueryRowContext(ctx,
		ss.dia.Rebind("SELECT seq, hash FROM "+auditHeadsTable+" WHERE tenant_id = ?"),
		tenant.String()).Scan(&lastSeq, &prevHash); err != nil {
		t.Fatalf("read head: %v", err)
	}
	meta := `{"mismatched":true}`
	occurred := model.NewTimestamp(time.Unix(1700000000, 0).UTC()).String()
	seq := lastSeq + 1
	// Hash under the BLINDED rule while storing NO blind: the row claims a rule its
	// own columns do not support.
	blind := make([]byte, canon.BlindLen)
	for i := range blind {
		blind[i] = 0x5A
	}
	hash := mustEventHash(t, canon.Event{
		TenantID: tenant.String(), Seq: seq, OccurredAt: occurred,
		Actor: "user:legacy", ActorKind: model.ActorUser, Action: "agent.update",
		MetaCommitment: canon.MetaCommitment(blind, meta),
		PrevHash:       prevHash,
	})
	if _, err := tamperTx.ExecContext(ctx, ss.dia.Rebind(
		"INSERT INTO "+auditTable+" (id, tenant_id, seq, occurred_at, actor, actor_kind, action,"+
			" target_kind, target_id, meta, meta_blind, payload_hash, prev_hash, hash, sig)"+
			" VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)"),
		model.NewID().String(), tenant.String(), seq, occurred, "user:legacy", model.ActorUser,
		"agent.update", "", "", meta, nil, nil, prevHash, hash, nil); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := tamperTx.ExecContext(ctx, ss.dia.Rebind(
		"UPDATE "+auditHeadsTable+" SET seq = ?, hash = ? WHERE tenant_id = ?"),
		seq, hash, tenant.String()); err != nil {
		t.Fatalf("advance head: %v", err)
	}
	if err := tamperTx.Commit(); err != nil {
		t.Fatalf("commit tamper transaction: %v", err)
	}
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		rep, verr := sc.Audit().Verify(ctx, 1)
		if verr != nil {
			return verr
		}
		if rep.OK {
			t.Fatal("a row hashed under the blinded rule with no stored blind must NOT verify")
		}
		if rep.Reason != "hash-mismatch" {
			t.Fatalf("break reason = %q, want hash-mismatch", rep.Reason)
		}
		return nil
	}); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

// openBlindingTest opens an in-memory ledger with an explicit metadata-commitment
// write mode.
func openBlindingTest(t *testing.T, mode store.AuditBlindingMode) store.Store {
	t.Helper()
	st, err := Open(context.Background(), store.Config{
		Engine: store.EngineSQLite, DSN: ":memory:", Debug: true,
		AuditMetaBlinding: mode,
	}, nil)
	if err != nil {
		t.Fatalf("open store (%q): %v", mode, err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// storedBlindLengths returns the byte length of each row's stored blind, in
// sequence order, reading the column directly so the test observes what was
// WRITTEN rather than what a resolver later derives.
func storedBlindLengths(t *testing.T, st store.Store, tenant model.TenantID) []int {
	t.Helper()
	ss := st.(*sqlStore)
	rows, err := ss.db.QueryContext(context.Background(), ss.dia.Rebind(
		"SELECT meta_blind FROM "+auditTable+" WHERE tenant_id = ? ORDER BY seq ASC"), tenant.String())
	if err != nil {
		t.Fatalf("read blinds: %v", err)
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			t.Fatalf("scan blind: %v", err)
		}
		out = append(out, len(b))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("blind rows: %v", err)
	}
	return out
}

// TestBlindingWriteGateIsExplicitAndAutoIsSafe pins the decoupling of WRITING the
// blinded rule from DEPLOYING the binary that can read it. Sealing one blinded row
// makes every node still running the previous binary report that row as a hash
// mismatch — a legitimate history denounced as forged — so the write side has to be
// a decision an operator takes AFTER the fleet is upgraded, not a side effect of
// deploying. The empty value is resolved from the ledger's own state: a ledger with
// no events has nothing to strand, so it starts blinded.
func TestBlindingWriteGateIsExplicitAndAutoIsSafe(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mode      store.AuditBlindingMode
		wantBlind bool
	}{
		{"auto on a fresh ledger blinds", store.AuditBlindingAuto, true},
		{"explicitly on blinds", store.AuditBlindingOn, true},
		{"explicitly off keeps the legacy rule", store.AuditBlindingOff, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := openBlindingTest(t, tc.mode)
			tenant := provisionTenant(t, st, "acme")
			appendN(t, st, tenant, 2)
			for i, n := range storedBlindLengths(t, st, tenant) {
				if tc.wantBlind && n != canon.BlindLen {
					t.Fatalf("row %d: stored blind is %d bytes, want %d", i, n, canon.BlindLen)
				}
				if !tc.wantBlind && n != 0 {
					t.Fatalf("row %d: stored blind is %d bytes, want none — the gate is closed", i, n)
				}
			}
			if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
				rep, verr := sc.Audit().Verify(context.Background(), 1)
				if verr != nil {
					return verr
				}
				if !rep.OK {
					t.Fatalf("chain must verify under either rule: %+v", rep)
				}
				return nil
			}); err != nil {
				t.Fatalf("verify: %v", err)
			}
		})
	}
}

// TestAnInterleavedChainVerifiesForever is the property that makes the gate safe
// rather than merely cautious: during a rollout two nodes may seal under different
// rules into the SAME chain, and that chain must verify end to end — now and for as
// long as the ledger exists. It holds because the rule is a property of each ROW
// (its stored blind, or its absence), never of the reader.
func TestAnInterleavedChainVerifiesForever(t *testing.T) {
	ctx := context.Background()
	st := openBlindingTest(t, store.AuditBlindingOff)
	tenant := provisionTenant(t, st, "acme") // seq 1, unblinded
	appendN(t, st, tenant, 2)                // seq 2..3, unblinded

	// The operator flips the gate mid-life, exactly as a rollout does.
	ss := st.(*sqlStore)
	ss.blindMeta = true
	appendN(t, st, tenant, 2) // seq 4..5, blinded
	ss.blindMeta = false
	appendN(t, st, tenant, 1) // seq 6, unblinded again (a lagging node)

	lens := storedBlindLengths(t, st, tenant)
	var blinded, plain int
	for _, n := range lens {
		if n == canon.BlindLen {
			blinded++
		} else {
			plain++
		}
	}
	if blinded == 0 || plain == 0 {
		t.Fatalf("the fixture must interleave both rules, got %v", lens)
	}
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		rep, verr := sc.Audit().Verify(ctx, 1)
		if verr != nil {
			return verr
		}
		if !rep.OK {
			t.Fatalf("an interleaved chain must verify end to end: %+v (blinds %v)", rep, lens)
		}
		if rep.Checked != int64(len(lens)) {
			t.Fatalf("verify checked %d of %d rows", rep.Checked, len(lens))
		}
		return nil
	}); err != nil {
		t.Fatalf("verify: %v", err)
	}
}

// openBlindingTestAt opens a store on a FILE database so the same ledger can be
// reopened under a different mode — a rollout is a sequence of boots against one
// database, and an in-memory store cannot express that.
func openBlindingTestAt(t *testing.T, dsn string, mode store.AuditBlindingMode) store.Store {
	t.Helper()
	st, err := Open(context.Background(), store.Config{
		Engine: store.EngineSQLite, DSN: dsn, Debug: true,
		AuditMetaBlinding: mode,
	}, nil)
	if err != nil {
		t.Fatalf("open store (%q): %v", mode, err)
	}
	return st
}

// readBlindingActuated reports the ledger's recorded actuation.
func readBlindingActuated(t *testing.T, st store.Store) int64 {
	t.Helper()
	ss := st.(*sqlStore)
	var n int64
	if err := ss.db.QueryRowContext(context.Background(), ss.dia.Rebind(
		"SELECT actuated FROM "+dialect.AuditBlindingStateTable+" WHERE id = 1")).Scan(&n); err != nil {
		t.Fatalf("read blinding actuation: %v", err)
	}
	_ = n
	return n
}

// setBlindingActuated forces the recorded actuation, standing in for a ledger
// that was accumulating rows before the blind column existed. The seed rule that
// produces this state on a real upgrade is covered by
// TestBlindingSeedFollowsWhetherTheLedgerPredatesTheColumn.
func setBlindingActuated(t *testing.T, st store.Store, v int64) {
	t.Helper()
	ss := st.(*sqlStore)
	if _, err := ss.db.ExecContext(context.Background(), ss.dia.Rebind(
		"UPDATE "+dialect.AuditBlindingStateTable+" SET default_blinded = ?, actuated = ? WHERE id = 1"), v, v); err != nil {
		t.Fatalf("set blinding state: %v", err)
	}
}

// TestAutoNeverActuatesAPreUpgradeLedgerAndNeverRegressesAnActuatedOne walks the
// rollout the write gate exists for, as a sequence of boots against ONE database.
//
// It pins the two directions that matter, and the second is the one that is easy
// to miss. A ledger that predates blinding must keep writing the legacy rule
// under the DEFAULT mode, because sealing one blinded row makes every node still
// running the old binary denounce a legitimate history as forged. And once an
// operator HAS actuated, a node that later boots with the default must not fall
// back to the legacy rule: that would be a security downgrade delivered by a
// routine deploy, silent by construction, since the resulting interleaved chain
// still verifies end to end and so nothing looks wrong.
func TestAutoNeverActuatesAPreUpgradeLedgerAndNeverRegressesAnActuatedOne(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "rollout.db")

	// A ledger that predates the blind column: recorded as not actuated.
	st := openBlindingTestAt(t, dsn, store.AuditBlindingAuto)
	tenant := provisionTenant(t, st, "acme")
	setBlindingActuated(t, st, 0)
	// Provisioning already sealed rows on what was, at that moment, a fresh and
	// therefore actuated ledger. Only rows written from here on are evidence about
	// the gate, so the assertions below are scoped past them.
	preexisting := len(storedBlindLengths(t, st, tenant))
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Boot 1 — the DEFAULT mode on that ledger must NOT start blinding.
	st = openBlindingTestAt(t, dsn, store.AuditBlindingAuto)
	appendN(t, st, tenant, 2)
	written := storedBlindLengths(t, st, tenant)
	if len(written) <= preexisting {
		t.Fatalf("boot 1 appended nothing: %d rows before, %d after", preexisting, len(written))
	}
	for i, n := range written[preexisting:] {
		if n != 0 {
			t.Fatalf("row %d after the upgrade: auto blinded a pre-upgrade ledger (%d bytes); deploying is not actuating", i, n)
		}
	}
	if got := readBlindingActuated(t, st); got != 0 {
		t.Fatalf("auto recorded an actuation it never performed: actuated=%d", got)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Boot 2 — the explicit operator act, which is what actuates and is recorded.
	st = openBlindingTestAt(t, dsn, store.AuditBlindingOn)
	appendN(t, st, tenant, 2)
	if got := readBlindingActuated(t, st); got != 1 {
		t.Fatalf("an explicit on did not record the actuation: actuated=%d", got)
	}
	blinds := storedBlindLengths(t, st, tenant)
	if n := blinds[len(blinds)-1]; n != canon.BlindLen {
		t.Fatalf("explicit on did not blind: last row's blind is %d bytes, want %d", n, canon.BlindLen)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Boot 3 — the regression guard: back on the DEFAULT, the ledger stays blinded.
	st = openBlindingTestAt(t, dsn, store.AuditBlindingAuto)
	appendN(t, st, tenant, 2)
	blinds = storedBlindLengths(t, st, tenant)
	if n := blinds[len(blinds)-1]; n != canon.BlindLen {
		t.Fatalf("a deploy with the default mode regressed an ACTUATED ledger to the legacy rule: last row's blind is %d bytes, want %d", n, canon.BlindLen)
	}
	// And the interleaved history — legacy rows, then blinded ones — still verifies.
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		rep, verr := sc.Audit().Verify(context.Background(), 1)
		if verr != nil {
			return verr
		}
		if !rep.OK {
			t.Fatalf("the chain written across the rollout must verify: %+v", rep)
		}
		return nil
	}); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// TestBlindingSeedFollowsWhetherTheLedgerPredatesTheColumn pins the seed rule at
// its source: the decision is taken once, from whether the blind column had to be
// ADDED, and it is never restated on a later boot.
func TestBlindingSeedFollowsWhetherTheLedgerPredatesTheColumn(t *testing.T) {
	for _, tc := range []struct {
		name   string
		legacy bool
		want   int64
	}{
		{"a ledger gaining the column starts un-actuated", true, 0},
		{"a ledger created with the column starts actuated", false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := openBlindingTest(t, store.AuditBlindingOff)
			ss := st.(*sqlStore)
			ctx := context.Background()
			if _, err := ss.db.ExecContext(ctx, "DELETE FROM "+dialect.AuditBlindingStateTable); err != nil {
				t.Fatalf("clear seed: %v", err)
			}
			if err := reconcileAuditBlindingState(ctx, ss.db, ss.dia, tc.legacy); err != nil {
				t.Fatalf("seed: %v", err)
			}
			if got := readBlindingDefault(t, st); got != tc.want {
				t.Fatalf("seed for legacy=%v recorded default_blinded=%d, want %d", tc.legacy, got, tc.want)
			}
			if got := readBlindingActuated(t, st); got != 0 {
				t.Fatalf("the seed must never pre-actuate a ledger, got actuated=%d", got)
			}
			// A second reconcile must NOT restate it: the record is the ledger's
			// history of the decision, and re-deriving it on every boot is exactly the
			// "a deploy changed the rule" failure the setting exists to prevent.
			if err := reconcileAuditBlindingState(ctx, ss.db, ss.dia, !tc.legacy); err != nil {
				t.Fatalf("second seed: %v", err)
			}
			if got := readBlindingDefault(t, st); got != tc.want {
				t.Fatalf("a later boot restated the ledger's rule: default_blinded=%d, want %d", got, tc.want)
			}
		})
	}
}

// TestTheDatabaseRefusesAMalformedBlind is the AT-REST half of the discriminator.
// A blind is legal only as NULL or exactly BlindLen bytes; a zero-length blob is
// the illegal third state, and it is the dangerous one, because it is len()==0 in
// Go exactly like a NULL and would otherwise be hashed under the LEGACY rule while
// the column claims the row is blinded.
//
// Scope, stated honestly: this constraint is in the CREATE TABLE, so it covers
// every freshly created database of either engine, and an existing Postgres ledger
// gains it at boot when the role owns the table. An existing SQLite ledger never
// gains it — SQLite cannot add a CHECK to an existing table, and rebuilding an
// append-only ledger is not an acceptable automated operation — so there the
// in-flight refusal below is the whole guarantee.
func TestTheDatabaseRefusesAMalformedBlind(t *testing.T) {
	st := openBlindingTest(t, store.AuditBlindingOn)
	tenant := provisionTenant(t, st, "acme")
	appendN(t, st, tenant, 1)
	ss := st.(*sqlStore)
	ctx := context.Background()
	for _, n := range []int{0, 1, canon.BlindLen - 1, canon.BlindLen + 1} {
		_, err := ss.db.ExecContext(ctx, ss.dia.Rebind(
			"UPDATE "+auditTable+" SET meta_blind = ? WHERE tenant_id = ?"),
			make([]byte, n), tenant.String())
		if err == nil {
			t.Fatalf("the database accepted a %d-byte blind; NULL or %d are the only legal states", n, canon.BlindLen)
		}
	}
}

// TestTheScannerRefusesAMalformedBlind is the IN-FLIGHT half, and the one that
// still holds on a database whose engine could not be given the constraint. The
// row never becomes an in-memory event, so no downstream path has to remember to
// check it, and the error carries the sequence so Verify can NAME the break rather
// than report the ledger as merely unavailable.
func TestTheScannerRefusesAMalformedBlind(t *testing.T) {
	for _, n := range []int{0, 1, canon.BlindLen - 1, canon.BlindLen + 1} {
		row := &stubAuditRow{seq: 7, blind: make([]byte, n)}
		_, _, _, err := scanAudit(row)
		if err == nil {
			t.Fatalf("the scanner accepted a %d-byte blind", n)
		}
		if !errors.Is(err, canon.ErrMalformedBlind) {
			t.Fatalf("a %d-byte blind must report ErrMalformedBlind, got %v", n, err)
		}
		var bad malformedRowErr
		if !errors.As(err, &bad) || bad.seq != 7 {
			t.Fatalf("the error must carry the sequence so the break can be named, got %v", err)
		}
	}
	// NULL stays legal: it is the legacy rule's discriminator, forever.
	if _, _, _, err := scanAudit(&stubAuditRow{seq: 7, blind: nil}); err != nil {
		t.Fatalf("a NULL blind is the legacy rule and must scan: %v", err)
	}
}

// stubAuditRow feeds scanAudit one synthetic row so the malformed-blind path can
// be reached on an engine whose CHECK constraint makes the state unwritable.
type stubAuditRow struct {
	seq   int64
	blind []byte
}

func (r *stubAuditRow) Scan(dest ...any) error {
	vals := []any{
		"019fa906-97c1-7dbc-ab1e-49d586a5567a", model.NewTenantID().String(), r.seq,
		model.NewTimestamp(time.Unix(1700000000, 0).UTC()).String(),
		"user:abc", model.ActorUser, "agent.update", "", "", "{}", r.blind,
		[]byte(nil), make([]byte, 32), make([]byte, 32), []byte(nil),
	}
	if len(dest) != len(vals) {
		return fmt.Errorf("stub row: %d destinations, have %d values", len(dest), len(vals))
	}
	for i, v := range vals {
		switch d := dest[i].(type) {
		case *string:
			*d = v.(string)
		case *int64:
			*d = v.(int64)
		case *[]byte:
			b, _ := v.([]byte)
			*d = b
		default:
			return fmt.Errorf("stub row: unsupported destination %T at %d", dest[i], i)
		}
	}
	return nil
}

// readBlindingDefault reports which rule this ledger records as its own.
func readBlindingDefault(t *testing.T, st store.Store) int64 {
	t.Helper()
	ss := st.(*sqlStore)
	var n int64
	if err := ss.db.QueryRowContext(context.Background(), ss.dia.Rebind(
		"SELECT default_blinded FROM "+dialect.AuditBlindingStateTable+" WHERE id = 1")).Scan(&n); err != nil {
		t.Fatalf("read blinding default: %v", err)
	}
	return n
}

// TestOffIsUsableOnAFreshLedgerButRefusedOnceActuated pins both edges of the
// fail-closed refusal, and the first edge is the one an over-eager guard breaks.
//
// A brand-new deployment that deliberately starts with blinding OFF is the
// legitimate case — a node joining a fleet that still runs binaries which cannot
// verify a blinded row — and it must start. What must NOT be allowed is returning
// a ledger that has already been writing blinded records to the legacy rule: those
// records are unaffected, but every NEW one would have a commitment that is again
// a deterministic function of its own metadata, and the resulting chain still
// verifies end to end, so nothing downstream would look wrong.
func TestOffIsUsableOnAFreshLedgerButRefusedOnceActuated(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "off.db")

	// A fresh ledger: "off" is a legitimate choice and must open.
	st := openBlindingTestAt(t, dsn, store.AuditBlindingOff)
	tenant := provisionTenant(t, st, "acme")
	appendN(t, st, tenant, 1)
	for i, n := range storedBlindLengths(t, st, tenant) {
		if n != 0 {
			t.Fatalf("row %d: off must seal under the legacy rule, got a %d-byte blind", i, n)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// The operator actuates.
	st = openBlindingTestAt(t, dsn, store.AuditBlindingOn)
	appendN(t, st, tenant, 1)
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Now "off" must refuse rather than quietly reopen the oracle for new records.
	_, err := Open(context.Background(), store.Config{
		Engine: store.EngineSQLite, DSN: dsn, Debug: true,
		AuditMetaBlinding: store.AuditBlindingOff,
	}, nil)
	if err == nil {
		t.Fatal("off on an actuated ledger opened the store; it must refuse")
	}
	if !strings.Contains(err.Error(), "already been actuated") {
		t.Fatalf("the refusal must name the reason, got: %v", err)
	}
}

// TestAnEntropylessBlindIsRefused pins the CSPRNG failure mode, which is a
// CONFIDENTIALITY failure and not an availability one.
//
// A blind of zeros makes the commitment a deterministic function of the metadata
// again, reopening for that record the exact confirmation oracle the blind exists
// to close — and nothing downstream would look wrong, because the chain still
// verifies. So the refusal is on the VALUE, not merely on the error: under Go 1.26
// crypto/rand does not report failure by returning an error, it kills the process,
// which leaves the error branch as a guard against a substituted reader rather than
// against the platform. Refusing costs nothing, since a real CSPRNG yields this
// with probability 2^-256.
func TestAnEntropylessBlindIsRefused(t *testing.T) {
	st := openBlindingTest(t, store.AuditBlindingOn)
	tenant := provisionTenant(t, st, "acme")

	// Substitute the source only AFTER setup: provisioning itself seals audit
	// events, and the guard rightly refuses those too.
	prev := rand.Reader
	rand.Reader = zeroReader{}
	t.Cleanup(func() { rand.Reader = prev })

	err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		_, aerr := sc.Audit().Append(context.Background(), model.AuditDraft{
			Actor: "user:abc", ActorKind: model.ActorUser, Action: "agent.update",
		})
		return aerr
	})
	if err == nil {
		t.Fatal("an event was sealed with an all-zero blind; the commitment is not hiding and the oracle is open for that record")
	}
	if !strings.Contains(err.Error(), "all-zero metadata blind") {
		t.Fatalf("the refusal must name the entropy failure, got: %v", err)
	}
}

func TestAuditRelationsIgnoreSQLiteTempShadows(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "audit-temp-shadows.db")
	initial := openSQLiteSpoolTest(t, store.Config{DSN: dsn})
	tenant := provisionTenant(t, initial, "audit-temp-shadows")
	if err := initial.Close(); err != nil {
		t.Fatalf("close initial audit store: %v", err)
	}
	st := openSQLiteSpoolTest(t, store.Config{
		DSN: dsn, AuditSpoolMaxBytes: 1, AuditSpoolOnFull: store.AuditSpoolDegrade,
	})
	ss := st.(*sqlStore)
	for _, statement := range []string{
		`CREATE TEMP TABLE audit_events (
id TEXT, tenant_id TEXT, seq INTEGER, occurred_at TEXT, actor TEXT, actor_kind TEXT,
action TEXT, target_kind TEXT, target_id TEXT, meta TEXT, meta_blind BLOB,
payload_hash BLOB, prev_hash BLOB, hash BLOB, sig BLOB)`,
		`CREATE TEMP TABLE audit_heads (
tenant_id TEXT PRIMARY KEY, seq INTEGER, hash BLOB)`,
		`CREATE TEMP TABLE audit_spool_usage (
id INTEGER PRIMARY KEY, bytes INTEGER NOT NULL)`,
		`CREATE TEMP TABLE audit_spool_gaps (
tenant_id TEXT PRIMARY KEY, dropped INTEGER NOT NULL, first_dropped_at TEXT NOT NULL)`,
	} {
		if _, err := ss.db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("create audit TEMP shadow: %v", err)
		}
	}

	var persisted model.AuditEvent
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var err error
		persisted, err = sc.Audit().Append(ctx, model.AuditDraft{
			Actor: model.ActorSystem, ActorKind: model.ActorSystem,
			Action: store.ActionAuditCheckpoint, Sig: []byte("test-checkpoint-signature"),
		})
		return err
	}); err != nil {
		t.Fatalf("append exempt event through TEMP shadows: %v", err)
	}
	if persisted.Seq <= 0 || len(persisted.Hash) != 32 {
		t.Fatalf("persisted exempt event = %+v", persisted)
	}
	var dropped model.AuditEvent
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var err error
		dropped, err = sc.Audit().Append(ctx, model.AuditDraft{
			Actor: "user:temp-shadow", ActorKind: model.ActorUser, Action: "agent.update",
		})
		return err
	}); err != nil {
		t.Fatalf("record degraded append through TEMP shadows: %v", err)
	}
	if dropped.Seq != 0 || !dropped.ID.IsZero() {
		t.Fatalf("over-budget append = %+v, want explicit zero event", dropped)
	}

	for _, table := range []string{
		auditTable, auditHeadsTable, auditSpoolUsageTable, auditSpoolGapsTable,
	} {
		var count int
		if err := ss.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM temp."+quoteIdent(table)).Scan(&count); err != nil {
			t.Fatalf("count TEMP %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("TEMP %s received %d diverted row(s)", table, count)
		}
	}
	var mainEvents, mainGaps int
	if err := ss.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM main.audit_events WHERE id = ?", persisted.ID.String()).
		Scan(&mainEvents); err != nil {
		t.Fatalf("read main audit event: %v", err)
	}
	if err := ss.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM main.audit_spool_gaps WHERE tenant_id = ? AND dropped = 1",
		tenant.String()).Scan(&mainGaps); err != nil {
		t.Fatalf("read main audit gap: %v", err)
	}
	var headSeq int64
	var headHash []byte
	if err := ss.db.QueryRowContext(ctx,
		"SELECT seq, hash FROM main.audit_heads WHERE tenant_id = ?", tenant.String()).
		Scan(&headSeq, &headHash); err != nil {
		t.Fatalf("read main audit head: %v", err)
	}
	if mainEvents != 1 || mainGaps != 1 || headSeq != persisted.Seq ||
		!bytes.Equal(headHash, persisted.Hash) {
		t.Fatalf("main audit projection events=%d gaps=%d head=%d/%x event=%d/%x",
			mainEvents, mainGaps, headSeq, headHash, persisted.Seq, persisted.Hash)
	}
}

// zeroReader stands in for a CSPRNG that is not delivering entropy.
type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}
