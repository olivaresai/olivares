// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// prepareAttempt is the private admission Interface. Its caller supplies a
// candidate binding, never targets, money totals, a clock or an active scope.
// Invariants: current query authority precedes existence; exact replay preserves
// the original group without writes; a new admission requires active scope and
// verified binding, and writes the parent plus ALL targets under one ledger lock
// and DB instant, or writes nothing. No result grants dispatch. Scans, payloads,
// targets and known-rollback retries are finite; uncertain commit requires lookup.
// New()/boot deliberately supplies no verifier. Only fixtures reach this cut.
type prepareAttemptRequest struct {
	AttemptRef  AttemptRef
	Binding     AttemptBinding
	OwnerRef    string
	ReviewAfter model.Timestamp
}

type prepareAttemptResult struct {
	Decision string // admitted | denied
	Attempt  *AttemptView
	Denial   *attemptDenial
	Replayed bool
}

type attemptDenial struct {
	Code, Action, ReasonCode string
	PolicyID                 *model.ID
}

func (m *Module) prepareAttempt(ctx context.Context, tenant model.TenantID, req prepareAttemptRequest) (prepareAttemptResult, error) {
	if m.data == nil || m.attemptVerifier == nil {
		return prepareAttemptResult{}, attemptErr(errCodeCapabilityUnavailable, nil)
	}
	if err := businessTenant(tenant); err != nil {
		return prepareAttemptResult{}, err
	}
	if !validAttemptRef(req.AttemptRef) || !attemptText(req.OwnerRef, true) || req.ReviewAfter.Time().IsZero() {
		return prepareAttemptResult{}, attemptErr(errCodeInvalidAttempt, nil)
	}
	if err := validatePreparedBinding(req.Binding); err != nil {
		return prepareAttemptResult{}, attemptErr(errCodeInvalidAttempt, err)
	}
	// Detach caller-owned maps/slices before entering the transaction. The explicit
	// codec also bounds the whole binding, without changing any global decoder.
	body, err := marshalBounded(encodeBinding(req.Binding), maxAttemptPayloadBytes, "attempt binding")
	if err != nil {
		return prepareAttemptResult{}, attemptErr(errCodeInvalidAttempt, err)
	}
	var jb jsonBinding
	if err := strictUnmarshal(body, &jb); err != nil {
		return prepareAttemptResult{}, attemptErr(errCodeInvalidAttempt, err)
	}
	req.Binding, err = decodeBinding(jb)
	if err != nil {
		return prepareAttemptResult{}, attemptErr(errCodeInvalidAttempt, err)
	}
	bindingDigest := bindingDigestOf(tenant, req.AttemptRef, req.Binding)
	for retry := 0; retry < maxReserveRetries; retry++ {
		var result prepareAttemptResult
		err = m.mutateAttempt(ctx, tenant, func(sc store.Scope) error {
			result = prepareAttemptResult{}
			if err := attemptLock(ctx, sc); err != nil {
				return err
			}
			now, err := attemptNow(ctx, sc)
			if err != nil {
				return err
			}
			if _, err := m.verifyAttempt(ctx, sc, queryCheck(req.AttemptRef)); err != nil {
				return err
			}
			_, existing, found, err := findAttemptByRef(ctx, sc, req.AttemptRef)
			if err != nil {
				return err
			}
			if found {
				if existing.BindingDigest != bindingDigest || !bytesEqual(
					canonBytes(domainBinding, req.Binding.canon), canonBytes(domainBinding, existing.Binding.canon)) {
					return attemptErr(errCodeAttemptIdentityConflict, nil)
				}
				result = prepareAttemptResult{Decision: "admitted", Attempt: &existing, Replayed: true}
				return nil
			}
			scope, found, err := readLifecycleScope(ctx, sc)
			if err != nil {
				return err
			}
			if !found || scope.State != lifecycleActive || scope.ActivatedAt == nil {
				return attemptErr(errCodeLifecycleActivation, nil)
			}
			if now.Time().IsZero() || req.ReviewAfter.Time().Before(now.Time()) {
				return attemptErr(errCodeInvalidAttempt, nil)
			}
			if err := validatePreparedEntities(ctx, sc, req.Binding); err != nil {
				return err
			}
			targets, err := planAttemptTargets(ctx, sc, req.Binding, now)
			if err != nil {
				return err
			}
			if len(targets) > maxTargetsPerAttempt {
				return attemptErr(errCodeLedgerIncomplete, nil)
			}
			handle := model.NewID()
			resvRepo, err := reservationRepoOf(sc)
			if err != nil {
				return err
			}
			manifest := make([]TargetSnapshot, len(targets))
			amounts, seqs := make([]int64, len(targets)), make([]int64, len(targets))
			for i := range targets {
				targets[i].snapshot.ChildID = model.NewID()
			}
			sort.Slice(targets, func(i, j int) bool {
				a, b := targets[i].snapshot, targets[j].snapshot
				if a.PolicyID != b.PolicyID {
					return a.PolicyID < b.PolicyID
				}
				if a.ScopeKey != b.ScopeKey {
					return a.ScopeKey < b.ScopeKey
				}
				if a.PeriodStart != b.PeriodStart {
					return a.PeriodStart.String() < b.PeriodStart.String()
				}
				return a.ChildID < b.ChildID
			})
			var denial *attemptDenial
			for i, target := range targets {
				tg := target.snapshot
				manifest[i] = tg
				seqs[i], err = nextAttemptSequence(ctx, sc, resvRepo, tg)
				if err != nil {
					return err
				}
				if tg.Action == "unlimited" {
					continue
				}
				amounts[i] = req.Binding.Estimate.AmountMicroUSD
				amount, err := target.effectiveAmount(ctx, sc, now)
				if err != nil {
					return err
				}
				if amount.Class != amountExact {
					return attemptErr(attemptAmountError(amount), nil)
				}
				effective, ok := amount.RepresentableInt64()
				if !ok {
					return attemptErr(errCodeArithmetic, nil)
				}
				total := addInt64(effective, amounts[i])
				if !total.OK {
					return attemptErr(errCodeArithmetic, nil)
				}
				if total.Value > *tg.LimitMicroUSD && (denial == nil || (tg.Action == "block" && denial.Action == "throttle")) {
					id := tg.PolicyID
					denial = &attemptDenial{Code: errCodeBudgetDenied, Action: tg.Action, ReasonCode: "no_headroom", PolicyID: &id}
				}
			}
			basis := AccountingBasis{Kind: basisAttemptAdmission}
			reservationDigest := reservationDigestOf(tenant, req.AttemptRef, handle, bindingDigest, nil, basis, &now, manifest, amounts, seqs)
			frontier := EvidenceRef{Kind: evidenceStoreRow, Ref: scope.ID.String(), Version: scope.Version, Digest: scope.FrontierDigest}
			if _, err := m.verifyAttempt(ctx, sc, EvidenceCheck{
				Operation: opPrepareBinding, AttemptRef: req.AttemptRef, BindingDigest: bindingDigest,
				ReservationDigest: reservationDigest, ScopeFrontierRef: &frontier,
				OwnerRef: req.OwnerRef, ProposedBinding: &req.Binding, Evidence: req.Binding.AuthorityRefs,
			}); err != nil {
				return err
			}
			if denial != nil {
				result = prepareAttemptResult{Decision: "denied", Denial: denial}
				return nil
			}
			targetBody, err := marshalBounded(encodeTargets(manifest), maxAttemptPayloadBytes, "attempt targets")
			if err != nil {
				return attemptErr(errCodeLedgerIncomplete, err)
			}
			basisBody, err := marshalBounded(encodeAccountingBasis(basis), maxAttemptPayloadBytes, "accounting basis")
			if err != nil {
				return attemptErr(errCodeInvalidAttempt, err)
			}
			if len(body)+len(targetBody)+len(basisBody) > maxAttemptPayloadBytes {
				return attemptErr(errCodeLedgerIncomplete, nil)
			}
			repo, err := attemptRepoOf(sc)
			if err != nil {
				return err
			}
			_, err = repo.Create(ctx, model.Record{
				colAttemptContractVersion: attemptContractVersion, colAttemptRef: string(req.AttemptRef),
				colAttemptRequestRef: string(req.Binding.RequestRef), colAttemptHandle: handle.String(),
				colAttemptPhase: string(phasePrepared), colAttemptBindingDigest: string(bindingDigest),
				colAttemptBinding: body, colAttemptTargets: targetBody, colAttemptResvDigest: string(reservationDigest),
				colAttemptAccountingAt: now.String(), colAttemptAccountingBasis: basisBody,
				colAttemptOwnerRef: req.OwnerRef, colAttemptOwnerEpoch: int64(1),
				colAttemptReviewAfter: req.ReviewAfter.String(), colAttemptPublication: publicationNone,
			})
			if err != nil {
				return mapAttemptWriteErr(err)
			}
			for i, tg := range manifest {
				_, err := resvRepo.CreateWithID(ctx, tg.ChildID, model.Record{
					colResvPolicyRef: tg.PolicyID.String(), colResvPolicyKind: tg.PolicyKind,
					colResvDimension: tg.Dimension, colResvScopeKey: tg.ScopeKey, colResvPeriod: tg.Period,
					colResvPeriodStart: tg.PeriodStart.String(), colResvSeq: seqs[i], colResvAmount: amounts[i],
					colResvActual: int64(0), colResvState: resvStateActive, colResvHandle: handle.String(),
					colResvExpiresAt: req.ReviewAfter.String(), colResvAttemptRef: string(req.AttemptRef),
					colResvLifecycleVersion: lifecycleLinkageVersion,
				})
				if err != nil {
					return mapAttemptWriteErr(err)
				}
			}
			// Read the persisted complete group in this transaction, including any
			// pre-existing orphan claiming this ref. A contradictory or incomplete
			// read rolls every new row back, even for a zero-target parent.
			_, view, found, err := findAttemptByRef(ctx, sc, req.AttemptRef)
			if err != nil {
				return err
			}
			if !found {
				return attemptErr(errCodeLedgerIndeterminate, nil)
			}
			result = prepareAttemptResult{Decision: "admitted", Attempt: &view}
			return nil
		})
		if err == nil {
			return result, nil
		}
		if attemptCode(err) == errCodeWriteOutcomeUnknown {
			return prepareAttemptResult{}, attemptErrRef(errCodeWriteOutcomeUnknown, req.AttemptRef, err)
		}
		// Only a known rolled-back write conflict retries the whole decision. A
		// verifier/read error wrapping ErrConflict must not trigger admission again.
		if attemptCode(err) != errCodeStaleAttempt || !errors.Is(err, store.ErrConflict) {
			var ae *AttemptError
			if !errors.As(err, &ae) {
				return prepareAttemptResult{}, storeErr(err)
			}
			return prepareAttemptResult{}, err
		}
	}
	return prepareAttemptResult{}, attemptErrRef(errCodeConcurrencyExhausted, req.AttemptRef, err)
}

