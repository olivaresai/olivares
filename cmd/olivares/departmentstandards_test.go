// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sourcescope"
)

// A department carries its standards (#530), on the real engine and through the real routes: the
// instructions assigned to Engineering reach the Claude launches of its members and of the
// departments under it, a forbid on a sub-department holds, and the instructions file of the
// department's workstations is rendered from the same text. The Community build refuses to
// write an assignment, which this also pins; the assignments are stored the way a Business
// install stores them.

// argvFile is where the stub keeps the argv it was started with, in the directory it runs in:
// a session is confined to its own workspace and can write nowhere else.
const argvFile = "argv.json"

// recordArgv replaces the protocol stub with one that also keeps the argv it was started with.
func (d departmentEngine) recordArgv() {
	d.t.Helper()
	stub := "#!/usr/bin/env python3\nimport json, sys\n" +
		"open(" + pyString(argvFile) + ", 'w').write(json.dumps(sys.argv[1:]))\n" +
		"print(json.dumps({'type':'system','subtype':'init','session_id':'dormant-stub'}), flush=True)\n" +
		"for line in sys.stdin:\n" +
		"    print(json.dumps({'type':'result','subtype':'success','is_error':False,'result':'ok','session_id':'dormant-stub'}), flush=True)\n"
	if err := os.WriteFile(filepath.Join(d.s.folder, "agent.py"), []byte(stub), 0o700); err != nil {
		d.t.Fatal(err)
	}
}

func pyString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// appendedPrompt is the system prompt a launch passed the tool, and whether it passed one. The stub
// writes its argv when the engine has started it, a moment after the launch answered.
func appendedPrompt(t *testing.T, workspace string) (string, bool) {
	t.Helper()
	var raw []byte
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline) && len(raw) == 0; time.Sleep(20 * time.Millisecond) {
		raw, _ = os.ReadFile(filepath.Join(workspace, argvFile))
	}
	var argv []string
	if err := json.Unmarshal(raw, &argv); err != nil {
		t.Fatalf("the stub never wrote its argv in %s: %v", workspace, err)
	}
	for i, a := range argv {
		if a == "--append-system-prompt" && i+1 < len(argv) {
			return argv[i+1], true
		}
	}
	return "", false
}

func (d departmentEngine) workspace(slug string, parent model.ID) model.ID {
	d.t.Helper()
	var id model.ID
	if err := d.s.eng.store.Mutate(d.t.Context(), d.tenant, func(sc store.Scope) error {
		ws, err := sc.Workspaces().Create(d.t.Context(), model.Workspace{Name: slug, Slug: slug, Status: model.StatusActive, ParentID: parent})
		id = ws.ID
		return err
	}); err != nil {
		d.t.Fatal(err)
	}
	return id
}

// teamIn is a directory group of people, filed in the department the organization says they are
// (PUT /v1/groups/{id}/workspace), nested under another group when parent is not empty. It returns
// the token of its one member.
func (d departmentEngine) teamIn(name string, parent model.ID, department model.ID) (model.ID, string) {
	d.t.Helper()
	email := name + "@example.invalid"
	member := d.user(email)
	var id model.ID
	if err := d.s.eng.store.AuthMutate(d.t.Context(), func(as store.AuthScope) error {
		g, err := as.Groups().Create(d.t.Context(), model.UserGroup{TargetTenantID: d.tenant, DisplayName: name, ParentGroupID: parent, WorkspaceID: department})
		if err != nil {
			return err
		}
		id = g.ID
		_, err = as.GroupMembers().Create(d.t.Context(), model.UserGroupMember{GroupID: g.ID, UserID: member})
		return err
	}); err != nil {
		d.t.Fatal(err)
	}
	return id, d.login(email)
}

// instruction is a session template whose custom instructions are the text.
func (d departmentEngine) instruction(name, text string) string {
	d.t.Helper()
	out := d.s.do("POST", "/v1/m/sessions/templates", map[string]any{
		"name": name, "description": "standards", "body": map[string]any{"settings": map[string]any{"custom_instructions": text}},
	}, http.StatusCreated)
	return out["id"].(string)
}

