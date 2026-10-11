// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package gitpublish

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	gp "github.com/olivaresai/olivares/connectors/gitpublish"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// shaSession is a commit only the session folder holds; runOne is the run
// whose folder holds it, runOther a run the reader does not know.
const (
	shaSession = "6666666666666666666666666666666666666666"
	runOne     = "0199b0a1-0000-7000-8000-000000000001"
	runOther   = "0199b0a1-0000-7000-8000-000000000002"
)

// fakeSessions answers one run's folder in one workspace, like the confined
// sessions reader: every other question is store.ErrNotFound.
type fakeSessions struct {
	workspace model.ID
	run, dir  string
	err       error
	asked     int
	tenant    model.TenantID
}

func (s *fakeSessions) ReadRunWorkspacePath(_ context.Context, tenant model.TenantID, workspace model.ID, run string) (string, error) {
	s.asked++
	s.tenant = tenant
	if s.err != nil {
		return "", s.err
	}
	if workspace != s.workspace || run != s.run {
		return "", store.ErrNotFound
	}
	return s.dir, nil
}

func sessionHarness(t *testing.T) (*harness, *fakeSessions) {
	h := newHarness(t)
	h.git.sessionTrees = map[string]string{shaSession: shaTree}
	s := &fakeSessions{workspace: h.ws, run: runOne, dir: "/srv/work/run-1"}
	h.m.UseSessions(s)
	return h, s
}

func (h *harness) pushSession(c Caller, op, run string) (Receipt, error) {
	return h.m.Push(context.Background(), c, PushInput{Target: h.target.ID, OperationID: op, Ref: "refs/heads/olivares/run-1", Commit: shaSession, Tree: shaTree, SessionRun: run})
}

func TestPushNamingASessionRunFeedsTheManagedRepository(t *testing.T) {
	h, s := sessionHarness(t)
	// Without the run, the commit is not in the managed repository.
	if _, err := h.pushSession(h.user(), "op-plain", ""); codeOf(err) != "content_mismatch" {
		t.Fatalf("push without a session run: %v, want content_mismatch", err)
	}
	if len(h.git.fetches) != 0 {
		t.Fatalf("a push naming no run fetched: %v", h.git.fetches)
	}
	r, err := h.pushSession(h.user(), "op-session", runOne)
	if err != nil || r.Intent.State != StateApplied {
		t.Fatalf("push = %+v %v, want applied", r.Intent, err)
	}
	want := "/srv/olivares/gitpublish/r1.git|/srv/work/run-1|" + shaSession
	if len(h.git.fetches) != 1 || h.git.fetches[0] != want {
		t.Fatalf("fetches = %v, want [%s]", h.git.fetches, want)
	}
	if got := h.host.ref("olivares/run-1"); got != shaSession {
		t.Fatalf("host ref = %q, want %s", got, shaSession)
	}
	if s.tenant != h.tenant {
		t.Fatalf("the run was read in tenant %q, want %q", s.tenant, h.tenant)
	}
	// The run is transport, not request semantics: a replay naming the same,
	// another or no run answers the intent without fetching again.
	for _, run := range []string{runOne, runOther, ""} {
		again, err := h.pushSession(h.user(), "op-session", run)
		if err != nil || again.Intent.ID != r.Intent.ID || len(h.git.fetches) != 1 {
			t.Fatalf("replay with run %q = %+v %v, fetches %d", run, again.Intent, err, len(h.git.fetches))
		}
	}
	h.balanced()
}

func TestPushNamingASessionRunRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		run      string
		readErr  error
		fetchErr error
		unbound  bool
		blocks   bool
		code     string
	}{
		"run outside the target's workspace":   {run: runOther, code: "session_source_refused"},
		"folder is not an admitted repository": {run: runOne, fetchErr: gp.ErrSource, code: "session_source_refused"},
		"commit not in the session":            {run: runOne, fetchErr: gp.ErrContent, code: "content_mismatch"},
		"managed repository tampered":          {run: runOne, fetchErr: gp.ErrRepositoryConfig, code: "repository_config_refused"},
		"fetch could not run":                  {run: runOne, fetchErr: errors.New("staging failed"), code: "session_source_unavailable"},
		"sessions reader failed":               {run: runOne, readErr: errors.New("store down"), code: "session_source_unavailable"},
		"no sessions reader":                   {run: runOne, unbound: true, code: "session_source_unavailable"},
		"fetch outlives the admission window":  {run: runOne, blocks: true, code: "session_source_unavailable"},
		"not a run reference":                  {run: "run-1", code: "invalid_request"},
		"non-canonical run reference":          {run: "{" + runOne + "}", code: "invalid_request"},
	} {
		t.Run(name, func(t *testing.T) {
			h, s := sessionHarness(t)
			s.err, h.git.fetchErr, h.git.fetchBlocks = tc.readErr, tc.fetchErr, tc.blocks
			h.m.opts.AdmissionTimeout = 200 * time.Millisecond
			if tc.unbound {
				h.m.UseSessions(nil)
			}
			if _, err := h.pushSession(h.user(), "op-refused", tc.run); codeOf(err) != tc.code {
				t.Fatalf("err = %v, want %s", err, tc.code)
			}
			intents, err := h.m.Intents(context.Background(), h.user(), h.target.ID)
			if err != nil || len(intents) != 0 || h.git.count() != 0 || h.host.mints != 0 || h.host.ref("olivares/run-1") != "" {
				t.Fatalf("a refused feed went on: %d intents (%v), %d pushes, %d mints", len(intents), err, h.git.count(), h.host.mints)
			}
			if strings.HasPrefix(name, "not a") || strings.HasPrefix(name, "non-canonical") {
				if s.asked != 0 || len(h.git.fetches) != 0 {
					t.Fatalf("an invalid run reference was read: asked %d, fetches %v", s.asked, h.git.fetches)
				}
			}
			h.balanced()
		})
	}
}

func TestSessionCredentialCannotFeedOrPush(t *testing.T) {
	h, s := sessionHarness(t)
	c := h.caller(auth.KindToken, "", model.NewID(), 0)
	c.Principal.SessionIdentity = "sid-1"
	if _, err := h.pushSession(c, "op-rt", runOne); codeOf(err) != "runtime_credential_refused" {
		t.Fatalf("err = %v, want runtime_credential_refused", err)
	}
	if s.asked != 0 || len(h.git.fetches) != 0 {
		t.Fatalf("a session credential reached the feed: asked %d, fetches %v", s.asked, h.git.fetches)
	}
}

func TestRefusedTargetRuleNeverReadsTheSessionFolder(t *testing.T) {
	h, s := sessionHarness(t)
	in := PushInput{Target: h.target.ID, OperationID: "op-rule", Ref: "refs/heads/main", Commit: shaSession, Tree: shaTree, SessionRun: runOne}
	if _, err := h.m.Push(context.Background(), h.user(), in); err == nil {
		t.Fatal("a push to a merge base succeeded")
	}
	if s.asked != 0 || len(h.git.fetches) != 0 {
		t.Fatalf("a push refused by the target's rules reached the feed: asked %d, fetches %v", s.asked, h.git.fetches)
	}
}

func TestDeniedPushNeverReadsTheSessionFolder(t *testing.T) {
	h, s := sessionHarness(t)
	h.authz.deny[h.ws] = auth.ErrRouteDenied
	if _, err := h.pushSession(h.user(), "op-denied", runOne); err == nil {
		t.Fatal("a denied push succeeded")
	}
	if s.asked != 0 || len(h.git.fetches) != 0 {
		t.Fatalf("an unauthorized push reached the feed: asked %d, fetches %v", s.asked, h.git.fetches)
	}
}

func TestPushRouteAcceptsASessionRunAndNeverAPath(t *testing.T) {
	h, _ := sessionHarness(t)
	push := handlerFor(t, h.m, http.MethodPost, "/targets/{id}/pushes")
	body := map[string]any{"operation_id": "op-route", "ref": "refs/heads/olivares/run-1", "commit": shaSession, "tree": shaTree, "session_run": runOne}
	rec := call(push, h.user(), http.MethodPost, h.target.ID.String(), body)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"state":"applied"`) {
		t.Fatalf("push with a session run: %d %s", rec.Code, rec.Body.String())
	}
	if raw := rec.Body.String(); strings.Contains(raw, "/srv/work") || strings.Contains(raw, runOne) {
		t.Fatalf("the receipt carries the session folder or run: %s", raw)
	}
	body["operation_id"], body["session_run"] = "op-path", "/srv/work/run-1"
	if rec := call(push, h.user(), http.MethodPost, h.target.ID.String(), body); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_request") {
		t.Fatalf("push naming a path as the run: %d %s", rec.Code, rec.Body.String())
	}
}
