// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// This file turns Azure Monitor metrics into per-deployment token-usage CostSamples — the
// REAL Azure token surface (the OpenAI-org /v1/organization/usage path does NOT exist on
// Azure). It reads ProcessedPromptTokens (input) and GeneratedTokens (output) on the
// Cognitive Services account resource, split per deployment by the ModelDeploymentName
// dimension, and derives cost from the declared list pricing keyed by the deployment's
// UNDERLYING model (resolved from the deployment list).
//
// MINIMAL DATA (docs/SECURITY-HARDENING.md): only token COUNTS and the deployment/account refs are read —
// never a prompt or completion. Cost is DERIVED (Provenance=estimated): the metrics carry
// counts, not money (billed money comes only from Cost Management, cost.go). The two
// streams never double-count: usage has derived cost, billed has no tokens.
//
// Honesty: Azure Monitor metric REST names are NOT the portal display names
// ("Processed Prompt Tokens"); the verified REST names are used. Dimension names come back
// lowercased/normalized, so ModelDeploymentName is matched case-insensitively.
package azureopenai

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Verified Azure Monitor REST metric names (NOT the portal display names) and the
// Cognitive Services metric namespace + per-deployment dimension.
const (
	metricInputTokens    = "ProcessedPromptTokens" // input tokens
	metricOutputTokens   = "GeneratedTokens"       // output tokens
	metricNamespace      = "Microsoft.CognitiveServices/accounts"
	dimModelDeployment   = "modeldeploymentname" // lower-cased, as the API returns it
	metricDeploymentGlob = "ModelDeploymentName eq '*'"
)

// usageKey identifies one per-(deployment, bucket) accumulation across the input and
// output metrics within one account.
type usageKey struct {
	deployment string
	bucket     int64 // unix seconds
}

type inOut struct {
	in  int64
	out int64
}

// gatherUsage reads Azure Monitor token metrics for every LLM-hosting account across the
// subscription set and emits one CostSample per (deployment, bucket) with derived cost.
func (s *Source) gatherUsage(ctx context.Context, sink sdk.Sink, subs []string, at time.Time) error {
	for _, sub := range subs {
		if err := ctx.Err(); err != nil {
			return err
		}
		accounts, err := s.listAccounts(ctx, sub)
		if err != nil {
			return err
		}
		for _, a := range accounts {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := s.gatherAccountUsage(ctx, sink, a, at); err != nil {
				return err
			}
		}
	}
	return nil
}

// gatherAccountUsage reads one account's per-deployment token metrics and emits the derived
// CostSamples. It first lists the account's deployments to map each deployment name to its
// underlying model (for pricing); a deployment with no known model derives cost 0 (real
// tokens, underived cost) rather than guessing.
func (s *Source) gatherAccountUsage(ctx context.Context, sink sdk.Sink, a account, at time.Time) error {
	deployments, err := s.listDeployments(ctx, a.ID)
	if err != nil {
		return err
	}
	depToModel := make(map[string]string, len(deployments))
	for _, d := range deployments {
		depToModel[d.Name] = d.Properties.Model.Name
	}

	resp, err := s.readMetrics(ctx, a.ID, at)
	if err != nil {
		return err
	}
	acc := map[usageKey]*inOut{}
	keys := make([]usageKey, 0, 16)
	accumulateMetrics(resp, acc, &keys)

	for _, k := range keys {
		if err := ctx.Err(); err != nil {
			return err
		}
		io := acc[k]
		if io.in <= 0 && io.out <= 0 {
			continue
		}
		if err := emit(ctx, sink, s.usageSample(a, k, io, depToModel[k.deployment], at)); err != nil {
			return err
		}
	}
	return nil
}

