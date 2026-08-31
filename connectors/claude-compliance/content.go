// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// This file is the CONTENT arm of the multi-resource Compliance surface: a
// minimal-data ENUMERATION of erasure targets (read) and the governed, DUAL-CONTROL,
// irreversible RTBF DELETE (right-to-be-forgotten / eDiscovery erasure). It is the most
// dangerous capability in the connector, and it is built to be inert and deny-closed by
// construction:
//
//   - The connector NEVER ingests raw customer content. The Compliance API can return
//     chat message bodies, file bytes, project instructions and artifact text — all
//     SENSITIVE customer content. Routing that through the control plane's ledger would
//     violate minimal-data (docs/SECURITY-HARDENING.md), so this connector deliberately does NOT. The
//     enumeration returns only REFERENCES + structural metadata (ids, kind, deleted_at,
//     org/project refs) — enough to identify WHAT to erase, never the content itself.
//
//   - The RTBF DELETE is a SEPARATE governed actuator (ComplianceEraser), not a source
//     poll. It mirrors the A2A delegation PEP and the admin-action seam, with one extra,
//     non-negotiable control for irreversible deletion of CUSTOMER content:
//     DUAL-CONTROL (two distinct human approvers) on top of HITL. Each erase passes:
//
//     allowlist (deny-by-default (target, subject))
//     → PlanHash (anti-TOCTOU binding of target+subject+case)
//     → ApprovalGate (HITL seam, deny-closed) returning ≥2 DISTINCT approvers
//     → the connector RE-VERIFIES the dual-control quorum (never trusts the gate alone)
//     → execute one DELETE
//     → audit the decision (allow or deny) with the case reference
//
//   - INERT BY DEFAULT. NewEraser defaults the allowlist to deny-all, the gate to the
//     deny-closed denyEraseGate, the auditor to no-op, and holds NO credential unless one
//     is wired. The delete:compliance_user_data Compliance Access Key and the real
//     dual-control bridge are wired at the AGPL composition root (cmd) — in-tree the
//     eraser CANNOT delete anything. The general dual-control infra is this seam
//     already expresses the two-person quorum so it is correct the day it is wired.
//
// Authority (jun-2026): platform.claude.com/docs/en/api/compliance/apps/chats — DELETE
// chats/files/projects/project-documents are PERMANENT and IMMEDIATE with NO recovery
// window; a project must have NO attached chats before deletion (409 otherwise). Scope:
// delete:compliance_user_data. 600 req/min shared per parent org.
package claudecompliance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/modelprovider"
)

// ---- Content enumeration (minimal-data read; identifies erasure targets) -----------

// ContentRef is a minimal-data reference to one piece of governed content — enough to
// target an erasure, NEVER the content itself. Kind is "chat"|"project"; DeletedAt is
// set for content the user soft-deleted in claude.ai (still present until hard-deleted
// via the RTBF DELETE here). No name, title, body, or PII is carried.
type ContentRef struct {
	ID        string
	Kind      string
	OrgUUID   string
	ProjectID string
	DeletedAt string
	CreatedAt string
}

// chatsListResponse / projectsListResponse are the minimal projections of the content
// list endpoints. The SENSITIVE fields the API also returns (name, user email, message
// bodies, file bytes) are deliberately NOT mapped — the connector cannot surface what
// it does not decode (docs/SECURITY-HARDENING.md).
type chatsListResponse struct {
	Data []struct {
		ID               string `json:"id"`
		OrganizationUUID string `json:"organization_uuid"`
		ProjectID        string `json:"project_id"`
		DeletedAt        string `json:"deleted_at"`
		CreatedAt        string `json:"created_at"`
	} `json:"data"`
	HasMore bool   `json:"has_more"`
	LastID  string `json:"last_id"`
}

type projectsListResponse struct {
	Data []struct {
		ID               string `json:"id"`
		OrganizationUUID string `json:"organization_uuid"`
		DeletedAt        string `json:"deleted_at"`
		CreatedAt        string `json:"created_at"`
	} `json:"data"`
	HasMore  bool   `json:"has_more"`
	NextPage string `json:"next_page"`
}

