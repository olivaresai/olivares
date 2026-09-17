// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
)

// Fault-only wrappers retain the activated estate's real credentials, authorization,
// module and tenant transactions. No wrapper supplies a fabricated positive verdict.
type nondisclosureStore struct {
	store.Store
	reads      int
	faults     int
	fail       model.ID
	panicOnGet bool
}

func (s *nondisclosureStore) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return s.Store.View(ctx, tenant, func(sc store.Scope) error { return fn(nondisclosureScope{Scope: sc, spy: s}) })
}

type nondisclosureScope struct {
	store.Scope
	spy *nondisclosureStore
}

func (s nondisclosureScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	repo, err := s.Scope.Ext(kind)
	if err != nil || kind != "sessions.channel" {
		return repo, err
	}
	return nondisclosureRepo{GenericRepo: repo, spy: s.spy}, nil
}

type nondisclosureRepo struct {
	store.GenericRepo
	spy *nondisclosureStore
}

func (r nondisclosureRepo) Get(ctx context.Context, id model.ID) (model.Record, error) {
	r.spy.reads++
	if id == r.spy.fail {
		r.spy.faults++
		if r.spy.panicOnGet {
			panic("private target panic sentinel")
		}
		return nil, errors.New("private target scan failure sentinel")
	}
	return r.GenericRepo.Get(ctx, id)
}

type nondisclosureClock struct{ advance atomic.Int64 }

func (c *nondisclosureClock) Now() model.Timestamp {
	return model.NewTimestamp(time.Now().Add(time.Duration(c.advance.Load())))
}

type nondisclosureModule struct {
	*sessions.Module
	clock       *nondisclosureClock
	expire      model.ID
	sawPositive bool
}

func (m *nondisclosureModule) APIRoutes(reg api.RouteRegistrar) {
	m.Module.APIRoutes(reg)
	reg.(interface {
		HandlePolicy(string, string, auth.Permission, api.RouteMetadata, api.ModuleHandler)
	}).HandlePolicy("GET", "/n2-step-up", "sessions:channel:admin",
		api.RouteMetadata{RouteMetadata: auth.RouteMetadata{MinimumAAL: auth.AAL3}},
		func(http.ResponseWriter, *http.Request, api.ModuleContext) { panic("unreachable test handler") })
}
func (m *nondisclosureModule) ProjectModuleCapability(ctx context.Context, q api.ModuleCapabilityQuestion) api.ModuleCapabilityResult {
	result := m.Module.ProjectModuleCapability(ctx, q)
	if q.Resource.ID == m.expire.String() && result.State == api.CapabilityAllowed {
		m.sawPositive = true
		// Advance ONLY the API's closing clock after the actual module returned its
		// positive. Its real evidence/transaction clock and result are untouched.
		m.clock.advance.Store(int64(time.Minute))
	}
	return result
}

