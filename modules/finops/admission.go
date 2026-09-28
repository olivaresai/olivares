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
	"io/fs"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The admission API. Reserve holds the money a billable effect may cost before the
// caller performs it; Commit stamps the measured cost and Release returns the headroom.
// An idempotency key bound to the canonical payload makes a retry of one call return
// that call's hold instead of taking a second one.
//
// It is built on the reservation ledger. A hold is one identity: every ledger row of
// it, budget and spend limit alike, carries that identity as its handle, and from the
// claim until its money is settled the key's admission row names it. CheckBudget stays
// the read-only pre-flight with its own posture.

// The scopes a caller admits under. Each can choose its own unreachable posture.
const (
	AdmissionScopeSessionLaunch = "session_launch"
	AdmissionScopeModelGateway  = "model_gateway"
	AdmissionScopeScheduledJob  = "scheduled_job"
)

const (
	// UnreachableDeny is the default: an admission that cannot be established is
	// refused. An empty Unreachable on AdmissionRequest means this value.
	UnreachableDeny UnreachablePosture = "deny"
	// UnreachableAllow is the explicit opt-in to admitting, with no hold, when the
	// admission cannot be established. It is never the default.
	UnreachableAllow UnreachablePosture = "allow"
)

// ReasonStoreUnreachable is the money-free reason of a deny-closed refusal: the store
// could not be read, or the key's state could not be established safely. Callers and
// the operator documentation match this string.
const ReasonStoreUnreachable = "budget store unreachable (deny-closed)"

// ReasonAdmissionIntegrity is the reason of a refusal whose key leads to an admission
// row no writer stores: a slot that is not a hold identity, or a hold that another row
// names too. The store was read, so the unreachable posture does not apply: the key is
// refused in every posture, with nothing written, until the row is repaired.
const ReasonAdmissionIntegrity = "admission row failed its integrity check (refused)"

// ErrAdmissionConflict is an idempotency key reused with a different payload.
var ErrAdmissionConflict = errors.New("finops: admission idempotency key reused with a different payload")

var (
	// errLegacyClaim refuses a key whose row is a claim an earlier build staged, which
	// only a stated stop of those writers lets this build retire.
	errLegacyClaim = errors.New("finops: the key holds a claim an earlier build staged")
	// errAdmissionRetriesExhausted ends a Reserve whose key kept moving, or stayed in
	// flight, for as long as it was willing to look again.
	errAdmissionRetriesExhausted = errors.New("finops: admission retries exhausted")
)

const (
	// auditActorFinOps and auditActionAdmissionDenied name a refusal on the audit
	// chain. The engine log carries the same action when the chain cannot take the
	// row, so an operator finds a refusal under one spelling either way.
	auditActorFinOps           = "finops"
	auditActionAdmissionDenied = "finops.admission.denied"

	maxIdempotencyKeyBytes = 256
)

// admissionClaimTakeover is how long a pending row is believed to be another caller's
// claim in flight. Waiting for such a claim is what keeps two callers from evaluating
// one key at once; past this bound the claimant is gone — no evaluation this module
// performs takes half a minute under the writer lock — and the claim is taken over.
const admissionClaimTakeover = 30 * time.Second

// admissionClaimPoll is the pause between two reads of a key another caller holds.
const admissionClaimPoll = time.Millisecond

// maxClaimWaitPolls bounds one wait for a key another caller holds. The caller reads
// the row again and decides afresh after it.
const maxClaimWaitPolls = 32

// UnreachablePosture is how Reserve answers when the admission cannot be established.
// The zero value and "deny" are the same: refuse.
type UnreachablePosture string

// ParseUnreachablePosture maps operator input. Empty is deny, and so is an unknown
// value: a typo must not weaken the gate.
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

func (p UnreachablePosture) allowOnUnread() bool { return p == UnreachableAllow }

