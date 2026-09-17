// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
)

// incomingHandoffHTTPEstate is the fresh, explicitly activated estate the
// personal handoff reads are exercised on: two tenants, an owner, the recipient
// user, a non-recipient member, a stranger in the other tenant, a governed agent
// recipient, an offering session and a recipient session, one sealed Channel and
// grants for every subject that is meant to reach it.
type incomingHandoffHTTPEstate struct {
	eng           *engine
	backing       communicationHTTPTestStore
	tenant        model.TenantID
	otherTenant   model.TenantID
	workspace     model.ID
	otherTenantWS model.ID
	channelID     model.ID
	owner         communicationHTTPTestUser
	recipient     communicationHTTPTestUser
	bystander     communicationHTTPTestUser
	stranger      communicationHTTPTestUser
	agent         communicationHTTPTestAgent
	targetSession communicationHTTPTestSession
}

// incomingHandoffHTTPOffer is what the SENDER got back, plus the session that
// made the offer. A run binds one work item at a time, so every offer is made by
// its own session; that is also what lets the sender-side isolation checks name
// the exact credential that holds the receipt.
type incomingHandoffHTTPOffer struct {
	sessions.HandoffOfferResult
	source communicationHTTPTestSession
}

func bootIncomingHandoffHTTPEstate(
	t *testing.T,
	backing communicationHTTPTestStore,
) incomingHandoffHTTPEstate {
	t.Helper()
	eng := bootActivatedCommunicationHTTPTestEngine(t, backing)
	setupToken, _, err := eng.setupTok.Ensure()
	if err != nil {
		t.Fatalf("setup token: %v", err)
	}
	setup := communicationHTTPTestRequest(t, eng, http.MethodPost, "/v1/setup", "", "",
		map[string]any{
			"token": setupToken, "email": "admin@k3-handoff-read.test",
			"password": "k3-handoff-read-admin-password",
		}, nil)
	if setup.status != http.StatusCreated {
		t.Fatalf("setup = %d: %s", setup.status, setup.raw)
	}
	login := communicationHTTPTestRequest(t, eng, http.MethodPost, "/v1/auth/login", "", "",
		map[string]any{
			"email": "admin@k3-handoff-read.test", "password": "k3-handoff-read-admin-password",
		}, nil)
	if login.status != http.StatusOK {
		t.Fatalf("admin login = %d: %s", login.status, login.raw)
	}
	admin := communicationHTTPTestDecode[struct {
		Token string `json:"token"`
	}](t, login)
	createdOrg := communicationHTTPTestRequest(t, eng, http.MethodPost, "/v1/system/orgs",
		admin.Token, "", map[string]any{"name": "K3 handoff read", "slug": "k3-handoff-read"}, nil)
	if createdOrg.status != http.StatusCreated {
		t.Fatalf("create tenant = %d: %s", createdOrg.status, createdOrg.raw)
	}
	org := communicationHTTPTestDecode[struct {
		TenantID model.TenantID `json:"tenant_id"`
	}](t, createdOrg)
	createdOther := communicationHTTPTestRequest(t, eng, http.MethodPost, "/v1/system/orgs",
		admin.Token, "", map[string]any{"name": "K3 handoff other", "slug": "k3-handoff-other"}, nil)
	if createdOther.status != http.StatusCreated {
		t.Fatalf("create other tenant = %d: %s", createdOther.status, createdOther.raw)
	}
	other := communicationHTTPTestDecode[struct {
		TenantID model.TenantID `json:"tenant_id"`
	}](t, createdOther)

	owner := createCommunicationHTTPTestUser(
		t, eng, admin.Token, org.TenantID, "owner@k3-handoff-read.test", auth.RoleOwner)
	recipient := createCommunicationHTTPTestUser(
		t, eng, admin.Token, org.TenantID, "recipient@k3-handoff-read.test", auth.RoleEditor)
	bystander := createCommunicationHTTPTestUser(
		t, eng, admin.Token, org.TenantID, "bystander@k3-handoff-read.test", auth.RoleEditor)
	stranger := createCommunicationHTTPTestUser(
		t, eng, admin.Token, other.TenantID, "stranger@k3-handoff-read.test", auth.RoleOwner)

	if err := eng.Close(); err != nil {
		t.Fatalf("close bootstrapped estate: %v", err)
	}
	eng = bootCommunicationHTTPTestEngine(t, backing)
	t.Cleanup(func() { _ = eng.Close() })
	if readiness, err := eng.sessionsMod.EvaluateCommunicationReadiness(
		context.Background(),
	); err != nil || !readiness.Effective {
		t.Fatalf("communication readiness after bootstrap restart = %+v, err %v", readiness, err)
	}
	owner = loginCommunicationHTTPTestUser(t, eng, owner.id, "owner@k3-handoff-read.test")
	recipient = loginCommunicationHTTPTestUser(t, eng, recipient.id, "recipient@k3-handoff-read.test")
	bystander = loginCommunicationHTTPTestUser(t, eng, bystander.id, "bystander@k3-handoff-read.test")
	stranger = loginCommunicationHTTPTestUser(t, eng, stranger.id, "stranger@k3-handoff-read.test")
	stepUpCommunicationHTTPTestUser(t, eng, owner.token)
	stepUpCommunicationHTTPTestUser(t, eng, stranger.token)

	var workspace, otherWorkspace model.ID
	if err := eng.store.View(context.Background(), org.TenantID, func(sc store.Scope) error {
		value, err := sc.DefaultWorkspace(context.Background())
		workspace = value.ID
		return err
	}); err != nil {
		t.Fatalf("read handoff-read workspace: %v", err)
	}
	if err := eng.store.View(context.Background(), other.TenantID, func(sc store.Scope) error {
		value, err := sc.DefaultWorkspace(context.Background())
		otherWorkspace = value.ID
		return err
	}); err != nil {
		t.Fatalf("read other-tenant workspace: %v", err)
	}

	estate := incomingHandoffHTTPEstate{
		eng: eng, backing: backing, tenant: org.TenantID, otherTenant: other.TenantID,
		workspace: workspace, otherTenantWS: otherWorkspace,
		owner: owner, recipient: recipient, bystander: bystander, stranger: stranger,
		targetSession: createCommunicationHTTPTestSession(
			t, eng, org.TenantID, workspace, "handoff-target"),
	}
	estate.agent = createIncomingHandoffHTTPAgent(t, estate)
	estate.channelID = createIncomingHandoffHTTPChannel(t, &estate)
	return estate
}

