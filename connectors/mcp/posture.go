// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/connectors/internal/textscan"
	"github.com/olivaresai/olivares/sdk/model"
)

// posture.go is the MCP server POSTURE SCANNER (AIP-10): the DETECTIVE,
// introspection-time counterpart to the inline PEP (the PREVENTIVE, enforce-
// side). Where the PEP blocks a poisoned tools/call at runtime from a SERVER-OWNED
// toolset, the scanner evaluates the server's UNTRUSTED catalog metadata as observed
// — tool/prompt/resource names + descriptions, the server `instructions`, tool
// annotations, and the operator-requested OAuth scopes — and grades the server's
// security POSTURE against the static tool-poisoning vectors of the OWASP MCP Top 10:
//
//	MCP01 Token Mismanagement & Secret Exposure  — a secret shape embedded in metadata
//	MCP02 Scope Creep                            — an over-broad requested OAuth scope
//	MCP03 Tool Poisoning                         — invisible/homoglyph names, poisoned readOnly hint
//	MCP05 Command Injection & Execution          — an arbitrary-code/exec tool surface
//	MCP06 Intent Flow Subversion                 — instruction-injection in a description
//
// It emits one MINIMAL-DATA finding per detected issue (a non-sensitive [MCPxx]
// title + a hashed detail; the raw offending text is NEVER persisted, docs/SECURITY-HARDENING.md),
// plus ONE per-server posture-SCORE summary finding (grade A–F over 0–100). It never
// trusts a server's self-declared annotation — a poisoned readOnlyHint is itself a
// finding, not a fact. The text heuristics live in connectors/internal/
// textscan (promoted from this package in for the agent-artifact scanners).

// mutatingRe matches a tool name/description that strongly implies a STATE-CHANGING
// operation — used to catch a tool that poisons its readOnlyHint to claim it is
// non-mutating (MCP03). Word-boundaried verbs only, to keep false positives low.
var mutatingRe = regexp.MustCompile(
	`(?i)\b(?:delete|remove|drop|destroy|truncate|wipe|erase|overwrite|write|update|modify|edit|create|insert|append|send|post|put|patch|deploy|terminate|kill|revoke|grant|transfer|pay|purchase|rename|move|upload|publish|merge|push|reset)\b`)

// broadScopeRe matches an OAuth scope string that is over-broad — a wildcard, an
// admin/superuser grant, or a "<resource>:*"/"*:<action>" pattern (MCP02 scope creep).
var broadScopeRe = regexp.MustCompile(
	`(?i)^(?:\*|all|any|admin|root|superuser|owner|full[_-]?access|read[_-]?write[_-]?all|everything|[^:\s]+:\*|\*:[^:\s]+|\*/\*)$`)

// severityPenalty is the score deduction for an issue of each severity (out of 100).
// A clean server keeps 100 (grade A); the weights are chosen so a single High-class
// issue (a hidden instruction, a homoglyph name) drops the server below grade B.
var severityPenalty = map[model.Severity]int{
	model.SeverityCritical: 40,
	model.SeverityHigh:     25,
	model.SeverityMedium:   12,
	model.SeverityLow:      5,
	model.SeverityInfo:     0,
}

// Marker severities live with the markers themselves (textscan.MarkerSeverity,
// promoted in so the same marker never grades differently across the MCP,
// Skills and AGENTS.md scanners). The values are unchanged.

// postureIssue is one detected problem, accumulated before being turned into a
// finding. detailKey is a stable, non-sensitive string hashed into DetailHash so a
// re-scan dedups without ever persisting the raw offending text.
type postureIssue struct {
	mcp       string
	severity  model.Severity
	title     string
	detailKey string
}

