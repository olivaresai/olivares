// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/evals"
	"github.com/olivaresai/olivares/modules/knowledge"
	"github.com/olivaresai/olivares/modules/recording"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// This file is the composition-root glue that makes "Claude is first-class" execute
// at RUNTIME (CLA-17-A): it builds the Claude inference client (connectors/
// claude-api, Apache) and wraps it as the evals.Judge and knowledge.Embedder the
// modules already expose seams for (evals.WithJudge / knowledge.WithEmbedder). The
// adapters live HERE, in cmd (AGPL), because they bridge the AGPL module ports
// (which take core/model.TenantID) to the Apache connector — a connector must never
// import /core, so the bridge cannot live in the connector.
//
// Both seams stay FAIL-CLOSED unless explicitly configured: with no inference
// credential the Judge is not wired (llm_judge stays SKIPPED — honest, never a false
// pass); with no embeddings provider the Embedder is not wired (knowledge keeps its
// zero-egress LocalHashEmbedder). The operator opts in via env (see resolveInference).
//
// Minimal-data (docs/SECURITY-HARDENING.md): the adapters pass prompts/outputs to the model in
// flight and return only a verdict/score or vectors — nothing raw is persisted here.

// claudeInference holds the wired adapters (any may be nil if unconfigured).
type claudeInference struct {
	judge           *claudeJudgeAdapter
	embedder        *claudeEmbedderAdapter
	knowledgeStatus *knowledgePlaneStatus
	// recSummarizer is the recording.Summarizer over the SAME inference
	// client as the judge (one credential, one gateway posture); nil keeps the
	// recording module's honest 501.
	recSummarizer *recordingSummarizerAdapter
}

// Compile-time proof the adapters satisfy the module seams they are injected into.
var (
	_ evals.Judge        = (*claudeJudgeAdapter)(nil)
	_ evals.PairJudge    = (*claudeJudgeAdapter)(nil)
	_ knowledge.Embedder = (*claudeEmbedderAdapter)(nil)
)

// ---- evals.Judge adapter ---------------------------------------------------------

// runtimeObservationSink publishes the runtime cost/forensic observations of a
// governed Claude call onto the bus (the in-process liveingest producer, the only
// component that can Host.Publish a sealed Observation the out-of-process connector
// cannot). It is late-bound after the module set is built (liveingest's Host exists
// only at Init); nil means no emission. Implemented by *liveingest.Module
// (PublishCostSample / PublishFinding).
type runtimeObservationSink interface {
	PublishCostSample(ctx context.Context, tenant string, cs sdkmodel.CostSample) error
	PublishFinding(ctx context.Context, tenant string, f sdkmodel.FindingReport) error
}

// claudeJudgeAdapter implements evals.Judge by invoking the Claude Messages API. A
// transport/parse fault surfaces as an error (the scorer records outcome=error); it
// never returns the offline-judge sentinel, so a wired-but-failing judge is visibly
// an error, not a silent SKIPPED. It is ALSO the live emitter of the runtime cost/
// forensic the verdict hides (CLA-15/ANT2-15): after every judge call it publishes
// the top-level + advisor cost lines and the thinking/advisor/refusal findings to
// the bus through costSink, fail-open.
type claudeJudgeAdapter struct {
	inf      *claudeapi.Inference
	costSink runtimeObservationSink // nil until late-bound; emission is fail-open
	log      *slog.Logger
}

func (a *claudeJudgeAdapter) Judge(ctx context.Context, tenant model.TenantID, req evals.JudgeRequest) (evals.JudgeVerdict, error) {
	res, resp, err := a.inf.JudgeWithResponse(ctx, claudeapi.JudgeInput{
		ModelRef:  req.ModelRef,
		Input:     req.Input,
		Output:    req.Output,
		Expected:  req.Expected,
		Criterion: req.Criterion,
	})
	// Emit the runtime cost/forensic this billable call incurred (CLA-15/ANT2-15),
	// even when the verdict failed to parse — the Messages call was still made and
	// billed. Fail-open: a publish/transport hiccup never changes the eval outcome.
	a.emitRuntime(ctx, tenant, resp)
	if err != nil {
		return evals.JudgeVerdict{}, err
	}
	return evals.JudgeVerdict{Score: res.Score, Passed: res.Passed, Reason: res.Reason}, nil
}

