// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// inspectiondecision.go defines the pure DATA envelope the inline-proxy decider
// (cmd/olivares, AGPL) and the OPTIONAL commercial content firewall
// (enterprise/contentfirewall, closed) exchange — the content-inspection sibling of
// egressdecision.go's ServerToolEgressDecision. It carries NO policy: the prompt-injection
// / exfiltration / unsafe-action detectors, their thresholds and the deny/HITL decision all
// live in the commercial add-on. This connector only defines the SHAPE the two sides agree
// on (built on the channels CollectRequestContent/CollectResponseContent produce), so
// neither imports the other's package — exactly as it already does for the egress gate.
//
// The zero value is a NO-FORWARD-CHANGE allow that BLOCKS nothing (Forward=false here means
// "I made no decision", which the decider reads as deny only when the inspector explicitly
// set Forward=false with a status; see ContentInspectionDecision). Minimal data (docs/SECURITY-HARDENING.md
// §3): nothing here carries a prompt, a response body, or a matched value — only channel
// kinds, counts, detector names and redacted detail strings the decider hashes.

package claudeapi

// Inspection direction labels.
const (
	InspectDirectionRequest  = "request"
	InspectDirectionResponse = "response"
)

// ContentInspectionInput is the minimal-data input the decider hands the firewall for one
// request or response: the resolved tenant + acting agent reference (both derived from the
// inbound credential, never a body field), the direction, the model, and the collected
// content channels. Tenant/ActorRef are opaque string keys; this connector is
// identity-blind and models no principal type. Channels carry the extracted text in flight
// (the deterministic detectors run on it in-process); the firewall returns only hashes.
type ContentInspectionInput struct {
	Tenant          string // resolved tenant key ("" = the global scope)
	ActorRef        string // authenticated acting agent ref ("" when none is bound)
	UnbindableAgent bool   // true for an API token whose authenticated identity has no agent binding
	Direction       string // request | response
	Model           string
	Channels        []ContentChannel // the collected content (CollectRequestContent/CollectResponseContent)
	Unscanned       bool             // at least one channel was opaque (the deny-closed signal the core DLP already saw)
}

// ContentInspectionDecision is the firewall's verdict for one inbound request or one
// outbound response. On a REQUEST a non-Forward verdict maps Status/ErrorType/Reason onto a
// proxy deny (the decider runs the firewall AFTER the deny-closed gates and BEFORE the
// fail-open budget gate, so it can DENY but never force an Allow nor bypass a prior gate).
// On a RESPONSE a Block is honored only when the connector buffered the response (the same
// preventive-vs-detective split as response DLP); a streamed response cannot be un-sent, so
// Block then records a detective finding only.
type ContentInspectionDecision struct {
	// Forward allows the request to continue (request direction). The zero value (false)
	// with a zero Status is "no decision" — the decider treats it as a clean pass, NOT a
	// deny (the inspector that found nothing returns the zero value). A deny MUST set Status.
	Forward bool
	// Block withholds the response (response direction; effective only in buffer mode).
	Block bool
	// Deny/Block mapping (used when Forward is false with a Status, or Block is true).
	Status    int    // e.g. 403
	ErrorType string // Anthropic-style error type ("permission_error", …)
	Reason    string // short, non-sensitive (never enumerates policy internals or a matched value)
	// ApprovalIntent, when non-nil, asks the decider to OPEN a governed approval for this
	// denied content via the existing approval seam (a future HITL plane can release it).
	// It never resumes the synchronous proxy call. nil = a clean deny.
	ApprovalIntent *ContentInspectionApprovalIntent
	// Findings the decider should publish on the bus (posture/forensic). nil = none.
	Findings []ContentInspectionFinding
	// Meter is the billable usage of THIS inspection (the metered add-on quantity). The
	// decider emits it as a CostSample on the observation sink. A zero Inspections value
	// means no work was billable (nothing to inspect).
	Meter ContentInspectionMeter
}

// ContentInspectionApprovalIntent is the minimal-data request to open a governed approval
// for denied content (the decider binds it to the existing approval bridge). Identifiers
// only — never a prompt, a matched value, or a secret.
type ContentInspectionApprovalIntent struct {
	Action   string // the governed action label (e.g. "inference.content.firewall")
	Detector string // the detector that fired (prompt_injection | exfiltration | unsafe_action)
	Subject  string // the approval subject reference (direction + detector)
	Reason   string // short, non-sensitive
	PlanHash string // anti-TOCTOU binding hash the firewall computed over the denied intent
}

// ContentInspectionFinding is a posture/forensic observation the firewall asks the decider
// to publish. Detail is non-sensitive context the decider hashes into a DetailHash; Severity
// is "high" | "medium" | "info". It carries no prompt, response, or matched value.
type ContentInspectionFinding struct {
	Kind     string
	Severity string // "high" | "medium" | "info"
	Title    string
	Detector string // prompt_injection | exfiltration | unsafe_action
	Channel  string // the channel kind that tripped it (tool_result, web_search_result, …)
	Detail   string // non-sensitive context, hashed by the decider
	OWASPLLM []string
	OWASPASI []string
}

// ContentInspectionMeter is the billable usage of one inspection. Inspections is the
// per-inspection billing count (1 when the firewall actually inspected ≥1 channel, else 0);
// Channels and Bytes carry the richer volume so a per-channel or per-byte pricing model can
// be derived later WITHOUT changing this contract (the unit is pricing call). It
// carries no content — only counts.
type ContentInspectionMeter struct {
	Inspections int
	Channels    int
	Bytes       int64
	Detectors   int
}
