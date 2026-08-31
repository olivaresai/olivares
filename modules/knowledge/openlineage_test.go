// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

func TestBuildOLEventAllowed(t *testing.T) {
	lr := lineageRow{
		kbRef:       model.ID("kb-123"),
		agentRef:    "agent-1",
		queryHash:   "abc123",
		chunkRefs:   []chunkRef{{ChunkID: "c1", KBRef: "kb-123", DocRef: "d1", SourceKind: "sharepoint", SourceRef: "sp1", ContentHash: "h1"}},
		sourceRefs:  []string{"d1"},
		region:      "eu-west-1",
		decision:    decisionAllowed,
		egress:      false,
		resultCount: 1,
	}
	ev := buildOLEvent(lr, "lineage-001", "local-hash")

	if ev.EventType != "COMPLETE" {
		t.Errorf("EventType = %q, want COMPLETE", ev.EventType)
	}
	if ev.Producer != Name {
		t.Errorf("Producer = %q, want %q", ev.Producer, Name)
	}
	if ev.SchemaURL != olSchemaURL {
		t.Errorf("SchemaURL = %q, want %q", ev.SchemaURL, olSchemaURL)
	}
	if ev.Job.Namespace != Name {
		t.Errorf("Job.Namespace = %q, want %q", ev.Job.Namespace, Name)
	}
	if ev.Job.Name != "retrieval/kb-123" {
		t.Errorf("Job.Name = %q, want retrieval/kb-123", ev.Job.Name)
	}
	if ev.Run.RunID != "lineage-001" {
		t.Errorf("Run.RunID = %q, want lineage-001", ev.Run.RunID)
	}
	if len(ev.Inputs) != 1 {
		t.Fatalf("Inputs len = %d, want 1", len(ev.Inputs))
	}
	if ev.Inputs[0].Namespace != "sharepoint" {
		t.Errorf("Inputs[0].Namespace = %q, want sharepoint", ev.Inputs[0].Namespace)
	}
	if ev.Inputs[0].Name != "d1" {
		t.Errorf("Inputs[0].Name = %q, want d1", ev.Inputs[0].Name)
	}
	if len(ev.Outputs) != 1 {
		t.Fatalf("Outputs len = %d, want 1", len(ev.Outputs))
	}
	if ev.Outputs[0].Namespace != Name {
		t.Errorf("Outputs[0].Namespace = %q, want %q", ev.Outputs[0].Namespace, Name)
	}
	if ev.Outputs[0].Name != "response/lineage-001" {
		t.Errorf("Outputs[0].Name = %q, want response/lineage-001", ev.Outputs[0].Name)
	}

	gov, ok := ev.Run.Facets["olivares:governance"].(map[string]any)
	if !ok {
		t.Fatal("olivares:governance facet missing or wrong type")
	}
	if gov["decision"] != decisionAllowed {
		t.Errorf("governance.decision = %v, want %q", gov["decision"], decisionAllowed)
	}
	if gov["egress"] != false {
		t.Errorf("governance.egress = %v, want false", gov["egress"])
	}
	if gov["region"] != "eu-west-1" {
		t.Errorf("governance.region = %v, want eu-west-1", gov["region"])
	}
	if gov["result_count"] != 1 {
		t.Errorf("governance.result_count = %v, want 1", gov["result_count"])
	}

	prov, ok := ev.Run.Facets["olivares:provenance"].(map[string]any)
	if !ok {
		t.Fatal("olivares:provenance facet missing or wrong type")
	}
	if prov["embed_model"] != "local-hash" {
		t.Errorf("provenance.embed_model = %v, want local-hash", prov["embed_model"])
	}
	if prov["query_hash"] != "abc123" {
		t.Errorf("provenance.query_hash = %v, want abc123", prov["query_hash"])
	}
	if prov["chunk_count"] != 1 {
		t.Errorf("provenance.chunk_count = %v, want 1", prov["chunk_count"])
	}
}

