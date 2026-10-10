// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance/testsupport"
	"github.com/olivaresai/olivares/modules/skills"
)

func (c skillsComposition) confineMember(who skillsComposition, workspace model.ID) {
	c.t.Helper()
	ctx := context.Background()
	p, err := c.eng.authr.Authenticate(ctx, who.token)
	if err != nil {
		c.t.Fatal(err)
	}
	err = c.eng.store.AuthMutate(ctx, func(sc store.AuthScope) error {
		rows, _, err := sc.Memberships().List(ctx, model.Query{Filters: []model.Filter{{Column: "user_id", Op: model.OpEq, Value: p.UserID.String()}}})
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.TargetTenantID.String() == c.tenant {
				row.WorkspaceID = workspace
				_, err = sc.Memberships().Update(ctx, row)
				return err
			}
		}
		return store.ErrNotFound
	})
	if err != nil {
		c.t.Fatal(err)
	}
}

func TestSkillsConfinedDepartmentAdminPinsThroughHTTP(t *testing.T) {
	c := bootSkillsComposition(t)
	ctx := context.Background()
	revision := c.installPack("department-pack")
	tenant, err := model.ParseTenantID(c.tenant)
	if err != nil {
		t.Fatal(err)
	}
	var home model.ID
	targets := map[string][]string{}
	err = c.eng.store.Mutate(ctx, tenant, func(sc store.Scope) error {
		for _, slug := range []string{"home", "other"} {
			ws, err := sc.Workspaces().Create(ctx, model.Workspace{Name: slug, Slug: slug, Status: model.StatusActive})
			if err != nil {
				return err
			}
			if slug == "home" {
				home = ws.ID
			}
			group, err := sc.AgentGroups().Create(ctx, model.AgentGroup{WorkspaceID: ws.ID, Name: slug, Slug: slug, Status: model.StatusActive})
			if err != nil {
				return err
			}
			agent, err := sc.Agents().Create(ctx, model.Agent{WorkspaceID: ws.ID, Name: slug, ExternalID: slug, Kind: "claude-code", Status: model.StatusActive})
			if err != nil {
				return err
			}
			targets["workspace"] = append(targets["workspace"], ws.ID.String())
			targets["agent_group"] = append(targets["agent_group"], group.ID.String())
			targets["agent"] = append(targets["agent"], agent.ID.String())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	admin := c.member("department@x.io", "admin")
	c.confineMember(admin, home)
	for _, kind := range []string{"workspace", "agent_group", "agent"} {
		t.Run(kind, func(t *testing.T) {
			id := targets[kind][0]
			code, raw := c.assign(admin, kind, id, revision)
			if code != http.StatusCreated {
				t.Fatalf("confined admin pins own %s = %d %s", kind, code, raw)
			}
			var result skills.AssignmentResult
			if err := json.Unmarshal(raw, &result); err != nil || result.Assignment.RevisionID != revision || len(result.Assignment.Members) != 1 {
				t.Fatalf("pin = %s, %v", raw, err)
			}
			pin := result.Assignment
			body := skills.AssignmentRequest{Target: pin.Target, RevisionID: revision, Members: []string{"research"}}
			path := "/v1/m/skills/assignments/" + pin.ID
			if code, raw := admin.doHeader("PUT", path, admin.token, strconv.FormatInt(pin.Version, 10), body); code != http.StatusOK {
				t.Fatalf("confined admin updates pin = %d %s", code, raw)
			}
			if code, raw := c.assign(admin, kind, targets[kind][1], revision); code != http.StatusNotFound {
				t.Fatalf("confined admin pins another department's %s = %d %s", kind, code, raw)
			}
			if code, raw := admin.do("GET", "/v1/m/skills/assignments?target_kind="+kind+"&target_id="+id, admin.token, nil); code != http.StatusOK {
				t.Fatalf("confined admin lists pins = %d %s", code, raw)
			}
			if code, raw := admin.doHeader("DELETE", path, admin.token, strconv.FormatInt(pin.Version+1, 10), nil); code != http.StatusOK {
				t.Fatalf("confined admin unpins = %d %s", code, raw)
			}
		})
	}
	// Catalog administration stays tenant-wide even though pinning may read a revision.
	if code, raw := admin.do("POST", "/v1/m/skills/packs", admin.token, nil); code != http.StatusForbidden {
		t.Fatalf("confined admin changes the shared catalog = %d %s", code, raw)
	}
}

func TestSkillsPreparedTargetRefusesChangedAuthority(t *testing.T) {
	c := bootSkillsComposition(t)
	ctx := context.Background()
	tenant, err := model.ParseTenantID(c.tenant)
	if err != nil {
		t.Fatal(err)
	}
	admin := c.member("fence-admin@x.io", "admin")
	p, err := c.eng.authr.Authenticate(ctx, admin.token)
	if err != nil {
		t.Fatal(err)
	}
	authority := skillsTargetAuthority{principals: c.eng.authr, st: c.eng.store, authz: c.eng.authz, sessions: c.eng.sessionsMod}
	target := skills.Target{Kind: "agent_group", ID: c.agentGroup("fenced")}
	for _, variant := range []string{"target", "membership", "graph", "policy"} {
		t.Run(variant, func(t *testing.T) {
			c.confineMember(admin, model.ID(""))
			prepared, release, err := authority.PrepareSkillsTarget(ctx, api.ModuleContext{Tenant: tenant, Principal: p, Resource: auth.ResourceAttrs{Kind: "agent_group", ID: target.ID}}, target, true)
			defer release()
			if err != nil {
				t.Fatal(err)
			}
			if variant == "target" {

				err = c.eng.store.Mutate(ctx, tenant, func(sc store.Scope) error {
					row, err := sc.AgentGroups().Get(ctx, model.ID(target.ID))
					if err != nil {
						return err
					}
					row.Name = "changed after authorization"
					_, err = sc.AgentGroups().Update(ctx, row)
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
			} else if variant == "membership" {
				c.confineMember(admin, model.ID(c.workspaceID()))
			} else if variant == "graph" {
				agentID := c.agent("graph-fence")
				err := c.eng.store.Mutate(ctx, tenant, func(sc store.Scope) error {
					_, err := sc.AgentGroupMembers().Create(ctx, model.AgentGroupMember{GroupID: model.ID(target.ID), AgentID: model.ID(agentID)})
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
			} else {
				testsupport.SeedCedar(t, c.eng.store, model.TenantID(c.tenant), `forbid(principal, action, resource) when { context.permission == "agent:write" };`, c.eng.nhiEnforcer)
			}
			err = c.eng.store.Mutate(prepared, tenant, func(sc store.Scope) error {
				_, err := authority.ResolveSkillsTarget(prepared, sc, p, target, true)
				return err
			})
			if !errors.Is(err, store.ErrConflict) {
				t.Fatalf("write after %s changed = %v, want conflict", variant, err)
			}
		})
	}
}

// Session launchers must retain their native authority across preparation and
// the real assignment mutation, even when neither target nor pack changes.
func TestSkillsSessionPreparedTargetRefusesChangedAuthority(t *testing.T) {
	for _, variant := range []string{"unchanged", "membership removed", "membership downgraded", "credential revoked", "user disabled", "credential expired", "token unchanged", "token revoked", "session revoked", "policy changed"} {
		t.Run(variant, func(t *testing.T) {
			c := bootSkillsComposition(t)
			ctx := t.Context()
			tenant, err := model.ParseTenantID(c.tenant)
			if err != nil {
				t.Fatal(err)
			}
			admin := c.member("session-fence@x.io", auth.RoleAdmin)
			owner, err := c.eng.authr.Authenticate(ctx, admin.token)
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(variant, "token") {
				token, _, err := c.eng.authr.IssueToken(ctx, owner, auth.TokenSpec{Name: "session authority fixture", BoundTenant: tenant, Role: auth.RoleAdmin})
				if err != nil {
					t.Fatal(err)
				}
				owner, err = c.eng.authr.Authenticate(ctx, token)
				if err != nil {
					t.Fatal(err)
				}
			}
			target := skills.Target{Kind: "agent_group", ID: c.agentGroup("session-fenced")}
			revision := c.installPack("session-fenced-pack")
			var expiry time.Time
			if variant == "credential expired" {
				expiry = time.Now().Add(2 * time.Second)
				err = c.eng.store.AuthMutate(ctx, func(sc store.AuthScope) error {
					row, err := sc.Sessions().Get(ctx, owner.CredID)
					if err != nil {
						return err
					}
					stamp := model.NewTimestamp(expiry)
					row.ExpiresAt = stamp
					_, err = sc.Sessions().Update(ctx, row)
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
				owner, err = c.eng.authr.Authenticate(ctx, admin.token)
				if err != nil {
					t.Fatal(err)
				}
			}
			issuer := auth.NewSessionCredentials(c.eng.authr, func(context.Context, auth.SessionScope) error { return nil })
			bearer, err := issuer.Mint(ctx, owner, auth.SessionScope{TenantID: tenant, WorkspaceID: model.ID(c.workspaceID()), FolderRef: "fixture", SessionRef: "osn_" + model.NewID().String(), RunRef: model.NewID().String(), Fence: 1})
			if err != nil {
				t.Fatal(err)
			}
			p, err := issuer.AuthenticateLauncher(ctx, bearer)
			if err != nil {
				t.Fatal(err)
			}
			a := skillsTargetAuthority{principals: c.eng.authr, st: c.eng.store, authz: c.eng.authz, sessions: c.eng.sessionsMod}
			mc := api.ModuleContext{Tenant: tenant, Principal: p, Resource: auth.ResourceAttrs{Kind: "agent_group", ID: target.ID}, Data: api.NewScopedData(c.eng.store, tenant)}
			prepared, release, err := a.PrepareSkillsTarget(ctx, mc, target, true)
			defer release()
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(variant, "membership") {
				err = c.eng.store.AuthMutate(ctx, func(sc store.AuthScope) error {
					rows, _, err := sc.Memberships().List(ctx, model.Query{Filters: []model.Filter{{Column: "user_id", Op: model.OpEq, Value: owner.UserID.String()}}})
					if err != nil {
						return err
					}
					for _, row := range rows {
						if row.TargetTenantID != tenant {
							continue
						}
						if variant == "membership removed" {
							return sc.Memberships().Delete(ctx, row.ID)
						}
						row.Role = auth.RoleViewer
						_, err = sc.Memberships().Update(ctx, row)
						return err
					}
					return store.ErrNotFound
				})
			} else if variant == "credential revoked" {
				err = c.eng.authr.RevokeSession(ctx, owner, owner.CredID)
			} else if variant == "token revoked" {
				err = c.eng.authr.RevokeToken(ctx, owner, owner.CredID)
			} else if variant == "user disabled" {
				err = c.eng.store.AuthMutate(ctx, func(sc store.AuthScope) error {
					row, err := sc.Users().Get(ctx, owner.UserID)
					if err != nil {
						return err
					}
					row.Status = model.StatusInactive
					_, err = sc.Users().Update(ctx, row)
					return err
				})
			} else if variant == "session revoked" {
				issuer.Revoke(tenant, p.SessionRunRef)
			} else if variant == "policy changed" {
				testsupport.SeedCedar(t, c.eng.store, model.TenantID(c.tenant), `forbid(principal, action, resource) when { context.permission == "skills:assignment:write" };`, c.eng.nhiEnforcer)
			} else if variant == "credential expired" {
				<-time.After(time.Until(expiry) + 10*time.Millisecond)
			}
			if err != nil {
				t.Fatal(err)
			}
			reached := false
			err = a.MutateSkillsAssignment(prepared, mc, revision, func(sc store.Scope, read skills.AssignmentRevisionReader) error {
				reached = true
				if _, err := a.ResolveSkillsTarget(prepared, sc, p, target, true); err != nil {
					return err
				}
				_, err := read(prepared, revision)
				return err
			})
			if variant == "unchanged" || variant == "token unchanged" {
				if err != nil || !reached {
					t.Fatalf("unchanged session authority = %v, reached=%t", err, reached)
				}
			} else if err == nil || reached {
				t.Fatalf("assignment after %s = %v, mutation reached=%t; want refusal before write", variant, err, reached)
			}
		})
	}
}

// agentGroup creates an agent group in the default workspace and returns its ID.
func (c skillsComposition) agentGroup(slug string) string {
	c.t.Helper()
	code, raw := c.do("POST", "/v1/agent-groups", c.token, map[string]any{"name": slug, "slug": slug})
	if code != http.StatusCreated {
		c.t.Fatalf("create agent group = %d %s", code, raw)
	}
	return decodeField(c.t, raw, "id")
}

// agent registers an agent in the default workspace and returns its ID.
func (c skillsComposition) agent(name string) string {
	c.t.Helper()
	code, raw := c.do("POST", "/v1/agents", c.token, map[string]any{"name": name, "kind": "claude-code", "external_id": name})
	if code != http.StatusCreated {
		c.t.Fatalf("create agent = %d %s", code, raw)
	}
	return decodeField(c.t, raw, "id")
}

// A department admin pins a skill set to an agent group or to one agent through
// the shipped binary: the composition root connects both kinds to the stored
// directory rows, and a row that does not exist is not found.
func TestSkillsAssignmentForAgentGroupAndAgentIsWiredInComposition(t *testing.T) {
	c := bootSkillsComposition(t)
	revision := c.installPack("directory-pack")
	targets := map[string]string{"agent_group": c.agentGroup("platform"), "agent": c.agent("reviewer")}

	for kind, id := range targets {
		if code, raw := c.assign(c, kind, id, revision); code != http.StatusCreated {
			t.Fatalf("owner assigns to an %s = %d %s", kind, code, raw)
		}
		code, raw := c.do("GET", "/v1/m/skills/assignments?target_kind="+kind+"&target_id="+id, c.token, nil)
		var listed struct {
			Items []struct {
				Members []string `json:"members"`
			} `json:"items"`
		}
		if err := json.Unmarshal(raw, &listed); err != nil || code != http.StatusOK || len(listed.Items) != 1 || len(listed.Items[0].Members) != 1 || listed.Items[0].Members[0] != "research" {
			t.Fatalf("list %s assignments = %d %s", kind, code, raw)
		}
		if code, raw := c.assign(c, kind, model.NewID().String(), revision); code != http.StatusNotFound {
			t.Fatalf("assign to an absent %s = %d %s", kind, code, raw)
		}
	}
	editor := c.member("editor@x.io", "editor")
	if code, raw := c.assign(editor, "agent_group", c.agentGroup("editors"), revision); code != http.StatusCreated {
		t.Fatalf("an editor holds agent:write and assigns to a group it may edit = %d %s", code, raw)
	}
	viewer := c.member("viewer@x.io", "viewer")
	if code, raw := c.assign(viewer, "agent_group", targets["agent_group"], revision); code == http.StatusCreated {
		t.Fatalf("a viewer pinned a skill set to a group = %d %s", code, raw)
	}
}

// An agent or group with no workspace belongs to the default one. Its pin
// records that workspace rather than none, because the assignment table hides a
// row with no workspace from every confined principal, the one confined to the
// default workspace included.
func TestSkillsDirectoryTargetWithoutAWorkspaceIsPinnedToTheDefaultWorkspace(t *testing.T) {
	c := bootSkillsComposition(t)
	ctx := context.Background()
	revision := c.installPack("default-lineage")
	defaultWorkspace := c.workspaceID()
	tenant, err := model.ParseTenantID(c.tenant)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := c.eng.authr.Authenticate(ctx, c.token)
	if err != nil {
		t.Fatal(err)
	}
	authority := skillsTargetAuthority{principals: c.eng.authr, st: c.eng.store, authz: c.eng.authz, sessions: c.eng.sessionsMod}

	for kind, id := range map[string]string{"agent_group": c.agentGroup("unplaced"), "agent": c.agent("unplaced")} {
		code, raw := c.assign(c, kind, id, revision)
		var pinned struct {
			Assignment struct {
				ID string `json:"id"`
			} `json:"assignment"`
		}
		if err := json.Unmarshal(raw, &pinned); err != nil || code != http.StatusCreated {
			t.Fatalf("assign to an unplaced %s = %d %s", kind, code, raw)
		}
		// Bound resolution so a nested authorization read fails instead of
		// holding SQLite's single connection until the suite times out.
		resolveCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		target := skills.Target{Kind: kind, ID: id}
		prepared, release, err := authority.PrepareSkillsTarget(resolveCtx, api.ModuleContext{Tenant: tenant, Principal: owner}, target, false)
		defer release()
		if err != nil {
			t.Fatal(err)
		}
		err = c.eng.store.View(prepared, tenant, func(raw store.Scope) error {
			repo, err := raw.Ext(skills.AssignmentKind)
			if err != nil {
				return err
			}
			row, err := repo.Get(prepared, model.ID(pinned.Assignment.ID))
			if err != nil {
				return err
			}
			if got := row.String("workspace_id"); got != defaultWorkspace {
				t.Errorf("%s pin workspace = %q, want the default workspace %q", kind, got, defaultWorkspace)
			}
			confined, err := store.ConfineWorkspace(prepared, raw, model.ID(defaultWorkspace))
			if err != nil {
				return err
			}
			stored, err := authority.ResolveSkillsTarget(prepared, confined, owner, target, false)
			if err != nil || stored.WorkspaceID.String() != defaultWorkspace {
				t.Errorf("%s through the default workspace's scope: %+v, %v", kind, stored, err)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	// The workspace's one contents read lists both pins.
	code, body := c.do("GET", "/v1/workspaces/"+defaultWorkspace+"/contents", c.token, nil)
	var contents struct {
		Kinds []struct {
			Kind  string `json:"kind"`
			Count int    `json:"count"`
		} `json:"kinds"`
	}
	if err := json.Unmarshal(body, &contents); err != nil || code != http.StatusOK {
		t.Fatalf("workspace contents = %d %s", code, body)
	}
	pins := 0
	for _, kind := range contents.Kinds {
		if kind.Kind == string(skills.AssignmentKind) {
			pins = kind.Count
		}
	}
	if pins != 2 {
		t.Errorf("the default workspace's contents list %d skills assignments, want the group's and the agent's: %s", pins, body)
	}
}

// The authority reads each row through the request's own scope and asks the
// target's native permission, not the skills one: a principal confined to one
// workspace never reaches a group or agent in another, and a viewer reads a
// group's assignments but cannot write them.

func TestSkillsCatalogPackForbidAppliesToAssignment(t *testing.T) {
	c := bootSkillsComposition(t)
	revision := c.installPack("denied-pack")
	other := c.installPack("allowed-pack")
	tenant, _ := model.ParseTenantID(c.tenant)
	var pack string
	if err := c.eng.store.View(context.Background(), tenant, func(sc store.Scope) error {
		r, err := skills.ReadAssignmentRevision(context.Background(), sc, revision)
		pack = r.PackID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	source := `forbid(principal, action, resource == Resource::"` + pack + `") when { context.permission == "skills:catalog:read" };`
	testsupport.SeedCedar(t, c.eng.store, model.TenantID(c.tenant), source, c.eng.nhiEnforcer)
	if code, raw := c.do("GET", "/v1/m/skills/packs/"+pack, c.token, nil); code != http.StatusNotFound {
		t.Fatalf("read forbidden pack = %d %s", code, raw)
	}
	group := c.agentGroup("pack-policy")
	if code, raw := c.assign(c, "agent_group", group, revision); code != http.StatusNotFound {
		t.Fatalf("pin forbidden pack = %d %s", code, raw)
	}
	if code, raw := c.assign(c, "agent_group", group, other); code != http.StatusCreated {
		t.Fatalf("pin allowed sibling = %d %s", code, raw)
	}
}

func TestSkillsConfinedAdminRemovesDeletedDirectoryTargetPin(t *testing.T) {
	c := bootSkillsComposition(t)
	revision := c.installPack("orphan-pack")
	tenant, _ := model.ParseTenantID(c.tenant)
	home := c.workspaceID()
	admin := c.member("orphan-admin@x.io", "admin")
	c.confineMember(admin, model.ID(home))
	ctx := context.Background()
	var foreignWorkspace model.ID
	if err := c.eng.store.Mutate(ctx, tenant, func(sc store.Scope) error {
		row, err := sc.Workspaces().Create(ctx, model.Workspace{Name: "Foreign", Slug: "foreign", Status: model.StatusActive})
		foreignWorkspace = row.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"agent_group", "agent"} {
		t.Run(kind, func(t *testing.T) {
			id := ""
			if err := c.eng.store.Mutate(ctx, tenant, func(sc store.Scope) error {
				if kind == "agent_group" {
					row, err := sc.AgentGroups().Create(ctx, model.AgentGroup{WorkspaceID: model.ID(home), Name: "orphan", Slug: "orphan", Status: model.StatusActive})
					id = row.ID.String()
					return err
				}
				row, err := sc.Agents().Create(ctx, model.Agent{WorkspaceID: model.ID(home), Name: "orphan", Kind: "claude-code", ExternalID: "orphan", Status: model.StatusActive})
				id = row.ID.String()
				return err
			}); err != nil {
				t.Fatal(err)
			}
			code, raw := c.assign(admin, kind, id, revision)
			var pinned skills.AssignmentResult
			if code != http.StatusCreated || json.Unmarshal(raw, &pinned) != nil {
				t.Fatalf("pin = %d %s", code, raw)
			}
			if err := c.eng.store.Mutate(ctx, tenant, func(sc store.Scope) error {
				if kind == "agent_group" {
					return sc.AgentGroups().Delete(ctx, model.ID(id))
				}
				return sc.Agents().Delete(ctx, model.ID(id))
			}); err != nil {
				t.Fatal(err)
			}
			if code, raw := admin.doHeader("DELETE", "/v1/m/skills/assignments/"+pinned.Assignment.ID, admin.token, strconv.FormatInt(pinned.Assignment.Version, 10), nil); code != http.StatusOK {
				t.Fatalf("unpin deleted %s = %d %s", kind, code, raw)
			}
			if err := c.eng.store.Mutate(ctx, tenant, func(sc store.Scope) error {
				if kind == "agent_group" {
					row, err := sc.AgentGroups().Create(ctx, model.AgentGroup{WorkspaceID: foreignWorkspace, Name: "foreign-orphan", Slug: "foreign-orphan", Status: model.StatusActive})
					id = row.ID.String()
					return err
				}
				row, err := sc.Agents().Create(ctx, model.Agent{WorkspaceID: foreignWorkspace, Name: "foreign-orphan", Kind: "claude-code", ExternalID: "foreign-orphan", Status: model.StatusActive})
				id = row.ID.String()
				return err
			}); err != nil {
				t.Fatal(err)
			}
			code, raw = c.assign(c, kind, id, revision)
			if code != http.StatusCreated || json.Unmarshal(raw, &pinned) != nil {
				t.Fatalf("foreign pin = %d %s", code, raw)
			}
			if err := c.eng.store.Mutate(ctx, tenant, func(sc store.Scope) error {
				if kind == "agent_group" {
					return sc.AgentGroups().Delete(ctx, model.ID(id))
				}
				return sc.Agents().Delete(ctx, model.ID(id))
			}); err != nil {
				t.Fatal(err)
			}
			path := "/v1/m/skills/assignments/" + pinned.Assignment.ID
			version := strconv.FormatInt(pinned.Assignment.Version, 10)
			if code, raw := admin.doHeader("DELETE", path, admin.token, version, nil); code != http.StatusNotFound {
				t.Fatalf("foreign orphan cleanup = %d %s", code, raw)
			}
			if code, raw := c.doHeader("DELETE", path, c.token, version, nil); code != http.StatusOK {
				t.Fatalf("owner orphan cleanup = %d %s", code, raw)
			}
		})
	}
}

func TestSkillsExistingPinPreparationKeepsAssignmentPolicyQuestion(t *testing.T) {
	c := bootSkillsComposition(t)
	revision := c.installPack("assignment-policy")
	group := c.agentGroup("assignment-policy")
	code, raw := c.assign(c, "agent_group", group, revision)
	var pinned skills.AssignmentResult
	if code != http.StatusCreated || json.Unmarshal(raw, &pinned) != nil {
		t.Fatalf("pin = %d %s", code, raw)
	}
	ctx := context.Background()
	tenant, _ := model.ParseTenantID(c.tenant)
	principal, err := c.eng.authr.Authenticate(ctx, c.token)
	if err != nil {
		t.Fatal(err)
	}
	source := `forbid(principal, action, resource == Resource::"` + pinned.Assignment.ID + `") when { context.permission == "skills:assignment:write" };`
	testsupport.SeedCedar(t, c.eng.store, model.TenantID(c.tenant), source, c.eng.nhiEnforcer)
	authority := skillsTargetAuthority{principals: c.eng.authr, st: c.eng.store, authz: c.eng.authz, sessions: c.eng.sessionsMod}
	mc := api.ModuleContext{Tenant: tenant, Principal: principal, Resource: auth.ResourceAttrs{Kind: "skills:assignment", ID: pinned.Assignment.ID, WorkspaceID: model.ID(c.workspaceID())}}
	for _, variant := range []string{"active", "orphan"} {
		t.Run(variant, func(t *testing.T) {
			if variant == "orphan" {
				if err := c.eng.store.Mutate(ctx, tenant, func(sc store.Scope) error { return sc.AgentGroups().Delete(ctx, model.ID(group)) }); err != nil {
					t.Fatal(err)
				}
			}
			_, release, err := authority.PrepareSkillsTarget(ctx, mc, pinned.Assignment.Target, true)
			defer release()
			if !errors.Is(err, auth.ErrRouteDenied) {
				t.Fatalf("%s assignment denied after admission: %v", variant, err)
			}
		})
	}
}

// Delay only the genuine store barrier, after it has acquired every real fact.
type skillsExpiryBarrierScope struct {
	store.Scope
	until time.Time
}

func (s skillsExpiryBarrierScope) LockDirectoryAuthoritySnapshot(ctx context.Context, bundle store.AuthoritySnapshotBundle) error {
	if err := s.Scope.(store.DirectoryAuthoritySnapshotLocker).LockDirectoryAuthoritySnapshot(ctx, bundle); err != nil {
		return err
	}
	timer := time.NewTimer(time.Until(s.until) + time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func TestSkillsTargetBarrierCannotExtendAuthorityLifetime(t *testing.T) {
	c := bootSkillsComposition(t)
	ctx := context.Background()
	tenant, _ := model.ParseTenantID(c.tenant)
	principal, err := c.eng.authr.Authenticate(ctx, c.token)
	if err != nil {
		t.Fatal(err)
	}
	target := skills.Target{Kind: "agent_group", ID: c.agentGroup("expiry-fence")}
	authority := skillsTargetAuthority{principals: c.eng.authr, st: c.eng.store, authz: c.eng.authz, sessions: c.eng.sessionsMod}
	bounded, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	prepared, release, err := authority.PrepareSkillsTarget(bounded, api.ModuleContext{Tenant: tenant, Principal: principal, Resource: auth.ResourceAttrs{Kind: "agent_group", ID: target.ID}}, target, true)
	defer release()
	if err != nil {
		t.Fatal(err)
	}
	value := prepared.Value(skillsPreparedTargetKey{}).(skillsPreparedTarget)
	metadata, err := value.authority.MetadataFor(time.Now(), value.request)
	if err != nil {
		t.Fatal(err)
	}
	// The HTTP handler's original context can be unbounded, so only the retained
	// evidence window protects against an unexpectedly slow store acquisition.
	prepared = context.WithValue(ctx, skillsPreparedTargetKey{}, value)
	err = c.eng.store.Mutate(prepared, tenant, func(sc store.Scope) error {
		_, err := authority.ResolveSkillsTarget(prepared, skillsExpiryBarrierScope{Scope: sc, until: metadata.FreshUntil}, principal, target, true)
		return err
	})
	if !errors.Is(err, auth.ErrRouteUndecided) {
		t.Fatalf("write after barrier outlives evidence = %v", err)
	}
}

// The admission locator and Go's struct decoder must never select different
// targets, even when an alias follows the authorized canonical field.
func TestSkillsAssignmentRejectsMixedCaseTargetAliases(t *testing.T) {
	c := bootSkillsComposition(t)
	revision := c.installPack("alias-pack")
	allowed, denied := c.agentGroup("allowed"), c.agentGroup("denied")
	source := `forbid(principal, action, resource == Resource::"` + denied + `") when { context.permission == "skills:assignment:write" };`
	testsupport.SeedCedar(t, c.eng.store, model.TenantID(c.tenant), source, c.eng.nhiEnforcer)
	if code, raw := c.assign(c, "agent_group", denied, revision); code != http.StatusNotFound {
		t.Fatalf("canonical forbidden target = %d %s", code, raw)
	}
	for _, tc := range []struct{ name, fields string }{
		{"id after", fmt.Sprintf(`"target_kind":"agent_group","target_id":%q,"TARGET_ID":%q`, allowed, denied)},
		{"id before", fmt.Sprintf(`"target_kind":"agent_group","TARGET_ID":%q,"target_id":%q`, denied, allowed)},
		{"kind after", fmt.Sprintf(`"target_kind":"agent_group","TARGET_KIND":"agent","target_id":%q`, allowed)},
		{"kind before", fmt.Sprintf(`"TARGET_KIND":"agent","target_kind":"agent_group","target_id":%q`, allowed)},
		{"escaped id alias", fmt.Sprintf(`"target_kind":"agent_group","target_id":%q,"\u0054ARGET_ID":%q`, allowed, denied)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := json.RawMessage(`{` + tc.fields + fmt.Sprintf(`,"pack_revision_id":%q}`, revision))
			if code, raw := c.do("POST", "/v1/m/skills/assignments", c.token, body); code != http.StatusBadRequest {
				t.Errorf("ambiguous target = %d %s, want 400", code, raw)
			}
		})
	}
	for _, id := range []string{allowed, denied} {
		code, raw := c.do("GET", "/v1/m/skills/assignments?target_kind=agent_group&target_id="+id, c.token, nil)
		var list struct {
			Items []skills.Assignment `json:"items"`
		}
		if err := json.Unmarshal(raw, &list); err != nil || code != http.StatusOK || len(list.Items) != 0 {
			t.Errorf("refused requests left assignments = %d %s", code, raw)
		}
	}
	if code, raw := c.assign(c, "agent_group", allowed, revision); code != http.StatusCreated {
		t.Fatalf("canonical allowed target = %d %s", code, raw)
	}
}

func TestSkillsCatalogEntriesAppearInDefaultWorkspaceContents(t *testing.T) {
	c := bootSkillsComposition(t)
	c.installPack("contents-pack")
	defaultID := c.workspaceID()
	code, raw := c.do("POST", "/v1/workspaces", c.token, map[string]any{"name": "Other", "slug": "other"})
	if code != http.StatusCreated {
		t.Fatalf("create workspace = %d %s", code, raw)
	}
	otherID := decodeField(t, raw, "id")
	for _, tc := range []struct {
		id   string
		want int
	}{{defaultID, 1}, {otherID, 0}} {
		code, raw := c.do("GET", "/v1/workspaces/"+tc.id+"/contents", c.token, nil)
		var contents struct {
			Kinds []struct {
				Kind  string `json:"kind"`
				Count int    `json:"count"`
			} `json:"kinds"`
		}
		if err := json.Unmarshal(raw, &contents); err != nil || code != http.StatusOK {
			t.Fatalf("contents = %d %s", code, raw)
		}
		counts := map[string]int{}
		for _, kind := range contents.Kinds {
			counts[kind.Kind] = kind.Count
		}
		for _, kind := range []model.Kind{skills.PackKind, skills.RevisionKind} {
			if got, present := counts[string(kind)]; !present || got != tc.want {
				t.Errorf("workspace %s catalog %s = %d present=%v, want %d: %s", tc.id, kind, got, present, tc.want, raw)
			}
		}
	}
}

func TestSkillsPreparedTargetMustMatchAdmittedNativeTarget(t *testing.T) {
	c := bootSkillsComposition(t)
	ctx := context.Background()
	tenant, err := model.ParseTenantID(c.tenant)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := c.eng.authr.Authenticate(ctx, c.token)
	if err != nil {
		t.Fatal(err)
	}
	authority := skillsTargetAuthority{principals: c.eng.authr, st: c.eng.store, authz: c.eng.authz, sessions: c.eng.sessionsMod}
	target := skills.Target{Kind: "agent_group", ID: c.agentGroup("admitted")}
	for _, resource := range []auth.ResourceAttrs{{Kind: "agent_group", ID: model.NewID().String()}, {Kind: "agent", ID: target.ID}} {
		_, release, err := authority.PrepareSkillsTarget(ctx, api.ModuleContext{Tenant: tenant, Principal: principal, Resource: resource, BodyEntityFields: map[string]string{"target_kind": resource.Kind, "target_id": resource.ID}}, target, true)
		release()
		if !errors.Is(err, auth.ErrRouteDenied) {
			t.Errorf("decoded target differs from admitted resource %+v: %v, want denial", resource, err)
		}
	}
}

func TestSkillsPreparedTargetMustMatchAdmittedTemplateAndSession(t *testing.T) {
	c := bootSkillsComposition(t)
	ctx := context.Background()
	tenant, err := model.ParseTenantID(c.tenant)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := c.eng.authr.Authenticate(ctx, c.token)
	if err != nil {
		t.Fatal(err)
	}
	authority := skillsTargetAuthority{principals: c.eng.authr, st: c.eng.store, authz: c.eng.authz, sessions: c.eng.sessionsMod}
	for _, target := range []skills.Target{{Kind: "template", ID: c.template("admitted")}, {Kind: "session", ID: model.NewID().String()}} {
		for _, fields := range []map[string]string{
			{"target_kind": target.Kind, "target_id": model.NewID().String()},
			{"target_kind": "agent", "target_id": target.ID},
			{"target_id": target.ID},
		} {
			_, release, err := authority.PrepareSkillsTarget(ctx, api.ModuleContext{Tenant: tenant, Principal: principal, Resource: auth.ResourceFor("skills:assignment:write"), BodyEntityFields: fields}, target, true)
			release()
			if !errors.Is(err, auth.ErrRouteDenied) {
				t.Errorf("%s decoded target differs from admitted fields: %v, want denial", target.Kind, err)
			}
		}
	}
}

func TestSkillsAssignmentForSessionUsesAdmittedRunReference(t *testing.T) {
	c := bootSkillsComposition(t)
	ctx := context.Background()
	revision := c.installPack("session-locator")
	target := skills.Target{Kind: "session", ID: model.NewID().String()}
	workspace := c.workspaceID()
	tenant, err := model.ParseTenantID(c.tenant)
	if err != nil {
		t.Fatal(err)
	}
	var rowID string
	if err := c.eng.store.Mutate(ctx, tenant, func(sc store.Scope) error {
		runs, err := sc.Ext("sessions.run")
		if err != nil {
			return err
		}
		row, err := runs.Create(ctx, model.Record{"run_ref": target.ID, "transport": "stream-json", "permission_mode": "", "isolation": "native", "state": "stopped", "last_event_seq": int64(0), "authz_workspace_id": workspace})
		rowID = row.String(model.ColID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if rowID == target.ID {
		t.Fatal("fixture run reference must differ from its primary row ID")
	}
	if code, raw := c.assign(c, target.Kind, target.ID, revision); code != http.StatusCreated {
		t.Fatalf("session pin by public run reference = %d %s", code, raw)
	}
}

// A session target is authorized against its stored row ID, the resource the
// native session routes name, not the public run reference the request carries.
func TestSkillsSessionForbidOnStoredIDGovernsItsPinsThroughHTTP(t *testing.T) {
	c := bootSkillsComposition(t)
	ctx := context.Background()
	revision, second := c.installPack("session-forbid"), c.installPack("session-forbid-second")
	tenant, err := model.ParseTenantID(c.tenant)
	if err != nil {
		t.Fatal(err)
	}
	workspace := c.workspaceID()
	refs := map[string]string{"denied": model.NewID().String(), "sibling": model.NewID().String()}
	rows := map[string]string{}
	if err := c.eng.store.Mutate(ctx, tenant, func(sc store.Scope) error {
		runs, err := sc.Ext("sessions.run")
		if err != nil {
			return err
		}
		for name, ref := range refs {
			row, err := runs.Create(ctx, model.Record{"run_ref": ref, "transport": "stream-json", "permission_mode": "", "isolation": "native", "state": "stopped", "last_event_seq": int64(0), "authz_workspace_id": workspace})
			if err != nil {
				return err
			}
			if rows[name] = row.String(model.ColID); rows[name] == ref {
				return fmt.Errorf("fixture run reference must differ from its primary row ID")
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	pins := map[string]skills.Assignment{}
	for name, ref := range refs {
		code, raw := c.assign(c, "session", ref, revision)
		var result skills.AssignmentResult
		if code != http.StatusCreated || json.Unmarshal(raw, &result) != nil {
			t.Fatalf("owner pins %s session = %d %s", name, code, raw)
		}
		pins[name] = result.Assignment
	}
	source := `forbid(principal, action, resource == Resource::"` + rows["denied"] + `") when { context.permission == "sessions:run:read" || context.permission == "sessions:run:write" };`
	testsupport.SeedCedar(t, c.eng.store, tenant, source, c.eng.nhiEnforcer)

	listed := func(name string) int {
		t.Helper()
		code, _ := c.do("GET", "/v1/m/skills/assignments?target_kind=session&target_id="+refs[name], c.token, nil)
		return code
	}
	if code := listed("denied"); code != http.StatusNotFound {
		t.Errorf("list pins of the forbidden session = %d, want 404", code)
	}
	if code := listed("sibling"); code != http.StatusOK {
		t.Errorf("list pins of the sibling session = %d", code)
	}
	code, raw := c.do("GET", "/v1/m/skills/packs/"+pins["sibling"].PackID+"/assignments", c.token, nil)
	var page struct {
		Items []skills.Assignment `json:"items"`
	}
	if code != http.StatusOK || json.Unmarshal(raw, &page) != nil || len(page.Items) != 1 || page.Items[0].ID != pins["sibling"].ID {
		t.Errorf("pack uses = %d %s, want only the sibling session's pin", code, raw)
	}
	if code, raw := c.assign(c, "session", refs["denied"], second); code != http.StatusNotFound {
		t.Errorf("pin the forbidden session = %d %s", code, raw)
	}
	if code, raw := c.assign(c, "session", refs["sibling"], second); code != http.StatusCreated {
		t.Errorf("pin the sibling session = %d %s", code, raw)
	}
	for name, want := range map[string]int{"denied": http.StatusNotFound, "sibling": http.StatusOK} {
		pin := pins[name]
		path := "/v1/m/skills/assignments/" + pin.ID
		body := skills.AssignmentRequest{Target: pin.Target, RevisionID: revision, Members: []string{"research"}}
		if code, raw := c.doHeader("PUT", path, c.token, strconv.FormatInt(pin.Version, 10), body); code != want {
			t.Errorf("update the %s session's pin = %d %s, want %d", name, code, raw, want)
		}
		version := pin.Version
		if want == http.StatusOK {
			version++
		}
		if code, raw := c.doHeader("DELETE", path, c.token, strconv.FormatInt(version, 10), nil); code != want {
			t.Errorf("unpin the %s session = %d %s, want %d", name, code, raw, want)
		}
	}
}
