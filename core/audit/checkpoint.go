// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package audit

import (
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// ActionCheckpoint is the audit action of a checkpoint event.
const ActionCheckpoint = "audit.checkpoint"

// checkpointDomain binds the checkpoint signature preimage to its purpose and
// version, so a signature over a checkpoint can never be confused with any other
// Ed25519 signature the engine produces.
const checkpointDomain = "olivares.audit.checkpoint.v1"

// Signer issues and is keyed to verify audit checkpoints. The on-box Ed25519 key
// signs per-event signatures (the hot path) and, by default, the checkpoints. An
// optional off-box CheckpointKey (WithCheckpointKey) takes over ONLY
// the checkpoint signatures so the host-compromise case is covered without a
// private key on the host — the per-event hot path stays on-box (docs/SECURITY-HARDENING.md).
type Signer struct {
	priv ed25519.PrivateKey
	cp   CheckpointKey // nil => on-box Ed25519 checkpoints (the default)
}

// NewSigner wraps an Ed25519 private key. Options may attach an off-box checkpoint
// key (WithCheckpointKey); with none, behavior is byte-identical to before
// (on-box Ed25519 for both per-event and checkpoint signatures).
func NewSigner(priv ed25519.PrivateKey, opts ...Option) (*Signer, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("audit: bad private key size %d", len(priv))
	}
	s := &Signer{priv: priv}
	for _, o := range opts {
		o(s)
	}
	return s, nil
}

// PublicKey returns the on-box Ed25519 verification key (per-event signatures, and
// checkpoints when no off-box key is configured). For the off-box checkpoint key's
// public material use CheckpointVerifier / the configured CheckpointKey.
func (s *Signer) PublicKey() ed25519.PublicKey {
	return s.priv.Public().(ed25519.PublicKey)
}

// OffBoxCheckpoints reports whether checkpoints are signed by an off-box
// (KMS/HSM) key rather than the on-box Ed25519 key.
func (s *Signer) OffBoxCheckpoints() bool { return s.cp != nil }

// CheckpointKey returns the configured off-box checkpoint key, or nil when
// checkpoints are signed on-box (the default). The archival export reads its
// public material for the advisory keys.json.
func (s *Signer) CheckpointKey() CheckpointKey { return s.cp }

// CheckpointVerifier returns a verifier covering this signer's checkpoint key(s):
// always the on-box Ed25519 key, plus the off-box KMS/HSM key when configured — so
// a chain that adopted an off-box signer mid-life self-verifies end-to-end. It may
// fetch the off-box public key (cached) and so takes a context.
func (s *Signer) CheckpointVerifier(ctx context.Context) (*CheckpointVerifier, error) {
	v := NewCheckpointVerifier().AddEd25519(s.PublicKey())
	if s.cp == nil {
		return v, nil
	}
	der, err := s.cp.PublicKey(ctx)
	if err != nil {
		return nil, fmt.Errorf("audit: fetch off-box checkpoint public key: %w", err)
	}
	if s.cp.Algorithm() == AlgEd25519 {
		if err := v.AddEd25519Raw(der); err != nil {
			return nil, err
		}
		return v, nil
	}
	if err := v.AddPublicKey(s.cp.Algorithm(), der); err != nil {
		return nil, err
	}
	return v, nil
}

