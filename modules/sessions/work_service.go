// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type workData interface {
	View(context.Context, func(store.Scope) error) error
	Mutate(context.Context, func(store.Scope) error) error
}

type tenantWorkData struct {
	data   api.ModuleData
	tenant model.TenantID
}

func (d tenantWorkData) View(ctx context.Context, fn func(store.Scope) error) error {
	if sc, ok := protocolReplayScopeFromContext(ctx, d.tenant); ok {
		return fn(sc)
	}
	if d.data == nil {
		return store.ErrStoreUnavailable
	}
	return d.data.View(ctx, d.tenant, fn)
}

func (d tenantWorkData) Mutate(ctx context.Context, fn func(store.Scope) error) error {
	if sc, ok := protocolReplayScopeFromContext(ctx, d.tenant); ok {
		return fn(sc)
	}
	if d.data == nil {
		return store.ErrStoreUnavailable
	}
	return d.data.Mutate(ctx, d.tenant, fn)
}

func (m *Module) workData(tenant model.TenantID) workData {
	return tenantWorkData{data: m.data, tenant: tenant}
}

// Validate performs the same syntactic, referential, FSM and policy checks as
// apply without writing a row or invoking an external effect.
func (m *Module) Validate(ctx context.Context, tenant model.TenantID, principal WorkPrincipal, cmd WorkCommand) (Assessment, error) {
	plan, err := m.planWithData(ctx, m.workData(tenant), tenant, principal, cmd)
	if err != nil {
		if a, ok := assessmentFromError(m.clock.Now().String(), err); ok {
			return a, nil
		}
		return Assessment{}, err
	}
	plan.PlanHash = ""
	return plan.Assessment, nil
}

// Plan is observational and returns a content-addressed description of the
// rows and evidence a later apply is expected to create.
func (m *Module) Plan(ctx context.Context, tenant model.TenantID, principal WorkPrincipal, cmd WorkCommand) (Plan, error) {
	plan, err := m.planWithData(ctx, m.workData(tenant), tenant, principal, cmd)
	if err != nil {
		if a, ok := assessmentFromError(m.clock.Now().String(), err); ok {
			return Plan{Assessment: a, Command: cmd.Command, RowEffects: []string{}, ExternalCalls: []string{}}, nil
		}
		return Plan{}, err
	}
	return plan, nil
}

func (m *Module) planWithData(ctx context.Context, data workData, tenant model.TenantID, principal WorkPrincipal, cmd WorkCommand) (Plan, error) {
	cmd = normalizeWorkCommand(cmd)
	if err := validateCommandSyntax(cmd); err != nil {
		return Plan{}, err
	}
	var err error
	cmd, err = hydrateWorkCommand(ctx, data, cmd)
	if err != nil {
		return Plan{}, classifyWorkStoreError(err)
	}
	if err := m.preflightContent(ctx, tenant, cmd); err != nil {
		return Plan{}, err
	}
	if err := m.preflightIdentity(ctx, data, tenant, principal, &cmd); err != nil {
		return Plan{}, err
	}
	var plan Plan
	err = data.View(ctx, func(sc store.Scope) error {
		if cmd.Command != "item.create" {
			items, err := sc.Ext(workItemKind)
			if err != nil {
				return err
			}
			item, err := items.Get(ctx, cmd.WorkItemID)
			if err != nil {
				return err
			}
			if err := m.observeAgentWorkAuthority(
				ctx, tenant, cmd.WorkspaceID, principal, &cmd, item,
			); err != nil {
				return err
			}
		}
		var err error
		plan, _, err = m.planInScope(ctx, sc, tenant, principal, cmd, nil)
		return err
	})
	if err != nil {
		return Plan{}, classifyWorkStoreError(err)
	}
	return plan, nil
}

func assessmentFromError(observedAt string, err error) (Assessment, bool) {
	we := asWorkError(err)
	if we == nil {
		return Assessment{}, false
	}
	return Assessment{
		Verdict: we.verdict, Code: we.code, ObservedAt: observedAt,
		// ⛔ EL CAMPO CULPABLE VIAJA EN `EvidenceRef`, y es simetrico a proposito: el camino
		// de EXITO ya pone ahi el esquema que dijo que si (`work_command_v1`), y el de FALLO
		// no ponia nada. Un `invalid_command` mudo obliga al llamante a adivinar cual de los
		// nueve campos del comando sobra o falta -- medido el 2026-08-25: tres viajes.
		// Aditivo: `EvidenceRef` ya existia en `WorkCheck` y queda vacio cuando el rechazo
		// no es de un campo.
		Checks: []WorkCheck{{Name: we.code, Verdict: we.verdict, EvidenceRef: we.field}}, PlanHash: "",
	}, true
}

func classifyWorkStoreError(err error) error {
	switch {
	case err == nil:
		return nil
	case asWorkError(err) != nil:
		return err
	case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrUnknownEntity):
		return broken(http.StatusNotFound, "not_found")
	case errors.Is(err, store.ErrConflict):
		return broken(http.StatusConflict, "state_conflict")
	case errors.Is(err, store.ErrWorkspaceConfinement), errors.Is(err, store.ErrWorkspaceLineageRequired):
		return broken(http.StatusForbidden, "workspace_confined")
	// THESE FOUR ARE BROKEN, NOT UNKNOWN, and the distinction is the whole reason they
	// are named here instead of being left to the shared status in writeWorkError.
	//
	// First centralized only the STATUS for them and left code="internal_error"
	// and VerdictUnknown, on the grounds that inventing work vocabulary was not that
	// session's to do. The external contrast showed that was incoherent rather than
	// merely incomplete: VerdictUnknown means the observation could not be completed,
	// and the console picks its "could not look" screen off exactly that verdict
	// (web/src/features/work/api.ts:97-108). A withdrawn tenant or a cross-region
	// refusal is a KNOWN, deterministic decision — the engine looked and said no.
	//
	// No vocabulary is invented: "forbidden" is what this file already answers for an
	// authority denial (:390,:401,:411) and "invalid_cursor" is what the read path
	// already answers for a cursor it rejects (work_api.go:518, work_read.go:57). They
	// are coarser than the core codes, and saying so is better than a code that is
	// false: distinguishing suspension from authorization is a contract change to
	// coordinate with the console, not a side effect of centralizing a mapping.
	case errors.Is(err, store.ErrTenantSuspended), errors.Is(err, store.ErrTenantNotInService):
		return broken(http.StatusLocked, "forbidden")
	case errors.Is(err, store.ErrResidencyViolation):
		return broken(http.StatusForbidden, "forbidden")
	case errors.Is(err, store.ErrCursorWithSort):
		return broken(http.StatusBadRequest, "invalid_cursor")
	case errors.Is(err, store.ErrAuditSpoolFull):
		return unknown("evidence_unavailable", err)
	case errors.Is(err, store.ErrStoreUnavailable), errors.Is(err, store.ErrNotLeader):
		return unknown("observation_unavailable", err)
	default:
		return err
	}
}

func (m *Module) preflightContent(ctx context.Context, tenant model.TenantID, cmd WorkCommand) error {
	var content []struct {
		kind string
		text string
	}
	switch cmd.Command {
	case "item.create", "item.update":
		if cmd.Title != "" {
			content = append(content, struct{ kind, text string }{"title", cmd.Title})
		}
		if cmd.BriefMD != "" {
			content = append(content, struct{ kind, text string }{"brief", cmd.BriefMD})
		}
	case "item.block", "item.fail", "item.cancel":
		content = append(content, struct{ kind, text string }{"reason", cmd.Reason})
	case "acceptance.add", "acceptance.update":
		content = append(content, struct{ kind, text string }{"acceptance.statement", cmd.Acceptance[0].Statement})
	case "decision.set", "decision.supersede", "decision.revoke":
		if cmd.StatementMD != "" {
			content = append(content, struct{ kind, text string }{"decision.statement", cmd.StatementMD})
		}
		if cmd.RationaleMD != "" {
			content = append(content, struct{ kind, text string }{"decision.rationale", cmd.RationaleMD})
		}
	}
	if len(content) == 0 {
		return nil
	}
	if m.workContent == nil {
		return unknown("policy_unavailable", nil)
	}
	for _, item := range content {
		decision, err := m.workContent.Inspect(ctx, tenant, cmd.WorkspaceID, item.kind, []byte(item.text))
		if err != nil {
			return unknown("policy_unavailable", err)
		}
		if !decision.Allowed {
			return broken(http.StatusUnprocessableEntity, stableContentCode(decision.Code))
		}
	}
	return nil
}

func stableContentCode(code string) string {
	if boundedToken(code, 64) {
		return code
	}
	return "content_rejected"
}

