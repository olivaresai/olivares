// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
)

// elicitationpep.go defines the RUNTIME governance seams for MCP elicitation
// (SEP-1865/SEP-1036) and sampling (SEP-1577) — the "future seam" surface.go
// :36-45 named, now materialized.
//
// The detective (surface.go) inventories elicitation/sampling as CAPABILITY
// ADVERTISEMENTS when a server declares them; this file governs the RUNTIME
// act: a server REQUESTING user input (elicitation) or model invocation
// (sampling). The RS mediates both directions:
//
//   - elicitation request (server → client): the prompt + schema + URL-mode
//     target the server sends. A prompt-injection or a schema that asks for
//     undeclared sensitive data is a denial surface (MCP10).
//   - elicitation response (client → server): the user's structured input.
//     This is the EXFIL ROUTE for the data the server requested — always
//     inspected (decision).
//   - sampling request (server → client): the messages the server injects
//     for the client's model. This is prompt-injection-via-server directly
//     at the model (MCP10, OWASP LLM01:2025).
//
// The interface lives in this Apache-2.0 connector; the implementation is
// wired from cmd/olivares (AGPL glue) or enterprise/contentfirewall
// (commercial depth). A nil mediator means no mediation — the surface.go
// detective still inventories the capability advertisement (no rug-pull).
//
// HONESTY: the RS mediates what transits through it. In a deployment where
// server→client messages bypass the RS (direct SSE), this PEP does not see
// them. The RS governs the FETCH path and the tool-call path; the
// server→client SSE channel is a transport concern the RS can proxy but
// does not force. Verified-deployed at the PEP, never "impossible to bypass."

// Elicitation/sampling inspection channels.
const (
	ChannelElicitationRequest  = "mcp_elicitation_request"
	ChannelElicitationResponse = "mcp_elicitation_response"
	ChannelSamplingRequest     = "mcp_sampling_request"
	ChannelMRTRInputRequest    = "mcp_mrtr_input_request"
	ChannelMRTRInputResponse   = "mcp_mrtr_input_response"
)

// ElicitationInspectionInput is the minimal-data input for mediating one
// elicitation or sampling message at runtime. Content carries the text
// in-process (prompt, user input, or sampling messages); ContentHash is the
// auditable fingerprint — the content is NEVER persisted.
type ElicitationInspectionInput struct {
	Tenant      string // resolved tenant key
	Subject     string // authenticated subject
	Channel     string // one of the Channel* constants
	Content     []byte // the inspected text (in-flight only, never persisted)
	ContentHash string // SHA-256 hex of Content
	URLTarget   string // URL-mode elicitation (SEP-1036): the redirect target; "" for non-URL
	SchemaHash  string // SHA-256 hex of the requestedSchema (if any); "" when absent
	TraceParent string // W3C traceparent for correlation
}

// ElicitationInspectionDecision is the mediator's verdict.
type ElicitationInspectionDecision struct {
	// Allow permits the message to proceed. The zero value (false) with an
	// empty Reason is "no decision" (clean pass). A deny MUST set Reason.
	Allow  bool
	HITL   bool   // request human-in-the-loop approval before proceeding
	Reason string // short, non-sensitive
	// ApprovalPlanHash binds the HITL approval to an anti-TOCTOU hash so a
	// changed prompt/input cannot reuse a prior approval.
	ApprovalPlanHash string
	Findings         []ElicitationInspectionFinding
	Meter            ElicitationInspectionMeter
}

// ElicitationInspectionFinding is a posture/forensic observation.
type ElicitationInspectionFinding struct {
	Kind     string
	Severity string // "high" | "medium" | "info"
	Title    string
	Detector string
	Channel  string
	Detail   string // non-sensitive, hashed by the consumer
}

// ElicitationInspectionMeter is the billable usage of one inspection.
type ElicitationInspectionMeter struct {
	Inspections int
	Channels    int
	Bytes       int64
	Detectors   int
}

// ElicitationMediator governs runtime elicitation and sampling messages.
// A nil mediator means no mediation (the surface.go detective still
// inventories the capability advertisement — no rug-pull). Structurally
// satisfied by *enterprise/contentfirewall.ElicitationMediator under
// -tags enterprise.
type ElicitationMediator interface {
	Mediate(ctx context.Context, in ElicitationInspectionInput) ElicitationInspectionDecision
}

