// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/auth/authtest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance/testsupport"
)

func TestWorkspaceResourceRegistration(t *testing.T) {
	m, st, tenant, _ := newRuntimeHarness(t)
	ws := mkWorkspace(t, m, tenant, CreateWorkspaceParams{Name: "Shared"})
	rec, err := m.loadWorkspaceRec(t.Context(), tenant, ws.WorkspaceRef)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.View(t.Context(), tenant, func(sc store.Scope) error {
		resource, err := sc.Resources().Get(t.Context(), model.ID(rec.String(model.ColID)))
		if err != nil {
			return err
		}
		if resource.Kind != "folder" || resource.Name != "Shared" || resource.URI != "workspace:"+ws.WorkspaceRef {
			t.Fatalf("folder resource = %+v", resource)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// An administrator names a registered folder in a scoped grant or an inheritance
// filter by its Resource id, so both reads of the registry have to report it.
func TestWorkspaceResponseNamesItsFolderResource(t *testing.T) {
	auth.SetTestHashParams(auth.TestArgonMemKiB, auth.TestArgonTime, auth.TestArgonThreads)
	f := newStreamConfinementFixture(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true})
	admin := f.adminLogin()
	tenant := f.createOrg(admin, "folder-resource-id")
	ws := mkWorkspace(t, f.m, tenant, CreateWorkspaceParams{RootPath: t.TempDir()})
	rec, err := f.m.loadWorkspaceRec(t.Context(), tenant, ws.WorkspaceRef)
	if err != nil {
		t.Fatal(err)
	}
	want := rec.String(model.ColID)
	one := f.do("GET", "/v1/m/sessions/workspaces/"+ws.WorkspaceRef, admin, tenantHdr(tenant))
	if one.code != http.StatusOK || one.body["resource_id"] != want {
		t.Fatalf("GET workspace: %d resource_id = %v, want %s (%s)", one.code, one.body["resource_id"], want, one.raw)
	}
	list := f.do("GET", "/v1/m/sessions/workspaces", admin, tenantHdr(tenant))
	if list.code != http.StatusOK {
		t.Fatalf("list: %d %s", list.code, list.raw)
	}
	for _, item := range list.body["items"].([]any) {
		if row := item.(map[string]any); row["workspace_ref"] == ws.WorkspaceRef {
			if row["resource_id"] != want {
				t.Fatalf("list resource_id = %v, want %s", row["resource_id"], want)
			}
			return
		}
	}
	t.Fatalf("list lacks %s: %s", ws.WorkspaceRef, list.raw)
}

func TestWorkspaceResourceHTTP(t *testing.T) {
	auth.SetTestHashParams(auth.TestArgonMemKiB, auth.TestArgonTime, auth.TestArgonThreads)
	f := newStreamConfinementFixture(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true})
	admin := f.adminLogin()
	tenant := f.createOrg(admin, "folder-rights")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	ws := mkWorkspace(t, f.m, tenant, CreateWorkspaceParams{RootPath: root})
	other := mkWorkspace(t, f.m, tenant, CreateWorkspaceParams{})
	rec, err := f.m.loadWorkspaceRec(t.Context(), tenant, ws.WorkspaceRef)
	if err != nil {
		t.Fatal(err)
	}
	id := rec.String(model.ColID)
	created := f.doJSON("POST", "/v1/users", admin, map[string]any{"email": "folder-reader@test.io", "password": "folder-member-pass1", "tenant": tenant.String(), "role": auth.RoleViewer}, nil)
	if created.code != http.StatusCreated {
		t.Fatalf("user: %d %s", created.code, created.raw)
	}
	login := f.doJSON("POST", "/v1/auth/login", "", map[string]any{"email": "folder-reader@test.io", "password": "folder-member-pass1"}, nil)
	if login.code != http.StatusOK {
		t.Fatalf("login: %d %s", login.code, login.raw)
	}
	token := login.body["token"].(string)
	principal, err := f.authr.Authenticate(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	base := "/v1/m/sessions/workspaces/" + ws.WorkspaceRef
	check := func(method, path string, want int) {
		t.Helper()
		got := f.do(method, path, token, tenantHdr(tenant))
		if got.code != want {
			t.Errorf("%s %s = %d %s; want %d", method, path, got.code, got.raw, want)
		}
	}
	super, err := f.authr.Authenticate(t.Context(), admin)
	if err != nil {
		t.Fatal(err)
	}
	group := authtest.Group(t, f.st, tenant, "Folder editors", "", principal.UserID)
	var parent model.Resource
	if err := f.st.Mutate(t.Context(), tenant, func(sc store.Scope) error {
		var err error
		parent, err = sc.Resources().Create(t.Context(), model.Resource{Name: "Department", Kind: "folder"})
		if err != nil {
			return err
		}
		if _, err = sc.Resources().Move(t.Context(), model.ID(id), parent.ID); err != nil {
			return err
		}
		filters, err := sc.Ext("governance.inheritance_filter")
		if err != nil {
			return err
		}
		_, err = filters.Create(t.Context(), model.Record{"scope_tree": "folder", "scope_ref": parent.ID.String(), "scope_class": "resource", "created_by": super.Actor()})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	readPermit := fmt.Sprintf(`permit(principal in Group::"%s", action in [Action::"sessions:workspace:read", Action::"resource:read"], resource in Resource::"%s");`, group.Group.ID, parent.ID)
	// The administrator also needs a grant below the filter to inspect this node.
	readPermit += fmt.Sprintf(`permit(principal in User::"%s", action == Action::"resource:read", resource in Resource::"%s");`, super.UserID, parent.ID)
	testsupport.SeedCedar(t, f.st, tenant, readPermit, f.gov)
	check("GET", "/v1/m/sessions/workspaces/"+model.NewID().String()+"/files", 404)
	check("GET", base+"/files", 200)
	read := f.do("GET", base+"/files/raw?path=hello.txt", token, tenantHdr(tenant))
	if read.code != 200 || read.body["content"] != "hello" {
		t.Fatalf("group Read: %d %s", read.code, read.raw)
	}
	check("PUT", base+"/files/raw?path=hello.txt", 403)
	if data, err := os.ReadFile(filepath.Join(root, "hello.txt")); err != nil || string(data) != "hello" {
		t.Fatalf("refused write changed the file: %q %v", data, err)
	}
	// The real engine follows the folder ancestry and directory group membership.
	permit := readPermit + fmt.Sprintf(`permit(principal in Group::"%s", action in [Action::"sessions:workspace:write", Action::"resource:write"], resource in Resource::"%s");`, group.Group.ID, parent.ID)
	testsupport.SeedCedar(t, f.st, tenant, permit, f.gov)
	check("PUT", base+"/files/raw?path=hello.txt", 200)
	if data, err := os.ReadFile(filepath.Join(root, "hello.txt")); err != nil || len(data) != 0 {
		t.Fatalf("group Write did not write the empty body: %q %v", data, err)
	}
	check("PUT", "/v1/m/sessions/workspaces/"+other.WorkspaceRef+"/files/raw?path=other.txt", 403)
	rights := f.do("GET", "/v1/auth/effective-rights?subject_id="+principal.UserID.String()+"&kind=resource&id="+id, admin, tenantHdr(tenant))
	if rights.code != http.StatusOK {
		t.Fatalf("effective rights: %d %s", rights.code, rights.raw)
	}
	for _, entry := range rights.body["rights"].([]any) {
		right := entry.(map[string]any)
		if (right["name"] == "Read" || right["name"] == "Write" || right["name"] == "Create" || right["name"] == "Erase") && right["state"] != "held" {
			t.Errorf("effective right disagrees with files: %v", right)
		}
		// Each right says why: the group chain that carries it, at the ancestor it was
		// granted on, or the inheritance filter that removed it.
		switch right["name"] {
		case "Read", "Write":
			because, _ := right["because"].([]any)
			if len(because) != 1 {
				t.Errorf("%v: because = %v", right["name"], right["because"])
				continue
			}
			reason := because[0].(map[string]any)
			subject := reason["subject"].(map[string]any)
			chain, _ := subject["chain"].([]any)
			if reason["node"].(map[string]any)["ref"] != parent.ID.String() || subject["ref"] != group.Group.ID.String() || len(chain) != 2 {
				t.Errorf("%v: because does not name the group chain at the ancestor: %v", right["name"], reason)
			}
		case "Supervisor":
			removed, _ := right["removed_by"].([]any)
			if right["state"] != "not_held" || len(removed) != 1 || removed[0].(map[string]any)["kind"] != "irf" {
				t.Errorf("Supervisor is not explained by the inheritance filter: %v", right)
			}
		}
	}
	// A module-action forbid must also hide the registry entry.
	moduleForbid := fmt.Sprintf(`forbid(principal in User::"%s", action == Action::"sessions:workspace:read", resource in Resource::"%s");`, principal.UserID, id)
	testsupport.SeedCedar(t, f.st, tenant, permit+moduleForbid, f.gov)
	check("GET", base+"/files", 403)
	moduleList := f.do("GET", "/v1/m/sessions/workspaces", token, tenantHdr(tenant))
	if moduleList.code != 200 {
		t.Fatalf("registry: %d %s", moduleList.code, moduleList.raw)
	}
	for _, item := range moduleList.body["items"].([]any) {
		if item.(map[string]any)["workspace_ref"] == ws.WorkspaceRef {
			t.Error("registry bypassed a module Read forbid")
		}
	}
	// Canonical Read forbids reach current/HEAD reads, metadata and listings.
	forbid := fmt.Sprintf(`forbid(principal in User::"%s", action == Action::"resource:read", resource in Resource::"%s");`, principal.UserID, id)
	testsupport.SeedCedar(t, f.st, tenant, permit+forbid, f.gov)
	for _, path := range []string{base, base + "/files", base + "/files/stat?path=hello.txt", base + "/files/raw?path=hello.txt", base + "/files/raw?path=hello.txt&rev=HEAD"} {
		check("GET", path, 403)
	}
	listed := f.do("GET", "/v1/m/sessions/workspaces", token, tenantHdr(tenant))
	if listed.code != 200 {
		t.Fatalf("registry list: %d %s", listed.code, listed.raw)
	}
	for _, item := range listed.body["items"].([]any) {
		if item.(map[string]any)["workspace_ref"] == ws.WorkspaceRef {
			t.Error("registry lists a forbidden folder")
		}
	}
	// An absolute forbid beats the positive grant on every mutation route.
	absolute := fmt.Sprintf(`forbid(principal in User::"%s", action, resource in Resource::"%s");`, principal.UserID, id)
	testsupport.SeedCedar(t, f.st, tenant, permit+absolute, f.gov)
	check("PUT", base+"/files/raw?path=hello.txt", 403)
	check("POST", base+"/files/dir?path=sub", 403)
	check("DELETE", base+"/files?path=hello.txt", 403)
	if err := f.st.Mutate(t.Context(), tenant, func(sc store.Scope) error { return sc.Resources().Delete(t.Context(), model.ID(id)) }); err != nil {
		t.Fatal(err)
	}
	testsupport.SeedCedar(t, f.st, tenant, `permit(principal in Role::"nobody", action, resource);`, f.gov)
	check("GET", base+"/files", 503)
	listed = f.do("GET", "/v1/m/sessions/workspaces", admin, tenantHdr(tenant))
	if listed.code != 200 {
		t.Fatalf("broken anchor poisoned list: %d %s", listed.code, listed.raw)
	}
	foundOther := false
	for _, item := range listed.body["items"].([]any) {
		ref := item.(map[string]any)["workspace_ref"]
		if ref == ws.WorkspaceRef {
			t.Error("listed broken folder")
		}
		if ref == other.WorkspaceRef {
			foundOther = true
		}
	}
	if !foundOther {
		t.Error("healthy folder hidden by unrelated broken anchor")
	}
}

func TestWorkspaceResourceAdminForbids(t *testing.T) {
	auth.SetTestHashParams(auth.TestArgonMemKiB, auth.TestArgonTime, auth.TestArgonThreads)
	f := newStreamConfinementFixture(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true})
	admin := f.adminLogin()
	tenant := f.createOrg(admin, "folder-admin-forbids")
	for _, tc := range []struct{ method, path, action string }{
		{"GET", "/project-config", "resource:read"},
		{"POST", "/wiki", "resource:write"},
		{"PATCH", "", "resource:write"},
		{"DELETE", "", "resource:write"},
		{"PUT", "/setup-command", "resource:write"},
		{"POST", "/setup-command/approve", "resource:write"},
		{"POST", "/project-config/approve", "resource:write"},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			ws := mkWorkspace(t, f.m, tenant, CreateWorkspaceParams{})
			base := "/v1/m/sessions/workspaces/" + ws.WorkspaceRef
			testsupport.SeedCedar(t, f.st, tenant, `forbid(principal, action == Action::"`+tc.action+`", resource);`, f.gov)
			got := f.do(tc.method, base+tc.path, admin, tenantHdr(tenant))
			if got.code != http.StatusNotFound {
				t.Errorf("denied folder = %d %s, want concealed 404", got.code, got.raw)
			}
		})
	}
}

// A registry folder has no explicit workspace lineage. It remains a tenant-level
// registration: workspace-confined memberships cannot gain filesystem reach
// through it, including a membership confined to the default workspace.
func TestWorkspaceResourceConfinement(t *testing.T) {
	auth.SetTestHashParams(auth.TestArgonMemKiB, auth.TestArgonTime, auth.TestArgonThreads)
	f := newStreamConfinementFixture(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true})
	admin := f.adminLogin()
	tenant := f.createOrg(admin, "folder-confinement")
	folder := mkWorkspace(t, f.m, tenant, CreateWorkspaceParams{})
	var def, other model.ID
	if err := f.st.Mutate(t.Context(), tenant, func(sc store.Scope) error {
		d, err := sc.DefaultWorkspace(t.Context())
		if err != nil {
			return err
		}
		def = d.ID
		w, err := sc.Workspaces().Create(t.Context(), model.Workspace{Name: "Other", Slug: "other", Status: model.StatusActive})
		other = w.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		ws    model.ID
		email string
		want  int
	}{
		{model.ID(""), "tenant-folder@test.io", 200},
		{def, "default-folder@test.io", 403}, {other, "other-folder@test.io", 403},
	} {
		token := f.member(t, admin, tenant, tc.email, tc.ws)
		got := f.do("GET", "/v1/m/sessions/workspaces/"+folder.WorkspaceRef+"/files", token, tenantHdr(tenant))
		if got.code != tc.want {
			t.Errorf("workspace %s: %d %s, want %d", tc.ws, got.code, got.raw, tc.want)
		}
	}
}

// A person's folder is made the first time they ask for their folders, holds a 0700
// directory, and is theirs alone: another editor, and the administrator, get 404 on
// its content, while the administrator still finds it to manage.
func TestPersonalFolderIsPrivateToItsOwner(t *testing.T) {
	auth.SetTestHashParams(auth.TestArgonMemKiB, auth.TestArgonTime, auth.TestArgonThreads)
	f := newStreamConfinementFixture(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true})
	files := filepath.Join(t.TempDir(), "files")
	if err := f.m.UsePersonalFilesRoot(files); err != nil {
		t.Fatal(err)
	}
	admin := f.adminLogin()
	tenant := f.createOrg(admin, "my-files")
	ann, bob := f.member(t, admin, tenant, "ann@test.io", model.ID("")), f.member(t, admin, tenant, "bob@test.io", model.ID(""))
	annUser, err := f.authr.Authenticate(t.Context(), ann)
	if err != nil {
		t.Fatal(err)
	}
	refs := func(token string) []string {
		t.Helper()
		got := f.do("GET", "/v1/m/sessions/workspaces", token, tenantHdr(tenant))
		if got.code != http.StatusOK {
			t.Fatalf("list folders: %d %s", got.code, got.raw)
		}
		var out []string
		for _, item := range got.body["items"].([]any) {
			out = append(out, item.(map[string]any)["workspace_ref"].(string))
		}
		return out
	}
	mine := refs(ann)
	if len(mine) != 1 {
		t.Fatalf("first sign-in: ann sees %d folders, want her own", len(mine))
	}
	if info, err := os.Stat(filepath.Join(files, annUser.UserID.String())); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("ann's directory = %v, %v; want mode 0700", info, err)
	}
	if again := refs(ann); len(again) != 1 || again[0] != mine[0] {
		t.Fatalf("a second request registered another folder: %v then %v", mine, again)
	}
	base := "/v1/m/sessions/workspaces/" + mine[0]
	if got := f.do("PUT", base+"/files/raw?path=note.txt", ann, tenantHdr(tenant)); got.code != http.StatusOK {
		t.Fatalf("owner write: %d %s", got.code, got.raw)
	}
	if got := f.do("GET", base+"/files", ann, tenantHdr(tenant)); got.code != http.StatusOK {
		t.Fatalf("owner list: %d %s", got.code, got.raw)
	}
	for _, tc := range []struct{ who, token string }{{"editor", bob}, {"administrator", admin}} {
		for _, call := range []struct{ method, path string }{
			{"GET", "/files"}, {"GET", "/files/raw?path=note.txt"}, {"PUT", "/files/raw?path=x.txt"}, {"DELETE", "/files?path=note.txt"},
		} {
			if got := f.do(call.method, base+call.path, tc.token, tenantHdr(tenant)); got.code != http.StatusNotFound {
				t.Errorf("%s %s %s = %d %s; want 404", tc.who, call.method, call.path, got.code, got.raw)
			}
		}
	}
	if theirs := refs(bob); len(theirs) != 1 || theirs[0] == mine[0] {
		t.Errorf("bob's folders = %v, want only his own, not ann's %s", theirs, mine[0])
	}
	if !slices.Contains(refs(admin), mine[0]) {
		t.Errorf("the administrator cannot find ann's folder to manage it")
	}
}

