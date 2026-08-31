// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"context"
	"errors"
	"time"

	"github.com/olivaresai/olivares/sdk"
)

// Source is the runtime-inventory SourceConnector. It is a BATCH source: each
// Gather runs one discovery pass across the three enabled sub-discoverers and
// returns; the engine owns re-scheduling, so the connector holds no ticker
// (per the SDK contract).
type Source struct {
	cfg config
}

// Compile-time proof that Source satisfies the contract.
var _ sdk.SourceConnector = (*Source)(nil)

// New returns a runtime connector; configuration is supplied in Open.
func New() *Source { return &Source{} }

// Descriptor returns the connector's stable self-description.
func (s *Source) Descriptor() sdk.Descriptor { return descriptor() }

// Open resolves the configuration. Resolution never fails (an absent target is a
// silent skip at Gather), so Open only records the resolved config.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.cfg = loadConfig(cfg)
	return nil
}

// Gather runs each enabled, present discoverer once in the fixed order
// linux → docker → k8s, emitting containment edges to sink and a health finding
// for any enabled+present discoverer that fails (then continuing). ctx is honored
// between discoverers and inside each discoverer's loops; it returns ctx.Err()
// promptly when canceled. A single per-pass timestamp stamps every observation.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	at := time.Now().UTC()

	if s.cfg.enableLinux {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := gatherLinux(ctx, s.cfg, sink, at); err != nil {
			if isCanceled(err) {
				return err
			}
			if emitErr := sink.Emit(ctx, healthFinding(subjectHost, s.cfg.host, "linux discovery failed", err, at)); emitErr != nil {
				return emitErr
			}
		}
	}

	if s.cfg.enableDocker {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := gatherDocker(ctx, s.cfg, sink, at); err != nil {
			if isCanceled(err) {
				return err
			}
			if emitErr := sink.Emit(ctx, healthFinding(subjectDockerHost, s.cfg.host, "docker discovery failed", err, at)); emitErr != nil {
				return emitErr
			}
		}
	}

	if s.cfg.enableK8s {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := gatherK8s(ctx, s.cfg, sink, at); err != nil {
			if isCanceled(err) {
				return err
			}
			if emitErr := sink.Emit(ctx, healthFinding(subjectK8sCluster, k8sRef(s.cfg), "kubernetes discovery failed", err, at)); emitErr != nil {
				return emitErr
			}
		}
	}

	return nil
}

// Close releases the connector's resources; it holds none between Gather runs.
func (s *Source) Close(context.Context) error { return nil }

// isCanceled reports whether err is a context cancellation/deadline, which must
// propagate (it is a control-flow signal, not a discoverer fault). The
// discoverers already return ctx.Err() directly on cancellation, but a transport
// can also wrap it; errors.Is catches both the bare and wrapped forms.
func isCanceled(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
