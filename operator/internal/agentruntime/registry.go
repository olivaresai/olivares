// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package agentruntime is a TRACK-don't-block seam for emerging Kubernetes
// agent-runtime CRD ecosystems (Agent Sandbox, kagent).
//
// HONEST POSTURE: these external CRD APIs are PRE-STABLE and moving.
// This package therefore does NOT import their Go types, does NOT register any
// controller-runtime watches against them, and does NOT depend on their schema.
// It is a DATA-ONLY registry that names the CRDs we are tracking, marks every one
// as NOT stable, and offers an OPT-IN, presence-only discovery helper. Nothing
// here runs by default.
//
// Everything below is "verified at 2026-06 against primary sources" — but those
// sources explicitly warn the APIs are subject to change. Treat the names and
// versions here as a tracking note, not a contract. When you wire a real
// integration, re-verify against the upstream docs cited in each KnownCRD.Caveat
// before trusting any group/version/kind.
package agentruntime

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// verifiedAt is the date the registry below was last cross-checked against the
// cited primary sources. It is intentionally a plain string, not a build stamp:
// it documents WHEN a human verified the facts, not when the binary was built.
const verifiedAt = "2026-06"

// KnownCRD is a tracking record for one external agent-runtime CRD. It carries
// only metadata — no Go type, no informer, no watch. Stable is ALWAYS false in
// this release: by construction we do not assert any of these APIs are stable.
type KnownCRD struct {
	// Project is the upstream project this CRD belongs to.
	Project string
	// Group is the API group (e.g. "kagent.dev").
	Group string
	// Version is the served version we are tracking (e.g. "v1alpha2").
	Version string
	// Kind is the CRD Kind (e.g. "Agent").
	Kind string
	// Plural is the resource (lower-case) plural, used for discovery/RBAC notes.
	Plural string
	// Stable reports whether the upstream API is declared stable (>= v1, with a
	// compatibility guarantee). It is false for every entry in this registry.
	Stable bool
	// Caveat is the verbatim honesty note + the primary source(s) the record was
	// verified against. ALWAYS read this before acting on the record.
	Caveat string
	// VerifiedAt records when the record was last checked against its sources.
	VerifiedAt string
}

// GroupKind returns the apimachinery GroupKind for presence checks. It does not
// pin a version on purpose: discovery is by GroupKind so a version bump upstream
// does not silently make a tracked CRD "disappear" from a presence check.
func (c KnownCRD) GroupKind() schema.GroupKind {
	return schema.GroupKind{Group: c.Group, Kind: c.Kind}
}

// GroupVersionKind returns the fully-qualified GVK as currently tracked. The
// version is the one verified at VerifiedAt and is SUBJECT TO CHANGE upstream.
func (c KnownCRD) GroupVersionKind() schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: c.Group, Version: c.Version, Kind: c.Kind}
}

// agentSandboxCaveat / kagentCaveat are shared, source-cited honesty notes.
const (
	agentSandboxCaveat = "Agent Sandbox (kubernetes-sigs/agent-sandbox, K8s SIG Apps; " +
		"https://agent-sandbox.sigs.k8s.io). Maturity at " + verifiedAt + ": v0.21, " +
		"\"moving fast\", API NOT stable (pre-1.0). Verified " + verifiedAt + " against " +
		"primary sources, subject to change — track, do not hard-code."

	kagentCaveat = "kagent (CNCF Sandbox project, by Solo.io; https://kagent.dev, " +
		"https://www.cncf.io/projects/kagent/). API group migrated kagent.io/v1alpha1 " +
		"-> kagent.dev/v1alpha2. NOTE: the toolservers CRD is being REMOVED in favor of " +
		"the kmcp APIs. Verified " + verifiedAt + " against primary sources, subject to " +
		"change — track, do not hard-code."
)

