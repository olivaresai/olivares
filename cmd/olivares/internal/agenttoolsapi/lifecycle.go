// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package agenttoolsapi

import (
	"errors"
	"net/http"

	"github.com/olivaresai/olivares/cmd/olivares/internal/toolinstall"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/modules/sessions"
)

// SetSessions wires the product's pin store and the same live registry that launches children.
func (m *Module) SetSessions(s *sessions.Module) { m.sessions = s }

func (m *Module) lifecycleWritable(w http.ResponseWriter) bool {
	if m.readOnly {
		fail(w, 409, "read_only", "Tool changes are disabled on this read-only control plane.")
		return false
	}
	if m.sessions == nil {
		fail(w, 503, "unavailable", "The session runtime is unavailable. No tool change was made.")
		return false
	}
	return true
}

// handleRemove deletes one verified managed host release after checking live sessions.
func (m *Module) handleRemove(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.guard(true, m.remove)(w, r, mc)
}

func (m *Module) remove(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if !m.lifecycleWritable(w) {
		return
	}
	var in struct {
		Driver  string `json:"driver"`
		Version string `json:"version"`
	}
	if !decode(w, r, &in) {
		return
	}
	meta := map[string]any{"driver": in.Driver, "version": in.Version}
	if err := audit(r.Context(), mc, "remove.requested", "", meta); err != nil {
		fail(w, 503, "unavailable", "Removal audit is unavailable. No release was removed.")
		return
	}
	admissionAuditFailed := false
	removed, err := m.engine.Remove(r.Context(), m.root, in.Driver, in.Version, func(release toolinstall.Installed) (func(), error) {
		unlock, err := m.sessions.LockUnusedTool(release.Executable)
		if err != nil {
			return nil, err
		}
		meta["sha256"] = release.SHA256
		if err := audit(r.Context(), mc, "remove.approved", "", meta); err != nil {
			admissionAuditFailed = true
			unlock()
			return nil, err
		}
		return unlock, nil
	})
	if err != nil {
		if admissionAuditFailed {
			fail(w, 503, "unavailable", "Removal audit is unavailable. No release was removed.")
			return
		}
		if errors.Is(err, sessions.ErrToolInUse) {
			fail(w, 409, "tool_in_use", err.Error())
			return
		}
		installerError(w, err)
		return
	}
	m.forgetProviders(in.Driver)
	meta["sha256"] = removed.SHA256
	if err := audit(r.Context(), mc, "remove.completed", "", meta); err != nil {
		fail(w, 503, "audit_failed", "The release was removed, but its completion audit could not be retained. The approval audit remains.")
		return
	}
	write(w, 200, map[string]any{"ok": true, "driver": removed.Driver, "version": removed.Version, "sha256": removed.SHA256})
}

// handlePins lists the selected organization's stored session tool versions.
func (m *Module) handlePins(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.guard(false, func(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
		tenant, ok := tenantOf(r.URL.Query().Get("tenant_id"))
		if !ok {
			fail(w, 400, "bad_request", "Choose an organization.")
			return
		}
		if m.sessions == nil {
			fail(w, 503, "unavailable", "Tool version storage is unavailable.")
			return
		}
		pins, err := m.sessions.ToolVersionPins(r.Context(), tenant)
		if err != nil {
			fail(w, 503, "unavailable", "Stored tool versions could not be read.")
			return
		}
		write(w, 200, map[string]any{"pins": pins})
	})(w, r, mc)
}

// handlePin saves or clears a version selection for a tenant's workspace or agent.
func (m *Module) handlePin(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	m.guard(true, m.pin)(w, r, mc)
}

func (m *Module) pin(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if !m.lifecycleWritable(w) {
		return
	}
	var in struct {
		TenantID string `json:"tenant_id"`
		sessions.ToolVersionPin
	}
	if !decode(w, r, &in) {
		return
	}
	tenant, ok := tenantOf(in.TenantID)
	if !ok {
		fail(w, 400, "bad_request", "Choose an organization.")
		return
	}
	// The install lock keeps an accepted pin from racing release removal.
	unlock, err := toolinstall.LockRoot(m.root)
	if err != nil {
		installerError(w, err)
		return
	}
	defer unlock()
	if in.Version != "" {
		if _, err := m.engine.InstalledVersion(r.Context(), m.root, in.Driver, in.Version); err != nil {
			installerError(w, err)
			return
		}
	}
	if err := m.sessions.SetToolVersionPin(r.Context(), tenant, in.ToolVersionPin, mc.Principal.Actor(), mc.Principal.ActorKind()); err != nil {
		fail(w, 422, "tool_pin_failed", err.Error())
		return
	}
	write(w, 200, map[string]any{"ok": true})
}
