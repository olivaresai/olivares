// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package dr

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// RestoreReport is the structured outcome of verifying a restored store against
// the manifest it was restored from. OK is true only when the restore is
// ledger-continuity-safe: every tenant chain is internally consistent, every
// per-event signature verifies against the restored key, every checkpoint
// verifies, the restored signing-key fingerprint matches the manifest, and (in
// TipExact mode) every restored tip equals the manifest tip.
type RestoreReport struct {
	OK       bool           `json:"ok"`
	Engine   string         `json:"engine"`
	TipMatch string         `json:"tip_match_mode"`
	Tenants  []TenantVerify `json:"tenants"`
	Key      KeyVerify      `json:"audit_key"`
	Problems []string       `json:"problems,omitempty"`
}

// TenantVerify is one tenant chain's post-restore verification.
type TenantVerify struct {
	Tenant           string `json:"tenant"`
	System           bool   `json:"system,omitempty"`
	ChainOK          bool   `json:"chain_ok"`
	ChainReason      string `json:"chain_reason,omitempty"`
	EventsOK         bool   `json:"events_ok"`
	EventsReason     string `json:"events_reason,omitempty"`
	EventsSigned     int    `json:"events_signed"`
	EventsTotal      int    `json:"events_total"`
	CheckpointsOK    bool   `json:"checkpoints_ok"`
	CheckpointReason string `json:"checkpoint_reason,omitempty"`
	// CheckpointNote is an ADVISORY note (not a failure) — e.g. a restored young
	// estate that never sealed a checkpoint. Integrity is still proven by the chain
	// verify + per-event signatures; the missing off-box anchor is informational.
	CheckpointNote string `json:"checkpoint_note,omitempty"`
	// TipOK is the tip-match outcome. In TipExact mode a false TipOK fails the
	// tenant; in TipAdvisory mode TipOK is informational (TipNote explains).
	TipOK bool `json:"tip_ok"`
	// Missing is true when the manifest recorded a non-empty chain for this tenant
	// but the restored store has NO events for it — a lost chain. It ALWAYS fails
	// the restore, in both tip modes (an empty restored chain verifies vacuously,
	// so the chain/tip checks would otherwise miss it in advisory mode).
	Missing     bool   `json:"missing,omitempty"`
	RestoredSeq int64  `json:"restored_seq"`
	ManifestSeq int64  `json:"manifest_seq"`
	TipNote     string `json:"tip_note,omitempty"`
}

// KeyVerify is the restored audit signing key's fingerprint check.
type KeyVerify struct {
	Match       bool   `json:"match"`
	ManifestPub string `json:"manifest_pub_sha256"`
	RestoredPub string `json:"restored_pub_sha256"`
	Note        string `json:"note,omitempty"`
}

