// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"strings"

	"github.com/olivaresai/olivares/appliance/layer/helpers/invocation"
)

// invocationServe serves one connection of h to the peer k attests, with document on standard
// input.
func invocationServe(h invocation.Helper, k invocation.Kernel, args []string, document string, out, log *bytes.Buffer) int {
	return invocation.Serve(context.Background(), h, k, args, 0, strings.NewReader(document), out, log)
}
