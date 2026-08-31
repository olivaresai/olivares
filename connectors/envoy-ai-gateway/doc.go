// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package envoyaigw is the Olivares AI governance connector for the Envoy AI
// Gateway CONFIG POSTURE — a read-only, minimal-data source that turns the
// gateway's declared configuration into governed posture and policy drift. It is
// the sibling of, and deliberately distinct from, the two existing Envoy
// connectors: connectors/ai-gateway ingests the gateway's per-request USAGE/COST
// telemetry (module XXI CostSamples), and connectors/envoy ingests the L7 mesh
// (ALS / ext_authz / ext_proc). This connector reads neither traffic nor cost — it
// reads the DECLARED CONFIG (the applied CRDs) and answers "is the gateway itself
// configured safely, and does its policy agree with ours?".
//
// Olivares is NOT a gateway (product doctrine): the customer's Envoy AI
// Gateway is a SURFACE to govern, exactly like Cloudflare AI Gateway. The
// differentiation lives above the wire — identity-bound policy and offline evidence.
//
// # What it reads (read-only, never the API)
//
// An operator-exported snapshot of the applied Envoy AI Gateway CRDs, as a file or a
// directory of *.json / *.yaml / *.yml (a single k8s object, a List, or a stream).
// The honest ingest path mirrors the sibling connectors: the operator dumps the
// config and points "config_path" at it, e.g.
//
//	kubectl get aigatewayroutes,aiservicebackends,backendsecuritypolicies,\
//	  mcproutes,quotapolicies -A -o json > envoy-aigw.json
//
// The connector never calls the Kubernetes API, never opens a listener, never
// proxies a request, and never reads a prompt, a completion, or a secret value.
//
// # Verified schema (anti-fabrication) — Envoy AI Gateway v1.0 (v1alpha1)
//
// Field names verified against aigateway.envoyproxy.io (API reference) 2026-07-12:
//   - AIGatewayRoute.spec.rules[].backendRefs[]{name, modelNameOverride, weight,
//     priority}, rules[].llmRequestCosts[]{metadataKey, type}, rules[].timeouts.
//   - AIServiceBackend.spec.schema.name ∈ {OpenAI, Cohere, AWSBedrock, AzureOpenAI,
//     GCPVertexAI, GCPAnthropic, Anthropic, AWSAnthropic}.
//   - BackendSecurityPolicy.spec.targetRefs[].name, .type ∈ {APIKey, AWSCredentials,
//     AzureAPIKey, AnthropicAPIKey, AzureCredentials, GCPCredentials}.
//   - MCPRoute.spec.backendRefs[]{name, toolSelector{include, includeRegex, exclude,
//     excludeRegex}, securityPolicy}, spec.securityPolicy, spec.path.
//   - QuotaPolicy.spec.targetRefs[].name, .defaultBucket{limit, duration}.
//
// Every field is optional (tolerant decode); an unknown kind or a renamed field
// degrades to fewer findings, never a fabricated one.
//
// # What it governs (findings + edges)
//
//   - A reachable AIServiceBackend with NO BackendSecurityPolicy targeting it →
//     High: an unauthenticated upstream (anyone routed there reaches the provider).
//   - An MCPRoute (or one of its backends) with NO securityPolicy → High: MCP
//     passthrough with no auth. A backend with no toolSelector allowlist → Medium:
//     every tool on that MCP server is exposed.
//   - A served model (backendRef.modelNameOverride, else the backend name) outside
//     the operator's declared model-access allowlist → High drift (only when
//     approved_models is configured — the honest gateway-vs-Olivares policy diff).
//   - An AIGatewayRoute rule with neither llmRequestCosts nor a QuotaPolicy →
//     Low: a FinOps/quota blind spot (cost is not metered and spend is uncapped).
//   - Edges route→backend (SignalConfig), so the estate map sees which gateway
//     route can reach which provider backend.
//
// Minimal data: only names, kinds, the schema/provider label, the security-policy
// TYPE (never the key), and boolean presence are read — a negative test asserts an
// embedded secret never reaches an observation. It imports only the SDK and
// connectors/internal — never the engine (/core).
package envoyaigw
