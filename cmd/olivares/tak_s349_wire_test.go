// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/sdk"
)

// TestS349TAKConnectorWiredAndCoTFeedScopeable is the wire-proof that the TAK
// connector is a real, reachable capability of the production binary — not merged code
// that no server path exercises ("código mergeado != capacidad entregada"). It proves
// two things end-to-end:
//
//	a) The tak connector is CONSTRUCTIBLE through buildInProcSource — the exact
//	   in-process source builder the production `serve`/`collector` wiring calls
//	   (wireSources → buildInProcSource, sources.go). A green run means an operator's
//	   OLIVARES_SOURCES_CONFIG entry `{"kind":"tak"}` resolves to a live connector whose
//	   stable descriptor name is "olivares.tak", not an "unknown kind" WARN.
//
//	b) The CoT feed is SCOPEABLE through the FULLY-wired production server: a
//	   sourcescope binding on the subject axes (scope_tree=agent) governs the feed
//	   addressed as source_type=data, source_ref=tak (the connector's feed_ref). A
//	   forbid on agent "drone-1" is created and read back with its effect preserved,
//	   proving the same subject-axis machinery that governs models/knowledge also
//	   governs a Cursor-on-Target feed — the whole point of ingesting CoT as governed
//	   signal rather than raw telemetry.
func TestS349TAKConnectorWiredAndCoTFeedScopeable(t *testing.T) {
	// (a) Constructible through the real production source builder.
	conn, ok := buildInProcSource("tak")
	if !ok {
		t.Fatal("buildInProcSource(\"tak\") = _, false: the tak connector is NOT wired into the production source builder")
	}
	if conn == nil {
		t.Fatal("buildInProcSource(\"tak\") returned a nil sdk.SourceConnector")
	}
	var _ sdk.SourceConnector = conn // the production wiring depends on exactly this type
	if got := conn.Descriptor().Name; got != "olivares.tak" {
		t.Fatalf("tak connector Descriptor().Name = %q, want %q", got, "olivares.tak")
	}

	// (b) The CoT feed is scopeable end-to-end through the production server.
	h := newHarness(t)
	tenant := h.createOrg("Falcon Squadron", "falcon-s349")

	var created struct {
		ID         string `json:"id"`
		SourceType string `json:"source_type"`
		SourceRef  string `json:"source_ref"`
		ScopeTree  string `json:"scope_tree"`
		ScopeRef   string `json:"scope_ref"`
		Effect     string `json:"effect"`
	}
	code := h.reqInto("POST", "/v1/m/sourcescope/bindings", h.adminToken, tenant, map[string]any{
		"source_type": "data", "source_ref": "tak", "scope_tree": "agent",
		"scope_ref": "drone-1", "effect": "forbid", "enabled": true,
	}, &created)
	if code != http.StatusCreated {
		t.Fatalf("scope the tak CoT feed (agent forbid) through the production server = %d", code)
	}
	if created.SourceType != "data" || created.SourceRef != "tak" ||
		created.ScopeTree != "agent" || created.ScopeRef != "drone-1" || created.Effect != "forbid" {
		t.Fatalf("created CoT-feed binding wire shape = %+v", created)
	}

	// Read it back through the real server: the subject axis + forbid effect round-trip.
	got := h.getJSON(h.adminToken, tenant, "/v1/m/sourcescope/bindings/"+created.ID)
	if got["source_type"] != "data" || got["source_ref"] != "tak" ||
		got["scope_tree"] != "agent" || got["scope_ref"] != "drone-1" || got["effect"] != "forbid" {
		t.Fatalf("read-back CoT-feed binding = %v", got)
	}
}
