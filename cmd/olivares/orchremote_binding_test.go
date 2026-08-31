// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	a2a "github.com/olivaresai/olivares/connectors/a2a"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

func protocolRESTBindingFixture(
	t *testing.T,
) (*orchRemoteExecutor, *fakeRemoteWorkStore, *fakeRemoteA2A, model.TenantID, sessions.ProtocolBinding) {
	t.Helper()
	executor, store, plan := remoteExecutorFixture(t)
	tenant := model.NewTenantID()
	binding := remoteBindingFixture(plan.WorkspaceID, plan.WorkItemID, plan.BindingSpecID)
	binding.TenantID = tenant
	binding.Version = 4
	binding.ExternalKind = string(sessions.ProtocolBindingResultTask)
	binding.ExternalID = "task-rest-1"
	binding.RemoteState = "working"
	binding.RemoteRevision = a2a.ProtocolVersion
	binding.ObservationVerdict = sessions.ProtocolObservationClean
	binding.ObservationCode = "remote_working"
	observed := time.Date(2026, 8, 18, 11, 58, 0, 0, time.UTC)
	binding.LastObservedAt = &observed
	store.binding = binding
	client := &fakeRemoteA2A{reconcileOK: true, reconcile: a2a.TaskResult{
		TaskID: binding.ExternalID, ResultKind: "task", ContextID: "context-rest-1",
		State: a2a.TaskStateCompleted, Terminal: true, TrustLevel: "verified",
	}}
	executor.client = func(_ orchRemoteTarget, gate a2a.DelegationGate) remoteA2AClient {
		client.gate = gate
		return client
	}
	return executor, store, client, tenant, binding
}

func TestK5ProtocolBindingRESTTestIsReadOnlyAndApplyCommitsExactGeneration(t *testing.T) {
	executor, store, client, tenant, binding := protocolRESTBindingFixture(t)
	request := sessions.ProtocolBindingReconcileRequest{
		Binding: binding, ExpectedVersion: binding.Version,
		SemanticKey: "binding-rest-reconcile-1", ExpectedPlanHash: strings.Repeat("a", 64),
	}

	tested, err := executor.TestProtocolBinding(context.Background(), tenant, request)
	if err != nil {
		t.Fatalf("test binding: %v", err)
	}
	if tested.Verdict != sessions.ProtocolObservationClean || tested.Code != "remote_completed" ||
		tested.Binding.Version != binding.Version || tested.Binding.RemoteState != "working" ||
		tested.Replayed || client.reconcileCall != 1 {
		t.Fatalf("test result = %#v; calls=%d", tested, client.reconcileCall)
	}
	if store.binding.Version != binding.Version || store.binding.RemoteState != "working" {
		t.Fatalf("test changed durable binding: %#v", store.binding)
	}

	applied, err := executor.ReconcileProtocolBinding(context.Background(), tenant, request)
	if err != nil {
		t.Fatalf("apply binding: %v", err)
	}
	if applied.Verdict != sessions.ProtocolObservationClean || applied.Code != "remote_completed" ||
		applied.Binding.Version != binding.Version+1 || applied.Binding.RemoteState != "completed" ||
		applied.Binding.LocalState != "review" || !applied.Binding.Terminal ||
		client.reconcileCall != 2 {
		t.Fatalf("apply result = %#v; calls=%d", applied, client.reconcileCall)
	}
	if store.binding.ID != binding.ID || store.binding.Generation != binding.Generation ||
		store.binding.RemoteState != "completed" || store.binding.LastEventSeq <= binding.LastEventSeq {
		t.Fatalf("durable binding after apply = %#v", store.binding)
	}
}

func TestK5ProtocolBindingRESTRefusesStaleAndKeepsUnknownReadOnly(t *testing.T) {
	t.Run("stale", func(t *testing.T) {
		executor, store, client, tenant, binding := protocolRESTBindingFixture(t)
		store.binding.Version++
		_, err := executor.ReconcileProtocolBinding(context.Background(), tenant,
			sessions.ProtocolBindingReconcileRequest{
				Binding: binding, ExpectedVersion: binding.Version,
				SemanticKey: "binding-rest-stale", ExpectedPlanHash: strings.Repeat("b", 64),
			})
		if err == nil || client.reconcileCall != 0 {
			t.Fatalf("stale reconcile = %v; remote calls=%d", err, client.reconcileCall)
		}
	})

	t.Run("unknown test", func(t *testing.T) {
		executor, store, client, tenant, binding := protocolRESTBindingFixture(t)
		client.reconcileErr = errors.New("peer unavailable")
		result, err := executor.TestProtocolBinding(context.Background(), tenant,
			sessions.ProtocolBindingReconcileRequest{
				Binding: binding, ExpectedVersion: binding.Version,
				SemanticKey: "binding-rest-test-unknown", ExpectedPlanHash: strings.Repeat("c", 64),
			})
		if err != nil || result.Verdict != sessions.ProtocolObservationUnknown ||
			result.Code != "observe_unavailable" || result.Binding.Version != binding.Version {
			t.Fatalf("unknown test = %#v, %v", result, err)
		}
		if store.binding.Version != binding.Version || store.binding.ObservationCode != binding.ObservationCode {
			t.Fatalf("unknown test changed durable binding: %#v", store.binding)
		}
	})
}
