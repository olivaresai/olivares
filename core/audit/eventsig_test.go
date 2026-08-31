// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package audit_test

import (
	"context"
	"crypto/ed25519"
	"testing"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// signedStore opens an in-memory store whose audit log signs every event with
// signer (store.Config.SignEvent), mirroring the production wiring in boot.go.
func signedStore(t *testing.T, signer *audit.Signer) store.Store {
	t.Helper()
	st, err := sqlstore.Open(context.Background(), store.Config{
		Engine: store.EngineSQLite, DSN: ":memory:", Debug: true, SignEvent: signer.SignEvent,
	}, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// TestEventSignaturesEndToEnd proves every appended event is signed at write time
// and verifies against the key — and that a checkpoint (signed under its own
// domain) is excluded from the per-event check, not double-counted.
func TestEventSignaturesEndToEnd(t *testing.T) {
	ctx := context.Background()
	pub, priv, _ := ed25519.GenerateKey(nil)
	signer, err := audit.NewSigner(priv)
	if err != nil {
		t.Fatal(err)
	}
	st := signedStore(t, signer)
	tenant := provisionTenant(t, st) // org.create (seq 1)
	appendEvents(t, st, tenant, 3)   // seqs 2,3,4

	verify := func(p ed25519.PublicKey) audit.EventSigReport {
		var rep audit.EventSigReport
		if err := st.View(ctx, tenant, func(sc store.Scope) error {
			r, e := audit.VerifyEvents(ctx, sc.Audit(), p)
			rep = r
			return e
		}); err != nil {
			t.Fatalf("verify: %v", err)
		}
		return rep
	}

	rep := verify(pub)
	if !rep.OK || rep.Events != 4 || rep.Signed != 4 {
		t.Fatalf("good verify = %+v (want OK, Events=Signed=4)", rep)
	}

	// The wrong key rejects every signature.
	otherPub, _, _ := ed25519.GenerateKey(nil)
	if bad := verify(otherPub); bad.OK || bad.Reason != "event-sig-invalid" {
		t.Fatalf("wrong-key verify = %+v", bad)
	}

	// A checkpoint event is signed under the checkpoint domain and must NOT be
	// counted or rejected by the per-event verifier.
	if _, _, err := signer.Checkpoint(ctx, st, tenant); err != nil {
		t.Fatal(err)
	}
	if rep := verify(pub); !rep.OK || rep.Events != 4 || rep.Signed != 4 {
		t.Fatalf("post-checkpoint verify = %+v (checkpoint must be skipped)", rep)
	}
}

// walkLog is a minimal store.AuditLog that Walks a fixed slice of events, so the
// per-event verifier can be tested against hand-crafted (incl. tampered) chains
// without raw DB access.
type walkLog struct{ events []model.AuditEvent }

func (w walkLog) Append(context.Context, model.AuditDraft) (model.AuditEvent, error) {
	return model.AuditEvent{}, nil
}
func (w walkLog) Verify(context.Context, int64) (store.VerifyReport, error) {
	return store.VerifyReport{}, nil
}
func (w walkLog) Head(context.Context) (store.HeadRef, bool, error) {
	return store.HeadRef{}, false, nil
}
func (w walkLog) Walk(_ context.Context, fromSeq int64, fn func(model.AuditEvent) error) error {
	for _, e := range w.events {
		if e.Seq < fromSeq {
			continue
		}
		if err := fn(e); err != nil {
			return err
		}
	}
	return nil
}

func TestEmptyLedgerVerifiersDoNotReportSuccess(t *testing.T) {
	ctx := context.Background()
	pub, _, _ := ed25519.GenerateKey(nil)
	empty := walkLog{}

	events, err := audit.VerifyEvents(ctx, empty, pub)
	if err != nil {
		t.Fatal(err)
	}
	if events.OK || events.Events != 0 || events.Signed != 0 || events.Reason != "no-events" {
		t.Fatalf("empty event ledger reported success: %+v", events)
	}

	checkpoints, err := audit.VerifyCheckpoints(ctx, empty, pub)
	if err != nil {
		t.Fatal(err)
	}
	if checkpoints.OK || checkpoints.Checkpoints != 0 || checkpoints.Reason != "no-checkpoints" {
		t.Fatalf("empty checkpoint ledger reported success: %+v", checkpoints)
	}
}

// TestVerifyEventsDetectsTamper proves the per-event signature binds to the chain
// hash: an attacker who rewrites a tail event's content (and recomputes the chain
// hash so the chain still links) cannot reuse the old signature — the per-event
// check fails even though no chain break is left behind. A stripped signature is
// likewise flagged.
func TestVerifyEventsDetectsTamper(t *testing.T) {
	ctx := context.Background()
	pub, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := audit.NewSigner(priv)

	tenant := "22222222-2222-7222-8222-222222222222"
	mk := func(seq int64, hash []byte) model.AuditEvent {
		return model.AuditEvent{
			TenantID: model.TenantID(tenant), Seq: seq, Action: "agent.create",
			Hash: hash, Sig: signer.SignEvent(tenant, seq, hash),
		}
	}

	good := walkLog{events: []model.AuditEvent{
		mk(1, []byte{0x01}), mk(2, []byte{0x02}), mk(3, []byte{0x03}),
	}}
	if rep, err := audit.VerifyEvents(ctx, good, pub); err != nil || !rep.OK || rep.Signed != 3 {
		t.Fatalf("good chain verify = %+v err=%v", rep, err)
	}

	// Tamper: keep the seq-2 signature (over hash 0x02) but change the stored hash
	// to 0x99 (the attacker altered content and recomputed the linking hash).
	tampered := good
	tampered.events = append([]model.AuditEvent(nil), good.events...)
	tampered.events[1].Hash = []byte{0x99}
	rep, err := audit.VerifyEvents(ctx, tampered, pub)
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK || rep.Reason != "event-sig-invalid" || rep.FirstBadSeq != 2 {
		t.Fatalf("tampered verify = %+v (want invalid at seq 2)", rep)
	}

	// A stripped signature is flagged as missing.
	stripped := good
	stripped.events = append([]model.AuditEvent(nil), good.events...)
	stripped.events[2].Sig = nil
	if rep, _ := audit.VerifyEvents(ctx, stripped, pub); rep.OK || rep.Reason != "event-sig-missing" || rep.FirstBadSeq != 3 {
		t.Fatalf("stripped verify = %+v (want missing at seq 3)", rep)
	}
}

// TestVerifyEventsFencedClosesF07 is the repro: after a key rotation, the
// RETIRED per-event key must NOT be able to validate a CURRENT-epoch event (that
// is F-07 — retired key + DB write = undetectable tail rewrite), while a
// legitimate event signed by the old key WITHIN its epoch must still pass.
func TestVerifyEventsFencedClosesF07(t *testing.T) {
	ctx := context.Background()
	pub1, priv1, _ := ed25519.GenerateKey(nil)
	pub2, priv2, _ := ed25519.GenerateKey(nil)
	gen1, _ := audit.NewSigner(priv1)
	gen2, _ := audit.NewSigner(priv2)

	tenant := "22222222-2222-7222-8222-222222222222"
	mk := func(s *audit.Signer, seq int64, hash []byte) model.AuditEvent {
		return model.AuditEvent{
			TenantID: model.TenantID(tenant), Seq: seq, Action: "agent.create",
			Hash: hash, Sig: s.SignEvent(tenant, seq, hash),
		}
	}
	// Legit rotated chain: gen1 signs seqs 1-2, gen2 (current) signs seqs 3-4.
	// Rotation boundary: gen1's last_seq = 2.
	legit := walkLog{events: []model.AuditEvent{
		mk(gen1, 1, []byte{0x01}), mk(gen1, 2, []byte{0x02}),
		mk(gen2, 3, []byte{0x03}), mk(gen2, 4, []byte{0x04}),
	}}
	// F-07 attack: the RETIRED key (gen1) re-signs a CURRENT-epoch tail event
	// (seq 4). Its raw Ed25519 signature is valid — only the epoch fence rejects it.
	forged := walkLog{events: append([]model.AuditEvent(nil), legit.events...)}
	forged.events[3] = mk(gen1, 4, []byte{0x04})

	fenced := []audit.FencedKey{{Key: pub1, LastSeq: 2}, {Key: pub2}} // pub2 = current (unbounded)

	// The fence rejects the retired-key forgery at seq 4...
	if rep, err := audit.VerifyEventsFenced(ctx, forged, 1, fenced); err != nil || rep.OK || rep.FirstBadSeq != 4 || rep.Reason != "event-sig-invalid" {
		t.Fatalf("fenced verifier accepted a retired-key forgery: %+v err=%v", rep, err)
	}
	// ...while the legit rotated chain (incl. gen1's in-range seqs 1-2) still
	// verifies end-to-end.
	if rep, err := audit.VerifyEventsFenced(ctx, legit, 1, fenced); err != nil || !rep.OK || rep.Events != 4 || rep.Signed != 4 {
		t.Fatalf("fenced verifier rejected a legitimate rotated chain: %+v err=%v", rep, err)
	}
	// CONTRAST — the pre FLAT verifier is exactly F-07: with any candidate
	// key trusted for any sequence, it (insecurely) ACCEPTS the same forgery. This
	// witnesses why fencing is load-bearing; VerifyEventsFenced is the fix.
	if rep, err := audit.VerifyEventsWith(ctx, forged, []ed25519.PublicKey{pub1, pub2}); err != nil || !rep.OK {
		t.Fatalf("expected the flat verifier to accept the forgery (documenting F-07): %+v err=%v", rep, err)
	}
}

// TestVerifyEventsWithRotation proves a chain whose per-event signing key
// rotated mid-life (the `keys rotate` ceremony) verifies end-to-end with
// the candidate set {old, new}, and that neither key alone covers the whole
// chain — the property that makes the envelope's prior_public_keys history
// load-bearing rather than decorative.
func TestVerifyEventsWithRotation(t *testing.T) {
	ctx := context.Background()
	pub1, priv1, _ := ed25519.GenerateKey(nil)
	pub2, priv2, _ := ed25519.GenerateKey(nil)
	gen1, _ := audit.NewSigner(priv1)
	gen2, _ := audit.NewSigner(priv2)

	tenant := "22222222-2222-7222-8222-222222222222"
	mk := func(s *audit.Signer, seq int64, hash []byte) model.AuditEvent {
		return model.AuditEvent{
			TenantID: model.TenantID(tenant), Seq: seq, Action: "agent.create",
			Hash: hash, Sig: s.SignEvent(tenant, seq, hash),
		}
	}
	// Seqs 1-2 signed by generation 1, seqs 3-4 by generation 2 (post-rotation).
	chain := walkLog{events: []model.AuditEvent{
		mk(gen1, 1, []byte{0x01}), mk(gen1, 2, []byte{0x02}),
		mk(gen2, 3, []byte{0x03}), mk(gen2, 4, []byte{0x04}),
	}}

	rep, err := audit.VerifyEventsWith(ctx, chain, []ed25519.PublicKey{pub1, pub2})
	if err != nil || !rep.OK || rep.Events != 4 || rep.Signed != 4 {
		t.Fatalf("rotated chain with both keys = %+v err=%v", rep, err)
	}
	// Either key alone leaves half the chain unverifiable.
	if rep, _ := audit.VerifyEventsWith(ctx, chain, []ed25519.PublicKey{pub1}); rep.OK || rep.FirstBadSeq != 3 {
		t.Fatalf("old key alone = %+v (want invalid at seq 3)", rep)
	}
	if rep, _ := audit.VerifyEventsWith(ctx, chain, []ed25519.PublicKey{pub2}); rep.OK || rep.FirstBadSeq != 1 {
		t.Fatalf("new key alone = %+v (want invalid at seq 1)", rep)
	}
	// Guard rails: no candidates / a malformed candidate are loud errors.
	if _, err := audit.VerifyEventsWith(ctx, chain, nil); err == nil {
		t.Fatal("empty candidate set accepted")
	}
	if _, err := audit.VerifyEventsWith(ctx, chain, []ed25519.PublicKey{pub1[:5]}); err == nil {
		t.Fatal("undersized candidate accepted")
	}
}
