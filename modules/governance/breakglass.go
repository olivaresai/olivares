// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// break-glass (emergency access), NIST AC-3(2) / OWASP AI-agent
// dual-control's escape valve. When the two-person flow cannot complete in time
// (an incident at 03:00 with one approver reachable), an ADMIN — a real human,
// never a system token — activates a time-boxed emergency grant. While the grant
// is effectively active, the bridge lets a gated action proceed WITHOUT its
// approval quorum, but the path is never silent:
//
//   - ACTIVATION is admin-tier, requires a justification, is self-audited to the
//     hash-chain ledger in the same transaction, and emits a CRITICAL finding to
//     the notification rail after commit.
//   - EVERY USE appends an immutable row to the breakglass_use trail and a
//     ledger event naming the grant, the action and the subject — an action that
//     proceeded under break-glass is permanently distinguishable from an
//     approved one.
//   - TIME-BOX: the grant carries a mandatory expiry (default 1h, hard cap 24h)
//     derived lazily like approval expiry — an expired grant can never authorize
//     a use, sweep or no sweep.
//   - FORCED POST-REVIEW: a new grant cannot be activated while ANY prior grant
//     remains unreviewed (active ones included — close them first), and the
//     review must come from a DIFFERENT human than the activator (separation of
//     duties). The review is itself audited and emitted.
//
// The engine answers only "is there an active grant covering this action, and
// record that it was used"; the policy that break-glass never overrides an
// explicit human REJECTION lives at the consumer (the bridge applies it to
// pending/expired approvals only).

// Break-glass lifecycle states. active is the only non-terminal state.
const (
	bgStatusActive  = "active"
	bgStatusRevoked = "revoked"
	bgStatusExpired = "expired"
)

// Time-box bounds: a forgotten emergency window lapses on its own. The cap is a
// day — an "emergency" that needs longer is an operating mode, not an emergency,
// and must go through the normal dual-control flow.
const (
	defaultBreakGlassSeconds = int64(3600)      // 1 hour
	maxBreakGlassSeconds     = int64(24 * 3600) // hard cap, 24 hours
)

// maxConsumeRetries bounds the optimistic retry of the use-count increment when
// concurrent consumes race on the grant row's version.
const maxConsumeRetries = 6

// maxBreakGlassTxnRetries bounds retries of the COMPLETE activation/review
// transaction. A session or grant CAS conflict must replay every dependent
// write; retrying only one repository update would split the atomic protocol.
const maxBreakGlassTxnRetries = 6

// bgActiveGuard is the constant the nullable active_guard sentinel holds while a
// grant blocks new activations (active or terminal-but-unreviewed). The unique
// (tenant_id, active_guard) index then permits at most one such grant per tenant;
// a reviewed grant clears it to NULL (schema.go colBGActiveGuard).
const bgActiveGuard = "unreviewed"

// Finding kinds emitted by the break-glass path (delivered to outputs).
const (
	findingBreakGlassActivated = "governance_breakglass_activated"
	findingBreakGlassUsed      = "governance_breakglass_used"
	findingBreakGlassRevoked   = "governance_breakglass_revoked"
	findingBreakGlassExpired   = "governance_breakglass_expired"
	findingBreakGlassReviewed  = "governance_breakglass_reviewed"
)

// activateBreakGlassRequest opens an emergency window. MatchAction scopes it:
// empty covers every governed action, an exact action covers one, a trailing-*
// covers a prefix family (e.g. "deploy.*").
type activateBreakGlassRequest struct {
	MatchAction      string `json:"match_action,omitempty"`
	Reason           string `json:"reason"`
	ExpiresInSeconds int64  `json:"expires_in_seconds,omitempty"`
}

// consumeBreakGlassRequest asks "may this action proceed under an active
// emergency grant?" — and, when yes, RECORDS the use (trail + ledger) in the
// same transaction. The subject fields are evidence, mirroring an approval's.
type consumeBreakGlassRequest struct {
	Action      string `json:"action"`
	SubjectKind string `json:"subject_kind,omitempty"`
	SubjectRef  string `json:"subject_ref,omitempty"`
}

