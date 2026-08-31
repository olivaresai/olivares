// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

func writeCodexPEPConfig(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "codexpep.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("OLIVARES_CODEX_HOOK_PEP_CONFIG", path)
}

// TestCodexPEPMountsNothingWhenUnset is the safe default: an un-provisioned node serves no
// Codex enforcement surface at all, rather than an endpoint whose behavior nobody declared.
func TestCodexPEPMountsNothingWhenUnset(t *testing.T) {
	t.Setenv("OLIVARES_CODEX_HOOK_PEP_CONFIG", "")
	srv, err := buildCodexHookPEPServer(&engine{}, sessions.New(), discardLog())
	if err != nil {
		t.Fatalf("an unset config must not error: %v", err)
	}
	if srv != nil {
		t.Error("an unset config must mount nothing")
	}
}

// TestCodexPEPFailsClosedOnABrokenTenant: a governed endpoint whose decisions would be
// filed nowhere sensible is worse than no endpoint, so it refuses to start.
func TestCodexPEPFailsClosedOnABrokenTenant(t *testing.T) {
	writeCodexPEPConfig(t, `{"tenant":"not-a-tenant-id"}`)
	if _, err := buildCodexHookPEPServer(&engine{}, sessions.New(), discardLog()); err == nil {
		t.Error("an invalid tenant must fail startup, not mount a surface that cannot attribute")
	}
}

// TestCodexPEPRefusesWithoutTheIdentityPlane: without modules/sessions the decider cannot
// resolve a Codex session id to anything, so every decision would be unattributable.
// Mounting anyway would produce governed-looking denials with no session behind them.
func TestCodexPEPRefusesWithoutTheIdentityPlane(t *testing.T) {
	writeCodexPEPConfig(t, `{"tenant":"`+model.NewID().String()+`"}`)
	_, err := buildCodexHookPEPServer(&engine{}, nil, discardLog())
	if err == nil {
		t.Fatal("a missing identity plane must refuse the mount")
	}
	if !strings.Contains(err.Error(), "identity plane") {
		t.Errorf("the refusal must say why, got %q", err)
	}
}

// TestCodexPEPUsesItsOwnSocket pins the separation from the Claude PEP. Sharing one socket
// would let a misconfigured hooks.json receive an answer in a shape Codex ignores.
func TestCodexPEPUsesItsOwnSocket(t *testing.T) {
	if defaultCodexHookPEPListen == defaultHookPEPListen {
		t.Fatal("the Codex and Claude PEPs must not default to the same socket: they speak different wire dialects")
	}
	writeCodexPEPConfig(t, `{"tenant":"`+model.NewID().String()+`"}`)
	srv, err := buildCodexHookPEPServer(&engine{}, sessions.New(), discardLog())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if srv == nil || srv.Addr != defaultCodexHookPEPListen {
		t.Fatalf("expected the default Codex socket, got %+v", srv)
	}
	if srv.ReadHeaderTimeout == 0 {
		t.Error("a listening governed surface must bound its header read")
	}
}
