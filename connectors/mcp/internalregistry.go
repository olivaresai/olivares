// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/connectors/internal/textscan"
	"github.com/olivaresai/olivares/sdk/model"
)

// internalregistry.go is the "register the internal ones" half of objective 1:
// the operator-declared registry of the organization's OWN MCP servers. The PUBLIC
// MCP Registry (registry.go) is a PREVIEW directory with minimal-to-no moderation, so
// a server's absence from it does NOT make the server illegitimate — many internal
// stdio/private servers are simply unpublished. The internal registry is the source
// of truth that lets governance tell three cases apart honestly:
//
//   - a server under a namespace the org OWNS, or an explicitly APPROVED entry → it is
//     ours/vetted; it must NOT be flagged a shadow just for being absent from the
//     public preview (registry.go would otherwise emit MCP09);
//   - an approved entry whose RUNNING version drifts from the PINNED version → a
//     supply-chain / rug-pull signal (MCP04 — the tool the org vetted is not the tool
//     now running);
//   - anything else → falls through to the public-registry provenance/shadow logic.
//
// It is pure operator config (no network), so reconciliation works even with the
// public-registry client disabled.

// internalEntry is one operator-approved internal MCP server. Name matches the
// connector's local server name (serverSpec.Name); RegistryName is the reverse-DNS
// name it is (or would be) published under; Version, when set, is the approved/pinned
// version used for drift detection against the introspected ServerInfo.Version.
type internalEntry struct {
	Name         string `json:"name"`
	RegistryName string `json:"registry_name"`
	Version      string `json:"version"`
}

// internalRegistry is the resolved set of owned namespaces + approved entries.
type internalRegistry struct {
	ownedNamespaces map[string]struct{} // reverse-DNS namespaces the org controls
	byName          map[string]internalEntry
	byRegistryName  map[string]internalEntry
}

// newInternalRegistry resolves owned namespaces + approved entries into lookups. A
// duplicate entry name/registry_name is a configuration error (ambiguous approval
// must never resolve nondeterministically).
func newInternalRegistry(namespaces []string, entries []internalEntry) (internalRegistry, error) {
	ir := internalRegistry{
		ownedNamespaces: map[string]struct{}{},
		byName:          map[string]internalEntry{},
		byRegistryName:  map[string]internalEntry{},
	}
	for _, ns := range namespaces {
		if ns = strings.TrimSpace(ns); ns != "" {
			ir.ownedNamespaces[strings.ToLower(ns)] = struct{}{}
		}
	}
	for _, e := range entries {
		e.Name = strings.TrimSpace(e.Name)
		e.RegistryName = strings.TrimSpace(e.RegistryName)
		if e.Name == "" && e.RegistryName == "" {
			return internalRegistry{}, fmt.Errorf("mcp: internal_servers: an entry has neither name nor registry_name")
		}
		if e.Name != "" {
			if _, dup := ir.byName[e.Name]; dup {
				return internalRegistry{}, fmt.Errorf("mcp: internal_servers: duplicate entry for name %q", e.Name)
			}
			ir.byName[e.Name] = e
		}
		if e.RegistryName != "" {
			if _, dup := ir.byRegistryName[e.RegistryName]; dup {
				return internalRegistry{}, fmt.Errorf("mcp: internal_servers: duplicate entry for registry_name %q", e.RegistryName)
			}
			ir.byRegistryName[e.RegistryName] = e
		}
	}
	return ir, nil
}

// empty reports whether the internal registry declares nothing (no owned namespaces,
// no approved entries) — in which case reconciliation is a no-op and provenance falls
// straight through to the public registry.
func (ir internalRegistry) empty() bool {
	return len(ir.ownedNamespaces) == 0 && len(ir.byName) == 0 && len(ir.byRegistryName) == 0
}

// owns reports whether registryName sits under a namespace the org controls, and the
// matched namespace. An empty registryName never matches (an un-asserted server can't
// be claimed as owned).
func (ir internalRegistry) owns(registryName string) (string, bool) {
	registryName = strings.TrimSpace(registryName)
	if registryName == "" {
		return "", false
	}
	ns := strings.ToLower(namespaceOf(registryName))
	if _, ok := ir.ownedNamespaces[ns]; ok {
		return namespaceOf(registryName), true
	}
	return "", false
}

// approved resolves a spec to an approved internal entry, matching by registry name
// first (the stronger key) then by local name.
func (ir internalRegistry) approved(spec serverSpec) (internalEntry, bool) {
	if spec.RegistryName != "" {
		if e, ok := ir.byRegistryName[spec.RegistryName]; ok {
			return e, true
		}
	}
	if spec.Name != "" {
		if e, ok := ir.byName[spec.Name]; ok {
			return e, true
		}
	}
	return internalEntry{}, false
}

// knownRegistryName reports whether a reverse-DNS registry name is an APPROVED
// internal entry — used by the registry-sync discovery pass to tell a vetted
// publication apart from an unmanaged one published under an owned namespace.
func (ir internalRegistry) knownRegistryName(registryName string) bool {
	_, ok := ir.byRegistryName[strings.TrimSpace(registryName)]
	return ok
}