func attemptText(s string, required bool) bool {
	return (!required || s != "") && len(s) <= maxOpaqueRefBytes && utf8.ValidString(s)
}

func canonicalAttemptID(id model.ID, required bool) bool {
	if id == "" {
		return !required
	}
	parsed, err := model.ParseID(string(id))
	return err == nil && !parsed.IsZero() && parsed.String() == string(id)
}

// Shape is separate from authority. The verifier must attest principal, routing,
// runtime delegation and complete membership; neither a valid ID nor a hash does.
func validatePreparedBinding(b AttemptBinding) error {
	bad := func() error { return attemptErr(errCodeInvalidAttempt, nil) }
	if b.Status != bindingResolved || !validAttemptRef(b.RequestRef) ||
		(b.PredecessorRef != nil && !validAttemptRef(*b.PredecessorRef)) ||
		len(b.Attribution) != len(attributionDimensions) {
		return bad()
	}
	for _, dim := range attributionDimensions {
		f, present := b.Attribution[dim]
		if !present || !validEvidenceRefs(f.Evidence) || !attemptText(f.NotApplicableRule, false) {
			return bad()
		}
		switch f.State {
		case factKnown:
			if len(f.Evidence) == 0 || f.NotApplicableRule != "" {
				return bad()
			}
			if !isGroupDimension(dim) && len(f.Values) != 1 {
				return bad()
			}
			for i, v := range f.Values {
				if !attemptText(v, true) {
					return bad()
				}
				if isGroupDimension(dim) && (!canonicalAttemptID(model.ID(v), true) || (i > 0 && f.Values[i-1] >= v)) {
					return bad()
				}
			}
		case factMissing:
			if len(f.Values) != 0 || f.NotApplicableRule != "" {
				return bad()
			}
		case factNotApplicable:
			if len(f.Values) != 0 || f.NotApplicableRule == "" || len(f.Evidence) == 0 {
				return bad()
			}
		default:
			return bad()
		}
	}
	s, d, e := b.Subject, b.Destination, b.Estimate
	for _, v := range []string{s.ActorRef, s.ActorKind, d.ProfileRef, d.ProfileRevision, d.ProviderRef, d.ModelRef,
		d.Action, d.Protocol, d.AdapterID, d.AdapterVersion, d.Surface, d.CredentialAudience, d.AuthScheme, e.Method, e.Revision} {
		if !attemptText(v, true) {
			return bad()
		}
	}
	for _, v := range []string{s.AgentIdentity, s.SessionIdentity, s.SessionRunRef, s.UntrustedSessionRef, d.InferenceGeo} {
		if !attemptText(v, false) {
			return bad()
		}
	}
	for _, id := range []model.ID{s.UserID, s.CredentialID, s.SessionWorkspaceID, b.Entities.SessionID, b.Entities.AgentID} {
		if !canonicalAttemptID(id, false) {
			return bad()
		}
	}
	for _, id := range []model.ID{b.Entities.ProviderID, b.Entities.ModelID, d.PolicyID} {
		if !canonicalAttemptID(id, true) {
			return bad()
		}
	}
	if !knownAttemptScalar(b, "actor", s.ActorRef) || !knownAttemptScalar(b, "provider", d.ProviderRef) ||
		!knownAttemptScalar(b, "model", d.ModelRef) || (d.InferenceGeo != "" && !knownAttemptScalar(b, "inference_geo", d.InferenceGeo)) {
		return bad()
	}
	if strings.HasPrefix(s.ActorRef, "user:") && (!canonicalAttemptID(s.UserID, true) || s.ActorRef != "user:"+s.UserID.String()) {
		return bad()
	}
	if strings.HasPrefix(s.ActorRef, "token:") && (!canonicalAttemptID(s.CredentialID, true) || s.ActorRef != "token:"+s.CredentialID.String() || s.UserID != "") {
		return bad()
	}
	if s.SessionWorkspaceID != "" || s.SessionRunRef != "" || s.SessionFence != 0 {
		if s.SessionWorkspaceID == "" || s.SessionRunRef == "" || s.SessionFence <= 0 {
			return bad()
		}
	}
	if (b.Attribution["session"].State == factKnown) != (b.Entities.SessionID != "") ||
		(b.Attribution["agent"].State == factKnown) != (b.Entities.AgentID != "") {
		return bad()
	}
	if !validEvidenceRefs(s.DelegationRefs) || !validEvidenceRefs(b.AuthorityRefs) || len(b.AuthorityRefs) == 0 ||
		!validEvidenceRefs(e.RateRefs) || !e.QualificationRef.valid() || !validDigest(e.PriceDigest) ||
		e.AmountMicroUSD < 0 || (e.InputBound != nil && *e.InputBound < 0) || (e.OutputBound != nil && *e.OutputBound < 0) {
		return bad()
	}
	for _, dg := range []Digest{d.EndpointDigest, d.TransportDigest, d.PolicySpecDigest, d.ProxyPolicyDigest, d.PreparedDigest} {
		if !validDigest(dg) {
			return bad()
		}
	}
	if d.PolicyVersion < 1 || d.PreparedBytes < 0 || d.MaxOutputTokens < 0 || d.MaxRequestBytes <= 0 ||
		d.MaxResponseBytes <= 0 || d.TimeoutNanos <= 0 || d.PreparedBytes > d.MaxRequestBytes {
		return bad()
	}
	return nil
}