// Checkpoint appends a signed checkpoint to a tenant's ledger, notarizing the
// current chain tip. It is a no-op (returns ok=false) for an empty chain. The
// checkpoint event's PrevHash is exactly the attested head hash, and its
// signature covers the canonical (tenant, attestedSeq, headHash) preimage.
//
// It runs through store.Custody, not Mutate, because anchoring a chain is a
// CUSTODIAL act and not service: it must keep working for a tenant whose service
// has been withdrawn, whose evidence still has to be provable during the grace
// period. Custody still crosses the residency guard, so this cannot anchor a chain
// from a region that may not serve it.
func (s *Signer) Checkpoint(ctx context.Context, st store.Store, tenant model.TenantID) (model.AuditEvent, bool, error) {
	var ev model.AuditEvent
	var ok bool
	err := st.Custody(ctx, tenant, func(sc store.CustodyScope) error {
		head, has, err := sc.Audit().Head(ctx)
		if err != nil {
			return err
		}
		if !has {
			// "No events" has two meanings and they are not both benign. A tenant
			// nobody has written to yet is a normal empty chain: nothing to
			// notarize, no news. A tenant whose events are gone while the store
			// still RECORDS a head is the other one — the ledger was emptied under
			// a live head (TRUNCATE, wholesale DELETE, a botched restore), which
			// Measured passing through unnoticed and reproduced.
			//
			// Head cannot tell them apart (it reports the tip it can SEE), so this
			// used to return a silent no-op and the hourly checkpointer notarized
			// nothing without moving its failure metric. Ask the store the other
			// question when it can answer it.
			rh, canAsk := sc.Audit().(store.RecordedHeadReader)
			if !canAsk {
				return nil
			}
			rec, hasRec, rerr := rh.RecordedHead(ctx)
			if rerr != nil {
				return rerr
			}
			if !hasRec {
				return nil // a genuinely empty chain
			}
			return fmt.Errorf("evidence ledger is EMPTY while audit_heads still records seq %d: the events were removed under a live head (TRUNCATE, wholesale DELETE or a bad restore) — no checkpoint was written. Run `olivares audit verify --tenant %s --strict` for the full report; out of this check's reach: an actor who rewrites BOTH tables consistently, detected only by comparing against an EXTERNALLY RETAINED tip (an off-box copy of the anchor, or the DR manifest's recorded seq+hash) — signatures prove nothing was forged, never that nothing was removed", rec.Seq, tenant)
		}
		// Sign the canonical (tenant, attestedSeq, headHash) preimage with the
		// off-box key when configured, else the on-box Ed25519 key. The off-box call
		// is network I/O held inside this Mutate so the attested head cannot advance
		// between read and append (checkpoints are infrequent and off the hot path;
		// the checkpointer bounds it with a 30s context).
		sig, meta, serr := s.signCheckpoint(ctx, checkpointPreimage(tenant.String(), head.Seq, head.Hash))
		if serr != nil {
			return serr
		}
		ev, err = sc.Audit().Append(ctx, model.AuditDraft{
			Actor: model.ActorSystem, ActorKind: model.ActorSystem,
			Action: ActionCheckpoint, TargetKind: "core.audit_checkpoint",
			Meta: meta, Sig: sig,
		})
		if err != nil {
			return err
		}
		ok = true
		return nil
	})
	if err != nil {
		return model.AuditEvent{}, false, err
	}
	return ev, ok, nil
}

