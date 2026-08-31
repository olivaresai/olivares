// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudemanagedagents

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/httpx"
	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
	"github.com/olivaresai/olivares/sdk/netbind"
)

// Source is the CMA control-plane observation connector. It is a STREAMING source: Gather
// runs the inbound webhook receiver (when configured) and the inventory/governance
// GET-pollers (when an API key is configured) concurrently, and blocks until ctx is
// canceled. It must be registered with poll_seconds=0 (it owns its own poll cadence via
// refresh_interval; the engine never re-polls a streaming source).
type Source struct {
	cfg config
	cl  *client
	lis net.Listener // bound in Open when the webhook receiver is enabled (errors surface early)

	// dreamsGated latches once a dreams fetch returns the GATED-preview signal (403/404)
	// so the no-access posture is declared ONCE per Gather lifetime instead of every
	// refresh. Touched only by the poll goroutine.
	dreamsGated bool

	doer httpx.Doer       // injected transport (tests); nil => default
	now  func() time.Time // injected clock (tests); nil => wall clock
}

// Compile-time proof that Source satisfies the contract.
var _ sdk.SourceConnector = (*Source)(nil)

// New returns a CMA connector; configuration is supplied in Open.
func New() *Source { return &Source{} }

// Descriptor returns the connector's stable self-description.
func (s *Source) Descriptor() sdk.Descriptor { return descriptor() }

// clock returns the injectable time source (UTC).
func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now().UTC()
}

// Open resolves configuration, builds the read-only client, and (when the webhook receiver
// is enabled) binds its listener now so a bind/permission error surfaces here, before Gather.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	c, err := loadConfig(cfg)
	if err != nil {
		return err
	}
	s.cfg = c
	s.cl = newClient(c, s.doer)
	if c.webhookEnabled() {
		// One admission point for every socket this product opens.
		lis, err := netbind.Listen(context.Background(), "tcp", c.webhookAddr, c.bindPolicy())
		if err != nil {
			return fmt.Errorf("claude-managed-agents: bind webhook receiver %s: %w", c.webhookAddr, err)
		}
		s.lis = lis
	}
	return nil
}

// serialSink serializes sink.Emit across the Source's emitter goroutines (each webhook
// delivery runs on its own net/http goroutine, concurrently with the poll loop). The
// out-of-process SDK sinks call grpc stream.Send, whose contract forbids concurrent
// SendMsg on one stream — so the connector serializes its own emission (the same
// stance as cowork's single-dispatcher design). Backpressure is preserved: Emit still
// blocks; concurrent emitters queue on the mutex.
type serialSink struct {
	mu   sync.Mutex
	sink sdk.Sink
}

func (k *serialSink) Emit(ctx context.Context, obs model.Observation) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.sink.Emit(ctx, obs)
}

// Gather runs the receiver and/or pollers and blocks until ctx is canceled. It is a
// streaming source: it returns ctx.Err() on a clean cancel, or a fatal receiver error.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	sink = &serialSink{sink: sink}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	errCh := make(chan error, 1)

	if s.cfg.webhookEnabled() && s.lis != nil {
		// The GET-back enrichment is mounted only when the read client is usable (an
		// API key is configured): webhook-only mode keeps the thin envelope facts and
		// nothing else — never a fabricated resource state.
		var enrich func(context.Context, webhookEnvelope, time.Time) []model.Observation
		if s.cfg.apiKey != "" {
			enrich = s.enrichWebhook
		}
		rcv, err := newWebhookReceiver(s.cfg.webhookSecret, s.cfg.webhookSkew, s.clock, func(ec context.Context, obs model.Observation) error {
			return sink.Emit(ec, obs)
		}, enrich)
		if err != nil {
			return err
		}
		mux := http.NewServeMux()
		mux.Handle(s.cfg.webhookPath, rcv)
		srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
		go func() {
			<-runCtx.Done()
			sc, c := context.WithTimeout(context.Background(), 5*time.Second)
			defer c()
			_ = srv.Shutdown(sc)
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := srv.Serve(s.lis); err != nil && !errors.Is(err, http.ErrServerClosed) {
				select {
				case errCh <- err:
				default:
				}
				cancel()
			}
		}()
	}

	if s.cfg.pollEnabled() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.pollLoop(runCtx, sink)
		}()
	}

	<-runCtx.Done()
	wg.Wait()
	select {
	case err := <-errCh:
		return err
	default:
		return ctx.Err()
	}
}