func knownAttemptScalar(b AttemptBinding, dim, value string) bool {
	f := b.Attribution[dim]
	return f.State == factKnown && len(f.Values) == 1 && f.Values[0] == value
}

func validatePreparedEntities(ctx context.Context, sc store.Scope, b AttemptBinding) error {
	bad := func(err error) error {
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return storeErr(err)
		}
		return attemptErr(errCodeLedgerIndeterminate, err)
	}
	p, err := sc.Providers().Get(ctx, b.Entities.ProviderID)
	if err != nil {
		return bad(err)
	}
	if p.ID != b.Entities.ProviderID || p.TenantID != sc.Tenant() || p.Name != b.Destination.ProviderRef {
		return bad(nil)
	}
	m, err := sc.Models().Get(ctx, b.Entities.ModelID)
	if err != nil {
		return bad(err)
	}
	if m.ID != b.Entities.ModelID || m.TenantID != sc.Tenant() || m.Name != b.Destination.ModelRef || m.ProviderID != p.ID {
		return bad(nil)
	}
	if b.Entities.AgentID != "" {
		a, err := sc.Agents().Get(ctx, b.Entities.AgentID)
		if err != nil {
			return bad(err)
		}
		if a.ID != b.Entities.AgentID || a.TenantID != sc.Tenant() || !knownAttemptScalar(b, "agent", a.ExternalID) {
			return bad(nil)
		}
	}
	if b.Entities.SessionID != "" {
		s, err := sc.Sessions().Get(ctx, b.Entities.SessionID)
		if err != nil {
			return bad(err)
		}
		if s.ID != b.Entities.SessionID || s.TenantID != sc.Tenant() || !knownAttemptScalar(b, "session", s.ExternalID) ||
			(!s.AgentID.IsZero() && s.AgentID != b.Entities.AgentID) {
			return bad(nil)
		}
	}
	return nil
}

