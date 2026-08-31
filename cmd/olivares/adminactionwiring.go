// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"

	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/core/eventbus"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/finops"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// adminactionwiring.go is the FinOps → Anthropic-org defense-in-depth backstop: an
// OPT-IN, default-OFF, fail-closed bridge that, when a budget hits its hard BLOCK cap,
// pushes an upstream cap into the customer's Anthropic org through the governed Admin-API
// actuator — so a key that DODGES Olivares' in-line edge is still stopped at
// the source. It is the composition-root half (the connector actuator is Apache-2.0 and
// cannot import FinOps/governance; this AGPL wiring bridges them — the approvalbridge.go /
// erasurewiring.go pattern).
//
// What it actuates (Decisions):
//   - DEFAULT, surgical, RECOVERABLE: deactivate the OFFENDING KEY (the exact threat —
//     "a key escaped the edge"). Single HITL approval. Reversible if it was a false
//     positive (re-activate).
//   - OPT-IN escalation, NUCLEAR, IRREVERSIBLE: archive the WORKSPACE (revokes every key
//     in it). Dual-control. Only when escalate_workspace_archive is set AND the capped
//     budget is workspace-scoped.
//
// HITL completion model (the security-posture-gate reuse-approved grant): a BLOCK
// cap fires once per period (the alert ledger dedups it — finops/budgets.go), and the
// actuator's gate uses gateOnce (reuse-approved within the approval's time-box). So the
// FIRST cap OPENS a governed, plan-bound, allowlisted, AUDITED approval to deactivate the
// offending key (it lands in the operator's approval queue, deny-closed = pending until a
// human approves). The approval.resolved re-driver fires the cut the instant that human
// approval lands, independent of a cap re-fire; the actuator still re-runs the full PEP,
// finds the just-approved, plan-bound grant and consumes it. The cap "pushes the decision
// into governance"; the human authorizes; the re-driver triggers the governed actuator.
//
// FAIL-CLOSED throughout: default OFF (Enabled must be set); a tenant with no admin_key is
// skipped; no usable actuator ⇒ the whole backstop is nil (inert); a cap-target lookup
// error declines to actuate; a non-api_key/workspace dimension has no surgical upstream
// target and is an honest no-op (the backstop never guesses which key to cut). The
// finops_budget_cap finding remains the standalone signal when the backstop is off.

// OLIVARES_CLAUDE_ADMIN_ACTUATOR_CONFIG names the operator-secret JSON provisioning file
// (the loadClaudeEraserConfig pattern): the Anthropic Admin credential(s) and the
// deny-by-default allowlist live here by value, never in the store.
const claudeAdminActuatorConfigEnv = "OLIVARES_CLAUDE_ADMIN_ACTUATOR_CONFIG"

// finopsBudgetCapKind is the FinOps hard-cap finding kind (modules/finops/budgets.go).
const finopsBudgetCapKind = "finops_budget_cap"

// backstopActor is the audit actor the backstop proposes as (a service identity, never a
// human — the separation-of-duty keys on a principal that never approves).
const backstopActor = "finops-backstop"

// claudeAdminActuatorConfig is the operator's provisioning of the governed Admin-API
// actuator + the FinOps backstop. BaseURL/Version are global; each tenant carries its own
// Admin credential and allowlist (a business deployment is multi-tenant). Backstop is the
// opt-in trigger config (default OFF).
type claudeAdminActuatorConfig struct {
	BaseURL  string                      `json:"base_url"`
	Version  string                      `json:"version"`
	Tenants  []claudeAdminActuatorTenant `json:"tenants"`
	Backstop finopsBackstopConfig        `json:"backstop"`
}

// claudeAdminActuatorTenant maps one business tenant to its Anthropic Admin credential
// (sk-ant-admin…, an out-of-band header, secret) and the deny-by-default (action,
// subject) allowlist the connector PEP enforces. An empty admin_key skips the tenant.
type claudeAdminActuatorTenant struct {
	Tenant    string                     `json:"tenant"`
	AdminKey  string                     `json:"admin_key"`
	Allowlist []claudeapi.AdminAllowRule `json:"allowlist"`
}

