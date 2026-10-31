// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/governance"
	"github.com/olivaresai/olivares/modules/sessions"
)

func TestSessionStopCancelsPendingApprovalThroughAPI(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("provider-reference=%v", legacy), func(t *testing.T) {
			var credentials *sessionHookCredentials
			f := newDenialBudgetHookWith(t, sessions.DenialBudget{}, func(_ *sessions.Module, c *sessionHookCredentials) { credentials = c }, nil)
			h := f.h
			wireSessionGovernance(h.set, h.st, nil, nil, func(string) string { return "" }, discardLog())
			waitStoppedApprovalProvider(t, h, f.run, "pending-approval-provider")
			tenant := model.TenantID(h.tenantA)
			principal, scope, err := credentials.Resolve(t.Context(), f.token)
			if err != nil {
				t.Fatal(err)
			}
			pending, err := h.set.gov.EngineApprovals().Request(t.Context(), tenant, principal, governance.ApprovalRequest{Action: "github.write", SubjectKind: "repository", SubjectRef: "stop-approval-proof", SessionRef: scope.SessionRef})
			if err != nil {
				t.Fatal(err)
			}
			if legacy {
				code := h.reqInto(http.MethodPost, "/v1/m/governance/approvals", h.adminToken, h.tenantA, map[string]any{"action": "github.write", "subject_kind": "repository", "subject_ref": "legacy-proof", "session_ref": "pending-approval-provider"}, &pending)
				if code != http.StatusCreated {
					t.Fatalf("legacy approval=%d", code)
				}
			}
			code, raw := h.req(http.MethodPost, "/v1/m/sessions/runs/"+f.run+"/stop", h.adminToken, h.tenantA, nil)
			if code != http.StatusOK {
				t.Fatalf("stop=%d %s", code, raw)
			}

			read, readErr := h.set.gov.EngineApprovals().Read(t.Context(), tenant, pending.ID)
			if readErr != nil || read.Status != "canceled" {
				t.Fatalf("after stop approval=%+v err=%v", read, readErr)
			}
			code, raw = h.req(http.MethodPost, "/v1/m/governance/approvals/"+pending.ID+"/decisions", h.adminToken, h.tenantA, map[string]any{"decision": "approve"})
			if code != http.StatusConflict {
				t.Fatalf("decision after stop=%d, want409: %s", code, raw)
			}
			code, raw = h.req(http.MethodPost, "/v1/m/governance/approvals/"+pending.ID+"/consume", h.adminToken, h.tenantA, map[string]any{"consumer_id": "stop-proof"})
			var consumption struct {
				Granted bool   `json:"granted"`
				Status  string `json:"status"`
			}
			if err := json.Unmarshal(raw, &consumption); err != nil {
				t.Fatal(err)
			}
			if code != http.StatusOK || consumption.Granted || consumption.Status != "canceled" {
				t.Fatalf("consume after stop=%d %s", code, raw)
			}
			stored, err := h.set.gov.EngineApprovals().Read(t.Context(), tenant, pending.ID)
			if err != nil || stored.Status != "canceled" {
				t.Fatalf("approval=%+v err=%v", stored, err)
			}
		})
	}
}

