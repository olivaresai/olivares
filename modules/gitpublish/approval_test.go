// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package gitpublish

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Every route that publishes asks the tenant's approval policy, as a ship
// does: a declined approval answers 403 approval_denied, a pending one 202
// with the ship's awaiting_approval answer, and nothing reaches the host. A
// session credential is refused on each of them. The routes are the module's
// own sealed effect routes, so a publishing route added later without a case
// here fails.
func TestEveryPublishingRouteAsksTheShipApprovalPolicy(t *testing.T) {
	type walk struct {
		action string
		body   func(h *harness, op string) map[string]any
	}
	cases := map[string]walk{
		"/targets/{id}/pushes": {"gitpublish.push", func(_ *harness, op string) map[string]any {
			return map[string]any{"operation_id": op, "ref": "refs/heads/olivares/walk", "commit": shaCommit, "tree": shaTree}
		}},
		"/targets/{id}/ships": {"gitpublish.push", func(_ *harness, op string) map[string]any {
			return map[string]any{"operation_id": op, "session_run": runOne, "message": "Add the feature", "title": "Add the feature", "base": "main"}
		}},
		"/targets/{id}/pull-requests": {"gitpublish.pull_request", func(_ *harness, op string) map[string]any {
			return map[string]any{"operation_id": op, "head_ref": "olivares/pr", "base": "main", "commit": shaCommit, "title": "t"}
		}},
		"/targets/{id}/merges": {"gitpublish.merge", func(h *harness, op string) map[string]any {
			openPR(h, 7, shaCommit)
			return map[string]any{"operation_id": op, "number": 7, "expected_head": shaCommit, "method": "merge"}
		}},
		"/targets/{id}/issue-comments": {"gitpublish.issue_comment", func(_ *harness, op string) map[string]any {
			return map[string]any{"operation_id": op, "number": 3, "state": "completed"}
		}},
	}
	var routes []mounted
	New(Options{}).APIRoutes(sealedRecorder{routes: &routes})
	walked := 0
	for _, r := range routes {
		if r.method != http.MethodPost || !strings.HasPrefix(r.pattern, "/targets/{id}/") {
			continue
		}
		wc, ok := cases[r.pattern]
		if !ok {
			t.Fatalf("publishing route %s has no approval walk", r.pattern)
		}
		walked++
		for _, state := range []string{GateDenied, GatePending, "session"} {
			h, _, _ := shipHarness(t)
			h.host.refs["olivares/pr"] = shaCommit
			gate := &fakeGate{state: state, ref: "appr-walk"}
			c := h.admin()
			if state == "session" {
				gate.state = ""
				c.Principal.SessionRunRef = runOne
			}
			h.m.UseShipGate(gate)
			rec := call(handlerFor(t, h.m, r.method, r.pattern), c, r.method, h.target.ID.String(), wc.body(h, "walk-op"))
			var out map[string]any
			_ = json.Unmarshal(rec.Body.Bytes(), &out)
			errCode := ""
			if e, ok := out["error"].(map[string]any); ok {
				errCode, _ = e["code"].(string)
			}
			switch state {
			case GateDenied:
				if rec.Code != http.StatusForbidden || errCode != "approval_denied" {
					t.Errorf("%s declined: %d %s", r.pattern, rec.Code, rec.Body.String())
				}
			case GatePending:
				if rec.Code != http.StatusAccepted || out["state"] != ShipAwaitingApproval || out["approval_id"] != "appr-walk" {
					t.Errorf("%s pending: %d %s", r.pattern, rec.Code, rec.Body.String())
				}
			default:
				if rec.Code != http.StatusForbidden || errCode != "runtime_credential_refused" || len(gate.asked) != 0 {
					t.Errorf("%s session credential: %d %s (gate asked %d)", r.pattern, rec.Code, rec.Body.String(), len(gate.asked))
				}
			}
			if state != "session" {
				if len(gate.asked) != 1 {
					t.Errorf("%s %s: gate asked %d times, want once", r.pattern, state, len(gate.asked))
				} else if ask := gate.asked[0]; ask.Action != wc.action || ask.SubjectKind != "gitpublish.target" || ask.SubjectRef != h.target.ID.String() ||
					ask.ConsumerID != "walk-op" || ask.RequestedBy != c.Principal.Actor() || ask.PlanHash == "" {
					t.Errorf("%s %s: gate request = %+v, want action %s", r.pattern, state, ask, wc.action)
				}
			}
			if h.git.count() != 0 || h.host.creates != 0 || h.host.merges != 0 || len(h.host.comments) != 0 {
				t.Errorf("%s %s published without an approval", r.pattern, state)
			}
		}
	}
	if walked != len(cases) {
		t.Fatalf("walked %d publishing routes, want %d", walked, len(cases))
	}
}