func (m *Module) planInScope(
	ctx context.Context,
	sc store.Scope,
	tenant model.TenantID,
	principal WorkPrincipal,
	cmd WorkCommand,
	applyNow *model.Timestamp,
) (Plan, model.Record, error) {
	checks := []WorkCheck{{Name: "syntax", Verdict: VerdictClean, EvidenceRef: "work_command_v1"}}
	var current model.Record
	var expectedETag string
	if cmd.Command == "item.create" {
		if _, err := sc.Workspaces().Get(ctx, cmd.WorkspaceID); err != nil {
			return Plan{}, nil, err
		}
		if !cmd.participantResolved {
			return Plan{}, nil, unknown("evidence_unavailable", nil)
		}
		if err := validateWorkReferences(ctx, sc, cmd.WorkspaceID, cmd.ParentID, cmd.SupersedesID); err != nil {
			return Plan{}, nil, err
		}
		checks = append(checks,
			WorkCheck{Name: "workspace_lineage", Verdict: VerdictClean, EvidenceRef: cmd.WorkspaceID.String()},
			WorkCheck{Name: "owner_eligible", Verdict: VerdictClean, EvidenceRef: cmd.OwnerKind},
		)
	} else {
		repo, err := sc.Ext(workItemKind)
		if err != nil {
			return Plan{}, nil, err
		}
		current, err = repo.Get(ctx, cmd.WorkItemID)
		if err != nil {
			return Plan{}, nil, err
		}
		cmd.WorkspaceID = model.ID(current.String(colWorkWorkspaceID))
		expectedETag = fmt.Sprintf("\"v%d\"", current.Int(model.ColVersion))
		if err := m.validateStateCommand(ctx, sc, tenant, principal, cmd, current); err != nil {
			return Plan{}, nil, err
		}
		checks = append(checks, WorkCheck{Name: "state_and_references", Verdict: VerdictClean, EvidenceRef: expectedETag})
	}

	eventType, eventTypes, err := plannedWorkEventTypes(ctx, sc, cmd, current, applyNow)
	if err != nil {
		return Plan{}, nil, err
	}
	verdict, code := aggregateChecks(checks)
	plan := Plan{
		Assessment: Assessment{
			Verdict: verdict, Code: code, ObservedAt: m.clock.Now().String(), Checks: checks,
		},
		Command: cmd.Command, ExpectedETag: expectedETag,
		RowEffects: workRowEffects(cmd, current, principal, max(1, len(eventTypes))),
		EventType:  eventType, EventTypes: eventTypes,
		AuditAction: "sessions.work." + cmd.Command, Permission: workCommandPermission(cmd.Command),
		ExternalCalls: []string{},
	}
	preimage := plan
	preimage.PlanHash = ""
	// Observation time is evidence about when the plan was produced, not an
	// input to the planned mutation. Keeping it out of the digest lets apply
	// reproduce a still-valid plan while the semantic command and observed
	// state remain bound by the hash.
	preimage.ObservedAt = ""
	hashCommand := cmd
	hashCommand.PlanHash = ""
	b, err := canonicalJSON(struct {
		Plan            Plan        `json:"plan"`
		Command         WorkCommand `json:"command"`
		AuthorityDigest string      `json:"authority_digest,omitempty"`
	}{Plan: preimage, Command: hashCommand, AuthorityDigest: cmd.agentAuthority.Digest})
	if err != nil {
		return Plan{}, nil, err
	}
	plan.PlanHash = hexHash(hashBytes(b))
	plan.Assessment.PlanHash = plan.PlanHash
	return plan, current, nil
}

func plannedWorkEventTypes(
	ctx context.Context,
	sc store.Scope,
	cmd WorkCommand,
	current model.Record,
	applyNow *model.Timestamp,
) (string, []string, error) {
	primary := workCommandEvent(cmd.Command)
	if current == nil || (cmd.Command != "lease.acquire" && cmd.Command != "lease.takeover") {
		return primary, nil, nil
	}
	lease, found, err := findWorkLease(ctx, sc, recordID(current))
	if err != nil {
		return "", nil, err
	}
	if !found {
		return "", nil, unknown("evidence_unavailable", nil)
	}
	state, err := workLeaseFenceState(lease)
	if err != nil {
		return "", nil, err
	}
	var observedAt model.Timestamp
	if applyNow != nil {
		observedAt = *applyNow
	} else {
		observedAt, err = observeLeaseClock(ctx, sc, model.ID(current.String(colWorkWorkspaceID)))
		if err != nil {
			return "", nil, err
		}
	}
	if state.Lifecycle != fenceActive || fenceIsLive(state, observedAt.Time()) {
		return primary, nil, nil
	}

	// An expired active generation is first materialized as an ended fact. A
	// replacement is a second fact only when two monotonic tokens remain: one to
	// invalidate the expired generation and one to mint the successor.
	expiredFence, expiryErr := nextFence(state.Fence)
	if expiryErr != nil {
		return "work.lease.ended", nil, nil
	}
	if _, acquireErr := nextFence(expiredFence); acquireErr != nil {
		return "work.lease.ended", nil, nil
	}
	return primary, []string{"work.lease.ended", primary}, nil
}

func workRowEffects(
	cmd WorkCommand,
	current model.Record,
	principal WorkPrincipal,
	plannedEventCounts ...int,
) []string {
	eventCount := 1
	if len(plannedEventCounts) > 0 {
		eventCount = max(1, plannedEventCounts[0])
	}
	base := []string{"sessions.work_item:update"}
	for range eventCount {
		base = append(base, "sessions.work_event:append", "sessions.work_outbox:insert")
	}
	base = append(base, "sessions.work_command:append")
	if workLeaseCommandNeedsClock(cmd.Command) {
		base = append(base, "sessions.work_guard:cas")
	}
	switch cmd.Command {
	case "item.create":
		effects := []string{"sessions.work_item:insert", "sessions.work_lease:insert", "sessions.work_event:append", "sessions.work_outbox:insert", "sessions.work_command:append"}
		if len(cmd.Acceptance) > 0 {
			effects = append(effects, "sessions.work_acceptance:insert")
		}
		return effects
	case "item.submit", "item.block", "item.fail", "item.cancel", "item.assign":
		if current != nil && current.String(colWorkStatus) == "active" {
			effects := append(base, "sessions.work_lease:cas")
			if workCommandConsumesExecutionLease(cmd, current, principal) {
				effects = append(effects, "sessions.claim:cas")
			}
			return effects
		}
		return base
	case "acceptance.evaluate":
		if current != nil && current.String(colWorkStatus) == "active" {
			return append(base, "sessions.work_acceptance:mutate", "sessions.claim:cas")
		}
		return append(base, "sessions.work_acceptance:mutate")
	case "dependency.add", "dependency.remove":
		return append(base, "sessions.work_guard:cas", "sessions.work_dependency:mutate")
	case "acceptance.add", "acceptance.update":
		return append(base, "sessions.work_acceptance:mutate")
	case "decision.set", "decision.supersede":
		return append(base, "sessions.work_decision:append", "sessions.work_decision_head:cas")
	case "decision.revoke":
		return append(base, "sessions.work_decision:append", "sessions.work_decision_head:cas", "sessions.work_acceptance:mutate")
	case "lease.acquire", "lease.takeover":
		effects := append(base, "sessions.claim:cas", "sessions.work_lease:cas")
		if cmd.HolderRunRef != "" {
			effects = append(effects, "sessions.run:bind")
		}
		if current != nil && current.String(colWorkStatus) == "review" {
			effects = append(effects, "sessions.work_acceptance:mutate")
		}
		return effects
	case "lease.renew", "lease.release":
		return append(base, "sessions.claim:cas", "sessions.work_lease:cas")
	case "lease.revoke", "lease.expire", "lease.owner_died":
		return append(base, "sessions.work_lease:cas")
	case "lease.clock_rebase":
		return base
	default:
		return base
	}
}

func workCommandConsumesExecutionLease(cmd WorkCommand, current model.Record, principal WorkPrincipal) bool {
	if current == nil || current.String(colWorkStatus) != "active" {
		return false
	}
	switch cmd.Command {
	case "item.submit", "acceptance.evaluate":
		return true
	case "item.block", "item.fail":
		return !principal.Admin
	default:
		return false
	}
}

func workCommandPermission(command string) string {
	if strings.HasPrefix(command, "lease.") {
		switch command {
		case "lease.takeover", "lease.revoke", "lease.clock_rebase":
			return "sessions:lease:admin"
		default:
			return "sessions:lease:write"
		}
	}
	if strings.HasPrefix(command, "decision.") {
		if command == "decision.revoke" {
			return "sessions:decision:admin"
		}
		return "sessions:decision:write"
	}
	if command == "item.assign" || command == "item.archive" {
		return "sessions:work:admin"
	}
	return "sessions:work:write"
}

