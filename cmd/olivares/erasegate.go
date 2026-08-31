// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	claudecompliance "github.com/olivaresai/olivares/connectors/claude-compliance"
	"github.com/olivaresai/olivares/core/model"
)

// erasegate.go wires the RTBF eraser's dual-control seam
// (claudecompliance.EraseGate) to the approval bridge — the "general
// dual-control infra" the eraser's seam was deliberately left inert for.
//
// The flow: Authorize opens (or idempotently finds, reusing an approved grant
// within its time-box — the eraser is a one-shot gate like the hooks PEP) a
// governed approval for action "compliance.content.erase", bound to the
// exact PlanHash the approvers see (anti-TOCTOU). That action is in the
// Default CRITICAL set, so the engine floors its threshold at
// TWO distinct human approvers (governance risktier.go) — the quorum, the
// requester≠decider SoD and the one-decision-per-human dedupe are all the
// engine's own invariants. On an approved status the adapter reads the
// approval's immutable decision trail and returns the distinct approving
// principals as the EraseDecision.Approvers evidence, which the connector
// independently re-verifies (≥2 distinct — defense in depth).
//
// NO BREAK-GLASS PATH, deliberately: an RTBF deletion is irreversible customer
// content — there is no emergency that justifies skipping the second human. The
// adapter never consults the emergency grant, and even if it did, a
// break-glass authorization carries no approvers, so the connector's own quorum
// re-check (EraseDecision.HasDualControl) could never pass. Deny-closed twice.
//
// WIRING (for whoever constructs the eraser): the ComplianceEraser itself is
// not built in the composition root yet — it needs operator provisioning (the
// Anthropic delete credential, the per-target allowlist) that is the
// actuator's own deployment story. When that lands, construct it as
//
//	claudecompliance.NewEraser(claudecompliance.EraserConfig{
//	    ...credential/allowlist...,
//	    Gate: eng.approvalBridge.eraseGate(tenant),
//	})
//
// — everything governance-side (CRITICAL classification, the two-person floor,
// the audited approval, the approver evidence) is already live through this
// adapter. A nil bridge keeps the connector's deny-closed denyEraseGate.

// eraseActionCapability is the governed action an RTBF erasure opens an approval
// for. It is in the default CRITICAL set (modules/governance/risktier.go), so the
// engine floors its approval threshold at two distinct humans.
const eraseActionCapability = "compliance.content.erase"

// eraseGate returns the EraseGate adapter for one business tenant (the eraser is
// constructed per tenant deployment; the gate seam carries the tenant in the
// request, which the adapter validates against its pinned tenant).
func (b *approvalBridge) eraseGate(tenant model.TenantID) claudecompliance.EraseGate {
	return eraseApprovalAdapter{b: b, tenant: tenant}
}

var _ claudecompliance.EraseGate = eraseApprovalAdapter{}

type eraseApprovalAdapter struct {
	b      *approvalBridge
	tenant model.TenantID
}

// Authorize opens/finds the governed CRITICAL approval for this exact erasure
// and reports the decision with its dual-control evidence. Deny-closed at every
// edge: a mismatched/invalid tenant, an unconfigured credential or any bridge
// error is a no_gate/error deny — and the zero EraseDecision already fails both
// Allowed() checks.
func (a eraseApprovalAdapter) Authorize(ctx context.Context, req claudecompliance.EraseRequest) (claudecompliance.EraseDecision, error) {
	tid, present, err := parseBusinessTenant("erase request: tenant", req.Tenant)
	if err != nil || !present || tid != a.tenant {
		// A request for a tenant this gate was not built for is a provisioning
		// error, not an authorization: mirror the connector's denyEraseGate shape.
		return claudecompliance.EraseDecision{ApprovalRef: "no-gate:" + req.PlanHash, Status: claudecompliance.EraseNoGate, PlanHash: req.PlanHash}, nil
	}
	subjectKind := "content." + string(req.Target)
	reason := "irreversible RTBF erasure (dual-control). case=" + req.CaseRef
	ref, status, boundHash, err := a.b.gateOnceNoBreakGlass(ctx, tid, eraseActionCapability, subjectKind, req.SubjectRef, req.PlanHash, reason, req.RequestedBy)
	if err != nil {
		return claudecompliance.EraseDecision{}, err
	}
	dec := claudecompliance.EraseDecision{ApprovalRef: ref, Status: eraseStatusOf(status), PlanHash: boundHash}
	if status == nbApproved {
		// The dual-control evidence, read from the approval's immutable decision trail:
		// the credentials for provenance and the distinct PEOPLE for the quorum the
		// connector re-checks. A read failure degrades to zero of both — the connector's
		// quorum re-check then denies (deny-closed, never a fabricated approver).
		if cred, ok := a.b.cred(tid); ok {
			ev := a.b.approvalApproverEvidence(ctx, cred, ref)
			dec.Approvers, dec.ApproverPersons = ev.Actors, ev.Persons
		}
	}
	return dec, nil
}

