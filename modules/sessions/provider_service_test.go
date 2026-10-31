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
	"github.com/olivaresai/olivares/core/store"
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

// serviceProbe answers service probes the way the DeepSeek connector does.
type serviceProbe struct{ *fakeProbe }

func (p serviceProbe) ProbeService(ctx context.Context, req ProviderProbeRequest, _ string) (ProviderProbeResult, error) {
	return p.Probe(ctx, req)
}

// A governed pin names the credential in place at a record version. A connection test,
// a rename or a new default model leave that credential in place, so the pin keeps
// working; a new key replaces it, so the old pin is refused and a new one works.
func TestDeepSeekPinSurvivesWritesThatKeepItsCredential(t *testing.T) {
	m, st, tenant, _, _ := providerHarness(t, WithProviderProbe(serviceProbe{&fakeProbe{result: ProviderProbeResult{Models: []string{"deepseek-chat"}}}}))
	defer st.Close()
	ctx := context.Background()
	rec := mustCreateRecord(t, m, tenant, CreateProviderRecordInput{
		Kind: ProviderKindOpenAICompatible, Service: "deepseek", DisplayName: "DeepSeek", APIKey: testProviderKey,
	})
	pin := rec.Version
	resolves := func(step string, version int64, want string) {
		t.Helper()
		key, err := m.ResolveProviderCredential(ctx, tenant, rec.Ref, version, "deepseek", modelprovider.DeepSeekChatURL)
		if want == "" {
			if err == nil || len(key) != 0 {
				t.Fatalf("%s: pin %d still resolved a credential", step, version)
			}
			return
		}
		if err != nil || string(key) != want {
			t.Fatalf("%s: pin %d = %v, want the registered key", step, version, err)
		}
	}
	if _, err := m.TestProviderRecord(ctx, tenant, rec.Ref); err != nil {
		t.Fatal(err)
	}
	resolves("Test connection", pin, testProviderKey)
	name, chosen := "DeepSeek main", "deepseek-chat"
	if _, err := m.PatchProviderRecord(ctx, tenant, rec.Ref, ProviderRecordPatch{DisplayName: &name}); err != nil {
		t.Fatal(err)
	}
	resolves("rename", pin, testProviderKey)
	current, err := m.PatchProviderRecord(ctx, tenant, rec.Ref, ProviderRecordPatch{DefaultModel: &chosen})
	if err != nil {
		t.Fatal(err)
	}
	resolves("default model", pin, testProviderKey)
	resolves("a pin newer than the record", current.Version+1, "")
	wrong := "https://other.example.com"
	if _, err := m.PatchProviderRecord(ctx, tenant, rec.Ref, ProviderRecordPatch{BaseURL: &wrong}); err == nil {
		t.Fatal("managed service re-endpoint accepted")
	}
	resolves("refused re-endpoint", pin, testProviderKey)

	const rotated = "sk-rotated-fixture-key-0002"
	key := rotated
	after, err := m.PatchProviderRecord(ctx, tenant, rec.Ref, ProviderRecordPatch{APIKey: &key, Actor: testActor()})
	if err != nil {
		t.Fatal(err)
	}
	resolves("key rotation, old pin", pin, "")
	resolves("key rotation, pin before the rotation", current.Version, "")
	resolves("key rotation, new pin", after.Version, rotated)
	if _, err := m.TestProviderRecord(ctx, tenant, rec.Ref); err != nil {
		t.Fatal(err)
	}
	resolves("Test connection after rotation, new pin", after.Version, rotated)
	resolves("Test connection after rotation, old pin", pin, "")
}

// A row written before the credential version was recorded holds only its current
// version, as before. Its next write records that version, so a pin taken then keeps
// working across a connection test, and an older pin still refuses.
func TestDeepSeekPinOnARowFromBeforeTheCredentialVersion(t *testing.T) {
	m, st, tenant, _, _ := providerHarness(t, WithProviderProbe(serviceProbe{&fakeProbe{result: ProviderProbeResult{Models: []string{"deepseek-chat"}}}}))
	defer st.Close()
	ctx := context.Background()
	rec := mustCreateRecord(t, m, tenant, CreateProviderRecordInput{
		Kind: ProviderKindOpenAICompatible, Service: "deepseek", DisplayName: "DeepSeek", APIKey: testProviderKey,
	})
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		row, err := findProviderRecordRec(ctx, sc, rec.Ref)
		if err != nil {
			return err
		}
		row[colPRCredentialVersion] = nil
		repo, err := sc.Ext(providerRecordKind)
		if err != nil {
			return err
		}
		_, err = repo.Update(ctx, row)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	legacy, err := m.GetProviderRecord(ctx, tenant, rec.Ref)
	if err != nil || legacy.CredentialVersion != 0 || legacy.Version != rec.Version+1 {
		t.Fatalf("row from before the column = %+v %v", legacy, err)
	}
	holds := func(step string, version int64, want bool) {
		t.Helper()
		key, err := m.ResolveProviderCredential(ctx, tenant, rec.Ref, version, "deepseek", modelprovider.DeepSeekChatURL)
		if got := err == nil && string(key) == testProviderKey; got != want {
			t.Fatalf("%s: pin %d resolved=%v (%v), want %v", step, version, got, err, want)
		}
	}
	holds("before any write, current version", legacy.Version, true)
	holds("before any write, older version", rec.Version, false)
	if _, err := m.TestProviderRecord(ctx, tenant, rec.Ref); err != nil {
		t.Fatal(err)
	}
	holds("after a connection test, the version it replaced", legacy.Version, true)
	holds("after a connection test, older version", rec.Version, false)
}
