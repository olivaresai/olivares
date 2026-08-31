// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"fmt"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// policyFreshnessKind is the single durable policy-freshness row owned by each
// tenant. Unlike the process-local scoped-engine snapshot, this record survives a
// restart, so boot can restore the signed/published trust anchor instead of extending it.
const (
	policyFreshnessKind  model.Kind = "governance.policy_freshness"
	policyFreshnessTable            = "governance_policy_freshness"
)

const (
	colFreshRefreshedAt     = "refreshed_at"
	colFreshMaxStaleness    = "max_staleness"
	colFreshAdoptedRevision = "adopted_revision"
	colFreshAdoptedCreated  = "adopted_created_at"
)

// FreshnessRecord is the tenant's durable policy-freshness state. MaxStaleness
// zero means there is no per-tenant override and the deployment default applies.
type FreshnessRecord struct {
	RefreshedAt      time.Time
	MaxStaleness     time.Duration
	AdoptedRevision  string
	AdoptedCreatedAt time.Time
}

// PolicyFreshness returns the tenant's durable policy-freshness record (zero values
// when none exists yet).
func PolicyFreshness(ctx context.Context, st store.Store, tenant model.TenantID) (FreshnessRecord, bool, error) {
	var out FreshnessRecord
	var found bool
	err := st.View(ctx, tenant, func(sc store.Scope) error {
		var err error
		out, found, err = readPolicyFreshness(ctx, sc)
		return err
	})
	return out, found, err
}

func readPolicyFreshness(ctx context.Context, sc store.Scope) (FreshnessRecord, bool, error) {
	repo, err := sc.Ext(policyFreshnessKind)
	if err != nil {
		return FreshnessRecord{}, false, err
	}
	recs, _, err := repo.List(ctx, model.Query{Limit: 2})
	if err != nil {
		return FreshnessRecord{}, false, err
	}
	if len(recs) == 0 {
		return FreshnessRecord{}, false, nil
	}
	if len(recs) > 1 {
		return FreshnessRecord{}, false, fmt.Errorf("governance: tenant has multiple policy-freshness records")
	}
	out, err := decodePolicyFreshness(recs[0])
	return out, err == nil, err
}

func decodePolicyFreshness(rec model.Record) (FreshnessRecord, error) {
	refreshed, err := parseFreshnessTime(rec.String(colFreshRefreshedAt), colFreshRefreshedAt)
	if err != nil {
		return FreshnessRecord{}, err
	}
	adoptedAt, err := parseFreshnessTime(rec.String(colFreshAdoptedCreated), colFreshAdoptedCreated)
	if err != nil {
		return FreshnessRecord{}, err
	}
	var bound time.Duration
	if raw := rec.String(colFreshMaxStaleness); raw != "" {
		bound, err = time.ParseDuration(raw)
		if err != nil || bound <= 0 {
			return FreshnessRecord{}, fmt.Errorf("governance: invalid durable %s %q", colFreshMaxStaleness, raw)
		}
	}
	return FreshnessRecord{
		RefreshedAt: refreshed, MaxStaleness: bound,
		AdoptedRevision: rec.String(colFreshAdoptedRevision), AdoptedCreatedAt: adoptedAt,
	}, nil
}

func parseFreshnessTime(raw, field string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	ts, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("governance: invalid durable %s %q: %w", field, raw, err)
	}
	return ts.UTC(), nil
}

func freshnessRecordValues(in FreshnessRecord) model.Record {
	rec := model.Record{
		colFreshRefreshedAt:     model.NewTimestamp(in.RefreshedAt).String(),
		colFreshMaxStaleness:    "",
		colFreshAdoptedRevision: in.AdoptedRevision,
		colFreshAdoptedCreated:  nil,
	}
	if in.MaxStaleness > 0 {
		rec[colFreshMaxStaleness] = in.MaxStaleness.String()
	}
	if !in.AdoptedCreatedAt.IsZero() {
		rec[colFreshAdoptedCreated] = model.NewTimestamp(in.AdoptedCreatedAt).String()
	}
	return rec
}

// upsertPolicyFreshness replaces the complete record. Adoption uses it because the
// signed bundle authoritatively supplies the clock, bound and replay anchors together.
func upsertPolicyFreshness(ctx context.Context, sc store.Scope, in FreshnessRecord) error {
	repo, err := sc.Ext(policyFreshnessKind)
	if err != nil {
		return err
	}
	existing, found, err := findOne(ctx, repo)
	if err != nil {
		return err
	}
	values := freshnessRecordValues(in)
	if !found {
		_, err = repo.Create(ctx, values)
		return err
	}
	for key, value := range values {
		existing[key] = value
	}
	_, err = repo.Update(ctx, existing)
	return err
}

