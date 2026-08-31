// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

// Tests for the defensive SCIM agent extension
// (draft-abbey-scim-agent-extension-00). The extension is opt-in: absent means the
// user provisions normally; present means the parsed fields travel through the
// pipeline. Nothing here is wired to enforcement. Tests span two layers:
//
//   - scim package layer: schema registration, DecodeUser, ApplyPatch
//   - auth package layer: SCIMProvisionUser / SCIMUpdateUser accept agent fields
//
// The auth-layer tests verify the defensive guarantee: having agent fields in the
// input never breaks normal provisioning.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/olivaresai/olivares/core/api/scim"
	"github.com/olivaresai/olivares/core/auth"
)

// --- schema layer -------------------------------------------------------------

// TestSCIMAgentSchemaRegistered verifies that /Schemas (scim.Schemas) includes the
// agent extension URN and that SchemaByID can retrieve it.
func TestSCIMAgentSchemaRegistered(t *testing.T) {
	const base = "https://h/v1/scim/v2"
	schemas := scim.Schemas(base)

	var found bool
	for _, s := range schemas {
		if id, _ := s["id"].(string); id == scim.SchemaAgentExtension {
			found = true
			// Verify it carries the expected attribute names.
			attrs, _ := s["attributes"].([]map[string]any)
			names := make(map[string]bool, len(attrs))
			for _, a := range attrs {
				if name, ok := a["name"].(string); ok {
					names[name] = true
				}
			}
			for _, want := range []string{"agentKind", "sponsorRef", "delegationScope"} {
				if !names[want] {
					t.Errorf("agent schema missing attribute %q", want)
				}
			}
			// No attribute may be required (defensive/opt-in).
			for _, a := range attrs {
				if req, _ := a["required"].(bool); req {
					t.Errorf("agent schema attribute %q has required:true — must be opt-in", a["name"])
				}
			}
		}
	}
	if !found {
		t.Fatalf("scim.Schemas() does not include %q", scim.SchemaAgentExtension)
	}

	// SchemaByID must also find it.
	sc, ok := scim.SchemaByID(base, scim.SchemaAgentExtension)
	if !ok {
		t.Fatal("scim.SchemaByID: agent extension not found")
	}
	if id, _ := sc["id"].(string); id != scim.SchemaAgentExtension {
		t.Errorf("SchemaByID id = %q, want %q", id, scim.SchemaAgentExtension)
	}
}

// TestSCIMAgentResourceTypeExtension verifies that the User ResourceType advertises
// the agent extension as a non-required schemaExtension.
func TestSCIMAgentResourceTypeExtension(t *testing.T) {
	const base = "https://h/v1/scim/v2"
	rt, ok := scim.ResourceTypeByID(base, "User")
	if !ok {
		t.Fatal("ResourceTypeByID(User) not found")
	}
	exts, _ := rt["schemaExtensions"].([]map[string]any)
	var found bool
	for _, ext := range exts {
		if sch, _ := ext["schema"].(string); sch == scim.SchemaAgentExtension {
			found = true
			if req, _ := ext["required"].(bool); req {
				t.Errorf("User ResourceType lists agent extension as required:true — must be false")
			}
		}
	}
	if !found {
		t.Errorf("User ResourceType schemaExtensions does not include %q", scim.SchemaAgentExtension)
	}
}

// --- decode layer -------------------------------------------------------------

// decodeAgentBody is a local helper that JSON-unmarshals the raw body and calls
// scim.DecodeUser, mirroring what the SCIM handler does.
func decodeAgentBody(t *testing.T, raw string) scim.InboundUser {
	t.Helper()
	var b scim.UserBodyType
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return scim.DecodeUser(b)
}

// TestSCIMAgentDecodeAbsent verifies that when the agent extension is absent the
// fields are empty and provisioning is unaffected.
func TestSCIMAgentDecodeAbsent(t *testing.T) {
	in := decodeAgentBody(t, `{"userName":"agent@example.com","active":true}`)
	if in.AgentKind != "" || in.AgentSponsorRef != "" || in.AgentDelegation != "" {
		t.Errorf("absent agent extension: got non-empty fields %+v", in)
	}
}

// TestSCIMAgentDecodePresent verifies that when the IdP sends the agent extension,
// the three agent attributes are parsed into InboundUser correctly.
func TestSCIMAgentDecodePresent(t *testing.T) {
	const urn = `"urn:ietf:params:scim:schemas:extension:agent:2.0:User"`
	body := `{"userName":"agent@example.com","active":true,` + urn + `:{
		"agentKind":"ai_assistant","sponsorRef":"sponsor-ext-id","delegationScope":"read"}}`
	in := decodeAgentBody(t, body)
	if in.AgentKind != "ai_assistant" {
		t.Errorf("AgentKind = %q, want ai_assistant", in.AgentKind)
	}
	if in.AgentSponsorRef != "sponsor-ext-id" {
		t.Errorf("AgentSponsorRef = %q, want sponsor-ext-id", in.AgentSponsorRef)
	}
	if in.AgentDelegation != "read" {
		t.Errorf("AgentDelegation = %q, want read", in.AgentDelegation)
	}
}

