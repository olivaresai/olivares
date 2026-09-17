// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
)

type channelCatalogHTTPEstate struct {
	eng              *engine
	tenant           model.TenantID
	other            model.TenantID
	owner            communicationHTTPTestUser
	reader           communicationHTTPTestUser
	stranger         communicationHTTPTestUser
	workspace        model.ID
	foreignWorkspace string
}

// bootChannelCatalogHTTPEstate is the fresh, explicitly activated estate every
// catalog HTTP test starts from: setup, admin, two tenants, an owner and an
// editor reader in the catalog tenant, a stranger owner elsewhere, a reopen
// after tenant bootstrap, and one product-created workspace per tenant.
func bootChannelCatalogHTTPEstate(t *testing.T) channelCatalogHTTPEstate {
	t.Helper()
	return bootChannelCatalogHTTPEstateOn(t, communicationHTTPTestSQLiteStore(t))
}

// bootChannelCatalogHTTPEstateOn boots that same estate on a caller-owned
// backing store, so one journey can be exercised on SQLite and on an isolated
// split-owner PostgreSQL database without duplicating the provisioning steps.
func bootChannelCatalogHTTPEstateOn(
	t *testing.T,
	estate communicationHTTPTestStore,
) channelCatalogHTTPEstate {
	t.Helper()
	eng := bootActivatedCommunicationHTTPTestEngine(t, estate)
	setupToken, _, err := eng.setupTok.Ensure()
	if err != nil {
		t.Fatalf("setup token: %v", err)
	}
	setup := communicationHTTPTestRequest(t, eng, http.MethodPost, "/v1/setup", "", "",
		map[string]any{"token": setupToken, "email": "admin@k3-catalog.test", "password": "k3-catalog-admin-password"}, nil)
	if setup.status != http.StatusCreated {
		t.Fatalf("setup = %d: %s", setup.status, setup.raw)
	}
	login := communicationHTTPTestRequest(t, eng, http.MethodPost, "/v1/auth/login", "", "",
		map[string]any{"email": "admin@k3-catalog.test", "password": "k3-catalog-admin-password"}, nil)
	if login.status != http.StatusOK {
		t.Fatalf("admin login = %d: %s", login.status, login.raw)
	}
	admin := communicationHTTPTestDecode[struct {
		Token string `json:"token"`
	}](t, login)
	createdOrg := communicationHTTPTestRequest(t, eng, http.MethodPost, "/v1/system/orgs",
		admin.Token, "", map[string]any{"name": "K3 catalog", "slug": "k3-catalog"}, nil)
	if createdOrg.status != http.StatusCreated {
		t.Fatalf("create tenant = %d: %s", createdOrg.status, createdOrg.raw)
	}
	org := communicationHTTPTestDecode[struct {
		TenantID model.TenantID `json:"tenant_id"`
	}](t, createdOrg)
	otherOrg := communicationHTTPTestRequest(t, eng, http.MethodPost, "/v1/system/orgs",
		admin.Token, "", map[string]any{"name": "K3 catalog other", "slug": "k3-catalog-other"}, nil)
	if otherOrg.status != http.StatusCreated {
		t.Fatalf("create other tenant = %d: %s", otherOrg.status, otherOrg.raw)
	}
	other := communicationHTTPTestDecode[struct {
		TenantID model.TenantID `json:"tenant_id"`
	}](t, otherOrg)
	owner := createCommunicationHTTPTestUser(t, eng, admin.Token, org.TenantID, "owner@k3-catalog.test", auth.RoleOwner)
	reader := createCommunicationHTTPTestUser(t, eng, admin.Token, org.TenantID, "reader@k3-catalog.test", auth.RoleEditor)
	stranger := createCommunicationHTTPTestUser(t, eng, admin.Token, other.TenantID, "stranger@k3-catalog.test", auth.RoleOwner)

	// Reopen after tenant bootstrap exactly as the configured installation does.
	if err := eng.Close(); err != nil {
		t.Fatalf("close bootstrapped estate: %v", err)
	}
	eng = bootCommunicationHTTPTestEngine(t, estate)
	t.Cleanup(func() { _ = eng.Close() })
	if readiness, err := eng.sessionsMod.EvaluateCommunicationReadiness(context.Background()); err != nil || !readiness.Effective {
		t.Fatalf("communication readiness after bootstrap restart = %+v, err %v", readiness, err)
	}
	owner = loginCommunicationHTTPTestUser(t, eng, owner.id, "owner@k3-catalog.test")
	reader = loginCommunicationHTTPTestUser(t, eng, reader.id, "reader@k3-catalog.test")
	stranger = loginCommunicationHTTPTestUser(t, eng, stranger.id, "stranger@k3-catalog.test")
	stepUpCommunicationHTTPTestUser(t, eng, owner.token)
	stepUpCommunicationHTTPTestUser(t, eng, stranger.token)
	tenant := org.TenantID

	workspaceResponse := communicationHTTPTestRequest(t, eng, http.MethodPost, "/v1/workspaces", owner.token, tenant,
		map[string]any{"name": "K3 catalog workspace", "slug": "k3-catalog-ws"}, nil)
	if workspaceResponse.status != http.StatusCreated {
		t.Fatalf("create workspace = %d: %s", workspaceResponse.status, workspaceResponse.raw)
	}
	workspace, err := model.ParseID(communicationHTTPTestDecode[struct {
		ID string `json:"id"`
	}](t, workspaceResponse).ID)
	if err != nil {
		t.Fatalf("workspace id: %v", err)
	}
	strangerWorkspace := communicationHTTPTestRequest(t, eng, http.MethodPost, "/v1/workspaces", stranger.token, other.TenantID,
		map[string]any{"name": "Stranger workspace", "slug": "stranger-ws"}, nil)
	if strangerWorkspace.status != http.StatusCreated {
		t.Fatalf("create stranger workspace = %d: %s", strangerWorkspace.status, strangerWorkspace.raw)
	}
	foreignWorkspace := communicationHTTPTestDecode[struct {
		ID string `json:"id"`
	}](t, strangerWorkspace).ID
	return channelCatalogHTTPEstate{
		eng: eng, tenant: tenant, other: other.TenantID, owner: owner, reader: reader, stranger: stranger,
		workspace: workspace, foreignWorkspace: foreignWorkspace,
	}
}