// AdmissionRequest is the input to Reserve.
type AdmissionRequest struct {
	// Scope is one of the three named caller surfaces. Required.
	Scope string
	// Dims is the provider-neutral attribution the budgets match on.
	Dims SpendDims
	// ActorRef, when set, also holds against that actor's spend limits.
	ActorRef string
	// Groups are directory group ids for spend-limit resolution.
	Groups []string
	// EstimateMicroUSD is the amount to hold. Zero HOLDS NOTHING: every enforcing
	// target is still evaluated, and a cap already past its limit still refuses, but no
	// ledger row is inserted and no handle is issued, so an admission of zero is
	// evaluated afresh on every call instead of being replayed.
	EstimateMicroUSD int64
	// IdempotencyKey is required, and it is bound to tenant, scope and the canonical
	// payload. A retry of the same call inside the replay window is answered with the
	// hold the call took, and a committed call with the hold it was charged under;
	// every other request carrying the key is evaluated against the ledger as it
	// stands. The same key with a different payload is ErrAdmissionConflict.
	IdempotencyKey string
	// Unreachable is deny by default. Set allow only when the operator has chosen
	// availability over evidence for this scope.
	Unreachable UnreachablePosture
}

// Reservation is the outcome of Reserve.
type Reservation struct {
	Allowed bool `json:"allowed"`
	// Handle is the hold identity to settle with Commit or Release; empty when the
	// admission holds nothing.
	Handle           string `json:"handle,omitempty"`
	Action           string `json:"action,omitempty"`
	BudgetID         string `json:"budget_id,omitempty"`
	BudgetName       string `json:"budget_name,omitempty"`
	Reason           string `json:"reason,omitempty"`
	EstimateMicroUSD int64  `json:"estimate_micro_usd,omitempty"`
	Replayed         bool   `json:"replayed,omitempty"`
	// SpendLimit says the refusal came from a per-seat spend limit and not from a
	// pooled budget: the two are different things to the operator who acts on them.
	SpendLimit bool `json:"spend_limit,omitempty"`
}

func validAdmissionScope(s string) bool {
	switch s {
	case AdmissionScopeSessionLaunch, AdmissionScopeModelGateway, AdmissionScopeScheduledJob:
		return true
	}
	return false
}

// validateAdmissionRequest refuses a request Reserve cannot act on with
// ErrInvalidAdmission: an unknown scope, an empty or oversized key, a negative amount.
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

// Reserve admits a prospective effect against every enforcing budget that scopes the
// request and, when ActorRef is set, every spend limit of that actor. It holds the
// estimate under one hold identity and answers with that identity as the handle, or
// refuses. A retry of the same call inside the replay window is answered with the hold
// the call took. It refuses deny-closed whenever the admission cannot be established,
// unless Unreachable is explicitly allow. An invalid request — unknown scope, empty or
// oversized key, negative estimate — is ErrInvalidAdmission.
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
		res, done, err := m.reserveOnce(ctx, tenant, req, hash)
		if done {
			return res, err
		}
	}
	return m.refuseUnreachable(ctx, tenant, req, errAdmissionRetriesExhausted)
}

