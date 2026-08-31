// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"fmt"
	"sort"
	"strings"
)

// toolset.go is the SERVER-OWNED tool→scope policy the inline MCP PEP enforces on
// `tools/call` (AIP-02 §c). It is deny-by-default and is the single source of truth
// for what a tool requires — it NEVER trusts the client's request, and it NEVER trusts
// the server's own tool annotations (readOnlyHint/destructiveHint are UNTRUSTED per the
// MCP spec, already treated so in annotations.go: a tool poisoning its annotation to
// claim readOnly=true cannot lower its gate, because the gate reads THIS map, not the
// annotation). This closes OWASP MCP01/MCP03 (tool poisoning) and MCP02 (scope creep)
// at enforcement time.

// ToolPolicy is the operator-declared policy for one tool. RequiredScope is the OAuth
// scope a caller's token MUST carry to invoke it (empty ⇒ no scope beyond a valid,
// audience-bound token). Destructive marks a tool whose call mutates/▸acts and MUST
// pass a human-in-the-loop approval (the server-owned classification — NOT the
// server's annotation). Deny explicitly blocks a tool regardless of scope (a kill
// switch). A tool ABSENT from the toolset is denied by default.
//
// AllowedRoles is the (E1) PER-ROLE allowlist layered on top of the per-tool
// allowlist: when non-empty, the caller's token MUST carry at least one of these roles
// (a `roles` claim, see tokenvalidate.go) to invoke the tool — scope alone is not enough.
// Empty ⇒ no role restriction (scope governs). HONEST: the Claude MCP connector API has
// NO native role abstraction (VERIFIED 2026-06-09); this is the control plane's PEP-side
// authorization layer, enforced HERE at tools/call and reflected in tools/list filtering.
// AppOnly is the (SEP-1865) marker for a tool with visibility ["app"]:
// callable ONLY from a rendered MCP App View, never by the model. The RS
// enforces the spec's host MUST on the discovery side — an AppOnly tool is
// excluded from the model-facing tools/list — and AUDITS every call as
// UI-originated (the call gates — scope, role, HITL — still apply in full).
type ToolPolicy struct {
	Name          string           `json:"name"`
	RequiredScope string           `json:"required_scope"`
	Destructive   bool             `json:"destructive"`
	Deny          bool             `json:"deny"`
	AllowedRoles  []string         `json:"allowed_roles,omitempty"`
	AppOnly       bool             `json:"app_only,omitempty"`
	Annotations   *ToolAnnotations `json:"annotations,omitempty"`
}

// Toolset is the server-owned allow/deny policy map for tools/call. It is deny-by-
// default: a tool with no entry is refused. Build it with NewToolset (which validates
// every name against SEP-986).
type Toolset struct {
	byName map[string]ToolPolicy
}

// NewToolset builds a validated toolset. Every policy name MUST satisfy the MCP
// SEP-986 tool-name rules (validated here, at config load — a malformed policy name is
// a configuration error, never a silently-ignored entry). A duplicate name is an
// error (ambiguous policy must never resolve nondeterministically).
func NewToolset(policies []ToolPolicy) (*Toolset, error) {
	ts := &Toolset{byName: make(map[string]ToolPolicy, len(policies))}
	for _, p := range policies {
		if err := validateToolName(p.Name); err != nil {
			return nil, fmt.Errorf("mcp: toolset: %w", err)
		}
		if _, dup := ts.byName[p.Name]; dup {
			return nil, fmt.Errorf("mcp: toolset: duplicate policy for tool %q", p.Name)
		}
		ts.byName[p.Name] = p
	}
	return ts, nil
}

// resolve returns the policy for a tool and whether it is ALLOWED to be considered at
// all. ok is false (deny) when: the toolset is nil/empty, the name is absent (deny-by-
// default), the name violates SEP-986 (a malformed requested name can never match a
// validated entry), or the entry is an explicit Deny. A true ok means the tool is in
// policy and not kill-switched; the returned ToolPolicy then drives scope + HITL.
func (t *Toolset) resolve(name string) (ToolPolicy, bool) {
	if t == nil {
		return ToolPolicy{}, false
	}
	if validateToolName(name) != nil {
		return ToolPolicy{}, false
	}
	p, ok := t.byName[name]
	if !ok || p.Deny {
		return ToolPolicy{}, false
	}
	return p, true
}

// allowedNamesForRoles is the ROLE-AWARE discovery filter (E1): the set of non-denied
// tools whose per-role allowlist admits the caller's roles. A tool with no AllowedRoles
// is included (no role gate); a role-restricted tool is included only when roles holds
// one of its AllowedRoles — so a client never even SEES a tool its role can never invoke
// (deny-by-default at discovery, consistent with the call-time gate). Scope is still not
// filtered here (the caller may hold the scope); scope is enforced at call time.
// An APP-ONLY tool (SEP-1865) is excluded too: the spec's host MUST is that the
// MODEL never sees it in tools/list — only the rendered View may call it.
func (t *Toolset) allowedNamesForRoles(roles map[string]struct{}) map[string]struct{} {
	out := map[string]struct{}{}
	if t == nil {
		return out
	}
	for name, p := range t.byName {
		if p.Deny || p.AppOnly {
			continue
		}
		if roleAllowed(p, roles) {
			out[name] = struct{}{}
		}
	}
	return out
}

// roleAllowed reports whether a caller holding the given roles may invoke a tool under
// policy p. A policy with NO AllowedRoles imposes no role restriction (true). Otherwise
// the caller must hold at least ONE of the policy's allowed roles (deny-closed: an empty
// or non-matching role set is refused).
func roleAllowed(p ToolPolicy, roles map[string]struct{}) bool {
	if len(p.AllowedRoles) == 0 {
		return true
	}
	for _, r := range p.AllowedRoles {
		if _, ok := roles[r]; ok {
			return true
		}
	}
	return false
}

// requiredScopes returns the sorted, de-duplicated set of non-empty OAuth scopes the
// toolset's allowed tools actually require — the ground truth the RS advertises in its
// RFC 9728 scopes_supported so that ADVERTISED scopes match ENFORCED scopes (a client
// can discover every scope it may be challenged for). A denied/kill-switched tool is
// excluded (it can never be called, so its scope is not part of the served surface).
func (t *Toolset) requiredScopes() []string {
	if t == nil {
		return nil
	}
	seen := map[string]struct{}{}
	for _, p := range t.byName {
		if p.Deny {
			continue
		}
		if s := strings.TrimSpace(p.RequiredScope); s != "" {
			seen[s] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// validateToolName enforces the MCP SEP-986 tool-name rules: 1–128 characters, each in
// [A-Za-z0-9_.-], case-sensitive. A name outside this set is rejected (both at config
// load and at call resolution) so a hostile or malformed name can neither be stored as
// policy nor smuggled past the gate.
func validateToolName(name string) error {
	if n := len(name); n < 1 || n > 128 {
		return fmt.Errorf("tool name %q length %d outside SEP-986 range 1..128", name, n)
	}
	for _, r := range name {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9',
			r == '_', r == '.', r == '-':
		default:
			return fmt.Errorf("tool name %q has illegal character %q (SEP-986 allows [A-Za-z0-9_.-])", name, string(r))
		}
	}
	return nil
}

// scopesFromString parses a space-delimited OAuth scope string (RFC 6749 §3.3) into a
// set, used for both the token's granted scopes and a challenge's requested scope.
func scopesFromString(s string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, f := range strings.Fields(s) {
		out[f] = struct{}{}
	}
	return out
}
