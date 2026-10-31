// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package gitpublish

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gp "github.com/olivaresai/olivares/connectors/gitpublish"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// fakeWork is the sessions module's CommitRun: it commits the run's folder and
// names what to publish, or refuses as the folder does.
type fakeWork struct {
	head    gp.LocalHead
	err     error
	calls   int
	message string
	run     string
}

func (w *fakeWork) CommitRun(_ context.Context, _ auth.Principal, _ model.TenantID, _ model.ID, run, message string) (gp.LocalHead, error) {
	w.calls++
	w.run, w.message = run, message
	return w.head, w.err
}

// fakeGate is the approval policy's door: Authorize answers what the tenant's
// policy and its people have decided so far.
type fakeGate struct {
	state string // "" = no policy asks for review
	ref   string
	err   error
	asked []GateRequest
}

func (g *fakeGate) Authorize(_ context.Context, _ model.TenantID, r GateRequest) (GateDecision, error) {
	g.asked = append(g.asked, r)
	if g.err != nil {
		return GateDecision{}, g.err
	}
	if g.state == "" {
		return GateDecision{State: GateGranted}, nil
	}
	return GateDecision{Ref: g.ref, State: g.state}, nil
}

func shipHarness(t *testing.T) (*harness, *fakeWork, *fakeGate) {
	h, _ := sessionHarness(t)
	h.git.changed = []string{"internal/feature.go"}
	h.custody.webBase = "https://github.com/acme/widgets"
	w := &fakeWork{head: gp.LocalHead{Branch: "olivares/run-1", Commit: shaSession, Tree: shaTree, Committed: true}}
	g := &fakeGate{}
	h.m.UseSessionWork(w)
	h.m.UseShipGate(g)
	return h, w, g
}

func (h *harness) ship(c Caller, op string) (ShipResult, error) {
	return h.m.Ship(context.Background(), c, ShipInput{Target: h.target.ID, OperationID: op, SessionRun: runOne, Message: "Add the feature", Title: "Add the feature", Body: "Why.", Base: "main"})
}

func TestShipCommitsPushesAndOpensThePullRequest(t *testing.T) {
	h, work, _ := shipHarness(t)
	res, err := h.ship(h.user(), "ship-1")
	if err != nil {
		t.Fatal(err)
	}
	if res.State != ShipShipped || res.Branch != "olivares/run-1" || res.Commit != shaSession || !res.Committed {
		t.Fatalf("result = %+v", res)
	}
	if work.calls != 1 || work.run != runOne || work.message != "Add the feature" {
		t.Fatalf("commit step = %+v", work)
	}
	if got := h.host.ref("olivares/run-1"); got != shaSession {
		t.Fatalf("host ref = %q, want the session commit", got)
	}
	if res.Push == nil || res.Push.Intent.State != StateApplied || res.PullRequest == nil || res.PullRequest.Intent.State != StateApplied {
		t.Fatalf("receipts = %+v", res)
	}
	if h.host.creates != 1 || res.PullRequest.Intent.Observed.Number != 101 {
		t.Fatalf("pull request creates = %d, receipt %+v", h.host.creates, res.PullRequest.Intent.Observed)
	}
	if res.PullRequestURL != "https://github.com/acme/widgets/pull/101" {
		t.Fatalf("url = %q", res.PullRequestURL)
	}
	h.balanced()
}

func TestShipRetryReplaysInsteadOfPushingAgain(t *testing.T) {
	h, _, _ := shipHarness(t)
	first, err := h.ship(h.user(), "ship-1")
	if err != nil {
		t.Fatal(err)
	}
	again, err := h.ship(h.user(), "ship-1")
	if err != nil || again.State != ShipShipped || again.Push.Intent.ID != first.Push.Intent.ID || again.PullRequest.Intent.ID != first.PullRequest.Intent.ID {
		t.Fatalf("retry = %+v %v", again, err)
	}
	if h.git.count() != 1 || h.host.creates != 1 {
		t.Fatalf("retry pushed %d times and opened %d pull requests", h.git.count(), h.host.creates)
	}
}

