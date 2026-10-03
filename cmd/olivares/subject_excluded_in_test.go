// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestSubjectExcludedInAgreesWithSubjectExcluded(t *testing.T) {
	onConsentEngines(t, func(t *testing.T, e *consentEstate) {
		ctx := context.Background()
		const email = "transaction-fence@consent.test"
		user := e.onboard(e.tT, email, "viewer")
		want := func(tenant model.TenantID, excluded bool) {
			t.Helper()
			standalone, err := e.eng.authr.SubjectExcluded(ctx, tenant, user)
			if err != nil || standalone != excluded {
				t.Fatalf("standalone exclusion=%v err=%v; want %v", standalone, err, excluded)
			}
			err = e.eng.store.AuthMutate(ctx, func(as store.AuthScope) error {
				transactional, err := auth.SubjectExcludedIn(ctx, as, tenant, user)
				if err != nil || transactional != excluded || transactional != standalone {
					t.Fatalf("transactional exclusion=%v err=%v; standalone=%v want=%v", transactional, err, standalone, excluded)
				}
				if _, err := auth.SubjectExcludedIn(ctx, as, "", user); err == nil {
					t.Error("empty tenant accepted by the transaction port")
				}
				if _, err := auth.SubjectExcludedIn(ctx, as, tenant, ""); err == nil {
					t.Error("empty user accepted by the transaction port")
				}
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				if _, err := auth.SubjectExcludedIn(cancelled, as, tenant, user); !errors.Is(err, context.Canceled) {
					t.Errorf("canceled transactional read=%v", err)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		want(e.tT, false)
		e.scimDelete(e.tT, user)
		if rec, found := e.record(user, e.tT); !found || rec.RetirementState != model.RetirementRetiring {
			t.Fatal("offboard did not enter retiring")
		}
		want(e.tT, true)
		want(e.tB, false)
		step := &erasureFixtureStep{st: e.eng.store, outcome: auth.RetirementOutcome{Stores: []auth.RetirementStoreState{{Store: "ldap:owned-directory", State: "offline"}}}}
		erasureFixturePass(t, e, user, step)
		e.wantBlocked(t, user, e.tT, "ldap:owned-directory")
		want(e.tT, true)
		step.outcome.Stores[0].State = "clean"
		erasureFixturePass(t, e, user, step)
		e.wantRetired(t, user, e.tT)
		want(e.tT, true)
		if res := e.readmit(e.tT, email, "viewer"); res.code != http.StatusCreated {
			t.Fatalf("readmit=%d %s", res.code, res.raw)
		}
		if rec, found := e.record(user, e.tT); !found || rec.RetirementState != model.RetirementLifted {
			t.Fatal("readmission did not lift the fence")
		}
		want(e.tT, false)
	})
}