// NOTE (stage 5): the case-folding `elicitationParams` / `samplingParams`
// request structs are GONE — the same F-07 differential the MRTR request side
// closed in stage 4. The enforced handlers read message/requestedSchema/messages
// and the SEP-1036 URL-mode members from the STRICT canonical tree with exact
// casing (canonicalMediatedParams, evidencerelease.go), and the forwarded bytes
// are the canonical bytes the EffectDigest binds.
//
// CORRECTION (review round 1, findings S5-02/S5-03 — this comment used to
// end with "result-side extraction stays permissive on purpose: an
// over-inclusive read there is fail-safe", and that claim was FALSE as of stage
// 5). It was true through stage 4, when the extractors only decided which
// payloads to hand an inspector and every result was relayed either way. Stage 5
// made the SAME bit gate the response-release evidence and the mediation itself:
// a case-folding read that returns NOTHING is now read as "no mediation needed"
// and takes the plain write leg. A permissive decoder is therefore fail-OPEN
// here, in exactly the F-07 shape the request side already closed — an alias or
// duplicate lets the gateway classify one member while an exact-cased peer
// consumes another out of the very bytes released.
//
// The rule these selectors now follow (design adjudication, PREMISE:
// UNSOUND): AUTHORITATIVE LOCAL CONTEXT selects which response contract and
// which exact discriminators are authoritative; the body supplies the
// discriminator value and the exact payload to mediate — never its own
// treatment. Two different regimes fall out of that:
//
//   - a METHOD-SELECTED CLOSED shape (the elicitation response, the sampling
//     messages, the ui:// render) may retain strict member validation and
//     ambiguity refusal: the method already selected the schema, so a
//     case-variant alias of an interpreted member is a genuine two-consumer
//     differential and the extractor reports AMBIGUOUS for the caller to
//     refuse — never a silent "nothing to mediate";
//   - the OPEN `Result` MRTR classification (extractMRTRInputRequests) takes a
//     caller-selected AUTHORITY PROFILE instead: exact discriminators under
//     that profile decide, case variants are untouched extension data, and
//     "unreadable" means only that the exact governed member the selected
//     contract names cannot be projected.
//
// Extraction of the CONTENT itself stays deliberately over-inclusive (more
// bytes inspected — canonical subtree bytes, keys included), which is the safe
// direction and is not what any of these findings were about.

// samplingMessageReservedMembers / samplingContentReservedMembers are the nested
// sampling members the PEP interprets. A case-variant alias of any of them makes
// the message AMBIGUOUS: encoding/json folds them together and keeps the last,
// so an aliased pair lets the gateway inspect one spelling while a case-folding
// consumer of the forwarded bytes reads the other (MCP property names are
// case-SENSITIVE — schema.ts:2232-2236, 2303-2317).
var (
	samplingMessageReservedMembers = []string{"role", "content"}
	samplingContentReservedMembers = []string{"type", "text"}
)

// hasReservedAlias reports whether the object carries a member that case-folds
// to one of the reserved names without BEING that exact name — the same
// predicate rejectReservedKeyAliases applies to request params (evidence.go
// :528-550), applied here to the members of a RESULT.
func hasReservedAlias(v canonValue, reserved []string) bool {
	if v.kind != canonObject {
		return false
	}
	for i := range v.obj {
		key := v.obj[i].key
		for _, res := range reserved {
			if key != res && keyFoldsTo(key, res) {
				return true
			}
		}
	}
	return false
}

// extractSamplingText extracts a flat text representation of the sampling
// messages for content inspection, from the STRICT canonical tree of the
// governed `messages` member. It concatenates role + the model-visible text of
// every content block — the structure is untrusted server input.
//
// MCP 2026-07-28 RC (an internal design note (not shipped):2232-2250):
// `SamplingMessage.content` is a content block OR an ARRAY of content blocks,
// and the union is Text | Image | Audio | ToolUse | ToolResult. Every block that
// can carry model-visible text is inspected:
//
//   - TextContent.text                          (schema.ts:2303-2317);
//   - ToolUseContent.input, arbitrary structured input (schema.ts:2393-2411);
//   - ToolResultContent.content, a nested ContentBlock[], AND
//     ToolResultContent.structuredContent, an arbitrary JSON value
//     (schema.ts:2432-2456).
//
// Review round 2 (blocker 1): stage 5 read ONLY a direct `text` member of
// each block, so a schema-valid `tool_result` (or `tool_use`) block reached the
// model with NO inspection at all. The nested `input`/`content`/
// `structuredContent` subtrees are walked with an over-inclusive string
// collector — the SAFE direction (more bytes inspected), never a silent skip.
//
// The second return is AMBIGUOUS: the messages are not the schema's array of
// objects, a message/block is not an object, or a block member case-folds to a
// reserved one. The caller REFUSES the request (pre-claim, nothing forwarded)
// rather than forwarding content it could not classify.
func extractSamplingText(messages *canonValue) ([]byte, bool) {
	if messages == nil {
		return nil, false // absent member: nothing governed, nothing to inspect
	}
	if messages.kind != canonArray {
		// A mediated surface that cannot read its own governed member fails closed.
		return nil, true
	}
	var b strings.Builder
	for i := range messages.arr {
		msg := messages.arr[i]
		if msg.kind != canonObject {
			// A message element that is not the schema's object is unreadable to the
			// mediated surface: fail closed rather than skip it (review round 2).
			return nil, true
		}
		if hasReservedAlias(msg, samplingMessageReservedMembers) {
			return nil, true
		}
		role := ""
		if r := msg.member("role"); r != nil && r.val.kind == canonString {
			role = r.val.str
		}
		content := msg.member("content")
		if content == nil {
			continue
		}
		blocks := []canonValue{content.val}
		if content.val.kind == canonArray {
			blocks = content.val.arr
		}
		for _, blk := range blocks {
			if blk.kind != canonObject {
				// A content block that is not an object is unreadable: fail closed.
				return nil, true
			}
			if hasReservedAlias(blk, samplingContentReservedMembers) {
				return nil, true
			}
			if text := samplingBlockText(blk); text != "" {
				b.WriteString(role)
				b.WriteString(": ")
				b.WriteString(text)
				b.WriteByte('\n')
			}
		}
	}
	if b.Len() == 0 {
		return nil, false
	}
	return []byte(b.String()), false
}

