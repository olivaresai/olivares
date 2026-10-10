// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package skills_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/skills"
)

type catalogHarness struct {
	server    http.Handler
	token     string
	store     store.Store
	tenant    model.TenantID
	module    *skills.Module
	artifacts string
}

func TestSkillsCatalogLargeRevisionHistoryPagesWithoutLosingManifests(t *testing.T) {
	h := catalog(t)
	names, contents := []string{"research/SKILL.md"}, []string{harmless}
	for i := 0; i < 2000; i++ {
		names = append(names, "research/"+strings.Repeat("a", 220)+"/"+strings.Repeat("b", 220)+fmt.Sprintf("/%04d.md", i))
		contents = append(contents, "retained support")
	}
	fixture := archive(t, names, contents)
	first := h.upload(t, "large-history-1", "large", "", fixture)
	var installed skills.InstallResult
	if err := json.Unmarshal(first.Body.Bytes(), &installed); err != nil || first.Code != http.StatusCreated || first.Body.Len() <= 1<<20 {
		t.Fatalf("large publication: status=%d bytes=%d err=%v", first.Code, first.Body.Len(), err)
	}
	second := h.upload(t, "large-history-2", "large", installed.Pack.ID, fixture)
	var updated skills.InstallResult
	if err := json.Unmarshal(second.Body.Bytes(), &updated); err != nil || second.Code != http.StatusCreated {
		t.Fatalf("large update: status=%d err=%v", second.Code, err)
	}
	route := "/v1/m/skills/packs/" + installed.Pack.ID
	seen := map[string]bool{}
	cursor := ""
	for page := 0; page < 2; page++ {
		reply := h.request("GET", route+"?cursor="+url.QueryEscape(cursor), nil, "")
		var detail skills.PackDetail
		if err := json.Unmarshal(reply.Body.Bytes(), &detail); err != nil || reply.Code != http.StatusOK || len(detail.Revisions) != 1 {
			t.Fatalf("bounded revision page %d: status=%d revisions=%d err=%v", page, reply.Code, len(detail.Revisions), err)
		}
		rev := detail.Revisions[0]
		if len(rev.Manifest) != 2001 || seen[rev.ID] {
			t.Fatalf("revision manifest lost or repeated: %s files=%d", rev.ID, len(rev.Manifest))
		}
		seen[rev.ID] = true
		if detail.HasMore != (page == 0) || detail.HasMore && (detail.Cursor == "" || detail.Cursor == cursor) {
			t.Fatalf("pagination did not advance: %+v", detail.Pack)
		}
		cursor = detail.Cursor
	}
	if !seen[installed.Revision.ID] || !seen[updated.Revision.ID] {
		t.Fatal("immutable revision was dropped from history")
	}
}

