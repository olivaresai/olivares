// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// This file is the GOVERNED EXPORT surface: the deny-by-default
// policy-enforcement point in front of the AgentCore Policy writes that project
// structured Olivares governance rows into AgentCore Cedar. It mirrors the
// Claude admin-action actuator: the read Source remains read-only, while this
// separate exporter is inert unless cmd wires an allowlist, an approval gate,
// write credentials, and an auditor.
//
// Every apply passes, in order: engine allowlist → plan-bound HITL gate →
// anti-TOCTOU plan-hash echo → connector-side dual-control re-check for
// enforcement-weakening plans → create/update/delete writes → minimal-data
// audit. The plan content, not a caller flag, decides whether dual-control is
// needed: any delete or ACTIVE→LOG_ONLY update weakens enforcement.
package agentcore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/awssig"
	"github.com/olivaresai/olivares/connectors/internal/httpx"
)

const validationModeFailOnAnyFindings = "FAIL_ON_ANY_FINDINGS"

// ExportGate is the governance HITL seam for applying one rendered AgentCore
// export plan. The real adapter lives in cmd and binds to approvals.
type ExportGate interface {
	Authorize(ctx context.Context, req ExportGateRequest) (ExportGateDecision, error)
}

// ExportGateRequest is the minimal-data plan summary the gate sees. It carries
// refs and counts only, never statements or credentials.
type ExportGateRequest struct {
	Tenant      string
	EngineID    string
	PlanHash    string
	RequestedBy string
	Creates     int
	Updates     int
	Deletes     int
	Weakens     bool
}

// ExportGateStatus is the gate verdict vocabulary shared with admin actions.
type ExportGateStatus string

const (
	ExportApproved ExportGateStatus = "approved"
	ExportPending  ExportGateStatus = "pending"
	ExportRejected ExportGateStatus = "rejected"
	ExportExpired  ExportGateStatus = "expired"
	ExportNoGate   ExportGateStatus = "no_gate"
)

// ExportGateDecision is the gate's answer. Allowed is true only for an
// explicit approved status; dual-control evidence is re-counted here.
type ExportGateDecision struct {
	ApprovalRef string
	Status      ExportGateStatus
	PlanHash    string
	// Approvers are the CREDENTIALS that approved — the audit provenance. They are NOT
	// the quorum: an audit-actor string identifies a credential, not a human, and one
	// human holding a session and a token contributes two of them.
	Approvers []string
	// ApproverPersons are the DISTINCT PEOPLE who approved — the only list the
	// dual-control re-count reads. A credential with no person behind it is absent from
	// it by construction: it cannot be one of the two humans.
	ApproverPersons []string
}

// Allowed reports whether the decision authorizes the export.
func (d ExportGateDecision) Allowed() bool { return d.Status == ExportApproved }

// distinctApprovers counts the distinct, non-empty approving PEOPLE — never the
// credentials. Counting Approvers here would let one human with two credentials satisfy
// two-person control, which is the whole thing this quorum exists to prevent.
func (d ExportGateDecision) distinctApprovers() int {
	seen := make(map[string]struct{}, len(d.ApproverPersons))
	for _, a := range d.ApproverPersons {
		a = strings.TrimSpace(a)
		if a != "" {
			seen[a] = struct{}{}
		}
	}
	return len(seen)
}

// HasDualControl reports whether the decision carries a two-person quorum.
func (d ExportGateDecision) HasDualControl() bool { return d.distinctApprovers() >= 2 }

type denyExportGate struct{}

func (denyExportGate) Authorize(_ context.Context, req ExportGateRequest) (ExportGateDecision, error) {
	return ExportGateDecision{ApprovalRef: "no-gate:" + req.PlanHash, Status: ExportNoGate, PlanHash: req.PlanHash}, nil
}

// ExportAllowlist is the deny-by-default set of policy engines this exporter may touch.
type ExportAllowlist struct {
	engineIDs []string
}

// NewExportAllowlist copies the allowed policy engine ids. "*" permits any
// engine; an empty list denies all.
func NewExportAllowlist(engineIDs []string) *ExportAllowlist {
	cp := append([]string(nil), engineIDs...)
	return &ExportAllowlist{engineIDs: cp}
}

// Allowed reports whether engineID is permitted by exact id or "*".
func (a *ExportAllowlist) Allowed(engineID string) bool {
	if a == nil {
		return false
	}
	engineID = strings.TrimSpace(engineID)
	for _, allowed := range a.engineIDs {
		allowed = strings.TrimSpace(allowed)
		if allowed == "*" || (allowed != "" && allowed == engineID) {
			return true
		}
	}
	return false
}