// A publication that names a session run is its launcher's, or an
// administrator's who also holds sessions:run:admin; anyone else is answered as
// for a run in another workspace, before the folder is committed or read and
// before anything reaches the host. An administrator's publication names the
// launcher next to the administrator.
func TestSessionRunPublishesOnlyForItsLauncherOrARunAdministrator(t *testing.T) {
	h, work, _ := shipHarness(t)
	sessions := h.m.opts.Sessions.(*fakeSessions)
	sessions.launcher = userA
	h.authz.denyPermission = map[auth.Permission]error{"sessions:run:admin": auth.ErrRouteDenied}
	ctx := context.Background()

	// Another member of the workspace, with the publishing right but not the run.
	member := h.caller(auth.KindUser, model.NewID(), model.NewID(), 1)
	if _, err := h.pushSession(member, "op-member", runOne); codeOf(err) != "session_source_refused" {
		t.Fatalf("member push: %v, want session_source_refused, as for a run that is not there", err)
	}
	if _, err := h.ship(member, "ship-member"); codeOf(err) != "session_source_refused" {
		t.Fatalf("member ship: %v, want session_source_refused, as for a run that is not there", err)
	}
	if _, err := h.m.PushChanges(ctx, member, h.ws, PushInput{Target: h.target.ID, Ref: "refs/heads/olivares/run-1", Commit: shaSession, Tree: shaTree, SessionRun: runOne}); codeOf(err) != "session_source_refused" {
		t.Fatalf("member push changes: %v, want session_source_refused, as for a run that is not there", err)
	}
	if work.calls != 0 || len(h.git.fetches) != 0 || h.git.count() != 0 || h.host.mints != 0 {
		t.Fatalf("a member reached the run: commits %d, fetches %v, pushes %d, mints %d", work.calls, h.git.fetches, h.git.count(), h.host.mints)
	}

	// The launcher publishes their own run.
	own, err := h.pushSession(h.user(), "op-own", runOne)
	if err != nil || own.Intent.State != StateApplied {
		t.Fatalf("launcher push = %+v %v", own.Intent, err)
	}

	// An administrator without sessions:run:admin is another member.
	admin := h.admin()
	delete(h.host.refs, "olivares/run-1") // so the administrator's push is a new one
	if _, err := h.pushSession(admin, "op-admin-no-run-right", runOne); codeOf(err) != "session_source_refused" {
		t.Fatalf("admin without sessions:run:admin: %v, want session_source_refused", err)
	}
	// With it, the administrator publishes, and the audit names both.
	h.authz.denyPermission = nil
	r, err := h.pushSession(admin, "op-admin", runOne)
	if err != nil || r.Intent.State != StateApplied {
		t.Fatalf("run administrator push = %+v %v", r.Intent, err)
	}
	// A walked event carries no Meta by design; the stored canonical metadata is read.
	named := false
	if err := h.m.data.View(ctx, h.tenant, func(sc store.Scope) error {
		walker, ok := sc.Audit().(store.CanonicalWalker)
		if !ok {
			t.Fatal("canonical audit read unavailable")
		}
		return walker.WalkCanonical(ctx, 0, func(e model.AuditEvent, meta string, _ []byte) error {
			if e.TargetID != r.Intent.ID || e.Action != "gitpublish.push.admitted" {
				return nil
			}
			var fields map[string]any
			if err := json.Unmarshal([]byte(meta), &fields); err != nil {
				return err
			}
			named = e.Actor == admin.Principal.Actor() && fields["session_launcher"] == userA.String()
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	if !named {
		t.Fatal("the administrator's publication does not name both the administrator and the launcher")
	}
}

// TestAnotherPersonsRunAnswersAsAMissingRun: whatever refuses the run administrator's
// right (a denial, a scoped grant it lacks, an undecided route), naming another person's
// run answers byte for byte what naming a run that is not there answers, so a member
// cannot tell the two apart.
func TestAnotherPersonsRunAnswersAsAMissingRun(t *testing.T) {
	for _, denial := range []error{auth.ErrRouteDenied, auth.ErrScopedGrantRequired, auth.ErrRouteUndecided} {
		h, _, _ := shipHarness(t)
		h.m.opts.Sessions.(*fakeSessions).launcher = userA
		h.authz.denyPermission = map[auth.Permission]error{"sessions:run:admin": denial}
		member := h.caller(auth.KindUser, model.NewID(), model.NewID(), 1)
		_, another := h.pushSession(member, "op-another", runOne)
		_, missing := h.pushSession(member, "op-missing", model.NewID().String())
		a, errA := json.Marshal(another)
		b, errB := json.Marshal(missing)
		if errA != nil || errB != nil || string(a) != string(b) || codeOf(another) != "session_source_refused" {
			t.Errorf("%v: another person's run = %s, a missing run = %s; want the same session_source_refused", denial, a, b)
		}
	}
}

// TestThePushPlanBindsTheLease: an approval is for one exact push, the lease included, so
// a push of the same commit over another expected tip asks again.
func TestThePushPlanBindsTheLease(t *testing.T) {
	a := PushInput{Target: model.NewID(), Ref: "refs/heads/olivares/x", Commit: shaSession, Tree: shaTree}
	b := a
	b.ExpectedOld = shaCommit
	if shipPlan(a) == shipPlan(b) {
		t.Fatalf("the plan of a push does not bind its lease: %s", shipPlan(a))
	}
}
