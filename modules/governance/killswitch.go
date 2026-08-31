// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// the estate kill switch: the one-click emergency stop the
// runtime-governance story composes out of pieces the plane already has.
//
//   - ENGAGE is deliberately CHEAP: admin-tier, a justification, one Mutate. No
//     approval gate, no AAL3 step-up, no break-glass — an emergency stop that
//     waits for quorum is not a stop (docs/SECURITY-HARDENING.md: the asymmetric-risk bias is
//     to stop too much, not too late). The engaging session's assurance level
//     is RECORDED (row + ledger) so forensics can weigh the attribution. Abuse
//     of engage costs availability only — recovery is what dual-control guards.
//   - The stop row is the SINGLE source of truth. Every actuation gate (hooks
//     PEP, MCP forward, orchestration fire, voice open, models route, deploy
//     apply/retire, eventing dispatch) consults it LIVE via KillSwitchState and
//     fails CLOSED on a read error — the exact inverse of the budget gates'
//     documented fail-open contract (budgetgate.go), because a stop is positive
//     enforcement and an unreadable stop state must never mean "go".
//   - ENGAGE also revokes the queued work the gates cannot reach: every PENDING
//     approval for an in-scope actuation action is canceled in the same
//     transaction, so pre-stop intents cannot ripen into approved grants that
//     dispatch the moment the estate is re-enabled. Approved-but-unconsumed
//     grants cannot be canceled (the engine cancels pending only, by design);
//     they are listed in the evidence pack for the post-review instead.
//   - RE-ENABLE is NEVER unilateral: it is gated on a fresh dual-control
//     approval (action security.killswitch.reenable — pre-classified CRITICAL,
//     risktier.go:103 — so the engine enforces 2 distinct humans, anti-self-
//     approval and AAL3 step-up on every decision), and this handler
//     additionally re-verifies STRUCTURALLY, in the same transaction, that at
//     least two distinct humans approved — so even an operator approval policy
//     that downgrades the tier (resolveRiskTier honors explicit risk_tier both
//     ways) can never make re-enable single-handed. There is deliberately NO
//     break-glass path here: break-glass exists to bypass a quorum in an
//     emergency, and "the estate stays stopped" IS the safe state.
//   - FORCED POST-REVIEW closes the incident: a second re-enable of the same
//     scope is blocked while a prior incident remains unreviewed (the
//     break-glass backpressure pattern), but engaging a NEW stop is never
//     blocked — paperwork must not delay containment.
const (
	ksStatusActive    = "active"
	ksStatusReenabled = "reenabled"

	ksScopeEstate = "estate"
	ksScopeAgent  = "agent"

	ksSourceOperator       = "operator"
	ksSourceGuardian       = "guardian"
	ksSourceTierFloor      = "tier_floor"
	ksSourceCircuitBreaker = "circuit_breaker"
)

// ksReenableAction is the governed action of the re-enable approval. It MUST
// stay under the "security.killswitch." prefix: that family is pre-classified
// CRITICAL-by-default in the risk registry (risktier.go:103, reserved for
// exactly this), which is what gives the approval its two-person floor and
// AAL3 decision bar with zero engine changes.
const ksReenableAction = "security.killswitch.reenable"

// ksReenableFloor is the non-negotiable structural floor for re-enable: two
// distinct human approvers, re-verified in this module's own transaction at the
// flip. The engine's CRITICAL floor already enforces this for the default
// classification; this second, structural check is what makes "never
// unilateral" hold even against an approval policy that explicitly downgrades
// the security.killswitch.* tier (an operator CAN retune tiers — but not this
// invariant).
const ksReenableFloor = 2

// ksActuationActions is the set of governed actuation actions whose PENDING
// approvals an engage revokes (the work-queue half of the stop). Governance
// actions (nhi.*, security.*, compliance.*) are deliberately NOT in the set:
// the stop halts the AGENTIC estate, never the controls that govern it.
var ksActuationActions = map[string]struct{}{
	"claude.tool.use":             {},
	"mcp.tool.call":               {},
	"deploy.apply":                {},
	"deploy.retire":               {},
	"orchestration.schedule.fire": {},
	"voice.session.open":          {},
	"routine.fire":                {},
}

// Finding kinds emitted by the kill-switch path (routed by modules/notify).
// The guardian loop HARD-SKIPS the "killswitch_" prefix so a stop's own
// findings can never re-trigger containment (the escalation feedback loop).
const (
	findingKillSwitchEngaged   = "killswitch_engaged"
	findingKillSwitchReenabled = "killswitch_reenabled"
	findingKillSwitchReviewed  = "killswitch_reviewed"
)

// ksMaxRevokedIDsInMeta caps how many canceled approval ids ride the engage
// audit Meta (small, bounded evidence; the full count is always recorded).
const ksMaxRevokedIDsInMeta = 50

// engageKillSwitchRequest opens an emergency stop. Scope graduates it:
// "estate" stops every actuation surface of the tenant (org == tenant in this
// model — core/model/entities.go); "agent" stops one agent across the surfaces
// that carry an agent dimension.
type engageKillSwitchRequest struct {
	ScopeKind string `json:"scope_kind"`
	ScopeRef  string `json:"scope_ref,omitempty"`
	Reason    string `json:"reason"`
}