// samplingBlockText returns the model-visible text of one sampling content block,
// dispatched on the block's `type` discriminator (schema.ts:2245-2250). A
// TextContent contributes its exact-cased `text`; a ToolUseContent contributes
// its arbitrary `input`; a ToolResultContent contributes its nested `content`
// and its arbitrary `structuredContent`.
//
// Round 3 (finding 1) / design adjudication §2-§3: each arbitrary
// structured subtree is projected as its CANONICAL JSON BYTES — every object
// KEY, scalar and structural boundary, never an ambiguous text join. The
// round-2 "string values only" collector could not claim to represent the whole
// structured input: `ToolUseContent.input` is an arbitrary object and
// `ToolResultContent.structuredContent` is arbitrary JSON
// (schema.ts:2393-2411,2432-2456), so a pin-valid request whose only sensitive
// text was an object KEY produced EMPTY inspection content and skipped the
// mediator entirely. Canonical bytes are the over-inclusive (safe) direction;
// an ALLOW decision still forwards the request unchanged (the adjudication's
// mandatory positive control, §4). The binary payloads of outer image/audio
// blocks remain the explicitly NAMED residual of the textual mediator seam
// (`ElicitationInspectionInput.Content`).
func samplingBlockText(blk canonValue) string {
	var parts []string
	typ := ""
	if tm := blk.member("type"); tm != nil && tm.val.kind == canonString {
		typ = tm.val.str
	}
	switch typ {
	case "tool_use":
		if in := blk.member("input"); in != nil {
			parts = append(parts, string(encodeCanonical(in.val)))
		}
	case "tool_result":
		if c := blk.member("content"); c != nil {
			parts = append(parts, string(encodeCanonical(c.val)))
		}
		if sc := blk.member("structuredContent"); sc != nil {
			parts = append(parts, string(encodeCanonical(sc.val)))
		}
	default:
		if t := blk.member("text"); t != nil && t.val.kind == canonString && t.val.str != "" {
			parts = append(parts, t.val.str)
		}
	}
	return strings.Join(parts, "\n")
}

// elicitResultReservedMembers are the ElicitResult members the PEP interprets
// (MCP 2026-07-28 RC, schema.ts:3121-3136).
var elicitResultReservedMembers = []string{"action", "content"}

// extractElicitationResponseContent extracts the user's input from an
// elicitation/create result (the exfil route). Per the pinned RC schema
// (an internal design note (not shipped):3121-3136) the result is:
//
//	{"action": "accept" | "decline" | "cancel", "content"?: {...}}
//
// and `content` is present only for an ACCEPTED form submission.
//
// Review round 1 (S5-03): stage 5 recognized only `action:"submit"` — a
// value the schema does not define — so a CONFORMING `{"action":"accept",
// "content":{...}}` carrying user data was released with no response mediation
// at all. The action value is no longer load-bearing for the decision to
// inspect: ANY content present is inspected, whatever the action says. That
// covers `accept`, the historical `submit` spelling (still honored — no
// conforming client behavior is narrowed) and any action a later revision
// adds.
//
// The second return is AMBIGUOUS: the result does not decode strictly, or it
// carries a case-variant alias of a member the PEP interprets. The caller
// WITHHOLDS the response rather than releasing content it could not classify.
func extractElicitationResponseContent(result json.RawMessage) ([]byte, bool) {
	if len(bytes.TrimSpace(result)) == 0 {
		// No result bytes at all: nothing to classify and nothing to release. This
		// is not the ambiguity class (there is no second reading of nothing), and
		// treating it as one would refuse an upstream that answers with an empty
		// body instead of a malformed one.
		return nil, false
	}
	v, err := decodeStrictJSON(result)
	if err != nil || v.kind != canonObject {
		// ElicitResult is a closed shape; a result a strict reader cannot read is
		// one this gateway cannot govern.
		return nil, true
	}
	if hasReservedAlias(v, elicitResultReservedMembers) {
		return nil, true
	}
	content := v.member("content")
	if content == nil {
		return nil, false // decline / cancel / URL-mode accept: no content member
	}
	return encodeCanonical(content.val), false
}

// NOTE (round-1 F-07): the mirror-image `mrtrResponseEnvelope` /
// `extractMRTRInputResponses` pair is GONE. Reading params.inputResponses back
// out of the request bytes with encoding/json matched the member
// case-INSENSITIVELY, so a request carrying both `InputResponses` and
// `inputResponses` let the mediator approve one member while a case-folding
// upstream consumed the other from the very bytes forwarded. The mediated member
// is now a reserved key of the request canonicalization (aliases refused) and is
// extracted from the STRICT tree with exact casing —
// canonicalTaskParams.InputResponses / canonicalToolCallParams.InputResponses.

// mrtrAuthority is the CALLER-SELECTED authority profile of one MRTR result
// classification (design adjudication §2): the AUTHORITATIVE LOCAL CONTEXT
// — the method the gateway dispatched, the task registration and ledger record,
// the per-request task-capability declaration — selects WHICH response contract
// and WHICH exact discriminators are authoritative; the body then supplies the
// discriminator value and the exact payload to mediate. Incidental body shape
// never selects its own contract. There is NO default profile: the zero value
// is invalid and planMRTRRelease fails safe on it (§5 — no body-only fallback).
type mrtrAuthority int

