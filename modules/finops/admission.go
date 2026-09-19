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
	// auditActorFinOps and auditActionAdmissionDenied name the refusal on the audit
	// chain. They are constants because the engine log carries the SAME action when
	// the chain cannot take the row, and a record an operator can only find under
	// one of two spellings is not a record.
	auditActorFinOps           = "finops"
	auditActionAdmissionDenied = "finops.admission.denied"

	maxIdempotencyKeyBytes = 256
	admStatePending        = "pending"
	admStateReserved       = "reserved"
	admStateCommitted      = "committed"
	admStateReleased       = "released"
	// admStateOwesRelease is a row that OWNS MONEY IT HAS NOT HANDED BACK. The key's
	// previous admission has been superseded, so the row is no longer an admission and
	// is never replayed; the handles it names are holds whose release has not been
	// confirmed, and it is not a claim either, so the next call on the key takes it over
	// and completes them at once instead of waiting out the claim bound.
	//
	// It exists because the only durable, authoritative link to a hold is the row that
	// names it. Recovery has to take the row over BEFORE it releases anything — the
	// version it acquires is what stops two callers cleaning up at once — so between
	// those two moments the row must keep pointing at the money. Clearing the handles
	// first and then releasing them meant a release that failed left an active hold that
	// nothing referred to and nobody was told about.
	admStateOwesRelease = "owes_release"
)

// admissionReplayWindow is how long one admission answers for RETRIES of the call
// that produced it. It is the bound in the definition above: inside it an identical
// request is the same call and is handed the same hold and the same charge; outside
// it the request is evaluated again against the ledger.
//
// Its value is the reservation TTL, and the two cannot be chosen apart. A replay of
// a still-reserved row hands back that row's HOLD, and a hold stops withholding
// headroom when it expires: a longer window would hand back a handle to money nobody
// is reserving, and it would let the re-evaluation of a row whose hold is still live
// orphan that hold — an active reservation no idempotency row points at, which is
// exactly the drift the reconciliation reports. At this value the two bounds close
// together and neither case can arise.
const admissionReplayWindow = 5 * time.Minute

// admissionClaimTakeover is how long a `pending` row is believed to be another
// caller's claim IN FLIGHT. A claim is staged before the budgets are evaluated
// precisely so two callers cannot evaluate one key at once, and waiting for it is
// what makes that work; but a caller that dies in that window leaves the row
// pending and nothing ever clears it, so the key refuses every later call for the
// life of the row. Past this bound the claim is not in flight — no evaluation this
// module performs takes half a minute under the writer lock — and the row is taken
// over and evaluated afresh.
//
// It is generous on purpose. Too short and two live callers take each other's
// claims and both evaluate; too long only delays the recovery of a key whose owner
// is already gone.
const admissionClaimTakeover = 30 * time.Second

// admissionClaimPoll is the pause between two reads of a pending row. Without it
// the wait was a busy loop — 64 attempts of 32 reads, measured at 330 ms of CPU for
// a row that was never going to change — and what it bought was nothing: the claim
// it waits for is finished by another goroutine or another process, and reading
// faster does not make that happen sooner.
const admissionClaimPoll = time.Millisecond

