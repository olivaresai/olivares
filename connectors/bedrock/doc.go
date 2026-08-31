// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package bedrock is the read-only Amazon Bedrock usage/cost + Guardrails observability
// connector. It governs the REST of an account's Bedrock estate — every model
// vendor, the token consumption, the billed cost, and the Guardrails posture — where the
// s3-cloudtrail connector deliberately covers only the Claude ACCESS edge (no tokens, no
// cost). It is provider-agnostic: Titan/Nova, Llama, Mistral, Cohere, AI21, etc. are
// classified the same as Claude via the shared connectors/internal/bedrockid helper.
// OpenAI-on-Bedrock is the same Bedrock surface: bare openai.* model ids (for example
// openai.gpt-5.5) are served through the bedrock-mantle endpoint's openai/v1 path
// (verified 2026-07-04) and remain ProviderRef="bedrock" usage here.
// OpenAI organization usage/cost APIs do not see this AWS-billed traffic, so no
// cross-connector dedupe with connectors/openai is required.
//
// # What it emits (the contract dependents consume)
//
// Token usage → model.CostSample (one per model invocation, from model-invocation
// logging):
//   - ProviderRef = "bedrock"; ModelRef = the bare model id (e.g. "anthropic.claude-…",
//     "amazon.nova-pro-v1:0", or an opaque application-inference-profile id);
//     Gateway = bedrock-mantle (bare vendor.model id) or bedrock-legacy (CRIS geo-prefixed
//     id / unrecognized); InputTokens/OutputTokens from input.inputTokenCount /
//     output.outputTokenCount; Actor = the caller principal ARN (attribution ref).
//   - CostMicroUSD = 0, Provenance = estimated: the invocation log carries NO money, so
//     the cost is not reported (0 ≠ "free"); the token counts are real. MINIMAL DATA: the
//     model input/output bodies in the log are never read, only the counts and refs.
//
// Billed cost → model.CostSample (from Cost Explorer GetCostAndUsage, opt-in):
//   - ProviderRef = "bedrock"; ModelRef = the AWS line_item_usage_type (the model is
//     embedded in it, e.g. "USE1-Claude4.6Sonnet-input-tokens" — Cost Explorer has no
//     per-model dimension); CostMicroUSD = the UnblendedCost (parsed from the decimal
//     string with no float); tokens = 0. Provenance = billed for a finalized period, or
//     estimated for a not-yet-finalized period (AWS marks it Estimated — preliminary, not
//     yet reconcilable); never billed for a preliminary figure.
//   - No row ⇒ no sample (cost is never fabricated; absence ≠ zero). A negative line is a
//     real billed credit/refund and is kept so net spend reconciles; only zero is skipped.
//
// The usage stream and the cost stream are SEPARATE, honest lenses at different grains
// and do NOT double-count: the usage stream has cost=0, the cost stream has tokens=0.
//
// Guardrails posture → model.FindingReport{Kind:"safety_posture"} (opt-in), on the
// contract defined: SubjectKind "bedrock.guardrail" (per-guardrail config: content/
// topic/word/PII/contextual-grounding counts, PROMPT_ATTACK presence, and the Automated
// Reasoning policy — the field the aws connector does not read) and "bedrock.logging"
// (model-invocation-logging decision-auditability). It is read-first: it never calls the
// paid ApplyGuardrail runtime, and it reports the absence of guardrails/logging as the
// posture, not a silent gap. Enable Guardrails on EXACTLY ONE Bedrock connector (this one
// OR the aws connector's enable_bedrock) to avoid redundant reads.
//
// # Sources and credentials
//
// Token usage is read from BOTH delivery destinations: S3-delivered log files via a local
// path (usage_log_path — local I/O, no AWS credentials, like s3-cloudtrail) and CloudWatch
// Logs via FilterLogEvents (usage_log_group — signed). Cost and Guardrails are signed AWS
// reads (credentials required). Bedrock control plane and CloudWatch Logs are regional;
// Cost Explorer is global (always signed us-east-1). The connector imports no engine
// package (Apache-2.0 boundary) and reads no payloads, secrets or key material.
package bedrock
