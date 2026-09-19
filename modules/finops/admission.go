// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Public admission API. Reserve consumes headroom before the caller
// performs a billable effect. Commit stamps the measured cost. Release returns
// unused headroom. An idempotency key bound to the canonical payload makes a
// retry return the original reservation instead of inserting a second one.
//
// This wraps the existing reserve ledger (ReserveBudget / ReserveSpendLimit /
// CommitReservation / ReleaseReservation). It does not invent a second money
// ledger. CheckBudget remains the read-only pre-flight and keeps its historical
// fail-open contract; new callers use this API.

// Admission scopes the brief names. Each scope can set Unreachable independently.
const (
	AdmissionScopeSessionLaunch = "session_launch"
	AdmissionScopeModelGateway  = "model_gateway"
	AdmissionScopeScheduledJob  = "scheduled_job"
)

const (
	// UnreachableDeny is the default: a store that cannot be read refuses the
	// request. Empty Unreachable on AdmissionRequest means this value.
	UnreachableDeny UnreachablePosture = "deny"
	// UnreachableAllow is the explicit opt-in to the historical fail-open
	// posture. An operator must set it per request; it is never the default.
	UnreachableAllow UnreachablePosture = "allow"
)

// ReasonStoreUnreachable is the money-free deny reason when the budget store
// cannot be read. Callers and the operator doc match this string.
const ReasonStoreUnreachable = "budget store unreachable (deny-closed)"

// ErrInvalidAdmission is a malformed Reserve request (unknown scope, empty
// idempotency key, negative estimate).
var ErrInvalidAdmission = errors.New("finops: invalid admission request")

// ErrAdmissionConflict is an idempotency key reused with a different payload.
var ErrAdmissionConflict = errors.New("finops: admission idempotency key reused with a different payload")

const (
	maxIdempotencyKeyBytes = 256
	admStatePending        = "pending"
	admStateReserved       = "reserved"
	admStateCommitted      = "committed"
	admStateReleased       = "released"
)

// UnreachablePosture is how Reserve answers when the budget store cannot be
// read. The zero value and "deny" are the same: refuse the request.
type UnreachablePosture string

// ParseUnreachablePosture maps operator input. Empty is deny. An unknown value
// is deny: a typo must not weaken the gate.
func ParseUnreachablePosture(s string) UnreachablePosture {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "deny":
		return UnreachableDeny
	case "allow":
		return UnreachableAllow
	default:
		return UnreachableDeny
	}
}

func (p UnreachablePosture) allowOnUnread() bool {
	return p == UnreachableAllow
}

// AdmissionRequest is the input to Reserve.
type AdmissionRequest struct {
	// Scope is one of the three named caller surfaces. Required.
	Scope string
	// Dims is the provider-neutral attribution the budgets match on.
	Dims SpendDims
	// ActorRef, when set, also reserves the per-seat spend limit for that actor.
	ActorRef string
	// Groups are directory group ids for spend-limit resolution.
	Groups []string
	// EstimateMicroUSD is the a-priori hold. Zero still fail-closes on an
	// unreadable store and still denies an already-exhausted cap; it inserts
	// reservation rows only when an enforcing target matches.
	EstimateMicroUSD int64
	// IdempotencyKey is required. Bound to tenant + scope + canonical payload.
	// Reuse with the same payload replays. Reuse with a different payload is a
	// conflict. A released or expired row is re-reserved (a failed attempt of
	// the same intent).
	IdempotencyKey string
	// Unreachable is deny by default. Set allow only when the operator has
	// chosen availability over evidence for this scope.
	Unreachable UnreachablePosture
}

// Reservation is the outcome of Reserve.
type Reservation struct {
	Allowed          bool   `json:"allowed"`
	Handle           string `json:"handle,omitempty"`
	SpendHandle      string `json:"spend_handle,omitempty"`
	Action           string `json:"action,omitempty"`
	BudgetID         string `json:"budget_id,omitempty"`
	BudgetName       string `json:"budget_name,omitempty"`
	Reason           string `json:"reason,omitempty"`
	EstimateMicroUSD int64  `json:"estimate_micro_usd,omitempty"`
	Replayed         bool   `json:"replayed,omitempty"`
	// SpendLimit says the deny came from a per-seat spend limit and not from a
	// pooled budget. The two are different things to the operator who has to act
	// on them, and the apps-gateway contract publishes the distinction on the
	// wire (docs/contracts/Spend enforcement row). Reason cannot carry it:
	// a budget's reason names the budget, and a caller that echoed it would put
	// an internal name in front of a client.
	SpendLimit bool `json:"spend_limit,omitempty"`
}

