// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// This file models the 2026 Anthropic-hosted SERVER tools on the request side and
// their GOVERNANCE by tool TYPE (D1, D7): the versioned web/code tool builders
// (web_search_20260209 / web_fetch_20260209 with built-in dynamic filtering,
// code_execution_20260120) and the advisor tool (advisor_20260301, D1), plus the
// operator-declared allowlist that governs which server-tool TYPES an API-driven
// Claude agent MAY use. It is the type-level sibling of mcp.go's per-MCP-tool
// allowlist: where mcp.go governs which MCP tools a server exposes to an agent, this
// governs which Anthropic-hosted server tools an agent may invoke at all — turning
// each allow into a PERMITTED access edge (Source=policy) module III crosses against
// the observed/runtime tool use.
//
// The connector keeps its OWN minimal recognition set of the dated identifiers rather
// than importing the AGPL modules/models tool-type catalog (a connector imports only
// /sdk + modelprovider, never /core or /modules — LICENSING.md); this mirrors the
// surfaces.go precedent of a small, surface-descriptive reimplementation. The dated
// ids drift quarterly, so the set is AsOf-stamped and an UNRECOGNIZED allow surfaces a
// posture finding (verify the identifier) rather than a silent pass or a hard reject.
//
// Authority (verbatim, jun-2026): …/tool-use/{tool-reference,web-search,web-fetch,
// code-execution-tool,advisor-tool}; dynamic filtering is built into the _20260209
// web tools (no extra tool/beta needed).
package claudeapi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Dated server-tool TYPE identifiers (the value of a tool's "type"). Current
// generation; prior versions are recognized too (see recognizedServerTools).
const (
	webSearchToolType = "web_search_20260209"
	webFetchToolType  = "web_fetch_20260209"
	codeExecToolType  = "code_execution_20260120"
	advisorToolType   = "advisor_20260301"
)

// serverToolTypesAsOf stamps the recognized dated identifiers (they drift quarterly).
const serverToolTypesAsOf = "2026-06-09"

// resServerTool is the ResourceKind of an Anthropic server tool in a PERMITTED edge.
const resServerTool = "anthropic.server_tool"

// subjectServerTool is the FindingReport subject for server-tool governance posture.
const subjectServerTool = "anthropic.server_tool"

// ServerTool is one entry of the request's tools[] for an Anthropic-hosted server
// tool. Model is set only for the advisor tool (where it is REQUIRED — the model the
// advisor sub-inference runs on). The connector ships builders for the current types;
// a caller may also hand-build a map[string]any, which BetaHeaders still recognizes.
type ServerTool struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Model string `json:"model,omitempty"` // advisor_20260301: required
}

// requiredBeta implements betaTagged: only the advisor tool needs a beta header
// (BetaAdvisorTool). The web/code tools at their current dated versions are GA.
func (t ServerTool) requiredBeta() string {
	if t.Type == advisorToolType {
		return BetaAdvisorTool
	}
	return ""
}

// advisorToolMissingModel reports whether t is an advisor tool with no model set. The
// advisor's model is REQUIRED (D1) — a missing one 400s — so CreateMessage rejects it
// client-side. It handles the typed ServerTool and a hand-built map[string]any (the
// AdvisorTool builder always sets the model; this guards a caller who did not).
func advisorToolMissingModel(t any) bool {
	switch v := t.(type) {
	case ServerTool:
		return v.Type == advisorToolType && strings.TrimSpace(v.Model) == ""
	case *ServerTool:
		return v != nil && v.Type == advisorToolType && strings.TrimSpace(v.Model) == ""
	case map[string]any:
		if ty, _ := v["type"].(string); ty == advisorToolType {
			m, _ := v["model"].(string)
			return strings.TrimSpace(m) == ""
		}
	}
	return false
}

// WebSearchTool builds the current web_search server tool (D7). The _20260209 version
// has built-in dynamic filtering — Claude writes/executes code to filter results
// before they hit the context — with no separate code_execution tool or beta header.
func WebSearchTool() ServerTool { return ServerTool{Type: webSearchToolType, Name: "web_search"} }

// WebFetchTool builds the current web_fetch server tool (D7), also dynamic-filtering.
func WebFetchTool() ServerTool { return ServerTool{Type: webFetchToolType, Name: "web_fetch"} }

// CodeExecutionTool builds the current code_execution server tool (D7).
func CodeExecutionTool() ServerTool {
	return ServerTool{Type: codeExecToolType, Name: "code_execution"}
}

// AdvisorTool builds the advisor server tool (D1): a SECOND server-side inference over
// the transcript on the given model (required). CreateMessage adds BetaAdvisorTool
// when this tool is present; the advisor's separate token spend is accounted distinctly
// by forensic.go (AdvisorCostSamples / the advisor forensic signal) — it is never
// invisible in the top-level usage.
func AdvisorTool(model string) ServerTool {
	return ServerTool{Type: advisorToolType, Name: "advisor", Model: model}
}

