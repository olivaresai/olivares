// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package orchestration

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// Decision ledger ops (the immutable governance-evidence verbs).
const (
	opFireRequest = "fire_request" // phase 1: a fire requested approval; awaiting decision
	opFire        = "fire"         // phase 2: a fire was decided (blocked/dispatched/declared_not_fired)
	opCadenceMiss = "cadence_miss" // anti-evasion: an active recurring schedule went overdue
)

// actionScheduleActivate is the governed action a require_approval routine
// declaration consumes. It is deliberately "activate" and not
// "create": every way INTO the active population needs the same human decision,
// or "create paused, then activate" is the loophole.
//
// It is deliberately NOT added to governance's ksActuationActions: that set is
// documented as halting the AGENTIC estate, and declaring desired state
// actuates nothing (an actual fire stays blocked by the kill switch on its own).
const actionScheduleActivate = "orchestration.schedule.activate"

// Decision op_status values (the read-first state of a fire/miss).
const (
	opStatusRequested        = "requested"          // approval requested; awaiting decision
	opStatusBlocked          = "blocked"            // denied (no/expired/rejected approval) — deny-by-default
	opStatusDispatched       = "dispatched"         // a real dispatcher actuated the fire
	opStatusDeclaredNotFired = "declared_not_fired" // approved, but no dispatcher wired — declared, not actuated
	opStatusFailed           = "failed"             // the dispatcher errored
	opStatusMissed           = "missed"             // cadence-miss recorded
	// FIN-08 budget enforcement (Denial-of-Wallet): an approved fire denied because an
	// enforcing budget that scopes it is at its cap. block is a hard cap; throttle is a
	// soft cap (this period's budget exhausted, retry next period / after a top-up).
	opStatusBudgetBlocked   = "budget_blocked"
	opStatusBudgetThrottled = "budget_throttled"
)

// Schedule field bounds (docs/SECURITY-HARDENING.md: operator input is bounded, not trusted).
const (
	minInterval  = 60       // 1 minute floor: a tighter cadence over-fires the miss check
	maxInterval  = 31622400 // ~366 days ceiling
	defaultGrace = 2
	maxGrace     = 10
)

// Valid enums for a schedule declaration.
var (
	validSubjectKinds = map[string]bool{"agent": true, "swarm": true}
	validTriggerKinds = map[string]bool{"cron": true, "event": true, "manual": true}
	validStatuses     = map[string]bool{"active": true, "paused": true, "retired": true}
)

// createScheduleRequest declares a governed schedule (desired state only).
type createScheduleRequest struct {
	Name                    string `json:"name"`
	SubjectKind             string `json:"subject_kind"`
	SubjectRef              string `json:"subject_ref"`
	TriggerKind             string `json:"trigger_kind"`
	CadenceSpec             string `json:"cadence_spec"`
	ExpectedIntervalSeconds int64  `json:"expected_interval_seconds"`
	GraceFactor             int64  `json:"grace_factor"`
	// ApprovalRef carries phase 2 of a declaration that a routine policy
	// requires a human to approve (require_approval). It rides the
	// existing body rather than a new route, exactly like the two-phase fire.
	ApprovalRef string `json:"approval_ref,omitempty"`
}

// patchScheduleRequest partially updates a schedule (enable/disable/retarget). Each
// pointer is applied only when present, so a PATCH never clobbers an omitted field.
type patchScheduleRequest struct {
	DesiredStatus           *string `json:"desired_status"`
	SubjectRef              *string `json:"subject_ref"`
	CadenceSpec             *string `json:"cadence_spec"`
	ExpectedIntervalSeconds *int64  `json:"expected_interval_seconds"`
	GraceFactor             *int64  `json:"grace_factor"`
	// ApprovalRef is phase 2 when this patch ACTIVATES the routine and a policy
	// requires approval.
	ApprovalRef string `json:"approval_ref,omitempty"`
}

// fireRequest is the body of a two-phase fire. An empty ApprovalRef is phase 1
// (request an approval); a present ApprovalRef is phase 2 (consume the decision).
type fireRequest struct {
	ApprovalRef string `json:"approval_ref"`
}

// fireResponse reports the outcome of a fire phase.
type fireResponse struct {
	Op               string     `json:"op"`
	OpStatus         string     `json:"op_status"`
	PlanHash         string     `json:"plan_hash"`
	ApprovalRef      string     `json:"approval_ref,omitempty"`
	GateStatus       GateStatus `json:"gate_status"`
	DispatchRef      string     `json:"dispatch_ref,omitempty"`
	RequiresApproval bool       `json:"requires_approval,omitempty"`
	Detail           string     `json:"detail,omitempty"`
}

// validInterval checks an expected_interval_seconds: 0 disables the miss check,
// otherwise it must lie within the bounds.
func validInterval(s int64) bool { return s == 0 || (s >= minInterval && s <= maxInterval) }

// planHashOfSchedule binds an approval to the EXACT schedule + cadence a human saw
// AND the effective operator dispatcher generation (item 6c): a re-target,
// re-cadence OR a config reload (attacker image/URL under the same subject) changes
// the hash and voids a stale approval. Canonical (length-prefixed) preimage so a
// controllable subject_ref/cadence cannot collide across field boundaries.
func (m *Module) planHashOfSchedule(rec model.Record) string {
	return canonicalHash("orchestration.schedule.plan.v2",
		rec.String(model.ColID), rec.String(colSubjectRef), rec.String(colCadenceSpec),
		strconv.FormatInt(rec.Int(colExpectedIvl), 10),
		m.dispatchGen.Generation(rec.String(colSubjectKind), rec.String(colSubjectRef)))
}

// decisionRow is one row of the append-only fire/miss ledger.
type decisionRow struct {
	subjectKind string
	subjectRef  string
	scheduleRef string
	op          string
	planHash    string
	approvalRef string
	gateStatus  GateStatus
	opStatus    string
	dispatchRef string
	actor       string
	actorKind   string
	detail      string
	result      string
}

// recordDecision appends one immutable decision row (the deploy_operation shape).
// The sensitive detail is reduced to a one-way detail_hash.
func (m *Module) recordDecision(ctx context.Context, sc store.Scope, d decisionRow) error {
	repo, err := sc.Ext(decisionKind)
	if err != nil {
		return err
	}
	rec := model.Record{
		colDecSubjectKind: d.subjectKind, colDecSubjectRef: clamp(d.subjectRef, maxRefLen),
		colOp: d.op, colGateStatus: string(d.gateStatus), colOpStatus: d.opStatus,
		colActor: d.actor, colActorKind: d.actorKind, colOccurredAt: m.clock.Now().String(),
	}
	setIf(rec, colScheduleRef, d.scheduleRef)
	setIf(rec, colPlanHash, d.planHash)
	setIf(rec, colApprovalRef, d.approvalRef)
	setIf(rec, colDispatchRef, d.dispatchRef)
	if d.detail != "" {
		rec[colDetailHash] = hashHex(d.detail)
	}
	if d.result != "" {
		rec[colResult] = clamp(d.result, maxNameLen)
	}
	_, err = repo.Create(ctx, rec)
	return err
}

// recordBlocked records a denied fire to the append-only ledger in its own
// transaction, so it persists regardless of the request outcome (best-effort).
func (m *Module) recordBlocked(ctx context.Context, mc api.ModuleContext, id model.ID, d decisionRow) {
	if err := mc.Data.Mutate(ctx, func(sc store.Scope) error {
		if err := m.recordDecision(ctx, sc, d); err != nil {
			return err
		}
		return auditEvent(ctx, sc, mc, "orchestration.schedule.fire.blocked", scheduleKind, id,
			map[string]any{"gate_status": string(d.gateStatus), "approval_ref": d.approvalRef})
	}); err != nil {
		m.errorf("orchestration: failed to record blocked-fire evidence", "schedule", id.String(), "err", err)
	}
}

// lastSubjectActivity returns the subject's most recent observed activity timestamp,
// derived from the relation table (the subject appearing as a supervisor or a
// worker). It is the read-time liveness signal for the cadence check — no per-edge
// schedule write is needed.
func (m *Module) lastSubjectActivity(ctx context.Context, sc store.Scope, subjectRef string) (string, error) {
	repo, err := sc.Ext(relationKind)
	if err != nil {
		return "", err
	}
	mostRecent := func(col string) (string, error) {
		recs, _, lerr := repo.List(ctx, model.Query{
			Filters: []model.Filter{eq(col, subjectRef)},
			Sort:    []model.Sort{{Column: colLastSeenAt, Desc: true}},
			Limit:   1,
		})
		if lerr != nil {
			return "", lerr
		}
		if len(recs) == 0 {
			return "", nil
		}
		return recs[0].String(colLastSeenAt), nil
	}
	asSup, err := mostRecent(colSupervisorRef)
	if err != nil {
		return "", err
	}
	asWorker, err := mostRecent(colWorkerRef)
	if err != nil {
		return "", err
	}
	return maxTS(asSup, asWorker), nil
}

// missedSubject is a schedule whose cadence was newly missed, collected during the
// scan so the finding is emitted on the bus AFTER the scan transaction commits.
type missedSubject struct {
	subjectKind, subjectRef, scheduleRef, detail string
}