// knownCRDs is the immutable backing slice. Access it through KnownCRDs(), which
// returns a copy so callers cannot mutate the registry.
//
// Every entry has Stable=false. We do NOT claim these CRDs are depended-on,
// watched, or required — only that we are aware of them and would detect their
// presence if explicitly asked to.
var knownCRDs = []KnownCRD{
	// --- Agent Sandbox (kubernetes-sigs/agent-sandbox) ---
	{Project: "agent-sandbox", Group: "agents.x-k8s.io", Version: "v1alpha1", Kind: "Sandbox", Plural: "sandboxes", Stable: false, Caveat: agentSandboxCaveat, VerifiedAt: verifiedAt},
	{Project: "agent-sandbox", Group: "agents.x-k8s.io", Version: "v1alpha1", Kind: "SandboxTemplate", Plural: "sandboxtemplates", Stable: false, Caveat: agentSandboxCaveat, VerifiedAt: verifiedAt},
	{Project: "agent-sandbox", Group: "agents.x-k8s.io", Version: "v1alpha1", Kind: "SandboxClaim", Plural: "sandboxclaims", Stable: false, Caveat: agentSandboxCaveat, VerifiedAt: verifiedAt},
	{Project: "agent-sandbox", Group: "agents.x-k8s.io", Version: "v1alpha1", Kind: "SandboxWarmPool", Plural: "sandboxwarmpools", Stable: false, Caveat: agentSandboxCaveat, VerifiedAt: verifiedAt},

	// --- kagent (CNCF Sandbox, Solo.io) — group kagent.dev/v1alpha2 ---
	{Project: "kagent", Group: "kagent.dev", Version: "v1alpha2", Kind: "Agent", Plural: "agents", Stable: false, Caveat: kagentCaveat, VerifiedAt: verifiedAt},
	{Project: "kagent", Group: "kagent.dev", Version: "v1alpha2", Kind: "ModelConfig", Plural: "modelconfigs", Stable: false, Caveat: kagentCaveat, VerifiedAt: verifiedAt},
	{Project: "kagent", Group: "kagent.dev", Version: "v1alpha2", Kind: "MCPServer", Plural: "mcpservers", Stable: false, Caveat: kagentCaveat, VerifiedAt: verifiedAt},
	{Project: "kagent", Group: "kagent.dev", Version: "v1alpha2", Kind: "RemoteMCPServer", Plural: "remotemcpservers", Stable: false, Caveat: kagentCaveat, VerifiedAt: verifiedAt},
	{Project: "kagent", Group: "kagent.dev", Version: "v1alpha2", Kind: "Memory", Plural: "memories", Stable: false, Caveat: kagentCaveat, VerifiedAt: verifiedAt},
	{Project: "kagent", Group: "kagent.dev", Version: "v1alpha2", Kind: "SandboxAgent", Plural: "sandboxagents", Stable: false, Caveat: kagentCaveat, VerifiedAt: verifiedAt},
	// toolservers: tracked but flagged in the caveat as being REMOVED upstream in
	// favor of the kmcp APIs. Kept here precisely so the migration is on record.
	{Project: "kagent", Group: "kagent.dev", Version: "v1alpha2", Kind: "ToolServer", Plural: "toolservers", Stable: false, Caveat: kagentCaveat + " ToolServer specifically is slated for REMOVAL — do not build on it.", VerifiedAt: verifiedAt},
}

// KnownCRDs returns a copy of the tracked CRD registry. Every entry has
// Stable=false; the registry asserts AWARENESS, not a dependency.
func KnownCRDs() []KnownCRD {
	out := make([]KnownCRD, len(knownCRDs))
	copy(out, knownCRDs)
	return out
}

// KnownCRDsForProject returns the tracked CRDs belonging to one project
// ("agent-sandbox" or "kagent"). The result is a fresh slice.
func KnownCRDsForProject(project string) []KnownCRD {
	var out []KnownCRD
	for _, c := range knownCRDs {
		if c.Project == project {
			out = append(out, c)
		}
	}
	return out
}

// PresenceReport is the result of an OPT-IN presence check for one tracked CRD.
type PresenceReport struct {
	CRD KnownCRD
	// Installed is true if a CRD with this GroupKind is served by the cluster's
	// discovery API. It says nothing about the CRD's schema or our support for it.
	Installed bool
}

// GroupKindLister is the minimal slice of the K8s discovery API this package
// needs. *discovery.DiscoveryClient (and the cached discovery client) satisfy it.
// We depend on this narrow interface rather than the concrete client so the
// opt-in path stays testable and so we never accidentally reach for schema
// introspection — presence-by-GroupKind is the ONLY thing we look at.
type GroupKindLister interface {
	// ServerGroups lists the API groups the server exposes. This matches the
	// signature of *discovery.DiscoveryClient.ServerGroups so the real client is
	// accepted directly with no adapter.
	ServerGroups() (*metav1.APIGroupList, error)
}

// DiscoverInstalled reports which tracked CRDs are *installed* on the cluster, by
// GroupKind, using only the discovery API. It is OPT-IN: a default operator never
// calls it. It deliberately does NOT introspect, watch, or depend on the tracked
// CRDs' schemas — it only answers "is a CRD with this group present?".
//
// HONESTY: presence is detected at GROUP granularity here (the discovery
// ServerGroups call). Confirming the exact Kind requires resource discovery,
// which this seam intentionally leaves as a documented extension point so the
// operator carries no hard dependency on the moving APIs. The returned reports
// therefore mean "the tracked CRD's API GROUP is served by the cluster", which is
// the honest, low-coupling signal we are willing to assert. Callers that need
// Kind-level certainty must opt further in and accept the coupling.
func DiscoverInstalled(ctx context.Context, d GroupKindLister) ([]PresenceReport, error) {
	if d == nil {
		return nil, fmt.Errorf("agentruntime: nil discovery client (DiscoverInstalled is opt-in and requires an explicit client)")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	groups, err := d.ServerGroups()
	if err != nil {
		return nil, fmt.Errorf("agentruntime: discovery ServerGroups: %w", err)
	}
	present := map[string]bool{}
	for _, g := range groups.Groups {
		present[g.Name] = true
	}
	reports := make([]PresenceReport, 0, len(knownCRDs))
	for _, c := range knownCRDs {
		reports = append(reports, PresenceReport{CRD: c, Installed: present[c.Group]})
	}
	return reports, nil
}
