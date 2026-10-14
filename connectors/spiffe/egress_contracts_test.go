// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package spiffe

import (
	"context"
	"errors"
	"testing"
)

func TestEmergentSeamDenyClosed(t *testing.T) {
	// Even with the flag "enabled", there is no in-tree backend, so the seam stays
	// deny-closed — we never pretend a pre-RFC draft is implemented.
	for _, enabled := range []bool{false, true} {
		seam := EmergentIdentity(enabled)
		_, err := seam.PresentWorkloadIdentity(context.Background(), AnthropicAudience)
		if !errors.Is(err, ErrEmergentIdentityDisabled) {
			t.Errorf("enabled=%v: emergent seam must be deny-closed, got err=%v", enabled, err)
		}
	}
}
