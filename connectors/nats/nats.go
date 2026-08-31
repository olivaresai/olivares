// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package nats

import (
	"context"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/brokerobs"
	"github.com/olivaresai/olivares/sdk"
)

// SourceName is the globally unique connector identifier. NATS is observed by a
// SOURCE only — there is no NATS egress connector in this session.
const SourceName = "olivares.nats"

const sourceVersion = "0.1.0"

// Source is the NATS JetStream observer. It is a STREAMING source: Gather attaches a
// dedicated durable PULL consumer and blocks issuing pull requests, emitting
// minimal-data edges for the stream traffic it sees, until ctx is canceled (the
// engine owns scheduling — the connector holds no ticker, S02 §5). It never persists
// or emits a message's payload (docs/SECURITY-HARDENING.md).
type Source struct {
	cfg config
	obs *observer
	otl brokerobs.Instrumentation
	// newClient builds the wire client; defaults to the hand-rolled net.Conn client
	// and is overridden in tests with a fake that yields canned messages (offline).
	newClient func(config) (jsClient, error)
}

var _ sdk.SourceConnector = (*Source)(nil)

// New returns a NATS source connector; configuration is supplied in Open.
func New() *Source { return &Source{newClient: defaultClientFactory} }

// Descriptor returns the source's stable self-description and declared config.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:         SourceName,
		Version:      sourceVersion,
		APIVersion:   sdk.APIVersion,
		Type:         sdk.TypeSource,
		Title:        "NATS JetStream (wire-protocol)",
		Description:  "Observes a NATS JetStream stream via a dedicated durable PULL consumer; minimal-data edges, never message payloads.",
		ConfigFields: descriptorFields(),
	}
}

// Open resolves configuration and builds the observer. A configuration error surfaces
// here, not in Gather.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	c, err := loadConfig(cfg)
	if err != nil {
		return err
	}
	s.cfg = c
	s.obs = &observer{streamRef: c.streamRef, consumerRef: c.consumer}
	s.otl = brokerobs.InstrumentationFromConfig(cfg, "nats")
	if s.newClient == nil {
		s.newClient = defaultClientFactory
	}
	return nil
}

// Gather dials the cluster, emits the consumer→stream read edge once, then blocks
// issuing JetStream pull requests and emitting edges until ctx is canceled. An empty
// pull window (no traffic) simply re-requests. A read error is returned so the engine
// can restart the source with backoff.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	nc := s.newClient
	if nc == nil {
		nc = defaultClientFactory
	}
	cli, err := nc(s.cfg)
	if err != nil {
		return err
	}
	defer cli.Close()

	if emitErr := sink.Emit(ctx, s.obs.observeAttach(time.Now().UTC())); emitErr != nil {
		return emitErr
	}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		msgs, err := cli.Next(ctx, s.cfg.batch)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		now := time.Now().UTC()
		for _, m := range msgs {
			for _, e := range s.obs.observeMsg(m, now) {
				if emitErr := sink.Emit(ctx, e); emitErr != nil {
					return emitErr
				}
			}
		}
	}
}

// Close releases the connector's resources; the client is owned by Gather and closed
// there, so this is a safe no-op even if Open failed.
func (s *Source) Close(context.Context) error { return nil }