// RunCadenceScan runs the tenant-scoped cadence-miss scan for mc's tenant. It is
// the exported seam for the composition root's leader-gated cross-tenant pump
// (cmd/olivares orchcadencepump.go): a module cannot enumerate tenants
// itself, so unattended detection needs the root to drive the same check the
// read handlers piggyback on. mc needs Tenant and Data only (no Principal — the
// scan self-attributes its decisions).
func (m *Module) RunCadenceScan(ctx context.Context, mc api.ModuleContext) {
	m.runCadenceScan(ctx, mc)
}

// runCadenceScan is the read-time, tenant-pinned anti-evasion check (docs/SECURITY-HARDENING.md). A
// module cannot enumerate tenants, so there WAS no background cross-tenant scan: the
// check runs over the request's single authorized tenant on a read, and since
// also on the composition root's periodic pump (RunCadenceScan above), so detection
// no longer depends on a human listing schedules. For every
// ACTIVE schedule with a positive expected interval, it compares the subject's
// observed activity (plus last_fired/created) against interval*grace; a newly
// overdue schedule gets a sticky missed_at + a cadence_miss decision + a Finding,
// and a recovered one clears its marker. A one-shot/event/paused schedule (interval
// 0 or not active) is never flagged — finishing is honest silence, not evasion.
func (m *Module) runCadenceScan(ctx context.Context, mc api.ModuleContext) {
	var newly []missedSubject
	err := mc.Data.Mutate(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(scheduleKind)
		if err != nil {
			return err
		}
		actives, err := listAll(ctx, repo, eq(colDesiredStat, "active"))
		if err != nil {
			return err
		}
		now := m.clock.Now().Time()
		for _, s := range actives {
			// Only a cron (recurring) schedule has a cadence to miss. An event-driven
			// or manual schedule going quiet is NOT evasion — emitting a miss for it
			// would be a fabricated signal (docs/SECURITY-HARDENING.md).
			if s.String(colTriggerKind) != "cron" {
				continue
			}
			ivl := s.Int(colExpectedIvl)
			if ivl <= 0 {
				continue
			}
			grace := s.Int(colGraceFactor)
			if grace < 1 {
				grace = 1
			}
			subjectRef := s.String(colSubjectRef)
			observed, err := m.lastSubjectActivity(ctx, sc, subjectRef)
			if err != nil {
				return err
			}
			reference := maxTS(observed, s.String(colLastFiredAt), s.String(model.ColCreatedAt))
			overdue := isOverdue(reference, now, time.Duration(ivl*grace)*time.Second)
			missed := s.String(colMissedAt) != ""
			switch {
			case overdue && !missed:
				s[colMissedAt] = model.NewTimestamp(now).String()
				detail := fmt.Sprintf("schedule:%s subject:%s overdue (interval=%ds grace=%d)", s.String(colSchedName), subjectRef, ivl, grace)
				if err := m.persistFinding(ctx, sc, finding{
					kind: busCadenceMiss, severity: sdkmodel.SeverityHigh, subjectKind: s.String(colSubjectKind),
					subjectRef: subjectRef, title: "scheduled agent stopped emitting vs its cadence", detail: detail,
					meta: map[string]any{"schedule": s.String(colSchedName)},
				}); err != nil {
					return err
				}
				if err := m.recordDecision(ctx, sc, decisionRow{
					subjectKind: s.String(colSubjectKind), subjectRef: subjectRef, scheduleRef: s.String(model.ColID),
					op: opCadenceMiss, opStatus: opStatusMissed, gateStatus: StatusNotRequired,
					actor: model.ActorSystem, actorKind: model.ActorSystem, detail: detail, result: "cadence miss detected",
				}); err != nil {
					return err
				}
				if _, err := repo.Update(ctx, s); err != nil {
					return err
				}
				newly = append(newly, missedSubject{s.String(colSubjectKind), subjectRef, s.String(model.ColID), detail})
			case !overdue && missed:
				s[colMissedAt] = nil // recovered: clear the sticky marker
				if _, err := repo.Update(ctx, s); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		m.errorf("orchestration: cadence scan failed", "err", err)
		return
	}
	for _, ms := range newly {
		m.emitFinding(ctx, mc.Tenant, busCadenceMiss, sdkmodel.SeverityHigh, ms.subjectKind, ms.subjectRef,
			"scheduled agent stopped emitting vs its cadence", ms.detail)
	}
}

// isOverdue reports whether now is more than window past the reference timestamp.
// An unparseable/empty reference is treated as NOT overdue (a fresh schedule with no
// activity yet is not a miss — its created_at is part of the reference).
func isOverdue(reference string, now time.Time, window time.Duration) bool {
	t, err := model.ParseTimestamp(reference)
	if err != nil {
		return false
	}
	return now.Sub(t.Time()) > window
}

// handleListSchedules lists governed schedules. It runs the cadence scan first so a
// just-missed schedule is reflected, then derives each subject's last observed
// activity for display.
func (m *Module) handleListSchedules(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.runCadenceScan(r.Context(), mc)
	q := listQuery(r)
	out := listResponse[scheduleDTO]{Items: []scheduleDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(scheduleKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			observed, oerr := m.lastSubjectActivity(r.Context(), sc, rec.String(colSubjectRef))
			if oerr != nil {
				return oerr
			}
			out.Items = append(out.Items, toScheduleDTO(rec, observed))
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

// handleCreateSchedule declares a governed schedule (write-tier, self-audited). The
// declaring principal is captured as owner_actor — the accountable principal for any
// later autonomous fire.
func (m *Module) handleCreateSchedule(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in createScheduleRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Name == "" || in.SubjectRef == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("name and subject_ref are required"))
		return
	}
	if !validSubjectKinds[in.SubjectKind] || !validTriggerKinds[in.TriggerKind] {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid subject_kind or trigger_kind"))
		return
	}
	if !validInterval(in.ExpectedIntervalSeconds) {
		writeJSON(w, http.StatusBadRequest, errorBody("expected_interval_seconds must be 0 or between 60 and 31622400"))
		return
	}
	if in.ExpectedIntervalSeconds > 0 && in.TriggerKind != "cron" {
		writeJSON(w, http.StatusBadRequest, errorBody("expected_interval_seconds (cadence-miss check) is only meaningful for a cron trigger"))
		return
	}
	grace := in.GraceFactor
	if grace == 0 {
		grace = defaultGrace
	}
	if grace < 1 || grace > maxGrace {
		writeJSON(w, http.StatusBadRequest, errorBody("grace_factor must be between 1 and 10"))
		return
	}

	// routine governance. The declaring principal's axes are FROZEN on
	// the row here and are what every later patch/restore/fire resolves policy
	// from, so a more privileged principal acting on this routine cannot step
	// outside its owner's policy.
	scope := routineScopeOfPrincipal(mc)
	pol, denial := m.resolvePolicy(r.Context(), mc.Tenant, scope)
	if denial != nil {
		m.auditRoutineDenial(r.Context(), mc, "orchestration.schedule.create.policy_denied", "", denial)
		writeRoutineDenial(w, denial)
		return
	}
	shape := routineShape{
		triggerKind: in.TriggerKind, cadenceSpec: in.CadenceSpec,
		intervalSec: in.ExpectedIntervalSeconds, graceFactor: grace,
		subjectKind: in.SubjectKind, subjectRef: in.SubjectRef,
		active: true, // create always declares an ACTIVE routine
	}
	if d := m.checkDeclaration(r.Context(), pol, shape); d != nil {
		m.auditRoutineDenial(r.Context(), mc, "orchestration.schedule.create.policy_denied", "", d)
		writeRoutineDenial(w, d)
		return
	}
	// require_approval: a HITL decision bound to THIS exact declaration.
	if pol.RequireApproval {
		if done := m.routineApproval(w, r, mc, routineApprovalReq{
			planHash: createPlanHash(mc.Tenant, in, grace, mc.Principal.Actor(), scope, pol),
			subject:  "pending-schedule:" + clamp(in.Name, maxNameLen),
			ref:      in.ApprovalRef, auditID: "",
			auditAction: "orchestration.schedule.create.policy_denied", pol: pol,
		}); done {
			return
		}
	}

	var dto scheduleDTO
	var capDenial *routineDenial
	err := m.withAdmissionFence(r.Context(), mc, len(pol.ActiveCaps) > 0, func(sc store.Scope) error {
		// The cap is checked INSIDE the fenced transaction that inserts, so a
		// concurrent admitter cannot slip past the same count. It runs BEFORE
		// the approval is spent: a declaration refused for capacity must not
		// burn the human decision and force the operator to seek a new one.
		d, aerr := admitActive(r.Context(), sc, pol, scope, "")
		if aerr != nil {
			return aerr
		}
		if d != nil {
			capDenial = d
			return nil
		}
		// Spend the approval EXACTLY once, in the same transaction as the row
		// it authorizes, so a replay cannot mint a second routine.
		if pol.RequireApproval {
			cd, cerr := claimActivationApproval(r.Context(), sc, in.ApprovalRef, "", createPlanHash(mc.Tenant, in, grace, mc.Principal.Actor(), scope, pol))
			if cerr != nil {
				return cerr
			}
			if cd != nil {
				capDenial = cd
				return nil
			}
		}
		repo, err := sc.Ext(scheduleKind)
		if err != nil {
			return err
		}
		rec := model.Record{
			colSchedName: clamp(in.Name, maxNameLen), colSubjectKind: in.SubjectKind,
			colSubjectRef: clamp(normSubjectRef(in.SubjectRef), maxRefLen), colTriggerKind: in.TriggerKind,
			colExpectedIvl: in.ExpectedIntervalSeconds, colGraceFactor: grace,
			colDesiredStat: "active", colOwnerActor: mc.Principal.Actor(), colOwnerActorK: mc.Principal.ActorKind(),
		}
		setIf(rec, colCadenceSpec, clamp(in.CadenceSpec, maxRefLen))
		setIf(rec, colOwnerUserRef, scope.UserRef)
		setIf(rec, colWorkspaceRef, scope.WorkspaceRef)
		created, err := repo.Create(r.Context(), rec)
		if err != nil {
			return err
		}
		dto = toScheduleDTO(created, "")
		if err := appendRevision(r.Context(), sc, mc, model.ID(created.String(model.ColID)), revOpCreate, dto); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "orchestration.schedule.create", scheduleKind, model.ID(created.String(model.ColID)),
			activationMeta(pol, in.ApprovalRef, map[string]any{
				"name": dto.Name, "subject_ref": dto.SubjectRef, "trigger_kind": dto.TriggerKind}))
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if capDenial != nil {
		m.auditRoutineDenial(r.Context(), mc, "orchestration.schedule.create.policy_denied", "", capDenial)
		writeRoutineDenial(w, capDenial)
		return
	}
	writeJSON(w, http.StatusCreated, dto)
}

// createPlanHash binds a create approval to the EXACT declaration phase 2 will
// persist. A create has no row id yet, so — unlike planHashOfSchedule — the
// binding is over the normalized declaration itself PLUS the owner scope and
// the policy posture that demanded the approval: a different principal cannot
// consume the approved declaration and become its owner, and a policy change
// between the two phases voids the approval instead of silently riding it.
func createPlanHash(tenant model.TenantID, in createScheduleRequest, grace int64, ownerActor string, scope RoutineScope, pol RoutinePolicy) string {
	return canonicalHash("orchestration.schedule.create.plan.v1",
		tenant.String(), clamp(in.Name, maxNameLen), in.SubjectKind, clamp(in.SubjectRef, maxRefLen),
		in.TriggerKind, clamp(in.CadenceSpec, maxRefLen),
		strconv.FormatInt(in.ExpectedIntervalSeconds, 10), strconv.FormatInt(grace, 10),
		// The OWNER ACTOR, not only the user/workspace axes: two service tokens
		// share an empty user and workspace, so without this a token could take
		// another token's approved declaration and land it owning the routine.
		ownerActor,
		scope.UserRef, scope.WorkspaceRef, pol.Digest)
}

// activationMeta adds the governance provenance of an ADMITTED activation to an
// audit event: which approval authorized it, which policies demanded one, and
// the composed posture. Without it the trail records that a routine became
// active but not that (or by whose decision) it was approved — and a replay of
// the same approval would be byte-identical to the first, legitimate use.
func activationMeta(pol RoutinePolicy, approvalRef string, meta map[string]any) map[string]any {
	if meta == nil {
		meta = map[string]any{}
	}
	if len(pol.PolicyRefs) > 0 {
		meta["policy_refs"] = pol.PolicyRefs
	}
	if pol.Digest != "" {
		meta["policy_digest"] = pol.Digest
	}
	if pol.RequireApproval {
		meta["required_approval"] = true
		if approvalRef != "" {
			meta["approval_ref"] = approvalRef
		}
	}
	return meta
}

// routineApprovalReq describes one require_approval gate consultation.
type routineApprovalReq struct {
	planHash    string
	subject     string
	ref         string
	auditID     model.ID
	auditAction string
	pol         RoutinePolicy
}

// routineApproval runs the two-phase HITL for a routine that policy says needs
// one. It reuses the module's EXISTING ApprovalGate — the same seam the fire
// path consumes, with its own action so the composition root's scoped status
// check (action + subject + plan) cannot be satisfied by an approval opened for
// something else.
//
// It returns true when the response has already been written (phase 1, or a
// denial) and the caller must stop.
func (m *Module) routineApproval(w http.ResponseWriter, r *http.Request, mc api.ModuleContext, req routineApprovalReq) bool {
	if req.ref == "" {
		decision, gerr := m.gate.Request(r.Context(), ApprovalRequest{
			Tenant: mc.Tenant, Action: actionScheduleActivate, SubjectKind: "schedule",
			SubjectRef: req.subject, PlanHash: req.planHash, RequestedBy: mc.Principal.Actor(),
		})
		if gerr != nil {
			m.errorf("orchestration: approval gate request failed for a governed routine declaration", "err", gerr)
			writeJSON(w, http.StatusBadGateway, errorBody("approval gate unavailable"))
			return true
		}
		writeJSON(w, http.StatusAccepted, map[string]any{
			"approval_ref": decision.ApprovalRef, "gate_status": string(decision.Status),
			"requires_approval": true, "plan_hash": req.planHash,
			"code":        codeRoutineApproval,
			"policy_refs": req.pol.PolicyRefs, "policy_digest": req.pol.Digest,
			"detail": "a routine policy requires approval for this routine to become active; re-submit the IDENTICAL declaration with approval_ref",
		})
		return true
	}
	decision, gerr := m.gate.Status(r.Context(), ApprovalCheck{
		Tenant: mc.Tenant, ApprovalRef: req.ref, PlanHash: req.planHash,
		Action: actionScheduleActivate, SubjectKind: "schedule", SubjectRef: req.subject,
	})
	if gerr != nil {
		m.errorf("orchestration: approval gate status failed for a governed routine declaration", "err", gerr)
		writeJSON(w, http.StatusBadGateway, errorBody("approval gate unavailable"))
		return true
	}
	// Strict plan binding (anti-TOCTOU), the fire path's rule applied here: the
	// recomputed hash is always a non-empty SHA-256, so an approved decision
	// echoing an empty or different hash is a NON-match and denies.
	if !decision.Allowed() || decision.PlanHash != req.planHash {
		d := &routineDenial{
			code: codeRoutineApproval, httpStatus: http.StatusForbidden,
			message:    "the routine policy requires approval and this declaration is not approved (" + string(decision.Status) + ")",
			policyRefs: req.pol.PolicyRefs, digest: req.pol.Digest,
		}
		m.auditRoutineDenial(r.Context(), mc, req.auditAction, req.auditID, d)
		writeRoutineDenial(w, d)
		return true
	}
	return false
}

// handleGetSchedule returns one schedule with its derived health and last observed
// activity.
func (m *Module) handleGetSchedule(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("schedule id required"))
		return
	}
	var dto scheduleDTO
	found := false
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(scheduleKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			if isNotFound(err) {
				return nil
			}
			return err
		}
		observed, oerr := m.lastSubjectActivity(r.Context(), sc, rec.String(colSubjectRef))
		if oerr != nil {
			return oerr
		}
		dto = toScheduleDTO(rec, observed)
		found = true
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
	writeJSON(w, http.StatusOK, dto)
}

// handlePatchSchedule enables/disables/retargets a schedule (write-tier,
// self-audited). A change to the subject or cadence clears any sticky cadence-miss
// (it is a different governed thing) and inherently voids a stale fire approval
// (the plan_hash changes).
func (m *Module) handlePatchSchedule(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("schedule id required"))
		return
	}
	var in patchScheduleRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.DesiredStatus != nil && !validStatuses[*in.DesiredStatus] {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid desired_status"))
		return
	}
	if in.ExpectedIntervalSeconds != nil && !validInterval(*in.ExpectedIntervalSeconds) {
		writeJSON(w, http.StatusBadRequest, errorBody("expected_interval_seconds must be 0 or between 60 and 31622400"))
		return
	}
	if in.GraceFactor != nil && (*in.GraceFactor < 1 || *in.GraceFactor > maxGrace) {
		writeJSON(w, http.StatusBadRequest, errorBody("grace_factor must be between 1 and 10"))
		return
	}

	// routine governance. The post-patch shape is judged against the
	// policy of the routine's OWNER (frozen at declaration), and a transition
	// INTO the active population re-runs the same admission a create does.
	current, ok, gerr := m.loadSchedule(r.Context(), mc, id)
	if gerr != nil {
		writeStoreError(w, gerr)
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	scope := routineScopeOfSchedule(current)
	pol, denial := m.resolvePolicy(r.Context(), mc.Tenant, scope)
	if denial != nil {
		m.auditRoutineDenial(r.Context(), mc, "orchestration.schedule.update.policy_denied", id, denial)
		writeRoutineDenial(w, denial)
		return
	}
	post := patchedShape(current, in)
	if d := m.checkDeclaration(r.Context(), pol, post); d != nil {
		m.auditRoutineDenial(r.Context(), mc, "orchestration.schedule.update.policy_denied", id, d)
		writeRoutineDenial(w, d)
		return
	}
	// Entering the active population is an ACTIVATION: it consumes the same
	// human decision a create does, and it must fit the same caps.
	activating := post.active && current.String(colDesiredStat) != "active"
	if activating && pol.RequireApproval {
		if done := m.routineApproval(w, r, mc, routineApprovalReq{
			planHash: activationPlanHash(mc.Tenant, id, post, scope, pol),
			subject:  id.String(), ref: in.ApprovalRef, auditID: id,
			auditAction: "orchestration.schedule.update.policy_denied", pol: pol,
		}); done {
			return
		}
	}

	var dto scheduleDTO
	found := false
	badCombo := false
	var capDenial *routineDenial
	raced := false
	changed := map[string]any{}
	// Every path that can change the ACTIVE population takes the admission
	// fence — including a deactivation, which RELEASES capacity and so must not
	// interleave with an admission that is counting.
	err := m.withAdmissionFence(r.Context(), mc, len(pol.ActiveCaps) > 0, func(sc store.Scope) error {
		repo, err := sc.Ext(scheduleKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			if isNotFound(err) {
				return nil
			}
			return err
		}
		// Re-derive the activation from the FRESHLY read row, not from the
		// pre-flight read: between them another request may have paused this
		// schedule, which would turn a "not activating" patch into a real
		// activation that skipped both the cap and the approval.
		//
		// A patch that does not carry desired_status never activates anything,
		// whatever the row says — deriving the post-state from `rec` (not from
		// the pre-flight `post`) keeps that true and avoids a spurious 409 when
		// something else paused the row underneath a status-preserving patch.
		freshActive := rec.String(colDesiredStat) == "active"
		if in.DesiredStatus != nil {
			freshActive = *in.DesiredStatus == "active"
		}
		freshActivating := freshActive && rec.String(colDesiredStat) != "active"
		if freshActivating {
			if !activating {
				// The pre-flight decided no approval was needed for a
				// transition that IS one. Refuse and let the caller retry
				// against the current state rather than admit it unapproved.
				raced, found = true, true
				return nil
			}
			// Capacity first, then spend the approval (see create).
			d, aerr := admitActive(r.Context(), sc, pol, scope, id)
			if aerr != nil {
				return aerr
			}
			if d != nil {
				capDenial, found = d, true
				return nil
			}
		}
		retarget := false
		if in.DesiredStatus != nil {
			rec[colDesiredStat] = *in.DesiredStatus
			changed["desired_status"] = *in.DesiredStatus
		}
		if in.SubjectRef != nil && *in.SubjectRef != "" {
			rec[colSubjectRef] = clamp(normSubjectRef(*in.SubjectRef), maxRefLen)
			changed["subject_ref"] = rec[colSubjectRef]
			retarget = true
		}
		if in.CadenceSpec != nil {
			// A non-nil pointer is an explicit set, INCLUDING to empty — clear to NULL
			// rather than suppressing it (setIf would drop the empty and the audit would
			// then claim a change that did not persist).
			if *in.CadenceSpec == "" {
				rec[colCadenceSpec] = nil
			} else {
				rec[colCadenceSpec] = clamp(*in.CadenceSpec, maxRefLen)
			}
			changed["cadence_spec"] = true
			retarget = true
		}
		if in.ExpectedIntervalSeconds != nil {
			rec[colExpectedIvl] = *in.ExpectedIntervalSeconds
			changed["expected_interval_seconds"] = *in.ExpectedIntervalSeconds
			retarget = true
		}
		if in.GraceFactor != nil {
			rec[colGraceFactor] = *in.GraceFactor
			changed["grace_factor"] = *in.GraceFactor
		}
		// Reject the incoherent combination (a cadence-miss interval on a non-cron
		// trigger) without persisting it, mirroring the create-time rule.
		if rec.Int(colExpectedIvl) > 0 && rec.String(colTriggerKind) != "cron" {
			badCombo = true
			return nil
		}
		if retarget || (in.DesiredStatus != nil && *in.DesiredStatus != "active") {
			rec[colMissedAt] = nil // a re-targeted or paused/retired schedule is no longer "missed"
		}
		// The approval is spent LAST, immediately before the row it authorizes.
		// Every refusal in this transaction returns nil rather than an error, so
		// the transaction COMMITS: a claim taken any earlier survives a 400/422
		// and burns a human decision for a change that never landed.
		if freshActivating && pol.RequireApproval {
			// Bind the claim to the shape THIS transaction persists, recomputed
			// from the fresh row — not to the pre-flight `post`. The row can
			// change between the out-of-transaction read that the human
			// approved and this write, and spending an approval for
			// declaration A while persisting declaration B is exactly the
			// substitution the plan hash exists to prevent. A drift makes the
			// recomputed hash differ from the approved one, so the gate check
			// above no longer covers it and the request is refused as a race.
			freshPost := patchedShape(rec, in)
			if activationPlanHash(mc.Tenant, id, freshPost, scope, pol) !=
				activationPlanHash(mc.Tenant, id, post, scope, pol) {
				raced, found = true, true
				return nil
			}
			cd, cerr := claimActivationApproval(r.Context(), sc, in.ApprovalRef, id.String(),
				activationPlanHash(mc.Tenant, id, freshPost, scope, pol))
			if cerr != nil {
				return cerr
			}
			if cd != nil {
				capDenial, found = cd, true
				return nil
			}
		}
		updated, err := repo.Update(r.Context(), rec)
		if err != nil {
			return err
		}
		observed, oerr := m.lastSubjectActivity(r.Context(), sc, updated.String(colSubjectRef))
		if oerr != nil {
			return oerr
		}
		dto = toScheduleDTO(updated, observed)
		found = true
		if err := appendRevision(r.Context(), sc, mc, id, revOpUpdate, dto); err != nil {
			return err
		}
		if activating {
			changed = activationMeta(pol, in.ApprovalRef, changed)
		}
		return auditEvent(r.Context(), sc, mc, "orchestration.schedule.update", scheduleKind, id, changed)
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if raced {
		writeJSON(w, http.StatusConflict, errorBody("the schedule's status changed concurrently; re-submit against the current state"))
		return
	}
	if capDenial != nil {
		m.auditRoutineDenial(r.Context(), mc, "orchestration.schedule.update.policy_denied", id, capDenial)
		writeRoutineDenial(w, capDenial)
		return
	}
	if badCombo {
		writeJSON(w, http.StatusBadRequest, errorBody("expected_interval_seconds (cadence-miss check) is only meaningful for a cron trigger"))
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

// loadSchedule reads one schedule outside a mutation, for the pre-flight policy
// resolution (the gate reads ANOTHER module's store, so it must never run
// nested inside this module's open write transaction).
func (m *Module) loadSchedule(ctx context.Context, mc api.ModuleContext, id model.ID) (model.Record, bool, error) {
	var out model.Record
	found := false
	err := mc.Data.View(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(scheduleKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(ctx, id)
		if err != nil {
			if isNotFound(err) {
				return nil
			}
			return err
		}
		out, found = rec, true
		return nil
	})
	return out, found, err
}

// patchedShape projects the governed shape a patch WOULD persist, so policy
// judges the post-state rather than the pre-state.
func patchedShape(rec model.Record, in patchScheduleRequest) routineShape {
	sh := routineShape{
		triggerKind: rec.String(colTriggerKind), // immutable
		cadenceSpec: rec.String(colCadenceSpec),
		intervalSec: rec.Int(colExpectedIvl),
		graceFactor: rec.Int(colGraceFactor),
		subjectKind: rec.String(colSubjectKind), // immutable
		subjectRef:  rec.String(colSubjectRef),
		active:      rec.String(colDesiredStat) == "active",
	}
	if in.DesiredStatus != nil {
		sh.active = *in.DesiredStatus == "active"
	}
	if in.SubjectRef != nil && *in.SubjectRef != "" {
		sh.subjectRef = *in.SubjectRef
	}
	if in.CadenceSpec != nil {
		sh.cadenceSpec = *in.CadenceSpec
	}
	if in.ExpectedIntervalSeconds != nil {
		sh.intervalSec = *in.ExpectedIntervalSeconds
	}
	if in.GraceFactor != nil {
		sh.graceFactor = *in.GraceFactor
	}
	return sh
}

// activationPlanHash binds an ACTIVATION approval to the exact post-state shape
// of an existing routine, its owner scope and the policy posture that demanded
// the decision — so a re-shape between the two phases voids the approval.
func activationPlanHash(tenant model.TenantID, id model.ID, sh routineShape, scope RoutineScope, pol RoutinePolicy) string {
	return canonicalHash("orchestration.schedule.activate.plan.v1",
		tenant.String(), id.String(), sh.subjectKind, sh.subjectRef, sh.triggerKind, sh.cadenceSpec,
		strconv.FormatInt(sh.intervalSec, 10),
		// grace_factor IS persisted by patch and restore, so leaving it out let
		// an approval for grace=1 authorize a declaration with grace=10.
		strconv.FormatInt(sh.graceFactor, 10),
		scope.UserRef, scope.WorkspaceRef, pol.Digest)
}

// handleFire is the two-phase governed fire — the ONLY production-affecting path of
// this module. Phase 1 (no approval_ref) requests a HITL approval bound to the
// schedule's plan_hash and records a fire_request. Phase 2 (approval_ref present)
// consumes the decision: a fire proceeds ONLY when the gate approved AND the plan
// still matches; otherwise it is blocked and recorded. An approved fire is actuated
// through the deny-closed Dispatcher seam — absent a dispatcher it is honestly
// "declared, not fired". The module never spawns a process.
func (m *Module) handleFire(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("schedule id required"))
		return
	}
	// The fire body is OPTIONAL: an empty body is phase 1 (request approval). Gate on
	// the actual presence of body bytes, not Content-Length (which is unknown/-1 for a
	// chunked request), so a chunked empty body still triggers phase 1 rather than 400.
	var in fireRequest
	if !decodeOptionalJSON(w, r, &in) {
		return
	}

	var sched model.Record
	found := false
	if err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(scheduleKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			if isNotFound(err) {
				return nil
			}
			return err
		}
		sched = rec
		found = true
		return nil
	}); err != nil {
		writeStoreError(w, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	// A retired OR PAUSED routine must not fire. Only "retired" was rejected
	// before so a paused schedule — the very state an operator uses to
	// STOP a routine, and the state a max_active_routines denial pushes one
	// into — could still be fired on demand.
	if st := sched.String(colDesiredStat); st != "active" {
		writeJSON(w, http.StatusConflict, errorBody("schedule is "+st+"; only an active schedule can fire"))
		return
	}
	subjectKind := sched.String(colSubjectKind)
	subjectRef := sched.String(colSubjectRef)
	planHash := m.planHashOfSchedule(sched)

	// Estate kill switch: an active stop freezes BOTH phases — no new fire
	// request queues and no already-approved fire dispatches while the scope
	// (estate-wide, or this agent) is stopped. It runs FIRST: a stop outranks an
	// approval, a budget and even an active break-glass grant.
	if m.stopBlocksFire(w, r, mc, id, subjectKind, subjectRef, planHash, in.ApprovalRef) {
		return
	}

	// Routine policy, immediately after the kill switch and BEFORE an
	// approval is opened or consumed: a routine the current policy refuses must
	// not queue a human decision it can never legitimately spend. It runs on
	// BOTH phases, so a phase-1 request cannot pre-bank an approval that phase 2
	// then rides past a policy authored in between.
	// A retry of an already-claimed fire REPLAYS its recorded outcome (D-05) and actuates nothing, so it is not admitted again — running the
	// floor here would refuse the replay 429 instead of returning what happened.
	replay, rerr := m.fireAlreadyClaimed(r.Context(), mc, colOpApprovalRef, in.ApprovalRef)
	if rerr != nil {
		writeStoreError(w, rerr)
		return
	}
	if !replay && m.routinePolicyBlocksFire(w, r, mc, id, sched, planHash, in.ApprovalRef) {
		return
	}

	if in.ApprovalRef == "" {
		m.firePhaseRequest(w, r, mc, id, subjectKind, subjectRef, planHash)
		return
	}
	m.firePhaseDecide(w, r, mc, id, subjectKind, subjectRef, planHash, in.ApprovalRef)
}

// admitScheduleFire is the SHARED routine-policy admission for actuation. Both
// fire paths use it — the direct POST /schedules/{id}/fire and the DAG
// schedule-fire step — because a check wired into only one of them is bypassed
// by embedding the same schedule in a workflow (the seam taught this repo
// to look for).
//
// It re-evaluates the CURRENT policy against the routine's frozen owner scope:
// the elapsed cadence since its last fire, and the authoritative environment of
// the dispatcher route that would actuate it. max_active_routines is NOT
// re-checked here — it is an activation invariant, and denying every member of
// an already-over-cap population is not a usable selection rule.
// admitScheduleFireUnlessReplay is admitScheduleFire, skipped entirely when the
// effect was already claimed (a replay actuates nothing).
func (m *Module) admitScheduleFireUnlessReplay(ctx context.Context, mc api.ModuleContext, sched model.Record, replay bool) *routineDenial {
	if replay {
		return nil
	}
	return m.admitScheduleFire(ctx, mc, sched)
}

func (m *Module) admitScheduleFire(ctx context.Context, mc api.ModuleContext, sched model.Record) *routineDenial {
	pol, denial := m.resolvePolicyTenant(ctx, mc.Tenant, routineScopeOfSchedule(sched))
	if denial != nil {
		return denial
	}
	// The floor protects the SUBJECT, not one row. Nothing stops an operator
	// declaring fifty individually-compliant routines for one agent and driving
	// them all from a single approved DAG run, so the elapsed check is taken
	// across every routine that actuates the same subject.
	last, lerr := m.lastFireOfSubject(ctx, mc, sched)
	if lerr != nil {
		m.errorf("orchestration: could not read the subject's last fire; refusing (deny-closed)",
			"subject", sched.String(colSubjectRef), "err", lerr)
		d := denyUnreadable("the routine subject's fire history is unreadable and a routine policy sets a cadence floor")
		d.policyRefs, d.digest = pol.PolicyRefs, pol.Digest
		return d
	}
	if d := m.checkFireCadence(pol, sched, last); d != nil {
		return d
	}
	// The cron allowlist describes an ONGOING property too: a routine declared
	// before the allowlist existed keeps its cadence, so it is re-checked here
	// against the stored spec rather than grandfathered forever.
	if pol.CronAllowlistInForce {
		spec := strings.TrimSpace(sched.String(colCadenceSpec))
		if sched.String(colTriggerKind) != "cron" || spec == "" || !cronAllowed(pol.CronAllowed, spec) {
			return &routineDenial{
				code: codeRoutineCron, httpStatus: http.StatusForbidden,
				message:    "this routine's cadence is not permitted by the routine policy in force",
				policyRefs: pol.PolicyRefs, digest: pol.Digest,
			}
		}
	}
	return m.checkEnvironment(ctx, pol, sched.String(colSubjectKind), sched.String(colSubjectRef), http.StatusForbidden)
}

// normSubjectRef is how a subject reference is STORED. The dispatcher resolves
// its route on a TRIMMED reference (cmd/olivares subjectKey), so " agent-1" and
// "agent-1" actuate the same target — but they are different strings, and the
// subject-wide cadence floor groups routines by that string. Storing the
// untrimmed form would let whitespace aliases split one subject into several
// populations and multiply its permitted firing rate.
func normSubjectRef(s string) string { return strings.TrimSpace(s) }

// lastFireOfSubject returns the most recent last_fired_at across EVERY schedule
// in the tenant that actuates the same subject, including this one. Only rows
// that carry the same (subject_kind, subject_ref) count — the floor is about
// how often the subject is driven, not about how many rows point at it.
func (m *Module) lastFireOfSubject(ctx context.Context, mc api.ModuleContext, sched model.Record) (string, error) {
	kind, ref := sched.String(colSubjectKind), sched.String(colSubjectRef)
	if ref == "" {
		return sched.String(colLastFiredAt), nil
	}
	last := sched.String(colLastFiredAt)
	err := mc.Data.View(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(scheduleKind)
		if err != nil {
			return err
		}
		siblings, err := listAll(ctx, repo, eq(colSubjectRef, ref))
		if err != nil {
			return err
		}
		for _, rec := range siblings {
			if rec.String(colSubjectKind) != kind {
				continue
			}
			// Validate every stamp, like the authoritative twin in
			// reserveFireSlot: an unreadable sibling must not be skipped just
			// because it sorts below the current maximum.
			for _, v := range []string{rec.String(colLastFiredAt), rec.String(colFireReservedAt)} {
				if v == "" {
					continue
				}
				if _, perr := model.ParseTimestamp(v); perr != nil {
					return fmt.Errorf("schedule %s has an unreadable fire timestamp", rec.String(model.ColID))
				}
				last = maxTS(last, v)
			}
		}
		return nil
	})
	return last, err
}

// resolvePolicyTenant is resolvePolicy without a ModuleContext, for the
// non-HTTP (workflow) caller.
func (m *Module) resolvePolicyTenant(ctx context.Context, tenant model.TenantID, scope RoutineScope) (RoutinePolicy, *routineDenial) {
	pol, err := m.routineGate.Resolve(ctx, tenant, scope)
	if err != nil {
		m.errorf("orchestration: routine-policy gate error; failing CLOSED", "err", err)
		return RoutinePolicy{}, denyUnreadable("routine policy unreadable")
	}
	if pol.Indeterminate {
		d := denyUnreadable("routine policy scopes " + pol.IndeterminateAxis +
			", which this routine does not record; it cannot be proven inapplicable")
		d.policyRefs, d.digest = pol.PolicyRefs, pol.Digest
		return pol, d
	}
	return pol, nil
}

// routinePolicyBlocksFire applies admitScheduleFire on the HTTP fire path. It
// returns true when the fire was DENIED (the response is written). The denial
// is recorded in the append-only ledger with the EXISTING blocked vocabulary —
// deliberately no new op_status, which the console types as a closed union —
// plus a policy-specific semantic audit, mirroring the kill-switch and budget
// denials. Evidence is best-effort; the denial is authoritative regardless.
func (m *Module) routinePolicyBlocksFire(w http.ResponseWriter, r *http.Request, mc api.ModuleContext, id model.ID, sched model.Record, planHash, approvalRef string) bool {
	d := m.admitScheduleFire(r.Context(), mc, sched)
	if d == nil {
		return false
	}
	op := opFireRequest
	if approvalRef != "" {
		op = opFire
	}
	if err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		if err := m.recordDecision(r.Context(), sc, decisionRow{
			subjectKind: sched.String(colSubjectKind), subjectRef: sched.String(colSubjectRef),
			scheduleRef: id.String(), op: op, planHash: planHash, approvalRef: approvalRef,
			opStatus: opStatusBlocked, actor: mc.Principal.Actor(), actorKind: mc.Principal.ActorKind(),
			result: "denied: " + d.code,
		}); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "orchestration.schedule.fire.routine_policy_denied", scheduleKind, id,
			map[string]any{"code": d.code, "plan_hash": planHash,
				"policy_refs": d.policyRefs, "policy_digest": d.digest})
	}); err != nil {
		m.errorf("orchestration: failed to record routine-policy-denied fire evidence (the denial stands)",
			"schedule", id.String(), "err", err)
	}
	writeRoutineDenial(w, d)
	return true
}

