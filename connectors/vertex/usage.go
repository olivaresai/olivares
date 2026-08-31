// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// This file turns Google Cloud Monitoring token-count time series into per-model
// token-usage CostSamples — the surface the gemini (AI Studio) connector honestly lacks
// but Vertex AI genuinely exposes. It reads the DELTA metric
// aiplatform.googleapis.com/publisher/online_serving/token_count on the PublisherModel
// monitored resource, where the input/output split is the metric `type` label and the
// per-model identity (model id, version, publisher, location, project) lives in the
// RESOURCE labels.
//
// MINIMAL DATA (docs/SECURITY-HARDENING.md): only token COUNTS and the model/location refs are read —
// never a prompt or a completion (Cloud Monitoring carries no content). Cost is DERIVED
// from the declared list pricing with Provenance=estimated: the metric reports counts,
// not money (the authoritative billed figure comes only from the BigQuery billing export,
// cost.go). The two streams never double-count: usage has derived cost, billed has no
// tokens.
package vertex

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// tokenCountMetric is the verified Cloud Monitoring metric type for Vertex AI per-model
// token usage. (The plausible-looking `consumed_token_count` does NOT exist — querying it
// returns empty; token_count is the real metric.)
const tokenCountMetric = "aiplatform.googleapis.com/publisher/online_serving/token_count"

// --- Cloud Monitoring v3 timeSeries.list wire shapes (only the fields we read) -------

// timeSeriesResponse is one page of monitoring.timeSeries.list.
type timeSeriesResponse struct {
	TimeSeries    []monTimeSeries `json:"timeSeries"`
	NextPageToken string          `json:"nextPageToken"`
}

// monTimeSeries is one returned series. The input/output split is metric.labels.type; the
// per-model identity is in resource.labels (model_user_id/model_version_id/publisher/
// location/resource_container).
type monTimeSeries struct {
	Metric struct {
		Type   string            `json:"type"`
		Labels map[string]string `json:"labels"`
	} `json:"metric"`
	Resource struct {
		Type   string            `json:"type"`
		Labels map[string]string `json:"labels"`
	} `json:"resource"`
	Points []monPoint `json:"points"`
}

// monPoint is one aligned datapoint. int64Value is a STRING in proto3 JSON, not a number.
type monPoint struct {
	Interval struct {
		StartTime string `json:"startTime"`
		EndTime   string `json:"endTime"`
	} `json:"interval"`
	Value struct {
		Int64Value string `json:"int64Value"`
	} `json:"value"`
}

// usageKey identifies one per-(model, version, publisher, location, bucket) accumulation
// across the separate input and output time series.
type usageKey struct {
	model     string
	version   string
	publisher string
	location  string
	container string
	bucket    int64 // bucket start, unix seconds
}

// inOut accumulates the input and output token counts for one usageKey.
type inOut struct {
	in  int64
	out int64
}

// gatherUsage reads the token_count time series for the lookback window and emits one
// CostSample per (model, location, bucket) with the input/output split, deriving cost
// from the declared family pricing. It paginates by nextPageToken up to max_pages.
func (s *Source) gatherUsage(ctx context.Context, sink sdk.Sink, at time.Time) error {
	start := at.Add(-s.cfg.usageLookback)
	acc := map[usageKey]*inOut{}
	keys := make([]usageKey, 0, 16) // preserve first-seen order for deterministic emission

	base := joinURL(s.cfg.monitoringEndpoint, "/v3/projects/"+url.PathEscape(s.cfg.project)+"/timeSeries", nil)
	token := ""
	for page := 0; page < s.cfg.maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		q := url.Values{}
		q.Set("filter", `metric.type="`+tokenCountMetric+`"`)
		q.Set("interval.startTime", start.Format(time.RFC3339))
		q.Set("interval.endTime", at.Format(time.RFC3339))
		q.Set("aggregation.alignmentPeriod", strconv.FormatInt(int64(s.cfg.usageAlignment.Seconds()), 10)+"s")
		q.Set("aggregation.perSeriesAligner", "ALIGN_SUM") // token_count is DELTA: sum per bucket
		q.Set("view", "FULL")
		if token != "" {
			q.Set("pageToken", token)
		}

		var resp timeSeriesResponse
		if err := s.getURL(ctx, base+"?"+q.Encode(), &resp); err != nil {
			return err
		}
		accumulateSeries(resp.TimeSeries, acc, &keys)
		if resp.NextPageToken == "" {
			break
		}
		token = resp.NextPageToken
	}

	for _, k := range keys {
		if err := ctx.Err(); err != nil {
			return err
		}
		io := acc[k]
		if io.in <= 0 && io.out <= 0 {
			continue
		}
		if err := emit(ctx, sink, s.usageSample(k, io, at)); err != nil {
			return err
		}
	}
	return nil
}