// reserveOnce is one pass over the key. It reads the row and answers from it — a
// replay, a conflict, a refusal — or waits while another caller holds the key, or
// claims the key and creates and publishes the hold under that claim. done=false means
// the key moved under this pass: the pass writes nothing more and the row is read again.
func (m *Module) reserveOnce(ctx context.Context, tenant model.TenantID, req AdmissionRequest, hash string) (Reservation, bool, error) {
	view, err := m.readAdmission(ctx, tenant, req.IdempotencyKey, hash)
	if err != nil {
		return m.refused(ctx, tenant, req, err)
	}
	if view.found {
		switch view.outcome {
		case replayConflict:
			return Reservation{}, true, ErrAdmissionConflict
		case replayAnswer:
			return replayedReservation(req, view.answer), true, nil
		}
		if err := takeoverRefusal(view.row); err != nil {
			return m.refused(ctx, tenant, req, err)
		}
		if m.keyBusy(view) {
			res, ok, err := m.waitForKey(ctx, tenant, req, hash)
			if err != nil {
				return m.refused(ctx, tenant, req, err)
			}
			return res, ok, nil
		}
	}

	h := newHoldID()
	tok, step, err := m.acquireKey(ctx, tenant, req, hash, view, h)
	switch step {
	case stepAgain:
		return Reservation{}, false, nil
	case stepRefuse:
		return m.refused(ctx, tenant, req, err)
	}

	// The targets are read under the claim and before the create, as the pre-flight
	// check reads them; the create evaluates them all under the writer lock at one
	// instant, so both components of the hold expire together.
	now := m.clock.Now().Time()
	budgets, truncated, err := m.budgetTargets(ctx, tenant, req.Dims, now)
	if err != nil {
		return m.giveUp(ctx, tenant, req, tok, h, err)
	}
	if truncated {
		return m.denied(ctx, tenant, req, tok, h, BudgetReservation{
			Allowed: false, Action: "block", Reason: "budget set truncated at scan cap; enforced fail-closed",
		}, false)
	}
	var seats []reservationTarget
	if strings.TrimSpace(req.ActorRef) != "" {
		if seats, err = m.spendLimitTargets(ctx, tenant, req.ActorRef, req.Groups, now); err != nil {
			return m.giveUp(ctx, tenant, req, tok, h, err)
		}
	}

	created, step, err := m.createUnderClaim(ctx, tenant, tok, h, budgets, seats, req.EstimateMicroUSD, now)
	switch step {
	case stepAgain:
		return Reservation{}, false, nil
	case stepRefuse:
		return m.refused(ctx, tenant, req, err)
	case stepAbandon:
		return m.giveUp(ctx, tenant, req, tok, h, err)
	case stepDenied:
		return m.denied(ctx, tenant, req, tok, h, created.verdict, created.spendLimit)
	}

	step, err = m.publishUnderClaim(ctx, tenant, created.tok, h, created.issued)
	switch step {
	case stepAgain:
		return Reservation{}, false, nil
	case stepRefuse:
		return m.refused(ctx, tenant, req, err)
	case stepAbandon:
		return m.giveUp(ctx, tenant, req, created.tok, h, err)
	}
	return Reservation{
		Allowed: true, Handle: publishedHandle(h, created.issued).String(),
		EstimateMicroUSD: req.EstimateMicroUSD,
	}, true, nil
}

// admissionView is what one read of a key's row decided.
type admissionView struct {
	row     admissionRow
	found   bool
	outcome replayOutcome
	answer  holdID
	// busy says the row is a pair an earlier build published with one hold still
	// withholding inside the window: the key may be neither replayed nor taken over.
	busy bool
}

// readAdmission reads the row of key and decides, in one read transaction, whether it
// answers a request that hashes to hash.
func (m *Module) readAdmission(ctx context.Context, tenant model.TenantID, key, hash string) (admissionView, error) {
	var view admissionView
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		view = admissionView{}
		row, found, err := rowOfKey(ctx, sc, key)
		if err != nil || !found {
			return err
		}
		view.row, view.found = row, true
		if view.outcome, view.answer, err = m.replayFor(ctx, sc, row, hash); err != nil {
			return err
		}
		if view.outcome == replayEvaluate {
			view.busy, err = pairHoldsBack(ctx, sc, row, m.clock.Now())
		}
		return err
	})
	return view, err
}

// takeoverRefusal names why a row that does not answer the request may not be taken
// over either, or returns nil when it may: an owed list that does not decode, a claim an
// earlier build staged, a state no writer stores.
func takeoverRefusal(row admissionRow) error {
	if row.owedErr != nil {
		return row.owedErr
	}
	switch row.state {
	case admStatePending:
		if row.handle.isZero() {
			return errLegacyClaim
		}
		return nil
	case admStateReserved, admStateCommitted, admStateReleased, admStateOwesRelease:
		return nil
	}
	return fmt.Errorf("%w: a row in a state no writer stores", errAdmissionRowCorrupt)
}

// keyBusy reports that another caller holds the key: a claim still in flight, or a
// published pair whose remaining hold still withholds.
func (m *Module) keyBusy(view admissionView) bool {
	if view.busy {
		return true
	}
	return view.row.state == admStatePending && !view.row.handle.isZero() && !m.staleClaim(view.row)
}

