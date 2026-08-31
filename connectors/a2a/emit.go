// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// Origin/resource kinds emitted by this connector (documented in the contract).
// An A2A edge is agent→agent: the origin is the client/calling agent, the resource
// is the remote/server agent it communicated with.
const (
	originAgent   = "agent"
	subjectAgent  = "a2a.agent" // SubjectKind for an A2A agent (findings)
	resourceAgent = "a2a.agent" // ResourceKind for the remote agent (edges)
)

// Finding kinds this connector emits (documented in the contracts).
const (
	findingTrust            = "a2a_trust"             // agent-card signature trust outcome
	findingSecurity         = "a2a_security"          // declared securitySchemes (catalog metadata)
	findingDiscovery        = "a2a_discovery"         // card discovery failed
	findingTask             = "a2a_task"              // notable Task lifecycle state
	findingCapability       = "a2a_capability"        // declared skills + capabilities (catalog metadata)
	findingOAuthHygiene     = "a2a_oauth_hygiene"     // deprecated/weak OAuth posture in declared schemes
	findingSecurityEnforced = "a2a_security_enforced" // SecurityScheme enforcement result (deny or pass)
)

// interactionEdge turns one observed A2A interaction into a capability/communication
// edge feeding module IV. Confidence is `attributed` only when the REMOTE agent's
// card was verified against the operator's trust anchor (identity established);
// otherwise `approximate` (we observed the relationship but could not establish the
// peer's identity). Mode is unknown — this is a communication edge, not an R/RW
// access. References are scrubbed for secret shapes (minimal data, docs/SECURITY-HARDENING.md).
func interactionEdge(it interactionSpec, toTrust trustLevel, at time.Time) model.EdgeObservation {
	conf := model.ConfidenceApproximate
	if toTrust.trusted() {
		conf = model.ConfidenceAttributed
	}
	return model.EdgeObservation{
		OriginKind:   originAgent,
		OriginRef:    redact.Clean(it.From),
		ResourceKind: resourceAgent,
		ResourceRef:  redact.Clean(it.To),
		Mode:         model.ModeUnknown,
		Source:       model.SignalA2A,
		Confidence:   conf,
		ToolRef:      redact.Clean(it.Skill),
		ObservedAt:   at,
	}
}

// agentFindings returns the trust finding (always), a security-schemes finding (when
// the card declares any), a capability finding (when the card declares any skills or
// capabilities), and an OAuth-hygiene finding (when a declared oauth2 scheme carries a
// deprecated or weak posture) for a discovered agent.
func agentFindings(agent string, card AgentCard, lvl trustLevel, detail string, at time.Time) []model.FindingReport {
	out := []model.FindingReport{trustFinding(agent, lvl, detail, at)}
	if schemes := schemeTypes(card); len(schemes) > 0 {
		out = append(out, securitySchemesFinding(agent, schemes, at))
	}
	if f, ok := capabilityFinding(agent, card, lvl, at); ok {
		out = append(out, f)
	}
	if f, ok := oauthHygieneFinding(agent, card, at); ok {
		out = append(out, f)
	}
	return out
}