func TestBuildOLEventDenied(t *testing.T) {
	lr := lineageRow{
		kbRef:    model.ID("kb-1"),
		decision: decisionDenied,
	}
	ev := buildOLEvent(lr, "l-2", "local-hash")
	if ev.EventType != "FAIL" {
		t.Errorf("EventType = %q, want FAIL", ev.EventType)
	}
	if ev.Job.Name != "retrieval/kb-1" {
		t.Errorf("Job.Name = %q, want retrieval/kb-1", ev.Job.Name)
	}
}

func TestBuildOLEventDedupsInputs(t *testing.T) {
	lr := lineageRow{
		kbRef:    model.ID("kb-1"),
		decision: decisionAllowed,
		chunkRefs: []chunkRef{
			{ChunkID: "c1", DocRef: "d1", SourceKind: "gdrive", SourceRef: "g1"},
			{ChunkID: "c2", DocRef: "d1", SourceKind: "gdrive", SourceRef: "g1"}, // same doc → dedup
		},
	}
	ev := buildOLEvent(lr, "l-3", "local-hash")
	if len(ev.Inputs) != 1 {
		t.Errorf("Inputs len = %d, want 1 (deduped by sourceKind+docRef)", len(ev.Inputs))
	}
	if ev.Inputs[0].Namespace != "gdrive" {
		t.Errorf("Inputs[0].Namespace = %q, want gdrive", ev.Inputs[0].Namespace)
	}
	// Provenance chunk_count reflects the raw count (2 chunks), not the deduped input count.
	prov := ev.Run.Facets["olivares:provenance"].(map[string]any)
	if prov["chunk_count"] != 2 {
		t.Errorf("provenance.chunk_count = %v, want 2", prov["chunk_count"])
	}
}

func TestBuildOLEventEmptyChunks(t *testing.T) {
	// A denied retrieval with no chunks should produce an empty inputs slice and
	// a single response output, without panicking.
	lr := lineageRow{
		kbRef:    model.ID("kb-empty"),
		decision: decisionDenied,
	}
	ev := buildOLEvent(lr, "l-4", "local-hash")
	if ev.EventType != "FAIL" {
		t.Errorf("EventType = %q, want FAIL", ev.EventType)
	}
	if len(ev.Inputs) != 0 {
		t.Errorf("Inputs len = %d, want 0 for a denied retrieval", len(ev.Inputs))
	}
	if len(ev.Outputs) != 1 {
		t.Errorf("Outputs len = %d, want 1", len(ev.Outputs))
	}
}

func TestBuildOLEventMultipleDistinctDocs(t *testing.T) {
	lr := lineageRow{
		kbRef:    model.ID("kb-multi"),
		decision: decisionAllowed,
		chunkRefs: []chunkRef{
			{ChunkID: "c1", DocRef: "doc-a", SourceKind: "sharepoint", SourceRef: "sp-a"},
			{ChunkID: "c2", DocRef: "doc-a", SourceKind: "sharepoint", SourceRef: "sp-a"}, // dup
			{ChunkID: "c3", DocRef: "doc-b", SourceKind: "sharepoint", SourceRef: "sp-b"}, // distinct
			{ChunkID: "c4", DocRef: "doc-c", SourceKind: "gdrive", SourceRef: "gd-c"},     // distinct source kind
		},
	}
	ev := buildOLEvent(lr, "l-5", "text-embedding-3")
	if len(ev.Inputs) != 3 {
		t.Errorf("Inputs len = %d, want 3 (3 unique sourceKind/docRef pairs)", len(ev.Inputs))
	}
	prov := ev.Run.Facets["olivares:provenance"].(map[string]any)
	if prov["chunk_count"] != 4 {
		t.Errorf("provenance.chunk_count = %v, want 4 (raw chunk count)", prov["chunk_count"])
	}
	if prov["embed_model"] != "text-embedding-3" {
		t.Errorf("provenance.embed_model = %v, want text-embedding-3", prov["embed_model"])
	}
}