// NewContentEnumerator builds a Source configured ONLY for the minimal-data content
// enumeration (EnumerateChats / EnumerateProjects): a read-scoped Compliance Access
// Key (read:compliance_user_data), no Activity-Feed key, no directory ingest. It is
// the constructor the erasure orchestrator uses to identify provider-side
// targets before routing each deletion through the governed ComplianceEraser.
// baseURL/version default like New(); doer is the injectable transport (nil ⇒
// http.DefaultClient via modelprovider).
func NewContentEnumerator(baseURL, version, readKey string, doer modelprovider.Doer) (*Source, error) {
	readKey = strings.TrimSpace(readKey)
	if readKey == "" {
		return nil, fmt.Errorf("claude-compliance: a content enumerator needs a read:compliance_user_data key")
	}
	s := New()
	s.doer = doer
	if b := strings.TrimRight(baseURL, "/"); b != "" {
		s.baseURL = b
	}
	if v := strings.TrimSpace(version); v != "" {
		s.version = v
	}
	s.complianceAccessKey = readKey
	s.cakClient = modelprovider.NewClient(s.baseURL, s.doer, modelprovider.AuthAnthropicKey, readKey,
		map[string]string{"anthropic-version": s.version})
	return s, nil
}

// EnumerateChats lists chat REFERENCES for the given users (1-10 per the API) so an RTBF
// operator can identify erasure targets. It is read-only and minimal-data: it returns
// ids + structural metadata, never names, message bodies, or PII. It requires the
// Compliance Access Key with read:compliance_user_data (the directory key slot); with no
// such key it returns nil (deny-closed). userIDs is required (the API rejects an
// unscoped content listing). Chats list OLDEST FIRST; the after_id cursor is opaque and
// survives key rotation because it is scoped to the org, not to the key (VERIFIED
// 2026-07-03).
func (s *Source) EnumerateChats(ctx context.Context, userIDs []string) ([]ContentRef, error) {
	if s.cakClient == nil || len(userIDs) == 0 {
		return nil, nil
	}
	var out []ContentRef
	after := ""
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		q := url.Values{"limit": {defaultPageLimit}}
		for _, u := range userIDs {
			q.Add("user_ids[]", u)
		}
		if after != "" {
			q.Set("after_id", after)
		}
		var resp chatsListResponse
		if err := s.cakClient.GetJSON(ctx, "/v1/compliance/apps/chats", q, &resp); err != nil {
			return nil, err
		}
		for _, c := range resp.Data {
			out = append(out, ContentRef{
				ID: c.ID, Kind: "chat", OrgUUID: c.OrganizationUUID,
				ProjectID: c.ProjectID, DeletedAt: c.DeletedAt, CreatedAt: c.CreatedAt,
			})
		}
		if !resp.HasMore || resp.LastID == "" {
			break
		}
		after = resp.LastID
	}
	return out, nil
}

// EnumerateProjects lists project REFERENCES (optionally filtered by creator user ids)
// so an RTBF operator can identify erasure targets. Read-only, minimal-data (ids +
// structural metadata only; no name/description/instructions). Requires the Compliance
// Access Key with read:compliance_user_data; nil when unconfigured (deny-closed).
func (s *Source) EnumerateProjects(ctx context.Context, userIDs []string) ([]ContentRef, error) {
	if s.cakClient == nil {
		return nil, nil
	}
	var out []ContentRef
	page := ""
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		q := url.Values{"limit": {defaultPageLimit}}
		for _, u := range userIDs {
			q.Add("user_ids[]", u)
		}
		if page != "" {
			q.Set("page", page)
		}
		var resp projectsListResponse
		if err := s.cakClient.GetJSON(ctx, "/v1/compliance/apps/projects", q, &resp); err != nil {
			return nil, err
		}
		for _, p := range resp.Data {
			out = append(out, ContentRef{
				ID: p.ID, Kind: "project", OrgUUID: p.OrganizationUUID,
				DeletedAt: p.DeletedAt, CreatedAt: p.CreatedAt,
			})
		}
		if !resp.HasMore || resp.NextPage == "" {
			break
		}
		page = resp.NextPage
	}
	return out, nil
}

// ---- RTBF DELETE: governed, dual-control, irreversible -------------------------------

// EraseTarget is one irreversible RTBF deletion target. Each maps to a permanent DELETE
// with no recovery window; an unknown target is never executable.
type EraseTarget string

const (
	// EraseChat hard-deletes a chat and all its messages/files.
	EraseChat EraseTarget = "chat"
	// EraseFile hard-deletes a chat-attached file.
	EraseFile EraseTarget = "file"
	// EraseProject hard-deletes a project and all its data (must have no attached chats).
	EraseProject EraseTarget = "project"
	// EraseProjectDocument hard-deletes a single project document.
	EraseProjectDocument EraseTarget = "project_document"
)