const (
	// mrtrAuthorityNone is the invalid zero value — an internal bug, never a
	// classification. planMRTRRelease answers it with a noAuthority plan and the
	// call site withholds the raw result behind a fixed evidence-unavailable
	// response (adjudication §5).
	mrtrAuthorityNone mrtrAuthority = iota
	// mrtrAuthorityCoreResult: a synchronous core result (the tools/call
	// CallToolResult | InputRequiredResult union, schema.ts:1830-1837). Only the
	// exact core discriminator `resultType:"input_required"` selects MRTR;
	// `status` has no authority here — it is extension data on an open Result
	// (schema.ts:223-235).
	mrtrAuthorityCoreResult
	// mrtrAuthorityTaskHandle: a strictly validated CreateTaskResult selected by
	// the request's declared Tasks capability plus exact resultType:"task". The
	// validated task `status` is authoritative for MRTR here.
	mrtrAuthorityTaskHandle
	// mrtrAuthorityTaskGet: the ledger-bound authoritative tasks/get read of an
	// owned, generation-pinned TaskRecord. The exact task `status` is
	// authoritative — the record selects the task contract and identity, the
	// response supplies its current exact variant.
	//
	// STAGE-7 (P0-3): the exact CORE resultType:"input_required" no longer
	// selects here. SEP-2663 defines `GetTaskResult` as `resultType:"complete"`
	// and represents an outstanding input requirement with the task status, so
	// the core discriminator on this method is an upstream MUST NOT — refused,
	// never mediated and never released (see coreResultTypeAuthoritative).
	mrtrAuthorityTaskGet
	// mrtrAuthorityTaskCancel: the cooperative tasks/cancel acknowledgement — an
	// upstream task result relayed verbatim but NOT an authoritative task-status
	// read (tasks.go TaskCancel; adjudication §2). `status` is NEVER interpreted
	// as task state, and exact status/inputRequests on a complete acknowledgement
	// are preserved extension data.
	//
	// STAGE-7 (P0-3): the exact CORE resultType:"input_required" no longer
	// selects here either — `CancelTaskResult` is the ack-only
	// `resultType:"complete"` shape. This profile therefore has NO authoritative
	// MRTR discriminator left: every classification it can produce is the
	// unsanctioned-core refusal. That is the contract, not an oversight, and
	// TestStage7GuardStrictness pins it so a future edit that gives the cancel
	// leg a discriminator back has to face the mediation/release legs it revives.
	mrtrAuthorityTaskCancel
)

// mrtrSanctionedCoreMethods is the CLOSED matrix of client requests whose
// response MAY carry the CORE `InputRequiredResult` variant.
//
// MRTR (mrtr.mdx:182-192), verbatim: "Servers MAY send `InputRequiredResult`
// responses on the following client requests: prompts/get | Yes;
// resources/read | Yes; tools/call | Yes … Servers MUST NOT send
// `InputRequiredResult` responses on any other client requests."
//
// This is the ONE place that norm is written down. Two mechanisms consult it,
// deliberately, because two kinds of relay surface exist:
//
//   - the surfaces that run the common classifier (tools/call, ui:// reads and
//     the task methods) get the answer through the AUTHORITY PROFILE, which
//     restates the same fact at the level the classifier works at
//     (mrtrAuthority.coreResultTypeAuthoritative): the core-result contract
//     serves exactly these three methods, every Tasks profile serves a tasks/*
//     method this table excludes;
//   - the surfaces that relay a result without classifying it (the generic
//     dispatch, elicitation/create, sampling/createMessage) consult the table
//     itself through coreMRTRRefusalOn.
//
// The redundancy is the point: a MUST NOT enforced by one route's copy is
// enforced by whichever copy the traffic happens to reach.
var mrtrSanctionedCoreMethods = map[string]struct{}{
	"prompts/get":    {},
	"resources/read": {},
	"tools/call":     {},
}

// mrtrReasonUnsanctionedOrigin is the STABLE reason class of an upstream result
// that carries the core input-required discriminator on a method that must not
// carry one — the mediation design's controlled `reason_code` enum (§5, §7).
//
// ADJUDICATION (2026-07-30): the design names `mrtr.unsanctioned_origin` for the
// whole class while the r2 review proposed `mrtr.unsanctioned_task_result` for
// its Tasks subcase. ONE code is used, the design's, because the fact is one:
// the ORIGIN METHOD does not sanction the variant. Which method it was is
// already in the audit record; a second code for a subset of the same rule would
// make the enum describe the route instead of the violation.
const mrtrReasonUnsanctionedOrigin = "mrtr.unsanctioned_origin"

// mrtrReasonMalformedResult is the design's controlled class for a result whose
// STRUCTURE violates the contract (mediation design §5 names it in the reason
// enum): here, a DUPLICATED exact discriminator key — ambiguous by
// construction, produced by no conforming server. M-1R2 (r2 contrast): it is a
// DIFFERENT fact from mrtr.unsanctioned_origin, and the two must never share a
// record — only a single exact string `input_required` may be recorded as
// carrying the core discriminator; a duplicate is refused for what it IS.
const mrtrReasonMalformedResult = "mrtr.malformed_result"

