// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package cliruntime

import "strings"

// LaunchArgs is the owned operate argv of one official CLI. It never returns a
// daemon, remote, attach or adoption form.
func LaunchArgs(kind string, req LaunchRequest) ([]string, error) {
	switch kind {
	case KindClaude:
		return ClaudeArgs(req), nil
	case KindCodex:
		return CodexArgs(req), nil
	case KindGrok:
		return GrokArgs(req), nil
	default:
		return nil, ErrUnknownKind
	}
}

// ClaudeArgs is the governed stream-json transport of the official Claude Code
// CLI, and it is the ONLY definition of that form in this repository — the
// engine's own launch path (sessions.Module.buildLaunchSpec) builds its argv
// here.
//
// ⛔ IT IS ONE TABLE BECAUSE TWO WERE MEASURED TO DRIFT APART. Until r3 the
// engine built this same `--print` form by hand beside this function, so the
// transport declared here (LaunchTransport) protected a path production did not
// take: the declaration could be changed for a kind and nothing production
// launches would change. A second table does not need to be wrong to be a
// defect — it needs only to be second.
//
// Remote-control is a lifecycle-only form and is not produced here:
// ClaudeRemoteControlArgs is that form, and it shares this one's governed tail.
func ClaudeArgs(req LaunchRequest) []string {
	// The supported headless control transport: bidirectional NDJSON over stdio.
	args := []string{
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose", "--print",
	}
	return append(args, claudeGovernedTail(req)...)
}

// ClaudeRemoteControlArgs is the LIFECYCLE-ONLY remote-control form of the
// official Claude Code CLI: the child relays its I/O to the vendor's cloud, so
// Olivares owns the process and cannot bridge the stream. It is never produced
// by LaunchArgs, which returns owned operate forms only.
func ClaudeRemoteControlArgs(req LaunchRequest) []string {
	args := []string{"--remote-control"}
	if name := strings.TrimSpace(req.Name); name != "" {
		args = append(args, "--name", name)
	}
	return append(args, claudeGovernedTail(req)...)
}

// claudeGovernedTail is the part of the argv that does not depend on which
// Claude Code form is launched: the governed launch terms the server decided.
//
// The permission mode is ALWAYS emitted, and defaulted here, because it is the
// flag that denies what the allowlist does not name: a form that omitted it
// would inherit the CLI's own default and read as governed while it was not.
//
// The allowlist travels as ONE comma-separated value rather than the flag's
// space-separated variadic form: a variadic `--allowedTools A B` would swallow
// the flags that follow it. Tool specs routinely contain spaces (`Bash(git *)`)
// and never commas, so the join is lossless (the server's merge refuses a spec
// that contains one).
//
// ⛔ `--allowedTools` AUTO-APPROVES, IT DOES NOT CONFINE. It is a restriction
// only in company: what denies everything it does not name is the permission
// mode dontAsk that the template merge pins alongside it. Emitting this flag
// without that mode would read as a lock-down and be a widening.
func claudeGovernedTail(req LaunchRequest) []string {
	mode := strings.TrimSpace(req.PermissionMode)
	if mode == "" {
		mode = "default"
	}
	args := []string{"--permission-mode", mode}
	if model := strings.TrimSpace(req.Model); model != "" {
		args = append(args, "--model", model)
	}
	if effort := strings.TrimSpace(req.Effort); effort != "" {
		args = append(args, "--effort", effort)
	}
	if len(req.AllowedTools) > 0 {
		args = append(args, "--allowedTools", strings.Join(req.AllowedTools, ","))
	}
	// ⛔ AND THIS ONE DOES CONFINE, WHICH IS WHY IT IS A SEPARATE FLAG. `--tools`
	// selects the child's BUILT-IN tool surface from the official set, and the CLI
	// documents `""` as "disable all tools" — it is what the child's own init frame
	// reports back, so it is checkable rather than asserted. The server emits it for
	// every profiled launch: the declared surface, or nothing at all when the
	// profile declared nothing.
	//
	// It travels as ONE comma-separated value for the same reason --allowedTools
	// does: `--tools A B` is variadic and would swallow the flags that follow it.
	// Built-in tool names carry no comma, so the join is lossless, and the server
	// refuses a declared name that contains one.
	if req.ToolSurfaceDeclared {
		args = append(args, "--tools", strings.Join(req.ToolSurface, ","))
	}
	if instructions := req.Instructions; instructions != "" {
		args = append(args, "--append-system-prompt", instructions)
	}
	if id := strings.TrimSpace(req.ResumeID); id != "" {
		args = append(args, "--resume", id)
	}
	return args
}

