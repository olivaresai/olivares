// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

type protocolBindingReconcileFake struct {
	testCalls  int
	applyCalls int
}

func (f *protocolBindingReconcileFake) TestProtocolBinding(
	_ context.Context,
	_ model.TenantID,
	request sessions.ProtocolBindingReconcileRequest,
) (sessions.ProtocolBindingReconcileResult, error) {
	f.testCalls++
	return sessions.ProtocolBindingReconcileResult{Binding: request.Binding}, nil
}

func (f *protocolBindingReconcileFake) ReconcileProtocolBinding(
	_ context.Context,
	_ model.TenantID,
	request sessions.ProtocolBindingReconcileRequest,
) (sessions.ProtocolBindingReconcileResult, error) {
	f.applyCalls++
	return sessions.ProtocolBindingReconcileResult{Binding: request.Binding}, nil
}

func TestProtocolBindingReconcileMuxPreservesA2AAndRoutesMCP(t *testing.T) {
	mux := newProtocolBindingReconcileMux()
	a2aAdapter := &protocolBindingReconcileFake{}
	mcpAdapter := &protocolBindingReconcileFake{}
	if err := mux.Use(sessions.BindingProtocolA2A, a2aAdapter); err != nil {
		t.Fatal(err)
	}
	if err := mux.Use(sessions.BindingProtocolMCP, mcpAdapter); err != nil {
		t.Fatal(err)
	}

	tenant := model.NewTenantID()
	a2aRequest := sessions.ProtocolBindingReconcileRequest{
		Binding: sessions.ProtocolBinding{Protocol: sessions.BindingProtocolA2A},
	}
	if _, err := mux.TestProtocolBinding(context.Background(), tenant, a2aRequest); err != nil {
		t.Fatal(err)
	}
	mcpRequest := sessions.ProtocolBindingReconcileRequest{
		Binding: sessions.ProtocolBinding{Protocol: sessions.BindingProtocolMCP},
	}
	if _, err := mux.ReconcileProtocolBinding(context.Background(), tenant, mcpRequest); err != nil {
		t.Fatal(err)
	}
	if a2aAdapter.testCalls != 1 || a2aAdapter.applyCalls != 0 ||
		mcpAdapter.testCalls != 0 || mcpAdapter.applyCalls != 1 {
		t.Fatalf("unexpected route calls: a2a=%#v mcp=%#v", a2aAdapter, mcpAdapter)
	}
}

func TestProtocolBindingReconcileMuxRefusesUnwiredProtocol(t *testing.T) {
	mux := newProtocolBindingReconcileMux()
	_, err := mux.TestProtocolBinding(context.Background(), model.NewTenantID(),
		sessions.ProtocolBindingReconcileRequest{
			Binding: sessions.ProtocolBinding{Protocol: sessions.BindingProtocolMCP},
		})
	if err == nil {
		t.Fatal("expected unwired MCP protocol to be refused")
	}
}
