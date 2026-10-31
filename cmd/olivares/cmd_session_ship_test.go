// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

const shipTargetPath = "/v1/m/gitpublish/targets/tg1/ships"

func shipEngine(t *testing.T, status int, answer map[string]any, targets ...map[string]any) *fakeSessionEngine {
	t.Helper()
	return newFakeSessionEngine(t, func(f *fakeSessionEngine) {
		f.runs = []map[string]any{{"run_ref": "r1", "name": "feature", "state": "stopped", "authz_workspace_id": "ws1", "worktree_branch": "olivares/feature"}}
		f.shipTargets = targets
		f.shipStatus, f.shipAnswer = status, answer
	})
}

var shipTarget1 = map[string]any{"id": "tg1", "workspace_id": "ws1", "push_prefix": "olivares/"}

func TestSessionShipCommitsPushesAndSaysWhereThePullRequestIs(t *testing.T) {
	f := shipEngine(t, 200, map[string]any{"state": "shipped", "branch": "olivares/feature", "commit": "0123456789abcdef0123456789abcdef01234567", "committed": true,
		"pull_request_url": "https://github.com/acme/widgets/pull/7", "push": map[string]any{"state": "applied"}, "pull_request": map[string]any{"state": "applied"}},
		shipTarget1, map[string]any{"id": "tg-other", "workspace_id": "ws2", "push_prefix": "x/"})
	out, _, err := execSessionCLI(t, nil, append([]string{"session", "ship", "feature", "--title", "Add the export", "--body", "Why.", "--draft"}, sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatal(err)
	}
	posts := f.postsTo(shipTargetPath)
	if len(posts) != 1 {
		t.Fatalf("ships = %v", f.hits)
	}
	body := posts[0]
	if body["session_run"] != "r1" || body["title"] != "Add the export" || body["message"] != "Add the export" || body["body"] != "Why." || body["draft"] != true ||
		!strings.HasPrefix(str(body, "operation_id"), "cli-ship-") || body["base"] != nil {
		t.Fatalf("ship body = %v", body)
	}
	for _, want := range []string{"Shipped feature: olivares/feature at 0123456789ab is pushed.", "Pull request: https://github.com/acme/widgets/pull/7"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output = %q, want %q", out, want)
		}
	}
}

func TestSessionShipNamesTheTargetAndTheTitleItDefaultsTo(t *testing.T) {
	f := shipEngine(t, 200, map[string]any{"state": "shipped", "branch": "b", "commit": "c"}, shipTarget1, map[string]any{"id": "tg2", "workspace_id": "ws1", "push_prefix": "alt/"})
	_, _, err := execSessionCLI(t, nil, append([]string{"session", "ship", "feature"}, sessionCreds(f.URL)...)...)
	if exitcode.From(err) != exitcode.Usage || !strings.Contains(err.Error(), "--target") || !strings.Contains(err.Error(), "tg1") || !strings.Contains(err.Error(), "tg2") {
		t.Fatalf("err = %v, want a usage error listing both targets", err)
	}
	if len(f.postsTo(shipTargetPath)) != 0 {
		t.Fatal("a ship without a target reached the engine")
	}
	if _, _, err := execSessionCLI(t, nil, append([]string{"session", "ship", "feature", "--target", "tg1", "--message", "Fix the flake\n\nlong text", "--base", "develop"}, sessionCreds(f.URL)...)...); err != nil {
		t.Fatal(err)
	}
	body := f.postsTo(shipTargetPath)[0]
	if body["title"] != "Fix the flake" || body["message"] != "Fix the flake\n\nlong text" || body["base"] != "develop" {
		t.Fatalf("ship body = %v", body)
	}
}

func TestSessionShipWithoutAnyTitleUsesTheSessionName(t *testing.T) {
	f := shipEngine(t, 200, map[string]any{"state": "shipped", "branch": "b", "commit": "c"}, shipTarget1)
	if _, _, err := execSessionCLI(t, nil, append([]string{"session", "ship", "feature"}, sessionCreds(f.URL)...)...); err != nil {
		t.Fatal(err)
	}
	if body := f.postsTo(shipTargetPath)[0]; body["title"] != "Work from feature" || body["message"] != "Work from feature" {
		t.Fatalf("ship body = %v", body)
	}
}

