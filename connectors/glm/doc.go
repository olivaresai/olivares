// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package glm is the Olivares AI connector for Zhipu AI / Z.ai GLM hosted APIs.
//
// GLM exposes two first-party OpenAI-compatible /api/paas/v4 surfaces: the default
// international Z.ai API (api.z.ai, USD list pricing) and the China BigModel API
// (open.bigmodel.cn, CNY console/pricing surface). This connector only declares
// verified USD list prices; model ids whose USD prices are UNVERIFIED remain
// cataloged with nil Pricing rather than a guessed value.
//
// SOVEREIGNTY CAVEAT (E1). The BigModel surface is PRC-hosted under Chinese
// law. The Z.ai surface is legally operated from Singapore, but shares a PRC-domiciled
// parent, Beijing Zhipu Huazhang, which is on the US BIS Entity List. The caveat
// therefore applies to both hosted surfaces. For sovereign governance, Olivares AI
// recommends self-hosting the MIT open weights (GLM-4.5, GLM-4.5-Air, GLM-4.6,
// GLM-5.2 and successors) through Ollama or vLLM and governing that local surface.
//
// HONEST SCOPE. The /models path exists, but its response schema is undocumented and
// UNVERIFIED, so Snapshot always returns the declared catalog with
// CapabilitySource="declared"; Gather uses GET /models only as a liveness/entitlement
// probe and discards the body. GLM exposes no verified usage, billing, balance,
// admin, key or organization API. Cost is therefore metered around the inference path
// through the exported Meter helper from declared list pricing, never pulled by Gather.
//
// READ-ONLY and minimal-data (docs/SECURITY-HARDENING.md-3): every provider call is a GET via the
// shared GET-only modelprovider client. The connector carries model ids, capabilities,
// declared prices and entitlement posture only — never prompts, completions or key
// values. It imports only the connector SDK, connectors/modelprovider and the internal
// redaction helper, never the engine.
package glm
