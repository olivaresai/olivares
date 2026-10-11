// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestRecordingRetentionPolicyPreservesAppendOnlyEvidence(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "recording-retention")
	const class = "privileged-session-recording"
	classes := h.do("GET", s138Base+"/retention/classes", admin, nil, tenantHdr(tenant))
	if classes.code != http.StatusOK {
		t.Fatalf("classes: %d %s", classes.code, classes.raw)
	}
	var found bool
	for _, item := range itemsOf(t, classes) {
		if item["id"] != class {
			continue
		}
		found = true
		if item["purgeable"] != false || intOf(item["recommended_days"]) != 180 ||
			!strings.Contains(item["note"].(string), "append-only") {
			t.Fatalf("recording disposition must disclose preservation: %v", item)
		}
	}
	if !found {
		t.Fatal("recording retention tag is missing from the class registry")
	}
	r := h.putPolicy(admin, tenant, class, map[string]any{
		"retention_days": 180, "disposition": "retain", "basis": "privileged activity evidence",
	})
	if r.code != http.StatusOK || r.body["data_class"] != class || r.body["enabled"] != true {
		t.Fatalf("retain policy: %d %s", r.code, r.raw)
	}
	r = h.putPolicy(admin, tenant, class, map[string]any{"retention_days": 1, "disposition": "purge"})
	if r.code != http.StatusBadRequest || !strings.Contains(r.raw, "not purgeable") {
		t.Fatalf("purge must refuse before approval or deletion: %d %s", r.code, r.raw)
	}
	policies := itemsOf(t, h.do("GET", s138Base+"/retention/policies", admin, nil, tenantHdr(tenant)))
	if len(policies) != 1 || policies[0]["disposition"] != "retain" || intOf(policies[0]["retention_days"]) != 180 {
		t.Fatalf("refused purge must preserve the retain policy: %v", policies)
	}
}

func TestRecordingClassLegalHoldIsTenantScoped(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "held-recording")
	other := h.createOrg(admin, "unheld-recording")
	const class = "privileged-session-recording"
	id := h.createHold(admin, tenant, map[string]any{
		"matter_ref": "recording-preservation", "reason": "preserve privileged activity",
		"scope_kind": "data_class", "data_class": class,
	})
	decision, err := h.mod.CheckHold(context.Background(), tenant, HoldSubject{DataClass: class})
	if err != nil || !decision.Held || len(decision.Holds) != 1 || decision.Holds[0].ID != id {
		t.Fatalf("class hold: %+v, %v", decision, err)
	}
	decision, err = h.mod.CheckHold(context.Background(), other, HoldSubject{DataClass: class})
	if err != nil || decision.Held {
		t.Fatalf("recording hold must stay in its tenant: %+v, %v", decision, err)
	}
}
