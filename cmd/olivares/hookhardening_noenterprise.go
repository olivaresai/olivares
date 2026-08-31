// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

package main

// newHookHardening returns NO hooks-hardening admin engine in the default (AGPL) build:
// the `olivares hooks attest` / `olivares hooks conform` verbs answer honestly that they require
// an enterprise build (hookhardeninggate.go). A nil engine means the default artifact never links
// enterprise/hookhardening for these verbs. Build with -tags enterprise to wire it
// (hookhardening_enterprise.go).
func newHookHardening() hookHardeningEngine { return nil }