// CheckpointAll checkpoints every tenant's chain plus the system chain. It is the
// cadence entry point a scheduler or graceful-shutdown hook calls.
func (s *Signer) CheckpointAll(ctx context.Context, st store.Store) error {
	var tenants []model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		orgs, err := sys.ListOrgs(ctx)
		if err != nil {
			return err
		}
		for _, o := range orgs {
			// EVERY tenant is anchored, including one whose service is withdrawn.
			//
			// This loop used to skip non-active orgs, on the premise that "a withdrawn
			// tenant has a FROZEN chain, so there is nothing new to anchor". That
			// premise is false in the very code that withdraws service: SetOrgStatus
			// changes orgs.status and then appends org.suspend_service to that same
			// tenant's chain (core/internal/store/sqlstore/system.go:337, :361), and a
			// re-assertion appends another one. The chain therefore advances at exactly
			// the moment the skip began, leaving the suspension event and the whole tail
			// since the previous checkpoint permanently unanchored — the customer's
			// evidence stops being provable during the grace period that exists to
			// preserve it.
			//
			// The skip was introduced for a real reason — the guarded Mutate failed and
			// aborted the sweep for every later tenant — but it treated the symptom. The
			// cause was that anchoring went through the SERVICE door at all. It now goes
			// through store.Custody, which service state does not gate, so there is
			// nothing to skip and no race to lose: a tenant suspended between this
			// enumeration and its turn in the loop below is anchored just the same.
			tenants = append(tenants, o.TenantID)
		}
		return nil
	}); err != nil {
		return err
	}
	// The system tenant holds auth/cross-tenant events and needs its own anchor.
	tenants = append(tenants, model.SystemTenantID)
	// Report and CONTINUE, rather than returning on the first tenant that fails.
	// A tenant-local problem must stay tenant-local: ListOrgs has a stable
	// ORDER BY id ASC, and the emptied-ledger alarm below is persistent until an
	// operator repairs the database — so failing fast would let one corrupt
	// tenant that happens to sort first abort the sweep before every later
	// tenant and the system chain, every hourly tick, indefinitely. That would
	// cost exactly the anchors the alarm exists to protect (found by the Codex
	// contrast of 2026-08-06, F-05).
	//
	// A ctx that is done is the one thing worth stopping for: continuing would
	// pile up identical deadline errors instead of the one that matters.
	seen := map[model.TenantID]bool{}
	var errs []error
	for _, t := range tenants {
		if seen[t] {
			continue
		}
		seen[t] = true
		if _, _, err := s.Checkpoint(ctx, st, t); err != nil {
			errs = append(errs, fmt.Errorf("audit: checkpoint tenant %s: %w", t, err))
			if ctx.Err() != nil {
				break
			}
		}
	}
	return errors.Join(errs...)
}

// ReasonNoCheckpoints is the CheckpointReport.Reason of a chain that carries no
// checkpoint at all. It is the NAME of the empty case — "nothing has been attested
// yet" — and never the name of a failure. Anything that renders a verdict must
// match on this exact string rather than on OK alone.
const ReasonNoCheckpoints = "no-checkpoints"

// CheckpointStatus is the checkpoint verdict as the THREE answers it really has,
// so no caller has to re-derive them from a boolean that cannot carry them:
// verified, verified BAD, or nothing to verify yet. A young ledger whose scheduler
// has not fired lands on the third; rendering that as the second trains an operator
// to ignore the red that one day is real (the same distinction core/dr/verify.go
// already draws for a restored young estate).
type CheckpointStatus string

const (
	// CheckpointStatusOK: at least one checkpoint exists and every signature and
	// link verified.
	CheckpointStatusOK CheckpointStatus = "ok"
	// CheckpointStatusFailed: a checkpoint exists and did NOT verify — tamper
	// evidence, and the loud case. It is also the DENY-CLOSED answer for any report
	// that is neither a clean pass nor the named empty case (a zero-value report
	// from an error path lands here): "I could not look" is never "there was
	// nothing to look at".
	CheckpointStatusFailed CheckpointStatus = "failed"
	// CheckpointStatusPending: no checkpoint has been written yet. NOT a failure —
	// structural chain verification (store.AuditLog.Verify) already proves the chain
	// is internally consistent; the off-box anchor is simply not written yet.
	//
	// HONEST LIMIT, measured — a ledger TRUNCATED below its first checkpoint also
	// arrives here: zero checkpoints, and the surviving prefix still self-verifies.
	// This tri-state does not make that case worse. It was already the verdict
	// before the state existed: /v1/audit/verify answered ok=true for a truncated
	// ledger with its checkpoints deleted (measured against a tampered SQLite file,
	// pre-change binary), because the endpoint has always treated "no-checkpoints"
	// as trustworthy. What defends against that truncation is elsewhere and
	// unchanged, and it is ONE control, not three: an OFF-BOX copy of the anchor —
	// a retained expected tip to compare against (docs/SECURITY-HARDENING.md,
	// the security-hardening guide R1).
	//
	// This sentence used to also name the per-event Ed25519 signatures and the
	// system chain's own checkpoints. Neither proves completeness, and saying so
	// here was the same overclaim corrected in the alarm above: signatures
	// authenticate the events that REMAIN and carry no retained length, and
	// CheckpointAll anchors each tenant and the system tenant as SEPARATE chains —
	// it never copies a tenant tip into the system chain, so the system chain
	// witnesses nothing about a tenant's. Distinguishing "young" from "truncated" needs
	// a signal this report does not carry (checkpoint cadence vs. ledger age) and
	// is a design decision, not something to infer here.
	CheckpointStatusPending CheckpointStatus = "pending"
)

