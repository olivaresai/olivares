// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/modules/sessions"
)

type mcpProtocolReconcileUpstreamFake struct {
	requests []mcpc.UpstreamRequest
	result   mcpc.UpstreamResult
	err      error
}

func (f *mcpProtocolReconcileUpstreamFake) Forward(
	_ context.Context,
	request mcpc.UpstreamRequest,
) (mcpc.UpstreamResult, error) {
	request.Params = append([]byte(nil), request.Params...)
	request.Scopes = append([]string(nil), request.Scopes...)
	f.requests = append(f.requests, request)
	return f.result, f.err
}

func mcpProtocolReconcileFixture(
	t *testing.T,
) (*mcpDurableAdapterFixture, *mcpProtocolBindingReconciler, *mcpProtocolReconcileUpstreamFake, sessions.ProtocolBinding) {
	t.Helper()
	fixture := newMCPDurableAdapterFixture(t)
	intent := fixture.intent(
		"task-rest-mcp-1", "operation-rest-mcp-1", strings.Repeat("a", 64),
	)
	binding := fixture.seedBinding(intent, 1, false, "active", "working")
	fixture.bindings.bindings = []sessions.ProtocolBinding{binding}
	fixture.work.item = sessions.WorkItem{
		ID: binding.WorkItemID, WorkspaceID: fixture.config.WorkspaceID, Version: 3,
		Status: "active", OwnerKind: fixture.config.OwnerKind, OwnerRef: fixture.config.OwnerRef,
		OwnerEpoch: 1,
	}
	peer := &mcpProtocolReconcileUpstreamFake{result: mcpc.UpstreamResult{
		State: mcpc.DispatchCompleted,
		Result: json.RawMessage(`{
			"resultType":"complete",
			"taskId":"task-rest-mcp-1",
			"status":"completed",
			"statusMessage":"ready for governed review",
			"createdAt":"2026-08-20T10:00:00Z",
			"lastUpdatedAt":"2026-08-20T10:00:01Z",
			"ttlMs":60000,
			"pollIntervalMs":1000,
			"result":{"content":[]}
		}`),
	}}
	reconciler, err := newMCPProtocolBindingReconciler(fixture.store, peer)
	if err != nil {
		t.Fatal(err)
	}
	reconciler.now = func() time.Time {
		return time.Date(2026, 8, 20, 10, 0, 2, 0, time.UTC)
	}
	return fixture, reconciler, peer, binding
}

func mcpProtocolReconcileRequest(binding sessions.ProtocolBinding) sessions.ProtocolBindingReconcileRequest {
	return sessions.ProtocolBindingReconcileRequest{
		Binding: binding, ExpectedVersion: binding.Version,
		SemanticKey: "mcp-rest-reconcile-1", ExpectedPlanHash: strings.Repeat("b", 64),
	}
}

func TestMCPProtocolBindingReconcileTestIsReadOnlyAndApplyCommits(t *testing.T) {
	fixture, reconciler, peer, binding := mcpProtocolReconcileFixture(t)
	request := mcpProtocolReconcileRequest(binding)

	tested, err := reconciler.TestProtocolBinding(context.Background(), fixture.tenant, request)
	if err != nil {
		t.Fatalf("test MCP binding: %v", err)
	}
	if tested.Verdict != sessions.ProtocolObservationClean || tested.Code != "mcp_get" ||
		tested.Binding.Version != binding.Version || tested.Binding.RemoteState != "working" ||
		len(fixture.bindings.observations) != 0 || len(peer.requests) != 1 {
		t.Fatalf("test result=%#v observations=%d requests=%d",
			tested, len(fixture.bindings.observations), len(peer.requests))
	}

	applied, err := reconciler.ReconcileProtocolBinding(context.Background(), fixture.tenant, request)
	if err != nil {
		t.Fatalf("apply MCP binding: %v", err)
	}
	if applied.Verdict != sessions.ProtocolObservationClean || applied.Code != "mcp_get" ||
		applied.Binding.Version != binding.Version+1 || applied.Binding.RemoteState != "completed" ||
		applied.Binding.LocalState != "review" || !applied.Binding.Terminal ||
		len(fixture.bindings.observations) != 1 || len(peer.requests) != 2 {
		t.Fatalf("apply result=%#v observations=%d requests=%d",
			applied, len(fixture.bindings.observations), len(peer.requests))
	}
	forwarded := peer.requests[0]
	if forwarded.Method != "tasks/get" || forwarded.Subject != fixture.owner.Subject ||
		len(forwarded.Scopes) != 1 || forwarded.Scopes[0] != "search:read" ||
		forwarded.FenceToken != "1" || forwarded.OperationID == "" {
		t.Fatalf("forwarded request = %#v", forwarded)
	}
	var params struct {
		TaskID string                     `json:"taskId"`
		Meta   map[string]json.RawMessage `json:"_meta"`
	}
	if err := json.Unmarshal(forwarded.Params, &params); err != nil ||
		params.TaskID != binding.ExternalID ||
		params.Meta["io.modelcontextprotocol/protocolVersion"] == nil ||
		params.Meta["io.modelcontextprotocol/clientCapabilities"] == nil {
		t.Fatalf("final tasks/get params = %s, %v", forwarded.Params, err)
	}
}

