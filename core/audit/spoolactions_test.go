// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package audit

import (
	"testing"

	"github.com/olivaresai/olivares/core/store"
)

func TestStoreAuditActionTwins(t *testing.T) {
	if store.ActionAuditCheckpoint != ActionCheckpoint {
		t.Fatalf("store checkpoint action = %q, audit action = %q", store.ActionAuditCheckpoint, ActionCheckpoint)
	}
	if store.ActionAuditArchiveSegment != ActionArchiveSegment {
		t.Fatalf("store archive action = %q, audit action = %q", store.ActionAuditArchiveSegment, ActionArchiveSegment)
	}
	if store.ActionAuditGap != ActionGap {
		t.Fatalf("store gap action = %q, audit action = %q", store.ActionAuditGap, ActionGap)
	}
}
