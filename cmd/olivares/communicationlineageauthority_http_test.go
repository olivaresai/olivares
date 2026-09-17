// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
)

// TestCommunicationLineageAuthorityCustomGrantAdminHTTP is the F1 acceptance
// journey on SQLite. Once any Cedar/scoped-grant authority is live, core's
// ScopedEvidence binds its decision to the closed lineage-epoch generations it
// actually read. K3 consumes those exact facts, so the supported custom-grant
// administrator can administer a Channel it may not read, and the original
// owner keeps administering after policy activation.
func TestCommunicationLineageAuthorityCustomGrantAdminHTTP(t *testing.T) {
	exerciseCommunicationLineageAuthorityCustomGrantAdminHTTP(t, communicationHTTPTestSQLiteStore(t))
}

// TestCommunicationLineageAuthorityCustomGrantAdminHTTPPostgres is the same
// journey on an owned IsolatedPostgresSplitOwner database, where
// LockAuthoritySnapshot takes the real per-lineage row locks. Absent PostgreSQL
// skips; a required misconfiguration fails.
func TestCommunicationLineageAuthorityCustomGrantAdminHTTPPostgres(t *testing.T) {
	exerciseCommunicationLineageAuthorityCustomGrantAdminHTTP(t, communicationHTTPTestPostgresStore(t))
}

const communicationLineageAuthorityRole = "review-channel-steward"

