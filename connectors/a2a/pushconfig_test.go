// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// cardWithPush returns a base card whose signed capabilities advertise
// pushNotifications.
func cardWithPush(name string) map[string]any {
	c := baseCard(name)
	c["capabilities"] = map[string]any{"streaming": true, "pushNotifications": true}
	return c
}

func pushDelegator(t *testing.T, doer *stubDoer, jwks []byte) *Delegator {
	t.Helper()
	return NewDelegator(DelegatorConfig{
		Emit:      EmitConfig{TrustJWKS: jwks, Doer: doer},
		Allowlist: billingAllowlist(),
		Gate:      &fakeGate{status: StatusApproved},
	})
}

// TestCreatePushConfigCapabilityDeny: a card whose signed capabilities do NOT
// advertise pushNotifications refuses every config operation (§3.3.4, deny-closed
// client-side) — nothing is sent.
func TestCreatePushConfigCapabilityDeny(t *testing.T) {
	doer, jwks := verifiedDoer(t, "billing", `{}`) // baseCard: no pushNotifications
	d := pushDelegator(t, doer, jwks)
	_, err := d.CreateTaskPushNotificationConfig(context.Background(), PushConfigSpec{
		AgentName: "billing", AgentURL: "https://billing.example.com",
		TaskID: "t1", WebhookURL: "https://webhook.olivares.example/a2a/push",
	})
	var ce *CapabilityError
	if !errors.As(err, &ce) {
		t.Fatalf("push config without the capability must be a CapabilityError, got %v", err)
	}
	if doer.postCount != 0 {
		t.Fatalf("a capability deny must send NOTHING, got %d POSTs", doer.postCount)
	}
}

// TestCreatePushConfigAllowlistDeny: the agent must be on the operator allowlist.
func TestCreatePushConfigAllowlistDeny(t *testing.T) {
	doer, jwks := verifiedDoerCard(t, "billing", `{}`, cardWithPush("billing"))
	d := NewDelegator(DelegatorConfig{
		Emit:      EmitConfig{TrustJWKS: jwks, Doer: doer},
		Allowlist: NewAllowlist(nil), // deny-all
		Gate:      &fakeGate{status: StatusApproved},
	})
	_, err := d.CreateTaskPushNotificationConfig(context.Background(), PushConfigSpec{
		AgentName: "billing", AgentURL: "https://billing.example.com",
		TaskID: "t1", WebhookURL: "https://webhook.olivares.example/a2a/push",
	})
	var de *DenyError
	if !errors.As(err, &de) {
		t.Fatalf("an unlisted agent must be a DenyError, got %v", err)
	}
	if doer.postCount != 0 {
		t.Fatalf("an allowlist deny must send NOTHING, got %d POSTs", doer.postCount)
	}
}

// TestCreatePushConfigSuccess: the v1.0 flat TaskPushNotificationConfig is sent
// (url REQUIRED, singular authentication.scheme — v0.3's schemes[] is gone), the
// method name is the v1.0 rename, and the response's credentials are NOT echoed.
func TestCreatePushConfigSuccess(t *testing.T) {
	resp := `{"jsonrpc":"2.0","id":"x","result":{"id":"cfg-1","taskId":"t1",` +
		`"url":"https://webhook.olivares.example/a2a/push",` +
		`"authentication":{"scheme":"Bearer","credentials":"hook-secret"}}}`
	doer, jwks := verifiedDoerCard(t, "billing", resp, cardWithPush("billing"))
	d := pushDelegator(t, doer, jwks)
	cfg, err := d.CreateTaskPushNotificationConfig(context.Background(), PushConfigSpec{
		AgentName: "billing", AgentURL: "https://billing.example.com",
		TaskID: "t1", WebhookURL: "https://webhook.olivares.example/a2a/push",
		Scheme: "Bearer", Credentials: "hook-secret",
	})
	if err != nil {
		t.Fatalf("CreateTaskPushNotificationConfig: %v", err)
	}
	if cfg.ID != "cfg-1" || cfg.TaskID != "t1" || cfg.Scheme != "Bearer" {
		t.Errorf("config = %+v", cfg)
	}
	var env struct {
		Method string `json:"method"`
		Params struct {
			TaskID         string `json:"taskId"`
			URL            string `json:"url"`
			Authentication struct {
				Scheme      string `json:"scheme"`
				Credentials string `json:"credentials"`
			} `json:"authentication"`
		} `json:"params"`
	}
	if err := json.Unmarshal(doer.postBody, &env); err != nil {
		t.Fatalf("decode posted body: %v", err)
	}
	if env.Method != "CreateTaskPushNotificationConfig" {
		t.Errorf("method = %q, want CreateTaskPushNotificationConfig", env.Method)
	}
	if env.Params.URL == "" || env.Params.Authentication.Scheme != "Bearer" || env.Params.Authentication.Credentials != "hook-secret" {
		t.Errorf("wire config wrong: %+v", env.Params)
	}
	if strings.Contains(string(doer.postBody), `"schemes"`) {
		t.Errorf("the v0.3 schemes[] array must not be sent: %s", doer.postBody)
	}
}

