// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import "errors"

// ErrNotInEdition is the stable refusal from paid authoring in Community.
var ErrNotInEdition = errors.New("FinOps budgets are a Business feature: https://olivares.ai/pricing")