func exerciseCommunicationLineageAuthorityCustomGrantAdminHTTP(
	t *testing.T,
	backing communicationHTTPTestStore,
) {
	t.Helper()
	e := bootChannelCatalogHTTPEstateOn(t, backing)

	call := func(
		label, method, path, token string,
		body any,
		headers map[string]string,
		want int,
	) communicationHTTPTestResponse {
		t.Helper()
		response := communicationHTTPTestRequest(t, e.eng, method, path, token, e.tenant, body, headers)
		t.Logf("K3_LINEAGE_HTTP|case=%s|method=%s|path=%s|status=%d", label, method, path, response.status)
		if response.status >= 400 {
			t.Logf("K3_LINEAGE_HTTP_BODY|case=%s|body=%s", label, response.raw)
		}
		if response.status != want {
			t.Fatalf("%s = %d, want %d: %s", label, response.status, want, response.raw)
		}
		return response
	}

	// membership is asserted from the product authenticator, never from a test
	// double: this journey must not move Membership.role or auth.IsRole.
	membership := func(label string, user communicationHTTPTestUser, wantRole string, wantFound bool) {
		t.Helper()
		principal, err := e.eng.authr.Authenticate(context.Background(), user.token)
		if err != nil {
			t.Fatalf("%s: authenticate: %v", label, err)
		}
		role, found := principal.RoleIn(e.tenant)
		t.Logf("K3_LINEAGE_MEMBERSHIP|case=%s|found=%t|base_role=%s|superadmin=%t",
			label, found, role, principal.Superadmin)
		if role != wantRole || found != wantFound {
			t.Fatalf("%s: membership = (%q, %t), want (%q, %t)", label, role, found, wantRole, wantFound)
		}
	}

	// whoami is recorded as the permission REFLECTION it is. It is deliberately
	// not used as a decision: a tenant-wide set cannot express a per-resource
	// forbid, and this journey must not paper that over.
	whoami := func(label string, user communicationHTTPTestUser) (admin, read bool) {
		t.Helper()
		response := call(label+"_whoami", http.MethodGet, "/v1/auth/whoami", user.token, nil, nil, http.StatusOK)
		out := communicationHTTPTestDecode[struct {
			Grants []struct {
				Tenant      string   `json:"tenant"`
				Permissions []string `json:"permissions"`
			} `json:"grants"`
		}](t, response)
		reported := false
		for _, grant := range out.Grants {
			if grant.Tenant != e.tenant.String() {
				continue
			}
			reported = true
			for _, permission := range grant.Permissions {
				admin = admin || permission == "sessions:channel:admin"
				read = read || permission == "sessions:channel:read"
			}
		}
		t.Logf("K3_LINEAGE_WHOAMI|case=%s|tenant_reported=%t|channel_admin=%t|channel_read=%t",
			label, reported, admin, read)
		return admin, read
	}

	// coreAuthorization is the core authorization OUTPUT, captured from the
	// product Authorizer over a currently resolved principal. It is recorded
	// separately from whoami on purpose: they answer different questions.
	coreAuthorization := func(
		label string,
		user communicationHTTPTestUser,
		channel model.ID,
	) (auth.AuthorizationEvidence, error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		principal, err := e.eng.authr.Authenticate(ctx, user.token)
		if err != nil {
			t.Fatalf("%s: authenticate: %v", label, err)
		}
		ref, ok := principal.Ref()
		if !ok {
			t.Fatalf("%s: authenticated principal carries no ref", label)
		}
		resolved, err := e.eng.authr.ResolvePrincipalScope(ctx, ref, e.tenant)
		if err != nil {
			t.Logf("K3_LINEAGE_CORE|case=%s|current_principal_error=%v", label, err)
			return auth.AuthorizationEvidence{}, err
		}
		evidence := e.eng.authz.AuthorizeEvidence(ctx, auth.Request{
			Principal: resolved, Tenant: e.tenant, Permission: "sessions:channel:admin",
			Resource: auth.ResourceAttrs{
				Kind: "sessions.channel", ID: channel.String(), WorkspaceID: e.workspace,
			},
		})
		t.Logf("K3_LINEAGE_CORE|case=%s|outcome=%v|core=%+v|resource=%+v|forbid=%+v|facts=%d",
			label, evidence.Outcome, evidence.CorePermission, evidence.ResourceGuard,
			evidence.ForbidAbsence, len(evidence.Facts))
		for _, fact := range communicationLineageAuthoritySortedFacts(evidence.Facts) {
			t.Logf("K3_LINEAGE_CORE_FACT|case=%s|kind=%s|id_is_tenant=%t|version=%d|lineage=%t",
				label, fact.Kind, fact.ID == model.ID(e.tenant), fact.Version,
				model.IsLineageEpochKind(fact.Kind))
		}
		return evidence, nil
	}

	membership("member_baseline", e.reader, auth.RoleEditor, true)
	membership("nonmember_baseline", e.stranger, "", false)

	channel := e.create(t, "lineage-authority", []map[string]any{
		channelCatalogGrant(channelCatalogSubject("user", e.owner.id), true, true, true),
		channelCatalogGrant(channelCatalogSubject("user", e.reader.id), false, false, true),
	})
	channelPath := "/v1/m/sessions/channels/" + channel.Channel.ID.String()
	catalogPath := "/v1/m/sessions/channels?workspace_id=" + e.workspace.String()
	etag := channel.ETag
	patch := func(label string, user communicationHTTPTestUser, want int) communicationHTTPTestResponse {
		t.Helper()
		response := call(label, http.MethodPatch, "/v1/m/sessions/channels", user.token,
			map[string]any{"channel_id": channel.Channel.ID, "name": "Channel " + label},
			map[string]string{"If-Match": etag}, want)
		if response.status == http.StatusOK {
			etag = communicationHTTPTestDecode[sessions.ChannelMutationResult](t, response).ETag
		}
		return response
	}

	// ⛔ THE TWO ADMINISTRATIVE READS ARE DRIVEN BY THE SAME DELEGATED PRINCIPAL.
	// The read model's whole claim is that core `sessions:channel:admin` plus a
	// LOCAL admin grant is sufficient and that neither implies read. Until this
	// composition, the F1 journey proved that for the MUTATION (PATCH) and the
	// reader lane proved the GETs against a fixture closure — nobody had driven
	// the delegated custom-role administrator through the actual GETs. These
	// closures do, at every stage the journey already distinguishes.
	administrationPath := "/v1/m/sessions/channels/administration?workspace_id=" + e.workspace.String()
	sheetPath := "/v1/m/sessions/channels/" + channel.Channel.ID.String() +
		"/grants?workspace_id=" + e.workspace.String() + "&state=all"
	administration := func(label string, user communicationHTTPTestUser, want int) communicationHTTPTestResponse {
		t.Helper()
		return call(label+"_administration", http.MethodGet, administrationPath,
			user.token, nil, nil, want)
	}
	grantSheet := func(label string, user communicationHTTPTestUser, want int) communicationHTTPTestResponse {
		t.Helper()
		return call(label+"_grant_sheet", http.MethodGet, sheetPath, user.token, nil, nil, want)
	}
	// adminOnly says whether the caller's OWN generation must carry can_admin and
	// NOT can_read. It is a parameter and not a constant because the owner holds
	// read+write+admin and the delegated member holds admin alone: asserting the
	// member's shape for the owner made the positive control fail, which is what
	// the positive control is for.
	administersTheChannel := func(label string, user communicationHTTPTestUser, adminOnly bool) {
		t.Helper()
		page := communicationHTTPTestDecode[sessions.ChannelAdministrationPage](t,
			administration(label, user, http.StatusOK))
		listed := make([]string, 0, len(page.Items))
		for _, item := range page.Items {
			listed = append(listed, item.Channel.ID.String())
		}
		t.Logf("K3_LINEAGE_ADMINISTRATION|case=%s|items=%v", label, listed)
		if !slices.Contains(listed, channel.Channel.ID.String()) {
			t.Fatalf("%s: the administered Channel is absent from the administrative catalog: %v",
				label, listed)
		}
		sheet := communicationHTTPTestDecode[sessions.ChannelGrantAdministrationPage](t,
			grantSheet(label, user, http.StatusOK))
		if sheet.Channel.ID != channel.Channel.ID || sheet.ETag == "" {
			t.Fatalf("%s: grant sheet Channel/etag = %s / %q",
				label, sheet.Channel.ID, sheet.ETag)
		}
		found := false
		for _, item := range sheet.Items {
			if item.Grant.Subject.Ref == user.id.String() {
				found = true
				if !item.Grant.CanAdmin {
					t.Fatalf("%s: the caller's own generation carries no can_admin: %+v",
						label, item.Grant)
				}
				if adminOnly && item.Grant.CanRead {
					t.Fatalf("%s: the delegated administrator's own generation = %+v, want "+
						"admin WITHOUT read", label, item.Grant)
				}
			}
		}
		if !found {
			t.Fatalf("%s: the grant sheet does not carry the caller's own generation", label)
		}
		t.Logf("K3_LINEAGE_ADMINISTRATION_SHEET|case=%s|items=%d|etag=%q",
			label, len(sheet.Items), sheet.ETag)
	}

	patch("owner_before_policy", e.owner, http.StatusOK)
	patch("member_before_custom_grant", e.reader, http.StatusForbidden)
	patch("nonmember_before_custom_grant", e.stranger, http.StatusForbidden)
	// Without the core permission the local admin grant is not enough, and the
	// point surface conceals rather than confirming the Channel exists.
	administration("member_before_custom_grant", e.reader, http.StatusForbidden)
	grantSheet("member_before_custom_grant", e.reader, http.StatusNotFound)
	// The owner administers from the start: this is the positive control that
	// keeps the refusals above from passing for the wrong reason.
	administersTheChannel("owner_before_policy", e.owner, false)

	// The supported assignment: a tenant-wide custom-role definition carrying
	// exactly one channel permission, then a user grant referencing it. Neither
	// call touches Membership.role.
	call("custom_role_definition", http.MethodPost, "/v1/m/governance/rbac/roles", e.owner.token,
		map[string]any{
			"name":        communicationLineageAuthorityRole,
			"permissions": []string{"sessions:channel:admin"},
			"excludes": []string{
				"sessions:channel:read", "governance:rbac:read", "governance:rbac:admin",
			},
		}, nil, http.StatusCreated)
	assign := func(label string, user communicationHTTPTestUser) string {
		t.Helper()
		response := call(label, http.MethodPost, "/v1/m/governance/rbac/grants", e.owner.token,
			map[string]any{
				"subject_kind": "user", "subject_ref": user.id.String(),
				"role": communicationLineageAuthorityRole, "role_custom": true, "scope_tree": "tenant",
			}, nil, http.StatusCreated)
		return communicationHTTPTestDecode[struct {
			ID string `json:"id"`
		}](t, response).ID
	}
	memberGrant := assign("member_custom_grant_assigned", e.reader)
	nonmemberGrant := assign("nonmember_custom_grant_assigned", e.stranger)

	membership("member_after_custom_grant", e.reader, auth.RoleEditor, true)
	membership("nonmember_after_custom_grant", e.stranger, "", false)

	// Core now produces the lineage-epoch generations it read while resolving
	// this decision. K3 must consume exactly these.
	evidence, err := coreAuthorization("member_after_custom_grant", e.reader, channel.Channel.ID)
	if err != nil {
		t.Fatalf("member current principal must resolve: %v", err)
	}
	if evidence.Outcome != auth.EvidenceAllow {
		t.Fatalf("core authorization outcome = %v, want allow", evidence.Outcome)
	}
	lineageKinds := communicationLineageAuthorityLineageKinds(evidence.Facts)
	if len(lineageKinds) == 0 {
		t.Fatalf("this journey no longer exercises F1: core produced no lineage-epoch fact in %v",
			communicationLineageAuthorityKinds(evidence.Facts))
	}
	t.Logf("K3_LINEAGE_KINDS|observed=%v|all=%v", lineageKinds,
		communicationLineageAuthorityKinds(evidence.Facts))
	if !communicationLineageAuthorityHasKind(evidence.Facts, model.AuthorizationEpochKind) {
		t.Fatalf("core authorization epoch is missing from %v",
			communicationLineageAuthorityKinds(evidence.Facts))
	}
	for _, fact := range evidence.Facts {
		if !model.IsLineageEpochKind(fact.Kind) {
			continue
		}
		if fact.ID != model.ID(e.tenant) {
			t.Fatalf("lineage fact %s is keyed on %s, want the tenant %s", fact.Kind, fact.ID, e.tenant)
		}
		if fact.Version < 1 {
			t.Fatalf("lineage fact %s carries version %d", fact.Kind, fact.Version)
		}
	}
	canonical, canonicalErr := sessions.CanonicalAuthorizationFacts(evidence.Facts)
	if canonicalErr != nil {
		t.Fatalf("K3 rejects the exact facts core produced: %v (%v)",
			canonicalErr, communicationLineageAuthorityKinds(evidence.Facts))
	}
	if len(canonical) != len(evidence.Facts) {
		t.Fatalf("canonicalization dropped facts: %d in, %d out", len(evidence.Facts), len(canonical))
	}

	// F1: neither the original owner nor the newly authorized administrator may
	// be turned into an UNKNOWN by policy activation alone.
	patch("owner_after_policy_activation", e.owner, http.StatusOK)
	patch("member_core_and_local_admin", e.reader, http.StatusOK)
	// And the same two principals through the two administrative READS, on the
	// same live policy. A 503 here would be the exact F1 defect on the new
	// surface rather than on PATCH.
	administersTheChannel("owner_after_policy_activation", e.owner, false)
	administersTheChannel("member_core_and_local_admin", e.reader, true)

	// A second and third path through the same canonicalizer, with policy live:
	// Channel creation and the send gate, on a Channel where both participants
	// hold the local grants the operation needs.
	sendChannel := e.create(t, "lineage-authority-send", []map[string]any{
		channelCatalogGrant(channelCatalogSubject("user", e.owner.id), true, true, true),
		channelCatalogGrant(channelCatalogSubject("user", e.reader.id), true, false, false),
	})
	call("owner_send_after_policy_activation", http.MethodPost, "/v1/m/sessions/messages/send",
		e.owner.token, map[string]any{
			"channel_id": sendChannel.Channel.ID,
			"recipient":  map[string]any{"kind": "user", "ref": e.reader.id},
			"content": map[string]any{
				"subject": "lineage authority",
				"blocks": []map[string]any{{
					"type": "text", "format": "plain", "text": "policy is live",
				}},
			},
			"urgency": "normal",
		}, map[string]string{"Idempotency-Key": model.NewID().String()}, http.StatusCreated)

	// The administrator still may not READ the Channel: no local read grant.
	call("member_point_read_without_local_grant", http.MethodGet, channelPath, e.reader.token,
		nil, nil, http.StatusNotFound)
	catalog := communicationHTTPTestDecode[sessions.ChannelCatalogPage](t,
		call("member_catalog_core_read_present", http.MethodGet, catalogPath, e.reader.token,
			nil, nil, http.StatusOK))
	listed := channelCatalogIDs(catalog)
	t.Logf("K3_LINEAGE_CATALOG|case=member_core_read_present|items=%v", listed)
	// Core read is still permitted, so the catalog answers; visibility is the
	// local read grant alone. The administered Channel has none and stays out;
	// the send Channel, where the member does hold read, is listed.
	if slices.Contains(listed, channel.Channel.ID.String()) {
		t.Fatalf("administrator without a local read grant listed the Channel it administers: %v", listed)
	}
	if !slices.Contains(listed, sendChannel.Channel.ID.String()) {
		t.Fatalf("the Channel the member may locally read is missing from %v", listed)
	}

	// F2 stays a separate contract: a grant-only principal with no direct tenant
	// membership is still refused admission, deny-closed as UNKNOWN — and that
	// refusal leaves no effect behind.
	if _, err := coreAuthorization("nonmember_after_custom_grant", e.stranger, channel.Channel.ID); err == nil {
		t.Fatal("a user without a direct tenant membership must not resolve a current principal (F2)")
	}
	beforeRefusal := communicationHTTPTestEffects(t, e.eng, e.tenant)
	patch("nonmember_directory_boundary", e.stranger, http.StatusServiceUnavailable)
	assertCommunicationHTTPTestNoEffects(t, e.eng, e.tenant, beforeRefusal, "the UNKNOWN K3 refusal")

	// An explicit authored forbid on the credential's parent user removes core
	// read without touching the custom grant.
	call("publish_explicit_read_forbid", http.MethodPost, "/v1/m/governance/pdp/publish", e.owner.token,
		map[string]any{"engine": "cedar", "source": fmt.Sprintf(
			`forbid(principal in User::%q, action == Action::"sessions:channel:read", resource);`,
			e.reader.id.String())}, nil, http.StatusOK)

	call("member_catalog_after_forbid", http.MethodGet, catalogPath, e.reader.token,
		nil, nil, http.StatusForbidden)
	call("member_point_read_after_forbid", http.MethodGet, channelPath, e.reader.token,
		nil, nil, http.StatusNotFound)
	patch("member_admin_without_core_or_local_read", e.reader, http.StatusOK)
	patch("owner_after_forbid", e.owner, http.StatusOK)
	// ⛔ THE DECISIVE PAIR, AND THE REASON THIS JOURNEY HAD TO REACH THE GETs.
	// The same request that is 403 on the READ catalog and 404 on the point read
	// answers 200 on BOTH administrative reads, for a principal whose core read is
	// denied by an authored Cedar forbid and whose local grant carries no read bit.
	// "Core channel:admin and the local admin closure are independent, with no
	// implied read" is a claim about these two routes; this is where it is measured.
	administersTheChannel("member_admin_after_read_forbid", e.reader, true)
	administersTheChannel("owner_after_forbid", e.owner, false)

	principal, err := e.eng.authr.Authenticate(context.Background(), e.reader.token)
	if err != nil {
		t.Fatalf("authenticate member after forbid: %v", err)
	}
	for _, permission := range []auth.Permission{"sessions:channel:admin", "sessions:channel:read"} {
		decision := e.eng.authz.Authorize(context.Background(), auth.Request{
			Principal: principal, Tenant: e.tenant, Permission: permission,
			Resource: auth.ResourceFor(permission),
		})
		t.Logf("K3_LINEAGE_AUTHZ|case=member_after_forbid|permission=%s|allowed=%t|reason=%s",
			permission, decision.Allow, decision.Reason)
		if decision.Allow != (permission == "sessions:channel:admin") {
			t.Fatalf("permission %s after the explicit forbid = %t", permission, decision.Allow)
		}
	}
	// Recorded, not asserted as a decision: the tenant-wide reflection still
	// reports read. F3 is a separate capabilities contract.
	if admin, read := whoami("member_after_forbid", e.reader); !admin || !read {
		t.Logf("K3_LINEAGE_WHOAMI_NOTE|the reflection changed shape: admin=%t read=%t", admin, read)
	}

	// Revocation takes effect on the same session, with no relogin.
	call("revoke_member_custom_grant", http.MethodDelete,
		"/v1/m/governance/rbac/grants/"+memberGrant, e.owner.token, nil, nil, http.StatusNoContent)
	beforeRevoked := communicationHTTPTestEffects(t, e.eng, e.tenant)
	patch("member_after_revocation", e.reader, http.StatusForbidden)
	// The GETs lose it on the same session too, and each in its own shape: the
	// collection refuses with 403, the point surface conceals with 404.
	administration("member_after_revocation", e.reader, http.StatusForbidden)
	grantSheet("member_after_revocation", e.reader, http.StatusNotFound)
	// The owner is unaffected, so the two refusals above are the revocation and
	// not a surface that stopped answering.
	administersTheChannel("owner_after_member_revocation", e.owner, false)
	assertCommunicationHTTPTestNoEffects(t, e.eng, e.tenant, beforeRevoked, "the revoked administrator")
	if admin, _ := whoami("member_after_revocation", e.reader); admin {
		t.Fatal("the revoked custom grant is still reflected as channel admin")
	}
	membership("member_after_revocation", e.reader, auth.RoleEditor, true)
	call("revoke_nonmember_custom_grant", http.MethodDelete,
		"/v1/m/governance/rbac/grants/"+nonmemberGrant, e.owner.token, nil, nil, http.StatusNoContent)
	patch("nonmember_after_revocation", e.stranger, http.StatusForbidden)
	patch("owner_after_revocation", e.owner, http.StatusOK)
}

