// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Command emptydescsource is a TEST FIXTURE: a real out-of-process source-connector
// plugin whose Descriptor does NOT name itself. It exists so the host's refusal of a
// malformed component can be measured against a real subprocess — the handshake
// succeeds, the connector is dispensed, and only then does the engine discover that
// the component has no identity. What has to be proved at that point is that the
// registration is refused AND the subprocess is reaped with its confinement, which
// cannot be shown with an in-process fake.
//
// It lives under testdata on purpose: the go tool excludes testdata from `./...`, so
// this is never built, embedded or shipped by anything but the test that names it.
// It is not a connector: it emits nothing and answers nothing beyond its own
// (deliberately invalid) self-description.
package main

import (
	"context"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/plugin"
)

type namelessSource struct{}

// Descriptor returns an EMPTY Name — the whole point of the fixture. Everything
// else is well formed, so the only thing the host can refuse it for is its identity.
func (namelessSource) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: "", Type: sdk.TypeSource, APIVersion: sdk.APIVersion}
}

// Open must never be reached: the host refuses the component before configuring it.
func (namelessSource) Open(context.Context, sdk.Config) error { return nil }

func (namelessSource) Gather(ctx context.Context, _ sdk.Sink) error {
	<-ctx.Done()
	return ctx.Err()
}

func (namelessSource) Close(context.Context) error { return nil }

func main() { plugin.ServeSource(namelessSource{}) }
