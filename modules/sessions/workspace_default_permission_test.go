// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"fmt"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// A folder can store the permission its sessions start under. It is set
// by PATCH with the same validation as the launch's names, shown by the next read,
// and a setting the request does not name is left alone.
func TestPatchWorkspaceDefaultPermissionRoundTrip(t *testing.T) {
	h := newHarness(t, New())
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "perm-patch")
	other := h.createOrg(admin, "perm-patch-other")
	viewer := h.viewerToken(admin, tenant, "perm-viewer@example.invalid")
	editor := dlpPatchToken(h, admin, tenant, "perm-editor@example.invalid", auth.RoleEditor)
	tools := t.TempDir()
	created := h.doJSON("POST", "/v1/m/sessions/workspaces", admin, map[string]any{
		"root_path": t.TempDir(), "dlp_mode": "label", "read_only_folders": []string{tools},
	}, tenantHdr(tenant))
	if created.code != http.StatusCreated {
		t.Fatalf("register=%d %s", created.code, created.raw)
	}
	if _, has := created.body["default_permission"]; has {
		t.Fatalf("a new folder shows a default permission: %s", created.raw)
	}
	ref := created.body["workspace_ref"].(string)
	path := "/v1/m/sessions/workspaces/" + ref
	read := func() resp { return h.do("GET", path, admin, tenantHdr(tenant)) }

	// A name, an older name and a label are one permission, stored by its name.
	for _, tc := range []struct{ send, want string }{
		{"supervised", "supervised"}, {"ask", "supervised"}, {"Auto-accept edits", "auto-accept-edits"},
		{"edits-only", "auto-accept-edits"}, {"read-only", "read-only"}, {"auto", "auto"}, {"edits-and-commands", "edits-and-commands"},
		{"  auto \n", "auto"},
	} {
		r := h.doJSON("PATCH", path, admin, map[string]any{"default_permission": tc.send}, tenantHdr(tenant))
		if r.code != http.StatusOK || r.body["default_permission"] != tc.want {
			t.Fatalf("PATCH default_permission=%q -> %d %s, want %q", tc.send, r.code, r.raw, tc.want)
		}
		if got := read(); got.body["default_permission"] != tc.want {
			t.Fatalf("GET after PATCH %q -> %s", tc.send, got.raw)
		}
	}
	// The other settings of the folder are untouched.
	got := read()
	if folders, _ := got.body["read_only_folders"].([]any); got.body["dlp_mode"] != "label" || len(folders) != 1 || folders[0] != tools {
		t.Fatalf("PATCH default_permission changed another setting: %s", got.raw)
	}
	// Full is for a run administrator, who is what the harness administrator is.
	if r := h.doJSON("PATCH", path, admin, map[string]any{"default_permission": "full"}, tenantHdr(tenant)); r.code != http.StatusOK || r.body["default_permission"] != "full" {
		t.Fatalf("PATCH default_permission=full by an administrator -> %d %s", r.code, r.raw)
	}
	// An empty string takes the default away.
	if r := h.doJSON("PATCH", path, admin, map[string]any{"default_permission": ""}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("PATCH default_permission=\"\" -> %d %s", r.code, r.raw)
	} else if _, has := r.body["default_permission"]; has {
		t.Fatalf("the cleared default is still shown: %s", r.raw)
	}

	// Refusals change nothing.
	h.doJSON("PATCH", path, admin, map[string]any{"default_permission": "supervised"}, tenantHdr(tenant))
	before := read().raw
	for _, tc := range []struct {
		name   string
		token  string
		hdr    map[string]string
		body   map[string]any
		status int
	}{
		{"unknown name", admin, tenantHdr(tenant), map[string]any{"default_permission": "yolo"}, http.StatusBadRequest},
		{"a raw engine mode is not a name", admin, tenantHdr(tenant), map[string]any{"default_permission": "bypassPermissions"}, http.StatusBadRequest},
		{"number", admin, tenantHdr(tenant), map[string]any{"default_permission": 3}, http.StatusBadRequest},
		{"null only", admin, tenantHdr(tenant), map[string]any{"default_permission": nil}, http.StatusBadRequest},
		{"good name, bad folder", admin, tenantHdr(tenant), map[string]any{"default_permission": "auto", "read_only_folders": []string{"relative"}}, http.StatusBadRequest},
		{"editor", editor, tenantHdr(tenant), map[string]any{"default_permission": "auto"}, http.StatusForbidden},
		{"viewer", viewer, tenantHdr(tenant), map[string]any{"default_permission": "auto"}, http.StatusForbidden},
		{"anonymous", "", tenantHdr(tenant), map[string]any{"default_permission": "auto"}, http.StatusUnauthorized},
		{"other organization", admin, tenantHdr(other), map[string]any{"default_permission": "auto"}, http.StatusNotFound},
	} {
		if r := h.doJSON("PATCH", path, tc.token, tc.body, tc.hdr); r.code != tc.status {
			t.Fatalf("%s: PATCH -> %d %s, want %d", tc.name, r.code, r.raw, tc.status)
		}
	}
	if after := read().raw; after != before {
		t.Fatalf("refused PATCH changed the workspace:\nbefore %s\nafter  %s", before, after)
	}
}

