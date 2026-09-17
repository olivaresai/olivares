// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
)

// channelAdministrationHTTPEstate is the fresh, explicitly activated estate the
// administrative journey starts from: setup, admin, two tenants, TWO workspaces
// in the administering tenant, an owner (core sessions:channel:admin), a
// steward the fixture grants ADMIN-only local access to, an editor (core
// read+write), a viewer (core read) and a stranger owner in the other tenant.
type channelAdministrationHTTPEstate struct {
	eng              *engine
	store            communicationHTTPTestStore
	tenant           model.TenantID
	other            model.TenantID
	adminToken       string
	owner            communicationHTTPTestUser
	steward          communicationHTTPTestUser
	editor           communicationHTTPTestUser
	viewer           communicationHTTPTestUser
	stranger         communicationHTTPTestUser
	workspace        model.ID
	sideWorkspace    model.ID
	foreignWorkspace string
}

func bootChannelAdministrationHTTPEstate(
	t *testing.T,
	storeEstate communicationHTTPTestStore,
) channelAdministrationHTTPEstate {
	t.Helper()
	eng := bootActivatedCommunicationHTTPTestEngine(t, storeEstate)
	setupToken, _, err := eng.setupTok.Ensure()
	if err != nil {
		t.Fatalf("setup token: %v", err)
	}
	setup := communicationHTTPTestRequest(t, eng, http.MethodPost, "/v1/setup", "", "",
		map[string]any{"token": setupToken, "email": "admin@k3-admin.test", "password": "k3-admin-admin-password"}, nil)
	if setup.status != http.StatusCreated {
		t.Fatalf("setup = %d: %s", setup.status, setup.raw)
	}
	login := communicationHTTPTestRequest(t, eng, http.MethodPost, "/v1/auth/login", "", "",
		map[string]any{"email": "admin@k3-admin.test", "password": "k3-admin-admin-password"}, nil)
	if login.status != http.StatusOK {
		t.Fatalf("admin login = %d: %s", login.status, login.raw)
	}
	admin := communicationHTTPTestDecode[struct {
		Token string `json:"token"`
	}](t, login)
	newOrg := func(name, slug string) model.TenantID {
		t.Helper()
		created := communicationHTTPTestRequest(t, eng, http.MethodPost, "/v1/system/orgs",
			admin.Token, "", map[string]any{"name": name, "slug": slug}, nil)
		if created.status != http.StatusCreated {
			t.Fatalf("create tenant %s = %d: %s", slug, created.status, created.raw)
		}
		return communicationHTTPTestDecode[struct {
			TenantID model.TenantID `json:"tenant_id"`
		}](t, created).TenantID
	}
	tenant := newOrg("K3 administration", "k3-administration")
	other := newOrg("K3 administration other", "k3-administration-other")

	owner := createCommunicationHTTPTestUser(t, eng, admin.Token, tenant, "owner@k3-admin.test", auth.RoleOwner)
	steward := createCommunicationHTTPTestUser(t, eng, admin.Token, tenant, "steward@k3-admin.test", auth.RoleOwner)
	editor := createCommunicationHTTPTestUser(t, eng, admin.Token, tenant, "editor@k3-admin.test", auth.RoleEditor)
	viewer := createCommunicationHTTPTestUser(t, eng, admin.Token, tenant, "viewer@k3-admin.test", auth.RoleViewer)
	stranger := createCommunicationHTTPTestUser(t, eng, admin.Token, other, "stranger@k3-admin.test", auth.RoleOwner)

	// Reopen after tenant bootstrap exactly as the configured installation does.
	if err := eng.Close(); err != nil {
		t.Fatalf("close bootstrapped estate: %v", err)
	}
	eng = bootCommunicationHTTPTestEngine(t, storeEstate)
	t.Cleanup(func() { _ = eng.Close() })
	if readiness, err := eng.sessionsMod.EvaluateCommunicationReadiness(context.Background()); err != nil || !readiness.Effective {
		t.Fatalf("communication readiness after bootstrap restart = %+v, err %v", readiness, err)
	}
	owner = loginCommunicationHTTPTestUser(t, eng, owner.id, "owner@k3-admin.test")
	steward = loginCommunicationHTTPTestUser(t, eng, steward.id, "steward@k3-admin.test")
	editor = loginCommunicationHTTPTestUser(t, eng, editor.id, "editor@k3-admin.test")
	viewer = loginCommunicationHTTPTestUser(t, eng, viewer.id, "viewer@k3-admin.test")
	stranger = loginCommunicationHTTPTestUser(t, eng, stranger.id, "stranger@k3-admin.test")
	stepUpCommunicationHTTPTestUser(t, eng, owner.token)
	stepUpCommunicationHTTPTestUser(t, eng, stranger.token)

	newWorkspace := func(token string, forTenant model.TenantID, name, slug string) model.ID {
		t.Helper()
		created := communicationHTTPTestRequest(t, eng, http.MethodPost, "/v1/workspaces", token, forTenant,
			map[string]any{"name": name, "slug": slug}, nil)
		if created.status != http.StatusCreated {
			t.Fatalf("create workspace %s = %d: %s", slug, created.status, created.raw)
		}
		id, err := model.ParseID(communicationHTTPTestDecode[struct {
			ID string `json:"id"`
		}](t, created).ID)
		if err != nil {
			t.Fatalf("workspace id %s: %v", slug, err)
		}
		return id
	}
	workspace := newWorkspace(owner.token, tenant, "K3 administration workspace", "k3-admin-ws")
	sideWorkspace := newWorkspace(owner.token, tenant, "K3 administration side", "k3-admin-side")
	foreign := newWorkspace(stranger.token, other, "Stranger workspace", "k3-admin-stranger")

	return channelAdministrationHTTPEstate{
		eng: eng, store: storeEstate, tenant: tenant, other: other, adminToken: admin.Token,
		owner: owner, steward: steward, editor: editor, viewer: viewer, stranger: stranger,
		workspace: workspace, sideWorkspace: sideWorkspace, foreignWorkspace: foreign.String(),
	}
}

// relogin re-establishes every operator's credential after an engine restart, so
// a journey that reopens the estate keeps the same identities without carrying a
// stale token across the boundary.
func (e *channelAdministrationHTTPEstate) relogin(t *testing.T) {
	t.Helper()
	e.owner = loginCommunicationHTTPTestUser(t, e.eng, e.owner.id, "owner@k3-admin.test")
	e.steward = loginCommunicationHTTPTestUser(t, e.eng, e.steward.id, "steward@k3-admin.test")
	e.editor = loginCommunicationHTTPTestUser(t, e.eng, e.editor.id, "editor@k3-admin.test")
	e.viewer = loginCommunicationHTTPTestUser(t, e.eng, e.viewer.id, "viewer@k3-admin.test")
}

func (e channelAdministrationHTTPEstate) admin(
	t *testing.T, token string, query string,
) communicationHTTPTestResponse {
	t.Helper()
	return communicationHTTPTestRequest(t, e.eng, http.MethodGet,
		"/v1/m/sessions/channels/administration?"+query, token, e.tenant, nil, nil)
}

func (e channelAdministrationHTTPEstate) adminPage(
	t *testing.T, token string, query string,
) sessions.ChannelAdministrationPage {
	t.Helper()
	response := e.admin(t, token, query)
	if response.status != http.StatusOK {
		t.Fatalf("administration %q = %d: %s", query, response.status, response.raw)
	}
	if got := response.header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("administration Cache-Control = %q, want no-store", got)
	}
	if got := response.header.Get("ETag"); got != "" {
		t.Fatalf("administration page emitted an HTTP ETag %q: the body etag is a Channel precondition, not a representation validator", got)
	}
	return communicationHTTPTestDecode[sessions.ChannelAdministrationPage](t, response)
}

func (e channelAdministrationHTTPEstate) sheet(
	t *testing.T, token string, channel model.ID, query string,
) communicationHTTPTestResponse {
	t.Helper()
	return communicationHTTPTestRequest(t, e.eng, http.MethodGet,
		"/v1/m/sessions/channels/"+channel.String()+"/grants?"+query, token, e.tenant, nil, nil)
}

func (e channelAdministrationHTTPEstate) sheetPage(
	t *testing.T, token string, channel model.ID, query string,
) sessions.ChannelGrantAdministrationPage {
	t.Helper()
	response := e.sheet(t, token, channel, query)
	if response.status != http.StatusOK {
		t.Fatalf("grant sheet %s %q = %d: %s", channel, query, response.status, response.raw)
	}
	if got := response.header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("grant sheet Cache-Control = %q, want no-store", got)
	}
	if got := response.header.Get("ETag"); got != "" {
		t.Fatalf("grant sheet emitted an HTTP ETag %q: filters, pagination and observed_at move the body without moving Channel.version", got)
	}
	return communicationHTTPTestDecode[sessions.ChannelGrantAdministrationPage](t, response)
}

func (e channelAdministrationHTTPEstate) createChannel(
	t *testing.T, workspace model.ID, slug string, grants []map[string]any,
) sessions.ChannelMutationResult {
	t.Helper()
	response := communicationHTTPTestRequest(t, e.eng, http.MethodPost, "/v1/m/sessions/channels",
		e.owner.token, e.tenant,
		map[string]any{"workspace_id": workspace.String(), "slug": slug,
			"name": "Channel " + slug, "initial_grants": grants}, nil)
	if response.status != http.StatusCreated {
		t.Fatalf("create Channel %s = %d: %s", slug, response.status, response.raw)
	}
	return communicationHTTPTestDecode[sessions.ChannelMutationResult](t, response)
}

func (e channelAdministrationHTTPEstate) grant(
	t *testing.T, token string, channel model.ID, body map[string]any, etag string,
) communicationHTTPTestResponse {
	t.Helper()
	return communicationHTTPTestRequest(t, e.eng, http.MethodPost,
		"/v1/m/sessions/channels/"+channel.String()+"/grants", token, e.tenant, body,
		map[string]string{"If-Match": etag})
}

func (e channelAdministrationHTTPEstate) revoke(
	t *testing.T, token string, channel, grant model.ID, etag string,
) communicationHTTPTestResponse {
	t.Helper()
	headers := map[string]string{}
	if etag != "" {
		headers["If-Match"] = etag
	}
	return communicationHTTPTestRequest(t, e.eng, http.MethodPost,
		"/v1/m/sessions/channels/"+channel.String()+"/grants/"+grant.String()+"/revoke",
		token, e.tenant, nil, headers)
}

func channelAdministrationIDs(page sessions.ChannelAdministrationPage) []string {
	out := make([]string, 0, len(page.Items))
	for _, item := range page.Items {
		out = append(out, item.Channel.ID.String())
	}
	return out
}

func channelAdministrationGrantIDs(page sessions.ChannelGrantAdministrationPage) []string {
	out := make([]string, 0, len(page.Items))
	for _, item := range page.Items {
		out = append(out, item.Grant.ID.String())
	}
	return out
}

func channelAdministrationSubject(kind string, ref model.ID) map[string]any {
	return map[string]any{"kind": kind, "ref": ref.String()}
}

func channelAdministrationGrant(subject map[string]any, read, write, admin bool) map[string]any {
	return map[string]any{"subject": subject, "can_read": read, "can_write": write, "can_admin": admin}
}

// findChannelAdministrationGrant returns the grant a creation or mutation result
// issued for one subject, so a later revoke names the EXACT row that was shown
// rather than a "similar" generation.
func findChannelAdministrationGrant(
	t *testing.T, grants []sessions.ChannelGrant, subject model.ID,
) sessions.ChannelGrant {
	t.Helper()
	for _, grant := range grants {
		if grant.Subject.Ref == subject.String() {
			return grant
		}
	}
	t.Fatalf("grant for subject %s absent from %+v", subject, grants)
	return sessions.ChannelGrant{}
}

// TestCommunicationChannelAdministrationFreshHTTP drives both new GETs through
// the production router on a fresh, explicitly activated SQLite estate.
func TestCommunicationChannelAdministrationFreshHTTP(t *testing.T) {
	exerciseCommunicationChannelAdministrationHTTP(t, communicationHTTPTestSQLiteStore(t))
}

