// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package clauderoutines

import (
	"context"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/httpx"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Source is the Claude Code Routines inventory connector. It is a STREAMING
// source: Gather runs the poll loop and blocks until ctx is canceled. It must
// be registered with poll_seconds=0 (it owns its own poll cadence via
// refresh_interval; the engine never re-polls a streaming source).
type Source struct {
	cfg  config
	cl   *client
	doer httpx.Doer       // injected transport (tests); nil => default
	now  func() time.Time // injected clock (tests); nil => wall clock
}

var _ sdk.SourceConnector = (*Source)(nil)

func New() *Source { return &Source{} }

func (s *Source) Descriptor() sdk.Descriptor { return descriptor() }

func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now().UTC()
}

// Open resolves configuration and builds the read-only client.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	c, err := loadConfig(cfg)
	if err != nil {
		return err
	}
	s.cfg = c
	s.cl = newClient(c, s.doer)
	return nil
}

// Gather runs the poll loop and blocks until ctx is canceled.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	s.pollLoop(ctx, sink)
	return ctx.Err()
}

// Close is a no-op — there are no listeners or resources to release.
func (s *Source) Close(context.Context) error { return nil }

// emit sends one observation, returning false when the sink reports an error so
// the caller stops the current surface promptly.
func (s *Source) emit(ctx context.Context, sink sdk.Sink, obs model.Observation) bool {
	return sink.Emit(ctx, obs) == nil
}
