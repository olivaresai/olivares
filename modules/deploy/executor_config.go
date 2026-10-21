// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package deploy

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
)

// ExecutorConfig is the saved local-Docker setup. Credentials remain references
// into the tenant's existing sealed secret store. Other backends keep their
// published operator configuration until their product setup is qualified.
type ExecutorConfig struct {
	SocketPath    string `json:"socket_path"`
	CredentialRef string `json:"credential_ref"`
}

// ExecutorSetup reuses the composition root's governed executor and secret store.
// Build does no infrastructure mutation. Test checks the credential and daemon
// connection read-only; it must never plan, create, start, stop or remove a unit.
type ExecutorSetup interface {
	Build(context.Context, model.TenantID, ExecutorConfig) (Executor, error)
	Test(context.Context, model.TenantID, ExecutorConfig) error
}

// WithExecutorSetup enables saved configuration. An explicit operator-file
// override remains authoritative even when it wires no backend.
func WithExecutorSetup(setup ExecutorSetup, operatorOverride bool) Option {
	return func(m *Module) { m.executorSetup, m.executorOverride = setup, operatorOverride }
}
