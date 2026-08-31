// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package dr

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// BuildOptions carry the bundle metadata the caller has already determined (the
// snapshot it took and the keys it sealed) into the manifest.
type BuildOptions struct {
	// EngineKind is "sqlite" or "postgres".
	EngineKind string
	// Version is the engine binary version (operability).
	Version string
	// Store is the snapshot descriptor (method/file/size/sha256), filled by the
	// caller after it took the snapshot.
	Store StoreSnapshot
	// Keys is the list of sealed signing keys in the bundle, by public fingerprint.
	Keys []KeyRef
	// TipMatch is TipExact (manifest built from the snapshot itself) or
	// TipAdvisory (built from the live store). See the constants.
	TipMatch string
	// Now is the backup instant (RFC3339 UTC is recorded). The caller injects it so
	// the manifest is deterministic in tests.
	Now time.Time
	// Notes is free-form operator context (no secrets).
	Notes string
}

// BuildManifest reads every tenant chain tip (including the system tenant) from
// st and verifies each chain at backup time, producing the control record for a
// DR bundle. eventPub is the on-box audit verification key (per-event signatures);
// cpVerifier covers the checkpoint key(s). A backup is only ever CERTIFIED over a
// chain that is already green — a tenant whose chain does not verify is recorded
// with VerifiedAtBackup=false and a reason, so the caller can refuse to capture a
// corrupt ledger as if it were a good restore point.
func BuildManifest(ctx context.Context, st store.Store, eventPub ed25519.PublicKey, cpVerifier *audit.CheckpointVerifier, opts BuildOptions) (*Manifest, error) {
	if opts.TipMatch != TipExact && opts.TipMatch != TipAdvisory {
		return nil, fmt.Errorf("dr: BuildManifest invalid TipMatch %q", opts.TipMatch)
	}
	tenants, err := enumerateTenants(ctx, st)
	if err != nil {
		return nil, err
	}
	// EVERY tenant gets a real, verified TenantTip — including one whose service is
	// withdrawn. This used to substitute a free-text note for the tip of a withdrawn
	// tenant, because the tip read went through the guarded View and was denied.
	// A note is not a control record: RestoreVerify only ever verifies the tenants
	// listed in m.Tenants, so that tenant's chain, signatures, checkpoints and tip
	// were never checked and `dr verify` could still report PASSED. The physical
	// rows were in the copy; the certification of continuity needed to trust them
	// was not, and the report said otherwise. Reading a chain tip is custodial, so
	// it goes through store.Custody and is no longer denied.
	notes := opts.Notes

	m := &Manifest{
		Format:     ManifestFormat,
		CreatedAt:  opts.Now.UTC().Format(time.RFC3339),
		EngineKind: opts.EngineKind,
		Version:    opts.Version,
		Store:      opts.Store,
		Keys:       opts.Keys,
		TipMatch:   opts.TipMatch,
		Notes:      notes,
	}
	for _, t := range tenants {
		tip, err := tenantTip(ctx, st, t, eventPub, cpVerifier)
		if err != nil {
			return nil, fmt.Errorf("dr: tip for tenant %s: %w", t, err)
		}
		m.Tenants = append(m.Tenants, tip)
	}
	return m, nil
}

// enumerateTenants returns every business tenant plus the reserved system tenant,
// de-duplicated and order-stable. Service state is NOT a filter here: a tenant
// whose service is withdrawn is still in the estate, still in the physical copy,
// and still owed a verifiable chain tip — the whole point of the grace period.
//
// On Postgres without a BYPASSRLS admin pool it FAILS CLOSED: ListOrgs returns
// store.ErrEnumerationNotAuthoritative and this function propagates it, so a backup
// is refused rather than written over a tenant set nobody enumerated.
//
// It used to return an empty set there, and this comment used to say so and put
// the duty on the caller ("must check the count"). Moved the refusal into the
// read itself for exactly that reason — a duty spread across callers is inherited
// by whoever is written next — so there is no count for a caller to check any more.
func enumerateTenants(ctx context.Context, st store.Store) ([]model.TenantID, error) {
	var ids []model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		orgs, err := sys.ListOrgs(ctx)
		if err != nil {
			return err
		}
		for _, o := range orgs {
			ids = append(ids, o.TenantID)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	// The system tenant holds auth + cross-tenant events; it has its own chain and
	// must be captured even though it is not a business org.
	ids = append(ids, model.SystemTenantID)
	seen := make(map[model.TenantID]bool, len(ids))
	out := ids[:0]
	for _, id := range ids {
		if id.IsZero() || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, nil
}

// tenantTip reads one tenant's chain tip and verifies the chain, per-event
// signatures and checkpoints at backup time.
func tenantTip(ctx context.Context, st store.Store, t model.TenantID, eventPub ed25519.PublicKey, cpVerifier *audit.CheckpointVerifier) (TenantTip, error) {
	tip := TenantTip{Tenant: t.String(), System: t.IsSystem()}
	// Custody, not View: reading a chain tip is a custodial act, so a tenant whose
	// service is withdrawn still gets a real, verified tip in the manifest.
	err := st.Custody(ctx, t, func(sc store.CustodyScope) error {
		head, has, err := sc.Audit().Head(ctx)
		if err != nil {
			return err
		}
		if !has {
			// An empty chain verifies vacuously.
			tip.VerifiedAtBackup = true
			return nil
		}
		tip.HeadSeq = head.Seq
		tip.HeadHash = hex.EncodeToString(head.Hash)

		chain, err := sc.Audit().Verify(ctx, 1)
		if err != nil {
			return err
		}
		events, err := audit.VerifyEvents(ctx, sc.Audit(), eventPub)
		if err != nil {
			return err
		}
		var cp audit.CheckpointReport
		if cpVerifier != nil && !cpVerifier.Empty() {
			cp, err = audit.VerifyCheckpointsWith(ctx, sc.Audit(), cpVerifier)
			if err != nil {
				return err
			}
		}
		tip.Checkpoints = cp.Checkpoints
		tip.VerifiedAtBackup = chain.OK && events.OK && (cpVerifier == nil || cpVerifier.Empty() || cp.OK)
		tip.VerifyReason = firstVerifyReason(chain, events, cp, cpVerifier)
		return nil
	})
	if err != nil {
		return TenantTip{}, err
	}
	return tip, nil
}

// firstVerifyReason names the first failure across the three checks, or "".
func firstVerifyReason(chain store.VerifyReport, events audit.EventSigReport, cp audit.CheckpointReport, v *audit.CheckpointVerifier) string {
	if !chain.OK {
		return "chain:" + chain.Reason
	}
	if !events.OK {
		return "events:" + events.Reason
	}
	if v != nil && !v.Empty() && !cp.OK {
		return "checkpoints:" + cp.Reason
	}
	return ""
}
