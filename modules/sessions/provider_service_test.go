// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/core/model"
)

func TestDeepSeekProviderRegistrationAndCredentialUseStayScoped(t *testing.T) {
	m, st, tenant, _, _ := providerHarness(t)
	defer st.Close()
	rec := mustCreateRecord(t, m, tenant, CreateProviderRecordInput{
		Kind: ProviderKindOpenAICompatible, Service: "deepseek", DisplayName: "DeepSeek", APIKey: testProviderKey,
	})
	if rec.Service != "deepseek" || rec.BaseURL != modelprovider.DeepSeekBaseURL {
		t.Fatalf("wrong preset: %+v", rec)
	}
	ctx := context.Background()
	key, err := m.ResolveProviderCredential(ctx, tenant, rec.Ref, rec.Version, "deepseek", modelprovider.DeepSeekChatURL)
	if err != nil || string(key) != testProviderKey {
		t.Fatalf("registered credential unavailable: %v", err)
	}
	for _, tc := range []struct {
		tenant            model.TenantID
		version           int64
		service, endpoint string
	}{
		{model.NewTenantID(), rec.Version, "deepseek", modelprovider.DeepSeekChatURL},
		{tenant, rec.Version + 1, "deepseek", modelprovider.DeepSeekChatURL},
		{tenant, rec.Version, "", modelprovider.DeepSeekChatURL},
		{tenant, rec.Version, "deepseek", "https://api.deepseek.com/v1/chat/completions"},
	} {
		if key, err := m.ResolveProviderCredential(ctx, tc.tenant, rec.Ref, tc.version, tc.service, tc.endpoint); err == nil || len(key) != 0 {
			t.Fatal("mismatched credential use accepted")
		}
	}
	wrong := "https://other.example.com"
	if _, err := m.PatchProviderRecord(ctx, tenant, rec.Ref, ProviderRecordPatch{BaseURL: &wrong}); err == nil {
		t.Fatal("managed service re-endpoint accepted")
	}
	if _, err := m.RevokeProviderRecord(ctx, testActor(), tenant, rec.Ref); err != nil {
		t.Fatal(err)
	}
	if key, err := m.ResolveProviderCredential(ctx, tenant, rec.Ref, rec.Version, "deepseek", modelprovider.DeepSeekChatURL); err == nil || len(key) != 0 {
		t.Fatal("revoked credential used")
	}
}

func TestDeepSeekProviderResetKeepsTheServiceEndpoint(t *testing.T) {
	m, st, tenant, _, _ := providerHarness(t)
	defer st.Close()
	rec := mustCreateRecord(t, m, tenant, CreateProviderRecordInput{
		Kind: ProviderKindOpenAICompatible, Service: "deepseek", DisplayName: "DeepSeek", APIKey: testProviderKey,
	})
	empty := ""
	updated, err := m.PatchProviderRecord(context.Background(), tenant, rec.Ref, ProviderRecordPatch{BaseURL: &empty})
	if err != nil || updated.BaseURL != modelprovider.DeepSeekBaseURL {
		t.Fatalf("reset service endpoint = %q, %v", updated.BaseURL, err)
	}
}

func TestDeepSeekProviderMetadataPreservesUntaggedJSON(t *testing.T) {
	for _, service := range []string{"", "deepseek"} {
		body, err := json.Marshal(toProviderRecordDTO(ProviderRecord{Service: service, Version: 7}))
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(body, &fields); err != nil {
			t.Fatal(err)
		}
		_, hasService := fields["service"]
		_, hasVersion := fields["version"]
		if hasService != (service != "") || hasVersion != (service != "") {
			t.Fatalf("service %q changed optional metadata: %s", service, body)
		}
	}
}
