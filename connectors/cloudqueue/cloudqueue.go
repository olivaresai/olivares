// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cloudqueue

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/brokerobs"
	"github.com/olivaresai/olivares/connectors/internal/cloudevents"
	"github.com/olivaresai/olivares/sdk"
)

// maxResponseBytes caps how much of any provider response we read into memory. List
// pages are small; this is a defensive bound against a pathological or hostile
// endpoint, not a functional limit.
const maxResponseBytes = 16 << 20 // 16 MiB

// --- Source ------------------------------------------------------------------

// Source is the cloudqueue topology SourceConnector. It is a BATCH source: each
// Gather runs one non-destructive discovery pass over the enabled managed buses and
// returns nil; the engine owns re-scheduling, so the connector holds no ticker (the
// SDK contract). It keeps no state between passes beyond its resolved config, a
// shared HTTP client and the OTel gate.
type Source struct {
	cfg    config
	client *http.Client
	otel   brokerobs.Instrumentation
}

// Compile-time proof Source satisfies the contract.
var _ sdk.SourceConnector = (*Source)(nil)

// New returns a cloudqueue Source; configuration is supplied in Open.
func New() *Source { return &Source{} }

// Descriptor returns the Source's stable self-description.
func (s *Source) Descriptor() sdk.Descriptor { return sourceDescriptor() }

// Open resolves and validates configuration, reads the secret credentials into
// memory, and builds the shared HTTP client. A configuration error (missing
// provider, missing credentials for an enabled service) surfaces here, before
// Gather (the SDK contract).
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	c, err := loadConfig(cfg)
	if err != nil {
		return err
	}
	if err := c.validateSource(); err != nil {
		return err
	}
	s.cfg = c
	s.client = &http.Client{Timeout: c.timeout}
	s.otel = brokerobs.InstrumentationFromConfig(cfg, "cloudqueue/"+c.provider)
	return nil
}

// Gather runs one discovery pass over the enabled services for the configured
// provider and returns nil (the engine re-schedules). A disabled service is skipped
// silently (absent ⇒ no finding). An enabled service that fails to list yields
// exactly one health finding (a gap is a signal) and the pass continues. ctx is
// honored: it is checked before each service and a cancellation returns ctx.Err()
// promptly. Every observation is stamped with one per-pass UTC timestamp.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	at := time.Now().UTC()
	switch s.cfg.provider {
	case providerGCP:
		return s.gatherGCP(ctx, sink, at)
	default:
		return s.gatherAWS(ctx, sink, at)
	}
}

// Close releases the Source's resources. It holds no long-lived resources between
// passes; it is safe to call even if Open failed.
func (s *Source) Close(context.Context) error { return nil }

// httpClient returns the Source's HTTP client, falling back to a default when Open
// did not set one (defensive; Open always sets it on success).
func (s *Source) httpClient() *http.Client {
	if s.client != nil {
		return s.client
	}
	return &http.Client{Timeout: defaultTimeout}
}

// --- Output ------------------------------------------------------------------

// Output is the cloudqueue egress OutputConnector: it wraps a Notification in a
// CloudEvents 1.0 structured document and publishes it to the operator-configured
// egress target (an SNS topic or a Pub/Sub topic). This is the only write the
// connector issues, and it writes only our own CloudEvents — never a message body.
type Output struct {
	cfg    config
	client *http.Client
}

// Compile-time proof Output satisfies the contract.
var _ sdk.OutputConnector = (*Output)(nil)

// NewOutput returns a cloudqueue egress connector; configuration is supplied in Open.
func NewOutput() *Output { return &Output{} }

// Descriptor returns the Output's stable self-description.
func (o *Output) Descriptor() sdk.Descriptor { return outputDescriptor() }

// Open resolves and validates configuration (provider, egress target, the
// credentials needed to publish) and builds the HTTP client. A configuration error
// surfaces here, before any Notify (the SDK contract).
func (o *Output) Open(_ context.Context, cfg sdk.Config) error {
	c, err := loadConfig(cfg)
	if err != nil {
		return err
	}
	if err := c.validateOutput(); err != nil {
		return err
	}
	o.cfg = c
	o.client = &http.Client{Timeout: c.timeout}
	return nil
}

// Notify wraps n in a CloudEvent (with a fresh crypto/rand id), serializes it to a
// structured JSON document, and publishes that document to the egress target via
// the configured provider. The CloudEvents body is the ONLY content published — no
// secret, no consumed message body.
func (o *Output) Notify(ctx context.Context, n sdk.Notification) error {
	if o.client == nil {
		return fmt.Errorf("cloudqueue egress: not open")
	}
	id, err := newEventID()
	if err != nil {
		return err
	}
	ev, err := cloudevents.FromNotification(id, o.cfg.egressSource, n)
	if err != nil {
		return err
	}
	body, err := ev.StructuredBytes()
	if err != nil {
		return err
	}
	switch o.cfg.provider {
	case providerGCP:
		return o.publishPubSub(ctx, body)
	default:
		return o.publishSNS(ctx, body)
	}
}

// Close releases the Output's resources; safe to call even if Open failed.
func (o *Output) Close(context.Context) error { return nil }

// httpClient returns the Output's HTTP client, falling back to a default when Open
// did not set one (defensive).
func (o *Output) httpClient() *http.Client {
	if o.client != nil {
		return o.client
	}
	return &http.Client{Timeout: defaultTimeout}
}

// newEventID returns a unique CloudEvents id (16 random bytes, hex). A Notification
// has no id of its own and the publisher owns uniqueness.
func newEventID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("cloudqueue egress: event id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