// oauthHygieneFinding inventories the OAuth POSTURE of a card's declared oauth2
// schemes against the v1.0 hygiene bar: the implicit and password (ROPC)
// flows are DEPRECATED in v1.0 ("Use Authorization Code + PKCE instead" / "... or
// Device Code", a2a.proto — whats-new lists them as removed per the OAuth 2.0
// Security BCP); an authorization-code flow SHOULD require PKCE (RFC 7636); the
// RFC 8414 metadata URL requires TLS. A deprecated flow is Low (a peer still
// advertising removed, BCP-deprecated grants); PKCE-not-required / plain-HTTP
// metadata alone is Info (worth cataloging, not a violation). ok=false when the
// card declares no oauth2 scheme or a clean posture. Minimal data: issue LABELS
// only, detail hashed.
func oauthHygieneFinding(agent string, card AgentCard, at time.Time) (model.FindingReport, bool) {
	var issues []string
	sev := model.SeverityInfo
	for name, sc := range card.SecuritySchemes {
		o := sc.OAuth2
		if o == nil {
			continue
		}
		label := strings.TrimSpace(redact.Clean(name))
		if o.Flows != nil {
			if o.Flows.Implicit != nil {
				issues = append(issues, label+":implicit-flow(deprecated)")
				sev = model.SeverityLow
			}
			if o.Flows.Password != nil {
				issues = append(issues, label+":password-flow(deprecated)")
				sev = model.SeverityLow
			}
			if o.Flows.AuthorizationCode != nil && !o.Flows.AuthorizationCode.PKCERequired {
				issues = append(issues, label+":pkce-not-required")
			}
		}
		if u := strings.TrimSpace(o.OAuth2MetadataURL); u != "" && !strings.HasPrefix(strings.ToLower(u), "https://") {
			issues = append(issues, label+":oauth2-metadata-not-https")
			sev = model.SeverityLow
		}
	}
	if len(issues) == 0 {
		return model.FindingReport{}, false
	}
	sort.Strings(issues)
	return model.FindingReport{
		Kind:        findingOAuthHygiene,
		Severity:    sev,
		SubjectKind: subjectAgent,
		SubjectRef:  redact.Clean(agent),
		Title:       "A2A agent OAuth posture: " + strings.Join(issues, ", "),
		DetailHash:  redact.Hash("a2a-oauth-hygiene agent=" + agent + " issues=" + strings.Join(issues, ",")),
		OccurredAt:  at,
	}, true
}

// capabilityFinding inventories the SKILLS and capabilities a discovered agent declares —
// the discovery half of "Agent Cards firmados: verificación de capacidades". It is
// catalog metadata (Info) feeding module IV/V: the skills a peer claims and the protocol
// capabilities (streaming/push/extensions) it advertises. The trust dimension is explicit:
// only a `verified` card's declarations are cryptographically attributed; for any other
// trust level the declarations are the card's OWN (self-claimed, UNTRUSTED) and the title
// says so — a control plane never silently treats a self-claimed capability as verified
// (docs/SECURITY-HARDENING.md anti-evasion). ok is false when the card declares no skills and no capabilities
// (nothing to catalog). Minimal data: skill/capability LABELS only, detail hashed.
func capabilityFinding(agent string, card AgentCard, lvl trustLevel, at time.Time) (model.FindingReport, bool) {
	skills := card.skillRefs()
	caps := card.capabilityLabels()
	if len(skills) == 0 && len(caps) == 0 {
		return model.FindingReport{}, false
	}
	trustNote := "self-claimed (UNTRUSTED)"
	if lvl.trusted() {
		trustNote = "verified"
	}
	title := "A2A agent capability surface (" + trustNote + "): " +
		strconv.Itoa(len(skills)) + " skill(s)"
	if len(caps) > 0 {
		title += ", capabilities " + strings.Join(caps, "/")
	}
	return model.FindingReport{
		Kind:        findingCapability,
		Severity:    model.SeverityInfo,
		SubjectKind: subjectAgent,
		SubjectRef:  redact.Clean(agent),
		Title:       title,
		DetailHash: redact.Hash("a2a-capability agent=" + agent + " trust=" + string(lvl) +
			" protocol=" + card.ProtocolVersion +
			" skills=" + strings.Join(skills, ",") + " caps=" + strings.Join(caps, ",")),
		OccurredAt: at,
	}, true
}

