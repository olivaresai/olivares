// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// decodeOptionalJSON decodes a request body into v but treats an EMPTY body as a
// no-op (all fields optional) — the rotate/offboard/finalize bodies carry only an
// optional reason/target, so a bare POST is valid. Unknown fields are still
// rejected. Returns false (and writes a 400) on a malformed non-empty body.
func decodeOptionalJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return true
		}
		writeJSON(w, http.StatusBadRequest, errorBody("invalid JSON body"))
		return false
	}
	return true
}

// --- read endpoints ----------------------------------------------------------

// handleListNHI lists the NHI lifecycle rows, optionally filtered by enforcement
// state or offboard state. Read-tier and self-audited (the lifecycle posture is
// recon-relevant, docs/SECURITY-HARDENING.md).
func (m *Module) handleListNHI(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if v := strings.TrimSpace(r.URL.Query().Get("enforcement")); v != "" {
		q.Filters = append(q.Filters, eq(colNHIEnforce, v))
	}
	if v := strings.TrimSpace(r.URL.Query().Get("offboard_state")); v != "" {
		q.Filters = append(q.Filters, eq(colNHIOffboard, v))
	}
	out := listResponse[nhiDTO]{Items: []nhiDTO{}}
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		if err := auditEvent(r.Context(), sc, mc, "governance.nhi.list", nhiLifecycleKind, "", nil); err != nil {
			return err
		}
		repo, err := sc.Ext(nhiLifecycleKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toNHIDTO(rec))
		}
		out.Cursor, out.HasMore = page.Cursor, page.HasMore
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleGetNHI returns one NHI lifecycle row by identity_ref.
func (m *Module) handleGetNHI(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	ref := strings.TrimSpace(chi.URLParam(r, "ref"))
	if ref == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid identity ref"))
		return
	}
	var (
		out   nhiDTO
		found bool
	)
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(nhiLifecycleKind)
		if err != nil {
			return err
		}
		rec, ok, err := findOne(r.Context(), repo, eq(colNHIIdentityRef, ref))
		if err != nil {
			return err
		}
		found, out = ok, toNHIDTO(rec)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// nhiEventDTO is one lifecycle event in the append-only trail.
type nhiEventDTO struct {
	IdentityRef string `json:"identity_ref"`
	Event       string `json:"event"`
	Actor       string `json:"actor"`
	Detail      string `json:"detail,omitempty"`
	OccurredAt  string `json:"occurred_at"`
}

// handleListNHIEvents lists the append-only lifecycle event trail for one NHI.
func (m *Module) handleListNHIEvents(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	ref := strings.TrimSpace(chi.URLParam(r, "ref"))
	if ref == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid identity ref"))
		return
	}
	out := listResponse[nhiEventDTO]{Items: []nhiEventDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(nhiEventKind)
		if err != nil {
			return err
		}
		recs, err := listAll(r.Context(), repo, eq(colNHIEvtIdentity, ref))
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, nhiEventDTO{
				IdentityRef: rec.String(colNHIEvtIdentity), Event: rec.String(colNHIEvtKind),
				Actor: rec.String(colNHIEvtActor), Detail: rec.String(colNHIEvtDetail),
				OccurredAt: rec.String(colNHIEvtAt),
			})
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// --- ownership / policy ------------------------------------------------------

// ownershipRequest assigns the accountable human owner + sponsor for an NHI (the
// Entra Agent ID-shaped mandatory ownership). Both reference a roster HUMAN
// identity by its external_id so orphan detection can WATCH them.
type ownershipRequest struct {
	OwnerRef   string `json:"owner_ref"`
	SponsorRef string `json:"sponsor_ref"`
}

