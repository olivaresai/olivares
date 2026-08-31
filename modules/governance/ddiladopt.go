// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

var contentRevisionPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// PolicyAdoption is the policy plane of a verified DDIL bundle, as plain values — this
// package deliberately does not import core/ddil (the bundle format stays consumer-free;
// cmd/olivares is the composition point).
type PolicyAdoption struct {
	Snapshot        []byte
	Revision        string
	BundleCreatedAt time.Time
	// MaxStaleness follows the bundle verbatim: a positive duration installs the
	// per-tenant override; zero clears it so the deployment default applies.
	MaxStaleness time.Duration
	Actor        string
}

type AdoptReport struct {
	Adopted         bool   `json:"adopted"`
	Reason          string `json:"reason"`
	SurfaceRevision int64  `json:"surface_revision,omitempty"`
}

// AdoptBundlePolicy persists the policy plane of a VERIFIED bundle: an active append-only
// revision on the cedar-ddil surface + the durable freshness record, audited in the same
// transaction. It is package-level over store.Store so the CLI (no live module) and the
// future sync loop share ONE implementation; the LIVE engine picks the adoption up on the
// next ReloadActivePDP/boot (restart-safe by design — boot restores, it no longer re-stamps).
func AdoptBundlePolicy(ctx context.Context, st store.Store, tenant model.TenantID, in PolicyAdoption, now time.Time) (AdoptReport, error) {
	if err := validatePolicyAdoption(in); err != nil {
		return AdoptReport{}, err
	}
	if _, err := compileGrantSet(string(in.Snapshot)); err != nil {
		return AdoptReport{}, fmt.Errorf("governance: DDIL policy snapshot does not compile: %w", err)
	}
	freshness := FreshnessRecord{
		RefreshedAt: in.BundleCreatedAt, MaxStaleness: in.MaxStaleness,
		AdoptedRevision: in.Revision, AdoptedCreatedAt: in.BundleCreatedAt,
	}
	for attempt := 0; attempt < maxDecisionRetries; attempt++ {
		var report AdoptReport
		err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
			// Pin the one canonical authorization generation before reading any durable
			// policy input. Every epoch-aware authored/managed/adopted writer serializes
			// through this exact row, so the union classified below cannot cross snapshots.
			if err := lockPolicyAuthorizationEpoch(ctx, sc); err != nil {
				return err
			}

			free, _, err := latestActiveContent(ctx, sc, surfaceCedar)
			if err != nil {
				return err
			}
			managed, _, err := latestActiveContent(ctx, sc, surfaceCedarManaged)
			if err != nil {
				return err
			}
			adopted, adoptedFound, err := latestActiveContent(ctx, sc, surfaceCedarDDIL)
			if err != nil {
				return err
			}
			rec, found, err := readPolicyFreshness(ctx, sc)
			if err != nil {
				return err
			}
			mode, err := decideAdoption(rec, found, adopted, adoptedFound, in)
			if err != nil {
				return err
			}
			if mode == adoptRefuse {
				// Any non-exact delivery at an equal/older signed time is a replay or
				// rollback. Refuse before the epoch CAS: this branch performs zero writes.
				return fmt.Errorf(
					"governance: DDIL policy replay/rollback refused: bundle created_at %s is not strictly newer than adopted_created_at %s",
					in.BundleCreatedAt.UTC().Format(time.RFC3339Nano),
					rec.AdoptedCreatedAt.UTC().Format(time.RFC3339Nano),
				)
			}

			prospectiveAdopted := adopted
			if mode == adoptFull {
				prospectiveAdopted = string(in.Snapshot)
			}
			prospective := mergeCedarSources(mergeCedarSources(free, managed), prospectiveAdopted)
			if _, err := compileGrantSet(prospective); err != nil {
				return fmt.Errorf("governance: DDIL policy would break the tenant's active Cedar union: %w", err)
			}

			switch mode {
			case adoptNoop:
				// Only the exact signed tuple is idempotent: content revision, bundle
				// created_at and signed max_staleness. It writes neither epoch nor data.
				report = AdoptReport{Reason: "already adopted"}
				return nil
			case adoptReattest:
				// The epoch CAS is the first write. Freshness/bound authority changes
				// authorization even though the policy bytes remain identical.
				if err := advancePolicyAuthorizationEpoch(ctx, sc); err != nil {
					return err
				}
				// Identical policy, freshly re-signed by the center: advance the durable
				// freshness clock and bound from the new signed envelope WITHOUT appending
				// a duplicate revision row (cedar-ddil already holds these exact bytes).
				// This is what lets a stable-policy site stay non-expired across a gap by
				// carrying a re-attestation, rather than expiring deny-closed.
				if err := upsertPolicyFreshness(ctx, sc, freshness); err != nil {
					return err
				}
				seq, err := appendAdoptionAudit(ctx, sc, in, "governance.ddil.policy_reattest", "", 0, now)
				if err != nil {
					return err
				}
				report = AdoptReport{Adopted: true, Reason: adoptionReason("re-attested (unchanged policy, refreshed signed clock)", seq)}
				return nil
			default: // adoptFull
				// The CAS remains the first write; revision, freshness and audit all
				// follow it in this transaction and therefore roll back with it.
				if err := advancePolicyAuthorizationEpoch(ctx, sc); err != nil {
					return err
				}
				note := fmt.Sprintf("DDIL revision=%s bundle_created_at=%s",
					in.Revision, in.BundleCreatedAt.UTC().Format(time.RFC3339))
				num, id, err := appendRevision(ctx, sc, surfaceCedarDDIL, string(in.Snapshot), in.Actor, true, true, note)
				if err != nil {
					return err
				}
				if err := upsertPolicyFreshness(ctx, sc, freshness); err != nil {
					return err
				}
				seq, err := appendAdoptionAudit(ctx, sc, in, "governance.ddil.policy_adopt", id, num, now)
				if err != nil {
					return err
				}
				report = AdoptReport{Adopted: true, Reason: adoptionReason("adopted", seq), SurfaceRevision: num}
				return nil
			}
		})
		if err == nil {
			return report, nil
		}
		if isConflict(err) {
			continue
		}
		return AdoptReport{}, fmt.Errorf("governance: adopt DDIL policy: %w", err)
	}
	return AdoptReport{}, fmt.Errorf("governance: adopt DDIL policy conflicted repeatedly; please retry")
}