func createIncomingHandoffHTTPAgent(
	t *testing.T,
	estate incomingHandoffHTTPEstate,
) communicationHTTPTestAgent {
	t.Helper()
	eng := estate.eng
	// The delegation gate requires the exchanging subject to BE the agent's
	// registered sponsor, so the owner's own external identity is set through the
	// authenticated SCIM route before the roster row names it.
	sponsorRef := "human:k3-handoff-read-owner:" + estate.owner.id.String()
	scimUpdate := communicationHTTPTestRequest(t, eng, http.MethodPut,
		"/v1/scim/v2/Users/"+estate.owner.id.String(), estate.owner.token, estate.tenant,
		map[string]any{
			"schemas":  []string{"urn:ietf:params:scim:schemas:core:2.0:User"},
			"userName": "owner@k3-handoff-read.test", "displayName": "K3 handoff read owner",
			"externalId": sponsorRef, "active": true,
		}, map[string]string{"Content-Type": "application/scim+json"})
	if scimUpdate.status != http.StatusOK {
		t.Fatalf("set agent sponsor external identity = %d: %s", scimUpdate.status, scimUpdate.raw)
	}
	if err := eng.store.Mutate(
		context.Background(), estate.tenant, func(sc store.Scope) error {
			_, err := sc.Identities().Create(context.Background(), model.Identity{
				Name: "K3 handoff read owner", Kind: "user", ExternalID: sponsorRef,
				Provider: "test-roster", Metadata: map[string]any{"principal_type": "human"},
			})
			return err
		},
	); err != nil {
		t.Fatalf("materialize sponsor roster identity: %v", err)
	}
	agentRef := "agent:k3-handoff-read"
	registered := communicationHTTPTestRequest(t, eng, http.MethodPost,
		"/v1/m/governance/agents", estate.owner.token, estate.tenant, map[string]any{
			"identity_ref": agentRef, "source": "test-roster",
			"sponsor_ref": sponsorRef, "criticality": "medium",
		}, nil)
	if registered.status != http.StatusCreated {
		t.Fatalf("register agent lifecycle = %d: %s", registered.status, registered.raw)
	}
	created := communicationHTTPTestRequest(t, eng, http.MethodPost, "/v1/agents",
		estate.owner.token, estate.tenant, map[string]any{
			"name": "K3 handoff read agent", "kind": "api", "external_id": agentRef,
			"status": "active", "workspace_id": estate.workspace,
		}, nil)
	if created.status != http.StatusCreated {
		t.Fatalf("create core Agent = %d: %s", created.status, created.raw)
	}
	createdAgent := communicationHTTPTestDecode[struct {
		ID model.ID `json:"id"`
	}](t, created)
	bound := communicationHTTPTestRequest(t, eng, http.MethodPost,
		"/v1/m/governance/agents/"+createdAgent.ID.String()+"/identity",
		estate.owner.token, estate.tenant, map[string]any{"identity_ref": agentRef}, nil)
	if bound.status != http.StatusOK {
		t.Fatalf("bind agent identity = %d: %s", bound.status, bound.raw)
	}
	boundBody := communicationHTTPTestDecode[struct {
		IdentityID model.ID `json:"identity_id"`
	}](t, bound)
	if boundBody.IdentityID.IsZero() {
		t.Fatal("bound agent identity is empty")
	}
	return communicationHTTPTestAgent{
		identityID: boundBody.IdentityID, externalID: agentRef,
		token: exchangeCommunicationHTTPTestAgentToken(t, eng, estate.owner.token, agentRef),
	}
}

func createIncomingHandoffHTTPChannel(t *testing.T, estate *incomingHandoffHTTPEstate) model.ID {
	t.Helper()
	eng := estate.eng
	created := communicationHTTPTestRequest(t, eng, http.MethodPost, "/v1/m/sessions/channels",
		estate.owner.token, estate.tenant, map[string]any{
			"workspace_id": estate.workspace.String(), "slug": "k3-handoff-read",
			"name": "K3 handoff read", "content_protection": "application_sealed",
			"default_ack_policy": "each_required", "default_ack_timeout_ms": 600_000,
			"max_fanout": 1,
			"initial_grants": []map[string]any{{
				"subject":  map[string]any{"kind": "user", "ref": estate.owner.id},
				"can_read": true, "can_write": true, "can_admin": true,
			}},
		}, nil)
	if created.status != http.StatusCreated {
		t.Fatalf("create sealed Channel = %d: %s", created.status, created.raw)
	}
	channel := communicationHTTPTestDecode[sessions.ChannelMutationResult](t, created)
	etag := channel.ETag
	grant := func(subject map[string]any, label string) {
		t.Helper()
		response := communicationHTTPTestRequest(t, eng, http.MethodPost,
			"/v1/m/sessions/channels/"+channel.Channel.ID.String()+"/grants",
			estate.owner.token, estate.tenant,
			map[string]any{"subject": subject, "can_read": true, "can_write": true},
			map[string]string{"If-Match": etag})
		if response.status != http.StatusOK {
			t.Fatalf("grant Channel to %s = %d: %s", label, response.status, response.raw)
		}
		etag = communicationHTTPTestDecode[sessions.ChannelMutationResult](t, response).ETag
	}
	grant(map[string]any{"kind": "user", "ref": estate.recipient.id}, "recipient")
	grant(map[string]any{"kind": "user", "ref": estate.bystander.id}, "bystander")
	grant(map[string]any{"kind": "agent", "ref": estate.agent.identityID}, "agent")
	grant(map[string]any{"kind": "session", "ref": estate.targetSession.sid}, "target session")
	return channel.Channel.ID
}