// assignToDepartment stores an assignment on a department the way a Business install does.
func (d departmentEngine) assignToDepartment(sourceType, template, department, effect string) {
	d.t.Helper()
	if err := d.s.eng.store.Mutate(d.t.Context(), d.tenant, func(sc store.Scope) error {
		ws, _, err := sc.Workspaces().List(d.t.Context(), model.Query{Filters: []model.Filter{{Column: "slug", Op: model.OpEq, Value: department}}, Limit: 1})
		if err != nil || len(ws) == 0 {
			d.t.Fatalf("department %s: %v", department, err)
		}
		repo, err := sc.Ext("sourcescope.binding")
		if err != nil {
			return err
		}
		_, err = repo.Create(d.t.Context(), model.Record{
			"source_type": sourceType, "source_ref": template, "scope_tree": "workspace", "scope_ref": department,
			"workspace_id": ws[0].ID.String(), "enabled": true, "created_by": "fixture", "effect": effect,
		})
		return err
	}); err != nil {
		d.t.Fatal(err)
	}
}

// fileOf is the instructions file the department's workstations are given, as the engine renders it.
func (d departmentEngine) fileOf(department string) map[string]any {
	d.t.Helper()
	return d.s.do("POST", "/v1/m/claude-policy/managed-instructions@"+department+"/dry-run", map[string]any{"content": ""}, http.StatusOK)
}

func TestADepartmentCarriesItsInstructionsToItsSessionsAndItsWorkstations(t *testing.T) {
	d := newDepartmentEngine(t)
	d.recordArgv()
	profile := d.profile(d.s.home, "tools")

	// The Community build does not write an assignment ...
	org := d.instruction("Org standards", "Use tabs.")
	platformRules := d.instruction("Platform standards", "Be terse.")
	refused := d.s.do("POST", "/v1/m/sourcescope/bindings", map[string]any{
		"source_type": "instructions", "source_ref": org, "scope_tree": "workspace", "scope_ref": "default", "enabled": true,
	}, http.StatusNotImplemented)
	if e, _ := refused["error"].(map[string]any); e["code"] != "business_required" {
		t.Fatalf("assigning instructions in Community = %v, want business_required", refused)
	}

	// ... and applies the ones a Business install stored.
	eng := d.workspace("engineering", "")
	platform := d.workspace("platform", eng)
	sales := d.workspace("sales", "")
	d.assignToDepartment("instructions", org, "engineering", "allow")
	d.assignToDepartment("instructions", platformRules, "platform", "allow")
	// Their people are directory groups the organization filed in them; a group nested under
	// another is inside it.
	engineering, engineer := d.teamIn("engineering", "", eng)
	_, platformer := d.teamIn("platform", engineering, platform)
	_, seller := d.teamIn("sales", "", sales)

	launch := func(token string) (string, bool) {
		t.Helper()
		code, body, raw := d.as(token, "POST", "/v1/m/sessions/runs", map[string]any{
			"transport": "stream-json", "permission_mode": "default", "isolation": "native", "provider_profile_ref": profile,
		})
		if code != http.StatusCreated {
			t.Fatalf("launch = %d %s", code, raw)
		}
		defer d.as(token, "POST", "/v1/m/sessions/runs/"+body["run_ref"].(string)+"/stop", map[string]any{})
		return appendedPrompt(t, body["workspace_path"].(string))
	}

	gotEngineer, ok := launch(engineer)
	if want := "# Org standards\n\nUse tabs.\n"; !ok || gotEngineer != want {
		t.Fatalf("a member of Engineering was given %q (%v), want %q", gotEngineer, ok, want)
	}
	// A sub-department inherits its parent's, parent first.
	if got, ok := launch(platformer); !ok || got != "# Org standards\n\nUse tabs.\n\n# Platform standards\n\nBe terse.\n" {
		t.Errorf("a member of Platform was given %q (%v), want Engineering's then Platform's", got, ok)
	}
	// A department nobody assigned anything to is given nothing.
	if got, ok := launch(seller); ok {
		t.Errorf("a member of Sales was given %q", got)
	}

	// The workstations of Engineering read the very text its sessions are given.
	sum := sha256.Sum256([]byte(gotEngineer))
	if dry := d.fileOf("engineering"); dry["content"] != gotEngineer || dry["sha256"] != hex.EncodeToString(sum[:]) {
		t.Fatalf("the instructions file of Engineering = %v, want the text its session was given (sha256 %x)", dry, sum)
	}

	// Editing the record changes both.
	d.s.do("PUT", "/v1/m/sessions/templates/"+org, map[string]any{
		"name": "Org standards", "description": "standards", "body": map[string]any{"settings": map[string]any{"custom_instructions": "Use spaces."}},
	}, http.StatusOK)
	edited := "# Org standards\n\nUse spaces.\n"
	if got, _ := launch(engineer); got != edited {
		t.Errorf("after the edit a member of Engineering was given %q, want %q", got, edited)
	}
	if dry := d.fileOf("engineering"); dry["content"] != edited {
		t.Errorf("after the edit the file of Engineering is %v, want %q", dry["content"], edited)
	}

	// A forbid on a sub-department holds, for its people and for its file.
	d.assignToDepartment("instructions", org, "platform", "forbid")
	if got, ok := launch(platformer); !ok || got != "# Platform standards\n\nBe terse.\n" {
		t.Errorf("with Engineering's instructions forbidden at Platform its member was given %q (%v), want only Platform's", got, ok)
	}
	if got, _ := launch(engineer); got != edited {
		t.Errorf("a forbid at Platform reached Engineering: %q", got)
	}
	if dry := d.fileOf("platform"); dry["content"] != "# Platform standards\n\nBe terse.\n" {
		t.Errorf("the file of Platform is %v, want only its own", dry["content"])
	}
}