// adoptionMode classifies a verified bundle against the tenant's current freshness record.
type adoptionMode int

const (
	adoptFull     adoptionMode = iota // new policy content, strictly newer → append + stamp
	adoptReattest                     // same content, strictly newer signed time → re-stamp only
	adoptNoop                         // exact same bundle already adopted → no-op
	adoptRefuse                       // different content, not strictly newer → replay/rollback
)

// decideAdoption is the single classification shared by the write transaction. "newer" is
// measured against the AUTHENTICATED bundle creation time, never a wall clock, so a courier
// delay or a replayed bundle can never masquerade as fresh.
func decideAdoption(
	current FreshnessRecord,
	freshnessFound bool,
	adopted string,
	adoptedFound bool,
	in PolicyAdoption,
) (adoptionMode, error) {
	hasRevisionAnchor := current.AdoptedRevision != ""
	hasCreatedAnchor := !current.AdoptedCreatedAt.IsZero()
	if !adoptedFound && !hasRevisionAnchor && !hasCreatedAnchor {
		// No prior signed adoption. A local-only freshness row is valid and will be
		// replaced atomically by the first signed bundle's complete anchors.
		return adoptFull, nil
	}
	if !freshnessFound || !adoptedFound || !hasRevisionAnchor || !hasCreatedAnchor {
		return adoptRefuse, fmt.Errorf("governance: inconsistent DDIL durable adoption state: adopted policy and replay anchors must be present together")
	}
	if current.RefreshedAt.IsZero() || !current.RefreshedAt.Equal(current.AdoptedCreatedAt) {
		return adoptRefuse, fmt.Errorf("governance: inconsistent DDIL durable adoption state: signed freshness clock does not equal adopted created_at")
	}
	if policyContentRevision([]byte(adopted)) != current.AdoptedRevision {
		return adoptRefuse, fmt.Errorf("governance: inconsistent DDIL durable adoption state: active adopted policy does not match its revision anchor")
	}

	sameRevision := current.AdoptedRevision == in.Revision
	exact := sameRevision && current.AdoptedCreatedAt.Equal(in.BundleCreatedAt) &&
		current.MaxStaleness == in.MaxStaleness
	newer := in.BundleCreatedAt.After(current.AdoptedCreatedAt)
	switch {
	case exact:
		return adoptNoop, nil
	case sameRevision && newer:
		return adoptReattest, nil
	case !newer:
		return adoptRefuse, nil
	default:
		return adoptFull, nil
	}
}