// TestSCIMAgentDecodePartial verifies that partial agent extension (only some
// attributes present) is parsed leniently — unset attributes are empty.
func TestSCIMAgentDecodePartial(t *testing.T) {
	const urn = `"urn:ietf:params:scim:schemas:extension:agent:2.0:User"`
	body := `{"userName":"agent@example.com",` + urn + `:{
		"agentKind":"automation"}}`
	in := decodeAgentBody(t, body)
	if in.AgentKind != "automation" {
		t.Errorf("AgentKind = %q, want automation", in.AgentKind)
	}
	if in.AgentSponsorRef != "" || in.AgentDelegation != "" {
		t.Errorf("unset fields non-empty: sponsorRef=%q delegation=%q", in.AgentSponsorRef, in.AgentDelegation)
	}
}

// --- patch layer -------------------------------------------------------------

// TestSCIMAgentPatchApplyByPath verifies that a PATCH replacing agent attributes
// by path (URN:attribute) updates the InboundUser correctly.
func TestSCIMAgentPatchApplyByPath(t *testing.T) {
	const urn = "urn:ietf:params:scim:schemas:extension:agent:2.0:User"
	base := scim.InboundUser{UserName: "a@x.com", Active: true, AgentKind: "automation"}

	patched, err := scim.ApplyPatch(base, scim.PatchBody{Operations: []scim.PatchOp{
		{Op: "replace", Path: urn + ":agentKind", Value: json.RawMessage(`"ai_assistant"`)},
		{Op: "replace", Path: urn + ":sponsorRef", Value: json.RawMessage(`"s-1"`)},
	}})
	if err != nil {
		t.Fatalf("ApplyPatch = %v", err)
	}
	if patched.AgentKind != "ai_assistant" {
		t.Errorf("AgentKind = %q, want ai_assistant", patched.AgentKind)
	}
	if patched.AgentSponsorRef != "s-1" {
		t.Errorf("AgentSponsorRef = %q, want s-1", patched.AgentSponsorRef)
	}
}

// TestSCIMAgentPatchReplaceWholeExtension verifies that a PATCH replacing the whole
// agent extension object clears un-mentioned sub-attributes (RFC 7644 §3.5.2.3).
func TestSCIMAgentPatchReplaceWholeExtension(t *testing.T) {
	const urn = "urn:ietf:params:scim:schemas:extension:agent:2.0:User"
	base := scim.InboundUser{
		UserName: "a@x.com", Active: true,
		AgentKind: "automation", AgentSponsorRef: "s-old", AgentDelegation: "write",
	}

	patched, err := scim.ApplyPatch(base, scim.PatchBody{Operations: []scim.PatchOp{
		{Op: "replace", Path: urn, Value: json.RawMessage(`{"agentKind":"ai_assistant"}`)},
	}})
	if err != nil {
		t.Fatalf("ApplyPatch = %v", err)
	}
	if patched.AgentKind != "ai_assistant" {
		t.Errorf("AgentKind = %q, want ai_assistant", patched.AgentKind)
	}
	if patched.AgentSponsorRef != "" {
		t.Errorf("replace-whole left stale AgentSponsorRef=%q; must clear unmentioned sub-attrs", patched.AgentSponsorRef)
	}
	if patched.AgentDelegation != "" {
		t.Errorf("replace-whole left stale AgentDelegation=%q; must clear unmentioned sub-attrs", patched.AgentDelegation)
	}
}

// TestSCIMAgentPatchAddMerges verifies that an ADD op on the agent extension
// merges (leaves unmentioned sub-attributes intact).
func TestSCIMAgentPatchAddMerges(t *testing.T) {
	const urn = "urn:ietf:params:scim:schemas:extension:agent:2.0:User"
	base := scim.InboundUser{
		UserName: "a@x.com", Active: true,
		AgentKind: "automation", AgentSponsorRef: "s-1",
	}

	added, err := scim.ApplyPatch(base, scim.PatchBody{Operations: []scim.PatchOp{
		{Op: "add", Path: urn, Value: json.RawMessage(`{"agentKind":"ai_assistant"}`)},
	}})
	if err != nil {
		t.Fatalf("ApplyPatch = %v", err)
	}
	if added.AgentKind != "ai_assistant" {
		t.Errorf("AgentKind after add = %q, want ai_assistant", added.AgentKind)
	}
	// sponsorRef must survive because add merges.
	if added.AgentSponsorRef != "s-1" {
		t.Errorf("AgentSponsorRef after add = %q; should survive merge (was s-1)", added.AgentSponsorRef)
	}
}

