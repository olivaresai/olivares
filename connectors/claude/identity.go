// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import (
	"sync"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// Identity resource kinds (OBS-09). Claude Code tags every OTEL record with the
// organization and account that owns the session, deliberately in the same format
// as the Anthropic admin APIs so a SIEM/audit trail can build a per-user view and
// correlate to org-level inventory. The connector links the session to those
// identities as topology edges (ModeUnknown — an identity link is not an R/RW
// access, like the MCP-connection edge), so module I/III/VI can attribute activity
// per-org/per-account/per-agent instead of seeing a bare session id.
//
// These are opaque CORRELATION identifiers, not content: organization.id is a
// UUID and user.account_id is Anthropic's tagged id (user_01…). They are emitted
// by default; the OBS-10 redaction matrix governs whether they are carried further
// when the engine RE-EXPORTS to an external SIEM. The actually-sensitive field
// (user.email) is never parsed or emitted by this connector.
const (
	resIdentityOrg     = "identity.org"
	resIdentityAccount = "identity.account"
	resIdentityAgent   = "identity.agent"
	// resIdentitySubagent is the per-INSTANCE subagent id (VERIFIED
	// 2026-06-10): the agent_id a claude_code.llm_request/tool TRACE SPAN carries
	// (2.1.139/2.1.145). Distinct from identity.agent (agent.name — a TYPE label):
	// two concurrent subagents of the same type are distinct identity.subagent
	// nodes, which is what makes the spawn hierarchy walkable.
	resIdentitySubagent = "identity.subagent"
	// resEntrypoint is the session-launch surface (app.entrypoint: cli, sdk-ts,
	// claude-vscode, …). WHICH surface launched a fleet's sessions is a
	// governance dimension (an SDK-embedded fleet has a different risk posture
	// than interactive CLI use), modeled as session topology like the MCP edge.
	resEntrypoint = "app.entrypoint"
)

// claudeIdentity is the session-scoped identity an OTEL record/span/metric tags:
// the Claude org, account and agent that owns the session, plus the
// opt-in launch surface and the operator's allowlisted resource-attribute labels.
type claudeIdentity struct {
	sessionID  string
	orgID      string
	accountID  string
	agentName  string
	entrypoint string
	labels     map[string]string
}

// identity extracts the identity attribution from a parsed log event.
func (e claudeEvent) identity() claudeIdentity {
	return claudeIdentity{
		sessionID: e.sessionID, orgID: e.orgID, accountID: e.accountID,
		agentName: e.agentName, entrypoint: e.entrypoint, labels: e.labels,
	}
}

// identityEdges builds the session→identity topology edges for an identity that
// carries attribution. It returns one edge per present field (org, account,
// agent) and an empty slice when there is no session or no identity. The mode is
// Unknown (a link, not an access) and the confidence is Attributed (the origin is
// a concrete session). All three telemetry signals (logs, traces, metrics) feed
// it, so attribution is identical regardless of which Claude Code signal carried it.
func identityEdges(id claudeIdentity, at time.Time) []model.EdgeObservation {
	if id.sessionID == "" {
		return nil
	}
	var out []model.EdgeObservation
	add := func(kind, ref string) {
		if ref == "" {
			return
		}
		out = append(out, model.EdgeObservation{
			OriginKind:   originSession,
			OriginRef:    id.sessionID,
			ResourceKind: kind,
			ResourceRef:  ref,
			Mode:         model.ModeUnknown,
			Source:       model.SignalOTEL,
			Confidence:   model.ConfidenceAttributed,
			ObservedAt:   at,
			// The operator's allowlisted OTEL_RESOURCE_ATTRIBUTES ride the
			// once-per-session identity edges — session-stable attribution,
			// already scrubbed at collection (labelsFromAttrs). nil when off.
			Labels: id.labels,
		})
	}
	add(resIdentityOrg, id.orgID)
	add(resIdentityAccount, id.accountID)
	add(resIdentityAgent, id.agentName)
	add(resEntrypoint, id.entrypoint)
	return out
}

// labelsFromAttrs collects the operator-allowlisted resource-attribute labels
// from a merged attribute view (VERIFIED 2026-06-10: since 2.1.161 Claude
// Code attaches OTEL_RESOURCE_ATTRIBUTES values to every metric datapoint and
// event record, in addition to the OTLP resource block). Minimal-data rules:
// only the operator's ALLOWLISTED keys are read (empty allowlist = feature off →
// nil); a key colliding with a standard attribute the connector itself extracts
// is SKIPPED (mirroring the client's own "custom keys never override the
// built-ins" rule); values are scrubbed for embedded secrets.
func labelsFromAttrs(a attrs, allow []string) map[string]string {
	if len(allow) == 0 {
		return nil
	}
	var out map[string]string
	for _, key := range allow {
		if builtinAttrKeys[key] {
			continue
		}
		v := a.str(key)
		if v == "" {
			continue
		}
		scrubbed, _ := redact.Scrub(v)
		if scrubbed == "" {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[key] = scrubbed
	}
	return out
}

// builtinAttrKeys are the standard attributes the connector extracts itself — an
// operator allowlist entry naming one is ignored (it is already first-class
// attribution, and the client never lets a custom key override it either).
var builtinAttrKeys = map[string]bool{
	attrEventName: true, attrSessionID: true, attrAccountUUID: true,
	attrOrgID: true, attrAppVersion: true, attrAgentName: true,
	attrAppEntrypoint: true,
}

// identitySeen tracks which sessions have already had their identity edges emitted
// so the connector links a session to its org/account/agent ONCE rather than on
// every event (the link is stable for a session's lifetime; the engine merges any
// cross-restart re-emission by natural key). It is safe for concurrent use.
type identitySeen struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

func newIdentitySeen() *identitySeen { return &identitySeen{seen: map[string]struct{}{}} }

// first reports whether this is the first time the session is seen, recording it.
// An empty session id is never "first" (nothing to attribute).
func (s *identitySeen) first(sessionID string) bool {
	if sessionID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.seen[sessionID]; ok {
		return false
	}
	s.seen[sessionID] = struct{}{}
	return true
}
