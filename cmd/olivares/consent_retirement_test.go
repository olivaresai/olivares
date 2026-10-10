// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The retirement barrier: an offboard excludes the account at once, each module
// the composition declares removes or reports what names the account in the
// tenant, and only a retired account can be re-admitted. These cases drive the
// product routes and the composition's own retirement pump.

// grantUser creates a tenant-scope user-subject grant of role for user in tenant
// and returns its id.
func (e *consentEstate) grantUser(tenant model.TenantID, user model.ID, role string) string {
	e.t.Helper()
	r := e.do("POST", "/v1/m/governance/rbac/grants", e.admin, tenant, map[string]any{
		"subject_kind": "user", "subject_ref": user.String(), "role": role, "scope_tree": "tenant",
	})
	if r.code != http.StatusCreated {
		e.t.Fatalf("grant %s to %s = %d %s", role, user, r.code, r.raw)
	}
	id, _ := r.body["id"].(string)
	return id
}

// grantCount counts the user-subject grants that name user in tenant.
func (e *consentEstate) grantCount(tenant model.TenantID, user model.ID) int {
	e.t.Helper()
	r := e.do("GET", "/v1/m/governance/rbac/grants", e.admin, tenant, nil)
	if r.code != http.StatusOK {
		e.t.Fatalf("list grants = %d %s", r.code, r.raw)
	}
	return strings.Count(r.raw, `"`+user.String()+`"`)
}

// readmit onboards the address into tenant again at role.
func (e *consentEstate) readmit(tenant model.TenantID, email, role string) consentResp {
	e.t.Helper()
	return e.do("POST", "/v1/onboard", e.admin, tenant, map[string]any{
		"email": email, "role": role, "mode": "password", "password": "readmission-pass-1",
	})
}

// scimCreate provisions an address through the tenant's SCIM connection.
func (e *consentEstate) scimCreate(tenant model.TenantID, email string) consentResp {
	e.t.Helper()
	return e.scim("POST", "/v1/scim/v2/Users", e.scimToken(tenant),
		`{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":"`+email+`","active":true}`)
}

// tryLogin signs in and returns the token, or "" with the status on refusal.
func (e *consentEstate) tryLogin(email, password string) (string, int) {
	e.t.Helper()
	r := e.do("POST", "/v1/auth/login", "", "", map[string]any{"email": email, "password": password})
	tok, _ := r.body["token"].(string)
	return tok, r.code
}

// TestAnOffboardedUsersGrantDoesNotReturnWhenRetirementFailed: while a
// retirement has not completed, no re-admission path joins the account, and the
// account's grant does not authorize it in the tenant.

// TestRetirementSurvivesRestartAndRetries: a retirement whose step failed stays
// pending across a pump restart and completes on a later pass.
func TestRetirementSurvivesRestartAndRetries(t *testing.T) {
	onConsentEngines(t, func(t *testing.T, e *consentEstate) {
		const email = "restarted@consent.test"
		user := e.onboard(e.tT, email, "viewer")
		e.scimDelete(e.tT, user)
		failed := false
		e.pump().beforeStepHook = func(string, auth.RetirementRequest) error {
			if failed {
				return nil
			}
			failed = true
			return errors.New("the module is unavailable")
		}
		e.runPump()
		if r := e.readmit(e.tT, email, "viewer"); r.code != http.StatusConflict {
			t.Errorf("re-admission after a failed pass = %d %s, want 409", r.code, r.raw)
		}

		// A new pump over the same store resumes from the record.
		old := e.pump()
		e.eng.retirementPump = &retirementPump{
			authr: old.authr, modules: old.modules,
			now: func() time.Time { return time.Now().Add(24 * time.Hour) },
		}
		e.runPump()
		rec, found := e.record(user, e.tT)
		if !found || rec.RetirementState != model.RetirementRetired || rec.Attempts != 2 {
			t.Errorf("the record after the restarted pump = %+v (found %t), want retired after 2 attempts", rec, found)
		}
		if r := e.readmit(e.tT, email, "viewer"); r.code != http.StatusCreated {
			t.Errorf("re-admission after the retirement completed = %d %s, want 201", r.code, r.raw)
		}
	})
}

// TestReadmissionAtALesserRoleDoesNotRestoreTheOldGrant: the retirement deletes
// the account's grants in the tenant, so a re-admission at a lesser role gets the
// lesser role only.

