// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package inferencegateway is the Olivares AI connector for the Kubernetes Gateway
// API Inference Extension — the PERMITTED (declared-topology)
// side of the permitted-vs-observed access diff (ARCHITECTURE.md, module III), NOT an
// observed-traffic feed.
//
// # The platform
//
// The Gateway API Inference Extension (WG-Serving / SIG-Network) standardizes how a
// Gateway routes LLM/inference requests to model-serving pods. The operator declares
// the routing topology as Kubernetes CRDs:
//
//   - An InferencePool declares spec.selector (the model-serving pods/labels it
//     fronts), the serving port (spec.targetPortNumber in v1alpha2,
//     spec.targetPorts[] in v1 GA) and the Endpoint Picker (EPP) extension that
//     chooses an endpoint per request (spec.extensionRef in v1alpha2,
//     spec.endpointPickerRef in v1 GA).
//   - A model resource binds a served model NAME to a pool: an InferenceModel
//     (v1alpha2: spec.modelName -> spec.poolRef, + spec.criticality) or its v1 GA
//     successor InferenceObjective (spec.poolRef + spec.priority — the v1 API
//     "replaces InferenceModel with InferenceObjective").
//
// This connector parses those declared CRDs and emits the DECLARED routing topology
// as the permitted side of the R/RW map: which identity (the pool / its EPP) routes
// to which model-serving resource, and which model name is bound to which pool.
//
// # Ingest path (read-first, honest — docs/SECURITY-HARDENING.md)
//
// The operator EXPORTS its inference-routing manifests to a file or directory of
// Kubernetes YAML/JSON (the same GitOps manifests it applies), and this connector
// PARSES THAT FILE (the canonical connectors/externalsecrets multi-document yaml.v3
// path). It never talks to the Kubernetes API server, never reaches the Gateway, the
// EPP or a model-serving pod, never opens a network listener, never sends or routes a
// request, and never decrypts anything. It is a batch poller: Gather lists the files,
// parses the CRDs, emits the edges and returns nil at EOF; the engine re-runs it on
// the operator's schedule. Close holds no handles.
//
// # Minimal data (docs/SECURITY-HARDENING.md)
//
// A routing CRD carries only structural metadata — pod-selector labels, an FQDN-free
// pool/model name, a port number, an EPP reference, a criticality/priority band.
// There is no request body, prompt, completion, token, or secret in the manifest at
// all. This connector reads ONLY the identities, names, namespaces and the declared
// EPP/poolRef references; the record structs declare ONLY those fields, so nothing
// else can be emitted. Any free text (a selector value) is scrubbed
// (connectors/internal/redact) before it ever reaches an edge, and a negative test
// asserts a planted secret never appears in the emitted observations.
//
// # What it emits (the permitted/declared side)
//
// For each InferencePool, ONE model.EdgeObservation:
//
//	OriginKind   "identity"
//	OriginRef    <the EPP extension/endpointPicker name, else the pool name>
//	ResourceKind "inference.pool"
//	ResourceRef  "<namespace>/<poolName>"
//	Mode         readwrite          (inference is request + response)
//	Source       model.SignalPolicy (DECLARED grant — permitted side, not observed)
//	Confidence   attributed         (it is exact declared config)
//	ToolRef      "k8s.inference_gateway"
//
// For each InferenceModel / InferenceObjective, ONE model.EdgeObservation:
//
//	OriginKind   "identity"
//	OriginRef    <modelName>        (InferenceObjective has none -> metadata.name)
//	ResourceKind "inference.model"
//	ResourceRef  "<namespace>/<modelName> -> <poolRef>"
//	Mode         readwrite
//	Source       model.SignalPolicy
//	Confidence   attributed
//	ToolRef      "k8s.inference_gateway"
//
// # Why Source = model.SignalPolicy (not an observed-traffic signal)
//
// These edges are DECLARED configuration, exactly like the iceberg-catalog grant
// edges and the external-secrets provisioning edges — the PERMITTED side of the
// diff, never an observed access. So the edge Source is model.SignalPolicy (the SDK's
// established value for a declared grant), and the runtime/L7 observation of the SAME
// route (an Envoy ALS / Hubble flow hitting the EPP) is what the
// access map diffs against it. The connector still publishes its own provenance value
// as a PACKAGE-LOCAL open-string const (SignalInferenceGateway = "inference_gateway",
// per the snowflake-audit precedent) for display/ToolRef context; the edge Source
// itself stays SignalPolicy so the permitted and observed halves are never collapsed.
//
// # Schema authority (anti-fabrication)
//
// The parsed apiVersions, kinds and field names were VERIFIED against the upstream
// Kubernetes Gateway API Inference Extension reference
// (https://gateway-api-inference-extension.sigs.k8s.io/):
//
//   - GA v1 group "inference.networking.k8s.io/v1": InferencePool
//     {spec.selector, spec.targetPorts, spec.endpointPickerRef}; InferenceObjective
//     {spec.poolRef, spec.priority} — the v1 API "replaces InferenceModel with
//     InferenceObjective".
//   - Pre-GA v1alpha2 group "inference.networking.x-k8s.io/v1alpha2": InferencePool
//     {spec.selector, spec.targetPortNumber, spec.extensionRef}; InferenceModel
//     {spec.modelName, spec.poolRef, spec.criticality in Critical|Standard|Sheddable}.
//
// This is a REAL, standardized CRD schema — not an invented shape. The connector's
// struct is deliberately TOLERANT (it accepts both group versions and both the old
// and new field names for the port and the EPP reference) so a cluster mid-migration
// is parsed correctly; the tolerance is a documented superset of the verified fields,
// never a fabricated field.
//
// It imports only the SDK, the Apache connectors/internal helpers, gopkg.in/yaml.v3
// and the standard library — never the engine (/core).
package inferencegateway