func (e channelCatalogHTTPEstate) catalog(t *testing.T, token string, query string) communicationHTTPTestResponse {
	t.Helper()
	return communicationHTTPTestRequest(t, e.eng, http.MethodGet, "/v1/m/sessions/channels?"+query, token, e.tenant, nil, nil)
}

func (e channelCatalogHTTPEstate) page(t *testing.T, token string, query string) sessions.ChannelCatalogPage {
	t.Helper()
	response := e.catalog(t, token, query)
	if response.status != http.StatusOK {
		t.Fatalf("catalog %q = %d: %s", query, response.status, response.raw)
	}
	return communicationHTTPTestDecode[sessions.ChannelCatalogPage](t, response)
}

func (e channelCatalogHTTPEstate) create(t *testing.T, slug string, grants []map[string]any) sessions.ChannelMutationResult {
	t.Helper()
	response := communicationHTTPTestRequest(t, e.eng, http.MethodPost, "/v1/m/sessions/channels", e.owner.token, e.tenant,
		map[string]any{"workspace_id": e.workspace.String(), "slug": slug, "name": "Channel " + slug, "initial_grants": grants}, nil)
	if response.status != http.StatusCreated {
		t.Fatalf("create Channel %s = %d: %s", slug, response.status, response.raw)
	}
	return communicationHTTPTestDecode[sessions.ChannelMutationResult](t, response)
}

func channelCatalogSubject(kind string, ref model.ID) map[string]any {
	return map[string]any{"kind": kind, "ref": ref.String()}
}

func channelCatalogGrant(subject map[string]any, read, write, admin bool) map[string]any {
	return map[string]any{"subject": subject, "can_read": read, "can_write": write, "can_admin": admin}
}

func channelCatalogIDs(page sessions.ChannelCatalogPage) []string {
	out := make([]string, 0, len(page.Items))
	for _, item := range page.Items {
		out = append(out, item.ID.String())
	}
	return out
}

