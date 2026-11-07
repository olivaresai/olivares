// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package agenttoolsapi

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// statusLifetime is how long a read answer stands before it is read again behind a caller.
const statusLifetime = 30 * time.Second

type statusSelection struct {
	tenant                            model.TenantID
	driver, accountRef, program, home string
	// serverHome: the engine user's login read as a governed session reads it, and the
	// same login read with its settings on, are two answers.
	serverHome string
}

type statusRead struct {
	program, auth os.FileInfo
	done          chan struct{}
	status        SignInStatus
	err           error
	callerEnded   bool
	expires       time.Time
	refreshing    bool // an expired answer is being read again behind its callers
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
// invalidate the result, with a short lifetime for changes outside these files. An
// answer past that lifetime still answers at once while the files are as they were,
// and the tool is asked again behind it, so a page does not wait for a cold start.
// ponytail: a login the tool ended without touching its credential file reads as signed
// in once more after a pause; add a fresh-read flag for the launch path if that shows.
func (m *Module) readCachedStatus(ctx context.Context, tenant model.TenantID, out SignInStatus, program, configDir string, env []string) (SignInStatus, error) {
	for {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		key := statusSelection{tenant, out.Driver, out.AccountRef, program, envValue(env, "HOME"), out.serverHome}
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
			dataHome := envValue(env, "XDG_DATA_HOME")
			if dataHome == "" {
				dataHome = filepath.Join(key.home, ".local", "share")
			}
			authPath = filepath.Join(dataHome, "opencode", "auth.json")
		default:
			return m.readNativeStatus(ctx, out, program, configDir, env)
		}
		authInfo, err := os.Stat(authPath)
		if err != nil && !os.IsNotExist(err) {
			return out, err
		}
		// Without its credential file OpenCode has no native login to list.
		// Avoid its cold runtime startup; existing files still need auth list.
		if out.Driver == "opencode" && os.IsNotExist(err) {
			m.mu.Lock()
			delete(m.statusReads, key)
			// A missing login does not erase a failure already found by a probe.
			var probeErr error
			for entryKey, entry := range m.providerEntries {
				command, cause, _ := strings.Cut(entry.snap.Error, ": ")
				// A version failure belongs to the executable, across login homes.
				if entry.snap.Error != "" && (entryKey == providerKey(out.Driver, configDir) ||
					entry.snap.Driver == out.Driver && command == cmdLine(program, "--version")) {
					probeErr = &probeError{command, errors.New(cause)}
					break
				}
			}
			m.mu.Unlock()
			return out, probeErr
		}
		m.mu.Lock()
		prev := m.statusReads[key]
		same := prev != nil && sameStatusFile(programInfo, prev.program) && sameStatusFile(authInfo, prev.auth)
		if same && !time.Now().Before(prev.expires) {
			select {
			case <-prev.done:
				// Only a finished, usable answer is served late; a failed one was dropped already.
				if prev.err == nil {
					if !prev.refreshing {
						prev.refreshing = true
						go m.refreshStatus(context.WithoutCancel(ctx), key, prev, out, program, configDir, env)
					}
					m.mu.Unlock()
					return prev.status, nil
				}
			default:
			}
		}
		if read := prev; same && time.Now().Before(read.expires) {
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
		read := &statusRead{program: programInfo, auth: authInfo, done: make(chan struct{}), expires: time.Now().Add(statusLifetime)}
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

// refreshStatus asks the tool again for an answer that was served past its lifetime and
// keeps the result only while that answer is still the one in the cache: a sign-in, a
// sign-out or an install in between cleared it, and its result is no longer the truth.
func (m *Module) refreshStatus(ctx context.Context, key statusSelection, old *statusRead, out SignInStatus, program, configDir string, env []string) {
	// Outside the request's own recovery: a panic here would end the engine.
	defer func() {
		if p := recover(); p != nil {
			slog.Error("tool sign-in status refresh panicked", "driver", out.Driver, "panic", p, "stack", string(debug.Stack()))
		}
	}()
	status, err := m.readNativeStatus(ctx, out, program, configDir, env)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.statusReads[key] != old {
		return
	}
	if err != nil {
		delete(m.statusReads, key) // the next read asks again and says what failed
		return
	}
	done := make(chan struct{})
	close(done)
	m.statusReads[key] = &statusRead{program: old.program, auth: old.auth, done: done, status: status, expires: time.Now().Add(statusLifetime)}
}