// JudgePair implements evals.PairJudge over the SAME inference client/credential as
// the pointwise judge: one ORDERED pairwise comparison per call — the
// order-swap (two calls with the candidates exchanged) is the evals module's job,
// so every PairJudge implementation inherits the position-bias mitigation. Like
// Judge it emits the runtime cost/forensic of the billed call, fail-open.
func (a *claudeJudgeAdapter) JudgePair(ctx context.Context, tenant model.TenantID, req evals.PairRequest) (evals.PairVerdict, error) {
	res, resp, err := a.inf.JudgePairWithResponse(ctx, claudeapi.JudgePairInput{
		ModelRef:     req.ModelRef,
		Input:        req.Input,
		Criterion:    req.Criterion,
		OutputFirst:  req.OutputFirst,
		OutputSecond: req.OutputSecond,
	})
	a.emitRuntime(ctx, tenant, resp)
	if err != nil {
		return evals.PairVerdict{}, err
	}
	// Map the connector's positional labels onto the module's exported ones —
	// explicit, so neither package depends on the other's strings staying equal.
	winner := evals.PairWinnerTie
	switch res.Winner {
	case claudeapi.PairWinnerFirst:
		winner = evals.PairWinnerFirst
	case claudeapi.PairWinnerSecond:
		winner = evals.PairWinnerSecond
	}
	return evals.PairVerdict{Winner: winner, Reason: res.Reason}, nil
}

// emitRuntime publishes the cost lines + forensic findings of one governed judge
// response. It runs only when a response actually came back (resp.ID set — a
// transport error returns a zero response, nothing to account) and a sink is bound.
// The advisor sub-line is CostType="advisor" (a SEPARATE spend), a refusal yields a
// security finding and NO cost line, and thinking is already inside the top-level
// output cost (never a second line) — RuntimeObservations enforces all of that.
func (a *claudeJudgeAdapter) emitRuntime(ctx context.Context, tenant model.TenantID, resp claudeapi.MessageResponse) {
	if a.costSink == nil || resp.ID == "" {
		return
	}
	samples, findings := a.inf.RuntimeObservations(resp, "", time.Time{}, false)
	for _, cs := range samples {
		if err := a.costSink.PublishCostSample(ctx, tenant.String(), cs); err != nil && a.log != nil {
			a.log.Debug("evals: runtime cost publish failed (fail-open)", "err", err)
		}
	}
	for _, f := range findings {
		if err := a.costSink.PublishFinding(ctx, tenant.String(), f); err != nil && a.log != nil {
			a.log.Debug("evals: runtime forensic publish failed (fail-open)", "err", err)
		}
	}
}

// bindRuntimeCostSink late-binds the in-process publisher into the judge adapter so
// each governed judge call emits its runtime cost/forensic. The sink's Host is nil
// until Init and PublishCostSample is nil-safe, so binding the (stable) module
// pointer at buildModules time is in time. No-op when no judge is wired.
func (ci claudeInference) bindRuntimeCostSink(sink runtimeObservationSink) {
	if ci.judge != nil {
		ci.judge.costSink = sink
	}
	if ci.recSummarizer != nil {
		ci.recSummarizer.costSink = sink
	}
}

// ---- knowledge.Embedder adapter --------------------------------------------------

// claudeEmbedderAdapter implements knowledge.Embedder over a model-backed embeddings
// provider. Hosted providers are egressing; an explicitly self-hosted OpenAI-compatible
// endpoint is not third-party egress, so AllowsEgress follows the selected provider.
// NB: Anthropic exposes no first-party embeddings API, so this targets the operator's
// configured embeddings provider (Voyage/OpenAI-compatible/self-hosted).
type claudeEmbedderAdapter struct {
	emb      *modelprovider.EmbeddingsClient
	provider string
	modelRef string
	dim      int
	geo      string // the provider's data-residency region (inference_geo); "" = undeclared
	egress   bool
}

func (a *claudeEmbedderAdapter) Embed(ctx context.Context, _ model.TenantID, texts []string) ([][]float32, string, error) {
	vecs, err := a.emb.Embed(ctx, texts)
	if err != nil {
		return nil, "", err
	}
	return vecs, a.modelRef, nil
}

func (a *claudeEmbedderAdapter) Dim() int            { return a.dim }
func (a *claudeEmbedderAdapter) AllowsEgress() bool  { return a.egress }
func (a *claudeEmbedderAdapter) ModelRef() string    { return a.modelRef }
func (a *claudeEmbedderAdapter) ProviderRef() string { return a.provider }

