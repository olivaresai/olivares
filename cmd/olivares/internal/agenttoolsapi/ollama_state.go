// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package agenttoolsapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// RestartOllama starts the service again at engine start when a person started it and
// did not stop it: after a restart it stayed "Not running" until someone
// pressed Start, and the tools it serves failed meanwhile. The engine starts it as
// itself, for the tenant the person named; a failure is shown on the row.
func (m *Module) RestartOllama() {
	m.ollama.lifecycle.Lock()
	defer m.ollama.lifecycle.Unlock()
	m.mu.Lock()
	cfg := m.ollama.cfg
	m.mu.Unlock()
	if m.readOnly || cfg.StateFile == "" || cfg.ModelsDir == "" || cfg.HomeDir == "" || cfg.Audit == nil {
		return
	}
	was, ok, readErr := m.rememberedOllama()
	if readErr != nil {
		m.failRestart("Ollama was running before the engine restarted, but its start record cannot be read: " + readErr.Error() +
			" A container it started may still be serving on " + m.ollamaEndpoint() + "; stop Ollama to remove it.")
		return
	}
	if !ok {
		return
	}
	if was.CleanupOnly {
		m.failRestart("Ollama is stopped, but the engine could not prove that its container was removed; it may still be serving on " + m.ollamaEndpoint() + "; stop Ollama to retry the removal.")
		return
	}
	// A container the killed engine started belongs to the runtime's daemon, so it keeps
	// serving across the restart. When the restart fails before it can be removed, the
	// row must say the endpoint may still answer.
	fail := func(message string) {
		if was.Container {
			message += " A container it started may still be serving on " + m.ollamaEndpoint() + "; stop Ollama to remove it."
		}
		m.failRestart(message)
	}
	if was.Container && !sameOllamaDaemonEnvironment(was, cfg.Getenv) {
		fail("Ollama was running before the engine restarted, but its container connection changed; restore the original connection and stop that container before restarting")
		return
	}
	var tenant model.TenantID
	if was.TenantID != "" {
		var err error
		if tenant, err = model.ParseTenantID(was.TenantID); err != nil {
			fail("Ollama was running before the engine restarted, but its tenant cannot be read: " + err.Error())
			return
		}
	}
	program := ""
	var c OllamaContainer
	if was.Container {
		// The runtime and image the person started, not today's setting; a node whose
		// runtime or image is no longer usable says so on the row and starts nothing.
		resolved, err := m.resolveOllamaContainer(cfg, &was)
		if err != nil {
			fail("Ollama was running before the engine restarted and could not start again: " + err.Error())
			return
		}
		c = resolved
		if trustedOllamaRuntime(was.Runtime) && !sameRuntimeDaemon(c.Runtime, was.Runtime) {
			fail("Ollama was running before the engine restarted, but the runtime that created its container is unavailable; restore it before restarting")
			return
		}
	} else {
		program = m.program("ollama")
		if program == "" {
			m.failRestart("Ollama was running before the engine restarted, but the installed program is gone; install Ollama again.")
			return
		}
	}
	actor, err := auth.NewSystemOperator("engine start", "restart the Ollama a person started before the engine stopped")
	if err != nil {
		fail("Ollama was running before the engine restarted, but the engine could not act to start it again: " + err.Error())
		return
	}
	runtime := ollamaFlavour(was.Container)
	// The person's Start was audited (handleOllamaStart); this start is the engine's,
	// once per boot, and is recorded as such before it happens.
	reason := "restarted with the engine; started by " + was.StartedBy + " at " + was.StartedAt
	meta := map[string]any{"reason": reason, "register_in": was.TenantID, "endpoint": m.ollamaEndpoint(), "runtime": runtime}
	if was.Container {
		meta["image"] = c.Image
	}
	if err := cfg.Audit(m.ctx, model.AuditDraft{
		Actor: actor.Actor(), ActorKind: actor.ActorKind(), Action: "agenttools.ollama.restart", TargetKind: "agenttools.job",
		Meta: meta,
	}); err != nil {
		fail("Ollama was running before the engine restarted; it was not started again because its audit record could not be written.")
		return
	}
	if err := m.startOllama(program, actor, tenant, was.Container, c); err != nil {
		fail("Ollama was running before the engine restarted and could not start again: " + err.Error())
		return
	}
	if was.Container {
		// The record now names the runtime, image and mount mode that actually ran, so a
		// later Stop removes the container from the CLI that created it.
		was.Runtime, was.Image, was.SELinux = c.Runtime, c.Image, c.SELinux
		was.Daemon = ollamaDaemonFingerprint(cfg.Getenv)
		if err := m.writeRememberedOllama(was); err != nil {
			m.mu.Lock()
			m.ollama.message = appendNote(m.ollama.message, "Running, but the engine could not update the record of this start: "+err.Error())
			m.mu.Unlock()
		}
	}
}

