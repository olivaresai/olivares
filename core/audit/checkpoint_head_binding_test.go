// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package audit_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// CH1: a checkpoint signature must be bound to the predecessor the chain actually
// commits. Signer.Checkpoint reads the head, signs (tenant, headSeq, headHash) and
// then appends; the verifier derives the attested head from the appended event
// (Seq-1, PrevHash). If another writer advances the chain between the head read
// and the append, the append relinks to the newer tail and the committed
// checkpoint carries a signature over a head it does not follow.
//
// The tests below drive the real Signer.Checkpoint. The first group uses a store
// whose Custody hands out a scripted ledger, so the ORDER of lock, head, signer
// and append is observable and every mismatch can be produced exactly. The second
// group uses the real SQLite store and moves the head inside the same transaction,
// which is the actual-path witness that a refused binding rolls back.

// bindingJournal records what Signer.Checkpoint touched, in order.
type bindingJournal struct {
	calls []string
	sigs  [][]byte
}

func (j *bindingJournal) add(call string) { j.calls = append(j.calls, call) }

func (j *bindingJournal) count(call string) int {
	n := 0
	for _, c := range j.calls {
		if c == call {
			n++
		}
	}
	return n
}

// bindingCheckpointKey is an off-box checkpoint key that journals each signing,
// which is how a test proves the signer was or was not reached.
type bindingCheckpointKey struct {
	journal *bindingJournal
	priv    ed25519.PrivateKey
}

func (k *bindingCheckpointKey) SignCheckpoint(_ context.Context, preimage []byte) ([]byte, error) {
	k.journal.add("sign")
	sig := ed25519.Sign(k.priv, preimage)
	k.journal.sigs = append(k.journal.sigs, sig)
	return sig, nil
}

func (k *bindingCheckpointKey) Algorithm() audit.SigAlg { return audit.AlgEd25519 }

func (k *bindingCheckpointKey) KeyID() string { return "test-binding-key" }

func (k *bindingCheckpointKey) PublicKey(context.Context) ([]byte, error) {
	return k.priv.Public().(ed25519.PublicKey), nil
}

// bindingLog is a scripted AuditLog. It deliberately implements neither optional
// capability; the wrappers below add them one at a time.
type bindingLog struct {
	journal     *bindingJournal
	tenant      model.TenantID
	head        store.HeadRef
	hasHead     bool
	lockErr     error
	recorded    store.HeadRef
	hasRecorded bool
	// appended decides what Append reports. Nil reports the faithful successor of head.
	appended func(model.AuditDraft) (model.AuditEvent, error)
}

func (l *bindingLog) Append(_ context.Context, d model.AuditDraft) (model.AuditEvent, error) {
	l.journal.add("append")
	if l.appended != nil {
		return l.appended(d)
	}
	return model.AuditEvent{
		TenantID: l.tenant,
		Seq:      l.head.Seq + 1,
		PrevHash: bytes.Clone(l.head.Hash),
		Action:   d.Action,
		Sig:      d.Sig,
	}, nil
}

func (l *bindingLog) Verify(context.Context, int64) (store.VerifyReport, error) {
	l.journal.add("verify")
	return store.VerifyReport{}, errors.New("binding fixture: Verify is not part of the checkpoint path")
}

func (l *bindingLog) Walk(context.Context, int64, func(model.AuditEvent) error) error {
	l.journal.add("walk")
	return errors.New("binding fixture: Walk is not part of the checkpoint path")
}

func (l *bindingLog) Head(context.Context) (store.HeadRef, bool, error) {
	l.journal.add("head")
	return l.head, l.hasHead, nil
}

type bindingLockingLog struct{ *bindingLog }

func (l bindingLockingLog) LockAppends(context.Context) error {
	l.journal.add("lock")
	return l.lockErr
}

type bindingRecordingLog struct{ *bindingLog }

func (l bindingRecordingLog) RecordedHead(context.Context) (store.HeadRef, bool, error) {
	l.journal.add("recorded-head")
	return l.recorded, l.hasRecorded, nil
}

type bindingLockingRecordingLog struct{ *bindingLog }

func (l bindingLockingRecordingLog) LockAppends(context.Context) error {
	l.journal.add("lock")
	return l.lockErr
}

