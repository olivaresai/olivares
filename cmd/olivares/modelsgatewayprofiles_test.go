// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/models"
)

const testGatewayTenant = "11111111-1111-4111-8111-111111111111"

func validModelGatewayProfile(t *testing.T) modelGatewayProfileConfig {
	t.Helper()
	p := modelGatewayProfileConfig{
		Ref: "primary-chat", TenantRef: testGatewayTenant,
		Action: models.ExecutionActionTextGenerate, Protocol: models.ExecutionProtocolChatTextV1,
		AdapterID: models.ExecutionAdapterModelProviderChat, AdapterVersion: models.ExecutionAdapterVersion1,
		ProviderRef: "operator-gateway", ModelRef: "chat-model-v1",
		Endpoint: "https://gateway.example.invalid/v1/chat/completions",
		Surface:  "direct", InferenceGeo: "us", CredentialAudience: "https://gateway.example.invalid",
		AuthScheme: "bearer", CredentialRef: "env:OLIVARES_CHAT_TOKEN",
		MaxRequestBytes: 1 << 20, MaxResponseBytes: 8 << 20, TimeoutMS: 30_000,
	}
	revision, err := calculateModelGatewayProfileRevision(p)
	if err != nil {
		t.Fatal(err)
	}
	p.Revision = revision
	return p
}

func TestModelGatewayProfileRevisionIsDeterministicAndContentAddressed(t *testing.T) {
	base := validModelGatewayProfile(t)
	if base.Revision != "sha256:a61fa36590fe4cec852c4587908433e27e5cd36c251f41c0c1266f04d99ee29b" {
		t.Fatalf("operator example revision drifted: %s", base.Revision)
	}
	if _, _, err := validateModelGatewayProfile(base); err != nil {
		t.Fatalf("valid profile: %v", err)
	}

	mutations := map[string]func(*modelGatewayProfileConfig){
		"credential_reference": func(p *modelGatewayProfileConfig) { p.CredentialRef = "file:/run/secrets/chat-token" },
		"credential_audience": func(p *modelGatewayProfileConfig) {
			p.Endpoint = "https://second.example.invalid/v1/chat/completions"
			p.CredentialAudience = "https://second.example.invalid"
		},
		"request_bound":  func(p *modelGatewayProfileConfig) { p.MaxRequestBytes++ },
		"response_bound": func(p *modelGatewayProfileConfig) { p.MaxResponseBytes++ },
		"timeout_bound":  func(p *modelGatewayProfileConfig) { p.TimeoutMS++ },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := base
			mutate(&changed)
			if _, _, err := validateModelGatewayProfile(changed); err == nil || !strings.Contains(err.Error(), "revision") {
				t.Fatalf("content drift with old revision = %v, want revision refusal", err)
			}
		})
	}
}

func TestModelGatewayProfilesJSONIsStrict(t *testing.T) {
	tests := map[string]string{
		"duplicate_top_level":      `{"schema_version":"olivares.model-gateway-profiles.v1","schema_version":"olivares.model-gateway-profiles.v1","profiles":[]}`,
		"duplicate_profile_member": `{"schema_version":"olivares.model-gateway-profiles.v1","profiles":[{"ref":"a","ref":"b"}]}`,
		"trailing_data":            `{"schema_version":"olivares.model-gateway-profiles.v1","profiles":[]} true`,
		"unknown_top_level":        `{"schema_version":"olivares.model-gateway-profiles.v1","profiles":[],"extra":true}`,
		"unknown_profile_member":   `{"schema_version":"olivares.model-gateway-profiles.v1","profiles":[{"surprise":true}]}`,
		"missing_profiles":         `{"schema_version":"olivares.model-gateway-profiles.v1"}`,

		// encoding/json matches struct fields case-insensitively, so a differently cased
		// spelling is another name for the same effective field. Alone it is an unknown
		// member; beside the exact spelling it assigns the field twice and the last one
		// wins, which the exact-name duplicate scanner cannot see. Both orderings are
		// pinned because only the second one silently replaces an already decoded value.
		"alias_root_after_exact":       `{"schema_version":"olivares.model-gateway-profiles.v1","profiles":[],"PROFILES":[]}`,
		"alias_root_before_exact":      `{"schema_version":"olivares.model-gateway-profiles.v1","PROFILES":[],"profiles":[]}`,
		"alias_root_alone":             `{"schema_version":"olivares.model-gateway-profiles.v1","PROFILES":[]}`,
		"alias_schema_alone":           `{"Schema_Version":"olivares.model-gateway-profiles.v1","profiles":[]}`,
		"alias_profile_after_exact":    `{"schema_version":"olivares.model-gateway-profiles.v1","profiles":[{"ref":"first","REF":"second"}]}`,
		"alias_profile_before_exact":   `{"schema_version":"olivares.model-gateway-profiles.v1","profiles":[{"REF":"first","ref":"second"}]}`,
		"alias_profile_alone":          `{"schema_version":"olivares.model-gateway-profiles.v1","profiles":[{"Ref":"only"}]}`,
		"alias_profile_numeric_member": `{"schema_version":"olivares.model-gateway-profiles.v1","profiles":[{"ref":"a","Timeout_MS":1,"timeout_ms":2}]}`,
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			doc, err := decodeModelGatewayProfiles([]byte(input))
			if err == nil {
				t.Fatalf("invalid JSON contract was accepted: %+v", doc)
			}
			// Recorded so the refusal can be attributed to the check that is meant to
			// fire, rather than to an unrelated one that happens to reject the same input.
			t.Logf("refused: %v", err)
		})
	}
}