// Close releases the webhook listener (benign if Gather's Shutdown already closed it).
func (s *Source) Close(context.Context) error {
	if s.lis != nil {
		if err := s.lis.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			return err
		}
	}
	return nil
}

// webhookEnrichTimeout bounds the GET-back a webhook delivery triggers, so a slow
// upstream cannot wedge the receiver past Anthropic's delivery timeout (the envelope
// facts were already verified; a missed enrichment degrades to a finding, never a hang).
const webhookEnrichTimeout = 10 * time.Second

// enrichWebhook expands a VERIFIED thin webhook envelope into full governance
// observations by GETting the named resource back (sessions.go / threads.go). Errors
// degrade to an honest self-audit finding — the enrichment is best-effort on top of the
// envelope facts the receiver already emitted.
func (s *Source) enrichWebhook(ctx context.Context, ev webhookEnvelope, at time.Time) []model.Observation {
	ctx, cancel := context.WithTimeout(ctx, webhookEnrichTimeout)
	defer cancel()
	id := ev.Data.ID
	switch {
	case ev.Data.Type == "session.status_idled":
		return s.enrichIdledSession(ctx, id, at)
	case ev.Data.Type == "session.status_terminated", ev.Data.Type == "session.outcome_evaluation_ended":
		return s.enrichSessionState(ctx, id, at)
	case strings.HasPrefix(ev.Data.Type, "session.thread_"):
		// The thread webhook family names the thread; threads are listed per session,
		// so only a session-shaped id can be enriched (an sthr_ id alone cannot be
		// resolved to its session via the read API — honest skip).
		if strings.HasPrefix(id, "sesn_") {
			return s.enrichThreads(ctx, id, at)
		}
		return nil
	default:
		return nil
	}
}

// enrichDegraded is the honest enrichment-failure record (mirror of poll degrade, but
// returned as an observation for the receiver to emit).
func enrichDegraded(surface string, err error, at time.Time) model.Observation {
	return model.FindingReport{
		Kind:        findingSelfAudit,
		Severity:    model.SeverityLow,
		SubjectKind: connectorSubject,
		SubjectRef:  surface,
		Title:       "CMA webhook enrichment degraded: " + surface,
		DetailHash:  redact.Hash("cma webhook enrichment degraded surface=" + surface + " err=" + redact.Clean(err.Error())),
		OccurredAt:  at,
	}
}

// enrichIdledSession handles session.status_idled: the session state (mounts, vault
// use, outcome verdicts) plus the requires_action HITL probe — an idle paused on an
// always_ask policy is routed as the pending-confirmation finding the bridge
// consumes (ANT2-14 queue), recovered from the session event list because the session
// resource itself carries no stop_reason.
func (s *Source) enrichIdledSession(ctx context.Context, sessionID string, at time.Time) []model.Observation {
	sess, err := s.cl.fetchSession(ctx, sessionID)
	if err != nil {
		return []model.Observation{enrichDegraded("session_get_back", err, at)}
	}
	out := sessionObservations(sess, at)
	blocking, awaiting, err := s.cl.fetchAwaitingConfirmation(ctx, sessionID)
	if err != nil {
		return append(out, enrichDegraded("session_events", err, at))
	}
	if awaiting {
		out = append(out,
			toolConfirmationFinding(sessionID, blocking, at),
			permissionPolicyEdge(sessionID, at),
		)
	}
	return out
}

// enrichSessionState handles session.status_terminated / session.outcome_evaluation_
// ended: re-read the session so terminal outcome verdicts and final mounts land even
// without the poller (which lists only active sessions).
func (s *Source) enrichSessionState(ctx context.Context, sessionID string, at time.Time) []model.Observation {
	sess, err := s.cl.fetchSession(ctx, sessionID)
	if err != nil {
		return []model.Observation{enrichDegraded("session_get_back", err, at)}
	}
	return sessionObservations(sess, at)
}

// enrichThreads handles the session.thread_* family: re-list the session's threads so
// the multi-agent topology stays current between polls.
func (s *Source) enrichThreads(ctx context.Context, sessionID string, at time.Time) []model.Observation {
	threads, err := s.cl.fetchThreads(ctx, sessionID)
	if err != nil {
		return []model.Observation{enrichDegraded("session_threads", err, at)}
	}
	var out []model.Observation
	for _, t := range threads {
		if e, ok := threadEdge(t, at); ok {
			out = append(out, e)
		}
	}
	return out
}
