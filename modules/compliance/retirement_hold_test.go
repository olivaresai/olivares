// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"context"
	"github.com/olivaresai/olivares/core/model"
	"testing"
)

func TestCheckHoldUnavailableFailsClosed(t *testing.T) {
	for _, m := range []*Module{nil, New()} {
		if _, err := m.CheckHold(context.Background(), model.TenantID(model.NewID()), HoldSubject{Kind: "session", Ref: "session-ref", DataClass: "session.timeline"}); err == nil {
			t.Fatal("unavailable hold checker returned no error")
		}
	}
}
