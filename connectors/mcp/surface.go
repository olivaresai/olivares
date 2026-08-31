// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// AIP-04 — MCP feature-surface inventory. The connector already parses the
// server's advertised capability map (InitializeResult.Capabilities) but never
// surfaced it; and it now captures tool outputSchema/icons. This file turns that
// governance-relevant surface into UNTRUSTED catalog-metadata findings so modules
// III/V/IX can inventory what a server can actually do — not just the listing
// surface (tools/resources/prompts) the capability edges already carry.
//
// SPEC ATTRIBUTION (verified against the official changelogs, not assumed):
//   - structured tool output (outputSchema/structuredContent) and elicitation were
//     introduced in revision 2025-06-18 — NOT 2025-11-25.
//   - 2025-11-25 adds: JSON Schema 2020-12 default dialect (SEP-1613), sampling
//     tool-calling (SEP-1577), URL-mode elicitation (SEP-1036), icons (SEP-973),
//     experimental Tasks (SEP-1686).
// So the surfaces inventoried here span BOTH revisions; the findings do not claim
// any single revision introduced all of them.
//
// UNTRUSTED: every capability here is a server SELF-DECLARATION, treated exactly
// like an MCP annotation — surfaced, never trusted (docs/SECURITY-HARDENING.md). A
// finding's title is non-sensitive; the detail is hashed.
//
// NOTE on elicitation/sampling: in the MCP spec these are CLIENT capabilities a
// server REQUESTS at runtime (the connector declares none and is read-only, so a
// compliant server will not drive them against it). They are surfaced HERE when a
// server ADVERTISES them in its capability map (some servers advertise an
// experimental/extended set, and future revisions may move them); the authoritative
// runtime observation of an actual elicitation/sampling request is governed by the
// Runtime PEP (elicitationpep.go + rs.go handleElicitation/handleSampling),
// which mediates the server's prompt, the user's response, and sampling injection
// through the ElicitationMediator seam. When advertised they are tagged OWASP MCP10
// (Context Injection & Over-Sharing) because they are user/model-input vectors
// III/IX must govern.

// advertisedCap describes one governance-relevant capability key the connector
// recognizes in a server's capability map, with the severity and OWASP MCP-Top-10
// id (if any) to stamp on its finding.
type advertisedCap struct {
	key   string
	sev   model.Severity
	mcp   string // OWASP MCP Top 10 id, "" if none
	label string
}

// governedCapabilities is the allow-listed set of capability keys worth surfacing.
// It is closed by design: an unrecognized key is ignored rather than turned into a
// noisy finding (and a server cannot smuggle a free-form capability into a finding
// title). elicitation/sampling carry the MCP10 tag (input vectors); the rest are
// informational inventory.
var governedCapabilities = []advertisedCap{
	{key: "elicitation", sev: model.SeverityLow, mcp: "MCP10", label: "elicitation (server can request input from the user)"},
	{key: "sampling", sev: model.SeverityLow, mcp: "MCP10", label: "sampling (server can request model generations from the client)"},
	{key: "logging", sev: model.SeverityInfo, mcp: "", label: "logging (server emits log messages)"},
	{key: "completions", sev: model.SeverityInfo, mcp: "", label: "completions (argument autocompletion)"},
	// roots is a CLIENT capability — a server advertising it is nonstandard,
	// and the feature is deprecated (SEP-2577). Surfaced here as inventory; the
	// scored deprecation issue lives in deprecation.go.
	{key: "roots", sev: model.SeverityLow, mcp: "", label: "roots (a CLIENT capability advertised by a server — nonstandard; deprecated by SEP-2577)"},
}