// mrtrUnsanctionedWireMessage is the ONE client-facing message of that refusal,
// shared by every surface so a client sees the same protocol fault whichever
// door produced it. It is a Bad Gateway, not a policy deny: the fault is the
// upstream's conformance, never the content (adjudication §7).
const mrtrUnsanctionedWireMessage = "upstream returned an input-required result on a method that must not carry one"

// mrtrAmbiguousDiscriminatorWireMessage is the client-facing message of a
// duplicated-discriminator refusal. Deliberately free of the `input_required`
// literal: the wire must state what was OBSERVED (an ambiguous duplicate),
// never a value the body may not carry (M-1R2).
const mrtrAmbiguousDiscriminatorWireMessage = "upstream returned a result with an ambiguous duplicated discriminator"

// coreMRTRRefusal classifies what the exact-discriminator scan OBSERVED on a
// method outside the sanctioned matrix — two refusals that must never share a
// record (M-1R2): a single exact `input_required` literal is the unsanctioned
// core variant; a duplicated exact key is an ambiguity refused for what it IS.
type coreMRTRRefusal int

const (
	// coreMRTRAdmitted: nothing at the discriminator level refuses this result
	// here (the method is sanctioned, or the discriminator is a clean non-match).
	coreMRTRAdmitted coreMRTRRefusal = iota
	// coreMRTRUnsanctioned: a SINGLE exact string `resultType:"input_required"`
	// on a method that must not carry the variant — mrtr.unsanctioned_origin.
	coreMRTRUnsanctioned
	// coreMRTRDuplicated: the exact `resultType` key appears more than once,
	// whatever the value types — ambiguous by construction, refused as
	// mrtr.malformed_result. The record never affirms the input_required
	// literal: the duplication, not a value, is the observed fact.
	coreMRTRDuplicated
)

// mrtrReasonUnmappedRefusal is NOT one of the design's controlled reason codes, and that
// is the whole point of it. coreMRTRRefusal is an OPEN enum — this stage widened it from
// two values to three — so a fourth class can be added without its mapping. Before P-2
// (2026-08-01) both reason() and wire() were `if duplicated { … } return <unsanctioned>`,
// so that fourth class would have been RECORDED as mrtr.unsanctioned_origin and told the
// client it was an unsanctioned origin: a controlled code asserted about a fact nobody
// established. A wrong controlled code is worse than an obviously invalid one — analytics
// and evidence key on that enum, and a lie there is indistinguishable from the truth.
//
// So an unmapped class refuses, says so, and is self-announcing rather than borrowing
// another class's identity. TestCoreMRTRRefusalTotalShape forbids reaching it: every
// refusing value must carry a mapped controlled code, so adding a value without its
// mapping is a red test, not a mislabelled ledger row.
const mrtrReasonUnmappedRefusal = "mrtr.unmapped_refusal_class"

// mrtrUnmappedRefusalWireMessage is its client-facing twin: true, and claiming nothing
// about WHICH conformance fault was observed, because at this point nothing knows.
const mrtrUnmappedRefusalWireMessage = "upstream returned a result the discriminator guard refused"

// refused reports whether the classification denies the relay.
//
// THIS is the question every consumer must ask — not `== coreMRTRUnsanctioned ||
// == coreMRTRDuplicated`. An open enum consumed by enumeration is fail-open by
// extension: the day it grows, every site that enumerates admits the new class while
// every site that asks the property refuses it. See governGenericMRTR (P-3).
func (r coreMRTRRefusal) refused() bool { return r != coreMRTRAdmitted }

// reason returns the controlled reason class of the refusal.
func (r coreMRTRRefusal) reason() string {
	switch r {
	case coreMRTRUnsanctioned:
		return mrtrReasonUnsanctionedOrigin
	case coreMRTRDuplicated:
		return mrtrReasonMalformedResult
	case coreMRTRAdmitted:
		// Not a refusal; it has no reason class, and inventing one would put a
		// refusal code on a record that admitted the result.
		return ""
	}
	return mrtrReasonUnmappedRefusal
}

// wire returns the client-facing message of the refusal.
func (r coreMRTRRefusal) wire() string {
	switch r {
	case coreMRTRUnsanctioned:
		return mrtrUnsanctionedWireMessage
	case coreMRTRDuplicated:
		return mrtrAmbiguousDiscriminatorWireMessage
	case coreMRTRAdmitted:
		return ""
	}
	return mrtrUnmappedRefusalWireMessage
}

// genericAuditSentence is the audit record this refusal produces on the generic dispatch
// route. Each modeled class states what was OBSERVED and carries its own controlled code
// (M-1R2: the two must never share a record); an unmapped class states that it is one,
// rather than borrowing a sentence that would assert a fact nobody measured.
func (r coreMRTRRefusal) genericAuditSentence() string {
	switch r {
	case coreMRTRUnsanctioned:
		return "upstream answered input_required on a method the specification forbids it on (MRTR is limited to " +
			"prompts/get, resources/read and tools/call); reason class: " + r.reason()
	case coreMRTRDuplicated:
		return "upstream answered with a duplicated exact resultType discriminator — ambiguous by construction, " +
			"withheld; reason class: " + r.reason()
	}
	return "upstream result refused by the discriminator guard under a refusal class this build does not map; " +
		"reason class: " + r.reason()
}