// ksFindingSubject names WHO a stop is about, for a finding title.
//
// ⛔ POR QUÉ EXISTE, y no es cosmético. Los títulos de los findings de parada eran CONSTANTES:
// seis paradas distintas producían seis filas IDÉNTICAS en Security > Findings, indistinguibles
// salvo abriendo cada una. Lo midió y no era el seed. Un operador que mira esa lista
// durante un incidente necesita saber a QUIÉN se paró y POR QUÉ sin abrir seis filas.
//
// El modelo es `tierfloor.go`, que ya nombraba el agente y el tier en su título. Esto lo lleva a
// las paradas manuales.
//
// La fila guarda tres identificadores de menos a más internos; gana el primero no vacío, para que
// el operador lea el nombre que él escribió y no un id que no reconoce.
func ksFindingSubject(d killSwitchDTO) string {
	if d.ScopeKind == ksScopeEstate {
		return "the whole estate"
	}
	for _, v := range []string{d.AgentExternalID, d.AgentID, d.ScopeRef} {
		if s := strings.TrimSpace(v); s != "" {
			return "agent '" + s + "'"
		}
	}
	return "an agent scope with no recorded identifier"
}

// ksTitleReason folds an operator-supplied reason into ONE line.
//
// ⛔ SE PLIEGA Y SE RECORTA A PROPÓSITO. La razón es texto libre acotado sólo por `maxNoteLen`, y
// un título de finding es una FILA: un salto de línea o un párrafo de dos kilobytes rompen la
// superficie que el título existe para informar. `strings.Fields` colapsa saltos y tabuladores.
//
// Y el recorte va por RUNAS, nunca por bytes: cortar una razón multibyte a la mitad de un carácter
// publicaría UTF-8 inválido dentro del finding.
func ksTitleReason(reason string) string {
	s := strings.Join(strings.Fields(reason), " ")
	if s == "" {
		return ""
	}
	const maxTitleReason = 120
	if r := []rune(s); len(r) > maxTitleReason {
		s = strings.TrimSpace(string(r[:maxTitleReason])) + "…"
	}
	return s
}

// killSwitchDTO is the stop-row view: who/when/scope/reason plus the full
// re-enable + review lifecycle — the "estado del stop persistido y visible".
type killSwitchDTO struct {
	ID               string `json:"id"`
	ScopeKind        string `json:"scope_kind"`
	ScopeRef         string `json:"scope_ref,omitempty"`
	AgentID          string `json:"agent_id,omitempty"`
	AgentExternalID  string `json:"agent_external_id,omitempty"`
	Status           string `json:"status"`
	Reason           string `json:"reason,omitempty"`
	Source           string `json:"source"`
	RuleRef          string `json:"rule_ref,omitempty"`
	EngagedBy        string `json:"engaged_by,omitempty"`
	EngagedAAL       int64  `json:"engaged_aal"`
	EngagedAt        string `json:"engaged_at,omitempty"`
	EngageAuditSeq   int64  `json:"engage_audit_seq"`
	RevokedApprovals int64  `json:"revoked_approvals"`
	ReenableApproval string `json:"reenable_approval,omitempty"`
	ReenabledBy      string `json:"reenabled_by,omitempty"`
	ReenabledAt      string `json:"reenabled_at,omitempty"`
	ReenableAuditSeq int64  `json:"reenable_audit_seq,omitempty"`
	Reviewed         bool   `json:"reviewed"`
	ReviewedBy       string `json:"reviewed_by,omitempty"`
	ReviewedAt       string `json:"reviewed_at,omitempty"`
	ReviewNote       string `json:"review_note,omitempty"`
}

func toKillSwitchDTO(rec model.Record) killSwitchDTO {
	return killSwitchDTO{
		ID: rec.String(model.ColID), ScopeKind: rec.String(colKSScopeKind), ScopeRef: rec.String(colKSScopeRef),
		AgentID: rec.String(colKSAgentID), AgentExternalID: rec.String(colKSAgentExternal),
		Status: rec.String(colKSStatus), Reason: rec.String(colKSReason), Source: rec.String(colKSSource),
		RuleRef: rec.String(colKSRuleRef), EngagedBy: rec.String(colKSEngagedBy),
		EngagedAAL: rec.Int(colKSEngagedAAL), EngagedAt: rec.String(colKSEngagedAt),
		EngageAuditSeq: rec.Int(colKSEngageSeq), RevokedApprovals: rec.Int(colKSRevokedCount),
		ReenableApproval: rec.String(colKSReenableAppr), ReenabledBy: rec.String(colKSReenabledBy),
		ReenabledAt: rec.String(colKSReenabledAt), ReenableAuditSeq: rec.Int(colKSReenableSeq),
		Reviewed: rec.Bool(colKSReviewed), ReviewedBy: rec.String(colKSReviewedBy),
		ReviewedAt: rec.String(colKSReviewedAt), ReviewNote: rec.String(colKSReviewNote),
	}
}

