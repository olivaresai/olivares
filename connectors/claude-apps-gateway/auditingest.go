// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeappsgateway

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

const signalClaudeAppsGateway model.SignalSource = "claude-apps-gateway"

var metricEventTypes = map[string]struct{}{
	"config.load":      {},
	"session.refresh":  {},
	"device.authorize": {},
	"device.verify":    {},
	"managed.serve":    {},
}

func (s *Source) ingestAuditEvents(ctx context.Context, sink sdk.Sink, ref string, at time.Time) error {
	f, err := os.Open(s.cfg.auditLogPath) //nolint:gosec // operator-supplied audit export path
	if err != nil {
		return fmt.Errorf("claude-apps-gateway: open audit_log_path: %w", err)
	}
	defer func() { _ = f.Close() }()

	counts := map[string]int64{}
	unparsed := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var evt map[string]any
		if err := json.Unmarshal([]byte(line), &evt); err != nil {
			unparsed++
			continue
		}
		kind := eventString(evt, "evt")
		switch kind {
		case "auth.denied", "access.denied", "admin.denied":
			if err := sink.Emit(ctx, auditFinding(kind, model.SeverityLow, ref, line, evt, at)); err != nil {
				return err
			}
		case "spend.blocked":
			if err := sink.Emit(ctx, auditFinding(kind, model.SeverityMedium, ref, line, evt, at)); err != nil {
				return err
			}
		case "inference":
			sub := eventString(evt, "sub")
			upstream := eventString(evt, "upstream")
			if sub != "" && upstream != "" {
				if err := sink.Emit(ctx, model.EdgeObservation{
					OriginKind:   "gateway.principal",
					OriginRef:    sub,
					ResourceKind: "inference.upstream",
					ResourceRef:  upstream,
					Mode:         model.ModeUnknown,
					Source:       signalClaudeAppsGateway,
					Confidence:   model.ConfidenceAttributed,
					ObservedAt:   at,
				}); err != nil {
					return err
				}
			}
		case "session.mint":
			sub := eventString(evt, "sub")
			if sub != "" {
				if err := sink.Emit(ctx, model.EdgeObservation{
					OriginKind:   "gateway.principal",
					OriginRef:    sub,
					ResourceKind: "gateway",
					ResourceRef:  ref,
					Mode:         model.ModeUnknown,
					Source:       signalClaudeAppsGateway,
					Confidence:   model.ConfidenceAttributed,
					ObservedAt:   at,
				}); err != nil {
					return err
				}
			}
		default:
			if _, ok := metricEventTypes[kind]; ok {
				counts[kind]++
			} else {
				counts["unknown"]++
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("claude-apps-gateway: read audit_log_path: %w", err)
	}
	if unparsed > 0 {
		title := fmt.Sprintf("gateway audit unparsed lines: %d", unparsed)
		if err := sink.Emit(ctx, finding("gateway_audit_unparsed", model.SeverityLow, ref, title, title, at)); err != nil {
			return err
		}
	}
	for evt, count := range counts {
		if count == 0 {
			continue
		}
		if err := sink.Emit(ctx, model.MetricSample{
			Name:        "claude_apps_gateway.audit_events.count",
			Value:       count,
			Additive:    true,
			Unit:        "1",
			SubjectKind: "gateway",
			SubjectRef:  ref,
			OccurredAt:  at,
			Dimensions:  map[string]string{"evt": evt},
		}); err != nil {
			return err
		}
	}
	return nil
}

func auditFinding(evt string, sev model.Severity, ref, raw string, fields map[string]any, at time.Time) model.FindingReport {
	title := evt
	if reason := scrubReason(eventString(fields, "reason")); reason != "" {
		title += ": " + reason
	}
	return model.FindingReport{
		Kind:        "gateway_evt_" + strings.ReplaceAll(evt, ".", "_"),
		Severity:    sev,
		SubjectKind: "gateway",
		SubjectRef:  ref,
		Title:       title,
		DetailHash:  hashString(raw),
		OccurredAt:  at,
	}
}

func eventString(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok {
		return ""
	}
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	default:
		return strings.TrimSpace(fmt.Sprint(t))
	}
}

func scrubReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return ""
	}
	if strings.Contains(reason, "@") {
		return "redacted_reason"
	}
	if len(reason) > 120 {
		return reason[:120]
	}
	return reason
}
