// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package bedrock

import (
	"context"
	"net/http"
	"time"

	"github.com/olivaresai/olivares/sdk"
)

// maxResponseBytes caps how much of any AWS response we read into memory. It is a
// defensive bound against a pathological or hostile endpoint, not a functional limit.
const maxResponseBytes = 16 << 20 // 16 MiB

// Source is the AWS Bedrock SourceConnector. It is a batch source: each Gather runs one
// read pass over the enabled sources (S3-delivered usage logs, CloudWatch usage logs,
// Cost Explorer billed cost, Guardrails posture) and returns; the engine owns
// re-scheduling, so the connector holds no ticker (per the SDK contract). It keeps no
// state between passes beyond its resolved config and a shared HTTP client.
type Source struct {
	cfg    config
	client *http.Client
}

// Compile-time proof that Source satisfies the contract.
var _ sdk.SourceConnector = (*Source)(nil)

// New returns an AWS Bedrock connector; configuration is supplied in Open.
func New() *Source { return &Source{} }

// Descriptor returns the connector's stable self-description.
func (s *Source) Descriptor() sdk.Descriptor { return descriptor() }

// Open resolves and validates configuration, reads the secret credentials into memory,
// and builds the shared HTTP client. A configuration error (missing credentials when a
// signed source is enabled, unparsable settings) surfaces here, before Gather, per the
// SDK contract.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	c, err := loadConfig(cfg)
	if err != nil {
		return err
	}
	s.cfg = c
	s.client = &http.Client{Timeout: c.timeout}
	return nil
}

// Gather runs one read pass over the enabled sources. A disabled source is skipped
// silently (not configured ⇒ no finding). An enabled source that fails yields exactly
// one health finding (a gap is a signal, not silence) and the pass continues with the
// next source. ctx is honored: it is checked before each source and inside every
// list/page loop, and a cancellation returns ctx.Err() promptly. Every observation is
// stamped from a single per-pass UTC timestamp.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	at := time.Now().UTC()

	// 1. Token usage from S3-delivered model-invocation-log files (local I/O, no creds).
	if s.cfg.usageLogPath != "" {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.gatherUsageFiles(ctx, sink, at); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if e := emit(ctx, sink, healthFinding(subjectUsage, s.cfg.usageLogPath,
				"Bedrock model-invocation-log file read failed", err, at)); e != nil {
				return e
			}
		}
	}

	// 2. Token usage from CloudWatch Logs (FilterLogEvents).
	if s.cfg.usageLogGroup != "" {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.gatherUsageCloudWatch(ctx, sink, at); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if e := emit(ctx, sink, healthFinding(subjectUsage, s.cfg.usageLogGroup,
				"Bedrock CloudWatch model-invocation-log read failed", err, at)); e != nil {
				return e
			}
		}
	}

	// 3. Billed cost from Cost Explorer.
	if s.cfg.enableCost {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.gatherCost(ctx, sink, at); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if e := emit(ctx, sink, healthFinding(subjectCost, s.cfg.accountScope(),
				"Bedrock Cost Explorer read failed", err, at)); e != nil {
				return e
			}
		}
	}

	// 4. Guardrails safety posture.
	if s.cfg.enableGuardrails {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.gatherGuardrails(ctx, sink, at); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if e := emit(ctx, sink, healthFinding(subjectGuardrails, s.cfg.accountScope(),
				"Bedrock Guardrails read failed", err, at)); e != nil {
				return e
			}
		}
	}
	return nil
}

// Close releases the connector's resources. It holds no long-lived resources between
// passes; it is safe to call even if Open failed.
func (s *Source) Close(context.Context) error { return nil }

// httpClient returns the connector's HTTP client, falling back to a default when Open
// did not set one (defensive; Open always sets it on success).
func (s *Source) httpClient() *http.Client {
	if s.client != nil {
		return s.client
	}
	return &http.Client{Timeout: defaultTimeout}
}