func (l bindingLockingRecordingLog) RecordedHead(context.Context) (store.HeadRef, bool, error) {
	l.journal.add("recorded-head")
	return l.recorded, l.hasRecorded, nil
}

var (
	_ store.AuditLog           = (*bindingLog)(nil)
	_ store.AuditAppendLocker  = bindingLockingLog{}
	_ store.RecordedHeadReader = bindingRecordingLog{}
	_ store.AuditAppendLocker  = bindingLockingRecordingLog{}
	_ store.RecordedHeadReader = bindingLockingRecordingLog{}
)

// bindingStore implements only Custody. The embedded Store is nil on purpose: a
// checkpoint that reached for anything else would panic instead of passing.
type bindingStore struct {
	store.Store
	log        store.AuditLog
	auditCalls int
	committed  int
	rolledBack int
}

func (s *bindingStore) Custody(_ context.Context, tenant model.TenantID, fn func(store.CustodyScope) error) error {
	if err := fn(&bindingScope{store: s, tenant: tenant}); err != nil {
		s.rolledBack++
		return err
	}
	s.committed++
	return nil
}

type bindingScope struct {
	store  *bindingStore
	tenant model.TenantID
}

func (sc *bindingScope) Tenant() model.TenantID { return sc.tenant }

func (sc *bindingScope) Org(context.Context) (model.Org, error) {
	return model.Org{}, errors.New("binding fixture: Org is not part of the checkpoint path")
}

func (sc *bindingScope) Audit() store.AuditLog {
	sc.store.auditCalls++
	return sc.store.log
}

func newBindingSigner(t *testing.T, journal *bindingJournal) *audit.Signer {
	t.Helper()
	_, onBox, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate on-box key: %v", err)
	}
	_, offBox, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate off-box key: %v", err)
	}
	s, err := audit.NewSigner(onBox, audit.WithCheckpointKey(&bindingCheckpointKey{journal: journal, priv: offBox}))
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	return s
}

// bindingHeadAt is a deterministic chain tip whose hash differs for every seq.
func bindingHeadAt(seq int64) store.HeadRef {
	h := make([]byte, 32)
	for i := range h {
		h[i] = byte(seq) ^ byte(i*7)
	}
	return store.HeadRef{Seq: seq, Hash: h}
}

func assertBindingCalls(t *testing.T, j *bindingJournal, want ...string) {
	t.Helper()
	if !slices.Equal(j.calls, want) {
		t.Fatalf("checkpoint call order = %v, want %v", j.calls, want)
	}
}

// decimalBytes renders b the way %v prints a byte slice ("[12 34 ...]").
func decimalBytes(b []byte) string {
	parts := make([]string, len(b))
	for i, v := range b {
		parts[i] = fmt.Sprint(int(v))
	}
	return "[" + strings.Join(parts, " ") + "]"
}

// assertNoSignatureMaterial proves a refusal names the failed binding without
// carrying the signature it produced, in any common rendering.
func assertNoSignatureMaterial(t *testing.T, err error, sigs [][]byte) {
	t.Helper()
	msg := err.Error()
	for _, sig := range sigs {
		for _, rendering := range []string{
			hex.EncodeToString(sig),
			base64.StdEncoding.EncodeToString(sig),
			base64.RawURLEncoding.EncodeToString(sig),
			decimalBytes(sig),
			string(sig),
		} {
			if strings.Contains(msg, rendering) {
				t.Fatalf("the checkpoint binding error carries signature material: %q", msg)
			}
		}
	}
}

func TestCheckpointTakesTheAppendLockBeforeReadingTheHead(t *testing.T) {
	j := &bindingJournal{}
	tenant := model.NewTenantID()
	base := &bindingLog{journal: j, tenant: tenant, head: bindingHeadAt(7), hasHead: true}
	st := &bindingStore{log: bindingLockingLog{base}}

	ev, ok, err := newBindingSigner(t, j).Checkpoint(context.Background(), st, tenant)
	if err != nil || !ok {
		t.Fatalf("checkpoint over a matching append = (ok=%v, err=%v)", ok, err)
	}
	assertBindingCalls(t, j, "lock", "head", "sign", "append")
	if st.auditCalls != 1 {
		t.Fatalf("Custody scope Audit() was called %d times, want the log captured once", st.auditCalls)
	}
	if st.committed != 1 || st.rolledBack != 0 {
		t.Fatalf("custody committed=%d rolledBack=%d, want one commit", st.committed, st.rolledBack)
	}
	if ev.TenantID != tenant || ev.Seq != 8 || !bytes.Equal(ev.PrevHash, base.head.Hash) {
		t.Fatalf("returned checkpoint = tenant %s seq %d, want tenant %s seq 8 linked to the signed head", ev.TenantID, ev.Seq, tenant)
	}
}