func (m *Module) checkParticipant(ctx context.Context, tenant model.TenantID, workspace model.ID, kind, ref string) error {
	if m.workIdentity == nil {
		return unknown("evidence_unavailable", nil)
	}
	p, err := m.workIdentity.ResolveParticipant(ctx, tenant, workspace, kind, ref)
	if err != nil {
		// ⛔ UN REF QUE NO RESUELVE ES UNA DECISION, NO UNA CEGUERA — y esto contestaba
		// la TERCERA respuesta para las dos cosas.
		//
		// `unknown()` significa «el instrumento no pudo observar», y la consola elige su
		// pantalla de «no pude mirar» leyendo EL VEREDICTO (web/src/features/work/api.ts).
		// Un `owner_ref` mal formado salia por aqui, asi que al llamante se le decia que
		// el motor no habia podido mirar cuando el motor SI habia decidido: su dato estaba
		// mal. Y «no pude mirar» INVITA A REINTENTAR algo que no va a funcionar nunca.
		//
		// Medido el 2026-08-25 conduciendo el motor: `owner_ref` con prefijo `user:` en vez
		// del id pelado costo tres sondas por este camino, mientras que los rechazos que
		// nombran su campo costaron cero.
		//
		// El propio fichero ya tenia el principio escrito para el otro lado
		// (cmd/olivares/workkernel.go:358-360: «an unwired plane is I could not look, never
		// this session is not eligible»). Esto es el mismo principio en la direccion que
		// faltaba: un plano caido es ceguera; el dato del llamante, no.
		switch {
		case errors.Is(err, store.ErrInvalidID):
			return brokenField(http.StatusBadRequest, "invalid_command", fldOwnerRef)
		case errors.Is(err, store.ErrNotFound):
			return broken(http.StatusUnprocessableEntity, "owner_ineligible")
		default:
			return unknown("evidence_unavailable", err)
		}
	}
	if p.Kind != kind || p.CanonicalRef == "" || !p.Active || !p.WorkspaceEligible || p.CanonicalRef != ref {
		return broken(http.StatusUnprocessableEntity, "owner_ineligible")
	}
	return nil
}

// revalidateAgentWorkOwner refreshes the eligibility of the canonical owner
// loaded from the WorkItem. The caller must never pass holder_agent_ref or an
// authenticated ExternalID here: those are attribution/authentication
// namespaces, while owner_ref is the durable Identity.ID selected when the
// item was created. ResolveParticipant is the neutral composition seam that
// translates that ID and observes agent lifecycle plus sponsor validity.
func (m *Module) revalidateAgentWorkOwner(
	ctx context.Context,
	tenant model.TenantID,
	workspace model.ID,
	item model.Record,
) error {
	if item.String(colWorkOwnerKind) != "agent" {
		return nil
	}
	ownerRef := item.String(colWorkOwnerRef)
	if ownerRef == "" {
		return broken(http.StatusUnprocessableEntity, "owner_ineligible")
	}
	return m.checkParticipant(ctx, tenant, workspace, "agent", ownerRef)
}

func (m *Module) revalidateAgentWorkOwnerInScope(
	ctx context.Context,
	sc store.Scope,
	principal WorkPrincipal,
	cmd WorkCommand,
	item model.Record,
) error {
	if cmd.internal || item.String(colWorkOwnerKind) != "agent" {
		return nil
	}
	needsFence := workCommandNeedsAgentAuthority(cmd, item, principal)
	// lease.release deliberately stays out: it only reduces authority. An agent
	// becoming ineligible must stop renewal/consumption, but must not trap a live
	// generation by making its cooperative release unreachable.
	if !needsFence {
		return nil
	}
	checker, ok := m.workIdentity.(WorkAgentEligibilityInScope)
	if !ok {
		return unknown("evidence_unavailable", store.ErrRowLockUnavailable)
	}
	if !cmd.agentAuthority.Eligible || cmd.agentAuthority.Digest == "" || cmd.agentAuthority.Token == nil {
		return unknown("evidence_unavailable", nil)
	}
	if err := checker.LockAgentWorkAuthority(ctx, sc, cmd.agentAuthority); err != nil {
		if errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrNotFound) {
			if cmd.ExpectedPlanHash != "" {
				return broken(http.StatusPreconditionFailed, "plan_changed")
			}
			return broken(http.StatusUnprocessableEntity, "owner_ineligible")
		}
		return unknown("evidence_unavailable", err)
	}
	return nil
}

func (m *Module) observeAgentWorkAuthority(
	ctx context.Context,
	tenant model.TenantID,
	workspace model.ID,
	principal WorkPrincipal,
	cmd *WorkCommand,
	item model.Record,
) error {
	if cmd == nil || cmd.internal || item.String(colWorkOwnerKind) != "agent" {
		return nil
	}
	if !workCommandNeedsAgentAuthority(*cmd, item, principal) {
		return nil
	}
	checker, ok := m.workIdentity.(WorkAgentEligibilityInScope)
	if !ok {
		return unknown("evidence_unavailable", store.ErrRowLockUnavailable)
	}
	authenticatedAgentRef := ""
	if principal.ActorKind == model.ActorAgent {
		authenticatedAgentRef = principal.ActorRef
	}
	snapshot, err := checker.ObserveAgentWorkAuthority(
		ctx, tenant, workspace, item.String(colWorkOwnerRef), authenticatedAgentRef,
	)
	if err != nil {
		return unknown("evidence_unavailable", err)
	}
	if !snapshot.Eligible {
		return broken(http.StatusUnprocessableEntity, "owner_ineligible")
	}
	if snapshot.Digest == "" || snapshot.Token == nil {
		return unknown("evidence_unavailable", nil)
	}
	cmd.agentAuthority = snapshot
	return nil
}

func workCommandNeedsAgentAuthority(cmd WorkCommand, item model.Record, principal WorkPrincipal) bool {
	if cmd.internal || item.String(colWorkOwnerKind) != "agent" {
		return false
	}
	switch cmd.Command {
	case "lease.acquire", "lease.renew", "lease.takeover":
		return true
	case "item.fail":
		return !principal.Admin
	case "item.submit", "item.block", "acceptance.evaluate":
		return workCommandConsumesExecutionLease(cmd, item, principal)
	default:
		return false
	}
}

// sessionHolderProven answers whether authenticated session authority is the
// exact SID named by the command. holder_sid comes from the request and is only
// a routing declaration; Principal.SessionID comes from a server-side
// credential binding. Agent identity is deliberately not a substitute because
// one agent may drive multiple sibling sessions.
func (m *Module) sessionHolderProven(
	_ context.Context,
	_ model.TenantID,
	principal WorkPrincipal,
	sid string,
) (bool, error) {
	return sid != "" && principal.SessionID != "" && principal.SessionID == sid, nil
}