func communicationLineageAuthoritySortedFacts(
	facts []store.AuthorizationFactRef,
) []store.AuthorizationFactRef {
	sorted := append([]store.AuthorizationFactRef(nil), facts...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Kind != sorted[j].Kind {
			return sorted[i].Kind < sorted[j].Kind
		}
		return sorted[i].ID.String() < sorted[j].ID.String()
	})
	return sorted
}

func communicationLineageAuthorityKinds(facts []store.AuthorizationFactRef) []string {
	kinds := make([]string, 0, len(facts))
	for _, fact := range communicationLineageAuthoritySortedFacts(facts) {
		kinds = append(kinds, string(fact.Kind))
	}
	return kinds
}

func communicationLineageAuthorityLineageKinds(facts []store.AuthorizationFactRef) []string {
	kinds := make([]string, 0, len(facts))
	for _, fact := range communicationLineageAuthoritySortedFacts(facts) {
		if model.IsLineageEpochKind(fact.Kind) {
			kinds = append(kinds, string(fact.Kind))
		}
	}
	return kinds
}

func communicationLineageAuthorityHasKind(
	facts []store.AuthorizationFactRef,
	kind model.Kind,
) bool {
	for _, fact := range facts {
		if fact.Kind == kind {
			return true
		}
	}
	return false
}