// TestCommunicationChannelAdministrationFreshHTTPPostgres runs the SAME journey
// against a real, isolated split-owner PostgreSQL database: the schema is
// reconciled by the product's own path (so the new indexes are created on the
// real engine), the estate is reopened keeping the engine, and every verdict is
// the engine's, not a SQLite fixture wearing a PostgreSQL name.
func TestCommunicationChannelAdministrationFreshHTTPPostgres(t *testing.T) {
	exerciseCommunicationChannelAdministrationHTTP(t, communicationHTTPTestPostgresStore(t))
}

func exerciseCommunicationChannelAdministrationHTTP(t *testing.T, storeEstate communicationHTTPTestStore) {
	estate := bootChannelAdministrationHTTPEstate(t, storeEstate)
	eng, tenant, workspace := estate.eng, estate.tenant, estate.workspace
	owner, steward, editor, viewer, stranger := estate.owner, estate.steward, estate.editor, estate.viewer, estate.stranger
	wsQuery := "workspace_id=" + workspace.String()

	ownerAll := channelAdministrationGrant(channelAdministrationSubject("user", owner.id), true, true, true)
	stewardAdminOnly := channelAdministrationGrant(channelAdministrationSubject("user", steward.id), false, false, true)
	stewardReadOnly := channelAdministrationGrant(channelAdministrationSubject("user", steward.id), true, false, false)

	// The core case: the steward administers WITHOUT any local read or write.
	adminNoRead := estate.createChannel(t, workspace, "admin-no-read", []map[string]any{ownerAll, stewardAdminOnly})
	// A Channel the steward can only READ: it must never appear in the
	// administrative catalog, and its sheet must be concealed.
	readNoAdmin := estate.createChannel(t, workspace, "read-no-admin", []map[string]any{ownerAll, stewardReadOnly})
	// A Channel nobody but the owner holds anything on.
	ownerOnly := estate.createChannel(t, workspace, "owner-only", []map[string]any{ownerAll})
	// A Channel in the OTHER workspace of the same tenant.
	sideChannel := estate.createChannel(t, estate.sideWorkspace, "side-admin", []map[string]any{ownerAll, stewardAdminOnly})

	stewardAdminGrant := findChannelAdministrationGrant(t, adminNoRead.Grants, steward.id)

	t.Run("closed query and selector validation", func(t *testing.T) {
		before := communicationHTTPTestEffects(t, eng, tenant)
		for name, query := range map[string]string{
			"missing workspace":        "limit=1",
			"malformed workspace":      "workspace_id=not-a-uuid",
			"unknown key":              wsQuery + "&cursor=abc",
			"raw position key":         wsQuery + "&after=abc",
			"repeated limit":           wsQuery + "&limit=1&limit=2",
			"repeated workspace":       wsQuery + "&" + wsQuery,
			"limit zero":               wsQuery + "&limit=0",
			"limit above maximum":      wsQuery + "&limit=201",
			"limit non-numeric":        wsQuery + "&limit=abc",
			"limit non-canonical":      wsQuery + "&limit=01",
			"unknown state":            wsQuery + "&state=deleted",
			"empty state":              wsQuery + "&state=",
			"raw channel continuation": wsQuery + "&continuation=" + model.NewID().String(),
			"raw c3a1 continuation":    wsQuery + "&continuation=c3a1.raw",
			"read catalog family":      wsQuery + "&continuation=c3n1.raw",
		} {
			if response := estate.admin(t, steward.token, query); response.status != http.StatusBadRequest {
				t.Fatalf("administration %s = %d: %s", name, response.status, response.raw)
			}
		}
		for name, query := range map[string]string{
			"missing workspace":       "limit=1",
			"malformed workspace":     "workspace_id=not-a-uuid",
			"unknown key":             wsQuery + "&cursor=abc",
			"repeated state":          wsQuery + "&state=all&state=active",
			"unknown state":           wsQuery + "&state=pending",
			"subject kind alone":      wsQuery + "&subject_kind=user",
			"subject ref alone":       wsQuery + "&subject_ref=" + steward.id.String(),
			"unknown subject kind":    wsQuery + "&subject_kind=robot&subject_ref=" + steward.id.String(),
			"non-canonical subject":   wsQuery + "&subject_kind=user&subject_ref=not-a-uuid",
			"limit above maximum":     wsQuery + "&limit=201",
			"limit non-canonical":     wsQuery + "&limit=+1",
			"raw grant continuation":  wsQuery + "&continuation=" + model.NewID().String(),
			"admin catalog family":    wsQuery + "&continuation=c3a1.raw",
			"read catalog family":     wsQuery + "&continuation=c3n1.raw",
			"inbox navigation family": wsQuery + "&continuation=c2n1.raw",
		} {
			response := estate.sheet(t, steward.token, adminNoRead.Channel.ID, query)
			if response.status != http.StatusBadRequest {
				t.Fatalf("grant sheet %s = %d: %s", name, response.status, response.raw)
			}
		}
		// A workspace selector that names another tenant's workspace resolves to
		// nothing; one that names a REAL sibling workspace of this tenant must not
		// be able to relabel the stored Channel either.
		//
		// ⛔ THIS EXPECTED 404 UNTIL G1-A, AND THE CHANGE IS THE POINT RATHER THAN A
		// REGRESSION. The workspace selector of this collection is now corroborated
		// BEFORE the route is authorized, so that a workspace-scoped grant can reach
		// the exact collection it was granted for. Once the resolver runs in front of
		// the decision, its answers become visible to a caller who has NOT been
		// authorized — and a 404 here, next to the 403 an unauthorized caller gets on
		// the active workspace, would tell that caller which workspaces exist.
		//
		// So a known absence, a known non-active workspace and a known denial now
		// share ONE public refusal: the collection's own generic 403. The sibling
		// workspace below is a DIFFERENT question — an entity route comparing the
		// selector with the row's stored workspace — and it keeps its 404 unchanged.
		if response := estate.admin(t, steward.token, "workspace_id="+estate.foreignWorkspace); response.status != http.StatusForbidden {
			t.Fatalf("administration with a foreign workspace = %d: %s", response.status, response.raw)
		}
		crossed := estate.sheet(t, steward.token, adminNoRead.Channel.ID,
			"workspace_id="+estate.sideWorkspace.String())
		if crossed.status != http.StatusNotFound {
			t.Fatalf("grant sheet with a sibling workspace selector = %d: %s", crossed.status, crossed.raw)
		}
		if response := estate.admin(t, "", wsQuery); response.status != http.StatusUnauthorized {
			t.Fatalf("anonymous administration = %d: %s", response.status, response.raw)
		}
		if response := estate.sheet(t, "", adminNoRead.Channel.ID, wsQuery); response.status != http.StatusUnauthorized {
			t.Fatalf("anonymous grant sheet = %d: %s", response.status, response.raw)
		}
		crossTenant := communicationHTTPTestRequest(t, eng, http.MethodGet,
			"/v1/m/sessions/channels/administration?"+wsQuery, stranger.token, tenant, nil, nil)
		if crossTenant.status != http.StatusForbidden && crossTenant.status != http.StatusUnauthorized {
			t.Fatalf("stranger tenant administration = %d: %s", crossTenant.status, crossTenant.raw)
		}
		// "administration" is a literal segment, never a Channel identifier: the
		// point read must not resolve it.
		literal := communicationHTTPTestRequest(t, eng, http.MethodGet,
			"/v1/m/sessions/channels/administration", steward.token, tenant, nil, nil)
		if literal.status != http.StatusBadRequest {
			t.Fatalf("administration without a workspace selector = %d: %s", literal.status, literal.raw)
		}
		assertCommunicationHTTPTestNoEffects(t, eng, tenant, before, "administration query validation")
	})

	t.Run("administration is core admin and a current local admin grant, never read", func(t *testing.T) {
		before := communicationHTTPTestEffects(t, eng, tenant)

		page := estate.adminPage(t, steward.token, wsQuery+"&limit=50")
		if got := channelAdministrationIDs(page); len(got) != 1 || got[0] != adminNoRead.Channel.ID.String() {
			t.Fatalf("steward administration = %v, want only the admin-only Channel", got)
		}
		if page.HasMore || page.Continuation != "" {
			t.Fatalf("single-item page reports more: %+v", page)
		}
		if page.Items[0].ETag != fmt.Sprintf("%q", "v"+fmt.Sprint(adminNoRead.Channel.Version)) {
			t.Fatalf("administration item etag = %q, want the current Channel validator", page.Items[0].ETag)
		}
		raw := string(estate.admin(t, steward.token, wsQuery+"&limit=50").raw)
		if strings.Contains(raw, "my_access") {
			t.Fatalf("administration page emitted accessory access bits: %s", raw)
		}

		// The very same principal, at the very same moment, has NO read: the read
		// catalog does not list the Channel and the read point read conceals it.
		readCatalog := communicationHTTPTestRequest(t, eng, http.MethodGet,
			"/v1/m/sessions/channels?"+wsQuery+"&limit=50", steward.token, tenant, nil, nil)
		if readCatalog.status != http.StatusOK {
			t.Fatalf("steward read catalog = %d: %s", readCatalog.status, readCatalog.raw)
		}
		catalog := communicationHTTPTestDecode[sessions.ChannelCatalogPage](t, readCatalog)
		for _, item := range catalog.Items {
			if item.ID == adminNoRead.Channel.ID {
				t.Fatalf("the administered Channel leaked into the read catalog: %+v", item)
			}
		}
		if len(catalog.Items) != 1 || catalog.Items[0].ID != readNoAdmin.Channel.ID {
			t.Fatalf("steward read catalog = %+v, want only the read-only Channel", catalog.Items)
		}
		detail := communicationHTTPTestRequest(t, eng, http.MethodGet,
			"/v1/m/sessions/channels/"+adminNoRead.Channel.ID.String(), steward.token, tenant, nil, nil)
		if detail.status != http.StatusNotFound {
			t.Fatalf("steward read detail on the administered Channel = %d: %s", detail.status, detail.raw)
		}

		// And the mirror: a Channel the steward can only READ is not administrable.
		if response := estate.sheet(t, steward.token, readNoAdmin.Channel.ID, wsQuery); response.status != http.StatusNotFound {
			t.Fatalf("read-only Channel sheet = %d: %s", response.status, response.raw)
		}
		// Core admin with NO local grant at all: concealed, and absent from the page.
		if response := estate.sheet(t, owner.token, sideChannel.Channel.ID, wsQuery); response.status != http.StatusNotFound {
			t.Fatalf("owner sheet with a crossed workspace selector = %d: %s", response.status, response.raw)
		}
		ownerPage := estate.adminPage(t, owner.token, wsQuery+"&limit=50")
		ownerIDs := channelAdministrationIDs(ownerPage)
		if len(ownerIDs) != 3 {
			t.Fatalf("owner administration = %v, want the three Channels of this workspace", ownerIDs)
		}
		// A principal WITHOUT core sessions:channel:admin never reaches the module,
		// whichever local grants it holds: editor is read+write, viewer is read.
		// The COLLECTION says 403 (there is no entity to conceal); the POINT route
		// conceals the denial as 404, which is the ratified uniform answer and the
		// same shape the read point read already uses.
		for name, token := range map[string]string{"editor": editor.token, "viewer": viewer.token} {
			if response := estate.admin(t, token, wsQuery); response.status != http.StatusForbidden {
				t.Fatalf("%s administration = %d: %s", name, response.status, response.raw)
			}
			if response := estate.sheet(t, token, adminNoRead.Channel.ID, wsQuery); response.status != http.StatusNotFound {
				t.Fatalf("%s grant sheet = %d: %s", name, response.status, response.raw)
			}
			// And the same principals keep the reads their role does grant, so the
			// refusal above is about the administrative permission and nothing else.
			if response := communicationHTTPTestRequest(t, eng, http.MethodGet,
				"/v1/m/sessions/channels?"+wsQuery, token, tenant, nil, nil); response.status != http.StatusOK {
				t.Fatalf("%s read catalog = %d: %s", name, response.status, response.raw)
			}
		}
		assertCommunicationHTTPTestNoEffects(t, eng, tenant, before, "administration authority separation")

		// A communication-session credential keeps its ceiling: it never carries
		// core sessions:channel:admin, so holding a grant whose subject kind is
		// `session` cannot buy an administrative read. The grant below is a real
		// mutation, so the purity census is retaken after it.
		session := createCommunicationHTTPTestSession(t, eng, tenant, workspace, "administration")
		sheet := estate.sheetPage(t, steward.token, adminNoRead.Channel.ID, wsQuery+"&state=all&limit=50")
		sessionGrant := estate.grant(t, owner.token, adminNoRead.Channel.ID, map[string]any{
			"subject":  map[string]any{"kind": "session", "ref": session.sid},
			"can_read": false, "can_write": false, "can_admin": true,
		}, sheet.ETag)
		if sessionGrant.status != http.StatusOK {
			t.Fatalf("grant the session subject an admin generation = %d: %s", sessionGrant.status, sessionGrant.raw)
		}
		afterGrant := communicationHTTPTestEffects(t, eng, tenant)
		for name, response := range map[string]communicationHTTPTestResponse{
			"administration": estate.admin(t, session.communication.Token, wsQuery),
			"grant sheet":    estate.sheet(t, session.communication.Token, adminNoRead.Channel.ID, wsQuery),
		} {
			if response.status != http.StatusForbidden && response.status != http.StatusUnauthorized &&
				response.status != http.StatusNotFound {
				t.Fatalf("communication-session credential %s = %d: %s — a session grant must not buy a core admin ceiling",
					name, response.status, response.raw)
			}
		}
		assertCommunicationHTTPTestNoEffects(t, eng, tenant, afterGrant,
			"communication-session credential on the administrative routes")
		// Undo the session generation so the later CAS journey starts from the
		// generations the fixture created.
		reread := estate.sheetPage(t, steward.token, adminNoRead.Channel.ID, wsQuery+"&state=all&limit=50")
		granted := communicationHTTPTestDecode[sessions.ChannelMutationResult](t, sessionGrant)
		if undone := estate.revoke(t, owner.token, adminNoRead.Channel.ID, granted.Grant.ID, reread.ETag); undone.status != http.StatusOK {
			t.Fatalf("revoke the session subject generation = %d: %s", undone.status, undone.raw)
		}
	})

	t.Run("a custom role name is not assignable through Membership.role", func(t *testing.T) {
		// ⛔ THIS TEST WAS RENAMED BECAUSE ITS OLD NAME OVERCLAIMED, and the
		// overclaim mattered. It used to be called "core admin without core read is
		// not expressible with an assignable role", and its comment said that no
		// role-assignment path accepts a custom role name — which reads as "this
		// principal cannot be built", i.e. as a statement about the PRODUCT.
		//
		// What it actually measures is one path: POST /v1/memberships refusing a
		// custom name in `Membership.role`, which stays a closed set. That closed
		// set is real and this test still holds it.
		//
		// The principal IS buildable, and the composed tree proves it: the supported
		// route is a governance scoped grant (`role_custom: true`) plus a Cedar
		// forbid on the parent user, and
		// TestCommunicationLineageAuthorityCustomGrantAdminHTTP drives exactly that
		// principal through BOTH administrative GETs — 200/200 while its read
		// catalog is 403 and its point read 404. So granular admin acceptance is
		// that journey's, not this one's.
		//
		// Nothing here relaxes `auth.IsRole`: the assertion below is unchanged.
		custom := "k3-admin-no-core-read"
		created := communicationHTTPTestRequest(t, eng, http.MethodPost, "/v1/m/governance/rbac/roles",
			owner.token, tenant, map[string]any{
				"name": custom, "display_name": "K3 administration without core read",
				"base_role": "owner", "excludes": []string{"sessions:channel:read"},
			}, nil)
		if created.status != http.StatusCreated {
			t.Fatalf("create the excluding custom role = %d: %s", created.status, created.raw)
		}
		assigned := communicationHTTPTestRequest(t, eng, http.MethodPost, "/v1/memberships",
			estate.adminToken, "", map[string]any{
				"user_id": viewer.id, "tenant": tenant, "role": custom,
			}, nil)
		if assigned.status == http.StatusCreated {
			t.Fatalf("a custom role name became assignable through Membership.role: %d %s — "+
				"the closed membership-role set this test holds has changed and the change "+
				"needs its own decision", assigned.status, assigned.raw)
		}
		t.Logf("K3_MEMBERSHIP_ROLE_CLOSED|custom_role_created=%d|membership_with_custom_role=%d|body=%s",
			created.status, assigned.status, assigned.raw)
	})

	t.Run("the sheet returns the exact stored generations", func(t *testing.T) {
		before := communicationHTTPTestEffects(t, eng, tenant)
		page := estate.sheetPage(t, steward.token, adminNoRead.Channel.ID, wsQuery+"&state=all&limit=50")
		if page.Channel.ID != adminNoRead.Channel.ID || page.Channel.WorkspaceID != workspace ||
			page.Channel.TenantID != tenant {
			t.Fatalf("sheet Channel scope = %+v", page.Channel)
		}
		if page.ETag != fmt.Sprintf("%q", "v"+fmt.Sprint(page.Channel.Version)) {
			t.Fatalf("sheet etag = %q, want the current Channel validator", page.ETag)
		}
		if page.ObservedAt.IsZero() {
			t.Fatal("sheet observed_at is absent")
		}
		// state=all also retains the session generation the authority-separation
		// subtest granted and revoked: history is kept, never rewritten.
		if len(page.Items) != 3 || page.HasMore {
			t.Fatalf("sheet items = %v", channelAdministrationGrantIDs(page))
		}
		if active := estate.sheetPage(t, steward.token, adminNoRead.Channel.ID, wsQuery+"&state=active&limit=50"); len(active.Items) != 2 {
			t.Fatalf("state=active items = %v, want the two live generations",
				channelAdministrationGrantIDs(active))
		}
		revoked := estate.sheetPage(t, steward.token, adminNoRead.Channel.ID, wsQuery+"&state=revoked&limit=50")
		if len(revoked.Items) != 1 || revoked.Items[0].Grant.Subject.Kind != "session" ||
			revoked.Items[0].TemporalState != sessions.ChannelGrantTemporalRevoked ||
			revoked.Items[0].Grant.RevokedBy == nil {
			t.Fatalf("state=revoked items = %+v, want the revoked session generation with its actor", revoked.Items)
		}
		seen := map[string]sessions.ChannelGrantAdministrationItem{}
		for index, item := range page.Items {
			if index > 0 && page.Items[index-1].Grant.ID.String() >= item.Grant.ID.String() {
				t.Fatalf("sheet is not ordered by grant id: %v", channelAdministrationGrantIDs(page))
			}
			if item.Grant.ChannelID != adminNoRead.Channel.ID || item.Grant.Generation != 1 {
				t.Fatalf("sheet item crossed its Channel or generation: %+v", item.Grant)
			}
			seen[item.Grant.Subject.Ref] = item
		}
		stewardItem, ok := seen[steward.id.String()]
		if !ok || !stewardItem.Grant.CanAdmin || stewardItem.Grant.CanRead || stewardItem.Grant.CanWrite {
			t.Fatalf("steward grant bits = %+v", stewardItem.Grant)
		}
		if stewardItem.TemporalState != sessions.ChannelGrantTemporalActive ||
			stewardItem.Grant.State != sessions.ChannelGrantActive {
			t.Fatalf("steward temporal state = %q, stored %q", stewardItem.TemporalState, stewardItem.Grant.State)
		}
		// The exact-subject filter answers the same row without walking the history.
		exact := estate.sheetPage(t, steward.token, adminNoRead.Channel.ID,
			wsQuery+"&state=all&subject_kind=user&subject_ref="+steward.id.String())
		if len(exact.Items) != 1 || exact.Items[0].Grant.ID != stewardItem.Grant.ID {
			t.Fatalf("exact-subject sheet = %v", channelAdministrationGrantIDs(exact))
		}
		// A subject with no generation at all is an empty page, still carrying the
		// Channel and its precondition, because the admin closure DID authorize it.
		absent := estate.sheetPage(t, steward.token, adminNoRead.Channel.ID,
			wsQuery+"&state=all&subject_kind=user&subject_ref="+viewer.id.String())
		if len(absent.Items) != 0 || absent.HasMore || absent.Channel.ID != adminNoRead.Channel.ID ||
			absent.ETag == "" {
			t.Fatalf("empty filtered sheet = %+v", absent)
		}
		if body := string(estate.sheet(t, steward.token, adminNoRead.Channel.ID, wsQuery).raw); !strings.Contains(body, `"items":[`) {
			t.Fatalf("sheet items must render as an array, not null: %s", body)
		}
		assertCommunicationHTTPTestNoEffects(t, eng, tenant, before, "administrative sheet reads")
	})

	// Close the client and reopen the engine BEFORE the next block: the journey
	// must re-enter with a new credential and rediscover everything, with no
	// memory of the fixture. The reopen is done here, on the parent test, so the
	// replacement engine's cleanup outlives the sub-test that uses it.
	if err := eng.Close(); err != nil {
		t.Fatalf("close estate before reload: %v", err)
	}
	estate.eng = bootCommunicationHTTPTestEngine(t, estate.store)
	eng = estate.eng
	t.Cleanup(func() { _ = eng.Close() })
	estate.relogin(t)
	steward, owner, editor, viewer = estate.steward, estate.owner, estate.editor, estate.viewer
	_, _ = editor, viewer

	t.Run("reload, revoke the exact row and generate the successor with CAS", func(t *testing.T) {
		page := estate.adminPage(t, steward.token, wsQuery+"&limit=50")
		if len(page.Items) != 1 || page.Items[0].Channel.ID != adminNoRead.Channel.ID {
			t.Fatalf("administration after reload = %v", channelAdministrationIDs(page))
		}
		sheet := estate.sheetPage(t, steward.token, adminNoRead.Channel.ID, wsQuery+"&state=active&limit=50")
		var shown sessions.ChannelGrant
		for _, item := range sheet.Items {
			if item.Grant.Subject.Ref == steward.id.String() {
				shown = item.Grant
			}
		}
		if shown.ID != stewardAdminGrant.ID {
			t.Fatalf("sheet after reload shows %s, want the stored generation %s", shown.ID, stewardAdminGrant.ID)
		}
		// Move the CHANNEL version so the two numbers stop coinciding: while both
		// are 1, an If-Match built from the grant row version would be accepted for
		// the right reason and the probe below would measure nothing.
		patched := communicationHTTPTestRequest(t, eng, http.MethodPatch, "/v1/m/sessions/channels",
			owner.token, tenant,
			map[string]any{"channel_id": adminNoRead.Channel.ID.String(), "description": "administered"},
			map[string]string{"If-Match": sheet.ETag})
		if patched.status != http.StatusOK {
			t.Fatalf("patch to separate the Channel and row versions = %d: %s", patched.status, patched.raw)
		}
		sheet = estate.sheetPage(t, steward.token, adminNoRead.Channel.ID, wsQuery+"&state=active&limit=50")
		beforeChannel := sheet.Channel
		if beforeChannel.Version == shown.Version {
			t.Fatalf("Channel version %d still equals the grant row version %d: the probe below cannot discriminate",
				beforeChannel.Version, shown.Version)
		}

		// The row's own version is NOT the precondition: the Channel ETag is.
		rowVersion := estate.revoke(t, steward.token, adminNoRead.Channel.ID, shown.ID,
			fmt.Sprintf("%q", "v"+fmt.Sprint(shown.Version)))
		if rowVersion.status != http.StatusConflict {
			t.Fatalf("revoke with the grant row version as If-Match = %d: %s", rowVersion.status, rowVersion.raw)
		}
		if missing := estate.revoke(t, steward.token, adminNoRead.Channel.ID, shown.ID, ""); missing.status != http.StatusPreconditionRequired {
			t.Fatalf("revoke without If-Match = %d: %s", missing.status, missing.raw)
		}
		if malformed := estate.revoke(t, steward.token, adminNoRead.Channel.ID, shown.ID, "v3"); malformed.status != http.StatusBadRequest {
			t.Fatalf("revoke with a weak If-Match = %d: %s", malformed.status, malformed.raw)
		}
		revoked := estate.revoke(t, steward.token, adminNoRead.Channel.ID, shown.ID, sheet.ETag)
		if revoked.status != http.StatusOK {
			t.Fatalf("revoke the presented row = %d: %s", revoked.status, revoked.raw)
		}
		afterRevoke := communicationHTTPTestDecode[sessions.ChannelMutationResult](t, revoked)
		if afterRevoke.Channel.Version != beforeChannel.Version+1 ||
			afterRevoke.Channel.ACLRevision != beforeChannel.ACLRevision+1 {
			t.Fatalf("revoke did not advance version and acl_revision: %+v", afterRevoke.Channel)
		}
		// The steward has just revoked its own only administrative path: the
		// mutation receipt does not grant it a future query.
		if lost := estate.sheet(t, steward.token, adminNoRead.Channel.ID, wsQuery); lost.status != http.StatusNotFound {
			t.Fatalf("sheet after revoking one's own admin path = %d: %s", lost.status, lost.raw)
		}
		if empty := estate.adminPage(t, steward.token, wsQuery+"&limit=50"); len(empty.Items) != 0 {
			t.Fatalf("administration after revoking one's own admin path = %v", channelAdministrationIDs(empty))
		}

		// The owner reads the history and grants the SUCCESSOR generation.
		ownerSheet := estate.sheetPage(t, owner.token, adminNoRead.Channel.ID, wsQuery+"&state=all&limit=50")
		var revokedItem sessions.ChannelGrantAdministrationItem
		for _, item := range ownerSheet.Items {
			if item.Grant.ID == shown.ID {
				revokedItem = item
			}
		}
		if revokedItem.Grant.State != sessions.ChannelGrantRevoked ||
			revokedItem.TemporalState != sessions.ChannelGrantTemporalRevoked ||
			revokedItem.Grant.RevokedBy == nil {
			t.Fatalf("revoked generation in the history = %+v", revokedItem)
		}
		granted := estate.grant(t, owner.token, adminNoRead.Channel.ID,
			map[string]any{"subject": channelAdministrationSubject("user", steward.id),
				"can_read": false, "can_write": false, "can_admin": true}, ownerSheet.ETag)
		if granted.status != http.StatusOK {
			t.Fatalf("grant the successor generation = %d: %s", granted.status, granted.raw)
		}
		successor := communicationHTTPTestDecode[sessions.ChannelMutationResult](t, granted)
		if successor.Grant == nil || successor.Grant.Generation != shown.Generation+1 ||
			successor.Grant.SupersedesID != shown.ID {
			t.Fatalf("successor generation = %+v, want generation %d superseding %s",
				successor.Grant, shown.Generation+1, shown.ID)
		}
		// A stale ETag must not commit: the second client re-reads and re-confirms.
		stale := estate.grant(t, owner.token, adminNoRead.Channel.ID,
			map[string]any{"subject": channelAdministrationSubject("user", viewer.id),
				"can_read": true, "can_write": false, "can_admin": false}, ownerSheet.ETag)
		if stale.status != http.StatusConflict {
			t.Fatalf("grant with a stale Channel ETag = %d: %s", stale.status, stale.raw)
		}
		// Revoking the OLD generation never touches its successor.
		reread := estate.sheetPage(t, owner.token, adminNoRead.Channel.ID, wsQuery+"&state=all&limit=50")
		if again := estate.revoke(t, owner.token, adminNoRead.Channel.ID, shown.ID, reread.ETag); again.status != http.StatusConflict {
			t.Fatalf("revoking an already revoked generation = %d: %s", again.status, again.raw)
		}
		final := estate.sheetPage(t, owner.token, adminNoRead.Channel.ID,
			wsQuery+"&state=active&subject_kind=user&subject_ref="+steward.id.String())
		if len(final.Items) != 1 || final.Items[0].Grant.ID != successor.Grant.ID ||
			final.Items[0].Grant.State != sessions.ChannelGrantActive {
			t.Fatalf("successor after revoking its predecessor = %+v", final.Items)
		}
		// The steward is administering again, through the NEW generation only.
		back := estate.adminPage(t, steward.token, wsQuery+"&limit=50")
		if len(back.Items) != 1 || back.Items[0].Channel.ID != adminNoRead.Channel.ID {
			t.Fatalf("administration after the successor grant = %v", channelAdministrationIDs(back))
		}
		stewardAdminGrant = *successor.Grant
	})

	t.Run("no-store is on every response the routes produce, not only the 200", func(t *testing.T) {
		exerciseChannelAdministrationNoStoreHTTP(t, estate, adminNoRead.Channel.ID, readNoAdmin.Channel.ID)
	})

	t.Run("navigation tokens are family, reader, Channel and filter bound", func(t *testing.T) {
		exerciseChannelAdministrationTokenCrossingHTTP(t, estate)
	})

	t.Run("a Channel revision change between pages is 409 without a mixed listing", func(t *testing.T) {
		exerciseChannelAdministrationRevisionChangeHTTP(t, estate)
	})

	t.Run("late revocation, expiry and the OR horizon of two admin paths", func(t *testing.T) {
		exerciseChannelAdministrationHorizonsHTTP(t, estate)
	})

	t.Run("unknown evidence is 503, never an authorized empty page", func(t *testing.T) {
		exerciseChannelAdministrationUnknownEvidenceHTTP(t, estate)
	})

	t.Run("no GET creates a mutation audit or any durable effect", func(t *testing.T) {
		before := communicationHTTPTestEffects(t, eng, tenant)
		estate.adminPage(t, steward.token, wsQuery+"&limit=50")
		estate.adminPage(t, owner.token, wsQuery+"&state=all&limit=1")
		estate.sheetPage(t, steward.token, adminNoRead.Channel.ID, wsQuery+"&state=all&limit=50")
		estate.sheetPage(t, owner.token, ownerOnly.Channel.ID, wsQuery+"&state=all&limit=50")
		estate.sheet(t, steward.token, readNoAdmin.Channel.ID, wsQuery)
		assertCommunicationHTTPTestNoEffects(t, eng, tenant, before, "administrative GET purity")
	})
}

