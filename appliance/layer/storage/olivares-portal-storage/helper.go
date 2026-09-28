// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"

	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
	"github.com/olivaresai/olivares/appliance/layer/helpers/invocation"
	"github.com/olivaresai/olivares/appliance/layer/storage"
)

// helper is the storage helper over read: the inventory read, admitted by the seam for the
// Appliance Console alone, answered with the inventory document.
func helper(read storage.Reader) invocation.Helper {
	return invocation.Helper{
		Name:       storage.HelperName,
		Rules:      storage.HelperRules(),
		NewRequest: func() helperschema.Request { return &storage.InventoryRequest{} },
		Perform: func(ctx context.Context, _ helperschema.Peer, _ helperschema.Request) helperschema.Response {
			return storage.Answer(read(ctx))
		},
	}
}