// grantChannel adds one read/write grant against the Channel's CURRENT version,
// re-read every time so a caller never carries a stale ETag between offers.
func (e incomingHandoffHTTPEstate) grantChannel(
	t *testing.T,
	subject map[string]any,
	label string,
) {
	t.Helper()
	read := communicationHTTPTestRequest(t, e.eng, http.MethodGet,
		"/v1/m/sessions/channels/"+e.channelID.String(), e.owner.token, e.tenant, nil, nil)
	if read.status != http.StatusOK {
		t.Fatalf("read Channel before granting %s = %d: %s", label, read.status, read.raw)
	}
	current := communicationHTTPTestDecode[sessions.Channel](t, read)
	response := communicationHTTPTestRequest(t, e.eng, http.MethodPost,
		"/v1/m/sessions/channels/"+e.channelID.String()+"/grants", e.owner.token, e.tenant,
		map[string]any{"subject": subject, "can_read": true, "can_write": true},
		map[string]string{"If-Match": fmt.Sprintf(`"v%d"`, current.Version)})
	if response.status != http.StatusOK {
		t.Fatalf("grant Channel to %s = %d: %s", label, response.status, response.raw)
	}
}

// restart reopens the estate the way an operator restart does — close, boot the
// same store, re-verify readiness — and re-authenticates the two user credentials.
// The agent and communication-session credentials are DURABLE and are deliberately
// not reissued: reusing them across the restart is what proves a personal handoff
// read is state a recipient can come back to, not a cache the process held.
func (e incomingHandoffHTTPEstate) restart(t *testing.T) incomingHandoffHTTPEstate {
	t.Helper()
	if err := e.eng.Close(); err != nil {
		t.Fatalf("close estate for restart: %v", err)
	}
	eng := bootCommunicationHTTPTestEngine(t, e.backing)
	t.Cleanup(func() { _ = eng.Close() })
	readiness, err := eng.sessionsMod.EvaluateCommunicationReadiness(context.Background())
	if err != nil || !readiness.Effective {
		t.Fatalf("communication readiness after restart = %+v, err %v", readiness, err)
	}
	restarted := e
	restarted.eng = eng
	restarted.owner = loginCommunicationHTTPTestUser(t, eng, e.owner.id, "owner@k3-handoff-read.test")
	restarted.recipient = loginCommunicationHTTPTestUser(
		t, eng, e.recipient.id, "recipient@k3-handoff-read.test")
	restarted.bystander = loginCommunicationHTTPTestUser(
		t, eng, e.bystander.id, "bystander@k3-handoff-read.test")
	stepUpCommunicationHTTPTestUser(t, eng, restarted.owner.token)
	return restarted
}

func incomingHandoffListPath(workspace model.ID, state string, limit int, continuation string) string {
	values := url.Values{"workspace_id": {workspace.String()}}
	if state != "" {
		values.Set("state", state)
	}
	if limit > 0 {
		values.Set("limit", fmt.Sprintf("%d", limit))
	}
	if continuation != "" {
		values.Set("continuation", continuation)
	}
	return "/v1/m/sessions/inbox/handoffs?" + values.Encode()
}

func (e incomingHandoffHTTPEstate) list(
	t *testing.T,
	token string,
	state string,
	limit int,
	continuation string,
) communicationHTTPTestResponse {
	t.Helper()
	return communicationHTTPTestRequest(t, e.eng, http.MethodGet,
		incomingHandoffListPath(e.workspace, state, limit, continuation), token, e.tenant, nil, nil)
}

func (e incomingHandoffHTTPEstate) page(
	t *testing.T,
	token string,
	state string,
	limit int,
	continuation string,
) sessions.IncomingHandoffPage {
	t.Helper()
	response := e.list(t, token, state, limit, continuation)
	if response.status != http.StatusOK {
		t.Fatalf("listing (%s, limit %d) = %d: %s", state, limit, response.status, response.raw)
	}
	assertIncomingHandoffPrivateHeaders(t, response, "listing")
	return communicationHTTPTestDecode[sessions.IncomingHandoffPage](t, response)
}

func (e incomingHandoffHTTPEstate) detail(
	t *testing.T,
	token string,
	deliveryID model.ID,
) communicationHTTPTestResponse {
	t.Helper()
	return communicationHTTPTestRequest(t, e.eng, http.MethodGet,
		"/v1/m/sessions/deliveries/"+deliveryID.String()+"/handoff", token, e.tenant, nil, nil)
}

func assertIncomingHandoffPrivateHeaders(
	t *testing.T,
	response communicationHTTPTestResponse,
	label string,
) {
	t.Helper()
	if got := response.header.Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("%s Cache-Control = %q, want %q", label, got, "private, no-store")
	}
	// No response ETag: an If-None-Match round trip would let a 304 stand in for
	// an authorization this surface re-runs on every request.
	if got := response.header.Get("ETag"); got != "" {
		t.Fatalf("%s published a response ETag %q", label, got)
	}
}