// An administrator registers a folder as one person's own, the way a network folder
// mounted with that person's own login is registered: its content is theirs alone, as a
// personal folder's is, and an owner that names no person is refused.
func TestAdministratorRegistersAFolderOwnedByAPerson(t *testing.T) {
	auth.SetTestHashParams(auth.TestArgonMemKiB, auth.TestArgonTime, auth.TestArgonThreads)
	f := newStreamConfinementFixture(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true})
	admin := f.adminLogin()
	tenant := f.createOrg(admin, "owned-folder")
	ann, bob := f.member(t, admin, tenant, "ann@test.io", model.ID("")), f.member(t, admin, tenant, "bob@test.io", model.ID(""))
	annUser, err := f.authr.Authenticate(t.Context(), ann)
	if err != nil {
		t.Fatal(err)
	}
	register := func(owner string) resp {
		return f.doJSON("POST", "/v1/m/sessions/workspaces", admin, map[string]any{"name": "Ann's share", "root_path": t.TempDir(), "owner": owner}, tenantHdr(tenant))
	}
	if got := register("not-a-person"); got.code != http.StatusBadRequest {
		t.Fatalf("an owner that is no user id = %d %s, want 400", got.code, got.raw)
	}
	created := register(annUser.UserID.String())
	if created.code != http.StatusCreated {
		t.Fatalf("register a folder owned by ann = %d %s, want 201", created.code, created.raw)
	}
	base := "/v1/m/sessions/workspaces/" + created.body["workspace_ref"].(string)
	if got := f.do("PUT", base+"/files/raw?path=note.txt", ann, tenantHdr(tenant)); got.code != http.StatusOK {
		t.Fatalf("owner write: %d %s", got.code, got.raw)
	}
	for _, tc := range []struct{ who, token string }{{"editor", bob}, {"administrator", admin}} {
		if got := f.do("GET", base+"/files/raw?path=note.txt", tc.token, tenantHdr(tenant)); got.code != http.StatusNotFound {
			t.Errorf("%s reads the owned folder's content = %d %s; want 404", tc.who, got.code, got.raw)
		}
	}
	listed := func(token string) bool {
		t.Helper()
		got := f.do("GET", "/v1/m/sessions/workspaces", token, tenantHdr(tenant))
		for _, item := range got.body["items"].([]any) {
			if item.(map[string]any)["workspace_ref"] == created.body["workspace_ref"] {
				return true
			}
		}
		return false
	}
	if !listed(ann) || listed(bob) || !listed(admin) {
		t.Errorf("the owned folder is listed for ann=%v bob=%v admin=%v; want true false true", listed(ann), listed(bob), listed(admin))
	}
}