// ExportAuditRecord is the minimal-data audit record for one apply attempt.
type ExportAuditRecord struct {
	Tenant        string
	EngineID      string
	PlanHash      string
	Allowed       bool
	DualControl   bool
	ApproverCount int
	Reason        string
	ApprovalRef   string
	RequestedBy   string
	Creates       int
	Updates       int
	Deletes       int
	Failed        int
	At            time.Time
}

// ExportAuditor records each allow or deny decision. The default is no-op.
type ExportAuditor interface {
	Record(ctx context.Context, rec ExportAuditRecord)
}

type nopExportAuditor struct{}

func (nopExportAuditor) Record(context.Context, ExportAuditRecord) {}

// ExporterConfig configures a governed AgentCore export writer.
type ExporterConfig struct {
	Region          string
	AccountID       string
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	Endpoint        string
	Doer            httpx.Doer
	Timeout         time.Duration
	Allowlist       *ExportAllowlist
	Gate            ExportGate
	Auditor         ExportAuditor
	Clock           func() time.Time
}

// Exporter is the governed AgentCore Policy writer. Construct it with
// NewExporter so nil governance seams are hardened to deny/no-op defaults.
type Exporter struct {
	region    string
	accountID string
	c         *client
	allowlist *ExportAllowlist
	gate      ExportGate
	auditor   ExportAuditor
	now       func() time.Time
	maxPages  int
}

// NewExporter builds an inert-by-default governed exporter. A nil allowlist
// denies every engine, a nil gate returns no_gate, and a nil auditor is no-op.
// Endpoint derivation and validation mirror Source.Open.
func NewExporter(cfg ExporterConfig) (*Exporter, error) {
	endpoint := strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/")
	region := strings.TrimSpace(cfg.Region)
	if endpoint != "" {
		u, err := url.Parse(endpoint)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return nil, fmt.Errorf("agentcore: endpoint must be an absolute http(s) URL, got %q", endpoint)
		}
	} else if region != "" {
		endpoint = "https://bedrock-agentcore-control." + region + ".amazonaws.com"
	}

	doer := cfg.Doer
	if doer == nil {
		doer = http.DefaultClient
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	now := cfg.Clock
	if now == nil {
		now = time.Now
	}
	allowlist := cfg.Allowlist
	if allowlist == nil {
		allowlist = NewExportAllowlist(nil)
	}
	gate := cfg.Gate
	if gate == nil {
		gate = denyExportGate{}
	}
	auditor := cfg.Auditor
	if auditor == nil {
		auditor = nopExportAuditor{}
	}

	return &Exporter{
		region:    region,
		accountID: strings.TrimSpace(cfg.AccountID),
		c: &client{
			endpoint: endpoint,
			region:   region,
			creds: awssig.Creds{
				AKID:   strings.TrimSpace(cfg.AccessKeyID),
				Secret: cfg.SecretAccessKey,
				Token:  cfg.SessionToken,
			},
			doer:    doer,
			now:     now,
			timeout: timeout,
		},
		allowlist: allowlist,
		gate:      gate,
		auditor:   auditor,
		now:       now,
		maxPages:  defaultMaxPages,
	}, nil
}

// Plan reads remote policies and builds the local dry-run diff. This is an
// ungoverned read; governance applies only to writes in Apply.
func (e *Exporter) Plan(ctx context.Context, engineID, tenant string, desired []RenderedPolicy) (ExportPlan, error) {
	remote, err := listEnginePolicies(ctx, e.c, engineID, e.maxPages)
	if err != nil {
		return ExportPlan{}, err
	}
	return BuildExportPlan(engineID, tenant, desired, remote), nil
}

// ExportSpec is the actor/tenant attribution for one apply request.
type ExportSpec struct {
	Tenant      string
	RequestedBy string
}

// ExportResult is the per-policy write result. Err is set only for that item;
// later items are still attempted.
type ExportResult struct {
	Name          string
	Op            string
	PolicyID      string
	PolicyArn     string
	Status        string
	StatusReasons []string
	Err           error
}

// ExportDenyError is returned for policy denials. It is distinct from
// transport/API errors and always carries the plan hash when known.
type ExportDenyError struct {
	Reason   string
	PlanHash string
}