// coreMRTRRefusalOn is the guard for the relay surfaces that do not run the
// classifier; the classifier's own equivalents are
// mrtrClassification.unsanctionedCore and .ambiguousDiscriminator. On a
// SANCTIONED method it admits everything — the classifier owns those results,
// and refuses a duplicated discriminator there through its unreadable leg.
func coreMRTRRefusalOn(method string, result json.RawMessage) coreMRTRRefusal {
	if _, ok := mrtrSanctionedCoreMethods[method]; ok {
		return coreMRTRAdmitted
	}
	if selectsCoreInputRequired(result) {
		return coreMRTRUnsanctioned
	}
	if duplicatedResultTypeDiscriminator(result) {
		return coreMRTRDuplicated
	}
	return coreMRTRAdmitted
}

// selectsCoreInputRequired reads the EXACT core `resultType` discriminator with
// the duplicate-TOLERANT scan (scanTopLevelExactMember), so a result whose
// strict tree was refused for an unrelated duplicate or lossy string is still
// classified rather than silently relayed.
//
// It answers TRUE for exactly ONE observation — a SINGLE occurrence of the
// exact key whose string value is exactly "input_required" — because it feeds
// records that AFFIRM that literal (M-1R2: a refusal may never state a value
// the body does not carry). The DUPLICATED key, whatever its value types, is a
// different observed fact with its own predicate
// (duplicatedResultTypeDiscriminator) and its own reason class; every caller
// consults both, so splitting them weakened nothing.
//
// A case variant (`ResultType`) is EXTENSION data on an open Result and never
// participates — JSON and MCP property names are case-sensitive, so it is a
// DIFFERENT member a conforming extension may carry (schema.ts:208-216,223-235);
// a custom open-enum value ("INPUT_REQUIRED") is not the reserved literal; bytes
// with no readable top-level discriminator select nothing. A SINGLE non-string
// occurrence selects nothing either: there is no duplication ambiguity and no
// reserved literal.
func selectsCoreInputRequired(raw []byte) bool {
	vals, occurrences := scanTopLevelExactMember(raw, "resultType")
	return occurrences == 1 && len(vals) == 1 && vals[0] == resultTypeInputRequired
}

// duplicatedResultTypeDiscriminator reports the OTHER deny-closed observation:
// the exact `resultType` key appears MORE THAN ONCE — WHATEVER the value types
// (M-1 of the r1 contrast: a string-only count let a `null` second occurrence
// hide the duplication). A first-wins and a last-wins reader can disagree about
// the member's very type, no conforming server produces the shape, and the
// refusal records the DUPLICATION — never a literal the scan did not observe
// as the single value (M-1R2).
func duplicatedResultTypeDiscriminator(raw []byte) bool {
	_, occurrences := scanTopLevelExactMember(raw, "resultType")
	return occurrences > 1
}

// valid reports whether the profile is one of the four caller-selectable
// authorities (the zero value is not).
func (a mrtrAuthority) valid() bool {
	switch a {
	case mrtrAuthorityCoreResult, mrtrAuthorityTaskHandle, mrtrAuthorityTaskGet, mrtrAuthorityTaskCancel:
		return true
	}
	return false
}

// taskStatusAuthoritative reports whether the exact task `status` member is an
// MRTR discriminator under this profile (adjudication §2: only the task-handle
// and task-get contracts; never the core result, never the cancel ack).
func (a mrtrAuthority) taskStatusAuthoritative() bool {
	return a == mrtrAuthorityTaskHandle || a == mrtrAuthorityTaskGet
}

// coreResultTypeAuthoritative reports whether the exact CORE
// `resultType:"input_required"` discriminator SELECTS MRTR under this profile —
// the profile-level restatement of mrtrSanctionedCoreMethods (stage-7 P0-3).
//
// Only the core-result contract sanctions it, because only it serves the three
// methods the table lists. Every Tasks profile serves a tasks/* method, and the
// extension defines all three of those results as `resultType:"complete"` — the
// task handle's own variant included — so the core discriminator under a task
// profile is an upstream MUST NOT, not a payload to mediate.
func (a mrtrAuthority) coreResultTypeAuthoritative() bool {
	return a == mrtrAuthorityCoreResult
}

