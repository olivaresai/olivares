// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeappsgateway

import (
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

func inventoryEdges(gw gatewayYAML, ref string, at time.Time) []model.EdgeObservation {
	var out []model.EdgeObservation
	if issuer := strings.TrimSpace(gw.OIDC.Issuer); issuer != "" {
		out = append(out, configEdge(ref, "idp.issuer", issuer, nil, at))
	}
	for _, u := range gw.Upstreams {
		ures := strings.TrimSpace(u.Provider)
		if ures == "" {
			ures = strings.TrimSpace(u.Name)
		}
		if ures == "" {
			continue
		}
		var labels map[string]string
		if strings.TrimSpace(u.Region) != "" {
			labels = map[string]string{"region": strings.TrimSpace(u.Region)}
		}
		out = append(out, configEdge(ref, "inference.upstream", ures, labels, at))
	}
	for _, dst := range gw.Telemetry.ForwardTo {
		host := urlHost(dst.URL)
		if host == "" {
			continue
		}
		out = append(out, configEdge(ref, "otlp.destination", host, nil, at))
	}
	for _, p := range policies(gw) {
		for _, origin := range policyOrigins(p) {
			for _, modelID := range availableModels(p.effectiveCLI()) {
				out = append(out, model.EdgeObservation{
					OriginKind:   "idp.group",
					OriginRef:    origin,
					ResourceKind: "model",
					ResourceRef:  modelID,
					Mode:         model.ModeUnknown,
					Source:       model.SignalPolicy,
					Confidence:   model.ConfidenceAttributed,
					ObservedAt:   at,
				})
			}
		}
	}
	return out
}

func configEdge(originRef, resourceKind, resourceRef string, labels map[string]string, at time.Time) model.EdgeObservation {
	return model.EdgeObservation{
		OriginKind:   "gateway",
		OriginRef:    originRef,
		ResourceKind: resourceKind,
		ResourceRef:  resourceRef,
		Mode:         model.ModeUnknown,
		Source:       model.SignalConfig,
		Confidence:   model.ConfidenceAttributed,
		ObservedAt:   at,
		Labels:       labels,
	}
}

func policies(gw gatewayYAML) []managedPolicy {
	if gw.Managed == nil {
		return nil
	}
	return gw.Managed.Policies
}

func policyOrigins(p managedPolicy) []string {
	switch {
	case len(p.Match.Groups) > 0:
		out := append([]string(nil), p.Match.Groups...)
		sort.Strings(out)
		return out
	case strings.TrimSpace(p.Match.EmailDomain) != "":
		return []string{"email-domain:" + strings.TrimSpace(p.Match.EmailDomain)}
	default:
		return []string{"*"}
	}
}

func availableModels(doc map[string]any) []string {
	if len(doc) == 0 {
		return nil
	}
	raw, ok := doc["availableModels"]
	if !ok {
		return nil
	}
	var out []string
	switch v := raw.(type) {
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
	case []string:
		for _, s := range v {
			if strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
	case string:
		if strings.TrimSpace(v) != "" {
			out = append(out, strings.TrimSpace(v))
		}
	}
	sort.Strings(out)
	return out
}

func urlHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if u.Host != "" {
		return u.Host
	}
	return u.Path
}
