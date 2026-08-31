// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// renderinspect.go defines the SEAM the RS render-gate (handleUIRead, rs.go)
// depends on for DEEP content inspection of MCP App template HTML — the
// channel the content-firewall did not cover until (the render
// was forwarded verbatim after authorization). The interface lives in this
// Apache-2.0 connector; the implementation is wired from cmd/olivares (AGPL
// glue) or enterprise/contentfirewall (commercial depth, //go:build enterprise).
//
// A nil inspector means NO deep inspection — the render-gate, consent, and
// deny-closed inventory all keep functioning exactly as before (no rug-pull).
// The AGPL default binary injects nil (wire_noenterprise.go); only the
// enterprise build with a firewall config injects the real inspector.
//
// HONESTY (docs/SECURITY-HARDENING.md): verified-deployed AT THE RS PEP (the
// render fetch), NEVER "impossible to bypass." The iframe↔host postMessage
// channel is client-side; the RS governs the FETCH of the template and the
// resulting tool calls — a host that points ANTHROPIC_BASE_URL elsewhere or
// a client that does not transit the RS evades this, exactly as it evades
// the inference proxy's firewall.

// Render inspection channel label (parallels the direction labels).
const ChannelRender = "mcp_app_render"

// RenderInspectionInput is the minimal-data input for inspecting an MCP App
// template's HTML content before it is forwarded to the client. Tenant and
// Subject are opaque string keys (identity-blind); Content carries the HTML
// in-process for the deterministic detectors; ContentHash is the auditable
// reference (the HTML is NEVER persisted — docs/SECURITY-HARDENING.md).
type RenderInspectionInput struct {
	Tenant      string // resolved tenant key
	Subject     string // authenticated subject (from the validated token)
	TemplateURI string // the ui:// URI being rendered
	Content     []byte // the HTML body from the resources/read result (in-flight only)
	ContentHash string // SHA-256 hex of Content (the auditable fingerprint)
	TraceParent string // W3C traceparent for correlation (an identifier, never a payload)
}

// RenderInspectionDecision is the inspector's verdict for one template render.
// Deny-closed: a non-Allow with a Reason is a deny (the RS withholds the
// render and audits). Advisory findings (Severity medium/info) are published
// but do NOT block — the same FP-bounded algebra as.
type RenderInspectionDecision struct {
	// Allow permits the render to continue. The zero value (false) with an
	// empty Reason is "no decision" — the RS treats it as a clean pass (an
	// inspector that found nothing returns the zero value). A deny MUST set
	// Reason.
	Allow bool
	// HITL requests human-in-the-loop approval before the fetched render is
	// released — the `hitl` action of the firewall policy, which the
	// content-firewall contract words "bloquea+abre aprobación+finding" and
	// which until then had NO CHANNEL on this surface.
	// Without this field an inspector could express "block" (Allow=false +
	// Reason) but never "block UNTIL a human approves", so a policy configured
	// `hitl` for the render channel collapsed onto plain `deny`.
	HITL   bool
	Reason string // short, non-sensitive (never the HTML content or a matched value)
	// ApprovalPlanHash binds the HITL approval to an anti-TOCTOU hash so a
	// changed template cannot reuse a prior approval.
	//
	// It is NOT optional dressing on the field above: without the binding, an
	// approval granted for one render authorizes ANY later render on the same
	// channel — the consumer re-checks plan EQUALITY (handleUIRead, rs.go) for
	// exactly that reason, because GateDecision.Allowed() means "approved", not
	// "approved for THIS content" (gate.go:60-70). Same discipline as the
	// elicitation sibling (ElicitationInspectionDecision) and as the
	// agentcore-export plan_hash, which re-plans and answers `409 plan changed`
	// (modules/governance/agentcoreexport.go:177-188).
	ApprovalPlanHash string
	Findings         []RenderInspectionFinding
	Meter            RenderInspectionMeter
}