// appendAdoptionAudit records an adoption/re-attestation on the ledger in the SAME
// transaction as the revision/freshness write and returns the resulting seq (0 when a
// configured degrade mode dropped the evidence — a real store error still rolls back).
func appendAdoptionAudit(ctx context.Context, sc store.Scope, in PolicyAdoption, action string, targetID model.ID, surfaceRevision int64, now time.Time) (int64, error) {
	meta := map[string]any{
		"surface":           surfaceCedarDDIL,
		"revision":          in.Revision,
		"bundle_created_at": in.BundleCreatedAt.UTC().Format(time.RFC3339),
		"max_staleness":     durableBoundString(in.MaxStaleness),
		"imported_at":       now.UTC().Format(time.RFC3339Nano),
	}
	if surfaceRevision > 0 {
		meta["surface_revision"] = surfaceRevision
	}
	draft := model.AuditDraft{
		Actor: in.Actor, ActorKind: model.ActorSystem,
		Action: action, TargetKind: revisionKind, TargetID: targetID, Meta: meta,
	}
	ev, err := sc.Audit().Append(ctx, draft)
	if err != nil {
		return 0, err
	}
	return ev.Seq, nil
}

// adoptionReason annotates the success reason, flagging a degrade-mode evidence drop loudly.
// Owner decision: the drop must not keep an even staler policy active — the append-only
// cedar-ddil revision (and the durable freshness record) remain provenance either way.
func adoptionReason(base string, seq int64) string {
	if seq == 0 {
		return base + "; WARNING: audit event dropped by configured degrade mode; the durable revision/freshness record remains provenance"
	}
	return base
}

func validatePolicyAdoption(in PolicyAdoption) error {
	if len(in.Snapshot) == 0 || strings.TrimSpace(string(in.Snapshot)) == "" {
		return fmt.Errorf("governance: DDIL policy snapshot is empty")
	}
	if len(in.Snapshot) > maxPolicyContentBytes {
		return fmt.Errorf("governance: DDIL policy snapshot exceeds %d bytes", maxPolicyContentBytes)
	}
	if !contentRevisionPattern.MatchString(in.Revision) {
		return fmt.Errorf("governance: DDIL policy revision %q is not sha256:<64 lowercase hex>", in.Revision)
	}
	want := policyContentRevision(in.Snapshot)
	if in.Revision != want {
		return fmt.Errorf("governance: DDIL policy revision/bytes mismatch: got %s, recomputed %s", in.Revision, want)
	}
	if containsInlineKey(string(in.Snapshot)) {
		return fmt.Errorf("governance: DDIL policy snapshot must not contain an inline credential (sk-ant-…)")
	}
	if in.MaxStaleness < 0 {
		return fmt.Errorf("governance: DDIL policy max staleness must not be negative")
	}
	if in.BundleCreatedAt.IsZero() {
		return fmt.Errorf("governance: DDIL bundle created_at is required")
	}
	return nil
}

func policyContentRevision(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func durableBoundString(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	return strings.TrimSpace(d.String())
}