// stopBlocksFire consults the kill-switch gate before any fire work. It
// returns true when the fire was DENIED — the response is already written. The
// denial is recorded in the append-only ledger (best-effort, mirroring the
// budget denial: the denial is authoritative even if the evidence write fails).
// It FAILS CLOSED: a gate error denies the fire — an unreadable stop state must
// never mean "go" (the exact inverse of budgetBlocksFire's posture).
func (m *Module) stopBlocksFire(w http.ResponseWriter, r *http.Request, mc api.ModuleContext, id model.ID, subjectKind, subjectRef, planHash, approvalRef string) bool {
	dims := StopDims{}
	if subjectKind == nodeAgent {
		dims.AgentRef = subjectRef
	}
	op := opFireRequest
	if approvalRef != "" {
		op = opFire
	}
	verdict, err := m.stopGate.Check(r.Context(), mc.Tenant, dims)
	if err != nil {
		m.errorf("orchestration: kill-switch gate error; failing CLOSED (fire denied)", "schedule", id.String(), "err", err)
		writeJSON(w, http.StatusServiceUnavailable, errorBody("kill-switch state unreadable; fire denied (deny-closed)"))
		return true
	}
	if !verdict.Stopped {
		return false
	}
	detail := "denied: emergency stop active (" + verdict.Scope + " kill switch " + verdict.StopRef + "); re-enable requires dual-control"
	if err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		if err := m.recordDecision(r.Context(), sc, decisionRow{
			subjectKind: subjectKind, subjectRef: subjectRef, scheduleRef: id.String(), op: op,
			planHash: planHash, approvalRef: approvalRef, opStatus: opStatusBlocked,
			actor: mc.Principal.Actor(), actorKind: mc.Principal.ActorKind(), result: detail,
		}); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "orchestration.schedule.fire.killswitch_denied", scheduleKind, id,
			map[string]any{"plan_hash": planHash, "stop_ref": verdict.StopRef, "stop_scope": verdict.Scope})
	}); err != nil {
		m.errorf("orchestration: failed to record kill-switch-denied fire evidence", "schedule", id.String(), "err", err)
	}
	writeJSON(w, http.StatusLocked, fireResponse{
		Op: op, OpStatus: opStatusBlocked, PlanHash: planHash, ApprovalRef: approvalRef, Detail: detail,
	})
	return true
}