func TestSkillsCatalogRemovalRetainsProvenanceAndRefusesPins(t *testing.T) {
	h := catalogOptions(t, skills.Options{Targets: workspaceAuthority{}})
	pack := h.install(t, "retire-pack", "retain these reviewed bytes")
	target := h.workspace(t, false)
	assigned := h.request("POST", "/v1/m/skills/assignments", skills.AssignmentRequest{Target: target, RevisionID: pack.Revision.ID}, "")
	var binding skills.AssignmentResult
	if err := json.Unmarshal(assigned.Body.Bytes(), &binding); err != nil || assigned.Code != http.StatusCreated {
		t.Fatalf("assign: %d %s", assigned.Code, assigned.Body.String())
	}
	route := "/v1/m/skills/packs/" + pack.Pack.ID
	current := h.request("GET", route, nil, "")
	var currentPack skills.PackDetail
	if err := json.Unmarshal(current.Body.Bytes(), &currentPack); err != nil || current.Code != http.StatusOK {
		t.Fatalf("current pack: %d %s", current.Code, current.Body.String())
	}
	version := strconv.FormatInt(currentPack.Pack.Version, 10)
	refused := h.request("DELETE", route, nil, version)
	if refused.Code != http.StatusConflict || !bytes.Contains(refused.Body.Bytes(), []byte("pack_in_use")) {
		t.Fatalf("referenced pack: %d %s", refused.Code, refused.Body.String())
	}
	unassigned := h.request("DELETE", "/v1/m/skills/assignments/"+binding.Assignment.ID, nil, strconv.FormatInt(binding.Assignment.Version, 10))
	if unassigned.Code != http.StatusOK {
		t.Fatalf("unassign: %d %s", unassigned.Code, unassigned.Body.String())
	}
	// A native target adapter is not enough to prove that old conversations no
	// longer reference the pack. Its runtime owner must fence that fact too.
	refused = h.request("DELETE", route, nil, version)
	if refused.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconnected recorded usage: %d %s", refused.Code, refused.Body.String())
	}

	catalogOnly := catalog(t)
	unbound := catalogOnly.install(t, "unbound-pack", "retain catalog content")
	route = "/v1/m/skills/packs/" + unbound.Pack.ID
	stale := catalogOnly.request("DELETE", route, nil, strconv.FormatInt(unbound.Pack.Version+1, 10))
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale removal: %d %s", stale.Code, stale.Body.String())
	}
	removed := catalogOnly.request("DELETE", route, nil, strconv.FormatInt(unbound.Pack.Version, 10))
	if removed.Code != http.StatusOK {
		t.Fatalf("remove catalog pack: %d %s", removed.Code, removed.Body.String())
	}
	detail := catalogOnly.request("GET", route, nil, "")
	var retained skills.PackDetail
	if err := json.Unmarshal(detail.Body.Bytes(), &retained); err != nil || detail.Code != http.StatusOK || retained.Pack.State != "retired" || len(retained.Revisions) != 1 {
		t.Fatalf("retained provenance: %d %s", detail.Code, detail.Body.String())
	}
	if _, err := os.Stat(filepath.Join(catalogOnly.artifacts, catalogOnly.tenant.String(), unbound.Revision.ID, "research", "SKILL.md")); err != nil {
		t.Fatalf("retained content: %v", err)
	}
}

type workspaceSnapshotFixture struct {
	version int64
	files   []skills.WorkspaceFile
}

func (f workspaceSnapshotFixture) ReadSkillsWorkspace(context.Context, api.ModuleContext, string, string) (skills.WorkspaceSnapshot, error) {
	return skills.WorkspaceSnapshot{RegistrationVersion: f.version, Files: f.files}, nil
}

func TestSkillsCatalogRegisteredFolderKeepsMemberAndRegistration(t *testing.T) {
	for _, version := range []int64{7, 0} {
		t.Run(strconv.FormatInt(version, 10), func(t *testing.T) {
			h := catalogOptions(t, skills.Options{Workspace: workspaceSnapshotFixture{version: version, files: []skills.WorkspaceFile{
				{Path: "SKILL.md", Mode: fs.FileMode(0644), Bytes: []byte(harmless)},
				{Path: "support.md", Mode: fs.FileMode(0644), Bytes: []byte("registered fixture")},
			}}})
			body, _ := json.Marshal(map[string]any{"name": "registered", "source": map[string]string{"kind": "workspace", "workspace_ref": model.NewID().String(), "directory": "packs/research"}})
			req := httptest.NewRequest("POST", "/v1/m/skills/packs", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+h.token)
			req.Header.Set("X-Olivares-Tenant", h.tenant.String())
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", "registered-folder")
			out := httptest.NewRecorder()
			h.server.ServeHTTP(out, req)
			if version == 0 {
				if out.Code != http.StatusBadRequest || !bytes.Contains(out.Body.Bytes(), []byte("source_changed")) {
					t.Fatalf("unverified registration: %d %s", out.Code, out.Body.String())
				}
				return
			}
			var result skills.InstallResult
			if err := json.Unmarshal(out.Body.Bytes(), &result); err != nil || out.Code != http.StatusCreated || result.Revision.Source.RegistrationVersion != version || len(result.Revision.Members) != 1 || result.Revision.Members[0].Directory != "research" {
				t.Fatalf("registered folder: %d %s", out.Code, out.Body.String())
			}
		})
	}
}

func catalog(t *testing.T) catalogHarness {
	return catalogOptions(t, skills.Options{})
}