// offerIncomingHandoffOverHTTP creates a session-owned WorkItem, leases it to the
// offering session and offers it to `recipient` through the existing atomic
// offer route. It returns only what the SENDER receives.
func (e incomingHandoffHTTPEstate) offer(
	t *testing.T,
	recipient map[string]any,
	summary string,
	deadline time.Duration,
) incomingHandoffHTTPOffer {
	t.Helper()
	source := createCommunicationHTTPTestSession(
		t, e.eng, e.tenant, e.workspace, "offer-"+model.NewID().String(),
	)
	e.grantChannel(t, map[string]any{"kind": "session", "ref": source.sid}, "offering session")
	work, leased := createCommunicationHTTPSessionOwnedWork(
		t, e.eng, e.owner, e.tenant, source, "Handoff read: "+summary,
	)
	response := communicationHTTPTestRequest(t, e.eng, http.MethodPost, "/v1/m/sessions/handoffs",
		source.communication.Token, e.tenant, map[string]any{
			"channel_id": e.channelID, "work_item_id": work.ResultID, "recipient": recipient,
			"handoff": map[string]any{
				"summary": summary, "next_action": "Continue " + summary,
				"risk": "None recorded for " + summary,
			},
			"ack_deadline": time.Now().UTC().Add(deadline), "expected_owner_epoch": 1,
		}, map[string]string{
			"If-Match":        fmt.Sprintf(`"v%d"`, leased.Version),
			"Idempotency-Key": model.NewID().String(),
		})
	if response.status != http.StatusCreated {
		t.Fatalf("offer %q = %d: %s", summary, response.status, response.raw)
	}
	return incomingHandoffHTTPOffer{
		HandoffOfferResult: communicationHTTPTestDecode[sessions.HandoffOfferResult](t, response),
		source:             source,
	}
}

func TestCommunicationIncomingHandoffFreshHTTP(t *testing.T) {
	exerciseCommunicationIncomingHandoffFreshHTTP(t, communicationHTTPTestSQLiteStore(t))
}

// TestCommunicationIncomingHandoffFreshHTTPPostgres is the same authenticated
// product journey on an owned IsolatedPostgresSplitOwner estate: the same boot,
// the same writer activation and the same application/owner/admin roles.
func TestCommunicationIncomingHandoffFreshHTTPPostgres(t *testing.T) {
	exerciseCommunicationIncomingHandoffFreshHTTP(t, communicationHTTPTestPostgresStore(t))
}

func exerciseCommunicationIncomingHandoffFreshHTTP(
	t *testing.T,
	backing communicationHTTPTestStore,
) {
	t.Helper()
	estate := bootIncomingHandoffHTTPEstate(t, backing)

	userOffer := estate.offer(t,
		map[string]any{"kind": "user", "ref": estate.recipient.id}, "user recipient", 30*time.Minute)
	agentOffer := estate.offer(t,
		map[string]any{"kind": "agent", "ref": estate.agent.identityID}, "agent recipient", 30*time.Minute)
	sessionOffer := estate.offer(t,
		map[string]any{"kind": "session", "ref": estate.targetSession.sid}, "session recipient", 30*time.Minute)

	exerciseIncomingHandoffRecipientJourney(t, estate, userOffer, agentOffer, sessionOffer)
	exerciseIncomingHandoffIsolation(t, estate, userOffer, agentOffer, sessionOffer)
	exerciseIncomingHandoffSelectorValidation(t, estate)
	exerciseIncomingHandoffPagination(t, estate)
	exerciseIncomingHandoffResponseRevalidation(t, estate)
	// Everything below runs on a RESTARTED engine with the same durable store.
	estate = estate.restart(t)
	exerciseIncomingHandoffRestartAndReplay(t, estate, userOffer, agentOffer, sessionOffer)
}

// exerciseIncomingHandoffRecipientJourney is the acceptance walk: each recipient
// kind discovers ITS OWN offer with its own credential, reads the id, the version
// and the content, and never receives the sender's receipt.
func exerciseIncomingHandoffRecipientJourney(
	t *testing.T,
	estate incomingHandoffHTTPEstate,
	userOffer, agentOffer, sessionOffer incomingHandoffHTTPOffer,
) {
	t.Helper()
	before := communicationHTTPTestEffects(t, estate.eng, estate.tenant)
	readers := []struct {
		label   string
		token   string
		offer   incomingHandoffHTTPOffer
		summary string
	}{
		{"user", estate.recipient.token, userOffer, "user recipient"},
		{"agent", estate.agent.token, agentOffer, "agent recipient"},
		{"session", estate.targetSession.communication.Token, sessionOffer, "session recipient"},
	}
	for _, reader := range readers {
		page := estate.page(t, reader.token, "", 0, "")
		if len(page.Items) != 1 || page.HasMore || page.Continuation != "" {
			t.Fatalf("%s recipient page = %+v", reader.label, page)
		}
		card := page.Items[0]
		if card.Handoff.ID != reader.offer.HandoffID || card.Handoff.ETag != reader.offer.ETag ||
			card.Handoff.Version != reader.offer.Version ||
			card.Handoff.State != sessions.HandoffOffered ||
			card.Carrier.DeliveryID != reader.offer.DeliveryID ||
			card.Carrier.MessageID != reader.offer.MessageID ||
			card.Carrier.ChannelID != estate.channelID ||
			card.WorkItem.ID != reader.offer.WorkItemID ||
			card.WorkItem.Presentation != "handoff_context" ||
			card.ObservedAt.IsZero() || card.DeadlineElapsed {
			t.Fatalf("%s recipient card = %+v", reader.label, card)
		}
		listing := estate.list(t, reader.token, "", 0, "")
		if strings.Contains(string(listing.raw), reader.summary) {
			t.Fatalf("%s listing opened protected content: %s", reader.label, listing.raw)
		}
		// The listing carries no sender receipt: no command id, no audit sequence,
		// no plan hash and no event id.
		for _, forbidden := range []string{
			"command_id", "audit_seq", "plan_hash", "event_id", "replayed",
		} {
			if strings.Contains(string(listing.raw), forbidden) {
				t.Fatalf("%s listing leaked the sender receipt field %q: %s",
					reader.label, forbidden, listing.raw)
			}
		}

		response := estate.detail(t, reader.token, reader.offer.DeliveryID)
		if response.status != http.StatusOK {
			t.Fatalf("%s recipient detail = %d: %s", reader.label, response.status, response.raw)
		}
		assertIncomingHandoffPrivateHeaders(t, response, reader.label+" detail")
		detail := communicationHTTPTestDecode[sessions.IncomingHandoffReadResult](t, response)
		if detail.Content.Summary != reader.summary ||
			detail.Content.NextAction != "Continue "+reader.summary ||
			detail.OfferContext != sessions.IncomingHandoffContextCurrent ||
			detail.TerminalReason != nil || detail.Handoff.ETag != reader.offer.ETag {
			t.Fatalf("%s recipient detail = %+v", reader.label, detail)
		}
		for _, forbidden := range []string{
			"command_id", "audit_seq", "plan_hash", "brief_md", "acceptance",
			"Handoff read: " + reader.summary,
		} {
			if strings.Contains(string(response.raw), forbidden) {
				t.Fatalf("%s detail leaked %q: %s", reader.label, forbidden, response.raw)
			}
		}
		// Being the recipient of an offer is not authority over the work it names.
		// The communication-session credential is the exact control: its immutable
		// four-permission ceiling holds delivery:read but not sessions:work:read,
		// so the personal read above succeeded and the work record stays closed.
		// A user or agent that ALREADY holds sessions:work:read through its own
		// role keeps it; this surface neither granted nor removed that.
		work := communicationHTTPTestRequest(t, estate.eng, http.MethodGet,
			"/v1/m/sessions/work-items/"+reader.offer.WorkItemID.String(),
			reader.token, estate.tenant, nil, nil)
		if reader.label == "session" && work.status != http.StatusForbidden {
			t.Fatalf("communication-session credential reached the WorkItem = %d: %s",
				work.status, work.raw)
		}
	}
	assertCommunicationHTTPTestNoEffects(t, estate.eng, estate.tenant, before,
		"personal handoff discovery and reading")
}