// Region declares the data-residency region of the embeddings provider this adapter
// egresses to (the inference_geo, e.g. "global"|"us"|"eu"|...). The knowledge
// module reads it (the optional embedder Region() capability) to refuse embedding a
// residency-locked KB whose region differs — the data must not cross the residency
// boundary (docs/SECURITY-HARDENING.md). "" (undeclared) makes every residency-locked KB refuse
// egress, fail-closed: the module cannot prove the provider is in-region. NOTE:
// the Workspace/data-residency geo is a control SEPARATE from inference routing —
// declaring it here is the operator's attestation that the provider honors it.
func (a *claudeEmbedderAdapter) Region() string { return a.geo }

// ---- env resolution (SCP-08 deploy env-contract) ---------------------------------

// gatewayFromEnv resolves the Claude deployment SURFACE and its Messages API base
// URL from the documented Claude Code / Anthropic env vars (verified jun-2026;
// VERIFIED 2026-07-03 against code.claude.com/docs/en/claude-platform-on-aws).
// This is the env-passthrough the Helm chart will template; the control plane
// reads the same names so a single env-contract governs both. Precedence: an explicit
// gateway selector (CLAUDE_CODE_USE_*) picks the surface; the matching
// ANTHROPIC_*_BASE_URL (or ANTHROPIC_BASE_URL for direct) overrides the endpoint.
// Claude Platform on AWS defaults to aws-external-anthropic.{region}.api.aws computed
// from AWS_REGION, and ANTHROPIC_AWS_BASE_URL is the documented gateway/proxy override.
func gatewayFromEnv(getenv func(string) string) (sdkmodel.Gateway, string) {
	truthy := func(k string) bool {
		v := strings.TrimSpace(getenv(k))
		return v == "1" || strings.EqualFold(v, "true")
	}
	switch {
	case truthy("CLAUDE_CODE_USE_MANTLE"):
		return sdkmodel.GatewayBedrockMantle, getenv("ANTHROPIC_BEDROCK_MANTLE_BASE_URL")
	case truthy("CLAUDE_CODE_USE_BEDROCK"):
		// USE_BEDROCK without USE_MANTLE selects the legacy InvokeModel/Converse surface.
		return sdkmodel.GatewayBedrockLegacy, getenv("ANTHROPIC_BEDROCK_BASE_URL")
	case truthy("CLAUDE_CODE_USE_VERTEX"):
		return sdkmodel.GatewayVertex, getenv("ANTHROPIC_VERTEX_BASE_URL")
	case truthy("CLAUDE_CODE_USE_FOUNDRY"):
		return sdkmodel.GatewayFoundry, getenv("ANTHROPIC_FOUNDRY_BASE_URL")
	case truthy("CLAUDE_CODE_USE_ANTHROPIC_AWS"):
		// Provider routing precedence is documented as Bedrock/Foundry before Claude
		// Platform on AWS (VERIFIED 2026-07-03, code.claude.com/docs/en/claude-platform-on-aws).
		return sdkmodel.GatewayClaudePlatformAWS, getenv("ANTHROPIC_AWS_BASE_URL")
	default:
		return sdkmodel.GatewayDirect, getenv("ANTHROPIC_BASE_URL")
	}
}

// resolveInference builds the inference config from env. The inference credential is
// OLIVARES_CLAUDE_INFERENCE_KEY (preferred) or ANTHROPIC_API_KEY. The judge model
// defaults to the pinned ANTHROPIC_DEFAULT_OPUS_MODEL or the repo's current Opus id
// (claude-opus-4-8) — a verified, catalog-consistent default, not a fabrication. It
// returns ok=false when no credential is set (the Judge then stays unwired).
func resolveInference(getenv func(string) string) (claudeapi.InferenceConfig, bool) {
	key := strings.TrimSpace(getenv("OLIVARES_CLAUDE_INFERENCE_KEY"))
	if key == "" {
		key = strings.TrimSpace(getenv("ANTHROPIC_API_KEY"))
	}
	if key == "" {
		return claudeapi.InferenceConfig{}, false
	}
	gw, base := gatewayFromEnv(getenv)
	defModel := strings.TrimSpace(getenv("ANTHROPIC_DEFAULT_OPUS_MODEL"))
	if defModel == "" {
		defModel = "claude-opus-4-8"
	}
	return claudeapi.InferenceConfig{
		BaseURL:          strings.TrimSpace(base),
		APIKey:           key,
		AnthropicVersion: strings.TrimSpace(getenv("ANTHROPIC_VERSION")),
		Gateway:          gw,
		DefaultModel:     defModel,
	}, true
}