// TestCommunicationChannelCatalogFreshHTTP drives GET /v1/m/sessions/channels
// through the production router on a fresh, explicitly activated SQLite estate:
// query and selector validation, Channel creation through the first-delivery
// route, user and exchanged-agent readers, visibility by current local read
// grant only, revocation and expiry between requests, opaque continuation
// disclosure, an UNKNOWN closure, and an unchanged effect census across every
// catalog request.
func TestCommunicationChannelCatalogFreshHTTP(t *testing.T) {
	estate := bootChannelCatalogHTTPEstate(t)
	eng, tenant, owner, reader, stranger, workspace := estate.eng, estate.tenant, estate.owner, estate.reader, estate.stranger, estate.workspace
	foreignWorkspace := estate.foreignWorkspace
	catalog := func(token string, query string) communicationHTTPTestResponse { return estate.catalog(t, token, query) }
	page := func(token string, query string) sessions.ChannelCatalogPage { return estate.page(t, token, query) }
	ids := channelCatalogIDs
	create := func(slug string, grants []map[string]any) sessions.ChannelMutationResult {
		return estate.create(t, slug, grants)
	}
	subject := channelCatalogSubject
	grant := channelCatalogGrant
	wsQuery := "workspace_id=" + workspace.String()

	t.Run("query and selector validation", func(t *testing.T) {
		before := communicationHTTPTestEffects(t, eng, tenant)
		for name, query := range map[string]string{
			"missing workspace":        "limit=1",
			"malformed workspace":      "workspace_id=not-a-uuid",
			"unknown key":              wsQuery + "&cursor=abc",
			"raw position key":         wsQuery + "&after=abc",
			"repeated limit":           wsQuery + "&limit=1&limit=2",
			"limit zero":               wsQuery + "&limit=0",
			"limit above maximum":      wsQuery + "&limit=201",
			"limit non-numeric":        wsQuery + "&limit=abc",
			"limit non-canonical":      wsQuery + "&limit=01",
			"raw channel continuation": wsQuery + "&continuation=" + model.NewID().String(),
			"raw c3n1 continuation":    wsQuery + "&continuation=c3n1.raw",
		} {
			if response := catalog(reader.token, query); response.status != http.StatusBadRequest {
				t.Fatalf("%s = %d: %s", name, response.status, response.raw)
			}
		}
		// ⛔ THIS EXPECTED 404 UNTIL G1-A. The workspace selector of this collection is
		// now corroborated BEFORE the route is authorized — that is what lets a
		// workspace-scoped grant reach the exact collection it was granted for — and
		// once the resolver runs in front of the decision its answers become visible to
		// a caller who has not been authorized. A 404 here, beside the 403 an
		// unauthorized caller gets on the active workspace, would report which
		// workspaces exist. A known absence, a non-active workspace and a denial now
		// share ONE public refusal, and the capability battery compares the bodies byte
		// for byte. The 400s above are unchanged: form is still judged before any
		// lookup, and the handler's closed query set still answers behind it.
		if response := catalog(reader.token, "workspace_id="+foreignWorkspace); response.status != http.StatusForbidden {
			t.Fatalf("other tenant workspace = %d: %s", response.status, response.raw)
		}
		if response := communicationHTTPTestRequest(t, eng, http.MethodGet,
			"/v1/m/sessions/channels?"+wsQuery, stranger.token, tenant, nil, nil); response.status != http.StatusForbidden && response.status != http.StatusUnauthorized {
			t.Fatalf("stranger tenant = %d: %s", response.status, response.raw)
		}
		if response := catalog("", wsQuery); response.status != http.StatusUnauthorized {
			t.Fatalf("anonymous = %d: %s", response.status, response.raw)
		}
		empty := page(reader.token, wsQuery)
		if len(empty.Items) != 0 || empty.HasMore || empty.Continuation != "" {
			t.Fatalf("empty workspace page = %+v", empty)
		}
		assertCommunicationHTTPTestNoEffects(t, eng, tenant, before, "catalog validation and empty page")
	})

	ownerAll := grant(subject("user", owner.id), true, true, true)

	readOnly := create("read-only", []map[string]any{ownerAll, grant(subject("user", reader.id), true, false, false)})
	writeOnly := create("write-only", []map[string]any{ownerAll, grant(subject("user", reader.id), false, true, false)})
	adminOnly := create("admin-only", []map[string]any{ownerAll, grant(subject("user", reader.id), false, false, true)})
	readWrite := create("read-write", []map[string]any{ownerAll, grant(subject("user", reader.id), true, true, false)})
	ownerOnly := create("owner-only", []map[string]any{ownerAll})
	readAdmin := create("read-admin", []map[string]any{grant(subject("user", reader.id), true, false, true)})

	var readerGrantOnReadOnly model.ID
	for _, g := range readOnly.Grants {
		if g.Subject.Ref == reader.id.String() {
			readerGrantOnReadOnly = g.ID
		}
	}
	if readerGrantOnReadOnly.IsZero() {
		t.Fatalf("reader grant absent from creation result: %+v", readOnly.Grants)
	}

	t.Run("visibility is a current local read grant only", func(t *testing.T) {
		before := communicationHTTPTestEffects(t, eng, tenant)
		got := page(reader.token, wsQuery+"&limit=50")
		want := map[string]sessions.ChannelCatalogAccess{
			readOnly.Channel.ID.String():  {Read: true},
			readWrite.Channel.ID.String(): {Read: true, Write: true},
			readAdmin.Channel.ID.String(): {Read: true, Admin: true},
		}
		if len(got.Items) != len(want) || got.HasMore || got.Continuation != "" {
			t.Fatalf("reader catalog = %+v, want %d visible", got, len(want))
		}
		for index, item := range got.Items {
			access, visible := want[item.ID.String()]
			if !visible || item.MyAccess != access {
				t.Fatalf("reader item %s (%s) access = %+v, visible=%t want %+v", item.ID, item.Slug, item.MyAccess, visible, access)
			}
			if index > 0 && got.Items[index-1].ID.String() >= item.ID.String() {
				t.Fatalf("catalog not ordered by Channel ID")
			}
			if item.WorkspaceID != workspace || item.TenantID != tenant || item.State != sessions.ChannelActive {
				t.Fatalf("catalog item scope/state = %+v", item)
			}
		}
		ownerPage := page(owner.token, wsQuery+"&limit=50")
		ownerWant := map[string]bool{
			readOnly.Channel.ID.String(): true, writeOnly.Channel.ID.String(): true,
			adminOnly.Channel.ID.String(): true, readWrite.Channel.ID.String(): true,
			ownerOnly.Channel.ID.String(): true,
		}
		if len(ownerPage.Items) != len(ownerWant) {
			t.Fatalf("owner catalog = %v, want the five Channels the owner holds read on (not read-admin, where the owner has no grant)", ids(ownerPage))
		}
		for _, item := range ownerPage.Items {
			if !ownerWant[item.ID.String()] || item.MyAccess != (sessions.ChannelCatalogAccess{Read: true, Write: true, Admin: true}) {
				t.Fatalf("owner item = %+v", item)
			}
		}
		assertCommunicationHTTPTestNoEffects(t, eng, tenant, before, "reader and owner catalogs")
	})

	t.Run("pagination and continuation disclosure", func(t *testing.T) {
		first := page(reader.token, wsQuery+"&limit=1")
		if len(first.Items) != 1 || first.Items[0].ID != readOnly.Channel.ID || !first.HasMore || first.Continuation == "" {
			t.Fatalf("page one = %+v", first)
		}
		parts := strings.Split(first.Continuation, ".")
		if len(parts) != 4 || parts[0] != "c3n1" {
			t.Fatalf("continuation shape = %q", first.Continuation)
		}
		raw, err := base64.RawURLEncoding.DecodeString(parts[2])
		if err != nil {
			t.Fatalf("decode continuation claims without the key: %v", err)
		}
		var claims map[string]any
		if err := json.Unmarshal(raw, &claims); err != nil {
			t.Fatalf("continuation claims: %v", err)
		}
		if claims["lc"] != readOnly.Channel.ID.String() || claims["rr"] != reader.id.String() || claims["rk"] != "user" ||
			claims["ws"] != workspace.String() || claims["ten"] != tenant.String() {
			t.Fatalf("continuation claims = %v", claims)
		}
		for _, hidden := range []string{writeOnly.Channel.ID.String(), adminOnly.Channel.ID.String(), readWrite.Channel.ID.String(),
			ownerOnly.Channel.ID.String(), readAdmin.Channel.ID.String(), readerGrantOnReadOnly.String()} {
			if bytes.Contains(raw, []byte(hidden)) || strings.Contains(first.Continuation, hidden) {
				t.Fatalf("continuation discloses %s", hidden)
			}
		}
		second := page(reader.token, wsQuery+"&limit=1&continuation="+first.Continuation)
		if len(second.Items) != 1 || second.Items[0].ID != readWrite.Channel.ID || !second.HasMore || second.Continuation == "" {
			t.Fatalf("page two = %+v", second)
		}
		third := page(reader.token, wsQuery+"&limit=1&continuation="+second.Continuation)
		if len(third.Items) != 1 || third.Items[0].ID != readAdmin.Channel.ID || third.HasMore || third.Continuation != "" {
			t.Fatalf("page three = %+v", third)
		}
		if response := catalog(owner.token, wsQuery+"&limit=1&continuation="+first.Continuation); response.status != http.StatusBadRequest {
			t.Fatalf("another reader's continuation = %d: %s", response.status, response.raw)
		}
		tampered := first.Continuation[:len(first.Continuation)-4] + "AAAA"
		if response := catalog(reader.token, wsQuery+"&limit=1&continuation="+tampered); response.status != http.StatusBadRequest {
			t.Fatalf("tampered continuation = %d: %s", response.status, response.raw)
		}
	})

	t.Run("revocation and expiry between requests", func(t *testing.T) {
		current := communicationHTTPTestRequest(t, eng, http.MethodGet,
			"/v1/m/sessions/channels/"+readOnly.Channel.ID.String(), owner.token, tenant, nil, nil)
		if current.status != http.StatusOK {
			t.Fatalf("read Channel = %d: %s", current.status, current.raw)
		}
		channel := communicationHTTPTestDecode[sessions.Channel](t, current)
		revoked := communicationHTTPTestRequest(t, eng, http.MethodPost,
			"/v1/m/sessions/channels/"+readOnly.Channel.ID.String()+"/grants/"+readerGrantOnReadOnly.String()+"/revoke",
			owner.token, tenant, nil, map[string]string{"If-Match": fmt.Sprintf(`"v%d"`, channel.Version)})
		if revoked.status != http.StatusOK {
			t.Fatalf("revoke reader grant = %d: %s", revoked.status, revoked.raw)
		}
		got := page(reader.token, wsQuery+"&limit=50")
		if len(got.Items) != 2 || got.Items[0].ID != readWrite.Channel.ID || got.Items[1].ID != readAdmin.Channel.ID {
			t.Fatalf("catalog after revocation = %v", ids(got))
		}
		// An expiring grant: visible now, hidden once the engine clock passes it.
		expiring := create("expiring", []map[string]any{ownerAll})
		expiresAt := time.Now().UTC().Add(3 * time.Second)
		granted := communicationHTTPTestRequest(t, eng, http.MethodPost,
			"/v1/m/sessions/channels/"+expiring.Channel.ID.String()+"/grants", owner.token, tenant,
			map[string]any{"subject": subject("user", reader.id), "can_read": true, "can_write": false, "can_admin": false,
				"expires_at": expiresAt.Format(time.RFC3339Nano)},
			map[string]string{"If-Match": expiring.ETag})
		if granted.status != http.StatusOK {
			t.Fatalf("grant expiring read = %d: %s", granted.status, granted.raw)
		}
		got = page(reader.token, wsQuery+"&limit=50")
		if len(got.Items) != 3 || got.Items[2].ID != expiring.Channel.ID || got.Items[2].MyAccess != (sessions.ChannelCatalogAccess{Read: true}) {
			t.Fatalf("catalog with expiring grant = %v", ids(got))
		}
		time.Sleep(time.Until(expiresAt) + 1500*time.Millisecond)
		got = page(reader.token, wsQuery+"&limit=50")
		if len(got.Items) != 2 {
			t.Fatalf("catalog after expiry = %v, want the expired grant hidden", ids(got))
		}
	})

	t.Run("exchanged agent reader", func(t *testing.T) {
		sponsorRef := "human:k3-catalog-owner:" + owner.id.String()
		scimUpdate := communicationHTTPTestRequest(t, eng, http.MethodPut,
			"/v1/scim/v2/Users/"+owner.id.String(), owner.token, tenant,
			map[string]any{
				"schemas":  []string{"urn:ietf:params:scim:schemas:core:2.0:User"},
				"userName": "owner@k3-catalog.test", "displayName": "K3 catalog owner",
				"externalId": sponsorRef, "active": true,
			}, map[string]string{"Content-Type": "application/scim+json"})
		if scimUpdate.status != http.StatusOK {
			t.Fatalf("set agent sponsor external identity = %d: %s", scimUpdate.status, scimUpdate.raw)
		}
		if err := eng.store.Mutate(context.Background(), tenant, func(sc store.Scope) error {
			_, err := sc.Identities().Create(context.Background(), model.Identity{
				Name: "K3 catalog owner", Kind: "user", ExternalID: sponsorRef, Provider: "test-roster",
				Metadata: map[string]any{"principal_type": "human"},
			})
			return err
		}); err != nil {
			t.Fatalf("materialize sponsor roster identity: %v", err)
		}
		agentRef := "agent:k3-catalog-agent"
		registered := communicationHTTPTestRequest(t, eng, http.MethodPost, "/v1/m/governance/agents", owner.token, tenant,
			map[string]any{"identity_ref": agentRef, "source": "test-roster", "sponsor_ref": sponsorRef, "criticality": "medium"}, nil)
		if registered.status != http.StatusCreated {
			t.Fatalf("register agent lifecycle = %d: %s", registered.status, registered.raw)
		}
		createdAgent := communicationHTTPTestRequest(t, eng, http.MethodPost, "/v1/agents", owner.token, tenant,
			map[string]any{"name": "K3 catalog agent", "kind": "api", "external_id": agentRef, "status": "active", "workspace_id": workspace.String()}, nil)
		if createdAgent.status != http.StatusCreated {
			t.Fatalf("create core Agent = %d: %s", createdAgent.status, createdAgent.raw)
		}
		agentID := communicationHTTPTestDecode[struct {
			ID model.ID `json:"id"`
		}](t, createdAgent).ID
		bound := communicationHTTPTestRequest(t, eng, http.MethodPost, "/v1/m/governance/agents/"+agentID.String()+"/identity",
			owner.token, tenant, map[string]any{"identity_ref": agentRef}, nil)
		if bound.status != http.StatusOK {
			t.Fatalf("bind agent identity = %d: %s", bound.status, bound.raw)
		}
		identityID := communicationHTTPTestDecode[struct {
			IdentityID model.ID `json:"identity_id"`
		}](t, bound).IdentityID
		agentToken := exchangeCommunicationHTTPTestAgentToken(t, eng, owner.token, agentRef)

		empty := page(agentToken, wsQuery+"&limit=50")
		if len(empty.Items) != 0 {
			t.Fatalf("agent without grants sees %v", ids(empty))
		}
		agentChannel := create("agent-read", []map[string]any{ownerAll, grant(subject("agent", identityID), true, true, false)})
		got := page(agentToken, wsQuery+"&limit=50")
		if len(got.Items) != 1 || got.Items[0].ID != agentChannel.Channel.ID || got.Items[0].MyAccess != (sessions.ChannelCatalogAccess{Read: true, Write: true}) {
			t.Fatalf("agent catalog = %+v", got)
		}
		// The agent's own identity, not the sponsoring user, is the reader: the
		// sponsor does not inherit the agent's grant and the token names the
		// canonical identity UUID, never the external login identifier.
		ownerPage := page(owner.token, wsQuery+"&limit=50")
		for _, item := range ownerPage.Items {
			if item.ID == agentChannel.Channel.ID && item.MyAccess != (sessions.ChannelCatalogAccess{Read: true, Write: true, Admin: true}) {
				t.Fatalf("owner access on agent Channel = %+v", item.MyAccess)
			}
		}
		readerPage := page(reader.token, wsQuery+"&limit=50")
		for _, item := range readerPage.Items {
			if item.ID == agentChannel.Channel.ID {
				t.Fatalf("reader inherited the agent grant: %+v", item)
			}
		}
		first := page(agentToken, wsQuery+"&limit=1")
		if first.HasMore {
			t.Fatalf("agent page one has an unexpected lookahead: %+v", first)
		}
		agentGranted := create("agent-two", []map[string]any{ownerAll, grant(subject("agent", identityID), true, false, false)})
		first = page(agentToken, wsQuery+"&limit=1")
		if !first.HasMore || first.Continuation == "" {
			t.Fatalf("agent page one lacks lookahead: %+v", first)
		}
		raw, _ := base64.RawURLEncoding.DecodeString(strings.Split(first.Continuation, ".")[2])
		if bytes.Contains(raw, []byte(agentRef)) || !bytes.Contains(raw, []byte(identityID.String())) {
			t.Fatalf("agent continuation must bind the canonical identity, never the external id: %s", raw)
		}
		second := page(agentToken, wsQuery+"&limit=1&continuation="+first.Continuation)
		if len(second.Items) != 1 || second.Items[0].ID != agentGranted.Channel.ID || second.HasMore {
			t.Fatalf("agent page two = %+v", second)
		}
	})

	t.Run("unknown current closure is unavailable without effects", func(t *testing.T) {
		if eng.communicationComposition == nil || eng.communicationComposition.closure == nil {
			t.Fatal("production communication composition is unavailable")
		}
		before := communicationHTTPTestEffects(t, eng, tenant)
		realClosure := sessions.ChannelGrantSubjectClosureResolver(eng.communicationComposition.closure)
		eng.sessionsMod.UseCommunicationChannelGrantSubjectClosureResolver(
			communicationHTTPUnknownGrantClosureResolver{ChannelGrantSubjectClosureResolver: realClosure},
		)
		unknown := catalog(reader.token, wsQuery+"&limit=5")
		eng.sessionsMod.UseCommunicationChannelGrantSubjectClosureResolver(realClosure)
		if unknown.status != http.StatusServiceUnavailable || !strings.Contains(string(unknown.raw), "evidence_unavailable") ||
			strings.Contains(string(unknown.raw), readWrite.Channel.ID.String()) {
			t.Fatalf("UNKNOWN closure catalog = %d: %s", unknown.status, unknown.raw)
		}
		assertCommunicationHTTPTestNoEffects(t, eng, tenant, before, "UNKNOWN closure catalog")
	})

}