// accumulateSeries folds one page of time series into acc, recording first-seen keys in
// order. The input/output split is metric.labels.type.
func accumulateSeries(series []monTimeSeries, acc map[usageKey]*inOut, keys *[]usageKey) {
	for _, ts := range series {
		kind := strings.ToLower(strings.TrimSpace(ts.Metric.Labels["type"]))
		model := trimPublisherPath(ts.Resource.Labels["model_user_id"])
		if model == "" {
			continue // no model dimension: nothing to attribute (never fabricate a model)
		}
		for _, p := range ts.Points {
			v, ok := parseInt64(p.Value.Int64Value)
			if !ok || v == 0 {
				continue
			}
			k := usageKey{
				model:     model,
				version:   ts.Resource.Labels["model_version_id"],
				publisher: ts.Resource.Labels["publisher"],
				location:  ts.Resource.Labels["location"],
				container: ts.Resource.Labels["resource_container"],
				bucket:    bucketUnix(p),
			}
			cur, seen := acc[k]
			if !seen {
				cur = &inOut{}
				acc[k] = cur
				*keys = append(*keys, k)
			}
			switch kind {
			case "output":
				cur.out += v
			default: // "input" and any unexpected/missing type fold to input
				cur.in += v
			}
		}
	}
}

// usageSample builds the derived-cost CostSample for one accumulated bucket.
func (s *Source) usageSample(k usageKey, io *inOut, fallback time.Time) model.CostSample {
	u := modelprovider.Usage{
		ProviderRef:  providerRef,
		ModelRef:     k.model,
		InputTokens:  io.in,
		OutputTokens: io.out,
		OccurredAt:   bucketTime(k.bucket, fallback),
		Gateway:      model.GatewayVertex,
		Provenance:   model.ProvenanceEstimated, // derived from list pricing, not billed
		InferenceGeo: k.location,
		WorkspaceRef: workspaceRef(k.container, s.cfg.project),
	}
	if p, ok := pricingFor(k.model); ok {
		return modelprovider.ToCostSample(u, p)
	}
	// Unknown price: record real usage with an underived (0) cost rather than guess.
	return modelprovider.ToCostSampleWithCost(u, 0)
}

// workspaceRef returns the project the usage is attributed to: the resource_container
// label (a "projects/123" / project id) if present, else the configured project.
func workspaceRef(container, project string) string {
	if c := strings.TrimSpace(container); c != "" {
		return c
	}
	if project != "" {
		return "projects/" + project
	}
	return ""
}

// trimPublisherPath maps a "publishers/google/models/gemini-2.0-flash" model_user_id to
// the bare ref "gemini-2.0-flash"; a bare id passes through unchanged.
func trimPublisherPath(id string) string {
	id = strings.TrimSpace(id)
	if i := strings.LastIndex(id, "/models/"); i >= 0 {
		return id[i+len("/models/"):]
	}
	return id
}

// parseInt64 parses a proto3-JSON int64 string, reporting ok=false on an empty/odd value
// so a malformed point is skipped rather than counted as zero.
func parseInt64(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// bucketUnix returns the point's bucket start as unix seconds, preferring startTime and
// falling back to endTime; 0 when neither parses (the caller substitutes the pass time).
func bucketUnix(p monPoint) int64 {
	for _, ts := range []string{p.Interval.StartTime, p.Interval.EndTime} {
		if t, ok := parseRFC3339(ts); ok {
			return t.Unix()
		}
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
