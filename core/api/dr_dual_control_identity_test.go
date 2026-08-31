// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Dual-control restore is advertised as a two-party gate: the error text and the
// handler comment both promise "a DIFFERENT administrator". The check behind that
// promise compared Principal.Actor() — a CREDENTIAL string ("user:<id>" for a
// session, "token:<id>" for a token). One account holds both shapes, so one account
// produced two distinct strings and satisfied a gate that exists precisely to require
// two parties.
//
// These two tests pin the property the copy claims AND the ceiling of that property,
// which is why their names say ACCOUNT and not "human":
//
//   - the gate separates user ACCOUNTS — a requester cannot approve through a second
//     credential of their own account;
//   - it does NOT separate humans, and the second test is the demonstration, not a
//     footnote. It admits a second account that the REQUESTER creates here, choosing
//     its password (POST /v1/users needs only superadmin + user:write,
//     core/api/handlers_core.go:256-273, and never a step-up). Read as "still admits a
//     second HUMAN" it teaches the opposite of what it runs: one human drives every
//     request in it.
//
// core/auth/person.go records the same limit at the primitive, with its measurement.

// enableDualControl turns the restore dual-control gate on through the real
// schedule endpoint (the same path an operator uses).
func enableDualControl(t *testing.T, h *harness, admin string) {
	t.Helper()
	r := h.do("PUT", "/v1/console/dr/schedule", admin, map[string]any{
		"enabled": false, "cron": "", "retain_days": 7,
		"require_dual_control_restore": true,
	}, nil)
	if r.code != http.StatusOK {
		t.Fatalf("enable dual-control = %d %s", r.code, r.raw)
	}
	if r.body["require_dual_control_restore"] != true {
		t.Fatalf("dual-control gate did not stick: %s", r.raw)
	}
}