type attemptTarget struct {
	snapshot TargetSnapshot
	policy   model.Policy
	budget   budgetSpec
}

func (t attemptTarget) effectiveAmount(ctx context.Context, sc store.Scope, now model.Timestamp) (effectiveAmount, error) {
	if t.snapshot.PolicyKind == policyKindBudget {
		e, err := evaluateBudgetAmount(ctx, sc, t.policy, t.budget, now.Time(), now.Time())
		if err != nil {
			return effectiveAmount{}, storeErr(err)
		}
		if e.Config != configFaultNone {
			return effectiveAmount{}, attemptErr(errCodeLedgerIndeterminate, nil)
		}
		return e.Amount, nil
	}
	tg := t.snapshot
	w := strictCostWindow{Tenant: sc.Tenant(), Filters: []model.Filter{eq(colActor, tg.ScopeKey)},
		ScopeResolved: true, Start: tg.PeriodStart.Time(), HasStart: true, End: tg.PeriodEnd.Time(), Bounded: true}
	cost := readStrictCostTotal(ctx, sc, w)
	if cost.Err != nil {
		return effectiveAmount{}, storeErr(cost.Err)
	}
	held, err := heldReservedForWindow(ctx, sc, tg.PolicyID, tg.ScopeKey, w.Start, w.End, true, now.Time())
	if err != nil {
		return effectiveAmount{}, storeErr(err)
	}
	return classifyEffectiveAmount(effectiveAmountInputs{Cost: cost, StaticKnown: true, Dynamic: dynamicFromReservedTotal(held)}), nil
}