// postureFindings scans one introspected server's catalog + spec and returns the
// per-issue findings followed by the per-server posture-score summary. It is a pure
// function of (spec, catalog, extra): the registry PROVENANCE signals stay separate
// findings (registry.go, revision.go — not double-counted here), while two scored
// vector families joined here: deprecation reliance (deprecation.go — derived
// from the same spec/catalog, including the revision-implied HTTP+SSE-era inference,
// which the unscored mcp_revision finding only inventories) and the caller-supplied
// extra issues (federation supply-chain verification verdicts, federation.go —
// computed by Gather because they need the network the pure scan must not touch).
func postureFindings(spec serverSpec, cat catalog, extra []postureIssue, at time.Time) []model.FindingReport {
	server := spec.Name
	var issues []postureIssue
	// deprecation-aware rules (Roots/Sampling/Logging, HTTP+SSE, DCR-only,
	// includeContext) — reliance on a feature with a running removal clock is a
	// scored posture defect.
	issues = append(issues, deprecationIssues(spec, cat)...)
	issues = append(issues, extra...)

	// Server identity (name/title) — identifier spoofing via invisible/homoglyph runes.
	issues = append(issues, scanIdentity("server name", cat.server.ServerInfo.Name)...)
	issues = append(issues, scanIdentity("server title", cat.server.ServerInfo.Title)...)
	// Server instructions — a free-text field the agent is told to follow: a prime
	// injection + secret-exposure surface.
	issues = append(issues, scanText("server instructions", cat.server.Instructions)...)

	// Tools: the highest-value poisoning target (name spoof, poisoned hint, injected
	// description, exec surface).
	for _, t := range cat.tools {
		elem := "tool " + textscan.SanitizeDisplay(firstNonEmpty(t.Name, t.Title))
		issues = append(issues, scanIdentity(elem+" name", t.Name)...)
		issues = append(issues, scanIdentity(elem+" title", t.Title)...)
		issues = append(issues, scanText(elem+" description", t.Description)...)
		if textscan.LooksExecutional(t.Name, t.Description) {
			issues = append(issues, postureIssue{
				mcp: "MCP05", severity: model.SeverityMedium,
				title:     elem + " exposes an arbitrary-code/command-execution surface",
				detailKey: "exec tool=" + textscan.SanitizeDisplay(t.Name),
			})
		}
		if poisonedReadOnly(t) {
			issues = append(issues, postureIssue{
				mcp: "MCP03", severity: model.SeverityMedium,
				title:     elem + " declares readOnlyHint=true but its name/description implies mutation (poisoned annotation)",
				detailKey: "poisoned-readonly tool=" + textscan.SanitizeDisplay(t.Name),
			})
		}
	}

	// Prompts: agent-facing templates — a direct injection vector.
	for _, p := range cat.prompts {
		elem := "prompt " + textscan.SanitizeDisplay(firstNonEmpty(p.Name, p.Title))
		issues = append(issues, scanIdentity(elem+" name", p.Name)...)
		issues = append(issues, scanText(elem+" description", p.Description)...)
	}

	// Resources + templates: lighter, but a name/description still hides instructions.
	for _, r := range cat.resources {
		elem := "resource " + textscan.SanitizeDisplay(firstNonEmpty(r.Name, r.Title))
		issues = append(issues, scanText(elem+" description", r.Description)...)
	}
	for _, tpl := range cat.templates {
		elem := "resource template " + textscan.SanitizeDisplay(firstNonEmpty(tpl.Name, tpl.Title))
		issues = append(issues, scanText(elem+" description", tpl.Description)...)
	}

	// Over-broad OAuth scopes the operator configured the connector to request for
	// this server (MCP02 scope creep) — visible on the observe path via the spec.
	if spec.Auth != nil {
		for _, s := range spec.Auth.Scopes {
			if broadScopeRe.MatchString(strings.TrimSpace(s)) {
				issues = append(issues, postureIssue{
					mcp: "MCP02", severity: model.SeverityMedium,
					title:     "MCP server is configured to request an over-broad OAuth scope",
					detailKey: "broad-scope server=" + server + " scope=" + s,
				})
			}
		}
	}

	out := make([]model.FindingReport, 0, len(issues)+1)
	score := 100
	worst := model.SeverityInfo
	for _, is := range issues {
		out = append(out, postureIssueFinding(server, is, at))
		score -= severityPenalty[is.severity]
		if is.severity.AtLeast(worst) {
			worst = is.severity
		}
	}
	if score < 0 {
		score = 0
	}
	out = append(out, postureScoreFinding(server, score, len(issues), worst, at))
	return out
}