func TestMCPProtocolBindingReconcileAcceptsExactAncestorAfterSuccessor(t *testing.T) {
	fixture, _, peer, binding := mcpProtocolReconcileFixture(t)
	oldSpec := fixture.bindings.specs[fixture.config.BindingSpecID]
	oldSpec.State = sessions.ProtocolBindingSpecSuperseded
	successor := mcpDurableSuccessorSpec(t, oldSpec)
	fixture.bindings.specs[oldSpec.ID] = oldSpec
	fixture.bindings.specs[successor.ID] = successor
	fixture.store.specs = mcpDurableSpecFake{specs: fixture.bindings.specs}
	fixture.store.config.BindingSpecID = successor.ID
	fixture.store.config.Generation = successor.Generation

	reconciler, err := newMCPProtocolBindingReconciler(fixture.store, peer)
	if err != nil {
		t.Fatal(err)
	}
	reconciler.now = func() time.Time { return time.Date(2026, 8, 20, 11, 0, 0, 0, time.UTC) }
	result, err := reconciler.TestProtocolBinding(
		context.Background(), fixture.tenant, mcpProtocolReconcileRequest(binding),
	)
	if err != nil || result.Verdict != sessions.ProtocolObservationClean || len(peer.requests) != 1 {
		t.Fatalf("ancestor REST reconcile = %#v, %v; requests=%d", result, err, len(peer.requests))
	}
}

func TestMCPProtocolBindingReconcileUnknownApplyPersistsNoPeerText(t *testing.T) {
	fixture, reconciler, peer, binding := mcpProtocolReconcileFixture(t)
	peer.err = errors.New("peer-private-transport-detail")
	peer.result = mcpc.UpstreamResult{State: mcpc.DispatchUnknown}

	result, err := reconciler.ReconcileProtocolBinding(
		context.Background(), fixture.tenant, mcpProtocolReconcileRequest(binding),
	)
	if err != nil {
		t.Fatalf("unknown MCP reconcile: %v", err)
	}
	if result.Verdict != sessions.ProtocolObservationUnknown || result.Code != "mcp_get" ||
		result.Binding.ObservationVerdict != sessions.ProtocolObservationUnknown ||
		result.Binding.RemoteState != binding.RemoteState || result.Binding.Terminal {
		t.Fatalf("unknown result = %#v", result)
	}
	if len(fixture.bindings.observations) != 1 ||
		strings.Contains(fixture.bindings.observations[0].Code, "peer-private") ||
		strings.Contains(string(fixture.bindings.observations[0].DetailHash), "peer-private") {
		t.Fatalf("peer detail reached observation: %#v", fixture.bindings.observations)
	}
}

func TestMCPProtocolBindingReconcileRefusesStaleBindingBeforePeerRead(t *testing.T) {
	fixture, reconciler, peer, binding := mcpProtocolReconcileFixture(t)
	fixture.bindings.bindings[0].Version++
	if _, err := reconciler.TestProtocolBinding(
		context.Background(), fixture.tenant, mcpProtocolReconcileRequest(binding),
	); err == nil || len(peer.requests) != 0 {
		t.Fatalf("stale reconcile error=%v requests=%d", err, len(peer.requests))
	}
}
