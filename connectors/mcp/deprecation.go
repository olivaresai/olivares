// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/connectors/internal/textscan"
	"github.com/olivaresai/olivares/sdk/model"
)

// deprecation.go —: deprecation-aware MCP posture. The MCP project now runs a
// formal feature lifecycle (SEP-2596: Active → Deprecated → Removed, ≥12-month
// windows, 90-day expedited path only with a security advisory) with a canonical
// registry of Deprecated features. A server/client that still depends on a
// Deprecated feature is operational debt TODAY — the fleet must see it before the
// removal clocks run out.
//
// Verified against the primary sources on 2026-07-03. The 2026-07-28 revision is
// a frozen RC (frozen 2026-05-21) served under /specification/draft/ until FINAL
// publication on 2026-07-28; re-verify these rows against the published path after
// publication.
//
//   - Registry: https://modelcontextprotocol.io/specification/draft/deprecated
//     ("the canonical answer to what is on its way out, and by when"; a derived
//     view — the normative records are the per-feature notices + changelogs).
//     It is an MDX TABLE, not machine-readable JSON; the feed ingest below parses
//     the raw MDX (deprecationFeedURL).
//   - Roots, Sampling, Logging: SEP-2577 (merged 2026-05-15), Deprecated in the
//     2026-07-28 revision, earliest removal = first revision on or after
//     2027-07-28. Annotation-only: the methods keep working until removal.
//   - HTTP+SSE transport (2024-11-05): deprecated since 2025-03-26 (replaced by
//     Streamable HTTP); SEP-2596 reclassifies it formally — registry removal
//     window: "Three months after SEP-2596 reaches Final".
//   - sampling includeContext "thisServer"/"allServers": soft-deprecated in
//     2025-11-25 ("Servers SHOULD avoid using these values… SHOULD NOT use them
//     unless the client declares sampling.context"), Deprecated under SEP-2596;
//     removal follows Sampling.
//   - Dynamic Client Registration (RFC 7591): demoted to MAY in 2025-11-25
//     (CIMD SHOULD, SEP-991), Deprecated in 2026-07-28 (PR #2858), earliest
//     removal ≥ 2027-07-28.
//
// The static rules below are COMPILED-IN, severity-graded posture vectors fed by
// what the connector actually sees: the operator's fleet config (spec), the
// introspected catalog (capability advertisements, negotiated revision), the OAuth
// registration path actually taken (authRegistrationObservation), and any
// server-INITIATED requests observed during introspection (serverRequestObservation
// — the runtime seam surface.go documented). The remote feed (deprecationFeed*)
// is a DRIFT DETECTOR only: it can add Info findings about features these rules do
// not cover yet, but it NEVER changes a rule or a severity — a poisoned/hijacked
// feed must not be able to rescore the fleet (deny-closed, docs/SECURITY-HARDENING.md).

// Governance-relevant server-initiated methods the transports record when a server
// drives them against this client during introspection. The introspection client
// declares ZERO capabilities, so a conforming server sends NONE of these — any
// observation is both a feature-USE signal and a capability-negotiation violation.
const (
	methodSamplingCreate    = "sampling/createMessage"
	methodRootsList         = "roots/list"
	methodElicitationCreate = "elicitation/create"
	notifRootsListChanged   = "notifications/roots/list_changed"
)

// deprecatedFeature is one compiled-in entry of the official deprecated-features
// registry (the data the rule titles cite — public spec metadata, verified above).
type deprecatedFeature struct {
	token           string // normalized substring that matches the feed's Feature cell
	label           string
	source          string // SEP/PR that deprecated it
	deprecatedIn    string
	earliestRemoval string
}