// upsertPolicyRefreshedAt deliberately changes ONLY refreshed_at: an authoritative
// local publish/reprojection refreshes policy, but must not erase a bundle-supplied
// bound or its anti-replay anchors.
func upsertPolicyRefreshedAt(ctx context.Context, sc store.Scope, refreshedAt time.Time) error {
	repo, err := sc.Ext(policyFreshnessKind)
	if err != nil {
		return err
	}
	existing, found, err := findOne(ctx, repo)
	if err != nil {
		return err
	}
	stamp := model.NewTimestamp(refreshedAt).String()
	if !found {
		_, err = repo.Create(ctx, model.Record{
			colFreshRefreshedAt: stamp, colFreshMaxStaleness: "",
			colFreshAdoptedRevision: "", colFreshAdoptedCreated: nil,
		})
		return err
	}
	existing[colFreshRefreshedAt] = stamp
	_, err = repo.Update(ctx, existing)
	return err
}

// validatePolicyFreshnessAuthority checks that an adopted DDIL surface and its signed
// clock/bound/replay anchors are one indivisible authority tuple. It is shared by every
// local writer and by reload; accepting a partially present tuple would let a missing
// signed anchor be interpreted as a locally renewable clock.
func validatePolicyFreshnessAuthority(
	current FreshnessRecord,
	found bool,
	adopted string,
	adoptedFound bool,
) (signed bool, err error) {
	hasRevisionAnchor := current.AdoptedRevision != ""
	hasCreatedAnchor := !current.AdoptedCreatedAt.IsZero()
	signed = adoptedFound || hasRevisionAnchor || hasCreatedAnchor
	if signed {
		if !found || !adoptedFound || !hasRevisionAnchor || !hasCreatedAnchor {
			return false, fmt.Errorf("governance: inconsistent DDIL durable adoption state: adopted policy and replay anchors must be present together")
		}
		if current.RefreshedAt.IsZero() || !current.RefreshedAt.Equal(current.AdoptedCreatedAt) {
			return false, fmt.Errorf("governance: inconsistent DDIL durable adoption state: signed freshness clock does not equal adopted created_at")
		}
		if policyContentRevision([]byte(adopted)) != current.AdoptedRevision {
			return false, fmt.Errorf("governance: inconsistent DDIL durable adoption state: active adopted policy does not match its revision anchor")
		}
		return true, nil
	}
	if found && current.MaxStaleness > 0 {
		return false, fmt.Errorf("governance: inconsistent DDIL durable adoption state: signed staleness bound has no adopted policy anchors")
	}
	return false, nil
}

// readLocalPolicyFreshnessAuthority reads and validates the durable record before a
// locally authored Cedar mutation. A caller MUST have already locked the exact epoch
// before calling this, because adopted content and anchors are inputs to its union.
func readLocalPolicyFreshnessAuthority(
	ctx context.Context,
	sc store.Scope,
	adopted string,
	adoptedFound bool,
) (FreshnessRecord, bool, error) {
	current, found, err := readPolicyFreshness(ctx, sc)
	if err != nil {
		return FreshnessRecord{}, false, err
	}
	signed, err := validatePolicyFreshnessAuthority(current, found, adopted, adoptedFound)
	if err != nil {
		return FreshnessRecord{}, false, err
	}
	return current, signed, nil
}

// sampleLocalPolicyFreshness takes time from the database before the caller's epoch CAS.
// Sampling is a fallible READ, not a write: doing it before the CAS means a missing clock
// aborts with zero durable effects, while the CAS remains the transaction's first write.
// Signed DDIL authority is returned verbatim and must never call TransactionClock.
func sampleLocalPolicyFreshness(
	ctx context.Context,
	sc store.Scope,
	current FreshnessRecord,
	signed bool,
) (FreshnessRecord, error) {
	if signed {
		return current, nil
	}
	clock, ok := sc.(store.TransactionClock)
	if !ok {
		return FreshnessRecord{}, fmt.Errorf("governance: scope lacks authoritative transaction clock")
	}
	now, err := clock.TransactionNow(ctx)
	if err != nil {
		return FreshnessRecord{}, fmt.Errorf("governance: read authoritative transaction clock: %w", err)
	}
	if now.IsZero() {
		return FreshnessRecord{}, fmt.Errorf("governance: authoritative transaction clock returned zero")
	}
	current.RefreshedAt = now.Time()
	return current, nil
}

// persistSampledLocalPolicyFreshness writes the DB-time sample already obtained by
// sampleLocalPolicyFreshness. It follows the epoch CAS and policy selection writes in
// the SAME Mutate; a later audit failure rolls the complete authority change back.
func persistSampledLocalPolicyFreshness(
	ctx context.Context,
	sc store.Scope,
	current FreshnessRecord,
	signed bool,
) error {
	if signed {
		return nil
	}
	return upsertPolicyRefreshedAt(ctx, sc, current.RefreshedAt)
}