// trustFinding grades the agent-card signature verification. A verified card is
// Info; a self-asserted (jku-only) or unsigned card is Low (identity not
// established); an unverifiable signature is Medium and clearly UNTRUSTED. A bad
// signature is NEVER treated as good (anti-evasion).
func trustFinding(agent string, lvl trustLevel, detail string, at time.Time) model.FindingReport {
	var sev model.Severity
	var title string
	switch lvl {
	case trustVerified:
		sev, title = model.SeverityInfo, "A2A agent card signature verified (identity attributed)"
	case trustSelfAsserted:
		sev, title = model.SeverityLow, "A2A agent card verified only against self-asserted jku — identity NOT established (UNTRUSTED)"
	case trustUnsigned:
		sev, title = model.SeverityLow, "A2A agent card is UNSIGNED — identity not verifiable (UNTRUSTED)"
	default: // trustUnverified
		sev, title = model.SeverityMedium, "A2A agent card signature could NOT be verified — UNTRUSTED"
	}
	return model.FindingReport{
		Kind:        findingTrust,
		Severity:    sev,
		SubjectKind: subjectAgent,
		SubjectRef:  redact.Clean(agent),
		Title:       title,
		DetailHash:  redact.Hash("a2a-trust agent=" + agent + " level=" + string(lvl) + " detail=" + detail),
		OccurredAt:  at,
	}
}

// securitySchemesFinding inventories the auth schemes a peer declares (which kinds,
// never credentials) — catalog metadata for module IX/governance.
func securitySchemesFinding(agent string, schemes []string, at time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        findingSecurity,
		Severity:    model.SeverityInfo,
		SubjectKind: subjectAgent,
		SubjectRef:  redact.Clean(agent),
		Title:       "A2A agent declares security schemes: " + strings.Join(schemes, ", "),
		DetailHash:  redact.Hash("a2a-security agent=" + agent + " schemes=" + strings.Join(schemes, ",")),
		OccurredAt:  at,
	}
}

// discoveryFailedFinding reports that an agent's card could not be discovered/parsed
// (a gap is a signal, not silence). The error detail is hashed.
func discoveryFailedFinding(agent string, err error, at time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        findingDiscovery,
		Severity:    model.SeverityMedium,
		SubjectKind: subjectAgent,
		SubjectRef:  redact.Clean(agent),
		Title:       "A2A agent card discovery failed",
		DetailHash:  redact.Hash("a2a-discovery agent=" + agent + " err=" + err.Error()),
		OccurredAt:  at,
	}
}

// taskStateFinding emits a finding for a notable Task lifecycle state (failure,
// rejection, or an input/auth request) or an unrecognized state; the happy path
// emits only the edge (ok=false). The state name is non-sensitive.
func taskStateFinding(it interactionSpec, at time.Time) (model.FindingReport, bool) {
	state := TaskState(strings.TrimSpace(it.State))
	if state == "" {
		return model.FindingReport{}, false
	}
	var sev model.Severity
	var title string
	switch {
	case state.notable():
		sev = model.SeverityLow
		title = "A2A task reached " + string(state)
	case !state.known():
		sev = model.SeverityInfo
		title = "A2A task reported an unrecognized state"
	default:
		return model.FindingReport{}, false // submitted/working/completed/canceled — normal
	}
	return model.FindingReport{
		Kind:        findingTask,
		Severity:    sev,
		SubjectKind: subjectAgent,
		SubjectRef:  redact.Clean(it.To),
		Title:       title,
		DetailHash:  redact.Hash("a2a-task from=" + it.From + " to=" + it.To + " state=" + it.State + " skill=" + it.Skill),
		OccurredAt:  at,
	}, true
}

// schemeTypes returns the sorted, recognized security-scheme KINDS a card declares.
// In v1.0 the kind is the SecurityScheme oneof member (kindLabel); the v0.x `type`
// string is a lenient-parse fallback, with an unrecognized legacy type reported as
// "other:<type>" rather than dropped.
func schemeTypes(card AgentCard) []string {
	if len(card.SecuritySchemes) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	for _, sc := range card.SecuritySchemes {
		t := sc.kindLabel()
		if t == "" {
			continue
		}
		if _, ok := securitySchemeTypes[t]; !ok {
			t = "other:" + t
		}
		seen[t] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}