func (m *Module) preflightIdentity(
	ctx context.Context,
	data workData,
	tenant model.TenantID,
	principal WorkPrincipal,
	cmd *WorkCommand,
) error {
	// A work-session bearer is minted for one exact runtime generation. The
	// holder_run_ref remains optional for a session-only lease, but once a
	// command binds execution to a run it must be the run authenticated by that
	// bearer. Comparing here, before any body-derived run lookup, keeps two runs
	// of the same SID/agent from borrowing one another's credential.
	if !cmd.internal && principal.PurposeRestricted && cmd.HolderRunRef != "" &&
		(principal.SessionRunRef == "" || principal.SessionRunRef != cmd.HolderRunRef) {
		return broken(http.StatusUnprocessableEntity, "owner_ineligible")
	}
	switch cmd.Command {
	case "item.create", "item.assign", "item.ready":
		if err := m.checkParticipant(ctx, tenant, cmd.WorkspaceID, cmd.OwnerKind, cmd.OwnerRef); err != nil {
			return err
		}
		cmd.participantResolved = true
	case "item.block", "item.submit", "item.fail", "acceptance.evaluate":
		// These carry the holder triple that validateExecutionLeaseInScope
		// matches against the live lease, and they read the SAME predicate as
		// acquire. A session-owned lease holds an EMPTY agent slot by design, so
		// the agent fall-through in leasePrincipalMatches can never admit its
		// holder: without the proof here, a session that COULD take its lease
		// still could not submit the work it took. Reachable means the whole
		// loop, not the first call in it.
		//
		// A caller who is MEASURED not entitled is left unproven rather than
		// refused here: the refusal these commands already give stays exactly
		// where it was, so a wrong 403 becomes a 200 and never the reverse.
		//
		// An UNRESOLVABLE proof is a different answer and is not swallowed. It
		// can only fire for an agent caller that presented a holder_sid — a user
		// principal never reaches the resolver, and an absent holder_sid returns
		// above — and in exactly that situation the lease family already answered
		// evidence_unavailable when the resolver failed, so this is the module's
		// established third answer applied consistently, not a new fragility.
		// Reporting "I could not look" as "you are refused" is the failure this
		// repository cares about, and it is the one thing not done here.
		if cmd.internal {
			return nil
		}
		if cmd.HolderSID == "" {
			if cmd.Command != "item.fail" || principal.ActorKind != model.ActorAgent ||
				principal.ActorRef == "" {
				return nil
			}
			// A blocked/review item has no execution lease to prove the owner
			// through. Resolve the authenticated ExternalID against owner_ref's
			// canonical Identity.ID instead; absence of that seam is UNKNOWN, and
			// a measured mismatch stays an ordinary 403 in validateStateCommand.
			var item model.Record
			if err := data.View(ctx, func(sc store.Scope) error {
				items, err := sc.Ext(workItemKind)
				if err != nil {
					return err
				}
				item, err = items.Get(ctx, cmd.WorkItemID)
				return err
			}); err != nil {
				return classifyWorkStoreError(err)
			}
			if item.String(colWorkWorkspaceID) != cmd.WorkspaceID.String() {
				return broken(http.StatusNotFound, "not_found")
			}
			if item.String(colWorkOwnerKind) != "agent" {
				return nil
			}
			if err := m.revalidateAgentWorkOwner(
				ctx, tenant, cmd.WorkspaceID, item,
			); err != nil {
				return err
			}
			ownerRef := item.String(colWorkOwnerRef)
			if cmd.HolderAgentRef != "" && cmd.HolderAgentRef != ownerRef {
				return broken(http.StatusUnprocessableEntity, "owner_ineligible")
			}
			matcher, ok := m.workIdentity.(WorkAuthenticatedAgentMatcher)
			if !ok {
				return unknown("evidence_unavailable", nil)
			}
			matches, err := matcher.AuthenticatedAgentMatches(
				ctx, tenant, ownerRef, principal.ActorRef,
			)
			if err != nil {
				return unknown("evidence_unavailable", err)
			}
			if matches {
				cmd.HolderAgentRef = ownerRef
				cmd.agentOwnerProven = true
			}
			return nil
		}
		proven, err := m.sessionHolderProven(ctx, tenant, principal, cmd.HolderSID)
		if err != nil {
			return err
		}
		cmd.holderSIDProven = proven
		if !proven {
			return nil
		}

		// Agent ownership is stored in the canonical core Identity.ID namespace,
		// while Principal.ActorRef and sessions_run.agent_ref are ExternalID. A
		// direct string comparison is therefore neither reachable nor a proof.
		// Re-establish the exact SID -> canonical owner relation for every
		// execution mutation and derive the canonical lease tuple server-side.
		var item model.Record
		if err := data.View(ctx, func(sc store.Scope) error {
			items, err := sc.Ext(workItemKind)
			if err != nil {
				return err
			}
			item, err = items.Get(ctx, cmd.WorkItemID)
			return err
		}); err != nil {
			return classifyWorkStoreError(err)
		}
		if item.String(colWorkWorkspaceID) != cmd.WorkspaceID.String() {
			return broken(http.StatusNotFound, "not_found")
		}
		if item.String(colWorkOwnerKind) != "agent" {
			return nil
		}
		if err := m.revalidateAgentWorkOwner(
			ctx, tenant, cmd.WorkspaceID, item,
		); err != nil {
			return err
		}
		ownerRef := item.String(colWorkOwnerRef)
		if cmd.HolderAgentRef != "" && cmd.HolderAgentRef != ownerRef {
			return broken(http.StatusUnprocessableEntity, "owner_ineligible")
		}
		if m.workIdentity == nil {
			return unknown("evidence_unavailable", nil)
		}
		acts, err := m.workIdentity.SessionActsForAgent(ctx, tenant, cmd.HolderSID, ownerRef)
		if err != nil {
			return unknown("evidence_unavailable", err)
		}
		if !acts {
			return broken(http.StatusUnprocessableEntity, "owner_ineligible")
		}
		cmd.HolderAgentRef = ownerRef
		cmd.agentOwnerProven = true
	case "lease.acquire", "lease.renew", "lease.release", "lease.takeover":
		if cmd.internal {
			cmd.leaseHolderResolved = true
			return nil
		}
		if !validCanonicalSID(cmd.HolderSID) {
			return broken(http.StatusBadRequest, "invalid_command")
		}
		var item model.Record
		var runAgentRef string
		err := data.View(ctx, func(sc store.Scope) error {
			items, err := sc.Ext(workItemKind)
			if err != nil {
				return err
			}
			item, err = items.Get(ctx, cmd.WorkItemID)
			if err != nil {
				return err
			}
			if cmd.HolderRunRef == "" {
				return nil
			}
			resolvedSID, err := resolveMerge(ctx, sc, cmd.HolderSID)
			if err != nil {
				return err
			}
			operatedRef, found, err := operatedRunRef(ctx, sc, resolvedSID)
			if err != nil {
				return err
			}
			if !found || operatedRef != cmd.HolderRunRef {
				return broken(http.StatusUnprocessableEntity, "owner_ineligible")
			}
			runs, err := sc.Ext(runKind)
			if err != nil {
				return err
			}
			run, err := findRunRec(ctx, runs, cmd.HolderRunRef)
			if err != nil {
				return err
			}
			runAgentRef = run.String(colRunAgentRef)
			return nil
		})
		if err != nil {
			return classifyWorkStoreError(err)
		}
		if item.String(colWorkWorkspaceID) != cmd.WorkspaceID.String() {
			return broken(http.StatusNotFound, "not_found")
		}
		// Every WorkLease holder is a live canonical session, regardless of
		// whether the WorkItem owner is that session or its agent. The owner check
		// below answers a different question and cannot substitute for liveness.
		if err := m.checkParticipant(ctx, tenant, cmd.WorkspaceID, "session", cmd.HolderSID); err != nil {
			return err
		}
		switch item.String(colWorkOwnerKind) {
		case "session":
			if cmd.HolderRunRef != "" {
				if cmd.HolderAgentRef != "" && cmd.HolderAgentRef != runAgentRef {
					return broken(http.StatusUnprocessableEntity, "owner_ineligible")
				}
				// A session-owned run may retain its authenticated ExternalID as
				// informational attribution; it is not the owner namespace.
				cmd.HolderAgentRef = runAgentRef
			}
			// owner_ref for a session-owned item is the CANONICAL SID, prefix and
			// all — "un SID canónico" in the kernel spec. It used to be the bare
			// uuid inside it, and that stripping is precisely how the composition
			// root came to hand a canonical sid to a core model.Session lookup: two
			// id spaces that look identical once the prefix is gone. Nothing is in
			// production, so the incompatible change is free today and expensive
			// after the release (canon §1.3).
			if item.String(colWorkOwnerRef) != cmd.HolderSID ||
				(cmd.HolderRunRef == "" && cmd.HolderAgentRef != "") {
				return broken(http.StatusUnprocessableEntity, "owner_ineligible")
			}
			if err := m.checkParticipant(ctx, tenant, cmd.WorkspaceID, "session", cmd.HolderSID); err != nil {
				return err
			}
			// The caller must be entitled to act for this SID. The two lines
			// above establish that the SID is the item's owner and a live
			// participant — properties of the SESSION, not of whoever typed the
			// request. This is the missing half, and it is the same question the
			// "agent" arm below asks with the same call.
			//
			// takeover is EXCLUDED, and the exclusion is the point rather than an
			// oversight. Takeover is by definition not the holder acting: its
			// authority is principal.Admin plus the observed fence plus, on a live
			// lease, Force and an effective Decision, all checked in
			// validateLeaseCommandInScope — which never consults
			// leasePrincipalMatches for it. Requiring the holder proof here made
			// an admin fail with owner_ineligible BEFORE reaching that
			// authorization, closing the only administrative recovery a
			// session-owned item has. That is the very path the decision recorded
			// on sessionHolderProven leans on to justify refusing a user actor, so
			// letting it close would have made this change contradict itself.
			if cmd.Command != "lease.takeover" {
				proven, err := m.sessionHolderProven(ctx, tenant, principal, cmd.HolderSID)
				if err != nil {
					return err
				}
				if !proven {
					return broken(http.StatusUnprocessableEntity, "owner_ineligible")
				}
				cmd.holderSIDProven = true
			}
		case "agent":
			ownerRef := item.String(colWorkOwnerRef)
			// owner_ref is a canonical Identity.ID. Never compare it with the
			// ExternalID read from sessions_run; SessionActsForAgent is the
			// composition seam that translates and proves those namespaces.
			if cmd.HolderAgentRef != "" && cmd.HolderAgentRef != ownerRef {
				return broken(http.StatusUnprocessableEntity, "owner_ineligible")
			}
			if cmd.Command != "lease.release" {
				if err := m.revalidateAgentWorkOwner(
					ctx, tenant, cmd.WorkspaceID, item,
				); err != nil {
					return err
				}
			}
			if m.workIdentity == nil {
				return unknown("evidence_unavailable", nil)
			}
			acts, err := m.workIdentity.SessionActsForAgent(ctx, tenant, cmd.HolderSID, ownerRef)
			if err != nil {
				return unknown("evidence_unavailable", err)
			}
			if !acts {
				return broken(http.StatusUnprocessableEntity, "owner_ineligible")
			}
			cmd.HolderAgentRef = ownerRef
			cmd.agentOwnerProven = true
		default:
			return broken(http.StatusUnprocessableEntity, "owner_ineligible")
		}
		cmd.leaseHolderResolved = true
	}
	return nil
}

func validateWorkReferences(ctx context.Context, sc store.Scope, workspace, parentID, supersedesID model.ID) error {
	repo, err := sc.Ext(workItemKind)
	if err != nil {
		return err
	}
	if !parentID.IsZero() {
		parent, err := repo.Get(ctx, parentID)
		if err != nil {
			return err
		}
		if parent.String(colWorkWorkspaceID) != workspace.String() {
			return broken(http.StatusNotFound, "not_found")
		}
	}
	if !supersedesID.IsZero() {
		old, err := repo.Get(ctx, supersedesID)
		if err != nil {
			return err
		}
		if old.String(colWorkWorkspaceID) != workspace.String() || !terminalWorkStatuses[old.String(colWorkStatus)] {
			return broken(http.StatusConflict, "illegal_transition")
		}
	}
	return nil
}

