// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// skillsComposition boots the real composition root with one owner and one
// organization, and returns a caller bound to that tenant.
type skillsComposition struct {
	t      *testing.T
	eng    *engine
	h      http.Handler
	token  string
	tenant string
}

func bootSkillsComposition(t *testing.T) skillsComposition {
	t.Helper()
	eng, err := boot(context.Background(), bootConfig{DataDir: t.TempDir(), Engine: "sqlite", DSN: ":memory:", Version: "test"})
	if err != nil {
		t.Fatalf("boot the composition root: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	c := skillsComposition{t: t, eng: eng, h: eng.api.Handler()}
	setup, _, err := eng.setupTok.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if code, raw := c.do("POST", "/v1/setup", "", map[string]any{"token": setup, "email": "root@x.io", "password": "supersecret1"}); code != http.StatusCreated {
		t.Fatalf("setup = %d %s", code, raw)
	}
	code, raw := c.do("POST", "/v1/auth/login", "", map[string]any{"email": "root@x.io", "password": "supersecret1"})
	if code != http.StatusOK {
		t.Fatalf("login = %d %s", code, raw)
	}
	c.token = decodeField(t, raw, "token")
	code, raw = c.do("POST", "/v1/system/orgs", c.token, map[string]any{"name": "acme", "slug": "acme"})
	if code != http.StatusCreated {
		t.Fatalf("create org = %d %s", code, raw)
	}
	c.tenant = decodeField(t, raw, "tenant_id")
	return c
}

func decodeField(t *testing.T, raw []byte, field string) string {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	value, _ := body[field].(string)
	if value == "" {
		t.Fatalf("no %q in %s", field, raw)
	}
	return value
}

func (c skillsComposition) do(method, path, token string, body any) (int, []byte) {
	c.t.Helper()
	return c.doHeader(method, path, token, "", body)
}

// doHeader is do with an If-Match version, for the routes that fence on one.
func (c skillsComposition) doHeader(method, path, token, ifMatch string, body any) (int, []byte) {
	c.t.Helper()
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			c.t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(payload))
	ctx, cancel := context.WithTimeout(req.Context(), 10*time.Second)
	defer cancel()
	req = req.WithContext(ctx)
	req.RemoteAddr = "10.0.0.1:1234"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if c.tenant != "" && token != "" {
		req.Header.Set("X-Olivares-Tenant", c.tenant)
	}
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

// member onboards a person with role into the tenant and returns a caller that
// is logged in as them.
func (c skillsComposition) member(email, role string) skillsComposition {
	c.t.Helper()
	const password = "skills-member-pass"
	if code, raw := c.do("POST", "/v1/onboard", c.token, map[string]any{"email": email, "role": role, "mode": "password", "password": password}); code != http.StatusCreated {
		c.t.Fatalf("onboard %s = %d %s", email, code, raw)
	}
	code, raw := c.do("POST", "/v1/auth/login", "", map[string]any{"email": email, "password": password})
	if code != http.StatusOK {
		c.t.Fatalf("login %s = %d %s", email, code, raw)
	}
	c.token = decodeField(c.t, raw, "token")
	return c
}

// template creates a session template and returns its ID.
func (c skillsComposition) template(name string) string {
	c.t.Helper()
	code, raw := c.do("POST", "/v1/m/sessions/templates", c.token, map[string]any{"name": name})
	if code != http.StatusCreated {
		c.t.Fatalf("create template = %d %s", code, raw)
	}
	return decodeField(c.t, raw, "id")
}

func (c skillsComposition) assign(who skillsComposition, kind, id, revision string) (int, []byte) {
	c.t.Helper()
	return who.do("POST", "/v1/m/skills/assignments", who.token, map[string]any{"target_kind": kind, "target_id": id, "pack_revision_id": revision})
}

// installPack publishes a one-skill pack and returns its first revision ID.
func (c skillsComposition) installPack(name string) string {
	c.t.Helper()
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	file, err := zw.Create("research/SKILL.md")
	if err != nil {
		c.t.Fatal(err)
	}
	if _, err := file.Write([]byte("---\nname: research\ndescription: Review primary sources.\nlicense: MIT\n---\nRead the cited source.\n")); err != nil {
		c.t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		c.t.Fatal(err)
	}
	var form bytes.Buffer
	mw := multipart.NewWriter(&form)
	_ = mw.WriteField("name", name)
	_ = mw.WriteField("format", "zip")
	part, err := mw.CreateFormFile("archive", "pack.zip")
	if err != nil {
		c.t.Fatal(err)
	}
	_, _ = part.Write(archive.Bytes())
	_ = mw.Close()
	req := httptest.NewRequest("POST", "/v1/m/skills/packs", &form)
	req.RemoteAddr = "10.0.0.1:1234"
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-Olivares-Tenant", c.tenant)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Idempotency-Key", "install-"+name)
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		c.t.Fatalf("install pack = %d %s", rec.Code, rec.Body.String())
	}
	var result struct {
		Revision struct {
			ID string `json:"id"`
		} `json:"revision"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil || result.Revision.ID == "" {
		c.t.Fatalf("install result %s: %v", rec.Body.String(), err)
	}
	return result.Revision.ID
}

// workspaceID is the tenant's default workspace.
func (c skillsComposition) workspaceID() string {
	c.t.Helper()
	code, raw := c.do("GET", "/v1/workspaces", c.token, nil)
	if code != http.StatusOK {
		c.t.Fatalf("list workspaces = %d %s", code, raw)
	}
	var list struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil || len(list.Items) == 0 {
		c.t.Fatalf("workspaces %s: %v", raw, err)
	}
	return list.Items[0].ID
}

// A workspace's skill assignment is saved by the shipped binary: the composition
// root connects the sessions-owned target authority to the skills module.
func TestSkillsAssignmentForWorkspaceIsWiredInComposition(t *testing.T) {
	c := bootSkillsComposition(t)
	revision := c.installPack("workspace-pack")
	workspace := c.workspaceID()

	code, raw := c.do("POST", "/v1/m/skills/assignments", c.token, map[string]any{
		"target_kind": "workspace", "target_id": workspace, "pack_revision_id": revision,
	})
	if code != http.StatusCreated {
		t.Fatalf("assign to workspace = %d %s", code, raw)
	}
	code, raw = c.do("GET", "/v1/m/skills/assignments?target_kind=workspace&target_id="+workspace, c.token, nil)
	var listed struct {
		Items []struct {
			Members []string `json:"members"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil || code != http.StatusOK || len(listed.Items) != 1 || len(listed.Items[0].Members) != 1 || listed.Items[0].Members[0] != "research" {
		t.Fatalf("list after assign = %d %s", code, raw)
	}
}

// Holding the skills write permission is not enough: each target keeps its own
// native permission. An editor writes templates but does not administer a
// workspace, and an absent target is not found.
func TestSkillsAssignmentTargetsKeepTheirNativePermission(t *testing.T) {
	c := bootSkillsComposition(t)
	revision := c.installPack("native-pack")
	workspace := c.workspaceID()
	ownerTemplate, editorTemplate := c.template("owner-review"), c.template("editor-review")
	editor := c.member("editor@x.io", "editor")

	if code, raw := c.assign(c, "template", ownerTemplate, revision); code != http.StatusCreated {
		t.Fatalf("owner assigns to a template = %d %s", code, raw)
	}
	if code, raw := c.assign(editor, "template", editorTemplate, revision); code != http.StatusCreated {
		t.Fatalf("editor assigns to a template = %d %s", code, raw)
	}
	if code, raw := c.assign(editor, "workspace", workspace, revision); code != http.StatusNotFound {
		t.Fatalf("editor assigns to a workspace it cannot administer = %d %s", code, raw)
	}
	if code, raw := c.assign(c, "workspace", workspace, revision); code != http.StatusCreated {
		t.Fatalf("owner assigns to a workspace = %d %s", code, raw)
	}
	if code, raw := c.assign(c, "template", model.NewID().String(), revision); code != http.StatusNotFound {
		t.Fatalf("assign to an absent template = %d %s", code, raw)
	}
	if code, raw := c.assign(c, "session", model.NewID().String(), revision); code != http.StatusNotFound {
		t.Fatalf("assign to an absent session = %d %s", code, raw)
	}
}

// Connecting the target authority must not take pack retirement away: an
// unassigned pack is still removed, and an assigned one is still refused.
func TestSkillsPackRetirementSurvivesTargetAuthority(t *testing.T) {
	c := bootSkillsComposition(t)
	workspace := c.workspaceID()
	version := func(packID string) string {
		code, raw := c.do("GET", "/v1/m/skills/packs/"+packID, c.token, nil)
		var detail struct {
			Pack struct {
				Version int64 `json:"version"`
			} `json:"pack"`
		}
		if err := json.Unmarshal(raw, &detail); err != nil || code != http.StatusOK {
			t.Fatalf("read pack = %d %s", code, raw)
		}
		return strconv.FormatInt(detail.Pack.Version, 10)
	}
	packOf := func(revision string) string {
		code, raw := c.do("GET", "/v1/m/skills/packs", c.token, nil)
		if code != http.StatusOK {
			t.Fatalf("list packs = %d %s", code, raw)
		}
		var list struct {
			Items []struct {
				ID               string `json:"id"`
				LatestRevisionID string `json:"latest_revision_id"`
			} `json:"items"`
		}
		if err := json.Unmarshal(raw, &list); err != nil {
			t.Fatal(err)
		}
		for _, pack := range list.Items {
			if pack.LatestRevisionID == revision {
				return pack.ID
			}
		}
		t.Fatalf("no pack for revision %s in %s", revision, raw)
		return ""
	}

	free := packOf(c.installPack("free-pack"))
	if code, raw := c.doHeader("DELETE", "/v1/m/skills/packs/"+free, c.token, version(free), nil); code != http.StatusOK {
		t.Fatalf("retire an unassigned pack = %d %s", code, raw)
	}
	revision := c.installPack("pinned-pack")
	pinned := packOf(revision)
	if code, raw := c.assign(c, "workspace", workspace, revision); code != http.StatusCreated {
		t.Fatalf("assign = %d %s", code, raw)
	}
	code, raw := c.doHeader("DELETE", "/v1/m/skills/packs/"+pinned, c.token, version(pinned), nil)
	if code != http.StatusConflict || !strings.Contains(string(raw), "pack_in_use") {
		t.Fatalf("retire an assigned pack = %d %s", code, raw)
	}
}