// waitForKey waits while another caller holds the key, and hands back that caller's
// hold if the key starts answering this request as a retry. ok=false is never a
// refusal: it means read the row again and decide.
func (m *Module) waitForKey(ctx context.Context, tenant model.TenantID, req AdmissionRequest, hash string) (Reservation, bool, error) {
	for i := 0; i < maxClaimWaitPolls; i++ {
		if err := m.pauseBetweenClaimPolls(ctx); err != nil {
			return Reservation{}, false, err
		}
		view, err := m.readAdmission(ctx, tenant, req.IdempotencyKey, hash)
		if err != nil {
			return Reservation{}, false, err
		}
		if view.found && view.outcome == replayAnswer {
			return replayedReservation(req, view.answer), true, nil
		}
		if !view.found || view.outcome != replayEvaluate || !m.keyBusy(view) {
			return Reservation{}, false, nil
		}
	}
	return Reservation{}, false, nil
}

// staleClaim reports whether a pending row has stopped being a claim in flight. It
// reads the module's clock. A row with no stamp is stale: believing it in flight would
// make a key that nobody holds refuse forever.
func (m *Module) staleClaim(row admissionRow) bool {
	if row.stateAt.IsZero() {
		return true
	}
	return m.clock.Now().Time().Sub(row.stateAt.Time()) >= admissionClaimTakeover
}

// pauseBetweenClaimPolls waits out one poll interval, or returns as soon as the
// caller's context is done.
func (m *Module) pauseBetweenClaimPolls(ctx context.Context) error {
	timer := time.NewTimer(admissionClaimPoll)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("finops: waiting for a key another caller holds: %w", ctx.Err())
	case <-timer.C:
		return nil
	}
}

// replayedReservation is the answer to a retry: the hold the call it repeats took.
func replayedReservation(req AdmissionRequest, h holdID) Reservation {
	return Reservation{Allowed: true, Handle: h.String(), EstimateMicroUSD: req.EstimateMicroUSD, Replayed: true}
}

// publishedHandle is the handle a publication records: the hold when the create issued
// it, and nothing when the create inserted no row.
func publishedHandle(h holdID, issued bool) holdID {
	if issued {
		return h
	}
	return ""
}

// refused answers a pass with a deny-closed refusal. A cause that is an integrity fault
// is refused in every posture: the store answered, and what it holds under the key is
// not an admission this module can act on.
func (m *Module) refused(ctx context.Context, tenant model.TenantID, req AdmissionRequest, cause error) (Reservation, bool, error) {
	if errors.Is(cause, errAdmissionRowCorrupt) {
		out := Reservation{
			Allowed: false, Action: "block", Reason: ReasonAdmissionIntegrity,
			EstimateMicroUSD: req.EstimateMicroUSD,
		}
		m.recordDeny(ctx, tenant, req, out)
		return out, true, nil
	}
	res, err := m.refuseUnreachable(ctx, tenant, req, cause)
	return res, true, err
}

// giveUp gives the claim back and refuses deny-closed. If the key has already moved to
// another caller, this caller's failure is not the key's answer: it reads again. If
// giving the claim back finds an integrity fault, that fault is the answer.
func (m *Module) giveUp(ctx context.Context, tenant model.TenantID, req AdmissionRequest, tok admissionToken, h holdID, cause error) (Reservation, bool, error) {
	_, err := m.abandon(ctx, tenant, tok, h)
	switch {
	case errors.Is(err, errKeyMoved):
		return Reservation{}, false, nil
	case errors.Is(err, errAdmissionRowCorrupt):
		cause = err
	}
	return m.refused(ctx, tenant, req, cause)
}