// knownDeprecations mirrors the official registry rows. token is the EXACT
// normalized Feature cell (normalizeFeatureCell of the live row, verified
// 2026-06-10) — substring matching would silently swallow a NEW upstream
// deprecation that merely mentions one of these names (e.g. a future "Sampling
// preferences" row), defeating the drift detector. Exact matching errs the
// other way: an upstream cosmetic rename produces a (cheap, honest) Info drift
// finding instead of silence.
var knownDeprecations = []deprecatedFeature{
	{token: "includecontext: thisserver / allservers (sampling)", label: `sampling includeContext "thisServer"/"allServers"`, source: "SEP-2596", deprecatedIn: "2025-11-25 (soft)", earliestRemoval: "follows Sampling (eligible ≥ 2027-07-28)"},
	{token: "http+sse transport", label: "HTTP+SSE transport", source: "SEP-2596", deprecatedIn: "2025-03-26", earliestRemoval: "three months after SEP-2596 reaches Final"},
	{token: "dynamic client registration", label: "Dynamic Client Registration (RFC 7591)", source: "PR #2858", deprecatedIn: "2026-07-28", earliestRemoval: "first revision ≥ 2027-07-28"},
	{token: "roots", label: "Roots", source: "SEP-2577", deprecatedIn: "2026-07-28", earliestRemoval: "first revision ≥ 2027-07-28"},
	{token: "sampling", label: "Sampling", source: "SEP-2577", deprecatedIn: "2026-07-28", earliestRemoval: "first revision ≥ 2027-07-28"},
	{token: "logging", label: "Logging", source: "SEP-2577", deprecatedIn: "2026-07-28", earliestRemoval: "first revision ≥ 2027-07-28"},
}

// knownDeprecation returns the compiled entry whose token EXACTLY equals the
// normalized feed feature name.
func knownDeprecation(normalized string) (deprecatedFeature, bool) {
	for _, d := range knownDeprecations {
		if normalized == d.token {
			return d, true
		}
	}
	return deprecatedFeature{}, false
}

// deprecationIssues returns the deprecation-aware posture issues for one server,
// a pure function of (spec, catalog). They are SCORED (folded into the per-server
// posture grade by postureFindings): reliance on a feature with a running removal
// clock is a posture defect, not mere inventory. All carry MCP04 (OWASP MCP Top-10
// Software Supply Chain — an EOL'd protocol dependency is upgrade risk, the same
// family as a yanked package), except the context-over-sharing vectors (MCP10).
func deprecationIssues(spec serverSpec, cat catalog) []postureIssue {
	var out []postureIssue

	// --- Fleet config: the operator declares the legacy HTTP+SSE transport. The
	// connector introspects an "sse" spec over Streamable HTTP (best-effort), but
	// the CONFIG still binds this server to a transport with the registry removal
	// window "Three months after SEP-2596 reaches Final". Medium.
	if spec.Transport == transportSSE {
		out = append(out, postureIssue{
			mcp: "MCP04", severity: model.SeverityMedium,
			title:     "server is configured with the deprecated HTTP+SSE transport (SEP-2596; deprecated since 2025-03-26, removal window: three months after SEP-2596 reaches Final) — migrate to Streamable HTTP",
			detailKey: "deprecated-transport-config transport=sse",
		})
	}
	// --- Wire evidence: an HTTP server that negotiated 2024-11-05 — the only
	// revision whose HTTP transport IS HTTP+SSE (Streamable HTTP arrived in
	// 2025-03-26). The generic staleness flag lives in revision.go (unscored,
	// mcp_revision); THIS is the narrower deprecated-transport-era inference and
	// is scored. stdio servers on 2024-11-05 are merely stale, not SSE-bound.
	if spec.URL != "" && cat.server.ProtocolVersion == revision20241105 {
		out = append(out, postureIssue{
			mcp: "MCP04", severity: model.SeverityMedium,
			title:     "HTTP server negotiated protocol revision 2024-11-05 — the HTTP+SSE-era revision (transport deprecated by SEP-2596; migrate to Streamable HTTP)",
			detailKey: "deprecated-transport-era revision=" + revision20241105,
		})
	}

	// --- Capability advertisements (UNTRUSTED catalog metadata): a server that
	// ADVERTISES a SEP-2577-deprecated capability still builds on it. Low — the
	// deprecations are annotation-only until ≥ 2027-07-28, but the clock runs.
	for _, dep := range []struct{ key, note string }{
		{"logging", "migrate to stderr (stdio) / OpenTelemetry"},
		{"sampling", "migrate to direct LLM-provider integration"},
		{"roots", "a CLIENT capability advertised by a server (nonstandard); migrate to tool parameters / resource URIs"},
	} {
		if hasCapability(cat.server.Capabilities, dep.key) {
			out = append(out, postureIssue{
				mcp: "MCP04", severity: model.SeverityLow,
				title:     "server advertises the deprecated " + dep.key + " capability (SEP-2577, deprecated in 2026-07-28, earliest removal ≥ 2027-07-28) — " + dep.note,
				detailKey: "deprecated-capability key=" + dep.key,
			})
		}
	}

	out = append(out, registrationIssues(spec, cat)...)
	out = append(out, observedRequestIssues(cat)...)
	return out
}