// An approval is spent by the push it allowed: a retry of that ship replays the
// receipt and is not asked to be approved again, while a new ship of the same
// commit is.
func TestShipRetryOfAnApprovedShipNeedsNoSecondApproval(t *testing.T) {
	h, _, gate := shipHarness(t)
	gate.state, gate.ref = GateGranted, "appr-1"
	if _, err := h.ship(h.user(), "ship-1"); err != nil {
		t.Fatal(err)
	}
	gate.state = GateDenied // the approval is spent now
	again, err := h.ship(h.user(), "ship-1")
	if err != nil || again.State != ShipShipped || len(gate.asked) != 1 {
		t.Fatalf("retry = %+v %v, gate asked %d times", again, err, len(gate.asked))
	}
	if _, err := h.ship(h.user(), "ship-2"); codeOf(err) != "approval_denied" || len(gate.asked) != 2 {
		t.Fatalf("a new ship of the same commit: %v", err)
	}
}

func TestShipAgainAfterMoreWorkLeasesTheBranchTip(t *testing.T) {
	h, work, _ := shipHarness(t)
	if _, err := h.ship(h.user(), "ship-1"); err != nil {
		t.Fatal(err)
	}
	h.git.sessionTrees[shaOther] = shaTree
	work.head.Commit = shaOther
	// Like the host, the open pull request follows its branch.
	h.git.afterApply = func() {
		h.host.mu.Lock()
		defer h.host.mu.Unlock()
		for i := range h.host.changes {
			h.host.changes[i].HeadSHA = h.host.refs[h.host.changes[i].HeadRef]
		}
	}
	res, err := h.ship(h.user(), "ship-2")
	if err != nil || res.State != ShipShipped {
		t.Fatalf("second ship = %+v %v", res, err)
	}
	if got := h.host.ref("olivares/run-1"); got != shaOther {
		t.Fatalf("host ref = %q, want the new commit", got)
	}
	if h.git.lastReq.ExpectedOld != shaSession {
		t.Fatalf("lease = %q, want the branch's tip %s", h.git.lastReq.ExpectedOld, shaSession)
	}
	if h.host.creates != 1 || res.PullRequest.Intent.Observed.Number != 101 {
		t.Fatalf("the open pull request was not adopted: creates %d, %+v", h.host.creates, res.PullRequest.Intent.Observed)
	}
}

// A ship that cannot publish must not leave the session's folder changed when
// the refusal does not need the commit: those are answered before it.
func TestShipRefusalsNameTheMissingThing(t *testing.T) {
	for name, tc := range map[string]struct {
		setup      func(h *harness, w *fakeWork)
		code       string
		committed  bool
		wantStatus int
	}{
		"credential binding is not approved": {setup: func(h *harness, _ *fakeWork) { h.custody.approved["cb1"] = false }, code: "binding_not_approved", wantStatus: http.StatusUnprocessableEntity},
		"session is in another workspace": {setup: func(h *harness, _ *fakeWork) {
			h.m.UseSessions(&fakeSessions{workspace: model.NewID(), run: runOne, dir: "/x"})
		}, code: "session_source_refused", wantStatus: http.StatusUnprocessableEntity},
		"folder is on no branch":          {setup: func(_ *harness, w *fakeWork) { w.err = gp.ErrNoBranch }, code: "no_branch", committed: true, wantStatus: http.StatusConflict},
		"branch has no commit":            {setup: func(_ *harness, w *fakeWork) { w.err = gp.ErrNoCommit }, code: "no_commit", committed: true, wantStatus: http.StatusConflict},
		"folder refuses the commit":       {setup: func(_ *harness, w *fakeWork) { w.err = errors.Join(gp.ErrLocalInput, errors.New("read-only")) }, code: "commit_refused", committed: true, wantStatus: http.StatusUnprocessableEntity},
		"commit adds nothing to the base": {setup: func(h *harness, _ *fakeWork) { h.git.changed = nil }, code: "nothing_to_ship", committed: true, wantStatus: http.StatusConflict},
		"commit is not in the session": {setup: func(h *harness, w *fakeWork) { w.head.Commit = shaOther; h.git.sessionTrees = nil }, code: "content_mismatch", committed: true,
			wantStatus: http.StatusUnprocessableEntity},
	} {
		t.Run(name, func(t *testing.T) {
			h, work, _ := shipHarness(t)
			tc.setup(h, work)
			_, err := h.ship(h.user(), "ship-r")
			var e *Error
			if !errors.As(err, &e) || e.Code != tc.code || e.Status != tc.wantStatus {
				t.Fatalf("err = %v, want %s (%d)", err, tc.code, tc.wantStatus)
			}
			if (work.calls == 1) != tc.committed {
				t.Fatalf("commit step ran %d times, want it to run = %v", work.calls, tc.committed)
			}
			if h.git.count() != 0 || h.host.creates != 0 {
				t.Fatalf("a refused ship pushed %d times and opened %d pull requests", h.git.count(), h.host.creates)
			}
			h.balanced()
		})
	}
}