// maxClaimWaitPolls bounds one wait. The caller re-reads and decides again after
// it, so this is not the total: it is how long a single wait holds its opinion of
// the row before looking at the world afresh.
const maxClaimWaitPolls = 32

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
	// EstimateMicroUSD is the a-priori hold. Zero HOLDS NOTHING: it inserts no
	// reservation row and is handed no handle, because a hold of zero withholds
	// no headroom from any concurrent caller and leaves nothing for a settlement
	// to return. It is still a real question — it fail-closes on an unreadable
	// store and an already-exhausted cap still denies it — and, because it holds
	// nothing, it is re-evaluated on every call instead of replayed under its
	// idempotency key.
	EstimateMicroUSD int64
	// IdempotencyKey is required. Bound to tenant + scope + canonical payload.
	//
	// WHAT A REPLAY IS. A replay answers for the SAME call within a bounded retry
	// window (admissionReplayWindow), measured from the moment the row reached the
	// state it is in; outside that window the request is evaluated again against the
	// ledger. The key exists so that a RETRY of one call does not hold or charge
	// twice — not so that a later, separate call carrying the same bytes gets a free
	// pass. A caller whose key is stable by construction (a run reference, a task id,
	// a digest of the request) therefore gets a fresh verdict as soon as the window
	// has passed, which is what makes a cap blown since the first answer refuse.
	//
	// Two further rules decide the same question and are part of the definition: an
	// admission that took no hold has nothing to hand back and is re-evaluated on
	// every call (handlesLive), and reuse with a DIFFERENT payload is never a replay
	// but ErrAdmissionConflict. A released or expired row is re-reserved (a failed
	// attempt of the same intent).
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
				// A claim still in flight is waited for: that wait is what keeps two
				// callers from evaluating one key at once. A claim OLDER than
				// admissionClaimTakeover is not in flight — the caller that staged it
				// died between staging and its verdict — and waiting for a caller that
				// is gone is how one crash makes a key refuse forever. Falling out of
				// the switch takes the row over and evaluates it afresh below.
				if !m.staleClaim(existing) {
					replay, ok, rerr := m.waitForClaim(ctx, tenant, req, hash)
					if rerr != nil {
						return m.refuseUnreachable(ctx, tenant, req, rerr)
					}
					if ok {
						return replay, nil
					}
					continue
				}
			}
		}

		var claim idempotencyRow
		if found {
			// TAKING THE ROW OVER, which is the act that grants the right to evaluate
			// this key: an abandoned claim, or an admission whose window has passed. It
			// is a CONDITIONAL transition on the version that justified it, so of any
			// number of callers that read the same row exactly one acquires it. The rest
			// are told to look again — and by then this claim is fresh, so they wait for
			// it instead of taking it from a caller that is alive.
			// A row that names no hold owns no money — an abandoned claim, or an
			// admission that never had an enforcing target — so it becomes this caller's
			// claim in ONE fenced write, exactly as it did before there was anything to
			// recover. That is the common recovery, and it does not pay for the other one.
			claim = existing
			if existing.handle != "" || existing.spendHandle != "" {
				// Otherwise the row is taken over STILL NAMING the holds the previous
				// admission owns, because from here until their release is confirmed this
				// row is the only authoritative record of that money. Only then are they
				// handed back, each on its own merits.
				owed, oerr := m.writeIdempotency(ctx, tenant, admissionWrite{
					row: existing, to: admStateOwesRelease, acquire: true,
					handle: existing.handle, spendHandle: existing.spendHandle,
				})
				if lostTheKey(oerr) {
					continue
				}
				if oerr != nil {
					return m.refuseUnreachable(ctx, tenant, req, oerr)
				}
				unfinished, rerr := m.recoverHolds(ctx, tenant, owed.handle, owed.spendHandle)
				if len(unfinished) > 0 {
					// The row already names what is owed and it stays that way: the next
					// call on this key finds it and completes it. Admitting this caller now
					// would replace the only record of live money with a fresh admission,
					// which is how a hold stops being anybody's.
					m.recordUnrecoveredHolds(tenant, req.IdempotencyKey, unfinished, rerr)
					return m.refuseUnreachable(ctx, tenant, req, rerr)
				}
				claim = owed
			}
			// The money is accounted for, so the row stops pointing at it and this
			// caller's claim begins with a lease of its own.
			claim, err = m.writeIdempotency(ctx, tenant, admissionWrite{
				row: claim, to: admStatePending, acquire: true,
			})
			if lostTheKey(err) {
				continue
			}
			if err != nil {
				return m.refuseUnreachable(ctx, tenant, req, err)
			}
		} else {
			var claimed bool
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
			if lostTheKey(m.abandonClaim(ctx, tenant, claim)) {
				continue
			}
			return reservation, ferr
		}
		if denied {
			// A caller whose claim was taken from it reached this verdict without the
			// key, so the verdict is not this key's answer: the successor's row is left
			// alone and the request is decided again against it.
			if lostTheKey(m.abandonClaim(ctx, tenant, claim)) {
				continue
			}
			m.recordDeny(ctx, tenant, req, reservation)
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
				// The pooled hold this caller took goes back before it walks away, and
				// whether it went back is not discarded: a hold it could not hand back
				// stays named on the row it still owns, for the next call to complete.
				unfinished, uerr := m.recoverHolds(ctx, tenant, budgetRes.Handle)
				if len(unfinished) > 0 {
					m.recordUnrecoveredHolds(tenant, req.IdempotencyKey, unfinished, uerr)
					if lostTheKey(m.retainOwedHolds(ctx, tenant, claim, unfinished)) {
						continue
					}
				} else if lostTheKey(m.abandonClaim(ctx, tenant, claim)) {
					continue
				}
				if ferr != nil {
					return reservation, ferr
				}
				m.recordDeny(ctx, tenant, req, reservation)
				return reservation, nil
			}
		}

		// PUBLISHING THE VERDICT under the claim this caller still holds. A caller whose
		// claim was taken over while it evaluated does not publish: it hands its own hold
		// back and is answered from the row that now owns the key.
		if _, perr := m.writeIdempotency(ctx, tenant, admissionWrite{
			row: claim, to: admStateReserved,
			handle: budgetRes.Handle, spendHandle: spendRes.Handle,
		}); perr != nil {
			// The holds go back on EITHER failure: they are not the successor's to keep
			// and they are not this call's to leak. Each is asked about on its own, and
			// what could not be handed back is reported rather than assumed done.
			unfinished, uerr := m.recoverHolds(ctx, tenant, budgetRes.Handle, spendRes.Handle)
			if len(unfinished) > 0 {
				m.recordUnrecoveredHolds(tenant, req.IdempotencyKey, unfinished, uerr)
			}
			if lostTheKey(perr) {
				// A successor owns the key, so this caller may not write the row at all —
				// that is the fence, and reaching past it to record its own trouble would
				// replace the successor's admission. What it could not hand back is
				// reported above and stops withholding money at its own expiry, which the
				// reservation sweep then makes explicit and reports as drift.
				continue
			}
			// The key is still this caller's, so the row is where the unfinished effect
			// stays, named and retryable, instead of being abandoned as if it were done.
			if len(unfinished) > 0 {
				_ = m.retainOwedHolds(ctx, tenant, claim, unfinished)
			} else {
				_ = m.abandonClaim(ctx, tenant, claim)
			}
			return m.refuseUnreachable(ctx, tenant, req, perr)
		}
		return Reservation{
			Allowed: true, Handle: budgetRes.Handle, SpendHandle: spendRes.Handle,
			EstimateMicroUSD: req.EstimateMicroUSD,
		}, nil
	}
	return m.refuseUnreachable(ctx, tenant, req, fmt.Errorf("finops: admission retries exhausted"))
}