func (e *ExportDenyError) Error() string {
	if e.PlanHash == "" {
		return "agentcore-export: apply denied: " + e.Reason
	}
	return "agentcore-export: apply denied (" + e.Reason + ") plan=" + e.PlanHash
}

// ExportApplyError summarizes per-item failures after all remaining writes
// have been attempted.
type ExportApplyError struct {
	Failed int
	Total  int
}

func (e *ExportApplyError) Error() string {
	return fmt.Sprintf("agentcore-export: %d of %d policy write(s) failed", e.Failed, e.Total)
}

// Apply is the governed policy-enforcement point for AgentCore writes.
func (e *Exporter) Apply(ctx context.Context, plan ExportPlan, spec ExportSpec) ([]ExportResult, error) {
	tenant := spec.Tenant
	if tenant == "" {
		tenant = plan.Tenant
	}
	counts := exportPlanCounts(plan)
	if strings.TrimSpace(plan.EngineID) == "" {
		e.recordExport(tenant, plan, false, false, 0, "empty engine id", "", spec.RequestedBy, counts, 0)
		return nil, &ExportDenyError{Reason: "empty engine id", PlanHash: plan.PlanHash}
	}
	if strings.TrimSpace(plan.PlanHash) == "" {
		e.recordExport(tenant, plan, false, false, 0, "empty plan hash", "", spec.RequestedBy, counts, 0)
		return nil, &ExportDenyError{Reason: "empty plan hash", PlanHash: plan.PlanHash}
	}

	if !e.allowlist.Allowed(plan.EngineID) {
		e.recordExport(tenant, plan, false, false, 0, "allowlist deny (engine not permitted)", "", spec.RequestedBy, counts, 0)
		return nil, &ExportDenyError{Reason: "policy engine not on the export allowlist", PlanHash: plan.PlanHash}
	}

	weakens := planWeakensEnforcement(plan)
	dec, err := e.gate.Authorize(ctx, ExportGateRequest{
		Tenant:      tenant,
		EngineID:    plan.EngineID,
		PlanHash:    plan.PlanHash,
		RequestedBy: spec.RequestedBy,
		Creates:     counts.creates,
		Updates:     counts.updates,
		Deletes:     counts.deletes,
		Weakens:     weakens,
	})
	if err != nil {
		e.recordExport(tenant, plan, false, false, 0, "gate error (fail-closed)", "", spec.RequestedBy, counts, 0)
		return nil, fmt.Errorf("agentcore-export: export gate error (deny): %w", err)
	}
	approvers := dec.distinctApprovers()
	dual := dec.HasDualControl()
	if !dec.Allowed() {
		e.recordExport(tenant, plan, false, dual, approvers, "gate not approved ("+string(dec.Status)+")", dec.ApprovalRef, spec.RequestedBy, counts, 0)
		return nil, &ExportDenyError{Reason: "export not approved by governance (" + string(dec.Status) + ")", PlanHash: plan.PlanHash}
	}
	if dec.PlanHash != plan.PlanHash {
		e.recordExport(tenant, plan, false, dual, approvers, "plan not bound (anti-TOCTOU)", dec.ApprovalRef, spec.RequestedBy, counts, 0)
		return nil, &ExportDenyError{Reason: "approval not bound to the export plan (anti-TOCTOU)", PlanHash: plan.PlanHash}
	}
	if weakens && !dual {
		e.recordExport(tenant, plan, false, false, approvers, "dual-control not satisfied (need 2 distinct approvers)", dec.ApprovalRef, spec.RequestedBy, counts, 0)
		return nil, &ExportDenyError{Reason: fmt.Sprintf("enforcement-weakening export requires dual-control (got %d distinct approver(s), need 2)", approvers), PlanHash: plan.PlanHash}
	}

	results := make([]ExportResult, 0, counts.total())
	failed := 0
	for _, ch := range plan.Creates {
		res := e.applyCreate(ctx, plan, ch)
		if res.Err != nil {
			failed++
		}
		results = append(results, res)
	}
	for _, ch := range plan.Updates {
		res := e.applyUpdate(ctx, plan, ch)
		if res.Err != nil {
			failed++
		}
		results = append(results, res)
	}
	for _, ch := range plan.Deletes {
		res := e.applyDelete(ctx, plan, ch)
		if res.Err != nil {
			failed++
		}
		results = append(results, res)
	}
	reason := "executed"
	if failed > 0 {
		reason = "approved; execution failed"
	}
	e.recordExport(tenant, plan, true, dual, approvers, reason, dec.ApprovalRef, spec.RequestedBy, counts, failed)
	if failed > 0 {
		return results, &ExportApplyError{Failed: failed, Total: counts.total()}
	}
	return results, nil
}

