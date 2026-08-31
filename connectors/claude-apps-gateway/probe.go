// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeappsgateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

func (s *Source) probe(ctx context.Context, sink sdk.Sink, gw *gatewayYAML, ref string, at time.Time) (string, error) {
	client := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	version := ""
	discoveryURL := s.cfg.endpoint + "/.well-known/oauth-authorization-server"
	resp, err := probeGET(ctx, client, discoveryURL)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			_ = resp.Body.Close()
		}
		if emitErr := sink.Emit(ctx, finding("gateway_probe_unreachable", model.SeverityInfo, ref, "gateway discovery probe unreachable", "discovery", at)); emitErr != nil {
			return "", emitErr
		}
	} else {
		version = firstNonEmpty(normalizeVersion(resp.Header.Get("x-cc-gateway-version")), version)
		var discovery struct {
			Issuer string `json:"issuer"`
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		_ = resp.Body.Close()
		if readErr != nil || json.Unmarshal(body, &discovery) != nil || strings.TrimSpace(discovery.Issuer) == "" {
			if emitErr := sink.Emit(ctx, finding("gateway_probe_unreachable", model.SeverityInfo, ref, "gateway discovery probe unreadable", "discovery-json", at)); emitErr != nil {
				return "", emitErr
			}
		} else if gw != nil && strings.TrimSpace(gw.Listen.PublicURL) != "" && discovery.Issuer != strings.TrimSpace(gw.Listen.PublicURL) {
			if emitErr := sink.Emit(ctx, finding("gateway_issuer_mismatch", model.SeverityMedium, ref, "discovery issuer differs from listen.public_url", "issuer-mismatch", at)); emitErr != nil {
				return "", emitErr
			}
		}
	}

	protocolURL := s.cfg.endpoint + "/protocol"
	protoResp, protoErr := probeGET(ctx, client, protocolURL)
	if protoErr == nil && protoResp.StatusCode == http.StatusOK {
		// Version comes from the documented x-cc-gateway-version header only. The
		// /protocol body is a per-version protocol descriptor whose shape is not
		// stable; scraping a dotted version out of it would conflate a protocol
		// revision with the gateway build and yield spurious version findings.
		version = firstNonEmpty(normalizeVersion(protoResp.Header.Get("x-cc-gateway-version")), version)
		_, _ = io.Copy(io.Discard, io.LimitReader(protoResp.Body, 64*1024))
		_ = protoResp.Body.Close()
		if err := sink.Emit(ctx, model.MetricSample{
			Name:        "claude_apps_gateway.probe.count",
			Value:       1,
			Additive:    true,
			Unit:        "1",
			SubjectKind: "gateway",
			SubjectRef:  ref,
			OccurredAt:  at,
			Dimensions:  map[string]string{"endpoint": "protocol"},
		}); err != nil {
			return "", err
		}
	} else if protoResp != nil {
		_ = protoResp.Body.Close()
	}
	return version, nil
}

func probeGET(ctx context.Context, client *http.Client, u string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	return resp, err
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