// scimGroupWithMember creates a real directory user-group through the product's
// SCIM surface and returns its canonical id, so a second CLOSURE SUBJECT for the
// same principal is a directory fact and not a fixture row.
func scimGroupWithMember(
	t *testing.T,
	eng *engine,
	token string,
	tenant model.TenantID,
	display string,
	member model.ID,
) model.ID {
	t.Helper()
	created := communicationHTTPTestRequest(t, eng, http.MethodPost, "/v1/scim/v2/Groups", token, tenant,
		map[string]any{
			"schemas":     []string{"urn:ietf:params:scim:schemas:core:2.0:Group"},
			"displayName": display,
			"members":     []map[string]any{{"value": member.String()}},
		}, map[string]string{"Content-Type": "application/scim+json"})
	if created.status != http.StatusCreated {
		t.Fatalf("create SCIM group %s = %d: %s", display, created.status, created.raw)
	}
	id, err := model.ParseID(communicationHTTPTestDecode[struct {
		ID string `json:"id"`
	}](t, created).ID)
	if err != nil {
		t.Fatalf("SCIM group id: %v", err)
	}
	return id
}

// exerciseChannelAdministrationHorizonsHTTP covers what only TIME and a second
// authority path can show: a revocation taken after the first observation, an
// expiry crossed between two requests, and the OR-horizon of two admin grants
// with different lifetimes — one still-valid path is enough, and the shortest
// one dying does not end the access.
func exerciseChannelAdministrationHorizonsHTTP(
	t *testing.T,
	estate channelAdministrationHTTPEstate,
) {
	eng, tenant, workspace := estate.eng, estate.tenant, estate.workspace
	owner, steward := estate.owner, estate.steward
	wsQuery := "workspace_id=" + workspace.String()
	ownerAll := channelAdministrationGrant(channelAdministrationSubject("user", owner.id), true, true, true)

	group := scimGroupWithMember(t, eng, owner.token, tenant, "K3 admin stewards", steward.id)
	horizons := estate.createChannel(t, workspace, "admin-horizons", []map[string]any{ownerAll})

	// Two administrative paths for the SAME principal: a direct grant that
	// expires soon and a group grant that does not.
	expiresAt := time.Now().UTC().Add(4 * time.Second)
	direct := estate.grant(t, owner.token, horizons.Channel.ID, map[string]any{
		"subject":  channelAdministrationSubject("user", steward.id),
		"can_read": false, "can_write": false, "can_admin": true,
		"expires_at": expiresAt.Format(time.RFC3339Nano),
	}, horizons.ETag)
	if direct.status != http.StatusOK {
		t.Fatalf("grant the expiring direct admin path = %d: %s", direct.status, direct.raw)
	}
	directResult := communicationHTTPTestDecode[sessions.ChannelMutationResult](t, direct)

	// Only the expiring path exists yet: the Channel is administrable now.
	if page := estate.adminPage(t, steward.token, wsQuery+"&limit=50"); len(page.Items) == 0 {
		t.Fatal("the expiring direct admin path did not make the Channel administrable")
	}
	sheet := estate.sheetPage(t, steward.token, horizons.Channel.ID, wsQuery+"&state=active&limit=50")
	var expiring sessions.ChannelGrantAdministrationItem
	for _, item := range sheet.Items {
		if item.Grant.ID == directResult.Grant.ID {
			expiring = item
		}
	}
	if expiring.TemporalState != sessions.ChannelGrantTemporalActive || expiring.Grant.ExpiresAt == nil {
		t.Fatalf("expiring grant before its horizon = %+v", expiring)
	}

	groupGrant := estate.grant(t, owner.token, horizons.Channel.ID, map[string]any{
		"subject":  map[string]any{"kind": "user_group", "ref": group.String()},
		"can_read": false, "can_write": false, "can_admin": true,
	}, sheet.ETag)
	if groupGrant.status != http.StatusOK {
		t.Fatalf("grant the non-expiring group admin path = %d: %s", groupGrant.status, groupGrant.raw)
	}
	groupResult := communicationHTTPTestDecode[sessions.ChannelMutationResult](t, groupGrant)

	// Cross the direct grant's horizon on the ENGINE clock, then re-ask.
	time.Sleep(time.Until(expiresAt) + 1500*time.Millisecond)
	after := estate.adminPage(t, steward.token, wsQuery+"&limit=50")
	if len(after.Items) == 0 {
		t.Fatal("the surviving group admin path was not honoured: the OR-horizon collapsed to the shortest grant")
	}
	survived := estate.sheetPage(t, steward.token, horizons.Channel.ID, wsQuery+"&state=all&limit=50")
	for _, item := range survived.Items {
		switch item.Grant.ID {
		case directResult.Grant.ID:
			if item.Grant.State != sessions.ChannelGrantActive {
				t.Fatalf("the GET materialized the expiry into stored state: %+v", item.Grant)
			}
			if item.TemporalState != sessions.ChannelGrantTemporalExpired {
				t.Fatalf("expired-by-time grant temporal state = %q", item.TemporalState)
			}
		case groupResult.Grant.ID:
			if item.TemporalState != sessions.ChannelGrantTemporalActive {
				t.Fatalf("surviving group grant temporal state = %q", item.TemporalState)
			}
		}
	}
	// The default `active` selection still reports the expired-by-time row,
	// because it is exactly the row the next mutation has to reckon with.
	stillListed := estate.sheetPage(t, steward.token, horizons.Channel.ID,
		wsQuery+"&state=active&subject_kind=user&subject_ref="+steward.id.String())
	if len(stillListed.Items) != 1 || stillListed.Items[0].Grant.ID != directResult.Grant.ID ||
		stillListed.Items[0].TemporalState != sessions.ChannelGrantTemporalExpired {
		t.Fatalf("persisted-active/expired-by-time row under state=active = %+v", stillListed.Items)
	}
	// GrantChannel still refuses a second ACTIVE generation for that subject: the
	// explicit revoke -> grant flow is preserved and nothing here automates it.
	refused := estate.grant(t, owner.token, horizons.Channel.ID, map[string]any{
		"subject":  channelAdministrationSubject("user", steward.id),
		"can_read": false, "can_write": false, "can_admin": true,
	}, stillListed.ETag)
	if refused.status != http.StatusConflict {
		t.Fatalf("granting over a persisted-active expired row = %d: %s", refused.status, refused.raw)
	}

	// Revoke the surviving path AFTER the first observation: the next request has
	// no authorized page and no ETag to reuse.
	current := estate.sheetPage(t, owner.token, horizons.Channel.ID, wsQuery+"&state=all&limit=50")
	revoked := estate.revoke(t, owner.token, horizons.Channel.ID, groupResult.Grant.ID, current.ETag)
	if revoked.status != http.StatusOK {
		t.Fatalf("revoke the group admin path = %d: %s", revoked.status, revoked.raw)
	}
	if gone := estate.sheet(t, steward.token, horizons.Channel.ID, wsQuery); gone.status != http.StatusNotFound {
		t.Fatalf("sheet after losing every admin path = %d: %s", gone.status, gone.raw)
	}
	final := estate.adminPage(t, steward.token, wsQuery+"&limit=50")
	for _, item := range final.Items {
		if item.Channel.ID == horizons.Channel.ID {
			t.Fatalf("the Channel survived in the administrative catalog after both paths ended: %+v", item)
		}
	}
}