// breakGlassDTO is the grant view. Status is the EFFECTIVE status (expiry
// derived at read), mirroring approvalDTO.
type breakGlassDTO struct {
	ID          string `json:"id"`
	MatchAction string `json:"match_action,omitempty"`
	Reason      string `json:"reason,omitempty"`
	ActivatedBy string `json:"activated_by,omitempty"`
	Status      string `json:"status"`
	ActivatedAt string `json:"activated_at,omitempty"`
	ExpiresAt   string `json:"expires_at,omitempty"`
	RevokedAt   string `json:"revoked_at,omitempty"`
	UseCount    int64  `json:"use_count"`
	Reviewed    bool   `json:"reviewed"`
	ReviewedBy  string `json:"reviewed_by,omitempty"`
	ReviewedAt  string `json:"reviewed_at,omitempty"`
	ReviewNote  string `json:"review_note,omitempty"`
}

func toBreakGlassDTO(rec model.Record, now model.Timestamp) breakGlassDTO {
	return breakGlassDTO{
		ID: rec.String(model.ColID), MatchAction: rec.String(colBGMatchAction), Reason: rec.String(colBGReason),
		ActivatedBy: rec.String(colBGActivatedBy), Status: effectiveBreakGlassStatus(rec, now),
		ActivatedAt: rec.String(colBGActivatedAt), ExpiresAt: rec.String(colBGExpiresAt),
		RevokedAt: rec.String(colBGRevokedAt), UseCount: rec.Int(colBGUseCount),
		Reviewed: rec.Bool(colBGReviewed), ReviewedBy: rec.String(colBGReviewedBy),
		ReviewedAt: rec.String(colBGReviewedAt), ReviewNote: rec.String(colBGReviewNote),
	}
}

// effectiveBreakGlassStatus is the authoritative grant status: the stored status
// unless the grant is still active past its expiry, in which case it is
// "expired". Every consume re-derives this, so a lapsed grant can never
// authorize a use even before a sweep materializes the expiry (deny-closed).
func effectiveBreakGlassStatus(rec model.Record, now model.Timestamp) string {
	stored := rec.String(colBGStatus)
	if stored != bgStatusActive {
		return stored
	}
	if exp, ok := tsValue(rec, colBGExpiresAt); ok && !now.Before(exp) {
		return bgStatusExpired
	}
	return bgStatusActive
}

// breakGlassMatches reports whether a grant's action scope covers action:
// "" covers all, a trailing "*" covers the prefix, else exact.
func breakGlassMatches(matchAction, action string) bool {
	switch {
	case matchAction == "" || matchAction == "*":
		return true
	case strings.HasSuffix(matchAction, "*"):
		return strings.HasPrefix(action, strings.TrimSuffix(matchAction, "*"))
	default:
		return matchAction == action
	}
}

