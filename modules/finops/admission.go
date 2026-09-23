// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/model"
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

// auditActionAdmissionDenied names a refusal on the audit chain. The engine log carries
// the same action when the chain cannot take the row.
const auditActionAdmissionDenied = "finops.admission.denied"

// admissionClaimTakeover is how long a pending row is believed to be another caller's
// claim in flight. Waiting for such a claim is what keeps two callers from evaluating
// one key at once; past this bound the claimant is gone — no evaluation this module
// performs takes half a minute under the writer lock — and the claim is taken over.
const admissionClaimTakeover = 30 * time.Second

// admissionClaimPoll is the pause between two reads of a key another caller holds.
const admissionClaimPoll = time.Millisecond

// UnreachablePosture is how Reserve answers when the admission cannot be established.
// The zero value and "deny" are the same: refuse.
type UnreachablePosture string

// ParseUnreachablePosture maps operator input. Not implemented yet: it maps nothing.
func ParseUnreachablePosture(s string) UnreachablePosture {
	return ""
}

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
// request and, when ActorRef is set, every spend limit of that actor. Not implemented
// yet: it answers the zero Reservation, after one pass over the key.
func (m *Module) Reserve(ctx context.Context, tenant model.TenantID, req AdmissionRequest) (Reservation, error) {
	if m.data != nil {
		_ = m.passOverKey(ctx, tenant, req)
	}
	return Reservation{}, nil
}

// passOverKey takes the key through the transactions of an admission, in their order,
// and decides nothing: the row lookup; over a row it finds, a takeover that writes
// nothing, and nothing more; for a key nobody holds, a claim, the budget and spend-limit
// reads, a create under that claim and a publication that writes nothing. It stops at
// the first failure.
func (m *Module) passOverKey(ctx context.Context, tenant model.TenantID, req AdmissionRequest) error {
	row, found, err := m.readRow(ctx, tenant, req.IdempotencyKey)
	if err != nil {
		return err
	}
	if found {
		_, _, err = m.takeOver(ctx, tenant, row, row)
		return err
	}
	h := newHoldID()
	claimed, _, err := m.claim(ctx, tenant, admissionRow{
		key: req.IdempotencyKey, payloadHash: admissionPayloadHash(req), scope: req.Scope,
		estimate: req.EstimateMicroUSD, state: admStatePending, stateAt: m.clock.Now(), handle: h,
	})
	if err != nil {
		return err
	}
	now := m.clock.Now().Time()
	budgets, _, err := m.budgetTargets(ctx, tenant, req.Dims, now)
	if err != nil {
		return err
	}
	var seats []reservationTarget
	if req.ActorRef != "" {
		if seats, err = m.spendLimitTargets(ctx, tenant, req.ActorRef, req.Groups, now); err != nil {
			return err
		}
	}
	created, _, err := m.create(ctx, tenant, claimed.token(), h, budgets, seats, req.EstimateMicroUSD, now)
	if err != nil {
		return err
	}
	_, err = m.publish(ctx, tenant, created.tok, h, created.issued)
	return err
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
// caller's context is done. Not implemented yet: it returns at once.
func (m *Module) pauseBetweenClaimPolls(ctx context.Context) error {
	return nil
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
// through the chain. Not implemented yet: it names no class.
func auditFailureClass(err error) string {
	return ""
}
