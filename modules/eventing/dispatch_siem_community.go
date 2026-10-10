// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

package eventing

// Community has no SIEM renderer, so stored SIEM deliveries stay parked.
func classifyDispatchBody(string, []byte, bool) (string, bool) { return "", false }
