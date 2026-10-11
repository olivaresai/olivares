// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/eventbus"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
)

func TestAWSConnectorOnboardingGatherRestart(t *testing.T) {
	for _, name := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN"} {
		t.Setenv(name, "")
	}
	exerciseSourceActivation(t, "aws", func(base string) map[string]string {
		return map[string]string{"iam_endpoint": base, "cloudtrail_endpoint": base, "bedrock_endpoint": base, "enable_bedrock": "true", "account_id": "123456789012"}
	}, map[string]string{"access_key_id": "fixture-access-only", "secret_access_key": "fixture-secret-only"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Query().Get("Action") != "":
			if r.Method != http.MethodGet {
				t.Errorf("IAM method = %s", r.Method)
			}
			action := r.URL.Query().Get("Action")
			contents := ""
			switch action {
			case "ListRoles":
				contents = `<Roles><member><RoleName>reader</RoleName><Arn>arn:aws:iam::123456789012:role/reader</Arn></member></Roles>`
			case "ListUsers", "ListPolicies", "ListAttachedRolePolicies":
			default:
				t.Errorf("unexpected IAM action %q", action)
			}
			fmt.Fprintf(w, "<%sResponse><%sResult>%s</%sResult></%sResponse>", action, action, contents, action, action)
		case r.Header.Get("X-Amz-Target") == "com.amazonaws.cloudtrail.v20131101.CloudTrail_20131101.LookupEvents":
			if r.Method != http.MethodPost {
				t.Errorf("CloudTrail method = %s", r.Method)
			}
			fmt.Fprint(w, `{"Events":[{"CloudTrailEvent":"{\"eventSource\":\"ec2.amazonaws.com\",\"eventName\":\"DescribeInstances\",\"eventCategory\":\"Management\",\"readOnly\":true,\"userIdentity\":{\"type\":\"IAMUser\",\"arn\":\"arn:aws:iam::123456789012:user/reader\"}}"}]}`)
		case r.URL.Path == "/guardrails":
			fmt.Fprint(w, `{"guardrails":[]}`)
		case r.URL.Path == "/logging/modelinvocations":
			fmt.Fprint(w, `{}`)
		default:
			t.Errorf("unexpected local fixture request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}), []string{"arn:aws:iam::123456789012:role/reader", "ec2.amazonaws.com:DescribeInstances", "bedrock.guardrail", "bedrock.logging"})
}

// Exercise the public onboarding/roster interfaces with the real constructor,
// runtime and sealed SQLite stores. Only the external service is a local stand-in.
func exerciseSourceActivation(t *testing.T, kind string, settings func(string) map[string]string, secrets map[string]string, service http.Handler, want []string) {
	t.Helper()
	ctx := context.Background()
	catalog, err := (&sourceReconciler{}).ListConnectors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, info := range catalog {
		if info.Kind == kind {
			found = info.Transport == "in_process" && info.FieldsKnown && len(info.Fields) > 0
		}
	}
	if !found {
		t.Fatalf("%s is absent from the built-in onboarding catalog", kind)
	}
	var refuse atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if refuse.Load() {
			http.Error(w, "fixture refusal", http.StatusForbidden)
			return
		}
		service.ServeHTTP(w, r)
	}))
	defer server.Close()
	dir := t.TempDir()
	input := api.ConnectorOnboardInput{Name: "fixture-" + kind, Kind: kind, Tenant: "acme", Enabled: true, Config: settings(server.URL)}
	var savedID string
	var savedConfig map[string]string
	// The second pass closes and reopens the real database and runtime, as boot does.
	for phase := 0; phase < 2; phase++ {
		func() {
			bus := eventbus.NewInProc(eventbus.Options{})
			defer bus.Close()
			received := make(chan event.Event, 64)
			sub, err := bus.Subscribe(nil, func(_ context.Context, e event.Event) error { received <- e; return nil })
			if err != nil {
				t.Fatal(err)
			}
			defer sub.Unsubscribe()
			rt := runtime.New(runtime.Options{Logger: quietLog(), Bus: bus})
			st, err := coreengine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: filepath.Join(dir, "sources.db")}, rt.RegisterSchema)
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			if err := st.System(ctx, func(s store.SystemScope) error { _, err := s.EnsureSystemTenant(ctx); return err }); err != nil {
				t.Fatal(err)
			}
			if err := rt.Start(ctx); err != nil {
				t.Fatal(err)
			}
			defer rt.Stop(ctx)
			sealer, err := newSecretSealer(dir, os.Getenv)
			if err != nil {
				t.Fatal(err)
			}
			sealed := auth.NewSecretStore(st, sealer)
			sr := newSourceReconciler(rt, auth.NewSourceStore(st), newSecretResolver(sealed, os.Getenv, quietLog()), sealed, dir, nil, quietLog())
			if phase == 0 {
				if err := sr.TestConnector(ctx, recAdmin(), input); err == nil {
					t.Fatal("missing required credentials were accepted")
				}
				rows, err := sr.ListSources(ctx)
				if err != nil || len(rows) != 0 {
					t.Fatalf("connection test persisted a source: %v, %v", rows, err)
				}
				input.Secrets = secrets
				if err := sr.TestConnector(ctx, recAdmin(), input); err != nil {
					t.Fatalf("configured connection test: %v", err)
				}
				res, err := sr.PutConnector(ctx, recAdmin(), input)
				if err != nil || !res.Persisted || !res.Applied {
					t.Fatalf("onboard: %+v, %v", res, err)
				}
			} else {
				res, err := sr.ReloadSources(ctx, recAdmin())
				if err != nil || len(res.Rejected) != 0 || len(res.Added) != 1 {
					t.Fatalf("restart reload: %+v, %v", res, err)
				}
			}
			rows, err := sr.ListSources(ctx)
			if err != nil || len(rows) != 1 {
				t.Fatalf("roster: %+v, %v", rows, err)
			}
			row := rows[0]
			if row.Kind != kind || row.Tenant != "acme" || row.Component != "olivares."+kind {
				t.Fatalf("wrong source binding: %+v", row)
			}
			for field := range secrets {
				if row.Config[field] != "store:source/"+input.Name+"/"+field {
					t.Errorf("%s is not an owned sealed reference", field)
				}
			}
			if phase == 0 {
				savedID, savedConfig = row.ID, row.Config
			} else if row.ID != savedID || !reflect.DeepEqual(row.Config, savedConfig) {
				t.Fatal("source identity or configuration changed across restart")
			}
			awaitSourceObservations(t, received, input.Name, secrets, want)
			if phase == 1 {
				refuse.Store(true)
				// A changed interval rotates through the existing live-apply path.
				input.PollSeconds = 3600
				input.Secrets = nil // reuse the sealed values after restart
				input.Config = row.Config
				res, err := sr.PutConnector(ctx, recAdmin(), input)
				if err != nil || !res.Applied {
					t.Fatalf("rotate: %+v, %v", res, err)
				}
				awaitSourceObservations(t, received, input.Name, secrets, []string{"health"})
				input.Enabled = false
				res, err = sr.PutConnector(ctx, recAdmin(), input)
				if err != nil || !res.Applied || res.Action != "disabled" || rt.SourceIsRegistered(input.Name) {
					t.Fatalf("disable: %+v, %v", res, err)
				}
				rows, err = sr.ListSources(ctx)
				if err != nil || len(rows) != 1 || rows[0].ID != savedID || !reflect.DeepEqual(rows[0].Config, savedConfig) {
					t.Fatal("disable lost source data")
				}
			}
		}()
	}
}