func (m *Module) validateStateCommand(ctx context.Context, sc store.Scope, tenant model.TenantID, principal WorkPrincipal, cmd WorkCommand, item model.Record) error {
	status := item.String(colWorkStatus)
	terminal := terminalWorkStatuses[status]
	switch cmd.Command {
	case "item.update":
		if status != "draft" && status != "ready" {
			return broken(http.StatusConflict, "illegal_transition")
		}
		changed, err := workUpdateChanges(item, cmd)
		if err != nil {
			return err
		}
		if !changed {
			return broken(http.StatusConflict, "state_conflict")
		}
	case "item.ready":
		if status != "draft" {
			return broken(http.StatusConflict, "illegal_transition")
		}
		if err := requiredAcceptanceExists(ctx, sc, model.ID(item.String(model.ColID))); err != nil {
			return err
		}
		if err := blockersCompleted(ctx, sc, model.ID(item.String(model.ColID))); err != nil {
			return err
		}
		if !cmd.participantResolved || item.String(colWorkOwnerKind) != cmd.OwnerKind || item.String(colWorkOwnerRef) != cmd.OwnerRef {
			return broken(http.StatusConflict, "stale_owner")
		}
	case "item.block":
		if status != "ready" && status != "active" && status != "review" {
			return broken(http.StatusConflict, "illegal_transition")
		}
		if principal.PurposeRestricted && status != "active" {
			return broken(http.StatusForbidden, "forbidden")
		}
		if status == "active" && (principal.PurposeRestricted || !principal.Admin) {
			return m.validateExecutionLeaseInScope(ctx, sc, principal, cmd, item)
		}
	case "item.unblock":
		if status != "blocked" {
			return broken(http.StatusConflict, "illegal_transition")
		}
		if err := blockersCompleted(ctx, sc, model.ID(item.String(model.ColID))); err != nil {
			return err
		}
		if err := m.requireNoLiveLeaseInScope(ctx, sc, item); err != nil {
			return err
		}
	case "item.submit":
		if status != "active" {
			return broken(http.StatusConflict, "illegal_transition")
		}
		if err := allAcceptanceEvaluated(ctx, sc, model.ID(item.String(model.ColID))); err != nil {
			return err
		}
		if err := m.validateExecutionLeaseInScope(ctx, sc, principal, cmd, item); err != nil {
			return err
		}
	case "item.complete":
		if status != "review" {
			return broken(http.StatusConflict, "illegal_transition")
		}
		if err := acceptanceComplete(ctx, sc, model.ID(item.String(model.ColID))); err != nil {
			return err
		}
		if err := m.requireNoLiveLeaseInScope(ctx, sc, item); err != nil {
			return err
		}
	case "item.fail":
		if status != "active" && status != "blocked" && status != "review" {
			return broken(http.StatusConflict, "illegal_transition")
		}
		if principal.PurposeRestricted && status != "active" {
			return broken(http.StatusForbidden, "forbidden")
		}
		if !principal.Admin && !principalIsWorkOwner(principal, cmd, item) {
			return broken(http.StatusForbidden, "forbidden")
		}
		if status == "active" && (principal.PurposeRestricted || !principal.Admin) {
			if err := m.validateExecutionLeaseInScope(ctx, sc, principal, cmd, item); err != nil {
				return err
			}
		}
	case "item.cancel":
		if terminal {
			return broken(http.StatusConflict, "illegal_transition")
		}
	case "item.archive":
		if !terminal || !item.IsNull(colWorkArchivedAt) {
			return broken(http.StatusConflict, "illegal_transition")
		}
		if !principal.Admin {
			return broken(http.StatusForbidden, "forbidden")
		}
	case "item.assign":
		if terminal {
			return broken(http.StatusConflict, "illegal_transition")
		}
		if !cmd.participantResolved {
			return unknown("evidence_unavailable", nil)
		}
		if !principal.Admin {
			return broken(http.StatusForbidden, "forbidden")
		}
	case "dependency.add":
		if status != "draft" && status != "ready" && status != "blocked" {
			return broken(http.StatusConflict, "illegal_transition")
		}
		if err := validateDependencyTarget(ctx, sc, item, cmd.DependsOnID); err != nil {
			return err
		}
		return dependencyWouldCycle(ctx, sc, recordID(item), cmd.DependsOnID, item.String(colWorkWorkspaceID))
	case "dependency.remove":
		if status != "draft" && status != "blocked" {
			return broken(http.StatusConflict, "illegal_transition")
		}
		return validateDependencyRemoval(ctx, sc, item, cmd.TargetID)
	case "acceptance.add":
		if status != "draft" {
			return broken(http.StatusConflict, "illegal_transition")
		}
		return validateAcceptanceAdd(ctx, sc, item, cmd.Acceptance[0])
	case "acceptance.update":
		if status != "draft" {
			return broken(http.StatusConflict, "illegal_transition")
		}
		return validateAcceptanceDefinitionUpdate(ctx, sc, item, cmd.CriterionID, cmd.Acceptance[0])
	case "acceptance.evaluate":
		if status != "active" && status != "review" {
			return broken(http.StatusConflict, "illegal_transition")
		}
		if principal.PurposeRestricted && status != "active" {
			return broken(http.StatusForbidden, "forbidden")
		}
		if status == "active" {
			if err := m.validateExecutionLeaseInScope(ctx, sc, principal, cmd, item); err != nil {
				return err
			}
		} else if err := m.requireNoLiveLeaseInScope(ctx, sc, item); err != nil {
			return err
		}
		return validateAcceptanceEvaluation(ctx, sc, item, cmd.CriterionID, cmd.Acceptance[0])
	case "decision.set", "decision.supersede", "decision.revoke":
		if terminal {
			return broken(http.StatusConflict, "illegal_transition")
		}
		return validateDecisionMutation(ctx, sc, item, cmd)
	case "lease.acquire", "lease.renew", "lease.release", "lease.takeover", "lease.revoke",
		"lease.expire", "lease.owner_died", "lease.clock_rebase":
		return m.validateLeaseCommandInScope(ctx, sc, principal, cmd, item)
	default:
		return broken(http.StatusBadRequest, "invalid_command")
	}
	return nil
}

// principalIsWorkOwner reports whether the caller IS this item's owner.
//
// For a user- or agent-owned item that is the actor identity itself, which is
// the comparison this replaced. It could never be true for a SESSION-owned item:
// model has exactly three actor kinds — user, agent and system
// (core/model/audit.go:117-124) — and none of them is "session", so
// `principal.ActorKind != "session"` held for every caller that has ever
// existed. That is the same asymmetry that made the session half of the lease
// unreachable, in the one other place a WorkItem asks who its owner is, and it
// is why item.fail is fixed here rather than left for a later session to
// rediscover.
//
// The owner of a session-owned item is represented by the caller PROVEN to act
// for the owning session — the proof preflightIdentity already established, read
// from the server-set flag rather than from the request body.
func principalIsWorkOwner(principal WorkPrincipal, cmd WorkCommand, item model.Record) bool {
	ownerKind, ownerRef := item.String(colWorkOwnerKind), item.String(colWorkOwnerRef)
	if ownerKind == "session" {
		return cmd.holderSIDProven && cmd.HolderSID != "" && cmd.HolderSID == ownerRef
	}
	if ownerKind == "agent" {
		return cmd.agentOwnerProven && cmd.HolderAgentRef == ownerRef
	}
	return principal.ActorKind == ownerKind && principal.ActorRef == ownerRef
}

func validateAcceptanceDefinitionUpdate(
	ctx context.Context,
	sc store.Scope,
	item model.Record,
	criterionID model.ID,
	input AcceptanceInput,
) error {
	repo, err := sc.Ext(workAcceptanceKind)
	if err != nil {
		return err
	}
	criterion, err := repo.Get(ctx, criterionID)
	if err != nil {
		return err
	}
	if criterion.String(colWorkItemID) != item.String(model.ColID) ||
		criterion.String(colWorkWorkspaceID) != item.String(colWorkWorkspaceID) {
		return broken(http.StatusNotFound, "not_found")
	}
	if criterion.String(colAccStatement) == input.Statement &&
		criterion.Int(colAccOrdinal) == input.Ordinal && criterion.Bool(colAccRequired) == input.Required {
		return broken(http.StatusConflict, "state_conflict")
	}
	return nil
}

func validateDependencyTarget(ctx context.Context, sc store.Scope, item model.Record, targetID model.ID) error {
	if model.ID(item.String(model.ColID)) == targetID {
		return broken(http.StatusConflict, "dependency_cycle")
	}
	repo, err := sc.Ext(workItemKind)
	if err != nil {
		return err
	}
	target, err := repo.Get(ctx, targetID)
	if err != nil {
		return err
	}
	if target.String(colWorkWorkspaceID) != item.String(colWorkWorkspaceID) {
		return broken(http.StatusNotFound, "not_found")
	}
	return nil
}

func validateDependencyRemoval(ctx context.Context, sc store.Scope, item model.Record, dependencyID model.ID) error {
	repo, err := sc.Ext(workDependencyKind)
	if err != nil {
		return err
	}
	dep, err := repo.Get(ctx, dependencyID)
	if err != nil {
		return err
	}
	if dep.String(colWorkItemID) != item.String(model.ColID) ||
		dep.String(colWorkWorkspaceID) != item.String(colWorkWorkspaceID) {
		return broken(http.StatusNotFound, "not_found")
	}
	if !dep.Bool(colDepActive) {
		return broken(http.StatusConflict, "target_closed")
	}
	return nil
}