// channelCatalogLightCensus counts the K3 effect rows a catalog read must not
// touch. The shared census caps each kind at 1000 rows, which the sparse estate
// deliberately exceeds for Channels and grants, so this one counts the mutation
// carriers (messages, deliveries, acks, commands, outbox, cursors, audits) and
// hashes nothing.
type channelCatalogLightCensusCounts struct {
	messages, deliveries, acks, commands, outbox, cursors, successAudits int
}

func channelCatalogLightCensus(t *testing.T, eng *engine, tenant model.TenantID) channelCatalogLightCensusCounts {
	t.Helper()
	var out channelCatalogLightCensusCounts
	if err := eng.store.View(context.Background(), tenant, func(sc store.Scope) error {
		for _, counter := range []struct {
			kind model.Kind
			set  *int
		}{
			{kind: "sessions.message", set: &out.messages},
			{kind: "sessions.message_delivery", set: &out.deliveries},
			{kind: "sessions.message_ack", set: &out.acks},
			{kind: "sessions.communication_command", set: &out.commands},
			{kind: "sessions.work_outbox", set: &out.outbox},
			{kind: "sessions.inbox_cursor", set: &out.cursors},
		} {
			repo, err := sc.Ext(counter.kind)
			if err != nil {
				return err
			}
			rows, page, err := repo.List(context.Background(), model.Query{Limit: 1000})
			if err != nil {
				return err
			}
			if page.HasMore {
				return fmt.Errorf("%s light census exceeded 1000 rows", counter.kind)
			}
			*counter.set = len(rows)
		}
		return sc.Audit().Walk(context.Background(), 1, func(event model.AuditEvent) error {
			if strings.HasPrefix(event.Action, "sessions.communication.") {
				out.successAudits++
			}
			return nil
		})
	}); err != nil {
		t.Fatalf("light census: %v", err)
	}
	return out
}

