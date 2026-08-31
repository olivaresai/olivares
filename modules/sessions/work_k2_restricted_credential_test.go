// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

type k2RestrictedWorkState struct {
	snapshot string
	lease    WorkLease
}

func k2RestrictedSnapshot(t *testing.T, f *k2Fixture) k2RestrictedWorkState {
	t.Helper()
	snapshot, err := f.h.m.Get(context.Background(), f.tenant, WorkPrincipal{}, model.ID(f.itemID))
	if err != nil {
		t.Fatalf("read WorkItem snapshot: %v", err)
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("encode WorkItem snapshot: %v", err)
	}
	return k2RestrictedWorkState{snapshot: string(raw), lease: f.lease(t)}
}

func k2RestrictedAssertNoEffect(
	t *testing.T,
	f *k2Fixture,
	before k2RestrictedWorkState,
	got resp,
	wantStatus int,
	wantCode string,
) {
	t.Helper()
	if got.code != wantStatus || workAPIErrorCode(got) != wantCode {
		t.Fatalf("restricted mutation = %d %s, want %d %s",
			got.code, got.raw, wantStatus, wantCode)
	}
	after := k2RestrictedSnapshot(t, f)
	if after != before {
		t.Fatalf("refused restricted mutation changed durable work state:\n before=%#v\n after=%#v",
			before, after)
	}
}

func k2RestrictedCriterionID(t *testing.T, f *k2Fixture) string {
	t.Helper()
	got := f.h.do(http.MethodGet, "/v1/m/sessions/work-items/"+f.itemID+"/acceptance",
		f.admin, tenantHdr(f.tenant))
	items, _ := got.body["items"].([]any)
	if got.code != http.StatusOK || len(items) != 1 {
		t.Fatalf("read acceptance = %d %s, want one criterion", got.code, got.raw)
	}
	criterion, _ := items[0].(map[string]any)
	id, _ := criterion["id"].(string)
	if id == "" {
		t.Fatalf("acceptance criterion has no id: %s", got.raw)
	}
	return id
}

func k2RestrictedEvaluate(
	t *testing.T,
	f *k2Fixture,
	token string,
	fence int64,
	state string,
) resp {
	t.Helper()
	body := map[string]any{
		"holder_sid": f.sid,
		"fence":      fence,
		"acceptance": []any{map[string]any{
			"state":         state,
			"evidence_ref":  "job:k2-restricted-" + state,
			"evidence_hash": hexHash(hashBytes([]byte("k2-restricted-" + state))),
		}},
	}
	return f.h.doJSON(http.MethodPatch,
		"/v1/m/sessions/work-items/"+f.itemID+"/acceptance/"+
			k2RestrictedCriterionID(t, f)+"?mode=apply",
		token, body, workAPIHeaders(f.tenant, map[string]string{
			"Idempotency-Key": model.NewID().String(),
			"If-Match":        etag(f.version),
		}))
}

func k2RestrictedAcquire(t *testing.T, f *k2Fixture) int64 {
	t.Helper()
	acquired := f.acquire(f.driver, nil)
	if acquired.code != http.StatusOK {
		t.Fatalf("exact SID lease.acquire = %d %s, want 200", acquired.code, acquired.raw)
	}
	f.version = int64(acquired.body["version"].(float64))
	return f.lease(t).Fence
}

func k2RestrictedTransition(
	f *k2Fixture,
	token string,
	command string,
	fence int64,
) resp {
	body := map[string]any{
		"command":    command,
		"holder_sid": f.sid,
		"fence":      fence,
	}
	if command == "item.block" || command == "item.fail" {
		body["code"] = "k2_restricted_test"
		body["reason"] = "The exact work-session authority controls this transition."
	}
	return f.apply(http.MethodPost, "/transitions", token, body)
}

func k2RestrictedPromoteToReview(t *testing.T, f *k2Fixture) int64 {
	t.Helper()
	fence := k2RestrictedAcquire(t, f)
	evaluated := k2RestrictedEvaluate(t, f, f.driver, fence, "failed")
	if evaluated.code != http.StatusOK {
		t.Fatalf("seed failed acceptance = %d %s, want 200", evaluated.code, evaluated.raw)
	}
	f.version = int64(evaluated.body["version"].(float64))
	submitted := k2RestrictedTransition(f, f.driver, "item.submit", fence)
	if submitted.code != http.StatusOK || submitted.body["status"] != "review" {
		t.Fatalf("seed review = %d %s, want 200 review", submitted.code, submitted.raw)
	}
	f.version = int64(submitted.body["version"].(float64))
	if lease := f.lease(t); lease.State == workLeaseActive {
		t.Fatalf("review seed retained a live execution lease: %#v", lease)
	}
	return fence
}