// surfaceFindings turns the advertised capability map plus the tool-level
// structured-output/icons surface into UNTRUSTED catalog-metadata findings.
func surfaceFindings(server string, cat catalog, at time.Time) []model.FindingReport {
	var out []model.FindingReport
	caps := cat.server.Capabilities

	for _, c := range governedCapabilities {
		if hasCapability(caps, c.key) {
			out = append(out, surfaceFinding(server, c.label, c.mcp, c.sev, at))
		}
	}

	// experimental is a nested map; surface the named experimental features (e.g.
	// "tasks", SEP-1686) so the inventory shows a server opting into pre-stable
	// surface. Tasks is an input/async-execution vector worth visibility.
	for _, feat := range experimentalFeatures(caps) {
		out = append(out, surfaceFinding(server, "experimental capability "+feat, "", model.SeverityInfo, at))
	}

	// Tool structured output (outputSchema, 2025-06-18). Presence is the signal.
	if n := countToolsWithOutputSchema(cat.tools); n > 0 {
		out = append(out, surfaceFinding(server,
			strconv.Itoa(n)+" tool(s) declare structured output (outputSchema, JSON Schema; default dialect 2020-12)",
			"", model.SeverityInfo, at))
	}
	// Icons (SEP-973, 2025-11-25) on any tool.
	if n := countToolsWithIcons(cat.tools); n > 0 {
		out = append(out, surfaceFinding(server,
			strconv.Itoa(n)+" tool(s) declare icons (SEP-973)",
			"", model.SeverityInfo, at))
	}
	// RC surfaces (extensions framework, SEP-2549 cache hints, non-default
	// tool-schema dialects) — only ever present on a stateless-mode catalog.
	out = append(out, nextRevisionFindings(server, cat, at)...)
	return out
}

// governedExtensions is the allow-listed set of RC extension ids worth a named
// finding (closed by design, like governedCapabilities: an unrecognized id is
// counted, never echoed into a title a server could poison). Tasks is an
// async-execution surface; MCP Apps (io.modelcontextprotocol/ui — note: NOT
// ".../apps") renders server-supplied UI in a sandboxed iframe, a new
// governance surface tagged MCP10 (context-injection vector).
var governedExtensions = []advertisedCap{
	{key: extensionTasks, sev: model.SeverityInfo, mcp: "", label: "the Tasks extension " + extensionTasks + " (async task handles; tasks/get polling, tasks/list removed)"},
	{key: extensionMCPApps, sev: model.SeverityLow, mcp: "MCP10", label: "the MCP Apps extension " + extensionMCPApps + " (server-rendered UI in a sandboxed iframe)"},
}

// nextRevisionFindings surfaces what the RC stateless introspection observed:
// advertised extensions (capabilities.extensions, reverse-DNS ids), the
// SEP-2549 cache-freshness hints, and any tool schema declaring a non-2020-12
// dialect (the RC default; other dialects are legal via $schema but are a
// fleet-hygiene signal worth seeing). Empty on a stable-mode catalog.
func nextRevisionFindings(server string, cat catalog, at time.Time) []model.FindingReport {
	if !cat.nextRevision {
		return nil
	}
	var out []model.FindingReport

	exts := extensionIDs(cat.server.Capabilities)
	known := map[string]struct{}{}
	for _, g := range governedExtensions {
		if _, ok := exts[g.key]; ok {
			out = append(out, surfaceFinding(server, g.label, g.mcp, g.sev, at))
			known[g.key] = struct{}{}
		}
	}
	if n := len(exts) - len(known); n > 0 {
		out = append(out, surfaceFinding(server,
			strconv.Itoa(n)+" unrecognized protocol extension(s) (reverse-DNS ids surfaced in the hashed detail only)",
			"", model.SeverityInfo, at))
	}

	if n := len(cat.cacheHints); n > 0 {
		private := 0
		for _, h := range cat.cacheHints {
			if h.scope == cacheScopePrivate {
				private++
			}
		}
		out = append(out, surfaceFinding(server,
			"cache freshness metadata (SEP-2549 ttlMs/cacheScope) on "+strconv.Itoa(n)+" result(s), "+strconv.Itoa(private)+" private-scoped",
			"", model.SeverityInfo, at))
	}

	if n := countToolsWithNonDefaultDialect(cat.tools); n > 0 {
		out = append(out, surfaceFinding(server,
			strconv.Itoa(n)+" tool schema(s) declare a non-2020-12 JSON Schema dialect via $schema (RC default is 2020-12; implementations MUST support at least 2020-12)",
			"", model.SeverityLow, at))
	}
	return out
}

