// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cfaigateway

import (
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

// usageLog is the TOLERANT subset of one Cloudflare AI Gateway log entry this
// connector reads. Deliberately ABSENT: any request body, response body, prompt,
// completion, or header value — only structural usage metadata is read (docs/SECURITY-HARDENING.md
// §3); a free-text field added to the struct could leak, so none is.
//
// The shape is verified against the Cloudflare AI Gateway REST API documentation
// (GET /accounts/{id}/ai-gateway/gateways/{gw}/logs). The connector tolerates
// version drift by treating every field as optional and skipping a log entry
// that yields no usable usage.
type usageLog struct {
	ID        string `json:"id"`
	Model     string `json:"model"`
	Provider  string `json:"provider"`
	Status    int    `json:"status_code"`
	Duration  int64  `json:"duration"`
	Cost      *int64 `json:"cost"`
	Tokens    *int64 `json:"tokens_in"`
	TokensOut *int64 `json:"tokens_out"`
	// Timestamp fields — accept the documented spellings.
	CreatedAt string `json:"created_at"`
	Timestamp string `json:"timestamp"`
	// Custom metadata the operator attached via cf-aig-metadata header.
	Metadata map[string]string `json:"metadata"`
	// Gateway ref for multi-gateway passes.
	GatewayID string `json:"-"`
}

func (l usageLog) modelRef() string    { return strings.TrimSpace(l.Model) }
func (l usageLog) providerRef() string { return strings.TrimSpace(l.Provider) }

func (l usageLog) inputTokens() int64 {
	if l.Tokens != nil && *l.Tokens >= 0 {
		return *l.Tokens
	}
	return 0
}

func (l usageLog) outputTokens() int64 {
	if l.TokensOut != nil && *l.TokensOut >= 0 {
		return *l.TokensOut
	}
	return 0
}

func (l usageLog) hasUsage() bool {
	if l.inputTokens() > 0 || l.outputTokens() > 0 {
		return true
	}
	if l.Cost != nil && *l.Cost > 0 {
		return true
	}
	return false
}

func (l usageLog) costMicroUSD() (int64, bool) {
	if l.Cost != nil && *l.Cost >= 0 {
		return *l.Cost, true
	}
	return 0, false
}

var timeLayouts = []string{time.RFC3339Nano, time.RFC3339}

func (l usageLog) occurredAt() (time.Time, bool) {
	for _, s := range []string{l.CreatedAt, l.Timestamp} {
		if t, ok := parseTime(s); ok {
			return t, true
		}
	}
	return time.Time{}, false
}

func parseTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range timeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		if len(s) >= 13 {
			return time.UnixMilli(n).UTC(), true
		}
		return time.Unix(n, 0).UTC(), true
	}
	return time.Time{}, false
}

// buildSample maps one usageLog to a CostSample. Returns ok=false for a log
// that names no model or carries no usable token count / cost.
func buildSample(log usageLog, metadataKeys []string, now time.Time) (model.CostSample, bool) {
	m := log.modelRef()
	if m == "" || !log.hasUsage() {
		return model.CostSample{}, false
	}
	occurred, ok := log.occurredAt()
	if !ok {
		occurred = now
	}
	sample := model.CostSample{
		ProviderRef:  log.providerRef(),
		ModelRef:     m,
		InputTokens:  log.inputTokens(),
		OutputTokens: log.outputTokens(),
		OccurredAt:   occurred,
		Gateway:      GatewayCFAIGateway,
		Provenance:   model.ProvenanceEstimated,
	}
	if micro, ok := log.costMicroUSD(); ok {
		sample.CostMicroUSD = micro
		sample.Provenance = model.ProvenanceBilled
	}
	if log.Metadata != nil && len(metadataKeys) > 0 {
		extractAttribution(&sample, log.Metadata, metadataKeys)
	}
	return sample, true
}

// extractAttribution reads well-known metadata keys and maps them to CostSample
// attribution fields. Remaining extracted keys become Labels.
func extractAttribution(s *model.CostSample, meta map[string]string, keys []string) {
	for _, k := range keys {
		v, ok := meta[k]
		if !ok || v == "" {
			continue
		}
		switch k {
		case "workspace":
			s.WorkspaceRef = v
		case "user":
			s.Actor = v
		case "cost_center":
			if s.Labels == nil {
				s.Labels = make(map[string]string)
			}
			s.Labels["cost_center"] = v
		default:
			if s.Labels == nil {
				s.Labels = make(map[string]string)
			}
			s.Labels[k] = v
		}
	}
}
