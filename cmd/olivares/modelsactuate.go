// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/modules/models"
	"github.com/olivaresai/olivares/sdk"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// This file is the composition-root glue for module X's governed routing EXECUTION
//: the deny-closed models.Executor backed by the Claude inference client, and
// the read-only rate-limit inventory provider backed by the Claude Admin connector.
// Both live HERE (cmd, AGPL) because they bridge the AGPL module ports to the Apache
// connectors and hold operator credentials — a connector never embeds the wiring, and
// a module never imports a provider client (ARCHITECTURE.md,§4; LICENSING.md). Both stay
// FAIL-CLOSED unless explicitly configured: with no inference credential /execute is
// deny-closed (503), and with no Admin credential the rate-limit route degrades to an
// empty inventory with a reason — never a fabricated value.

// modelsExecutor implements models.Executor by invoking the resolved target through
// the Claude inference client — direct against Anthropic, or through the resolved
// gateway endpoint as the base URL when the decision routes ViaGateway — and then
// publishing the runtime cost/forensic (CLA-15/ANT2-15) through the SAME in-process
// liveingest sink the judge adapter uses. It tries the resolved chain in order, so a
// transport failure on the primary falls back to the next target (a model refusal is a
// terminal model decision, not a failure: it is returned, with no cost line emitted).
type modelsExecutor struct {
	cfg  claudeapi.InferenceConfig // base inference config (credential, version, surface)
	doer modelprovider.Doer        // trace-instrumented transport (OBS-03)
	sink runtimeObservationSink    // the in-process cost/forensic publisher (liveingest)
	log  *slog.Logger
}

// Compile-time proof the adapter satisfies the module's actuation seam.
var _ models.Executor = (*modelsExecutor)(nil)

func (e *modelsExecutor) Execute(ctx context.Context, req models.ExecuteRequest) (models.ExecuteResult, error) {
	var lastErr error
	for i, target := range req.Chain {
		cfg := e.cfg
		cfg.Doer = e.doer
		cfg.DefaultModel = target.ModelRef
		if target.ViaGateway && target.Endpoint != "" {
			// Route through the resolved external gateway (LiteLLM/OpenRouter-style): the
			// gateway endpoint is the Messages base URL; the operator credential proxies.
			cfg.BaseURL = target.Endpoint
		}
		inf := claudeapi.NewInference(cfg)
		resp, err := inf.CreateMessage(ctx, claudeapi.MessageRequest{
			Model:     target.ModelRef,
			MaxTokens: req.MaxTokens,
			Messages: []claudeapi.Message{{
				Role:    "user",
				Content: []claudeapi.ContentBlock{claudeapi.TextBlock(req.Input)},
			}},
		})
		if err != nil {
			lastErr = err
			if e.log != nil {
				e.log.Warn("models-exec: target failed; trying next in the routing chain", "model", target.ModelRef, "via_gateway", target.ViaGateway, "err", err)
			}
			continue
		}
		// Emit the runtime cost/forensic of the served response (fail-open): the
		// top-level + advisor cost lines and the thinking/advisor/refusal findings. A
		// refusal yields the security finding and NO cost line (RuntimeObservations).
		e.emit(ctx, inf, req, resp)
		return models.ExecuteResult{
			Text:         resp.Text(),
			Served:       target,
			FallbackUsed: i > 0,
			InputTokens:  resp.Usage.InputTokens,
			OutputTokens: resp.Usage.OutputTokens,
			Refusal:      resp.IsRefusal(),
		}, nil
	}
	if lastErr == nil {
		lastErr = errors.New("no target in the routing chain could be executed")
	}
	return models.ExecuteResult{}, lastErr
}

// emit publishes the served response's cost/forensic through the in-process sink.
func (e *modelsExecutor) emit(ctx context.Context, inf *claudeapi.Inference, req models.ExecuteRequest, resp claudeapi.MessageResponse) {
	if e.sink == nil {
		return
	}
	samples, findings := inf.RuntimeObservations(resp, req.SessionRef, time.Time{}, false)
	for _, cs := range samples {
		if err := e.sink.PublishCostSample(ctx, req.Tenant.String(), cs); err != nil && e.log != nil {
			e.log.Debug("models-exec: cost publish failed (fail-open)", "err", err)
		}
	}
	for _, f := range findings {
		if err := e.sink.PublishFinding(ctx, req.Tenant.String(), f); err != nil && e.log != nil {
			e.log.Debug("models-exec: forensic publish failed (fail-open)", "err", err)
		}
	}
}

// newModelsExecutor builds the governed routing executor from env (the SAME inference
// credential the judge uses, resolveInference). It returns nil — keeping the module's
// deny-closed unwiredExecutor (so /execute returns 503) — when no inference credential
// is configured, or when a non-direct gateway is selected without its base URL (the
// same honesty guard as the judge: never stamp a call with the wrong surface).
func newModelsExecutor(getenv func(string) string, doer modelprovider.Doer, sink runtimeObservationSink, log *slog.Logger) *modelsExecutor {
	cfg, ok := resolveInference(getenv)
	if !ok {
		return nil
	}
	if cfg.Gateway != sdkmodel.GatewayDirect && cfg.BaseURL == "" {
		log.Warn("models: non-direct Claude gateway selected without its base URL; routing execution stays deny-closed (set the matching ANTHROPIC_*_BASE_URL)", "gateway", string(cfg.Gateway))
		return nil
	}
	log.Info("models: wired governed routing executor (Messages API; deny-closed seam now active)", "gateway", string(cfg.Gateway), "default_model", cfg.DefaultModel)
	return &modelsExecutor{cfg: cfg, doer: doer, sink: sink, log: log}
}

// claudeRateLimitProvider adapts the Claude Admin connector's read-only rate-limit
// inventory to the module's RateLimitProvider seam (ANT2-05). Read-only by construction
// — the underlying connector exposes no mutation.
type claudeRateLimitProvider struct{ src *claudeapi.Source }

// Compile-time proof the adapter satisfies the module's read seam.
var _ models.RateLimitProvider = claudeRateLimitProvider{}

func (p claudeRateLimitProvider) RateLimits(ctx context.Context) ([]modelprovider.RateLimitRef, error) {
	return p.src.RateLimits(ctx)
}

// newModelsRateLimitProvider builds the read-only rate-limit inventory provider from a
// dedicated read-only Admin credential (OLIVARES_CLAUDE_ADMIN_KEY, optionally
// ANTHROPIC_BASE_URL / OLIVARES_CLAUDE_WORKSPACE_ID). It returns nil — so GET
// /rate-limits degrades to an empty inventory with a reason — when no Admin credential
// is configured. The credential lives in the operator's environment, never in the
// store, and the connector is read-only (it cannot mutate a limit).
func newModelsRateLimitProvider(getenv func(string) string, log *slog.Logger) models.RateLimitProvider {
	// the settings come from claudeAdminSettings (identityposturewiring.go) so
	// this inventory and the identity posture can never drift onto different env vars.
	settings, ok := claudeAdminSettings(getenv)
	if !ok {
		return nil
	}
	src := claudeapi.New()
	if err := src.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		// Never log the error (it can embed the endpoint/credential): a configuration
		// fault leaves the route degraded-with-reason, not wired.
		log.Warn("models: could not open the Claude Admin connector for the rate-limit inventory; GET /rate-limits stays empty-with-reason")
		return nil
	}
	log.Info("models: wired read-only Claude rate-limit inventory provider (ANT2-05)")
	return claudeRateLimitProvider{src: src}
}
