// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/internal/oastest"
)

// Every row of the preset catalog registers at its own endpoint with no base_url
// typed, and a record keeps the row's ref in its immutable service column.
func TestProviderPresetRegistrationTakesTheRowEndpoint(t *testing.T) {
	m, st, tenant, _, _ := providerHarness(t)
	defer st.Close()
	for _, row := range modelprovider.Presets() {
		t.Run(row.Ref, func(t *testing.T) {
			rec := mustCreateRecord(t, m, tenant, CreateProviderRecordInput{
				Kind: row.Kind, Service: row.Ref, DisplayName: row.Title, APIKey: testProviderKey,
			})
			if rec.Service != row.Ref || rec.Kind != row.Kind || rec.BaseURL != row.BaseURL {
				t.Fatalf("record %+v does not carry row %+v", rec, row)
			}
			// The row's default model is a preference of the first successful key test,
			// not a value saved before anything was tested.
			if rec.DefaultModel == nil || *rec.DefaultModel != "" {
				t.Fatalf("a new record's default = %v, want pending the first test", rec.DefaultModel)
			}
		})
	}
}

// presetServiceProbe is a key test that records which service it was asked about.
type presetServiceProbe struct {
	fakeProbe
	services []string
}

func (p *presetServiceProbe) ProbeService(ctx context.Context, req ProviderProbeRequest, service string) (ProviderProbeResult, error) {
	p.services = append(p.services, service)
	return p.Probe(ctx, req)
}

// The first successful test saves the row's default when the vendor lists it, and
// leaves the default pending when it does not: a default the vendor does not list
// would refuse every launch.
func TestProviderPresetDefaultModelIsChosenByTheFirstTest(t *testing.T) {
	for _, tc := range []struct {
		name   string
		listed []string
		want   string
	}{
		{"vendor lists the row's default", []string{"deepseek-v4-pro", "deepseek-flash"}, "deepseek-flash"},
		{"the row's default beats the kind's preferred model", []string{"claude-opus-5-5", "deepseek-flash"}, "deepseek-flash"},
		{"vendor does not list it", []string{"deepseek-v4-pro"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probe := &presetServiceProbe{fakeProbe: fakeProbe{result: ProviderProbeResult{Models: tc.listed}}}
			m, st, tenant, _, _ := providerHarness(t, WithProviderProbe(probe))
			defer st.Close()
			rec := mustCreateRecord(t, m, tenant, CreateProviderRecordInput{
				Kind: ProviderKindAnthropic, Service: "deepseek-anthropic", DisplayName: "DeepSeek", APIKey: testProviderKey,
			})
			after, err := m.TestProviderRecord(context.Background(), tenant, rec.Ref)
			if err != nil || after.ProbeState != ProbeOK {
				t.Fatalf("test = %+v, %v", after, err)
			}
			if len(probe.services) != 1 || probe.services[0] != "deepseek-anthropic" {
				t.Fatalf("the key test was asked about services %v, want the preset's ref", probe.services)
			}
			if after.DefaultModel == nil || *after.DefaultModel != tc.want {
				t.Fatalf("default after the first test = %v, want %q", after.DefaultModel, tc.want)
			}
		})
	}
}

func TestProviderPresetDefaultModelYieldsToTheCallersChoice(t *testing.T) {
	probe := &presetServiceProbe{fakeProbe: fakeProbe{result: ProviderProbeResult{Models: []string{"deepseek-flash", "my-model"}}}}
	m, st, tenant, _, _ := providerHarness(t, WithProviderProbe(probe))
	defer st.Close()
	own, none := "my-model", ""
	for _, tc := range []struct {
		name   string
		choice *string
		want   *string
	}{{"explicit model", &own, &own}, {"explicit none", &none, nil}} {
		rec := mustCreateRecord(t, m, tenant, CreateProviderRecordInput{
			Kind: ProviderKindAnthropic, Service: "deepseek-anthropic", DisplayName: "DeepSeek " + tc.name,
			APIKey: testProviderKey, DefaultModel: tc.choice,
		})
		after, err := m.TestProviderRecord(context.Background(), tenant, rec.Ref)
		if err != nil || (after.DefaultModel == nil) != (tc.want == nil) || after.DefaultModel != nil && *after.DefaultModel != *tc.want {
			t.Fatalf("%s: default after the test = %v (%v), want %v", tc.name, after.DefaultModel, err, tc.want)
		}
	}
}

