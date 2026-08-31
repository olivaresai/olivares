// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package crossplane is the Olivares AI read-first source connector that takes the
// Crossplane CompositeResourceDefinition (XRD) surface of a cluster into the estate
// inventory. It parses exported XRD manifests
// (apiextensions.crossplane.io/v1, kind CompositeResourceDefinition) — a file or
// directory the operator exports with `kubectl get xrd -o yaml` — and reports, per
// XRD, the composite API surface that the platform team introduced: the API group,
// the composite kind, and each declared version (with its served/referenceable
// flags). Each XRD becomes one inventory FindingReport (Severity Info), so the
// composite resource TYPES a cluster exposes are cataloged alongside the rest of
// the estate.
//
// INTROSPECTION ONLY — NOT AN OPERATOR (the boundary). This connector is the
// observation half of the Crossplane integration: it reads exported XRD manifests
// and never runs as a controller. It does NOT install a Crossplane provider, does
// NOT reconcile, does NOT read Composite Resources (XRs) or Claims (XRCs) or their
// live state, and mutates nothing. The Olivares Crossplane Operator (acting ON the
// estate through a controller) is module — out of scope here. Read-first by
// construction (docs/SECURITY-HARDENING.md): it touches no Crossplane or Kubernetes API.
//
// MINIMAL DATA (docs/SECURITY-HARDENING.md). A finding carries the XRD's metadata.name reference, a
// non-sensitive structural Title (group / kind / plural and the version list), and a
// one-way DetailHash of a stable non-sensitive key. The connector reads ONLY the
// structural API surface: spec.group, spec.names.{kind,plural}, and each
// spec.versions[] .name/.served/.referenceable. It MAY count the NAMES of the
// top-level required fields of a version's openAPIV3Schema (field names are part of
// the public API contract, not secrets) but it NEVER reads or emits a schema
// property's value, default, or description — those can carry environment-specific
// detail (payload-adjacent). It imports only the SDK and connector-internal helpers,
// never the engine (LICENSING.md), so it ships Apache-2.0.
//
// GRADUATION DATE (no-fabrication). The session prompt states
// Crossplane "graduated 2025-10-28". The PRIMARY sources — the Crossplane blog post
// "Crossplane Becomes a CNCF Graduated Project"
// (blog.crossplane.io/crossplane-cncf-graduation/) and the matching CNCF
// announcement — are dated 2025-11-06; no primary source confirms 2025-10-28. This
// connector records the graduation as "announced 2025-11-06"; the session-prompt
// date (2025-10-28) is UNVERIFIED and is not encoded anywhere.
//
// VERSION CAVEAT. The Crossplane minor version is intentionally left
// UNPINNED here: this connector reads only the long-stable XRD structural surface
// (group / names / versions / served / referenceable), which is byte-identical
// across recent Crossplane releases. Note that Crossplane v2 changed composite
// resource semantics relative to v1.x (e.g. namespaced composites and the
// claim-model rework); none of those behavioral differences affect the few
// structural XRD fields this connector parses, so no version is hardcoded — and an
// invented version is never asserted.
//
// Schema authority: verified against docs.crossplane.io (the XRD reference:
// apiextensions.crossplane.io/v1, kind CompositeResourceDefinition; spec.group,
// spec.names.{kind,plural}, spec.versions[].{name,served,referenceable,schema}).
package crossplane