// exerciseChannelAdministrationTokenCrossingHTTP proves the two continuation
// families are separate authorities-free coordinates: neither resumes the
// other's listing, neither crosses a reader, a Channel, a workspace or a filter
// selection, and neither survives tampering.
func exerciseChannelAdministrationTokenCrossingHTTP(
	t *testing.T,
	estate channelAdministrationHTTPEstate,
) {
	eng, tenant, workspace := estate.eng, estate.tenant, estate.workspace
	owner := estate.owner
	wsQuery := "workspace_id=" + workspace.String()
	ownerAll := channelAdministrationGrant(channelAdministrationSubject("user", owner.id), true, true, true)

	first := estate.createChannel(t, workspace, "token-crossing-a", []map[string]any{ownerAll})
	second := estate.createChannel(t, workspace, "token-crossing-b", []map[string]any{ownerAll})
	// A second grant on `first` so its sheet has more than one page at limit=1.
	extra := estate.grant(t, owner.token, first.Channel.ID, map[string]any{
		"subject":  channelAdministrationSubject("user", estate.viewer.id),
		"can_read": true, "can_write": false, "can_admin": false,
	}, first.ETag)
	if extra.status != http.StatusOK {
		t.Fatalf("seed a second grant = %d: %s", extra.status, extra.raw)
	}

	catalogPage := estate.adminPage(t, owner.token, wsQuery+"&limit=1")
	if !catalogPage.HasMore || catalogPage.Continuation == "" {
		t.Fatalf("administration page one has no lookahead: %+v", catalogPage)
	}
	if prefix := strings.SplitN(catalogPage.Continuation, ".", 2)[0]; prefix != "c3a1" {
		t.Fatalf("administration continuation family = %q, want c3a1", prefix)
	}
	sheetPage := estate.sheetPage(t, owner.token, first.Channel.ID, wsQuery+"&state=all&limit=1")
	if !sheetPage.HasMore || sheetPage.Continuation == "" {
		t.Fatalf("sheet page one has no lookahead: %+v", sheetPage)
	}
	if prefix := strings.SplitN(sheetPage.Continuation, ".", 2)[0]; prefix != "c3g1" {
		t.Fatalf("sheet continuation family = %q, want c3g1", prefix)
	}

	// Neither token names anything the page did not already return.
	catalogClaims := decodeChannelAdministrationTokenClaims(t, catalogPage.Continuation)
	if catalogClaims["lc"] != catalogPage.Items[0].Channel.ID.String() {
		t.Fatalf("administration token anchor = %v, want the last RETURNED Channel", catalogClaims["lc"])
	}
	sheetClaims := decodeChannelAdministrationTokenClaims(t, sheetPage.Continuation)
	if sheetClaims["lg"] != sheetPage.Items[0].Grant.ID.String() ||
		sheetClaims["ch"] != first.Channel.ID.String() {
		t.Fatalf("sheet token anchor/Channel = %v", sheetClaims)
	}
	for _, forbidden := range []string{
		second.Channel.ID.String(), estate.steward.id.String(), "cursor", "count",
	} {
		if strings.Contains(fmt.Sprint(catalogClaims), forbidden) ||
			strings.Contains(fmt.Sprint(sheetClaims), forbidden) {
			t.Fatalf("a continuation discloses %q", forbidden)
		}
	}

	// The families do not resume each other, and neither crosses its subject.
	for name, probe := range map[string]struct {
		sheetChannel model.ID
		query        string
		useSheet     bool
	}{
		"sheet token on the administrative catalog": {query: wsQuery + "&limit=1&continuation=" + sheetPage.Continuation},
		"catalog token on the sheet":                {sheetChannel: first.Channel.ID, query: wsQuery + "&state=all&limit=1&continuation=" + catalogPage.Continuation, useSheet: true},
		"sheet token on another Channel":            {sheetChannel: second.Channel.ID, query: wsQuery + "&state=all&limit=1&continuation=" + sheetPage.Continuation, useSheet: true},
		"sheet token with another state selection":  {sheetChannel: first.Channel.ID, query: wsQuery + "&state=active&limit=1&continuation=" + sheetPage.Continuation, useSheet: true},
		"sheet token with a subject filter added":   {sheetChannel: first.Channel.ID, query: wsQuery + "&state=all&limit=1&subject_kind=user&subject_ref=" + owner.id.String() + "&continuation=" + sheetPage.Continuation, useSheet: true},
		"catalog token with another state":          {query: wsQuery + "&state=active&limit=1&continuation=" + catalogPage.Continuation},
		"catalog token tampered":                    {query: wsQuery + "&limit=1&continuation=" + catalogPage.Continuation[:len(catalogPage.Continuation)-4] + "AAAA"},
		"sheet token tampered":                      {sheetChannel: first.Channel.ID, query: wsQuery + "&state=all&limit=1&continuation=" + sheetPage.Continuation[:len(sheetPage.Continuation)-4] + "AAAA", useSheet: true},
	} {
		var response communicationHTTPTestResponse
		if probe.useSheet {
			response = estate.sheet(t, owner.token, probe.sheetChannel, probe.query)
		} else {
			response = estate.admin(t, owner.token, probe.query)
		}
		if response.status != http.StatusBadRequest {
			t.Fatalf("%s = %d: %s", name, response.status, response.raw)
		}
	}
	// Another authenticated reader cannot resume this reader's listing, even with
	// the very same core permission on the very same Channel.
	if response := estate.admin(t, estate.steward.token, wsQuery+"&limit=1&continuation="+catalogPage.Continuation); response.status != http.StatusBadRequest {
		t.Fatalf("another reader resumed the administrative catalog = %d: %s", response.status, response.raw)
	}
	// A token minted in one workspace does not resume the other one.
	if response := communicationHTTPTestRequest(t, eng, http.MethodGet,
		"/v1/m/sessions/channels/administration?workspace_id="+estate.sideWorkspace.String()+
			"&limit=1&continuation="+catalogPage.Continuation, owner.token, tenant, nil, nil); response.status != http.StatusBadRequest {
		t.Fatalf("cross-workspace continuation = %d: %s", response.status, response.raw)
	}
}

