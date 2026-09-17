// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"reflect"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

func channelAdministrationIndex(
	t *testing.T,
	descriptor model.EntityDescriptor,
	name string,
) model.IndexSpec {
	t.Helper()
	for index := range descriptor.Indexes {
		if descriptor.Indexes[index].Name == name {
			return descriptor.Indexes[index]
		}
	}
	t.Fatalf("%s is not declared on %s", name, descriptor.Kind)
	return model.IndexSpec{}
}

// TestChannelGrantAdministrationIndexPinsTheProjectedShape pins the
// administrative projection index to the exact column order the projection
// binds, in the SAME shape the read catalog's measured index already has, with
// can_admin where that one has can_read. Pinning both sides — the declared index
// and the columns the statement binds — is what stops one from drifting into
// measuring an index the product no longer declares.
func TestChannelGrantAdministrationIndexPinsTheProjectedShape(t *testing.T) {
	t.Parallel()
	reg := communicationCaptureSchema(t)
	descriptor := communicationDescriptor(t, reg, channelGrantKind)
	want := []string{
		"tenant_id", "workspace_id", "subject_kind", "subject_ref", "state", "can_admin", "channel_id", "expires_at",
	}
	found := channelAdministrationIndex(t, descriptor, "sessions_channel_grant_administration")
	if found.Unique || !reflect.DeepEqual(found.Columns, want) {
		t.Fatalf("administration index = %+v, want non-unique %v", found, want)
	}
	projected := []string{
		model.ColTenantID, colWorkWorkspaceID, colCommSubjectKind, colCommSubjectRef,
		colCommState, colCommCanAdmin, colCommChannelID, colCommExpiresAt,
	}
	if !reflect.DeepEqual(projected, want) {
		t.Fatalf("projection columns %v differ from the index %v", projected, want)
	}
	// The read catalog's own index is untouched and still leads with can_read:
	// the administrative surface adds an index, it does not widen or reorder the
	// one whose plan and examined-row cost are measured on both engines.
	catalog := channelAdministrationIndex(t, descriptor, "sessions_channel_grant_catalog")
	catalogWant := []string{
		"tenant_id", "workspace_id", "subject_kind", "subject_ref", "state", "can_read", "channel_id", "expires_at",
	}
	if catalog.Unique || !reflect.DeepEqual(catalog.Columns, catalogWant) {
		t.Fatalf("read catalog index changed: %+v, want non-unique %v", catalog, catalogWant)
	}
}

// TestChannelGrantHistoryIndexesPinTheKeysetShapes pins the two indexes the
// administrative sheet's keyset page binds. The `all` selection filters by
// Channel only and orders by id, so the id column has to come BEFORE the state
// column — the pre-existing sessions_channel_grant_channel index leads with
// state and cannot serve that shape as a range scan.
func TestChannelGrantHistoryIndexesPinTheKeysetShapes(t *testing.T) {
	t.Parallel()
	reg := communicationCaptureSchema(t)
	descriptor := communicationDescriptor(t, reg, channelGrantKind)

	history := channelAdministrationIndex(t, descriptor, "sessions_channel_grant_history")
	wantHistory := []string{"tenant_id", "channel_id", "id", "state"}
	if history.Unique || !reflect.DeepEqual(history.Columns, wantHistory) {
		t.Fatalf("history index = %+v, want non-unique %v", history, wantHistory)
	}
	bound := []string{model.ColTenantID, colCommChannelID, model.ColID, colCommState}
	if !reflect.DeepEqual(bound, wantHistory) {
		t.Fatalf("history page columns %v differ from the index %v", bound, wantHistory)
	}

	subject := channelAdministrationIndex(t, descriptor, "sessions_channel_grant_subject_history")
	wantSubject := []string{
		"tenant_id", "channel_id", "subject_kind", "subject_ref", "id", "state",
	}
	if subject.Unique || !reflect.DeepEqual(subject.Columns, wantSubject) {
		t.Fatalf("subject history index = %+v, want non-unique %v", subject, wantSubject)
	}
	boundSubject := []string{
		model.ColTenantID, colCommChannelID, colCommSubjectKind, colCommSubjectRef,
		model.ColID, colCommState,
	}
	if !reflect.DeepEqual(boundSubject, wantSubject) {
		t.Fatalf("subject page columns %v differ from the index %v", boundSubject, wantSubject)
	}

	// The pre-existing per-Channel index keeps its own shape: nothing here
	// degrades or reorders a declaration another read already depends on.
	channel := channelAdministrationIndex(t, descriptor, "sessions_channel_grant_channel")
	if channel.Unique || !reflect.DeepEqual(channel.Columns,
		[]string{"tenant_id", "channel_id", "state", "id"}) {
		t.Fatalf("existing per-Channel index changed: %+v", channel)
	}
}

// TestChannelGrantAdministrationAddsNoDurableRelation proves the administrative
// read model declares NO new entity kind: it adds indexes to the existing
// ChannelGrant relation and nothing else. A new durable relation would need its
// own migration, its own guard reasoning and its own retention answer.
func TestChannelGrantAdministrationAddsNoDurableRelation(t *testing.T) {
	t.Parallel()
	reg := communicationCaptureSchema(t)
	kinds := make(map[model.Kind]bool, len(reg.descriptors))
	for _, descriptor := range reg.descriptors {
		kinds[descriptor.Kind] = true
	}
	for _, forbidden := range []model.Kind{
		"sessions.channel_administration", "sessions.channel_grant_administration",
		"sessions.channel_admin_view", "sessions.channel_grant_history",
	} {
		if kinds[forbidden] {
			t.Fatalf("the administrative read model declared a durable relation %q", forbidden)
		}
	}
	for _, kind := range CommunicationSchemaKinds() {
		if !kinds[kind] {
			t.Fatalf("declared K3 kind %s disappeared from the registered schema", kind)
		}
	}
}