// TestCommunicationChannelCatalogSparseEstateHTTP proves discovery progresses
// across thousands of unrelated hidden Channels on the product router: full
// ordered pages, a terminal has_more=false, no hidden identifier in any
// continuation, and bounded latency that does not grow with the hidden estate.
func TestCommunicationChannelCatalogSparseEstateHTTP(t *testing.T) {
	estate := bootChannelCatalogHTTPEstate(t)
	eng, tenant, owner, reader, workspace := estate.eng, estate.tenant, estate.owner, estate.reader, estate.workspace
	page := func(token string, query string) sessions.ChannelCatalogPage { return estate.page(t, token, query) }
	ids := channelCatalogIDs
	create := func(slug string, grants []map[string]any) sessions.ChannelMutationResult {
		return estate.create(t, slug, grants)
	}
	subject := channelCatalogSubject
	grant := channelCatalogGrant
	ownerAll := grant(subject("user", owner.id), true, true, true)
	wsQuery := "workspace_id=" + workspace.String()
	// Hidden filler is a fixture: active Channels with active read grants for
	// an unrelated user, written through the typed store API in one
	// transaction each, exactly the rows a grant-first catalog never reads.
	// The visible Channels are created through the product route.
	seedHidden := func(prefix string, n int) []model.ID {
		t.Helper()
		strangerSubject := model.NewID().String()
		seeded := make([]model.ID, 0, n)
		if err := eng.store.Mutate(context.Background(), tenant, func(sc store.Scope) error {
			channels, err := sc.Ext("sessions.channel")
			if err != nil {
				return err
			}
			grants, err := sc.Ext("sessions.channel_grant")
			if err != nil {
				return err
			}
			for index := 0; index < n; index++ {
				channelID := model.NewID()
				if _, err := channels.CreateWithID(context.Background(), channelID, model.Record{
					"workspace_id": workspace.String(), "slug": fmt.Sprintf("%s-%05d", prefix, index),
					"name": "Hidden " + prefix, "kind": "coordination", "state": "active",
					"sensitivity": "internal", "content_protection": "application_sealed",
					"protection_generation": int64(1), "default_ack_policy": "none",
					"default_ack_timeout_ms": int64(0), "default_wake": "none", "max_fanout": int64(1),
					"max_automation_depth": int64(4), "acl_revision": int64(1), "route_revision": int64(1),
					"subscription_revision": int64(1),
				}); err != nil {
					return err
				}
				if _, err := grants.CreateWithID(context.Background(), model.NewID(), model.Record{
					"workspace_id": workspace.String(), "channel_id": channelID.String(),
					"subject_kind": "user", "subject_ref": strangerSubject, "generation": int64(1),
					"can_read": true, "can_write": false, "can_admin": false, "state": "active",
					"granted_by_kind": "user", "granted_by_ref": owner.id.String(),
				}); err != nil {
					return err
				}
				seeded = append(seeded, channelID)
			}
			return nil
		}); err != nil {
			t.Fatalf("seed %d hidden Channels: %v", n, err)
		}
		return seeded
	}
	hiddenBefore := seedHidden("hidden-before", 4200)
	visibleA := create("sparse-visible-a", []map[string]any{ownerAll, grant(subject("user", reader.id), true, false, false)})
	hiddenBetween := seedHidden("hidden-between", 120)
	visibleB := create("sparse-visible-b", []map[string]any{ownerAll, grant(subject("user", reader.id), true, true, false)})
	hiddenAfter := seedHidden("hidden-after", 60)
	if err := eng.sessionsMod.ReconcileCommunicationGuards(context.Background(), tenant, sessions.CommunicationGuardReconcileStaged); err != nil {
		t.Fatalf("reconcile guards after seeding: %v", err)
	}
	before := channelCatalogLightCensus(t, eng, tenant)
	started := time.Now()
	got := page(reader.token, wsQuery+"&limit=200")
	elapsed := time.Since(started)
	wantTail := []string{visibleA.Channel.ID.String(), visibleB.Channel.ID.String()}
	gotIDs := ids(got)
	if len(gotIDs) < 2 || gotIDs[len(gotIDs)-2] != wantTail[0] || gotIDs[len(gotIDs)-1] != wantTail[1] || got.HasMore {
		t.Fatalf("sparse catalog = %v (has_more=%t), want the two visible Channels last", gotIDs, got.HasMore)
	}
	t.Logf("sparse estate: %d hidden Channels seeded; full page of %d visible in %s", len(hiddenBefore)+len(hiddenBetween)+len(hiddenAfter), len(gotIDs), elapsed)
	// Page by one across the 4200-row hidden run, then the 120-row run.
	var anchor string
	var walked []string
	for pages := 0; pages < 50; pages++ {
		query := wsQuery + "&limit=1"
		if anchor != "" {
			query += "&continuation=" + anchor
		}
		current := page(reader.token, query)
		if len(current.Items) != 1 {
			t.Fatalf("walk page %d = %+v", pages, current)
		}
		walked = append(walked, current.Items[0].ID.String())
		if current.Continuation != "" {
			raw, _ := base64.RawURLEncoding.DecodeString(strings.Split(current.Continuation, ".")[2])
			for _, hidden := range append(append(hiddenBefore, hiddenBetween...), hiddenAfter...) {
				if bytes.Contains(raw, []byte(hidden.String())) {
					t.Fatalf("continuation discloses hidden Channel %s", hidden)
				}
			}
		}
		if !current.HasMore {
			break
		}
		anchor = current.Continuation
	}
	if len(walked) != len(gotIDs) || walked[len(walked)-1] != visibleB.Channel.ID.String() {
		t.Fatalf("walked %v, want the same %d visible Channels as the full page", walked, len(gotIDs))
	}
	if elapsed > 5*time.Second {
		t.Fatalf("sparse catalog took %s: work scaled with hidden Channels", elapsed)
	}
	if after := channelCatalogLightCensus(t, eng, tenant); after != before {
		t.Fatalf("sparse catalog reads changed communication effects: before=%+v after=%+v", before, after)
	}
}
