// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sourcescope"
)

func wireBackends(t *testing.T) []store.Config {
	t.Helper()
	configs := []store.Config{{Engine: store.EngineSQLite, DSN: ":memory:"}}
	if enginetest.PostgresAvailable(t) {
		dsns := enginetest.IsolatedPostgres(t)
		configs = append(configs, store.Config{Engine: store.EnginePostgres, DSN: dsns.App, OwnerDSN: dsns.Owner, AdminDSN: dsns.Admin})
	} else {
		t.Log("PostgreSQL not exercised without the configured fixture")
	}
	return configs
}

func wireBoot(t *testing.T, cfg store.Config, dir string) (*engine, string, model.TenantID) {
	t.Helper()
	auth.SetTestHashParams(auth.TestArgonMemKiB, auth.TestArgonTime, auth.TestArgonThreads)
	eng, err := boot(context.Background(), bootConfig{DataDir: dir, Engine: string(cfg.Engine), DSN: cfg.DSN,
		OwnerDSN: cfg.OwnerDSN, AdminDSN: cfg.AdminDSN, Version: "test", Logger: quietLog()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	setup, _, err := eng.setupTok.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if code, _, _ := doDemoViewJSON(t, eng.api.Handler(), "POST", "/v1/setup", "", "", map[string]any{
		"token": setup, "email": "wire@example.invalid", "password": "fixture-password-1",
	}); code != http.StatusCreated {
		t.Fatalf("setup = %d", code)
	}
	code, login, _ := doDemoViewJSON(t, eng.api.Handler(), "POST", "/v1/auth/login", "", "", map[string]any{
		"email": "wire@example.invalid", "password": "fixture-password-1",
	})
	if code != http.StatusOK {
		t.Fatalf("login = %d", code)
	}
	token := login["token"].(string)
	code, org, _ := doDemoViewJSON(t, eng.api.Handler(), "POST", "/v1/system/orgs", token, "", map[string]any{"name": "WIRE", "slug": "wire"})
	if code != http.StatusCreated {
		t.Fatalf("create org = %d", code)
	}
	return eng, token, model.TenantID(org["tenant_id"].(string))
}

func TestWorkspaceConnectorInlineSecretsWired(t *testing.T) {
	for _, cfg := range wireBackends(t) {
		t.Run(string(cfg.Engine), func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			eng, token, tenant := wireBoot(t, cfg, dir)
			sealer, err := newSecretSealer(dir, func(string) string { return "" })
			if err != nil {
				t.Fatal(err)
			}
			vault := auth.NewSecretStore(eng.store, sealer)
			request := func(method, path string, body any, want int) map[string]any {
				t.Helper()
				code, out, raw := doDemoViewJSON(t, eng.api.Handler(), method, path, token, tenant.String(), body)
				if code != want {
					t.Fatalf("%s %s = %d, want %d", method, path, code, want)
				}
				for _, literal := range []string{"wire-secret-one", "wire-secret-two", "wire-refused-secret"} {
					if strings.Contains(raw, literal) {
						t.Fatal("response exposed inline secret")
					}
				}
				return out
			}
			body := map[string]any{"name": "docs", "kind": "confluence", "workspace_ref": "default", "enabled": true,
				"secrets": map[string]string{"api_key": "wire-secret-one"}}
			if code, _, _ := doDemoViewJSON(t, eng.api.Handler(), "POST", "/v1/m/sourcescope/workspace-connectors", "", tenant.String(), body); code != http.StatusUnauthorized {
				t.Fatalf("unauthenticated create = %d", code)
			}
			created := request("POST", "/v1/m/sourcescope/workspace-connectors", body, http.StatusCreated)
			id := created["id"].(string)
			if created["secrets"].(map[string]any)["api_key"] != "***" {
				t.Fatal("secret presence was not masked")
			}
			checkVault := func(want string) string {
				t.Helper()
				entries, err := vault.List(ctx, tenant)
				if err != nil || len(entries) != 1 {
					t.Fatalf("tenant vault entries = %d, error = %v", len(entries), err)
				}
				value, err := vault.Resolve(ctx, tenant, entries[0].Name)
				if err != nil || string(value) != want {
					t.Fatal("tenant vault did not resolve the expected fixture")
				}
				if err := eng.store.AuthView(ctx, func(sc store.AuthScope) error {
					rows, _, err := sc.Secrets().List(ctx, model.Query{Filters: []model.Filter{{Column: "scope", Op: model.OpEq, Value: tenant.String()}}})
					if err != nil {
						return err
					}
					if len(rows) != 1 || !strings.HasPrefix(rows[0].ValueSealed, secretStoreSealPrefix) || strings.Contains(rows[0].ValueSealed, want) {
						t.Fatal("vault persisted an unsealed value")
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				return entries[0].Name
			}
			first := checkVault("wire-secret-one")
			body["secrets"] = map[string]string{"api_key": "wire-refused-secret"}
			request("POST", "/v1/m/sourcescope/workspace-connectors", body, http.StatusConflict)
			if checkVault("wire-secret-one") != first {
				t.Fatal("refused create changed an existing secret")
			}
			request("PUT", "/v1/m/sourcescope/workspace-connectors/"+id, map[string]any{
				"enabled": true, "secrets": map[string]string{"api_key": "wire-secret-two"},
			}, http.StatusOK)
			if checkVault("wire-secret-two") == first {
				t.Fatal("secret update reused the previous reference")
			}
			request("PUT", "/v1/m/sourcescope/workspace-connectors/"+id, map[string]any{
				"enabled": true, "secrets": map[string]string{"api_key": ""},
			}, http.StatusOK)
			retained := checkVault("wire-secret-two")
			// Historical reference-only rows permitted padded locators. The new
			// normalization must preserve their resolved secret on the next write.
			if err := eng.store.Mutate(ctx, tenant, func(sc store.Scope) error {
				repo, err := sc.Ext("sourcescope.workspace_connector")
				if err != nil {
					return err
				}
				rec, err := repo.Get(ctx, model.ID(id))
				if err != nil {
					return err
				}
				refs, _ := json.Marshal(map[string]string{"api_key": "store: " + retained + " "})
				rec["secrets_ref"] = string(refs)
				_, err = repo.Update(ctx, rec)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			request("PUT", "/v1/m/sourcescope/workspace-connectors/"+id, map[string]any{
				"enabled": true, "secrets": map[string]string{"token": "store: " + retained + " "},
			}, http.StatusOK)
			if checkVault("wire-secret-two") != retained {
				t.Fatal("moving a reference to another field deleted the live secret")
			}
			code, other, _ := doDemoViewJSON(t, eng.api.Handler(), "POST", "/v1/system/orgs", token, "", map[string]any{"name": "Other", "slug": "wire-other"})
			if code != http.StatusCreated {
				t.Fatalf("other org = %d", code)
			}
			foreign := model.TenantID(other["tenant_id"].(string))
			if _, err := vault.Resolve(ctx, foreign, checkVault("wire-secret-two")); err == nil {
				t.Fatal("foreign tenant opened workspace secret")
			}
			code, _, _ = doDemoViewJSON(t, eng.api.Handler(), "POST", "/v1/m/sourcescope/workspace-connectors", token, foreign.String(), body)
			if code != http.StatusCreated {
				t.Fatalf("same connector in foreign tenant = %d", code)
			}
			request("DELETE", "/v1/m/sourcescope/workspace-connectors/"+id, nil, http.StatusNoContent)
			if entries, err := vault.List(ctx, tenant); err != nil || len(entries) != 0 {
				t.Fatal("connector deletion left its workspace secrets")
			}
			if entries, err := vault.List(ctx, foreign); err != nil || len(entries) != 1 {
				t.Fatal("connector deletion changed another tenant's secrets")
			}
		})
	}
}

func TestWorkspaceConnectorInlineSecretsUnavailable(t *testing.T) {
	t.Setenv(secretStoreKeyEnv, "invalid-fixture-key")
	eng, token, tenant := wireBoot(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, t.TempDir())
	body := map[string]any{"name": "docs", "kind": "confluence", "workspace_ref": "default",
		"secrets": map[string]string{"api_key": "wire-secret-one"}}
	code, _, raw := doDemoViewJSON(t, eng.api.Handler(), "POST", "/v1/m/sourcescope/workspace-connectors", token, tenant.String(), body)
	if code != http.StatusBadRequest || !strings.Contains(raw, "workspace secret sealer") || strings.Contains(raw, "wire-secret-one") {
		t.Fatalf("unavailable sealer refusal = %d", code)
	}
	body["secrets"] = map[string]string{"api_key": "vault:docs#api_key"}
	if code, _, _ := doDemoViewJSON(t, eng.api.Handler(), "POST", "/v1/m/sourcescope/workspace-connectors", token, tenant.String(), body); code != http.StatusCreated {
		t.Fatalf("reference-only connector = %d", code)
	}
}

// Fail exactly the read following a successful vault write, after the durable
// row exists. The adapter must compensate even though Put returned no reference.
type workspaceVaultReadFailure struct {
	store.Store
	armed, failRead bool
}

func (s *workspaceVaultReadFailure) AuthMutate(ctx context.Context, fn func(store.AuthScope) error) error {
	err := s.Store.AuthMutate(ctx, fn)
	if err == nil && s.armed {
		s.armed, s.failRead = false, true
	}
	return err
}

func (s *workspaceVaultReadFailure) AuthView(ctx context.Context, fn func(store.AuthScope) error) error {
	if s.failRead {
		s.failRead = false
		return errors.New("fixture vault metadata read failed")
	}
	return s.Store.AuthView(ctx, fn)
}

func TestWorkspaceConnectorVaultCleanupBoundaries(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	eng, token, tenant := wireBoot(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, dir)
	actor, err := eng.authr.Authenticate(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := newSecretSealer(dir, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	fault := &workspaceVaultReadFailure{Store: eng.store, armed: true}
	adapter := &workspaceConnectorSecrets{vault: auth.NewSecretStore(fault, sealer)}
	if _, err := adapter.SealWorkspaceSecret(ctx, tenant, "eng", "docs", "key", "unpublished-secret", actor); err == nil {
		t.Fatal("post-write vault read failure was not returned")
	}
	if entries, err := eng.secretStore.List(ctx, tenant); err != nil || len(entries) != 0 {
		t.Fatal("failed Put left an unpublished secret")
	}
	owned, err := adapter.SealWorkspaceSecret(ctx, tenant, "eng", "docs", "key", "eng-secret", actor)
	if err != nil {
		t.Fatal(err)
	}
	sibling, err := adapter.SealWorkspaceSecret(ctx, tenant, "ops", "docs", "key", "ops-secret", actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.secretStore.Put(ctx, actor, tenant, "operator-owned", "external-secret", "Fixture"); err != nil {
		t.Fatal(err)
	}
	if err := adapter.DeleteWorkspaceSecrets(ctx, tenant, "eng", "docs", map[string]string{
		"owned": owned, "sibling": sibling, "external": "store:operator-owned",
	}, actor); err != nil {
		t.Fatal(err)
	}
	entries, err := eng.secretStore.List(ctx, tenant)
	if err != nil || len(entries) != 2 {
		t.Fatal("cleanup crossed a workspace or external-secret boundary")
	}
	for _, entry := range entries {
		if _, err := eng.secretStore.Resolve(ctx, tenant, entry.Name); err != nil {
			t.Fatal("cleanup broke a retained reference")
		}
	}
}

type workspaceBlockingSealer struct {
	sourcescope.WorkspaceConnectorSealer
	arrived chan struct{}
	resume  chan struct{}
}

func (s *workspaceBlockingSealer) SealWorkspaceSecret(ctx context.Context, tenant model.TenantID, workspace, connector, field, value string, actor auth.Principal) (string, error) {
	ref, err := s.WorkspaceConnectorSealer.SealWorkspaceSecret(ctx, tenant, workspace, connector, field, value, actor)
	if err == nil && value == "losing-update" {
		close(s.arrived)
		select {
		case <-s.resume:
		case <-ctx.Done():
			return ref, ctx.Err()
		}
	}
	return ref, err
}

func workspaceTestAPI(t *testing.T, eng *engine, st store.Store, sealer sourcescope.WorkspaceConnectorSealer) http.Handler {
	t.Helper()
	srv, err := api.New(api.Options{Store: st, Authenticator: eng.authr, Authorizer: eng.authz,
		Signer: eng.signer, SetupToken: eng.setupTok, Logger: quietLog(), PrincipalEvidenceProducer: eng.authr,
		Modules: []api.Module{sourcescope.New(sourcescope.WithWorkspaceSealer(sealer))}})
	if err != nil {
		t.Fatal(err)
	}
	return srv.Handler()
}

func TestWorkspaceConnectorConcurrentInlineUpdate(t *testing.T) {
	for _, cfg := range wireBackends(t) {
		t.Run(string(cfg.Engine), func(t *testing.T) {
			eng, token, tenant := wireBoot(t, cfg, t.TempDir())
			block := &workspaceBlockingSealer{WorkspaceConnectorSealer: &workspaceConnectorSecrets{vault: eng.secretStore},
				arrived: make(chan struct{}), resume: make(chan struct{})}
			var release sync.Once
			defer release.Do(func() { close(block.resume) })
			h := workspaceTestAPI(t, eng, eng.store, block)
			code, out, _ := doDemoViewJSON(t, h, "POST", "/v1/m/sourcescope/workspace-connectors", token, tenant.String(), map[string]any{
				"name": "docs", "kind": "confluence", "workspace_ref": "default", "enabled": true,
				"secrets": map[string]string{"key": "initial-secret"},
			})
			if code != http.StatusCreated {
				t.Fatalf("create = %d", code)
			}
			path := "/v1/m/sourcescope/workspace-connectors/" + out["id"].(string)
			result := make(chan int, 1)
			go func() {
				r := httptest.NewRequest("PUT", path, strings.NewReader(`{"enabled":true,"secrets":{"key":"losing-update"}}`))
				r.Header.Set("Authorization", "Bearer "+token)
				r.Header.Set("X-Olivares-Tenant", tenant.String())
				r.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				result <- w.Code
			}()
			select {
			case <-block.arrived:
			case <-time.After(5 * time.Second):
				t.Fatal("competing write never reached sealing")
			}
			code, _, _ = doDemoViewJSON(t, h, "PUT", path, token, tenant.String(), map[string]any{
				"enabled": true, "secrets": map[string]string{"key": "winning-update"},
			})
			if code != http.StatusOK {
				t.Fatalf("winning update = %d", code)
			}
			release.Do(func() { close(block.resume) })
			select {
			case code = <-result:
				if code != http.StatusConflict {
					t.Fatalf("losing update = %d", code)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("competing write did not finish")
			}
			entries, err := eng.secretStore.List(context.Background(), tenant)
			if err != nil || len(entries) != 1 {
				t.Fatal("competing writes left obsolete or losing credentials")
			}
			value, err := eng.secretStore.Resolve(context.Background(), tenant, entries[0].Name)
			if err != nil || string(value) != "winning-update" {
				t.Fatal("losing write changed the winning credential")
			}
		})
	}
}

type workspaceUnknownCommit struct {
	store.Store
	armed bool
}

func (s *workspaceUnknownCommit) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	err := s.Store.Mutate(ctx, tenant, fn)
	if err == nil && s.armed {
		s.armed = false
		return store.ErrCommitOutcomeUnknown
	}
	return err
}

func TestWorkspaceConnectorUnknownCommitKeepsPublishedSecret(t *testing.T) {
	eng, token, tenant := wireBoot(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, t.TempDir())
	fault := &workspaceUnknownCommit{Store: eng.store}
	h := workspaceTestAPI(t, eng, fault, &workspaceConnectorSecrets{vault: eng.secretStore})
	fault.armed = true
	code, _, _ := doDemoViewJSON(t, h, "POST", "/v1/m/sourcescope/workspace-connectors", token, tenant.String(), map[string]any{
		"name": "docs", "kind": "confluence", "workspace_ref": "default", "enabled": true,
		"secrets": map[string]string{"key": "committed-secret"},
	})
	if code < 500 {
		t.Fatalf("unknown commit = %d", code)
	}
	code, out, _ := doDemoViewJSON(t, h, "GET", "/v1/m/sourcescope/workspace-connectors", token, tenant.String(), nil)
	if code != http.StatusOK || len(out["items"].([]any)) != 1 {
		t.Fatal("fixture did not commit the connector")
	}
	entries, err := eng.secretStore.List(context.Background(), tenant)
	if err != nil || len(entries) != 1 {
		t.Fatal("unknown commit deleted a possibly published reference")
	}
	value, err := eng.secretStore.Resolve(context.Background(), tenant, entries[0].Name)
	if err != nil || string(value) != "committed-secret" {
		t.Fatal("committed connector lost its credential")
	}
}