// ksScopeKey is the normalized scope identity the active_guard sentinel encodes:
// "estate", or "agent:<resolved-uuid-else-given-ref>". Normalizing to the
// resolved UUID makes a stop engaged by external id and one engaged by UUID
// collide on the same agent (one active stop per agent, however it was named).
func ksScopeKey(scopeKind, scopeRef, agentID string) string {
	if scopeKind == ksScopeEstate {
		return ksScopeEstate
	}
	ref := strings.TrimSpace(agentID)
	if ref == "" {
		ref = strings.TrimSpace(scopeRef)
	}
	return ksScopeAgent + ":" + ref
}

// ksEngageParams is the internal engage input shared by the operator handler
// and the guardian loop (which has no HTTP principal).
type ksEngageParams struct {
	ScopeKind string
	ScopeRef  string
	Reason    string
	Source    string // ksSourceOperator | ksSourceGuardian
	RuleRef   string // guardian rule id (guardian engages only)
	Actor     string // audit-actor string
	ActorKind string
	UserID    string // stable user id ("" for guardian)
	AAL       int64  // recorded assurance of the engaging session (0 for guardian)
}

// ksEngageOutcome is what engageKillSwitchLocked hands back for the caller's
// post-commit emits and response.
type ksEngageOutcome struct {
	Record        model.Record
	AlreadyActive bool // an active stop for the same scope key existed; Record is that row
}

// engageKillSwitchLocked creates the stop row, revokes in-scope pending
// actuation approvals and appends the engage self-audit — all inside the
// caller's transaction. Idempotent on the scope key: an existing ACTIVE stop
// for the same key is returned as AlreadyActive (the guardian path treats that
// as success; the operator handler maps it to a 409 naming the existing stop).
func (m *Module) engageKillSwitchLocked(ctx context.Context, sc store.Scope, p ksEngageParams, now model.Timestamp) (ksEngageOutcome, error) {
	repo, err := sc.Ext(killSwitchKind)
	if err != nil {
		return ksEngageOutcome{}, err
	}

	// Resolve the agent (by UUID, then by external_id) so the stop matches the
	// agent under EVERY identifier the actuation surfaces use. Resolution is
	// best-effort and honest: an unknown ref still engages (a rogue agent that
	// never reached the inventory must still be stoppable by the ref the PEP
	// sees), it just matches on the given string only.
	agentID, agentExternal := "", ""
	if p.ScopeKind == ksScopeAgent {
		ref := strings.TrimSpace(p.ScopeRef)
		if id, perr := model.ParseID(ref); perr == nil && !id.IsZero() {
			if a, gerr := sc.Agents().Get(ctx, id); gerr == nil {
				agentID, agentExternal = a.ID.String(), a.ExternalID
			} else if !isNotFound(gerr) {
				return ksEngageOutcome{}, gerr
			}
		}
		if agentID == "" {
			if agents, _, lerr := sc.Agents().List(ctx, model.Query{Filters: []model.Filter{eq("external_id", ref)}, Limit: 1}); lerr != nil {
				return ksEngageOutcome{}, lerr
			} else if len(agents) > 0 {
				agentID, agentExternal = agents[0].ID.String(), agents[0].ExternalID
			}
		}
	}

	scopeKey := ksScopeKey(p.ScopeKind, p.ScopeRef, agentID)

	// Friendly fast path for the sentinel race: an active stop for this scope
	// already exists. The unique (tenant_id, active_guard) index backstops it.
	if existing, found, err := findOne(ctx, repo, eq(colKSActiveGuard, "stop:"+scopeKey)); err != nil {
		return ksEngageOutcome{}, err
	} else if found {
		return ksEngageOutcome{Record: existing, AlreadyActive: true}, nil
	}

	// Revoke the queued work: cancel every PENDING approval for an in-scope
	// actuation action, so a pre-stop intent cannot ripen into an approved
	// grant behind the stop's back. A row that races to decided in parallel
	// simply skips (it is no longer pending; the dispatch gates still deny it
	// while the stop is active).
	revokedIDs, err := m.cancelActuationApprovals(ctx, sc, p, agentID, agentExternal, now)
	if err != nil {
		return ksEngageOutcome{}, err
	}

	rec := model.Record{
		colKSScopeKind: p.ScopeKind, colKSScopeRef: strings.TrimSpace(p.ScopeRef),
		colKSStatus: ksStatusActive, colKSReason: p.Reason, colKSSource: p.Source,
		colKSEngagedBy: p.Actor, colKSEngagedByUser: p.UserID, colKSEngagedAAL: p.AAL,
		colKSEngagedAt: now.String(), colKSEngageSeq: int64(0),
		colKSRevokedCount: int64(len(revokedIDs)), colKSReviewed: false,
		colKSActiveGuard: "stop:" + scopeKey,
	}
	if agentID != "" {
		rec[colKSAgentID] = agentID
	}
	if agentExternal != "" {
		rec[colKSAgentExternal] = agentExternal
	}
	if p.RuleRef != "" {
		rec[colKSRuleRef] = p.RuleRef
	}
	created, err := repo.Create(ctx, rec)
	if err != nil {
		return ksEngageOutcome{}, err // unique-index race maps at the caller (isConflict)
	}
	id := model.ID(created.String(model.ColID))

	// The engage self-audit is the evidence pack's ANCHOR: its Seq bounds the
	// incident timeline from below, so it is captured onto the row in the same
	// transaction. Meta carries identifiers and counts only — never the
	// free-text reason (minimal data, docs/SECURITY-HARDENING.md; the reason lives on the row).
	meta := map[string]any{
		"scope_kind": p.ScopeKind, "scope_key": scopeKey, "source": p.Source,
		"engaged_aal": p.AAL, "revoked_approvals": len(revokedIDs),
	}
	if p.RuleRef != "" {
		meta["rule_ref"] = p.RuleRef
	}
	if n := len(revokedIDs); n > 0 {
		capped := revokedIDs
		if n > ksMaxRevokedIDsInMeta {
			capped = capped[:ksMaxRevokedIDsInMeta]
			meta["revoked_approvals_truncated"] = true
		}
		meta["revoked_approval_ids"] = capped
	}
	sealed, err := sc.Audit().Append(ctx, model.AuditDraft{
		Actor: p.Actor, ActorKind: p.ActorKind,
		Action: "governance.killswitch.engage", TargetKind: killSwitchKind, TargetID: id,
		Meta: meta,
	})
	if err != nil {
		return ksEngageOutcome{}, err
	}
	// Seq 0 = evidence dropped under the degrade spool policy (honest zero).
	created[colKSEngageSeq] = sealed.Seq
	created, err = repo.Update(ctx, created)
	if err != nil {
		return ksEngageOutcome{}, err
	}
	return ksEngageOutcome{Record: created}, nil
}