// failRestart puts a failed automatic restart on the row: a service the person asked
// to survive an engine restart must not come back silently or stay silent about why
// it did not.
func (m *Module) failRestart(message string) {
	m.mu.Lock()
	m.ollama.state, m.ollama.runtime = ollamaFailed, ""
	m.ollama.message = message
	m.mu.Unlock()
}

// rememberOllama keeps the person's start in StateFile; forgetOllama drops it on Stop.
func (m *Module) rememberOllama(tenant model.TenantID, startedBy string, container bool, runtime, image string, selinux bool) error {
	m.mu.Lock()
	cfg := m.ollama.cfg
	m.mu.Unlock()
	if cfg.StateFile == "" {
		return nil
	}
	was := ollamaRemembered{StartedBy: startedBy, StartedAt: time.Now().UTC().Format(time.RFC3339), Container: container}
	if !tenant.IsZero() {
		was.TenantID = tenant.String()
	}
	if container {
		was.Runtime, was.Image, was.SELinux = runtime, image, selinux
		was.Daemon = ollamaDaemonFingerprint(cfg.Getenv)
	}
	return m.writeRememberedOllama(was)
}

// writeRememberedOllama replaces StateFile with was.
func (m *Module) writeRememberedOllama(was ollamaRemembered) error {
	m.mu.Lock()
	path := m.ollama.cfg.StateFile
	m.mu.Unlock()
	if path == "" {
		return nil
	}
	raw, err := json.Marshal(was)
	if err != nil {
		return fmt.Errorf("the start could not be kept: %w", err)
	}
	// A fresh node has no ollama directory yet: the record is written before the service
	// runs, so the writer creates its own directory instead of failing the first start.
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("the start could not be kept: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("the start could not be kept: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("the start could not be kept: %w", err)
	}
	return nil
}

// rememberedOllama reads the start StateFile keeps. A missing file is no remembered
// start; any other failure is returned, because a start that cannot be read may still
// name a running container, and treating it as absent would report a stop that did not
// happen.
func (m *Module) rememberedOllama() (ollamaRemembered, bool, error) {
	m.mu.Lock()
	path := m.ollama.cfg.StateFile
	m.mu.Unlock()
	if path == "" {
		return ollamaRemembered{}, false, nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ollamaRemembered{}, false, nil
	}
	if err != nil {
		return ollamaRemembered{}, false, fmt.Errorf("the start record cannot be read: %w", err)
	}
	var was ollamaRemembered
	if err := json.Unmarshal(raw, &was); err != nil {
		return ollamaRemembered{}, false, fmt.Errorf("the start record cannot be read: %w", err)
	}
	return was, true, nil
}

func (m *Module) forgetOllama() error {
	m.mu.Lock()
	path := m.ollama.cfg.StateFile
	m.mu.Unlock()
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("the start could not be forgotten: %w", err)
	}
	return nil
}