// extensionIDs returns the keys of the capabilities.extensions map (the RC
// extension framework). Values are per-extension settings and are not read.
func extensionIDs(caps map[string]any) map[string]struct{} {
	out := map[string]struct{}{}
	if caps == nil {
		return out
	}
	raw, ok := caps["extensions"].(map[string]any)
	if !ok {
		return out
	}
	for k := range raw {
		if k = strings.TrimSpace(k); k != "" {
			out[k] = struct{}{}
		}
	}
	return out
}

// countToolsWithNonDefaultDialect counts tools whose input or output schema
// declares (via $schema) a dialect other than JSON Schema 2020-12. An absent
// $schema means the 2020-12 default and does not count.
func countToolsWithNonDefaultDialect(tools []Tool) int {
	n := 0
	for _, t := range tools {
		if schemaDeclaresNonDefaultDialect(t.InputSchema) || schemaDeclaresNonDefaultDialect(t.OutputSchema) {
			n++
		}
	}
	return n
}

// jsonSchema202012 is the canonical 2020-12 dialect URI (the RC default).
const jsonSchema202012 = "https://json-schema.org/draft/2020-12/schema"

// schemaDeclaresNonDefaultDialect reports whether a raw JSON Schema declares a
// $schema dialect other than 2020-12. Unparseable or $schema-less schemas
// report false (the default dialect applies; the schema contents stay UNTRUSTED
// and unvalidated — presence of a declared dialect is the only signal read).
func schemaDeclaresNonDefaultDialect(raw []byte) bool {
	if len(raw) == 0 {
		return false
	}
	var s struct {
		Schema string `json:"$schema"`
	}
	if json.Unmarshal(raw, &s) != nil || s.Schema == "" {
		return false
	}
	return strings.TrimSuffix(s.Schema, "#") != jsonSchema202012
}

// surfaceFinding builds one catalog-metadata finding, prefixing the OWASP MCP
// Top-10 id when applicable (the connector's FindingReport wire type carries no
// tag field, so the id rides in the title — the canonical mapping lives in
// modules/security/owasp_mcp.go, AIP-07).
func surfaceFinding(server, label, mcp string, sev model.Severity, at time.Time) model.FindingReport {
	title := "MCP server advertises " + label + " — UNTRUSTED catalog metadata"
	if mcp != "" {
		title = "[" + mcp + "] " + title
	}
	return model.FindingReport{
		Kind:        findingSurface,
		Severity:    sev,
		SubjectKind: subjectMCPServer,
		SubjectRef:  server,
		Title:       title,
		DetailHash:  redact.Hash("mcp-surface server=" + server + " label=" + label + " mcp=" + mcp),
		OccurredAt:  at,
	}
}

// experimentalFeatures returns the sorted key names under the server's
// "experimental" capability (deterministic order for stable findings/tests).
func experimentalFeatures(caps map[string]any) []string {
	if caps == nil {
		return nil
	}
	raw, ok := caps["experimental"].(map[string]any)
	if !ok {
		return nil
	}
	feats := make([]string, 0, len(raw))
	for k := range raw {
		if k = strings.TrimSpace(k); k != "" {
			feats = append(feats, k)
		}
	}
	sort.Strings(feats)
	return feats
}

// countToolsWithOutputSchema counts tools that declare a non-empty outputSchema.
func countToolsWithOutputSchema(tools []Tool) int {
	n := 0
	for _, t := range tools {
		if len(t.OutputSchema) > 0 {
			n++
		}
	}
	return n
}

// countToolsWithIcons counts tools that declare non-empty icons.
func countToolsWithIcons(tools []Tool) int {
	n := 0
	for _, t := range tools {
		if len(t.Icons) > 0 {
			n++
		}
	}
	return n
}