func TestProviderPresetRefusals(t *testing.T) {
	m, st, tenant, _, _ := providerHarness(t)
	defer st.Close()
	for _, tc := range []struct {
		name string
		in   CreateProviderRecordInput
		want string
	}{
		{"unknown ref lists the valid refs", CreateProviderRecordInput{Kind: ProviderKindAnthropic, Service: "not-a-vendor"},
			strings.Join(modelprovider.PresetRefs(), ", ")},
		{"kind of another row", CreateProviderRecordInput{Kind: ProviderKindOpenAICompatible, Service: "deepseek-anthropic"},
			"preset deepseek-anthropic has kind anthropic"},
		{"endpoint of another vendor", CreateProviderRecordInput{Kind: ProviderKindAnthropic, Service: "deepseek-anthropic", BaseURL: "https://other.example.com"},
			"documented endpoint"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.in.DisplayName, tc.in.APIKey, tc.in.Actor = "Refused "+tc.name, testProviderKey, testActor()
			_, err := m.CreateProviderRecord(context.Background(), tenant, tc.in)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want one naming %q", err, tc.want)
			}
		})
	}
}

// The refusal of an unknown ref repeats it back, so a huge ref must not come back whole.
func TestProviderPresetUnknownRefEchoIsBounded(t *testing.T) {
	m, st, tenant, _, _ := providerHarness(t)
	defer st.Close()
	huge := strings.Repeat("x", 1000)
	_, err := m.CreateProviderRecord(context.Background(), tenant, CreateProviderRecordInput{
		Kind: ProviderKindAnthropic, Service: huge, DisplayName: "Huge ref", APIKey: testProviderKey, Actor: testActor(),
	})
	if err == nil || !strings.Contains(err.Error(), strings.Repeat("x", maxPresetRefEcho)) {
		t.Fatalf("error = %v, want the unknown-preset refusal", err)
	}
	if strings.Contains(err.Error(), huge) {
		t.Fatalf("the refusal repeated all %d characters of the ref", len(huge))
	}
}

// retireService writes a service the table does not have into a stored record, as a
// later release that renames a row would leave behind; the engine refuses to create one.
func retireService(t *testing.T, m *Module, tenant model.TenantID, ref string) {
	t.Helper()
	ctx := context.Background()
	err := m.Data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(providerRecordKind)
		if err != nil {
			return err
		}
		rec, err := findProviderRecordRec(ctx, sc, ref)
		if err != nil {
			return err
		}
		rec["service"] = "retired-ref"
		_, err = repo.Update(ctx, rec)
		return err
	})
	if err != nil {
		t.Fatalf("writing the retired service: %v", err)
	}
}

// A stored service the table no longer has is not "no service": it must neither mint a
// native credential nor bind to a profile, so it fails closed at both gates.
func TestProviderPresetStoredRetiredServiceFailsClosed(t *testing.T) {
	t.Run("mint", func(t *testing.T) {
		vault := &nativeServiceVault{fakeVault: newFakeVault()}
		m, _, tenant, _ := newRuntimeHarness(t, WithProviderSecretVault(vault))
		record := mustCreateRecord(t, m, tenant, CreateProviderRecordInput{
			Kind: ProviderKindAnthropic, Service: "deepseek-anthropic", DisplayName: "Retired", APIKey: testProviderKey,
		})
		retireService(t, m, tenant, record.Ref)
		_, env, err := m.mintFromProviderRecord(context.Background(), tenant, providerDriverClaude, record.Ref)
		requireAPIOnlyNativeRefusal(t, err)
		if len(env) != 0 || vault.opens.Load() != 0 {
			t.Fatalf("a retired service opened the vault or produced an environment: opens=%d variables=%d", vault.opens.Load(), len(env))
		}
	})
	t.Run("profile", func(t *testing.T) {
		m, st, tenant, _, _ := providerHarness(t)
		defer st.Close()
		m.UseExecutionEnvironmentRef(testEnvRef)
		record := mustCreateRecord(t, m, tenant, CreateProviderRecordInput{
			Kind: ProviderKindAnthropic, Service: "deepseek-anthropic", DisplayName: "Retired", APIKey: testProviderKey,
		})
		retireService(t, m, tenant, record.Ref)
		configHome, userHome, _, _ := twoHomes(t)
		_, err := m.CreateProfile(context.Background(), tenant, CreateProfileInput{
			Driver: providerDriverClaude, ConfigHome: configHome, UserHome: userHome,
			AuthSource: AuthSourceManagedInjection, ProviderRecordRef: record.Ref,
		})
		requireAPIOnlyNativeRefusal(t, err)
	})
}