// exerciseIncomingHandoffIsolation proves the recipient graph is the only door:
// the sender, a granted non-recipient, a foreign tenant and a crossed carrier all
// see nothing, and none of them can substitute for the recipient.
func exerciseIncomingHandoffIsolation(
	t *testing.T,
	estate incomingHandoffHTTPEstate,
	userOffer, agentOffer, sessionOffer incomingHandoffHTTPOffer,
) {
	t.Helper()
	before := communicationHTTPTestEffects(t, estate.eng, estate.tenant)
	// The offering session holds the receipt; it is not a recipient.
	senderPage := estate.page(t, userOffer.source.communication.Token, "", 0, "")
	if len(senderPage.Items) != 0 || senderPage.HasMore {
		t.Fatalf("offering session discovered its own offers: %+v", senderPage)
	}
	// The channel owner and admin is not a recipient either.
	ownerPage := estate.page(t, estate.owner.token, "", 0, "")
	if len(ownerPage.Items) != 0 {
		t.Fatalf("channel owner discovered someone else's offers: %+v", ownerPage)
	}
	bystanderPage := estate.page(t, estate.bystander.token, "", 0, "")
	if len(bystanderPage.Items) != 0 {
		t.Fatalf("granted non-recipient discovered offers: %+v", bystanderPage)
	}
	crossed := []struct {
		label string
		token string
		id    model.ID
	}{
		{"sender", userOffer.source.communication.Token, userOffer.DeliveryID},
		{"owner", estate.owner.token, userOffer.DeliveryID},
		{"bystander", estate.bystander.token, userOffer.DeliveryID},
		{"user reading the agent carrier", estate.recipient.token, agentOffer.DeliveryID},
		{"agent reading the session carrier", estate.agent.token, sessionOffer.DeliveryID},
		{"session reading the user carrier", estate.targetSession.communication.Token, userOffer.DeliveryID},
	}
	for _, test := range crossed {
		response := estate.detail(t, test.token, test.id)
		if response.status != http.StatusNotFound {
			t.Fatalf("%s detail = %d: %s", test.label, response.status, response.raw)
		}
	}
	// A foreign tenant cannot even name the workspace.
	foreign := communicationHTTPTestRequest(t, estate.eng, http.MethodGet,
		incomingHandoffListPath(estate.workspace, "", 0, ""),
		estate.stranger.token, estate.otherTenant, nil, nil)
	if foreign.status == http.StatusOK {
		t.Fatalf("foreign tenant listed another tenant's workspace: %s", foreign.raw)
	}
	foreignDetail := communicationHTTPTestRequest(t, estate.eng, http.MethodGet,
		"/v1/m/sessions/deliveries/"+userOffer.DeliveryID.String()+"/handoff",
		estate.stranger.token, estate.otherTenant, nil, nil)
	if foreignDetail.status == http.StatusOK {
		t.Fatalf("foreign tenant read another tenant's offer: %s", foreignDetail.raw)
	}
	// A workspace selector the caller is not confined to is refused, and a
	// workspace of the other tenant is not reachable with this tenant header.
	crossWorkspace := communicationHTTPTestRequest(t, estate.eng, http.MethodGet,
		incomingHandoffListPath(estate.otherTenantWS, "", 0, ""),
		estate.recipient.token, estate.tenant, nil, nil)
	if crossWorkspace.status == http.StatusOK {
		t.Fatalf("recipient listed a workspace of another tenant: %s", crossWorkspace.raw)
	}
	assertCommunicationHTTPTestNoEffects(t, estate.eng, estate.tenant, before,
		"refused personal handoff reads")
}

