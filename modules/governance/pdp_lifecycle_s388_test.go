// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestPdpRollbackReactivatesOlderRevisionAndAudits(t *testing.T) {
	h := newHarness(t)
	tenant, token := h.tenantAdmin()
	headers := tenantHdr(tenant)

	rev1 := h.do("POST", "/v1/m/governance/pdp/publish", token, map[string]any{
		"engine": "cedar",
		"source": `forbid(principal, action, resource) when { context.permission == "agent:write" };`,
	}, headers)
	if rev1.code != http.StatusOK || rev1.body["revision"] != float64(1) {
		t.Fatalf("publish rev1: %d %s", rev1.code, rev1.raw)
	}
	rev2 := h.do("POST", "/v1/m/governance/pdp/publish", token, map[string]any{
		"engine": "cedar",
		"source": `forbid(principal, action, resource) when { context.permission == "agent:read" };`,
	}, headers)
	if rev2.code != http.StatusOK || rev2.body["revision"] != float64(2) {
		t.Fatalf("publish rev2: %d %s", rev2.code, rev2.raw)
	}
	assertActivePdpRevision(t, h, token, headers, 2)

	request := auth.Request{
		Principal:  auth.Principal{Kind: auth.KindToken, CredID: "tok1"},
		Permission: "agent:write", Tenant: tenant,
		Resource: auth.ResourceAttrs{Kind: "agent"},
	}
	if decision, _ := h.gov.Evaluator().Evaluate(context.Background(), request); !decision.Allow {
		t.Fatal("rev2 should not restrict agent:write before rollback")
	}

	rolledBack := h.do("POST", "/v1/m/governance/pdp/rollback", token, map[string]any{
		"engine": "cedar", "revision": 1,
	}, headers)
	if rolledBack.code != http.StatusOK || rolledBack.body["from_revision"] != float64(2) ||
		rolledBack.body["to_revision"] != float64(1) || rolledBack.body["active"] != true {
		t.Fatalf("rollback rev2 -> rev1: %d %s", rolledBack.code, rolledBack.raw)
	}
	assertActivePdpRevision(t, h, token, headers, 1)
	if decision, _ := h.gov.Evaluator().Evaluate(context.Background(), request); decision.Allow {
		t.Fatal("rollback must restore rev1's agent:write restriction on the live evaluator")
	}

	var (
		rollbackAudit *model.AuditEvent
		rollbackMeta  map[string]any
	)
	if err := h.st.View(context.Background(), tenant, func(sc store.Scope) error {
		walker, ok := sc.Audit().(store.CanonicalWalker)
		if !ok {
			return fmt.Errorf("audit log does not expose canonical metadata")
		}
		return walker.WalkCanonical(context.Background(), 1, func(event model.AuditEvent, canonical string, _ []byte) error {
			if event.Action == "governance.pdp.rollback" {
				copy := event
				rollbackAudit = &copy
				decoder := json.NewDecoder(strings.NewReader(canonical))
				decoder.UseNumber()
				if err := decoder.Decode(&rollbackMeta); err != nil {
					return err
				}
			}
			return nil
		})
	}); err != nil {
		t.Fatalf("walk audit: %v", err)
	}
	if rollbackAudit == nil {
		t.Fatal("successful rollback was not audited")
	}
	if rollbackAudit.Actor == "" || fmt.Sprint(rollbackMeta["actor"]) != rollbackAudit.Actor ||
		fmt.Sprint(rollbackMeta["from_revision"]) != "2" ||
		fmt.Sprint(rollbackMeta["to_revision"]) != "1" {
		t.Fatalf("rollback audit lacks actor/from/to: event=%+v meta=%v", rollbackAudit, rollbackMeta)
	}

	// An absent target is refused and cannot move the active pointer.
	absent := h.do("POST", "/v1/m/governance/pdp/rollback", token, map[string]any{
		"engine": "cedar", "revision": 99,
	}, headers)
	if absent.code != http.StatusNotFound {
		t.Fatalf("absent rollback target must be refused: %d %s", absent.code, absent.raw)
	}
	assertActivePdpRevision(t, h, token, headers, 1)

	// Seed a legacy/corrupt non-compiling committed row directly. The public publish
	// path can never create one, but rollback must still defend against old data.
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(model.Kind("governance.policy_revision"))
		if err != nil {
			return err
		}
		_, err = repo.Create(context.Background(), model.Record{
			"surface": "cedar", "revision": int64(3), "content": "forbid(((( garbage",
			"author": "legacy-seed", "validated": false, "active": false, "note": "",
		})
		return err
	}); err != nil {
		t.Fatalf("seed non-compiling revision: %v", err)
	}
	invalid := h.do("POST", "/v1/m/governance/pdp/rollback", token, map[string]any{
		"engine": "cedar", "revision": 3,
	}, headers)
	if invalid.code != http.StatusBadRequest || !strings.Contains(invalid.raw, "would not compile") {
		t.Fatalf("non-compiling rollback target must be refused: %d %s", invalid.code, invalid.raw)
	}
	assertActivePdpRevision(t, h, token, headers, 1)
	if decision, _ := h.gov.Evaluator().Evaluate(context.Background(), request); decision.Allow {
		t.Fatal("refused rollback must leave rev1 active on the live evaluator")
	}
}

