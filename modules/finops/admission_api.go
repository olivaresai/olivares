// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"errors"
	"net/http"

	"github.com/olivaresai/olivares/core/api"
)

type admissionReserveBody struct {
	Scope            string    `json:"scope"`
	Dims             SpendDims `json:"dims"`
	ActorRef         string    `json:"actor_ref,omitempty"`
	Groups           []string  `json:"groups,omitempty"`
	EstimateMicroUSD int64     `json:"estimate_micro_usd"`
	IdempotencyKey   string    `json:"idempotency_key"`
	Unreachable      string    `json:"unreachable,omitempty"`
}

type admissionCommitBody struct {
	Handle         string `json:"handle"`
	ActualMicroUSD int64  `json:"actual_micro_usd"`
	SpendHandle    string `json:"spend_handle,omitempty"`
}

type admissionReleaseBody struct {
	Handle      string `json:"handle"`
	SpendHandle string `json:"spend_handle,omitempty"`
}

// handleAdmissionReserve holds estimated spend against every enforcing budget that
// scopes the request, and against the actor's own spend limit when one is named. The
// idempotency key makes a retry replay the original reservation instead of taking a
// second hold, and a store that cannot be read refuses rather than admits.
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
	if errors.Is(err, ErrInvalidAdmission) {
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}
	if errors.Is(err, ErrAdmissionConflict) {
		writeJSON(w, http.StatusConflict, errorBody(err.Error()))
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	status := http.StatusOK
	if !res.Allowed {
		status = http.StatusPaymentRequired
		if res.Reason == ReasonStoreUnreachable {
			status = http.StatusServiceUnavailable
		} else if res.Action == "throttle" {
			status = http.StatusTooManyRequests
		}
	}
	writeJSON(w, status, res)
}

// handleAdmissionCommit settles a reservation with the cost the effect actually
// incurred, returning whatever headroom the estimate over-reserved. Ingest the
// measured spend first, so the budget ceiling never under-counts during settlement.
func (m *Module) handleAdmissionCommit(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in admissionCommitBody
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := m.Commit(r.Context(), mc.Tenant, in.Handle, in.ActualMicroUSD); err != nil {
		if errors.Is(err, ErrInvalidAdmission) {
			writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
			return
		}
		writeStoreError(w, err)
		return
	}
	if in.SpendHandle != "" && in.SpendHandle != in.Handle {
		if err := m.Commit(r.Context(), mc.Tenant, in.SpendHandle, in.ActualMicroUSD); err != nil {
			writeStoreError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"committed": true, "handle": in.Handle})
}

// handleAdmissionRelease returns an unused hold to its budget when the effect did not
// happen, so an abandoned reservation stops withholding headroom from the next caller.
func (m *Module) handleAdmissionRelease(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in admissionReleaseBody
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := m.Release(r.Context(), mc.Tenant, in.Handle); err != nil {
		writeStoreError(w, err)
		return
	}
	if in.SpendHandle != "" && in.SpendHandle != in.Handle {
		if err := m.Release(r.Context(), mc.Tenant, in.SpendHandle); err != nil {
			writeStoreError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"released": true, "handle": in.Handle})
}

// handleAdmissionReconciliation reports the reservation ledger against its commits and
// releases: how many holds are active, settled or lapsed, and whether the ledger drifted
// from what its callers settled. It only reads — it sweeps nothing and files no finding,
// which is the work POST /admission/reconcile does.
func (m *Module) handleAdmissionReconciliation(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	out, err := m.InspectReservations(r.Context(), mc.Tenant)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleAdmissionReconcile sweeps holds that expired unsettled, compares what remains
// against the commits and releases its callers made, and emits a posture finding when
// the ledger drifted. Drift is reported, never silently repaired.
func (m *Module) handleAdmissionReconcile(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	out, err := m.ReconcileReservations(r.Context(), mc.Tenant)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