// denied gives the claim back and answers the refusal the evaluation reached. If the key
// has already moved to another caller, the refusal was reached without the key and is
// not its answer: the request is decided again against the row that now owns it.
func (m *Module) denied(ctx context.Context, tenant model.TenantID, req AdmissionRequest, tok admissionToken, h holdID, verdict BudgetReservation, spendLimit bool) (Reservation, bool, error) {
	if _, err := m.abandon(ctx, tenant, tok, h); errors.Is(err, errKeyMoved) {
		return Reservation{}, false, nil
	}
	out := m.mapDenied(verdict, req)
	out.SpendLimit = spendLimit
	m.recordDeny(ctx, tenant, req, out)
	return out, true, nil
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

// refuseUnreachable answers an admission that could not be established: a refusal,
// recorded, or — only under the explicit allow posture — an admission with no hold and
// the cause returned beside it.
func (m *Module) refuseUnreachable(ctx context.Context, tenant model.TenantID, req AdmissionRequest, cause error) (Reservation, error) {
	if req.Unreachable.allowOnUnread() {
		return Reservation{Allowed: true, EstimateMicroUSD: req.EstimateMicroUSD}, cause
	}
	out := Reservation{
		Allowed: false, Action: "block", Reason: ReasonStoreUnreachable,
		EstimateMicroUSD: req.EstimateMicroUSD,
	}
	m.recordDeny(ctx, tenant, req, out)
	return out, nil
}

// recordDeny audits the refusal and, when the ledger cannot take the row, records it in
// the engine log instead. It is the one place a refusal becomes evidence.
//
// The fallback is not a nicety: the refusal given most often on a broken install is the
// deny-closed one, and its audit row goes through the very handle whose failure produced
// it. Without the log a request refused over an unreadable ledger left no trace.
func (m *Module) recordDeny(ctx context.Context, tenant model.TenantID, req AdmissionRequest, res Reservation) {
	if err := m.auditAdmissionDeny(ctx, tenant, req, res); err != nil {
		m.recordUnauditedDeny(tenant, req, res, err)
	}
}

// auditAdmissionDeny appends the refusal to the tenant's audit chain and returns the
// write error: whether a refusal was recorded is the caller's business.
func (m *Module) auditAdmissionDeny(ctx context.Context, tenant model.TenantID, req AdmissionRequest, res Reservation) error {
	if m.data == nil {
		return attemptErr(errCodeCapabilityUnavailable, nil)
	}
	meta := map[string]any{
		"scope":  req.Scope,
		"action": res.Action,
		"reason": res.Reason,
	}
	if res.BudgetID != "" {
		meta["budget_id"] = res.BudgetID
	}
	return m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, err := sc.Audit().Append(ctx, model.AuditDraft{
			Actor:      auditActorFinOps,
			ActorKind:  model.ActorSystem,
			Action:     auditActionAdmissionDenied,
			TargetKind: admissionIdempotencyKind,
			Meta:       meta,
		})
		return err
	})
}

// recordUnauditedDeny writes the refusal to the engine log with the fields the audit row
// would have carried, at ERROR: a governed refusal with no durable evidence is an
// evidence failure.
func (m *Module) recordUnauditedDeny(tenant model.TenantID, req AdmissionRequest, res Reservation, cause error) {
	if m.log == nil {
		return
	}
	args := []any{
		"action", auditActionAdmissionDenied,
		"actor", auditActorFinOps,
		"tenant", tenant.String(),
		"scope", req.Scope,
		"admission_action", res.Action,
		"reason", res.Reason,
		"audit_err_class", auditFailureClass(cause),
	}
	if res.BudgetID != "" {
		args = append(args, "budget_id", res.BudgetID)
	}
	m.log.Error("finops admission: refusal could not be audited and is recorded here instead", args...)
}

// The classes the unaudited-refusal record names. Each is something an operator does
// something different about. The store error's own text is deliberately absent: it
// names the host, the user and the database of the budget store, and this line is at
// ERROR where a support bundle collects it.
const (
	auditFailureUnreachable = "unreachable"
	auditFailureTimeout     = "timeout"
	auditFailurePermission  = "permission"
	auditFailureOther       = "other"
)

// auditFailureClass classifies a failed audit write by sentinel and by error type
// through the chain, never by the driver's message. A cause it cannot place is "other".
func auditFailureClass(err error) string {
	if err == nil {
		return auditFailureOther
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return auditFailureTimeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return auditFailureTimeout
		}
		return auditFailureUnreachable
	}
	switch {
	case errors.Is(err, store.ErrStoreUnavailable),
		attemptCode(err) == errCodeStoreUnavailable,
		attemptCode(err) == errCodeCapabilityUnavailable:
		return auditFailureUnreachable
	case errors.Is(err, store.ErrReadOnly),
		errors.Is(err, store.ErrAppendOnlyGrantMissing),
		errors.Is(err, store.ErrSchemaBoundaryGrantMissing),
		errors.Is(err, store.ErrSchemaTriggerUnexecutable),
		errors.Is(err, fs.ErrPermission):
		return auditFailurePermission
	}
	return auditFailureOther
}