// RestoreVerify proves a restored store is ledger-continuity-safe against the
// manifest it was restored from. st is the restored store; auditPub is the PUBLIC
// key of the restored on-box audit signing key (the caller derives it from the
// restored key file); cpVerifier covers the checkpoint key(s). It runs, per tenant
// in the manifest: the chain verification (store Verify), the per-event signature
// check (against the RESTORED key — a wrong key fails here), the checkpoint
// signature check, and a tip comparison against the manifest. It also checks the
// restored key fingerprint against the manifest, so an omitted or substituted key
// is caught two ways (signatures fail AND fingerprint mismatches).
//
// This is the gate the runbook's restore step blocks on: a non-OK report means
// the restore is NOT safe (do not resume writes), with Problems listing why.
func RestoreVerify(ctx context.Context, st store.Store, m *Manifest, auditPub ed25519.PublicKey, cpVerifier *audit.CheckpointVerifier) (*RestoreReport, error) {
	if m == nil {
		return nil, fmt.Errorf("dr: RestoreVerify nil manifest")
	}
	rep := &RestoreReport{OK: true, Engine: m.EngineKind, TipMatch: m.TipMatch}

	// Audit key fingerprint continuity: the restored key must be the SAME key the
	// ledger was signed with, or every per-event signature is meaningless.
	rep.Key = verifyAuditKey(m, auditPub)
	if !rep.Key.Match {
		rep.OK = false
		rep.Problems = append(rep.Problems, "audit signing key fingerprint mismatch: "+rep.Key.Note)
	}

	for _, mt := range m.Tenants {
		t, err := model.ParseTenantID(mt.Tenant)
		if err != nil {
			return nil, fmt.Errorf("dr: manifest tenant %q: %w", mt.Tenant, err)
		}
		tv, err := verifyTenant(ctx, st, t, mt, auditPub, cpVerifier, m.TipMatch)
		if err != nil {
			return nil, err
		}
		rep.Tenants = append(rep.Tenants, tv)
		if !tv.ChainOK {
			rep.OK = false
			rep.Problems = append(rep.Problems, fmt.Sprintf("tenant %s chain: %s", short(mt.Tenant), tv.ChainReason))
		}
		if !tv.EventsOK {
			rep.OK = false
			rep.Problems = append(rep.Problems, fmt.Sprintf("tenant %s per-event signatures: %s", short(mt.Tenant), tv.EventsReason))
		}
		if !tv.CheckpointsOK {
			rep.OK = false
			rep.Problems = append(rep.Problems, fmt.Sprintf("tenant %s checkpoints: %s", short(mt.Tenant), tv.CheckpointReason))
		}
		if m.TipMatch == TipExact && !tv.TipOK {
			rep.OK = false
			rep.Problems = append(rep.Problems, fmt.Sprintf("tenant %s tip: restored seq %d != manifest seq %d", short(mt.Tenant), tv.RestoredSeq, tv.ManifestSeq))
		}
		// A non-empty manifest chain ENTIRELY absent from the restored store is a lost
		// chain — always a failure, in BOTH tip modes. An empty restored chain passes
		// the chain/event checks vacuously and, in advisory mode, the tip mismatch is
		// not fatal, so without this the loss would go undetected.
		if tv.Missing {
			rep.OK = false
			rep.Problems = append(rep.Problems, fmt.Sprintf("tenant %s chain is MISSING from the restored store (manifest tip seq %d)", short(mt.Tenant), mt.HeadSeq))
		}
	}

	// Detect tenants present in the restored store but ABSENT from the manifest — a
	// foreign/wrong bundle or a restore into an unclean store.
	//
	// This was `if eerr == nil { … }` and NOTHING ELSE, which made it the one place
	// in this file where a check that could not run left no trace. caught by
	// the external contrast: on Postgres without a BYPASSRLS admin pool the
	// enumeration fails closed, so eerr was non-nil, the block was skipped,
	// and rep.OK — which starts true — stayed true. RestoreVerify then certified a
	// restore whose foreign-bundle check never happened, and OK is what authorizes
	// resuming writes. The comment that stood here said this "can only miss an
	// extra, never produce a false positive"; that is true only if a false positive
	// means falsely REPORTING an extra tenant. At the level of the report it is the
	// dangerous direction: OK=true is exactly the false positive that matters, and
	// it is the same "I could not look" vs "there is nothing there" confusion that
	// Exists to end.
	//
	// So an enumeration that cannot be trusted is a PROBLEM, not a silence. The
	// sentence is a constant keyed on the cause and never interpolates eerr: these
	// strings are joined into the console's failure text (dr_handler.go), so a
	// wrapped store error would carry its DSN there exactly as it once did through
	// the API's error envelope.
	storeTenants, eerr := enumerateTenants(ctx, st)
	switch {
	case errors.Is(eerr, store.ErrEnumerationNotAuthoritative):
		rep.OK = false
		rep.Problems = append(rep.Problems, "cannot rule out a foreign bundle or an unclean restore: this store's tenant enumeration is NOT authoritative (Postgres with no BYPASSRLS admin pool), so the extra-tenant check could not run. Verify with --admin-dsn pointing at a NOSUPERUSER BYPASSRLS role")
	case eerr != nil:
		rep.OK = false
		rep.Problems = append(rep.Problems, "cannot rule out a foreign bundle or an unclean restore: enumerating the restored store's tenants failed, so the extra-tenant check could not run")
	default:
		inManifest := make(map[string]bool, len(m.Tenants))
		for _, mt := range m.Tenants {
			inManifest[mt.Tenant] = true
		}
		for _, t := range storeTenants {
			if !inManifest[t.String()] {
				rep.OK = false
				rep.Problems = append(rep.Problems, fmt.Sprintf("restored store has tenant %s not in the manifest (wrong bundle or unclean restore)", short(t.String())))
			}
		}
	}
	return rep, nil
}