// finopsBackstopConfig is the FinOps→upstream-cap trigger config. Default zero value is
// OFF (the safe default): the actuator may be provisioned for manual/other use without the
// budget-cap auto-trigger firing.
type finopsBackstopConfig struct {
	// Enabled turns the budget-cap → upstream-cap auto-trigger ON. Default false.
	Enabled bool `json:"enabled"`
	// EscalateWorkspaceArchive additionally archives the WORKSPACE (irreversible,
	// dual-control) when the capped budget is workspace-scoped. Default false: only the
	// surgical, recoverable per-key deactivate is attempted.
	EscalateWorkspaceArchive bool `json:"escalate_workspace_archive"`
}

func loadClaudeAdminActuatorConfig(_ *slog.Logger) (claudeAdminActuatorConfig, error) {
	path := os.Getenv(claudeAdminActuatorConfigEnv)
	if path == "" {
		return claudeAdminActuatorConfig{}, nil
	}
	var cfg claudeAdminActuatorConfig
	if err := loadOperatorJSONConfig(claudeAdminActuatorConfigEnv, path, &cfg); err != nil {
		return claudeAdminActuatorConfig{}, err
	}
	return cfg, nil
}

// budgetCapResolver is the narrow read slice of FinOps the backstop depends on: turn a
// finops_budget_cap finding (whose subject is the BUDGET id) into the concrete upstream
// subject (dimension + key) to actuate on. Depending on the capability (not the concrete
// *finops.Module) keeps the backstop unit-testable.
type budgetCapResolver interface {
	BudgetCapTarget(ctx context.Context, tenant model.TenantID, budgetID string) (dimension, key string, ok bool, err error)
}

var _ budgetCapResolver = (*finops.Module)(nil)

// finopsBackstop is the opt-in, default-off, fail-closed FinOps→upstream-cap bridge.
type finopsBackstop struct {
	actuators       map[model.TenantID]*claudeapi.Actuator
	targets         budgetCapResolver
	bridge          *approvalBridge
	escalateArchive bool
	log             *slog.Logger
}

// newFinopsBackstop builds the backstop from the operator config. It returns nil (inert)
// when the backstop is OFF, unprovisioned, or has no usable tenant actuator — the honest
// absence that leaves the finops_budget_cap finding as the only signal.
func newFinopsBackstop(cfg claudeAdminActuatorConfig, targets budgetCapResolver, bridge *approvalBridge, log *slog.Logger) *finopsBackstop {
	if !cfg.Backstop.Enabled {
		return nil // default OFF — the safe state
	}
	if bridge == nil || targets == nil {
		log.Warn("finops-backstop: enabled but no approval bridge / budget resolver wired; staying inert (fail-closed)")
		return nil
	}
	actuators := map[model.TenantID]*claudeapi.Actuator{}
	for _, tc := range cfg.Tenants {
		tid, present, err := parseBusinessTenant("finops-backstop config: tenant", tc.Tenant)
		if err != nil || !present {
			log.Warn("finops-backstop: tenant entry has an invalid tenant id; skipped", "tenant", tc.Tenant)
			continue
		}
		if strings.TrimSpace(tc.AdminKey) == "" {
			log.Warn("finops-backstop: tenant has no admin_key; skipped (cannot actuate upstream)", "tenant", tc.Tenant)
			continue
		}
		actuators[tid] = claudeapi.NewActuator(claudeapi.ActuatorConfig{
			BaseURL:   cfg.BaseURL,
			Version:   cfg.Version,
			AdminKey:  tc.AdminKey,
			Allowlist: claudeapi.NewAdminActionAllowlist(tc.Allowlist),
			Gate:      bridge.adminGate(tid),
			Auditor:   slogAdminAuditor{log: log},
		})
	}
	if len(actuators) == 0 {
		log.Warn("finops-backstop: enabled but no usable tenant actuator provisioned; staying inert (fail-closed)")
		return nil
	}
	log.Info("finops-backstop: FinOps→upstream-cap backstop ENABLED (opt-in)",
		"tenants", len(actuators), "escalate_workspace_archive", cfg.Backstop.EscalateWorkspaceArchive)
	return &finopsBackstop{actuators: actuators, targets: targets, bridge: bridge, escalateArchive: cfg.Backstop.EscalateWorkspaceArchive, log: log}
}

// subscribe wires the backstop to the bus: it watches finding.reported for a
// finops_budget_cap and approval.resolved for the human-approval re-driver. A
// nil backstop (the default-off / unprovisioned case) is a no-op.
func (s *finopsBackstop) subscribe(bus eventbus.Bus) error {
	if s == nil || bus == nil {
		return nil
	}
	_, err := subscribeClassed(bus, eventbus.ClassEnforcement, "finops-backstop",
		[]event.Type{event.TypeFindingReported, event.TypeApprovalResolved}, s.onEvent)
	return err
}

