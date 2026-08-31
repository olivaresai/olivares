// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package tak

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Rejection reason classes reported by the listeners. They are a closed set so an
// operator can alert on a class without matching free text.
const (
	reasonRateLimited = "rate_limited"
	reasonOversize    = "oversize"
	reasonMalformed   = "malformed"
	reasonConnLimit   = "conn_limit"
)

// Transport names.
const (
	transportUDP = "udp"
	transportTCP = "tcp"
)

// rejectFlushInterval bounds how often aggregated rejection findings are emitted.
// A malformed-packet flood must not become a finding per packet — the ledger is
// append-only and a hostile peer would otherwise choose our storage bill.
const rejectFlushInterval = time.Minute

// acceptedQueue bounds the in-flight events between the listeners and the single
// goroutine that calls sink.Emit. Backpressure is intentional: a slow engine slows
// the listener (and, for UDP, the kernel drops — which we cannot count, and say so
// in the docs) rather than growing an unbounded queue in this process.
const acceptedQueue = 256

// httpDoer is the seam the posture pass talks to, so tests drive it without a
// TAK Server. The production implementation is an mTLS *http.Client.
type httpDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// listenerCallbacks is the contract listener.go delivers against. onEvent blocks
// (that is the backpressure); onReject must not block.
type listenerCallbacks struct {
	onEvent  func(ctx context.Context, ev Event, transport string) error
	onReject func(reason, transport string)
}

// acceptedEvent is one parsed event traveling from a listener to the emitter.
type acceptedEvent struct {
	ev        Event
	transport string
}

// Source inventories a TAK Server and ingests CoT as governed signal.
type Source struct {
	cfg config
	now func() time.Time

	// newDoer builds the posture HTTP client; overridable in tests.
	newDoer func(config) (httpDoer, error)

	mu       sync.Mutex
	rejects  map[rejectKey]int
	accepted chan acceptedEvent
}

type rejectKey struct{ reason, transport string }

var _ sdk.SourceConnector = (*Source)(nil)

// New returns a TAK connector with default configuration.
func New() *Source {
	return &Source{
		now:     time.Now,
		newDoer: newMTLSClient,
		rejects: map[rejectKey]int{},
	}
}

// Descriptor returns the connector's stable self-description.
func (s *Source) Descriptor() sdk.Descriptor { return descriptor() }

// Open validates configuration. It performs no network I/O: an unreachable TAK
// Server is a Gather-time fault the engine may retry, but a misconfiguration
// (plaintext URL, missing client certificate, bad listen address) is refused here.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	c, err := loadConfig(cfg)
	if err != nil {
		return err
	}
	s.cfg = c
	if s.now == nil {
		s.now = time.Now
	}
	if s.newDoer == nil {
		s.newDoer = newMTLSClient
	}
	if s.rejects == nil {
		s.rejects = map[rejectKey]int{}
	}
	return nil
}

// Gather runs one posture pass and then, if any CoT listener is configured, blocks
// serving it until ctx is done (a streaming source, per the sdk.SourceConnector
// contract). With neither configured it is an honest no-op: it emits nothing
// rather than fabricate a clean posture for a deployment it never contacted.
//
// Every sink.Emit in this connector happens on THIS goroutine. The SDK does not
// promise Sink is safe for concurrent use, and the listeners are inherently
// concurrent, so accepted events cross a channel rather than a mutex.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	if s.cfg.postureEnabled() {
		if err := s.gatherPostureOnce(ctx, sink); err != nil {
			return err
		}
	}
	if !s.cfg.ingestEnabled() {
		return nil
	}
	return s.serveCoT(ctx, sink)
}

// Close releases resources. Listeners are owned by Gather and torn down with its
// context; the connector holds no long-lived handles of its own.
func (s *Source) Close(context.Context) error { return nil }

func (s *Source) clock() time.Time {
	if s.now == nil {
		return time.Now()
	}
	return s.now()
}

