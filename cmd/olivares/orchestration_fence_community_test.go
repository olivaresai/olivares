// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise || !addon_ids

package main

import "testing"

func editionOrchestrationFenceWriters() []fenceWriter { return nil }

const editionOrchestrationAvailable = false

func editionOrchestrationTestLicense(*testing.T) {}