// gateOnceNoBreakGlass is gateOnce WITHOUT the emergency fallback: the
// one-shot find-or-open that reuses an approved grant within its time-box, for
// the irreversible actions that must never have an emergency path.
func (b *approvalBridge) gateOnceNoBreakGlass(ctx context.Context, tenant model.TenantID, action, subjectKind, subjectRef, planHash, reason, requestedBy string) (ref, status, boundHash string, err error) {
	cred, ok := b.cred(tenant)
	if !ok {
		b.warnUnconfigured(tenant)
		return noGateRefPrefix + planHash, nbNoGate, planHash, nil
	}
	return b.ensureApproval(ctx, cred, action, subjectKind, subjectRef, planHash, reason, requestedBy, true)
}

// approverEvidence is one approval's dual-control evidence, in the two forms a
// two-person control needs (core/auth.PersonRef states why they are not the same
// question). It is the ONE translation from append-only decision trail into the
// evidence every gate in this binary hands downstream, so the distinction has to be made
// here or it cannot be made at all.
//
// Actors are WHICH CREDENTIALS approved — the provenance the audit trail and every
// sealed receipt must keep. Persons are WHICH ACCOUNTS approved — distinct stable user
// accounts, and the ONLY list a quorum may be counted from. They differ in exactly the
// case that matters: Actor() renders "user:<UserID>" for a session and "token:<CredID>"
// for a token, so one account holding both contributes TWO actors and ONE account.
//
// The field is named Persons and the count is of ACCOUNTS. Two accounts are not
// provably two humans — a superadmin can create the second and choose its password —
// and core/auth/person.go carries that limit with its measurement. The name is kept
// because the whole Person* vocabulary is renamed in one move or not at all; the
// operator-facing strings below say account, which is what an operator can act on.
//
// Unattributed counts approve decisions with no account behind them. They are
// deliberately NOT in Persons (an identity-less party compares unequal to every real
// account, so counting it silently PASSES a distinct-approver check —
// core/auth.PersonRef.Stable), and they are deliberately not silently dropped either:
// "I am one approver short" and "there is an approval I cannot attribute to an account"
// are different facts and an operator must be able to tell them apart.
type approverEvidence struct {
	Actors       []string
	Persons      []string
	Unattributed int
}

// approvalApproverEvidence returns the approvers of an approval, read from the
// append-only decision trail. Best-effort read: any failure returns the zero evidence
// (the consumer's quorum re-check treats that as no evidence and denies).
//
// In production this is additionally protected by three invariants none of which can be
// read from here: the unique index (tenant_id, approval_id, decider_user) and the two
// handler guards that refuse a decision from a principal with no stable user. Counting
// people here is what makes those a defense in depth rather than the only defense.
func (b *approvalBridge) approvalApproverEvidence(ctx context.Context, cred serviceCred, ref string) approverEvidence {
	code, raw := b.do(ctx, cred, http.MethodGet, "/v1/m/governance/approvals/"+url.PathEscape(ref)+"/decisions", nil)
	if code != http.StatusOK {
		return approverEvidence{}
	}
	var resp struct {
		Items []struct {
			Decision    string `json:"decision"`
			Decider     string `json:"decider"`
			DeciderUser string `json:"decider_user"`
		} `json:"items"`
	}
	if jerr := json.Unmarshal(raw, &resp); jerr != nil {
		return approverEvidence{}
	}
	var (
		ev          approverEvidence
		seenActor   = map[string]struct{}{}
		seenPerson  = map[string]struct{}{}
		emptyPerson = 0
	)
	for _, d := range resp.Items {
		if d.Decision != "approve" {
			continue
		}
		actor, person := strings.TrimSpace(d.Decider), strings.TrimSpace(d.DeciderUser)
		if actor != "" {
			if _, dup := seenActor[actor]; !dup {
				seenActor[actor] = struct{}{}
				ev.Actors = append(ev.Actors, actor)
			}
		}
		if person == "" {
			// No person stands behind this credential: it is not one of the humans.
			// Counted separately so the absence is reported rather than inferred from
			// a short list. An approve decision with neither identity is still an
			// approval this evidence cannot attribute, so it counts here too.
			emptyPerson++
			continue
		}
		if _, dup := seenPerson[person]; dup {
			continue
		}
		seenPerson[person] = struct{}{}
		ev.Persons = append(ev.Persons, person)
	}
	ev.Unattributed = emptyPerson
	return ev
}

// eraseStatusOf maps the bridge's neutral status onto the erase-gate vocabulary;
// every non-approved value is a deny. nbBreakGlass is unreachable here (this
// gate never consults the emergency path) but maps to pending defensively —
// NEVER to approved.
func eraseStatusOf(neutral string) claudecompliance.EraseStatus {
	switch neutral {
	case nbApproved:
		return claudecompliance.EraseApproved
	case nbPending, nbBreakGlass:
		return claudecompliance.ErasePending
	case nbRejected, nbCanceled:
		return claudecompliance.EraseRejected
	case nbExpired:
		return claudecompliance.EraseExpired
	default:
		return claudecompliance.EraseNoGate
	}
}
