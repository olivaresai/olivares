// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package audit_test

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/core/audit"
)

// TestNopForwarder confirms the OBS-08(B) seam exists and the default is a safe
// no-op (ledger push is unavailable until transport lands).
func TestNopForwarder(t *testing.T) {
	var f audit.Forwarder = audit.NopForwarder{}
	if err := f.Forward(context.Background(), signedEvent()); err != nil {
		t.Fatalf("NopForwarder.Forward must not error: %v", err)
	}
}