// firePhaseRequest opens (phase 1) a HITL approval for a fire and records the
// request. When no gate is wired it surfaces the governance gap as a Finding rather
// than silently leaving the fire un-governable.
func (m *Module) firePhaseRequest(w http.ResponseWriter, r *http.Request, mc api.ModuleContext, id model.ID, subjectKind, subjectRef, planHash string) {
	decision, gerr := m.gate.Request(r.Context(), ApprovalRequest{
		Tenant: mc.Tenant, Action: "orchestration.schedule.fire", SubjectKind: "schedule",
		SubjectRef: id.String(), PlanHash: planHash, RequestedBy: mc.Principal.Actor(),
	})
	if gerr != nil {
		m.errorf("orchestration: approval gate request failed", "schedule", id.String(), "err", gerr)
		writeJSON(w, http.StatusBadGateway, errorBody("approval gate unavailable"))
		return
	}
	if err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		if err := m.recordDecision(r.Context(), sc, decisionRow{
			subjectKind: subjectKind, subjectRef: subjectRef, scheduleRef: id.String(), op: opFireRequest,
			planHash: planHash, approvalRef: decision.ApprovalRef, gateStatus: decision.Status,
			opStatus: opStatusRequested, actor: mc.Principal.Actor(), actorKind: mc.Principal.ActorKind(),
			result: "approval requested",
		}); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "orchestration.schedule.fire.request", scheduleKind, id,
			map[string]any{"plan_hash": planHash, "approval_ref": decision.ApprovalRef, "gate_status": string(decision.Status)})
	}); err != nil {
		writeStoreError(w, err)
		return
	}
	if decision.Status == StatusNoGate {
		m.reportUngovernedFire(r.Context(), mc, subjectKind, subjectRef, id.String())
	}
	writeJSON(w, http.StatusAccepted, fireResponse{
		Op: opFireRequest, OpStatus: opStatusRequested, PlanHash: planHash, ApprovalRef: decision.ApprovalRef,
		GateStatus: decision.Status, RequiresApproval: true, Detail: "approval requested; re-POST with approval_ref to fire",
	})
}