func TestCheckpointLockFailureStopsBeforeHeadSigningAndAppend(t *testing.T) {
	j := &bindingJournal{}
	tenant := model.NewTenantID()
	lockErr := errors.New("binding fixture: append lock unavailable")
	base := &bindingLog{journal: j, tenant: tenant, head: bindingHeadAt(3), hasHead: true, lockErr: lockErr}
	st := &bindingStore{log: bindingLockingLog{base}}

	ev, ok, err := newBindingSigner(t, j).Checkpoint(context.Background(), st, tenant)
	if !errors.Is(err, lockErr) {
		t.Fatalf("checkpoint err = %v, want the lock failure propagated", err)
	}
	if ok || ev.Seq != 0 {
		t.Fatalf("a failed lock reported ok=%v seq=%d", ok, ev.Seq)
	}
	assertBindingCalls(t, j, "lock")
	if st.committed != 0 || st.rolledBack != 1 {
		t.Fatalf("custody committed=%d rolledBack=%d, want the transaction rolled back", st.committed, st.rolledBack)
	}
}

// bindingSweepStore adds the enumeration CheckpointAll needs to bindingStore.
type bindingSweepStore struct {
	*bindingStore
	orgs []model.Org
}

func (s *bindingSweepStore) System(_ context.Context, fn func(store.SystemScope) error) error {
	return fn(bindingSystemScope{orgs: s.orgs})
}

// bindingSystemScope answers only ListOrgs; the embedded SystemScope is nil on purpose.
type bindingSystemScope struct {
	store.SystemScope
	orgs []model.Org
}

func (sc bindingSystemScope) ListOrgs(context.Context) ([]model.Org, error) { return sc.orgs, nil }

// Through the sweep, each tenant's lock failure names its tenant once (CheckpointAll
// adds the tenant) and keeps the store's error in the chain.
func TestCheckpointAllNamesEachTenantOnceWhenTheAppendLockFails(t *testing.T) {
	j := &bindingJournal{}
	tenant := model.NewTenantID()
	lockErr := errors.New("binding fixture: append lock unavailable")
	base := &bindingLog{journal: j, tenant: tenant, head: bindingHeadAt(5), hasHead: true, lockErr: lockErr}
	var org model.Org
	org.TenantID = tenant
	st := &bindingSweepStore{bindingStore: &bindingStore{log: bindingLockingLog{base}}, orgs: []model.Org{org}}

	err := newBindingSigner(t, j).CheckpointAll(context.Background(), st)
	if !errors.Is(err, lockErr) {
		t.Fatalf("sweep err = %v, want the lock failure kept in the error chain", err)
	}
	failures := strings.Split(err.Error(), "\n")
	wantTenants := []model.TenantID{tenant, model.SystemTenantID}
	if len(failures) != len(wantTenants) {
		t.Fatalf("sweep reported %d failures, want one per tenant: %q", len(failures), err)
	}
	for i, want := range wantTenants {
		if n := strings.Count(failures[i], want.String()); n != 1 {
			t.Errorf("failure %d names tenant %s %d times, want once: %q", i, want, n, failures[i])
		}
		if !strings.Contains(failures[i], "take the append lock before reading the head") {
			t.Errorf("failure %d does not name the step that failed: %q", i, failures[i])
		}
	}
	assertBindingCalls(t, j, "lock", "lock")
	if st.committed != 0 || st.rolledBack != 2 {
		t.Fatalf("custody committed=%d rolledBack=%d, want both transactions rolled back", st.committed, st.rolledBack)
	}
}