func exerciseIncomingHandoffSelectorValidation(t *testing.T, estate incomingHandoffHTTPEstate) {
	t.Helper()
	before := communicationHTTPTestEffects(t, estate.eng, estate.tenant)
	base := "/v1/m/sessions/inbox/handoffs?workspace_id=" + estate.workspace.String()
	refused := []struct {
		label string
		path  string
	}{
		{"missing workspace", "/v1/m/sessions/inbox/handoffs?limit=1"},
		{"malformed workspace", "/v1/m/sessions/inbox/handoffs?workspace_id=not-a-uuid"},
		{"unknown state", base + "&state=pending"},
		{"upper-case state", base + "&state=OFFERED"},
		{"comma-joined state", base + "&state=offered,accepted"},
		{"repeated state", base + "&state=offered&state=accepted"},
		{"unknown selector", base + "&recipient=" + estate.recipient.id.String()},
		{"raw offset", base + "&offset=1"},
		{"raw deadline anchor", base + "&after_ack_deadline=2026-01-01T00:00:00.000000000Z"},
		{"raw handoff anchor", base + "&after_handoff_id=" + model.NewID().String()},
		{"zero limit", base + "&limit=0"},
		{"negative limit", base + "&limit=-1"},
		{"oversized limit", base + "&limit=201"},
		{"padded limit", base + "&limit=01"},
		{"non-numeric limit", base + "&limit=many"},
	}
	for _, test := range refused {
		response := communicationHTTPTestRequest(t, estate.eng, http.MethodGet, test.path,
			estate.recipient.token, estate.tenant, nil, nil)
		if response.status != http.StatusBadRequest && response.status != http.StatusForbidden {
			t.Fatalf("%s = %d: %s", test.label, response.status, response.raw)
		}
	}
	// A continuation from ANOTHER token family is refused: the direct-notice
	// cursor and the channel catalog have their own domains.
	inbox := communicationHTTPTestRequest(t, estate.eng, http.MethodGet,
		communicationHTTPTestInboxPath(estate.workspace, 1, ""),
		estate.recipient.token, estate.tenant, nil, nil)
	if inbox.status != http.StatusOK {
		t.Fatalf("personal inbox = %d: %s", inbox.status, inbox.raw)
	}
	inboxPage := communicationHTTPTestDecode[sessions.DirectNoticeInboxPage](t, inbox)
	foreignTokens := []string{"c2n1.forged.token.value", "c3n1.forged.token.value", "h3n1"}
	if inboxPage.CursorTarget != "" {
		foreignTokens = append(foreignTokens, inboxPage.CursorTarget)
	}
	for _, token := range foreignTokens {
		response := estate.list(t, estate.recipient.token, "", 0, token)
		if response.status != http.StatusBadRequest {
			t.Fatalf("foreign continuation %q = %d: %s", token, response.status, response.raw)
		}
		if strings.Contains(string(response.raw), token) {
			t.Fatalf("refusal echoed the presented token: %s", response.raw)
		}
	}
	assertCommunicationHTTPTestNoEffects(t, estate.eng, estate.tenant, before,
		"refused personal handoff selectors")
}

// exerciseIncomingHandoffPagination walks a recipient's own offers interleaved
// with offers addressed to others, so every page must skip the hidden ones
// without ever reporting them.
func exerciseIncomingHandoffPagination(t *testing.T, estate incomingHandoffHTTPEstate) {
	t.Helper()
	const own = 5
	wanted := make(map[model.ID]bool, own)
	for index := 0; index < own; index++ {
		// Interleave an offer addressed to the bystander between each of the
		// recipient's own, all inside the same deadline neighbourhood.
		estate.offer(t, map[string]any{"kind": "user", "ref": estate.bystander.id},
			fmt.Sprintf("hidden %d", index), time.Duration(60+index)*time.Minute)
		offer := estate.offer(t, map[string]any{"kind": "user", "ref": estate.recipient.id},
			fmt.Sprintf("paged %d", index), time.Duration(60+index)*time.Minute)
		wanted[offer.HandoffID] = false
	}
	seen := make([]model.ID, 0, own+1)
	continuation := ""
	pagedAtLeastOnce := false
	for round := 0; round <= own+1; round++ {
		page := estate.page(t, estate.recipient.token, "offered", 2, continuation)
		if len(page.Items) == 0 && page.HasMore {
			t.Fatalf("round %d reported has_more with no visible item: %+v", round, page)
		}
		for _, item := range page.Items {
			seen = append(seen, item.Handoff.ID)
		}
		if !page.HasMore {
			if page.Continuation != "" {
				t.Fatalf("final page carried a continuation: %+v", page)
			}
			break
		}
		if page.Continuation == "" {
			t.Fatalf("round %d reported has_more without a continuation", round)
		}
		if len(page.Items) != 2 {
			t.Fatalf("round %d returned %d items with has_more", round, len(page.Items))
		}
		pagedAtLeastOnce = true
		continuation = page.Continuation
	}
	// Positive control: with six of its own offers and a page size of two, the
	// walk MUST have paged. A run that never sets has_more would otherwise pass
	// the counting assertions by returning everything at once.
	if !pagedAtLeastOnce {
		t.Fatal("the interleaved walk never reported has_more")
	}
	// The recipient's own five offers of this walk are all present, exactly once,
	// and the earlier journey offer is the only other one it owns.
	counts := make(map[model.ID]int, len(seen))
	for _, id := range seen {
		counts[id]++
	}
	for id, count := range counts {
		if count != 1 {
			t.Fatalf("offer %s appeared %d times across the walk", id, count)
		}
	}
	for id := range wanted {
		if counts[id] != 1 {
			t.Fatalf("paged offer %s was not returned exactly once (%d)", id, counts[id])
		}
	}
	if len(seen) != own+1 {
		t.Fatalf("walk returned %d offers, want %d", len(seen), own+1)
	}
	// A continuation minted for one filter cannot resume another, and a stale
	// continuation cannot be replayed by a different reader.
	first := estate.page(t, estate.recipient.token, "offered", 2, "")
	if !first.HasMore || first.Continuation == "" {
		t.Fatalf("first page = %+v", first)
	}
	crossedFilter := estate.list(t, estate.recipient.token, "accepted", 2, first.Continuation)
	if crossedFilter.status != http.StatusBadRequest {
		t.Fatalf("continuation crossed its filter = %d: %s", crossedFilter.status, crossedFilter.raw)
	}
	crossedReader := estate.list(t, estate.bystander.token, "offered", 2, first.Continuation)
	if crossedReader.status != http.StatusBadRequest {
		t.Fatalf("continuation crossed its reader = %d: %s", crossedReader.status, crossedReader.raw)
	}
	crossedSession := estate.list(
		t, estate.targetSession.communication.Token, "offered", 2, first.Continuation)
	if crossedSession.status != http.StatusBadRequest {
		t.Fatalf("continuation crossed a session reader = %d: %s",
			crossedSession.status, crossedSession.raw)
	}
}