func nondisclosureTestEngine(t *testing.T, estate channelAdministrationHTTPEstate) (*engine, *nondisclosureStore, *nondisclosureModule) {
	t.Helper()
	eng := estate.eng
	spy := &nondisclosureStore{Store: eng.store}
	module := &nondisclosureModule{Module: eng.sessionsMod, clock: &nondisclosureClock{}}
	server, err := api.New(api.Options{
		Store: spy, Authenticator: eng.authr, Authorizer: eng.authz, Signer: eng.signer,
		PrincipalEvidenceProducer: eng.authr, SetupToken: eng.setupTok,
		Logger: eng.log, Clock: module.clock, Modules: []api.Module{module}, Version: "n2-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	copy := *eng
	copy.api = server
	return &copy, spy, module
}

func nondisclosureSameBodies(t *testing.T, response communicationHTTPTestResponse, ids ...string) {
	t.Helper()
	if response.status != http.StatusOK || response.header.Get("Cache-Control") != "no-store" ||
		response.header.Get("ETag") != "" || response.header.Get("Retry-After") != "" {
		t.Fatalf("non-disclosure transport: status=%d headers=%v", response.status, response.header)
	}
	decoded := communicationHTTPTestDecode[struct {
		Schema  int              `json:"schema_version"`
		Results []map[string]any `json:"results"`
	}](t, response)
	if decoded.Schema != 2 || len(decoded.Results) != len(ids) {
		t.Fatalf("unexpected envelope: %s", response.raw)
	}
	var first map[string]any
	for i, result := range decoded.Results {
		if result["id"] != ids[i] || result["state"] != "undisclosed" || result["code"] != "not_disclosed" || len(result) != 5 {
			t.Fatalf("unexpected non-verdict: %v", result)
		}
		delete(result, "id")
		if first == nil {
			first = result
		} else if !reflect.DeepEqual(first, result) {
			t.Fatalf("non-verdict bodies differ, including observation marker: %v / %v", first, result)
		}
	}
	if strings.Contains(string(response.raw), "sentinel") {
		t.Fatal("private fault reached response")
	}
}

func TestCapabilityNonDisclosureHTTP(t *testing.T) {
	estate := bootChannelAdministrationHTTPEstate(t, communicationHTTPTestSQLiteStore(t))
	eng, spy, module := nondisclosureTestEngine(t, estate)
	ws, tenant := estate.workspace.String(), estate.tenant
	owner := channelAdministrationGrant(channelAdministrationSubject("user", estate.owner.id), true, true, true)
	admin := channelAdministrationGrant(channelAdministrationSubject("user", estate.steward.id), false, false, true)
	held := estate.createChannel(t, estate.workspace, "n2-held", []map[string]any{owner, admin}).Channel.ID
	hidden := estate.createChannel(t, estate.workspace, "n2-hidden", []map[string]any{owner}).Channel.ID
	foreign := capabilityForeignChannel(t, estate)
	missing, missing2 := model.NewID(), model.NewID()
	question := func(id string, target model.ID) map[string]any {
		return capabilityChannelQuestion(id, capOperationGrantSheet, ws, target)
	}
	ask := func(q ...map[string]any) communicationHTTPTestResponse {
		return capabilityAskRaw(t, eng, estate.steward.token, tenant, q...)
	}

	t.Run("negative singleton and reordered composition", func(t *testing.T) {
		for _, pair := range []struct {
			name string
			id   model.ID
		}{{"missing", missing}, {"hidden", hidden}, {"foreign", foreign}} {
			nondisclosureSameBodies(t, ask(question(pair.name, pair.id)), pair.name)
		}
		nondisclosureSameBodies(t, ask(question("m", missing), question("m2", missing2)), "m", "m2")
		nondisclosureSameBodies(t, ask(question("m", missing), question("h", hidden)), "m", "h")
		nondisclosureSameBodies(t, ask(question("h", hidden), question("m", missing)), "h", "m")
		wrong := capabilityChannelQuestion("wrong", capOperationGrantSheet, estate.sideWorkspace.String(), held)
		nondisclosureSameBodies(t, ask(wrong, question("foreign", foreign), question("hidden", hidden)), "wrong", "foreign", "hidden")
		capabilityWant(t, capabilityAsk(t, eng, estate.steward.token, tenant, question("held", held)), "held", "allowed", "authorized")
	})
	t.Run("producer unknown and internal typed denial stay distinct", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		raw, err := eng.authr.Authenticate(ctx, estate.viewer.token)
		if err != nil {
			t.Fatal(err)
		}
		ref, ok := raw.Ref()
		if !ok {
			t.Fatal("missing real credential reference")
		}
		principal, err := eng.authr.ResolvePrincipalScope(ctx, ref, tenant)
		if err != nil {
			t.Fatal(err)
		}
		req := auth.Request{Principal: principal, Tenant: tenant, Permission: "sessions:channel:admin", Resource: auth.ResourceAttrs{Kind: "channel", ID: hidden.String(), WorkspaceID: estate.workspace}}
		if got := eng.authz.AuthorizeEvidence(ctx, req); got.Outcome != auth.EvidenceDeny {
			t.Fatalf("real viewer negative = %v", got.Outcome)
		}
		original := *eng.authz
		*eng.authz = *auth.NewAuthorizer(nil, auth.WithScopedGrants(independentG1AScopedOutage{}))
		defer func() { *eng.authz = original }()
		if got := eng.authz.AuthorizeEvidence(ctx, req); got.Outcome != auth.EvidenceUnknown {
			t.Fatalf("fault was reclassified: %v", got.Outcome)
		}
		nondisclosureSameBodies(t, ask(question("h", hidden), question("m", missing)), "h", "m")
		nondisclosureSameBodies(t, ask(question("m", missing)), "m")
		nondisclosureSameBodies(t, ask(question("h", hidden)), "h")
		answers := capabilityAsk(t, eng, estate.steward.token, tenant,
			capabilitySurfaceQuestion("surface", capSurfaceAdministration, ws),
			capabilityBodyQuestion("ordinary", capOperationPatch, ws, held))
		for _, id := range []string{"surface", "ordinary"} {
			capabilityWant(t, answers, id, "unknown", "evidence_unavailable")
		}
	})
	t.Run("target loader error", func(t *testing.T) {
		spy.fail, spy.faults = hidden, 0
		defer func() { spy.fail = "" }()
		nondisclosureSameBodies(t, ask(question("h", hidden), question("m", missing)), "h", "m")
		if spy.faults != 1 {
			t.Fatalf("target fault hits = %d, want 1", spy.faults)
		}
	})
	t.Run("recoverable question panic leaves adjacent positive intact", func(t *testing.T) {
		spy.fail, spy.panicOnGet, spy.faults = hidden, true, 0
		defer func() { spy.fail, spy.panicOnGet = "", false }()
		nondisclosureSameBodies(t, ask(question("h", hidden), question("m", missing)), "h", "m")
		answers := capabilityAsk(t, eng, estate.steward.token, tenant, question("h", hidden), question("held", held))
		capabilityWant(t, answers, "h", "undisclosed", "not_disclosed")
		capabilityWant(t, answers, "held", "allowed", "authorized")
		if spy.faults != 2 {
			t.Fatalf("target panic hits = %d, want 2", spy.faults)
		}
	})
	t.Run("late expiry after actual module positive", func(t *testing.T) {
		module.expire = held
		defer func() { module.expire = ""; module.clock.advance.Store(0) }()
		nondisclosureSameBodies(t, ask(question("expired", held), question("m", missing)), "expired", "m")
		if !module.sawPositive {
			t.Fatal("test did not reach a real inner positive before closing-clock expiry")
		}
	})
	t.Run("request gates precede all target lookups", func(t *testing.T) {
		spy.reads = 0
		unauthenticated := communicationHTTPTestRequest(t, eng, http.MethodPost, "/v1/auth/capabilities", "", tenant,
			map[string]any{"schema_version": 2, "questions": []map[string]any{question("held", held)}}, nil)
		if unauthenticated.status != http.StatusUnauthorized {
			t.Fatalf("unauthenticated = %d", unauthenticated.status)
		}
		legacy := communicationHTTPTestRequest(t, eng, http.MethodPost, "/v1/auth/capabilities", estate.steward.token, tenant,
			map[string]any{"schema_version": 1, "questions": []map[string]any{question("held", held)}}, nil)
		if legacy.status != http.StatusBadRequest {
			t.Fatalf("schema 1 = %d", legacy.status)
		}
		malformed := communicationHTTPTestRequest(t, eng, http.MethodPost, "/v1/auth/capabilities", estate.steward.token, tenant,
			map[string]any{"schema_version": 2, "questions": []map[string]any{question("dup", held), question("dup", missing)}}, nil)
		if malformed.status != http.StatusBadRequest {
			t.Fatalf("duplicate ids = %d", malformed.status)
		}
		bad := question("selector", held)
		bad["selectors"] = map[string]any{"path": map[string]any{"id": "not-an-id"}}
		answers := capabilityAsk(t, eng, estate.steward.token, tenant, bad,
			capabilityChannelQuestion("unsupported", "GET /v1/m/sessions/channels/{id}", ws, hidden),
			capabilityChannelQuestion("unregistered", "GET /v1/m/sessions/n2-absent/{id}", ws, hidden),
			capabilityChannelQuestion("stepup", "GET /v1/m/sessions/n2-step-up", ws, hidden))
		capabilityWant(t, answers, "selector", "unknown", "inputs_required")
		for _, id := range []string{"unsupported", "unregistered"} {
			capabilityWant(t, answers, id, "unknown", "not_supported")
		}
		capabilityWant(t, answers, "stepup", "unknown", "step_up_required")
		if spy.reads != 0 {
			t.Fatalf("request gates read %d targets", spy.reads)
		}
	})
	t.Run("credential hard ceiling and ordinary denial", func(t *testing.T) {
		session := createCommunicationHTTPTestSession(t, estate.eng, tenant, estate.workspace, "n2-ceiling")
		answers := capabilityAsk(t, eng, session.communication.Token, tenant, question("sheet", held), capabilityBodyQuestion("patch", capOperationPatch, ws, held))
		capabilityWant(t, answers, "sheet", "undisclosed", "not_disclosed")
		capabilityWant(t, answers, "patch", "denied", "not_permitted")
		if got := estate.sheet(t, session.communication.Token, held, "workspace_id="+ws); got.status != http.StatusNotFound {
			t.Fatalf("real wrapper ignored credential ceiling: %d", got.status)
		}
	})
	t.Run("valid free form outside static checker retains exact positive", func(t *testing.T) {
		channel := estate.createChannel(t, estate.workspace, "n2-free-form", []map[string]any{owner,
			channelAdministrationGrant(channelAdministrationSubject("user", estate.viewer.id), false, false, true)}).Channel.ID
		capabilityPublishAuthored(t, estate.eng, estate, fmt.Sprintf(
			`permit(principal in User::%q, action == Action::"sessions:channel:admin", resource == Resource::%q) when { 1 + 1 == 2 };`, estate.viewer.id.String(), channel.String()))
		answers := capabilityAsk(t, eng, estate.viewer.token, tenant, question("sheet", channel), capabilitySurfaceQuestion("surface", capSurfaceAdministration, ws))
		capabilityWant(t, answers, "sheet", "allowed", "authorized")
		capabilityWant(t, answers, "surface", "not_reachable", "not_permitted")
		if got := estate.sheet(t, estate.viewer.token, channel, "workspace_id="+ws); got.status != http.StatusOK {
			t.Fatalf("real free-form wrapper = %d: %s", got.status, got.raw)
		}
	})
}