func decodeChannelAdministrationTokenClaims(t *testing.T, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 4 {
		t.Fatalf("continuation shape = %q", token)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode continuation claims without the key: %v", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatalf("continuation claims: %v", err)
	}
	return claims
}

// exerciseChannelAdministrationRevisionChangeHTTP proves the sheet refuses to
// splice two Channel revisions into one apparently-single history, and that a
// purely TEMPORAL change is not a revision change.
func exerciseChannelAdministrationRevisionChangeHTTP(
	t *testing.T,
	estate channelAdministrationHTTPEstate,
) {
	eng, tenant, workspace := estate.eng, estate.tenant, estate.workspace
	owner := estate.owner
	wsQuery := "workspace_id=" + workspace.String()
	ownerAll := channelAdministrationGrant(channelAdministrationSubject("user", owner.id), true, true, true)

	channel := estate.createChannel(t, workspace, "revision-pages", []map[string]any{ownerAll})
	etag := channel.ETag
	for _, subject := range []model.ID{estate.steward.id, estate.editor.id, estate.viewer.id} {
		granted := estate.grant(t, owner.token, channel.Channel.ID, map[string]any{
			"subject":  channelAdministrationSubject("user", subject),
			"can_read": true, "can_write": false, "can_admin": false,
		}, etag)
		if granted.status != http.StatusOK {
			t.Fatalf("seed grant for %s = %d: %s", subject, granted.status, granted.raw)
		}
		etag = communicationHTTPTestDecode[sessions.ChannelMutationResult](t, granted).ETag
	}

	first := estate.sheetPage(t, owner.token, channel.Channel.ID, wsQuery+"&state=all&limit=2")
	if !first.HasMore || first.Continuation == "" {
		t.Fatalf("sheet page one has no lookahead: %+v", first)
	}
	// The unchanged snapshot pages cleanly.
	second := estate.sheetPage(t, owner.token, channel.Channel.ID,
		wsQuery+"&state=all&limit=2&continuation="+first.Continuation)
	if len(second.Items) == 0 {
		t.Fatalf("sheet page two = %+v", second)
	}
	for _, item := range second.Items {
		if item.Grant.ID.String() <= first.Items[len(first.Items)-1].Grant.ID.String() {
			t.Fatalf("sheet page two restated a row of page one: %s", item.Grant.ID)
		}
	}

	// Now move the Channel revision between pages and resume.
	restart := estate.sheetPage(t, owner.token, channel.Channel.ID, wsQuery+"&state=all&limit=2")
	patched := communicationHTTPTestRequest(t, eng, http.MethodPatch, "/v1/m/sessions/channels",
		owner.token, tenant,
		map[string]any{"channel_id": channel.Channel.ID.String(), "description": "moved between pages"},
		map[string]string{"If-Match": restart.ETag})
	if patched.status != http.StatusOK {
		t.Fatalf("patch between pages = %d: %s", patched.status, patched.raw)
	}
	stale := estate.sheet(t, owner.token, channel.Channel.ID,
		wsQuery+"&state=all&limit=2&continuation="+restart.Continuation)
	if stale.status != http.StatusConflict ||
		!strings.Contains(string(stale.raw), "channel_snapshot_changed") {
		t.Fatalf("resuming across a revision change = %d: %s", stale.status, stale.raw)
	}
	if strings.Contains(string(stale.raw), `"items"`) {
		t.Fatalf("the 409 leaked a partial page: %s", stale.raw)
	}
	// Restarting from the first page is the documented remedy and it works.
	fresh := estate.sheetPage(t, owner.token, channel.Channel.ID, wsQuery+"&state=all&limit=2")
	if !fresh.HasMore || fresh.Continuation == "" {
		t.Fatalf("restarted sheet = %+v", fresh)
	}
	if _, err := time.Parse(time.RFC3339Nano, fresh.ObservedAt.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("observed_at is not an RFC3339 instant: %v", err)
	}
	// A crossed token never reaches the transaction at all: another reader's
	// continuation is refused as an invalid request, not answered with a page.
	if crossed := estate.sheet(t, estate.steward.token, channel.Channel.ID,
		wsQuery+"&state=all&limit=2&continuation="+fresh.Continuation); crossed.status != http.StatusBadRequest {
		t.Fatalf("another reader's continuation = %d: %s", crossed.status, crossed.raw)
	}

	// And the ordering that matters: a caller who minted its OWN token and then
	// lost the authority to inspect is told 404, never that the revision moved.
	// Authority is decided BEFORE snapshot coherence, so a revision difference is
	// not disclosed to someone who may no longer look at it.
	promoted := estate.grant(t, owner.token, channel.Channel.ID, map[string]any{
		"subject":  channelAdministrationSubject("user", estate.steward.id),
		"can_read": false, "can_write": false, "can_admin": true,
	}, fresh.ETag)
	if promoted.status != http.StatusConflict {
		// The steward already holds a read generation on this Channel, so a second
		// active generation is refused; revoke it first and grant the admin one.
		t.Fatalf("granting a second active generation unexpectedly succeeded: %d %s", promoted.status, promoted.raw)
	}
	current := estate.sheetPage(t, owner.token, channel.Channel.ID,
		wsQuery+"&state=active&subject_kind=user&subject_ref="+estate.steward.id.String())
	if len(current.Items) != 1 {
		t.Fatalf("steward generations on the revision Channel = %+v", current.Items)
	}
	dropped := estate.revoke(t, owner.token, channel.Channel.ID, current.Items[0].Grant.ID, current.ETag)
	if dropped.status != http.StatusOK {
		t.Fatalf("revoke the steward read generation = %d: %s", dropped.status, dropped.raw)
	}
	afterRevoke := communicationHTTPTestDecode[sessions.ChannelMutationResult](t, dropped)
	adminGrant := estate.grant(t, owner.token, channel.Channel.ID, map[string]any{
		"subject":  channelAdministrationSubject("user", estate.steward.id),
		"can_read": false, "can_write": false, "can_admin": true,
	}, afterRevoke.ETag)
	if adminGrant.status != http.StatusOK {
		t.Fatalf("grant the steward an admin generation = %d: %s", adminGrant.status, adminGrant.raw)
	}
	stewardAdmin := communicationHTTPTestDecode[sessions.ChannelMutationResult](t, adminGrant)
	own := estate.sheetPage(t, estate.steward.token, channel.Channel.ID, wsQuery+"&state=all&limit=2")
	if !own.HasMore || own.Continuation == "" {
		t.Fatalf("steward sheet page one = %+v", own)
	}
	removed := estate.revoke(t, owner.token, channel.Channel.ID, stewardAdmin.Grant.ID, own.ETag)
	if removed.status != http.StatusOK {
		t.Fatalf("revoke the steward admin generation = %d: %s", removed.status, removed.raw)
	}
	lost := estate.sheet(t, estate.steward.token, channel.Channel.ID,
		wsQuery+"&state=all&limit=2&continuation="+own.Continuation)
	if lost.status != http.StatusNotFound {
		t.Fatalf("a reader that lost admin resuming its own continuation = %d: %s", lost.status, lost.raw)
	}
	if strings.Contains(string(lost.raw), "channel_snapshot_changed") {
		t.Fatalf("the revision difference was disclosed to a reader without authority: %s", lost.raw)
	}
}