func TestShipNeedsAPersonWithPublishAuthority(t *testing.T) {
	h, work, _ := shipHarness(t)
	runtime := h.caller(auth.KindUser, userA, credA, 1)
	runtime.Principal.SessionIdentity = "sess-1"
	if _, err := h.ship(runtime, "ship-s"); codeOf(err) != "runtime_credential_refused" {
		t.Fatalf("session credential: %v", err)
	}
	h.authz.deny[h.ws] = auth.ErrRouteDenied
	if _, err := h.ship(h.user(), "ship-d"); codeOf(err) != "not_found" {
		t.Fatalf("denied caller: %v", err)
	}
	delete(h.authz.deny, h.ws)
	// A caller who may push but not open a pull request is refused before the commit.
	h.authz.denyPermission = map[auth.Permission]error{permPullRequest: auth.ErrRouteDenied}
	if _, err := h.ship(h.user(), "ship-p"); codeOf(err) != "not_found" {
		t.Fatalf("no pull request authority: %v", err)
	}
	if work.calls != 0 {
		t.Fatalf("a refused caller changed the session folder (%d commits)", work.calls)
	}
}

func TestShipWaitsForTheSecondPersonWhenAPolicyAsksForReview(t *testing.T) {
	h, work, gate := shipHarness(t)
	gate.state, gate.ref = GatePending, "appr-1"
	res, err := h.ship(h.user(), "ship-a")
	if err != nil || res.State != ShipAwaitingApproval || res.Approval != "appr-1" || res.Push != nil || res.PullRequest != nil {
		t.Fatalf("pending = %+v %v", res, err)
	}
	if h.git.count() != 0 || h.host.creates != 0 {
		t.Fatal("something was published before the second person decided")
	}
	if len(gate.asked) != 1 {
		t.Fatalf("gate asked %d times", len(gate.asked))
	}
	ask := gate.asked[0]
	if ask.Action != "gitpublish.push" || ask.SubjectKind != "gitpublish.target" || ask.SubjectRef != h.target.ID.String() || ask.ConsumerID != "ship-a" || ask.RequestedBy == "" || ask.PlanHash == "" {
		t.Fatalf("gate request = %+v", ask)
	}
	// The plan hash binds the exact push: another commit is another approval.
	work.head.Commit = shaOther
	h.git.sessionTrees[shaOther] = shaTree
	if _, err := h.ship(h.user(), "ship-b"); err != nil {
		t.Fatal(err)
	}
	if gate.asked[1].PlanHash == ask.PlanHash {
		t.Fatal("two different commits share one approval plan")
	}
	// The second person approves the first plan: the same ship now publishes.
	work.head.Commit = shaSession
	gate.state = GateGranted
	res, err = h.ship(h.user(), "ship-a")
	if err != nil || res.State != ShipShipped || h.git.count() != 1 {
		t.Fatalf("approved = %+v %v, pushes %d", res, err, h.git.count())
	}
	if gate.asked[2].PlanHash != ask.PlanHash {
		t.Fatal("the approval was asked for another plan than the one reviewed")
	}
}

