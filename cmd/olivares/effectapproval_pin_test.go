// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

// These tests pin what each approval caller answers for every approval outcome,
// through the caller's own entry point. They were written against the six
// copies of the approval rule and stay unchanged when the callers move onto the
// one-effect approval module.

// pinBridge is an in-memory approval bridge: it answers one status and one spend
// result, and records what the caller asked.
type pinBridge struct {
	ref, status, bound string
	openErr, spendErr  error
	granted, replay    bool

	opened, sessions, scoped, spent []string
}

func (b *pinBridge) GateOnceForSession(_ context.Context, _ model.TenantID, action, subjectKind, subjectRef, planHash, _, _, sessionRef string) (string, string, string, error) {
	b.opened = append(b.opened, action+"|"+subjectKind+"|"+subjectRef+"|"+planHash)
	b.sessions = append(b.sessions, sessionRef)
	if b.openErr != nil {
		return "", "", "", b.openErr
	}
	return b.ref, b.status, b.bound, nil
}

func (b *pinBridge) StatusScoped(_ context.Context, _ model.TenantID, ref, planHash, action, subjectKind, subjectRef string) (string, string, error) {
	b.scoped = append(b.scoped, ref+"|"+planHash+"|"+action+"|"+subjectKind+"|"+subjectRef)
	if b.openErr != nil {
		return "", "", b.openErr
	}
	return b.status, b.bound, nil
}

func (b *pinBridge) ConsumeApproval(_ context.Context, _ model.TenantID, ref, consumerID, policyVersion string) (bool, bool, error) {
	b.spent = append(b.spent, ref+"|"+consumerID+"|"+policyVersion)
	return b.granted, b.replay, b.spendErr
}

func TestPinCriticalLaunchApprovalOutcomes(t *testing.T) {
	broken := errors.New("bridge broke")
	intent := sessions.LaunchIntent{RunRef: "run-pin", Actor: "user:u1", ActorKind: model.ActorUser, PermissionMode: "bypassPermissions", ClaimSID: "sid-pin"}
	held := intent
	held.ApprovalRef = "appr-held"
	scoped := "appr-held|" + sessionLaunchPlanHash(held) + "|" + sessionLaunchAction + "|" + sessionLaunchSubjectKind + "|" + launchSubjectRef(held)
	for _, tc := range []struct {
		name    string
		intent  sessions.LaunchIntent
		bridge  *pinBridge
		ok      bool
		ref     string
		status  int
		reason  string
		spend   string
		scoped  string
		pending bool
	}{
		{"open error", intent, &pinBridge{openErr: broken}, false, "", http.StatusServiceUnavailable, "could not open a governed approval for the privileged launch (deny-closed)", "", "", false},
		{"pending", intent, &pinBridge{ref: "appr-1", status: nbPending}, false, "appr-1", http.StatusAccepted, "waiting for human approval", "", "", true},
		{"approved", intent, &pinBridge{ref: "appr-1", status: nbApproved, granted: true}, true, "appr-1", 0, "", "nonce|", "", false},
		{"replay", intent, &pinBridge{ref: "appr-1", status: nbApproved, replay: true}, false, "appr-1", 0, "privileged launch approval already consumed; a fresh human approval is required (appr-1)", "nonce|", "", false},
		{"not spendable", intent, &pinBridge{ref: "appr-1", status: nbApproved}, false, "appr-1", 0, "governed launch approval is no longer valid to spend (deny-closed)", "nonce|", "", false},
		{"spend error", intent, &pinBridge{ref: "appr-1", status: nbApproved, spendErr: broken}, false, "appr-1", http.StatusServiceUnavailable, "could not spend the governed launch approval (deny-closed)", "nonce|", "", false},
		{"break-glass", intent, &pinBridge{ref: "breakglass:g1", status: nbBreakGlass}, true, "breakglass:g1", 0, "", "", "", false},
		{"rejected", intent, &pinBridge{ref: "appr-1", status: nbRejected}, false, "appr-1", 0, "human review did not approve the privileged launch (status=rejected)", "", "", false},
		{"named approval", held, &pinBridge{status: nbApproved, granted: true}, true, "appr-held", 0, "", "nonce|", scoped, false},
		{"named approval, read error", held, &pinBridge{openErr: broken}, false, "", http.StatusServiceUnavailable, "could not open a governed approval for the privileged launch (deny-closed)", "", scoped, false},
		{"named approval, expired", held, &pinBridge{status: nbExpired}, false, "appr-held", 0, "human review did not approve the privileged launch (status=expired)", "", scoped, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &sessionLaunchGate{bridge: tc.bridge}
			ref, dec, ok := g.gateCriticalLaunch(context.Background(), model.TenantID("t"), tc.intent, "bypassPermissions")
			if ok != tc.ok || ref != tc.ref || dec.Allowed || dec.DeniedStatus != tc.status || dec.Reason != tc.reason {
				t.Fatalf("launch = ok:%v ref:%q %+v", ok, ref, dec)
			}
			if tc.pending != (dec.ApprovalRef == tc.ref && dec.Critical && dec.RecordIO) {
				t.Fatalf("pending launch decision = %+v", dec)
			}
			if got := strings.Join(tc.bridge.scoped, ","); got != tc.scoped {
				t.Fatalf("scoped read %q, want %q", got, tc.scoped)
			}
			if tc.scoped != "" && len(tc.bridge.opened) != 0 {
				t.Fatalf("a named approval opened another one: %v", tc.bridge.opened)
			}
			wantSession := "sid-pin" // the launch's claimed session
			if tc.scoped != "" {
				wantSession = "" // a named approval is read, not opened
			}
			if got := strings.Join(tc.bridge.sessions, ","); got != wantSession {
				t.Fatalf("opened for session %q, want %q", got, wantSession)
			}
			assertPinSpend(t, tc.bridge.spent, tc.spend)
		})
	}
}