type embeddingsProviderConfig struct {
	Provider string
	BaseURL  string
	APIKey   string
	Model    string
	Dim      int
	Geo      string
	Egress   bool
}

// resolveEmbeddingsProvider selects a model-backed embeddings provider from env. The
// order is deliberately modular and documented here because operators often carry
// multiple credentials:
//  1. unscoped OLIVARES_EMBEDDINGS_{BASE_URL,KEY,MODEL,DIM}: explicit operator
//     override, still the base contract and compatible with existing installs;
//  2. OLIVARES_EMBEDDINGS_PROVIDER=<voyage|openai|openai_compat|self_hosted>:
//     provider-specific block selected by policy or by the active proxy profile;
//  3. provider blocks in default order: Voyage (Anthropic's documented companion),
//     OpenAI, generic OpenAI-compatible gateway, then self-hosted OpenAI-compatible
//     endpoint. SELF_HOSTED is the air-gap path (TEI/Ollama/vLLM); no third-party
//     egress is declared for that provider.
//
// Anthropic is intentionally absent: it has no embeddings endpoint. All blocks use
// the same four suffixes plus optional _GEO:
// OLIVARES_EMBEDDINGS_<PROVIDER>_{BASE_URL,KEY,MODEL,DIM,GEO}.
func resolveEmbeddingsProvider(getenv func(string) string) (embeddingsProviderConfig, bool, string) {
	if cfg, ok, incomplete := readEmbeddingsBlock(getenv, "OLIVARES_EMBEDDINGS", "openai_compat", true); ok || incomplete {
		return cfg, ok, statusReason(ok, incomplete, reasonEmbeddingsConfigured, reasonEmbeddingsIncomplete)
	}

	order := []string{}
	pinned := normalizeEmbeddingsProvider(getenv("OLIVARES_EMBEDDINGS_PROVIDER"))
	if pinned != "" {
		order = append(order, pinned)
	}
	order = appendUnique(order, "voyage", "openai", "openai_compat", "self_hosted")
	for _, provider := range order {
		prefix := "OLIVARES_EMBEDDINGS_" + strings.ToUpper(strings.ReplaceAll(provider, "-", "_"))
		if cfg, ok, incomplete := readEmbeddingsBlock(getenv, prefix, provider, provider != "self_hosted"); ok || incomplete {
			return cfg, ok, statusReason(ok, incomplete, reasonEmbeddingsConfigured, reasonEmbeddingsIncomplete)
		}
	}
	if pinned != "" {
		// The operator NAMED a provider and no block backs it. That is declared
		// intent going unhonoured — an incomplete configuration to fix, not the
		// pristine "no provider wanted" default that /status may report as merely
		// not configured.
		return embeddingsProviderConfig{}, false, reasonEmbeddingsIncomplete
	}
	return embeddingsProviderConfig{}, false, reasonEmbeddingsMissing
}

func statusReason(ok, incomplete bool, okReason, incompleteReason string) string {
	if ok {
		return okReason
	}
	if incomplete {
		return incompleteReason
	}
	return reasonEmbeddingsMissing
}

func normalizeEmbeddingsProvider(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	v = strings.ReplaceAll(v, "-", "_")
	switch v {
	case "voyage", "openai", "openai_compat", "self_hosted":
		return v
	case "openai-compatible", "openai_compatible", "compat":
		return "openai_compat"
	case "selfhosted", "self-hosted", "local", "tei", "ollama", "vllm":
		return "self_hosted"
	default:
		return ""
	}
}