// serverToolKind is the recognition record for a dated server-tool type.
type serverToolKind struct {
	Family    string // version-independent family (web_search, code_execution, advisor…)
	Execution string // always "server" for these (Anthropic-hosted)
	Beta      string // beta header required, "" if GA
}

// recognizedServerTools is the connector-local recognition set of dated server-tool
// identifiers (AsOf serverToolTypesAsOf). It lets the governance allowlist flag an
// UNRECOGNIZED allow (a likely-stale/typo identifier) without importing the AGPL
// modules/models catalog. Both current and one prior version are listed so an estate
// mid-migration still resolves; operators verify against the tool reference.
var recognizedServerTools = map[string]serverToolKind{
	webSearchToolType:         {Family: "web_search", Execution: "server"},
	"web_search_20250305":     {Family: "web_search", Execution: "server"},
	webFetchToolType:          {Family: "web_fetch", Execution: "server"},
	"web_fetch_20250910":      {Family: "web_fetch", Execution: "server"},
	codeExecToolType:          {Family: "code_execution", Execution: "server"},
	"code_execution_20250825": {Family: "code_execution", Execution: "server"},
	advisorToolType:           {Family: "advisor", Execution: "server", Beta: BetaAdvisorTool},
}

// RecognizedServerToolType reports whether id is a server-tool type the connector
// recognizes (AsOf serverToolTypesAsOf). An unrecognized id is not necessarily wrong
// (the catalog drifts) — it is surfaced as a posture finding, not silently accepted.
func RecognizedServerToolType(id string) bool {
	_, ok := recognizedServerTools[strings.TrimSpace(id)]
	return ok
}

// knownServerToolFamilies is the version-independent family root set derived from
// recognizedServerTools (web_search, web_fetch, code_execution, advisor). It backs the
// version-bump-robust resolution in ServerToolFamily.
var knownServerToolFamilies = func() map[string]struct{} {
	out := make(map[string]struct{}, len(recognizedServerTools))
	for _, k := range recognizedServerTools {
		out[k.Family] = struct{}{}
	}
	return out
}()

// ServerToolFamily resolves the version-independent FAMILY of a server-tool TYPE id
// (e.g. "web_search_20260318" → "web_search") and reports whether the EXACT dated id is
// in the recognized set (AsOf serverToolTypesAsOf).
//
// It matches a recognized dated id first; failing that, it derives the family from the
// canonical "<family>_<YYYYMMDD>" shape — but ONLY when the prefix is a family root the
// connector already knows. This is deliberate for a GOVERNANCE gate: a newer, not-yet-
// listed version of a KNOWN family (the quarterly id bump) STILL maps to its family, so a
// version bump can never slip an internet-reaching server tool past a family-scoped
// egress grant. A genuinely unknown identifier returns ("", false) and is left to the
// caller (Anthropic will not server-execute a type it does not recognize, so it cannot
// egress). recognizedExact=false means "verify this dated id" — the observe layer raises a
// posture finding; an enforcement gate treats it deny-closed and re-checks the family.
func ServerToolFamily(typeID string) (family string, recognizedExact bool) {
	id := strings.TrimSpace(typeID)
	if id == "" {
		return "", false
	}
	if k, ok := recognizedServerTools[id]; ok {
		return k.Family, true
	}
	if fam, ok := serverToolFamilyRoot(id); ok {
		return fam, false
	}
	return "", false
}