// Making Full a folder's default takes the authority that running Full takes: the
// folder's administrator alone could otherwise leave a run administrator to start
// in Full without having chosen it.
func TestFullFolderDefaultNeedsRunAdministration(t *testing.T) {
	full, _ := ResolvePermission("full")
	supervised, _ := ResolvePermission("supervised")
	if err := refuseUnrestrictedFolderDefault(full, false); statusOf(err) != http.StatusForbidden {
		t.Errorf("full by someone who may not run it = %v, want 403", err)
	}
	if err := refuseUnrestrictedFolderDefault(full, true); err != nil {
		t.Errorf("full by a run administrator = %v", err)
	}
	auto, _ := ResolvePermission("auto")
	if err := refuseUnrestrictedFolderDefault(auto, false); statusOf(err) != http.StatusForbidden {
		t.Errorf("auto by someone who may not run it = %v, want 403", err)
	}
	if err := refuseUnrestrictedFolderDefault(supervised, false); err != nil {
		t.Errorf("supervised by anyone who administers the folder = %v", err)
	}
}

// The Full rule holds over HTTP, not only in its helper: when the caller's run authority
// cannot be established, the folder's administrator can still set the other permissions
// and cannot set Full, by its name, an older name or its label, and nothing changes.
func TestPatchWorkspaceFullDefaultIsRefusedWithoutRunAuthority(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "perm-full")
	created := h.doJSON("POST", "/v1/m/sessions/workspaces", admin, map[string]any{"root_path": t.TempDir()}, tenantHdr(tenant))
	if created.code != http.StatusCreated {
		t.Fatalf("register=%d %s", created.code, created.raw)
	}
	path := "/v1/m/sessions/workspaces/" + created.body["workspace_ref"].(string)
	if r := h.doJSON("PATCH", path, admin, map[string]any{"default_permission": "supervised"}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("PATCH supervised -> %d %s", r.code, r.raw)
	}
	authority := m.WorkAuthorizer
	m.WorkAuthorizer = nil // no way to tell whether the caller may run Full: that is a no
	t.Cleanup(func() { m.WorkAuthorizer = authority })
	for _, full := range []string{"full", "Full", "FULL", " full "} {
		if r := h.doJSON("PATCH", path, admin, map[string]any{"default_permission": full}, tenantHdr(tenant)); r.code != http.StatusForbidden {
			t.Errorf("PATCH default_permission=%q without run authority -> %d %s, want 403", full, r.code, r.raw)
		}
	}
	if r := h.doJSON("PATCH", path, admin, map[string]any{"default_permission": "auto-accept-edits"}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Errorf("PATCH auto-accept-edits without run authority -> %d %s, want 200", r.code, r.raw)
	}
	m.WorkAuthorizer = authority
	if r := h.doJSON("PATCH", path, admin, map[string]any{"default_permission": "full"}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Errorf("PATCH full with run authority -> %d %s, want 200", r.code, r.raw)
	}
}

