// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// Origin and resource kinds emitted by this connector (documented in the
// contract for modules I, III and V). The origin of every capability edge is the
// MCP server; the resource is the tool/resource/template/prompt it exposes.
const (
	originMCPServer = "mcp_server"
	resTool         = "mcp.tool"
	resResource     = "mcp.resource"
	resTemplate     = "mcp.resource_template"
	resPrompt       = "mcp.prompt"
)

// capabilityEdges turns an introspected server catalog into capability edges: one
// per tool (with its UNTRUSTED R/RW hint), resource, resource template and
// prompt. Every edge is SignalMCPAnnotation + ConfidenceApproximate — a DECLARED
// capability, never an observed access. A resource URI is scrubbed for embedded
// secrets before it becomes a reference (docs/SECURITY-HARDENING.md).
func capabilityEdges(server string, cat catalog, at time.Time) []model.EdgeObservation {
	var edges []model.EdgeObservation

	for _, tool := range cat.tools {
		edges = append(edges, capEdge(server, resTool, server+"/"+tool.Name, tool.Name, modeFromAnnotations(tool.Annotations), at))
	}
	for _, r := range cat.resources {
		edges = append(edges, capEdge(server, resResource, sanitizeRef(r.URI, r.Name), "", model.ModeRead, at))
	}
	for _, tpl := range cat.templates {
		edges = append(edges, capEdge(server, resTemplate, sanitizeRef(tpl.URITemplate, tpl.Name), "", model.ModeRead, at))
	}
	for _, p := range cat.prompts {
		edges = append(edges, capEdge(server, resPrompt, server+"/"+p.Name, "", model.ModeUnknown, at))
	}
	return edges
}

// capEdge builds one capability edge with the shared MCP-introspection provenance.
func capEdge(server, kind, ref, toolRef string, mode model.AccessMode, at time.Time) model.EdgeObservation {
	return model.EdgeObservation{
		OriginKind:   originMCPServer,
		OriginRef:    server,
		ResourceKind: kind,
		ResourceRef:  ref,
		Mode:         mode,
		Source:       model.SignalMCPAnnotation,
		Confidence:   model.ConfidenceApproximate,
		ToolRef:      toolRef,
		ObservedAt:   at,
	}
}

// sanitizeRef turns an MCP resource URI (or, if empty, a name) into a safe
// reference. A URI is passed through SanitizeURL — which strips basic-auth
// userinfo, query and fragment — because an introspected resource URI can embed
// credentials; a bare name is scrubbed for secret shapes. This matches the
// claude WebFetch path and upholds the minimal-data invariant (docs/SECURITY-HARDENING.md).
func sanitizeRef(uri, name string) string {
	if uri != "" {
		return redact.SanitizeURL(uri)
	}
	return redact.Clean(name)
}