func TestPdpTestsReturnsStoredPerRevisionArtifact(t *testing.T) {
	h := newHarness(t)
	tenant, token := h.tenantAdmin()
	headers := tenantHdr(tenant)

	absent := h.do("GET", "/v1/m/governance/pdp/tests?engine=cedar&revision=1", token, nil, headers)
	if absent.code != http.StatusOK || absent.body["available"] != false || absent.body["reason"] == "" {
		t.Fatalf("missing artifact must be an explained available=false: %d %s", absent.code, absent.raw)
	}

	published := h.do("POST", "/v1/m/governance/pdp/publish", token, map[string]any{
		"engine": "cedar",
		"source": `forbid(principal, action, resource) when { context.permission == "agent:write" };`,
	}, headers)
	if published.code != http.StatusOK {
		t.Fatalf("publish: %d %s", published.code, published.raw)
	}
	artifact := h.do("GET", "/v1/m/governance/pdp/tests?engine=cedar&revision=1", token, nil, headers)
	results, _ := artifact.body["results"].([]any)
	if artifact.code != http.StatusOK || artifact.body["available"] != true ||
		artifact.body["revision"] != float64(1) || artifact.body["passed"] != float64(1) ||
		artifact.body["failed"] != float64(0) || artifact.body["total"] != float64(1) || len(results) != 1 {
		t.Fatalf("stored artifact response mismatch: %d %s", artifact.code, artifact.raw)
	}
	result, _ := results[0].(map[string]any)
	if result["name"] != "publish_compile_validate" || result["passed"] != true {
		t.Fatalf("stored artifact result mismatch: %v", result)
	}

	missing := h.do("GET", "/v1/m/governance/pdp/tests?engine=cedar&revision=99", token, nil, headers)
	if missing.code != http.StatusOK || missing.body["available"] != false || missing.body["reason"] == "" {
		t.Fatalf("unknown artifact must not be a server error: %d %s", missing.code, missing.raw)
	}
}

func assertActivePdpRevision(t *testing.T, h *harness, token string, headers map[string]string, want int64) {
	t.Helper()
	versions := h.do("GET", "/v1/m/governance/pdp/versions", token, nil, headers)
	if versions.code != http.StatusOK {
		t.Fatalf("list versions: %d %s", versions.code, versions.raw)
	}
	active := int64(0)
	activeCount := 0
	for _, raw := range items(versions) {
		version, _ := raw.(map[string]any)
		if version["surface"] == "cedar" && version["active"] == true {
			active = int64(version["revision"].(float64))
			activeCount++
		}
	}
	if activeCount != 1 || active != want {
		t.Fatalf("active cedar revision = %d (count %d), want %d: %s", active, activeCount, want, versions.raw)
	}
}