// EraseStatus is the effective gate verdict; every value except EraseApproved is a DENY.
type EraseStatus string

const (
	EraseApproved EraseStatus = "approved"
	ErasePending  EraseStatus = "pending"
	EraseRejected EraseStatus = "rejected"
	EraseExpired  EraseStatus = "expired"
	EraseNoGate   EraseStatus = "no_gate"
)

// EraseRequest is the minimal-data description of a prospective erasure the gate
// authorizes. PlanHash binds it to the exact (target, subject, case) tuple the approvers
// saw (anti-TOCTOU); CaseRef is the RTBF/eDiscovery case reference (the WHY — an operator
// ticket id, never content); RequestedBy is the requesting principal. NO content, NO key.
type EraseRequest struct {
	Tenant      string
	Target      EraseTarget
	SubjectRef  string // the chat/file/project/document id to erase
	CaseRef     string // RTBF/eDiscovery case reference (provenance)
	PlanHash    string
	RequestedBy string
}

// EraseDecision is the gate's answer. For an irreversible customer-content deletion it
// MUST carry the DUAL-CONTROL quorum: at least two DISTINCT human approvers. Allowed()
// is the only authorization, and it requires BOTH an approved status AND the two-person
// quorum — the connector never deletes on a single approval.
type EraseDecision struct {
	ApprovalRef string
	Status      EraseStatus
	PlanHash    string
	// Approvers are the CREDENTIALS that approved — the audit provenance. They are NOT
	// the quorum: an audit-actor string identifies a credential, not a human, and one
	// human holding a session and a token contributes two of them.
	Approvers []string
	// ApproverPersons are the DISTINCT PEOPLE who approved — the dual-control evidence,
	// and the only list this connector counts. The connector re-verifies ≥2 distinct
	// entries; it does not trust the gate to have enforced the quorum (defense in
	// depth). A credential with no person behind it is absent from this list by
	// construction: it cannot be one of the two humans.
	ApproverPersons []string
}

// distinctApprovers counts the distinct, non-empty approving PEOPLE — never the
// credentials. Counting Approvers here would let one human with two credentials satisfy
// two-person control, which is the whole thing this quorum exists to prevent.
func (d EraseDecision) distinctApprovers() int {
	seen := map[string]struct{}{}
	for _, a := range d.ApproverPersons {
		a = strings.TrimSpace(a)
		if a != "" {
			seen[a] = struct{}{}
		}
	}
	return len(seen)
}

// HasDualControl reports whether the decision satisfies two-person control (≥2 distinct
// approvers). It is checked independently of Status so the audit can distinguish "not
// approved" from "approved but only one approver".
func (d EraseDecision) HasDualControl() bool { return d.distinctApprovers() >= 2 }

// Allowed reports whether this decision authorizes the irreversible erasure — true ONLY
// for an approved status WITH the dual-control quorum satisfied. Anything else is a deny.
func (d EraseDecision) Allowed() bool { return d.Status == EraseApproved && d.HasDualControl() }

// EraseGate is the governance dual-control HITL seam for an RTBF deletion. The real
// adapter (cmd/olivares) bridges to the ApprovalGate configured with a
// two-person policy, bound to the PlanHash, returning the distinct approvers. The Eraser
// never decides — it asks, re-verifies the quorum, and consumes.
type EraseGate interface {
	Authorize(ctx context.Context, req EraseRequest) (EraseDecision, error)
}

// denyEraseGate is the deny-closed default: with no gate wired, every erasure is denied
// with an explicit no_gate decision (and zero approvers — fails the quorum too).
type denyEraseGate struct{}

func (denyEraseGate) Authorize(_ context.Context, req EraseRequest) (EraseDecision, error) {
	return EraseDecision{ApprovalRef: "no-gate:" + req.PlanHash, Status: EraseNoGate, PlanHash: req.PlanHash}, nil
}

// EraseAllowRule grants the right to erase one EraseTarget, restricted to the listed
// Subjects (exact match; "*" grants any subject of that target kind). An empty Subjects
// set authorizes NOTHING — for an irreversible operation, deny-by-default down to the
// exact id.
type EraseAllowRule struct {
	Target   EraseTarget `json:"target"`
	Subjects []string    `json:"subjects"`
}