// exerciseIncomingHandoffResponseRevalidation changes the work under an offer
// between the personal GET and the existing POST: the ETag the read published is
// honestly current when it is read and is still revalidated by the mutation.
func exerciseIncomingHandoffResponseRevalidation(t *testing.T, estate incomingHandoffHTTPEstate) {
	t.Helper()
	offer := estate.offer(t,
		map[string]any{"kind": "user", "ref": estate.recipient.id}, "revalidated", 30*time.Minute)
	response := estate.detail(t, estate.recipient.token, offer.DeliveryID)
	if response.status != http.StatusOK {
		t.Fatalf("read before owner change = %d: %s", response.status, response.raw)
	}
	read := communicationHTTPTestDecode[sessions.IncomingHandoffReadResult](t, response)
	if read.OfferContext != sessions.IncomingHandoffContextCurrent {
		t.Fatalf("offer context before owner change = %q", read.OfferContext)
	}

	// A concurrent assignment moves the work; the offer becomes obsolete without
	// making the new owner an authorized recipient.
	workResponse := communicationHTTPTestRequest(t, estate.eng, http.MethodGet,
		"/v1/m/sessions/work-items/"+offer.WorkItemID.String(),
		estate.owner.token, estate.tenant, nil, nil)
	if workResponse.status != http.StatusOK {
		t.Fatalf("read WorkItem = %d: %s", workResponse.status, workResponse.raw)
	}
	work := communicationHTTPTestDecode[sessions.WorkSnapshot](t, workResponse)
	assignment := communicationHTTPTestRequest(t, estate.eng, http.MethodPost,
		"/v1/m/sessions/work-items/"+offer.WorkItemID.String()+"/assignments?mode=apply",
		estate.owner.token, estate.tenant, map[string]any{
			"owner_kind": "session", "owner_ref": estate.targetSession.sid,
		}, map[string]string{
			"If-Match":        fmt.Sprintf(`"v%d"`, work.Item.Version),
			"Idempotency-Key": model.NewID().String(),
		})
	if assignment.status != http.StatusOK {
		t.Fatalf("reassign WorkItem = %d: %s", assignment.status, assignment.raw)
	}

	stale := estate.detail(t, estate.recipient.token, offer.DeliveryID)
	if stale.status != http.StatusOK {
		t.Fatalf("read after owner change = %d: %s", stale.status, stale.raw)
	}
	staleRead := communicationHTTPTestDecode[sessions.IncomingHandoffReadResult](t, stale)
	if staleRead.OfferContext != sessions.IncomingHandoffContextStale ||
		staleRead.Handoff.State != sessions.HandoffOffered ||
		staleRead.Handoff.ETag != read.Handoff.ETag {
		t.Fatalf("stale offer projection = %+v", staleRead)
	}
	// The new owner is not the recipient and discovers nothing.
	newOwnerPage := estate.page(t, estate.targetSession.communication.Token, "offered", 0, "")
	for _, item := range newOwnerPage.Items {
		if item.Handoff.ID == offer.HandoffID {
			t.Fatalf("the new work owner discovered the recipient's offer: %+v", item)
		}
	}
	// And the mutation refuses it with the ETag the read published.
	before := communicationHTTPTestEffects(t, estate.eng, estate.tenant)
	rejected := communicationHTTPTestRequest(t, estate.eng, http.MethodPost,
		"/v1/m/sessions/handoffs/"+offer.HandoffID.String()+"/responses",
		estate.recipient.token, estate.tenant, map[string]any{"transition": "accept"},
		map[string]string{
			"If-Match": staleRead.Handoff.ETag, "Idempotency-Key": model.NewID().String(),
		})
	if rejected.status != http.StatusConflict {
		t.Fatalf("accept of a stale offer = %d: %s", rejected.status, rejected.raw)
	}
	assertCommunicationHTTPTestNoEffects(t, estate.eng, estate.tenant, before,
		"refused response to a stale offer")
	// The delivery version is NOT the Handoff CAS coordinate. This presents the
	// REAL current Delivery version — not an invented one — so the refusal is
	// about the wrong aggregate rather than about a number nothing recognises.
	// Here the two happen to coincide; the case that makes them genuinely diverge
	// and still refuses the Delivery value is
	// TestIncomingHandoffPublishesTwoAggregateVersionsAndOnlyAcceptsItsOwn.
	wrongETag := communicationHTTPTestRequest(t, estate.eng, http.MethodPost,
		"/v1/m/sessions/handoffs/"+offer.HandoffID.String()+"/responses",
		estate.recipient.token, estate.tenant, map[string]any{"transition": "reject",
			"reason": map[string]any{"code": "wrong_etag"}},
		map[string]string{
			"If-Match":        fmt.Sprintf(`"v%d"`, staleRead.Carrier.DeliveryVersion),
			"Idempotency-Key": model.NewID().String(),
		})
	if wrongETag.status == http.StatusOK {
		t.Fatalf("a delivery version was accepted as the Handoff ETag: %s", wrongETag.raw)
	}
}