// claimIdempotency stages the row for a key that has none. The unique index is the
// fence here, so exactly one of any number of concurrent callers inserts and the rest
// are told to look again. It returns the row AS STORED, which is what makes the
// claimant the only caller holding the version its verdict will be written against.
func (m *Module) claimIdempotency(ctx context.Context, tenant model.TenantID, row idempotencyRow) (bool, idempotencyRow, error) {
	stored, err := m.writeIdempotency(ctx, tenant, admissionWrite{row: row, to: admStatePending})
	if lostTheKey(err) {
		return false, idempotencyRow{}, nil
	}
	if err != nil {
		return false, idempotencyRow{}, err
	}
	return true, stored, nil
}

// abandonClaim gives up a claim this caller staged and will not answer for — the
// request was denied, or the store could not be read on the way to a verdict.
//
// It RETURNS the outcome instead of discarding it, because a refused abandonment is a
// fact the caller has to act on: the key belongs to a successor now, whose admission
// must be left exactly as it is and whose answer this caller should be given in place
// of a verdict it reached for a claim it no longer held. A failure that is NOT a
// refusal leaves a pending row behind, which is the state the takeover bound exists to
// recover and the reason it is generous.
func (m *Module) abandonClaim(ctx context.Context, tenant model.TenantID, row idempotencyRow) error {
	_, err := m.writeIdempotency(ctx, tenant, admissionWrite{row: row, to: admStateReleased})
	return err
}

// retainOwedHolds records ON THE ROW THIS CALLER STILL OWNS the holds whose release could
// not be confirmed, so the next call on the key completes them instead of never hearing of
// them. It is the difference between a failed release and a forgotten one.
//
// The row is not an admission afterwards and it is not a claim: it is re-evaluated by the
// next caller at once, which is what makes the retry immediate rather than something that
// waits out a claim bound. It goes through the same fenced writer as everything else, so a
// caller that has lost the key is refused here too and the successor's admission stands.
func (m *Module) retainOwedHolds(
	ctx context.Context, tenant model.TenantID, row idempotencyRow, owed []string,
) error {
	if len(owed) == 0 {
		return nil
	}
	// The two handle columns stop being "the pooled one" and "the seat one" here: a row
	// that only owes money back names what it owes, in the slots it has. Recovery reads
	// both and asks each what it is doing, so which slot a handle sits in changes nothing,
	// and a settlement cannot take authority from either (see settlementOwnsRow).
	w := admissionWrite{row: row, to: admStateOwesRelease, handle: owed[0]}
	if len(owed) > 1 {
		w.spendHandle = owed[1]
	}
	_, err := m.writeIdempotency(ctx, tenant, w)
	return err
}

// recordUnrecoveredHolds says, at ERROR and in the engine log, that money is still held
// under a key whose admission is gone. The durable, actionable record is the row — that is
// what a later call acts on — and this is the line an operator can see NOW, because a
// refusal alone does not say that a reservation outlived the call that took it.
//
// ERROR, not WARN: headroom withheld from every other caller with nothing pointing at it
// is a money fault, and on the one path where the row cannot carry it (a caller that has
// lost the key may not write another owner's row) this line and the reservation sweep are
// the whole record.
func (m *Module) recordUnrecoveredHolds(tenant model.TenantID, key string, owed []string, cause error) {
	if m.log == nil {
		return
	}
	m.log.Error("finops admission: a superseded hold could not be handed back and is still withheld",
		"tenant", tenant.String(),
		"idempotency_key", key,
		"holds_owed", len(owed),
		"cause_class", auditFailureClass(cause),
	)
}