// firePhaseDecide consumes (phase 2) a decision and, if approved and still matching
// the plan, dispatches through the deny-closed seam. D-05: the effect is
// reserved as a durable SINGLE-USE operation (its OperationID + evidence anchor +
// outbox committed BEFORE the dispatch), so the gate's pure-read Status can never
// authorize a second dispatch of the same approval and a client retry after a
// lost outcome replays the recorded result instead of re-actuating.
func (m *Module) firePhaseDecide(w http.ResponseWriter, r *http.Request, mc api.ModuleContext, id model.ID, subjectKind, subjectRef, planHash, approvalRef string) {
	decision, gerr := m.gate.Status(r.Context(), ApprovalCheck{
		Tenant: mc.Tenant, ApprovalRef: approvalRef, PlanHash: planHash,
		Action: "orchestration.schedule.fire", SubjectKind: "schedule", SubjectRef: id.String(),
	})
	if gerr != nil {
		m.errorf("orchestration: approval gate status failed", "schedule", id.String(), "err", gerr)
		writeJSON(w, http.StatusBadGateway, errorBody("approval gate unavailable"))
		return
	}
	// Strict plan binding (anti-TOCTOU): the recomputed planHash is always a non-empty
	// SHA-256, so an approved decision that echoes an EMPTY or different plan hash is a
	// non-match and is blocked — a partial/buggy gate cannot authorize a fire it was
	// not bound to (contract §5: deny unless Allowed() AND PlanHash == plan_hash).
	if !decision.Allowed() || decision.PlanHash != planHash {
		m.recordBlocked(r.Context(), mc, id, decisionRow{
			subjectKind: subjectKind, subjectRef: subjectRef, scheduleRef: id.String(), op: opFire,
			planHash: planHash, approvalRef: approvalRef, gateStatus: decision.Status, opStatus: opStatusBlocked,
			actor: mc.Principal.Actor(), actorKind: mc.Principal.ActorKind(), result: "denied: " + string(decision.Status),
		})
		if decision.Status == StatusNoGate {
			m.reportUngovernedFire(r.Context(), mc, subjectKind, subjectRef, id.String())
		}
		writeJSON(w, http.StatusForbidden, fireResponse{
			Op: opFire, OpStatus: opStatusBlocked, PlanHash: planHash, ApprovalRef: approvalRef,
			GateStatus: decision.Status, RequiresApproval: true, Detail: "fire denied (deny-by-default)",
		})
		return
	}

	// FIN-08 budget pre-flight: the SECOND, orthogonal gate. A fire a human approved is
	// still denied when an enforcing budget that scopes it is at its cap (Denial-of-
	// Wallet). It runs AFTER approval (a rejected fire never reaches it) and BEFORE
	// the claim (capped spend never reserves). A budget-gate error fails OPEN.
	if m.budgetBlocksFire(w, r, mc, id, subjectKind, subjectRef, planHash, approvalRef, decision.Status) {
		return
	}

	// MF1: reserve the CADENCE slot under the admission fence before the
	// effect, so two approved fires cannot both pass a floor by reading the same
	// pre-dispatch last_fired_at. It runs after the budget gate (a fire denied
	// for spend must not burn a cadence slot) and before the operation claim.
	sched, schedOK, schedErr := m.loadSchedule(r.Context(), mc, id)
	if schedErr != nil {
		writeStoreError(w, schedErr)
		return
	}
	if !schedOK {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	claimed, cerr2 := m.fireAlreadyClaimed(r.Context(), mc, colOpApprovalRef, approvalRef)
	if cerr2 != nil {
		writeStoreError(w, cerr2)
		return
	}
	if pol, pd := m.resolvePolicyTenant(r.Context(), mc.Tenant, routineScopeOfSchedule(sched)); pd != nil && !claimed {
		writeRoutineDenial(w, pd)
		return
	} else if !claimed && pol.MinIntervalSec > 0 {
		var rd *routineDenial
		if ferr := m.withAdmissionFence(r.Context(), mc, true, func(sc store.Scope) error {
			d, e := m.reserveFireSlot(r.Context(), sc, pol, sched)
			rd = d
			return e
		}); ferr != nil {
			writeStoreError(w, ferr)
			return
		}
		if rd != nil {
			// Evidence, like every sibling denial (kill switch, budget,
			// routine policy at request time). Best-effort: the denial stands
			// regardless.
			if aerr := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
				if e := m.recordDecision(r.Context(), sc, decisionRow{
					subjectKind: subjectKind, subjectRef: subjectRef, scheduleRef: id.String(),
					op: opFire, planHash: planHash, approvalRef: approvalRef, opStatus: opStatusBlocked,
					actor: mc.Principal.Actor(), actorKind: mc.Principal.ActorKind(),
					result: "denied: " + rd.code,
				}); e != nil {
					return e
				}
				return auditEvent(r.Context(), sc, mc, "orchestration.schedule.fire.routine_policy_denied", scheduleKind, id,
					map[string]any{"code": rd.code, "plan_hash": planHash,
						"policy_refs": rd.policyRefs, "policy_digest": rd.digest})
			}); aerr != nil {
				m.errorf("orchestration: failed to record cadence-reservation denial evidence (the denial stands)",
					"schedule", id.String(), "err", aerr)
			}
			writeRoutineDenial(w, rd)
			return
		}
	}

	// Reserve the single-use operation (OperationID + anchor + outbox) BEFORE the
	// effect. The (tenant, approval_ref) UNIQUE index makes it atomic under
	// concurrency; a re-POST finds the row and replays.
	spec := operationSpec{
		tenant: mc.Tenant.String(), approvalRef: approvalRef, surface: surfaceScheduleFire, action: surfaceScheduleFire,
		planHash: planHash, policyVersion: string(decision.Status), bindProfile: bindingProfileV1,
		// The direct-fire target digest binds the schedule identity, the approved
		// plan AND the effective dispatcher generation (item 6c), canonically.
		targetFp: canonicalHash("orchestration.schedule.target.v2", subjectKind, subjectRef, planHash,
			m.dispatchGen.Generation(subjectKind, subjectRef)),
		scheduleRef: id.String(), auditTarget: id,
	}
	claim, cerr := m.claimOperation(r.Context(), mc, spec)
	if errors.Is(cerr, errOperationRaced) {
		claim, cerr = m.claimOperation(r.Context(), mc, spec) // the winner is now visible
	}
	switch {
	case errors.Is(cerr, errOperationReplay):
		// Same OperationID bound to a DIFFERENT effect digest ⇒ sdk.FailureReplay
		// (a rebind), recorded as such in the evidence ledger.
		m.recordBlocked(r.Context(), mc, id, decisionRow{
			subjectKind: subjectKind, subjectRef: subjectRef, scheduleRef: id.String(), op: opFire,
			planHash: planHash, approvalRef: approvalRef, gateStatus: decision.Status, opStatus: opStatusBlocked,
			actor: mc.Principal.Actor(), actorKind: mc.Principal.ActorKind(),
			result: "refused: " + string(sdk.FailureReplay) + " (approval bound to a different effect)",
		})
		writeJSON(w, http.StatusConflict, fireResponse{
			Op: opFire, OpStatus: opStatusBlocked, PlanHash: planHash, ApprovalRef: approvalRef,
			GateStatus: decision.Status, Detail: "approval already consumed for a different fire (" + string(sdk.FailureReplay) + "); re-approve",
		})
		return
	case errors.Is(cerr, errEvidenceGap):
		// Evidence spool degraded: no anchor ⇒ no privileged effect (evidence-or-refuse).
		writeJSON(w, http.StatusServiceUnavailable, fireResponse{
			Op: opFire, OpStatus: opStatusBlocked, PlanHash: planHash, ApprovalRef: approvalRef,
			GateStatus: decision.Status, Detail: "evidence unavailable (spool degraded); fire refused",
		})
		return
	case cerr != nil:
		writeStoreError(w, cerr)
		return
	}
	if claim.replay {
		m.writeFireReplay(w, claim.rec, planHash, approvalRef, decision.Status)
		return
	}
	// Frozen evidence law (sdk/evidence.go:21,61): NO effect without an anchored
	// receipt for THIS exact binding. A faulted anchor (e.g. a committed append
	// with an empty/malformed ref) refuses deny-closed rather than dispatching.
	if claim.receipt.MustRefuse(claim.binding) {
		m.errorf("orchestration: fire claim receipt not anchored; refusing (evidence-or-refuse)", "schedule", id.String())
		writeJSON(w, http.StatusServiceUnavailable, fireResponse{
			Op: opFire, OpStatus: opStatusBlocked, PlanHash: planHash, ApprovalRef: approvalRef,
			GateStatus: decision.Status, Detail: "evidence not anchored; fire refused (" + string(claim.receipt.FailureClass(claim.binding)) + ")",
		})
		return
	}

	// This caller WON the claim: dispatch exactly once, propagating the OperationID.
	result, derr := m.dispatch.Fire(r.Context(), FireRequest{
		Tenant: mc.Tenant, SubjectKind: subjectKind, SubjectRef: subjectRef, ScheduleRef: id.String(),
		PlanHash: planHash, OperationID: string(claim.binding.OperationID),
	})
	opStatus, opState, obState, dispatchRef, detail := opStatusDispatched, opStateDispatched, obStateDispatched, result.Ref, "dispatched"
	httpStatus := http.StatusOK
	switch {
	case derr == nil:
		// dispatched
	case errors.Is(derr, errNoDispatcher):
		opStatus, opState, obState, dispatchRef, detail = opStatusDeclaredNotFired, opStateDeclared, obStateReady, "", "approved; no dispatcher wired (declared, not fired)"
	case errors.Is(derr, ErrDispatchAmbiguous):
		// The effect MAY have actuated: record UNKNOWN (never re-dispatched), not "failed".
		m.errorf("orchestration: dispatch ambiguous; recording unknown (may have actuated)", "schedule", id.String(), "err", derr)
		opStatus, opState, obState, dispatchRef, detail, httpStatus = opStatusFailed, opStateUnknown, obStateUnknown, "", "dispatch outcome uncertain; not re-actuated", http.StatusBadGateway
	default:
		m.errorf("orchestration: dispatcher failed", "schedule", id.String(), "err", derr)
		opStatus, opState, obState, dispatchRef, detail, httpStatus = opStatusFailed, opStateFailed, obStateFailed, "", "dispatcher error", http.StatusBadGateway
	}

	if err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		if err := m.settleOperation(r.Context(), sc, claim, opState, obState, dispatchRef, detail); err != nil {
			return err
		}
		// Only a real or declared (approved) fire advances last_fired_at; a failed
		// dispatch does not claim the agent ran.
		if opStatus != opStatusFailed {
			if err := m.advanceFired(r.Context(), sc, id); err != nil {
				return err
			}
		}
		if err := m.recordDecision(r.Context(), sc, decisionRow{
			subjectKind: subjectKind, subjectRef: subjectRef, scheduleRef: id.String(), op: opFire,
			planHash: planHash, approvalRef: approvalRef, gateStatus: decision.Status, opStatus: opStatus,
			dispatchRef: dispatchRef, actor: mc.Principal.Actor(), actorKind: mc.Principal.ActorKind(), result: detail,
		}); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "orchestration.schedule.fire", scheduleKind, id,
			map[string]any{"approval_ref": approvalRef, "plan_hash": planHash, "op_status": opStatus,
				"dispatch_ref": dispatchRef, "operation_id": string(claim.binding.OperationID)})
	}); err != nil {
		// The effect already happened (or failed ambiguously) but its settle did
		// not commit: the operation stays "claimed". Refuse to re-actuate — a retry
		// replays this uncertainty rather than firing again (at-most-once).
		m.errorf("orchestration: fire dispatched but settle failed; operation left uncertain", "schedule", id.String(), "err", err)
		writeJSON(w, http.StatusBadGateway, fireResponse{
			Op: opFire, OpStatus: opStatusFailed, PlanHash: planHash, ApprovalRef: approvalRef,
			GateStatus: decision.Status, DispatchRef: dispatchRef,
			Detail: "fire actuated but evidence write failed; outcome uncertain — a retry will not re-actuate",
		})
		return
	}
	writeJSON(w, httpStatus, fireResponse{
		Op: opFire, OpStatus: opStatus, PlanHash: planHash, ApprovalRef: approvalRef,
		GateStatus: decision.Status, DispatchRef: dispatchRef, Detail: detail,
	})
}

