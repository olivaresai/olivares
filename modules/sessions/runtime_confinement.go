// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"errors"
	"io/fs"
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

// WithCredentialDataDir names the engine data directory for credential grants. A broker may grant
// a file in that directory's own run directory (DataDir/run/<run ref>) even though the directory
// is protected, or lies inside a protected folder; the run directory of any other folder stays
// protected. Unset, nothing is granted from a protected folder.
func WithCredentialDataDir(dataDir string) Option {
	return func(m *Module) {
		m.rt.credentialDataDir = ""
		if dataDir = strings.TrimSpace(dataDir); filepath.IsAbs(dataDir) {
			m.rt.credentialDataDir = filepath.Clean(dataDir)
		}
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
	gitNamed := false
	if layout, ok := gitlayout.Read(dir); ok && layout.Linked() {
		linked = []string{layout.GitDir, layout.CommonDir}
		gitNamed = strings.HasSuffix(filepath.Base(layout.CommonDir), ".git")
	}
	// Inferred grants must not reopen a named directory beneath Protect for a
	// session outside it. A worktree below protected data still gets only its
	// own Git directory and its repository's common directory, and only when
	// that is a Git directory by name (.git, or name.git for a bare one): the
	// pointer files are the session's own, so the name is what keeps a forged
	// entry from naming the data directory or any other ancestor.
	// ponytail: a common directory with another name (--separate-git-dir, a bare
	// repository without the suffix) stays closed beneath Protect, as it always
	// was; upgrade when sessionConfinement can check it against the registered
	// workspace's own repository.
gitDirs:
	for _, path := range linked {
		for _, protected := range p.Protect {
			protected = resolvedPath(protected)
			if pathWithin(path, protected) && (!gitNamed || !pathWithin(dir, protected)) {
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

// withoutDelete takes the session folder out of the policy's writable grants and
// returns it as a no-delete folder (workspace mount_mode write_no_delete): the agent
// may create and change what is in it and never remove, rename or move a name.
// Landlock grants add up, so the folder cannot stay writable beside its no-delete
// grant. The folder is always returned: a writable grant this does not take out (an
// alias of the folder, a home above it) overlaps it, and the policy's own overlap
// check refuses the launch instead of starting it with the rule dropped. The caller
// skips a read-only session, which seals the folder and keeps the stricter rule.
// A plain repository's .git directory is the one part of the folder that keeps the
// delete rights (confine.CommandWithNoDelete): Git cannot finish an index write
// without renaming its lock file.
func withoutDelete(p *confine.Policy, dir string) []string {
	if p == nil || dir == "" {
		return nil
	}
	kept := make([]string, 0, len(p.ReadWrite))
	for _, path := range p.ReadWrite {
		if path != dir && resolvedPath(path) != resolvedPath(dir) {
			kept = append(kept, path)
		}
	}
	p.ReadWrite = kept
	return []string{dir}
}

// GitMounts are the mounts that keep a session from changing the Git hooks and
// configuration of its folder: the next `git` the person runs there would run
// what the session wrote. Landlock cannot deny a path inside a writable grant, a
// mount can. Each guarded node is the repository's own; the directories that
// hold them are pinned so the session cannot move one away and put a copy in
// its place. The note says which part the mounts could not cover (a symbolic
// link, a layout this launch cannot read) and is "" when all of it is.
// The guard on a file lasts as long as the file: when something outside the
// session deletes it or renames another over it (git does that to config on
// `git config`, `git remote add` or `git push -u`), the mount is gone in the
// session's namespace and the session writes the new file.
// Not covered: that, a sibling worktree's entry, a submodule's Git directory, a
// repository nested in the folder and a folder that has no .git yet.
func (m *Module) GitMounts(dir string) (confine.Mounts, string) {
	var mounts confine.Mounts
	var skipped []string
	if dir == "" {
		return mounts, "" // no folder: a relative ".git" would name the engine's own
	}
	layout, ok := gitlayout.Read(dir)
	if !ok {
		dotgit := filepath.Join(resolvedPath(dir), ".git")
		info, err := os.Lstat(dotgit)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return mounts, "" // not a Git folder
		case err != nil:
			skipped = append(skipped, ".git cannot be read")
		case info.Mode().IsRegular():
			// A pointer this launch cannot validate: at least keep it from being rewritten.
			mounts.ReadOnly = append(mounts.ReadOnly, dotgit)
			skipped = append(skipped, "its Git directory is not one this launch can read: a submodule, a separate Git directory or a changed pointer")
		default:
			skipped = append(skipped, "its Git directory is not one this launch can read")
		}
		return m.gitMountsNote(dir, mounts, skipped)
	}
	// guard binds path read-only. A missing path that git would read and the
	// session could create is made first (mounts.Create, by WrapMounts, on a host
	// that can then guard it); one that has no node to make is named instead.
	guard := func(path string, node *confine.Node) {
		info, err := os.Lstat(path)
		switch {
		case errors.Is(err, fs.ErrNotExist) && node != nil:
			mounts.Create = append(mounts.Create, *node)
			mounts.ReadOnly = append(mounts.ReadOnly, path)
		case err != nil:
			skipped = append(skipped, filepath.Base(path)+" cannot be read")
		case info.Mode()&os.ModeSymlink != 0:
			// The session could replace the link, so nothing about it is guarded.
			skipped = append(skipped, filepath.Base(path)+" is a symbolic link")
		default:
			mounts.ReadOnly = append(mounts.ReadOnly, path)
		}
	}
	file := func(path, content string) *confine.Node { return &confine.Node{Path: path, Content: content} }
	hooks := filepath.Join(layout.CommonDir, "hooks")
	guard(hooks, &confine.Node{Path: hooks, Dir: true}) // git init always makes hooks/
	guard(filepath.Join(layout.CommonDir, "config"), nil)
	// Git follows a commondir file in any Git directory, so one the session
	// created in the repository's own would redirect git to a repository the
	// session wrote. A commondir that names the directory itself changes nothing.
	commondir := filepath.Join(layout.CommonDir, "commondir")
	if _, err := os.Lstat(commondir); err == nil && !gitlayout.NamesItself(layout.CommonDir) {
		// Read checked a plain repository's; a linked worktree's main one is not.
		skipped = append(skipped, "commondir does not name its own directory")
	} else {
		guard(commondir, file(commondir, ".\n"))
	}
	// Per-worktree settings are read from any Git directory once
	// extensions.worktreeConfig is on, which a later `git sparse-checkout` does.
	// An empty file is a valid one.
	perWorktree := func(gitDir string) {
		path := filepath.Join(gitDir, "config.worktree")
		guard(path, file(path, ""))
	}
	perWorktree(layout.CommonDir)
	mounts.Pin = []string{layout.GitDir}
	if layout.Linked() {
		perWorktree(layout.GitDir)
		// worktrees/ is pinned too: renamed, it could be recreated with a forged entry.
		mounts.Pin = []string{filepath.Dir(layout.GitDir), layout.GitDir}
		// The links that say where the common directory is, from both sides.
		guard(filepath.Join(layout.GitDir, "commondir"), nil)
		guard(filepath.Join(layout.GitDir, "gitdir"), nil)
		guard(filepath.Join(resolvedPath(dir), ".git"), nil)
	}
	return m.gitMountsNote(dir, mounts, skipped)
}

func (m *Module) gitMountsNote(dir string, mounts confine.Mounts, skipped []string) (confine.Mounts, string) {
	if len(skipped) == 0 {
		return mounts, ""
	}
	note := strings.Join(skipped, ", ")
	m.warnf("sessions: part of a folder's Git hooks and configuration is not guarded against the session", "folder", dir, "reason", note)
	return mounts, note
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
