// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package agenttoolsapi

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

type statusSelection struct {
	tenant                            model.TenantID
	driver, accountRef, program, home string
}

type statusRead struct {
	program, auth os.FileInfo
	done          chan struct{}
	status        SignInStatus
	err           error
	callerEnded   bool
	expires       time.Time
}

func sameStatusFile(a, b os.FileInfo) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return os.SameFile(a, b) && a.Size() == b.Size() && a.Mode() == b.Mode() && a.ModTime().Equal(b.ModTime())
}

// Native status starts a tool process. The console and profile resolver share
// that read. Authorize the home before reaching here; only stat the native
// credential file, never open it. Native login/logout and executable replacement
// invalidate the result, with a short lifetime for changes outside these files.
func (m *Module) readCachedStatus(ctx context.Context, tenant model.TenantID, out SignInStatus, program, configDir string, env []string) (SignInStatus, error) {
	for {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		key := statusSelection{tenant, out.Driver, out.AccountRef, program, envValue(env, "HOME")}
		// Match exec.Command's PATH lookup and relative paths under cmd.Dir.
		programPath := program
		if !strings.ContainsRune(program, os.PathSeparator) {
			var err error
			programPath, err = exec.LookPath(program)
			if err != nil {
				return out, err
			}
		} else if !filepath.IsAbs(program) {
			programPath = filepath.Join(key.home, program)
		}
		programInfo, err := os.Stat(programPath)
		if err != nil {
			return out, err
		}
		var authPath string
		switch out.Driver {
		case "claude":
			authPath = filepath.Join(envValue(env, "CLAUDE_CONFIG_DIR"), ".credentials.json")
		case "codex":
			authPath = filepath.Join(envValue(env, "CODEX_HOME"), "auth.json")
		case "opencode":
			authPath = filepath.Join(envValue(env, "XDG_DATA_HOME"), "opencode", "auth.json")
		default:
			return m.readNativeStatus(ctx, out, program, configDir, env)
		}
		authInfo, err := os.Stat(authPath)
		if err != nil && !os.IsNotExist(err) {
			return out, err
		}
		m.mu.Lock()
		if read := m.statusReads[key]; read != nil && time.Now().Before(read.expires) && sameStatusFile(programInfo, read.program) && sameStatusFile(authInfo, read.auth) {
			m.mu.Unlock()
			select {
			case <-ctx.Done():
				return out, ctx.Err()
			case <-read.done:
				if read.callerEnded && ctx.Err() == nil {
					continue // The initiating request ended; this caller still needs a result.
				}
				return read.status, read.err
			}
		}
		if m.statusReads == nil {
			m.statusReads = make(map[statusSelection]*statusRead)
		}
		// Discard expired entries; at capacity evict completed reads, never waiters.
		for k, read := range m.statusReads {
			if time.Now().After(read.expires) || len(m.statusReads) >= maxRetained {
				select {
				case <-read.done:
					delete(m.statusReads, k)
				default:
				}
			}
		}
		read := &statusRead{program: programInfo, auth: authInfo, done: make(chan struct{}), expires: time.Now().Add(30 * time.Second)}
		m.statusReads[key] = read
		m.mu.Unlock()

		read.status, read.err = m.readNativeStatus(ctx, out, program, configDir, env)
		read.callerEnded = read.err != nil && ctx.Err() != nil
		m.mu.Lock()
		// Invalidation while the process ran must not restore the old entry.
		if (read.err != nil || read.status.unrecognized) && m.statusReads[key] == read {
			delete(m.statusReads, key)
		}
		close(read.done)
		m.mu.Unlock()
		return read.status, read.err
	}
}