// writeFireReplay returns the recorded outcome of an already-claimed operation
// WITHOUT re-actuating (D-05 single-use idempotency).
func (m *Module) writeFireReplay(w http.ResponseWriter, op model.Record, planHash, approvalRef string, gateStatus GateStatus) {
	dispatchRef := op.String(colOpDispatchRef)
	opStatus, httpStatus, detail := opStatusDispatched, http.StatusOK, "replay: original fire outcome (single-use approval already consumed)"
	switch op.String(colOpState) {
	case opStateDeclared:
		opStatus, detail = opStatusDeclaredNotFired, "replay: approved; no dispatcher wired (single-use approval already consumed)"
	case opStateFailed:
		opStatus, httpStatus, detail = opStatusFailed, http.StatusBadGateway, "replay: original fire failed (single-use approval already consumed)"
	case opStateClaimed, opStateUnknown:
		opStatus, httpStatus, detail = opStatusFailed, http.StatusBadGateway, "fire in-flight or uncertain; will not re-actuate (single-use approval already consumed)"
	}
	writeJSON(w, httpStatus, fireResponse{
		Op: opFire, OpStatus: opStatus, PlanHash: planHash, ApprovalRef: approvalRef,
		GateStatus: gateStatus, DispatchRef: dispatchRef, Detail: detail,
	})
}

