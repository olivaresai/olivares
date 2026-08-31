// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

var capTime = time.Date(2026, 6, 3, 9, 0, 0, 0, time.UTC)

func TestCapabilityEdges(t *testing.T) {
	cat := catalog{
		tools: []Tool{
			{Name: "read_file", Annotations: &ToolAnnotations{ReadOnlyHint: ptrBool(true)}},
			{Name: "delete_file", Annotations: &ToolAnnotations{DestructiveHint: ptrBool(true)}},
			{Name: "no_annotations"},
		},
		resources: []Resource{{URI: "file:///etc/hosts", Name: "hosts"}},
		templates: []ResourceTemplate{{URITemplate: "file:///{path}", Name: "file"}},
		prompts:   []Prompt{{Name: "review"}},
	}
	edges := capabilityEdges("github", cat, capTime)

	// 3 tools + 1 resource + 1 template + 1 prompt = 6 edges.
	if len(edges) != 6 {
		t.Fatalf("want 6 edges, got %d", len(edges))
	}

	byRef := map[string]model.EdgeObservation{}
	for _, e := range edges {
		// Every edge is a DECLARED capability: UNTRUSTED provenance.
		if e.Source != model.SignalMCPAnnotation {
			t.Errorf("edge %s source = %s, want mcp_annotation", e.ResourceRef, e.Source)
		}
		if e.Confidence != model.ConfidenceApproximate {
			t.Errorf("edge %s confidence = %s, want approximate", e.ResourceRef, e.Confidence)
		}
		if e.OriginKind != originMCPServer || e.OriginRef != "github" {
			t.Errorf("edge %s origin = %s/%s", e.ResourceRef, e.OriginKind, e.OriginRef)
		}
		byRef[e.ResourceRef] = e
	}

	if e := byRef["github/read_file"]; e.Mode != model.ModeRead || e.ResourceKind != resTool || e.ToolRef != "read_file" {
		t.Errorf("read_file edge = %+v", e)
	}
	if e := byRef["github/delete_file"]; e.Mode != model.ModeReadWrite {
		t.Errorf("destructive tool should be readwrite: %+v", e)
	}
	if e := byRef["github/no_annotations"]; e.Mode != model.ModeReadWrite {
		t.Errorf("un-annotated tool defaults to readwrite (UNTRUSTED): %+v", e)
	}
	if e := byRef["file:///etc/hosts"]; e.ResourceKind != resResource || e.Mode != model.ModeRead {
		t.Errorf("resource edge = %+v", e)
	}
	if e := byRef["file:///{path}"]; e.ResourceKind != resTemplate {
		t.Errorf("template edge = %+v", e)
	}
	if e := byRef["github/review"]; e.ResourceKind != resPrompt || e.Mode != model.ModeUnknown {
		t.Errorf("prompt edge = %+v", e)
	}
}

func TestCapabilityEdgesScrubResourceURI(t *testing.T) {
	cat := catalog{resources: []Resource{
		{URI: "https://api.test/x?token=ghp_1234567890abcdefghijklmnopqrstuvwxyzAB"},
		{URI: "redis://default:p4ssw0rd@cache:6379/0"},
		{URI: "postgres://admin:s3cr3tpw@db.internal/orders"},
	}}
	cat.templates = []ResourceTemplate{{URITemplate: "https://u:tplsecret@host/{id}"}}
	for _, e := range capabilityEdges("srv", cat, capTime) {
		for _, leak := range []string{"ghp_1234567890", "p4ssw0rd", "s3cr3tpw", "tplsecret"} {
			if strings.Contains(e.ResourceRef, leak) {
				t.Errorf("resource/template URI leaked %q in ref %q", leak, e.ResourceRef)
			}
		}
	}
}

func TestCapabilityEdgesEmptyCatalog(t *testing.T) {
	if edges := capabilityEdges("srv", catalog{}, capTime); len(edges) != 0 {
		t.Errorf("empty catalog should yield no edges, got %d", len(edges))
	}
}
