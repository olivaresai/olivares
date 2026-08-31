// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package vertex is the read-only Olivares AI connector for the Gemini Enterprise Agent
// Platform (formerly Vertex AI) — the Google ENTERPRISE generative-AI surface
// (aiplatform.googleapis.com), distinct from the Gemini Developer API / AI Studio that
// connectors/gemini covers. Where the gemini connector honestly declares AI Studio has
// no usage/cost metering, the enterprise platform genuinely exposes per-model token
// usage (Cloud Monitoring), a publisher-model catalog, billed cost (Cloud Billing →
// BigQuery export) and a safety-posture surface (Model Armor).
//
// It satisfies the two shared model/provider contracts (README.md): sdk.SourceConnector
// (usage/cost as model.CostSample observations) and modelprovider.CatalogProvider (the
// model catalog). ProviderRef is "google" (modelprovider.ProviderGoogle — the same
// provider as Gemini, a DIFFERENT deployment surface); every cost sample carries
// Gateway=vertex so FinOps/governance never collapses Vertex with AI-Studio traffic.
//
// # What it emits (the five surfaces, all read-only)
//
//   - CATALOG (modelprovider.Catalog): the Gemini + Claude-on-platform foundation models.
//     There is NO stable v1 publisher-models LIST API (only a per-model GET), so the
//     catalog is a declared, operator-maintainable model list (offline-usable) that the
//     connector ENRICHES per id via GET /v1/publishers/{publisher}/models/{id}
//     (launchStage/versionState) when credentialed. A per-model 404 is tolerated (an id
//     the operator declared that the project cannot see keeps its declared entry — never
//     a hard failure). Each model carries declared family list pricing + capabilities.
//
//   - TOKEN USAGE → model.CostSample (Cloud Monitoring v3 timeSeries.list of
//     aiplatform.googleapis.com/publisher/online_serving/token_count on the PublisherModel
//     resource): one sample per (model, location, time bucket), input/output tokens split
//     by the metric `type` label, cost DERIVED from declared list pricing
//     (Provenance=estimated — the metric carries counts, not money). MINIMAL DATA: only
//     token COUNTS and the model/location refs; never a prompt or completion.
//
//   - BILLED COST → model.CostSample (opt-in, operator-wired): GCP has NO real-time cost
//     API (cloudbilling.googleapis.com exposes only rate cards + budget thresholds, never
//     incurred spend); actual Vertex cost lives only in the BigQuery billing export. So,
//     mirroring the gemini connector's usage_url, the operator wires cost_export_url at a
//     materialized billing-export result (the connector GETs it read-only); each row
//     becomes a billed CostSample. With no cost_export_url, billed cost is ABSENT (never
//     fabricated) and only the derived-cost usage stream stands. The two streams are
//     separate, honest lenses that do not double-count (usage: real tokens / derived
//     cost; billed: real cost / no tokens).
//
//   - SAFETY POSTURE → model.FindingReport{Kind:"safety_posture"} (opt-in, default-off):
//     Model Armor — the Google equivalent of Bedrock Guardrails / Azure RAI. VERIFIED
//     2026-07-05 against the Model Armor v1 discovery document (revision 20260624) and
//     docs pages last updated 2026-06-29. Reads per-region TEMPLATES (RAI filters +
//     confidence, prompt-injection/jailbreak, malicious-URI, Sensitive-Data-Protection
//     enforcement, and templateMetadata enforcement/filter-version posture) and global
//     FLOOR SETTINGS. Project floors are the runtime floor; org/folder floors are
//     conformance baselines only, with lower levels taking precedence (project > folder >
//     org) and integratedServices runtime enforcement documented only at project level.
//     Optional expect_floor_* config emits policy_drift when the project floor diverges
//     from a declared baseline. Config STATE only — it NEVER calls the content-reading
//     :sanitize* data-plane methods, and read-only posture reads do not consume the Model
//     Armor per-sanitized-token meter (only request quota applies). The Model Armor
//     DetectionConfidenceLevel is INVERTED (LOW_AND_ABOVE is the strictest filter, HIGH
//     the most permissive); the posture scoring honors that.
//
//     Inline Gemini enforcement facts: Model Armor floor settings apply to Gemini
//     Enterprise Agent Platform generateContent calls in the project even when
//     modelArmorConfig is omitted. Per-request modelArmorConfig{promptTemplateName,
//     responseTemplateName} is mutually exclusive with safety_settings and has precedence
//     over the floor, which has precedence over Gemini built-in filters. Blocks surface as
//     MODEL_ARMOR prompt/candidate reasons. Only generateContent is documented here:
//     streamGenerateContent coverage is UNVERIFIED and treated as not covered, and the
//     Gemini Developer API (generativelanguage.googleapis.com) is not covered. The
//     integration is documented fail-open when Model Armor is unreachable or errors
//     in-region, so a floor is a baseline, not a guarantee. Image-modality screening
//     (Preview, us/eu only) and the streaming sanitization API (Preview) exist upstream
//     and are deliberately NOT read by this connector.
//
//   - SANITIZATION EVENTS → model.FindingReport{Kind:"guardrail"} +
//     model.MetricSample (opt-in, default-off): reads Model Armor sanitization RESULTS
//     from Cloud Logging entries:list using the documented canonical selector
//     jsonPayload.@type="type.googleapis.com/google.cloud.modelarmor.logging.v1.SanitizeOperationLogEntry"
//     (pinned 2026-07-05). It emits High findings for blocked operations, Medium
//     findings for matched-but-allowed operations, and aggregate sanitize-operation
//     MetricSamples by verdict and operation. MINIMAL DATA: Model Armor platform logs
//     can embed the full prompt/response text; this connector never decodes it —
//     sanitizationInput and SDP inspect/deidentify sub-payloads are not even struct
//     members. This surface depends on upstream logging being enabled
//     (aiPlatformFloorSetting.enableCloudLogging or templateMetadata.logSanitizeOperations);
//     expect_floor_logging is the drift key that declares that precondition for the
//     project floor. Caller identity is limited to the integration label and an optional
//     operator-supplied correlation id; there is no end-user principal on this platform
//     log surface, so findings are attributed to the template/project. Boundary:
//     connectors/gcp-audit reads Cloud Audit Logs (protoPayload management/data-access
//     events); this connector reads Model Armor PLATFORM logs (jsonPayload result
//     metadata), so there is no row overlap or double count. Cost: Cloud Logging bills
//     the stored log volume; Google's platform log design may include full payloads, but
//     Olivares reads only verdict/filter metadata.
//
// # Scope boundary (no duplication)
//
// IAM allow-policy reads and the Cloud Audit Logs access edge are DEFERRED to
// connectors/gcp-audit (Decision): gcp-audit already reads aiplatform
// Predict/GenerateContent activity generically. This connector owns only the catalog,
// usage, cost, Model Armor posture and Model Armor platform-log surfaces gcp-audit does
// not.
//
// # Security posture (docs/SECURITY-HARDENING.md-3)
//
// Read-only: it performs only GETs against Google APIs (and the operator's cost export),
// except Cloud Logging entries:list's read-only POST query body, same as gcp-audit —
// never a write, never the paid Model Armor data-plane. It mints OAuth2 access tokens
// from a service-account key with the standard-library JWT-bearer flow (auth.go), holds
// the credential only in memory, and never logs or emits it. It imports only the SDK and
// the Apache modelprovider contract, never the engine.
package vertex