// extractMRTRInputRequests reports the MRTR input-request payloads a result
// carries UNDER THE CALLER-SELECTED AUTHORITY PROFILE, and whether the governed
// member is UNREADABLE (the caller must refuse the result: 502, no raw body).
//
// The exact discriminator procedure (design adjudication §1):
//
//  1. Strictly decode one object (duplicate keys and lossy strings refused by
//     decodeStrictJSON). A result the strict reader cannot read is not
//     classifiable as an MRTR carrier HERE — the caller's own contract
//     validators own malformed results — and "unreadable" means only that an
//     exact governed member selected by the caller's contract cannot be
//     projected, never that some extension member looks unusual.
//  2. Read only the exact member `resultType`, and only under a profile that
//     SANCTIONS the core variant (coreResultTypeAuthoritative — stage-7 P0-3).
//     Exact "input_required" then selects the core InputRequiredResult variant
//     (schema.ts:208-216,223-235); every other exact value — "complete", "task",
//     custom open-enum strings, or an absent member — does not. Under a Tasks
//     profile the core discriminator selects NOTHING here; whether that is an
//     upstream MUST NOT is the CLASSIFIER's question (classifyMRTRResult), and a
//     caller that skips the classifier is skipping the guard — this extractor is
//     not the enforcement point and never was.
//  3. Only under a profile whose TASK contract is authoritative (task-handle,
//     task-get) may the exact task `status:"input_required"` select the task
//     MRTR payload instead. Under core-result and task-cancel, `status` is
//     extension data and is preserved, never interpreted.
//  4. No case-folded key or value participates: on an open Result, `Status`,
//     `ResultType` and every other case variant are EXTENSION members the exact
//     reader never confuses with the authoritative one (round-3 findings 2 and
//     3 dissolve — the former alias/status heuristics were the oscillation).
//  5. Only after one of those authoritative conditions holds is exact
//     `inputRequests` consulted: an absent member or an empty map `{}` yields
//     ZERO entries (the pin's map has no minimum cardinality and a
//     request-state-only result is expressly permitted —
//     schema.ts:553-555,571-595; round-3 finding 4); a present member of any
//     other kind is UNREADABLE and the result is refused (the retained
//     projection-integrity check, adjudication §4).
func extractMRTRInputRequests(result json.RawMessage, authority mrtrAuthority) (map[string]json.RawMessage, bool) {
	v, err := decodeStrictJSON(result)
	if err != nil {
		// Round-4 finding 1: a strict-decode failure (a duplicate key or a
		// lossy string at ANY depth) must have ONE fail-safe meaning at the selected
		// governed contract — UNREADABLE (refuse) — never a silent "not MRTR" that
		// takes the plain release leg. The reviewer's transaction is an EXACT core
		// input_required result carrying governed inputRequests plus an unrelated
		// duplicate extension key: decodeStrictJSON refuses the whole tree, and the
		// prior `return nil, false` released the payload. Read the exact
		// discriminator RESILIENTLY (tolerant of the unrelated duplicate) and refuse
		// only when it selects a governed input-required payload this gateway cannot
		// project; a DEFINITIVELY non-governed result (a single exact
		// non-input_required resultType, or bytes carrying no readable discriminator)
		// has no governed payload and relays (round-4 finding 3 — no new refusal of
		// a valid complete open result).
		if selectsInputRequiredGoverned(result, authority) {
			return nil, true
		}
		return nil, false
	}
	if v.kind != canonObject {
		return nil, false
	}
	inputRequired := false
	if authority.coreResultTypeAuthoritative() {
		if rt := v.member("resultType"); rt != nil && rt.val.kind == canonString &&
			rt.val.str == resultTypeInputRequired {
			inputRequired = true
		}
	}
	if !inputRequired && authority.taskStatusAuthoritative() {
		if st := v.member("status"); st != nil && st.val.kind == canonString &&
			st.val.str == taskStatusInputRequired {
			inputRequired = true
		}
	}
	if !inputRequired {
		return nil, false
	}
	reqs := v.member("inputRequests")
	if reqs == nil {
		// input_required with only requestState (load shedding, schema.ts:571-595):
		// no input-request payloads to mediate, nothing governed to release.
		return nil, false
	}
	if reqs.val.kind != canonObject {
		// The SELECTED governed member is present with the wrong type: it cannot
		// be projected, so the result is unreadable and refused.
		return nil, true
	}
	entries := make(map[string]json.RawMessage, len(reqs.val.obj))
	for i := range reqs.val.obj {
		m := reqs.val.obj[i]
		// Round-4 finding 2: the InputRequests map KEY is a server-assigned,
		// arbitrary, GOVERNED identifier (schema.ts:544-555), exactly the arbitrary
		// structured subtree round-3 finding 1 required be carried. Projecting only
		// `m.val` dropped the identifier from every inspected field, so a policy
		// token placed solely in the key was released without inspection. Project the
		// canonical SINGLETON entry {id: value}: the identifier is now an object key
		// of the inspected content and therefore participates in the mediator
		// decision and in the content hash.
		singleton := canonValue{kind: canonObject, obj: []canonMember{{key: m.key, val: m.val}}}
		entries[m.key] = encodeCanonical(singleton)
	}
	return entries, false
}

// mrtrClassification is the ONE answer every relay surface asks about an
// upstream result: is this an InputRequiredResult under the caller-selected
// authority, and can its governed member be projected?
//
// It exists because the guard it feeds is a MUST NOT, and a MUST NOT enforced by
// a copy per route is enforced by whichever copy the traffic happens to reach.
// Codex's 2026-07-29 adjudication (§7) states the rule this closes: "El guard
// debe vivir en el clasificador común previo a cualquier `writeResult`, no sólo
// en el generic, para que ninguna ruta especializada lo salte." Both routes that
// relay a resources/read — the generic dispatch and the ui:// render gate — now
// call THIS, as does the tools/call release planner.
type mrtrClassification struct {
	// inputRequired: the result selects the input-required variant through a
	// discriminator this profile sanctions.
	inputRequired bool
	// unreadable: the selected governed member is present but cannot be
	// projected. The caller refuses (502); it never relays raw bytes it could
	// not show the mediator.
	unreadable bool
	// unsanctionedCore: the result carries the CORE input-required discriminator
	// under a profile whose contract FORBIDS that variant — every Tasks profile
	// (stage-7 P0-3). It is a protocol violation of the upstream, never a payload:
	// no entries are projected, the mediator is never consulted, and the caller
	// refuses with 502/-31002. `inputRequired` stays true because governed bytes
	// WERE observed: the refusal is a release DISPOSITION and needs its durable
	// child (stage-7 B-bis), not a silent drop.
	unsanctionedCore bool
	// ambiguousDiscriminator: under a profile that forbids the core variant, the
	// exact `resultType` key is DUPLICATED (any value types) — refused for what
	// it IS: an ambiguity no conforming server produces (M-1R2, reason class
	// mrtr.malformed_result). Deliberately DISTINCT from unsanctionedCore so no
	// record can affirm the input_required literal for a body that may not carry
	// it, and `inputRequired` stays false for the same honesty. The refusal is
	// still a release disposition with its durable withheld child.
	ambiguousDiscriminator bool
	// entries are the exact-cased singleton {id: request} payloads to mediate;
	// empty for a requestState-only input_required result, which carries nothing
	// to inspect.
	entries map[string]json.RawMessage
}