// CheckpointReport is the outcome of verifying the signed checkpoints in a chain.
type CheckpointReport struct {
	// Checkpoints is the number of checkpoint events found.
	Checkpoints int
	// OK is true only when at least one checkpoint was found and every
	// checkpoint's signature and link verified. It deliberately stays FALSE for a
	// chain with no checkpoints (attesting nothing is not a pass — the
	// vacuous-truth rule); callers that must tell "not yet" from "bad" read Status.
	OK bool
	// FirstBadSeq is the sequence of the first bad checkpoint, or 0.
	FirstBadSeq int64
	// Reason describes the first failure ("checkpoint-sig-invalid",
	// "checkpoint-link-mismatch"), ReasonNoCheckpoints for the empty case, or "".
	Reason string
	// LatestAttestedSeq is the highest sequence number a valid checkpoint attests.
	LatestAttestedSeq int64
}

// Status collapses the report into the three answers a caller may render. The
// empty case is recognized by BOTH its count and its name: a report claiming zero
// checkpoints without saying ReasonNoCheckpoints did not come from a completed
// walk (a zero value, a future code path), and is reported failed rather than
// waved through as "not yet".
func (r CheckpointReport) Status() CheckpointStatus {
	if r.OK {
		return CheckpointStatusOK
	}
	if r.Checkpoints == 0 && r.Reason == ReasonNoCheckpoints {
		return CheckpointStatusPending
	}
	return CheckpointStatusFailed
}

// VerifyCheckpoints walks a tenant's chain and verifies every checkpoint's
// Ed25519 signature against pub, and that each checkpoint links to the recorded
// hash of the event it attests. Run it alongside store.AuditLog.Verify (which
// proves the chain is internally consistent): together they prove the chain up to
// each checkpoint is authentic and was signed by the holder of the private key.
func VerifyCheckpoints(ctx context.Context, log store.AuditLog, pub ed25519.PublicKey) (CheckpointReport, error) {
	if len(pub) != ed25519.PublicKeySize {
		return CheckpointReport{}, fmt.Errorf("audit: bad public key size %d", len(pub))
	}
	// Single implementation: delegate to the generalized verifier with one Ed25519
	// candidate (preserves the pre behavior and signature exactly).
	return VerifyCheckpointsWith(ctx, log, NewCheckpointVerifier().AddEd25519(pub))
}

func (r *CheckpointReport) fail(seq int64, reason string) {
	if r.Reason == "" {
		r.FirstBadSeq = seq
		r.Reason = reason
	}
}

// checkpointPreimage builds the canonical, domain-separated, length-prefixed
// preimage an external verifier reproduces: domain ‖ tenant ‖ seq(8 BE) ‖ hash.
func checkpointPreimage(tenant string, seq int64, hash []byte) []byte {
	var buf []byte
	buf = lenPrefix(buf, []byte(checkpointDomain))
	buf = lenPrefix(buf, []byte(tenant))
	var s [8]byte
	binary.BigEndian.PutUint64(s[:], uint64(seq))
	buf = append(buf, s[:]...)
	buf = lenPrefix(buf, hash)
	return buf
}

func lenPrefix(dst, b []byte) []byte {
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(b)))
	dst = append(dst, n[:]...)
	return append(dst, b...)
}