// An administrator of folders turns personal folders off for the tenant: nobody gets
// a new "My files" while it is off, a person without that right cannot change it,
// and turning it back on gives the folder at the next request.
func TestPersonalFoldersCanBeTurnedOffForATenant(t *testing.T) {
	auth.SetTestHashParams(auth.TestArgonMemKiB, auth.TestArgonTime, auth.TestArgonThreads)
	f := newStreamConfinementFixture(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true})
	files := filepath.Join(t.TempDir(), "files")
	if err := f.m.UsePersonalFilesRoot(files); err != nil {
		t.Fatal(err)
	}
	admin := f.adminLogin()
	tenant := f.createOrg(admin, "my-files-off")
	ann := f.member(t, admin, tenant, "ann@test.io", model.ID(""))
	path := "/v1/m/sessions/personal-folders-settings"
	folders := func() int {
		t.Helper()
		got := f.do("GET", "/v1/m/sessions/workspaces", ann, tenantHdr(tenant))
		if got.code != http.StatusOK {
			t.Fatalf("list folders: %d %s", got.code, got.raw)
		}
		return len(got.body["items"].([]any))
	}
	if got := f.do("GET", path, ann, tenantHdr(tenant)); got.code != http.StatusOK || got.body["enabled"] != true {
		t.Fatalf("default = %d %s, want enabled", got.code, got.raw)
	}
	if got := f.doJSON("PUT", path, ann, map[string]any{"enabled": false}, tenantHdr(tenant)); got.code != http.StatusForbidden {
		t.Fatalf("a person without folder administration changed it: %d %s", got.code, got.raw)
	}
	if got := f.doJSON("PUT", path, admin, map[string]any{"enabled": false}, tenantHdr(tenant)); got.code != http.StatusOK || got.body["enabled"] != false {
		t.Fatalf("turn off = %d %s", got.code, got.raw)
	}
	if n := folders(); n != 0 {
		t.Errorf("personal folders are off but ann sees %d folders", n)
	}
	if entries, err := os.ReadDir(files); err == nil && len(entries) != 0 {
		t.Errorf("personal folders are off but %d directories were made", len(entries))
	}
	if got := f.doJSON("PUT", path, admin, map[string]any{"enabled": true}, tenantHdr(tenant)); got.code != http.StatusOK || got.body["enabled"] != true {
		t.Fatalf("turn on = %d %s", got.code, got.raw)
	}
	if n := folders(); n != 1 {
		t.Errorf("personal folders are on again but ann sees %d folders, want her own", n)
	}
}

