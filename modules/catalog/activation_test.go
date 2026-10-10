// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package catalog_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/catalog"
)

func TestCatalogActivationRetainsStateUntilApply(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		body, want string
		tamper     bool
	}{
		{"pending", 202, `{"requires_approval":true,"approval_ref":"approval-1"}`, "approved", false},
		{"denied", 403, `{"error":"denied"}`, "approved", false},
		{"failed", 502, `{"error":"executor failed"}`, "approved", false},
		{"malformed", 200, `{}`, "approved", false},
		{"bad pending", 202, `{}`, "approved", false},
		{"applied", 200, `{"status":"applied"}`, "active", false},
		{"noop", 200, `{"status":"noop"}`, "active", false},
		{"tampered", 200, `{"status":"applied"}`, "approved", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			h := newHarness(t, false, catalog.WithActivation(func(r *http.Request, mc api.ModuleContext, in catalog.ActivationRequest) (int, json.RawMessage) {
				calls++
				if in.TargetRef != "deployment:test" || in.EntryID == "" || !json.Valid(in.Spec) || in.ApprovalRef != "approval-1" || mc.Principal.Actor() == "" || r.Header.Get("Authorization") == "" {
					t.Fatal("activation lost its source or authority")
				}
				return tc.status, json.RawMessage(tc.body)
			}))
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "activation")
			entry := h.createApproved(admin, admin, tenant, "activation", "1.0.0")
			r := h.do("POST", "/v1/m/catalog/entries/"+entry+"/instantiate", admin, map[string]any{"name": "activation", "target_ref": "deployment:test"}, tenantHdr(tenant))
			if r.code != http.StatusCreated {
				t.Fatalf("instantiate = %d %s", r.code, r.raw)
			}
			id := r.body["id"].(string)
			path := "/v1/m/catalog/instances/" + id + "/transition"
			if r := h.do("POST", path, admin, map[string]any{"status": "approved"}, tenantHdr(tenant)); r.code != http.StatusOK {
				t.Fatalf("approve = %d %s", r.code, r.raw)
			}
			if tc.tamper {
				if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
					repo, err := sc.Ext("catalog.entry")
					if err != nil {
						return err
					}
					rec, err := repo.Get(context.Background(), model.ID(entry))
					if err != nil {
						return err
					}
					rec["name"] = "tampered"
					_, err = repo.Update(context.Background(), rec)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			r = h.do("POST", path, admin, map[string]any{"status": "active", "approval_ref": "approval-1"}, tenantHdr(tenant))
			wantCode := tc.status
			if tc.name == "malformed" || tc.name == "bad pending" {
				wantCode = 502
			}
			if tc.tamper {
				wantCode = 409
				if calls != 0 {
					t.Fatal("tampered source was dispatched")
				}
			} else if calls != 1 {
				t.Fatalf("dispatch calls = %d", calls)
			}
			if r.code != wantCode {
				t.Fatalf("activation = %d %s, want %d", r.code, r.raw, wantCode)
			}
			if tc.name == "pending" && (r.body["id"] != id || r.body["status"] != "approved" || r.body["approval_ref"] != "approval-1" || r.body["requires_approval"] != true) {
				t.Fatalf("pending lost the instance contract: %v", r.body)
			}
			if got := h.do("GET", "/v1/m/catalog/instances/"+id, admin, nil, tenantHdr(tenant)).body["status"]; got != tc.want {
				t.Fatalf("retained status = %v, want %s", got, tc.want)
			}
		})
	}
}

func TestCatalogActivationSerializesConflictingDecisions(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	h := newHarness(t, false, catalog.WithActivation(func(_ *http.Request, _ api.ModuleContext, _ catalog.ActivationRequest) (int, json.RawMessage) {
		close(entered)
		<-release
		return 200, json.RawMessage(`{"status":"applied"}`)
	}))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "serialized activation")
	entry := h.createApproved(admin, admin, tenant, "serialized-activation", "1.0.0")
	created := h.do("POST", "/v1/m/catalog/entries/"+entry+"/instantiate", admin, map[string]any{"name": "serialized", "target_ref": "deployment:test"}, tenantHdr(tenant))
	if created.code != 201 {
		t.Fatalf("instantiate = %d", created.code)
	}
	id := created.body["id"].(string)
	path := "/v1/m/catalog/instances/" + id + "/transition"
	if r := h.do("POST", path, admin, map[string]any{"status": "approved"}, tenantHdr(tenant)); r.code != 200 {
		t.Fatalf("approve = %d", r.code)
	}
	other := h.do("POST", "/v1/m/catalog/entries/"+entry+"/instantiate", admin, map[string]any{"name": "unrelated"}, tenantHdr(tenant))
	if other.code != 201 {
		t.Fatalf("other instantiate = %d", other.code)
	}
	otherPath := "/v1/m/catalog/instances/" + other.body["id"].(string) + "/transition"
	active, rejected := make(chan int, 1), make(chan int, 1)
	go func() {
		active <- h.do("POST", path, admin, map[string]any{"status": "active"}, tenantHdr(tenant)).code
	}()
	<-entered
	unrelated := make(chan int, 1)
	go func() {
		unrelated <- h.do("POST", otherPath, admin, map[string]any{"status": "approved"}, tenantHdr(tenant)).code
	}()
	select {
	case code := <-unrelated:
		if code != 200 {
			t.Errorf("unrelated approval = %d", code)
		}
	case <-time.After(time.Second):
		t.Error("unrelated instance blocked by another instance's deployment")
	}
	go func() {
		rejected <- h.do("POST", path, admin, map[string]any{"status": "rejected"}, tenantHdr(tenant)).code
	}()
	var early int
	select {
	case early = <-rejected:
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if got := <-active; got != 200 {
		t.Errorf("activation = %d, want 200", got)
	}
	if early != 0 {
		t.Errorf("rejection completed during provisioning: %d", early)
	} else if got := <-rejected; got != 409 {
		t.Errorf("late rejection = %d, want 409", got)
	}
	if got := h.do("GET", "/v1/m/catalog/instances/"+id, admin, nil, tenantHdr(tenant)).body["status"]; got != "active" {
		t.Errorf("retained status = %v, want active", got)
	}
}
