// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package sessions

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

type providerApprovalGateFunc func(context.Context, model.TenantID, ProviderApprovalRequest) (ProviderApprovalDecision, error)

func (f providerApprovalGateFunc) Approve(ctx context.Context, tenant model.TenantID, req ProviderApprovalRequest) (ProviderApprovalDecision, error) {
	return f(ctx, tenant, req)
}

func TestProviderApprovalRechecksLivePolicyAfterHumanWait(t *testing.T) {
	for _, after := range []ProviderApprovalDisposition{ProviderApprovalDeny, "unknown", "error", ProviderApprovalAsk, ProviderApprovalAllow} {
		t.Run(string(after), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			entered, approved := make(chan struct{}, 1), make(chan struct{})
			var changed atomic.Bool
			var policyCalls, queueCalls, resolverCalls atomic.Int32
			m := New(WithProviderApprovalPrincipalResolver(func(context.Context, model.TenantID, string) (auth.Principal, string, error) {
				resolverCalls.Add(1)
				return auth.Principal{SessionIdentity: "owned-session"}, "owned-session", nil
			}), WithProviderApprovalPolicy(func(context.Context, model.TenantID, ProviderApprovalRequest) (ProviderApprovalPolicyDecision, error) {
				policyCalls.Add(1)
				if !changed.Load() {
					return ProviderApprovalPolicyDecision{Disposition: ProviderApprovalAsk}, nil
				}
				if after == "error" {
					return ProviderApprovalPolicyDecision{}, errors.New("policy unavailable")
				}
				return ProviderApprovalPolicyDecision{Disposition: after, Granted: []string{"read"}, Reason: "live policy changed while waiting"}, nil
			}), WithProviderApprovalGate(providerApprovalGateFunc(func(ctx context.Context, _ model.TenantID, _ ProviderApprovalRequest) (ProviderApprovalDecision, error) {
				queueCalls.Add(1)
				entered <- struct{}{}
				select {
				case <-approved:
					return ProviderApprovalDecision{Allow: true, Granted: []string{"read", "write"}}, nil
				case <-ctx.Done():
					return ProviderApprovalDecision{}, ctx.Err()
				}
			})))
			type result struct {
				decision ProviderApprovalDecision
				err      error
			}
			finished := make(chan result, 1)
			go func() {
				decision, err := m.authorizeProviderApproval(ctx, model.TenantID(model.NewID()), ProviderApprovalRequest{RunRef: "owned-run", SessionRef: "owned-session", Requested: []string{"read", "write"}})
				finished <- result{decision, err}
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("request did not enter the human queue")
			}
			// Author the new live verdict only once the human wait has started.
			changed.Store(true)
			close(approved)
			select {
			case got := <-finished:
				wantAllow := after == ProviderApprovalAsk || after == ProviderApprovalAllow
				if got.decision.Allow != wantAllow || (after == "error") != (got.err != nil) {
					t.Fatalf("post-wait decision=%+v error=%v", got.decision, got.err)
				}
				if after == ProviderApprovalAllow && !reflect.DeepEqual(got.decision.Granted, []string{"read"}) {
					t.Fatalf("live policy narrowing lost: %+v", got.decision)
				}
			case <-ctx.Done():
				t.Fatal("policy recheck entered a second human wait")
			}
			if policyCalls.Load() != 2 || resolverCalls.Load() != 2 || queueCalls.Load() != 1 {
				t.Fatalf("policy=%d resolver=%d human waits=%d", policyCalls.Load(), resolverCalls.Load(), queueCalls.Load())
			}
		})
	}
}

func TestProviderApprovalOnlyPolicyAskEntersHumanQueue(t *testing.T) {
	for _, disposition := range []ProviderApprovalDisposition{ProviderApprovalAllow, ProviderApprovalDeny, ProviderApprovalAsk, "unknown"} {
		t.Run(string(disposition), func(t *testing.T) {
			queue := &countingAllowGate{}
			m := New(WithProviderApprovalGate(queue), WithProviderApprovalPolicy(func(context.Context, model.TenantID, ProviderApprovalRequest) (ProviderApprovalPolicyDecision, error) {
				return ProviderApprovalPolicyDecision{Disposition: disposition}, nil
			}))
			decision, err := m.authorizeProviderApproval(context.Background(), model.TenantID(model.NewID()), ProviderApprovalRequest{})
			if err != nil {
				t.Fatal(err)
			}
			want := int32(0)
			if disposition == ProviderApprovalAsk {
				want = 1
			}
			if got := queue.calls.Load(); got != want {
				t.Fatalf("queue calls=%d want %d", got, want)
			}
			if decision.Allow != (disposition == ProviderApprovalAllow || disposition == ProviderApprovalAsk) {
				t.Fatalf("decision=%+v", decision)
			}
		})
	}
}

func TestProviderApprovalUsesResolvedSessionPrincipalBeforePolicy(t *testing.T) {
	tenant := model.TenantID(model.NewID())
	for _, revoked := range []bool{false, true} {
		t.Run(map[bool]string{false: "current", true: "revoked"}[revoked], func(t *testing.T) {
			called := false
			m := New(WithProviderApprovalPrincipalResolver(func(ctx context.Context, got model.TenantID, run string) (auth.Principal, string, error) {
				if got != tenant || run != "owned-run" {
					t.Fatal("resolver received another run")
				}
				if revoked {
					return auth.Principal{}, "", errors.New("revoked")
				}
				return auth.Principal{SessionIdentity: "owned-session"}, "owned-session", nil
			}), WithProviderApprovalPolicy(func(ctx context.Context, got model.TenantID, req ProviderApprovalRequest) (ProviderApprovalPolicyDecision, error) {
				called = true
				if req.SessionRef != "owned-session" || req.Principal.SessionIdentity != req.SessionRef {
					t.Fatal("policy did not receive resolved authority")
				}
				return ProviderApprovalPolicyDecision{Disposition: ProviderApprovalAllow}, nil
			}))
			decision, err := m.authorizeProviderApproval(context.Background(), tenant, ProviderApprovalRequest{RunRef: "owned-run", SessionRef: "owned-session"})
			if revoked {
				if err == nil || called || decision.Allow {
					t.Fatal("revoked run reached policy or approval")
				}
			} else if err != nil || !called || !decision.Allow {
				t.Fatalf("decision=%+v err=%v", decision, err)
			}
		})
	}
}