// TestSCIMAgentPatchRemoveExtension verifies that a PATCH removing the whole agent
// extension clears all agent attributes.
func TestSCIMAgentPatchRemoveExtension(t *testing.T) {
	const urn = "urn:ietf:params:scim:schemas:extension:agent:2.0:User"
	base := scim.InboundUser{
		UserName: "a@x.com", Active: true,
		AgentKind: "ai_assistant", AgentSponsorRef: "s-1", AgentDelegation: "read",
	}

	patched, err := scim.ApplyPatch(base, scim.PatchBody{Operations: []scim.PatchOp{
		{Op: "remove", Path: urn},
	}})
	if err != nil {
		t.Fatalf("ApplyPatch = %v", err)
	}
	if patched.AgentKind != "" || patched.AgentSponsorRef != "" || patched.AgentDelegation != "" {
		t.Errorf("remove-ext left stale agent attrs: kind=%q sponsor=%q scope=%q",
			patched.AgentKind, patched.AgentSponsorRef, patched.AgentDelegation)
	}
}

// TestSCIMAgentPatchPreservesFields verifies that a PATCH that does NOT mention
// the agent extension leaves existing agent fields intact (no silent clearing).
func TestSCIMAgentPatchPreservesFields(t *testing.T) {
	base := scim.InboundUser{
		UserName: "a@x.com", Active: true,
		AgentKind: "ai_assistant", AgentSponsorRef: "s-1",
	}

	// PATCH only touches displayName; agent fields must survive.
	patched, err := scim.ApplyPatch(base, scim.PatchBody{Operations: []scim.PatchOp{
		{Op: "replace", Path: "displayName", Value: json.RawMessage(`"Renamed"`)},
	}})
	if err != nil {
		t.Fatalf("ApplyPatch = %v", err)
	}
	if patched.AgentKind != "ai_assistant" {
		t.Errorf("AgentKind cleared by unrelated PATCH = %q; must be preserved", patched.AgentKind)
	}
	if patched.AgentSponsorRef != "s-1" {
		t.Errorf("AgentSponsorRef cleared by unrelated PATCH = %q; must be preserved", patched.AgentSponsorRef)
	}
}

// --- auth layer ---------------------------------------------------------------

// TestSCIMAgentProvisionWithExtension verifies that SCIMProvisionUser succeeds when
// the input carries agent fields — the defensive guarantee that the extension never
// breaks normal provisioning.
func TestSCIMAgentProvisionWithExtension(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme-agent")

	u, created, err := a.SCIMProvisionUser(ctx, super, tenant, auth.SCIMUserInput{
		UserName:        "agent@acme.com",
		ExternalID:      "idp-agent-1",
		DisplayName:     "Test Agent",
		Active:          true,
		AgentKind:       "ai_assistant",
		AgentSponsorRef: "sponsor-ext-id",
		AgentDelegation: "read",
	})
	if err != nil {
		t.Fatalf("SCIMProvisionUser with agent extension = %v", err)
	}
	if !created {
		t.Error("expected created=true for new agent user")
	}
	if u.Email != "agent@acme.com" {
		t.Errorf("email = %q, want normalized agent@acme.com", u.Email)
	}
	if u.ExternalID != "idp-agent-1" {
		t.Errorf("externalId = %q, want idp-agent-1", u.ExternalID)
	}
}

// TestSCIMAgentProvisionWithoutExtension verifies that normal provisioning
// (no agent extension) is completely unaffected by the new fields.
func TestSCIMAgentProvisionWithoutExtension(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme-plain")

	u, created, err := a.SCIMProvisionUser(ctx, super, tenant, auth.SCIMUserInput{
		UserName:    "plain@acme.com",
		ExternalID:  "idp-plain-1",
		DisplayName: "Plain User",
		Active:      true,
		// No agent fields — zero values.
	})
	if err != nil {
		t.Fatalf("SCIMProvisionUser without agent extension = %v", err)
	}
	if !created {
		t.Error("expected created=true for new plain user")
	}
	if u.Email != "plain@acme.com" {
		t.Errorf("email = %q, want plain@acme.com", u.Email)
	}
}

// TestSCIMAgentUpdateWithExtension verifies that SCIMUpdateUser also accepts agent
// fields without error — both create and update paths must be resilient.
func TestSCIMAgentUpdateWithExtension(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	a := auth.NewAuthenticator(st, nil)
	super := mustSuperadmin(t, ctx, a)
	tenant := provisionTenant(t, st, "acme-update")

	u, _, err := a.SCIMProvisionUser(ctx, super, tenant, auth.SCIMUserInput{
		UserName: "upd@acme.com", Active: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Update carrying agent fields — must succeed, not error.
	_, err = a.SCIMUpdateUser(ctx, super, tenant, u.ID, auth.SCIMUserInput{
		UserName:        "upd@acme.com",
		Active:          true,
		AgentKind:       "automation",
		AgentSponsorRef: "new-sponsor",
		AgentDelegation: "write",
	})
	if err != nil {
		t.Fatalf("SCIMUpdateUser with agent extension = %v", err)
	}
}