// budgetBlocksFire consults the FinOps budget gate before a fire dispatches (FIN-08).
// It returns true when the fire was DENIED — the response is already written and the
// caller must return: an enforcing budget (action=throttle|block) that scopes this
// subject is at its cap. The denial is recorded in the append-only ledger as a
// distinct op_status (budget_blocked|budget_throttled) and audited (docs/SECURITY-HARDENING.md:
// minimal data — refs + action, never a USD figure). It FAILS OPEN: a budget-gate
// error never blocks an approved fire (the finops_budget_cap finding is the backstop),
// consistent with finops.CheckBudget's documented contract.
func (m *Module) budgetBlocksFire(w http.ResponseWriter, r *http.Request, mc api.ModuleContext, id model.ID, subjectKind, subjectRef, planHash, approvalRef string, gateStatus GateStatus) bool {
	dims := BudgetDims{RoutineRef: id.String()}
	if subjectKind == nodeAgent {
		dims.AgentRef = subjectRef
	}
	verdict, err := m.budgetGate.Check(r.Context(), mc.Tenant, dims)
	if err != nil {
		// Fail open: a FinOps outage must not take down an approved fire.
		m.errorf("orchestration: budget gate error; failing open (approved fire proceeds)", "schedule", id.String(), "err", err)
		return false
	}
	if verdict.Allowed {
		return false
	}
	opStatus, httpStatus := opStatusBudgetBlocked, http.StatusPaymentRequired
	if verdict.Action == budgetActionThrottle {
		opStatus, httpStatus = opStatusBudgetThrottled, http.StatusTooManyRequests
	}
	detail := verdict.Reason
	if detail == "" {
		detail = "fire denied: budget cap reached"
	}
	// Best-effort evidence (mirrors recordBlocked): the budget denial is AUTHORITATIVE
	// even if its ledger/audit write fails — a lost record is logged (docs/SECURITY-HARDENING.md), never
	// turned into a 5xx that would mask the denial and tempt a retry. Spend is prevented
	// regardless (the dispatcher is never reached).
	if err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		if err := m.recordDecision(r.Context(), sc, decisionRow{
			subjectKind: subjectKind, subjectRef: subjectRef, scheduleRef: id.String(), op: opFire,
			planHash: planHash, approvalRef: approvalRef, gateStatus: gateStatus, opStatus: opStatus,
			actor: mc.Principal.Actor(), actorKind: mc.Principal.ActorKind(), result: detail,
		}); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "orchestration.schedule.fire.budget_denied", scheduleKind, id,
			map[string]any{"approval_ref": approvalRef, "plan_hash": planHash, "op_status": opStatus,
				"budget_ref": verdict.BudgetRef, "budget_action": verdict.Action})
	}); err != nil {
		m.errorf("orchestration: failed to record budget-denied fire evidence", "schedule", id.String(), "err", err)
	}
	writeJSON(w, httpStatus, fireResponse{
		Op: opFire, OpStatus: opStatus, PlanHash: planHash, ApprovalRef: approvalRef,
		GateStatus: gateStatus, Detail: detail,
	})
	return true
}