func attemptAmountError(a effectiveAmount) string {
	for _, c := range a.Causes {
		switch c {
		case causeCostScanTruncated, causeCostCursorMissing, causeCostCursorStalled, causeCostCursorCycle, causeDynamicScanIncomplete:
			return errCodeLedgerIncomplete
		}
	}
	return errCodeLedgerIndeterminate
}

func planAttemptTargets(ctx context.Context, sc store.Scope, b AttemptBinding, now model.Timestamp) ([]attemptTarget, error) {
	budgets, err := listAttemptPolicies(ctx, sc, policyKindBudget)
	if err != nil {
		return nil, err
	}
	var out []attemptTarget
	for _, p := range budgets {
		budget, _, digest, normalized, err := strictAttemptPolicy(p)
		if err != nil {
			return nil, err
		}
		if budget.Action == budgetActionAlert {
			continue
		}
		if budget.Dimension != "global" {
			fact := b.Attribution[budget.Dimension]
			if fact.State == factMissing {
				return nil, attemptErr(errCodeDimensionRequired, nil)
			}
			if fact.State == factNotApplicable || !contains(fact.Values, budget.Key) {
				continue
			}
		}
		if isGroupDimension(budget.Dimension) {
			return nil, attemptErr(errCodeLedgerIndeterminate, nil)
		}
		start, bounded := periodStart(budget.Period, now.Time())
		limit, static, version := budget.LimitMicroUSD, budget.ReservedMicroUSD, p.Version
		out = append(out, attemptTarget{policy: normalized, budget: budget, snapshot: TargetSnapshot{
			PolicyID: p.ID, PolicyKind: p.Kind, PolicyVersion: &version, PolicySpecDigest: &digest,
			Dimension: budget.Dimension, ScopeKey: budget.Key, Period: budget.Period,
			PeriodStart: model.NewTimestamp(start), PeriodEnd: model.NewTimestamp(periodEnd(budget.Period, start)), HasPeriodBounds: bounded,
			LimitMicroUSD: &limit, StaticReservedMicroUSD: &static, Action: budget.Action,
		}})
	}
	policies, err := listAttemptPolicies(ctx, sc, policyKindSpendLimit)
	if err != nil {
		return nil, err
	}
	type seat struct {
		p      model.Policy
		spec   storedSpendLimitSpec
		digest Digest
	}
	seats := make([]seat, 0, len(policies))
	groups := b.Attribution["user_group"]
	for _, p := range policies {
		_, spec, digest, _, err := strictAttemptPolicy(p)
		if err != nil {
			return nil, err
		}
		if b.ApplySeatLimits && spec.ScopeType == "rbac_group" && (groups.State == factMissing ||
			(strings.HasPrefix(b.Subject.ActorRef, "user:") && groups.State != factKnown)) {
			return nil, attemptErr(errCodeDimensionRequired, nil)
		}
		seats = append(seats, seat{p, spec, digest})
	}
	if !b.ApplySeatLimits {
		return out, nil
	}
	for _, period := range []string{"daily", "weekly", "monthly"} {
		var selected *seat
		bestRank := 4
		for i := range seats {
			s := &seats[i]
			if s.spec.Period != period {
				continue
			}
			rank := 4
			switch {
			case s.spec.ScopeType == "user" && s.spec.ScopeKey == b.Subject.ActorRef:
				rank = 1
			case s.spec.ScopeType == "rbac_group" && groups.State == factKnown && contains(groups.Values, s.spec.ScopeKey):
				rank = 2
			case s.spec.ScopeType == "organization":
				rank = 3
			}
			if rank == 4 {
				continue
			}
			if selected == nil || rank < bestRank || (rank == bestRank &&
				(moreRestrictive(&selected.spec, s.spec) ||
					(selected.spec.Unlimited == s.spec.Unlimited && selected.spec.AmountMicroUSD == s.spec.AmountMicroUSD && s.p.ID < selected.p.ID))) {
				selected, bestRank = s, rank
			}
		}
		if selected == nil {
			continue
		}
		s := selected
		start, _ := periodStart(period, now.Time())
		limit, static, version, digest := s.spec.AmountMicroUSD, int64(0), s.p.Version, s.digest
		tg := TargetSnapshot{PolicyID: s.p.ID, PolicyKind: policyKindSpendLimit, PolicyVersion: &version, PolicySpecDigest: &digest,
			Dimension: "spend_limit", ScopeKey: b.Subject.ActorRef, Period: period, PeriodStart: model.NewTimestamp(start),
			PeriodEnd: model.NewTimestamp(periodEnd(period, start)), HasPeriodBounds: true,
			LimitMicroUSD: &limit, StaticReservedMicroUSD: &static, Action: "block", Membership: groups.Evidence}
		if s.spec.Unlimited {
			tg.Action, tg.LimitMicroUSD = "unlimited", nil
		}
		out = append(out, attemptTarget{snapshot: tg})
	}
	return out, nil
}