// gatherPostureOnce authenticates to the TAK Server and emits its posture.
func (s *Source) gatherPostureOnce(ctx context.Context, sink sdk.Sink) error {
	doer, err := s.newDoer(s.cfg)
	if err != nil {
		return fmt.Errorf("tak: build TAK Server client: %w", err)
	}
	findings, err := gatherPosture(ctx, s.cfg, doer, s.clock().UTC())
	if err != nil {
		return err
	}
	for _, f := range findings {
		if err := sink.Emit(ctx, f); err != nil {
			return err
		}
	}
	return nil
}

// serveCoT runs the listeners until ctx is done, emitting observations for every
// accepted event and periodic aggregates for refused traffic.
func (s *Source) serveCoT(ctx context.Context, sink sdk.Sink) error {
	s.accepted = make(chan acceptedEvent, acceptedQueue)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg     sync.WaitGroup
		runErr error
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(s.accepted)
		runErr = runListeners(runCtx, s.cfg, listenerCallbacks{
			onEvent:  s.queueEvent,
			onReject: s.countReject,
		})
	}()

	ticker := time.NewTicker(rejectFlushInterval)
	defer ticker.Stop()

	var emitErr error
drain:
	for {
		select {
		case item, ok := <-s.accepted:
			if !ok {
				break drain
			}
			if err := s.emitEvent(ctx, sink, item.ev, item.transport); err != nil {
				emitErr = err
				cancel()
				break drain
			}
		case <-ticker.C:
			if err := s.flushRejects(ctx, sink); err != nil {
				emitErr = err
				cancel()
				break drain
			}
		case <-ctx.Done():
			cancel()
			break drain
		}
	}

	cancel()
	// Drain whatever the listeners already accepted so a clean shutdown does not
	// silently discard events the engine has not seen yet.
	for item := range s.accepted {
		if emitErr != nil {
			continue
		}
		if err := s.emitEvent(context.WithoutCancel(ctx), sink, item.ev, item.transport); err != nil {
			emitErr = err
		}
	}
	wg.Wait()

	if emitErr == nil {
		// Final aggregate: refusals observed since the last tick must still reach the
		// ledger, otherwise a short-lived flood leaves no trace.
		if err := s.flushRejects(context.WithoutCancel(ctx), sink); err != nil {
			emitErr = err
		}
	}

	switch {
	case emitErr != nil:
		return emitErr
	case runErr != nil && !errors.Is(runErr, context.Canceled):
		return runErr
	case ctx.Err() != nil:
		return ctx.Err()
	default:
		return nil
	}
}

// queueEvent is the listener callback. It blocks on the bounded queue, which is
// how backpressure reaches the listeners.
func (s *Source) queueEvent(ctx context.Context, ev Event, transport string) error {
	select {
	case s.accepted <- acceptedEvent{ev: ev, transport: transport}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// countReject accumulates a refusal. It never blocks and never allocates a finding.
func (s *Source) countReject(reason, transport string) {
	s.mu.Lock()
	s.rejects[rejectKey{reason: reason, transport: transport}]++
	s.mu.Unlock()
}

// flushRejects emits one aggregate finding per (reason, transport) seen since the
// last flush, in a deterministic order, and resets the counters.
func (s *Source) flushRejects(ctx context.Context, sink sdk.Sink) error {
	s.mu.Lock()
	if len(s.rejects) == 0 {
		s.mu.Unlock()
		return nil
	}
	snapshot := make([]rejectKey, 0, len(s.rejects))
	counts := make(map[rejectKey]int, len(s.rejects))
	for k, v := range s.rejects {
		snapshot = append(snapshot, k)
		counts[k] = v
	}
	s.rejects = map[rejectKey]int{}
	s.mu.Unlock()

	sort.Slice(snapshot, func(i, j int) bool {
		if snapshot[i].reason != snapshot[j].reason {
			return snapshot[i].reason < snapshot[j].reason
		}
		return snapshot[i].transport < snapshot[j].transport
	})

	at := s.clock().UTC()
	for _, k := range snapshot {
		f := s.rejectionFinding(k.reason, k.transport, counts[k], at)
		if err := sink.Emit(ctx, f); err != nil {
			return err
		}
	}
	return nil
}

// compile-time proof the posture pass returns what Gather emits.
var _ func(context.Context, config, httpDoer, time.Time) ([]model.FindingReport, error) = gatherPosture
