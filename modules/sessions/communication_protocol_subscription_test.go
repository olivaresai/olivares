// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func protocolSubscriptionRouteForTest(workspace model.ID, subject, filter string) ProtocolSubscriptionRoute {
	digest := sha256.Sum256([]byte(filter))
	return ProtocolSubscriptionRoute{
		WorkspaceID: workspace, Protocol: BindingProtocolMCP,
		PeerAuthority: "https://mcp.example", Subject: subject,
		FilterDigest: hex.EncodeToString(digest[:]),
	}
}

func appendProtocolSubscriptionForTest(
	t *testing.T,
	m *Module,
	tenant model.TenantID,
	route ProtocolSubscriptionRoute,
	expected string,
	ordinal int,
) ProtocolSubscriptionEvent {
	t.Helper()
	event, err := m.AppendProtocolSubscriptionEvent(context.Background(), tenant, ProtocolSubscriptionAppend{
		Route: route, ExpectedCursor: expected, Method: "notifications/tools/list_changed",
		Params: json.RawMessage(`{"change":` + string(rune('0'+ordinal)) + `}`),
	})
	if err != nil {
		t.Fatalf("append protocol subscription event %d: %v", ordinal, err)
	}
	return event
}

func TestProtocolSubscriptionSchemaSeparatesCASHeadFromAppendOnlyEvents(t *testing.T) {
	t.Parallel()

	registry := communicationCaptureSchema(t)
	descriptors := make(map[model.Kind]model.EntityDescriptor)
	for _, descriptor := range registry.descriptors {
		if descriptor.Kind == protocolSubscriptionCursorKind || descriptor.Kind == protocolSubscriptionEventKind {
			descriptors[descriptor.Kind] = descriptor
		}
	}
	head, headOK := descriptors[protocolSubscriptionCursorKind]
	events, eventsOK := descriptors[protocolSubscriptionEventKind]
	if !headOK || head.AppendOnly || head.RetainOnTenantDrop ||
		!eventsOK || !events.AppendOnly || events.RetainOnTenantDrop {
		t.Fatalf("subscription descriptors: head=%#v events=%#v", head, events)
	}
	for _, descriptor := range []model.EntityDescriptor{head, events} {
		if descriptor.WorkspaceLineage != hiddenWorkspaceLineage {
			t.Fatalf("%s workspace lineage = %#v", descriptor.Kind, descriptor.WorkspaceLineage)
		}
		if _, leaked := communicationDescriptorFields(descriptor)["subject"]; leaked {
			t.Fatalf("%s persists raw subject", descriptor.Kind)
		}
	}

	wantRouteUnique := []string{model.ColTenantID, colWorkWorkspaceID, colProtocolSubscriptionRouteHash}
	wantSeqUnique := []string{model.ColTenantID, colProtocolSubscriptionHeadID, colProtocolSubscriptionCursorSeq}
	routeUnique, seqUnique, cursorUnique := false, false, false
	for _, index := range head.Indexes {
		routeUnique = routeUnique || index.Unique && slices.Equal(index.Columns, wantRouteUnique)
	}
	for _, index := range events.Indexes {
		seqUnique = seqUnique || index.Unique && slices.Equal(index.Columns, wantSeqUnique)
		cursorUnique = cursorUnique || index.Unique && slices.Equal(
			index.Columns, []string{model.ColTenantID, colProtocolSubscriptionCursorID},
		)
	}
	if !routeUnique || !seqUnique || !cursorUnique {
		t.Fatalf("subscription unique indexes: head=%#v events=%#v", head.Indexes, events.Indexes)
	}
}

func TestProtocolSubscriptionLedgerPersistsCatchUpAcrossRestart(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "protocol-subscription.db")
	fixture := newWorkFixture(t, dbPath, nil)
	route := protocolSubscriptionRouteForTest(fixture.workspace, "agent:governed", "tools-list")

	first := appendProtocolSubscriptionForTest(t, fixture.m, fixture.tenant, route, "", 1)
	second := appendProtocolSubscriptionForTest(t, fixture.m, fixture.tenant, route, first.Cursor, 2)
	if first.Seq != 1 || second.Seq != 2 || second.PreviousEventID != first.ID {
		t.Fatalf("subscription chain first=%#v second=%#v", first, second)
	}
	if _, err := fixture.m.AppendProtocolSubscriptionEvent(
		context.Background(), fixture.tenant, ProtocolSubscriptionAppend{
			Route: route, ExpectedCursor: first.Cursor,
			Method: "notifications/tools/list_changed", Params: json.RawMessage(`{"change":3}`),
		},
	); !errors.Is(err, ErrProtocolSubscriptionConflict) {
		t.Fatalf("stale cursor append error = %v, want conflict", err)
	}

	if err := fixture.st.Close(); err != nil {
		t.Fatalf("close subscription store before restart: %v", err)
	}
	restarted := New()
	st, err := engine.Open(context.Background(), store.Config{
		Engine: store.EngineSQLite, DSN: dbPath, Debug: true,
	}, restarted.RegisterSchema)
	if err != nil {
		t.Fatalf("restart subscription store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	restarted.UseData(api.NewModuleData(st))

	page, err := restarted.CatchUpProtocolSubscription(
		context.Background(), fixture.tenant,
		ProtocolSubscriptionCatchUp{Route: route, Cursor: first.Cursor, Limit: 1},
	)
	if err != nil {
		t.Fatalf("catch up after restart: %v", err)
	}
	if len(page.Events) != 1 || page.Events[0].ID != second.ID || page.NextCursor != second.Cursor || page.HasMore {
		t.Fatalf("restart catch up page = %#v", page)
	}
	if _, err := restarted.CatchUpProtocolSubscription(
		context.Background(), fixture.tenant,
		ProtocolSubscriptionCatchUp{
			Route:  protocolSubscriptionRouteForTest(fixture.workspace, "agent:other", "tools-list"),
			Cursor: first.Cursor,
		},
	); !errors.Is(err, ErrProtocolSubscriptionCursor) {
		t.Fatalf("cross-subject cursor error = %v, want invalid cursor", err)
	}
}