func listAttemptPolicies(ctx context.Context, sc store.Scope, kind string) ([]model.Policy, error) {
	q := model.Query{Filters: []model.Filter{eq("kind", kind), model.Filter{Column: "enabled", Op: model.OpEq, Value: true}}, Limit: listCap}
	var out []model.Policy
	used := map[string]bool{}
	ids := map[model.ID]bool{}
	for pages := 0; pages < maxScanPages; pages++ {
		rows, page, err := sc.Policies().List(ctx, q)
		if err != nil {
			return nil, storeErr(err)
		}
		if len(rows) > listCap {
			return nil, attemptErr(errCodeLedgerIncomplete, nil)
		}
		for _, p := range rows {
			if p.TenantID != sc.Tenant() || !canonicalAttemptID(p.ID, true) || p.Version < 1 || p.Kind != kind || !p.Enabled || ids[p.ID] {
				return nil, attemptErr(errCodeLedgerIndeterminate, nil)
			}
			ids[p.ID] = true
			out = append(out, p)
		}
		if !page.HasMore {
			return out, nil
		}
		if page.Cursor == "" || page.Cursor == q.Cursor || used[page.Cursor] {
			return nil, attemptErr(errCodeLedgerIncomplete, nil)
		}
		used[page.Cursor], q.Cursor = true, page.Cursor
	}
	return nil, attemptErr(errCodeLedgerIncomplete, nil)
}