// EraseAllowlist is the deny-by-default set of permitted (target, subject) tuples. Empty
// denies everything — there is no "allow all" mode for irreversible deletion.
type EraseAllowlist struct {
	rules []EraseAllowRule
}

// NewEraseAllowlist builds an allowlist (a copy is taken so policy cannot mutate later).
func NewEraseAllowlist(rules []EraseAllowRule) *EraseAllowlist {
	cp := make([]EraseAllowRule, len(rules))
	copy(cp, rules)
	return &EraseAllowlist{rules: cp}
}

// Allowed reports whether erasing subjectRef under target is permitted. Deny-by-default.
func (a *EraseAllowlist) Allowed(target EraseTarget, subjectRef string) bool {
	if a == nil {
		return false
	}
	subjectRef = strings.TrimSpace(subjectRef)
	for _, r := range a.rules {
		if r.Target != target {
			continue
		}
		for _, s := range r.Subjects {
			s = strings.TrimSpace(s)
			if s == "*" || (s != "" && s == subjectRef) {
				return true
			}
		}
	}
	return false
}

// EraseRecord is the minimal-data audit record of one erasure attempt. It carries the
// target reference, the bound plan, the gate verdict, the dual-control approver count,
// and the case reference — NEVER content or a credential.
type EraseRecord struct {
	Tenant        string
	Target        EraseTarget
	SubjectRef    string
	CaseRef       string
	PlanHash      string
	Allowed       bool
	DualControl   bool
	ApproverCount int
	Reason        string
	ApprovalRef   string
	RequestedBy   string
	At            time.Time
}

// EraseAuditor records each erasure decision (allow or deny) for the ledger + an OTel
// span. The default is a no-op; the composition root wires the real hash-chained ledger.
type EraseAuditor interface {
	Record(ctx context.Context, rec EraseRecord)
}

type nopEraseAuditor struct{}

func (nopEraseAuditor) Record(context.Context, EraseRecord) {}

// EraserConfig configures a governed ComplianceEraser. BaseURL/Version/DeleteKey/Doer are
// the write transport (the delete:compliance_user_data Compliance Access Key is the
// out-of-band x-api-key header). Allowlist + Gate are the PEP (a nil Allowlist denies
// every erase; a nil Gate denies every erase). Auditor is the ledger seam (nil ⇒ no-op).
// Clock is injectable for tests.
type EraserConfig struct {
	BaseURL   string
	Version   string
	DeleteKey string
	Doer      modelprovider.Doer
	Allowlist *EraseAllowlist
	Gate      EraseGate
	Auditor   EraseAuditor
	Clock     func() time.Time
}

// ComplianceEraser is the governed, dual-control RTBF deletion client. Construct it with
// NewEraser; it is safe for concurrent use.
type ComplianceEraser struct {
	baseURL   string
	version   string
	deleteKey string
	doer      modelprovider.Doer
	allowlist *EraseAllowlist
	gate      EraseGate
	auditor   EraseAuditor
	now       func() time.Time
}

// NewEraser builds a governed eraser, defaulting every seam to its deny-closed / no-op
// safe value: a nil Allowlist becomes deny-all, a nil Gate becomes denyEraseGate, a nil
// Auditor becomes the no-op. So an eraser built with zero governance config can NEVER
// delete (the "inert executor" + "dual-control from the start" design). The
// delete-scoped credential and the real two-person bridge are wired at cmd.
func NewEraser(cfg EraserConfig) *ComplianceEraser {
	e := &ComplianceEraser{
		baseURL:   strings.TrimRight(cfg.BaseURL, "/"),
		version:   cfg.Version,
		deleteKey: cfg.DeleteKey,
		doer:      cfg.Doer,
		allowlist: cfg.Allowlist,
		gate:      cfg.Gate,
		auditor:   cfg.Auditor,
		now:       cfg.Clock,
	}
	if e.baseURL == "" {
		e.baseURL = defaultBaseURL
	}
	if e.version == "" {
		e.version = defaultAnthropicVersion
	}
	if e.doer == nil {
		e.doer = http.DefaultClient
	}
	if e.allowlist == nil {
		e.allowlist = NewEraseAllowlist(nil) // deny-all
	}
	if e.gate == nil {
		e.gate = denyEraseGate{} // deny-closed
	}
	if e.auditor == nil {
		e.auditor = nopEraseAuditor{}
	}
	if e.now == nil {
		e.now = time.Now
	}
	return e
}