// exerciseIncomingHandoffRestartAndReplay reopens the estate, re-reads every
// recipient's own offer, then accepts and rejects through the existing endpoint
// and replays both without duplicating a durable effect.
func exerciseIncomingHandoffRestartAndReplay(
	t *testing.T,
	estate incomingHandoffHTTPEstate,
	userOffer, agentOffer, sessionOffer incomingHandoffHTTPOffer,
) {
	t.Helper()
	// After the restart every recipient still discovers and reads its OWN offer,
	// with the durable agent and session credentials issued before the reboot.
	for label, reader := range map[string]struct {
		token string
		offer incomingHandoffHTTPOffer
	}{
		"user":    {estate.recipient.token, userOffer},
		"agent":   {estate.agent.token, agentOffer},
		"session": {estate.targetSession.communication.Token, sessionOffer},
	} {
		page := estate.page(t, reader.token, "offered", 0, "")
		found := false
		for _, item := range page.Items {
			if item.Handoff.ID == reader.offer.HandoffID {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s recipient lost its offer across the restart: %+v", label, page)
		}
		response := estate.detail(t, reader.token, reader.offer.DeliveryID)
		if response.status != http.StatusOK {
			t.Fatalf("%s recipient detail after restart = %d: %s",
				label, response.status, response.raw)
		}
		reread := communicationHTTPTestDecode[sessions.IncomingHandoffReadResult](t, response)
		if reread.Handoff.ETag != reader.offer.ETag ||
			reread.OfferContext != sessions.IncomingHandoffContextCurrent {
			t.Fatalf("%s recipient re-read after restart = %+v", label, reread)
		}
	}

	// Accept as the user recipient with the ETag its own read published.
	read := communicationHTTPTestDecode[sessions.IncomingHandoffReadResult](
		t, estate.detail(t, estate.recipient.token, userOffer.DeliveryID))
	acceptKey := model.NewID().String()
	accepted := communicationHTTPTestRequest(t, estate.eng, http.MethodPost,
		"/v1/m/sessions/handoffs/"+read.Handoff.ID.String()+"/responses",
		estate.recipient.token, estate.tenant, map[string]any{"transition": "accept"},
		map[string]string{"If-Match": read.Handoff.ETag, "Idempotency-Key": acceptKey})
	if accepted.status != http.StatusOK {
		t.Fatalf("accept with the published ETag = %d: %s", accepted.status, accepted.raw)
	}
	acceptResult := communicationHTTPTestDecode[sessions.HandoffResponseResult](t, accepted)
	if acceptResult.State != sessions.HandoffAccepted || acceptResult.Replayed {
		t.Fatalf("accept result = %+v", acceptResult)
	}
	// Reject as the agent recipient.
	agentRead := communicationHTTPTestDecode[sessions.IncomingHandoffReadResult](
		t, estate.detail(t, estate.agent.token, agentOffer.DeliveryID))
	rejectKey := model.NewID().String()
	rejected := communicationHTTPTestRequest(t, estate.eng, http.MethodPost,
		"/v1/m/sessions/handoffs/"+agentRead.Handoff.ID.String()+"/responses",
		estate.agent.token, estate.tenant,
		map[string]any{"transition": "reject", "reason": map[string]any{
			"code": "not_mine", "text": "The agent declines the transfer",
		}},
		map[string]string{"If-Match": agentRead.Handoff.ETag, "Idempotency-Key": rejectKey})
	if rejected.status != http.StatusOK {
		t.Fatalf("reject with the published ETag = %d: %s", rejected.status, rejected.raw)
	}
	rejectResult := communicationHTTPTestDecode[sessions.HandoffResponseResult](t, rejected)
	if rejectResult.State != sessions.HandoffRejected || rejectResult.Replayed {
		t.Fatalf("reject result = %+v", rejectResult)
	}

	// A terminal offer stays readable to its recipient, with its terminal reason
	// and a terminal offer context, and it is no longer offered.
	terminal := communicationHTTPTestDecode[sessions.IncomingHandoffReadResult](
		t, estate.detail(t, estate.agent.token, agentOffer.DeliveryID))
	if terminal.OfferContext != sessions.IncomingHandoffContextTerminal ||
		terminal.Handoff.State != sessions.HandoffRejected ||
		terminal.Handoff.TerminalAt == nil || terminal.TerminalReason == nil ||
		terminal.TerminalReason.Code != "not_mine" {
		t.Fatalf("rejected offer detail = %+v", terminal)
	}
	acceptedDetail := communicationHTTPTestDecode[sessions.IncomingHandoffReadResult](
		t, estate.detail(t, estate.recipient.token, userOffer.DeliveryID))
	if acceptedDetail.OfferContext != sessions.IncomingHandoffContextTerminal ||
		acceptedDetail.Handoff.State != sessions.HandoffAccepted ||
		acceptedDetail.TerminalReason != nil {
		t.Fatalf("accepted offer detail = %+v", acceptedDetail)
	}

	// Replay both commands: idempotent, and no new durable effect.
	before := communicationHTTPTestEffects(t, estate.eng, estate.tenant)
	acceptReplay := communicationHTTPTestRequest(t, estate.eng, http.MethodPost,
		"/v1/m/sessions/handoffs/"+read.Handoff.ID.String()+"/responses",
		estate.recipient.token, estate.tenant, map[string]any{"transition": "accept"},
		map[string]string{"If-Match": read.Handoff.ETag, "Idempotency-Key": acceptKey})
	if acceptReplay.status != http.StatusOK {
		t.Fatalf("accept replay = %d: %s", acceptReplay.status, acceptReplay.raw)
	}
	replayed := communicationHTTPTestDecode[sessions.HandoffResponseResult](t, acceptReplay)
	if !replayed.Replayed || replayed.CommandID != acceptResult.CommandID ||
		replayed.Version != acceptResult.Version {
		t.Fatalf("accept replay = %+v", replayed)
	}
	assertCommunicationHTTPTestNoEffects(t, estate.eng, estate.tenant, before,
		"replayed handoff response")

	// The session recipient's offer is untouched by the other two and survives a
	// restart: a personal read is state, not a cache.
	sessionPage := estate.page(t, estate.targetSession.communication.Token, "offered", 0, "")
	found := false
	for _, item := range sessionPage.Items {
		if item.Handoff.ID == sessionOffer.HandoffID {
			found = true
		}
	}
	if !found {
		t.Fatalf("session recipient lost its offer: %+v", sessionPage)
	}
	restartBefore := communicationHTTPTestEffects(t, estate.eng, estate.tenant)
	sessionDetail := estate.detail(
		t, estate.targetSession.communication.Token, sessionOffer.DeliveryID)
	if sessionDetail.status != http.StatusOK {
		t.Fatalf("session recipient detail after the other responses = %d: %s",
			sessionDetail.status, sessionDetail.raw)
	}
	assertCommunicationHTTPTestNoEffects(t, estate.eng, estate.tenant, restartBefore,
		"re-reading a personal handoff offer")
}