func (s *finopsBackstop) onEvent(ctx context.Context, e event.Event) error {
	switch e.Type {
	case event.TypeFindingReported:
		return s.onFinding(ctx, e)
	case event.TypeApprovalResolved:
		return s.onApprovalResolved(ctx, e)
	default:
		return nil
	}
}

// onFinding is the bus handler. It returns nil on every path (a backstop fault must never
// break delivery of a finding to other subscribers); failures are logged. It acts ONLY on
// a definitive BLOCK cap (Critical severity) — a throttle cap (High) is a soft showback
// signal, not grounds for a hard upstream cut.
func (s *finopsBackstop) onFinding(ctx context.Context, e event.Event) error {
	f, ok := event.FindingOf(e)
	if !ok || f.Kind != finopsBudgetCapKind || f.Severity != sdkmodel.SeverityCritical {
		return nil
	}
	tid, present, err := parseBusinessTenant("finops-backstop event: tenant", e.Tenant)
	if err != nil || !present {
		return nil
	}
	act, ok := s.actuators[tid]
	if !ok {
		return nil // no actuator provisioned for this tenant — fail-closed no-op
	}
	s.pushCap(ctx, tid, act, f.SubjectRef) // SubjectRef = the capped budget's id
	return nil
}

// onApprovalResolved re-drives an approved, plan-bound admin action through
// the same governed actuator path as the cap trigger. It is a trigger, never a
// bypass: the actuator re-runs allowlist, plan binding, HITL reuse and audit.
func (s *finopsBackstop) onApprovalResolved(ctx context.Context, e event.Event) error {
	res, ok := event.ApprovalResolutionOf(e)
	if !ok || res.Outcome != nbApproved || res.ApprovalID == "" {
		return nil
	}

	var (
		expectedKind string
		reportAction string
		drive        func(context.Context, *claudeapi.Actuator, string, claudeapi.ActionSpec) error
	)
	switch res.Action {
	case adminCapKeyDeactivate:
		expectedKind = "claude_admin.api_key"
		reportAction = "deactivate_key"
		drive = func(ctx context.Context, act *claudeapi.Actuator, subject string, spec claudeapi.ActionSpec) error {
			return act.DeactivateKey(ctx, claudeapi.ActionDeactivateKey, subject, spec)
		}
	case adminCapWorkspaceArchive:
		if !s.escalateArchive {
			s.log.Info("finops-backstop: approved workspace archive ignored; archive escalation is currently disabled", "approval", res.ApprovalID)
			return nil
		}
		expectedKind = "claude_admin.workspace"
		reportAction = "archive_workspace"
		drive = func(ctx context.Context, act *claudeapi.Actuator, subject string, spec claudeapi.ActionSpec) error {
			return act.ArchiveWorkspace(ctx, subject, spec)
		}
	default:
		return nil
	}

	tid, present, err := parseBusinessTenant("finops-backstop event: tenant", e.Tenant)
	if err != nil || !present {
		return nil
	}
	act, ok := s.actuators[tid]
	if !ok {
		return nil
	}
	if s.bridge == nil {
		s.log.Warn("finops-backstop: approval resolved but approval bridge is not wired; not actuating", "tenant", tid.String(), "approval", res.ApprovalID)
		return nil
	}
	cred, ok := s.bridge.cred(tid)
	if !ok {
		return nil
	}
	view, err := s.bridge.readApproval(ctx, cred, res.ApprovalID)
	if err != nil {
		s.log.Warn("finops-backstop: approval read failed; not actuating", "tenant", tid.String(), "approval", res.ApprovalID, "err", err)
		return nil
	}
	if view.status != nbApproved {
		s.log.Warn("finops-backstop: approval resolved but current approval status is not approved; not actuating",
			"tenant", tid.String(), "approval", res.ApprovalID, "status", view.status)
		return nil
	}
	if view.action != res.Action {
		s.log.Warn("finops-backstop: approval action mismatch; not actuating",
			"tenant", tid.String(), "approval", res.ApprovalID, "action", view.action, "want", res.Action)
		return nil
	}
	if view.subjectKind != expectedKind {
		s.log.Warn("finops-backstop: approval subject kind mismatch; not actuating",
			"tenant", tid.String(), "approval", res.ApprovalID, "action", res.Action,
			"subject_kind", view.subjectKind, "want", expectedKind)
		return nil
	}
	subject, ok := approvalResolvedRawSubject(view.subjectRef)
	if !ok || subject == "" {
		s.log.Warn("finops-backstop: approval subject is not plan-bound; not actuating",
			"tenant", tid.String(), "approval", res.ApprovalID)
		return nil
	}

	spec := claudeapi.ActionSpec{Tenant: tid.String(), RequestedBy: backstopActor}
	s.report(tid, reportAction, subject, drive(ctx, act, subject, spec))
	return nil
}