func catalogOptions(t *testing.T, opts skills.Options) catalogHarness {
	t.Helper()
	auth.SetTestHashParams(auth.TestArgonMemKiB, auth.TestArgonTime, auth.TestArgonThreads)
	artifacts := filepath.Join(t.TempDir(), "artifacts")
	opts.ArtifactRoot = artifacts
	m := skills.New(opts)
	cfg := store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}
	if enginetest.PostgresAvailable(t) {
		pg := enginetest.IsolatedPostgresSplitOwner(t)
		cfg.Engine, cfg.DSN, cfg.OwnerDSN = store.EnginePostgres, pg.App, pg.Owner
		t.Log("skills catalog HTTP traffic uses the PostgreSQL application role; migrations use the separate owner")
	}
	st, err := engine.Open(context.Background(), cfg, m.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(context.Background(), func(sys store.SystemScope) error { _, err := sys.EnsureSystemTenant(context.Background()); return err }); err != nil {
		t.Fatal(err)
	}
	m.UseData(api.NewModuleData(st))
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := audit.NewSigner(key)
	if err != nil {
		t.Fatal(err)
	}
	setup := secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token"))
	plaintext, _, err := setup.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	srv, err := api.New(api.Options{Store: st, Authenticator: auth.NewAuthenticator(st, nil), Authorizer: auth.NewAuthorizer(nil), Signer: signer, SetupToken: setup, Version: "test", Modules: []api.Module{m}})
	if err != nil {
		t.Fatal(err)
	}
	h := catalogHarness{server: srv.Handler(), store: st, module: m, artifacts: artifacts}
	setupResponse := h.request("POST", "/v1/setup", map[string]string{"token": plaintext, "email": "root@example.test", "password": "fixture-password1"}, "")
	if setupResponse.Code != http.StatusCreated {
		t.Fatalf("setup: %d %s", setupResponse.Code, setupResponse.Body.String())
	}
	login := h.request("POST", "/v1/auth/login", map[string]string{"email": "root@example.test", "password": "fixture-password1"}, "")
	var session struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(login.Body.Bytes(), &session); err != nil || login.Code != http.StatusOK || session.Token == "" {
		t.Fatalf("login: %d %s", login.Code, login.Body.String())
	}
	h.token = session.Token
	org := h.request("POST", "/v1/system/orgs", map[string]string{"name": "Engineering", "slug": "engineering"}, "")
	var created struct {
		Tenant model.TenantID `json:"tenant_id"`
	}
	if err := json.Unmarshal(org.Body.Bytes(), &created); err != nil || org.Code != http.StatusCreated || created.Tenant == "" {
		t.Fatalf("organization: %d %s", org.Code, org.Body.String())
	}
	h.tenant = created.Tenant
	return h
}