func validAdmissionScope(s string) bool {
	switch s {
	case AdmissionScopeSessionLaunch, AdmissionScopeModelGateway, AdmissionScopeScheduledJob:
		return true
	}
	return false
}

func validateAdmissionRequest(req AdmissionRequest) error {
	if !validAdmissionScope(req.Scope) {
		return fmt.Errorf("%w: scope must be session_launch, model_gateway or scheduled_job", ErrInvalidAdmission)
	}
	key := strings.TrimSpace(req.IdempotencyKey)
	if key == "" {
		return fmt.Errorf("%w: idempotency_key is required", ErrInvalidAdmission)
	}
	if utf8.RuneCountInString(key) > maxIdempotencyKeyBytes {
		return fmt.Errorf("%w: idempotency_key exceeds %d characters", ErrInvalidAdmission, maxIdempotencyKeyBytes)
	}
	if req.EstimateMicroUSD < 0 {
		return fmt.Errorf("%w: estimate must not be negative", ErrInvalidAdmission)
	}
	return nil
}

func admissionPayloadHash(req AdmissionRequest) string {
	groups := append([]string(nil), req.Groups...)
	sort.Strings(groups)
	fields := []string{
		req.Scope,
		strconv.FormatInt(req.EstimateMicroUSD, 10),
		req.ActorRef,
		strings.Join(groups, ","),
		req.Dims.ProviderRef, req.Dims.ModelRef, req.Dims.AgentRef, req.Dims.SessionRef,
		req.Dims.Team, req.Dims.Project, req.Dims.WorkspaceRef, req.Dims.APIKeyRef,
		req.Dims.ServiceTier, req.Dims.ContextWindow, req.Dims.InferenceGeo,
		req.Dims.Gateway, req.Dims.CostType, req.Dims.IdentityRef, req.Dims.RoutineRef,
		req.Dims.CostCenterRef,
		strings.Join(append([]string(nil), req.Dims.UserGroupRefs...), ","),
		strings.Join(append([]string(nil), req.Dims.AgentGroupRefs...), ","),
	}
	var b strings.Builder
	for _, f := range fields {
		b.WriteString(strconv.Itoa(len(f)))
		b.WriteByte(':')
		b.WriteString(f)
		b.WriteByte('|')
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// Reserve admits a prospective effect against every enforcing budget (and, when
// ActorRef is set, every spend limit) that scopes the request. It fails closed
// when the store is unreachable unless Unreachable is explicitly allow.
func (m *Module) Reserve(ctx context.Context, tenant model.TenantID, req AdmissionRequest) (Reservation, error) {
	if err := validateAdmissionRequest(req); err != nil {
		return Reservation{}, err
	}
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	req.Unreachable = ParseUnreachablePosture(string(req.Unreachable))
	hash := admissionPayloadHash(req)

	if m.data == nil {
		return m.refuseUnreachable(ctx, tenant, req, attemptErr(errCodeCapabilityUnavailable, nil))
	}

	for attempt := 0; attempt < maxReserveRetries; attempt++ {
		existing, found, err := m.lookupIdempotency(ctx, tenant, req.IdempotencyKey)
		if err != nil {
			return m.refuseUnreachable(ctx, tenant, req, err)
		}
		if found {
			if existing.payloadHash != hash {
				return Reservation{}, ErrAdmissionConflict
			}
			switch existing.state {
			case admStateReserved, admStateCommitted:
				if replay, ok, rerr := m.replayIfPresent(ctx, tenant, req, hash); rerr != nil {
					return m.refuseUnreachable(ctx, tenant, req, rerr)
				} else if ok {
					return replay, nil
				}
			case admStatePending:
				if replay, ok, rerr := m.waitForClaim(ctx, tenant, req, hash); rerr != nil {
					return m.refuseUnreachable(ctx, tenant, req, rerr)
				} else if ok {
					return replay, nil
				}
				continue
			}
		}

		var (
			claimed bool
			claim   idempotencyRow
		)
		if found {
			existing.state = admStatePending
			existing.payloadHash = hash
			existing.handle = ""
			existing.spendHandle = ""
			if err := m.putIdempotency(ctx, tenant, existing, true); err != nil {
				return m.refuseUnreachable(ctx, tenant, req, err)
			}
			claimed, claim = true, existing
		} else {
			claimed, claim, err = m.claimIdempotency(ctx, tenant, idempotencyRow{
				key: req.IdempotencyKey, payloadHash: hash, scope: req.Scope,
				estimate: req.EstimateMicroUSD, state: admStatePending,
			})
			if err != nil {
				return m.refuseUnreachable(ctx, tenant, req, err)
			}
			if !claimed {
				continue
			}
		}

		budgetRes, err := m.ReserveBudget(ctx, tenant, req.Dims, req.EstimateMicroUSD)
		denied, reservation, ferr := m.classifyReserveOutcome(ctx, tenant, req, budgetRes, err)
		if ferr != nil {
			m.abandonClaim(ctx, tenant, claim)
			return reservation, ferr
		}
		if denied {
			m.abandonClaim(ctx, tenant, claim)
			m.auditAdmissionDeny(ctx, tenant, req, reservation)
			return reservation, nil
		}

		var spendRes BudgetReservation
		if strings.TrimSpace(req.ActorRef) != "" {
			spendRes, err = m.ReserveSpendLimit(ctx, tenant, req.ActorRef, req.Groups, req.EstimateMicroUSD)
			denied, reservation, ferr = m.classifyReserveOutcome(ctx, tenant, req, spendRes, err)
			if denied {
				reservation.SpendLimit = true
			}
			if ferr != nil || denied {
				m.releaseQuiet(ctx, tenant, budgetRes.Handle)
				m.abandonClaim(ctx, tenant, claim)
				if ferr != nil {
					return reservation, ferr
				}
				m.auditAdmissionDeny(ctx, tenant, req, reservation)
				return reservation, nil
			}
		}

		claim.handle = budgetRes.Handle
		claim.spendHandle = spendRes.Handle
		claim.state = admStateReserved
		if err := m.putIdempotency(ctx, tenant, claim, true); err != nil {
			m.releaseQuiet(ctx, tenant, budgetRes.Handle)
			m.releaseQuiet(ctx, tenant, spendRes.Handle)
			m.abandonClaim(ctx, tenant, claim)
			return m.refuseUnreachable(ctx, tenant, req, err)
		}
		return Reservation{
			Allowed: true, Handle: budgetRes.Handle, SpendHandle: spendRes.Handle,
			EstimateMicroUSD: req.EstimateMicroUSD,
		}, nil
	}
	return m.refuseUnreachable(ctx, tenant, req, fmt.Errorf("finops: admission retries exhausted"))
}

func (m *Module) claimIdempotency(ctx context.Context, tenant model.TenantID, row idempotencyRow) (bool, idempotencyRow, error) {
	err := m.putIdempotency(ctx, tenant, row, false)
	if errors.Is(err, store.ErrConflict) {
		return false, idempotencyRow{}, nil
	}
	if err != nil {
		return false, idempotencyRow{}, err
	}
	got, found, err := m.lookupIdempotency(ctx, tenant, row.key)
	if err != nil {
		return false, idempotencyRow{}, err
	}
	if !found {
		return false, idempotencyRow{}, fmt.Errorf("finops: claimed idempotency row was not readable")
	}
	return true, got, nil
}

func (m *Module) abandonClaim(ctx context.Context, tenant model.TenantID, row idempotencyRow) {
	row.state = admStateReleased
	_ = m.putIdempotency(ctx, tenant, row, true)
}

func (m *Module) waitForClaim(ctx context.Context, tenant model.TenantID, req AdmissionRequest, hash string) (Reservation, bool, error) {
	for i := 0; i < 32; i++ {
		replay, ok, err := m.replayIfPresent(ctx, tenant, req, hash)
		if err != nil || ok {
			return replay, ok, err
		}
		existing, found, err := m.lookupIdempotency(ctx, tenant, req.IdempotencyKey)
		if err != nil {
			return Reservation{}, false, err
		}
		if !found || existing.state == admStateReleased {
			return Reservation{}, false, nil
		}
	}
	return Reservation{}, false, nil
}

func (m *Module) classifyReserveOutcome(ctx context.Context, tenant model.TenantID, req AdmissionRequest, res BudgetReservation, err error) (denied bool, out Reservation, retErr error) {
	if err != nil && !res.Allowed {
		out = m.mapDenied(res, req)
		return true, out, nil
	}
	if err != nil {
		out, retErr = m.refuseUnreachable(ctx, tenant, req, err)
		return true, out, retErr
	}
	if !res.Allowed {
		out = m.mapDenied(res, req)
		return true, out, nil
	}
	return false, Reservation{}, nil
}

func (m *Module) replayIfPresent(ctx context.Context, tenant model.TenantID, req AdmissionRequest, hash string) (Reservation, bool, error) {
	existing, found, err := m.lookupIdempotency(ctx, tenant, req.IdempotencyKey)
	if err != nil || !found || existing.payloadHash != hash {
		return Reservation{}, false, err
	}
	switch existing.state {
	case admStateCommitted:
		return Reservation{
			Allowed: true, Handle: existing.handle, SpendHandle: existing.spendHandle,
			EstimateMicroUSD: req.EstimateMicroUSD, Replayed: true,
		}, true, nil
	case admStateReserved:
		live, lerr := m.handlesLive(ctx, tenant, existing.handle, existing.spendHandle)
		if lerr != nil {
			return Reservation{}, false, lerr
		}
		if live {
			return Reservation{
				Allowed: true, Handle: existing.handle, SpendHandle: existing.spendHandle,
				EstimateMicroUSD: req.EstimateMicroUSD, Replayed: true,
			}, true, nil
		}
	}
	return Reservation{}, false, nil
}

func (m *Module) mapDenied(res BudgetReservation, req AdmissionRequest) Reservation {
	action := res.Action
	if action == "" {
		action = "block"
	}
	return Reservation{
		Allowed: false, Action: action, BudgetID: res.BudgetID, BudgetName: res.BudgetName,
		Reason: res.Reason, EstimateMicroUSD: req.EstimateMicroUSD,
	}
}

func (m *Module) refuseUnreachable(ctx context.Context, tenant model.TenantID, req AdmissionRequest, cause error) (Reservation, error) {
	if req.Unreachable.allowOnUnread() {
		return Reservation{Allowed: true, EstimateMicroUSD: req.EstimateMicroUSD}, cause
	}
	out := Reservation{
		Allowed: false, Action: "block", Reason: ReasonStoreUnreachable,
		EstimateMicroUSD: req.EstimateMicroUSD,
	}
	m.auditAdmissionDeny(ctx, tenant, req, out)
	return out, nil
}

func (m *Module) releaseQuiet(ctx context.Context, tenant model.TenantID, handle string) {
	if handle == "" {
		return
	}
	_ = m.ReleaseReservation(ctx, tenant, handle)
}

// Commit settles a reservation after the effect completed. Ingest the actual
// spend first, then call this, so the ceiling never under-counts during
// settlement. Empty handle is a no-op (admitted with no enforcing target).
func (m *Module) Commit(ctx context.Context, tenant model.TenantID, handle string, actualMicroUSD int64) error {
	if handle == "" {
		return nil
	}
	if actualMicroUSD < 0 {
		return fmt.Errorf("%w: actual must not be negative", ErrInvalidAdmission)
	}
	row, found, err := m.lookupIdempotencyByHandle(ctx, tenant, handle)
	if err != nil {
		return err
	}
	if err := m.settleHandle(ctx, tenant, handle, resvStateCommitted, actualMicroUSD); err != nil {
		return err
	}
	if found && row.spendHandle != "" && row.spendHandle != handle {
		if err := m.settleHandle(ctx, tenant, row.spendHandle, resvStateCommitted, actualMicroUSD); err != nil {
			return err
		}
	}
	if found {
		row.state = admStateCommitted
		return m.putIdempotency(ctx, tenant, row, true)
	}
	return nil
}

// Release returns unused headroom. Empty handle is a no-op.
func (m *Module) Release(ctx context.Context, tenant model.TenantID, handle string) error {
	if handle == "" {
		return nil
	}
	row, found, err := m.lookupIdempotencyByHandle(ctx, tenant, handle)
	if err != nil {
		return err
	}
	if err := m.settleHandle(ctx, tenant, handle, resvStateReleased, 0); err != nil {
		return err
	}
	if found && row.spendHandle != "" && row.spendHandle != handle {
		if err := m.settleHandle(ctx, tenant, row.spendHandle, resvStateReleased, 0); err != nil {
			return err
		}
	}
	if found {
		row.state = admStateReleased
		return m.putIdempotency(ctx, tenant, row, true)
	}
	return nil
}

func (m *Module) settleHandle(ctx context.Context, tenant model.TenantID, handle, state string, actual int64) error {
	var err error
	if state == resvStateCommitted {
		err = m.CommitReservation(ctx, tenant, handle, actual)
	} else {
		err = m.ReleaseReservation(ctx, tenant, handle)
	}
	if errors.Is(err, store.ErrNotFound) {
		// No rows: admitted with no enforcing target, or already settled.
		return nil
	}
	return err
}

func (m *Module) auditAdmissionDeny(ctx context.Context, tenant model.TenantID, req AdmissionRequest, res Reservation) {
	if m.data == nil {
		return
	}
	meta := map[string]any{
		"scope":  req.Scope,
		"action": res.Action,
		"reason": res.Reason,
	}
	if res.BudgetID != "" {
		meta["budget_id"] = res.BudgetID
	}
	_ = m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, err := sc.Audit().Append(ctx, model.AuditDraft{
			Actor:      "finops",
			ActorKind:  model.ActorSystem,
			Action:     "finops.admission.denied",
			TargetKind: admissionIdempotencyKind,
			Meta:       meta,
		})
		return err
	})
}