func TestShipRefusedByTheReviewerOrTheGate(t *testing.T) {
	h, _, gate := shipHarness(t)
	gate.state, gate.ref = GateDenied, "appr-1"
	if _, err := h.ship(h.user(), "ship-x"); codeOf(err) != "approval_denied" {
		t.Fatalf("declined: %v", err)
	}
	gate.state, gate.err = "", errors.New("approval service down")
	if _, err := h.ship(h.user(), "ship-y"); codeOf(err) != "approval_unavailable" {
		t.Fatalf("gate unreadable: %v", err)
	}
	if h.git.count() != 0 || h.host.creates != 0 {
		t.Fatal("a refused approval published")
	}
}

func TestShipStopsAfterAPushThatDidNotApply(t *testing.T) {
	h, _, _ := shipHarness(t)
	// The host holds the branch but hides it from the observation, so the ship
	// leases "absent" and git refuses the push as stale.
	h.host.refs["olivares/run-1"] = shaOther
	h.host.hidden["olivares/run-1"] = true
	res, err := h.ship(h.user(), "ship-z")
	if err != nil || res.State != ShipIncomplete || res.Push == nil || res.Push.Intent.State != StateRejected || res.PullRequest != nil {
		t.Fatalf("result = %+v %v", res, err)
	}
	if h.host.creates != 0 {
		t.Fatal("a pull request was opened for a push that did not apply")
	}
}

func TestShipRouteTakesNoPathOrSecret(t *testing.T) {
	h, _, _ := shipHarness(t)
	ship := handlerFor(t, h.m, http.MethodPost, "/targets/{id}/ships")
	body := map[string]any{"operation_id": "op-route", "session_run": runOne, "message": "m", "title": "t", "base": "main"}
	rec := call(ship, h.user(), http.MethodPost, h.target.ID.String(), body)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"state":"shipped"`) || !strings.Contains(rec.Body.String(), `/pull/101`) {
		t.Fatalf("ship: %d %s", rec.Code, rec.Body.String())
	}
	for _, field := range []string{"path", "commit", "credential_binding_id", "ref"} {
		body["operation_id"], body[field] = "op-"+field, "x"
		if rec := call(ship, h.user(), http.MethodPost, h.target.ID.String(), body); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "field_not_accepted") {
			t.Fatalf("%s: %d %s", field, rec.Code, rec.Body.String())
		}
	}
}

// The Session screen finds the pull request of a shipped session again from the
// target's intents: the landed one carries its page on the host; a push or a
// repository whose address is unknown carries none.
func TestShippedPullRequestKeepsItsPageOnTheIntents(t *testing.T) {
	h, _, _ := shipHarness(t)
	if _, err := h.ship(h.user(), "ship-1"); err != nil {
		t.Fatal(err)
	}
	list := handlerFor(t, h.m, http.MethodGet, "/intents")
	rec := callQuery(list, h.user(), "target_id="+h.target.ID.String())
	body := rec.Body.String()
	if rec.Code != http.StatusOK || strings.Count(body, `"url":"https://github.com/acme/widgets/pull/101"`) != 1 {
		t.Fatalf("intents: %d %s", rec.Code, body)
	}
	h.custody.webBaseErr = errors.New("unknown")
	if rec := callQuery(list, h.user(), "target_id="+h.target.ID.String()); strings.Contains(rec.Body.String(), `"url"`) {
		t.Fatalf("a page was named without a known address: %s", rec.Body.String())
	}
}

func callQuery(h api.ModuleHandler, c Caller, query string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/x?"+query, nil)
	rec := httptest.NewRecorder()
	h(rec, req, api.ModuleContext{Principal: c.Principal, Tenant: c.Tenant})
	return rec
}