// registrationIssues grades the OAuth client-registration path against the
// CIMD-over-DCR deprecation (PR #2858; spec priority: pre-registered → CIMD → DCR).
func registrationIssues(spec serverSpec, cat catalog) []postureIssue {
	if reg := cat.authReg; reg != nil {
		if reg.method != identityDCR {
			return nil // pre-registered/CIMD: nothing deprecated about the path taken
		}
		if reg.cimdSupported {
			// The AS advertises CIMD and the operator still registered via DCR —
			// migratable TODAY by hosting a client-id metadata document. Medium.
			return []postureIssue{{
				mcp: "MCP04", severity: model.SeverityMedium,
				title:     "client registered via deprecated Dynamic Client Registration although the AS advertises CIMD (PR #2858; set client_id_metadata_url to migrate)",
				detailKey: "dcr-despite-cimd",
			}}
		}
		// DCR-only AS: no CIMD migration path upstream yet — operational debt to
		// raise with the server's AS owner. Low.
		return []postureIssue{{
			mcp: "MCP04", severity: model.SeverityLow,
			title:     "client registered via deprecated Dynamic Client Registration and the AS advertises no CIMD support (DCR-only authorization server; RFC 7591 removal-eligible ≥ 2027-07-28)",
			detailKey: "dcr-only-as",
		}}
	}
	// No runtime observation (introspection failed, OAuth not exercised): flag the
	// declared intent when the fleet CONFIG can only ever register via DCR.
	if a := spec.Auth; a != nil && a.DynamicRegistration &&
		a.ClientID == "" && a.ClientIDMetadataURL == "" && a.BearerToken == "" {
		return []postureIssue{{
			mcp: "MCP04", severity: model.SeverityLow,
			title:     "fleet config relies on Dynamic Client Registration as the only client-identification path (RFC 7591 deprecated in favor of CIMD, PR #2858)",
			detailKey: "dcr-only-config",
		}}
	}
	return nil
}

