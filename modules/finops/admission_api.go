// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"errors"
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
)

// The admission routes. A caller reserves before a billable effect and is answered with
// one handle, then settles that handle: Commit with the measured cost, or Release when the
// effect did not run. The documents are decoded with unknown fields refused, so the names
// below are the published contract.

type admissionReserveBody struct {
	Scope            string    `json:"scope"`
	Dims             SpendDims `json:"dims"`
	ActorRef         string    `json:"actor_ref,omitempty"`
	Groups           []string  `json:"groups,omitempty"`
	EstimateMicroUSD int64     `json:"estimate_micro_usd"`
	IdempotencyKey   string    `json:"idempotency_key"`
	Unreachable      string    `json:"unreachable,omitempty"`
}

// ReasonAdmittedUnestablished is the reason of an answer admitted with no hold under the
// allow posture: the admission could not be established, and the request chose admission
// over a refusal. It is fixed text; the cause is recorded in the engine log by its class.
const ReasonAdmittedUnestablished = "admission could not be established; admitted without a hold (unreachable=allow)"

// admissionCommitBody and admissionReleaseBody name a hold by the one handle Reserve
// answered with. A hold an earlier build published as a pair is settled by either of its
// handles, and the other one with it.
type admissionCommitBody struct {
	Handle         string `json:"handle"`
	ActualMicroUSD int64  `json:"actual_micro_usd"`
}

type admissionReleaseBody struct {
	Handle string `json:"handle"`
}

// handleAdmissionReserve holds the estimated spend of a billable effect against every
// enforcing budget that scopes the request, and against the named actor's spend limits,
// under one handle. A retry with the same idempotency key and payload is answered with
// the hold the first call took. An admission that cannot be established refuses rather
// than admits, unless the request opts into admission without a hold, which the answer's
// reason then states.
func (m *Module) handleAdmissionReserve(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in admissionReserveBody
	if !decodeJSON(w, r, &in) {
		return
	}
	res, err := m.Reserve(r.Context(), mc.Tenant, AdmissionRequest{
		Scope:            in.Scope,
		Dims:             in.Dims,
		ActorRef:         in.ActorRef,
		Groups:           in.Groups,
		EstimateMicroUSD: in.EstimateMicroUSD,
		IdempotencyKey:   in.IdempotencyKey,
		Unreachable:      ParseUnreachablePosture(in.Unreachable),
	})
	switch {
	case errors.Is(err, ErrInvalidAdmission):
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
	case errors.Is(err, ErrAdmissionConflict):
		writeJSON(w, http.StatusConflict, errorBody(ErrAdmissionConflict.Error()))
	case res.Allowed && err != nil:
		// Under the allow posture an admission that could not be established admits with
		// no hold, and err names why. The answer says so in fixed text, and the engine log
		// records it by class: the cause can carry the store's own message.
		res.Reason = ReasonAdmittedUnestablished
		m.recordAdmittedUnestablished(mc.Tenant, in.Scope, err)
		writeJSON(w, http.StatusOK, res)
	case res.Allowed:
		writeJSON(w, http.StatusOK, res)
	case err != nil:
		writeStoreError(w, err)
	default:
		writeJSON(w, refusalStatus(res), res)
	}
}

// recordAdmittedUnestablished records, at ERROR, an admission that could not be
// established and was admitted with no hold under the allow posture: the tenant, the
// scope, the posture, the outcome and the class of the failure. The failure's own text
// is not recorded: it can name the store's host, user and database.
func (m *Module) recordAdmittedUnestablished(tenant model.TenantID, scope string, cause error) {
	if m.log == nil {
		return
	}
	m.log.Error("finops admission: admitted without a hold under the allow posture",
		"tenant", tenant.String(), "scope", scope, "posture", string(UnreachableAllow), "outcome", "admitted",
		"failure_class", auditFailureClass(cause))
}

// refusalStatus is the status of a reserve that was not admitted: 402 for a block verdict
// (a cap or a spend limit with no headroom, a budget set too large to evaluate, an
// activation frontier or one that cannot be read), 429 for a cap that throttles, 503 for
// an admission that could not be established, and 500 for a key whose admission row fails
// its integrity check.
func refusalStatus(res Reservation) int {
	switch {
	case res.Reason == ReasonStoreUnreachable:
		return http.StatusServiceUnavailable
	case res.Reason == ReasonAdmissionIntegrity:
		return http.StatusInternalServerError
	case res.Action == "throttle":
		return http.StatusTooManyRequests
	default:
		return http.StatusPaymentRequired
	}
}

// handleAdmissionCommit settles a hold with the cost the effect actually incurred,
// returning whatever headroom the estimate held beyond it. Ingest the measured spend
// first, so the budget ceiling never under-counts during settlement. A commit that
// arrives after a release, an expiry or a takeover still records the cost, and a repeat
// with the same amount is answered as the first one was.
func (m *Module) handleAdmissionCommit(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in admissionCommitBody
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := m.Commit(r.Context(), mc.Tenant, in.Handle, in.ActualMicroUSD); err != nil {
		writeSettlementError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"committed": true, "handle": in.Handle})
}

// handleAdmissionRelease returns an unused hold to its budget when the effect did not
// happen, so an abandoned reservation stops withholding headroom from the next caller. A
// release never undoes a commit, and a repeat is answered as the first one was.
func (m *Module) handleAdmissionRelease(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in admissionReleaseBody
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := m.Release(r.Context(), mc.Tenant, in.Handle); err != nil {
		writeSettlementError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"released": true, "handle": in.Handle})
}

// writeSettlementError answers a Commit or a Release that wrote nothing. Input that cannot
// be acted on is 400. A commit with another amount than the recorded one, a hold whose
// admission is still a claim, and a hold the attempt lifecycle owns are 409, each with its
// own fixed sentence. An admission row that fails its integrity check is 500 with the
// integrity failure's fixed sentence: it is a fault of the stored data, not a conflict,
// and the same call succeeds once the row is repaired. Any other error is the store's.
func writeSettlementError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalidAdmission):
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
	case errors.Is(err, ErrSettlementConflict):
		writeJSON(w, http.StatusConflict, errorBody(ErrSettlementConflict.Error()))
	case errors.Is(err, ErrAdmissionPending):
		writeJSON(w, http.StatusConflict, errorBody(ErrAdmissionPending.Error()))
	case attemptCode(err) == errCodeLifecycleAPIRequired:
		writeJSON(w, http.StatusConflict, errorBody(attemptErr(errCodeLifecycleAPIRequired, nil).Error()))
	case errors.Is(err, ErrAdmissionIntegrity):
		writeJSON(w, http.StatusInternalServerError, errorBody(ErrAdmissionIntegrity.Error()))
	default:
		writeStoreError(w, err)
	}
}

// handleAdmissionReconciliation reports the reservation ledger against its commits and
// releases, and what recovery left outstanding: holds still owed, claims and releases an
// earlier build left, undecided recovery writes, and admission rows that fail their
// integrity check. It only reads: it recovers, sweeps and files nothing.
func (m *Module) handleAdmissionReconciliation(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	out, err := m.InspectReservations(r.Context(), mc.Tenant)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleAdmissionReconcile runs admission recovery, sweeps holds that expired unsettled,
// compares what remains against the commits and releases its callers made, and files a
// posture finding when the ledger drifted. Drift is reported, never silently repaired.
func (m *Module) handleAdmissionReconcile(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	out, err := m.ReconcileReservations(r.Context(), mc.Tenant)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