func TestWorkK2RestrictedCredentialRejectsSiblingExecutionCommands(t *testing.T) {
	t.Run("acceptance.evaluate", func(t *testing.T) {
		f := newK2Fixture(t, "k2-restricted-sibling-evaluate")
		fence := k2RestrictedAcquire(t, f)
		before := k2RestrictedSnapshot(t, f)
		denied := k2RestrictedEvaluate(t, f, f.sibling, fence, "passed")
		k2RestrictedAssertNoEffect(t, f, before, denied, http.StatusForbidden, "forbidden")

		accepted := k2RestrictedEvaluate(t, f, f.driver, fence, "passed")
		if accepted.code != http.StatusOK {
			t.Fatalf("exact SID acceptance.evaluate = %d %s, want 200", accepted.code, accepted.raw)
		}
	})

	t.Run("item.block", func(t *testing.T) {
		f := newK2Fixture(t, "k2-restricted-sibling-block")
		fence := k2RestrictedAcquire(t, f)
		before := k2RestrictedSnapshot(t, f)
		denied := k2RestrictedTransition(f, f.sibling, "item.block", fence)
		k2RestrictedAssertNoEffect(t, f, before, denied, http.StatusForbidden, "forbidden")

		accepted := k2RestrictedTransition(f, f.driver, "item.block", fence)
		if accepted.code != http.StatusOK || accepted.body["status"] != "blocked" {
			t.Fatalf("exact SID item.block = %d %s, want 200 blocked", accepted.code, accepted.raw)
		}
	})

	t.Run("item.fail", func(t *testing.T) {
		f := newK2Fixture(t, "k2-restricted-sibling-fail")
		fence := k2RestrictedAcquire(t, f)
		before := k2RestrictedSnapshot(t, f)
		denied := k2RestrictedTransition(f, f.sibling, "item.fail", fence)
		k2RestrictedAssertNoEffect(t, f, before, denied, http.StatusForbidden, "forbidden")

		accepted := k2RestrictedTransition(f, f.driver, "item.fail", fence)
		if accepted.code != http.StatusOK || accepted.body["status"] != "failed" {
			t.Fatalf("exact SID item.fail = %d %s, want 200 failed", accepted.code, accepted.raw)
		}
	})

	t.Run("item.submit", func(t *testing.T) {
		f := newK2Fixture(t, "k2-restricted-sibling-submit")
		fence := k2RestrictedAcquire(t, f)
		evaluated := k2RestrictedEvaluate(t, f, f.driver, fence, "passed")
		if evaluated.code != http.StatusOK {
			t.Fatalf("seed passed acceptance = %d %s, want 200", evaluated.code, evaluated.raw)
		}
		f.version = int64(evaluated.body["version"].(float64))
		before := k2RestrictedSnapshot(t, f)
		denied := k2RestrictedTransition(f, f.sibling, "item.submit", fence)
		k2RestrictedAssertNoEffect(t, f, before, denied, http.StatusForbidden, "forbidden")

		accepted := k2RestrictedTransition(f, f.driver, "item.submit", fence)
		if accepted.code != http.StatusOK || accepted.body["status"] != "review" {
			t.Fatalf("exact SID item.submit = %d %s, want 200 review", accepted.code, accepted.raw)
		}
	})
}

func TestWorkK2RestrictedCredentialRequiresActiveExecutionLease(t *testing.T) {
	for _, command := range []string{"item.block", "item.fail", "acceptance.evaluate"} {
		command := command
		t.Run("ready/"+command, func(t *testing.T) {
			f := newK2Fixture(t, "k2-restricted-ready-"+command)
			if lease := f.lease(t); lease.State != workLeaseVacant {
				t.Fatalf("ready fixture lease = %#v, want vacant", lease)
			}
			before := k2RestrictedSnapshot(t, f)
			var denied resp
			if command == "acceptance.evaluate" {
				denied = k2RestrictedEvaluate(t, f, f.driver, 0, "passed")
			} else {
				denied = k2RestrictedTransition(f, f.driver, command, 0)
			}
			wantStatus, wantCode := http.StatusConflict, "illegal_transition"
			if command == "item.block" {
				wantStatus, wantCode = http.StatusForbidden, "forbidden"
			}
			k2RestrictedAssertNoEffect(t, f, before, denied, wantStatus, wantCode)
		})

		t.Run("review/"+command, func(t *testing.T) {
			f := newK2Fixture(t, "k2-restricted-review-"+command)
			fence := k2RestrictedPromoteToReview(t, f)
			before := k2RestrictedSnapshot(t, f)
			var denied resp
			if command == "acceptance.evaluate" {
				denied = k2RestrictedEvaluate(t, f, f.driver, fence, "passed")
			} else {
				denied = k2RestrictedTransition(f, f.driver, command, fence)
			}
			k2RestrictedAssertNoEffect(t, f, before, denied, http.StatusForbidden, "forbidden")
		})
	}
}