// stageUpload puts a present upload in the backup dir so the restore handlers
// reach the dual-control decision (the bundle's bytes are never read on this
// path — apply/approve stat the file and branch before any content is touched).
func stageUpload(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("bundle"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestDRRestoreDualControlIsPerAccountNotPerCredential is the reproduction: ONE
// superadmin ACCOUNT, TWO credentials of its own, satisfying the two-party gate
// end-to-end through the real HTTP stack.
func TestDRRestoreDualControlIsPerAccountNotPerCredential(t *testing.T) {
	h, dir := drHarness(t)
	admin := h.adminLogin()
	enableDualControl(t, h, admin)

	// The SAME account mints a superadmin token for itself. handleIssueToken asks
	// only for p.Superadmin (handlers_core.go:342-347) — no step-up, no second
	// party — and IssueToken stamps the token with the actor's OWN UserID
	// (accounts.go:360).
	r := h.do("POST", "/v1/tokens", admin, map[string]any{"name": "ops", "superadmin": true}, nil)
	if r.code != http.StatusCreated {
		t.Fatalf("self-issue superadmin token = %d %s", r.code, r.raw)
	}
	selfToken, _ := r.body["token"].(string)
	if selfToken == "" {
		t.Fatalf("no token in issue response: %s", r.raw)
	}

	// The premise, measured rather than asserted: both credentials authenticate to
	// the SAME account and to DIFFERENT actor strings.
	ctx := context.Background()
	pSess, err := h.authr.Authenticate(ctx, admin)
	if err != nil {
		t.Fatalf("authenticate session: %v", err)
	}
	pTok, err := h.authr.Authenticate(ctx, selfToken)
	if err != nil {
		t.Fatalf("authenticate token: %v", err)
	}
	if pSess.UserID != pTok.UserID || pSess.UserID.IsZero() {
		t.Fatalf("premise broken: session user %q vs token user %q — the two credentials are not the same account",
			pSess.UserID, pTok.UserID)
	}
	if pSess.Actor() == pTok.Actor() {
		t.Fatalf("premise broken: both credentials render the same actor %q — there would be nothing to bypass", pSess.Actor())
	}
	t.Logf("one account %s holds two actors: %q (session) and %q (token)", pSess.UserID, pSess.Actor(), pTok.Actor())

	stageUpload(t, dir, "upload-1")

	// Step 1 — the account REQUESTS the restore with its session.
	r = h.do("POST", "/v1/console/dr/restore/upload-1/apply", admin, map[string]any{}, nil)
	if r.code != http.StatusAccepted {
		t.Fatalf("restore apply under dual-control = %d %s, want 202", r.code, r.raw)
	}
	if r.body["awaiting_approval"] != true {
		t.Fatalf("dual-control did not hold the restore for a second approver: %s", r.raw)
	}
	requestID, _ := r.body["request_id"].(string)
	if requestID == "" {
		t.Fatalf("no request_id: %s", r.raw)
	}

	// Step 2 — the SAME account APPROVES with its own token. A gate that separates
	// accounts must refuse this; a gate that compares credential strings waves it
	// through.
	r = h.do("POST", "/v1/console/dr/restore/upload-1/approve", selfToken,
		map[string]any{"request_id": requestID, "passphrase": "correct horse battery staple"}, nil)
	if r.code != http.StatusForbidden {
		t.Fatalf("BYPASS: one account satisfied the two-party restore gate — approve with own token = %d %s, want 403.\n"+
			"initiator actor %q and approver actor %q are the SAME account (user %s)",
			r.code, r.raw, pSess.Actor(), pTok.Actor(), pSess.UserID)
	}
	if !strings.Contains(r.raw, "different") && !strings.Contains(r.raw, "DIFFERENT") {
		t.Fatalf("refusal should name the separation rule, got %s", r.raw)
	}
	// The refusal must promise what the gate ENFORCES. It compares ACCOUNTS, so a
	// message promising people would put the claim back where the accounts check
	// left it. A comment cannot go red when it lies; this string can.
	//
	// Case-FOLDED and with the synonyms, because the first version of this guard was
	// case-sensitive and the external contrast broke it on paper with "two DIFFERENT
	// PEOPLE using separate user accounts" — which contains "account", so the positive
	// assert below would also have waved it through (contrast, H4).
	low := strings.ToLower(r.raw)
	for _, banned := range []string{"people", "person", "human", "individual"} {
		if strings.Contains(low, banned) {
			t.Fatalf("the refusal promises %q, and this gate can only enforce ACCOUNTS: %s", banned, r.raw)
		}
	}
	if !strings.Contains(low, "account") {
		t.Fatalf("the refusal should tell the operator the unit it compares (accounts), got %s", r.raw)
	}

	// The refused approval must LEAVE the request pending for a real second
	// account — refusing must not consume the intent.
	r = h.do("GET", "/v1/console/dr/restore/pending", admin, nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("list pending = %d %s", r.code, r.raw)
	}
	items, _ := r.body["items"].([]any)
	found := false
	for _, it := range items {
		if m, ok := it.(map[string]any); ok && m["request_id"] == requestID {
			found = true
		}
	}
	if !found {
		t.Fatalf("a refused self-approval consumed the pending restore; a genuine second admin can no longer approve: %s", r.raw)
	}
}

// TestDRRestoreDualControlAdmitsASecondAccountTheRequesterMinted carries BOTH halves
// of the truth, and its name has to say the second one out loud.
//
//   - The control against over-denial: the fix must refuse the same account, not
//     everyone. A distinct superadmin account approves and the restore proceeds.
//   - The CEILING, which is the same run read honestly: the approving account is one
//     the REQUESTER creates here, choosing its password, in a call authenticated as
//     the requester. No IdP, no second seat, no DB surgery. ONE human drives every
//     request below.
//
// Named "StillAdmitsASecondHuman" it taught the opposite of what it runs, and a
// reader would have taken it as evidence the gate separates humans. It never did.
func TestDRRestoreDualControlAdmitsASecondAccountTheRequesterMinted(t *testing.T) {
	h, dir := drHarness(t)
	admin := h.adminLogin()
	enableDualControl(t, h, admin)

	// A SECOND, distinct superadmin ACCOUNT — created BY the requester, with a
	// password the requester picks. That is the whole ceremony.
	r := h.do("POST", "/v1/users", admin, map[string]any{
		"email": "second@x.io", "password": "supersecret2", "superadmin": true,
	}, nil)
	if r.code != http.StatusCreated {
		t.Fatalf("create second superadmin = %d %s", r.code, r.raw)
	}
	r = h.do("POST", "/v1/auth/login", "", map[string]any{"email": "second@x.io", "password": "supersecret2"}, nil)
	if r.code != http.StatusOK {
		t.Fatalf("login second admin = %d %s", r.code, r.raw)
	}
	second, _ := r.body["token"].(string)

	// Measured, not assumed: the approver is a DIFFERENT account id. That difference
	// is the entire basis on which the 202 below is granted — and it was manufactured
	// by the requester two HTTP calls ago.
	ctx := context.Background()
	pReq, err := h.authr.Authenticate(ctx, admin)
	if err != nil {
		t.Fatalf("authenticate requester: %v", err)
	}
	pApp, err := h.authr.Authenticate(ctx, second)
	if err != nil {
		t.Fatalf("authenticate approver: %v", err)
	}
	if pReq.UserID == pApp.UserID {
		t.Fatalf("premise broken: requester and approver share account %q", pReq.UserID)
	}
	t.Logf("the requester (%s) minted the approving account (%s) and chose its password; "+
		"the gate separates these two ACCOUNTS and cannot see that one human holds both",
		pReq.UserID, pApp.UserID)

	stageUpload(t, dir, "upload-2")

	r = h.do("POST", "/v1/console/dr/restore/upload-2/apply", admin, map[string]any{}, nil)
	if r.code != http.StatusAccepted {
		t.Fatalf("restore apply = %d %s", r.code, r.raw)
	}
	requestID, _ := r.body["request_id"].(string)

	r = h.do("POST", "/v1/console/dr/restore/upload-2/approve", second,
		map[string]any{"request_id": requestID, "passphrase": "correct horse battery staple"}, nil)
	if r.code != http.StatusAccepted {
		t.Fatalf("a DIFFERENT account must be able to approve, got %d %s", r.code, r.raw)
	}
}