// handleSetNHIOwnership assigns owner/sponsor. Each reference, when non-empty, must
// resolve to a roster identity classified HUMAN (never an NHI/unknown — owner and
// sponsor are accountable people). Write-tier, self-audited.
func (m *Module) handleSetNHIOwnership(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	ref := strings.TrimSpace(chi.URLParam(r, "ref"))
	if ref == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid identity ref"))
		return
	}
	var in ownershipRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	in.OwnerRef = strings.TrimSpace(in.OwnerRef)
	in.SponsorRef = strings.TrimSpace(in.SponsorRef)
	if in.OwnerRef == "" && in.SponsorRef == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("at least one of owner_ref, sponsor_ref is required"))
		return
	}
	var clientErr string
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		for _, ref := range []string{in.OwnerRef, in.SponsorRef} {
			if ref == "" {
				continue
			}
			ok, human, _, err := resolveHumanIdentity(r.Context(), sc, ref)
			if err != nil {
				return err
			}
			if !ok {
				clientErr = "owner/sponsor identity " + ref + " is not in the roster; sync it first"
				return nil
			}
			if !human {
				clientErr = "owner/sponsor identity " + ref + " is not a human identity (owner and sponsor must be accountable people)"
				return nil
			}
		}
		repo, rec, err := foLifecycle(r.Context(), sc, ref)
		if err != nil {
			return err
		}
		// an agent identity must ALWAYS have a sponsor (deny-closed). Clearing
		// the sponsor_ref on a kind=agent row is rejected. The caller can only change
		// the sponsor to another human (handled by the validation loop above), not
		// clear it entirely.
		if in.SponsorRef == "" && rec.String(colNHIKind) == NHIKindAgent {
			clientErr = ErrAgentRequiresSponsor.Error()
			return nil
		}
		actor := mc.Principal.Actor()
		if in.OwnerRef != "" {
			rec[colNHIOwnerRef] = in.OwnerRef
			rec[colNHIOwnerActor] = actor
		}
		if in.SponsorRef != "" {
			rec[colNHISponsorRef] = in.SponsorRef
			rec[colNHISponsorActor] = actor
		}
		// A freshly (re)sponsored NHI is no longer orphaned until the next sweep proves otherwise.
		rec[colNHIOrphaned] = false
		if _, err := repo.Update(r.Context(), rec); err != nil {
			return err
		}
		// when the sponsor changes on an agent identity, emit the more-specific
		// evtAgentSponsorChanged event so the trail clearly records the transfer of
		// accountability (mover scenario). Generic "assigned" is used for non-agents
		// and for owner-only changes.
		evtName := "assigned"
		if rec.String(colNHIKind) == NHIKindAgent && in.SponsorRef != "" {
			evtName = evtAgentSponsorChanged
		}
		if err := m.recordLifecycleEvent(r.Context(), sc, ref, evtName, actor, mc.Principal.UserID.String(),
			"owner="+in.OwnerRef+" sponsor="+in.SponsorRef); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "governance.nhi.ownership", nhiLifecycleKind, "", map[string]any{
			"identity_ref": ref, "owner_set": in.OwnerRef != "", "sponsor_set": in.SponsorRef != "",
		})
	})
	if clientErr != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(clientErr))
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

// policyRequestNHI sets a per-NHI rotation policy: its blast-radius criticality, the
// rotation window, the actuation target, and (since the roster drops created_at) the
// operator-seeded last-known rotation instant.
type policyRequestNHI struct {
	Criticality    string `json:"criticality,omitempty"`     // low|medium|high|critical
	MaxAgeSeconds  int64  `json:"max_age_seconds,omitempty"` // 0 = inherit the criticality default
	RotationTarget string `json:"rotation_target,omitempty"` // actuator target, e.g. "approle:ci-deployer"
	RotatedAt      string `json:"rotated_at,omitempty"`      // RFC3339 last-known rotation, canonicalized
}

