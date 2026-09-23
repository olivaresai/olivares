// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package policytext renders Cedar permit text from resolved policy inputs.
// Callers own validation, authorization, permission computation and ordering.
package policytext

import "strings"

// Rule describes a permit with an already rendered subject and optional condition.
// Actions are emitted in the supplied order.
type Rule struct {
	Subject string
	Actions []string
	When    string
}

// Render emits each permit in the supplied order, with one trailing newline per rule.
// It does not validate, sort, merge or omit rules.
func Render(rules []Rule) string {
	var b strings.Builder
	for _, rule := range rules {
		b.WriteString("permit(principal in ")
		b.WriteString(rule.Subject)
		b.WriteString(", action in [")
		for i, p := range rule.Actions {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString("Action::")
			b.WriteString(Str(p))
		}
		b.WriteString("], resource)")
		if rule.When != "" {
			b.WriteString(" ")
			b.WriteString(rule.When)
		}
		b.WriteString(";\n")
	}
	return b.String()
}

// Str quotes a Cedar string literal, escaping backslashes and double quotes.
func Str(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}

// SubjectUser renders a user reference as a Cedar subject operand.
func SubjectUser(ref string) string { return "User::" + Str(ref) }

// SubjectRole renders a role reference as a Cedar subject operand.
func SubjectRole(ref string) string { return "Role::" + Str(ref) }

// SubjectGroup renders a group reference as a Cedar subject operand.
func SubjectGroup(ref string) string { return "Group::" + Str(ref) }

// ScopeWhen renders workspace, agent_group and folder resource conditions.
// Other tree values produce no condition. Callers must validate scope inputs.
func ScopeWhen(tree, ref string) string {
	switch tree {
	case "workspace":
		return "when { resource in Workspace::" + Str(ref) + " }"
	case "agent_group":
		return "when { resource in AgentGroup::" + Str(ref) + " }"
	case "folder":
		return "when { resource in Resource::" + Str(ref) + " }"
	default:
		return ""
	}
}