type idempotencyRow struct {
	id          model.ID
	key         string
	payloadHash string
	handle      string
	spendHandle string
	scope       string
	estimate    int64
	state       string
}

func (m *Module) lookupIdempotency(ctx context.Context, tenant model.TenantID, key string) (idempotencyRow, bool, error) {
	return m.lookupIdempotencyFilter(ctx, tenant, eq(colAdmKey, key))
}

func (m *Module) lookupIdempotencyByHandle(ctx context.Context, tenant model.TenantID, handle string) (idempotencyRow, bool, error) {
	return m.lookupIdempotencyFilter(ctx, tenant, eq(colAdmHandle, handle))
}

func (m *Module) lookupIdempotencyFilter(ctx context.Context, tenant model.TenantID, filter model.Filter) (idempotencyRow, bool, error) {
	var (
		row   idempotencyRow
		found bool
	)
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(admissionIdempotencyKind)
		if err != nil {
			return err
		}
		recs, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{filter}, Limit: 2})
		if err != nil {
			return err
		}
		if len(recs) == 0 {
			return nil
		}
		found = true
		row = idempotencyFromRecord(recs[0])
		return nil
	})
	return row, found, err
}

func idempotencyFromRecord(rec model.Record) idempotencyRow {
	return idempotencyRow{
		id:          model.ID(rec.String(model.ColID)),
		key:         rec.String(colAdmKey),
		payloadHash: rec.String(colAdmPayloadHash),
		handle:      rec.String(colAdmHandle),
		spendHandle: rec.String(colAdmSpendHandle),
		scope:       rec.String(colAdmScope),
		estimate:    rec.Int(colAdmEstimate),
		state:       rec.String(colAdmState),
	}
}

