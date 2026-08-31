// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package argocd is the Olivares AI read-first source connector that observes the
// GitOps estate managed by Argo CD. It parses exported Argo CD
// Application CRD manifests (argoproj.io/v1alpha1) — a file or directory the
// operator exports with `kubectl get applications -A -o yaml` — and reports the
// reconciliation posture of each Application as minimal-data FindingReports:
//
//   - sync posture: Synced (Info) vs OutOfSync (Medium — configuration drift between
//     the desired Git state and the live cluster);
//   - health posture: Healthy/Progressing/Suspended (Info), Missing/Unknown
//     (Medium), Degraded (High);
//   - operation posture: the last sync operation Errored/Failed (High).
//
// READ-FIRST BY CONSTRUCTION (docs/SECURITY-HARDENING.md). It never calls the Argo CD API or the
// Kubernetes API, never triggers a sync, and never mutates the GitOps estate
// (acting on a deployment is module VII / HITL-gated). It is the observation
// half of the GitOps integration: sync/health/drift flow onto the bus through the
// CB-1 ingestion seam exactly like the other read-first observers.
//
// MINIMAL DATA (docs/SECURITY-HARDENING.md): a finding carries the Application's namespace/name
// reference, a non-sensitive structural Title, and a one-way DetailHash of a stable
// non-sensitive key. The connector reads ONLY status.{sync,health,operationState};
// it never reads spec (Helm values / plugin env can embed secrets) and never reads
// payloads. It imports only the SDK and connector-internal helpers, never the
// engine (LICENSING.md), so it ships Apache-2.0.
//
// The companion artifact (a Lua health check for Argo CD to report the health of
// Olivares-managed resources) ships under deploy/argocd/ — it is consumed by the
// customer's Argo CD, not by this connector.
package argocd