// joinGroup makes the person a member of the group.
func (d departmentEngine) joinGroup(person, group model.ID) {
	d.t.Helper()
	if err := d.s.eng.store.AuthMutate(d.t.Context(), func(as store.AuthScope) error {
		_, err := as.GroupMembers().Create(d.t.Context(), model.UserGroupMember{GroupID: group, UserID: person})
		return err
	}); err != nil {
		d.t.Fatal(err)
	}
}

// assignToGroup stores an assignment on a directory group the way a Business install does.
func (d departmentEngine) assignToGroup(sourceType, template string, group model.ID) {
	d.t.Helper()
	if err := d.s.eng.store.Mutate(d.t.Context(), d.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("sourcescope.binding")
		if err != nil {
			return err
		}
		_, err = repo.Create(d.t.Context(), model.Record{
			"source_type": sourceType, "source_ref": template, "scope_tree": "user_group", "scope_ref": group.String(),
			"enabled": true, "created_by": "fixture", "effect": "allow",
		})
		return err
	}); err != nil {
		d.t.Fatal(err)
	}
}

// A person in a directory group is given what the group was assigned, and a person in a group the
// organization filed in a department is given the department's too, the department's first.
func TestAGroupsInstructionsReachItsMembersAfterTheirDepartments(t *testing.T) {
	d := newDepartmentEngine(t)
	d.recordArgv()
	profile := d.profile(d.s.home, "tools")
	eng := d.workspace("engineering", "")
	org := d.instruction("Org standards", "Use tabs.")
	devs := d.instruction("Devs", "Review twice.")
	d.assignToDepartment("instructions", org, "engineering", "allow")

	_, engineer := d.teamIn("engineering", "", eng)
	devsGroup, devsOnly := d.teamIn("devs", "", "") // a group of people, filed in no department
	d.assignToGroup("instructions", devs, devsGroup)
	// A person in both.
	both := d.user("both@example.invalid")
	engineeringGroup, _ := d.teamIn("engineering-too", "", eng)
	d.joinGroup(both, engineeringGroup)
	d.joinGroup(both, devsGroup)
	bothToken := d.login("both@example.invalid")

	launch := func(token string) (string, bool) {
		t.Helper()
		code, body, raw := d.as(token, "POST", "/v1/m/sessions/runs", map[string]any{
			"transport": "stream-json", "permission_mode": "default", "isolation": "native", "provider_profile_ref": profile,
		})
		if code != http.StatusCreated {
			t.Fatalf("launch = %d %s", code, raw)
		}
		defer d.as(token, "POST", "/v1/m/sessions/runs/"+body["run_ref"].(string)+"/stop", map[string]any{})
		return appendedPrompt(t, body["workspace_path"].(string))
	}

	if got, _ := launch(devsOnly); got != "# Devs\n\nReview twice.\n" {
		t.Errorf("a member of the devs group alone was given %q, want the group's instructions", got)
	}
	if got, _ := launch(engineer); got != "# Org standards\n\nUse tabs.\n" {
		t.Errorf("a member of Engineering's group was given %q, want the department's instructions only", got)
	}
	if got, _ := launch(bothToken); got != "# Org standards\n\nUse tabs.\n\n# Devs\n\nReview twice.\n" {
		t.Errorf("a member of both was given %q, want the department's instructions, then the group's", got)
	}
}

