// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cfaigateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
)

// Source is the Cloudflare AI Gateway cost connector. It polls the CF AI Gateway
// REST API for per-request logs and emits one model.CostSample per log entry.
type Source struct {
	cfg config
	cl  *client
	now func() time.Time
}

var _ sdk.SourceConnector = (*Source)(nil)

func New() *Source { return &Source{} }

func (s *Source) Descriptor() sdk.Descriptor { return descriptor() }

func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	c, err := loadConfig(cfg)
	if err != nil {
		return err
	}
	s.cfg = c
	s.cl = newClient(c.apiBase, c.apiToken, &http.Client{Timeout: c.timeout})
	return nil
}

func (s *Source) Close(context.Context) error { return nil }

// Gather runs one ingest pass: list gateways (or use the configured one),
// then for each gateway page through the logs and emit one CostSample per
// log entry. A gateway whose logs cannot be listed yields one health finding
// and the pass continues with the next gateway.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	at := s.clock()

	gateways, err := s.resolveGateways(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return sink.Emit(ctx, healthFinding(originAccount, s.cfg.accountID, "Cloudflare AI Gateway list failed", err, at))
	}

	for _, gw := range gateways {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.gatherLogs(ctx, sink, gw, at); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// gatewayRef is a minimal gateway description from the list response.
type gatewayRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (s *Source) resolveGateways(ctx context.Context) ([]gatewayRef, error) {
	if !s.cfg.allGateways() {
		return []gatewayRef{{ID: s.cfg.gatewayID, Name: s.cfg.gatewayID}}, nil
	}
	path := "/accounts/" + s.cfg.accountID + "/ai-gateway/gateways"
	raw, err := s.cl.get(ctx, path, nil)
	if err != nil {
		return nil, err
	}
	gateways := make([]gatewayRef, 0, len(raw))
	for _, item := range raw {
		var gw gatewayRef
		if err := json.Unmarshal(item, &gw); err != nil {
			return nil, fmt.Errorf("cfaigateway: decode gateway: %w", err)
		}
		if gw.ID != "" {
			gateways = append(gateways, gw)
		}
	}
	return gateways, nil
}

func (s *Source) gatherLogs(ctx context.Context, sink sdk.Sink, gw gatewayRef, at time.Time) error {
	path := "/accounts/" + s.cfg.accountID + "/ai-gateway/gateways/" + gw.ID + "/logs"
	raw, err := s.cl.get(ctx, path, nil)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return sink.Emit(ctx, healthFinding(resGateway, redact.Clean(gw.ID), "Cloudflare AI Gateway logs list failed", err, at))
	}
	for _, item := range raw {
		if err := ctx.Err(); err != nil {
			return err
		}
		var log usageLog
		if err := json.Unmarshal(item, &log); err != nil {
			continue
		}
		log.GatewayID = gw.ID
		sample, ok := buildSample(log, s.cfg.metadataKeys, at)
		if !ok {
			continue
		}
		if err := sink.Emit(ctx, sample); err != nil {
			return err
		}
	}
	return nil
}

func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now().UTC()
}