// internalReconcile resolves a server against the internal registry FIRST (local, no
// network). It returns the resulting findings and whether the server was RECOGNIZED
// (owned namespace or approved entry): a recognized server is cleared — the caller
// must then SKIP the public-registry provenance/shadow logic so a vetted internal
// server is never mislabeled a shadow. An empty internal registry, or an unrecognized
// server, returns (nil, false) so the caller falls through to the public registry.
func (s *Source) internalReconcile(spec serverSpec, cat catalog, at time.Time) ([]model.FindingReport, bool) {
	if s.internal.empty() {
		return nil, false
	}
	if ns, ok := s.internal.owns(spec.RegistryName); ok {
		out := []model.FindingReport{internalOwnedFinding(spec.Name, ns, at)}
		if entry, ok := s.internal.approved(spec); ok {
			out = append(out, driftFindings(spec.Name, entry, cat, at)...)
		}
		return out, true
	}
	if entry, ok := s.internal.approved(spec); ok {
		out := []model.FindingReport{internalApprovedFinding(spec.Name, at)}
		out = append(out, driftFindings(spec.Name, entry, cat, at)...)
		return out, true
	}
	return nil, false
}

// internalOwnedFinding records that a server resolved to an org-OWNED namespace — it
// is internal/vetted, not a shadow. Info: ownership is provenance, not a problem.
func internalOwnedFinding(server, namespace string, at time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        findingProvenance,
		Severity:    model.SeverityInfo,
		SubjectKind: subjectMCPServer,
		SubjectRef:  server,
		Title:       "MCP server registered under org-owned namespace " + namespace + " (internal/vetted; not a shadow)",
		DetailHash:  redact.Hash("mcp-internal-owned server=" + server + " namespace=" + namespace),
		OccurredAt:  at,
	}
}

// internalApprovedFinding records that a server matched an approved internal-registry
// entry (curated, not a shadow).
func internalApprovedFinding(server string, at time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        findingProvenance,
		Severity:    model.SeverityInfo,
		SubjectKind: subjectMCPServer,
		SubjectRef:  server,
		Title:       "MCP server matches an approved internal-registry entry (vetted; not a shadow)",
		DetailHash:  redact.Hash("mcp-internal-approved server=" + server),
		OccurredAt:  at,
	}
}

// versionDriftFinding flags that an approved internal server is RUNNING a version
// different from the operator-pinned one — the tool the org vetted is not the tool now
// answering (a supply-chain / rug-pull signal, OWASP MCP04). Medium: it is a real
// change of a vetted dependency, but not proof of compromise. The versions are
// non-sensitive (they are public release identifiers).
func versionDriftFinding(server, pinned, running string, at time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        findingProvenance,
		Severity:    model.SeverityMedium,
		SubjectKind: subjectMCPServer,
		SubjectRef:  server,
		Title:       "[MCP04] MCP server version drift: running " + safeVersion(running) + ", approved/pinned " + safeVersion(pinned),
		DetailHash:  redact.Hash("mcp-version-drift server=" + server + " pinned=" + pinned + " running=" + running),
		OccurredAt:  at,
	}
}

// driftFindings returns findings for an approved internal server:
//   - a version-drift finding (MCP04) when the running version differs from the
//     pinned one (the existing check);
//   - one tool-definition fingerprint finding per tool introspected (a
//     rug-pull that mutates a description/schema while keeping the version-string
//     constant is caught here — the enterprise verifier compares against stored
//     pins; this function surfaces the current fingerprints for the observe pipeline).
func driftFindings(server string, entry internalEntry, cat catalog, at time.Time) []model.FindingReport {
	var out []model.FindingReport

	// Version-string drift (existing).
	pinned := strings.TrimSpace(entry.Version)
	running := strings.TrimSpace(cat.server.ServerInfo.Version)
	if pinned != "" && running != "" && pinned != running {
		out = append(out, versionDriftFinding(server, pinned, running, at))
	}

	// tool-definition drift — rug-pulls that keep the version constant but
	// mutate descriptions or schemas evade the version check above. Emit per-tool
	// fingerprints so the observe pipeline has the data to detect definition drift.
	if len(cat.tools) > 0 {
		out = append(out, toolDefinitionDriftFindings(server, cat.tools, at)...)
	}

	return out
}

// toolDefinitionDriftFindings emits one finding per tool, encoding the current
// definition fingerprint in DetailHash (minimal-data: hash only, never the raw
// definition text per docs/SECURITY-HARDENING.md). The comparison against a stored pin is the
// enterprise verifier's job; this function REPORTS the current fingerprints so
// the observe pipeline has the data to detect rug-pull drift.
func toolDefinitionDriftFindings(server string, tools []Tool, at time.Time) []model.FindingReport {
	out := make([]model.FindingReport, 0, len(tools))
	for _, t := range tools {
		fp := ToolFingerprint(t)
		out = append(out, model.FindingReport{
			Kind:        findingProvenance,
			Severity:    model.SeverityInfo,
			SubjectKind: subjectMCPServer,
			SubjectRef:  server,
			Title:       "MCP tool definition fingerprint: " + textscan.SanitizeDisplay(t.Name),
			DetailHash:  redact.Hash("mcp-tool-pin server=" + server + " tool=" + t.Name + " fp=" + fp),
			OccurredAt:  at,
		})
	}
	return out
}

// safeVersion renders a version string for a finding title; sanitizeDisplay strips
// hidden characters AND scrubs any secret shape, so an attacker-controlled
// ServerInfo.Version cannot smuggle content into a title.
func safeVersion(v string) string {
	if v = textscan.SanitizeDisplay(v); v == "" {
		return "(unknown)"
	}
	return v
}

// parseInternalEntries decodes the inline JSON array of approved internal entries.
func parseInternalEntries(raw string) ([]internalEntry, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var entries []internalEntry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return nil, fmt.Errorf("mcp: parse %q: %w", cfgInternalServers, err)
	}
	return entries, nil
}

// parseNamespaceList accepts either a JSON array or a comma-separated list of
// reverse-DNS namespaces (operator convenience).
func parseNamespaceList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if strings.HasPrefix(raw, "[") {
		var arr []string
		if json.Unmarshal([]byte(raw), &arr) == nil {
			return arr
		}
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