func nextAttemptSequence(ctx context.Context, sc store.Scope, repo store.GenericRepo, tg TargetSnapshot) (int64, error) {
	// The legacy max helper uses Record.Int (missing/malformed becomes zero).
	// This private probe retains its sorted-max strategy but requires exact cells.
	rows, page, err := repo.List(ctx, model.Query{Filters: []model.Filter{eq(colResvPolicyRef, tg.PolicyID.String()),
		eq(colResvScopeKey, tg.ScopeKey), eq(colResvPeriodStart, tg.PeriodStart.String())}, Sort: []model.Sort{{Column: colResvSeq, Desc: true}}, Limit: 1})
	if err != nil {
		return 0, storeErr(err)
	}
	if len(rows) == 0 {
		if page.HasMore {
			return 0, attemptErr(errCodeLedgerIncomplete, nil)
		}
		return 1, nil
	}
	if len(rows) != 1 {
		return 0, attemptErr(errCodeLedgerIndeterminate, nil)
	}
	r := rows[0]
	seq, ok := int64Cell(r, colResvSeq)
	if !ok || seq < 0 || tenantCellOf(r, sc.Tenant()) != tenantCellMatches ||
		!textCellEquals(r, colResvPolicyRef, tg.PolicyID.String()) || !textCellEquals(r, colResvScopeKey, tg.ScopeKey) ||
		!textCellEquals(r, colResvPeriodStart, tg.PeriodStart.String()) {
		return 0, attemptErr(errCodeLedgerIndeterminate, nil)
	}
	next := addInt64(seq, 1)
	if !next.OK {
		return 0, attemptErr(errCodeArithmetic, nil)
	}
	return next.Value, nil
}

// strictAttemptPolicy validates the observed logical Spec BEFORE matching. Its
// digest preserves presence, threshold order and duplicates. It is not custody
// of the original full artifact. The local normalized copy only lets the existing
// evaluator consume exact integers; global policy decoding stays unchanged.
func strictAttemptPolicy(p model.Policy) (budgetSpec, storedSpendLimitSpec, Digest, model.Policy, error) {
	bad := func() (budgetSpec, storedSpendLimitSpec, Digest, model.Policy, error) {
		return budgetSpec{}, storedSpendLimitSpec{}, "", model.Policy{}, attemptErr(errCodeLedgerIndeterminate, nil)
	}
	fields := []string{"dimension", "key", "limit_micro_usd", "period", "thresholds", "currency", "action", "reserved_micro_usd", "fail_closed"}
	if p.Kind == policyKindSpendLimit {
		fields = []string{"scope_type", "scope_key", "amount_micro_usd", "unlimited", "period"}
	}
	if p.Kind != policyKindBudget && p.Kind != policyKindSpendLimit {
		return bad()
	}
	raw, err := json.Marshal(p.Spec)
	if err != nil || len(raw) > maxAttemptPayloadBytes {
		return bad()
	}
	for name := range p.Spec {
		if !contains(fields, name) {
			return bad()
		}
	}
	normalized := p
	normalized.Spec = make(map[string]any, len(p.Spec))
	w := &canonWriter{}
	w.str(p.Kind)
	w.boolean(p.Enabled)
	w.count(len(fields))
	for _, name := range fields {
		v, present := p.Spec[name]
		w.str(name)
		w.presence(present)
		if !present {
			continue
		}
		if v == nil {
			return bad()
		}
		switch name {
		case "limit_micro_usd", "reserved_micro_usd", "amount_micro_usd":
			n, ok := exactPolicyInteger(v)
			if !ok {
				return bad()
			}
			w.str("integer")
			w.num(n)
			normalized.Spec[name] = n
		case "fail_closed", "unlimited":
			b, ok := v.(bool)
			if !ok {
				return bad()
			}
			w.str("bool")
			w.boolean(b)
			normalized.Spec[name] = b
		case "thresholds":
			a := reflect.ValueOf(v)
			if a.Kind() != reflect.Slice || a.IsNil() || a.Len() > 256 {
				return bad()
			}
			w.str("array")
			w.count(a.Len())
			thresholds := make([]float64, a.Len())
			for i := 0; i < a.Len(); i++ {
				f, ok := exactPolicyThreshold(a.Index(i).Interface())
				if !ok {
					return bad()
				}
				thresholds[i] = f
				w.str("decimal")
				w.str(strconv.FormatFloat(f, 'g', -1, 64))
			}
			normalized.Spec[name] = thresholds
		default:
			s, ok := v.(string)
			if !ok || !attemptText(s, false) {
				return bad()
			}
			w.str("string")
			w.str(s)
			normalized.Spec[name] = s
		}
	}
	var budget budgetSpec
	var seat storedSpendLimitSpec
	if p.Kind == policyKindBudget {
		budget = budgetSpec{Dimension: "global", Period: "monthly", Currency: "USD", Action: budgetActionAlert}
		for name, dest := range map[string]*string{"dimension": &budget.Dimension, "key": &budget.Key, "period": &budget.Period, "currency": &budget.Currency, "action": &budget.Action} {
			if v, ok := normalized.Spec[name]; ok {
				*dest = v.(string)
				if *dest == "" && name != "key" {
					return bad()
				}
			}
		}
		v, present := normalized.Spec["limit_micro_usd"]
		if !present {
			return bad()
		}
		budget.LimitMicroUSD = v.(int64)
		if v, ok := normalized.Spec["reserved_micro_usd"]; ok {
			budget.ReservedMicroUSD = v.(int64)
		}
		if v, ok := normalized.Spec["fail_closed"]; ok {
			budget.FailClosed = v.(bool)
		}
		if v, ok := normalized.Spec["thresholds"]; ok {
			budget.Thresholds = v.([]float64)
		}
		if !budgetDimensions[budget.Dimension] || !validPeriods[budget.Period] || !validActions[budget.Action] ||
			budget.Currency != "USD" || budget.LimitMicroUSD <= 0 || budget.ReservedMicroUSD < 0 ||
			(budget.Dimension == "global" && budget.Key != "") || (budget.Dimension != "global" && budget.Key == "") {
			return bad()
		}
		if isGroupDimension(budget.Dimension) && !canonicalAttemptID(model.ID(budget.Key), true) {
			return bad()
		}
		budget.fillDefaults()
	} else {
		if len(normalized.Spec) != len(fields) {
			return bad()
		}
		seat = storedSpendLimitSpec{ScopeType: normalized.Spec["scope_type"].(string), ScopeKey: normalized.Spec["scope_key"].(string),
			AmountMicroUSD: normalized.Spec["amount_micro_usd"].(int64), Unlimited: normalized.Spec["unlimited"].(bool), Period: normalized.Spec["period"].(string)}
		if seat.AmountMicroUSD < 0 || (seat.Unlimited && seat.AmountMicroUSD != 0) ||
			(seat.Period != "daily" && seat.Period != "weekly" && seat.Period != "monthly") {
			return bad()
		}
		switch seat.ScopeType {
		case "organization":
			if seat.ScopeKey != "" {
				return bad()
			}
		case "rbac_group":
			if !canonicalAttemptID(model.ID(seat.ScopeKey), true) {
				return bad()
			}
		case "user":
			prefix, id, ok := strings.Cut(seat.ScopeKey, ":")
			if !ok || (prefix != "user" && prefix != "token") || !canonicalAttemptID(model.ID(id), true) {
				return bad()
			}
		default:
			return bad()
		}
	}
	digest := canonDigest("olivares.finops.reservation-policy-spec.v1", func(dst *canonWriter) { dst.buf.Write(w.bytes()) })
	return budget, seat, digest, normalized, nil
}

