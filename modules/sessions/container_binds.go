// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// WHAT A CONTAINER SESSION SEES.
//
// A native child is held by a Landlock policy: the folder and the profile's homes are
// writable, the rest of the host is not. A container is held the other way round: it
// sees only what is mounted. This file turns the same decisions into mounts, so the
// promises a person was given (a read-only session cannot change its folder, a
// workspace's read-only folders stay read-only, the folder's Git hooks and
// configuration are not the session's to rewrite) hold under both isolations.
//
// The container runner (container_runner.go) renders these and decides nothing.

// containerSystemPaths are never mounted, and nothing that contains or lies inside one is:
// they hold the host's sockets (the container runtime's among them) and devices.
var containerSystemPaths = []string{"/run", "/var/run", "/proc", "/sys", "/dev"}

var containerMountRefusal = &runErr{http.StatusUnprocessableEntity, "a container session cannot be given a folder or account home that contains the engine's own data or one of the host's system directories; choose another folder or use native isolation"}

// containerRefusal is what a container launch cannot be given, decided before anything
// is built or started.
//
// A native child is held back from the engine's own data by Landlock, which carves a
// protected directory out of a grant that contains it (and grants a path inside one only
// when the policy names it, as it names an account home). A mount cannot be carved: it
// is the whole directory or nothing. So a folder or home that contains a protected path
// is refused, and one inside it is allowed, exactly as natively. The host's system
// directories (sockets, devices, /proc) are refused whichever way they overlap.
func (m *Module) containerRefusal(p CreateRunParams, d childDecision) error {
	var mounted []string
	if d.ws != nil {
		mounted = append(mounted, d.ws.rootReal)
	} else if p.WorkspaceDir != "" {
		mounted = append(mounted, p.WorkspaceDir)
	}
	if home := p.ProviderHome; home != nil {
		mounted = append(mounted, home.ConfigHome)
		if !isEngineUserHome(home.UserHome) {
			mounted = append(mounted, home.UserHome)
		}
	}
	for _, path := range mounted {
		if path == "" {
			continue
		}
		// What lies inside a system directory is as unfit as what contains one: /run/user/<id>
		// holds the container runtime's own socket.
		for _, system := range containerSystemPaths {
			if pathsOverlap(path, system) {
				return containerMountRefusal
			}
		}
		for _, protected := range m.rt.confineProtect {
			if pathWithin(protected, path) {
				return containerMountRefusal
			}
		}
	}
	return nil
}

// underImageSystemDir reports a path an image provides for itself: mounting the host's
// copy over it would replace the image's own programs and libraries.
func underImageSystemDir(path string) bool {
	for _, dir := range []string{"/usr", "/bin", "/sbin", "/lib", "/lib32", "/lib64", "/etc"} {
		if pathWithin(path, dir) {
			return true
		}
	}
	return false
}

// pathWithin reports whether child is parent or lies inside it, symbolic links resolved
// where the paths exist.
func pathWithin(child, parent string) bool {
	c, p := resolvedPath(child), resolvedPath(parent)
	sep := string(filepath.Separator)
	return c == p || strings.HasPrefix(c, strings.TrimSuffix(p, sep)+sep)
}

// containerFolderBinds are the mounts beyond the workspace itself. The workspace is
// LaunchSpec.Workspace, whose host root is d.ws.rootReal and whose container target is
// the path the tool runs in.
func (m *Module) containerFolderBinds(p CreateRunParams, d childDecision, preset string) []ContainerBind {
	var binds []ContainerBind
	readOnly := preset == PresetReadOnly
	root := ""
	if d.ws != nil {
		root = d.ws.rootReal
	}

	// The profile's homes, where the tool keeps its login and settings: writable,
	// except that a read-only session never gets a writable home over its folder.
	if home := p.ProviderHome; home != nil {
		grant := func(path string) {
			if path == "" {
				return
			}
			if readOnly && root != "" && pathsOverlap(path, root) {
				m.warnf("sessions: a read-only session does not get its profile home writable, because the home overlaps the session folder", "home", path, "folder", root)
				return
			}
			binds = append(binds, ContainerBind{HostPath: path, Target: path})
		}
		grant(home.ConfigHome)
		if !isEngineUserHome(home.UserHome) {
			grant(home.UserHome)
		}
	}
	if d.ws == nil {
		// No registered workspace: the run's own directory, at the path the tool is told.
		if dir := p.WorkspaceDir; dir != "" {
			binds = append(binds, ContainerBind{HostPath: dir, Target: dir, ReadOnly: readOnly})
		}
		return binds
	}

	// What the workspace holds read-only, over its read-write mount.
	for _, folder := range d.ws.readOnlyFolders {
		if target, ok := underContainerTarget(root, resolvedPath(folder), d.ws.containerTgt); ok {
			binds = append(binds, ContainerBind{HostPath: resolvedPath(folder), Target: target, ReadOnly: true})
		}
	}
	// The folder's Git hooks and configuration, which the next `git` the person runs
	// there would run. A read-only session cannot write them anyway. GitMounts names
	// files it would create inside a native child's own mount namespace (commondir,
	// config.worktree); a container has no such place, and a mount of a file that does
	// not exist would stop podman and make Docker create a directory in the folder, so
	// only what exists is mounted and the rest is said. A path outside the folder (a
	// linked worktree's Git directory) is not mounted at all, so Git does not work there
	// in a container; the tool's own error says so.
	if !readOnly {
		mounts, _ := m.GitMounts(root) // it logs what it could not guard
		var unguarded []string
		for _, guarded := range mounts.ReadOnly {
			if _, err := os.Lstat(guarded); err != nil {
				unguarded = append(unguarded, filepath.Base(guarded))
				continue
			}
			if target, ok := underContainerTarget(root, guarded, d.ws.containerTgt); ok {
				binds = append(binds, ContainerBind{HostPath: guarded, Target: target, ReadOnly: true})
			}
		}
		if len(unguarded) > 0 {
			m.warnf("sessions: part of a folder's Git configuration is not guarded against a container session because the files do not exist yet", "folder", root, "missing", strings.Join(unguarded, ", "))
		}
	}
	return binds
}

// underContainerTarget maps a host path inside root to where the container sees it.
func underContainerTarget(root, path, target string) (string, bool) {
	rel, err := filepath.Rel(root, path)
	if err == nil && rel == "." {
		return target, true // the folder itself
	}
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.Join(target, rel), true
}

// claudeManagedSettingsBind makes the host's Claude Code managed settings the policy a
// container session runs under: Claude Code reads /etc/claude-code in the image, and the
// hook check (CheckClaudeHookHostPolicy) judged the host's.
func claudeManagedSettingsBind() (ContainerBind, bool) {
	dir := claudeManagedSettingsDir
	if info, err := os.Stat(dir); err != nil || !info.IsDir() || !filepath.IsAbs(dir) {
		return ContainerBind{}, false
	}
	return ContainerBind{HostPath: filepath.Clean(dir), Target: "/etc/claude-code", ReadOnly: true, NoRelabel: true}, true
}