// serverToolFamilyRoot strips a trailing "_<YYYYMMDD>" (exactly 8 digits) and returns the
// prefix IF it is a known family root. A bare family name ("web_search") resolves too;
// an unknown root ("bash_20250124", "random") does not.
func serverToolFamilyRoot(id string) (string, bool) {
	root := id
	if i := strings.LastIndexByte(id, '_'); i > 0 {
		if suffix := id[i+1:]; len(suffix) == 8 && isAllDigits(suffix) {
			root = id[:i]
		}
	}
	if _, ok := knownServerToolFamilies[root]; ok {
		return root, true
	}
	return "", false
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// LooksLikeServerToolType reports whether id has Anthropic's canonical server-tool
// "<name>_<YYYYMMDD>" dated shape, REGARDLESS of whether the family is recognized. It is
// the deny-closed backstop for ServerToolFamily: a governance gate uses it to treat an
// UNKNOWN dated server-tool type — a family Anthropic may have shipped AFTER this connector's
// recognized set was stamped (serverToolTypesAsOf) — as a server-executed tool to deny/flag,
// rather than forwarding it ungoverned. The set has grown before (web_search → web_fetch →
// code_execution → advisor), so a new id with this shape is a realistic future event. A
// non-dated type (a custom/client tool Anthropic will not server-execute) returns false.
func LooksLikeServerToolType(id string) bool {
	id = strings.TrimSpace(id)
	i := strings.LastIndexByte(id, '_')
	if i <= 0 {
		return false
	}
	suffix := id[i+1:]
	return len(suffix) == 8 && isAllDigits(suffix)
}

// ---- D7: operator-declared server-tool-type allowlist (the PERMITTED side) ---------

// ServerToolGrant is the operator-declared governance of which Anthropic server-tool
// TYPES an API-driven agent may use. It mirrors the mcp_toolset grant (mcp.go) but at
// the tool-TYPE granularity: an allowed type becomes a PERMITTED access edge (policy);
// a denied type is modeled for visibility but grants nothing.
type ServerToolGrant struct {
	// AgentRef is the API-driven agent/deployment this grant governs (the edge origin).
	// As with mcp.go it MUST be the agent's EXTERNAL id so module III reconciles the
	// PERMITTED edge against observed tool use — otherwise the grant is an honest no-op.
	AgentRef string `json:"agent_ref"`
	// AllowedTypes are the explicitly-permitted server-tool TYPE ids (dated, e.g.
	// "web_search_20260209"). Each becomes a PERMITTED edge.
	AllowedTypes []string `json:"allowed_types,omitempty"`
	// DeniedTypes are explicitly-denied types (modeled for visibility; emit no edge).
	DeniedTypes []string `json:"denied_types,omitempty"`
}

// Valid reports whether the grant names an agent and at least one (denylist-subtracted)
// allowed type.
func (g ServerToolGrant) Valid() bool {
	return strings.TrimSpace(g.AgentRef) != "" && len(g.allowedSet()) > 0
}

// allowedSet returns the de-duplicated, denylist-subtracted allowed types (deny wins).
func (g ServerToolGrant) allowedSet() []string {
	denied := make(map[string]struct{}, len(g.DeniedTypes))
	for _, d := range g.DeniedTypes {
		if d = strings.TrimSpace(d); d != "" {
			denied[d] = struct{}{}
		}
	}
	seen := make(map[string]struct{}, len(g.AllowedTypes))
	out := make([]string, 0, len(g.AllowedTypes))
	for _, t := range g.AllowedTypes {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if _, isDenied := denied[t]; isDenied {
			continue
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

// PermittedEdges turns the grant into PERMITTED access edges — one per allowed type.
// Source is SignalPolicy (III derives permitted=true), OriginKind "agent", ResourceKind
// resServerTool, ResourceRef the dated type id, ToolRef the family (so a version bump
// of the SAME family still reconciles against an observed family-level tool use), Mode
// unknown (an allow is not itself a read/write). at stamps the observation.
func (g ServerToolGrant) PermittedEdges(at time.Time) []model.EdgeObservation {
	if !g.Valid() {
		return nil
	}
	var out []model.EdgeObservation
	for _, id := range g.allowedSet() {
		family := id
		if k, ok := recognizedServerTools[id]; ok {
			family = k.Family
		}
		out = append(out, model.EdgeObservation{
			OriginKind:   "agent",
			OriginRef:    g.AgentRef,
			ResourceKind: resServerTool,
			ResourceRef:  id,
			Mode:         model.ModeUnknown,
			Source:       model.SignalPolicy,
			Confidence:   model.ConfidenceAttributed,
			ToolRef:      family,
			ObservedAt:   at,
		})
	}
	return out
}

// parseServerToolGrants parses the connector's server_tool_grants config (a JSON array
// of ServerToolGrant). Empty is no governance (nil). A malformed policy is a hard error
// — a typo must not silently leave an API-driven agent's server-tool use ungoverned
// (same posture as parseToolsets).
func parseServerToolGrants(s string) ([]ServerToolGrant, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	var grants []ServerToolGrant
	if err := json.Unmarshal([]byte(s), &grants); err != nil {
		return nil, fmt.Errorf("claudeapi: server_tool_grants is not a valid JSON array of grants: %w", err)
	}
	return grants, nil
}

// gatherServerToolEdges emits the PERMITTED server-tool edges for every declared grant
// (D7) and a posture finding when a grant allows an UNRECOGNIZED type (a likely-stale
// or mistyped dated identifier — surfaced, not silently accepted). It is idempotent
// (III upserts by natural key) and credential-independent (operator policy flows even
// in offline mode), exactly like gatherToolsetEdges.
func (s *Source) gatherServerToolEdges(ctx context.Context, sink sdk.Sink) error {
	if len(s.serverTools) == 0 {
		return nil
	}
	now := s.clock().UTC()
	for _, g := range s.serverTools {
		for _, e := range g.PermittedEdges(now) {
			if err := sink.Emit(ctx, e); err != nil {
				return err
			}
		}
		for _, id := range g.allowedSet() {
			if RecognizedServerToolType(id) {
				continue
			}
			if err := sink.Emit(ctx, model.FindingReport{
				Kind:        "governance",
				Severity:    model.SeverityInfo,
				SubjectKind: subjectServerTool,
				SubjectRef:  g.AgentRef,
				Title:       "Server-tool allowlist references an unrecognized tool type",
				DetailHash:  redact.Hash("agent " + g.AgentRef + " allows unrecognized server-tool type " + id + "; verify the dated identifier against the tool reference (recognized set AsOf " + serverToolTypesAsOf + ")"),
				OccurredAt:  now,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}
