// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance"
)

// Real HTTP writers, real dual-control decisions and real engine transactions.
// This fails if either transition bump is removed, even though the first and
// last active-only observations are both clear. PostgreSQL must run in CI;
// enginetest reports its unavailable-server leg as a skip, not a pass.
func TestKillSwitchGenerationHTTPStateMachine(t *testing.T) {
	for _, eng := range []store.Engine{store.EngineSQLite, store.EnginePostgres} {
		t.Run(string(eng), func(t *testing.T) {
			opts := harnessOpts{}
			if eng == store.EnginePostgres {
				pg := enginetest.IsolatedPostgres(t)
				opts = harnessOpts{engine: eng, dsn: pg.App, adminDSN: pg.Admin}
			}
			h := newHarnessWith(t, opts)
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "generation")
			_, a := h.roleUser(admin, tenant, "first@example.test", "admin")
			_, b := h.roleUser(admin, tenant, "second@example.test", "admin")
			_, reviewer := h.roleUser(admin, tenant, "reviewer@example.test", "admin")
			snapshot := func(want int64, stopped bool) governance.KillSwitchSnapshot {
				t.Helper()
				var got governance.KillSwitchSnapshot
				if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
					var err error
					got, err = h.gov.LockKillSwitchState(context.Background(), sc)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				if got.Tenant() != tenant || got.Generation() != want || got.State().EstateStopped != stopped {
					t.Fatalf("snapshot = %+v, want %d/%v", got, want, stopped)
				}
				return got
			}
			first := snapshot(1, false)
			r := engage(h, admin, tenant, "estate", "", "incident")
			if r.code != http.StatusCreated {
				t.Fatalf("engage = %d %s", r.code, r.raw)
			}
			stop1 := r.body["id"].(string)
			snapshot(2, true)
			if r := engage(h, admin, tenant, "estate", "", "replay"); r.code != http.StatusConflict {
				t.Fatalf("replay = %d %s", r.code, r.raw)
			}
			snapshot(2, true)
			r = reenable(h, admin, tenant, stop1)
			if r.code != http.StatusAccepted {
				t.Fatalf("pending = %d %s", r.code, r.raw)
			}
			approvalID := r.body["approval"].(map[string]any)["id"].(string)
			snapshot(2, true)
			if d := decide(h, a, tenant, approvalID, "approve"); d.code != http.StatusOK {
				t.Fatalf("decision = %d %s", d.code, d.raw)
			}
			if r := reenable(h, admin, tenant, stop1); r.code != http.StatusAccepted {
				t.Fatalf("one human = %d %s", r.code, r.raw)
			}
			snapshot(2, true)
			if d := decide(h, b, tenant, approvalID, "approve"); d.code != http.StatusOK {
				t.Fatalf("decision = %d %s", d.code, d.raw)
			}
			if r := reenable(h, admin, tenant, stop1); r.code != http.StatusOK {
				t.Fatalf("reenable = %d %s", r.code, r.raw)
			}
			last := snapshot(3, false)
			if first.State().Any() || last.State().Any() || first.Generation() == last.Generation() {
				t.Fatal("clear cycle was invisible")
			}
			if r := reenable(h, admin, tenant, stop1); r.code != http.StatusConflict {
				t.Fatal("terminal replay accepted")
			}
			snapshot(3, false)

			r = engage(h, admin, tenant, "estate", "", "second incident")
			if r.code != http.StatusCreated {
				t.Fatalf("second engage = %d %s", r.code, r.raw)
			}
			stop2 := r.body["id"].(string)
			snapshot(4, true)
			r = reenable(h, admin, tenant, stop2)
			if r.code != http.StatusAccepted {
				t.Fatalf("second pending = %d %s", r.code, r.raw)
			}
			approvalID = r.body["approval"].(map[string]any)["id"].(string)
			for _, token := range []string{a, b} {
				if d := decide(h, token, tenant, approvalID, "approve"); d.code != http.StatusOK {
					t.Fatalf("decision = %d %s", d.code, d.raw)
				}
			}
			if r := reenable(h, admin, tenant, stop2); r.code != http.StatusConflict {
				t.Fatal("post-review backpressure bypassed")
			}
			snapshot(4, true)
			review := func(token, id string) resp {
				return h.do("POST", "/v1/m/governance/killswitch/"+id+"/review", token, map[string]any{"note": "incident investigated"}, tenantHdr(tenant))
			}
			if r := review(admin, stop1); r.code != http.StatusForbidden {
				t.Fatal("review separation bypassed")
			}
			snapshot(4, true)
			if r := review(reviewer, stop1); r.code != http.StatusOK {
				t.Fatalf("review = %d %s", r.code, r.raw)
			}
			snapshot(4, true)
			if r := reenable(h, admin, tenant, stop2); r.code != http.StatusOK {
				t.Fatalf("second reenable = %d %s", r.code, r.raw)
			}
			snapshot(5, false)

			// Malformed historical approval does not bypass the structural quorum and
			// its metadata-only unlink does not advance the posture generation.
			r = engage(h, admin, tenant, "agent", "corrupt-approval-agent", "third incident")
			if r.code != http.StatusCreated {
				t.Fatalf("third engage = %d %s", r.code, r.raw)
			}
			stop3 := r.body["id"].(string)
			r = reenable(h, admin, tenant, stop3)
			if r.code != http.StatusAccepted {
				t.Fatalf("third pending = %d %s", r.code, r.raw)
			}
			approvalID = r.body["approval"].(map[string]any)["id"].(string)
			if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
				repo, err := sc.Ext("governance.approval")
				if err != nil {
					return err
				}
				row, err := repo.Get(context.Background(), model.ID(approvalID))
				if err != nil {
					return err
				}
				row["status"] = "approved"
				_, err = repo.Update(context.Background(), row)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if r := reenable(h, admin, tenant, stop3); r.code != http.StatusConflict {
				t.Fatalf("corrupt quorum = %d %s", r.code, r.raw)
			}
			got := snapshot(6, false)
			if _, stopped := got.State().Stopped("corrupt-approval-agent"); !stopped {
				t.Fatal("corrupt quorum cleared agent")
			}
		})
	}
}