// handleActivateBreakGlass opens an emergency window. Admin-tier; a REAL human
// only (a system token has no stable identity for the review SoD to key on);
// justification required; time-boxed; blocked while any prior grant is
// unreviewed (the forced-post-review backpressure: you cannot stack emergencies
// over an unexamined one). Self-audited + CRITICAL finding after commit.
func (m *Module) handleActivateBreakGlass(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in activateBreakGlassRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	if mc.Principal.UserID.IsZero() {
		writeJSON(w, http.StatusForbidden, errorBody("a stable user identity is required to activate break-glass; a system token cannot"))
		return
	}
	// opening an emergency window is the most privileged act on the plane
	// — it demands a session whose hardware step-up (WebAuthn/PIV) is fresh,
	// unconditionally (break-glass bypasses the approval quorum, so its own
	// auth bar is the remaining preventive control). Composes with — never
	// replaces — the identity, justification, recording and post-review checks.
	if mc.Principal.AAL < auth.AAL3 {
		writeJSON(w, http.StatusForbidden, errorBodyCode("step_up_required",
			"break-glass activation requires a hardware-verified (AAL3) session; complete the WebAuthn/PIV step-up and retry"))
		return
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if in.Reason == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("a justification reason is required to activate break-glass"))
		return
	}
	if len(in.Reason) > maxNoteLen {
		writeJSON(w, http.StatusBadRequest, errorBody("reason too long"))
		return
	}
	in.MatchAction = strings.TrimSpace(in.MatchAction)
	if len(in.MatchAction) > maxMatchLen {
		writeJSON(w, http.StatusBadRequest, errorBody("match_action too long"))
		return
	}
	if i := strings.Index(in.MatchAction, "*"); i >= 0 && i != len(in.MatchAction)-1 {
		writeJSON(w, http.StatusBadRequest, errorBody("match_action may only use a trailing * (prefix scope)"))
		return
	}
	if containsInlineCredential(in.MatchAction) {
		writeJSON(w, http.StatusBadRequest, errorBody("match_action must not contain a credential"))
		return
	}
	expiresIn := in.ExpiresInSeconds
	if expiresIn <= 0 {
		expiresIn = defaultBreakGlassSeconds
	}
	if expiresIn > maxBreakGlassSeconds {
		writeJSON(w, http.StatusBadRequest, errorBody("break-glass window exceeds the 24h cap; an emergency longer than a day is not an emergency"))
		return
	}
	// MANDATORY recording precondition (SEC-G5). The API wrapper carries
	// the exact session ID Gate reserved into ModuleContext; never re-resolve by
	// credential here, because a concurrent seal may already have opened a newer
	// session. The scoped capability is required so grant+binding share Mutate.
	recordingGate := m.recordingGateNow()
	atomicRecording, ok := recordingGate.(AtomicRecordingGate)
	recSession := mc.RecordingSession
	if !ok || recSession.IsZero() {
		writeJSON(w, http.StatusPreconditionFailed, errorBodyCode("recording_session_unavailable",
			"break-glass requires the exact actively recorded session reserved by the request gate"))
		return
	}

	now := m.clock.Now()
	var (
		out           breakGlassDTO
		clientErr     string
		clientErrCode string
		clientCode    int
	)
	var err error
	for attempt := 0; attempt < maxBreakGlassTxnRetries; attempt++ {
		out = breakGlassDTO{}
		clientErr, clientErrCode, clientCode = "", "", 0
		err = mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
			repo, err := sc.Ext(breakGlassKind)
			if err != nil {
				return err
			}
			// FORCED POST-REVIEW: any unreviewed grant — active or terminal —
			// blocks a new activation.
			unreviewed, err := listAll(r.Context(), repo, eq(colBGReviewed, false))
			if err != nil {
				return err
			}
			for _, g := range unreviewed {
				if effectiveBreakGlassStatus(g, now) == bgStatusActive {
					clientErr, clientCode, clientErrCode = "an active break-glass grant already exists ("+g.String(model.ColID)+"); revoke it before opening another", http.StatusConflict, "active_grant_exists"
					return nil
				}
				clientErr, clientCode, clientErrCode = "a prior break-glass grant ("+g.String(model.ColID)+") has not been post-reviewed; review it before activating a new one", http.StatusConflict, "unreviewed_grant_blocks"
				return nil
			}
			rec := model.Record{
				colBGMatchAction: in.MatchAction, colBGReason: in.Reason,
				colBGActivatedBy: mc.Principal.Actor(), colBGActivatedByUser: mc.Principal.UserID.String(),
				colBGStatus: bgStatusActive, colBGActivatedAt: now.String(),
				colBGExpiresAt: model.NewTimestamp(now.Time().Add(time.Duration(expiresIn) * time.Second)).String(),
				colBGUseCount:  int64(0), colBGReviewed: false, colBGActiveGuard: bgActiveGuard,
			}
			created, err := repo.Create(r.Context(), rec)
			if err != nil {
				return err
			}
			grantID := model.ID(created.String(model.ColID))
			if err := atomicRecording.BindGrantInScope(r.Context(), sc, recSession, grantID, mc.Principal); err != nil {
				return err
			}
			out = toBreakGlassDTO(created, now)
			return auditEvent(r.Context(), sc, mc, "governance.breakglass.activate", breakGlassKind, grantID, map[string]any{
				"match_action": in.MatchAction, "expires_in_seconds": expiresIn,
				"recording_session": recSession.String(),
			})
		})
		if err == nil || !isConflict(err) {
			break
		}
	}
	if clientErr != "" {
		writeJSON(w, clientCode, errorBodyCode(clientErrCode, clientErr))
		return
	}
	if err != nil {
		if errors.Is(err, api.ErrRecordingSessionPrecondition) {
			writeJSON(w, http.StatusPreconditionFailed, errorBodyCode("recording_session_unavailable",
				"the exact recording session reserved by the request is no longer active and unbound; no break-glass grant was created"))
			return
		}
		// The (tenant_id, active_guard) unique index is the race backstop: a second
		// activation that slipped past the app-level check loses the insert and lands
		// here, reported as the same conflict rather than a 500.
		if isConflict(err) {
			writeJSON(w, http.StatusConflict, errorBody("an active or unreviewed break-glass grant already exists (concurrent activation); revoke and post-review it before opening another"))
			return
		}
		writeStoreError(w, err)
		return
	}
	// Emit AFTER commit (a rolled-back activation never signals). CRITICAL: an
	// open emergency window is the loudest event this module produces.
	m.emitBreakGlassFinding(r.Context(), mc.Tenant, findingBreakGlassActivated, out.ID, out.MatchAction, sdkmodel.SeverityCritical,
		"Break-glass emergency access ACTIVATED — dual-control is bypassed for in-scope actions until expiry; post-review required")
	writeJSON(w, http.StatusCreated, out)
}