func (m *Module) putIdempotency(ctx context.Context, tenant model.TenantID, row idempotencyRow, update bool) error {
	return m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		if err := lockFinOpsWriter(ctx, sc); err != nil {
			return err
		}
		repo, err := sc.Ext(admissionIdempotencyKind)
		if err != nil {
			return err
		}
		rec := model.Record{
			colAdmKey:         row.key,
			colAdmPayloadHash: row.payloadHash,
			colAdmHandle:      row.handle,
			colAdmSpendHandle: row.spendHandle,
			colAdmScope:       row.scope,
			colAdmEstimate:    row.estimate,
			colAdmState:       row.state,
		}
		if update && !row.id.IsZero() {
			existing, err := repo.Get(ctx, row.id)
			if err != nil {
				return err
			}
			for k, v := range rec {
				existing[k] = v
			}
			_, err = repo.Update(ctx, existing)
			return err
		}
		_, err = repo.Create(ctx, rec)
		return err
	})
}

func (m *Module) handlesLive(ctx context.Context, tenant model.TenantID, handle, spendHandle string) (bool, error) {
	if handle == "" && spendHandle == "" {
		return true, nil
	}
	live := true
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(budgetReservationKind)
		if err != nil {
			return err
		}
		now := m.clock.Now()
		for _, h := range []string{handle, spendHandle} {
			if h == "" {
				continue
			}
			rows, incomplete, err := scanReservations(ctx, repo, []model.Filter{eq(colResvHandle, h)})
			if err != nil {
				return err
			}
			if incomplete != "" {
				live = false
				return nil
			}
			anyActive := false
			for _, r := range rows {
				if r.String(colResvState) != resvStateActive {
					continue
				}
				exp, err := model.ParseTimestamp(r.String(colResvExpiresAt))
				if err == nil && !exp.Time().After(now.Time()) {
					continue
				}
				anyActive = true
			}
			if len(rows) > 0 && !anyActive {
				live = false
				return nil
			}
		}
		return nil
	})
	return live, err
}
