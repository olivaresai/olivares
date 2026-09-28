// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package gitpublish

import (
	"context"

	"github.com/olivaresai/olivares/sdk"
)

// Name is the module's stable runtime registry identifier.
const Name = "olivares.gitpublish"

var _ sdk.Module = (*Module)(nil)

// Descriptor identifies Git publication to the engine runtime.
func (m *Module) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeModule,
		Title:       "Git publication",
		Description: "Authorized pushes, pull requests and merges with recorded requests and observed outcomes.",
	}
}

// Init has no host subscriptions. The composition root binds publication ports.
func (m *Module) Init(context.Context, sdk.Host) error { return nil }

// Start owns no background work. The engine scheduler drives SweepPump.
func (m *Module) Start(context.Context) error { return nil }

// Stop releases no owned goroutines or subscriptions and is idempotent.
func (m *Module) Stop(context.Context) error { return nil }
