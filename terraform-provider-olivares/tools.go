// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build tools

// Package tools pins the provider's build-time tooling so `go mod` keeps it in
// the module graph. tfplugindocs generates the Registry docs from the provider
// schema + the examples/ tree (see `task provider:docs`); it is never imported
// by the provider binary (the `tools` build tag excludes this file from builds).
package tools

import (
	_ "github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs"
)
