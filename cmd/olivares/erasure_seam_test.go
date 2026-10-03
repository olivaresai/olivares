// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// erasureFixtureStep uses the ordinary retirement seam and its real core pin;
// only the external store's observed state is controlled by this fixture.
type erasureFixtureStep struct {
	st      store.Store
	outcome auth.RetirementOutcome
}

func (s *erasureFixtureStep) Module() string { return "erasure-fixture" }
func (s *erasureFixtureStep) RetireUser(ctx context.Context, req auth.RetirementRequest) (auth.RetirementOutcome, error) {
	out := s.outcome
	err := s.st.Mutate(ctx, req.Tenant, func(sc store.Scope) error {
		var err error
		out.FactVersion, err = auth.PinRetirement(ctx, sc, req)
		return err
	})
	return out, err
}
func erasureFixturePass(t *testing.T, e *consentEstate, user model.ID, step *erasureFixtureStep) auth.RetirementPass {
	t.Helper()
	steps := append(e.pump().steps(), step)
	pass, err := e.eng.authr.AdvanceRetirement(context.Background(), steps, user, e.tT)
	if err != nil {
		t.Fatalf("retirement pass: %v", err)
	}
	return pass
}

func TestSubjectExcludedFromOffboardUntilLift(t *testing.T) {
	onConsentEngines(t, func(t *testing.T, e *consentEstate) {
		ctx := context.Background()
		const email = "subject-fence@consent.test"
		user := e.onboard(e.tT, email, "viewer")
		want := func(tenant model.TenantID, excluded bool) {
			t.Helper()
			got, err := e.eng.authr.SubjectExcluded(ctx, tenant, user)
			if err != nil || got != excluded {
				t.Fatalf("subject excluded in %s=%v, err=%v; want %v", tenant, got, err, excluded)
			}
		}
		want(e.tT, false)
		e.scimDelete(e.tT, user)
		want(e.tT, true)
		want(e.tB, false)
		step := &erasureFixtureStep{st: e.eng.store, outcome: auth.RetirementOutcome{Stores: []auth.RetirementStoreState{{Store: "agent-spool:owned-node", State: "offline"}}}}
		erasureFixturePass(t, e, user, step)
		e.wantBlocked(t, user, e.tT, "agent-spool:owned-node")
		want(e.tT, true)
		step.outcome.Stores[0].State = "clean"
		erasureFixturePass(t, e, user, step)
		e.wantRetired(t, user, e.tT)
		want(e.tT, true)
		if res := e.readmit(e.tT, email, "viewer"); res.code != http.StatusCreated {
			t.Fatalf("readmit completed subject=%d %s", res.code, res.raw)
		}
		want(e.tT, false)
		if _, err := e.eng.authr.SubjectExcluded(ctx, "", user); err == nil {
			t.Error("empty tenant was admitted as not excluded")
		}
		if _, err := e.eng.authr.SubjectExcluded(ctx, e.tT, ""); err == nil {
			t.Error("empty subject was admitted as not excluded")
		}
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := e.eng.authr.SubjectExcluded(cancelled, e.tT, user); !errors.Is(err, context.Canceled) {
			t.Errorf("canceled fence read=%v", err)
		}
	})
}