// scanIdentity scans an IDENTIFIER (a name/title) for invisible/control/bidi runes
// and homoglyph/mixed-script spoofing — both MCP03 tool-poisoning vectors.
func scanIdentity(field, name string) []postureIssue {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	var out []postureIssue
	if classes, n := textscan.ScanInvisible(name); n > 0 {
		out = append(out, postureIssue{
			mcp: "MCP03", severity: model.SeverityHigh,
			title:     field + " contains " + strconv.Itoa(n) + " hidden character(s) [" + strings.Join(classes, ",") + "] — spoofing/poisoning",
			detailKey: "invisible-id field=" + field + " classes=" + strings.Join(classes, ",") + " ref=" + redact.Hash(name),
		})
	}
	if scripts, confusable := textscan.MixedScript(name); confusable {
		out = append(out, postureIssue{
			mcp: "MCP03", severity: model.SeverityHigh,
			title:     field + " mixes scripts [" + strings.Join(scripts, ",") + "] — homoglyph impersonation candidate",
			detailKey: "homoglyph field=" + field + " scripts=" + strings.Join(scripts, ",") + " ref=" + redact.Hash(name),
		})
	}
	return out
}

// scanText scans a FREE-TEXT field (a description / instructions) for instruction-
// injection markers (MCP06), hidden characters (MCP06/MCP03) and an embedded secret
// shape (MCP01). Free text is not subjected to mixed-script checks (legitimate
// multilingual text would false-positive).
func scanText(field, text string) []postureIssue {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	var out []postureIssue
	for _, id := range textscan.ScanInjection(text) {
		sev := textscan.MarkerSeverity(id)
		out = append(out, postureIssue{
			mcp: "MCP06", severity: sev,
			title:     field + " contains an instruction-injection marker [" + id + "] — tool poisoning",
			detailKey: "injection field=" + field + " rule=" + id,
		})
	}
	if classes, n := textscan.ScanInvisible(text); n > 0 {
		out = append(out, postureIssue{
			mcp: "MCP06", severity: model.SeverityHigh,
			title:     field + " hides " + strconv.Itoa(n) + " invisible character(s) [" + strings.Join(classes, ",") + "] — concealed instruction",
			detailKey: "invisible-text field=" + field + " classes=" + strings.Join(classes, ","),
		})
	}
	if redact.ContainsSecret(text) {
		out = append(out, postureIssue{
			mcp: "MCP01", severity: model.SeverityHigh,
			title:     field + " embeds a credential/secret shape — secret exposure",
			detailKey: "secret-in-metadata field=" + field,
		})
	}
	return out
}

// poisonedReadOnly reports whether a tool DECLARES readOnlyHint=true yet its
// name/description strongly implies a mutating operation — a poisoned annotation the
// gate must never trust (MCP03).
func poisonedReadOnly(t Tool) bool {
	if t.Annotations == nil || t.Annotations.ReadOnlyHint == nil || !*t.Annotations.ReadOnlyHint {
		return false
	}
	return mutatingRe.MatchString(t.Name) || mutatingRe.MatchString(t.Description)
}

// postureIssueFinding builds one minimal-data posture finding for a detected issue.
func postureIssueFinding(server string, is postureIssue, at time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        findingPosture,
		Severity:    is.severity,
		SubjectKind: subjectMCPServer,
		SubjectRef:  server,
		Title:       "[" + is.mcp + "] " + is.title,
		DetailHash:  redact.Hash("mcp-posture server=" + server + " mcp=" + is.mcp + " " + is.detailKey),
		OccurredAt:  at,
	}
}

// postureScoreFinding builds the per-server posture-score summary. A clean server
// (no issues) scores 100 / grade A at Info; otherwise the severity is the worst issue
// found, so the summary itself is actionable at a glance.
func postureScoreFinding(server string, score, issues int, worst model.Severity, at time.Time) model.FindingReport {
	grade := postureGrade(score)
	sev := model.SeverityInfo
	if issues > 0 {
		sev = worst
	}
	return model.FindingReport{
		Kind:        findingPosture,
		Severity:    sev,
		SubjectKind: subjectMCPServer,
		SubjectRef:  server,
		Title: "MCP server posture: grade " + grade + " (" + strconv.Itoa(score) + "/100), " +
			strconv.Itoa(issues) + " issue(s) — UNTRUSTED catalog metadata",
		DetailHash: redact.Hash("mcp-posture-score server=" + server + " score=" + strconv.Itoa(score) + " grade=" + grade),
		OccurredAt: at,
	}
}

// postureGrade maps a 0–100 posture score to a letter grade.
func postureGrade(score int) string {
	switch {
	case score >= 90:
		return "A"
	case score >= 75:
		return "B"
	case score >= 60:
		return "C"
	case score >= 40:
		return "D"
	default:
		return "F"
	}
}

// firstNonEmpty returns the first non-empty trimmed string (a tool's name, else its
// title) for a human-readable element reference.
func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
