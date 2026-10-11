// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/models"
)

// Exercise the registered discovery-to-routing join through the official API and
// concrete executor. Metadata GETs are measured separately from inference POSTs.
func TestDeepSeekAvailabilityRoutesOnlyFreshRegisteredObservation(t *testing.T) {
	backends := []store.Engine{store.EngineSQLite}
	if enginetest.PostgresAvailable(t) {
		backends = append(backends, store.EnginePostgres)
	} else {
		t.Log("PostgreSQL pending its configured runtime")
	}
	for _, backend := range backends {
		t.Run(string(backend), func(t *testing.T) {
			for _, scenario := range []string{"fresh", "service_preference", "missing", "failed", "stale", "other_model", "other_registration", "rotated_fresh", "changed_fresh", "revoked", "other_service_preference"} {
				t.Run(scenario, func(t *testing.T) {
					cfg := store.Config{Engine: backend, DSN: ":memory:"}
					if backend == store.EnginePostgres {
						d := enginetest.IsolatedPostgres(t)
						cfg.DSN, cfg.OwnerDSN, cfg.AdminDSN = d.App, d.Owner, d.Admin
					}
					c := newChatComposition(t, chatCompositionOptions{deepseek: true, backend: &cfg})
					ctx := context.Background()
					if err := c.store.View(ctx, c.tenant, func(sc store.Scope) error {
						providers, _, err := sc.Providers().List(ctx, model.Query{Limit: 1})
						if err != nil {
							return err
						}
						inventory, _, err := sc.Models().List(ctx, model.Query{Limit: 1})
						if len(providers) != 0 || len(inventory) != 0 {
							return errors.New("DeepSeek fixture seeded legacy core inventory")
						}
						return err
					}); err != nil {
						t.Fatal(err)
					}
					wantResolved, wantStatus := false, http.StatusUnprocessableEntity
					switch scenario {
					case "fresh", "service_preference":
						wantResolved, wantStatus = true, http.StatusOK
					case "missing", "failed", "stale", "other_model":
						changeDeepSeekAvailability(c, scenario)
					case "other_registration":
						other := c.do("POST", providersPath, c.admin, map[string]any{"kind": "openai_compatible", "service": "deepseek", "display_name": "Other DeepSeek", "api_key": deepseekFixtureKey})
						if other.code != http.StatusCreated || other.body["provider_ref"] == c.profile.ProviderRef {
							t.Fatalf("other registration = %d", other.code)
						}
						changeDeepSeekAvailability(c, "missing")
						if err := c.modelsModule.RefreshAvailability(ctx, c.tenant); err != nil {
							t.Fatal(err)
						}
						items, err := c.modelsModule.AvailableModels(ctx, c.tenant)
						if err != nil {
							t.Fatal(err)
						}
						otherFresh := false
						for _, item := range items {
							if item.ProviderRef == other.body["provider_ref"] && item.State == "fresh" && len(item.Models) == 1 && item.Models[0].ID == c.profile.ModelRef {
								otherFresh = true
							}
						}
						if !otherFresh {
							t.Fatal("other registered model was not actually discovered")
						}
					case "rotated_fresh", "changed_fresh":
						patch := map[string]any{"display_name": "Renamed DeepSeek"}
						if scenario == "rotated_fresh" {
							patch = map[string]any{"api_key": deepseekFixtureKey}
						}
						changed := c.do("PATCH", providersPath+"/"+c.profile.ProviderRef, c.admin, patch)
						if changed.code != http.StatusOK {
							t.Fatalf("changed registration = %d", changed.code)
						}
						beforeDiscovery, beforeDiscoveryOpens := c.transport.count(), c.providerVault.opens
						if err := c.modelsModule.RefreshAvailability(ctx, c.tenant); err != nil {
							t.Fatal(err)
						}
						discovery := c.transport.calls()[beforeDiscovery:]
						if len(discovery) != 1 || discovery[0].method != http.MethodGet || discovery[0].url != modelprovider.DeepSeekModelsURL || c.providerVault.opens != beforeDiscoveryOpens+1 {
							t.Fatal("changed registration was not rediscovered through the sealed metadata path")
						}
						items, err := c.modelsModule.AvailableModels(ctx, c.tenant)
						if err != nil || len(items) != 1 || items[0].State != "fresh" || items[0].ProviderRef != c.profile.ProviderRef {
							t.Fatal("new registration snapshot is not fresh")
						}
						// The selector cannot read the private credential pin. The existing
						// leaf resolver must refuse it even after a fresh new observation.
						wantResolved, wantStatus = true, http.StatusServiceUnavailable
					case "revoked":
						if revoked := c.do("POST", providersPath+"/"+c.profile.ProviderRef+"/revoke", c.admin, map[string]any{}); revoked.code != http.StatusOK {
							t.Fatalf("revoke = %d", revoked.code)
						}
					}
					if scenario == "service_preference" || scenario == "other_service_preference" {
						preference := "deepseek"
						if scenario == "other_service_preference" {
							preference = "anthropic"
						}
						updated := c.do("PUT", "/v1/m/models/routing-policies/"+c.policyID, c.admin, map[string]any{
							"name": "composition", "enabled": true, "strategy": "pinned", "pinned_model": c.profile.ModelRef,
							"execution_profile_ref": c.profile.Ref, "execution_profile_revision": c.profile.Revision, "preferred_providers": []string{preference},
						})
						if updated.code != http.StatusOK {
							t.Fatalf("provider preference = %d", updated.code)
						}
					}
					if !wantResolved {
						// Reproduce the real unrelated-inventory fallback, rather than
						// making an unresolved assertion vacuous against an empty catalog.
						if err := c.store.Mutate(ctx, c.tenant, func(sc store.Scope) error {
							provider, err := sc.Providers().Create(ctx, model.Provider{Name: "anthropic", Kind: "anthropic", Status: model.StatusActive})
							if err != nil {
								return err
							}
							_, err = sc.Models().Create(ctx, model.Model{Name: c.profile.ModelRef, ProviderID: provider.ID, Status: model.StatusActive})
							return err
						}); err != nil {
							t.Fatal(err)
						}
					}
					beforeCalls, beforeOpens := c.transport.count(), c.providerVault.opens
					preview := c.do("POST", "/v1/m/models/routing-policies/"+c.policyID+"/resolve", c.admin, map[string]any{})
					if preview.code != http.StatusOK || preview.body["resolved"] != wantResolved || c.transport.count() != beforeCalls || c.providerVault.opens != beforeOpens {
						t.Fatalf("preview = %d, resolved=%v; preview must not probe or open a credential", preview.code, preview.body["resolved"])
					}
					if wantResolved {
						primary, _ := preview.body["primary"].(map[string]any)
						if primary["provider_ref"] != c.profile.ProviderRef || primary["model_ref"] != c.profile.ModelRef {
							t.Fatal("preview substituted a service name, registration or model")
						}
					}
					result := c.execute(map[string]any{"input": "hello", "max_tokens": 64})
					if result.code != wantStatus {
						t.Fatalf("execute = %d, want %d; error=%s", result.code, wantStatus, result.errorCode())
					}
					calls := c.transport.calls()[beforeCalls:]
					if wantStatus == http.StatusOK {
						if len(calls) != 1 || calls[0].method != http.MethodPost || calls[0].url != modelprovider.DeepSeekChatURL || c.providerVault.opens != beforeOpens+1 {
							t.Fatal("fresh registered turn did not dispatch exactly once")
						}
					} else if len(calls) != 0 || c.providerVault.opens != beforeOpens {
						t.Fatal("refused availability or private version reached a credential or dispatch")
					}
					if wantStatus == http.StatusServiceUnavailable {
						if result.errorCode() != models.ChatErrCredentialUnavailable || len(c.records(chatIntentAction)) != 1 {
							t.Fatal("changed private credential pin lost its exact-version refusal or authorized attempt")
						}
						outcome := c.outcome()
						if outcome.meta["dispatch_state"] != models.ChatDispatchNotAttempted || outcome.meta["completion_state"] != models.ChatCompletionNotSent || outcome.meta["usage_status"] != models.ChatUsageUnknown {
							t.Fatal("credential refusal was not retained as an authorized attempt that was never sent")
						}
					} else if wantStatus != http.StatusOK && len(c.records(chatIntentAction))+len(c.records(chatOutcomeAction)) != 0 {
						t.Fatal("unresolved availability recorded an authorized inference attempt")
					}
					for _, action := range []string{chatIntentAction, chatOutcomeAction} {
						for _, retained := range c.records(action) {
							if strings.Contains(retained.raw, deepseekFixtureKey) || strings.Contains(retained.raw, "provider:") {
								t.Fatal("credential or private credential locator in canonical attempt evidence")
							}
						}
					}
				})
			}
		})
	}
}

// Change only a persisted availability observation, leaving the registered
// provider and private execution profile intact for absence/staleness cases.
func changeDeepSeekAvailability(c *chatComposition, scenario string) {
	c.t.Helper()
	ctx := context.Background()
	if err := c.store.Mutate(ctx, c.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(model.Kind("models.availability"))
		if err != nil {
			return err
		}
		rows, _, err := repo.List(ctx, model.Query{Limit: 100})
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.String("source_ref") != c.profile.ProviderRef {
				continue
			}
			if scenario == "missing" {
				return repo.Delete(ctx, model.ID(row.String(model.ColID)))
			}
			var snapshot models.Availability
			if err := json.Unmarshal([]byte(row.String("snapshot")), &snapshot); err != nil {
				return err
			}
			if scenario == "other_model" {
				snapshot.Models = []models.AvailableModel{{ID: "unrelated-model"}}
			} else {
				snapshot.State = scenario
			}
			encoded, err := json.Marshal(snapshot)
			if err != nil {
				return err
			}
			row["snapshot"] = string(encoded)
			_, err = repo.Update(ctx, row)
			return err
		}
		return errors.New("registered availability snapshot missing")
	}); err != nil {
		c.t.Fatal(err)
	}
}
