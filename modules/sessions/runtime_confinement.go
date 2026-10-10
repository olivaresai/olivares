// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/olivaresai/olivares/modules/sessions/confine"
	"github.com/olivaresai/olivares/sdk/gitlayout"
)

// WithConfinement confines every child this module starts (the agent tool,
// and stdio MCP servers that ask for ConfinementPolicy) so it can write only its
// session folder, its linked-worktree Git metadata, its account homes and a private
// temporary directory, and can never reach a protected path (the engine data
// directory, its configuration).
// required refuses a launch on a host that cannot confine instead of running it
// unconfined.
func WithConfinement(protect []string, required bool) Option {
	return func(m *Module) {
		m.rt.confineProtect = nil
		for _, p := range protect {
			if filepath.IsAbs(p) {
				m.rt.confineProtect = append(m.rt.confineProtect, filepath.Clean(p))
			}
		}
		m.rt.confineRequired = required
	}
}

// ConfinementPolicy is the policy for a child that may write readWrite and read
// readOnly, with this node's protected paths. It is nil when confinement is not
// wired, which runs the child unconfined.
func (m *Module) ConfinementPolicy(readWrite, readOnly []string) *confine.Policy {
	if len(m.rt.confineProtect) == 0 {
		return nil
	}
	p := &confine.Policy{Protect: append([]string{}, m.rt.confineProtect...)}
	for _, path := range readWrite {
		if path != "" {
			p.ReadWrite = append(p.ReadWrite, path)
		}
	}
	for _, path := range readOnly {
		if path != "" {
			p.ReadOnly = append(p.ReadOnly, path)
		}
	}
	return p
}

// sessionConfinement is the policy of an agent launch: its working directory
// its linked-worktree Git metadata and the profile's homes (where the tool keeps
// its login and settings).
//
// A profile whose user home is the engine user's own home directory (the
// tool's standard login location) gets only its configuration home: granting
// that whole home would let the agent write every project and key in it.
//
// A read-only session (preset read_only) may read its working directory and not
// change it: the folder is read-only for the whole process tree, whatever the
// tool's own settings say or a newer tool version does, subject to the kernel
// limitation the run reports on Landlock ABI 1/2. Its homes and temporary
// directory stay writable, so the tool still keeps its state, EXCEPT a home that
// overlaps the folder (above it, the same directory or inside it): Landlock
// grants add up, so a writable home there would reopen the folder.
func (m *Module) sessionConfinement(dir string, home *ProviderHomeSnapshot, preset string) *confine.Policy {
	readOnly := preset == PresetReadOnly
	rw, ro := []string{dir}, []string(nil)
	if readOnly {
		rw, ro = nil, []string{dir}
	}
	grant := func(path string) {
		if readOnly && pathsOverlap(path, dir) {
			m.warnf("sessions: a read-only session does not get its profile home writable, because the home overlaps the session folder", "home", path, "folder", dir)
			return
		}
		rw = append(rw, path)
	}
	if home != nil {
		grant(home.ConfigHome)
		if !isEngineUserHome(home.UserHome) {
			grant(home.UserHome)
		}
	}
	p := m.ConfinementPolicy(rw, ro)
	if p == nil {
		return nil
	}
	// A linked worktree needs its own index/HEAD and the repository's shared
	// objects, refs and logs; a normal clone's metadata is already in its folder.
	var linked []string
	if layout, ok := gitlayout.Read(dir); ok && layout.Linked() {
		linked = []string{layout.GitDir, layout.CommonDir}
	}
	// Inferred grants must not reopen a named directory beneath Protect.
gitDirs:
	for _, path := range linked {
		for _, protected := range p.Protect {
			protected = resolvedPath(protected)
			if path == protected || strings.HasPrefix(path, strings.TrimSuffix(protected, string(filepath.Separator))+string(filepath.Separator)) {
				continue gitDirs
			}
		}
		if readOnly {
			p.ReadOnly = append(p.ReadOnly, path)
		} else {
			p.ReadWrite = append(p.ReadWrite, path)
		}
	}
	if readOnly {
		// Sealed: any other writable grant that would reopen the folder (the
		// runner's temporary directory, a device directory) refuses the launch.
		p.Sealed = []string{dir}
	}
	return p
}

// pathsOverlap reports whether a and b are the same directory or one contains the
// other, with symlinks resolved where the paths exist.
func pathsOverlap(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	a, b = resolvedPath(a), resolvedPath(b)
	sep := string(filepath.Separator)
	return a == b || strings.HasPrefix(a, strings.TrimSuffix(b, sep)+sep) || strings.HasPrefix(b, strings.TrimSuffix(a, sep)+sep)
}

func resolvedPath(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return filepath.Clean(path)
}

// engineUserHome is the engine user's own home directory ("" when unknown).
var engineUserHome = func() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}

func isEngineUserHome(path string) bool {
	h := engineUserHome()
	return h != "" && path != "" && filepath.Clean(path) == filepath.Clean(h)
}

// confinementDetail is the run-event text that says how the child runs. An
// unconfined child is also logged as a warning: it can read the engine's files.
func (m *Module) confinementDetail(proc Process, runRef string) string {
	state, ok := confinementOf(proc)
	if !ok {
		return ""
	}
	if state.Mode == confine.ModeNone {
		m.warnf("sessions: this session runs UNCONFINED and can read what the engine user can", "run_ref", runRef, "reason", state.Reason)
	}
	if state.Mode == confine.ModeLandlock && state.Reason != "" {
		m.warnf("sessions: filesystem confinement has a kernel limit", "run_ref", runRef, "reason", state.Reason)
	}
	return "confinement: " + state.String()
}

// reportedConfinement carries the same process capability into the audit chain.
func reportedConfinement(proc Process) *confine.State {
	state, ok := confinementOf(proc)
	if !ok {
		return nil
	}
	return &state
}
