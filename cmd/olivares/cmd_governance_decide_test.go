// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// fakeApprovals is the governance approval read and its decisions route: one pending
// launch approval that needs one vote.
type fakeApprovals struct {
	*httptest.Server
	mu        sync.Mutex
	status    string
	decisions []map[string]any
}

func newFakeApprovals(t *testing.T) *fakeApprovals {
	t.Helper()
	f := &fakeApprovals{status: "pending"}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		approval := func(approves int) map[string]any {
			return map[string]any{"id": "apr-1", "action": "sessions.run.launch", "subject_ref": "workspace:ws-1",
				"status": f.status, "risk_tier": "high", "required_approvals": 1, "approve_count": approves,
				"requested_by": "session:osn-1", "reason": "Launch claude in /srv/payroll (DLP label, read-write)"}
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/m/governance/approvals/apr-1":
			_ = json.NewEncoder(w).Encode(approval(0))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/m/governance/approvals/apr-1/decisions":
			if f.status != "pending" {
				w.WriteHeader(http.StatusConflict)
				_, _ = io.WriteString(w, `{"error":{"message":"approval is `+f.status+`; no decision can be recorded"}}`)
				return
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.decisions = append(f.decisions, body)
			approves := 0
			if body["decision"] == "approve" {
				f.status, approves = "approved", 1
			} else {
				f.status = "rejected"
			}
			_ = json.NewEncoder(w).Encode(approval(approves))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

// TestApprovalsApproveShowsTheRequestThenRecordsTheDecision: an administrator on a
// server without a browser decides from the CLI. The request and its reason are shown
// first; --yes skips the one-line confirmation; the decision goes to the engine's
// decisions route with the note.
func TestApprovalsApproveShowsTheRequestThenRecordsTheDecision(t *testing.T) {
	f := newFakeApprovals(t)
	out, errb, err := execRoot(t, observeArgs(f.URL, "governance", "approvals", "approve", "apr-1", "--note", "checked the folder", "--yes", "-o", "text")...)
	if err != nil {
		t.Fatalf("approve: %v\n%s", err, errb)
	}
	reason, done := strings.Index(out, "Launch claude in /srv/payroll"), strings.Index(out, "Approved apr-1.")
	if reason < 0 || done < 0 || reason > done {
		t.Fatalf("the reason must come before the result:\n%s", out)
	}
	if !strings.Contains(out, "The request is approved.") {
		t.Fatalf("the result does not say where the request stands:\n%s", out)
	}
	if len(f.decisions) != 1 || f.decisions[0]["decision"] != "approve" || f.decisions[0]["note"] != "checked the folder" {
		t.Fatalf("decisions sent = %v", f.decisions)
	}
}

func TestApprovalsRejectRecordsARejection(t *testing.T) {
	f := newFakeApprovals(t)
	out, errb, err := execRoot(t, observeArgs(f.URL, "governance", "approvals", "reject", "apr-1", "--yes", "-o", "text")...)
	if err != nil {
		t.Fatalf("reject: %v\n%s", err, errb)
	}
	if !strings.Contains(out, "Rejected apr-1. The request is rejected.") || len(f.decisions) != 1 || f.decisions[0]["decision"] != "reject" {
		t.Fatalf("out=%s decisions=%v", out, f.decisions)
	}
}

// TestApprovalsDecideAsksFirst: without --yes and with nobody to ask (not a terminal),
// no decision is sent; a request that is no longer pending says so in the engine's words.
func TestApprovalsDecideAsksFirst(t *testing.T) {
	f := newFakeApprovals(t)
	_, _, err := execRoot(t, observeArgs(f.URL, "governance", "approvals", "approve", "apr-1", "-o", "text")...)
	if exitcode.From(err) != exitcode.Usage || len(f.decisions) != 0 {
		t.Fatalf("err = %v (exit %d), decisions = %v: an unconfirmed decision was sent", err, exitcode.From(err), f.decisions)
	}
	// A request that is no longer pending is refused by the CLI before it asks (refresh 06);
	// it used to reach the engine's 409 "approval is approved; no decision can be recorded".
	f.status = "approved"
	_, _, err = execRoot(t, observeArgs(f.URL, "governance", "approvals", "reject", "apr-1", "--yes")...)
	if exitcode.From(err) != exitcode.Conflict || err.Error() != "Approval apr-1 is already approved; there is nothing to decide." {
		t.Fatalf("err = %v (exit %d)", err, exitcode.From(err))
	}
}

// TestApprovalsDecideSaysWhenNothingIsPending is J7 on refresh 06: deciding a request that
// was already decided printed the whole request, then the engine's 409. A request that is
// not pending is said in one line before anything is shown or asked; no vote is sent.
func TestApprovalsDecideSaysWhenNothingIsPending(t *testing.T) {
	f := newFakeApprovals(t)
	f.status = "rejected"
	out, _, err := execRoot(t, observeArgs(f.URL, "governance", "approvals", "reject", "apr-1", "--yes", "-o", "text")...)
	if exitcode.From(err) != exitcode.Conflict || err == nil ||
		err.Error() != "Approval apr-1 is already rejected; there is nothing to decide." {
		t.Fatalf("err = %v (exit %d)", err, exitcode.From(err))
	}
	if strings.Contains(out, "REASON") || len(f.decisions) != 0 {
		t.Fatalf("a decided request was shown or voted on: out=%q decisions=%v", out, f.decisions)
	}
}