func TestCheckpointRefusesAPredecessorOtherThanTheSignedHead(t *testing.T) {
	otherHash := bytes.Repeat([]byte{0xab}, 32)
	cases := []struct {
		name    string
		head    store.HeadRef
		append  func(tenant model.TenantID, head store.HeadRef) model.AuditEvent
		wantErr string
		// notErr is a cause the refusal must not claim.
		notErr string
	}{
		{
			name: "sequence advanced past the signed head",
			head: bindingHeadAt(11),
			append: func(tenant model.TenantID, head store.HeadRef) model.AuditEvent {
				return model.AuditEvent{TenantID: tenant, Seq: head.Seq + 2, PrevHash: bytes.Clone(head.Hash)}
			},
			wantErr: "was appended at seq",
		},
		{
			name: "predecessor hash differs from the signed head",
			head: bindingHeadAt(11),
			append: func(tenant model.TenantID, head store.HeadRef) model.AuditEvent {
				return model.AuditEvent{TenantID: tenant, Seq: head.Seq + 1, PrevHash: otherHash}
			},
			wantErr: "predecessor hash",
		},
		{
			name: "another writer appended between head and checkpoint",
			head: bindingHeadAt(11),
			append: func(tenant model.TenantID, head store.HeadRef) model.AuditEvent {
				return model.AuditEvent{TenantID: tenant, Seq: head.Seq + 2, PrevHash: otherHash}
			},
			wantErr: "was appended at seq",
		},
		{
			name: "appended to another tenant's chain",
			head: bindingHeadAt(11),
			append: func(_ model.TenantID, head store.HeadRef) model.AuditEvent {
				return model.AuditEvent{TenantID: model.SystemTenantID, Seq: head.Seq + 1, PrevHash: bytes.Clone(head.Hash)}
			},
			wantErr: "belongs to chain",
		},
		{
			name: "store reported the checkpoint as dropped",
			head: bindingHeadAt(11),
			append: func(model.TenantID, store.HeadRef) model.AuditEvent {
				return model.AuditEvent{}
			},
			wantErr: "ledger did not record the checkpoint",
			notErr:  "belongs to chain",
		},
		{
			// A wrapped signedSeq+1 is math.MinInt64, so an unchecked addition would
			// accept exactly this event.
			name: "signed head has no representable successor",
			head: store.HeadRef{Seq: math.MaxInt64, Hash: bytes.Repeat([]byte{0x5a}, 32)},
			append: func(tenant model.TenantID, head store.HeadRef) model.AuditEvent {
				return model.AuditEvent{TenantID: tenant, Seq: math.MinInt64, PrevHash: bytes.Clone(head.Hash)}
			},
			wantErr: "no representable successor",
		},
	}
	capabilities := []struct {
		name      string
		wrap      func(*bindingLog) store.AuditLog
		wantCalls []string
	}{
		{"with AuditAppendLocker", func(l *bindingLog) store.AuditLog { return bindingLockingLog{l} }, []string{"lock", "head", "sign", "append"}},
		{"without AuditAppendLocker", func(l *bindingLog) store.AuditLog { return l }, []string{"head", "sign", "append"}},
	}
	for _, capability := range capabilities {
		for _, tc := range cases {
			t.Run(capability.name+"/"+tc.name, func(t *testing.T) {
				j := &bindingJournal{}
				tenant := model.NewTenantID()
				base := &bindingLog{journal: j, tenant: tenant, head: tc.head, hasHead: true}
				base.appended = func(model.AuditDraft) (model.AuditEvent, error) {
					return tc.append(tenant, tc.head), nil
				}
				st := &bindingStore{log: capability.wrap(base)}

				ev, ok, err := newBindingSigner(t, j).Checkpoint(context.Background(), st, tenant)
				if err == nil {
					t.Fatalf("checkpoint committed an event (seq %d) that does not follow the signed head seq %d", ev.Seq, tc.head.Seq)
				}
				if ok || ev.Seq != 0 || ev.TenantID != "" {
					t.Fatalf("a refused binding returned ok=%v event seq %d tenant %q", ok, ev.Seq, ev.TenantID)
				}
				for _, want := range []string{"checkpoint binding", "refusing to commit", tc.wantErr} {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("refusal must carry %q, got: %v", want, err)
					}
				}
				if tc.notErr != "" && strings.Contains(err.Error(), tc.notErr) {
					t.Fatalf("refusal names the wrong cause %q: %v", tc.notErr, err)
				}
				assertNoSignatureMaterial(t, err, j.sigs)
				assertBindingCalls(t, j, capability.wantCalls...)
				if j.count("sign") != 1 || j.count("append") != 1 {
					t.Fatalf("a refused binding signed %d and appended %d times, want exactly one attempt and no retry", j.count("sign"), j.count("append"))
				}
				if st.committed != 0 || st.rolledBack != 1 {
					t.Fatalf("custody committed=%d rolledBack=%d, want the transaction rolled back", st.committed, st.rolledBack)
				}
			})
		}
	}
}

