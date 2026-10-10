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
)

// OpenDirectory holds a canonical directory without following changed symlinks.
// The launch resolver closes the returned handle after launch.
func OpenDirectory(path string) (*os.File, error) { return coreconfine.OpenDirectory(path) }

// CommandWithHandles adds borrowed directory grants without changing the published
// four-field Policy or Command. The helper closes its copies before tool execution.
func CommandWithHandles(ctx context.Context, p Policy, files []*os.File, program string, args ...string) (*exec.Cmd, State, error) {
	if len(files) == 0 {
		return Command(ctx, p, program, args...)
	}
	state := Probe()
	if state.Mode != ModeLandlock {
		return nil, state, errors.New(state.Reason)
	}
	if err := p.validate(); err != nil {
		return nil, state, err
	}
	check := p
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
	var err error
	if cmd.Path, err = filepath.Abs(cmd.Path); err != nil {
		return nil, state, err
	}
	q := sessionPolicy(p, cmd.Path)
	q.SealedFiles = files
	_, err = coreconfine.Wrap(cmd, q)
	return cmd, state, err
}
