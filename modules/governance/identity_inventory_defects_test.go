// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// seedIdentityMeta stores an identity with exactly the given metadata.
func (h *harness) seedIdentityMeta(tenant model.TenantID, ref string, meta map[string]any) {
	h.t.Helper()
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		_, err := sc.Identities().Create(context.Background(), model.Identity{
			ExternalID: ref, Name: ref, Kind: "nhi", Provider: "test", Metadata: meta,
		})
		return err
	}); err != nil {
		h.t.Fatalf("seed identity %s: %v", ref, err)
	}
}

// seedGroup stores a mirrored group and its direct members (kind "identity" or
// "collection").
func (h *harness) seedGroup(tenant model.TenantID, ref string, members map[string]string) {
	h.t.Helper()
	ctx := context.Background()
	if err := h.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		groups, err := sc.Ext("governance.collection")
		if err != nil {
			return err
		}
		if _, err := groups.Create(ctx, model.Record{"source": "test", "col_ref": ref, "col_kind": "group"}); err != nil {
			return err
		}
		edges, err := sc.Ext("governance.collection_member")
		if err != nil {
			return err
		}
		for member, kind := range members {
			if _, err := edges.Create(ctx, model.Record{
				"source": "test", "collection_ref": ref, "member_ref": member, "member_kind": kind,
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		h.t.Fatalf("seed group %s: %v", ref, err)
	}
}

// A group that does not exist is not "a group with no members".
func TestGroupMembersUnknownGroupIsNotFound(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.seedGroup(tenant, "grp:empty", nil)

	if r := h.do("GET", govPath+"/groups/grp:typo/members", admin, nil, tenantHdr(tenant)); r.code != http.StatusOK || r.body["not_found"] != true || !strings.Contains(r.header.Get("Warning"), "Unknown-group") {
		t.Errorf("members of a group that does not exist = %d %s, want 200 with not_found and a warning", r.code, r.raw)
	}
	if r := h.do("GET", govPath+"/groups/grp:empty/members", admin, nil, tenantHdr(tenant)); r.code != http.StatusOK || len(items(r)) != 0 || r.body["not_found"] != nil || r.header.Get("Warning") != "" {
		t.Errorf("members of an existing empty group = %d %s, want 200 with no items", r.code, r.raw)
	}
}

// Transitive resolution stops at maxMembershipDepth. The answer must not read as
// the complete membership: truncated with no cursor is the list's "this is not all".
func TestGroupMembersTransitiveDepthCapIsMarked(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	const chain = 40 // deeper than maxMembershipDepth (32)
	for i := 0; i < chain; i++ {
		next := fmt.Sprintf("grp:%d", i+1)
		members := map[string]string{next: "collection"}
		if i == chain-1 {
			members = map[string]string{"leaf-too-deep": "identity"}
		}
		h.seedGroup(tenant, fmt.Sprintf("grp:%d", i), members)
	}
	h.seedGroup(tenant, "grp:shallow", map[string]string{"leaf-near": "identity"})

	deep := h.do("GET", govPath+"/groups/grp:0/members?transitive=true", admin, nil, tenantHdr(tenant))
	if deep.code != http.StatusOK {
		t.Fatalf("deep members = %d %s", deep.code, deep.raw)
	}
	if more, _ := deep.body["truncated"].(bool); !more {
		t.Errorf("a walk cut at the depth cap answered truncated=false: %s", deep.raw)
	}
	shallow := h.do("GET", govPath+"/groups/grp:shallow/members?transitive=true", admin, nil, tenantHdr(tenant))
	if more, _ := shallow.body["truncated"].(bool); more || len(items(shallow)) != 1 {
		t.Errorf("a complete walk must not be marked: %d %s", shallow.code, shallow.raw)
	}
}

// identity_ref is stored as Identity.ExternalID and read back by a viewer, so an
// inline credential must be refused like every other governance reference.
func TestBindRejectsInlineCredential(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	agent := h.createAgent(tenant, "a", "ext-a")
	path := govPath + "/agents/" + agent.ID.String() + "/identity"

	for name, ref := range map[string]string{
		"userinfo": "https://svc:hunter2@vault.example",
		"assign":   "svc?token=abc123",
	} {
		if r := h.do("POST", path, admin, map[string]any{"identity_ref": ref, "allow_unknown": true}, tenantHdr(tenant)); r.code != http.StatusBadRequest {
			t.Errorf("%s identity_ref bind = %d %.120s, want 400", name, r.code, r.raw)
		}
	}
	list := h.do("GET", govPath+"/identities", admin, nil, tenantHdr(tenant))
	if strings.Contains(list.raw, "hunter2") || strings.Contains(list.raw, "abc123") {
		t.Errorf("a rejected bind still stored the credential: %s", list.raw)
	}
}

// The bind commits even when the shared-identity finding cannot be published, but
// the response must not claim a finding that never went out.
func TestBindReportsFailedSharedIdentityFinding(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	a1 := h.createAgent(tenant, "a1", "ext-a1")
	a2 := h.createAgent(tenant, "a2", "ext-a2")
	bind := func(a model.Agent) resp {
		return h.do("POST", govPath+"/agents/"+a.ID.String()+"/identity", admin, map[string]any{"identity_ref": "shared-svc"}, tenantHdr(tenant))
	}
	bind(a1)

	h.host.publishErr = errors.New("event bus full")
	r := bind(a2)
	if r.code != http.StatusOK || r.body["shared"] != true {
		t.Fatalf("the bind itself must still commit: %d %s", r.code, r.raw)
	}
	if r.body["finding_failed"] != true {
		t.Errorf("response hides that the shared-identity finding was not published: %s", r.raw)
	}

	h.host.publishErr = nil
	a3 := h.createAgent(tenant, "a3", "ext-a3")
	if ok := bind(a3); ok.body["finding_failed"] != nil {
		t.Errorf("a published finding must not carry finding_failed: %s", ok.raw)
	}
}

// Who is in which group is as recon-relevant as who exists: both group reads
// self-audit, like /identities and /bindings.
func TestGroupReadsSelfAudit(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.seedGroup(tenant, "grp:ops", map[string]string{"svc-1": "identity"})
	for _, path := range []string{"/groups", "/groups/grp:ops/members"} {
		if r := h.do("GET", govPath+path, admin, nil, tenantHdr(tenant)); r.code != http.StatusOK {
			t.Fatalf("GET %s = %d %s", path, r.code, r.raw)
		}
	}
	got := map[string]bool{}
	for _, a := range h.auditActions(tenant) {
		got[a] = true
	}
	for _, want := range []string{"governance.group.list", "governance.group.members"} {
		if !got[want] {
			t.Errorf("audit chain lacks %q after the read; has %v", want, h.auditActions(tenant))
		}
	}
}

// Invalid limits retain the published default page and add a deprecation warning.
func TestListInvalidLimitWarnsAndRetainsDefaultPage(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.seedIdentityMeta(tenant, "default-page-row", nil)
	h.seedGroup(tenant, "default-page-group", nil)
	// Every list route that parses ?limit through the shared listQuery.
	for _, path := range []string{"/identities", "/groups", "/approvals", "/policies"} {
		defaultPage := h.do("GET", govPath+path, admin, nil, tenantHdr(tenant))
		for _, bad := range []string{"abc", "-3", "1.5"} {
			if r := h.do("GET", govPath+path+"?limit="+bad, admin, nil, tenantHdr(tenant)); r.code != http.StatusOK || !strings.Contains(r.header.Get("Warning"), "Invalid limit") || !reflect.DeepEqual(items(r), items(defaultPage)) {
				t.Errorf("GET %s?limit=%s = %d %.100s, want 200 with a deprecation warning", path, bad, r.code, r.raw)
			}
		}
	}
	if r := h.do("GET", govPath+"/identities?limit=5", admin, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Errorf("a numeric limit must still work: %d %s", r.code, r.raw)
	}
}

// The depth cap is exact: a chain that fits is whole, one level more is marked, and a
// cycle between groups is cut by the visited set without being called partial.
func TestGroupMembersDepthCapBoundaryAndCycle(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	chain := func(prefix string, n int) {
		for i := 0; i < n; i++ {
			members := map[string]string{fmt.Sprintf("%s:%d", prefix, i+1): "collection"}
			if i == n-1 {
				members = map[string]string{prefix + "-leaf": "identity"}
			}
			h.seedGroup(tenant, fmt.Sprintf("%s:%d", prefix, i), members)
		}
	}
	chain("fits", 32) // groups 0..31, the leaf under 31: the deepest walked level
	chain("over", 33) // one level deeper than the cap
	h.seedGroup(tenant, "cyc:a", map[string]string{"cyc:b": "collection", "cyc-leaf": "identity"})
	h.seedGroup(tenant, "cyc:b", map[string]string{"cyc:a": "collection"})

	for ref, wantMore := range map[string]bool{"fits:0": false, "over:0": true, "cyc:a": false} {
		r := h.do("GET", govPath+"/groups/"+ref+"/members?transitive=true", admin, nil, tenantHdr(tenant))
		if more, _ := r.body["truncated"].(bool); r.code != http.StatusOK || more != wantMore {
			t.Errorf("%s: %d truncated=%v, want %v (%.150s)", ref, r.code, more, wantMore, r.raw)
		}
	}
	if r := h.do("GET", govPath+"/groups/fits:0/members?transitive=true", admin, nil, tenantHdr(tenant)); len(items(r)) != 1 {
		t.Errorf("a chain that fits must reach its leaf: %s", r.raw)
	}
}

// A viewer reads the group routes too, and the reads are audited for the viewer.
func TestGroupReadsByAViewerAreAudited(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.seedGroup(tenant, "grp:ops", map[string]string{"svc-1": "identity"})
	_, viewer := h.roleUser(admin, tenant, "v@acme.com", "viewer")
	for _, path := range []string{"/groups", "/groups/grp:ops/members"} {
		if r := h.do("GET", govPath+path, viewer, nil, tenantHdr(tenant)); r.code != http.StatusOK {
			t.Fatalf("viewer GET %s = %d %s", path, r.code, r.raw)
		}
	}
	got := map[string]bool{}
	for _, a := range h.auditActions(tenant) {
		got[a] = true
	}
	if !got["governance.group.list"] || !got["governance.group.members"] {
		t.Errorf("a viewer's group reads must be audited, chain has %v", h.auditActions(tenant))
	}
}

// Directory references can exceed the policy-expression bound. Preserve them;
// extraordinary lengths receive an advisory warning rather than a new failure.
func TestBindLongCredentialFreeIdentityRefIsPreserved(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	for _, n := range []int{129, 4096, 4097} {
		agent := h.createAgent(tenant, fmt.Sprintf("long-%d", n), fmt.Sprintf("long-ext-%d", n))
		const prefix, suffix = "cn=", ",ou=Engineering,dc=example,dc=org"
		ref := prefix + strings.Repeat("a", n-len(prefix)-len(suffix)) + suffix
		r := h.do("POST", govPath+"/agents/"+agent.ID.String()+"/identity", admin, map[string]any{"identity_ref": ref}, tenantHdr(tenant))
		if r.code != http.StatusOK || r.body["identity_ref"] != ref {
			t.Fatalf("bind %d-byte directory ref = %d %s", len(ref), r.code, r.raw)
		}
		if warned := r.header.Get("Warning") != ""; warned != (n > 4096) {
			t.Errorf("%d-byte ref warning=%q", len(ref), r.header.Get("Warning"))
		}
	}
}
