// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// registrysync.go is the "discover/sync servers from the official registry" half of
// Objective 1. Where registry.go resolves provenance for ONE configured server,
// the sync pass ENUMERATES the public MCP Registry for each namespace the org OWNS
// (internalregistry.go) and reconciles what is published there against what the org
// has approved:
//
//   - a server published under an owned namespace whose status is deleted/deprecated
//     → a YANK (OWASP MCP04 Software Supply Chain — a dependency the org may still run
//     was pulled or deprecated upstream);
//   - a server published under an owned namespace that is NOT in the approved internal
//     registry → an UNMANAGED publication (governance: someone published under our
//     namespace without it being vetted/tracked);
//   - otherwise → a discovered, vetted publication (Info inventory).
//
// It is OPT-IN (registry_sync) and bounded (maxSyncPages). Unlike the provenance
// path, it DELIBERATELY requests include_deleted, because a yank is invisible
// otherwise — that is the entire point of the supply-chain signal. Every finding is
// minimal-data (the server NAME is a public reverse-DNS identifier, not a secret; the
// detail is hashed). A registry that cannot be reached degrades to one Info finding
// per namespace, never a fabricated yank or a silent pass.

// maxSyncPages caps the enumeration of a namespace so a hostile/huge registry cannot
// drive an unbounded fetch loop (100 per page → up to 2000 records).
const maxSyncPages = 20

// listNamespace enumerates the public registry for a reverse-DNS namespace, paging
// through the cursor and keeping only records whose namespace EXACTLY matches (the
// registry's ?search= is fuzzy, so the strict client-side filter avoids a false
// cross-namespace match). include_deleted is set so a yanked server is visible.
func (c *registryClient) listNamespace(ctx context.Context, namespace string) ([]registryRecord, error) {
	var out []registryRecord
	cursor := ""
	seen := map[string]struct{}{} // guard against a registry that returns a constant cursor
	for page := 0; page < maxSyncPages; page++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		res, err := c.fetchPage(ctx, namespace, cursor, true)
		if err != nil {
			return nil, err
		}
		for _, s := range res.Servers {
			if strings.EqualFold(namespaceOf(s.Server.Name), namespace) {
				out = append(out, s)
			}
		}
		if res.Metadata.NextCursor == "" {
			break
		}
		if _, dup := seen[res.Metadata.NextCursor]; dup {
			break // a repeated cursor would otherwise re-fetch the same page until the cap
		}
		seen[res.Metadata.NextCursor] = struct{}{}
		cursor = res.Metadata.NextCursor
	}
	return out, nil
}

// discoverFindings runs the registry-sync discovery pass once per Gather (before the
// per-server loop). It is a no-op unless registry sync is enabled, a registry client
// is configured, and the org declared at least one owned namespace.
func (s *Source) discoverFindings(ctx context.Context, at time.Time) []model.FindingReport {
	if s.reg == nil || !s.cfg.registrySync || s.internal.ownedNamespaceList() == nil {
		return nil
	}
	var out []model.FindingReport
	for _, ns := range s.internal.ownedNamespaceList() {
		servers, err := s.reg.listNamespace(ctx, ns)
		if err != nil {
			out = append(out, syncUnavailableFinding(ns, err, at))
			continue
		}
		for _, srv := range servers {
			switch status := srv.status(); status {
			case "deleted", "deprecated":
				out = append(out, yankFinding(srv.Server.Name, ns, status, at))
			default:
				if s.internal.knownRegistryName(srv.Server.Name) {
					out = append(out, discoveredFinding(srv.Server.Name, ns, at))
				} else {
					out = append(out, unmanagedPublishedFinding(srv.Server.Name, ns, at))
				}
			}
		}
	}
	return out
}

// ownedNamespaceList returns the owned namespaces as a sorted slice (deterministic
// findings/tests), or nil when none are declared.
func (ir internalRegistry) ownedNamespaceList() []string {
	return sortedKeys(ir.ownedNamespaces)
}

// sortedKeys returns the sorted keys of a set (deterministic findings/tests). It
// stayed behind when the text-scan primitives moved to connectors/internal/textscan
// — it is a generic helper, not a text-safety primitive.
func sortedKeys(m map[string]struct{}) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// yankFinding flags a server published under an owned namespace that the registry has
// DELETED/deprecated — a supply-chain signal (MCP04): a dependency the org may still
// be running was pulled upstream.
func yankFinding(name, namespace, status string, at time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        findingProvenance,
		Severity:    model.SeverityMedium,
		SubjectKind: subjectMCPServer,
		SubjectRef:  name,
		Title:       "[MCP04] MCP server in owned namespace " + namespace + " is " + status + " in the registry (supply-chain: possible yank/upgrade-risk)",
		DetailHash:  redact.Hash("mcp-yank name=" + name + " namespace=" + namespace + " status=" + status),
		OccurredAt:  at,
	}
}

// unmanagedPublishedFinding flags a server published under an owned namespace that is
// NOT in the approved internal registry — a governance/supply-chain signal: a
// publication under the org's namespace that was never vetted/tracked.
func unmanagedPublishedFinding(name, namespace string, at time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        findingProvenance,
		Severity:    model.SeverityLow,
		SubjectKind: subjectMCPServer,
		SubjectRef:  name,
		Title:       "[MCP04] MCP server published under owned namespace " + namespace + " is not in the approved internal registry (unmanaged publication)",
		DetailHash:  redact.Hash("mcp-unmanaged name=" + name + " namespace=" + namespace),
		OccurredAt:  at,
	}
}

// discoveredFinding records a vetted publication under an owned namespace (Info
// inventory — the org sees its own published servers reconciled from the registry).
func discoveredFinding(name, namespace string, at time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        findingProvenance,
		Severity:    model.SeverityInfo,
		SubjectKind: subjectMCPServer,
		SubjectRef:  name,
		Title:       "MCP server discovered in owned namespace " + namespace + " (approved internal registry; registry PREVIEW — self-verify)",
		DetailHash:  redact.Hash("mcp-discovered name=" + name + " namespace=" + namespace),
		OccurredAt:  at,
	}
}

// syncUnavailableFinding reports that the registry could not be enumerated for a
// namespace this pass — surfaced (a gap is a signal), never a fabricated yank.
func syncUnavailableFinding(namespace string, err error, at time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        findingProvenance,
		Severity:    model.SeverityInfo,
		SubjectKind: subjectMCPServer,
		SubjectRef:  namespace,
		Title:       "MCP Registry sync unavailable for owned namespace " + namespace + " this pass (enumeration failed)",
		DetailHash:  redact.Hash("mcp-sync-unavailable namespace=" + namespace + " err=" + err.Error()),
		OccurredAt:  at,
	}
}