// LaunchTerms says which choices of a LaunchRequest an owned launch form hands
// the child on its argv. A false field is a choice that form drops.
type LaunchTerms struct {
	Model, Effort, PermissionMode bool
}

// ClaudeLaunchTerms is which choices the owned Claude Code argv carries. The
// governed tail both forms share puts each of the three on the argv as its own
// flag — `--permission-mode` always, `--model` and `--effort` whenever one was
// chosen — so the declaration lives beside the table that makes it true.
func ClaudeLaunchTerms() LaunchTerms {
	return LaunchTerms{Model: true, Effort: true, PermissionMode: true}
}

// CodexArgs is the owned stdio app-server of the official Codex CLI.
func CodexArgs(LaunchRequest) []string {
	return []string{"app-server", "--listen", "stdio://"}
}

// GrokArgs is the owned non-leader stdio agent of the official Grok CLI.
func GrokArgs(req LaunchRequest) []string {
	args := []string{"agent", "--no-leader"}
	if model := strings.TrimSpace(req.Model); model != "" {
		args = append(args, "--model", model)
	}
	if effort := strings.TrimSpace(req.Effort); effort != "" {
		args = append(args, "--reasoning-effort", effort)
	}
	return append(args, "stdio")
}

// LaunchEnv is the explicit, non-secret environment this kind's child needs
// besides HOME and the configuration-home variable (those are applied by the
// runtime from the profile). Grok pins its version; Claude and Codex add none.
func LaunchEnv(kind string) []EnvVar {
	if kind == KindGrok {
		return []EnvVar{{Name: "GROK_DISABLE_AUTOUPDATER", Value: "1"}}
	}
	return nil
}

// HomeEnv builds HOME plus the kind's configuration-home variable. Empty
// paths are omitted. The values are paths, never credentials.
func HomeEnv(kind, userHome, configHome string) []EnvVar {
	var env []EnvVar
	if userHome != "" {
		env = append(env, EnvVar{Name: "HOME", Value: userHome})
	}
	if configHome != "" {
		if name := ConfigHomeEnv(kind); name != "" {
			env = append(env, EnvVar{Name: name, Value: configHome})
		}
	}
	env = append(env, LaunchEnv(kind)...)
	return env
}

// Transport is the child-side shape one owned launch form needs.
type Transport string

const (
	// TransportStdio is a protocol over the child's standard streams. The child
	// must NOT get a terminal on its stdin.
	TransportStdio Transport = "stdio"
	// TransportTerminal is a terminal session on a pseudo-terminal. No official
	// launch form in LaunchArgs asks for one.
	TransportTerminal Transport = "terminal"
)

// LaunchTransport is the transport the owned operate argv of a kind requires.
//
// ⛔ ALL THREE FORMS ARE STDIO PROTOCOLS, AND FOR CLAUDE CODE THAT IS A HARD
// REQUIREMENT, NOT A PREFERENCE. Measured on 2026-09-18 against claude 2.1.275:
// `--print` with a terminal on stdin refuses at once on stderr with
//
//	Error: Input must be provided either through stdin or as a prompt argument when using --print
//
// and exits 1 without one frame of the stream-json protocol. With a pipe on
// stdin the same argv prints its init frame and answers. The Codex app-server
// and the Grok stdio agent speak JSON-RPC over the same streams and gain
// nothing from a terminal.
//
// A terminal is what an INTERACTIVE form would need, and no form here asks for
// one. This function is where that changes when one does.
func LaunchTransport(kind string) Transport {
	switch kind {
	case KindClaude, KindCodex, KindGrok:
		return TransportStdio
	default:
		return ""
	}
}
