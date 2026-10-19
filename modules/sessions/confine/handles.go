// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package confine

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"

	coreconfine "github.com/olivaresai/olivares/core/runtime/confine"
	"github.com/olivaresai/olivares/sdk/gitlayout"
)

// OpenDirectory holds a canonical directory without following changed symlinks.
// The launch resolver closes the returned handle after launch.
func OpenDirectory(path string) (*os.File, error) { return coreconfine.OpenDirectory(path) }

// CommandWithHandles adds borrowed directory grants without changing the published
// four-field Policy or Command. The helper closes its copies before tool execution.
func CommandWithHandles(ctx context.Context, p Policy, files []*os.File, program string, args ...string) (*exec.Cmd, State, error) {
	return CommandWithNoDelete(ctx, p, false, files, nil, program, args...)
}

// CommandWithNoDelete is CommandWithHandles plus folders the child may fill and
// change but never lose a name from: no unlink, rmdir, rename or move out. Like
// the handles it leaves the published Policy alone. The rights it withholds
// exist from Landlock ABI 1, so it needs no newer kernel than confinement does;
// a host that cannot confine refuses the launch. A writable grant over or inside
// a no-delete folder would give the rights back and refuses the launch too.
// The one exception is the Git directory of a folder that is a repository: Git
// rewrites its index by renaming index.lock over it (even git status does, after a
// file's timestamp changed), and a refused rename leaves the lock behind for good.
// That directory keeps the full write rights, so the agent can empty it and destroy
// the repository's history (the directory itself stays: removing it needs the withheld
// rights on the folder); the working files cannot be removed. A linked worktree's Git directories are
// granted by the caller and lie outside the folder.
// required is CommandWithTruncateProtection's strict kernel requirement for p.Sealed.
func CommandWithNoDelete(ctx context.Context, p Policy, required bool, files []*os.File, noDelete []string, program string, args ...string) (*exec.Cmd, State, error) {
	if len(files) == 0 && len(noDelete) == 0 {
		return CommandWithTruncateProtection(ctx, p, required, program, args...)
	}
	state := Probe()
	if state.Mode != ModeLandlock {
		return nil, state, errors.New(state.Reason)
	}
	state, err := p.validateSealedABI(state, required)
	if err != nil {
		return nil, state, err
	}
	if err := p.validate(); err != nil {
		return nil, state, err
	}
	check := p
	// A no-delete folder is writable too: a read-only path under it is not read-only.
	keep := gitDirs(noDelete)
	check.ReadWrite = append(append(append([]string{}, p.ReadWrite...), noDelete...), keep...)
	check.Sealed = append([]string{}, p.Sealed...)
	for _, file := range files {
		if err := coreconfine.ValidateDirectoryHandle(file); err != nil {
			return nil, state, err
		}
		check.Sealed = append(check.Sealed, file.Name())
	}
	if err := check.sealedConflict(defaultWritable()); err != nil {
		return nil, state, err
	}
	cmd := exec.CommandContext(ctx, program, args...) // #nosec G204 -- the caller's original program/argv, no shell
	if cmd.Err != nil {
		return nil, state, cmd.Err
	}
	if cmd.Path, err = filepath.Abs(cmd.Path); err != nil {
		return nil, state, err
	}
	q := sessionPolicy(p, cmd.Path)
	q.SealedFiles = files
	q.NoDelete = noDelete
	q.KeepWritable = keep
	if _, err = coreconfine.Wrap(cmd, q); err != nil {
		return nil, state, err
	}
	return cmd, state, nil
}

// gitDirs is the Git directory inside each folder that is a plain repository.
func gitDirs(folders []string) []string {
	var dirs []string
	for _, folder := range folders {
		if layout, ok := gitlayout.Read(folder); ok && !layout.Linked() {
			dirs = append(dirs, layout.GitDir)
		}
	}
	return dirs
}

// Mounts names paths of a writable grant that the child must see read-only. They
// are applied in a mount namespace of the child's own, before Landlock.
type Mounts = coreconfine.Mounts

// Node is a directory or file that WrapMounts makes, when it is missing, before
// it binds it.
type Node = coreconfine.Node

// ErrMountsUnavailable is what WrapMounts wraps on a host that gives the child
// no user and mount namespace; the child can still be started without them.
var ErrMountsUnavailable = coreconfine.ErrMountsUnavailable

// WrapMounts applies m to cmd, which Command or CommandWithHandles returned and
// which is therefore the confinement helper's command. The Policy stays as it
// was published; the mounts are a separate layer in front of its arguments.
func WrapMounts(cmd *exec.Cmd, m Mounts) error { return coreconfine.WrapMounts(cmd, m) }
