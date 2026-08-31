// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeappsgateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

const maxGatewayYAMLBytes = 1 << 20

// Source is a batch source for Claude apps gateway posture, inventory, and audit ingest.
type Source struct {
	cfg config
	now func() time.Time
}

var _ sdk.SourceConnector = (*Source)(nil)

// New returns a Claude apps gateway connector with default configuration.
func New() *Source {
	return &Source{now: time.Now}
}

// Descriptor returns the connector's stable self-description.
func (s *Source) Descriptor() sdk.Descriptor { return descriptor() }

// Open validates connector configuration. It performs no filesystem or network I/O.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	c, err := loadConfig(cfg)
	if err != nil {
		return fmt.Errorf("claude-apps-gateway: %w", err)
	}
	s.cfg = c
	if s.now == nil {
		s.now = time.Now
	}
	return nil
}

// Gather performs one configured pass: gateway.yaml inventory/posture, optional live
// probe, and optional audit-event JSONL ingest.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	at := s.clock().UTC()
	var gw *gatewayYAML

	if s.cfg.configPath != "" {
		data, err := readCapped(s.cfg.configPath, maxGatewayYAMLBytes)
		if err != nil {
			if emitErr := sink.Emit(ctx, configUnreadableFinding(s.cfg.configPath, err, at)); emitErr != nil {
				return emitErr
			}
		} else {
			parsed, parseErr := parseGatewayYAML(data)
			if parseErr != nil {
				if emitErr := sink.Emit(ctx, configUnreadableFinding(s.cfg.configPath, parseErr, at)); emitErr != nil {
					return emitErr
				}
			} else {
				gw = &parsed
				ref := gatewayRef(gw, s.cfg)
				for _, obs := range inventoryEdges(*gw, ref, at) {
					if err := sink.Emit(ctx, obs); err != nil {
						return err
					}
				}
			}
		}
	}

	ref := gatewayRef(gw, s.cfg)
	probeVersion := ""
	if s.cfg.endpoint != "" {
		v, err := s.probe(ctx, sink, gw, ref, at)
		if err != nil {
			return err
		}
		probeVersion = v
	}

	if gw != nil {
		findings, err := postureFindings(*gw, s.cfg, ref, probeVersion, at)
		if err != nil {
			return err
		}
		for _, f := range findings {
			if err := sink.Emit(ctx, f); err != nil {
				return err
			}
		}
	}

	if s.cfg.auditLogPath != "" {
		if err := s.ingestAuditEvents(ctx, sink, ref, at); err != nil {
			return err
		}
	}
	return nil
}

// Close releases resources. The connector holds no long-lived handles.
func (s *Source) Close(context.Context) error { return nil }

func (s *Source) clock() time.Time {
	if s.now == nil {
		return time.Now()
	}
	return s.now()
}

func readCapped(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // operator-supplied inventory path
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, limit))
}

func hashString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func finding(kind string, sev model.Severity, ref, title, detail string, at time.Time) model.FindingReport {
	if detail == "" {
		detail = kind + "|" + ref + "|" + title
	}
	return model.FindingReport{
		Kind:        kind,
		Severity:    sev,
		SubjectKind: "gateway",
		SubjectRef:  ref,
		Title:       title,
		DetailHash:  hashString(detail),
		OccurredAt:  at,
	}
}

func configUnreadableFinding(path string, err error, at time.Time) model.FindingReport {
	return finding(
		"gateway_config_unreadable",
		model.SeverityMedium,
		path,
		"gateway.yaml unreadable",
		"path="+path+"|err="+err.Error(),
		at,
	)
}

func gatewayRef(gw *gatewayYAML, cfg config) string {
	if gw != nil && gw.Listen.PublicURL != "" {
		return gw.Listen.PublicURL
	}
	if cfg.configPath != "" {
		return cfg.configPath
	}
	if cfg.endpoint != "" {
		return cfg.endpoint
	}
	if cfg.auditLogPath != "" {
		return cfg.auditLogPath
	}
	return "claude-apps-gateway"
}