// handleListBreakGlass lists grants, optionally filtered by stored status; a
// grant past its expiry reads as "expired" in its DTO regardless.
func (m *Module) handleListBreakGlass(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if v := r.URL.Query().Get("status"); v != "" {
		q.Filters = append(q.Filters, eq(colBGStatus, v))
	}
	now := m.clock.Now()
	out := listResponse[breakGlassDTO]{Items: []breakGlassDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(breakGlassKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toBreakGlassDTO(rec, now))
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

// handleGetBreakGlass returns one grant with its effective status.
func (m *Module) handleGetBreakGlass(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	now := m.clock.Now()
	var (
		out   breakGlassDTO
		found bool
	)
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(breakGlassKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		found, out = true, toBreakGlassDTO(rec, now)
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

// breakGlassUseDTO is one immutable entry of the emergency-use trail.
type breakGlassUseDTO struct {
	GrantID     string `json:"grant_id"`
	Action      string `json:"action"`
	SubjectKind string `json:"subject_kind,omitempty"`
	SubjectRef  string `json:"subject_ref,omitempty"`
	UsedBy      string `json:"used_by,omitempty"`
	UsedAt      string `json:"used_at,omitempty"`
}

// handleListBreakGlassUses returns the immutable use trail for a grant — what
// actually proceeded under the emergency window (the post-review's evidence).
func (m *Module) handleListBreakGlassUses(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	out := listResponse[breakGlassUseDTO]{Items: []breakGlassUseDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(breakGlassUseKind)
		if err != nil {
			return err
		}
		recs, err := listAll(r.Context(), repo, eq(colBGUseGrant, id.String()))
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, breakGlassUseDTO{
				GrantID: rec.String(colBGUseGrant), Action: rec.String(colAction),
				SubjectKind: rec.String(colSubjectKind), SubjectRef: rec.String(colSubjectRef),
				UsedBy: rec.String(colBGUsedBy), UsedAt: rec.String(colBGUsedAt),
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

// handleRevokeBreakGlass closes an active grant early. Admin-tier, self-audited,
// emitted. The grant still demands its post-review (reviewed stays false).
func (m *Module) handleRevokeBreakGlass(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	now := m.clock.Now()
	var (
		out        breakGlassDTO
		clientErr  string
		clientCode int
	)
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(breakGlassKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		if eff := effectiveBreakGlassStatus(rec, now); eff != bgStatusActive {
			clientErr, clientCode = "break-glass grant is "+eff+"; it cannot be revoked", http.StatusConflict
			return nil
		}
		rec[colBGStatus] = bgStatusRevoked
		rec[colBGRevokedAt] = now.String()
		rec, err = repo.Update(r.Context(), rec)
		if err != nil {
			return err
		}
		out = toBreakGlassDTO(rec, now)
		return auditEvent(r.Context(), sc, mc, "governance.breakglass.revoke", breakGlassKind, id, nil)
	})
	if clientErr != "" {
		writeJSON(w, clientCode, errorBody(clientErr))
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	m.emitBreakGlassFinding(r.Context(), mc.Tenant, findingBreakGlassRevoked, out.ID, out.MatchAction, sdkmodel.SeverityMedium,
		"Break-glass grant revoked — emergency window closed; post-review still required")
	writeJSON(w, http.StatusOK, out)
}

// handleReviewBreakGlass records the FORCED post-review of a terminal grant.
// Admin-tier; a real human DIFFERENT from the activator (separation of duties —
// the person who opened the emergency cannot also be the one who signs it off);
// only once; only after the window closed (the review examines what the window
// was used for, so it cannot precede its closure). Self-audited + emitted.
func (m *Module) handleReviewBreakGlass(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var in struct {
		Note string `json:"note"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	in.Note = strings.TrimSpace(in.Note)
	if in.Note == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("a post-review note is required (what happened, was the use justified)"))
		return
	}
	if len(in.Note) > maxNoteLen {
		writeJSON(w, http.StatusBadRequest, errorBody("note too long"))
		return
	}
	if mc.Principal.UserID.IsZero() {
		writeJSON(w, http.StatusForbidden, errorBody("a stable user identity is required to review break-glass; a system token cannot"))
		return
	}
	recordingGate := m.recordingGateNow()
	atomicRecording, ok := recordingGate.(AtomicRecordingGate)
	if !ok {
		writeJSON(w, http.StatusPreconditionFailed, errorBodyCode("recording_session_unavailable",
			"break-glass review requires atomic recording seal support"))
		return
	}
	now := m.clock.Now()
	var (
		out           breakGlassDTO
		clientErr     string
		clientErrCode string
		clientCode    int
	)
	var err error
	for attempt := 0; attempt < maxBreakGlassTxnRetries; attempt++ {
		out = breakGlassDTO{}
		clientErr, clientErrCode, clientCode = "", "", 0
		err = mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
			repo, err := sc.Ext(breakGlassKind)
			if err != nil {
				return err
			}
			rec, err := repo.Get(r.Context(), id)
			if err != nil {
				return err
			}
			if eff := effectiveBreakGlassStatus(rec, now); eff == bgStatusActive {
				clientErr, clientCode, clientErrCode = "break-glass grant is still active; revoke it (or let it expire) before the post-review", http.StatusConflict, "grant_still_active"
				return nil
			}
			if rec.Bool(colBGReviewed) {
				clientErr, clientCode, clientErrCode = "break-glass grant is already reviewed", http.StatusConflict, "already_reviewed"
				return nil
			}
			if u := mc.Principal.UserID.String(); u == rec.String(colBGActivatedByUser) {
				clientErr, clientCode, clientErrCode = "separation of duty: the activator cannot post-review their own break-glass grant", http.StatusForbidden, "separation_of_duty"
				return nil
			}
			// Update the grant first to preserve the cross-backend lock order
			// grant → audit → recording session. Nothing is visible before commit,
			// and a later seal/audit error rolls this update back.
			if rec.String(colBGStatus) == bgStatusActive {
				rec[colBGStatus] = bgStatusExpired
			}
			rec[colBGReviewed] = true
			rec[colBGReviewedAt] = now.String()
			rec[colBGReviewedBy] = mc.Principal.Actor()
			rec[colBGReviewedByUser] = mc.Principal.UserID.String()
			rec[colBGReviewNote] = in.Note
			rec[colBGActiveGuard] = nil
			rec, err = repo.Update(r.Context(), rec)
			if err != nil {
				return err
			}
			if err := atomicRecording.SealGrantInScope(r.Context(), sc, id, mc.Principal); err != nil {
				return err
			}
			out = toBreakGlassDTO(rec, now)
			return auditEvent(r.Context(), sc, mc, "governance.breakglass.review", breakGlassKind, id, map[string]any{
				"use_count": rec.Int(colBGUseCount),
			})
		})
		if err == nil || !isConflict(err) {
			break
		}
	}
	if clientErr != "" {
		writeJSON(w, clientCode, errorBodyCode(clientErrCode, clientErr))
		return
	}
	if err != nil {
		if errors.Is(err, api.ErrRecordingSessionPrecondition) {
			writeJSON(w, http.StatusPreconditionFailed, errorBodyCode("recording_session_unavailable",
				"the break-glass recording could not be sealed; the review and active guard were not changed"))
			return
		}
		writeStoreError(w, err)
		return
	}
	m.emitBreakGlassFinding(r.Context(), mc.Tenant, findingBreakGlassReviewed, out.ID, out.MatchAction, sdkmodel.SeverityInfo,
		"Break-glass grant post-reviewed — emergency-access loop closed")
	writeJSON(w, http.StatusOK, out)
}

// handleConsumeBreakGlass is the authorization-and-evidence call the bridge
// makes: "may this action proceed under an active grant?" — and when yes, the
// use is RECORDED (append-only trail + ledger event) in the same transaction, so
// there is no instant in which an emergency authorization exists without its
// evidence. Write-tier (the bridge's editor-scoped service token). When no
// active grant covers the action it answers granted=false — never an error, so
// the bridge's normal deny-closed path continues unchanged.
func (m *Module) handleConsumeBreakGlass(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in consumeBreakGlassRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	in.Action = strings.TrimSpace(in.Action)
	in.SubjectKind = strings.TrimSpace(in.SubjectKind)
	if in.Action == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("action is required"))
		return
	}
	// The same minimal-data bounds as an approval's fields: these ride the
	// immutable use trail and the audit Meta.
	if len(in.Action) > maxMatchLen || len(in.SubjectKind) > maxMatchLen {
		writeJSON(w, http.StatusBadRequest, errorBody("action and subject_kind must be short identifiers"))
		return
	}
	if len(in.SubjectRef) > maxNoteLen {
		writeJSON(w, http.StatusBadRequest, errorBody("subject_ref too long"))
		return
	}
	if containsInlineCredential(in.Action) || containsInlineCredential(in.SubjectKind) || containsInlineCredential(in.SubjectRef) {
		writeJSON(w, http.StatusBadRequest, errorBody("action and subject fields must not contain a credential"))
		return
	}

	type consumeResponse struct {
		Granted   bool   `json:"granted"`
		Grant     string `json:"grant,omitempty"`
		ExpiresAt string `json:"expires_at,omitempty"`
	}

	for attempt := 0; attempt < maxConsumeRetries; attempt++ {
		now := m.clock.Now()
		var (
			resp    consumeResponse
			grantID string
			scope   string
		)
		err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
			repo, err := sc.Ext(breakGlassKind)
			if err != nil {
				return err
			}
			// Stored-active grants only (a small set: activation enforces one at a
			// time); effective status re-derived per grant, deny-closed on expiry.
			grants, err := listAll(r.Context(), repo, eq(colBGStatus, bgStatusActive))
			if err != nil {
				return err
			}
			var grant model.Record
			for _, g := range grants {
				if effectiveBreakGlassStatus(g, now) != bgStatusActive {
					continue
				}
				if !breakGlassMatches(g.String(colBGMatchAction), in.Action) {
					continue
				}
				grant = g
				break
			}
			if grant == nil {
				resp = consumeResponse{Granted: false}
				return nil
			}
			grantID, scope = grant.String(model.ColID), grant.String(colBGMatchAction)
			// Record the use FIRST (append-only evidence), then bump the counter
			// under the version-checked update that serializes concurrent consumes.
			useRepo, err := sc.Ext(breakGlassUseKind)
			if err != nil {
				return err
			}
			if _, err := useRepo.Create(r.Context(), model.Record{
				colBGUseGrant: grantID, colAction: in.Action,
				colSubjectKind: in.SubjectKind, colSubjectRef: in.SubjectRef,
				colBGUsedBy: mc.Principal.Actor(), colBGUsedByUser: mc.Principal.UserID.String(),
				colBGUsedAt: now.String(),
			}); err != nil {
				return err
			}
			grant[colBGUseCount] = grant.Int(colBGUseCount) + 1
			if _, err := repo.Update(r.Context(), grant); err != nil {
				return err // ErrConflict -> retry
			}
			resp = consumeResponse{Granted: true, Grant: grantID, ExpiresAt: grant.String(colBGExpiresAt)}
			return auditEvent(r.Context(), sc, mc, "governance.breakglass.use", breakGlassKind, model.ID(grantID), map[string]any{
				"action": in.Action, "subject_kind": in.SubjectKind,
			})
		})
		if err != nil {
			if isConflict(err) {
				continue // version race on the counter: reload and re-evaluate
			}
			writeStoreError(w, err)
			return
		}
		if resp.Granted {
			m.emitBreakGlassFinding(r.Context(), mc.Tenant, findingBreakGlassUsed, grantID, scope, sdkmodel.SeverityHigh,
				"Action proceeded under BREAK-GLASS emergency access — included in the grant's forced post-review")
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}
	writeJSON(w, http.StatusConflict, errorBody("break-glass consume conflicted repeatedly; please retry"))
}

// emitBreakGlassFinding publishes a break-glass lifecycle finding on the
// notification rail. Titles are fixed templates; the grant id rides SubjectRef
// and the (non-sensitive, bounded) action scope is hashed into DetailHash with
// the kind — minimal data, mirroring emitApprovalFinding (docs/SECURITY-HARDENING.md).
func (m *Module) emitBreakGlassFinding(ctx context.Context, tenant model.TenantID, kind, grantID, matchAction string, sev sdkmodel.Severity, title string) {
	if m.host == nil {
		return
	}
	sum := sha256.Sum256([]byte(kind + "|" + grantID + "|" + matchAction))
	finding := sdkmodel.FindingReport{
		Kind:        kind,
		Severity:    sev,
		SubjectKind: "breakglass",
		SubjectRef:  grantID,
		Title:       title,
		DetailHash:  hex.EncodeToString(sum[:]),
		OccurredAt:  m.clock.Now().Time(),
	}
	if err := m.host.Publish(ctx, event.FromObservation(tenant.String(), Name, finding)); err != nil {
		m.debugf("governance: emit break-glass finding failed", "err", err)
	}
}