// readMetrics issues the Azure Monitor metrics GET for the input+output token metrics over
// the lookback window, split per deployment.
func (s *Source) readMetrics(ctx context.Context, accountID string, at time.Time) (metricsResponse, error) {
	start := at.Add(-s.cfg.usageLookback)
	q := url.Values{
		"api-version":         {s.cfg.metricsAPIVersion},
		"metricnames":         {metricInputTokens + "," + metricOutputTokens},
		"aggregation":         {"Total"},
		"interval":            {s.cfg.metricsInterval},
		"timespan":            {start.Format(time.RFC3339) + "/" + at.Format(time.RFC3339)},
		"metricnamespace":     {metricNamespace},
		"$filter":             {metricDeploymentGlob},
		"AutoAdjustTimegrain": {"true"},
	}
	var resp metricsResponse
	if err := s.getURL(ctx, s.armURL(accountID+"/providers/Microsoft.Insights/metrics", q), &resp); err != nil {
		return metricsResponse{}, err
	}
	return resp, nil
}

// accumulateMetrics folds the metrics response into acc (per deployment+bucket), recording
// first-seen keys in order. The metric name decides input vs output; the deployment is the
// ModelDeploymentName dimension value; the bucket is the datapoint timeStamp.
func accumulateMetrics(resp metricsResponse, acc map[usageKey]*inOut, keys *[]usageKey) {
	for _, m := range resp.Value {
		isOutput := strings.EqualFold(m.Name.Value, metricOutputTokens)
		for _, ts := range m.Timeseries {
			dep := deploymentDimension(ts.Metadatavalues)
			if dep == "" {
				continue // no deployment attribution: skip (never fold into a fabricated ref)
			}
			for _, d := range ts.Data {
				if d.Total == nil {
					continue // aggregation absent for this bucket: never assume zero
				}
				v := int64(*d.Total + 0.5) // token counts are exact integers; round defensively
				if v == 0 {
					continue
				}
				k := usageKey{deployment: dep, bucket: bucketUnix(d.TimeStamp)}
				cur, seen := acc[k]
				if !seen {
					cur = &inOut{}
					acc[k] = cur
					*keys = append(*keys, k)
				}
				if isOutput {
					cur.out += v
				} else {
					cur.in += v
				}
			}
		}
	}
}

// deploymentDimension extracts the ModelDeploymentName dimension value (matched
// case-insensitively, as Azure returns the dimension name lowercased).
func deploymentDimension(meta []metricMetadataValue) string {
	for _, mv := range meta {
		if strings.EqualFold(strings.TrimSpace(mv.Name.Value), dimModelDeployment) {
			return strings.TrimSpace(mv.Value)
		}
	}
	return ""
}

// usageSample builds the derived-cost CostSample for one accumulated bucket. ModelRef is the
// DEPLOYMENT name (matching the catalog and the Azure Monitor dimension); the cost is
// derived from the deployment's underlying model pricing.
func (s *Source) usageSample(a account, k usageKey, io *inOut, modelName string, fallback time.Time) model.CostSample {
	u := modelprovider.Usage{
		ProviderRef:  providerRef,
		ModelRef:     k.deployment,
		InputTokens:  io.in,
		OutputTokens: io.out,
		OccurredAt:   bucketTime(k.bucket, fallback),
		Gateway:      model.GatewayFoundry,
		Provenance:   model.ProvenanceEstimated,
		WorkspaceRef: a.ID,
		InferenceGeo: a.Location,
	}
	if p, ok := pricingFor(modelName); ok {
		return modelprovider.ToCostSample(u, p)
	}
	return modelprovider.ToCostSampleWithCost(u, 0)
}

// bucketUnix parses an Azure metrics timeStamp (RFC3339) to unix seconds; 0 when it does
// not parse (the caller substitutes the pass time).
func bucketUnix(ts string) int64 {
	if t, ok := parseRFC3339(ts); ok {
		return t.Unix()
	}
	return 0
}

// bucketTime turns a bucket unix-seconds value into a UTC time, falling back to the pass
// time when the bucket is unknown (0).
func bucketTime(bucket int64, fallback time.Time) time.Time {
	if bucket == 0 {
		return fallback
	}
	return time.Unix(bucket, 0).UTC()
}

// parseRFC3339 parses an RFC3339(Nano) timestamp to UTC.
func parseRFC3339(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}
