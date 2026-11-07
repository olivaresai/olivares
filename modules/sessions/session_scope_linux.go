// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package sessions

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/olivaresai/olivares/modules/sessions/confine"
)

// scopeProbeTimeout bounds the one-shot scope that checks the ceilings are accepted.
const scopeProbeTimeout = 10 * time.Second

// steeringEnv are the variables that change how the first stages of the scope chain
// (env, systemd-run) run or whom they talk to. They run with the session's
// environment before the session's confinement applies, so those variables are
// given to the program by the last env stage and kept from the first ones.
var steeringEnv = []string{"LD_", "DBUS_SESSION_BUS_ADDRESS=", "SYSTEMD_"}

// sessionRuntimeDir is the runtime directory systemd-run finds the engine user's user
// manager through: the one XDG_RUNTIME_DIR names, else the user's own. "" when the
// engine's user has no user manager (no bus there).
func sessionRuntimeDir() string {
	if dirs := confine.UserBusDirs(); len(dirs) > 0 {
		return dirs[0]
	}
	return ""
}

// applySessionScope makes cmd start inside a transient systemd scope that carries the
// ceilings: when a session takes more memory than MemoryMax (swap included) the kernel
// kills it alone, and past TasksMax it cannot fork, while the engine, the console and
// every other session keep running. systemd-run execs the program in place, so the
// process the engine started is still the session and Stop still reaches it.
//
// hidden says the session's mount namespace takes the user manager's bus away from it
// (every confined command is built to take it away, whatever limits it has).
// The scope needs that bus and the session must not have it, so a session that cannot
// be kept away from it gets no scope; that does not close the bus to it, which only
// the hiding does.
//
// It returns "" when the scope is in place or no ceiling was asked for, and when the
// host has no systemd (a container: the operator's container limits apply there). On
// a host that has systemd but cannot make the scope for the engine's user, or cannot
// keep the session away from its user manager, the command is left as it was and the
// result is the reason for the run's confinement reason: an honest limit, not a
// refusal. A user manager is what lets the engine's user make a scope:
// `loginctl enable-linger <user>` for a service account.
//
// systemd-run finds that manager's bus through XDG_RUNTIME_DIR, which a session's
// environment does not carry. env gives systemd-run its own value, and gives the
// program back the one it had (or none), so the program's environment is unchanged.
func applySessionScope(cmd *exec.Cmd, memoryMax, tasksMax string, hidden bool) string {
	if memoryMax == "" && tasksMax == "" {
		return ""
	}
	systemdRun, err := exec.LookPath("systemd-run")
	if err != nil {
		return ""
	}
	envBin, err := exec.LookPath("env")
	if err != nil {
		return ""
	}
	const lost = "the session runs without its memory and task ceilings: "
	runtimeDir := sessionRuntimeDir()
	switch {
	case runtimeDir == "":
		return lost + "the engine's user has no user manager (no runtime directory)"
	case !hidden:
		return lost + "the session cannot be kept away from its user manager (it needs Landlock and a mount namespace of its own)"
	case !filepath.IsAbs(cmd.Path) || strings.Contains(cmd.Path, "="):
		return lost + "its program path cannot be given to env"
	}
	scope := []string{envBin, "XDG_RUNTIME_DIR=" + runtimeDir, systemdRun}
	if os.Geteuid() != 0 {
		scope = append(scope, "--user")
	}
	scope = append(scope, "--scope", "--quiet")
	if memoryMax != "" {
		// Without swap a runaway session is killed at the ceiling instead of slowing the host.
		scope = append(scope, "-p", "MemoryMax="+memoryMax, "-p", "MemorySwapMax=0")
	}
	if tasksMax != "" {
		scope = append(scope, "-p", "TasksMax="+tasksMax)
	}

	// One scope that does nothing, with the same ceilings: systemd itself says whether
	// this user can make a scope and whether it takes these values.
	ctx, cancel := context.WithTimeout(context.Background(), scopeProbeTimeout)
	defer cancel()
	probe := exec.CommandContext(ctx, scope[0], append(scope[1:], "--", envBin, "true")...) // #nosec G204 -- fixed tools found on PATH, ceilings are systemd property values, no shell
	probe.Env = []string{"PATH=" + os.Getenv("PATH")}
	var stderr bytes.Buffer
	probe.Stderr = &stderr
	if err := probe.Run(); err != nil {
		reason, _, _ := strings.Cut(strings.TrimSpace(stderr.String()), "\n")
		if reason == "" {
			reason = err.Error()
		}
		return lost + reason
	}

	// The first stages get the environment without what steers them; the program gets
	// all of it back from the last env stage, minus what a scope adds, with the
	// XDG_RUNTIME_DIR it had or none. env takes its options before its assignments.
	restore := []string{"-u", "INVOCATION_ID", "-u", "XDG_RUNTIME_DIR"}
	var first, assigned []string
	for _, kv := range cmd.Environ() {
		switch {
		case strings.HasPrefix(kv, "XDG_RUNTIME_DIR="):
			restore = []string{"-u", "INVOCATION_ID"}
			assigned = append(assigned, kv)
		case isSteeringEnv(kv):
			assigned = append(assigned, kv)
		default:
			first = append(first, kv)
		}
	}
	restore = append(restore, assigned...)
	cmd.Env = first
	argv := append(scope, "--", envBin)
	argv = append(argv, restore...)
	argv = append(argv, cmd.Path)
	if len(cmd.Args) > 1 {
		argv = append(argv, cmd.Args[1:]...)
	}
	cmd.Path, cmd.Args = envBin, argv
	return ""
}

func isSteeringEnv(kv string) bool {
	for _, prefix := range steeringEnv {
		if strings.HasPrefix(kv, prefix) {
			return true
		}
	}
	return false
}