func awaitSourceObservations(t *testing.T, received <-chan event.Event, source string, secrets map[string]string, want []string) {
	t.Helper()
	pending := append([]string(nil), want...)
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for len(pending) > 0 {
		select {
		case e := <-received:
			if e.Source != source || e.Tenant != "acme" {
				t.Fatalf("wrong observation binding: source=%q tenant=%q", e.Source, e.Tenant)
			}
			b, err := json.Marshal(e.Payload)
			if err != nil {
				t.Fatal(err)
			}
			for _, v := range secrets {
				if strings.Contains(string(b), v) {
					t.Fatal("observation exposed a fixture credential")
				}
			}
			for i := len(pending) - 1; i >= 0; i-- {
				if strings.Contains(string(b), pending[i]) {
					pending = append(pending[:i], pending[i+1:]...)
				}
			}
		case <-deadline.C:
			t.Fatalf("source %s did not deliver observations %v", source, pending)
		}
	}
}

func TestBedrockConnectorOnboardingGatherRestart(t *testing.T) {
	for _, name := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN"} {
		t.Setenv(name, "")
	}
	exerciseSourceActivation(t, "bedrock", func(base string) map[string]string {
		return map[string]string{"cloudwatch_logs_endpoint": base, "cost_explorer_endpoint": base, "bedrock_endpoint": base, "usage_log_group": "fixture-usage", "enable_cost": "true", "enable_guardrails": "true", "account_id": "123456789012"}
	}, map[string]string{"access_key_id": "fixture-access-only", "secret_access_key": "fixture-secret-only"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := r.Header.Get("X-Amz-Target")
		switch {
		case target == "Logs_20140328.FilterLogEvents":
			if r.Method != http.MethodPost {
				t.Errorf("CloudWatch method = %s", r.Method)
			}
			message := `{"schemaType":"ModelInvocationLog","modelId":"amazon.nova-lite-v1:0","input":{"inputTokenCount":42},"output":{"outputTokenCount":7}}`
			_ = json.NewEncoder(w).Encode(map[string]any{"events": []any{map[string]any{"timestamp": time.Now().UnixMilli(), "message": message}}})
		case target == "AWSInsightsIndexService.GetCostAndUsage":
			if r.Method != http.MethodPost {
				t.Errorf("Cost Explorer method = %s", r.Method)
			}
			fmt.Fprint(w, `{"ResultsByTime":[{"TimePeriod":{"Start":"2026-10-01","End":"2026-10-02"},"Groups":[{"Keys":["fixture-billed-input-tokens"],"Metrics":{"UnblendedCost":{"Amount":"1.25","Unit":"USD"}}}]}]}`)
		case r.URL.Path == "/guardrails":
			fmt.Fprint(w, `{"guardrails":[]}`)
		case r.URL.Path == "/logging/modelinvocations":
			fmt.Fprint(w, `{}`)
		default:
			t.Errorf("unexpected local fixture request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}), []string{"amazon.nova-lite-v1:0", "fixture-billed-input-tokens", "bedrock.guardrail", "bedrock.logging"})
}

func TestCloudflareAIGatewayConnectorOnboardingGatherRestart(t *testing.T) {
	exerciseSourceActivation(t, "cloudflare-ai-gateway", func(base string) map[string]string {
		return map[string]string{"api_base": base, "account_id": "fixture-account"}
	}, map[string]string{"api_token": "fixture-gateway-token-only"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("Cloudflare method = %s", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer fixture-gateway-token-only" {
			t.Error("Cloudflare request missing configured credential")
		}
		switch r.URL.Path {
		case "/accounts/fixture-account/ai-gateway/gateways":
			fmt.Fprint(w, `{"success":true,"result":[{"id":"fixture-gateway","name":"fixture"}]}`)
		case "/accounts/fixture-account/ai-gateway/gateways/fixture-gateway/logs":
			fmt.Fprintf(w, `{"success":true,"result":[{"id":"fixture-log","model":"fixture-gateway-model","provider":"anthropic","status_code":200,"tokens_in":42,"tokens_out":7,"cost":500,"created_at":%q,"metadata":{"workspace":"fixture-workspace"}}]}`, time.Now().UTC().Format(time.RFC3339))
		default:
			t.Errorf("unexpected local fixture request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}), []string{"fixture-gateway-model", "fixture-workspace"})
}
