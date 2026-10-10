// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/models"
)

// Qualify the shared module operation against the concrete adapter, registered
// product provider and sealed account credential. The stand-in is local TLS;
// only the fixture redirects the exact service endpoint to it.
func TestModelsTextOperationConcreteProviderParity(t *testing.T) {
	for _, scenario := range []string{"success", "unsupported_operation", "changed_account", "upstream_error"} {
		t.Run(scenario, func(t *testing.T) {
			c := newChatComposition(t, chatCompositionOptions{deepseek: true})
			in := models.TextExecutionInput{Input: "local stand-in turn", MaxTokens: 32, SessionRef: "attribution-only"}
			want := http.StatusOK
			switch scenario {
			case "unsupported_operation":
				in.Operation = "embeddings.create"
				want = 422
			case "changed_account":
				r := c.do("PATCH", providersPath+"/"+c.profile.ProviderRef, c.admin, map[string]any{"api_key": deepseekFixtureKey})
				if r.code != 200 {
					t.Fatal("rotate product provider credential")
				}
				if err := c.modelsModule.RefreshAvailability(t.Context(), c.tenant); err != nil {
					t.Fatal(err)
				}
				want = 503
			case "upstream_error":
				c.upstream.status = 502
				c.upstream.body = "sensitive vendor error body"
				want = 502
			}
			p, err := auth.NewAuthenticator(c.store, nil).Authenticate(t.Context(), c.admin)
			if err != nil {
				t.Fatal(err)
			}
			ref, ok := p.Ref()
			if !ok {
				t.Fatal("credential reference missing")
			}
			ctx, mc, err := c.srv.ModuleOperationContext(t.Context(), ref, c.tenant, models.Namespace)
			if err != nil {
				t.Fatal(err)
			}
			before, opens := c.transport.count(), c.providerVault.opens
			result, err := c.modelsModule.ExecuteText(ctx, mc, model.ID(c.policyID), in)
			if result.StatusCode != want || (err != nil) != (want >= 400) {
				t.Fatalf("operation=%d expected=%d error=%v", result.StatusCode, want, err)
			}
			if want >= 400 && result.Output != nil {
				t.Fatal("error released output")
			}
			if want == 200 && (result.Output == nil || result.Execution.BudgetAssurance != models.ChatBudgetAssuranceDevelopmentPrecheck) {
				t.Fatal("missing successful text/assurance")
			}
			opCalls, opOpens := c.transport.count()-before, c.providerVault.opens-opens
			if (scenario == "unsupported_operation" || scenario == "changed_account") && (opCalls != 0 || opOpens != 0) {
				t.Fatal("pre-dispatch refusal opened credential or reached provider")
			}
			encoded, e := json.Marshal(in)
			if e != nil {
				t.Fatal(e)
			}
			var body map[string]any
			if e = json.Unmarshal(encoded, &body); e != nil {
				t.Fatal(e)
			}
			wire := c.execute(body)
			if wire.code != result.StatusCode || c.transport.count()-before != 2*opCalls || c.providerVault.opens-opens != 2*opOpens {
				t.Fatal("concrete HTTP and module actuation diverged")
			}
			answer, e := json.Marshal(result)
			if e != nil {
				t.Fatal(e)
			}
			if strings.Contains(string(answer), deepseekFixtureKey) || strings.Contains(string(answer), "sensitive vendor error body") {
				t.Fatal("unsafe operation result")
			}
		})
	}
}
