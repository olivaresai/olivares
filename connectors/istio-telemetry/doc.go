// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package istiotelemetry is the Olivares AI connector for the Istio service-mesh
// observability posture. It OBSERVES the mesh-wide observability
// CONFIGURATION by parsing exported Istio "Telemetry" CRD manifests
// (apiVersion telemetry.istio.io/v1 — and the legacy v1alpha1; kind: Telemetry). It
// never calls the Istio control plane, never opens a network listener, and reads no
// traffic and no payloads.
//
// It is read-first / minimal-data (docs/SECURITY-HARDENING.md, §3): the operator EXPORTS the
// Telemetry resources the mesh already stores (e.g. `kubectl get telemetry -A -o
// yaml`) to a file or directory, and this connector parses that manifest. A Telemetry
// resource configures spec.accessLogging[], spec.tracing[] and spec.metrics[] for a
// workload scope (spec.selector.matchLabels in a namespace, or mesh-wide in the Istio
// root namespace). An entry may turn a signal OFF — accessLogging[].disabled=true,
// tracing[].disableSpanReporting=true, or metrics[].overrides[].disabled=true — which
// is a DELIBERATE OBSERVABILITY BLIND SPOT for that scope.
//
// What it emits: FindingReport ONLY (no edges). This is declared posture, not
// observed traffic — emitting an EdgeObservation would fabricate a flow that may
// never have happened, so the connector does not (ARCHITECTURE.md, no-fabrication). For
// each declared signal of each Telemetry resource it emits one finding:
//
//	Kind        "mesh_telemetry_posture"
//	SubjectKind "istio.telemetry"
//	SubjectRef  "<namespace>/<name>"
//	Severity    Info   when access logging / tracing / metrics is configured & ENABLED
//	            (the coverage map: "istio telemetry: <signal> enabled for <scope> …")
//	Severity    Medium when a resource DISABLES a signal — an observability blind spot
//	            ("istio telemetry: <signal> DISABLED for <scope> … — observability
//	            blind spot"), an anti-evasion signal: a scope deliberately made
//	            unobserved (a workload acting unwatched).
//	DetailHash  redact.Hash of a STABLE non-sensitive key
//	            "istio_telemetry:<ns/name>:<signal>:<enabled|disabled>" — no payload.
//
// The disable classification is taken VERBATIM from the source's own fields: the
// access-log suppress switch is `disabled`, the trace suppress switch is the
// distinctly-named `disableSpanReporting`, and a metric is suppressed by an
// `overrides[].disabled`. A `disabled`/`disableSpanReporting` value that is absent is
// treated as not-disabled (coverage), never guessed. Those fields are typed as
// google.protobuf.BoolValue in the API; this connector tolerates BOTH the canonical
// bare-bool YAML ("disabled: true") and the wrapper form ("disabled: {value: true}")
// so it never UNDER-detects a disable (a missed disable would silently hide the very
// blind spot the connector exists to catch).
//
// SignalSource: a package-local const SignalIstioTelemetry ("istio_telemetry").
// FindingReport has no Source field, so the provenance is woven into the finding
// Kind/Title and the const is kept for documentation/consistency with the sibling
// network connectors; sdk/model/enums.go is NOT modified.
//
// Minimal data (docs/SECURITY-HARDENING.md): the connector reads ONLY the structural posture — which
// signal a resource configures and whether it is disabled, plus the namespace/name
// and the selector label scope. It NEVER reads a status, a provider's config (which
// can carry collector endpoints), an access-log/tracing `filter` CEL expression or a
// tracing `customTags` literal (both of which can embed request header/attribute
// names — payload-adjacent). The selector label scope shown in a Title is scrubbed
// (connectors/internal/redact) defensively, and the finding detail is reduced to a
// SHA-256; the raw value never leaves. There is a negative no-leak test that embeds a
// recognizable secret in a fixture and asserts it never appears in the emitted
// observations.
//
// Schema authority: verified against the Istio Telemetry API reference,
// https://istio.io/latest/docs/reference/config/telemetry/ (apiVersion
// telemetry.istio.io/v1, kind Telemetry; AccessLogging.disabled "If set to true, no
// access logs will be generated for impacted workloads"; Tracing.disableSpanReporting
// "If set to true, no spans will be reported"; Metrics.overrides[].disabled). The
// connector parses the documented expected shape of those fields; it invents no
// field or metric.
//
// It imports only the SDK, the Apache internal helpers (redact) and the standard
// library, plus gopkg.in/yaml.v3 for CRD parsing — never the engine (/core).
package istiotelemetry