// observedRequestIssues grades server-INITIATED requests the transports observed
// during introspection — the authoritative runtime seam surface.go reserved (the
// connector declares zero client capabilities, so EVERY such request is also a
// capability-negotiation violation, not just feature use).
func observedRequestIssues(cat catalog) []postureIssue {
	var out []postureIssue
	// The observer dedups by (method, includeContext), so one method can surface
	// several times with distinct includeContext values; the METHOD-level issue
	// must still count once (the per-value MCP10 issues below stay per value).
	methodSeen := map[string]bool{}
	for _, o := range cat.observed {
		switch o.method {
		case methodSamplingCreate:
			if !methodSeen[o.method] {
				methodSeen[o.method] = true
				out = append(out, postureIssue{
					mcp: "MCP04", severity: model.SeverityMedium,
					title:     "server actively initiated deprecated sampling/createMessage during introspection (SEP-2577; the client declared no sampling capability)",
					detailKey: "observed-deprecated method=" + o.method,
				})
			}
			if ic := o.includeContext; ic == "thisServer" || ic == "allServers" {
				sev := model.SeverityMedium
				if ic == "allServers" {
					// allServers demands context gathered across EVERY connected
					// server — the strongest over-sharing shape of the deprecated
					// values. High.
					sev = model.SeverityHigh
				}
				out = append(out, postureIssue{
					mcp: "MCP10", severity: sev,
					title:     "sampling request carried deprecated includeContext=" + ic + " (soft-deprecated since 2025-11-25; context over-sharing vector)",
					detailKey: "observed-includecontext value=" + ic,
				})
			}
		case methodRootsList:
			out = append(out, postureIssue{
				mcp: "MCP04", severity: model.SeverityMedium,
				title:     "server actively requested deprecated roots/list during introspection (SEP-2577; the client declared no roots capability)",
				detailKey: "observed-deprecated method=" + o.method,
			})
		case notifRootsListChanged:
			out = append(out, postureIssue{
				mcp: "MCP04", severity: model.SeverityLow,
				title:     "server emitted " + notifRootsListChanged + " to a client that declared no roots capability (deprecated Roots reliance, SEP-2577)",
				detailKey: "observed-deprecated method=" + o.method,
			})
		case methodElicitationCreate:
			// Not deprecated — but a server driving elicitation against a client
			// that declared NO capabilities is the runtime confirmation of the
			// MCP10 input-vector the advertised-capability finding only suspects.
			out = append(out, postureIssue{
				mcp: "MCP10", severity: model.SeverityLow,
				title:     "server initiated elicitation/create against a client that declared no elicitation capability (capability-negotiation violation; user-input vector)",
				detailKey: "observed-violation method=" + o.method,
			})
		}
	}
	return out
}

// --- Official deprecated-features feed (drift detector) ----------------------

// defaultDeprecationFeedURL is the raw MDX source of the canonical Deprecated
// Features registry. There is NO machine-readable JSON upstream (verified
// 2026-06-10) — the registry page itself is the parseable artifact.
const defaultDeprecationFeedURL = "https://raw.githubusercontent.com/modelcontextprotocol/modelcontextprotocol/main/docs/specification/draft/deprecated.mdx"

// subjectDeprecationRegistry is the SubjectRef for feed-level findings (a public
// spec identifier, not a server).
const subjectDeprecationRegistry = "modelcontextprotocol.io/specification/deprecated"

// maxFeedBody caps the deprecation-registry document.
const maxFeedBody = 1 << 20

// deprecationFeedFindings fetches the official deprecated-features registry once
// per Gather (opt-in) and reports DRIFT between it and the compiled-in rules:
// a Deprecated row these rules do not cover (Info — new upstream deprecation),
// and a known feature moved to Removed (Low — the compiled horizons went stale).
// The feed is additive-info ONLY: it never alters rule severities, so a poisoned
// feed cannot rescore the fleet. An unreachable/unparseable feed degrades to one
// Info finding — a gap is a signal, never fabricated drift.
func (s *Source) deprecationFeedFindings(ctx context.Context, at time.Time) []model.FindingReport {
	if !s.cfg.deprecationFeed {
		return nil
	}
	deprecated, removed, err := fetchDeprecationRegistry(ctx, s.cfg.deprecationFeedURL, s.cfg.timeout)
	if err != nil {
		return []model.FindingReport{feedUnavailableFinding(err, at)}
	}
	var out []model.FindingReport
	for _, feature := range deprecated {
		if _, known := knownDeprecation(feature); !known {
			out = append(out, unknownDeprecationFinding(feature, at))
		}
	}
	for _, feature := range removed {
		if d, known := knownDeprecation(feature); known {
			out = append(out, removedFeatureFinding(d.label, at))
		}
	}
	return out
}