// handleSetNHIPolicy authors the per-NHI rotation policy. Write-tier, self-audited.
func (m *Module) handleSetNHIPolicy(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	ref := strings.TrimSpace(chi.URLParam(r, "ref"))
	if ref == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid identity ref"))
		return
	}
	var in policyRequestNHI
	if !decodeJSON(w, r, &in) {
		return
	}
	in.Criticality = strings.TrimSpace(strings.ToLower(in.Criticality))
	if in.Criticality != "" && !validRiskTier(in.Criticality) {
		writeJSON(w, http.StatusBadRequest, errorBody("criticality must be one of low, medium, high, critical"))
		return
	}
	if in.MaxAgeSeconds < 0 || in.MaxAgeSeconds > maxSeconds {
		writeJSON(w, http.StatusBadRequest, errorBody("max_age_seconds out of range"))
		return
	}
	in.RotationTarget = strings.TrimSpace(in.RotationTarget)
	if len(in.RotationTarget) > maxMatchLen || containsInlineCredential(in.RotationTarget) {
		writeJSON(w, http.StatusBadRequest, errorBody("rotation_target invalid"))
		return
	}
	var rotatedAt model.Timestamp
	if s := strings.TrimSpace(in.RotatedAt); s != "" {
		ts, ok := parseFlexibleTimestamp(s)
		if !ok {
			writeJSON(w, http.StatusBadRequest, errorBody("rotated_at must be an RFC3339 timestamp"))
			return
		}
		rotatedAt = ts
	}
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, rec, err := foLifecycle(r.Context(), sc, ref)
		if err != nil {
			return err
		}
		if in.Criticality != "" {
			rec[colNHICriticality] = in.Criticality
		}
		if in.MaxAgeSeconds > 0 {
			rec[colNHIMaxAgeSec] = in.MaxAgeSeconds
		}
		if in.RotationTarget != "" {
			rec[colNHITargetRef] = in.RotationTarget
		}
		if !rotatedAt.IsZero() {
			rec[colNHIRotatedAt] = rotatedAt.String()
		}
		if _, err := repo.Update(r.Context(), rec); err != nil {
			return err
		}
		if err := m.recordLifecycleEvent(r.Context(), sc, ref, "policy", mc.Principal.Actor(), mc.Principal.UserID.String(),
			"criticality="+in.Criticality); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "governance.nhi.policy", nhiLifecycleKind, "", map[string]any{
			"identity_ref": ref, "criticality": in.Criticality, "max_age_seconds": in.MaxAgeSeconds,
		})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

// --- governed actuations -----------------------------------------------------