func TestSessionShipNeedsATargetInTheSessionsWorkspace(t *testing.T) {
	f := shipEngine(t, 200, nil, map[string]any{"id": "tg-other", "workspace_id": "ws2", "push_prefix": "x/"})
	_, _, err := execSessionCLI(t, nil, append([]string{"session", "ship", "feature"}, sessionCreds(f.URL)...)...)
	if exitcode.From(err) != exitcode.NotFound || !strings.Contains(err.Error(), "has no publication target") {
		t.Fatalf("err = %v, want the missing target named", err)
	}
}

// The engine's reason is named in a sentence with its next step, and keeps the
// exit code of its status.
func TestSessionShipNamesWhyTheEngineRefused(t *testing.T) {
	for code, tc := range map[string]struct {
		status int
		want   string
		exit   int
	}{
		"no_branch":            {409, "on no branch", exitcode.Conflict},
		"nothing_to_ship":      {409, "nothing to ship", exitcode.Conflict},
		"binding_not_approved": {422, "no longer approved", exitcode.Err},
		"approval_denied":      {403, "did not approve", exitcode.Auth},
	} {
		f := shipEngine(t, tc.status, map[string]any{"error": map[string]any{"code": code, "message": code}}, shipTarget1)
		_, _, err := execSessionCLI(t, nil, append([]string{"session", "ship", "feature"}, sessionCreds(f.URL)...)...)
		if err == nil || !strings.Contains(err.Error(), tc.want) || exitcode.From(err) != tc.exit {
			t.Fatalf("%s: err = %v (exit %d), want %q (exit %d)", code, err, exitcode.From(err), tc.want, tc.exit)
		}
	}
}

func TestSessionShipWaitsForTheSecondPersonAndSaysHowToGoOn(t *testing.T) {
	f := shipEngine(t, 202, map[string]any{"state": "awaiting_approval", "branch": "olivares/feature", "commit": "0123456789abcdef0123456789abcdef01234567", "committed": true, "approval_id": "appr-1"}, shipTarget1)
	out, _, err := execSessionCLI(t, nil, append([]string{"session", "ship", "feature"}, sessionCreds(f.URL)...)...)
	if exitcode.From(err) != exitcode.ApprovalRequired {
		t.Fatalf("err = %v (exit %d), want the approval-required exit", err, exitcode.From(err))
	}
	for _, want := range []string{"Another person has to approve", "approval appr-1", "olivares session ship feature"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output = %q, want %q", out, want)
		}
	}
}

func TestSessionShipSaysWhenThePullRequestFailedAfterThePush(t *testing.T) {
	f := shipEngine(t, 202, map[string]any{"state": "incomplete", "branch": "olivares/feature", "commit": "0123456789abcdef", "push": map[string]any{"state": "applied"}, "pull_request_error": "base_not_allowed"}, shipTarget1)
	out, _, err := execSessionCLI(t, nil, append([]string{"session", "ship", "feature"}, sessionCreds(f.URL)...)...)
	if exitcode.From(err) != exitcode.Conflict || !strings.Contains(out, "but the pull request was not opened (base_not_allowed)") {
		t.Fatalf("exit %d, output %q", exitcode.From(err), out)
	}
}

func TestSessionShipJSONPrintsTheEnginesAnswer(t *testing.T) {
	f := shipEngine(t, 200, map[string]any{"state": "shipped", "branch": "b", "commit": "c", "pull_request_url": "https://github.com/a/b/pull/1"}, shipTarget1)
	out, _, err := execSessionCLI(t, nil, append([]string{"session", "ship", "feature", "-o", "json"}, sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeSessionBody(t, out); got["state"] != "shipped" || got["pull_request_url"] != "https://github.com/a/b/pull/1" {
		t.Fatalf("json = %v", got)
	}
}