// exerciseChannelAdministrationUnknownEvidenceHTTP proves an unresolvable
// closure is 503 evidence_unavailable on BOTH routes, discloses nothing, and
// leaves no durable effect. An authorized empty page would be a lie.
func exerciseChannelAdministrationUnknownEvidenceHTTP(
	t *testing.T,
	estate channelAdministrationHTTPEstate,
) {
	eng, tenant, workspace := estate.eng, estate.tenant, estate.workspace
	owner := estate.owner
	wsQuery := "workspace_id=" + workspace.String()
	if eng.communicationComposition == nil || eng.communicationComposition.closure == nil {
		t.Fatal("production communication composition is unavailable")
	}
	ownerAll := channelAdministrationGrant(channelAdministrationSubject("user", owner.id), true, true, true)
	channel := estate.createChannel(t, workspace, "unknown-evidence", []map[string]any{ownerAll})

	before := communicationHTTPTestEffects(t, eng, tenant)
	realClosure := sessions.ChannelGrantSubjectClosureResolver(eng.communicationComposition.closure)
	eng.sessionsMod.UseCommunicationChannelGrantSubjectClosureResolver(
		communicationHTTPUnknownGrantClosureResolver{ChannelGrantSubjectClosureResolver: realClosure},
	)
	catalog := estate.admin(t, owner.token, wsQuery+"&limit=5")
	sheet := estate.sheet(t, owner.token, channel.Channel.ID, wsQuery+"&state=all&limit=5")
	eng.sessionsMod.UseCommunicationChannelGrantSubjectClosureResolver(realClosure)

	for name, response := range map[string]communicationHTTPTestResponse{
		"administration catalog": catalog, "grant sheet": sheet,
	} {
		if response.status != http.StatusServiceUnavailable ||
			!strings.Contains(string(response.raw), "evidence_unavailable") ||
			strings.Contains(string(response.raw), channel.Channel.ID.String()) {
			t.Fatalf("UNKNOWN closure %s = %d: %s", name, response.status, response.raw)
		}
	}
	assertCommunicationHTTPTestNoEffects(t, eng, tenant, before, "UNKNOWN closure administration")
}

// channelAdministrationSeededHistory is the number of legal grant generations
// the long-history journey seeds for ONE subject. It is deliberately ABOVE the
// 256 rows the admin writer used to lock, which is why the read model and the
// writer are BOTH measured here on the same Channel.
const channelAdministrationSeededHistory = 300

// seedChannelGrantHistory writes a legal chain of generations for one synthetic
// subject through the typed store API, in one transaction: generation 1 carries
// no predecessor, every later generation supersedes the previous row's exact ID,
// every revoked row carries its revoking actor, and only the last one is active.
// It is a FIXTURE for history size; the journey's own generations are always
// created through the product routes.
func seedChannelGrantHistory(
	t *testing.T,
	eng *engine,
	tenant model.TenantID,
	workspace model.ID,
	channelID model.ID,
	actor model.ID,
	generations int,
) (model.ID, []model.ID) {
	t.Helper()
	subject := model.NewID()
	seeded := make([]model.ID, 0, generations)
	if err := eng.store.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("sessions.channel_grant")
		if err != nil {
			return err
		}
		var previous model.ID
		for generation := 1; generation <= generations; generation++ {
			id := model.NewID()
			record := model.Record{
				"workspace_id": workspace.String(), "channel_id": channelID.String(),
				"subject_kind": "user", "subject_ref": subject.String(),
				"generation": int64(generation),
				"can_read":   true, "can_write": false, "can_admin": false,
				"state":           "revoked",
				"granted_by_kind": "user", "granted_by_ref": actor.String(),
				"revoked_by_kind": "user", "revoked_by_ref": actor.String(),
			}
			if generation == generations {
				record["state"] = "active"
				delete(record, "revoked_by_kind")
				delete(record, "revoked_by_ref")
			}
			if !previous.IsZero() {
				record["supersedes_id"] = previous.String()
			}
			if _, err := repo.CreateWithID(context.Background(), id, record); err != nil {
				return fmt.Errorf("seed generation %d: %w", generation, err)
			}
			seeded = append(seeded, id)
			previous = id
		}
		return nil
	}); err != nil {
		t.Fatalf("seed %d grant generations: %v", generations, err)
	}
	return subject, seeded
}

// TestCommunicationChannelAdministrationLongHistoryHTTP proves the administrative
// sheet enumerates a history LARGER than the writer's retired 256-row lock, page
// by page, on a real SQLite estate through the product router — and that the
// writer now operates on that same Channel instead of answering 503.
func TestCommunicationChannelAdministrationLongHistoryHTTP(t *testing.T) {
	exerciseChannelAdministrationLongHistoryHTTP(t, communicationHTTPTestSQLiteStore(t))
}

// TestCommunicationChannelAdministrationLongHistoryHTTPPostgres runs the same
// measurement against a real isolated split-owner PostgreSQL database.
func TestCommunicationChannelAdministrationLongHistoryHTTPPostgres(t *testing.T) {
	exerciseChannelAdministrationLongHistoryHTTP(t, communicationHTTPTestPostgresStore(t))
}

func exerciseChannelAdministrationLongHistoryHTTP(t *testing.T, storeEstate communicationHTTPTestStore) {
	estate := bootChannelAdministrationHTTPEstate(t, storeEstate)
	eng, tenant, workspace, owner := estate.eng, estate.tenant, estate.workspace, estate.owner
	wsQuery := "workspace_id=" + workspace.String()
	ownerAll := channelAdministrationGrant(channelAdministrationSubject("user", owner.id), true, true, true)

	channel := estate.createChannel(t, workspace, "long-history", []map[string]any{ownerAll})
	subject, seeded := seedChannelGrantHistory(t, eng, tenant, workspace,
		channel.Channel.ID, owner.id, channelAdministrationSeededHistory)
	if len(seeded) != channelAdministrationSeededHistory {
		t.Fatalf("seeded %d generations, want %d", len(seeded), channelAdministrationSeededHistory)
	}

	before := channelCatalogLightCensus(t, eng, tenant)

	// Walk the FULL history in pages. Nothing may be repeated, skipped or
	// reordered, and the walk must terminate.
	walk := func(query string) []string {
		t.Helper()
		var out []string
		seen := map[string]bool{}
		continuation := ""
		// The cap is a termination guard, not a page budget: 301 rows at the
		// smallest size this journey uses need 43 pages, so it is set well above
		// that and a walk that reaches it is a real failure.
		for page := 0; page < 200; page++ {
			current := query
			if continuation != "" {
				current += "&continuation=" + continuation
			}
			got := estate.sheetPage(t, owner.token, channel.Channel.ID, current)
			for _, item := range got.Items {
				id := item.Grant.ID.String()
				if seen[id] {
					t.Fatalf("grant %s repeated across pages", id)
				}
				seen[id] = true
				if len(out) > 0 && out[len(out)-1] >= id {
					t.Fatalf("grant page order broke at %s", id)
				}
				out = append(out, id)
			}
			if !got.HasMore {
				if got.Continuation != "" {
					t.Fatalf("terminal page carries a continuation: %+v", got)
				}
				return out
			}
			if got.Continuation == "" {
				t.Fatalf("non-terminal page carries no continuation: %+v", got)
			}
			continuation = got.Continuation
		}
		t.Fatalf("history walk did not terminate for %q", query)
		return nil
	}

	all := walk(wsQuery + "&state=all&limit=200")
	// The owner's own creation grant plus every seeded generation, and no more.
	if len(all) != channelAdministrationSeededHistory+1 {
		t.Fatalf("state=all enumerated %d rows, want %d",
			len(all), channelAdministrationSeededHistory+1)
	}
	present := map[string]bool{}
	for _, id := range all {
		present[id] = true
	}
	for _, id := range seeded {
		if !present[id.String()] {
			t.Fatalf("seeded generation %s was not enumerated", id)
		}
	}
	// A small page size must reach exactly the same set: the size of the page is
	// not the size of the history.
	if small := walk(wsQuery + "&state=all&limit=7"); len(small) != len(all) {
		t.Fatalf("limit=7 enumerated %d rows, limit=200 enumerated %d", len(small), len(all))
	}
	// The exact-subject filter reaches every generation of that subject and
	// nothing else.
	subjectQuery := wsQuery + "&state=all&limit=64&subject_kind=user&subject_ref=" + subject.String()
	bySubject := walk(subjectQuery)
	if len(bySubject) != channelAdministrationSeededHistory {
		t.Fatalf("subject-filtered walk enumerated %d, want %d",
			len(bySubject), channelAdministrationSeededHistory)
	}
	// The persisted-state selections partition the same history.
	revoked := walk(wsQuery + "&state=revoked&limit=64")
	active := walk(wsQuery + "&state=active&limit=64")
	expired := walk(wsQuery + "&state=expired&limit=64")
	if len(revoked)+len(active)+len(expired) != len(all) {
		t.Fatalf("state partition %d+%d+%d does not sum to %d",
			len(revoked), len(active), len(expired), len(all))
	}
	if len(expired) != 0 || len(active) != 2 {
		t.Fatalf("expected two persisted-active rows and no persisted-expired row: active=%d expired=%d",
			len(active), len(expired))
	}

	// ⚠ THE WRITER'S HISTORICAL BOUND, MEASURED RATHER THAN ASSUMED. Until this
	// increment every admin mutation locked the Channel's WHOLE grant history
	// through lockAllChannelGrants(..., 256), and above that bound the row set
	// was UNKNOWN: even a PATCH answered 503 evidence_unavailable on this very
	// Channel. This test asserted that 503 and said, in so many words, that the
	// day the writer was corrected the expectation had to be changed
	// deliberately. This is that change. The writer now selects only the rows a
	// mutation actually needs, so a configuration change on a Channel with more
	// than 256 generations is an ordinary operation.
	sheet := estate.sheetPage(t, owner.token, channel.Channel.ID, wsQuery+"&state=active&limit=50")
	patched := communicationHTTPTestRequest(t, eng, http.MethodPatch, "/v1/m/sessions/channels",
		owner.token, tenant,
		map[string]any{"channel_id": channel.Channel.ID.String(), "description": "beyond the retired bound"},
		map[string]string{"If-Match": sheet.ETag})
	if patched.status != http.StatusOK {
		t.Fatalf("PATCH on a Channel with %d generations = %d: %s", len(all), patched.status, patched.raw)
	}
	administered := communicationHTTPTestDecode[sessions.ChannelMutationResult](t, patched)
	if administered.Channel.Description != "beyond the retired bound" ||
		administered.Channel.Version != sheet.Channel.Version+1 ||
		administered.Channel.ACLRevision != sheet.Channel.ACLRevision {
		t.Fatalf("PATCH past the retired bound = %+v", administered.Channel)
	}
	// The whole history is still there afterwards, to the row: a mutation that
	// works past the bound must not be one that trimmed the history to get there.
	retained := walk(wsQuery + "&state=all&limit=200")
	if len(retained) != len(all) {
		t.Fatalf("history after the mutation holds %d rows, want the same %d", len(retained), len(all))
	}
	for index, id := range all {
		if retained[index] != id {
			t.Fatalf("history row %d changed from %s to %s", index, id, retained[index])
		}
	}
	// And the read model keeps working on exactly that Channel.
	if after := estate.sheetPage(t, owner.token, channel.Channel.ID, wsQuery+"&state=all&limit=200"); len(after.Items) != 200 {
		t.Fatalf("sheet after the mutation = %d items", len(after.Items))
	}
	// The light census counts messages, deliveries, acks, commands, outbox rows
	// and cursors: a Channel administration act moves none of them. The Channel's
	// own audit is counted by the writer journey's census instead.
	after := channelCatalogLightCensus(t, eng, tenant)
	if after.messages != before.messages || after.deliveries != before.deliveries ||
		after.acks != before.acks || after.commands != before.commands ||
		after.outbox != before.outbox || after.cursors != before.cursors {
		t.Fatalf("the long-history journey moved messaging state: before=%+v after=%+v", before, after)
	}
	if after.successAudits != before.successAudits+1 {
		t.Fatalf("the long-history journey appended %d acts, want exactly the one PATCH",
			after.successAudits-before.successAudits)
	}
}