// assertPinSpend checks the one recorded spend: want is "" (none),
// "ref|consumer|version", or "nonce|version" for a fresh single-use consumer.
func assertPinSpend(t *testing.T, spent []string, want string) {
	t.Helper()
	if want == "" {
		if len(spent) != 0 {
			t.Fatalf("spent %v, want none", spent)
		}
		return
	}
	if version, ok := strings.CutPrefix(want, "nonce|"); ok {
		parts := strings.Split(strings.Join(spent, ","), "|")
		if len(spent) != 1 || len(parts) != 3 || !strings.HasPrefix(parts[1], "singleuse-") || parts[2] != version {
			t.Fatalf("spent %v, want one spend for a fresh single-use consumer, version %q", spent, version)
		}
		return
	}
	if len(spent) != 1 || spent[0] != want {
		t.Fatalf("spent %v, want %q", spent, want)
	}
}

// The MCP gateway is re-entrant: after a human rejection the next call for the
// plan, round trip or not, asks again with one fresh pending approval, and the
// rejected one stays rejected with nothing spent.
func TestPinMCPToolGateReasksAfterRejection(t *testing.T) {
	h := newHarness(t)
	_, approver := h.createApprover(t, "pin-mcp@bridge.test")
	gate := mcpToolGate{bridge: buildBridge(t, h, h.mintBoundToken(t, auth.RoleEditor)), tenant: tenantAID(t, h)}
	ctx := context.Background()

	rejected := mcpc.ToolApprovalRequest{Tenant: h.tenantA, Tool: "db.drop_table", PlanHash: "plan-pin-rejected", RequestedBy: "agent"}
	d, err := gate.Authorize(ctx, rejected)
	if err != nil || d.Status != mcpc.StatusPending {
		t.Fatalf("first authorize = %v err=%v", d.Status, err)
	}
	ref := d.ApprovalRef
	if code, body := h.decide(t, approver, ref, "reject"); code != http.StatusOK {
		t.Fatalf("reject = %d: %s", code, body)
	}
	asked := ""
	for _, consumer := range []string{"", "round-trip-rejected"} {
		call := rejected
		call.ConsumerID = consumer
		if d, err = gate.Authorize(ctx, call); err != nil || d.Status != mcpc.StatusPending || d.ApprovalRef == ref || d.Spent || d.PlanHash != "plan-pin-rejected" {
			t.Fatalf("consumer %q: call after the rejection = %+v err=%v", consumer, d, err)
		}
		if asked == "" {
			asked = d.ApprovalRef
		} else if d.ApprovalRef != asked {
			t.Fatalf("a retry opened another approval: %q, then %q", asked, d.ApprovalRef)
		}
	}
	if status := h.approvalStatus(t, h.adminToken, ref); status != nbRejected {
		t.Fatalf("the rejected approval became %q", status)
	}
}

// An interrupted managed-session call withdraws its own request and reports the
// refusal with the request's reference.
func TestPinManagedSessionApprovalWithdrawsOnInterrupt(t *testing.T) {
	f := newManagedMCPApprovalFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	p, err := f.management.sessionAuthenticator.Authenticate(ctx, f.token)
	if err != nil {
		t.Fatal(err)
	}
	gate := managedSessionApprovalGate{m: f.management, tenant: f.tenant, principal: p, serverID: "server", serverName: "Approval HTTPS", check: func(context.Context) error { return nil }}
	callCtx, interrupt := context.WithCancel(ctx)
	type result struct {
		decision mcpc.GateDecision
		err      error
	}
	answered := make(chan result, 1)
	go func() {
		decision, err := gate.Authorize(callCtx, mcpc.ToolApprovalRequest{
			Tenant: f.tenant.String(), Subject: p.SessionIdentity, RequestedBy: p.SessionIdentity,
			Tool: "write_echo", PlanHash: "plan", Arguments: json.RawMessage(`{"text":"interrupted"}`),
		})
		answered <- result{decision, err}
	}()
	ref := f.pendingApproval(ctx, make(chan struct{}), httptest.NewRecorder())
	f.waitForRunProjection(ctx, ref)
	interrupt()
	var got result
	select {
	case got = <-answered:
	case <-ctx.Done():
		t.Fatal("gate did not return after the interrupt")
	}
	if got.err != nil || got.decision.Allowed() || got.decision.Status != mcpc.StatusRejected || got.decision.ApprovalRef != ref || got.decision.Spent {
		t.Fatalf("interrupted decision = %+v, %v", got.decision, got.err)
	}
	if status := f.h.approvalStatus(t, f.h.adminToken, ref); status != nbCanceled {
		t.Fatalf("interrupted request status = %q, want canceled", status)
	}
	f.waitForRunProjection(ctx, "")
}