func verifyAuditKey(m *Manifest, auditPub ed25519.PublicKey) KeyVerify {
	restored := PubFingerprint(auditPub)
	kr, ok := m.auditKey()
	if !ok {
		return KeyVerify{Match: false, RestoredPub: restored, Note: "manifest records no audit-role key"}
	}
	kv := KeyVerify{ManifestPub: kr.PubSHA256, RestoredPub: restored}
	kv.Match = keyMatches(kr.PubSHA256, restored)
	if !kv.Match {
		kv.Note = "restored audit key is not the key the ledger was signed with"
	}
	return kv
}

func verifyTenant(ctx context.Context, st store.Store, t model.TenantID, mt TenantTip, auditPub ed25519.PublicKey, cpVerifier *audit.CheckpointVerifier, tipMode string) (TenantVerify, error) {
	tv := TenantVerify{Tenant: mt.Tenant, System: mt.System, ManifestSeq: mt.HeadSeq}
	// Custody, not View: a restore must be provable for EVERY tenant in the bundle,
	// including one whose service was withdrawn when the backup was taken.
	err := st.Custody(ctx, t, func(sc store.CustodyScope) error {
		chain, err := sc.Audit().Verify(ctx, 1)
		if err != nil {
			return err
		}
		tv.ChainOK = chain.OK
		tv.ChainReason = chain.Reason

		events, err := audit.VerifyEvents(ctx, sc.Audit(), auditPub)
		if err != nil {
			return err
		}
		tv.EventsOK = events.OK
		tv.EventsReason = events.Reason
		tv.EventsSigned = events.Signed
		tv.EventsTotal = events.Events

		tv.CheckpointsOK = true
		if cpVerifier != nil && !cpVerifier.Empty() {
			cp, err := audit.VerifyCheckpointsWith(ctx, sc.Audit(), cpVerifier)
			if err != nil {
				return err
			}
			// a restored estate that never sealed a checkpoint (Checkpoints == 0)
			// is NOT a broken restore — the chain verify + per-event signatures already
			// prove the ledger's integrity; a checkpoint is an additional off-box anchor a
			// young estate legitimately lacks. The audit layer (anti-vacuous-truth,
			// 01e72b69) reports CheckpointReport.OK=false / "no-checkpoints" for the empty
			// case; classify that ADVISORY here rather than a hard restore failure. A
			// checkpoint that IS present but fails to verify (tamper/mismatch) still fails.
			if cp.Checkpoints == 0 {
				tv.CheckpointNote = cp.Reason
			} else {
				tv.CheckpointsOK = cp.OK
				tv.CheckpointReason = cp.Reason
			}
		}

		head, has, err := sc.Audit().Head(ctx)
		if err != nil {
			return err
		}
		var restoredHash string
		if has {
			tv.RestoredSeq = head.Seq
			restoredHash = hex.EncodeToString(head.Hash)
		}
		// The manifest recorded events for this tenant but the restored store has
		// none: the chain was lost (not merely truncated within RPO).
		tv.Missing = !has && mt.HeadSeq > 0
		tv.TipOK = tv.RestoredSeq == mt.HeadSeq && restoredHash == mt.HeadHash
		if !tv.TipOK {
			if tipMode == TipAdvisory {
				tv.TipNote = tipDelta(tv.RestoredSeq, mt.HeadSeq)
			} else {
				tv.TipNote = "restored tip differs from the backed-up tip (incomplete or wrong-bundle restore)"
			}
		}
		return nil
	})
	if err != nil {
		return TenantVerify{}, err
	}
	return tv, nil
}

// tipDelta describes an advisory-mode tip difference (Postgres online backup): a
// restored tip behind the manifest is the RPO window (events after the snapshot
// were lost); ahead means the live read trailed the snapshot. Neither is a
// failure — the chain self-verification above is the real guarantee.
func tipDelta(restored, manifest int64) string {
	switch {
	case restored < manifest:
		return fmt.Sprintf("restored %d events behind the live tip at backup (within RPO window)", manifest-restored)
	case restored > manifest:
		return fmt.Sprintf("restored %d events ahead of the manifest's live read (advisory tip lagged the snapshot)", restored-manifest)
	default:
		return "tip hash differs at equal sequence (advisory)"
	}
}

func short(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}
