// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package hookpep

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/olivaresai/olivares/connectors/claude"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

type sessionHookScopeKey struct{}

// Resolve and human-review revalidation supply this immutable launch scope. A
// tenant allow cannot widen the person's preset; a tenant deny still wins.
func restrictSessionHookPreset(ctx context.Context, p auth.Principal, tenant model.TenantID, in claude.HookDecisionInput, disp hookDisposition) (hookDisposition, error) {
	if !claude.HookEnforcementFor(in.Event).ClassicGate || in.Event == "PostToolUse" {
		return disp, nil
	}
	scope, ok := ctx.Value(sessionHookScopeKey{}).(auth.SessionScope)
	if !ok || scope.TenantID != tenant || scope.SessionRef != p.SessionIdentity || scope.RunRef != p.SessionRunRef || scope.Fence != p.SessionFence || scope.WorkspaceID != p.SessionWorkspaceID {
		return hookDisposition{}, auth.ErrUnauthenticated
	}
	preset, err := SessionPresetDecision(scope.Preset, in.Mode)
	if err != nil {
		return hookDisposition{}, err
	}
	if scope.Preset == sessions.PresetCustom {
		original := in.RewriteBase()
		effective := original
		var rewritten claude.HookDecisionResult
		// Use the same merge that emits updatedInput. A tenant rewrite cannot
		// widen the launch surface, including before opening a human review.
		applyRewrite(&rewritten, in, disp)
		if rewritten.UpdatedInput != nil {
			effective = rewritten.UpdatedInput
		}
		originalAllowed, effectiveAllowed := false, false
		for _, rule := range scope.AllowedTools {
			if rule = strings.TrimSpace(rule); rule != "" {
				originalAllowed = originalAllowed || sessionTemplateToolMatches(rule, in.Tool, original, scope.FolderPath)
				effectiveAllowed = effectiveAllowed || sessionTemplateToolMatches(rule, in.Tool, effective, scope.FolderPath)
			}
		}
		if !originalAllowed || !effectiveAllowed {
			return hookDisposition{decision: claude.DecisionDeny, reason: "session template does not permit this tool", class: auth.ClassInvariant}, nil
		}
	}
	// The selected restriction remains enforced in a tenant observe window. Keep
	// the tenant's rewrite when an ask is added; approval rechecks this same scope.
	if preset == claude.DecisionDeny {
		return hookDisposition{decision: claude.DecisionDeny, reason: "read-only session does not permit this action", class: auth.ClassInvariant}, nil
	}
	if preset == claude.DecisionAsk {
		if disp.decision != claude.DecisionDeny {
			disp.decision = claude.DecisionAsk
			disp.reason = "session permission preset requires human approval"
		}
		disp.class = auth.ClassInvariant
	}
	return disp, nil
}

// SessionPresetDecision is the one preset table used by Claude hooks and native
// provider approvals. Tool-specific custom-template matching stays with the
// caller; an unknown preset never grants an action.
func SessionPresetDecision(preset, mode string) (string, error) {
	switch preset {
	case sessions.PresetReadOnly:
		if mode != "read" {
			return claude.DecisionDeny, nil
		}
	case sessions.PresetAsk:
		if mode != "read" {
			return claude.DecisionAsk, nil
		}
	case sessions.PresetEditsOnly:
		if mode != "read" && mode != "write" {
			return claude.DecisionAsk, nil
		}
	case sessions.PresetCustom, sessions.PresetEditsAndCommands, sessions.PresetFull:
	default:
		return "", auth.ErrUnauthenticated
	}
	return claude.DecisionAllow, nil
}

// Never discard a template rule's argument predicate: Bash(git *) does not
// grant all Bash calls. Unsupported or ambiguous forms refuse rather than widen.
// Matching is pure; paths use the engine-bound folder, never an agent's cwd hint.
func sessionTemplateToolMatches(rule, toolName string, raw map[string]any, folder string) bool {
	tool, spec, scoped := strings.Cut(rule, "(")
	if !scoped {
		return toolGlob(rule, toolName)
	}
	if !strings.HasSuffix(spec, ")") || !toolGlob(tool, toolName) {
		return false
	}
	spec = strings.TrimSuffix(spec, ")")
	if spec == "*" {
		return true
	}
	switch toolName {
	case "Bash":
		command, _ := raw["command"].(string)
		command = strings.TrimSpace(command)
		if command == "" {
			return false
		}
		if !strings.Contains(spec, "*") {
			return command == spec
		}
		// A wildcard cannot authorize a hidden shell operation in its tail. The
		// existing scanner detects substitutions, redirections and compound forms;
		// line breaks and background/group operators also require an exact rule.
		_, ambiguous := tokenizeBashCommand(command)
		if ambiguous || strings.ContainsAny(command, "\n\r&(){}") {
			return false
		}
		if strings.HasSuffix(spec, ":*") {
			spec = strings.TrimSuffix(spec, ":*") + " *"
		}
		if strings.Count(spec, "*") == 1 && strings.HasSuffix(spec, " *") && command == strings.TrimSuffix(spec, " *") {
			return true
		}
		return segmentGlobMatch(spec, command)
	case "Read", "Write", "Edit", "MultiEdit", "NotebookRead", "NotebookEdit", "Glob", "Grep", "LS":
		field := "file_path"
		if toolName == "NotebookRead" || toolName == "NotebookEdit" {
			field = "notebook_path"
		} else if toolName == "Glob" || toolName == "Grep" || toolName == "LS" {
			field = "path"
		}
		path, _ := raw[field].(string)
		path, ok := normalizePath(path, folder)
		if !ok || spec == "" || strings.HasPrefix(spec, "~") {
			return false
		}
		if strings.HasPrefix(spec, "//") {
			spec = strings.TrimPrefix(spec, "/")
		} else if !strings.HasPrefix(spec, "/") {
			if !filepath.IsAbs(folder) {
				return false
			}
			spec = filepath.Join(folder, spec)
		} else {
			// Native /path rules are relative to the project; //path is absolute.
			if !filepath.IsAbs(folder) {
				return false
			}
			spec = filepath.Join(folder, strings.TrimPrefix(spec, "/"))
		}
		return pathGlobMatch(spec, path)
	default:
		return false
	}
}
