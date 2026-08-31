// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package managedsettings

import (
	"encoding/json"
	"fmt"
	"strings"
)

// mcprules.go carries the wire decode/validate/compare helpers for the 2.1.17x
// dual-shape keys (VERIFIED 2026-06-10 against docs.claude.com/en/docs/
// claude-code/{settings,managed-mcp} and the raw changelog):
//
//   - allowedMcpServers / deniedMcpServers entries are PREDICATE objects
//     ({serverName} exact-match | {serverUrl} with '*' wildcards anywhere incl.
//     the scheme); allowedMcpServers is three-state (absent | [] lockdown | list)
//     and the denylist always takes precedence;
//   - fallbackModel accepts a STRING or an ARRAY of model strings (chain capped
//     at three by the client; extras ignored);
//   - forceLoginOrgUUID accepts a STRING (pre-selects the org) or an ARRAY of
//     UUIDs (any accepted, no pre-selection; empty array fails closed).
//
// All comparisons are over the AUTHORED PATTERNS — a glob is never expanded here
// (the connector verifies the host carries the org's rule, not its closure).

// mcpRuleToRaw renders one predicate entry in its wire form ({"serverName": ...}
// or {"serverUrl": ...}); exactly-one-selector is the authoring invariant
// (ValidateJSON enforces it — render emits whichever selector is set, URL winning
// only when Name is empty so a malformed double entry is still observable).
func mcpRuleToRaw(r MCPServerRule) json.RawMessage {
	m := map[string]string{}
	if n := strings.TrimSpace(r.Name); n != "" {
		m["serverName"] = n
	} else if u := strings.TrimSpace(r.URL); u != "" {
		m["serverUrl"] = u
	}
	b, _ := json.Marshal(m)
	return b
}

// mcpRulesToRaw marshals an allowlist to its wire array, normalizing nil to `[]`
// (the lockdown posture renders as an empty array, never JSON null).
func mcpRulesToRaw(rules []MCPServerRule) json.RawMessage {
	parts := make([]string, 0, len(rules))
	for _, r := range rules {
		parts = append(parts, string(mcpRuleToRaw(r)))
	}
	return json.RawMessage("[" + strings.Join(parts, ",") + "]")
}

// wireMCPRule is the live predicate shape. serverCommand is captured so a
// command-predicate entry (a documented historical form) is OBSERVABLE — but it is
// not representable in the canonical MCPServerRule and is dropped from the parsed
// intent (drift then honestly reports the authored rule as missing on host).
type wireMCPRule struct {
	ServerName    string `json:"serverName"`
	ServerURL     string `json:"serverUrl"`
	ServerCommand string `json:"serverCommand"`
}

// liveMCPRules parses a wire allowedMcpServers value. present is true ONLY when
// raw is a JSON array (the conformant shape); any other present shape (a hostile/
// legacy bool, an object) yields present=false so drift treats it as "no valid
// allowlist on host" — which the client itself enforces as an EMPTY allowlist
// (fail-closed, changelog 2.1.154/2.1.169). An empty `[]` parses to a non-nil
// empty slice with present=true (the lockdown is a PRESENT allowlist).
func liveMCPRules(raw json.RawMessage) (rules []MCPServerRule, present bool) {
	if !rawPresent(raw) {
		return nil, false
	}
	var arr []wireMCPRule
	if json.Unmarshal(raw, &arr) != nil {
		return nil, false
	}
	rules = make([]MCPServerRule, 0, len(arr))
	for _, w := range arr {
		r := MCPServerRule{Name: strings.TrimSpace(w.ServerName), URL: strings.TrimSpace(w.ServerURL)}
		if r.Name == "" && r.URL == "" {
			continue // serverCommand/unknown predicate: observable but not canonical
		}
		rules = append(rules, r)
	}
	return rules, true
}

// liveMCPRuleList parses the deniedMcpServers list form entry-by-entry.
func liveMCPRuleList(raws []json.RawMessage) []MCPServerRule {
	var rules []MCPServerRule
	for _, raw := range raws {
		var w wireMCPRule
		if json.Unmarshal(raw, &w) != nil {
			continue
		}
		r := MCPServerRule{Name: strings.TrimSpace(w.ServerName), URL: strings.TrimSpace(w.ServerURL)}
		if r.Name != "" || r.URL != "" {
			rules = append(rules, r)
		}
	}
	return rules
}

// mcpRuleKey is one predicate's stable identity for order-independent set
// comparison in drift (selector kind + authored pattern).
func mcpRuleKey(r MCPServerRule) string {
	return strings.TrimSpace(r.Name) + "\x00" + strings.TrimSpace(r.URL)
}

// sameMCPRuleSet reports whether two rule lists denote the same predicate set
// (order-independent, duplicate-insensitive).
func sameMCPRuleSet(a, b []MCPServerRule) bool {
	sa, sb := mcpRuleKeySet(a), mcpRuleKeySet(b)
	if len(sa) != len(sb) {
		return false
	}
	for k := range sa {
		if _, ok := sb[k]; !ok {
			return false
		}
	}
	return true
}

func mcpRuleKeySet(rules []MCPServerRule) map[string]struct{} {
	out := make(map[string]struct{}, len(rules))
	for _, r := range rules {
		out[mcpRuleKey(r)] = struct{}{}
	}
	return out
}