// waitForClaim waits for the caller that holds a pending row to reach its verdict,
// and hands that verdict back when it does. It gives up as soon as there is nothing
// left to wait for — the row answered, it was abandoned, or its owner stopped being
// believable — and it PAUSES between reads rather than spinning: what it is waiting
// for is another goroutine or another process, and reading faster does not make it
// arrive sooner.
//
// ok=false is not a refusal here either. It means the caller should read the row
// again and decide, which may be to take it over.
func (m *Module) waitForClaim(ctx context.Context, tenant model.TenantID, req AdmissionRequest, hash string) (Reservation, bool, error) {
	for i := 0; i < maxClaimWaitPolls; i++ {
		replay, ok, err := m.replayIfPresent(ctx, tenant, req, hash)
		if err != nil || ok {
			return replay, ok, err
		}
		existing, found, err := m.lookupIdempotency(ctx, tenant, req.IdempotencyKey)
		if err != nil {
			return Reservation{}, false, err
		}
		if !found || existing.state != admStatePending || m.staleClaim(existing) {
			return Reservation{}, false, nil
		}
		if err := m.pauseBetweenClaimPolls(ctx); err != nil {
			return Reservation{}, false, err
		}
	}
	return Reservation{}, false, nil
}

// staleClaim reports whether a pending row has stopped being an in-flight claim. It
// reads the module's clock, never the wall clock, so a test can age a claim without
// spending the time.
//
// A row with no stamp is STALE. It was written by a build that did not record one;
// believing it in flight would be believing a claim that may have been abandoned
// before this binary existed, and the cost of taking over a claim that was in fact
// live is one duplicated evaluation under the writer lock, while the cost of the
// other answer is a key that never answers again.
func (m *Module) staleClaim(row idempotencyRow) bool {
	if row.stateAt.IsZero() {
		return true
	}
	return m.clock.Now().Time().Sub(row.stateAt.Time()) >= admissionClaimTakeover
}

// pauseBetweenClaimPolls waits out one poll interval, or returns as soon as the
// caller's context is done. It is the one place this module sleeps, and it does so
// through the context so a cancelled request stops waiting immediately instead of
// finishing a wait nobody is listening to.
func (m *Module) pauseBetweenClaimPolls(ctx context.Context) error {
	timer := time.NewTimer(admissionClaimPoll)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("finops: waiting for an in-flight admission claim: %w", ctx.Err())
	case <-timer.C:
		return nil
	}
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

