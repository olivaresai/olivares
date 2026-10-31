// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/gitpublish"
)

type fakeShipPolicy struct {
	ref string
	err error
}

func (p fakeShipPolicy) ReviewPolicy(context.Context, model.TenantID, string, string) (string, error) {
	return p.ref, p.err
}

type fakeShipBridge struct {
	status, ref string
	err         error
	granted     bool
	replay      bool
	consumeErr  error
	gated       int
	consumed    []string
	requestedBy string
	session     string
}

func (b *fakeShipBridge) GateUnspentForSession(_ context.Context, _ model.TenantID, _, _, _, _, _, requestedBy, session string) (string, string, string, error) {
	b.gated++
	b.requestedBy, b.session = requestedBy, session
	return b.ref, b.status, "", b.err
}

func (b *fakeShipBridge) ConsumeApproval(_ context.Context, _ model.TenantID, ref, consumer, _ string) (bool, bool, error) {
	b.consumed = append(b.consumed, ref+"|"+consumer)
	return b.granted, b.replay, b.consumeErr
}

func TestShipGateAsksTheTenantsPolicyBeforeAnyPerson(t *testing.T) {
	req := gitpublish.GateRequest{Action: "gitpublish.push", SubjectKind: "gitpublish.target", SubjectRef: "t1", PlanHash: "p", RequestedBy: "user:u1", ConsumerID: "op-1"}
	policy := fakeShipPolicy{ref: "pol"}
	for name, tc := range map[string]struct {
		policy      fakeShipPolicy
		bridge      fakeShipBridge
		want        string
		wantErr     bool
		gated       int
		wantConsume []string
	}{
		"no policy asks for review: granted, nobody is asked": {want: gitpublish.GateGranted},
		"a policy asks and nobody decided yet":                {policy: policy, bridge: fakeShipBridge{status: nbPending, ref: "a1"}, want: gitpublish.GatePending, gated: 1},
		"a second person approved: the grant is spent once": {policy: policy, bridge: fakeShipBridge{status: nbApproved, ref: "a1", granted: true},
			want: gitpublish.GateGranted, gated: 1, wantConsume: []string{"a1|op-1"}},
		"the grant was spent by another ship": {policy: policy, bridge: fakeShipBridge{status: nbApproved, ref: "a1", replay: true},
			want: gitpublish.GateDenied, gated: 1, wantConsume: []string{"a1|op-1"}},
		"the grant cannot be spent":             {policy: policy, bridge: fakeShipBridge{status: nbApproved, ref: "a1", consumeErr: errors.New("down")}, wantErr: true, gated: 1, wantConsume: []string{"a1|op-1"}},
		"the person declined":                   {policy: policy, bridge: fakeShipBridge{status: nbRejected, ref: "a1"}, want: gitpublish.GateDenied, gated: 1},
		"the approval expired":                  {policy: policy, bridge: fakeShipBridge{status: nbExpired, ref: "a1"}, want: gitpublish.GateDenied, gated: 1},
		"no gate is wired":                      {policy: policy, bridge: fakeShipBridge{status: nbNoGate, ref: "no-gate:p"}, want: gitpublish.GateDenied, gated: 1},
		"an emergency grant is not an approval": {policy: policy, bridge: fakeShipBridge{status: nbBreakGlass, ref: "breakglass:g"}, want: gitpublish.GateDenied, gated: 1},
		"the policy cannot be read":             {policy: fakeShipPolicy{err: errors.New("unreadable")}, wantErr: true},
		"the bridge cannot be reached":          {policy: policy, bridge: fakeShipBridge{err: errors.New("down")}, wantErr: true, gated: 1},
	} {
		t.Run(name, func(t *testing.T) {
			g := shipGate{policy: tc.policy, bridge: &tc.bridge}
			got, err := g.Authorize(context.Background(), model.TenantID("t"), req)
			if (err != nil) != tc.wantErr || got.State != tc.want {
				t.Fatalf("Authorize = %+v, %v; want state %q, error %v", got, err, tc.want, tc.wantErr)
			}
			if tc.bridge.gated != tc.gated || len(tc.bridge.consumed) != len(tc.wantConsume) || (len(tc.wantConsume) == 1 && tc.bridge.consumed[0] != tc.wantConsume[0]) {
				t.Fatalf("bridge gated %d, consumed %v; want %d, %v", tc.bridge.gated, tc.bridge.consumed, tc.gated, tc.wantConsume)
			}
			if tc.gated == 1 && (tc.bridge.requestedBy != "user:u1" || tc.bridge.session != "") {
				t.Fatalf("approval asked by %q for session %q; want the person and no session", tc.bridge.requestedBy, tc.bridge.session)
			}
		})
	}
}