func TestCheckpointWithoutAppendLockerStillCompletesOnAMatchingHead(t *testing.T) {
	j := &bindingJournal{}
	tenant := model.NewTenantID()
	base := &bindingLog{journal: j, tenant: tenant, head: bindingHeadAt(21), hasHead: true}
	if _, lockable := store.AuditLog(base).(store.AuditAppendLocker); lockable {
		t.Fatal("fixture error: the no-locker log offers AuditAppendLocker")
	}
	st := &bindingStore{log: base}

	ev, ok, err := newBindingSigner(t, j).Checkpoint(context.Background(), st, tenant)
	if err != nil || !ok {
		t.Fatalf("a store without the optional locker must still checkpoint a matching head: ok=%v err=%v", ok, err)
	}
	assertBindingCalls(t, j, "head", "sign", "append")
	if ev.Seq != 22 || !bytes.Equal(ev.PrevHash, base.head.Hash) || st.committed != 1 {
		t.Fatalf("checkpoint seq %d committed=%d, want seq 22 linked to the signed head and one commit", ev.Seq, st.committed)
	}
}

func TestCheckpointEmptyAndRemovedLedgerKeepTheirMeaningUnderTheLock(t *testing.T) {
	type outcome int
	const (
		noOp outcome = iota
		alarm
	)
	cases := []struct {
		name        string
		wrap        func(*bindingLog) store.AuditLog
		hasRecorded bool
		want        outcome
		wantCalls   []string
	}{
		{"empty chain, locker without recorded-head reader", func(l *bindingLog) store.AuditLog { return bindingLockingLog{l} }, false, noOp, []string{"lock", "head"}},
		{"empty chain, no capabilities", func(l *bindingLog) store.AuditLog { return l }, false, noOp, []string{"head"}},
		{"empty chain, store records no head", func(l *bindingLog) store.AuditLog { return bindingLockingRecordingLog{l} }, false, noOp, []string{"lock", "head", "recorded-head"}},
		{"emptied ledger under a live head, with locker", func(l *bindingLog) store.AuditLog { return bindingLockingRecordingLog{l} }, true, alarm, []string{"lock", "head", "recorded-head"}},
		{"emptied ledger under a live head, without locker", func(l *bindingLog) store.AuditLog { return bindingRecordingLog{l} }, true, alarm, []string{"head", "recorded-head"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			j := &bindingJournal{}
			tenant := model.NewTenantID()
			base := &bindingLog{journal: j, tenant: tenant, hasHead: false, recorded: store.HeadRef{Seq: 42}, hasRecorded: tc.hasRecorded}
			st := &bindingStore{log: tc.wrap(base)}

			ev, ok, err := newBindingSigner(t, j).Checkpoint(context.Background(), st, tenant)
			switch tc.want {
			case noOp:
				if err != nil || ok || ev.Seq != 0 {
					t.Fatalf("an empty chain must stay a silent no-op: ok=%v seq=%d err=%v", ok, ev.Seq, err)
				}
				if st.committed != 1 {
					t.Fatalf("custody committed=%d rolledBack=%d, want the no-op transaction to complete", st.committed, st.rolledBack)
				}
			case alarm:
				if err == nil || ok {
					t.Fatalf("an emptied ledger under a live head checkpointed: ok=%v err=%v", ok, err)
				}
				for _, want := range []string{"audit_heads", "seq 42"} {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("the emptied-ledger alarm must carry %q, got: %v", want, err)
					}
				}
				if st.rolledBack != 1 {
					t.Fatalf("custody committed=%d rolledBack=%d, want the alarm to roll back", st.committed, st.rolledBack)
				}
			}
			assertBindingCalls(t, j, tc.wantCalls...)
		})
	}
}