// A change of the default is evidence like the other folder settings: it seals a
// configure event whose hash commits to the new value and names the previous one.
func TestPatchWorkspaceDefaultPermissionIsAudited(t *testing.T) {
	m := New()
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "perm-audit")
	created := h.doJSON("POST", "/v1/m/sessions/workspaces", admin, map[string]any{"root_path": t.TempDir()}, tenantHdr(tenant))
	if created.code != http.StatusCreated {
		t.Fatalf("register=%d %s", created.code, created.raw)
	}
	ref := created.body["workspace_ref"].(string)
	for _, name := range []string{"supervised", "auto", ""} {
		if r := h.doJSON("PATCH", "/v1/m/sessions/workspaces/"+ref, admin, map[string]any{"default_permission": name}, tenantHdr(tenant)); r.code != http.StatusOK {
			t.Fatalf("PATCH %s -> %d %s", name, r.code, r.raw)
		}
	}
	events := acctAuditsOf(t, m, model.TenantID(tenant), "sessions.workspace.configure")
	// A refused request seals nothing.
	if r := h.doJSON("PATCH", "/v1/m/sessions/workspaces/"+ref, admin, map[string]any{"default_permission": "yolo"}, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Fatalf("PATCH yolo -> %d %s, want 400", r.code, r.raw)
	}
	if after := acctAuditsOf(t, m, model.TenantID(tenant), "sessions.workspace.configure"); len(after) != len(events) {
		t.Errorf("a refused PATCH sealed %d configure events", len(after)-len(events))
	}
	for _, tc := range []struct{ name, previous string }{{"supervised", ""}, {"auto", "supervised"}, {"", "auto"}} {
		want := sha256Hex([]byte(fmt.Sprintf(`{"default_permission":%q}`, tc.name)))
		found := false
		for _, ev := range events {
			if ev.meta["sha256"] == want && ev.meta["default_permission"] == tc.name &&
				ev.meta["previous_default_permission"] == tc.previous && ev.meta["workspace_ref"] == ref {
				found = true
			}
		}
		if !found {
			t.Errorf("no configure event for default_permission %q after %q: %+v", tc.name, tc.previous, events)
		}
	}
}

// Replay the registration from before the column existed: the engine's additive
// reconciler owns the migration on both engines, and a folder registered earlier
// opens with no default, takes one, and keeps it across a restart.
type beforeDefaultPermissionRegistry struct{ store.ExtensionRegistry }

func (r beforeDefaultPermissionRegistry) Register(d model.EntityDescriptor) error {
	if d.Kind == workspaceKind {
		fields := make([]model.FieldSpec, 0, len(d.Fields))
		for _, f := range d.Fields {
			if f.Name != colWsDefaultPermission {
				fields = append(fields, f)
			}
		}
		d.Fields = fields
	}
	return r.ExtensionRegistry.Register(d)
}

func TestWorkspaceDefaultPermissionUpgradeSQLite(t *testing.T) {
	cfg := store.Config{Engine: store.EngineSQLite, DSN: filepath.Join(t.TempDir(), "upgrade.db"), Debug: true}
	ctx := t.Context()
	m := New()
	st, err := engine.Open(ctx, cfg, func(reg store.ExtensionRegistry) error { return m.RegisterSchema(beforeDefaultPermissionRegistry{reg}) })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if st != nil {
			_ = st.Close()
		}
	})
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		org, err := sys.CreateOrg(ctx, model.Org{Name: "Default permission upgrade", Slug: "default-permission-upgrade", Status: model.StatusActive})
		tenant = org.TenantID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	m.UseData(api.NewModuleData(st))
	folder, err := m.createWorkspace(ctx, tenant, CreateWorkspaceParams{RootPath: t.TempDir(), Actor: "user:upgrade", ActorKind: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	m = New()
	st, err = engine.Open(ctx, cfg, m.RegisterSchema)
	if err != nil {
		t.Fatalf("additive upgrade: %v", err)
	}
	m.UseData(api.NewModuleData(st))
	got, err := m.getWorkspace(ctx, tenant, folder.WorkspaceRef)
	if err != nil || got.DefaultPermission != "" {
		t.Fatalf("upgraded folder default = %q err = %v, want none", got.DefaultPermission, err)
	}
	name := "supervised"
	if _, err := m.patchWorkspace(ctx, tenant, folder.WorkspaceRef, nil, nil, &name, true, nil, nil, "user:upgrade", "user"); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	m = New()
	st, err = engine.Open(ctx, cfg, m.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	m.UseData(api.NewModuleData(st))
	got, err = m.getWorkspace(ctx, tenant, folder.WorkspaceRef)
	if err != nil || got.DefaultPermission != "supervised" {
		t.Fatalf("reopened folder default = %q err = %v, want supervised", got.DefaultPermission, err)
	}
}
