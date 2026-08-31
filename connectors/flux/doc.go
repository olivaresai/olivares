// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package flux is the Olivares AI read-first source connector that observes the
// GitOps estate managed by Flux. It parses exported Flux CRD
// manifests — a file or directory the operator exports with `kubectl get
// gitrepositories,kustomizations,helmreleases -A -o yaml` — and reports the
// reconciliation posture of each Flux object as minimal-data FindingReports. It is
// the sibling of the argocd connector for the other major GitOps
// engine; Flux graduated CNCF on 2022-11-30.
//
// Flux spreads its API across THREE distinct groups, so this connector accepts all
// three kinds rather than hardcoding one group/version:
//
//   - GitRepository (source.toolkit.fluxcd.io/v1): the source of truth. Posture
//     is taken from status.conditions[type=Ready], status.observedGeneration vs
//     metadata.generation, and the current revision in status.artifact.revision.
//   - Kustomization (kustomize.toolkit.fluxcd.io/v1): the applier. Posture is
//     status.conditions[type=Ready] (reason e.g. ReconciliationSucceeded /
//     ArtifactFailed), drift between status.lastAppliedRevision and
//     status.lastAttemptedRevision, and observedGeneration vs generation.
//   - HelmRelease (helm.toolkit.fluxcd.io/v2): the Helm applier. Posture is
//     status.conditions[type=Ready], drift between the APPLIED revision in
//     status.history[0] and status.lastAttemptedRevision, and observedGeneration
//     vs generation. CAVEAT: on HelmRelease v2 the applied chart version lives in
//     status.history[0].chartVersion, NOT in a status.lastAppliedRevision field
//     (that field is a Kustomization concept) — so the connector reads history[0].
//
// CLASSIFICATION (taken VERBATIM from the source's own fields — never guessed). The
// truth of "is this object reconciled" is status.conditions[?(@.type=="Ready")].status:
//
//   - Ready=="True"  => Info    (reconciled).
//   - Ready=="False" => High    (reconciliation failing). The condition REASON token
//     (e.g. ArtifactFailed, BuildFailed) is put in the Title; the condition MESSAGE
//     is NEVER read into any field (it can carry a registry URL, a chart path, a YAML
//     fragment — payload-adjacent, docs/SECURITY-HARDENING.md).
//   - Ready absent/empty => Unknown (Medium). An object that has not reported a Ready
//     condition yet is classified honestly as Unknown, never silently Healthy
//     (ARCHITECTURE.md).
//
// In addition, an object whose Ready is True but that is DRIFTED gets a Medium drift
// finding: applied != attempted revision (Kustomization: lastAppliedRevision vs
// lastAttemptedRevision; HelmRelease: history[0].chartVersion vs lastAttemptedRevision)
// OR status.observedGeneration != metadata.generation (the controller has not yet
// observed the latest spec generation).
//
// READ-FIRST BY CONSTRUCTION (docs/SECURITY-HARDENING.md). It never calls the Flux controllers, the
// source/notification API or the Kubernetes API, never triggers a reconcile or a
// `flux reconcile`, and never mutates the GitOps estate (acting on a deployment is
// module VII / HITL-gated). It is the observation half of the GitOps
// integration: reconciliation/drift posture flows onto the bus through the CB-1
// ingestion seam exactly like the other read-first observers. It emits NO edges —
// reconciliation status is observed posture, not an access flow; an edge would
// fabricate one (matches the argocd and istio-telemetry posture connectors).
//
// MINIMAL DATA (docs/SECURITY-HARDENING.md): a finding carries the object's namespace/name reference,
// a non-sensitive structural Title, and a one-way DetailHash of a stable
// non-sensitive key. The connector reads metadata.{name,namespace,generation} and
// ONLY the status fields above; it NEVER reads spec (a HelmRelease spec.values can
// embed secrets, a GitRepository spec.url/secretRef name an endpoint and a secret —
// payload-adjacent). No revision string (a commit SHA, a chart version, an artifact
// URL) is ever placed into a finding field; revisions are compared in-memory to
// decide drift, then discarded. It imports only the SDK and connector-internal
// helpers, never the engine (LICENSING.md), so it ships Apache-2.0.
//
// # OpenGitOps 1.0 → Flux evidence mapping
//
// OpenGitOps (opengitops.dev) is the CNCF-hosted vendor-neutral statement of what
// GitOps IS, in four principles (v1.0.0). This connector reports the Flux evidence
// for each principle, so an operator can show their GitOps estate conforms:
//
//	OpenGitOps 1.0 principle (verbatim)                          Flux evidence this connector observes
//	------------------------------------------------------------ ----------------------------------------------------------------
//	1. Declarative — "A system managed by GitOps must have       The presence of GitRepository / Kustomization / HelmRelease
//	   its desired state expressed declaratively."               declarative objects (the kinds this connector parses).
//	2. Versioned and Immutable — "Desired state is stored in a   GitRepository status.artifact.revision (the commit SHA the
//	   way that enforces immutability, versioning and retains a   source controller pinned). The connector reads it to decide
//	   complete version history."                                 drift; it never emits the SHA.
//	3. Pulled Automatically — "Software agents automatically     Controller-populated status (no external push):
//	   pull the desired state declarations from the source."     status and conditions show that a Flux controller handled it.
//	4. Continuously Reconciled — "Software agents continuously   status.conditions[Ready] + status.observedGeneration vs
//	   observe actual system state and attempt to apply the      metadata.generation + applied-vs-attempted revision drift.
//	   desired state."
//
// SignalSource: a package-local const SignalFlux ("flux"). FindingReport has no
// Source field, so the provenance is woven into the finding Kind/Title and the const
// is kept for documentation/consistency with the argocd connector; sdk/model/enums.go
// is NOT modified.
//
// Schema authority: verified against fluxcd.io — the source-controller GitRepository
// (source.toolkit.fluxcd.io/v1: status.artifact.revision, status.conditions,
// status.observedGeneration), the kustomize-controller Kustomization
// (kustomize.toolkit.fluxcd.io/v1: status.lastAppliedRevision,
// status.lastAttemptedRevision, status.conditions, status.observedGeneration) and the
// helm-controller HelmRelease (helm.toolkit.fluxcd.io/v2: status.history[],
// status.lastAttemptedRevision, status.conditions, status.observedGeneration). The
// connector parses the documented expected shape of those status fields; it invents
// no field, group or version, and it does not hardcode a single API group/version.
package flux