func exactPolicyInteger(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int32:
		return int64(n), true
	case int64:
		return n, true
	case json.Number:
		i, err := strconv.ParseInt(string(n), 10, 64)
		return i, err == nil && strconv.FormatInt(i, 10) == string(n)
	case float64:
		// core.policy currently decodes JSON through float64. Even a round-tripping
		// conversion cannot recover a rounded original integer outside this range.
		if math.IsNaN(n) || math.IsInf(n, 0) || math.Trunc(n) != n || math.Abs(n) > 9007199254740991 {
			return 0, false
		}
		return int64(n), true
	}
	return 0, false
}

func exactPolicyThreshold(v any) (float64, bool) {
	var f float64
	switch n := v.(type) {
	case float64:
		f = n
	case json.Number:
		var err error
		f, err = strconv.ParseFloat(string(n), 64)
		if err != nil {
			return 0, false
		}
		a, ok := new(big.Rat).SetString(string(n))
		if !ok {
			return 0, false
		}
		b, ok := new(big.Rat).SetString(strconv.FormatFloat(f, 'g', -1, 64))
		if !ok || a.Cmp(b) != 0 {
			return 0, false
		}
	default:
		i, ok := exactPolicyInteger(v)
		if !ok {
			return 0, false
		}
		f = float64(i)
		if new(big.Rat).SetFloat64(f).Cmp(new(big.Rat).SetInt64(i)) != 0 {
			return 0, false
		}
	}
	return f, !math.IsNaN(f) && !math.IsInf(f, 0) && f > 0
}