// channelAdministrationCacheProbe is one real response of one of the two
// administrative routes, kept with the name of the condition that produced it so
// a failure says WHICH refusal was cacheable rather than "a header was missing".
type channelAdministrationCacheProbe struct {
	name     string
	response communicationHTTPTestResponse
	want     int
}

// exerciseChannelAdministrationNoStoreHTTP proves `Cache-Control: no-store` is a
// property of the ROUTE and not of its happy path.
//
// A private cache may store a 400 or a 404 by default (RFC 9111 §4.2.2 heuristic
// freshness), and on these two routes those are exactly the answers that must
// never be replayed: a concealed denial replayed after a grant is issued, or a
// rejected query replayed after the selection changed, both hand the next caller
// an answer the server would no longer give. The engine's own refusals — entity
// lineage denial, authorization denial, concealed not-found — are written before
// the module handler runs, which is why the directive is declared at the route
// and not set inside the handler.
//
// It also re-asserts the ratified negative: no HTTP ETag, no 304, on success and
// under `If-None-Match: *`.
func exerciseChannelAdministrationNoStoreHTTP(
	t *testing.T,
	estate channelAdministrationHTTPEstate,
	administrable model.ID,
	concealed model.ID,
) {
	eng, tenant, workspace := estate.eng, estate.tenant, estate.workspace
	steward, viewer := estate.steward, estate.viewer
	wsQuery := "workspace_id=" + workspace.String()

	probes := []channelAdministrationCacheProbe{
		{"catalog 200", estate.admin(t, steward.token, wsQuery+"&limit=50"), http.StatusOK},
		{"catalog 400 malformed workspace", estate.admin(t, steward.token, "workspace_id=not-a-uuid"), http.StatusBadRequest},
		{"catalog 400 unknown state", estate.admin(t, steward.token, wsQuery+"&state=deleted"), http.StatusBadRequest},
		// Since G1-A these two are ONE public answer, and that is the ratified
		// requirement rather than a coincidence: an absent workspace and a denied
		// caller must not be distinguishable before admission. The bodies are
		// compared byte for byte in the capability battery.
		{"catalog 403 foreign workspace", estate.admin(t, steward.token, "workspace_id="+estate.foreignWorkspace), http.StatusForbidden},
		{"catalog 403 without core admin", estate.admin(t, viewer.token, wsQuery), http.StatusForbidden},
		{"sheet 200", estate.sheet(t, steward.token, administrable, wsQuery+"&state=all&limit=50"), http.StatusOK},
		{"sheet 400 unknown state", estate.sheet(t, steward.token, administrable, wsQuery+"&state=pending"), http.StatusBadRequest},
		{"sheet 400 partial subject", estate.sheet(t, steward.token, administrable, wsQuery+"&subject_kind=user"), http.StatusBadRequest},
		{"sheet 404 concealed denial", estate.sheet(t, steward.token, concealed, wsQuery), http.StatusNotFound},
		{"sheet 404 crossed workspace selector", estate.sheet(t, steward.token, administrable, "workspace_id="+estate.sideWorkspace.String()), http.StatusNotFound},
		{"sheet 404 concealed by core denial", estate.sheet(t, viewer.token, administrable, wsQuery), http.StatusNotFound},
	}
	for _, probe := range probes {
		if probe.response.status != probe.want {
			t.Fatalf("%s = %d: %s", probe.name, probe.response.status, probe.response.raw)
		}
		if got := probe.response.header.Get("Cache-Control"); got != "no-store" {
			t.Fatalf("%s Cache-Control = %q, want no-store — this refusal is cacheable",
				probe.name, got)
		}
		if got := probe.response.header.Get("ETag"); got != "" {
			t.Fatalf("%s emitted an HTTP ETag %q", probe.name, got)
		}
	}

	// The 503 path: an unresolvable closure. It is produced inside the module, so
	// it must carry the directive like every other answer.
	if eng.communicationComposition == nil || eng.communicationComposition.closure == nil {
		t.Fatal("production communication composition is unavailable")
	}
	realClosure := sessions.ChannelGrantSubjectClosureResolver(eng.communicationComposition.closure)
	eng.sessionsMod.UseCommunicationChannelGrantSubjectClosureResolver(
		communicationHTTPUnknownGrantClosureResolver{ChannelGrantSubjectClosureResolver: realClosure},
	)
	unavailable := []channelAdministrationCacheProbe{
		{"catalog 503", estate.admin(t, steward.token, wsQuery+"&limit=5"), http.StatusServiceUnavailable},
		{"sheet 503", estate.sheet(t, steward.token, administrable, wsQuery), http.StatusServiceUnavailable},
	}
	eng.sessionsMod.UseCommunicationChannelGrantSubjectClosureResolver(realClosure)
	for _, probe := range unavailable {
		if probe.response.status != probe.want {
			t.Fatalf("%s = %d: %s", probe.name, probe.response.status, probe.response.raw)
		}
		if got := probe.response.header.Get("Cache-Control"); got != "no-store" {
			t.Fatalf("%s Cache-Control = %q, want no-store", probe.name, got)
		}
	}

	// Success under a conditional request stays a full 200: the body etag is the
	// Channel precondition, never a representation validator.
	for name, path := range map[string]string{
		"catalog": "/v1/m/sessions/channels/administration?" + wsQuery,
		"sheet":   "/v1/m/sessions/channels/" + administrable.String() + "/grants?" + wsQuery,
	} {
		conditional := communicationHTTPTestRequest(t, eng, http.MethodGet, path,
			steward.token, tenant, nil, map[string]string{"If-None-Match": "*"})
		if conditional.status != http.StatusOK {
			t.Fatalf("%s with If-None-Match: * = %d: %s", name, conditional.status, conditional.raw)
		}
		if got := conditional.header.Get("Cache-Control"); got != "no-store" {
			t.Fatalf("%s conditional Cache-Control = %q", name, got)
		}
		if got := conditional.header.Get("ETag"); got != "" {
			t.Fatalf("%s conditional emitted an HTTP ETag %q", name, got)
		}
	}

	// ⛔ 401 HAS TWO PATHS AND ONLY ONE OF THEM WAS EVER COVERED HERE.
	//
	// An ABSENT credential reaches the route anonymously and is refused INSIDE it,
	// so it always carried the directive. An INVALID SUPPLIED credential is refused
	// by `authenticate` BEFORE chi routes the request, and an independent HTTP
	// witness measured that one arriving with no `Cache-Control` at all. Reporting
	// "401 is covered" from the first was the confusion the review named, so both
	// are now driven, named apart, and ASSERTED.
	anonymousCatalog := estate.admin(t, "", wsQuery)
	anonymousSheet := estate.sheet(t, "", administrable, wsQuery)
	if anonymousCatalog.status != http.StatusUnauthorized || anonymousSheet.status != http.StatusUnauthorized {
		t.Fatalf("anonymous administrative reads = %d/%d, want 401/401",
			anonymousCatalog.status, anonymousSheet.status)
	}
	invalidCatalog := estate.admin(t, "not-a-real-bearer-token", wsQuery)
	invalidSheet := estate.sheet(t, "not-a-real-bearer-token", administrable, wsQuery)
	if invalidCatalog.status != http.StatusUnauthorized || invalidSheet.status != http.StatusUnauthorized {
		t.Fatalf("invalid-credential administrative reads = %d/%d, want 401/401",
			invalidCatalog.status, invalidSheet.status)
	}
	t.Logf("K3_ADMIN_NOSTORE_401|absent_catalog=%q|absent_sheet=%q|invalid_catalog=%q|invalid_sheet=%q",
		anonymousCatalog.header.Get("Cache-Control"), anonymousSheet.header.Get("Cache-Control"),
		invalidCatalog.header.Get("Cache-Control"), invalidSheet.header.Get("Cache-Control"))
	for name, response := range map[string]communicationHTTPTestResponse{
		"absent-credential catalog 401":  anonymousCatalog,
		"absent-credential sheet 401":    anonymousSheet,
		"invalid-credential catalog 401": invalidCatalog,
		"invalid-credential sheet 401":   invalidSheet,
	} {
		if got := response.header.Get("Cache-Control"); got != "no-store" {
			t.Fatalf("%s Cache-Control = %q, want no-store", name, got)
		}
	}

	// The PLAIN SIBLING controls, on the same engine and the same two paths a
	// declaration now reaches: the pre-existing read catalog declares nothing and
	// must keep sending no cache directive, on its success and on its own
	// pre-routing refusal. Without these the assertions above would also pass for
	// an implementation that simply stamped every response in the product.
	siblingOK := communicationHTTPTestRequest(t, eng, http.MethodGet,
		"/v1/m/sessions/channels?"+wsQuery, steward.token, tenant, nil, nil)
	if siblingOK.status != http.StatusOK {
		t.Fatalf("sibling read catalog = %d: %s", siblingOK.status, siblingOK.raw)
	}
	siblingInvalid := communicationHTTPTestRequest(t, eng, http.MethodGet,
		"/v1/m/sessions/channels?"+wsQuery, "not-a-real-bearer-token", tenant, nil, nil)
	if siblingInvalid.status != http.StatusUnauthorized {
		t.Fatalf("sibling read catalog with an invalid bearer = %d: %s",
			siblingInvalid.status, siblingInvalid.raw)
	}
	t.Logf("K3_ADMIN_NOSTORE_SIBLING|read_catalog_200=%q|read_catalog_invalid_401=%q",
		siblingOK.header.Get("Cache-Control"), siblingInvalid.header.Get("Cache-Control"))
	for name, response := range map[string]communicationHTTPTestResponse{
		"sibling read catalog 200":             siblingOK,
		"sibling read catalog pre-routing 401": siblingInvalid,
	} {
		if got := response.header.Get("Cache-Control"); got != "" {
			t.Fatalf("%s Cache-Control = %q, want none — the route declared nothing and "+
				"the resolution must not have become a global policy", name, got)
		}
	}

	// ── ESCAPED PATHS: the metadata must follow the route chi DISPATCHES ──
	exerciseChannelAdministrationEscapedPathNoStoreHTTP(t, estate)
}

// channelAdministrationEscapedProbe is one escaped-URL regression case: the
// request as it goes on the wire, whether it is authenticated, and what the two
// answers a PRE-ROUTING writer produces must carry.
type channelAdministrationEscapedProbe struct {
	name          string
	path          string
	authenticated bool
	wantStatus    int
	wantCache     string
	why           string
}