// missingMCPRules returns the authored rules ABSENT from the live list, in the
// authored order (the denylist drift signal — mirrors blockedMarketplaces).
func missingMCPRules(authored, live []MCPServerRule) []MCPServerRule {
	liveSet := mcpRuleKeySet(live)
	var missing []MCPServerRule
	for _, r := range authored {
		if _, ok := liveSet[mcpRuleKey(r)]; !ok {
			missing = append(missing, r)
		}
	}
	return missing
}

// describeMCPRule renders a short, non-sensitive label for a predicate (drift
// titles). Patterns are operator-authored allowlist values, not secrets.
func describeMCPRule(r MCPServerRule) string {
	if n := strings.TrimSpace(r.Name); n != "" {
		return "serverName:" + n
	}
	return "serverUrl:" + strings.TrimSpace(r.URL)
}

// validateMCPRule validates ONE predicate entry SERVER-SIDE: it must carry
// exactly one of serverName/serverUrl, and a serverName with a '*' is rejected
// (names are exact-match only — a glob name matches NOTHING, a silent governance
// hole). ctx is the JSON path for the message.
func validateMCPRule(w wireMCPRule, ctx string) []string {
	name, url := strings.TrimSpace(w.ServerName), strings.TrimSpace(w.ServerURL)
	switch {
	case name == "" && url == "" && strings.TrimSpace(w.ServerCommand) == "":
		return []string{ctx + " must carry serverName or serverUrl"}
	case name != "" && url != "":
		return []string{ctx + " must carry exactly ONE of serverName/serverUrl, not both"}
	case strings.Contains(name, "*"):
		return []string{ctx + `.serverName contains '*' — names match EXACTLY (wildcards are not expanded), so a glob name matches nothing; use serverUrl for wildcard patterns`}
	}
	return nil
}

// validateMCPAllowRaw validates the raw allowedMcpServers wire value: it must be
// an ARRAY (any other present shape is enforced by the client as an EMPTY
// allowlist — fail-closed — so it must never publish silently).
func validateMCPAllowRaw(raw json.RawMessage, key string) []string {
	if !rawPresent(raw) {
		return nil
	}
	var arr []wireMCPRule
	if err := json.Unmarshal(raw, &arr); err != nil {
		return []string{key + " must be an ARRAY of {serverName|serverUrl} predicates (undefined = no restriction, [] = lockdown); the client enforces an invalid value as an EMPTY allowlist (fail-closed)"}
	}
	var issues []string
	for i, w := range arr {
		issues = append(issues, validateMCPRule(w, fmt.Sprintf("%s[%d]", key, i))...)
	}
	return issues
}

// validateMCPDenyList validates the deniedMcpServers list form (per-entry; the
// client drops a wholly-invalid denylist with a warning — fail-open — so a
// malformed entry must be flagged before it publishes).
func validateMCPDenyList(raws []json.RawMessage, key string) []string {
	var issues []string
	for i, raw := range raws {
		ctx := fmt.Sprintf("%s[%d]", key, i)
		var w wireMCPRule
		if err := json.Unmarshal(raw, &w); err != nil {
			issues = append(issues, ctx+" is not a {serverName|serverUrl} predicate object")
			continue
		}
		issues = append(issues, validateMCPRule(w, ctx)...)
	}
	return issues
}

// fallbackModelsFromRaw decodes the wire fallbackModel value (string | array).
// present reports whether the key carries a usable value; a malformed shape is
// (nil, false) — ValidateJSON flags it separately.
func fallbackModelsFromRaw(raw json.RawMessage) (models []string, present bool) {
	if !rawPresent(raw) {
		return nil, false
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if s = strings.TrimSpace(s); s == "" {
			return nil, false
		}
		return []string{s}, true
	}
	var arr []string
	if json.Unmarshal(raw, &arr) != nil {
		return nil, false
	}
	return arr, true
}

// fallbackModelsToRaw renders the canonical wire form: always an ARRAY (the
// documented example shape), never the string shorthand.
func fallbackModelsToRaw(models []string) json.RawMessage {
	b, _ := json.Marshal(models)
	return b
}

// forceLoginOrgFromRaw decodes the wire forceLoginOrgUUID value. single is the
// string form (pre-selects the org); list is the array form; emptyList reports a
// PRESENT empty array (the fail-closed block-all-login misconfiguration, kept
// distinct from absent so validation and drift can name it).
func forceLoginOrgFromRaw(raw json.RawMessage) (single string, list []string, emptyList bool) {
	if !rawPresent(raw) {
		return "", nil, false
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s), nil, false
	}
	var arr []string
	if json.Unmarshal(raw, &arr) == nil {
		if len(arr) == 0 {
			return "", nil, true
		}
		return "", arr, false
	}
	return "", nil, false
}

// sameStringSet reports whether two string slices denote the same set
// (order-independent, duplicate-insensitive).
func sameStringSet(a, b []string) bool {
	sa, sb := stringSet(a), stringSet(b)
	if len(sa) != len(sb) {
		return false
	}
	for k := range sa {
		if _, ok := sb[k]; !ok {
			return false
		}
	}
	return true
}

func stringSet(ss []string) map[string]struct{} {
	out := make(map[string]struct{}, len(ss))
	for _, s := range ss {
		out[strings.TrimSpace(s)] = struct{}{}
	}
	return out
}

// sameStringChain reports whether two slices are identical INCLUDING order — the
// fallbackModel comparison (position carries meaning in the chain).
func sameStringChain(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if strings.TrimSpace(a[i]) != strings.TrimSpace(b[i]) {
			return false
		}
	}
	return true
}