func validateAcceptanceAdd(ctx context.Context, sc store.Scope, item model.Record, input AcceptanceInput) error {
	repo, err := sc.Ext(workAcceptanceKind)
	if err != nil {
		return err
	}
	rows, err := listAll(ctx, repo, model.Filter{
		Column: colWorkItemID, Op: model.OpEq, Value: item.String(model.ColID),
	})
	if err != nil {
		return err
	}
	if len(rows) >= 64 {
		return broken(http.StatusUnprocessableEntity, "acceptance_incomplete")
	}
	for _, row := range rows {
		if row.String(colAccKey) == input.Key {
			return broken(http.StatusConflict, "acceptance_duplicate")
		}
	}
	return nil
}

func validateAcceptanceEvaluation(
	ctx context.Context,
	sc store.Scope,
	item model.Record,
	criterionID model.ID,
	input AcceptanceInput,
) error {
	repo, err := sc.Ext(workAcceptanceKind)
	if err != nil {
		return err
	}
	criterion, err := repo.Get(ctx, criterionID)
	if err != nil {
		return err
	}
	if criterion.String(colWorkItemID) != item.String(model.ColID) ||
		criterion.String(colWorkWorkspaceID) != item.String(colWorkWorkspaceID) {
		return broken(http.StatusNotFound, "not_found")
	}
	from, to := criterion.String(colAccState), input.State
	legal := from == "pending" && to != "pending" ||
		from == "failed" && (to == "pending" || to == "passed" || to == "waived")
	if !legal {
		return broken(http.StatusConflict, "illegal_transition")
	}
	if to == "waived" {
		return effectiveDecision(ctx, sc, item, input.WaiverDecisionID)
	}
	return nil
}

func validateDecisionMutation(ctx context.Context, sc store.Scope, item model.Record, cmd WorkCommand) error {
	heads, err := sc.Ext(workDecisionHeadKind)
	if err != nil {
		return err
	}
	rows, err := listAll(ctx, heads,
		model.Filter{Column: colWorkItemID, Op: model.OpEq, Value: item.String(model.ColID)},
		model.Filter{Column: colDecisionKey, Op: model.OpEq, Value: cmd.DecisionKey},
	)
	if err != nil {
		return err
	}
	if len(rows) > 1 {
		return broken(http.StatusConflict, "state_conflict")
	}
	var head model.Record
	if len(rows) == 1 {
		head = rows[0]
	}
	switch cmd.Command {
	case "decision.set":
		if head != nil && head.String(colDecisionHeadState) != "revoked" {
			return broken(http.StatusConflict, "target_closed")
		}
	case "decision.supersede":
		if head == nil || head.String(colDecisionHeadState) != "effective" {
			return broken(http.StatusConflict, "stale_decision")
		}
	case "decision.revoke":
		if head == nil || head.String(colDecisionHeadState) != "effective" ||
			head.String(colDecisionCurrentID) != cmd.DecisionID.String() {
			return broken(http.StatusConflict, "stale_decision")
		}
		decisions, err := sc.Ext(workDecisionKind)
		if err != nil {
			return err
		}
		decision, err := decisions.Get(ctx, cmd.DecisionID)
		if err != nil {
			return err
		}
		if decision.String(colWorkItemID) != item.String(model.ColID) ||
			decision.String(colWorkWorkspaceID) != item.String(colWorkWorkspaceID) ||
			decision.String(colDecisionKey) != cmd.DecisionKey {
			return broken(http.StatusConflict, "stale_decision")
		}
	}
	return nil
}

func workUpdateChanges(item model.Record, cmd WorkCommand) (bool, error) {
	if cmd.Title != "" && cmd.Title != item.String(colWorkTitle) {
		return true, nil
	}
	if cmd.BriefMD != "" && cmd.BriefMD != item.String(colWorkBrief) {
		return true, nil
	}
	if cmd.Priority != "" && cmd.Priority != item.String(colWorkPriority) {
		return true, nil
	}
	if cmd.DueAt != "" && cmd.DueAt != item.String(colWorkDueAt) {
		return true, nil
	}
	if cmd.ContextRefs != nil {
		refs, err := canonicalJSON(cmd.ContextRefs)
		if err != nil {
			return false, err
		}
		if string(refs) != item.String(colWorkContextRefs) {
			return true, nil
		}
	}
	return false, nil
}

func listAll(ctx context.Context, repo store.GenericRepo, filters ...model.Filter) ([]model.Record, error) {
	q := model.Query{Filters: filters, Limit: 200}
	var out []model.Record
	for {
		rows, page, err := repo.List(ctx, q)
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
		if !page.HasMore || page.Cursor == "" {
			return out, nil
		}
		q.Cursor = page.Cursor
	}
}

func requiredAcceptanceExists(ctx context.Context, sc store.Scope, itemID model.ID) error {
	repo, err := sc.Ext(workAcceptanceKind)
	if err != nil {
		return err
	}
	rows, err := listAll(ctx, repo,
		model.Filter{Column: colWorkItemID, Op: model.OpEq, Value: itemID.String()},
		model.Filter{Column: colAccRequired, Op: model.OpEq, Value: true},
	)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return broken(http.StatusUnprocessableEntity, "acceptance_incomplete")
	}
	return nil
}

func allAcceptanceEvaluated(ctx context.Context, sc store.Scope, itemID model.ID) error {
	repo, err := sc.Ext(workAcceptanceKind)
	if err != nil {
		return err
	}
	rows, err := listAll(ctx, repo, model.Filter{Column: colWorkItemID, Op: model.OpEq, Value: itemID.String()})
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return broken(http.StatusUnprocessableEntity, "acceptance_incomplete")
	}
	for _, row := range rows {
		if row.String(colAccState) == "pending" {
			return broken(http.StatusUnprocessableEntity, "acceptance_incomplete")
		}
	}
	return nil
}

func acceptanceComplete(ctx context.Context, sc store.Scope, itemID model.ID) error {
	repo, err := sc.Ext(workAcceptanceKind)
	if err != nil {
		return err
	}
	rows, err := listAll(ctx, repo,
		model.Filter{Column: colWorkItemID, Op: model.OpEq, Value: itemID.String()},
		model.Filter{Column: colAccRequired, Op: model.OpEq, Value: true},
	)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return broken(http.StatusUnprocessableEntity, "acceptance_incomplete")
	}
	itemRepo, err := sc.Ext(workItemKind)
	if err != nil {
		return err
	}
	item, err := itemRepo.Get(ctx, itemID)
	if err != nil {
		return err
	}
	for _, row := range rows {
		switch row.String(colAccState) {
		case "passed":
		case "waived":
			if row.IsNull(colAccWaiverDecisionID) {
				return broken(http.StatusUnprocessableEntity, "acceptance_incomplete")
			}
			if err := effectiveDecision(ctx, sc, item, model.ID(row.String(colAccWaiverDecisionID))); err != nil {
				return broken(http.StatusUnprocessableEntity, "acceptance_incomplete")
			}
		default:
			return broken(http.StatusUnprocessableEntity, "acceptance_incomplete")
		}
	}
	return nil
}

func effectiveDecision(ctx context.Context, sc store.Scope, item model.Record, decisionID model.ID) error {
	decisions, err := sc.Ext(workDecisionKind)
	if err != nil {
		return err
	}
	decision, err := decisions.Get(ctx, decisionID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return broken(http.StatusUnprocessableEntity, "acceptance_incomplete")
		}
		return err
	}
	if decision.String(colWorkItemID) != item.String(model.ColID) ||
		decision.String(colWorkWorkspaceID) != item.String(colWorkWorkspaceID) {
		return broken(http.StatusUnprocessableEntity, "acceptance_incomplete")
	}
	heads, err := sc.Ext(workDecisionHeadKind)
	if err != nil {
		return err
	}
	rows, err := listAll(ctx, heads,
		model.Filter{Column: colWorkItemID, Op: model.OpEq, Value: item.String(model.ColID)},
		model.Filter{Column: colDecisionKey, Op: model.OpEq, Value: decision.String(colDecisionKey)},
	)
	if err != nil {
		return err
	}
	if len(rows) != 1 || rows[0].String(colDecisionHeadState) != "effective" ||
		rows[0].String(colDecisionCurrentID) != decisionID.String() {
		return broken(http.StatusUnprocessableEntity, "acceptance_incomplete")
	}
	return nil
}

func blockersCompleted(ctx context.Context, sc store.Scope, itemID model.ID) error {
	deps, err := sc.Ext(workDependencyKind)
	if err != nil {
		return err
	}
	rows, err := listAll(ctx, deps,
		model.Filter{Column: colWorkItemID, Op: model.OpEq, Value: itemID.String()},
		model.Filter{Column: colDepActive, Op: model.OpEq, Value: true},
	)
	if err != nil {
		return err
	}
	items, err := sc.Ext(workItemKind)
	if err != nil {
		return err
	}
	for _, dep := range rows {
		predecessor, err := items.Get(ctx, model.ID(dep.String(colDepDependsOnID)))
		if err != nil {
			return err
		}
		if predecessor.String(colWorkStatus) != "completed" {
			return broken(http.StatusUnprocessableEntity, "dependency_incomplete")
		}
	}
	return nil
}

