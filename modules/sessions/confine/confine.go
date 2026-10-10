// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package confine runs a child process that can write only where it was told
// to and cannot read the engine's own files.
//
// Every program the engine starts for a session (the agent tool and its stdio
// MCP servers) runs as the engine's OS user. Without confinement that child can
// read the database and the key that seals every stored secret. On Linux the
// engine re-executes itself as a small helper (HelperArg) that applies a
// Landlock allow-list and then execs the program: the child may write its
// ReadWrite paths, read its ReadOnly paths and the system directories a program
// needs, and nothing else. Protected paths are carved out of every grant.
// Landlock needs no root, no namespaces and no extra binary. The native session
// runner adds its separate network namespace/proxy before entering this helper.
// This filesystem policy alone does not restrict networking. Other systems
// report ModeNone with the reason.
package confine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	coreconfine "github.com/olivaresai/olivares/core/runtime/confine"
)

// HelperArg is argv[1] of the engine binary re-executed as the confinement
// helper. The engine's main hands such an invocation to RunHelper before
// anything else runs.
const HelperArg = coreconfine.HelperArg

// The session limits the helper sets on Linux (limits_linux.go).
const (
	// SessionNice is how much lower than the engine a session runs, so the
	// console and the API keep answering while sessions build. On an idle node
	// a session is not slower.
	SessionNice = 10
	// SessionFileSizeLimit is the largest file one session process may write.
	SessionFileSizeLimit = 64 << 30
)

// SessionLimits describes the session limits for logs and doctor.
func SessionLimits() string {
	return "nice +10, no core dumps, files up to 64 GiB"
}

// Policy says where a confined child may write and read.
type Policy struct {
	// ReadWrite are the paths the child may read, create, change and run
	// (the session folder, the tool's account home, the session's temp dir).
	ReadWrite []string
	// ReadOnly are extra paths the child may read and run (a tool install).
	ReadOnly []string
	// Protect are paths the child may never reach, even when a grant contains
	// them (the engine data directory, its configuration).
	Protect []string
	// Sealed are paths the child may read and must never change (the folder of a
	// read-only session). Landlock grants add up, so no writable path may be the
	// same as one, lie inside one or contain one: not ReadWrite, not the runner's
	// temporary directory, not a directory the helper grants by default. A policy
	// that would reopen a sealed path is refused before anything starts. On ABI 1/2,
	// truncation is unhandled; the default continues and reports that kernel limit.
	Sealed []string
}

// Mode is how a child is confined.
type Mode string

const (
	// ModeLandlock is the Linux Landlock allow-list.
	ModeLandlock Mode = Mode(coreconfine.ModeLandlock)
	// ModeNone means the child runs unconfined; State.Reason says why.
	ModeNone Mode = Mode(coreconfine.ModeNone)
)

// State is what this host can enforce.
type State struct {
	Mode Mode
	// ABI is the Landlock ABI version (0 when unavailable).
	ABI int
	// Reason says why Mode is ModeNone, or which protection a confined run lacks.
	Reason string
}

// String is the one-line form recorded with a session.
func (s State) String() string {
	if s.Mode == ModeLandlock {
		text := fmt.Sprintf("landlock (ABI %d)", s.ABI)
		if s.Reason != "" {
			text += ": " + s.Reason
		}
		return text
	}
	return "none: " + s.Reason
}

// Probe reports what this host can enforce.
func Probe() State { return probe() }

// helperExecutable is the engine binary that serves HelperArg.
var helperExecutable = os.Executable

// Command returns the command that runs program confined by p. The program is
// resolved on the engine's PATH, as exec.CommandContext would. When the host
// cannot confine, the returned command runs the program unconfined and State
// says why: the caller decides whether to start it.
func Command(ctx context.Context, p Policy, program string, args ...string) (*exec.Cmd, State, error) {
	return CommandWithTruncateProtection(ctx, p, false, program, args...)
}

// CommandWithTruncateProtection applies an optional strict kernel requirement
// without changing the published four-field Policy or Command signature.
func CommandWithTruncateProtection(ctx context.Context, p Policy, required bool, program string, args ...string) (*exec.Cmd, State, error) {
	state := Probe()
	if state.Mode != ModeLandlock {
		return exec.CommandContext(ctx, program, args...), state, nil // #nosec G204 -- the caller's own program and argv; no shell
	}
	state, err := p.validateSealedABI(state, required)
	if err != nil {
		return nil, state, err
	}
	if err := p.validate(); err != nil {
		return nil, state, err
	}
	if err := p.sealedConflict(defaultWritable()); err != nil {
		return nil, state, err
	}
	resolved, err := exec.LookPath(program)
	if err != nil {
		return nil, state, err
	}
	if resolved, err = filepath.Abs(resolved); err != nil {
		return nil, state, err
	}
	self, err := helperExecutable()
	if err != nil {
		return nil, state, fmt.Errorf("confine: locate the engine binary: %w", err)
	}
	policyArgs := p.encode()
	if required {
		policyArgs = append([]string{"--require-truncate-protection"}, policyArgs...)
	}
	helperArgs := append(policyArgs, "--", resolved)
	helperArgs = append(helperArgs, args...)
	cmd := exec.CommandContext(ctx, self, append([]string{HelperArg}, helperArgs...)...) // #nosec G204 -- the engine re-executes itself with a fixed helper argument
	return cmd, state, nil
}

