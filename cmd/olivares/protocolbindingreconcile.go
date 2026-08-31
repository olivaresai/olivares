// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"fmt"
	"sync"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

// protocolBindingReconcileMux keeps the sessions REST surface protocol-neutral.
// Routes may be installed after module construction because the MCP gateway is
// composed later than the sessions module; reads and late registration are
// synchronized.
type protocolBindingReconcileMux struct {
	mu     sync.RWMutex
	routes map[sessions.BindingProtocol]sessions.ProtocolBindingRemoteReconciler
}

var _ sessions.ProtocolBindingRemoteReconciler = (*protocolBindingReconcileMux)(nil)

func newProtocolBindingReconcileMux() *protocolBindingReconcileMux {
	return &protocolBindingReconcileMux{
		routes: make(map[sessions.BindingProtocol]sessions.ProtocolBindingRemoteReconciler),
	}
}

func (m *protocolBindingReconcileMux) Use(
	protocol sessions.BindingProtocol,
	reconciler sessions.ProtocolBindingRemoteReconciler,
) error {
	if m == nil || reconciler == nil ||
		(protocol != sessions.BindingProtocolA2A && protocol != sessions.BindingProtocolMCP) {
		return fmt.Errorf("protocol binding reconcile: invalid adapter registration")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if current := m.routes[protocol]; current != nil {
		return fmt.Errorf("protocol binding reconcile: adapter for %q is already registered", protocol)
	}
	m.routes[protocol] = reconciler
	return nil
}

func (m *protocolBindingReconcileMux) route(
	protocol sessions.BindingProtocol,
) (sessions.ProtocolBindingRemoteReconciler, error) {
	if m == nil {
		return nil, fmt.Errorf("protocol binding reconcile: multiplexer is unavailable")
	}
	m.mu.RLock()
	reconciler := m.routes[protocol]
	m.mu.RUnlock()
	if reconciler == nil {
		return nil, fmt.Errorf(
			"protocol binding reconcile: protocol %q has no composed adapter", protocol,
		)
	}
	return reconciler, nil
}

func (m *protocolBindingReconcileMux) TestProtocolBinding(
	ctx context.Context,
	tenant model.TenantID,
	request sessions.ProtocolBindingReconcileRequest,
) (sessions.ProtocolBindingReconcileResult, error) {
	reconciler, err := m.route(request.Binding.Protocol)
	if err != nil {
		return sessions.ProtocolBindingReconcileResult{}, err
	}
	return reconciler.TestProtocolBinding(ctx, tenant, request)
}

func (m *protocolBindingReconcileMux) ReconcileProtocolBinding(
	ctx context.Context,
	tenant model.TenantID,
	request sessions.ProtocolBindingReconcileRequest,
) (sessions.ProtocolBindingReconcileResult, error) {
	reconciler, err := m.route(request.Binding.Protocol)
	if err != nil {
		return sessions.ProtocolBindingReconcileResult{}, err
	}
	return reconciler.ReconcileProtocolBinding(ctx, tenant, request)
}