// cancelActuationApprovals cancels the PENDING approvals an engage revokes and
// returns their ids. Estate scope cancels every pending actuation approval;
// agent scope cancels those whose subject is that agent (the surfaces that key
// approvals on other subjects — e.g. orchestration fires keyed on a schedule —
// stay pending and are denied at dispatch by the stop gates instead; honest
// coverage, not a pretend cancel).
func (m *Module) cancelActuationApprovals(ctx context.Context, sc store.Scope, p ksEngageParams, agentID, agentExternal string, now model.Timestamp) ([]string, error) {
	repo, err := sc.Ext(approvalKind)
	if err != nil {
		return nil, err
	}
	pending, err := listAll(ctx, repo, eq(colStatus, statusPending))
	if err != nil {
		return nil, err
	}
	refs := map[string]struct{}{}
	for _, r := range []string{strings.TrimSpace(p.ScopeRef), agentID, agentExternal} {
		if r != "" {
			refs[r] = struct{}{}
		}
	}
	var canceled []string
	for _, rec := range pending {
		if _, governed := ksActuationActions[rec.String(colAction)]; !governed {
			continue
		}
		if effectiveStatus(rec, now) != statusPending {
			continue // lazily expired; nothing to revoke
		}
		if p.ScopeKind == ksScopeAgent && !ksApprovalSubjectMatches(rec, refs) {
			continue
		}
		rec[colStatus] = statusCanceled
		rec[colDecidedAt] = now.String()
		if _, uerr := repo.Update(ctx, rec); uerr != nil {
			if isConflict(uerr) {
				continue // raced with a decision; no longer pending — the gates still deny
			}
			return nil, uerr
		}
		canceled = append(canceled, rec.String(model.ColID))
	}
	return canceled, nil
}

// ksApprovalSubjectMatches reports whether an approval's subject is one of the
// stopped agent's identifiers. The bridge appends "#plan=<hash>" to subject
// refs (encodeSubjectRef), so both the exact ref and the plan-suffixed form match.
func ksApprovalSubjectMatches(rec model.Record, refs map[string]struct{}) bool {
	if rec.String(colSubjectKind) != "agent" {
		return false
	}
	subject := rec.String(colSubjectRef)
	base := subject
	if i := strings.Index(subject, "#plan="); i >= 0 {
		base = subject[:i]
	}
	_, ok := refs[base]
	return ok
}