// A directory places a person in the department of their OU, which confines their
// membership to it. The administrator of that department registers a folder in it:
// the folder is a Resource of the department, so what administers the department
// governs the folder, and a sibling department, or none, is refused.
func TestDepartmentAdminRegistersAFolderInItsDepartment(t *testing.T) {
	auth.SetTestHashParams(auth.TestArgonMemKiB, auth.TestArgonTime, auth.TestArgonThreads)
	f := newStreamConfinementFixture(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true})
	admin := f.adminLogin()
	tenant := f.createOrg(admin, "folder-department")
	slugs := map[string]model.ID{}
	if err := f.st.Mutate(t.Context(), tenant, func(sc store.Scope) error {
		for _, slug := range []string{"engineering", "sales"} {
			ws, err := sc.Workspaces().Create(t.Context(), model.Workspace{Name: slug, Slug: slug, Status: model.StatusActive})
			if err != nil {
				return err
			}
			slugs[slug] = ws.ID
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	created := f.doJSON("POST", "/v1/users", admin, map[string]any{"email": "dept-admin@test.io", "password": "folder-member-pass1", "tenant": tenant.String(), "role": auth.RoleViewer, "workspace_id": slugs["engineering"].String()}, nil)
	if created.code != http.StatusCreated {
		t.Fatalf("confined member: %d %s", created.code, created.raw)
	}
	login := f.doJSON("POST", "/v1/auth/login", "", map[string]any{"email": "dept-admin@test.io", "password": "folder-member-pass1"}, nil)
	if login.code != http.StatusOK {
		t.Fatalf("login: %d %s", login.code, login.raw)
	}
	token := login.body["token"].(string)
	testsupport.SeedCedar(t, f.st, tenant, fmt.Sprintf(`permit(principal in User::"%s", action, resource) when { resource in Workspace::"engineering" };`, created.body["id"]), f.gov)
	register := func(department model.ID) resp {
		body := map[string]any{"name": "Delegated", "root_path": t.TempDir()}
		if !department.IsZero() {
			body["workspace_id"] = department.String()
		}
		return f.doJSON("POST", "/v1/m/sessions/workspaces", token, body, tenantHdr(tenant))
	}
	ok := register(slugs["engineering"])
	if ok.code != http.StatusCreated {
		t.Fatalf("department admin registers a folder in its department = %d %s, want 201", ok.code, ok.raw)
	}
	rec, err := f.m.loadWorkspaceRec(t.Context(), tenant, ok.body["workspace_ref"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.View(t.Context(), tenant, func(sc store.Scope) error {
		resource, err := sc.Resources().Get(t.Context(), model.ID(rec.String(model.ColID)))
		if err == nil && resource.WorkspaceID != slugs["engineering"] {
			t.Errorf("folder resource department = %q, want %s", resource.WorkspaceID, slugs["engineering"])
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if r := register(slugs["sales"]); r.code != http.StatusForbidden {
		t.Errorf("registering in a sibling department = %d %s, want 403", r.code, r.raw)
	}
	if r := register(""); r.code != http.StatusForbidden {
		t.Errorf("registering with no department = %d %s, want 403", r.code, r.raw)
	}
}