// Only the governed deepseek service is for API use. A preset record is an
// ordinary provider with its endpoint filled in, so a tool can be bound to it.
func TestProviderPresetRecordServesNativeSessions(t *testing.T) {
	vault := &nativeServiceVault{fakeVault: newFakeVault()}
	m, _, tenant, _ := newRuntimeHarness(t, WithProviderSecretVault(vault))
	row, _ := modelprovider.LookupPreset("deepseek-anthropic")
	rec := mustCreateRecord(t, m, tenant, CreateProviderRecordInput{
		Kind: row.Kind, Service: row.Ref, DisplayName: row.Title, APIKey: testProviderKey,
	})
	cred, env, err := m.mintFromProviderRecord(context.Background(), tenant, providerDriverClaude, rec.Ref)
	if err != nil || len(env) == 0 {
		t.Fatalf("a preset record refused a Claude Code launch: variables=%d error=%v", len(env), err)
	}
	if cred.bound.Endpoint != row.BaseURL {
		t.Fatalf("the launch is held to %q, want the row's %q", cred.bound.Endpoint, row.BaseURL)
	}
	governed := mustCreateRecord(t, m, tenant, CreateProviderRecordInput{
		Kind: ProviderKindOpenAICompatible, Service: modelprovider.ServiceDeepSeek, DisplayName: "API-only", APIKey: testProviderKey,
	})
	_, _, err = m.mintFromProviderRecord(context.Background(), tenant, providerDriverClaude, governed.Ref)
	requireAPIOnlyNativeRefusal(t, err)
}

func TestProviderPresetRecordBindsToAProfile(t *testing.T) {
	m, _, tenant, _, _ := providerHarness(t)
	row, _ := modelprovider.LookupPreset("zai-anthropic")
	rec := mustCreateRecord(t, m, tenant, CreateProviderRecordInput{
		Kind: row.Kind, Service: row.Ref, DisplayName: row.Title, APIKey: testProviderKey,
	})
	configHome, userHome, _, _ := twoHomes(t)
	if _, err := m.CreateProfile(context.Background(), tenant, CreateProfileInput{
		Driver: providerDriverClaude, ConfigHome: configHome, UserHome: userHome,
		AuthSource: AuthSourceManagedInjection, ProviderRecordRef: rec.Ref,
	}); err != nil {
		t.Fatalf("binding a Claude Code profile to a preset record: %v", err)
	}
}

func TestProviderPresetsRouteServesTheCatalog(t *testing.T) {
	m, st, tenant, _, _ := providerHarness(t)
	defer st.Close()
	got := newAcctAPI(m, st, tenant).call(http.MethodGet, "/provider-presets", nil)
	if got.code != http.StatusOK {
		t.Fatalf("GET /provider-presets = %d %s", got.code, got.raw)
	}
	var page struct {
		Items []modelprovider.Preset `json:"items"`
	}
	if err := json.Unmarshal([]byte(got.raw), &page); err != nil {
		t.Fatal(err)
	}
	want := modelprovider.Presets()
	if len(page.Items) != len(want) {
		t.Fatalf("route answered %d rows, the catalog has %d", len(page.Items), len(want))
	}
	for i := range want {
		// Key verification and Claude roles are internal launch/probe policy.
		want[i].KeyCheckURL = ""
		want[i].Models = modelprovider.PresetModels{Default: want[i].Models.Default}
		if page.Items[i] != want[i] {
			t.Fatalf("row %d = %+v, want %+v", i, page.Items[i], want[i])
		}
	}
	// The key names are the contract the CLI, the console and the SDKs read; decoding
	// into the same struct would pass a wrong tag, so they are named here.
	var wire struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal([]byte(got.raw), &wire); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"ref", "title", "kind", "base_url", "auth", "docs_url"} {
		if _, ok := wire.Items[0][key]; !ok {
			t.Fatalf("the first row has no %q key: %v", key, wire.Items[0])
		}
	}
	if wire.Items[1]["models_url"] != "https://api.deepseek.com/models" || wire.Items[1]["auth"] != "bearer" {
		t.Fatalf("deepseek-anthropic on the wire = %v, want models_url and auth bearer", wire.Items[1])
	}
}

