// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package governance_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance"
)

func init() {
	model.MustRegisterPolicyKind("fixture", model.None("test fixture value is never used as a principal: governance/policy_extension_test.go:26"))
}

func TestRegisteredPolicyKindUsesExistingPolicyLifecycle(t *testing.T) {
	h := newHarness(t)
	if err := h.gov.RegisterPolicyKind("fixture", governance.PolicyKindAuthoring{AuditFields: func(spec map[string]any) map[string]any { return map[string]any{"fixture_value": spec["value"]} }, Schema: map[string]any{"type": "object"}, Validate: func(_ context.Context, tenant model.TenantID, raw json.RawMessage) (map[string]any, string) {
		if tenant.IsZero() {
			return nil, "tenant is required"
		}
		var v struct {
			Value string `json:"value"`
		}
		if json.Unmarshal(raw, &v) != nil || v.Value == "" {
			return nil, "value is required"
		}
		return map[string]any{"value": v.Value}, ""
	}}); err != nil {
		t.Fatal(err)
	}
	doc, ok := h.gov.OperationDocumentation("POST", "/policies")
	encoded, err := json.Marshal(doc.RequestBody)
	if err != nil || !ok || !strings.Contains(string(encoded), `["abac","approval","grant_expiry","fixture"]`) {
		t.Fatalf("schema must keep every built-in kind and append the registered one: %s %v", encoded, err)
	}
	root := h.adminLogin()
	tenant := h.createOrg(root, "extension")
	path := "/v1/m/governance/policies"
	bad := h.do("POST", path, root, map[string]any{"name": "nope", "kind": "unregistered", "spec": map[string]any{}}, tenantHdr(tenant))
	if bad.code != http.StatusBadRequest || !strings.Contains(string(bad.raw), "kind must be one of abac, approval, grant_expiry, fixture") {
		t.Fatalf("refusal must name every accepted kind: %d %s", bad.code, bad.raw)
	}
	body := map[string]any{"name": "extension", "kind": "fixture", "enabled": true, "spec": map[string]any{"value": "one"}}
	r := h.do("POST", path, root, body, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("create: %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)
	for _, suffix := range []string{"?kind=fixture", "/" + id} {
		r = h.do("GET", path+suffix, root, nil, tenantHdr(tenant))
		if r.code != http.StatusOK {
			t.Fatalf("read: %d %s", r.code, r.raw)
		}
		if suffix == "?kind=fixture" && len(r.body["items"].([]any)) != 1 {
			t.Fatalf("list omitted policy: %s", r.raw)
		}
	}
	body["enabled"] = false
	r = h.do("PUT", path+"/"+id, root, body, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("update: %d %s", r.code, r.raw)
	}
	r = h.do("DELETE", path+"/"+id, root, nil, tenantHdr(tenant))
	if r.code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", r.code, r.raw)
	}
	count := 0
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		walker, ok := sc.Audit().(store.CanonicalWalker)
		if !ok {
			t.Fatal("audit store has no canonical walker")
		}
		return walker.WalkCanonical(context.Background(), 1, func(e model.AuditEvent, canonical string, _ []byte) error {
			if err := json.Unmarshal([]byte(canonical), &e.Meta); err != nil {
				return err
			}
			if strings.HasPrefix(e.Action, "governance.policy.") && e.TargetID.String() == id {
				count++
				if e.Meta["fixture_value"] != "one" {
					t.Errorf("audit attribution absent: %+v", e.Meta)
				}
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("policy audit events=%d, want 3", count)
	}
}

func TestPolicyExtensionCannotReplaceBuiltinsOrUnknownKinds(t *testing.T) {
	h := newHarness(t)
	for _, kind := range []string{"", "abac", "approval", "unregistered"} {
		if err := h.gov.RegisterPolicyKind(kind, governance.PolicyKindAuthoring{Schema: map[string]any{"type": "object"}, Validate: func(context.Context, model.TenantID, json.RawMessage) (map[string]any, string) { return nil, "" }}); err == nil {
			t.Errorf("registered %q", kind)
		}
	}
}