// replayIfPresent answers, for one idempotency row, the single question "is this
// call a retry of the call that wrote this row, or a new one?".
//
// A REPLAY ANSWERS FOR THE SAME CALL WITHIN A BOUNDED RETRY WINDOW; OUTSIDE IT THE
// REQUEST IS EVALUATED AGAIN AGAINST THE LEDGER. That is the whole contract, and it
// binds every state arm — the committed one included. A key is an instrument for
// making a retry cheap, not an instrument for making a verdict permanent: a caller
// whose key is stable by construction (a run reference, a task id, a digest of the
// bytes) would otherwise carry its first "allowed" for the life of the key, and the
// ledger it was allowed against would never be read again.
//
// The window is the ONE place that decision lives, and it is asked BEFORE the state
// is looked at, so no arm can be added later that forgets it. Inside the window the
// arms differ, and for a reason each: a COMMITTED row replays outright, because the
// call it answers for already ran and was charged and a retry of it must not be
// charged twice; a RESERVED row replays only while its hold is still live
// (handlesLive), because what a reserved replay hands back IS that hold.
//
// ok=false is never a refusal. It means "this row does not answer for this call",
// and the caller re-evaluates the request against the budgets — which is the honest
// answer and may well be yes.
func (m *Module) replayIfPresent(ctx context.Context, tenant model.TenantID, req AdmissionRequest, hash string) (Reservation, bool, error) {
	existing, found, err := m.lookupIdempotency(ctx, tenant, req.IdempotencyKey)
	if err != nil || !found || existing.payloadHash != hash {
		return Reservation{}, false, err
	}
	if !m.withinReplayWindow(existing) {
		return Reservation{}, false, nil
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
	m.recordDeny(ctx, tenant, req, out)
	return out, nil
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
	if settlementOwnsRow(row, found) {
		return m.recordSettlement(ctx, tenant, row, admStateCommitted)
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
	if settlementOwnsRow(row, found) {
		return m.recordSettlement(ctx, tenant, row, admStateReleased)
	}
	return nil
}

// recordSettlement writes the state a settled handle leaves its idempotency row in. It
// is the SECOND thing a settlement does, and the only one that is about authority
// rather than money: by the time this runs the handle's headroom has been charged or
// returned, which is right however late the settlement arrives, whereas writing the row
// claims the KEY — and a newer call may already hold it.
//
// So a REFUSED write is success here. The old monetary effect is settled and the
// current admission stands untouched; replacing it would hand the next retry a handle
// to money nobody is holding. Keeping the two apart is the whole point: settling the
// old effect is always right, and taking the key is never right once it has moved.
//
// A settlement of a handle already settled is a legal write that changes nothing, and
// it does not renew the replay deadline — see stateEntryStamp.
//
// settlementOwnsRow decides whether it may run at all. A settlement finds its row BY
// HANDLE, and a row can name a handle for two very different reasons: because that handle
// is its admission, or because the handle is money a superseded row still owes back. Only
// the first is authority over the key. Writing the second would turn a record of unfinished
// cleanup into a settled admission — which the replay window would then hand to the next
// retry as a handle to money nobody is holding.
func settlementOwnsRow(row idempotencyRow, found bool) bool {
	if !found {
		return false
	}
	switch row.state {
	case admStateReserved, admStateCommitted, admStateReleased:
		// The handle IS this row's outcome: a live admission being settled now, or an
		// idempotent repeat of a settlement already applied.
		return true
	}
	// A claim, or a row that owes money back. Neither is an admission of this handle.
	return false
}

func (m *Module) recordSettlement(ctx context.Context, tenant model.TenantID, row idempotencyRow, to string) error {
	_, err := m.writeIdempotency(ctx, tenant, admissionWrite{
		row: row, to: to, handle: row.handle, spendHandle: row.spendHandle,
	})
	if lostTheKey(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("finops: record the settlement of an admitted call: %w", err)
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

// recordDeny audits the refusal and, when the ledger cannot take the row,
// records it in the engine log instead. It is the ONE place a deny becomes
// evidence, so no caller has to remember which of the two happened.
//
// The fallback is not a nicety. The refusal this module gives most often on a
// broken install is ReasonStoreUnreachable, and its audit row goes through the
// very handle whose failure produced it: the write cannot succeed, by
// construction. With the write error discarded, a request refused over an
// unreadable ledger left no trace anywhere — the ledger could not take one and
// nothing else was asked to.
func (m *Module) recordDeny(ctx context.Context, tenant model.TenantID, req AdmissionRequest, res Reservation) {
	if err := m.auditAdmissionDeny(ctx, tenant, req, res); err != nil {
		m.recordUnauditedDeny(tenant, req, res, err)
	}
}

// auditAdmissionDeny appends the deny to the tenant's audit chain. It RETURNS the
// write error rather than discarding it: whether a refusal was recorded is the
// caller's business, and on an unreachable ledger it is the whole question.
func (m *Module) auditAdmissionDeny(ctx context.Context, tenant model.TenantID, req AdmissionRequest, res Reservation) error {
	if m.data == nil {
		// No handle, no chain to append to. It is reported as a failed audit and not
		// as "nothing to do", because the refusal still happened and still needs a
		// record somewhere.
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

// recordUnauditedDeny writes the refusal to the engine log with the fields the
// audit row would have carried, so the same action name finds it either way and
// an operator does not have to know which record they are reading. ERROR, not
// WARN: a governed refusal with no durable evidence is an evidence failure, and
// on the deny-closed path it is also the only sign the ledger is down.
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

// The classes the unauditable-refusal record names. Each is something an operator
// DOES something different about — bring the store back, look at what is making it
// slow, grant the engine's role the write, or read the chain they already have —
// and together they are all this line says about the failure.
//
// The error's own text is deliberately absent. It is a STORE error, and a store
// error names the host, the user and the database of the budget store; this line
// is at ERROR, and a support bundle collects it. A class is what the operator can
// act on, and the topology is what they cannot.
const (
	auditFailureUnreachable = "unreachable"
	auditFailureTimeout     = "timeout"
	auditFailurePermission  = "permission"
	auditFailureOther       = "other"
)

// auditFailureClass classifies the failed audit write CAUSALLY — by sentinel and
// by error type, through the chain — and never by matching the driver's message,
// which changes with the driver and is exactly the text this class exists to keep
// off the line. A cause it cannot place is "other" rather than a guess: a wrong
// class sends an operator somewhere the fault is not.
func auditFailureClass(err error) string {
	if err == nil {
		// Unreachable from recordUnauditedDeny, whose caller only reaches it with a
		// failure. Answered rather than left to the type's zero value, because an
		// empty class on an ERROR line reads as a field that was lost.
		return auditFailureOther
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return auditFailureTimeout
	}
	// The dial failure that produced the measured leak arrives here: the store
	// wraps it, AttemptError unwraps to it, and net.Error separates "did not answer
	// in time" from "could not be reached at all".
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

type idempotencyRow struct {
	id          model.ID
	key         string
	payloadHash string
	handle      string
	spendHandle string
	scope       string
	estimate    int64
	state       string
	// stateAt is when the row's current owner put it in state, by the module's
	// clock. Zero when the row predates the column, which every reader here treats
	// as "old enough to re-evaluate": the ledger is the answer a missing timestamp
	// cannot contradict. What moves it, and what must not, is stateEntryStamp.
	stateAt model.Timestamp
	// version is the store version this row was READ at, and it is the fence. A
	// write presents it and the store refuses the write if the row has moved since:
	// authority over a key is held, not assumed. Zero on a row that was never read,
	// which is a row that does not exist yet and is created rather than updated.
	version int64
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
		stateAt:     parseRowTimestamp(rec.String(colAdmStateAt)),
		version:     rec.Int(model.ColVersion),
	}
}

// parseRowTimestamp reads a stamp the module wrote. An absent or unparsable value
// is the zero Timestamp rather than an error: the two callers of this field both
// answer "re-evaluate" for a row they cannot date, so there is no decision left for
// an error to change and no path on which one could be swallowed.
func parseRowTimestamp(s string) model.Timestamp {
	if strings.TrimSpace(s) == "" {
		return model.Timestamp{}
	}
	ts, err := model.ParseTimestamp(s)
	if err != nil {
		return model.Timestamp{}
	}
	return ts
}

// admissionWrite is one attempted write of one admission idempotency row: the row AS
// READ, and the change this writer wants to make to it. Every write of
// finops.admission_idempotency is one of these.
type admissionWrite struct {
	// row is the row as the writer READ it — its id, the store version its decision
	// was made on, and the state it is being moved out of. A zero id means the read
	// found nothing, and the write is the INSERT that creates the row.
	row idempotencyRow
	// to is the state the row is to reach. It may EQUAL row.state: a settlement that
	// repeats a settlement already applied is a legal write that changes nothing.
	to string
	// handle and spendHandle are the reservation handles the row is to record. They
	// are given per write rather than carried on row, because CLEARING them is a
	// change like any other and a writer has to say it is making one.
	handle      string
	spendHandle string
	// acquire says this write TAKES THE KEY OVER from an owner that is gone. It is
	// the one thing the state string cannot express — a takeover goes from pending to
	// pending — and it is what gives the new claim a lease measured from its own
	// acquisition instead of inheriting a dead owner's.
	acquire bool
}

// writeIdempotency is the ONE function that writes a finops.admission_idempotency
// row. Six callers reach it — the claim, the takeover of an abandoned claim, the
// publication of a verdict, the abandonment of a claim, and the two settlements —
// and not one of them knows how the row is fenced. This is the contract.
//
// AUTHORITY OVER A KEY IS HELD, NOT ASSUMED. A writer may only change the row it
// READ: every write presents the store version its decision was made on, and the
// store performs it as a single
// `UPDATE ... SET ..., version = version + 1 WHERE id = ? AND tenant_id = ? AND
// version = ?`, so a row that has moved since that read affects no rows and the write
// comes back store.ErrConflict. The row is NOT re-fetched here to obtain a fresh
// version: re-reading the current version and then overwriting is not a fence, it is
// a fence-shaped way of overwriting whatever happens to be there, and that is the
// defect this function exists to make unreachable.
//
// A REFUSED WRITE NEVER OVERWRITES, AND IS NOT A FAILURE ON ITS OWN. It means the key
// has a newer owner, and the writer's obligation is to READ AGAIN AND DECIDE AGAIN: a
// caller that lost its claim is answered from the row that is now authoritative —
// which, for a retry of the same call, is that successor's admission — and a
// settlement that lost the key has already settled its own money and reports success.
// What no refused write does is replace or remove a successor's admission.
//
// WHAT FENCES IT, PER STORE. The version predicate is ONE statement and is atomic on
// both supported engines: on PostgreSQL the UPDATE locks the row for the rest of the
// transaction, and on SQLite the transaction IS the single writer. Neither can admit
// two winners, and the statement is the same on both — only the placeholder style
// differs. The module's writer lock (lockFinOpsWriter) is taken as well and does a
// DIFFERENT job: it serializes this module's writers against each other at a declared
// point, so a read and a write inside ONE Mutate cannot interleave. Neither mechanism
// replaces the other — the lock holds WITHIN a transaction, the version holds ACROSS
// transactions, and every interleaving that produced two live holds for one key was
// across them.
//
// THE CLAIM ITSELF IS FENCED BY THE UNIQUE INDEX on (tenant, idempotency_key), not by
// a version: there is no row to compare against, so exactly one INSERT wins and the
// losers are told the same thing on the same terms.
//
// It returns the row AS STORED, carrying the version the next write of it must
// present. A caller that chains two writes — a claim, then its verdict — passes the
// returned row on; a caller that presents the row it read a second time is refused,
// which is the correct answer to a caller writing twice from one decision.
func (m *Module) writeIdempotency(ctx context.Context, tenant model.TenantID, w admissionWrite) (idempotencyRow, error) {
	var stored idempotencyRow
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		if err := lockFinOpsWriter(ctx, sc); err != nil {
			return err
		}
		repo, err := sc.Ext(admissionIdempotencyKind)
		if err != nil {
			return err
		}
		rec := model.Record{
			colAdmKey:         w.row.key,
			colAdmPayloadHash: w.row.payloadHash,
			colAdmHandle:      w.handle,
			colAdmSpendHandle: w.spendHandle,
			colAdmScope:       w.row.scope,
			colAdmEstimate:    w.row.estimate,
			colAdmState:       w.to,
			colAdmStateAt:     m.stateEntryStamp(w),
		}
		var out model.Record
		if w.row.id.IsZero() {
			out, err = repo.Create(ctx, rec)
		} else {
			rec[model.ColID] = w.row.id.String()
			rec[model.ColVersion] = w.row.version
			out, err = repo.Update(ctx, rec)
		}
		if err != nil {
			return err
		}
		stored = idempotencyFromRecord(out)
		return nil
	})
	if err != nil {
		return idempotencyRow{}, err
	}
	return stored, nil
}

// stateEntryStamp answers what state_at is to hold after one write, and it IS the
// meaning of that column: the moment the row's CURRENT OWNER put it in the state it
// is in. Two things move it and nothing else does.
//
// A STATE CHANGE moves it, which is what bounds a replay: a row answers for retries of
// the call that put it in this state, for as long as that state is young.
//
// AN ACQUISITION moves it although the state string does not change, because what
// changed is the OWNER. A claim that inherited its predecessor's stamp would be born
// already past the takeover bound and would be taken from it by the next caller to
// look — a LIVE claim losing its key, which is the opposite of recovering an
// abandoned one.
//
// AND A REPEAT MOVES NOTHING. A settlement that repeats a settlement already applied
// is the same owner writing the same state; it entered that state when it entered it.
// Restamping renewed the replay deadline on every retry, so a caller committing every
// four minutes carried its first verdict for as long as it kept retrying — over a cap
// that had since been blown.
//
// An undated row STAYS undated on a repeat. Such a row was written by a build that did
// not record the column, and this write learned nothing about when it entered its
// state: stamping it now would date it to the moment a retry happened to arrive and
// would turn "older than any window" into "just now", which is the one reading that
// makes a stale verdict permanent.
func (m *Module) stateEntryStamp(w admissionWrite) any {
	if w.row.id.IsZero() || w.acquire || w.to != w.row.state {
		return m.clock.Now().String()
	}
	if w.row.stateAt.IsZero() {
		return nil
	}
	return w.row.stateAt.String()
}

// lostTheKey reports that a write of the idempotency row was refused because the row
// moved on and the writer no longer holds the key. It is neither a store failure nor a
// refusal of the request: see writeIdempotency for what the writer owes instead.
func lostTheKey(err error) bool { return errors.Is(err, store.ErrConflict) }

// withinReplayWindow reports whether a row is still young enough to answer for
// retries of the call that wrote it. It reads the module's clock, never the wall
// clock, so a test can move the window without spending it.
//
// A row with no stamp is OUTSIDE the window. It was written by a build that did not
// record one, and a row that cannot be dated cannot be shown to be a retry; sending
// that request to the ledger costs a read and returns the answer that is true now,
// while the other default would keep exactly the frozen verdicts this bound exists
// to end.
func (m *Module) withinReplayWindow(row idempotencyRow) bool {
	if row.stateAt.IsZero() {
		return false
	}
	return m.clock.Now().Time().Sub(row.stateAt.Time()) < admissionReplayWindow
}

// handlesLive reports whether the reservation handles recorded against an
// idempotency row still hold headroom, which is the one question a replay of that
// row answers: a retry is handed the hold the first call took instead of taking a
// second one.
//
// NO HANDLE IS NOT A LIVE HOLD. An admission that held nothing has nothing to
// hand back, so it is not replayed — it is re-evaluated, under the same writer
// lock, and a cap that has been blown since the first answer refuses. The reverse
// answer froze the first verdict under its key forever, and the callers whose key
// is stable by design are exactly the ones that must not be frozen: a session
// resume re-runs the launch gate with the same run reference precisely so a
// budget change since the last launch is honoured. Re-evaluating costs nothing a
// replay was protecting, because there is no row and no handle to double.
//
// This is the ONE place that decision lives. The idempotency row is still written
// and is still what makes a key reused with a DIFFERENT payload a conflict; what
// it no longer does is answer for the ledger.
func (m *Module) handlesLive(ctx context.Context, tenant model.TenantID, handle, spendHandle string) (bool, error) {
	if handle == "" && spendHandle == "" {
		return false, nil
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
			standing, err := readHoldStanding(ctx, repo, h, now)
			if err != nil {
				return err
			}
			if standing.unreadable {
				live = false
				return nil
			}
			if standing.rows > 0 && !standing.withholding {
				live = false
				return nil
			}
		}
		return nil
	})
	return live, err
}

// holdStanding is what ONE reservation handle is doing right now.
type holdStanding struct {
	// rows is how many reservation rows are recorded under the handle. Zero means the
	// admission had no enforcing target, which is not the same as a hold that was
	// settled.
	rows int
	// withholding says at least one of those rows is active AND its expiry has not
	// passed, so it is keeping money from other callers at this instant.
	withholding bool
	// unreadable says the enumeration did not complete, so nothing here is a conclusion.
	// A partial read must never be mistaken for "there is nothing to do".
	unreadable bool
}

// readHoldStanding answers that one question for one handle, and it is the only place the
// answer is computed — the replay predicate and the recovery of superseded money both
// stand on it, and they must not drift apart on what "still withholding" means.
func readHoldStanding(
	ctx context.Context, repo store.GenericRepo, handle string, now model.Timestamp,
) (holdStanding, error) {
	rows, incomplete, err := scanReservations(ctx, repo, []model.Filter{eq(colResvHandle, handle)})
	if err != nil {
		return holdStanding{}, err
	}
	if incomplete != "" {
		return holdStanding{unreadable: true}, nil
	}
	out := holdStanding{rows: len(rows)}
	for _, r := range rows {
		if r.String(colResvState) != resvStateActive {
			continue
		}
		exp, perr := model.ParseTimestamp(r.String(colResvExpiresAt))
		if perr == nil && !exp.Time().After(now.Time()) {
			continue
		}
		out.withholding = true
	}
	return out, nil
}

// recoverHolds hands back the money of holds this key owns and no longer points at, AND
// IT ASKS ABOUT EACH HANDLE ON ITS OWN. That is the whole difference from the replay
// predicate, which asks one question about the pair: an admission can own a pooled budget
// hold and a per-seat spend hold, they are settled in two separate transactions, and so
// the pair comes apart. One half settled and the other still live, or one half expired and
// the other still live, are both ordinary states — and "is the PAIR still live?" answers
// no for both, which read as "nothing left to recover" exactly when there was something.
//
// Only a hold that is STILL WITHHOLDING money is released. A lapsed one already withholds
// nothing, because the ceiling excludes rows past their expiry, and flipping it to
// released would file a settlement this call never performed in place of the expiry that
// is what actually happened to it.
//
// It returns the handles whose release it could NOT confirm, and the first cause. An
// empty list is the only thing that means the money is accounted for: a read it could not
// complete counts as unfinished, because a partial read is not evidence of absence.
func (m *Module) recoverHolds(
	ctx context.Context, tenant model.TenantID, handles ...string,
) ([]string, error) {
	var (
		unfinished []string
		firstErr   error
	)
	seen := make(map[string]bool, len(handles))
	for _, h := range handles {
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		withholding, err := m.holdWithholding(ctx, tenant, h)
		if err != nil {
			unfinished = append(unfinished, h)
			if firstErr == nil {
				firstErr = fmt.Errorf("finops: read a superseded hold: %w", err)
			}
			continue
		}
		if !withholding {
			continue
		}
		if err := m.ReleaseReservation(ctx, tenant, h); err != nil && !errors.Is(err, store.ErrNotFound) {
			unfinished = append(unfinished, h)
			if firstErr == nil {
				firstErr = fmt.Errorf("finops: hand back a superseded hold: %w", err)
			}
		}
	}
	return unfinished, firstErr
}

// holdWithholding reports whether ONE handle is keeping money from other callers now.
func (m *Module) holdWithholding(ctx context.Context, tenant model.TenantID, handle string) (bool, error) {
	var out holdStanding
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(budgetReservationKind)
		if err != nil {
			return err
		}
		out, err = readHoldStanding(ctx, repo, handle, m.clock.Now())
		return err
	})
	if err != nil {
		return false, err
	}
	if out.unreadable {
		return false, fmt.Errorf("finops: the reservations of hold %s could not be enumerated", handle)
	}
	return out.withholding, nil
}