// Observe init before stopping so the provider-reference case exercises its real binding.
func waitStoppedApprovalProvider(t *testing.T, h *harness, run, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var got struct {
			ProviderID string `json:"claude_session_id"`
		}
		code := h.reqInto(http.MethodGet, "/v1/m/sessions/runs/"+run, h.adminToken, h.tenantA, nil, &got)
		if code == http.StatusOK && got.ProviderID == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("provider init: HTTP %d id=%q want=%q", code, got.ProviderID, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type stopApprovalInitRunner struct{ providerID string }

func (r stopApprovalInitRunner) Launch(context.Context, sessions.LaunchSpec) (sessions.Process, error) {
	p := &approvalProjectionProcess{output: make(chan sessions.OutputFrame, 1), done: make(chan struct{})}
	frame, err := json.Marshal(map[string]string{"type": "system", "subtype": "init", "session_id": r.providerID})
	if err != nil {
		return nil, err
	}
	p.output <- sessions.OutputFrame{Stream: "stdout", Data: frame}
	return p, nil
}

func TestSessionStopDoesNotTrustProviderClaimOfAnotherCanonicalSID(t *testing.T) {
	foreignSID := "osn_" + model.NewID().String()
	var credentials *sessionHookCredentials
	f := newDenialBudgetHookWith(t, sessions.DenialBudget{}, func(m *sessions.Module, c *sessionHookCredentials) {
		credentials = c
		sessions.WithRunner(stopApprovalInitRunner{providerID: foreignSID})(m)
	}, nil)
	h := f.h
	wireSessionGovernance(h.set, h.st, nil, nil, func(string) string { return "" }, discardLog())
	waitStoppedApprovalProvider(t, h, f.run, foreignSID)
	tenant := model.TenantID(h.tenantA)
	principal, _, err := credentials.Resolve(t.Context(), f.token)
	if err != nil {
		t.Fatal(err)
	}
	principal.SessionIdentity = foreignSID
	pending, err := h.set.gov.EngineApprovals().Request(t.Context(), tenant, principal, governance.ApprovalRequest{Action: "github.write", SubjectKind: "repository", SubjectRef: "other-session", SessionRef: foreignSID})
	if err != nil {
		t.Fatal(err)
	}
	if code, raw := h.req(http.MethodPost, "/v1/m/sessions/runs/"+f.run+"/stop", h.adminToken, h.tenantA, nil); code != http.StatusOK {
		t.Fatalf("stop=%d %s", code, raw)
	}
	got, err := h.set.gov.EngineApprovals().Read(t.Context(), tenant, pending.ID)
	if err != nil || got.Status != "pending" {
		t.Fatalf("foreign approval=%+v err=%v", got, err)
	}
}

// Hold the real stop route at the process boundary while a decision arrives.
type stopApprovalBlockingRunner struct {
	entered chan struct{}
	release chan struct{}
}

func (r stopApprovalBlockingRunner) Launch(ctx context.Context, spec sessions.LaunchSpec) (sessions.Process, error) {
	p, err := (approvalProjectionRunner{}).Launch(ctx, spec)
	if err != nil {
		return nil, err
	}
	return &stopApprovalBlockingProcess{Process: p, entered: r.entered, release: r.release}, nil
}

type stopApprovalBlockingProcess struct {
	sessions.Process
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (p *stopApprovalBlockingProcess) Stop(ctx context.Context) error {
	p.once.Do(func() { close(p.entered) })
	select {
	case <-p.release:
		return p.Process.Stop(ctx)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestSessionStopRefusesDecisionDuringShutdownThroughAPI(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	var credentials *sessionHookCredentials
	f := newDenialBudgetHookWith(t, sessions.DenialBudget{}, func(m *sessions.Module, c *sessionHookCredentials) {
		credentials = c
		sessions.WithRunner(stopApprovalBlockingRunner{entered: entered, release: release})(m)
	}, nil)
	h := f.h
	wireSessionGovernance(h.set, h.st, nil, nil, func(string) string { return "" }, discardLog())
	waitStoppedApprovalProvider(t, h, f.run, "pending-approval-provider")
	tenant := model.TenantID(h.tenantA)
	principal, scope, err := credentials.Resolve(t.Context(), f.token)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, subject := range []string{"shutdown-race-one", "shutdown-race-two"} {
		pending, err := h.set.gov.EngineApprovals().Request(t.Context(), tenant, principal, governance.ApprovalRequest{Action: "github.write", SubjectKind: "repository", SubjectRef: subject, SessionRef: scope.SessionRef})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, pending.ID)
	}
	type response struct {
		code int
		raw  []byte
	}
	stopped := make(chan response, 1)
	go func() {
		code, raw := h.req(http.MethodPost, "/v1/m/sessions/runs/"+f.run+"/stop", h.adminToken, h.tenantA, nil)
		stopped <- response{code, raw}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("stop never reached the process boundary")
	}
	code, raw := h.req(http.MethodPost, "/v1/m/governance/approvals/"+ids[0]+"/decisions", h.adminToken, h.tenantA, map[string]any{"decision": "approve"})
	// Always release and join before an assertion, including the RED case.
	unblock()
	var stop response
	select {
	case stop = <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("stop did not finish after releasing the process")
	}
	if stop.code != http.StatusOK {
		t.Errorf("stop=%d %s", stop.code, stop.raw)
	}
	if code != http.StatusConflict {
		t.Errorf("decision during shutdown=%d, want409: %s", code, raw)
	}
	for _, id := range ids {
		got, err := h.set.gov.EngineApprovals().Read(t.Context(), tenant, id)
		if err != nil || got.Status != "canceled" {
			t.Errorf("shutdown approval=%+v err=%v", got, err)
		}
		code, raw := h.req(http.MethodPost, "/v1/m/governance/approvals/"+id+"/consume", h.adminToken, h.tenantA, map[string]any{"consumer_id": "shutdown-race"})
		var consumption struct {
			Granted bool   `json:"granted"`
			Status  string `json:"status"`
		}
		if err := json.Unmarshal(raw, &consumption); err != nil {
			t.Fatal(err)
		}
		if code != http.StatusOK || consumption.Granted || consumption.Status != "canceled" {
			t.Errorf("consume during shutdown=%d %s", code, raw)
		}
	}
}
