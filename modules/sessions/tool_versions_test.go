// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"fmt"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestToolRemovalNamesLiveSessionAndAllowsAfterCollection(t *testing.T) {
	m := New()
	lr := &liveRun{tenant: model.SystemTenantID, runRef: "run-in-use", program: "/tools/claude/old/claude"}
	m.rt.putLive(lr)
	release, err := m.LockUnusedTool(lr.program)
	if err == nil || release != nil || err.Error() != "tool_in_use: session run-in-use uses this release" {
		t.Fatalf("removal = %v", err)
	}
	lr.mu.Lock()
	lr.finalized, lr.processReaped = true, true
	lr.mu.Unlock()
	release, err = m.LockUnusedTool(lr.program)
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestStoredToolPinsAreScopedAndPreserveTenantSettings(t *testing.T) {
	for _, be := range profileBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			m, st := openProfileModule(t, be, nil)
			defer st.Close()
			ctx := context.Background()
			tenant := ensureTenant(t, st, "pins")
			other := ensureTenant(t, st, "other-pins")
			ws, err := m.createWorkspace(ctx, tenant, CreateWorkspaceParams{Name: "Pinned folder", RootPath: t.TempDir(), Actor: "admin", ActorKind: model.ActorUser})
			if err != nil {
				t.Fatal(err)
			}
			var agent model.Agent
			err = st.Mutate(ctx, tenant, func(sc store.Scope) error {
				_, err := sc.SetOrgSettings(ctx, map[string]any{"unrelated": "keep"})
				if err != nil {
					return err
				}
				agent, err = sc.Agents().Create(ctx, model.Agent{Name: "Pinned agent", ExternalID: "agent-a", Kind: "codex", Status: model.StatusActive})
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			pin := ToolVersionPin{Driver: "codex", WorkspaceRef: ws.WorkspaceRef, Version: "0.1"}
			if err := m.SetToolVersionPin(ctx, tenant, pin, "admin", model.ActorUser); err != nil {
				t.Fatal(err)
			}
			if got, err := m.ToolVersion(ctx, tenant, "codex", ws.WorkspaceRef, ""); err != nil || got != "0.1" {
				t.Fatalf("workspace pin = %q %v", got, err)
			}
			if got, err := m.ToolVersion(ctx, other, "codex", ws.WorkspaceRef, ""); err != nil || got != "" {
				t.Fatalf("cross-tenant pin = %q %v", got, err)
			}
			pin.AgentRef = string(agent.ID)
			pin.Version = "0.2"
			if err := m.SetToolVersionPin(ctx, tenant, pin, "admin", model.ActorUser); err != nil {
				t.Fatal(err)
			}
			if got, err := m.ToolVersion(ctx, tenant, "codex", ws.WorkspaceRef, string(agent.ID)); err != nil || got != "0.2" {
				t.Fatalf("agent override = %q %v", got, err)
			}
			pin.Version = ""
			if err := m.SetToolVersionPin(ctx, tenant, pin, "admin", model.ActorUser); err != nil {
				t.Fatal(err)
			}
			if got, err := m.ToolVersion(ctx, tenant, "codex", ws.WorkspaceRef, string(agent.ID)); err != nil || got != "0.1" {
				t.Fatalf("clear pin = %q %v", got, err)
			}
			if err := m.SetToolVersionPin(ctx, other, pin, "admin", model.ActorUser); err == nil {
				t.Fatal("cross-tenant target accepted")
			}
			if err := st.View(ctx, tenant, func(sc store.Scope) error {
				org, err := sc.Org(ctx)
				if err == nil && org.Settings["unrelated"] != "keep" {
					t.Fatal("pin replaced unrelated settings")
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
				org, err := sc.Org(ctx)
				if err != nil {
					return err
				}
				org.Settings["tool_versions"] = "corrupt"
				_, err = sc.SetOrgSettings(ctx, org.Settings)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := m.ToolVersion(ctx, tenant, "codex", ws.WorkspaceRef, ""); err == nil {
				t.Fatal("corrupt pin state downgraded to default")
			}
		})
	}
}

func TestVersionResolverRefusesUnavailablePinAndHonorsOverride(t *testing.T) {
	unavailable := errors.New("pinned release is not installed")
	m := New(WithProviderDriver(NewCodexDriver()), WithProgramResolver(func(string) string { return "/latest/codex" }, func(context.Context, string, string) (string, error) { return "", unavailable }))
	if _, err := m.versionProgram(context.Background(), "codex", "0.1"); !errors.Is(err, unavailable) {
		t.Fatalf("pin downgraded: %v", err)
	}
	override := New(WithProviderDriver(NewCodexDriver()), WithDriverProgram("codex", "/override/codex"), WithProgramResolver(nil, func(context.Context, string, string) (string, error) {
		t.Fatal("override consulted stored pin")
		return "", unavailable
	}))
	if got, err := override.versionProgram(context.Background(), "codex", "0.1"); err != nil || got != "/override/codex" {
		t.Fatalf("override = %q %v", got, err)
	}
}

func TestToolRemovalRecognizesExecutableAliases(t *testing.T) {
	root := t.TempDir()
	original := filepath.Join(root, "tool")
	if err := os.WriteFile(original, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(original, alias); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(root, "linked")
	if err := os.Link(original, linked); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root)
	for _, path := range []string{alias, linked, "tool"} {
		m := New()
		m.rt.putLive(&liveRun{tenant: model.SystemTenantID, runRef: "alias-use", program: path})
		if unlock, err := m.LockUnusedTool(original); !errors.Is(err, ErrToolInUse) {
			if unlock != nil {
				unlock()
			}
			t.Fatalf("alias %s was admitted: %v", path, err)
		}
	}
}

type pinAuditFaultData struct {
	api.ModuleData
	dropped bool
}
type pinAuditFaultScope struct {
	store.Scope
	dropped bool
}
type pinAuditFaultLog struct {
	store.AuditLog
	dropped bool
}

func (s pinAuditFaultScope) Audit() store.AuditLog {
	return pinAuditFaultLog{s.Scope.Audit(), s.dropped}
}
func (a pinAuditFaultLog) Append(ctx context.Context, draft model.AuditDraft) (model.AuditEvent, error) {
	if strings.HasPrefix(draft.Action, "sessions.tool.") {
		if a.dropped {
			return model.AuditEvent{}, nil
		}
		return model.AuditEvent{}, errors.New("synthetic pin audit failure")
	}
	return a.AuditLog.Append(ctx, draft)
}
func (d pinAuditFaultData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.ModuleData.Mutate(ctx, tenant, func(sc store.Scope) error { return fn(pinAuditFaultScope{sc, d.dropped}) })
}
func TestToolPinRollsBackWhenAuditFailsOrIsDropped(t *testing.T) {
	for _, dropped := range []bool{false, true} {
		m := New()
		h := newHarness(t, m)
		tenant := h.createOrg(h.adminLogin(), fmt.Sprintf("pin-audit-%v", dropped))
		ws, err := m.createWorkspace(t.Context(), tenant, CreateWorkspaceParams{Name: "Audit", RootPath: t.TempDir(), Actor: "admin", ActorKind: model.ActorUser})
		if err != nil {
			t.Fatal(err)
		}
		pin := ToolVersionPin{Driver: "claude", WorkspaceRef: ws.WorkspaceRef, Version: "2.1.261"}
		data := m.Data
		m.Data = pinAuditFaultData{data, dropped}
		if err := m.SetToolVersionPin(t.Context(), tenant, pin, "admin", model.ActorUser); err == nil {
			t.Fatal("missing audit admitted")
		}
		m.Data = data
		if got, err := m.ToolVersion(t.Context(), tenant, "claude", ws.WorkspaceRef, ""); err != nil || got != "" {
			t.Fatalf("failed audit retained pin: %q %v", got, err)
		}
	}
}

// Pause a real launch decision after executable selection but before registration.
// Removal cannot slip through the empty live registry in that interval.
type toolAdmissionRunner struct {
	Runner
	entered, release chan struct{}
}

func (r *toolAdmissionRunner) Launch(ctx context.Context, spec LaunchSpec) (Process, error) {
	close(r.entered)
	select {
	case <-r.release:
		return r.Runner.Launch(ctx, spec)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func TestToolRemovalRefusesDuringLaunchRegistration(t *testing.T) {
	runner := &toolAdmissionRunner{Runner: &fakeRunner{initSID: "pin-admission"}, entered: make(chan struct{}), release: make(chan struct{})}
	m := New(WithRunner(runner), WithProgram("/selected/claude"), WithSessionWorkspaceRoot(t.TempDir()), WithCredentialSource(staticCred()))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "tool-admission")
	profile := ensureRuntimeTestProfileRef(t, m, tenant)
	started := make(chan error, 1)
	go func() {
		_, err := m.createRun(t.Context(), tenant, CreateRunParams{ProviderProfileRef: profile, Isolation: IsolationNative, Transport: TransportStreamJSON, Actor: "admin", ActorKind: model.ActorUser})
		started <- err
	}()
	select {
	case <-runner.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("launch did not reach runner")
	}
	removed := make(chan error, 1)
	go func() {
		unlock, err := m.LockUnusedTool("/selected/claude")
		if unlock != nil {
			unlock()
		}
		removed <- err
	}()
	select {
	case err := <-removed:
		if !errors.Is(err, ErrToolInUse) {
			t.Errorf("removal passed launch admission: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("removal queued behind an active launch")
	}
	close(runner.release)
	if err := <-started; err != nil {
		t.Fatal(err)
	}
	unlock, err := m.LockUnusedTool("/selected/claude")
	if unlock != nil {
		unlock()
	}
	if !errors.Is(err, ErrToolInUse) {
		t.Fatalf("registered launch not retained: %v", err)
	}
}
