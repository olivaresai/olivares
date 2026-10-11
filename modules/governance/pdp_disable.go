// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/store"
)

// handlePdpDisable selects a fixed empty authored Cedar policy; it cannot accept replacement
// source or reactivate history. Managed grants and adopted protection stay in the
// same enforced union. The original policies remain in the immutable history.
func (m *Module) handlePdpDisable(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	engine, ok := normEngine(r.URL.Query().Get("engine"))
	if !ok || engine != surfaceCedar {
		writeJSON(w, http.StatusBadRequest, errorBody("engine must be cedar"))
		return
	}
	var revision int64
	var committed scopedTenantState
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		locked, err := pinPolicyAuthorizationEpochWitness(r.Context(), sc, nil)
		if err != nil {
			return err
		}
		inputs, err := readCedarMutationInputs(r.Context(), sc)
		if err != nil {
			return err
		}
		if err := compileProspectiveCedar("", inputs); err != nil {
			return err
		}
		sampled, err := sampleLocalPolicyFreshness(r.Context(), sc, inputs.freshness, inputs.signed)
		if err != nil {
			return err
		}
		generation, err := advancePolicyAuthorizationEpochFrom(r.Context(), sc, locked)
		if err != nil {
			return err
		}
		num, id, err := appendRevision(r.Context(), sc, surfaceCedar, "", mc.Principal.Actor(), true, true, "disabled")
		if err != nil {
			return err
		}
		if _, err := activateRevision(r.Context(), sc, surfaceCedar, num, mc.Principal.Actor()); err != nil {
			return err
		}
		if err := persistSampledLocalPolicyFreshness(r.Context(), sc, sampled, inputs.signed); err != nil {
			return err
		}
		revision = num
		committed = m.cedarExpectedState("", num, inputs, generation, sampled)
		return auditEvent(r.Context(), sc, mc, "governance.pdp.disable", revisionKind, id, map[string]any{"engine": surfaceCedar, "revision": num, "authorization_epoch": generation.Version})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if err := m.reloadGrants(r.Context(), mc.Tenant); err != nil && m.log != nil {
		m.log.Warn("pdp: post-disable Cedar reload failed", "tenant", mc.Tenant.String(), "revision", revision, "err", err)
	}
	outcome, err := m.confirmCommittedCedarLive(r.Context(), mc.Tenant, committed)
	result := pdpPublishResult{Engine: surfaceCedar, Revision: revision, Active: true, LiveActivation: liveDeferred, Note: deferredActivationNote}
	if err == nil && outcome == cedarLiveApplied {
		result.LiveActivation = liveApplied
		result.Note = "authored policy disabled in this process; immutable history and other enforced surfaces are preserved"
	} else {
		m.auditActivationDeferred(r.Context(), mc, surfaceCedar, revision, deferredCedarLiveState(outcome, err), err)
	}
	writeJSON(w, http.StatusOK, result)
}