// approvalResolvedRawSubject decodes the upstream admin subject from the stored
// plan-bound approval subject_ref. A missing binding is refused by the re-driver:
// Approvals are always opened by the admin-action gate with "#plan=<hash>".
func approvalResolvedRawSubject(encoded string) (string, bool) {
	i := strings.LastIndex(encoded, planBindingMarker)
	if i < 0 {
		return "", false
	}
	return encoded[:i], true
}

// pushCap resolves the capped budget to its upstream subject and drives the governed
// actuator (which runs the full PEP: allowlist → PlanHash → HITL → audit).
func (s *finopsBackstop) pushCap(ctx context.Context, tenant model.TenantID, act *claudeapi.Actuator, budgetID string) {
	dimension, key, ok, err := s.targets.BudgetCapTarget(ctx, tenant, budgetID)
	if err != nil {
		s.log.Warn("finops-backstop: cap-target lookup failed; not actuating (fail-closed)", "tenant", tenant.String(), "budget", budgetID, "err", err)
		return
	}
	if !ok || key == "" {
		return // not a budget, deleted, or a global/un-keyed budget — no surgical target
	}
	spec := claudeapi.ActionSpec{Tenant: tenant.String(), RequestedBy: backstopActor}
	switch dimension {
	case "api_key":
		// Surgical, recoverable: deactivate the offending key (single HITL).
		s.report(tenant, "deactivate_key", key, act.DeactivateKey(ctx, claudeapi.ActionDeactivateKey, key, spec))
	case "workspace":
		if !s.escalateArchive {
			s.log.Info("finops-backstop: workspace-scoped cap; archive escalation disabled — not actuating", "tenant", tenant.String(), "workspace", key)
			return
		}
		// Nuclear, irreversible: archive the workspace (dual-control, re-verified by the
		// connector — see adminactions.go).
		s.report(tenant, "archive_workspace", key, act.ArchiveWorkspace(ctx, key, spec))
	default:
		// identity/global/model/team/…: no single surgical upstream key target. Honest
		// no-op — the backstop never guesses which key to cut.
		s.log.Info("finops-backstop: cap dimension has no surgical upstream target; not actuating", "tenant", tenant.String(), "dimension", dimension)
	}
}

// report logs the actuation outcome. A governed DENY (most importantly the EXPECTED
// first-fire case: the approval is pending a human) is an INFO, not an error — the request
// is now in the HITL queue and a subsequent cap within the approved grant's window
// completes the cut.
func (s *finopsBackstop) report(tenant model.TenantID, action, subject string, err error) {
	if err == nil {
		s.log.Warn("finops-backstop: upstream cap ACTUATED via governed admin action", "tenant", tenant.String(), "action", action, "subject", subject)
		return
	}
	var deny *claudeapi.AdminDenyError
	if errors.As(err, &deny) {
		s.log.Info("finops-backstop: upstream cap not actuated (governed deny / pending HITL)", "tenant", tenant.String(), "action", action, "subject", subject, "reason", deny.Reason)
		return
	}
	s.log.Warn("finops-backstop: upstream cap actuation failed (transport)", "tenant", tenant.String(), "action", action, "subject", subject, "err", err)
}

// slogAdminAuditor records every connector-side admin-action decision to the operator log
// (the operational echo; the durable evidence is the approval trail + the connector's
// audit seam). AdminActionRecord carries references + the dual-control evidence, never a
// credential — minimal data by construction.
type slogAdminAuditor struct{ log *slog.Logger }

func (a slogAdminAuditor) Record(_ context.Context, rec claudeapi.AdminActionRecord) {
	a.log.Info("claude-admin: governed action decision",
		"action", string(rec.Action), "subject_kind", rec.SubjectKind, "subject", rec.SubjectRef,
		"allowed", rec.Allowed, "dual_control", rec.DualControl, "approvers", rec.ApproverCount,
		"reason", rec.Reason, "plan", rec.PlanHash)
}
