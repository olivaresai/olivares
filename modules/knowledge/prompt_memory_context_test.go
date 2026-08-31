// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// fakeClock is a deterministic, advanceable clock for retention tests.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() model.Timestamp {
	c.mu.Lock()
	defer c.mu.Unlock()
	return model.NewTimestamp(c.t)
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func TestPromptVersioningAndRollback(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")

	r := h.do("POST", "/v1/m/knowledge/prompts", editor, map[string]any{
		"name": "greeting", "template": "Hello {{name}}", "label": "v1",
	}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("create prompt = %d %s", r.code, r.raw)
	}
	pid := r.body["id"].(string)
	if cr, _ := r.body["current_rev"].(float64); cr != 1 {
		t.Fatalf("current_rev = %v", r.body["current_rev"])
	}

	// Add revision 2.
	r2 := h.do("POST", "/v1/m/knowledge/prompts/"+pid+"/revisions", editor, map[string]any{
		"template": "Hi {{name}}!", "label": "v2",
	}, tenantHdr(tenant))
	if r2.code != http.StatusCreated {
		t.Fatalf("add revision = %d %s", r2.code, r2.raw)
	}
	if rev, _ := r2.body["rev"].(float64); rev != 2 {
		t.Fatalf("new rev = %v", r2.body["rev"])
	}
	if g := h.do("GET", "/v1/m/knowledge/prompts/"+pid, editor, nil, tenantHdr(tenant)); g.body["current_rev"].(float64) != 2 {
		t.Fatalf("after add, current_rev should be 2, got %v", g.body["current_rev"])
	}

	// Rollback to rev 1 (pointer moves; rev 2 retained, immutable).
	rb := h.do("POST", "/v1/m/knowledge/prompts/"+pid+"/rollback", editor, map[string]any{"rev": 1}, tenantHdr(tenant))
	if rb.code != http.StatusOK {
		t.Fatalf("rollback = %d %s", rb.code, rb.raw)
	}
	if g := h.do("GET", "/v1/m/knowledge/prompts/"+pid, editor, nil, tenantHdr(tenant)); g.body["current_rev"].(float64) != 1 {
		t.Fatalf("after rollback, current_rev should be 1, got %v", g.body["current_rev"])
	}
	// Rev 2 still exists in history (immutable).
	if g := h.do("GET", "/v1/m/knowledge/prompts/"+pid+"/revisions/2", editor, nil, tenantHdr(tenant)); g.code != http.StatusOK {
		t.Fatalf("revision 2 must be retained, got %d", g.code)
	}
	// Rollback to a nonexistent revision is rejected.
	if rb := h.do("POST", "/v1/m/knowledge/prompts/"+pid+"/rollback", editor, map[string]any{"rev": 99}, tenantHdr(tenant)); rb.code != http.StatusBadRequest {
		t.Fatalf("rollback to missing rev should be 400, got %d", rb.code)
	}
}

func TestPromptTemplateIsRedacted(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")

	r := h.do("POST", "/v1/m/knowledge/prompts", editor, map[string]any{
		"name": "leaky", "template": "Use this key sk-ant-abcdefghijklmnopqrstuv to call the API.",
	}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}
	pid := r.body["id"].(string)
	rev := h.do("GET", "/v1/m/knowledge/prompts/"+pid+"/revisions/1", editor, nil, tenantHdr(tenant))
	if tmpl, _ := rev.body["template"].(string); strings.Contains(tmpl, "sk-ant-abcdefghijklmnopqrstuv") {
		t.Errorf("prompt template must be redacted, got %q", tmpl)
	}
}

func TestMemoryRetentionAndPurge(t *testing.T) {
	fc := newFakeClock()
	h := newHarnessWith(t, WithClock(fc), WithRetrievalGuard(fixedGuard{grants: Grants{Allowed: true, Clearance: classSecret}}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")

	// A permanent entry (no ttl) — content redacted.
	if r := h.do("POST", "/v1/m/knowledge/memory", editor, map[string]any{
		"agent_ref": "a1", "key": "pref", "content": "prefers dark mode; reach me at dev@acme.com",
	}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("put permanent = %d %s", r.code, r.raw)
	}
	// A short-lived entry (ttl 60s).
	if r := h.do("POST", "/v1/m/knowledge/memory", editor, map[string]any{
		"agent_ref": "a1", "key": "session", "content": "current task: ship", "ttl_seconds": 60,
	}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("put ttl = %d %s", r.code, r.raw)
	}

	// Both present now; the email is redacted.
	list := h.do("GET", "/v1/m/knowledge/memory?agent_ref=a1", editor, nil, tenantHdr(tenant))
	if items, _ := list.body["items"].([]any); len(items) != 2 {
		t.Fatalf("expected 2 memory entries, got %d", len(items))
	}
	for _, it := range listItems(list) {
		if strings.Contains(it["content"].(string), "dev@acme.com") {
			t.Errorf("memory content must be redacted: %v", it["content"])
		}
	}

	// Advance past the ttl: the short-lived entry is no longer returned (lazy expiry).
	fc.advance(120 * time.Second)
	list2 := h.do("GET", "/v1/m/knowledge/memory?agent_ref=a1", editor, nil, tenantHdr(tenant))
	if items, _ := list2.body["items"].([]any); len(items) != 1 {
		t.Fatalf("after expiry, expected 1 entry, got %d", len(items))
	}

	// Purge materializes the expiry (admin).
	adminTok := h.roleToken(admin, tenant, "adm@acme.com", "admin")
	p := h.do("POST", "/v1/m/knowledge/memory/purge?agent_ref=a1", adminTok, nil, tenantHdr(tenant))
	if p.code != http.StatusOK {
		t.Fatalf("purge = %d %s", p.code, p.raw)
	}
	if purged, _ := p.body["purged"].(float64); purged != 1 {
		t.Errorf("expected to purge 1 expired entry, got %v", p.body["purged"])
	}
}

func TestContextPolicyUpsert(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "ed@acme.com", "editor")

	r := h.do("POST", "/v1/m/knowledge/context-policies", editor, map[string]any{
		"scope_kind": "agent", "scope_ref": "a1", "max_tokens": 4000, "strategy": "summarize", "redaction_required": true,
	}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("first put (create) should be 201, got %d %s", r.code, r.raw)
	}
	list := h.do("GET", "/v1/m/knowledge/context-policies?scope_kind=agent", editor, nil, tenantHdr(tenant))
	items := listItems(list)
	if len(items) != 1 || items[0]["strategy"] != "summarize" || items[0]["redaction_required"] != true {
		t.Fatalf("unexpected policy list: %v", list.body)
	}
	// Upsert (same scope) does not duplicate.
	_ = h.do("POST", "/v1/m/knowledge/context-policies", editor, map[string]any{
		"scope_kind": "agent", "scope_ref": "a1", "max_tokens": 8000, "strategy": "window",
	}, tenantHdr(tenant))
	list2 := h.do("GET", "/v1/m/knowledge/context-policies?scope_kind=agent", editor, nil, tenantHdr(tenant))
	if items := listItems(list2); len(items) != 1 || items[0]["strategy"] != "window" {
		t.Fatalf("upsert should update in place: %v", list2.body)
	}
}

// listItems extracts the items array as []map[string]any.
func listItems(r resp) []map[string]any {
	raw, _ := r.body["items"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, it := range raw {
		if m, ok := it.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}