// The body the CLI sends (kind, display_name, base_url, api_key, service) is the one
// the route reads: a rename of a key on either side fails here, not in a session. It
// goes through the full-stack harness, a real login under the default step-up policy,
// because adding a provider key asks for the deployment's step-up (providerStepUp).
func TestProviderPresetRouteRegistersFromTheCLIBody(t *testing.T) {
	h := newHarness(t, New(WithProviderSecretVault(newFakeVault())))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	add := func(body map[string]any) resp {
		return h.doJSON("POST", "/v1/m/sessions/providers", admin, body, tenantHdr(tenant))
	}
	body := map[string]any{
		"kind": "anthropic", "display_name": "DeepSeek (Claude Code)", "base_url": "", "api_key": testProviderKey,
		"service": "deepseek-anthropic",
	}
	got := add(body)
	if got.code != http.StatusCreated || got.body["service"] != "deepseek-anthropic" || got.body["base_url"] != "https://api.deepseek.com/anthropic" {
		t.Fatalf("POST /providers with a preset = %d %s, want 201 at the row's endpoint", got.code, got.raw)
	}
	body["service"] = "not-a-vendor"
	got = add(body)
	if got.code != http.StatusBadRequest || !strings.Contains(got.raw, strings.Join(modelprovider.PresetRefs(), ", ")) {
		t.Fatalf("POST /providers with an unknown preset = %d %s, want 400 listing the valid refs", got.code, got.raw)
	}
}

// A preset record stays at the vendor's endpoint: a re-endpoint to another host is
// refused, and the vendor's own address, with or without a trailing slash, is not a change.
func TestProviderPresetRecordKeepsItsEndpointOnPatch(t *testing.T) {
	m, st, tenant, _, _ := providerHarness(t)
	defer st.Close()
	row, _ := modelprovider.LookupPreset("zai-anthropic")
	rec := mustCreateRecord(t, m, tenant, CreateProviderRecordInput{
		Kind: row.Kind, Service: row.Ref, DisplayName: row.Title, APIKey: testProviderKey,
	})
	ctx := context.Background()
	other := "https://other.example.com"
	if _, err := m.PatchProviderRecord(ctx, tenant, rec.Ref, ProviderRecordPatch{BaseURL: &other}); err == nil {
		t.Fatal("a preset record was re-endpointed to another host")
	}
	for _, same := range []string{row.BaseURL, row.BaseURL + "/", ""} {
		updated, err := m.PatchProviderRecord(ctx, tenant, rec.Ref, ProviderRecordPatch{BaseURL: &same})
		if err != nil || updated.BaseURL != row.BaseURL {
			t.Fatalf("patch base_url %q = %q, %v; want the row's %q", same, updated.BaseURL, err, row.BaseURL)
		}
	}
}

// Governed inference opens a credential only for the deepseek service. A preset
// record of the same vendor is a native-session credential and never that.
func TestProviderPresetRecordIsNeverAGovernedInferenceCredential(t *testing.T) {
	m, st, tenant, _, _ := providerHarness(t)
	defer st.Close()
	rec := mustCreateRecord(t, m, tenant, CreateProviderRecordInput{
		Kind: ProviderKindAnthropic, Service: "deepseek-anthropic", DisplayName: "DeepSeek (Claude Code)", APIKey: testProviderKey,
	})
	for _, service := range []string{"deepseek", "deepseek-anthropic"} {
		key, err := m.ResolveProviderCredential(context.Background(), tenant, rec.Ref, rec.Version, service, modelprovider.DeepSeekChatURL)
		if err == nil || len(key) != 0 {
			t.Fatalf("service %q opened a preset record for governed inference", service)
		}
	}
}

// permCapture records the permission each route is registered under.
type permCapture struct{ perms map[string]auth.Permission }

func (c permCapture) Handle(method, pattern string, perm auth.Permission, _ api.ModuleHandler) {
	c.perms[method+" "+pattern] = perm
}

func (c permCapture) HandleEntity(method, pattern string, perm auth.Permission, _ api.EntityRef, h api.ModuleHandler) {
	c.Handle(method, pattern, perm, h)
}

func TestProviderPresetsRouteNeedsTheProviderReadPermission(t *testing.T) {
	m, st, _, _, _ := providerHarness(t)
	defer st.Close()
	c := permCapture{perms: map[string]auth.Permission{}}
	m.APIRoutes(c)
	if got := c.perms["GET /provider-presets"]; got != permProviderRead {
		t.Fatalf("GET /provider-presets is registered under %q, want %q", got, permProviderRead)
	}
}