// advancingStore wraps the real store and moves the chain head inside the
// checkpoint's own Custody transaction: right after the head is read, it appends
// one ordinary event through the same ledger. That is the committed shape of a
// writer that interleaves between head and checkpoint append, produced without
// timing. The optional capabilities are forwarded only because the test first
// proves the real ledger has them.
type advancingStore struct {
	store.Store
	advanced  bool
	lockCalls int
}

func (a *advancingStore) Custody(ctx context.Context, tenant model.TenantID, fn func(store.CustodyScope) error) error {
	return a.Store.Custody(ctx, tenant, func(sc store.CustodyScope) error {
		return fn(&advancingScope{CustodyScope: sc, store: a})
	})
}

type advancingScope struct {
	store.CustodyScope
	store *advancingStore
}

func (sc *advancingScope) Audit() store.AuditLog {
	return &advancingLog{AuditLog: sc.CustodyScope.Audit(), store: sc.store}
}

type advancingLog struct {
	store.AuditLog
	store *advancingStore
}

func (l *advancingLog) Head(ctx context.Context) (store.HeadRef, bool, error) {
	head, ok, err := l.AuditLog.Head(ctx)
	if err != nil || !ok || l.store.advanced {
		return head, ok, err
	}
	if _, aerr := l.Append(ctx, model.AuditDraft{
		Actor: model.ActorSystem, ActorKind: model.ActorSystem,
		Action: "test.interleaved_append", TargetKind: "core.test",
	}); aerr != nil {
		return store.HeadRef{}, false, fmt.Errorf("advancing fixture: interleaved append: %w", aerr)
	}
	l.store.advanced = true
	return head, ok, nil
}

func (l *advancingLog) LockAppends(ctx context.Context) error {
	locker, ok := l.AuditLog.(store.AuditAppendLocker)
	if !ok {
		return errors.New("advancing fixture: the real custody ledger no longer offers AuditAppendLocker")
	}
	l.store.lockCalls++
	return locker.LockAppends(ctx)
}

func (l *advancingLog) RecordedHead(ctx context.Context) (store.HeadRef, bool, error) {
	reader, ok := l.AuditLog.(store.RecordedHeadReader)
	if !ok {
		return store.HeadRef{}, false, errors.New("advancing fixture: the real custody ledger no longer offers RecordedHeadReader")
	}
	return reader.RecordedHead(ctx)
}

func bindingChainTip(t *testing.T, st store.Store, tenant model.TenantID) store.HeadRef {
	t.Helper()
	var tip store.HeadRef
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		head, ok, err := sc.Audit().Head(context.Background())
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("tenant %s has an empty chain", tenant)
		}
		tip = head
		return nil
	}); err != nil {
		t.Fatalf("read chain tip for %s: %v", tenant, err)
	}
	return tip
}

func bindingCheckpointReport(t *testing.T, st store.Store, tenant model.TenantID, pub ed25519.PublicKey) (audit.CheckpointReport, store.VerifyReport) {
	t.Helper()
	var checkpoints audit.CheckpointReport
	var structure store.VerifyReport
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		var err error
		if checkpoints, err = audit.VerifyCheckpoints(context.Background(), sc.Audit(), pub); err != nil {
			return err
		}
		structure, err = sc.Audit().Verify(context.Background(), 1)
		return err
	}); err != nil {
		t.Fatalf("verify chain for %s: %v", tenant, err)
	}
	return checkpoints, structure
}

func TestCheckpointBindsTheCommittedPredecessorOnTheSQLiteStore(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	tenant := provisionTenant(t, st)
	appendEvents(t, st, tenant, 3)
	signer := testSigner(t)

	var hasLocker, hasRecorded bool
	if err := st.Custody(ctx, tenant, func(sc store.CustodyScope) error {
		_, hasLocker = sc.Audit().(store.AuditAppendLocker)
		_, hasRecorded = sc.Audit().(store.RecordedHeadReader)
		return nil
	}); err != nil {
		t.Fatalf("inspect custody ledger: %v", err)
	}
	if !hasLocker || !hasRecorded {
		t.Fatalf("the real custody ledger offers AuditAppendLocker=%v RecordedHeadReader=%v; this test needs both to exercise the locked path", hasLocker, hasRecorded)
	}

	for _, target := range []model.TenantID{tenant, model.SystemTenantID} {
		before := bindingChainTip(t, st, target)
		ev, ok, err := signer.Checkpoint(ctx, st, target)
		if err != nil || !ok {
			t.Fatalf("checkpoint %s = (ok=%v, err=%v)", target, ok, err)
		}
		if ev.TenantID != target || ev.Seq != before.Seq+1 || !bytes.Equal(ev.PrevHash, before.Hash) {
			t.Fatalf("checkpoint %s landed at seq %d, want seq %d linked to the head it signed", target, ev.Seq, before.Seq+1)
		}
		checkpoints, structure := bindingCheckpointReport(t, st, target, signer.PublicKey())
		if !checkpoints.OK || checkpoints.LatestAttestedSeq != before.Seq {
			t.Fatalf("checkpoint verification for %s = %+v, want ok attesting seq %d", target, checkpoints, before.Seq)
		}
		if !structure.OK {
			t.Fatalf("structural verification for %s = %+v", target, structure)
		}
	}
}