// TestModelGatewayProfilesJSONAcceptsExactMemberNames is the positive control for the
// refusals above: rejecting every differently cased spelling must not cost a document
// that spells every member exactly. It decodes only, like the finite diagnostic that
// found the defect, so the profile here is deliberately incomplete for the registry.
func TestModelGatewayProfilesJSONAcceptsExactMemberNames(t *testing.T) {
	if _, err := decodeModelGatewayProfiles([]byte(`{"schema_version":"olivares.model-gateway-profiles.v1","profiles":[]}`)); err != nil {
		t.Fatalf("canonical empty document = %v, want accepted", err)
	}
	doc, err := decodeModelGatewayProfiles([]byte(`{"schema_version":"olivares.model-gateway-profiles.v1","profiles":[{"ref":"first","timeout_ms":30000}]}`))
	if err != nil {
		t.Fatalf("canonical profile document = %v, want accepted", err)
	}
	if len(doc.Profiles) != 1 || doc.Profiles[0].Ref != "first" || doc.Profiles[0].TimeoutMS != 30_000 {
		t.Fatalf("exact member names decoded to %+v", doc.Profiles)
	}

	// The nested relation is keyed by a member name. A key that is not a member of the
	// root shape would leave every profile entry unchecked without failing anything else.
	for name := range modelGatewayProfileDocumentShape.nested {
		if _, supported := modelGatewayProfileDocumentShape.members[name]; !supported {
			t.Fatalf("root shape carries a nested shape under unsupported member %q", name)
		}
	}
}

func TestModelGatewayProfileCredentialGrammarAndDestination(t *testing.T) {
	tests := map[string]func(*modelGatewayProfileConfig){
		"literal_bearer":           func(p *modelGatewayProfileConfig) { p.CredentialRef = "literal-token" },
		"unknown_reference_scheme": func(p *modelGatewayProfileConfig) { p.CredentialRef = "custom:token" },
		"bearer_without_reference": func(p *modelGatewayProfileConfig) { p.CredentialRef = "" },
		"none_with_reference": func(p *modelGatewayProfileConfig) {
			p.AuthScheme = "none"
			p.CredentialRef = "env:OLIVARES_CHAT_TOKEN"
		},
		"http_without_opt_in": func(p *modelGatewayProfileConfig) {
			p.Endpoint = "http://gateway.example.invalid/v1/chat/completions"
			p.CredentialAudience = "http://gateway.example.invalid"
		},
		"audience_not_origin": func(p *modelGatewayProfileConfig) { p.CredentialAudience = "https://other.example.invalid" },
		"query_destination":   func(p *modelGatewayProfileConfig) { p.Endpoint += "?route=other" },
		"zero_request_bound":  func(p *modelGatewayProfileConfig) { p.MaxRequestBytes = 0 },
		"zero_response_bound": func(p *modelGatewayProfileConfig) { p.MaxResponseBytes = 0 },
		"zero_timeout":        func(p *modelGatewayProfileConfig) { p.TimeoutMS = 0 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			p := validModelGatewayProfile(t)
			mutate(&p)
			p.Revision, _ = calculateModelGatewayProfileRevision(p)
			if _, _, err := validateModelGatewayProfile(p); err == nil {
				t.Fatal("unsafe credential/destination configuration was accepted")
			}
		})
	}

	withoutAuth := validModelGatewayProfile(t)
	withoutAuth.AuthScheme = "none"
	withoutAuth.CredentialRef = ""
	withoutAuth.Revision, _ = calculateModelGatewayProfileRevision(withoutAuth)
	if _, _, err := validateModelGatewayProfile(withoutAuth); err != nil {
		t.Fatalf("explicit auth none should be valid: %v", err)
	}
}

