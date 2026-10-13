// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"crypto/sha256"
	"errors"
	"github.com/olivaresai/olivares/core/model"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoredWorkspaceVersionStartsTheSelectedRealChild(t *testing.T) {
	old, latest := writePrintFormPeer(t), writePrintFormPeer(t)
	body, err := os.ReadFile(old)
	if err != nil {
		t.Fatal(err)
	}
	body = append(body, []byte("\n# version 0.1\n")...)
	if err := os.WriteFile(old, body, 0755); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(journeyRunner(t)), WithCredentialSource(staticCred()), WithStopWaitDelay(2*time.Second), WithProgramResolver(func(string) string { return latest }, func(_ context.Context, driver, version string) (string, error) {
		if driver != "claude" || version != "0.1" {
			t.Fatalf("version lookup = %s %s", driver, version)
		}
		return old, nil
	}))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "pinned-launch")
	workspace := h.doJSON("POST", "/v1/m/sessions/workspaces", admin, map[string]any{"root_path": filepath.Dir(old), "name": "Pinned"}, tenantHdr(tenant))
	if workspace.code != http.StatusCreated {
		t.Fatalf("workspace = %s", workspace.raw)
	}
	ref := workspace.body["workspace_ref"].(string)
	if err := m.SetToolVersionPin(t.Context(), tenant, ToolVersionPin{Driver: "claude", WorkspaceRef: ref, Version: "0.1"}, "admin", model.ActorUser); err != nil {
		t.Fatal(err)
	}
	run := h.doJSON("POST", "/v1/m/sessions/runs", admin, map[string]any{"workspace_ref": ref, "provider_profile_ref": ensureRuntimeTestProfileRef(t, m, tenant), "isolation": "native"}, tenantHdr(tenant))
	if run.code != http.StatusCreated {
		t.Fatalf("run = %d %s", run.code, run.raw)
	}
	runRef := run.body["run_ref"].(string)
	lr, ok := m.rt.getLive(tenant, runRef)
	if !ok || lr.program != old {
		t.Fatalf("started program = %+v, want %s", lr, old)
	}
	started, err := os.ReadFile(lr.program)
	if err != nil || sha256.Sum256(started) != digest {
		t.Fatalf("started binary digest differs: %v", err)
	}
	if _, err := m.LockUnusedTool(old); err == nil {
		t.Fatal("running release admitted for removal")
	}
	stopped := h.doJSON("POST", "/v1/m/sessions/runs/"+runRef+"/stop", admin, nil, tenantHdr(tenant))
	if stopped.code != http.StatusOK {
		t.Fatalf("stop = %s", stopped.raw)
	}
	release, err := m.LockUnusedTool(old)
	if err != nil {
		t.Fatal(err)
	}
	release()
	resumed := h.doJSON("POST", "/v1/m/sessions/runs/"+runRef+"/resume", admin, nil, tenantHdr(tenant))
	if resumed.code != http.StatusOK {
		t.Fatalf("resume = %d %s", resumed.code, resumed.raw)
	}
	lr, ok = m.rt.getLive(tenant, runRef)
	if !ok || lr.program != old {
		t.Fatalf("resumed program did not retain the workspace pin")
	}
	if unlock, err := m.LockUnusedTool(old); !errors.Is(err, ErrToolInUse) {
		if unlock != nil {
			unlock()
		}
		t.Fatalf("resumed release admitted: %v", err)
	}
	if stopped := h.doJSON("POST", "/v1/m/sessions/runs/"+runRef+"/stop", admin, nil, tenantHdr(tenant)); stopped.code != http.StatusOK {
		t.Fatalf("resumed stop = %s", stopped.raw)
	}
	t.Logf("started pinned child sha256=%x; removal refused while live and admitted after stop", digest)
}

func TestToolRemovalRejectsRetargetedLiveOverride(t *testing.T) {
	old, newer := writePrintFormPeer(t), writePrintFormPeer(t)
	alias := filepath.Join(t.TempDir(), "override")
	if err := os.Symlink(old, alias); err != nil {
		t.Fatal(err)
	}
	m := New(WithSessionWorkspaceRoot(t.TempDir()), WithRunner(journeyRunner(t)), WithCredentialSource(staticCred()), WithProgram(alias), WithStopWaitDelay(2*time.Second))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "alias-removal")
	launchHTTPRun(t, h, admin, tenant, map[string]any{"isolation": "native"})
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(newer, alias); err != nil {
		t.Fatal(err)
	}
	if unlock, err := m.LockUnusedTool(old); !errors.Is(err, ErrToolInUse) {
		if unlock != nil {
			unlock()
		}
		t.Fatalf("retargeted live executable admitted: %v", err)
	}
}