// handleEngageKillSwitch is the one-click emergency stop (POST /killswitch).
// Admin-tier. Deliberately NO approval gate, NO AAL3 bar and NO break-glass —
// see the file header for the threat analysis; the session's assurance level is
// recorded for forensics, the reason is mandatory, and the CRITICAL finding
// reaches the notification rail seconds after commit.
func (m *Module) handleEngageKillSwitch(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in engageKillSwitchRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	in.ScopeKind = strings.ToLower(strings.TrimSpace(in.ScopeKind))
	in.ScopeRef = strings.TrimSpace(in.ScopeRef)
	in.Reason = strings.TrimSpace(in.Reason)
	switch in.ScopeKind {
	case ksScopeEstate:
		if in.ScopeRef != "" {
			writeJSON(w, http.StatusBadRequest, errorBody("scope_ref must be empty for an estate-wide stop"))
			return
		}
	case ksScopeAgent:
		if in.ScopeRef == "" {
			writeJSON(w, http.StatusBadRequest, errorBody("scope_ref (the agent id or external ref) is required for an agent-scoped stop"))
			return
		}
	default:
		writeJSON(w, http.StatusBadRequest, errorBody("scope_kind must be one of estate, agent"))
		return
	}
	if in.Reason == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("a reason is required to engage the kill switch (who is reading this later: the post-review and the regulator)"))
		return
	}
	if len(in.Reason) > maxNoteLen {
		writeJSON(w, http.StatusBadRequest, errorBody("reason too long"))
		return
	}
	if len(in.ScopeRef) > maxNoteLen {
		writeJSON(w, http.StatusBadRequest, errorBody("scope_ref too long"))
		return
	}
	if containsInlineCredential(in.ScopeRef) {
		writeJSON(w, http.StatusBadRequest, errorBody("scope_ref must not contain a credential"))
		return
	}

	now := m.clock.Now()
	var out ksEngageOutcome
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		var ierr error
		out, ierr = m.engageKillSwitchLocked(r.Context(), sc, ksEngageParams{
			ScopeKind: in.ScopeKind, ScopeRef: in.ScopeRef, Reason: in.Reason,
			Source: ksSourceOperator,
			Actor:  mc.Principal.Actor(), ActorKind: mc.Principal.ActorKind(),
			UserID: mc.Principal.UserID.String(), AAL: int64(mc.Principal.AAL),
		}, now)
		return ierr
	})
	if err != nil {
		if isConflict(err) {
			// The (tenant_id, active_guard) unique index lost a concurrent-engage
			// race: same outcome as the friendly check — the scope is stopped.
			writeJSON(w, http.StatusConflict, errorBody("an active stop for this scope already exists (concurrent engage)"))
			return
		}
		writeStoreError(w, err)
		return
	}
	dto := toKillSwitchDTO(out.Record)
	if out.AlreadyActive {
		writeJSON(w, http.StatusConflict, errorBody("an active stop for this scope already exists ("+dto.ID+"); re-enable it under dual-control first"))
		return
	}
	// Emit AFTER commit. An estate stop is CRITICAL (the estate is frozen); an
	// agent stop is HIGH. notify routes the kind to the tenant's channels —
	// notify is deliberately EXEMPT from the stop, so this alert always flows.
	// El título lleva SUJETO y RAZÓN. La clave semántica del finding no cambia: `Kind`,
	// `SubjectRef` y el `DetailHash` (sha256 de kind|stopID|scopeKind) NO derivan del título, y
	// la severidad se decide por scope igual que antes.
	subject, why := ksFindingSubject(dto), ksTitleReason(dto.Reason)
	if why != "" {
		why = " — " + why
	}
	sev, title := sdkmodel.SeverityHigh,
		"Kill switch ENGAGED for "+subject+why+"; stopped across all governed actuation surfaces, re-enable requires dual-control"
	if in.ScopeKind == ksScopeEstate {
		sev, title = sdkmodel.SeverityCritical,
			"Kill switch ENGAGED for "+subject+why+"; estate-wide emergency stop, all governed actuation is denied until a dual-control re-enable"
	}
	m.emitKillSwitchFinding(r.Context(), mc.Tenant, findingKillSwitchEngaged, dto.ID, dto.ScopeKind, sev, title)
	writeJSON(w, http.StatusCreated, dto)
}