func (p Policy) validateSealedABI(state State, required bool) (State, error) {
	if len(p.Sealed) != 0 && state.ABI < 3 {
		state.Reason = "this kernel cannot block truncation of existing files"
		if required {
			return state, fmt.Errorf("confine: read-only paths require Landlock ABI 3 to prevent truncation; this host has ABI %d; the session was not started", state.ABI)
		}
	}
	return state, nil
}

func (p Policy) validate() error {
	for _, group := range [][]string{p.ReadWrite, p.ReadOnly, p.Protect, p.Sealed} {
		for _, path := range group {
			if !filepath.IsAbs(path) {
				return fmt.Errorf("confine: %q is not an absolute path", path)
			}
		}
	}
	if len(p.ReadWrite) == 0 {
		return errors.New("confine: a confined child needs at least one writable path")
	}
	return nil
}

func (p Policy) encode() []string {
	var out []string
	for _, path := range p.ReadWrite {
		out = append(out, "--rw", path)
	}
	for _, path := range p.ReadOnly {
		out = append(out, "--ro", path)
	}
	for _, path := range p.Protect {
		out = append(out, "--protect", path)
	}
	for _, path := range p.Sealed {
		out = append(out, "--sealed", path)
	}
	return out
}

// sealedConflict refuses a writable path (ReadWrite or one of defaults, the
// helper's own writable grants) that is the same as a sealed path, lies inside
// it or contains it.
func (p Policy) sealedConflict(defaults []string) error {
	writable := append(append([]string{}, p.ReadWrite...), defaults...)
	for _, sealed := range p.Sealed {
		s := realPath(sealed)
		for _, path := range writable {
			w := realPath(path)
			if contains(s, w) || contains(w, s) {
				return fmt.Errorf("confine: the read-only path %q would be writable through %q; the session was not started", sealed, path)
			}
		}
	}
	return nil
}

// decode is encode's inverse; it returns the policy and the program argv.
func decode(args []string) (Policy, []string, error) {
	var p Policy
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--":
			if i+1 >= len(args) {
				return p, nil, errors.New("no program after --")
			}
			return p, args[i+1:], nil
		case "--rw", "--ro", "--protect", "--sealed":
			if i+1 >= len(args) {
				return p, nil, fmt.Errorf("%s needs a path", args[i])
			}
			path := filepath.Clean(args[i+1])
			switch args[i] {
			case "--rw":
				p.ReadWrite = append(p.ReadWrite, path)
			case "--ro":
				p.ReadOnly = append(p.ReadOnly, path)
			case "--sealed":
				p.Sealed = append(p.Sealed, path)
			default:
				p.Protect = append(p.Protect, path)
			}
			i++
		default:
			return p, nil, errors.New("unexpected confinement argument")
		}
	}
	return p, nil, errors.New("no program after --")
}

// RunHelper applies the policy encoded in args and execs the program. It
// returns only when it cannot: the exit code then says the program never ran.
func RunHelper(args []string) int {
	return RunHelperWithReady(args, nil)
}

// RunHelperWithReady reports readiness to the network supervisor after Landlock.
func RunHelperWithReady(args []string, ready *os.File) int {
	if coreconfine.IsExplicitPolicy(args) {
		return coreconfine.RunHelperWithReady(args, ready)
	}
	required := len(args) != 0 && args[0] == "--require-truncate-protection"
	if required {
		args = args[1:]
	}
	p, argv, err := decode(args)
	if err == nil {
		err = p.validate()
	}
	if err == nil {
		if state := Probe(); state.Mode != ModeLandlock {
			err = errors.New(state.Reason)
		} else if _, err = p.validateSealedABI(state, required); err != nil {
			// The helper repeats the same strict check before the native installer.
		} else if !filepath.IsAbs(argv[0]) {
			err = fmt.Errorf("program %q is not an absolute path", argv[0])
		} else {
			policy := sessionPolicy(p, argv[0])
			policy.Ready = ready
			err = coreconfine.Exec(policy, argv[0], argv)
		}
	}
	if ready != nil {
		_, _ = fmt.Fprintln(ready, "filesystem confinement failed:", err)
	}
	fmt.Fprintln(os.Stderr, "olivares: the session program was not started because it could not be confined:", err)
	return 126
}

// realPath resolves symlinks; a path that does not exist keeps its clean form.
func realPath(path string) string {
	if r, err := filepath.EvalSymlinks(path); err == nil {
		return r
	}
	return filepath.Clean(path)
}

// contains reports whether path is dir or lies under it.
func contains(dir, path string) bool {
	if dir == path {
		return true
	}
	return strings.HasPrefix(path, strings.TrimSuffix(dir, string(filepath.Separator))+string(filepath.Separator))
}
