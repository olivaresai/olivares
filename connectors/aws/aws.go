// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"net/http"
	"time"

	"github.com/olivaresai/olivares/sdk"
)

// maxResponseBytes caps how much of any AWS response we read into memory. IAM and
// CloudTrail list pages are small; this is a defensive bound against a pathological
// or hostile endpoint, not a functional limit.
const maxResponseBytes = 16 << 20 // 16 MiB

// cloudTrailPageSize is the LookupEvents page size; AWS caps a page at 50 events.
const cloudTrailPageSize = 50

// Source is the AWS SourceConnector. It is a batch source: each Gather runs one
// discovery pass over the enabled services (IAM then CloudTrail) and returns; the
// engine owns re-scheduling, so the connector holds no ticker (per the SDK
// contract). It keeps no state between passes beyond its resolved config and a
// shared HTTP client.
type Source struct {
	cfg    config
	client *http.Client
}

// Compile-time proof that Source satisfies the contract.
var _ sdk.SourceConnector = (*Source)(nil)

// New returns an AWS connector; configuration is supplied in Open.
func New() *Source { return &Source{} }

// Descriptor returns the connector's stable self-description.
func (s *Source) Descriptor() sdk.Descriptor { return descriptor() }

// Open resolves and validates configuration, reads the secret credentials into
// memory, and builds the shared HTTP client. A configuration error (missing
// credentials when a service is enabled, unparsable settings) surfaces here,
// before Gather, per the SDK contract.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	c, err := loadConfig(cfg)
	if err != nil {
		return err
	}
	s.cfg = c
	s.client = &http.Client{Timeout: c.timeout}
	return nil
}

// Gather runs one discovery pass over the enabled services. A disabled service is
// skipped silently (not present ⇒ no finding). An enabled service that fails to
// list yields exactly one health finding (a gap is a signal, not silence) and the
// pass continues with the next service. ctx is honored: it is checked before each
// service and inside every list/page loop, and a cancellation returns ctx.Err()
// promptly. Every observation is stamped with a single per-pass UTC timestamp.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	at := time.Now().UTC()

	if s.cfg.enableIAM {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.gatherIAM(ctx, sink, at); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if emitErr := sink.Emit(ctx, healthFinding(subjectIAM, s.cfg.originAccountRef(),
				"AWS IAM inventory failed", err, at)); emitErr != nil {
				return emitErr
			}
		}
	}

	if s.cfg.enableCloudTrail {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.gatherCloudTrail(ctx, sink, at); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if emitErr := sink.Emit(ctx, healthFinding(subjectCloudTrail, s.cfg.region,
				"AWS CloudTrail lookup failed", err, at)); emitErr != nil {
				return emitErr
			}
		}
	}

	if s.cfg.enableBedrock {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.gatherBedrock(ctx, sink, at); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if emitErr := sink.Emit(ctx, healthFinding(subjectBedrock, s.cfg.bedrockAccountScope(),
				"AWS Bedrock guardrails read failed", err, at)); emitErr != nil {
				return emitErr
			}
		}
	}
	return nil
}

// Close releases the connector's resources. It holds no long-lived resources
// between passes; it is safe to call even if Open failed.
func (s *Source) Close(context.Context) error { return nil }

// httpClient returns the connector's HTTP client, falling back to a default when
// Open did not set one (defensive; Open always sets it on success).
func (s *Source) httpClient() *http.Client {
	if s.client != nil {
		return s.client
	}
	return &http.Client{Timeout: defaultTimeout}
}