// TestCreatePushConfigRequiresHTTPSWebhook: this plane never registers a clear-text
// webhook (§13.2 SHOULD https — our floor).
func TestCreatePushConfigRequiresHTTPSWebhook(t *testing.T) {
	doer, jwks := verifiedDoerCard(t, "billing", `{}`, cardWithPush("billing"))
	d := pushDelegator(t, doer, jwks)
	_, err := d.CreateTaskPushNotificationConfig(context.Background(), PushConfigSpec{
		AgentName: "billing", AgentURL: "https://billing.example.com",
		TaskID: "t1", WebhookURL: "http://webhook.olivares.example/a2a/push",
	})
	if err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("a non-HTTPS webhook must be refused, got %v", err)
	}
	if doer.postCount != 0 {
		t.Fatalf("nothing may be sent for a refused webhook url, got %d POSTs", doer.postCount)
	}
}

// TestListAndDeletePushConfigs: List uses the PLURAL v1.0 method name (§9.4.7) with
// pageToken pagination; Delete tolerates the google.protobuf.Empty result.
func TestListAndDeletePushConfigs(t *testing.T) {
	listResp := `{"jsonrpc":"2.0","id":"x","result":{"configs":[` +
		`{"id":"cfg-1","taskId":"t1","url":"https://w.example/p","authentication":{"scheme":"Bearer"}}],` +
		`"nextPageToken":""}}`
	doer, jwks := verifiedDoerCard(t, "billing", listResp, cardWithPush("billing"))
	d := pushDelegator(t, doer, jwks)
	ref := TaskRef{AgentName: "billing", AgentURL: "https://billing.example.com", TaskID: "t1"}
	page, err := d.ListTaskPushNotificationConfigs(context.Background(), ref, "", 10)
	if err != nil {
		t.Fatalf("ListTaskPushNotificationConfigs: %v", err)
	}
	if len(page.Configs) != 1 || page.Configs[0].ID != "cfg-1" || page.NextPageToken != "" {
		t.Errorf("page = %+v", page)
	}
	if !strings.Contains(string(doer.postBody), `"ListTaskPushNotificationConfigs"`) {
		t.Errorf("List must use the plural v1.0 method name, got %s", doer.postBody)
	}

	doer.rpcBytes = []byte(`{"jsonrpc":"2.0","id":"x","result":{}}`)
	if err := d.DeleteTaskPushNotificationConfig(context.Background(), ref, "cfg-1"); err != nil {
		t.Fatalf("DeleteTaskPushNotificationConfig: %v", err)
	}
	var env struct {
		Method string `json:"method"`
		Params struct {
			TaskID string `json:"taskId"`
			ID     string `json:"id"`
		} `json:"params"`
	}
	if err := json.Unmarshal(doer.postBody, &env); err != nil {
		t.Fatalf("decode delete body: %v", err)
	}
	if env.Method != "DeleteTaskPushNotificationConfig" || env.Params.TaskID != "t1" || env.Params.ID != "cfg-1" {
		t.Errorf("delete wire shape wrong: %+v", env)
	}
}