func (e *Exporter) applyCreate(ctx context.Context, plan ExportPlan, ch PlannedChange) ExportResult {
	res := ExportResult{Name: ch.Name, Op: exportOpCreate}
	resp, err := createPolicy(ctx, e.c, plan.EngineID, createPolicyRequest{
		Name: ch.Name,
		Definition: writePolicyDefinition{
			Cedar: &cedarPolicyBody{Statement: ch.Statement},
		},
		Description:     ch.Description,
		EnforcementMode: normalizeEnforcementMode(ch.EnforcementMode),
		ValidationMode:  validationModeFailOnAnyFindings,
		ClientToken:     exportClientToken(plan.PlanHash, ch.Name),
	})
	fillExportResult(&res, resp, err)
	return res
}

func (e *Exporter) applyUpdate(ctx context.Context, plan ExportPlan, ch PlannedChange) ExportResult {
	res := ExportResult{Name: ch.Name, Op: exportOpUpdate, PolicyID: ch.PolicyID}
	def := writePolicyDefinition{Cedar: &cedarPolicyBody{Statement: ch.Statement}}
	resp, err := updatePolicy(ctx, e.c, plan.EngineID, ch.PolicyID, updatePolicyRequest{
		Definition:      &def,
		Description:     &updatedDescription{OptionalValue: ch.Description},
		EnforcementMode: normalizeEnforcementMode(ch.EnforcementMode),
		ValidationMode:  validationModeFailOnAnyFindings,
		ClientToken:     exportClientToken(plan.PlanHash, ch.Name),
	})
	fillExportResult(&res, resp, err)
	return res
}

func (e *Exporter) applyDelete(ctx context.Context, plan ExportPlan, ch PlannedChange) ExportResult {
	res := ExportResult{Name: ch.Name, Op: exportOpDelete, PolicyID: ch.PolicyID}
	resp, err := deletePolicy(ctx, e.c, plan.EngineID, ch.PolicyID)
	fillExportResult(&res, resp, err)
	return res
}

func fillExportResult(res *ExportResult, resp policyWriteResponse, err error) {
	if resp.PolicyID != "" {
		res.PolicyID = resp.PolicyID
	}
	res.PolicyArn = resp.PolicyArn
	res.Status = resp.Status
	res.StatusReasons = append([]string(nil), resp.StatusReasons...)
	if err != nil {
		res.Status = "ERROR"
		res.Err = err
	}
}

func (e *Exporter) recordExport(tenant string, plan ExportPlan, allowed, dualControl bool, approverCount int, reason, approvalRef, requestedBy string, counts exportCounts, failed int) {
	e.auditor.Record(context.Background(), ExportAuditRecord{
		Tenant:        tenant,
		EngineID:      plan.EngineID,
		PlanHash:      plan.PlanHash,
		Allowed:       allowed,
		DualControl:   dualControl,
		ApproverCount: approverCount,
		Reason:        reason,
		ApprovalRef:   approvalRef,
		RequestedBy:   requestedBy,
		Creates:       counts.creates,
		Updates:       counts.updates,
		Deletes:       counts.deletes,
		Failed:        failed,
		At:            e.now().UTC(),
	})
}

type exportCounts struct {
	creates int
	updates int
	deletes int
}

func (c exportCounts) total() int { return c.creates + c.updates + c.deletes }

func exportPlanCounts(plan ExportPlan) exportCounts {
	return exportCounts{creates: len(plan.Creates), updates: len(plan.Updates), deletes: len(plan.Deletes)}
}

func planWeakensEnforcement(plan ExportPlan) bool {
	if len(plan.Deletes) > 0 {
		return true
	}
	for _, ch := range plan.Updates {
		if normalizeRemoteEnforcementMode(ch.RemoteEnforcementMode) == enforcementModeActive &&
			normalizeEnforcementMode(ch.EnforcementMode) == enforcementModeLogOnly {
			return true
		}
	}
	return false
}

func exportClientToken(planHash, name string) string {
	h := sha256.New()
	for _, part := range []string{planHash, name} {
		writeLengthPrefixedHashPart(h, part)
	}
	return hex.EncodeToString(h.Sum(nil))
}
