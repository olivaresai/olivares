// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/olivaresai/olivares/modules/sessions/confine"
)

// command builds the child's command and environment. A spec with a
// confinement policy runs the program through confine.Command with a private
// temporary directory (TMPDIR) the child may write; release removes it once the
// child has exited. Without a policy the program runs as before.
func (pr *procRunner) command(ctx context.Context, spec LaunchSpec) (*exec.Cmd, []EnvVar, confine.State, func(), error) {
	none := confine.State{Mode: confine.ModeNone, Reason: "no confinement policy was given for this child (the engine was composed without WithConfinement)"}
	if spec.Confinement == nil {
		if spec.ConfinementRequired {
			return nil, nil, none, nil, errors.New("sessions: this session must run confined (a read-only session) and this node confines no session; it was not started")
		}
		cmd := exec.CommandContext(ctx, spec.Program, spec.Args...) // #nosec G204 -- spec.Program/Args are operator-configured session runtime; env is sanitized and container/sandbox isolation is refused deny-closed
		return cmd, spec.Env, none, func() {}, nil
	}
	tmp, err := os.MkdirTemp("", "olivares-session-")
	if err != nil {
		return nil, nil, none, nil, fmt.Errorf("sessions: the session's temporary directory: %w", err)
	}
	release := func() { _ = os.RemoveAll(tmp) }
	policy := *spec.Confinement
	policy.ReadWrite = append(append([]string{}, policy.ReadWrite...), tmp)
	cmd, state, err := confine.Command(ctx, policy, spec.Program, spec.Args...)
	if err == nil && state.Mode == confine.ModeNone && spec.ConfinementRequired {
		err = fmt.Errorf("sessions: this node cannot confine the session (%s); it was not started", state.Reason)
	}
	if err != nil {
		release()
		return nil, nil, state, nil, err
	}
	// The session's TMPDIR comes first so it wins over any inherited value: a
	// confined child can write nowhere else for scratch files. A child that names
	// no HOME (a stdio MCP server) gets the same directory as HOME, so tools that
	// keep caches there (npx, uvx) work; the inherited HOME would be unwritable.
	env := append([]EnvVar{{Name: "TMPDIR", Value: tmp}}, spec.Env...)
	if !envNamed(spec.Env, "HOME") {
		env = append(env, EnvVar{Name: "HOME", Value: tmp})
	}
	return cmd, env, state, release, nil
}

// Confinement reports how the child runs.
func (p *procProcess) Confinement() confine.State { return p.confinement }

// confinementOf is the confinement a launched process reports, if any.
func confinementOf(proc Process) (confine.State, bool) {
	c, ok := proc.(interface{ Confinement() confine.State })
	if !ok {
		return confine.State{}, false
	}
	return c.Confinement(), true
}

func envNamed(env []EnvVar, name string) bool {
	for _, e := range env {
		if e.Name == name {
			return true
		}
	}
	return false
}