func TestRetirementStoreStateBlocksUntilClean(t *testing.T) {
	onConsentEngines(t, func(t *testing.T, e *consentEstate) {
		user := e.onboard(e.tT, "store-state@consent.test", "viewer")
		e.scimDelete(e.tT, user)
		step := &erasureFixtureStep{st: e.eng.store}
		for _, state := range []string{"outside_custody", "offline", "unreadable", "unknown", "changing", "not-a-verdict", ""} {
			step.outcome = auth.RetirementOutcome{Stores: []auth.RetirementStoreState{{Store: "audit-archive:owned-sink", State: state, Retention: "30 days"}}, Limits: []string{"text by others not scanned: fixture.reason"}}
			if step.outcome.Clean() {
				t.Fatalf("store state %q counted as clean", state)
			}
			pass := erasureFixturePass(t, e, user, step)
			if pass.Record.RetirementState != model.RetirementBlocked || !strings.Contains(pass.Record.BlockingRefs, "audit-archive:owned-sink") {
				t.Fatalf("store state %q did not block by name: %+v", state, pass.Record)
			}
			var results map[string]struct {
				Stores []auth.RetirementStoreState `json:"stores"`
				Limits []string                    `json:"limits"`
			}
			if err := json.Unmarshal([]byte(pass.Record.ModuleResults), &results); err != nil {
				t.Fatal(err)
			}
			got := results[step.Module()]
			if len(got.Stores) != 1 || got.Stores[0].State != state || got.Stores[0].Retention != "30 days" || len(got.Limits) != 1 {
				t.Fatalf("store/retention/limit lost in receipt: %s", pass.Record.ModuleResults)
			}
			if state == "outside_custody" {
				step.outcome.Stores[0].Retention = "60 days"
				step.outcome.Stores = append(step.outcome.Stores, auth.RetirementStoreState{Store: "agent-spool:online-node", State: "clean"})
				step.outcome.Limits = append(step.outcome.Limits, "text by others not scanned: fixture.summary")
				updated := erasureFixturePass(t, e, user, step)
				if updated.Progress != auth.RetirementAdvanced || updated.Record.BlockingRefs != pass.Record.BlockingRefs {
					t.Fatalf("changed evidence with the same blocker was discarded: %+v", updated)
				}
				if err := json.Unmarshal([]byte(updated.Record.ModuleResults), &results); err != nil {
					t.Fatal(err)
				}
				got = results[step.Module()]
				if len(got.Stores) != 2 || got.Stores[0].Retention != "60 days" || len(got.Limits) != 2 {
					t.Fatalf("changed custody/retention/limits lost in receipt: %s", updated.Record.ModuleResults)
				}
				if unchanged := erasureFixturePass(t, e, user, step); unchanged.Progress != auth.RetirementUnchanged || unchanged.Record.Version != updated.Record.Version {
					t.Fatalf("equal evidence rewrote the blocked record: %+v", unchanged)
				}
			}
		}
		step.outcome.Stores[0] = auth.RetirementStoreState{Store: "audit-archive:owned-sink", State: "clean"}
		if !step.outcome.Clean() {
			t.Fatal("a stated limit blocked the otherwise clean store")
		}
		erasureFixturePass(t, e, user, step)
		e.wantRetired(t, user, e.tT)
		if (auth.RetirementOutcome{Stores: []auth.RetirementStoreState{{State: "clean"}}}).Clean() {
			t.Error("an unnamed store was accepted as a clean absence proof")
		}
	})
}

func TestReadmitRefusedUntilPrivateErasureCompletes(t *testing.T) {
	onConsentEngines(t, func(t *testing.T, e *consentEstate) {
		const email = "private-erasure@consent.test"
		user := e.onboard(e.tT, email, "viewer")
		e.scimDelete(e.tT, user)
		step := &erasureFixtureStep{st: e.eng.store, outcome: auth.RetirementOutcome{Stores: []auth.RetirementStoreState{{Store: "session-cockpit:sqlite:owned-node", State: "changing"}}}}
		erasureFixturePass(t, e, user, step)
		if res := e.readmit(e.tT, email, "viewer"); res.code != http.StatusConflict || res.errorCode() != "retirement_pending" {
			t.Fatalf("readmission before private absence=%d %s", res.code, res.raw)
		}
		if e.memberOf(user, e.tT) {
			t.Fatal("failed re-admission restored membership")
		}
		step.outcome.Stores[0].State = "clean"
		erasureFixturePass(t, e, user, step)
		if res := e.readmit(e.tT, email, "viewer"); res.code != http.StatusCreated {
			t.Fatalf("readmission after private absence=%d %s", res.code, res.raw)
		}
	})
}