// The departments of a person are the ones the organization filed their groups in, a group nested
// under another counts as inside it, and an acting agent adds the one it lives in.
func TestADepartmentsOfFollowsTheGroupsTheOrganizationFiled(t *testing.T) {
	d := newDepartmentEngine(t)
	eng := d.workspace("engineering", "")
	platform := d.workspace("platform", eng)
	ss := sourcescope.New()
	ss.UseData(api.NewModuleData(d.s.eng.store))
	adapter := departmentStandards{scopes: ss.Resolver(), st: d.s.eng.store}

	engineeringGroup, _ := d.teamIn("engineering", "", eng)
	_, nested := d.teamIn("platform", engineeringGroup, platform)
	_, unfiled := d.teamIn("unfiled", "", "")
	_, dangling := d.teamIn("ghost", "", model.NewID()) // filed in a workspace that is not there
	_, filed := d.teamIn("engineers", "", eng)

	if err := d.s.eng.store.Mutate(d.t.Context(), d.tenant, func(sc store.Scope) error {
		_, err := sc.Agents().Create(d.t.Context(), model.Agent{Name: "bot", ExternalID: "bot", Kind: "claude-code", Status: model.StatusActive, WorkspaceID: platform})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	principal := func(token string) auth.Principal {
		t.Helper()
		p, err := d.s.eng.authr.Authenticate(t.Context(), token)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	for name, tc := range map[string]struct {
		who  auth.Principal
		want []string
	}{
		"a member of a group filed in a department":    {principal(filed), []string{"engineering"}},
		"a group nested under one filed in another":    {principal(nested), []string{"engineering", "platform"}},
		"a group filed in no department":               {principal(unfiled), nil},
		"a group filed in a workspace that is gone":    {principal(dangling), nil},
		"an acting agent adds its own department":      {principal(unfiled).WithAgentIdentity("bot"), []string{"platform"}},
		"an acting agent and the groups of its caller": {principal(filed).WithAgentIdentity("bot"), []string{"engineering", "platform"}},
		"an agent the tenant does not know":            {principal(unfiled).WithAgentIdentity("nobody"), nil},
	} {
		got, err := adapter.DepartmentsOf(t.Context(), d.tenant, tc.who)
		if err != nil || !slices.Equal(got, tc.want) {
			t.Errorf("%s: DepartmentsOf = %v, %v; want %v", name, got, err, tc.want)
		}
	}
}