// classifyMRTRResult classifies result under authority. An empty result is not a
// classification (the caller has nothing to relay).
//
// The MUST NOT guard runs FIRST (stage-7 P0-3): a result carrying the core
// input-required discriminator under a profile that does not sanction it is
// refused as a protocol violation before anything is projected, so no Tasks
// caller can reach the mediator or a release with a variant the extension
// forbids — the exploit the r2 review measured on tasks/get and tasks/cancel.
func classifyMRTRResult(result json.RawMessage, authority mrtrAuthority) mrtrClassification {
	if len(result) == 0 {
		return mrtrClassification{}
	}
	if !authority.coreResultTypeAuthoritative() {
		if selectsCoreInputRequired(result) {
			return mrtrClassification{inputRequired: true, unsanctionedCore: true}
		}
		if duplicatedResultTypeDiscriminator(result) {
			// M-1R2: the OTHER discriminator-level refusal, recorded for what it
			// is — a duplicated key — never as the literal it might have carried.
			return mrtrClassification{ambiguousDiscriminator: true}
		}
	}
	entries, unreadable := extractMRTRInputRequests(result, authority)
	return mrtrClassification{
		inputRequired: unreadable || len(entries) > 0 || selectsInputRequiredGoverned(result, authority),
		unreadable:    unreadable,
		entries:       entries,
	}
}

// selectsInputRequiredGoverned reports whether raw resiliently selects a GOVERNED
// input-required payload under the authority profile, consulted ONLY after the
// strict decode has already FAILED (round-4 finding 1). It reads the exact
// discriminator with scanTopLevelExactMember — tolerant of an unrelated duplicate
// that refused the strict tree — and returns true (⇒ the caller refuses as
// UNREADABLE) when:
//
//   - under a profile that SANCTIONS the core variant, the exact `resultType`
//     is the single string "input_required" (selectsCoreInputRequired) or the
//     exact key is DUPLICATED (duplicatedResultTypeDiscriminator) — a reading
//     that cannot be excluded, deny-closed; OR
//   - under a task-status-authoritative profile (task-handle, task-get) the exact
//     `status` is "input_required" or the exact key appears more than once —
//     again whatever the value types (M-1: duplication is the ambiguity, not the
//     spelling of its values).
//
// It returns false — the result relays — for a DEFINITIVELY non-governed result:
// a single exact non-input_required `resultType` (e.g. "complete", a custom
// open-enum value), or bytes carrying no readable top-level discriminator at all.
// A non-pin-valid duplicate/lossy result that is NOT a governed carrier still
// relays its raw bytes exactly as before the redesign (round-4 finding 3 — zero
// new refusals of pin-valid traffic).
//
// STAGE-7 (P0-3): the core half is gated on the profile, and NOTHING is weakened
// by that gating — under a Tasks profile the very inputs it used to catch are
// caught EARLIER, and more precisely, by the unsanctioned-core guard in
// classifyMRTRResult (which is the only production caller's entry point).
func selectsInputRequiredGoverned(raw []byte, authority mrtrAuthority) bool {
	if authority.coreResultTypeAuthoritative() &&
		(selectsCoreInputRequired(raw) || duplicatedResultTypeDiscriminator(raw)) {
		return true
	}
	if authority.taskStatusAuthoritative() {
		stVals, stOccurrences := scanTopLevelExactMember(raw, "status")
		if stOccurrences > 1 {
			return true
		}
		if len(stVals) == 1 && stVals[0] == taskStatusInputRequired {
			return true
		}
	}
	return false
}

// extractMRTRPayloadContent projects ONE MRTR entry for content inspection: the
// entry's canonical JSON bytes, whole — every object key, scalar and structural
// boundary. Round 3 (the finding-1 class, adjudication §2 "for arbitrary
// structured JSON, project its canonical JSON bytes"): the previous
// string-values-only walk dropped every object KEY whenever at least one string
// value existed, so a secret encoded as a key of an InputRequest's params never
// reached the mediator. The entries are already canonical (extracted from the
// strict tree via encodeCanonical), so the projection is the bytes themselves.
func extractMRTRPayloadContent(raw json.RawMessage) []byte {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil
	}
	return append([]byte(nil), trimmed...)
}
