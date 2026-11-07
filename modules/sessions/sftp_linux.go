// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package sessions

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// startSFTPChild starts the operating system's sftp-server for a folder, confined to it as a
// terminal in that folder is (folderChildSpec), with its input and output on two pipes the SSH
// session carries. The pipes are plain file descriptors, not the line pump a tool's output uses,
// because SFTP is binary. The guard that ends every descendant when the engine dies is the one
// every session child has.
func (m *Module) startSFTPChild(ws *resolvedWorkspace, program string, readOnly bool) (*sftpChild, error) {
	spec := m.folderChildSpec(ws, readOnly)
	spec.Program = program
	// -d is the folder the client starts in. sftp-server expands '%' in it, so a folder with one
	// in its name is escaped.
	spec.Args = []string{"-d", strings.ReplaceAll(ws.rootReal, "%", "%%")}
	if readOnly || ws.mountMode == mountRO {
		spec.Args = append(spec.Args, "-R") // the protocol refuses writes too, besides the policy
	}
	if spec.Confinement != nil {
		// The program must run under the policy wherever the distribution keeps it.
		policy := *spec.Confinement
		policy.ReadOnly = append(append([]string{}, policy.ReadOnly...), filepath.Dir(program))
		spec.Confinement = &policy
	}

	pr := &procRunner{lineCap: maxOutputLine}
	runCtx, cancel := context.WithCancel(context.Background())
	cmd, env, state, release, err := pr.command(runCtx, spec)
	if err != nil {
		cancel()
		return nil, err
	}
	fail := func(err error) (*sftpChild, error) {
		cancel()
		release()
		return nil, err
	}
	cmd.Dir = spec.Dir
	cmd.Env = sanitizedEnv(spec.EnvAllow, env)
	if spec.WaitDelay > 0 {
		cmd.WaitDelay = spec.WaitDelay
	}
	configureProcGroup(cmd)
	stdinRead, stdin, err := os.Pipe()
	if err != nil {
		return fail(fmt.Errorf("sessions: sftp stdin pipe: %w", err))
	}
	defer stdinRead.Close()
	stdout, stdoutWrite, err := os.Pipe()
	if err != nil {
		_ = stdin.Close()
		return fail(fmt.Errorf("sessions: sftp stdout pipe: %w", err))
	}
	defer stdoutWrite.Close()
	// stderr is /dev/null: sftp-server's log lines name the files a person copies.
	cmd.Stdin, cmd.Stdout = stdinRead, stdoutWrite
	engine, err := watchEngine(cmd, true)
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return fail(fmt.Errorf("sessions: start %q: %w", program, err))
	}
	if engine != nil {
		cmd.Cancel = func() error { engine.endLifetime(); return nil }
	}
	if err := cmd.Start(); err != nil {
		engine.close()
		_ = stdin.Close()
		_ = stdout.Close()
		return fail(fmt.Errorf("sessions: start %q: %w", program, err))
	}
	engine.started()
	cmd.Env = nil
	return &sftpChild{cmd: cmd, stdin: stdin, stdout: stdout, cancel: cancel, release: release, confinement: state}, nil
}