// channelAdministrationEscapedProbes is the permanent regression set for
// K3-IR03-A, and it is a SET rather than two cases because the defect had two
// opposite halves and fixing one direction alone reintroduces the other.
//
// ⛔ THE DEFECT. The pre-routing resolver matched on `r.URL.Path`, the DECODED
// path, while chi's own dispatch (mux.go routeHTTP) prefers a non-empty
// `URL.RawPath`. For an escaped URL the two disagree about which route is being
// served, and an independent HTTP witness measured it going wrong both ways on
// this exact router:
//
//   - `channels/bad%2Fid/grants` is dispatched by chi to the DECLARED grant sheet
//     ({id}/grants with id="bad%2Fid"); its decoded form `channels/bad/id/grants`
//     matches no route at all, so the pre-routing 401 of a route that DID declare
//     no-store went out without it. This is the same early-response gap IR03
//     exists to close, reached through a different door.
//   - `channels/%61dministration` is dispatched by chi to the ordinary `{id}`
//     detail route, which declares NOTHING; its decoded form is the administrative
//     catalog. So a route that never asked for the directive received it — on its
//     401 and on its authenticated 404. That is the "global cache policy" this
//     middleware is forbidden to become, arriving by the back door, and it is the
//     half that a fix aimed only at the missing header would leave in place.
//
// The authenticated rows are not decoration: they pin that the ROUTE the request
// really reaches is the one whose policy applies, measured on a real refusal the
// module itself writes. The concealed malformed-locator answer is **404, not 400**
// — the product conceals a locator it cannot parse rather than reporting its
// shape, and this regression records that status rather than changing it.
func channelAdministrationEscapedProbes() []channelAdministrationEscapedProbe {
	return []channelAdministrationEscapedProbe{
		{
			name: "escaped sheet segment, invalid bearer", authenticated: false,
			path:       "/v1/m/sessions/channels/bad%2Fid/grants",
			wantStatus: http.StatusUnauthorized, wantCache: "no-store",
			why: "chi dispatches the raw segment to the DECLARED sheet route",
		},
		{
			name: "escaped sheet segment, authenticated", authenticated: true,
			path:       "/v1/m/sessions/channels/bad%2Fid/grants",
			wantStatus: http.StatusNotFound, wantCache: "no-store",
			why: "the sheet route conceals a locator it cannot parse, and it declared no-store",
		},
		{
			name: "escaped literal that routes as the plain detail sibling, invalid bearer",
			path: "/v1/m/sessions/channels/%61dministration", authenticated: false,
			wantStatus: http.StatusUnauthorized, wantCache: "",
			why: "the raw literal is an {id}, not the catalog: that route declared nothing",
		},
		{
			name: "escaped literal that routes as the plain detail sibling, authenticated",
			path: "/v1/m/sessions/channels/%61dministration", authenticated: true,
			wantStatus: http.StatusNotFound, wantCache: "",
			why: "the detail route's own concealed refusal, still with no declared directive",
		},
	}
}

// exerciseChannelAdministrationEscapedPathNoStoreHTTP drives the escaped-URL set
// on the real mounted router, for the EARLY invalid-credential refusal and for the
// route's own authenticated refusal.
//
// The plain-path equivalents stay where they are, above: this function is additive
// and the normal 401/429 and normal-sibling controls are what make these four
// meaningful rather than a private dialect about percent-encoding.
func exerciseChannelAdministrationEscapedPathNoStoreHTTP(
	t *testing.T,
	estate channelAdministrationHTTPEstate,
) {
	t.Helper()
	suffix := "?workspace_id=" + estate.workspace.String()
	for _, probe := range channelAdministrationEscapedProbes() {
		bearer := "not-a-real-bearer-token"
		if probe.authenticated {
			bearer = estate.owner.token
		}
		response := communicationHTTPTestRequest(t, estate.eng, http.MethodGet,
			probe.path+suffix, bearer, estate.tenant, nil, nil)
		t.Logf("K3_ADMIN_NOSTORE_ESCAPED|case=%q|path=%s|authenticated=%t|status=%d|cache=%q",
			probe.name, probe.path, probe.authenticated, response.status,
			response.header.Get("Cache-Control"))
		// ⛔ Errorf, NOT Fatalf, AND THE REASON IS THE DEFECT'S SHAPE. K3-IR03-A
		// went wrong in two OPPOSITE directions — a declared route losing its
		// directive and an undeclared one gaining it — so the set only means
		// something if a run reports every direction it broke. Stopping at the
		// first case would let a counterfactual (or a future regression) show one
		// half and hide the other, which is exactly how the second half survived
		// the first correction.
		if response.status != probe.wantStatus {
			t.Errorf("%s = %d, want %d (%s): %s",
				probe.name, response.status, probe.wantStatus, probe.why, response.raw)
		}
		if got := response.header.Get("Cache-Control"); got != probe.wantCache {
			t.Errorf("%s Cache-Control = %q, want %q — %s",
				probe.name, got, probe.wantCache, probe.why)
		}
		if got := response.header.Get("ETag"); got != "" {
			t.Errorf("%s emitted an HTTP ETag %q", probe.name, got)
		}
	}
}

// TestCommunicationChannelAdministrationRateLimitedHTTP drives the GLOBAL rate
// limiter's real 429 through BOTH administrative GETs on the production router.
//
// ⛔ IT NEEDS ITS OWN ENGINE, AND THAT IS THE POINT. The limiter answers before
// chi routes the request, so the only honest way to see what a throttled caller
// receives is to throttle a real caller: an operator rate-limit configuration
// with a read bucket small enough to empty deterministically, loaded by the
// composition root's own loader, on an estate built for it. The main journey
// cannot host this — every one of its reads would be metered against the same
// bucket — so it lives here, alone, with a minimal estate.
//
// It asserts the two directions together, because either alone is misleading:
// the DECLARED routes' 429 carries `Cache-Control: no-store`, and the plain
// sibling's 429 — same limiter, same emptied bucket, same tenant — carries
// nothing at all. That pair is what distinguishes route-specific response
// metadata resolved before the limiter from a global cache policy.
func TestCommunicationChannelAdministrationRateLimitedHTTP(t *testing.T) {
	// A read bucket that empties and does not meaningfully refill, and write and
	// aggregate budgets generous enough that building the estate is unaffected.
	// Rate is positive and burst >= 1, so the limiter honours these rather than
	// clamping them to its hard floor.
	config := filepath.Join(t.TempDir(), "ratelimit.json")
	body, err := json.Marshal(map[string]any{
		"mode":         "enforce",
		"default_tier": "k3tight",
		"tiers": map[string]any{
			"k3tight": map[string]any{
				"per_class": map[string]any{
					"read":  map[string]any{"rate": 0.001, "burst": 80},
					"write": map[string]any{"rate": 1000, "burst": 100000},
				},
				"total": map[string]any{"rate": 1000, "burst": 100000},
			},
		},
	})
	if err != nil {
		t.Fatalf("encode rate-limit config: %v", err)
	}
	if err := os.WriteFile(config, body, 0o600); err != nil {
		t.Fatalf("write rate-limit config: %v", err)
	}
	t.Setenv("OLIVARES_RATELIMIT_CONFIG", config)

	estate := bootChannelAdministrationHTTPEstate(t, communicationHTTPTestSQLiteStore(t))
	eng, tenant, workspace := estate.eng, estate.tenant, estate.workspace
	owner, steward := estate.owner, estate.steward
	wsQuery := "workspace_id=" + workspace.String()

	ownerAll := channelAdministrationGrant(channelAdministrationSubject("user", owner.id), true, true, true)
	stewardAdminOnly := channelAdministrationGrant(
		channelAdministrationSubject("user", steward.id), false, false, true)
	channel := estate.createChannel(t, workspace, "admin-rate-limited",
		[]map[string]any{ownerAll, stewardAdminOnly})

	// ── Before the bucket is empty: both routes answer, and they answer 200 ──
	catalog := estate.admin(t, steward.token, wsQuery+"&limit=50")
	sheet := estate.sheet(t, steward.token, channel.Channel.ID, wsQuery+"&state=all&limit=50")
	if catalog.status != http.StatusOK || sheet.status != http.StatusOK {
		t.Fatalf("pre-limit administrative reads = %d/%d, want 200/200",
			catalog.status, sheet.status)
	}
	sibling := communicationHTTPTestRequest(t, eng, http.MethodGet,
		"/v1/m/sessions/channels?"+wsQuery, owner.token, tenant, nil, nil)
	if sibling.status != http.StatusOK {
		t.Fatalf("pre-limit sibling read catalog = %d: %s", sibling.status, sibling.raw)
	}
	t.Logf("K3_ADMIN_RATELIMIT_POSITIVE|catalog_200=%q|sheet_200=%q|sibling_200=%q|"+
		"ratelimit_limit=%q|ratelimit_remaining=%q",
		catalog.header.Get("Cache-Control"), sheet.header.Get("Cache-Control"),
		sibling.header.Get("Cache-Control"),
		catalog.header.Get("RateLimit-Limit"), catalog.header.Get("RateLimit-Remaining"))
	if catalog.header.Get("Cache-Control") != "no-store" ||
		sheet.header.Get("Cache-Control") != "no-store" {
		t.Fatal("the administrative 200s lost their directive under a metered engine")
	}
	if got := sibling.header.Get("Cache-Control"); got != "" {
		t.Fatalf("the sibling's 200 carries Cache-Control=%q", got)
	}

	// ── Empty the shared read bucket ──
	var limitedCatalog communicationHTTPTestResponse
	for attempt := 0; attempt < 400; attempt++ {
		limitedCatalog = estate.admin(t, steward.token, wsQuery+"&limit=1")
		if limitedCatalog.status == http.StatusTooManyRequests {
			break
		}
		if limitedCatalog.status != http.StatusOK {
			t.Fatalf("administrative catalog while draining the bucket = %d: %s",
				limitedCatalog.status, limitedCatalog.raw)
		}
	}
	if limitedCatalog.status != http.StatusTooManyRequests {
		t.Fatalf("the read bucket never emptied; last administrative catalog = %d",
			limitedCatalog.status)
	}
	limitedSheet := estate.sheet(t, steward.token, channel.Channel.ID, wsQuery)
	limitedSibling := communicationHTTPTestRequest(t, eng, http.MethodGet,
		"/v1/m/sessions/channels?"+wsQuery, steward.token, tenant, nil, nil)
	if limitedSheet.status != http.StatusTooManyRequests ||
		limitedSibling.status != http.StatusTooManyRequests {
		t.Fatalf("sheet/sibling under the emptied bucket = %d/%d, want 429/429",
			limitedSheet.status, limitedSibling.status)
	}
	t.Logf("K3_ADMIN_RATELIMIT_429|catalog=%q|sheet=%q|plain_sibling=%q",
		limitedCatalog.header.Get("Cache-Control"), limitedSheet.header.Get("Cache-Control"),
		limitedSibling.header.Get("Cache-Control"))
	for name, response := range map[string]communicationHTTPTestResponse{
		"administrative catalog 429": limitedCatalog,
		"administrative sheet 429":   limitedSheet,
	} {
		if got := response.header.Get("Cache-Control"); got != "no-store" {
			t.Fatalf("%s Cache-Control = %q, want no-store. The published contract now "+
				"declares the header on 429 for these routes, so an omission here is a "+
				"promise the server does not keep", name, got)
		}
		if got := response.header.Get("ETag"); got != "" {
			t.Fatalf("%s emitted an HTTP ETag %q", name, got)
		}
	}
	if got := limitedSibling.header.Get("Cache-Control"); got != "" {
		t.Fatalf("the plain sibling's 429 carries Cache-Control=%q; the limiter's response "+
			"has been given a cache policy no route asked it for", got)
	}

	// ── The SAME emptied bucket, reached through ESCAPED paths ──
	//
	// The limiter is the other pre-routing writer, so K3-IR03-A applies to it
	// exactly as it applies to `authenticate`, and the bucket is already empty:
	// every request below is a real 429 from the real middleware. The escaped
	// sheet must carry the directive its route declared, and the escaped literal —
	// which chi dispatches to the plain `{id}` detail route — must carry none.
	for _, probe := range channelAdministrationEscapedProbes() {
		if probe.authenticated {
			// The authenticated rows of that set are about the route's own refusal;
			// here the limiter answers first, so drive each PATH once.
			continue
		}
		limited := communicationHTTPTestRequest(t, eng, http.MethodGet,
			probe.path+"?"+wsQuery, steward.token, tenant, nil, nil)
		t.Logf("K3_ADMIN_RATELIMIT_429_ESCAPED|path=%s|status=%d|cache=%q",
			probe.path, limited.status, limited.header.Get("Cache-Control"))
		// Errorf here for the same reason as the 401 set above: both directions.
		if limited.status != http.StatusTooManyRequests {
			t.Errorf("escaped path %s under the emptied bucket = %d, want 429: %s",
				probe.path, limited.status, limited.raw)
		}
		if got := limited.header.Get("Cache-Control"); got != probe.wantCache {
			t.Errorf("escaped path %s: limiter 429 Cache-Control = %q, want %q — %s",
				probe.path, got, probe.wantCache, probe.why)
		}
	}
}
