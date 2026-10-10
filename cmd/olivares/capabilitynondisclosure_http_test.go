// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
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