// rowsMentioning counts the rows of kind in tenant whose column contains needle.
// A core policy is matched on its whole spec.
func (e *consentEstate) rowsMentioning(tenant model.TenantID, kind model.Kind, column, needle string) int {
	e.t.Helper()
	ctx := context.Background()
	n := 0
	if kind == "core.policy" {
		if err := e.eng.store.View(ctx, tenant, func(sc store.Scope) error {
			q := model.Query{Limit: 500}
			for {
				policies, page, err := sc.Policies().List(ctx, q)
				if err != nil {
					return err
				}
				for _, p := range policies {
					if strings.Contains(fmt.Sprint(p.Spec), needle) {
						n++
					}
				}
				if !page.HasMore || page.Cursor == "" {
					return nil
				}
				q.Cursor = page.Cursor
			}
		}); err != nil {
			e.t.Fatalf("read policies: %v", err)
		}
		return n
	}
	if err := e.eng.store.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(kind)
		if err != nil {
			return err
		}
		q := model.Query{Limit: 500}
		for {
			recs, page, err := repo.List(ctx, q)
			if err != nil {
				return err
			}
			for _, rec := range recs {
				if strings.Contains(rec.String(column), needle) {
					n++
				}
			}
			if !page.HasMore || page.Cursor == "" {
				return nil
			}
			q.Cursor = page.Cursor
		}
	}); err != nil {
		e.t.Fatalf("read %s: %v", kind, err)
	}
	return n
}

// TestADelayedCleanupCannotRemoveANewGrantAndAConcurrentWriterCannotBypassTheBarrier:
// a writer that read the account's standing before an offboard conflicts at its
// barrier and is refused, and a retirement pass that read the record before a
// lift conflicts and removes nothing granted after the lift.

// parkEachWriter parks each writer, in a subtest of its own, between its read
// of the account's standing and its barrier, removes the account from T, and
// releases it: the writer is refused, and no row of its table names the account.
// A writer that checks its recipient's membership again first may be refused by
// that check; every other one answers the standing's refusal.
func parkEachWriter(t *testing.T, e *consentEstate, writers []fenceWriter) {
	t.Helper()
	for i, w := range writers {
		t.Run(w.name+" parked between its standing read and its barrier", func(t *testing.T) {
			e := e.forSubtest(t)
			s := e.fenceSubjectFor(w, fmt.Sprintf("parked-writer-%02d", i))
			write := w.prepare(e, s)
			if n := e.rowsNamingSubject(w, s); n != 0 {
				t.Fatalf("%d %s rows name the account before its writer ran", n, w.kind)
			}
			if e.eng.standing == nil {
				t.Fatal("the composition has no standing port")
			}
			parked := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			e.eng.standing.afterReadHook = func(_ model.TenantID, users []model.ID) {
				if slices.Contains(users, s.id) {
					once.Do(func() {
						close(parked)
						<-release
					})
				}
			}
			defer func() { e.eng.standing.afterReadHook = nil }()
			result := make(chan consentResp, 1)
			go func() { result <- write() }()
			select {
			case <-parked:
			case r := <-result:
				t.Fatalf("the writer never read the account's standing: %d %s", r.code, r.raw)
			case <-time.After(10 * time.Second):
				t.Fatal("the writer never read the account's standing")
			}
			e.scimDelete(e.tT, s.id)
			close(release)
			r := <-result
			switch {
			case w.recheck:
				if r.code < 400 || r.code >= 500 {
					t.Errorf("the parked writer after the offboard = %d %s, want a refusal", r.code, r.raw)
				}
			case r.code != http.StatusConflict || r.errorCode() != "subject_retirement_active":
				t.Errorf("the parked writer after the offboard = %d %s, want 409 subject_retirement_active", r.code, r.raw)
			}
			if n := e.rowsNamingSubject(w, s); n != 0 {
				t.Errorf("the parked writer stored %d %s rows naming the offboarded account", n, w.kind)
			}
		})
	}
}

// rowsNamingSubject counts the rows of w's table in T whose reference column
// holds one of the account's names.
func (e *consentEstate) rowsNamingSubject(w fenceWriter, s fenceSubject) int {
	e.t.Helper()
	n := 0
	for _, alias := range s.aliases() {
		n += e.rowsMentioning(e.tT, w.kind, w.column, alias)
	}
	return n
}

