// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

type recordsHoldGate struct {
	held    bool
	heldRef string
	err     error
	kind    string
	ref     string
}

func (g *recordsHoldGate) Check(_ context.Context, _ model.TenantID, kind, ref string) (bool, error) {
	g.kind, g.ref = kind, ref
	return g.held && (g.heldRef == "" || g.heldRef == ref), g.err
}

// A folder's records class is a legal hold and a retention period. Either one
// refuses the file edits that destroy bytes (overwrite, move, delete) with a coded
// 423; creating a file destroys nothing and stays allowed; a hold that cannot be
// ruled out refuses too (503).
func TestFolderRecordsRefuseDestruction(t *testing.T) {
	gate := &recordsHoldGate{}
	h := newHarness(t, New(WithHoldGate(gate)))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "records")
	root := t.TempDir()
	created := h.doJSON("POST", "/v1/m/sessions/workspaces", admin, map[string]any{"root_path": root}, tenantHdr(tenant))
	if created.code != http.StatusCreated {
		t.Fatalf("register=%d %s", created.code, created.raw)
	}
	ref := created.body["workspace_ref"].(string)
	base := "/v1/m/sessions/workspaces/" + ref
	file := func(name string) string {
		t.Helper()
		p := filepath.Join(root, name)
		if err := os.WriteFile(p, []byte("page"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	call := func(method, path string, body any) resp {
		if body != nil {
			return h.doJSON(method, base+path, admin, body, tenantHdr(tenant))
		}
		return h.do(method, base+path, admin, tenantHdr(tenant))
	}
	code := func(r resp) any {
		e, _ := r.body["error"].(map[string]any)
		return e["code"]
	}
	refused := func(r resp, want int, wantCode string) {
		t.Helper()
		if r.code != want || code(r) != wantCode {
			t.Fatalf("got %d %s, want %d %s", r.code, r.raw, want, wantCode)
		}
	}

	// Legal hold: overwrite, move and delete are refused, the file is intact, the gate was asked
	// about this folder, and a new file is still allowed.
	a := file("a.md")
	other := h.doJSON("POST", "/v1/m/sessions/workspaces", admin, map[string]any{"root_path": root}, tenantHdr(tenant))
	if other.code != http.StatusCreated {
		t.Fatalf("register before hold=%d %s", other.code, other.raw)
	}
	otherBase := "/v1/m/sessions/workspaces/" + other.body["workspace_ref"].(string)
	gate.heldRef = ref
	gate.held = true
	// A second reference must not grant access to the held bytes, including
	// through a parent, child or symlink. Deregistration must preserve the hold.
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{root, filepath.Dir(root), child, alias} {
		refused(h.doJSON("POST", "/v1/m/sessions/workspaces", admin, map[string]any{"root_path": path}, tenantHdr(tenant)), http.StatusLocked, "legal_hold")
	}
	refused(call("DELETE", "", nil), http.StatusLocked, "legal_hold")
	refused(h.do("DELETE", otherBase+"/files?path=a.md", admin, tenantHdr(tenant)), http.StatusLocked, "legal_hold")
	refused(call("PUT", "/files/raw?path=a.md", nil), http.StatusLocked, "legal_hold")
	refused(call("POST", "/files/move", map[string]string{"from": "a.md", "to": "b.md"}), http.StatusLocked, "legal_hold")
	refused(call("DELETE", "/files?path=a.md", nil), http.StatusLocked, "legal_hold")
	if _, err := os.Stat(a); err != nil {
		t.Fatalf("held file was touched: %v", err)
	}
	if gate.kind != "workspace" || gate.ref != ref {
		t.Fatalf("gate asked about %q %q, want workspace %s", gate.kind, gate.ref, ref)
	}
	if r := call("PUT", "/files/raw?path=new.md", nil); r.code != http.StatusOK {
		t.Fatalf("creating a file under a hold = %d %s, want 200", r.code, r.raw)
	}
	gate.err = errors.New("compliance down")
	refused(call("DELETE", "/files?path=a.md", nil), http.StatusServiceUnavailable, "legal_hold_unavailable")
	gate.held, gate.err = false, nil
	if r := call("DELETE", "/files?path=a.md", nil); r.code != http.StatusOK {
		t.Fatalf("delete after the hold is released = %d %s, want 200", r.code, r.raw)
	}

	// Retention period: an entry changed inside the period is kept, an older one is not.
	if r := call("PATCH", "", map[string]any{"retention_days": 30}); r.code != http.StatusOK || r.body["retention_days"] != float64(30) {
		t.Fatalf("PATCH retention_days = %d %s", r.code, r.raw)
	}
	if r := call("PATCH", "", map[string]any{"retention_days": -1}); r.code != http.StatusBadRequest {
		t.Fatalf("PATCH retention_days=-1 = %d %s, want 400", r.code, r.raw)
	}
	refused(h.doJSON("POST", "/v1/m/sessions/workspaces", admin, map[string]any{"root_path": root}, tenantHdr(tenant)), http.StatusLocked, "retention_period")
	refused(call("DELETE", "", nil), http.StatusLocked, "retention_period")
	c := file("c.md")
	refused(call("DELETE", "/files?path=c.md", nil), http.StatusLocked, "retention_period")
	refused(call("PUT", "/files/raw?path=c.md", nil), http.StatusLocked, "retention_period")
	refused(call("POST", "/files/move", map[string]string{"from": "c.md", "to": "d.md"}), http.StatusLocked, "retention_period")
	if err := os.MkdirAll(filepath.Join(root, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	file(filepath.Join("dir", "inner.md"))
	refused(call("DELETE", "/files?path=dir&recursive=true", nil), http.StatusLocked, "retention_period")
	old := time.Now().Add(-31 * 24 * time.Hour)
	for _, p := range []string{c, filepath.Join(root, "dir", "inner.md"), filepath.Join(root, "dir")} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"/files?path=c.md", "/files?path=dir&recursive=true"} {
		if r := call("DELETE", path, nil); r.code != http.StatusOK {
			t.Fatalf("DELETE %s after the period = %d %s, want 200", path, r.code, r.raw)
		}
	}
}