// RenderInspectionFinding is a posture/forensic observation from inspecting a
// rendered template's HTML. Detail is non-sensitive context the consumer
// hashes into a DetailHash; no prompt, HTML body, or matched value is carried.
type RenderInspectionFinding struct {
	Kind     string // e.g. "content_firewall"
	Severity string // "high" | "medium" | "info"
	Title    string
	Detector string // prompt_injection | exfiltration | unsafe_action
	Detail   string // non-sensitive, hashed by the consumer
}

// RenderInspectionMeter is the billable usage of one render inspection.
type RenderInspectionMeter struct {
	Inspections int
	Bytes       int64
	Detectors   int
}

// RenderInspector inspects the HTML body of a rendered MCP App template
// before it is forwarded to the client. A nil inspector means no deep
// content inspection (the render-gate + consent + inventory deny-closed
// all keep working — no rug-pull). Structurally satisfied by
// *enterprise/contentfirewall.RenderInspector under -tags enterprise.
type RenderInspector interface {
	InspectRender(ctx context.Context, in RenderInspectionInput) RenderInspectionDecision
}

// uiReadResultReservedMembers / uiContentReservedMembers are the ReadResourceResult
// members the render gate interprets (MCP 2026-07-28 RC,
// an internal design note (not shipped):1229-1231 and :1501-1527).
var (
	uiReadResultReservedMembers = []string{"contents"}
	uiContentReservedMembers    = []string{"uri", "mimeType", "text"}
)

// extractHTMLFromResult extracts the text content of the first ui:// resource
// from a resources/read JSON-RPC result, reading the EXACT-CASED members of the
// STRICT canonical tree. The MCP resources/read response is:
//
//	{"contents":[{"uri":"ui://...","mimeType":"text/html;profile=mcp-app","text":"<html>..."}]}
//
// Review round 1 (S5-03, UI leg): this used a case-folding struct unmarshal,
// so a result carrying exact `contents` followed by `"Contents":[]` made the
// inspector see NO html — encoding/json folds the two members and keeps the last
// — while an exact-cased consumer still renders the first. The render was then
// released with no inspection at all. Reading exact-cased closes the read side;
// the second return closes the decision side.
//
// The second return is AMBIGUOUS: the result does not decode strictly, or it
// carries a case-variant alias of a member the gate interprets. The caller
// WITHHOLDS the render — a template this gateway cannot classify is one it
// cannot inspect. A result that simply carries no ui:// text is (nil, false):
// there is nothing to inspect and the render proceeds, as before.
func extractHTMLFromResult(result json.RawMessage) ([]byte, bool) {
	if len(bytes.TrimSpace(result)) == 0 {
		// No result bytes at all: nothing to inspect and nothing to release — not
		// the ambiguity class (there is no second reading of nothing).
		return nil, false
	}
	v, err := decodeStrictJSON(result)
	if err != nil || v.kind != canonObject {
		return nil, true
	}
	if hasReservedAlias(v, uiReadResultReservedMembers) {
		return nil, true
	}
	contents := v.member("contents")
	if contents == nil || contents.val.kind != canonArray {
		return nil, false
	}
	for i := range contents.val.arr {
		item := contents.val.arr[i]
		if item.kind != canonObject {
			continue
		}
		if hasReservedAlias(item, uiContentReservedMembers) {
			return nil, true
		}
		uri := ""
		if u := item.member("uri"); u != nil && u.val.kind == canonString {
			uri = u.val.str
		}
		if !hasUIScheme(uri) {
			continue
		}
		if t := item.member("text"); t != nil && t.val.kind == canonString && len(t.val.str) > 0 {
			return []byte(t.val.str), false
		}
	}
	return nil, false
}

// contentSHA256 returns the hex-encoded SHA-256 of b. It is the auditable
// fingerprint for content the RS must never persist (HTML, user input).
func contentSHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