// Apply revalidates and performs exactly one semantic mutation. Audit, domain
// rows, WorkEvent, outbox and receipt share the caller's transaction.
func (m *Module) Apply(ctx context.Context, tenant model.TenantID, principal WorkPrincipal, cmd WorkCommand) (CommandResult, error) {
	return m.applyWithData(ctx, m.workData(tenant), tenant, principal, cmd)
}

// applyWithData is the route-safe apply path. HTTP callers pass the request's
// pinned (and, when applicable, workspace-confined) handle so the mutation
// cannot widen back to the module's tenant-wide background handle.
func (m *Module) applyWithData(
	ctx context.Context,
	data workData,
	tenant model.TenantID,
	principal WorkPrincipal,
	cmd WorkCommand,
) (CommandResult, error) {
	cmd = normalizeWorkCommand(cmd)
	if tenant.IsZero() || tenant.IsSystem() {
		return CommandResult{}, broken(http.StatusBadRequest, "invalid_command")
	}
	if err := validateCommandSyntax(cmd); err != nil {
		return CommandResult{}, err
	}
	if _, err := model.ParseID(cmd.IdempotencyKey); err != nil || cmd.IdempotencyKey == "" {
		return CommandResult{}, broken(http.StatusBadRequest, "idempotency_key_required")
	}
	if cmd.Command != "item.create" && cmd.ExpectedVersion < 1 {
		return CommandResult{}, broken(http.StatusPreconditionRequired, "version_required")
	}
	// A live force-takeover is the exceptional path that invalidates authority
	// which the store has just proved is still live. The ordinary K1 apply path
	// permits callers to omit a plan precondition, but the kernel specification
	// makes a plan mandatory for this override. Keep planning possible (Plan has
	// no hash to present yet) and require the exact server-issued digest only at
	// actuation. plan_changed is the existing stable 412 for an absent or stale
	// plan precondition; malformed body hashes are invalid commands.
	if cmd.Command == "lease.takeover" && cmd.Force {
		if cmd.ExpectedPlanHash == "" {
			return CommandResult{}, broken(http.StatusPreconditionFailed, "plan_changed")
		}
		if _, err := decodeHash(cmd.ExpectedPlanHash, true); err != nil {
			return CommandResult{}, broken(http.StatusBadRequest, "invalid_command")
		}
	}
	cmd, err := hydrateWorkCommand(ctx, data, cmd)
	if err != nil {
		return CommandResult{}, classifyWorkStoreError(err)
	}
	actorFP, requestHash, idemHash, scope, err := commandHashes(principal, cmd)
	if err != nil {
		return CommandResult{}, broken(http.StatusBadRequest, "invalid_command")
	}
	// An exact retry is already a completed durable fact. Resolve it before
	// depending again on content/identity observers that may have become
	// unavailable since the original commit. The lookup is repeated inside the
	// mutation below to close the concurrent-first-delivery race.
	if replay, found, err := m.lookupReplayResult(ctx, data, actorFP, idemHash, scope, requestHash); err != nil {
		return CommandResult{}, classifyWorkStoreError(err)
	} else if found {
		replay.Replayed = true
		if refusal := workCommandResultRefusal(replay); refusal != nil {
			return replay, refusal
		}
		return replay, nil
	}
	if err := m.preflightContent(ctx, tenant, cmd); err != nil {
		return CommandResult{}, err
	}
	if err := m.preflightIdentity(ctx, data, tenant, principal, &cmd); err != nil {
		return CommandResult{}, err
	}
	if cmd.Command != "item.create" {
		if err := data.View(ctx, func(sc store.Scope) error {
			items, err := sc.Ext(workItemKind)
			if err != nil {
				return err
			}
			item, err := items.Get(ctx, cmd.WorkItemID)
			if err != nil {
				return err
			}
			return m.observeAgentWorkAuthority(
				ctx, tenant, cmd.WorkspaceID, principal, &cmd, item,
			)
		}); err != nil {
			return CommandResult{}, classifyWorkStoreError(err)
		}
	}
	var postCommitRefusal error
	cmd.postCommitRefusal = &postCommitRefusal

	var result CommandResult
	var auditGap bool
	var event WorkEventEnvelope
	var nudgeLimit int
	for graphAttempt := 0; ; graphAttempt++ {
		result, auditGap, event, nudgeLimit = CommandResult{}, false, WorkEventEnvelope{}, 0
		err = data.Mutate(ctx, func(sc store.Scope) error {
			if replay, found, err := findCommandReceipt(ctx, sc, actorFP, idemHash, scope, requestHash); err != nil {
				return err
			} else if found {
				result = replay
				result.Replayed = true
				return nil
			}

			clock, ok := sc.(store.TransactionClock)
			if !ok {
				return unknown("clock_unavailable", nil)
			}
			now, err := clock.TransactionNow(ctx)
			if err != nil {
				return unknown("clock_unavailable", err)
			}
			if workLeaseCommandNeedsClock(cmd.Command) {
				if cmd.Command == "lease.clock_rebase" {
					// Keep the rollback visible through planning. The audited domain
					// effect applies the rebase under this same transaction lock.
					if err := lockLeaseClock(ctx, sc, tenant, cmd.WorkspaceID); err != nil {
						return err
					}
				} else if err := advanceLeaseClock(ctx, sc, tenant, cmd, now); err != nil {
					return err
				}
				if err := lockWorkLeaseItem(ctx, sc, tenant, cmd.WorkspaceID, cmd.WorkItemID); err != nil {
					return err
				}
				if cmd.Command == "lease.takeover" {
					// The recovery token is checked before the WorkItem ETag so two
					// contenders report the lost lease generation as a 409. An
					// unrelated WorkItem edit with the same lease fence remains a 412.
					lease, found, err := findWorkLease(ctx, sc, cmd.WorkItemID)
					if err != nil {
						return err
					}
					if !found {
						return unknown("evidence_unavailable", nil)
					}
					if lease.Int(colLeaseFence) != cmd.Fence {
						return broken(http.StatusConflict, "stale_fence")
					}
				}
			}
			// If-Match is the first WorkItem-state decision in apply. A stale
			// caller always receives 412, even when newer state would also make
			// the requested domain transition illegal.
			if cmd.Command != "item.create" {
				items, err := sc.Ext(workItemKind)
				if err != nil {
					return err
				}
				versioned, err := items.Get(ctx, cmd.WorkItemID)
				if err != nil {
					return err
				}
				if versioned.Int(model.ColVersion) != cmd.ExpectedVersion {
					return broken(http.StatusPreconditionFailed, "version_mismatch")
				}
				// Agent identity/lifecycle rows are in the global authority-fact
				// order before sessions.claim. Keep K2 in that same order so a K3
				// transaction cannot hold Identity while waiting for Claim as K2
				// holds Claim while waiting for Identity.
				if err := m.revalidateAgentWorkOwnerInScope(
					ctx, sc, principal, cmd, versioned,
				); err != nil {
					return err
				}
				leaseCommand := cmd.Command == "lease.acquire" || cmd.Command == "lease.renew" ||
					cmd.Command == "lease.release" || cmd.Command == "lease.takeover"
				consumesLease := workCommandConsumesExecutionLease(cmd, versioned, principal) &&
					leasePrincipalMatches(principal, cmd)
				if leaseCommand || consumesLease {
					// Planning observes liveness without writes. Apply additionally
					// touches the Claim under this transaction so a concurrent release
					// or expiry participates in OCC with the WorkLease mutation.
					if err := m.touchLiveWorkHolderClaim(
						ctx, sc, cmd.HolderSID, principal.SessionFence,
					); err != nil {
						return err
					}
				}
			}
			if cmd.Command == "dependency.add" || cmd.Command == "dependency.remove" {
				if err := touchDependencyGuard(ctx, sc, cmd.WorkspaceID); err != nil {
					return err
				}
			}
			plan, current, err := m.planInScope(ctx, sc, tenant, principal, cmd, &now)
			if err != nil {
				return err
			}
			if cmd.ExpectedPlanHash != "" && cmd.ExpectedPlanHash != plan.PlanHash {
				return broken(http.StatusPreconditionFailed, "plan_changed")
			}
			planHash, err := decodeHash(plan.PlanHash, true)
			if err != nil {
				return err
			}
			commandID := model.NewID()
			auditEvent, err := sc.Audit().Append(ctx, model.AuditDraft{
				Actor: principal.Actor, ActorKind: principal.ActorKind,
				Action:     "sessions.work." + cmd.Command,
				TargetKind: workCommandKind, TargetID: commandID, PayloadHash: planHash,
				Meta: map[string]any{
					"workspace_id": cmd.WorkspaceID.String(), "work_item_id": cmd.WorkItemID.String(),
					"command_scope": scope,
				},
			})
			if err != nil {
				return err
			}
			if auditEvent.Seq == 0 {
				auditGap = true
				return nil // commit the ledger's gap accounting and nothing else
			}

			result, event, err = m.applyDomain(ctx, sc, tenant, principal, cmd, current, now, commandID, plan, auditEvent)
			if err != nil {
				return err
			}
			nudgeLimit = max(1, len(plan.EventTypes))
			response, err := canonicalJSON(result)
			if err != nil || len(response) > 16*1024 {
				return broken(http.StatusInternalServerError, "response_too_large")
			}
			receipts, err := sc.Ext(workCommandKind)
			if err != nil {
				return err
			}
			httpStatus := int64(http.StatusOK)
			if result.Verdict == VerdictBroken {
				httpStatus = http.StatusConflict
			}
			_, err = receipts.Create(ctx, model.Record{
				colWorkWorkspaceID: cmd.WorkspaceID.String(),
				colCommandID:       commandID.String(), colCommandActorFP: actorFP,
				colCommandScope: scope, colCommandIdempotency: idemHash,
				colCommandRequestHash: requestHash, colCommandPlanHash: planHash,
				colCommandResultKind: result.ResultKind,
				colCommandResultID:   nullableID(result.ResultID),
				colCommandHTTPStatus: httpStatus, colCommandResponse: string(response),
				colCommandResponseHash: hashBytes(response), colCommandAuditSeq: auditEvent.Seq,
				colCommandAuditHash: auditEvent.Hash, colCommandCompletedAt: now.String(),
			})
			return err
		})
		if errors.Is(err, errDependencyGuardRaced) && graphAttempt < 3 {
			// The guard is an OCC serialization point. Reopen a fresh transaction
			// so an opposing edge is reported as dependency_cycle, while two
			// independent edges both get a chance to commit.
			continue
		}
		break
	}
	if err != nil {
		err = classifyWorkStoreError(err)
		// A concurrent first delivery may have won the unique receipt race. The
		// failed transaction is unusable on PostgreSQL, so reopen a fresh read.
		if errors.Is(err, store.ErrConflict) || (asWorkError(err) != nil && asWorkError(err).code == "state_conflict") {
			if replay, found, replayErr := m.lookupReplay(ctx, data, actorFP, idemHash, scope, requestHash); replayErr != nil {
				return CommandResult{}, classifyWorkStoreError(replayErr)
			} else if found {
				replay.Replayed = true
				if refusal := workCommandResultRefusal(replay); refusal != nil {
					return replay, refusal
				}
				return replay, nil
			}
		}
		return CommandResult{}, err
	}
	if auditGap {
		return CommandResult{}, unknown("evidence_unavailable", nil)
	}
	if !event.EventID.IsZero() {
		// Durability does not depend on this nudge. A failure leaves the outbox
		// pending; the periodic pump can retry the same stable event id. Preserve
		// the caller's workspace confinement for this opportunistic delivery.
		// A request-confined nudge may retry, but the tenant-wide leader pump owns
		// the tenth attempt because dead-lettering also creates a tenant Finding.
		_ = m.drainWorkOutboxWithData(ctx, data, tenant, nudgeLimit, false)
	}
	if refusal := workCommandResultRefusal(result); refusal != nil {
		return result, refusal
	}
	return result, nil
}