// handleListKillSwitch lists stop rows, optionally filtered by stored status —
// the persisted, visible stop state (who, when, scope, reason).
func (m *Module) handleListKillSwitch(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if v := r.URL.Query().Get("status"); v != "" {
		q.Filters = append(q.Filters, eq(colKSStatus, v))
	}
	out := listResponse[killSwitchDTO]{Items: []killSwitchDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(killSwitchKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toKillSwitchDTO(rec))
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

// handleGetKillSwitch returns one stop row.
func (m *Module) handleGetKillSwitch(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var (
		out   killSwitchDTO
		found bool
	)
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(killSwitchKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		found, out = true, toKillSwitchDTO(rec)
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

// killSwitchStateDTO is the live posture (GET /killswitch/state): what the
// console banner shows and what an operator checks first in an incident.
type killSwitchStateDTO struct {
	EstateStopped bool            `json:"estate_stopped"`
	Active        []killSwitchDTO `json:"active"`
}

// handleKillSwitchState returns the live stop posture for the tenant.
func (m *Module) handleKillSwitchState(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	out := killSwitchStateDTO{Active: []killSwitchDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(killSwitchKind)
		if err != nil {
			return err
		}
		recs, err := listAll(r.Context(), repo, eq(colKSStatus, ksStatusActive))
		if err != nil {
			return err
		}
		for _, rec := range recs {
			dto := toKillSwitchDTO(rec)
			out.Active = append(out.Active, dto)
			if dto.ScopeKind == ksScopeEstate {
				out.EstateStopped = true
			}
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// reenableKillSwitchRequest carries the optional operator note for the
// dual-control request the first POST opens.
type reenableKillSwitchRequest struct {
	Reason string `json:"reason,omitempty"`
}

// reenablePendingDTO is the 202 envelope while the dual-control approval is
// open: the caller (console) polls/links the approval for its two decisions.
type reenablePendingDTO struct {
	Status   string        `json:"status"` // pending_approval
	Approval approvalDTO   `json:"approval"`
	Stop     killSwitchDTO `json:"stop"`
}

// handleReenableKillSwitch lifts a stop — NEVER unilaterally. The first call
// opens the dual-control approval (CRITICAL: 2 distinct humans, anti-self-
// approval, AAL3 per decision — all enforced by the engine); subsequent calls
// report 202 while pending, and the call that finds it approved re-verifies the
// two-human floor STRUCTURALLY in the same transaction and flips the stop. A
// rejected/expired/canceled request is replaced by a fresh one on the next call
// (a new quorum — two humans must still actively approve; nothing is laundered).
// There is NO break-glass path here by design.
func (m *Module) handleReenableKillSwitch(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var in reenableKillSwitchRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if len(in.Reason) > maxNoteLen {
		writeJSON(w, http.StatusBadRequest, errorBody("reason too long"))
		return
	}
	if mc.Principal.UserID.IsZero() {
		writeJSON(w, http.StatusForbidden, errorBody("a stable user identity is required to request re-enable; a system token cannot"))
		return
	}

	now := m.clock.Now()
	var (
		stopDTO    killSwitchDTO
		pending    *approvalDTO
		reenabled  bool
		clientErr  string
		clientCode int
		clientCID  string
	)
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(killSwitchKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		if rec.String(colKSStatus) != ksStatusActive {
			clientErr, clientCode = "kill switch is "+rec.String(colKSStatus)+"; only an active stop can be re-enabled", http.StatusConflict
			return nil
		}

		apprRepo, err := sc.Ext(approvalKind)
		if err != nil {
			return err
		}

		// Load the bound approval, if one was already opened for THIS stop.
		var appr model.Record
		if ref := rec.String(colKSReenableAppr); ref != "" {
			a, gerr := apprRepo.Get(r.Context(), model.ID(ref))
			if gerr != nil && !isNotFound(gerr) {
				return gerr
			}
			appr = a
		}

		openFresh := appr == nil
		if appr != nil {
			switch effectiveStatus(appr, now) {
			case statusPending:
				pols, perr := loadApprovalPolicies(r.Context(), sc)
				if perr != nil {
					return perr
				}
				dto := toApprovalDTO(appr, now, liveRiskTier(pols, appr))
				pending, stopDTO = &dto, toKillSwitchDTO(rec)
				return nil
			case statusApproved:
				// STRUCTURAL dual-control floor: count the distinct humans who
				// actively APPROVED, in this same transaction. The engine already
				// floors CRITICAL at 2 — this check holds even if an operator
				// policy downgraded the tier ("never unilateral" is not tunable).
				decRepo, derr := sc.Ext(decisionKind)
				if derr != nil {
					return derr
				}
				decs, derr := listAll(r.Context(), decRepo, eq(colApprovalID, appr.String(model.ColID)))
				if derr != nil {
					return derr
				}
				approvers := map[string]struct{}{}
				for _, d := range decs {
					if d.String(colDecision) == decisionApprove {
						if u := d.String(colDeciderUser); u != "" {
							approvers[u] = struct{}{}
						}
					}
				}
				if len(approvers) < ksReenableFloor {
					// A downgraded approval policy let this request cross to approved
					// with fewer than two humans. The floor is non-negotiable — and an
					// APPROVED request can take no further decisions (it is terminal),
					// so it is SPENT: unbind it (the next attempt opens a fresh
					// dual-control request) and refuse, never silently flip.
					rec[colKSReenableAppr] = nil
					if _, uerr := repo.Update(r.Context(), rec); uerr != nil {
						return uerr
					}
					clientErr = "dual-control floor: re-enable requires approval by at least 2 distinct humans (an approval policy cannot lower this); distinct approvers on the spent request: " + strconv.Itoa(len(approvers)) + " — retry to open a fresh dual-control request"
					clientCID, clientCode = "dual_control_required", http.StatusConflict
					return nil
				}
				// Forced post-review backpressure: a prior incident of this scope
				// still awaiting review blocks THIS re-enable (review it first).
				scopeKey := ksScopeKey(rec.String(colKSScopeKind), rec.String(colKSScopeRef), rec.String(colKSAgentID))
				if prior, found, ferr := findOne(r.Context(), repo, eq(colKSActiveGuard, "review:"+scopeKey)); ferr != nil {
					return ferr
				} else if found {
					clientErr = "a prior incident for this scope (" + prior.String(model.ColID) + ") has not been post-reviewed; review it before re-enabling again"
					clientCode = http.StatusConflict
					return nil
				}
				sealed, aerr := sc.Audit().Append(r.Context(), model.AuditDraft{
					Actor: mc.Principal.Actor(), ActorKind: mc.Principal.ActorKind(),
					Action: "governance.killswitch.reenable", TargetKind: killSwitchKind, TargetID: id,
					Meta: map[string]any{
						"approval": appr.String(model.ColID), "distinct_approvers": len(approvers),
					},
				})
				if aerr != nil {
					return aerr
				}
				rec[colKSStatus] = ksStatusReenabled
				rec[colKSReenabledBy] = mc.Principal.Actor()
				rec[colKSReenabledUser] = mc.Principal.UserID.String()
				rec[colKSReenabledAt] = now.String()
				// Seq 0 = evidence dropped under the degrade spool policy (honest zero).
				rec[colKSReenableSeq] = sealed.Seq
				rec[colKSActiveGuard] = "review:" + scopeKey
				rec, err = repo.Update(r.Context(), rec)
				if err != nil {
					return err // unique-index race on review:<scopeKey> maps to 409 below
				}
				reenabled, stopDTO = true, toKillSwitchDTO(rec)
				return nil
			default:
				// rejected / canceled / expired: that request is spent. Fall through
				// to open a FRESH dual-control request (a new quorum of two humans —
				// the spent one stays in the engine rows + ledger as evidence).
				openFresh = true
			}
		}

		if openFresh {
			reason := in.Reason
			if reason == "" {
				reason = "re-enable estate kill switch " + id.String()
			}
			dto, oerr := m.openApprovalRecord(r.Context(), sc, mc.Principal.Actor(), mc.Principal.ActorKind(), mc.Principal.UserID.String(), createApprovalRequest{
				SubjectKind: "killswitch", SubjectRef: id.String(),
				Action: ksReenableAction, Reason: reason,
				EscalateInSeconds: 1800, // surface a stalled re-enable to the rail in 30m
			}, ksReenableFloor, now)
			if oerr != nil {
				return oerr
			}
			rec[colKSReenableAppr] = dto.ID
			rec[colKSReenableReqBy] = mc.Principal.UserID.String()
			if _, uerr := repo.Update(r.Context(), rec); uerr != nil {
				return uerr
			}
			pending, stopDTO = &dto, toKillSwitchDTO(rec)
		}
		return nil
	})
	if clientErr != "" {
		if clientCID != "" {
			writeJSON(w, clientCode, errorBodyCode(clientCID, clientErr))
			return
		}
		writeJSON(w, clientCode, errorBody(clientErr))
		return
	}
	if err != nil {
		if isConflict(err) {
			writeJSON(w, http.StatusConflict, errorBody("a prior incident for this scope awaits post-review, or a concurrent re-enable won the race; reload the stop state"))
			return
		}
		writeStoreError(w, err)
		return
	}
	if reenabled {
		m.emitKillSwitchFinding(r.Context(), mc.Tenant, findingKillSwitchReenabled, stopDTO.ID, stopDTO.ScopeKind, sdkmodel.SeverityMedium,
			"Kill switch re-enabled under dual-control for "+ksFindingSubject(stopDTO)+" — actuation resumes; mandatory post-review now due")
		writeJSON(w, http.StatusOK, stopDTO)
		return
	}
	// Pending (fresh or still collecting decisions): the approval is the next
	// step — two distinct humans decide via POST /approvals/{id}/decisions.
	m.emitApprovalRequestedIfFresh(r.Context(), mc.Tenant, pending)
	writeJSON(w, http.StatusAccepted, reenablePendingDTO{Status: "pending_approval", Approval: *pending, Stop: stopDTO})
}

// emitApprovalRequestedIfFresh emits approval.requested for a just-opened
// re-enable approval (zero decisions yet). Re-polls of an already-announced
// pending approval do not re-emit.
func (m *Module) emitApprovalRequestedIfFresh(ctx context.Context, tenant model.TenantID, dto *approvalDTO) {
	if dto == nil || dto.ApproveCount > 0 || dto.RejectCount > 0 || dto.Status != statusPending {
		return
	}
	m.emitApprovalRequested(ctx, tenant, *dto)
}

// reviewKillSwitchRequest carries the mandatory post-review note.
type reviewKillSwitchRequest struct {
	Note string `json:"note"`
}

// handleReviewKillSwitch records the FORCED post-review of a re-enabled stop:
// a real human DIFFERENT from the engager, the re-enable requester and the
// re-enabler (separation of duties — nobody signs off their own incident),
// once, note mandatory. Until it lands, the "review:<scopeKey>" sentinel blocks
// the NEXT re-enable of the same scope — never a new engage.
func (m *Module) handleReviewKillSwitch(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var in reviewKillSwitchRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	in.Note = strings.TrimSpace(in.Note)
	if in.Note == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("a post-review note is required (what happened, was the stop justified, what changed)"))
		return
	}
	if len(in.Note) > maxNoteLen {
		writeJSON(w, http.StatusBadRequest, errorBody("note too long"))
		return
	}
	if mc.Principal.UserID.IsZero() {
		writeJSON(w, http.StatusForbidden, errorBody("a stable user identity is required to review; a system token cannot"))
		return
	}
	now := m.clock.Now()
	var (
		out        killSwitchDTO
		clientErr  string
		clientCode int
	)
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(killSwitchKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		if rec.String(colKSStatus) != ksStatusReenabled {
			clientErr, clientCode = "kill switch is "+rec.String(colKSStatus)+"; the post-review examines a closed incident (re-enable first)", http.StatusConflict
			return nil
		}
		if rec.Bool(colKSReviewed) {
			clientErr, clientCode = "incident is already reviewed", http.StatusConflict
			return nil
		}
		u := mc.Principal.UserID.String()
		for _, involved := range []string{rec.String(colKSEngagedByUser), rec.String(colKSReenableReqBy), rec.String(colKSReenabledUser)} {
			if involved != "" && involved == u {
				clientErr, clientCode = "separation of duty: whoever engaged, requested or executed the re-enable cannot post-review this incident", http.StatusForbidden
				return nil
			}
		}
		rec[colKSReviewed] = true
		rec[colKSReviewedAt] = now.String()
		rec[colKSReviewedBy] = mc.Principal.Actor()
		rec[colKSReviewedUser] = u
		rec[colKSReviewNote] = in.Note
		// Clear the sentinel to NULL: the incident loop is closed; the next
		// re-enable of this scope is no longer blocked.
		rec[colKSActiveGuard] = nil
		rec, err = repo.Update(r.Context(), rec)
		if err != nil {
			return err
		}
		out = toKillSwitchDTO(rec)
		return auditEvent(r.Context(), sc, mc, "governance.killswitch.review", killSwitchKind, id, nil)
	})
	if clientErr != "" {
		writeJSON(w, clientCode, errorBody(clientErr))
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	m.emitKillSwitchFinding(r.Context(), mc.Tenant, findingKillSwitchReviewed, out.ID, out.ScopeKind, sdkmodel.SeverityInfo,
		"Kill-switch incident post-reviewed for "+ksFindingSubject(out)+" — emergency-stop loop closed")
	writeJSON(w, http.StatusOK, out)
}

// ----------------------------------------------------------------------------
// The live consult the actuation gates use (composition-root adapters in cmd).
// ----------------------------------------------------------------------------

// StopState is the tenant's live kill-switch posture. AgentRefs carries EVERY
// known identifier (row id aside: the given ref, the resolved agent UUID and
// the agent external id) of each agent under an active agent-scoped stop, so a
// surface can match on whichever identifier it natively holds.
type StopState struct {
	EstateStopped bool
	EstateStopID  model.ID
	AgentRefs     map[string]model.ID
}

// Stopped reports whether actuation attributed to agentRef (or any actuation,
// when the estate is stopped) is denied, and the stop row that denies it. An
// empty agentRef matches only the estate stop.
func (s StopState) Stopped(agentRef string) (model.ID, bool) {
	if s.EstateStopped {
		return s.EstateStopID, true
	}
	if agentRef == "" {
		return "", false
	}
	id, ok := s.AgentRefs[agentRef]
	return id, ok
}

// Any reports whether any stop is active at all (the cheap pre-check for
// surfaces with no agent dimension).
func (s StopState) Any() bool { return s.EstateStopped || len(s.AgentRefs) > 0 }

// KillSwitchState reads the tenant's live stop posture. Callers (the cmd
// actuation gates) MUST fail closed on error: an unreadable stop state never
// means "go". The read is live by design — a stop must bite on the very next
// governed action, and the surfaces that consult it per-call (hooks PEP) accept
// the same read cost as the NHI enforcement consult.
func (m *Module) KillSwitchState(ctx context.Context, tenant model.TenantID) (StopState, error) {
	st := StopState{AgentRefs: map[string]model.ID{}}
	if m.data == nil {
		return st, errNoData
	}
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(killSwitchKind)
		if err != nil {
			return err
		}
		recs, err := listAll(ctx, repo, eq(colKSStatus, ksStatusActive))
		if err != nil {
			return err
		}
		for _, rec := range recs {
			id := model.ID(rec.String(model.ColID))
			switch rec.String(colKSScopeKind) {
			case ksScopeEstate:
				st.EstateStopped, st.EstateStopID = true, id
			case ksScopeAgent:
				for _, ref := range []string{rec.String(colKSScopeRef), rec.String(colKSAgentID), rec.String(colKSAgentExternal)} {
					if ref != "" {
						st.AgentRefs[ref] = id
					}
				}
			}
		}
		return nil
	})
	return st, err
}

// emitKillSwitchFinding publishes a kill-switch lifecycle finding on the
// notification rail. Minimal data: fixed title, the stop id on SubjectRef, a
// hashed detail (mirroring emitBreakGlassFinding).
func (m *Module) emitKillSwitchFinding(ctx context.Context, tenant model.TenantID, kind, stopID, scopeKind string, sev sdkmodel.Severity, title string) {
	if m.host == nil {
		return
	}
	sum := sha256.Sum256([]byte(kind + "|" + stopID + "|" + scopeKind))
	finding := sdkmodel.FindingReport{
		Kind:        kind,
		Severity:    sev,
		SubjectKind: "killswitch",
		SubjectRef:  stopID,
		Title:       title,
		DetailHash:  hex.EncodeToString(sum[:]),
		OccurredAt:  m.clock.Now().Time(),
	}
	if err := m.host.Publish(ctx, event.FromObservation(tenant.String(), Name, finding)); err != nil {
		m.debugf("governance: emit kill-switch finding failed", "err", err)
	}
}