func TestCheckpointRefusesAHeadThatAdvancesBeforeItsAppendOnTheSQLiteStore(t *testing.T) {
	for _, target := range []struct {
		name   string
		system bool
	}{
		{name: "business tenant"},
		{name: "system chain", system: true},
	} {
		t.Run(target.name, func(t *testing.T) {
			ctx := context.Background()
			st := testStore(t)
			tenant := provisionTenant(t, st)
			appendEvents(t, st, tenant, 2)
			if target.system {
				tenant = model.SystemTenantID
			}
			signer := testSigner(t)

			before := bindingChainTip(t, st, tenant)
			beforeCheckpoints, _ := bindingCheckpointReport(t, st, tenant, signer.PublicKey())

			adv := &advancingStore{Store: st}
			ev, ok, err := signer.Checkpoint(ctx, adv, tenant)
			if !adv.advanced {
				t.Fatal("fixture error: the head was never advanced, so this run proves nothing")
			}
			if err == nil {
				checkpoints, structure := bindingCheckpointReport(t, st, tenant, signer.PublicKey())
				t.Fatalf("a checkpoint whose head advanced after signing was COMMITTED: ok=%v seq=%d signedHead=%d "+
					"prevLinksSignedHead=%v appendLockCalls=%d; checkpoint verification ok=%v reason=%q firstBadSeq=%d; structural ok=%v",
					ok, ev.Seq, before.Seq, bytes.Equal(ev.PrevHash, before.Hash), adv.lockCalls,
					checkpoints.OK, checkpoints.Reason, checkpoints.FirstBadSeq, structure.OK)
			}
			if ok || ev.Seq != 0 {
				t.Fatalf("a refused binding returned ok=%v seq=%d", ok, ev.Seq)
			}
			for _, want := range []string{"checkpoint binding", "refusing to commit", "was appended at seq"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("refusal must carry %q, got: %v", want, err)
				}
			}
			if adv.lockCalls != 1 {
				t.Fatalf("the real ledger's append lock was taken %d times, want once before the head read", adv.lockCalls)
			}

			after := bindingChainTip(t, st, tenant)
			if after.Seq != before.Seq || !bytes.Equal(after.Hash, before.Hash) {
				t.Fatalf("rollback: the head moved from seq %d to seq %d; neither the interleaved event nor the checkpoint may commit", before.Seq, after.Seq)
			}
			afterCheckpoints, structure := bindingCheckpointReport(t, st, tenant, signer.PublicKey())
			if afterCheckpoints.Checkpoints != beforeCheckpoints.Checkpoints || !structure.OK {
				t.Fatalf("after the refusal the chain holds %d checkpoints (was %d), structural ok=%v", afterCheckpoints.Checkpoints, beforeCheckpoints.Checkpoints, structure.OK)
			}

			// The same store still anchors the unchanged head once nothing interleaves.
			ev, ok, err = signer.Checkpoint(ctx, st, tenant)
			if err != nil || !ok || ev.Seq != before.Seq+1 {
				t.Fatalf("checkpoint after the refusal = (ok=%v seq=%d err=%v), want seq %d", ok, ev.Seq, err, before.Seq+1)
			}
			if checkpoints, _ := bindingCheckpointReport(t, st, tenant, signer.PublicKey()); !checkpoints.OK {
				t.Fatalf("checkpoint verification after the refusal = %+v", checkpoints)
			}
		})
	}
}
