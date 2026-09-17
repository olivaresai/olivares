// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"reflect"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// TestChannelGrantCatalogIndexPinsTheMeasuredShape pins the ChannelGrant catalog
// index to the exact column order whose plan and examined-row cost are measured
// on both engines by the store's own fixture
// (core/internal/store/sqlstore, TestCatalogProjectionPlanAndWorkBothEngines over
// a mirrored `dpt_grant` relation). core cannot import this module, so the two
// sides pin the same literal: a change to either order fails one of the two
// tests instead of silently measuring an index the product no longer declares.
// The columns the projection binds are asserted against the same list, so the
// per-subject arm keeps every equality column ahead of the projected Channel ID.
func TestChannelGrantCatalogIndexPinsTheMeasuredShape(t *testing.T) {
	t.Parallel()
	reg := communicationCaptureSchema(t)
	descriptor := communicationDescriptor(t, reg, channelGrantKind)
	want := []string{
		"tenant_id", "workspace_id", "subject_kind", "subject_ref", "state", "can_read", "channel_id", "expires_at",
	}
	var found *model.IndexSpec
	for index := range descriptor.Indexes {
		if descriptor.Indexes[index].Name == "sessions_channel_grant_catalog" {
			found = &descriptor.Indexes[index]
		}
	}
	if found == nil {
		t.Fatalf("sessions_channel_grant_catalog is not declared on %s", channelGrantKind)
	}
	if found.Unique || !reflect.DeepEqual(found.Columns, want) {
		t.Fatalf("catalog index = %+v, want non-unique %v", *found, want)
	}
	// The projection's common equality columns, the alternative's equality
	// columns, the projected column and the expiry column are exactly the index
	// columns after the tenant and workspace lineage the store forces.
	projected := []string{
		model.ColTenantID, colWorkWorkspaceID, colCommSubjectKind, colCommSubjectRef,
		colCommState, colCommCanRead, colCommChannelID, colCommExpiresAt,
	}
	if !reflect.DeepEqual(projected, want) {
		t.Fatalf("projection columns %v differ from the index %v", projected, want)
	}
}