func TestProviderPresetsOpenAPIPublishesCatalogFields(t *testing.T) {
	op := oastest.Op(t, New(), "get", "/v1/m/sessions/provider-presets")
	responses := oastest.Map(t, op["responses"], "responses")
	ok := oastest.Map(t, responses["200"], "success")
	content := oastest.Map(t, ok["content"], "content")
	body := oastest.Map(t, content["application/json"], "JSON body")
	schema := oastest.Map(t, body["schema"], "catalog page")
	page := oastest.Map(t, schema["properties"], "page fields")
	items := oastest.Map(t, page["items"], "items")
	row := oastest.Map(t, items["items"], "preset")
	fields := oastest.Map(t, row["properties"], "preset fields")
	for _, name := range []string{"ref", "title", "kind", "base_url", "models_url", "auth", "docs_url", "key_url"} {
		if field := oastest.Map(t, fields[name], name); field["type"] != "string" {
			t.Errorf("%s type = %v, want string", name, field["type"])
		}
	}
	models := oastest.Map(t, fields["models"], "models")
	modelFields := oastest.Map(t, models["properties"], "model fields")
	if field := oastest.Map(t, modelFields["default"], "default model"); field["type"] != "string" {
		t.Errorf("default model type = %v, want string", field["type"])
	}
	if field := oastest.Map(t, page["has_more"], "has_more"); field["type"] != "boolean" {
		t.Errorf("has_more type = %v, want boolean", field["type"])
	}
	for _, preset := range modelprovider.Presets() {
		encoded, err := json.Marshal(preset)
		if err != nil {
			t.Fatal(err)
		}
		var wire map[string]any
		if err := json.Unmarshal(encoded, &wire); err != nil {
			t.Fatal(err)
		}
		for key := range wire {
			if fields[key] == nil {
				t.Errorf("%s emits undocumented field %q", preset.Ref, key)
			}
		}
		for key := range oastest.Map(t, wire["models"], "serialized models") {
			if modelFields[key] == nil {
				t.Errorf("%s emits undocumented model field %q", preset.Ref, key)
			}
		}
	}
}

func TestProviderPresetLaunchUsesCatalogAuth(t *testing.T) {
	for _, row := range modelprovider.Presets() {
		if row.Kind != ProviderKindAnthropic {
			continue
		}
		t.Run(row.Ref, func(t *testing.T) {
			vault := &nativeServiceVault{fakeVault: newFakeVault()}
			m, _, tenant, _ := newRuntimeHarness(t, WithProviderSecretVault(vault))
			rec := mustCreateRecord(t, m, tenant, CreateProviderRecordInput{Kind: row.Kind, Service: row.Ref, DisplayName: row.Title, APIKey: testProviderKey})
			_, env, err := m.mintFromProviderRecord(context.Background(), tenant, providerDriverClaude, rec.Ref)
			if err != nil {
				t.Fatal(err)
			}
			want, absent := "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"
			if row.Auth == modelprovider.AuthBearer {
				want, absent = absent, want
			}
			found := false
			for _, item := range env {
				if item.Name == absent && item.Value != "" {
					t.Fatalf("launch injected %s instead of %s", absent, want)
				}
				if item.Name == want && item.Value == testProviderKey {
					found = true
				}
			}
			if !found {
				t.Fatalf("launch missing %s", want)
			}
			bound := BoundProvider{Kind: row.Kind, Endpoint: row.BaseURL}
			policy := sessionNetworkPolicy(providerDriverClaude, bound, "", nil, env)
			if policy == nil || policy.CredentialSHA256 != sha256.Sum256([]byte(testProviderKey)) {
				t.Fatal("egress lost the credential pin")
			}
		})
	}
}

func TestProviderPresetLaunchRefusesStoredEndpointDrift(t *testing.T) {
	vault := &nativeServiceVault{fakeVault: newFakeVault()}
	m, _, tenant, _ := newRuntimeHarness(t, WithProviderSecretVault(vault))
	rec := mustCreateRecord(t, m, tenant, CreateProviderRecordInput{Kind: ProviderKindAnthropic, Service: "zai-anthropic", DisplayName: "Drift", APIKey: testProviderKey})
	err := m.Data.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(providerRecordKind)
		if err != nil {
			return err
		}
		stored, err := findProviderRecordRec(context.Background(), sc, rec.Ref)
		if err != nil {
			return err
		}
		stored["base_url"] = "https://old.example.com"
		_, err = repo.Update(context.Background(), stored)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	_, env, err := m.mintFromProviderRecord(context.Background(), tenant, providerDriverClaude, rec.Ref)
	if err == nil || len(env) != 0 || vault.opens.Load() != 0 {
		t.Fatal("endpoint drift must be refused before opening the vault")
	}
}
