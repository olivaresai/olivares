// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/olivaresai/olivares/core/model"
)

// provider_profile_policy.go is the SESSION POLICY a provider profile declares:
// which tools the child may have, and under which permission mode it runs.
//
// ⛔ WHY IT EXISTS, MEASURED 2026-09-18. A
// governed session launched under a profile was handed the child's FULL tool
// surface — 34 tools including Bash, Write, Edit and NotebookEdit — under
// permission_mode=default, in a directory nobody had chosen. The requirement is
// one sentence: *"The product governs WHICH profile launches and WHOSE home it
// runs under, and then narrows nothing about what the child may do."*
//
// So the profile now declares it, and an UNDECLARED policy is deny-closed: the
// child is launched with no built-in tools at all. That is a real product
// decision with a real cost — a profile nobody configured can converse and cannot
// code — and it is the only honest default for a plane whose whole subject is
// governance. The operator declares what the agent may use, once, on the profile:
// `olivares agent profile create --tools ...` and `agent profile update --tools`.
//
// ⛔ AND IT IS EXPRESSED ONLY WHERE THE LAUNCH FORM CAN EXPRESS IT. The Claude
// form takes `--tools`, which is what the child's own init frame reports back. The
// Codex app-server and the Grok/OpenCode ACP children negotiate their tool surface
// IN PROTOCOL, and their owned argv has no such flag: a profile that DECLARES a
// tool policy for one of those drivers is refused by name rather than launched
// under a policy nobody applies. An undeclared policy on those drivers launches
// exactly as it did before, and this document says so rather than implying a
// confinement that is not there.

// colPPSessionTools and colPPSessionPermissionMode are the declaration. Both are
// NULLABLE and the difference between NULL and a value is the whole contract:
// NULL means the operator declared nothing (deny-closed), `[]` means they
// declared NO tools on purpose, and a list means exactly that list.
const (
	colPPSessionTools          = "session_tools"
	colPPSessionPermissionMode = "session_permission_mode"
)

// maxSessionTools bounds a declaration so a profile cannot carry an unbounded
// argv into every launch.
const maxSessionTools = 64

// sessionPolicy is one profile's declared session policy, resolved for a launch.
type sessionPolicy struct {
	// Tools is the declared surface. Nil with ToolsDeclared false means the
	// operator declared nothing; an empty non-nil slice with ToolsDeclared true
	// means they declared none.
	Tools         []string
	ToolsDeclared bool
	// PermissionMode is the declared mode, or "" when the profile declares none.
	PermissionMode string
}

// effectiveTools is what the launch form must emit for this policy: the declared
// list, or the EMPTY list when nothing was declared. There is no third answer —
// "undeclared" and "declared none" produce the same argv on purpose, and they are
// kept apart only so the operator can be told which one they are in.
func (p sessionPolicy) effectiveTools() []string {
	if !p.ToolsDeclared || len(p.Tools) == 0 {
		return []string{}
	}
	return append([]string(nil), p.Tools...)
}

// validSessionTool bounds one tool name. It is deliberately a SHAPE and not a
// closed set of today's tool names: the provider owns that vocabulary and adds to
// it, and a plane that pinned the list would refuse tomorrow's tool as invalid.
// What is closed is that a name cannot carry a comma (the flag joins on one), a
// control character, or a shell metacharacter that would make the argv ambiguous.
func validSessionTool(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 128 {
		return false
	}
	for _, r := range name {
		switch {
		case r < 0x20 || r == 0x7f:
			return false
		case r == ',' || r == '"' || r == '\'' || r == '`' || r == '$' || r == ';' || r == '|':
			return false
		}
	}
	return true
}

// normalizeSessionTools validates and canonicalises a declaration: trimmed,
// de-duplicated and sorted, so two operators who declare the same surface store
// the same bytes and a diff of two profiles is a diff of their policies.
func normalizeSessionTools(in []string) ([]string, error) {
	if len(in) > maxSessionTools {
		return nil, badRequest("too many tools declared on this profile")
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, raw := range in {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		if !validSessionTool(name) {
			return nil, badRequest("invalid tool name in the profile's session policy")
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

// encodeSessionTools stores a declaration as a JSON array, which is what keeps
// "declared none" (`[]`) distinguishable from "declared nothing" (NULL) through a
// column whose zero value is the empty string.
func encodeSessionTools(tools []string) (string, error) {
	blob, err := json.Marshal(tools)
	if err != nil {
		return "", badRequest("invalid tools declaration")
	}
	return string(blob), nil
}

// decodeSessionPolicy reads the declaration off a profile row. A stored value
// that cannot be parsed is treated as UNDECLARED, which is the deny-closed
// reading: a policy nobody can read is not a policy that permits something.
func decodeSessionPolicy(rec model.Record) sessionPolicy {
	out := sessionPolicy{PermissionMode: strings.TrimSpace(rec.String(colPPSessionPermissionMode))}
	raw := strings.TrimSpace(rec.String(colPPSessionTools))
	if raw == "" {
		return out
	}
	var tools []string
	if err := json.Unmarshal([]byte(raw), &tools); err != nil {
		return out
	}
	out.Tools, out.ToolsDeclared = tools, true
	return out
}

// driverExpressesToolSurface reports whether the owned launch form of a driver
// can express a tool surface in its argv. Only the Claude form does; the others
// negotiate tools inside their own protocol.
func driverExpressesToolSurface(driver string) bool {
	return driver == "" || driver == providerDriverClaude
}

// validateSessionPolicyInput validates a declaration against the DRIVER whose
// launch form has to carry it, and returns the two stored values.
//
// The driver check is the honest half: a Codex or ACP child negotiates its tools
// in protocol, so a declaration made for one of those profiles would be stored,
// displayed and never applied. Refusing it by name is what keeps a governance
// surface from promising a confinement nobody enforces.
func validateSessionPolicyInput(
	driver string, tools []string, toolsDeclared bool, permissionMode string,
) (storedTools, storedMode string, err error) {
	mode := strings.TrimSpace(permissionMode)
	if mode != "" && !validPermissionModes[mode] {
		return "", "", badRequest("invalid permission_mode in the profile's session policy")
	}
	if !toolsDeclared {
		return "", mode, nil
	}
	if !driverExpressesToolSurface(driver) {
		return "", "", &runErr{422,
			"driver " + driver + " negotiates its tool surface in its own protocol; its owned launch form has no tool flag, so a tool policy declared here would never be applied"}
	}
	normalized, nerr := normalizeSessionTools(tools)
	if nerr != nil {
		return "", "", nerr
	}
	encoded, eerr := encodeSessionTools(normalized)
	if eerr != nil {
		return "", "", eerr
	}
	return encoded, mode, nil
}