func appendUnique(order []string, providers ...string) []string {
	seen := make(map[string]struct{}, len(order)+len(providers))
	out := make([]string, 0, len(order)+len(providers))
	for _, p := range append(order, providers...) {
		p = normalizeEmbeddingsProvider(p)
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}

func readEmbeddingsBlock(getenv func(string) string, prefix, provider string, egress bool) (embeddingsProviderConfig, bool, bool) {
	base := strings.TrimSpace(getenv(prefix + "_BASE_URL"))
	key := strings.TrimSpace(getenv(prefix + "_KEY"))
	mdl := strings.TrimSpace(getenv(prefix + "_MODEL"))
	rawDim := strings.TrimSpace(getenv(prefix + "_DIM"))
	dim, _ := strconv.Atoi(rawDim)
	geo := strings.ToLower(strings.TrimSpace(getenv(prefix + "_GEO")))
	anySet := base != "" || key != "" || mdl != "" || rawDim != "" || geo != ""
	if base == "" || key == "" || mdl == "" || dim <= 0 {
		return embeddingsProviderConfig{Provider: provider, BaseURL: base, APIKey: key, Model: mdl, Dim: dim, Geo: geo, Egress: egress}, false, anySet
	}
	return embeddingsProviderConfig{Provider: provider, BaseURL: base, APIKey: key, Model: mdl, Dim: dim, Geo: geo, Egress: egress}, true, false
}

// resolveEmbedder builds the embeddings adapter from env, or returns ok=false.
// It keeps the original four-var contract but can also select a provider-specific
// OpenAI-compatible credential block via resolveEmbeddingsProvider.
func resolveEmbedder(getenv func(string) string) (*claudeEmbedderAdapter, bool) {
	cfg, ok, _ := resolveEmbeddingsProvider(getenv)
	if !ok {
		return nil, false
	}
	emb := modelprovider.NewEmbeddingsClient(modelprovider.EmbeddingsConfig{
		BaseURL: cfg.BaseURL, APIKey: cfg.APIKey, Model: cfg.Model, Dim: cfg.Dim,
	})
	return &claudeEmbedderAdapter{emb: emb, provider: cfg.Provider, modelRef: cfg.Model, dim: cfg.Dim, geo: cfg.Geo, egress: cfg.Egress}, true
}

// embeddingsRequired reports whether the operator declared a HARD requirement for a
// semantic, model-backed embedder (OLIVARES_EMBEDDINGS_REQUIRE truthy). It is the
// explicit "I intend semantic retrieval globally" switch.
func embeddingsRequired(getenv func(string) string) bool {
	v := strings.TrimSpace(getenv("OLIVARES_EMBEDDINGS_REQUIRE"))
	return v == "1" || strings.EqualFold(v, "true")
}

// checkEmbeddingsRequirement returns an error when the operator REQUIRED a semantic
// embedder (OLIVARES_EMBEDDINGS_REQUIRE) but none is configured. The control plane
// then refuses to boot rather than silently serve the lexical local-hash fallback as
// if it were semantic (docs/SECURITY-HARDENING.md — never a silent gap, never lexical-as-
// semantic). Without the flag the zero-egress LocalHashEmbedder remains the correct,
// VISIBLE air-gap default (knowledge Start() warns once); per-KB embed_policy=
// model_backed already refuses the local fallback at create/ingest (kb.go).
func checkEmbeddingsRequirement(getenv func(string) string) error {
	if !embeddingsRequired(getenv) {
		return nil
	}
	if _, ok, reason := resolveEmbeddingsProvider(getenv); !ok {
		return errors.New("OLIVARES_EMBEDDINGS_REQUIRE is set but no model-backed embedder is configured: set OLIVARES_EMBEDDINGS_{BASE_URL,KEY,MODEL,DIM} or a provider block (OLIVARES_EMBEDDINGS_<VOYAGE|OPENAI|OPENAI_COMPAT|SELF_HOSTED>_{BASE_URL,KEY,MODEL,DIM}); current reason=" + reason + " — refusing to serve lexical local-hash vectors as semantic")
	}
	return nil
}

// loadClaudeInference resolves the Judge and Embedder adapters from env (testable via
// the injected getenv). It logs which seams it wired so an operator sees, at boot,
// whether the Claude judge and a model-backed embedder are live or fail-closed.
//
// inferenceDoer is the HTTP transport the Messages API client uses (OBS-03): the
// composition root passes the trace-instrumented client, so every engine→Claude hop
// injects the W3C traceparent (stitching with the service mesh) and emits the
// gen_ai span + client metrics. nil falls back to the default client (untraced) —
// used by the e2e harness and tests that make no real calls.
func loadClaudeInference(getenv func(string) string, inferenceDoer modelprovider.Doer, log *slog.Logger) claudeInference {
	var ci claudeInference
	switch cfg, ok := resolveInference(getenv); {
	case !ok:
		log.Info("evals: no Claude inference credential (OLIVARES_CLAUDE_INFERENCE_KEY/ANTHROPIC_API_KEY); llm_judge stays SKIPPED (fail-closed, never a false pass)")
	case cfg.Gateway != sdkmodel.GatewayDirect && cfg.BaseURL == "":
		// A non-direct gateway was selected (CLAUDE_CODE_USE_*) but its endpoint is
		// unset, so the client would default to the DIRECT Anthropic base URL while
		// stamping the call with the non-direct gateway — mislabeled cost/governance
		// against the wrong endpoint. Refuse to wire rather than emit a lie.
		log.Warn("evals: non-direct Claude gateway selected without its base URL; refusing to wire llm_judge (set the matching ANTHROPIC_*_BASE_URL)", "gateway", string(cfg.Gateway))
	default:
		cfg.Doer = inferenceDoer // OBS-03: trace-instrument the engine→Claude hop
		inf := claudeapi.NewInference(cfg)
		ci.judge = &claudeJudgeAdapter{inf: inf, log: log}
		ci.recSummarizer = &recordingSummarizerAdapter{inf: inf, log: log}
		log.Info("evals: wired Claude-backed llm_judge (Messages API)", "gateway", string(cfg.Gateway), "judge_model_default", cfg.DefaultModel)
	}
	ci.knowledgeStatus = newKnowledgePlaneStatus(localHashKnowledgeStatus(reasonEmbeddingsMissing), log)
	_, _, embReason := resolveEmbeddingsProvider(getenv)
	if emb, ok := resolveEmbedder(getenv); ok {
		ci.embedder = emb
		ci.knowledgeStatus.set(semanticKnowledgeStatus(reasonEmbeddingsConfigured))
		log.Info("knowledge: wired model-backed Embedder", "provider", emb.provider, "model", emb.modelRef, "dim", emb.dim, "egress", emb.egress)
	} else {
		ci.knowledgeStatus.set(localHashKnowledgeStatus(embReason))
		// WARN, AND IT STAYS WARN (re-decided at integration, 2026-08-06). The boot log-level
		// rule this line was demoted under is "does the engine REFUSE (WARN) or does it ANSWER
		// (INFO)", and by that reading retrieval answers, lexically, so INFO looked right. It
		// is not, and the reason is written in own commit: the local-hash fallback was
		// made "a persistent, VISIBLE degradation (/status, health summary, `olivares status`,
		// boot WARN) INSTEAD OF ONE INFO LOG". Demoting it restored exactly the state that
		// session set out to fix.
		//
		// The two rules are about different axes and both are right. Refuse-vs-answer governs a
		// request the engine turns away. This is an answer the operator did not ask for: they
		// configured governed RAG and are being served LEXICAL retrieval where they expect
		// semantic, and no request fails to tell them. A capability silently below what was
		// configured is the shape a WARN exists for — the same reason the compliance line went
		// UP a level in that very sweep rather than down.
		//
		// TestLoadClaudeInference_NoEmbeddingsWarnsAndStatusIsLocalHash pins it. Neither branch
		// was red alone: the demotion shipped on a feature lane, which runs fast lints only.
		log.Warn("knowledge: no usable embeddings provider configured; keeping zero-egress LocalHashEmbedder — retrieval is lexical, NOT semantic", "reason", embReason)
	}
	return ci
}

// osGetenv is the production env reader. It overlays the enterprise ACTIVATION
// MANIFEST: when an add-on's OLIVARES_*_CONFIG variable is UNSET in the
// real environment but an operator activated that add-on via a preset
// (`olivares enterprise enable`), the manifest supplies the materialized config
// path. A real env value ALWAYS wins (operator override / break-glass), and the
// manifest only ever holds the specific activation keys — no other env read is
// affected. The manifest is loaded once by boot() (initActivationManifest);
// unloaded (every CLI path except serve) the overlay is a no-op.
func osGetenv(k string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return activationManifestLookup(k)
}

// evalsJudgeOptions / knowledgeEmbedderOptions return the WithJudge / WithEmbedder
// options to apply, or nil when the seam is unconfigured (so buildModules keeps the
// fail-closed default).
func (ci claudeInference) evalsJudgeOptions() []evals.Option {
	if ci.judge == nil {
		return nil
	}
	// The SAME adapter backs both judge seams (one credential, one gateway posture):
	// pointwise scoring for llm_judge/calibration and the ordered pairwise verdict
	// the bias-mitigated A/B order-swaps.
	return []evals.Option{evals.WithJudge(ci.judge), evals.WithPairJudge(ci.judge)}
}

// recordingOptions returns the WithSummarizer option for the recording
// module, or nil when no inference credential is configured (honest 501).
func (ci claudeInference) recordingOptions() []recording.Option {
	if ci.recSummarizer == nil {
		return nil
	}
	return []recording.Option{recording.WithSummarizer(ci.recSummarizer)}
}
