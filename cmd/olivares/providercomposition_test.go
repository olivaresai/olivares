// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/eventbus"
	"github.com/olivaresai/olivares/core/eventbus/natsbus"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

type compositionSource struct {
	regEmitSource
	resource string
}

func (s *compositionSource) Gather(ctx context.Context, sink sdk.Sink) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.fire:
			if err := sink.Emit(ctx, sdkmodel.EdgeObservation{OriginKind: "session", OriginRef: "ext-1", ResourceRef: s.resource, Mode: sdkmodel.ModeRead, ObservedAt: time.Now().UTC()}); err != nil {
				return err
			}
		}
	}
}

// This is the composition interaction: Source A's independent named instances
// feed B1's host registration and per-observation binding admission, then B2's
// real projection and authenticated HTTP reads. The connectors emit the same
// external id; the fixture supplies no managed-process proof.
func TestProviderCompositionTwoSourceInstancesAdmissionAndAPI(t *testing.T) {
	backends := []store.Config{{Engine: store.EngineSQLite, DSN: ":memory:"}}
	if enginetest.PostgresAvailable(t) {
		dsns := enginetest.IsolatedPostgres(t)
		backends = append(backends, store.Config{Engine: store.EnginePostgres, DSN: dsns.App, OwnerDSN: dsns.Owner, AdminDSN: dsns.Admin})
	} else {
		t.Log("PostgreSQL not exercised without the configured fixture")
	}
	for _, cfg := range backends {
		t.Run(string(cfg.Engine), func(t *testing.T) {
			ctx := context.Background()
			ss := sessions.New()
			ss.UseExecutionEnvironmentRef("composition-env")
			st, err := coreengine.Open(ctx, cfg, ss.RegisterSchema)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			if err := st.System(ctx, func(sys store.SystemScope) error { _, err := sys.EnsureSystemTenant(ctx); return err }); err != nil {
				t.Fatal(err)
			}
			ss.UseData(api.NewModuleData(st))
			_, priv, err := ed25519.GenerateKey(nil)
			if err != nil {
				t.Fatal(err)
			}
			signer, err := audit.NewSigner(priv)
			if err != nil {
				t.Fatal(err)
			}
			setup := secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token"))
			setupValue, _, err := setup.Ensure()
			if err != nil {
				t.Fatal(err)
			}
			authz := auth.NewAuthorizer(nil)
			server, err := api.New(api.Options{Store: st, Authenticator: auth.NewAuthenticator(st, nil), Authorizer: authz, Signer: signer, SetupToken: setup, Version: "test", Modules: []api.Module{ss}})
			if err != nil {
				t.Fatal(err)
			}
			var bearer, tenantHeader string
			request := func(method, path string, body any, want int) map[string]any {
				t.Helper()
				payload, err := json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
				req := httptest.NewRequest(method, path, bytes.NewReader(payload))
				req.RemoteAddr = "10.0.0.1:1234"
				if bearer != "" {
					req.Header.Set("Authorization", "Bearer "+bearer)
				}
				if tenantHeader != "" {
					req.Header.Set("X-Olivares-Tenant", tenantHeader)
				}
				rec := httptest.NewRecorder()
				server.Handler().ServeHTTP(rec, req)
				if rec.Code != want {
					t.Fatalf("%s %s status=%d want=%d", method, path, rec.Code, want)
				}
				var out map[string]any
				if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
					t.Fatal(err)
				}
				return out
			}
			request("POST", "/v1/setup", map[string]any{"token": setupValue, "email": "composition@example.invalid", "password": "fixture-password-1"}, 201)
			bearer = request("POST", "/v1/auth/login", map[string]any{"email": "composition@example.invalid", "password": "fixture-password-1"}, 200)["token"].(string)
			tenantHeader = request("POST", "/v1/system/orgs", map[string]any{"name": "composition", "slug": "composition"}, 201)["tenant_id"].(string)
			tenant := model.TenantID(tenantHeader)

			bus := eventbus.NewInProc(eventbus.Options{Logger: quietLog()})
			rt := runtime.New(runtime.Options{Logger: quietLog(), Bus: bus, SourceRegistrationAdmission: ss.AdmitSourceRegistration})
			capture := &regCaptureModule{got: make(chan event.Event, 16)}
			for _, mod := range []sdk.Module{capture, ss} {
				if err := rt.AddModule(mod, sdk.Config{}); err != nil {
					t.Fatal(err)
				}
			}
			if err := rt.Start(ctx); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := rt.Stop(stop); err != nil {
					t.Errorf("runtime stop: %v", err)
				}
				_ = bus.Close()
			})
			sourceStore := auth.NewSourceStore(st)
			sr := newSourceReconciler(rt, sourceStore, nil, nil, t.TempDir(), nil, quietLog())
			sr.useEnvironmentRef("composition-env")
			opened := map[string]*compositionSource{}
			sr.prepare = func(_ context.Context, def model.SourceDef) (*runtime.PreparedSource, sdk.Config, string) {
				source := &compositionSource{regEmitSource: regEmitSource{name: "olivares.claude", fire: make(chan struct{}, 4)}, resource: "/fixture/" + def.Name}
				opened[def.Name] = source
				return rt.PrepareInProcSource(source), sdk.Config{}, ""
			}
			ss.UseProviderSourceResolver(&providerSourceResolver{store: sourceStore, sr: sr, authz: authz, env: "composition-env"})
			rows := map[string]model.SourceDef{}
			profiles := map[string]sessions.ProviderProfile{}
			resourceByProfile := map[string]string{}
			bindings := map[string]sessions.ProviderSourceBinding{}
			for _, name := range []string{"home-a", "home-b"} {
				row, err := sourceStore.Put(ctx, recAdmin(), model.SourceDef{Scope: auth.GlobalSourceScope, Name: name, Kind: "claude", Tenant: tenantHeader, Enabled: true})
				if err != nil {
					t.Fatal(err)
				}
				rows[name] = row
				profile, err := ss.CreateProfile(ctx, tenant, sessions.CreateProfileInput{Driver: "claude", ConfigHome: t.TempDir(), UserHome: t.TempDir()})
				if err != nil {
					t.Fatal(err)
				}
				profiles[name] = profile
				resourceByProfile[profile.Ref] = "/fixture/" + name
			}
			if _, err := sr.reconcile(ctx); err != nil {
				t.Fatal(err)
			}
			if len(opened) != 2 || opened["home-a"] == opened["home-b"] {
				t.Fatal("two roster instances did not own independent connectors")
			}
			for name, row := range rows {
				binding, err := ss.CreateBinding(ctx, tenant, recAdmin(), sessions.CreateBindingInput{SourceID: row.ID, SourceRevision: row.Version, ProfileRef: profiles[name].Ref})
				if err != nil {
					t.Fatal(err)
				}
				bindings[name] = binding
			}
			recv := func() event.Event {
				t.Helper()
				select {
				case e := <-capture.got:
					return e
				case <-time.After(5 * time.Second):
					t.Fatal("source event not delivered")
				}
				return event.Event{}
			}
			original := map[string]event.Event{}
			for _, name := range []string{"home-a", "home-b"} {
				opened[name].fire <- struct{}{}
				e := recv()
				if e.Source != name || e.SourceRegistration == nil || e.SourceRegistration.SourceID != rows[name].ID.String() || e.SourceRegistration.SourceRevision != rows[name].Version || e.SourceRegistration.EnvironmentRef != "composition-env" || e.SourceRegistration.BindingRef != bindings[name].Ref {
					t.Fatal("source lost its exact admission provenance")
				}
				original[name] = e
			}
			waitRows := func(want int) []sessions.LiveSample {
				t.Helper()
				deadline := time.Now().Add(5 * time.Second)
				for {
					samples, err := ss.SampleLive(ctx, tenant, sessions.LiveSampleQuery{Limit: 10})
					if err != nil {
						t.Fatal(err)
					}
					if len(samples) == want {
						return samples
					}
					if time.Now().After(deadline) {
						t.Fatalf("live rows=%d want=%d", len(samples), want)
					}
					time.Sleep(5 * time.Millisecond)
				}
			}
			liveByProfile := map[string]string{}
			for _, sample := range waitRows(2) {
				if sample.SessionRef != "ext-1" || sample.Attribution != "observed" {
					t.Fatal("source became legacy or managed")
				}
				liveByProfile[sample.ProfileRef] = sample.LiveRef
			}
			if len(liveByProfile) != 2 {
				t.Fatal("equal external ids collapsed profiles")
			}
			assertHTTP := func(want int) {
				t.Helper()
				list := request("GET", "/v1/m/sessions/live?session_ref=ext-1", nil, 200)
				if len(list["items"].([]any)) != want {
					t.Fatal("HTTP list collapsed scopes")
				}
				request("GET", "/v1/m/sessions/live/ext-1", nil, 404)
				for profile, live := range liveByProfile {
					detail := request("GET", "/v1/m/sessions/live/by-id/"+live, nil, 200)
					if detail["provider_profile_ref"] != profile || detail["attribution"] != "observed" || detail["run_ref"] != nil || detail["canonical_sid"] != nil {
						t.Fatal("HTTP changed observed identity or fabricated managed authority")
					}
					timeline := request("GET", "/v1/m/sessions/live/by-id/"+live+"/timeline", nil, 200)["items"].([]any)
					if len(timeline) == 0 {
						t.Fatal("source observation missing from its scoped timeline")
					}
					for _, item := range timeline {
						if item.(map[string]any)["resource_ref"] != resourceByProfile[profile] {
							t.Fatal("timeline contains the other source instance's observation")
						}
					}
					runs := request("GET", "/v1/m/sessions/runs?live_ref="+live, nil, 200)
					if len(runs["items"].([]any)) != 0 {
						t.Fatal("observed source gained a managed run")
					}
				}
			}
			assertHTTP(2)
			encoded, err := natsbus.EncodeEvent(original["home-a"])
			if err != nil {
				t.Fatal(err)
			}
			replay, err := natsbus.DecodeEvent(encoded, natsbus.DefaultDecoders())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ss.RevokeBinding(ctx, tenant, bindings["home-a"].Ref); err != nil {
				t.Fatal(err)
			}
			if err := bus.Publish(ctx, replay); err != nil {
				t.Fatal(err)
			}
			if e := recv(); e.ID != original["home-a"].ID || *e.SourceRegistration != *original["home-a"].SourceRegistration {
				t.Fatal("replay changed the admitted envelope")
			}
			opened["home-a"].fire <- struct{}{}
			if e := recv(); e.SourceRegistration.BindingRef != "" || e.ID == replay.ID {
				t.Fatal("new observation reused revoked decision")
			}
			opened["home-b"].fire <- struct{}{}
			if e := recv(); e.SourceRegistration.BindingRef != bindings["home-b"].Ref {
				t.Fatal("revocation disturbed the independent sibling")
			}
			var sourceRows int
			for _, sample := range waitRows(3) {
				if sample.Attribution == "source" && sample.ProfileRef == "" {
					sourceRows++
					continue
				}
				if sample.Attribution != "observed" || liveByProfile[sample.ProfileRef] != sample.LiveRef {
					t.Fatal("replay relabeled historical identity")
				}
			}
			if sourceRows != 1 {
				t.Fatal("post-revoke observation acquired a profile")
			}
			assertHTTP(3)
		})
	}
}