// TestABlockedRetirementBacksOffAndWritesNothingUntilItChanges: a pass that
// finds what a blocked record already lists writes nothing and moves no epoch,
// the pump then leaves the record alone for the backoff of its attempts, and
// once the tenant resolves the blocker a later pass retires it. A pass that read
// the account's authority before it moved writes nothing and counts for
// nothing.
func TestABlockedRetirementBacksOffAndWritesNothingUntilItChanges(t *testing.T) {
	onConsentEngines(t, func(t *testing.T, e *consentEstate) {
		ctx := context.Background()
		p := e.pump()
		steps := 0
		p.beforeStepHook = func(string, auth.RetirementRequest) error {
			steps++
			return nil
		}
		defer func() { p.beforeStepHook = nil }()
		at := func(now time.Time) { p.now = func() time.Time { return now } }

		const email = "backoff@consent.test"
		user := e.onboard(e.tT, email, "viewer")
		e.publishCedar(e.tT, `permit(principal == User::"`+user.String()+`", action, resource);`)
		e.scimDelete(e.tT, user)
		start := time.Now().Add(time.Minute)
		at(start)
		if n, err := p.runOnce(ctx); err != nil || n != 1 {
			t.Fatalf("the first pass = %d, %v; want the record advanced to blocked", n, err)
		}
		e.wantBlocked(t, user, e.tT, "policy")
		blocked, _ := e.record(user, e.tT)
		if blocked.NextAttemptAt == nil || !blocked.NextAttemptAt.Time().After(time.Now()) {
			t.Errorf("a blocked record is due again at once: next attempt %v", blocked.NextAttemptAt)
		}
		epoch := e.directoryEpoch(e.tT)

		// Due again by its own schedule, the record is looked at once more: the
		// pass finds the same blocker and writes nothing.
		later := start.Add(auth.RetirementBackoff(blocked.Attempts) + time.Minute)
		at(later)
		ran := steps
		if n, err := p.runOnce(ctx); err != nil || n != 0 {
			t.Fatalf("the unchanged pass = %d, %v; want nothing advanced", n, err)
		}
		if steps == ran {
			t.Fatalf("the due blocked record was not looked at again")
		}
		again, _ := e.record(user, e.tT)
		if again.Attempts != blocked.Attempts || again.BlockingRefs != blocked.BlockingRefs ||
			again.Version != blocked.Version {
			t.Errorf("an unchanged pass rewrote the record: %+v, want %+v", again, blocked)
		}
		if got := e.directoryEpoch(e.tT); got != epoch {
			t.Errorf("an unchanged pass moved the tenant's directory epoch %d -> %d", epoch, got)
		}

		// Inside the backoff the pump leaves the record alone.
		ran = steps
		if _, err := p.runOnce(ctx); err != nil {
			t.Fatal(err)
		}
		if steps != ran {
			t.Errorf("the pump ran the steps of a record inside its backoff")
		}

		// Once the tenant resolves the blocker, the next pass after the backoff
		// retires the record.
		e.publishCedar(e.tT, `permit(principal in Role::"viewer", action == Action::"agent:read", resource);`)
		at(later.Add(time.Hour + time.Minute))
		if n, err := p.runOnce(ctx); err != nil || n != 1 {
			t.Fatalf("the pass after the resolution = %d, %v; want the record advanced", n, err)
		}
		e.wantRetired(t, user, e.tT)

		t.Run("a pass whose read went stale", func(t *testing.T) {
			const email = "stale@consent.test"
			user := e.onboard(e.tT, email, "viewer")
			e.seedMembership(user, e.tB, "viewer")
			e.scimDelete(e.tT, user)
			before, _ := e.record(user, e.tT)
			moved := false
			p.beforeStepHook = func(_ string, req auth.RetirementRequest) error {
				if req.User == user && !moved {
					moved = true
					// The account's authority moves after the pass read it.
					e.scimDelete(e.tB, user)
				}
				return nil
			}
			advancePumpClock(p)
			if n, err := p.runOnce(ctx); err != nil || n != 0 {
				t.Fatalf("the stale pass = %d, %v; want nothing advanced", n, err)
			}
			if after, _ := e.record(user, e.tT); after.RetirementState != before.RetirementState ||
				after.Attempts != before.Attempts || after.BlockingRefs != before.BlockingRefs {
				t.Errorf("the stale pass wrote the record: %+v, want %+v", after, before)
			}
			e.runPump()
			e.wantRetired(t, user, e.tT)
		})
	})
}

// TestTheRetirementPumpRunsOnlyWhileActiveAndWakesOnAnOffboard: the pump's own
// loop, started as the engine starts it, runs no pass while its process is not
// the active writer, is woken by an offboard through the engine's
// authenticator, and runs a pass once the process is the active writer.
func TestTheRetirementPumpRunsOnlyWhileActiveAndWakesOnAnOffboard(t *testing.T) {
	onConsentEngines(t, func(t *testing.T, e *consentEstate) {
		user := e.onboard(e.tT, "leader@consent.test", "viewer")
		p := e.pump()
		var active atomic.Bool
		asked := make(chan bool, 16)
		gate := func() bool {
			a := active.Load()
			asked <- a
			return a
		}
		ctx, cancel := context.WithCancel(context.Background())
		stopped := make(chan struct{})
		go func() {
			defer close(stopped)
			p.run(ctx, gate)
		}()
		defer func() {
			cancel()
			<-stopped
		}()
		turn := func(want bool) {
			t.Helper()
			select {
			case got := <-asked:
				if got != want {
					t.Fatalf("the pump's turn asked as active=%t, want %t", got, want)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("the pump's loop took no turn")
			}
		}
		turn(false)
		e.scimDelete(e.tT, user)
		turn(false)
		if rec, _ := e.record(user, e.tT); rec.RetirementState != model.RetirementRetiring || rec.Attempts != 0 {
			t.Errorf("a pump that is not the active writer ran a pass: %+v", rec)
		}
		active.Store(true)
		p.wake()
		turn(true)
		eventually(t, "the active pump's pass", func() bool {
			rec, _ := e.record(user, e.tT)
			return rec.RetirementState == model.RetirementRetired
		})
	})
}