// nhiActionRequest is the body of a rotate/offboard request.
type nhiActionRequest struct {
	TargetRef string `json:"target_ref,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// handleRotateNHI orchestrates a governed rotation: it opens the CRITICAL approval
// (two-person floor via the engine), and only on an approved/break-glass decision
// invokes the wired actuator, returning the minted credential ONCE. Where no
// actuator/capability exists it degrades honestly (a coverage finding, never a
// fabricated rotation). Admin-tier.
func (m *Module) handleRotateNHI(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	ref := strings.TrimSpace(chi.URLParam(r, "ref"))
	if ref == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid identity ref"))
		return
	}
	var in nhiActionRequest
	if !decodeOptionalJSON(w, r, &in) {
		return
	}

	// 1. Ensure the lifecycle row, read source/target/kind.
	var (
		source string
		target string
		kind   string
	)
	if err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		_, rec, err := foLifecycle(r.Context(), sc, ref)
		if err != nil {
			return err
		}
		source = rec.String(colNHISource)
		target = strings.TrimSpace(in.TargetRef)
		if target == "" {
			target = rec.String(colNHITargetRef)
		}
		if id, ok, e := identityByExternalID(r.Context(), sc, ref); e == nil && ok {
			kind = id.Kind
			if source == "" {
				source = id.Provider
			}
		}
		return nil
	}); err != nil {
		writeStoreError(w, err)
		return
	}

	// 2. Governed gate (CRITICAL → two-person floor; break-glass permitted for an
	//    emergency rotation). Plan-bound to (ref, op, target) anti-TOCTOU.
	planHash := planHashFor(ref, "rotate", target)
	reason := firstNonEmpty(in.Reason, "NHI key/secret rotation")
	dec, err := m.gate().Authorize(r.Context(), mc.Tenant, LifecycleGateRequest{
		Action: actionNHIRotate, SubjectKind: nhiSubjectKind, SubjectRef: ref, PlanHash: planHash,
		Reason: reason, RequestedBy: mc.Principal.Actor(), AllowBreakGlass: true,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody("could not open governed approval"))
		return
	}
	if !dec.Allowed() {
		writeJSON(w, gateHTTPStatus(dec.Status), nhiResultDTO{Status: dec.Status, ApprovalRef: dec.ApprovalRef,
			Detail: "rotation awaiting governed approval"})
		return
	}

	// 3. Honest degrade: no actuator or no rotation capability for this provider.
	act := m.actuatorFor(mc.Tenant, source)
	if act == nil {
		m.degradeRotation(r, mc, ref, source, "no rotation actuator wired for source "+quoteOrDash(source))
		writeJSON(w, http.StatusOK, nhiResultDTO{Status: "unavailable", ApprovalRef: dec.ApprovalRef,
			Detail: "rotation not available for source " + quoteOrDash(source) + "; rotate manually and POST /nhi/" + ref + "/policy with rotated_at"})
		return
	}
	capb, hasCap := identitysource.FindCapability(act.Capabilities(), identitysource.OpRotate, kind)
	if !hasCap {
		m.degradeRotation(r, mc, ref, source, "source "+quoteOrDash(source)+" exposes no rotation API")
		writeJSON(w, http.StatusOK, nhiResultDTO{Status: "unavailable", ApprovalRef: dec.ApprovalRef,
			Detail: "source " + quoteOrDash(source) + " exposes no rotation API; rotate manually"})
		return
	}
	if capb.RequiresTargetRef && target == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("rotation requires a target_ref (or a stored rotation_target) for source "+quoteOrDash(source)))
		return
	}

	// 4. Actuate OUTSIDE any store transaction (a network call).
	rotated, aerr := act.Rotate(r.Context(), identitysource.ActuationRequest{Ref: ref, Kind: kind, TargetRef: target})
	if aerr != nil {
		m.persistActuationFailure(r, mc, ref, "rotate", aerr.Error())
		writeJSON(w, http.StatusBadGateway, errorBody("rotation actuation failed"))
		return
	}

	// 5. Persist the result + event + audit (the SECRET is never stored).
	if err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, rec, err := foLifecycle(r.Context(), sc, ref)
		if err != nil {
			return err
		}
		rec[colNHIRotatedAt] = m.clock.Now().String()
		rec[colNHIStaleStatus] = staleOK
		rec[colNHIStaleSince] = nil
		rec[colNHIBlockAfter] = nil
		// Rotation clears a staleness-driven block (never an offboard block).
		if rec.String(colNHIEnforce) != enforceMonitor && rec.String(colNHIOffboard) == offboardNone {
			rec[colNHIEnforce] = enforceMonitor
			rec[colNHIEnforceWhy] = nil
		}
		if _, err := repo.Update(r.Context(), rec); err != nil {
			return err
		}
		detail := capb.Detail
		if rotated.Receipt.NewCredentialRef != "" {
			detail += " new_ref=" + rotated.Receipt.NewCredentialRef
		}
		if err := m.recordLifecycleEvent(r.Context(), sc, ref, "rotated", mc.Principal.Actor(), mc.Principal.UserID.String(), detail); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "governance.nhi.rotate", nhiLifecycleKind, "", map[string]any{
			"identity_ref": ref, "source": source, "new_credential_ref": rotated.Receipt.NewCredentialRef,
			"break_glass": dec.Status == GateStatusBreakGlass,
		})
	}); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, nhiResultDTO{
		Status: "done", ApprovalRef: dec.ApprovalRef, Detail: capb.Detail,
		NewSecret: rotated.Secret, NewCredentialRef: rotated.Receipt.NewCredentialRef,
	})
}

// handleOffboardNHI runs the reversible soft-delete step of a governed offboarding:
// it blocks the NHI in-product (enforcement=blocked, the cascade that denies every
// bound agent at the PEP), opens an audited recovery window, and best-effort disables
// the credential at the source. Governed (HIGH), break-glass permitted. Admin-tier.
func (m *Module) handleOffboardNHI(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	ref := strings.TrimSpace(chi.URLParam(r, "ref"))
	if ref == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid identity ref"))
		return
	}
	var in nhiActionRequest
	if !decodeOptionalJSON(w, r, &in) {
		return
	}
	var source, kind string
	if err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		_, rec, err := foLifecycle(r.Context(), sc, ref)
		if err != nil {
			return err
		}
		source = rec.String(colNHISource)
		if id, ok, e := identityByExternalID(r.Context(), sc, ref); e == nil && ok {
			kind = id.Kind
			if source == "" {
				source = id.Provider
			}
		}
		return nil
	}); err != nil {
		writeStoreError(w, err)
		return
	}

	planHash := planHashFor(ref, "offboard", "")
	dec, err := m.gate().Authorize(r.Context(), mc.Tenant, LifecycleGateRequest{
		Action: actionNHIOffboard, SubjectKind: nhiSubjectKind, SubjectRef: ref, PlanHash: planHash,
		Reason: firstNonEmpty(in.Reason, "NHI offboarding — soft-delete"), RequestedBy: mc.Principal.Actor(), AllowBreakGlass: true,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody("could not open governed approval"))
		return
	}
	if !dec.Allowed() {
		writeJSON(w, gateHTTPStatus(dec.Status), nhiResultDTO{Status: dec.Status, ApprovalRef: dec.ApprovalRef,
			Detail: "offboarding awaiting governed approval"})
		return
	}

	// Best-effort source disable (the in-product block below always applies).
	sourceDetail := "source disable unavailable; blocked in-product"
	if act := m.actuatorFor(mc.Tenant, source); act != nil {
		if _, ok := identitysource.FindCapability(act.Capabilities(), identitysource.OpDisable, kind); ok {
			if rec, derr := act.Disable(r.Context(), identitysource.ActuationRequest{Ref: ref, Kind: kind}); derr == nil {
				sourceDetail = rec.Detail
			} else {
				sourceDetail = "source disable failed; blocked in-product"
			}
		}
	}

	var cascade int
	if err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, rec, err := foLifecycle(r.Context(), sc, ref)
		if err != nil {
			return err
		}
		now := m.clock.Now()
		recoverUntil := model.NewTimestamp(now.Time().Add(recoveryWindow))
		rec[colNHIOffboard] = offboardSoft
		rec[colNHISoftAt] = now.String()
		rec[colNHIRecoverUntil] = recoverUntil.String()
		rec[colNHIEnforce] = enforceBlocked
		rec[colNHIEnforceWhy] = "offboarded (soft-delete)"
		if _, err := repo.Update(r.Context(), rec); err != nil {
			return err
		}
		// Cascade: every agent bound to this NHI is now blocked at the PEP (they
		// resolve to the blocked identity). Count them for the cascade finding.
		if id, ok, e := identityByExternalID(r.Context(), sc, ref); e == nil && ok {
			if n, e := countAgentsForIdentity(r.Context(), sc, id.ID); e == nil {
				cascade = n
			}
		}
		if err := m.recordLifecycleEvent(r.Context(), sc, ref, "offboard_soft", mc.Principal.Actor(), mc.Principal.UserID.String(), sourceDetail); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "governance.nhi.offboard", nhiLifecycleKind, "", map[string]any{
			"identity_ref": ref, "source": source, "cascade_agents": cascade, "break_glass": dec.Status == GateStatusBreakGlass,
		})
	}); err != nil {
		writeStoreError(w, err)
		return
	}
	m.emitNHIFinding(r.Context(), mc.Tenant, "nhi_offboard_soft", ref, sdkmodel.SeverityHigh,
		"NHI offboarded (soft-delete) — blocked; "+itoa(cascade)+" bound agents affected; recovery window open")
	writeJSON(w, http.StatusOK, nhiResultDTO{Status: "done", ApprovalRef: dec.ApprovalRef,
		Detail: "soft-deleted; " + sourceDetail + "; recovery window open"})
}

// handleFinalizeNHI is the irreversible end of the offboarding cascade: a CRITICAL,
// NO-break-glass approval (the erase-gate precedent — no emergency justifies skipping
// the second human on an irreversible revocation), then a best-effort definitive
// revoke at the source. Requires a prior soft-delete. Admin-tier.
func (m *Module) handleFinalizeNHI(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	ref := strings.TrimSpace(chi.URLParam(r, "ref"))
	if ref == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid identity ref"))
		return
	}
	var in nhiActionRequest
	if !decodeOptionalJSON(w, r, &in) {
		return
	}
	var (
		source, kind, offboard string
	)
	if err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(nhiLifecycleKind)
		if err != nil {
			return err
		}
		rec, ok, err := findOne(r.Context(), repo, eq(colNHIIdentityRef, ref))
		if err != nil {
			return err
		}
		if ok { // a missing row means offboard stays "" → the 409 guard below fires
			source = rec.String(colNHISource)
			offboard = rec.String(colNHIOffboard)
		}
		if id, ok, e := identityByExternalID(r.Context(), sc, ref); e == nil && ok {
			kind = id.Kind
		}
		return nil
	}); err != nil {
		writeStoreError(w, err)
		return
	}
	if offboard != offboardSoft {
		writeJSON(w, http.StatusConflict, errorBody("finalize requires a prior soft-delete (POST /nhi/{ref}/offboard first)"))
		return
	}

	planHash := planHashFor(ref, "offboard.finalize", "")
	dec, err := m.gate().Authorize(r.Context(), mc.Tenant, LifecycleGateRequest{
		Action: actionNHIOffboardFinal, SubjectKind: nhiSubjectKind, SubjectRef: ref, PlanHash: planHash,
		Reason: firstNonEmpty(in.Reason, "NHI offboarding — irreversible finalize"), RequestedBy: mc.Principal.Actor(), AllowBreakGlass: false,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody("could not open governed approval"))
		return
	}
	if !dec.Allowed() {
		writeJSON(w, gateHTTPStatus(dec.Status), nhiResultDTO{Status: dec.Status, ApprovalRef: dec.ApprovalRef,
			Detail: "finalize awaiting governed dual-control approval"})
		return
	}

	sourceDetail := "source finalize unavailable; stays blocked in-product"
	if act := m.actuatorFor(mc.Tenant, source); act != nil {
		if _, ok := identitysource.FindCapability(act.Capabilities(), identitysource.OpFinalize, kind); ok {
			if rec, ferr := act.Finalize(r.Context(), identitysource.ActuationRequest{Ref: ref, Kind: kind}); ferr == nil {
				sourceDetail = rec.Detail
			} else {
				sourceDetail = "source finalize failed; stays blocked in-product"
			}
		}
	}

	if err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, rec, err := foLifecycle(r.Context(), sc, ref)
		if err != nil {
			return err
		}
		rec[colNHIOffboard] = offboardFinal
		rec[colNHIEnforce] = enforceBlocked
		rec[colNHIEnforceWhy] = "offboarded (finalized)"
		rec[colNHIRecoverUntil] = nil
		if _, err := repo.Update(r.Context(), rec); err != nil {
			return err
		}
		if err := m.recordLifecycleEvent(r.Context(), sc, ref, "offboard_finalize", mc.Principal.Actor(), mc.Principal.UserID.String(), sourceDetail); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "governance.nhi.offboard.finalize", nhiLifecycleKind, "", map[string]any{
			"identity_ref": ref, "source": source,
		})
	}); err != nil {
		writeStoreError(w, err)
		return
	}
	m.emitNHIFinding(r.Context(), mc.Tenant, "nhi_offboard_finalized", ref, sdkmodel.SeverityHigh,
		"NHI offboarding finalized — credential definitively revoked")
	writeJSON(w, http.StatusOK, nhiResultDTO{Status: "done", ApprovalRef: dec.ApprovalRef, Detail: sourceDetail})
}

// handleRestoreNHI reverses a soft-delete within the recovery window: best-effort
// re-enable at the source, clear the in-product block. Admin-tier, self-audited (no
// dual-control — reversing one's own quarantine within an audited window). A finalized
// offboarding cannot be restored.
func (m *Module) handleRestoreNHI(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	ref := strings.TrimSpace(chi.URLParam(r, "ref"))
	if ref == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid identity ref"))
		return
	}
	var source, kind, offboard string
	if err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(nhiLifecycleKind)
		if err != nil {
			return err
		}
		rec, ok, err := findOne(r.Context(), repo, eq(colNHIIdentityRef, ref))
		if err != nil {
			return err
		}
		if ok { // a missing row falls through to the "nothing to restore" 409 below
			source, offboard = rec.String(colNHISource), rec.String(colNHIOffboard)
		}
		if id, ok, e := identityByExternalID(r.Context(), sc, ref); e == nil && ok {
			kind = id.Kind
		}
		return nil
	}); err != nil {
		writeStoreError(w, err)
		return
	}
	if offboard == offboardFinal {
		writeJSON(w, http.StatusConflict, errorBody("a finalized offboarding is irreversible and cannot be restored"))
		return
	}
	if offboard != offboardSoft {
		writeJSON(w, http.StatusConflict, errorBody("nothing to restore (not soft-deleted)"))
		return
	}

	sourceDetail := "source restore unavailable"
	if act := m.actuatorFor(mc.Tenant, source); act != nil {
		if _, ok := identitysource.FindCapability(act.Capabilities(), identitysource.OpRestore, kind); ok {
			if rec, rerr := act.Restore(r.Context(), identitysource.ActuationRequest{Ref: ref, Kind: kind}); rerr == nil {
				sourceDetail = rec.Detail
			} else {
				sourceDetail = "source restore failed"
			}
		}
	}

	if err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, rec, err := foLifecycle(r.Context(), sc, ref)
		if err != nil {
			return err
		}
		rec[colNHIOffboard] = offboardNone
		rec[colNHISoftAt] = nil
		rec[colNHIRecoverUntil] = nil
		rec[colNHIEnforce] = enforceMonitor
		rec[colNHIEnforceWhy] = nil
		if _, err := repo.Update(r.Context(), rec); err != nil {
			return err
		}
		if err := m.recordLifecycleEvent(r.Context(), sc, ref, "restored", mc.Principal.Actor(), mc.Principal.UserID.String(), sourceDetail); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "governance.nhi.restore", nhiLifecycleKind, "", map[string]any{"identity_ref": ref})
	}); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, nhiResultDTO{Status: "done", Detail: "restored; " + sourceDetail})
}

// --- shared helpers ----------------------------------------------------------

// recoveryWindow is the audited soft-delete recovery window before a finalize is
// recommended (Entra-shaped offboarding). The block is in force throughout; the
// window governs the restore-vs-finalize decision.
const recoveryWindow = 7 * 24 * time.Hour

// resolveHumanIdentity reports whether ref resolves to a roster identity, whether it
// is classified HUMAN, and whether it is disabled.
func resolveHumanIdentity(ctx context.Context, sc store.Scope, ref string) (found, human, disabled bool, err error) {
	id, ok, err := identityByExternalID(ctx, sc, ref)
	if err != nil || !ok {
		return false, false, false, err
	}
	pt, _ := id.Metadata["principal_type"].(string)
	dis, _ := id.Metadata["disabled"].(bool)
	return true, pt == string(identitysource.PrincipalHuman), dis, nil
}

// degradeRotation records the honest-degrade outcome: a coverage finding + a lifecycle
// event, never a fabricated rotation.
func (m *Module) degradeRotation(r *http.Request, mc api.ModuleContext, ref, source, why string) {
	_ = mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		return m.recordLifecycleEvent(r.Context(), sc, ref, "rotate_unavailable", mc.Principal.Actor(), mc.Principal.UserID.String(), why)
	})
	m.emitNHIFinding(r.Context(), mc.Tenant, "nhi_rotation_unavailable", ref, sdkmodel.SeverityMedium,
		"NHI rotation cannot be orchestrated automatically — "+why)
}

// persistActuationFailure records an actuation failure in the trail (non-secret).
func (m *Module) persistActuationFailure(r *http.Request, mc api.ModuleContext, ref, op, detail string) {
	if len(detail) > maxMatchLen*2 {
		detail = detail[:maxMatchLen*2]
	}
	_ = mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		return m.recordLifecycleEvent(r.Context(), sc, ref, op+"_failed", mc.Principal.Actor(), mc.Principal.UserID.String(), detail)
	})
}