// advanceFired stamps last_fired_at and clears any cadence-miss marker (a fire is
// activity) on the schedule, version-checked inside the caller's transaction.
func (m *Module) advanceFired(ctx context.Context, sc store.Scope, id model.ID) error {
	repo, err := sc.Ext(scheduleKind)
	if err != nil {
		return err
	}
	rec, err := repo.Get(ctx, id)
	if err != nil {
		return err
	}
	rec[colLastFiredAt] = m.clock.Now().String()
	rec[colMissedAt] = nil
	_, err = repo.Update(ctx, rec)
	return err
}

// reportUngovernedFire records and emits a Finding that a fire could not be
// governed because no approval gate is wired — an operator-visible governance
// gap, never suppressed (the fire itself is still denied by default).
func (m *Module) reportUngovernedFire(ctx context.Context, mc api.ModuleContext, subjectKind, subjectRef, scheduleID string) {
	detail := fmt.Sprintf("schedule:%s fire attempted with no approval gate wired", scheduleID)
	if err := mc.Data.Mutate(ctx, func(sc store.Scope) error {
		return m.persistFinding(ctx, sc, finding{
			kind: busUngovernedFire, severity: sdkmodel.SeverityMedium, subjectKind: subjectKind, subjectRef: subjectRef,
			title: "scheduled fire blocked: no approval gate wired", detail: detail,
			meta: map[string]any{"schedule": scheduleID},
		})
	}); err != nil {
		m.errorf("orchestration: failed to persist ungoverned-fire finding", "schedule", scheduleID, "err", err)
	}
	m.emitFinding(ctx, mc.Tenant, busUngovernedFire, sdkmodel.SeverityMedium, subjectKind, subjectRef,
		"scheduled fire blocked: no approval gate wired", detail)
}

// handleScheduleDecisions lists the append-only fire/miss ledger for one schedule.
func (m *Module) handleScheduleDecisions(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("schedule id required"))
		return
	}
	m.listDecisions(w, r, mc, nil, eq(colScheduleRef, id.String()))
}

// handleDecisions lists the whole append-only fire/miss ledger for the tenant. It is the
// only surface that returns decisions belonging to no schedule, such as every
// workflow-run decision.
//
// Ordering is chronological by ingestion and keyset-paginable by default. `?order=newest`
// returns the most recent decisions first; that mode is a TOP-N view and does not
// paginate — the store issues no cursor for a custom sort.
func (m *Module) handleDecisions(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	// ⛔ EL ORDEN INVERSO ES OPT-IN Y NO EL DEFECTO, Y ESO LO DECIDIÓ UNA MEDIDA, NO EL
	// GUSTO. Ponerlo siempre parecía la mejora obvia —en un ledger de gobierno el recorte
	// se queda con las filas MÁS ANTIGUAS, que es justo lo que nadie busca— pero rompe la
	// paginación pública: el store NO emite cursor para una consulta con Sort
	// personalizado (sqlstore/generic.go) y aun así contesta `has_more: true`. Es decir,
	// la primera página anunciaría que hay más y no habría forma de pedir la siguiente.
	// Y hay consumidores con `--cursor` hoy: el CLI, los SDKs generados y la doc.
	//
	// ⇒ el default se queda EXACTAMENTE como estaba y quien quiere lo reciente lo pide.
	// La consola lo pide porque enseña un top-N y declara el recorte; nunca pagina.
	//
	// La ruta POR SCHEDULE no acepta el parámetro a propósito: ahí la cronología de ESE
	// schedule es la lectura natural y su cursor sigue siendo la forma de recorrerla.
	var sorts []model.Sort
	if r.URL.Query().Get("order") == orderNewest && r.URL.Query().Get("cursor") == "" {
		sorts = []model.Sort{{Column: colOccurredAt, Desc: true}}
	}
	m.listDecisions(w, r, mc, sorts)
}

// orderNewest is the opt-in value of the `order` query parameter: most recent first.
// It is a TOP-N mode — the store issues no keyset cursor for a custom sort — so it is
// never the default, which stays chronological and paginable.
const orderNewest = "newest"

// listDecisions returns one page of the decision ledger. `sorts` is the ORDER BY the
// caller wants and is ignored when the request carries a cursor (a custom sort disables
// the keyset cursor and the store rejects both together). With no sorts it keeps the
// historical behavior: keyset-paginated by the time-ordered row id, chronological by
// ingestion.
func (m *Module) listDecisions(w http.ResponseWriter, r *http.Request, mc api.ModuleContext, sorts []model.Sort, filters ...model.Filter) {
	q := listQuery(r)
	q.Filters = append(q.Filters, filters...)
	if q.Cursor == "" {
		q.Sort = sorts
	}
	out := listResponse[decisionDTO]{Items: []decisionDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(decisionKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toDecisionDTO(rec))
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