// EraseSpec is the attribution + provenance for one erasure: who is asking, in which
// tenant, and under which RTBF/eDiscovery case (the legal basis). It carries no content.
type EraseSpec struct {
	Tenant      string
	RequestedBy string
	CaseRef     string
}

// EraseDenyError is the typed error an erasure returns when the PEP refuses it (an
// unlisted (target, subject), a gate that did not approve, or a dual-control quorum that
// was not met). It always carries the bound plan and is never a transport error. Status
// is the gate verdict when the denial came from the gate path ("" for an allowlist or
// plan-binding deny) — an orchestrator distinguishes a PENDING approval (retry later)
// from a hard deny without parsing display text.
type EraseDenyError struct {
	Reason   string
	PlanHash string
	Status   EraseStatus
}

func (e *EraseDenyError) Error() string {
	if e.PlanHash == "" {
		return "claude-compliance: erasure denied: " + e.Reason
	}
	return "claude-compliance: erasure denied (" + e.Reason + ") plan=" + e.PlanHash
}

// Erase performs one governed, dual-control, irreversible RTBF deletion of target/
// subjectRef. It is the full PEP: allowlist → PlanHash → ApprovalGate (≥2 distinct
// approvers) → re-verify the dual-control quorum → DELETE → audit. A 409 (e.g. a project
// with attached chats) surfaces honestly. Every exit path is audited with minimal data.
func (e *ComplianceEraser) Erase(ctx context.Context, target EraseTarget, subjectRef string, spec EraseSpec) error {
	path, ok := erasePath(target, subjectRef)
	if !ok {
		return &EraseDenyError{Reason: "unsupported or empty erase target"}
	}
	plan := ErasePlanHash(target, subjectRef, hashCase(spec.CaseRef))

	// 1) Allowlist: deny-by-default least-privilege over (target, subject).
	if !e.allowlist.Allowed(target, subjectRef) {
		e.record(target, subjectRef, spec, plan, false, false, 0, "allowlist deny (target/subject not permitted)", "")
		return &EraseDenyError{Reason: "target/subject not on the erase allowlist", PlanHash: plan}
	}

	// 2) Dual-control ApprovalGate (HITL + two-person), bound to the PlanHash.
	dec, err := e.gate.Authorize(ctx, EraseRequest{
		Tenant: spec.Tenant, Target: target, SubjectRef: subjectRef,
		CaseRef: spec.CaseRef, PlanHash: plan, RequestedBy: spec.RequestedBy,
	})
	if err != nil {
		e.record(target, subjectRef, spec, plan, false, false, 0, "gate error (fail-closed)", "")
		return fmt.Errorf("claude-compliance: erasure gate error (deny): %w", err)
	}
	approvers := dec.distinctApprovers()
	// Status: only an explicit approval authorizes.
	if dec.Status != EraseApproved {
		e.record(target, subjectRef, spec, plan, false, dec.HasDualControl(), approvers, "gate not approved ("+string(dec.Status)+")", dec.ApprovalRef)
		return &EraseDenyError{Reason: "erasure not approved by governance (" + string(dec.Status) + ")", PlanHash: plan, Status: dec.Status}
	}
	// Anti-TOCTOU: the approval MUST echo the EXACT plan. An empty/absent echo means the
	// gate did not bind the erasure to a plan the approvers saw — deny. The connector never
	// trusts the gate to have bound it (defense in depth, like the dual-control re-check).
	if dec.PlanHash != plan {
		e.record(target, subjectRef, spec, plan, false, dec.HasDualControl(), approvers, "plan not bound (anti-TOCTOU)", dec.ApprovalRef)
		return &EraseDenyError{Reason: "approval not bound to the erasure plan (anti-TOCTOU)", PlanHash: plan}
	}
	// 3) Dual-control quorum, RE-VERIFIED by the connector (never trust the gate alone).
	if !dec.HasDualControl() {
		e.record(target, subjectRef, spec, plan, false, false, approvers, "dual-control not satisfied (need 2 distinct approvers)", dec.ApprovalRef)
		return &EraseDenyError{Reason: fmt.Sprintf("dual-control not satisfied (got %d distinct approver(s), need 2)", approvers), PlanHash: plan}
	}

	// 4) Execute exactly one irreversible DELETE (credential out-of-band in the header).
	if err := e.do(ctx, path); err != nil {
		e.record(target, subjectRef, spec, plan, true, true, approvers, "approved+dual-control; deletion failed", dec.ApprovalRef)
		return err
	}
	e.record(target, subjectRef, spec, plan, true, true, approvers, "erased (irreversible)", dec.ApprovalRef)
	return nil
}

