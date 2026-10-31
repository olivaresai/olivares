// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/olivaresai/olivares/core/driverfacts"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// ToolVersionPin selects a managed release for a registered folder or agent.
// An empty version clears that selection. Agent selections outrank folder selections.
type ToolVersionPin struct {
	Driver       string `json:"driver"`
	WorkspaceRef string `json:"workspace_ref,omitempty"`
	AgentRef     string `json:"agent_ref,omitempty"`
	Version      string `json:"version"`
}

func toolPins(settings map[string]any) ([]ToolVersionPin, error) {
	pins := []ToolVersionPin{}
	v, ok := settings["tool_versions"]
	if !ok {
		return pins, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if string(b) == "null" {
		return nil, errors.New("tool version settings are invalid")
	}
	if err := json.Unmarshal(b, &pins); err != nil {
		return nil, fmt.Errorf("tool version settings are invalid: %w", err)
	}
	for _, p := range pins {
		if p.Driver == "" || p.Version == "" || (p.WorkspaceRef == "" && p.AgentRef == "") {
			return nil, errors.New("tool version settings are invalid")
		}
	}
	return pins, nil
}

func (m *Module) ToolVersionPins(ctx context.Context, tenant model.TenantID) ([]ToolVersionPin, error) {
	var pins []ToolVersionPin
	if m.Data == nil {
		return nil, errors.New("tool version store unavailable")
	}
	err := m.Data.View(ctx, tenant, func(sc store.Scope) error {
		org, err := sc.Org(ctx)
		if err != nil {
			return err
		}
		pins, err = toolPins(org.Settings)
		return err
	})
	return pins, err
}

func (m *Module) SetToolVersionPin(ctx context.Context, tenant model.TenantID, pin ToolVersionPin, actor, actorKind string) error {
	facts, ok := driverfacts.Lookup(pin.Driver)
	if !ok || !facts.Session || (pin.WorkspaceRef == "" && pin.AgentRef == "") || len(pin.Version) > 128 || strings.ContainsAny(pin.Version, "/\\\x00") {
		return badRequest("Choose a session tool, a registered workspace or agent, and an installed version.")
	}
	if m.Data == nil {
		return errors.New("tool version store unavailable")
	}
	return m.Data.Mutate(ctx, tenant, func(sc store.Scope) error {
		if pin.WorkspaceRef != "" {
			repo, err := sc.Ext(workspaceKind)
			if err != nil {
				return err
			}
			if _, err := findWorkspaceRec(ctx, repo, pin.WorkspaceRef); err != nil {
				return err
			}
		}
		if pin.AgentRef != "" {
			id, err := model.ParseID(pin.AgentRef)
			if err != nil {
				return badRequest("Choose an agent in this organization.")
			}
			agent, err := sc.Agents().Get(ctx, id)
			if err != nil {
				return err
			}
			if agent.Status != model.StatusActive {
				return badRequest("Choose an active agent.")
			}
		}
		org, err := sc.Org(ctx)
		if err != nil {
			return err
		}
		pins, err := toolPins(org.Settings)
		if err != nil {
			return err
		}
		next := make([]ToolVersionPin, 0, len(pins)+1)
		for _, old := range pins {
			if old.Driver != pin.Driver || old.WorkspaceRef != pin.WorkspaceRef || old.AgentRef != pin.AgentRef {
				next = append(next, old)
			}
		}
		if pin.Version != "" {
			next = append(next, pin)
		}
		if org.Settings == nil {
			org.Settings = map[string]any{}
		}
		org.Settings["tool_versions"] = next
		if _, err := sc.SetOrgSettings(ctx, org.Settings, org.Version); err != nil {
			return err
		}
		action := "sessions.tool.version_pinned"
		if pin.Version == "" {
			action = "sessions.tool.version_unpinned"
		}
		ev, err := sc.Audit().Append(ctx, model.AuditDraft{Actor: actor, ActorKind: actorKind, Action: action, TargetKind: "sessions.tool", Meta: map[string]any{"driver": pin.Driver, "version": pin.Version, "workspace_ref": pin.WorkspaceRef, "agent_ref": pin.AgentRef}})
		if err == nil && ev.Seq == 0 {
			return errors.New("tool version audit was not retained")
		}
		return err
	})
}

func (m *Module) ToolVersion(ctx context.Context, tenant model.TenantID, driver, workspace, agent string) (string, error) {
	// Business: a candidate ring gives its group's members the version under trial,
	// whatever pin they would otherwise resolve. Community's hook returns "".
	if version, err := m.ringToolVersion(ctx, tenant, driver, workspace, agent); err != nil || version != "" {
		return version, err
	}
	pins, err := m.ToolVersionPins(ctx, tenant)
	if err != nil {
		return "", err
	}
	var folder, own, scoped string
	for _, p := range pins {
		if p.Driver != driver {
			continue
		}
		if p.AgentRef == "" && p.WorkspaceRef == workspace && workspace != "" {
			folder = p.Version
		}
		if agent != "" && p.AgentRef == agent && (p.WorkspaceRef == "" || p.WorkspaceRef == workspace) {
			if p.WorkspaceRef != "" {
				scoped = p.Version
			} else {
				own = p.Version
			}
		}
	}
	if scoped != "" {
		return scoped, nil
	}
	if own != "" {
		return own, nil
	}
	return folder, nil
}

func (m *Module) explicitToolProgram(driver string) string {
	if driver == providerDriverClaude && m.rt.programPinned {
		return m.rt.program
	}
	return m.rt.driverPrograms[driver]
}

func (m *Module) versionProgram(ctx context.Context, driver, version string) (string, error) {
	if p := m.explicitToolProgram(driver); p != "" {
		return p, nil
	}
	if m.rt.VersionProgramResolver == nil {
		return "", errors.New("pinned tool version resolver is unavailable")
	}
	p, err := m.rt.VersionProgramResolver(ctx, driver, version)
	if err == nil && p == "" {
		err = errors.New("pinned tool version is not installed; install it or clear the version pin")
	}
	return p, err
}

func (m *Module) resolveRunProgram(ctx context.Context, tenant model.TenantID, p CreateRunParams) (string, error) {
	driver := launchDriverKey(p)
	if override := m.explicitToolProgram(driver); override != "" {
		return override, nil
	}
	// Standalone integrations keep their existing resolver contract. The engine wires both.
	if m.rt.VersionProgramResolver != nil {
		agent := p.AgentRef
		if agent == "" && p.ActorKind == model.ActorAgent {
			agent = p.Actor
		}
		version, err := m.ToolVersion(ctx, tenant, driver, p.WorkspaceRef, agent)
		if err != nil {
			m.warnf("sessions: stored tool version selection could not be read", "tenant", tenant.String(), "driver", driver, "error", redactErr(err))
			return "", &runErr{503, "Stored tool version selection could not be read; repair the organization settings before starting this session"}
		}
		if version != "" {
			program, err := m.versionProgram(ctx, driver, version)
			if err != nil {
				return "", badRequest(fmt.Sprintf("Pinned %s version %s is unavailable; install it from AI tools or clear the workspace or agent version selection", driver, version))
			}
			return program, nil
		}
	}
	if d, ok := m.driverFor(driver); ok {
		program := m.driverProgram(d)
		if program == "" {
			name, exe := driver, d.DefaultProgram()
			if facts, ok := driverfacts.Lookup(driver); ok {
				name, exe = facts.Name, facts.Program
			}
			return "", &runErr{503, name + " is not installed on this node; install it from AI tools or pin its official " + exe + " executable"}
		}
		return program, nil
	}
	return m.claudeProgram(), nil
}

var ErrToolInUse = errors.New("tool_in_use")

// LockUnusedTool holds launch admission until the caller finishes removing a release.
// Retained attach tails are safe only after the runtime confirms collection.
func (m *Module) LockUnusedTool(program string) (func(), error) {
	if !m.rt.toolMu.TryLock() {
		return nil, fmt.Errorf("%w: a tool operation is active or its child collection is unconfirmed", ErrToolInUse)
	}
	m.rt.mu.Lock()
	live := make([]*liveRun, 0, len(m.rt.live))
	for _, lr := range m.rt.live {
		live = append(live, lr)
	}
	m.rt.mu.Unlock()
	for _, lr := range live {
		if !lr.collectionConfirmed() && (sameToolProgram(lr.program, program) || sameToolIdentity(lr.programIdentity, program)) {
			m.rt.toolMu.Unlock()
			return nil, fmt.Errorf("%w: session %s uses this release", ErrToolInUse, lr.runRef)
		}
	}
	return m.rt.toolMu.Unlock, nil
}

func sameToolProgram(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if path, err := exec.LookPath(a); err == nil {
		a = path
	}
	if path, err := exec.LookPath(b); err == nil {
		b = path
	}
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	pa, ea := filepath.EvalSymlinks(a)
	pb, eb := filepath.EvalSymlinks(b)
	if ea == nil && eb == nil && pa == pb {
		return true
	}
	fa, ea := os.Stat(a)
	fb, eb := os.Stat(b)
	return ea == nil && eb == nil && os.SameFile(fa, fb)
}

// Freeze aliases before exec: PATH and symlink changes must not rewrite the
// identity of a child already started from an older managed release.
func toolProgramIdentity(program string) (string, fs.FileInfo) {
	path, err := exec.LookPath(program)
	if err != nil {
		return program, nil
	}
	if canonical, err := filepath.EvalSymlinks(path); err == nil {
		path = canonical
	}
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	identity, _ := os.Stat(path)
	return path, identity
}

func sameToolIdentity(identity fs.FileInfo, program string) bool {
	if identity == nil {
		return false
	}
	_, candidate := toolProgramIdentity(program)
	return candidate != nil && os.SameFile(identity, candidate)
}