func (h catalogHarness) request(method, route string, body any, version string) *httptest.ResponseRecorder {
	encoded, _ := json.Marshal(body)
	req := httptest.NewRequest(method, route, bytes.NewReader(encoded))
	req.Header.Set("Content-Type", "application/json")
	if h.token != "" {
		req.Header.Set("Authorization", "Bearer "+h.token)
	}
	if h.tenant != "" {
		req.Header.Set("X-Olivares-Tenant", h.tenant.String())
	}
	if version != "" {
		req.Header.Set("If-Match", version)
	}
	out := httptest.NewRecorder()
	h.server.ServeHTTP(out, req)
	return out
}
func (h catalogHarness) upload(t *testing.T, key, name, packID string, data []byte) *httptest.ResponseRecorder {
	t.Helper()
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	_ = w.WriteField("name", name)
	_ = w.WriteField("format", "zip")
	part, err := w.CreateFormFile("archive", "pack.zip")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(data)
	_ = w.Close()
	route := "/v1/m/skills/packs"
	if packID != "" {
		route += "/" + packID + "/revisions"
	}
	req := httptest.NewRequest("POST", route, &b)
	req.Header.Set("Authorization", "Bearer "+h.token)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Idempotency-Key", key)
	req.Header.Set("X-Olivares-Tenant", h.tenant.String())
	out := httptest.NewRecorder()
	h.server.ServeHTTP(out, req)
	return out
}
func TestSkillsCatalogPublishesImmutableRevisionAndReplays(t *testing.T) {
	h := catalog(t)
	fixture := archive(t, []string{"research/SKILL.md"}, []string{harmless})
	first := h.upload(t, "install-one", "engineering", "", fixture)
	if first.Code != http.StatusCreated {
		t.Fatalf("install: %d %s", first.Code, first.Body.String())
	}
	var result skills.InstallResult
	if err := json.Unmarshal(first.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.State != "catalog_published" || result.Revision.ID == "" || result.Revision.ManifestDigest == "" {
		t.Fatalf("result: %+v", result)
	}
	second := h.upload(t, "install-one", "engineering", "", fixture)
	if second.Code != http.StatusOK {
		t.Fatalf("replay: %d %s", second.Code, second.Body.String())
	}
	var replay skills.InstallResult
	_ = json.Unmarshal(second.Body.Bytes(), &replay)
	if result.Revision.ID != replay.Revision.ID {
		t.Fatal("idempotent retry published another revision")
	}
	changed := archive(t, []string{"research/SKILL.md"}, []string{harmless + "updated\n"})
	if conflict := h.upload(t, "install-one", "engineering", "", changed); conflict.Code != http.StatusConflict {
		t.Fatalf("changed replay: %d %s", conflict.Code, conflict.Body.String())
	}
	req := httptest.NewRequest("GET", "/v1/m/skills/packs/"+result.Pack.ID, nil)
	req.Header.Set("Authorization", "Bearer "+h.token)
	req.Header.Set("X-Olivares-Tenant", h.tenant.String())
	response := httptest.NewRecorder()
	h.server.ServeHTTP(response, req)
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(result.Revision.ID)) {
		t.Fatalf("readback: %d %s", response.Code, response.Body.String())
	}
	// Catalog publication and its semantic audit share the native transaction.
	err := h.store.View(context.Background(), h.tenant, func(sc store.Scope) error {
		found := 0
		err := sc.Audit().Walk(context.Background(), 1, func(row model.AuditEvent) error {
			if row.Action == "skills.catalog.publish" {
				found++
			}
			return nil
		})
		if err != nil {
			return err
		}
		if found != 1 {
			t.Fatalf("publication audit count = %d", found)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// No anonymous catalog read, even with a known identifier.
	denied := httptest.NewRecorder()
	h.server.ServeHTTP(denied, httptest.NewRequest("GET", "/v1/m/skills/packs/"+result.Pack.ID, nil))
	if denied.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous catalog: %d", denied.Code)
	}
}

func TestSkillsCatalogUpdateRetainsOriginalRetryResult(t *testing.T) {
	h := catalog(t)
	fixture := archive(t, []string{"research/SKILL.md"}, []string{harmless})
	first := h.upload(t, "initial", "engineering", "", fixture)
	if first.Code != http.StatusCreated {
		t.Fatalf("install: %d %s", first.Code, first.Body.String())
	}
	var initial skills.InstallResult
	if err := json.Unmarshal(first.Body.Bytes(), &initial); err != nil {
		t.Fatal(err)
	}
	changed := archive(t, []string{"research/SKILL.md"}, []string{harmless + "new reviewed instruction\n"})
	update := h.upload(t, "update", "", initial.Pack.ID, changed)
	if update.Code != http.StatusCreated {
		t.Fatalf("update: %d %s", update.Code, update.Body.String())
	}
	var next skills.InstallResult
	if err := json.Unmarshal(update.Body.Bytes(), &next); err != nil {
		t.Fatal(err)
	}
	if next.Revision.ID == initial.Revision.ID || next.Revision.Number != 2 {
		t.Fatal("update did not create a distinct numbered immutable revision")
	}
	replay := h.upload(t, "initial", "engineering", "", fixture)
	if replay.Code != http.StatusOK || replay.Body.String() != first.Body.String() {
		t.Fatalf("original retry changed after update: %d %s", replay.Code, replay.Body.String())
	}
	req := httptest.NewRequest("GET", "/v1/m/skills/packs/"+initial.Pack.ID, nil)
	req.Header.Set("Authorization", "Bearer "+h.token)
	req.Header.Set("X-Olivares-Tenant", h.tenant.String())
	response := httptest.NewRecorder()
	h.server.ServeHTTP(response, req)
	var detail skills.PackDetail
	if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || len(detail.Revisions) != 2 || detail.Pack.LatestRevisionID != next.Revision.ID {
		t.Fatalf("revision history: %d %+v", response.Code, detail)
	}
}

func TestSkillsCatalogUnsafeImportHasNoPublication(t *testing.T) {
	h := catalog(t)
	refused := h.upload(t, "unsafe", "engineering", "", archive(t, []string{"../research/SKILL.md"}, []string{harmless}))
	if refused.Code != http.StatusBadRequest {
		t.Fatalf("unsafe import: %d %s", refused.Code, refused.Body.String())
	}
	req := httptest.NewRequest("GET", "/v1/m/skills/packs", nil)
	req.Header.Set("Authorization", "Bearer "+h.token)
	req.Header.Set("X-Olivares-Tenant", h.tenant.String())
	response := httptest.NewRecorder()
	h.server.ServeHTTP(response, req)
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"items":[]`)) {
		t.Fatalf("unsafe import published a catalog pack: %d %s", response.Code, response.Body.String())
	}
}

func TestSkillsCatalogGitRetryReturnsRecordedResultWithoutRefetch(t *testing.T) {
	_, _, source := gitFixture(t)
	var offline atomic.Bool
	var fetches atomic.Int64
	g, _ := gitImporter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		if offline.Load() {
			http.Error(w, "source unavailable", http.StatusServiceUnavailable)
			return
		}
		source.ServeHTTP(w, r)
	}))
	h := catalogOptions(t, skills.Options{Git: &g})
	body := map[string]any{"name": "git-retry", "source": map[string]string{"kind": "git", "url": "https://example.com/fixture.git", "ref": "HEAD"}}
	send := func() *httptest.ResponseRecorder {
		encoded, _ := json.Marshal(body)
		req := httptest.NewRequest("POST", "/v1/m/skills/packs", bytes.NewReader(encoded))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+h.token)
		req.Header.Set("X-Olivares-Tenant", h.tenant.String())
		req.Header.Set("Idempotency-Key", "retry-recorded-source")
		out := httptest.NewRecorder()
		h.server.ServeHTTP(out, req)
		return out
	}
	first := send()
	if first.Code != http.StatusCreated {
		t.Fatalf("first import: %d %s", first.Code, first.Body.String())
	}
	before := fetches.Load()
	offline.Store(true)
	retry := send()
	if retry.Code != http.StatusOK || retry.Body.String() != first.Body.String() || fetches.Load() != before {
		t.Fatalf("recorded retry refetched source: %d %s, fetches %d -> %d", retry.Code, retry.Body.String(), before, fetches.Load())
	}
}

func TestSkillsCatalogTenantIsolationAndPublisherPermission(t *testing.T) {
	h := catalog(t)
	pack := h.install(t, "isolated", "fixture")
	org := h.request("POST", "/v1/system/orgs", map[string]string{"name": "Other", "slug": "other"}, "")
	var created struct {
		Tenant model.TenantID `json:"tenant_id"`
	}
	if err := json.Unmarshal(org.Body.Bytes(), &created); err != nil || org.Code != http.StatusCreated {
		t.Fatalf("other org: %d %s", org.Code, org.Body.String())
	}
	other := h
	other.tenant = created.Tenant
	hidden := other.request("GET", "/v1/m/skills/packs/"+pack.Pack.ID, nil, "")
	if hidden.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant pack: %d %s", hidden.Code, hidden.Body.String())
	}
	update := other.upload(t, "other-update", "", pack.Pack.ID, archive(t, []string{"research/SKILL.md"}, []string{harmless}))
	if update.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant revision: %d %s", update.Code, update.Body.String())
	}
	user := h.request("POST", "/v1/users", map[string]string{"email": "viewer@example.test", "password": "fixture-viewer1", "tenant": h.tenant.String(), "role": "viewer"}, "")
	if user.Code != http.StatusCreated {
		t.Fatalf("viewer: %d %s", user.Code, user.Body.String())
	}
	login := h.request("POST", "/v1/auth/login", map[string]string{"email": "viewer@example.test", "password": "fixture-viewer1"}, "")
	var session struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(login.Body.Bytes(), &session); err != nil || login.Code != http.StatusOK {
		t.Fatalf("viewer login: %d %s", login.Code, login.Body.String())
	}
	viewer := h
	viewer.token = session.Token
	published := viewer.upload(t, "viewer-publish", "forbidden", "", archive(t, []string{"research/SKILL.md"}, []string{harmless}))
	if published.Code != http.StatusForbidden {
		t.Fatalf("viewer published: %d %s", published.Code, published.Body.String())
	}
}