// EraseChat / EraseFile / EraseProject / EraseProjectDocument are the typed convenience
// entry points; each routes through the dual-control PEP in Erase.
func (e *ComplianceEraser) EraseChat(ctx context.Context, chatID string, spec EraseSpec) error {
	return e.Erase(ctx, EraseChat, chatID, spec)
}
func (e *ComplianceEraser) EraseFile(ctx context.Context, fileID string, spec EraseSpec) error {
	return e.Erase(ctx, EraseFile, fileID, spec)
}
func (e *ComplianceEraser) EraseProject(ctx context.Context, projectID string, spec EraseSpec) error {
	return e.Erase(ctx, EraseProject, projectID, spec)
}
func (e *ComplianceEraser) EraseProjectDocument(ctx context.Context, documentID string, spec EraseSpec) error {
	return e.Erase(ctx, EraseProjectDocument, documentID, spec)
}

// erasePath maps a target+id to its DELETE path, and whether the target is supported and
// the id non-empty.
func erasePath(target EraseTarget, ref string) (string, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", false
	}
	esc := url.PathEscape(ref)
	switch target {
	case EraseChat:
		return "/v1/compliance/apps/chats/" + esc, true
	case EraseFile:
		return "/v1/compliance/apps/chats/files/" + esc, true
	case EraseProject:
		return "/v1/compliance/apps/projects/" + esc, true
	case EraseProjectDocument:
		return "/v1/compliance/apps/projects/documents/" + esc, true
	default:
		return "", false
	}
}

// record emits a minimal-data audit decision (best-effort; never blocks the result).
func (e *ComplianceEraser) record(target EraseTarget, subjectRef string, spec EraseSpec, plan string, allowed, dual bool, approvers int, reason, approvalRef string) {
	e.auditor.Record(context.Background(), EraseRecord{
		Tenant: spec.Tenant, Target: target, SubjectRef: subjectRef, CaseRef: spec.CaseRef,
		PlanHash: plan, Allowed: allowed, DualControl: dual, ApproverCount: approvers,
		Reason: reason, ApprovalRef: approvalRef, RequestedBy: spec.RequestedBy, At: e.now().UTC(),
	})
}

// do issues one authenticated DELETE and fails on any non-2xx status (a 409 — e.g. a
// project with attached chats — surfaces as an error, never silently). A bounded slice
// of an error body is surfaced for diagnostics; the credential never appears in an error.
func (e *ComplianceEraser) do(ctx context.Context, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, e.baseURL+path, nil)
	if err != nil {
		return err
	}
	if e.deleteKey != "" {
		req.Header.Set("x-api-key", e.deleteKey)
	}
	req.Header.Set("anthropic-version", e.version)
	resp, err := e.doer.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		slice, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
		return fmt.Errorf("claude-compliance: DELETE %s: status %d: %s", path, resp.StatusCode, strings.TrimSpace(string(slice)))
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	return nil
}

// erasePlanHashVersion namespaces the canonical erase plan-hash so a future change to the
// tuple shape cannot collide with an existing bound approval.
const erasePlanHashVersion = "compliance-erase-v1"

// ErasePlanHash computes the canonical, anti-TOCTOU binding for an erasure: a stable
// SHA-256 over the normalized (target, subjectRef, caseHash) tuple. Any re-target,
// different subject, or changed case voids a stale approval. Length-prefixed so no
// separator collision can forge a matching plan.
func ErasePlanHash(target EraseTarget, subjectRef, caseHash string) string {
	h := sha256.New()
	for _, part := range []string{
		erasePlanHashVersion,
		string(target),
		strings.TrimSpace(subjectRef),
		strings.TrimSpace(caseHash),
	} {
		var lenbuf [8]byte
		n := len(part)
		for i := 0; i < 8; i++ {
			lenbuf[i] = byte(n >> (8 * (7 - i)))
		}
		_, _ = h.Write(lenbuf[:])
		_, _ = h.Write([]byte(part))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// hashCase is a stable digest of the RTBF/eDiscovery case reference, so the PlanHash
// binds the legal basis the approvers saw without the connector needing to interpret it.
func hashCase(caseRef string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(caseRef)))
	return hex.EncodeToString(sum[:])
}