// fetchDeprecationRegistry fetches and parses the registry MDX into the normalized
// feature names of its Deprecated and Removed sections.
func fetchDeprecationRegistry(ctx context.Context, feedURL string, timeout time.Duration) (deprecated, removed []string, err error) {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return nil, nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("mcp deprecation feed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxFeedBody))
		return nil, nil, fmt.Errorf("mcp deprecation feed: http %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFeedBody))
	if err != nil {
		return nil, nil, fmt.Errorf("mcp deprecation feed: read: %w", err)
	}
	deprecated, removed = parseDeprecationRegistry(string(body))
	if len(deprecated) == 0 {
		// The registry currently lists six Deprecated rows; an empty parse means
		// the page restructured — surface as unavailability, not as "all clear".
		return nil, nil, fmt.Errorf("mcp deprecation feed: no Deprecated rows parsed (page format drift?)")
	}
	return deprecated, removed, nil
}

// mdLinkRe rewrites a markdown link [label](url) to its label.
var mdLinkRe = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)

// parseDeprecationRegistry extracts the normalized Feature-cell names of the
// "## Deprecated" and "## Removed" table sections of the registry MDX.
func parseDeprecationRegistry(doc string) (deprecated, removed []string) {
	section := ""
	for _, line := range strings.Split(doc, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") {
			section = strings.TrimSpace(strings.TrimPrefix(trimmed, "## "))
			continue
		}
		if !strings.HasPrefix(trimmed, "|") {
			continue
		}
		feature := normalizeFeatureCell(firstTableCell(trimmed))
		if feature == "" || feature == "feature" || strings.HasPrefix(feature, "---") {
			continue // header / separator rows
		}
		switch section {
		case "Deprecated":
			deprecated = append(deprecated, feature)
		case "Removed":
			removed = append(removed, feature)
		}
	}
	return deprecated, removed
}

// firstTableCell returns the first cell of a markdown table row.
func firstTableCell(row string) string {
	row = strings.TrimPrefix(row, "|")
	if i := strings.IndexByte(row, '|'); i >= 0 {
		row = row[:i]
	}
	return row
}

// normalizeFeatureCell folds a Feature cell to a comparable lowercase name:
// links become labels, backticks/quotes drop, whitespace and separator dashes
// collapse.
func normalizeFeatureCell(cell string) string {
	cell = mdLinkRe.ReplaceAllString(cell, "$1")
	cell = strings.NewReplacer("`", "", `"`, "", "'", "").Replace(cell)
	cell = strings.ToLower(strings.Join(strings.Fields(cell), " "))
	return strings.TrimSpace(cell)
}

// unknownDeprecationFinding reports a Deprecated row the compiled rules do not
// cover — a NEW upstream deprecation the posture scanner cannot grade yet. The
// feature name is network-sourced text: sanitized before it rides in a title.
func unknownDeprecationFinding(feature string, at time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        findingPosture,
		Severity:    model.SeverityInfo,
		SubjectKind: subjectMCPServer,
		SubjectRef:  subjectDeprecationRegistry,
		Title:       "official deprecated-features registry lists a feature this connector has no posture rule for: " + textscan.SanitizeDisplay(feature),
		DetailHash:  redact.Hash("mcp-deprecation-unknown feature=" + feature),
		OccurredAt:  at,
	}
}

// removedFeatureFinding reports that a feature the compiled rules still grade as
// Deprecated has moved to Removed upstream — the horizons here are stale and the
// affected rule should escalate.
func removedFeatureFinding(label string, at time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        findingPosture,
		Severity:    model.SeverityLow,
		SubjectKind: subjectMCPServer,
		SubjectRef:  subjectDeprecationRegistry,
		Title:       "official deprecated-features registry now lists " + label + " as REMOVED — the compiled deprecation rules are stale and should escalate this feature",
		DetailHash:  redact.Hash("mcp-deprecation-removed feature=" + label),
		OccurredAt:  at,
	}
}

// feedUnavailableFinding reports that the deprecated-features registry could not
// be consulted this pass (the compiled rules keep working; only drift detection
// is degraded).
func feedUnavailableFinding(err error, at time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        findingPosture,
		Severity:    model.SeverityInfo,
		SubjectKind: subjectMCPServer,
		SubjectRef:  subjectDeprecationRegistry,
		Title:       "official deprecated-features registry unavailable this pass (compiled deprecation rules unaffected; drift detection degraded)",
		DetailHash:  redact.Hash("mcp-deprecation-feed-unavailable err=" + err.Error()),
		OccurredAt:  at,
	}
}
