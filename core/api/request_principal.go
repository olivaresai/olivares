// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"github.com/olivaresai/olivares/core/auth"
)

// RequestPrincipal returns the identity authenticated by the engine middleware.
// It lets an in-process launch bind a narrow child credential to that identity.
// Request bodies and launch parameters cannot populate this context value.
func RequestPrincipal(ctx context.Context) (auth.Principal, bool) { return principalFrom(ctx) }