func TestModelGatewayProfileRegistryIsTenantScopedAndSnapshotImmutable(t *testing.T) {
	p := validModelGatewayProfile(t)
	doc := modelGatewayProfilesDocument{
		SchemaVersion: modelGatewayProfilesV1, Profiles: []modelGatewayProfileConfig{p},
	}
	registry, err := newModelGatewayProfileRegistry(doc)
	if err != nil {
		t.Fatal(err)
	}
	doc.Profiles[0].Endpoint = "https://mutated-input.example.invalid"
	tenant, _ := model.ParseTenantID(testGatewayTenant)
	first, err := registry.ResolveExecutionProfile(context.Background(), tenant, p.Ref, p.Revision)
	if err != nil {
		t.Fatal(err)
	}
	first.Endpoint = "https://mutated.example.invalid"
	first.MaxRequestBytes = 1
	second, err := registry.ResolveExecutionProfile(context.Background(), tenant, p.Ref, p.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if second.Endpoint != p.Endpoint || second.MaxRequestBytes != int(p.MaxRequestBytes) {
		t.Fatalf("caller mutated a later snapshot: %+v", second)
	}
	foreign, _ := model.ParseTenantID("22222222-2222-4222-8222-222222222222")
	if _, err := registry.ResolveExecutionProfile(context.Background(), foreign, p.Ref, p.Revision); !errors.Is(err, models.ErrExecutionProfileUnavailable) {
		t.Fatalf("foreign lookup = %v, want opaque unavailable", err)
	}
	if _, err := registry.ResolveExecutionProfile(context.Background(), tenant, p.Ref, "sha256:"+strings.Repeat("b", 64)); !errors.Is(err, models.ErrExecutionProfileUnavailable) {
		t.Fatalf("revision lookup = %v, want exact unavailable", err)
	}
}

func TestModelGatewayProfileRegistryRejectsDuplicateAndAudienceReuse(t *testing.T) {
	p := validModelGatewayProfile(t)
	if _, err := newModelGatewayProfileRegistry(modelGatewayProfilesDocument{Profiles: []modelGatewayProfileConfig{p, p}}); err == nil {
		t.Fatal("duplicate tenant/ref/revision was accepted")
	}
	otherRevision := p
	otherRevision.MaxRequestBytes++
	otherRevision.Revision, _ = calculateModelGatewayProfileRevision(otherRevision)
	if _, err := newModelGatewayProfileRegistry(modelGatewayProfilesDocument{Profiles: []modelGatewayProfileConfig{p, otherRevision}}); err == nil {
		t.Fatal("duplicate tenant/ref with another revision was accepted")
	}

	other := p
	other.Ref = "other-chat"
	other.Endpoint = "https://other.example.invalid/v1/chat/completions"
	other.CredentialAudience = "https://other.example.invalid"
	other.Revision, _ = calculateModelGatewayProfileRevision(other)
	if _, err := newModelGatewayProfileRegistry(modelGatewayProfilesDocument{Profiles: []modelGatewayProfileConfig{p, other}}); err == nil {
		t.Fatal("one credential reference was accepted for two audiences")
	}
}

func TestLoadModelGatewayProfilesIsOptionalAndLocal(t *testing.T) {
	if registry, err := loadModelGatewayProfiles(func(string) string { return "" }, nil); err != nil || registry != nil {
		t.Fatalf("unset config = (%v,%v), want (nil,nil)", registry, err)
	}
	p := validModelGatewayProfile(t)
	doc := modelGatewayProfilesDocument{SchemaVersion: modelGatewayProfilesV1, Profiles: []modelGatewayProfileConfig{p}}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "profiles.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := loadModelGatewayProfiles(func(name string) string {
		if name == envModelGatewayProfiles {
			return path
		}
		return ""
	}, nil)
	if err != nil || registry == nil || len(registry.profiles) != 1 {
		t.Fatalf("load local profile = (%v,%v)", registry, err)
	}
}