func workCommandResultRefusal(result CommandResult) error {
	if result.Verdict == VerdictBroken && result.Code == "fence_exhausted" {
		return mapWorkFenceError(ErrFenceExhausted)
	}
	return nil
}

func hydrateWorkCommand(ctx context.Context, data workData, cmd WorkCommand) (WorkCommand, error) {
	if cmd.Command == "item.create" {
		return cmd, nil
	}
	err := data.View(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(workItemKind)
		if err != nil {
			return err
		}
		item, err := repo.Get(ctx, cmd.WorkItemID)
		if err != nil {
			return err
		}
		storedWorkspace := model.ID(item.String(colWorkWorkspaceID))
		if !cmd.WorkspaceID.IsZero() && cmd.WorkspaceID != storedWorkspace {
			return store.ErrNotFound
		}
		cmd.WorkspaceID = storedWorkspace
		if cmd.Command == "item.ready" {
			cmd.OwnerKind, cmd.OwnerRef = item.String(colWorkOwnerKind), item.String(colWorkOwnerRef)
		}
		return nil
	})
	return cmd, err
}

func commandHashes(principal WorkPrincipal, cmd WorkCommand) ([]byte, []byte, []byte, string, error) {
	if principal.Actor == "" || principal.ActorKind == "" || principal.ActorRef == "" {
		return nil, nil, nil, "", fmt.Errorf("principal is not attributable")
	}
	scope := cmd.CommandScope
	if scope == "" {
		scope = cmd.Command
		if !cmd.WorkItemID.IsZero() {
			scope += ":" + cmd.WorkItemID.String()
		}
	}
	actor := sha256.Sum256([]byte(principal.ActorKind + "\x00" + principal.ActorRef + "\x00" + principal.Actor))
	idem := sha256.Sum256([]byte(cmd.IdempotencyKey))
	req := struct {
		Command          WorkCommand `json:"command"`
		Method           string      `json:"method"`
		Scope            string      `json:"scope"`
		ExpectedVersion  int64       `json:"expected_version"`
		ExpectedPlanHash string      `json:"expected_plan_hash"`
	}{cmd, cmd.HTTPMethod, scope, cmd.ExpectedVersion, cmd.ExpectedPlanHash}
	b, err := canonicalJSON(req)
	if err != nil {
		return nil, nil, nil, "", err
	}
	request := sha256.Sum256(b)
	return actor[:], request[:], idem[:], scope, nil
}

func findCommandReceipt(ctx context.Context, sc store.Scope, actorFP, idemHash []byte, scope string, requestHash []byte) (CommandResult, bool, error) {
	repo, err := sc.Ext(workCommandKind)
	if err != nil {
		return CommandResult{}, false, err
	}
	rows, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{
		{Column: colCommandActorFP, Op: model.OpEq, Value: actorFP},
		{Column: colCommandScope, Op: model.OpEq, Value: scope},
		{Column: colCommandIdempotency, Op: model.OpEq, Value: idemHash},
	}, Limit: 1})
	if err != nil || len(rows) == 0 {
		return CommandResult{}, false, err
	}
	if !bytesEqual(rows[0].Bytes(colCommandRequestHash), requestHash) {
		return CommandResult{}, false, broken(http.StatusConflict, "idempotency_key_reused")
	}
	response := []byte(rows[0].String(colCommandResponse))
	if !bytesEqual(rows[0].Bytes(colCommandResponseHash), hashBytes(response)) {
		return CommandResult{}, false, unknown("evidence_unavailable", errors.New("command receipt response digest mismatch"))
	}
	var result CommandResult
	if err := json.Unmarshal(response, &result); err != nil {
		return CommandResult{}, false, unknown("evidence_unavailable", err)
	}
	validResult := result.Verdict == VerdictClean && result.Code == "applied" ||
		result.Verdict == VerdictBroken && result.Code == "fence_exhausted"
	if !validResult ||
		result.CommandID.String() != rows[0].String(colCommandID) ||
		result.ResultKind != rows[0].String(colCommandResultKind) ||
		result.ResultID.String() != rows[0].String(colCommandResultID) ||
		result.PlanHash != hexHash(rows[0].Bytes(colCommandPlanHash)) ||
		result.AuditSeq != rows[0].Int(colCommandAuditSeq) || result.EventID.IsZero() ||
		result.EventSeq < 1 || result.OwnerEpoch < 1 || result.LeaseFence < 0 {
		return CommandResult{}, false, unknown("evidence_unavailable", errors.New("command receipt response anchors mismatch"))
	}
	events, err := sc.Ext(workEventKind)
	if err != nil {
		return CommandResult{}, false, err
	}
	eventRows, _, err := events.List(ctx, model.Query{Filters: []model.Filter{{
		Column: colEventID, Op: model.OpEq, Value: result.EventID.String(),
	}}, Limit: 1})
	if err != nil {
		return CommandResult{}, false, err
	}
	if len(eventRows) != 1 || eventRows[0].String(colEventCommandID) != result.CommandID.String() ||
		eventRows[0].Int(colEventSeq) != result.EventSeq ||
		eventRows[0].Int(colEventAuditSeq) != result.AuditSeq ||
		!bytesEqual(eventRows[0].Bytes(colEventAuditHash), rows[0].Bytes(colCommandAuditHash)) ||
		!bytesEqual(eventRows[0].Bytes(colEventPayloadHash), hashBytes([]byte(eventRows[0].String(colEventPayload)))) {
		return CommandResult{}, false, unknown("evidence_unavailable", errors.New("command receipt event anchors mismatch"))
	}
	return result, true, nil
}

func (m *Module) lookupReplay(ctx context.Context, data workData, actorFP, idemHash []byte, scope string, requestHash []byte) (CommandResult, bool, error) {
	out, found, err := m.lookupReplayResult(ctx, data, actorFP, idemHash, scope, requestHash)
	if err != nil {
		return CommandResult{}, false, err
	}
	return out, found, nil
}

func (m *Module) lookupReplayResult(
	ctx context.Context,
	data workData,
	actorFP, idemHash []byte,
	scope string,
	requestHash []byte,
) (CommandResult, bool, error) {
	var out CommandResult
	var found bool
	if err := data.View(ctx, func(sc store.Scope) error {
		var err error
		out, found, err = findCommandReceipt(ctx, sc, actorFP, idemHash, scope, requestHash)
		return err
	}); err != nil {
		return CommandResult{}, false, err
	}
	return out, found, nil
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

func nullableID(id model.ID) any {
	if id.IsZero() {
		return nil
	}
	return id.String()
}
