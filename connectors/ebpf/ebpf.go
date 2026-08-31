// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package ebpf

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// dispatchBuffer is the depth of the observation hand-off channel. A full buffer
// applies backpressure to the read loop (slowing ingestion) rather than dropping
// facts — the SDK Sink contract.
const dispatchBuffer = 1024

// readBufferSize is the initial line-reader buffer. Kernel paths and command
// lines can be long, so it is generous; the reader grows further as needed.
const readBufferSize = 256 * 1024

// eofPollInterval is how long the reader waits before re-checking a followed
// stream that has reached EOF (a tailed file or an idle FIFO).
const eofPollInterval = 250 * time.Millisecond

// defaultShutdownGrace bounds how long emission may outlive a parent-context
// cancellation. After the engine cancels Gather's ctx, a sink that stays blocked
// past this grace is abandoned so a stuck engine cannot wedge shutdown (the SDK
// Sink contract lets Emit block until its own ctx is done). It is a Source field
// so tests can shorten it.
const defaultShutdownGrace = 5 * time.Second

// Source is the eBPF/Tetragon backstop SourceConnector. It is a streaming source:
// Gather reads the Tetragon JSON event stream and blocks, emitting access edges
// (and optional anti-evasion findings) until the engine cancels ctx.
type Source struct {
	cfg           config
	evasion       *evasionDetector
	now           func() time.Time
	shutdownGrace time.Duration

	useStdin  bool // events come from standard input
	pollOnEOF bool // keep reading after EOF (a followed file/FIFO)
}

// Compile-time proof that Source satisfies the contract.
var _ sdk.SourceConnector = (*Source)(nil)

// New returns an eBPF connector with default configuration (overridden in Open).
func New() *Source {
	return &Source{
		cfg:           loadConfig(sdk.Config{}),
		evasion:       newEvasionDetector(loadConfig(sdk.Config{})),
		now:           func() time.Time { return time.Now().UTC() },
		shutdownGrace: defaultShutdownGrace,
	}
}

// Descriptor returns the connector's stable self-description.
func (s *Source) Descriptor() sdk.Descriptor { return descriptor() }

// Open resolves configuration and validates the event source, so a misconfiguration
// (missing path, unreadable file) surfaces here, before Gather, as the SDK intends.
// A FIFO is not opened here — opening it for read blocks until Tetragon connects —
// only confirmed to exist; the actual open happens in Gather.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.cfg = loadConfig(cfg)
	s.evasion = newEvasionDetector(s.cfg)

	if s.cfg.eventsPath == "" {
		return fmt.Errorf("ebpf: %s is required", cfgEventsPath)
	}
	if s.cfg.eventsPath == stdinPath {
		s.useStdin = true
		s.pollOnEOF = false // a pipe's EOF means the producer finished
		return nil
	}
	info, err := os.Stat(s.cfg.eventsPath)
	if err != nil {
		return fmt.Errorf("ebpf: events source %q not accessible: %w", s.cfg.eventsPath, err)
	}
	if info.IsDir() {
		return fmt.Errorf("ebpf: events source %q is a directory, want a file or FIFO", s.cfg.eventsPath)
	}
	s.useStdin = false
	s.pollOnEOF = s.cfg.follow // tail a regular file / idle FIFO when following
	return nil
}

// Gather runs the connector: it opens the event source, starts a single dispatcher
// that serializes emission to the sink and (when enabled) the anti-evasion janitor,
// then reads the Tetragon stream until ctx is canceled or the stream ends, shutting
// everything down cleanly. It returns nil on a ctx-driven stop or a clean
// end-of-stream, and a non-nil error only on a genuine read fault the engine may retry.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	r, closeFn, err := s.openReader()
	if err != nil {
		return err
	}
	defer closeFn()

	// Cancel-closer: a read parked in the kernel on an idle FIFO cannot be
	// interrupted by a top-of-loop ctx check, so on cancellation we close the
	// reader, which makes the in-flight Read return and lets readLoop honor ctx. It
	// is disarmed once readLoop has returned.
	readDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			closeFn()
		case <-readDone:
		}
	}()

	// Dispatcher: the only caller of sink.Emit, so emission is serial regardless of
	// the SDK's concurrency contract. emitCtx is Background-derived so a final
	// shutdown sweep still delivers; the watchdog below bounds how long it may
	// outlive a parent cancellation so a stuck sink cannot wedge shutdown.
	obsCh := make(chan model.Observation, dispatchBuffer)
	emitCtx, emitCancel := context.WithCancel(context.Background())
	defer emitCancel()
	dispatcherDone := make(chan struct{})
	go func() {
		defer close(dispatcherDone)
		for o := range obsCh {
			_ = sink.Emit(emitCtx, o)
		}
	}()
	dispatch := func(o model.Observation) {
		select {
		case obsCh <- o:
		case <-emitCtx.Done():
		}
	}

	// Emission watchdog: once the parent ctx is canceled, give in-flight emission a
	// bounded grace, then force emitCtx closed so a permanently-blocked Emit (a
	// stuck/slow engine sink) cannot hang Gather forever. It exits cleanly when
	// emission finishes first. Backpressure during normal operation (ctx alive) is
	// intentional and is not bounded here.
	emitDone := make(chan struct{})
	go func() {
		select {
		case <-emitDone:
			return
		case <-ctx.Done():
		}
		select {
		case <-emitDone:
		case <-time.After(s.shutdownGrace):
			emitCancel()
		}
	}()

	// Janitor: periodically sweep the anti-evasion detector (no-op when disabled).
	runCtx, runCancel := context.WithCancel(ctx)
	janitorDone := make(chan struct{})
	go func() {
		defer close(janitorDone)
		if s.evasion.enabled {
			s.evasion.run(runCtx, dispatch, s.now)
		}
	}()

	readErr := s.readLoop(runCtx, r, dispatch)
	close(readDone) // disarm the cancel-closer

	// Shutdown: stop the janitor, run a final sweep so a gap that matured during
	// the last interval is still reported, then drain and release the sink.
	runCancel()
	<-janitorDone
	s.evasion.sweep(s.now(), dispatch)
	close(obsCh)
	<-dispatcherDone
	close(emitDone)
	emitCancel()

	if readErr != nil && ctx.Err() == nil {
		return readErr
	}
	return nil
}

// Close releases resources. Gather owns and closes the reader within its own run,
// so Close holds nothing; it is safe to call even if Open failed.
func (s *Source) Close(context.Context) error { return nil }

// openReader resolves the configured source to an io.Reader and a close function.
// Standard input is never closed by this connector.
func (s *Source) openReader() (io.Reader, func(), error) {
	if s.useStdin {
		return os.Stdin, func() {}, nil
	}
	f, err := os.Open(s.cfg.eventsPath)
	if err != nil {
		return nil, func() {}, fmt.Errorf("ebpf: open events source %q: %w", s.cfg.eventsPath, err)
	}
	return f, func() { _ = f.Close() }, nil
}

// readLoop reads newline-delimited Tetragon JSON from r, handing each event to
// handle, until ctx is canceled or (for a non-followed source) the stream ends. A
// followed source waits at EOF and resumes, so it tails a growing file or a FIFO.
// A malformed line is skipped, never fatal to the stream.
func (s *Source) readLoop(ctx context.Context, r io.Reader, dispatch func(model.Observation)) error {
	br := bufio.NewReaderSize(r, readBufferSize)
	var pending []byte
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			pending = append(pending, line...)
			if pending[len(pending)-1] == '\n' {
				s.handleLine(pending, dispatch)
				pending = pending[:0]
			}
		}
		if err == nil {
			continue
		}
		if err != io.EOF {
			return fmt.Errorf("ebpf: read events: %w", err)
		}
		if !s.pollOnEOF {
			if len(pending) > 0 {
				s.handleLine(pending, dispatch)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(eofPollInterval):
		}
	}
}

// handleLine parses one Tetragon JSON line and routes it. A parse error is dropped
// (a single bad line must not stop ingestion).
func (s *Source) handleLine(line []byte, dispatch func(model.Observation)) {
	env, err := parseEnvelope(line)
	if err != nil {
		return
	}
	s.handle(env, dispatch)
}

// handle routes one decoded Tetragon event: process lifecycle updates the
// anti-evasion state; a kprobe becomes a file or network edge (classified by the
// shape of its arguments) and feeds the detector.
func (s *Source) handle(env tetragonEnvelope, dispatch func(model.Observation)) {
	at := env.eventTime(s.now)
	switch {
	case env.ProcessExit != nil:
		s.evasion.onExit(procFromTetragon(env.ProcessExit.Process, env.NodeName))
	case env.ProcessKprobe != nil:
		s.handleKprobe(env.ProcessKprobe, env.NodeName, at, dispatch)
	case env.ProcessExec != nil:
		// Process start is observed for attribution context but not emitted on its
		// own (the sealed Observation set has no process kind; host inventory is
		// Scope).
	}
}

// handleKprobe maps a kprobe event to an edge and feeds the anti-evasion detector.
func (s *Source) handleKprobe(kp *tetragonKprobe, node string, at time.Time, dispatch func(model.Observation)) {
	pi := procFromTetragon(kp.Process, node)
	origin := pi.originRef()

	if fa := firstFileArg(kp.Args); fa != nil {
		mask, _ := firstIntArg(kp.Args)
		if e, ok := fileEdge(origin, fa.Path, mask, at); ok {
			dispatch(e)
		}
		s.evasion.observeAccess(pi, at)
		return
	}

	if t, ok := tupleFromArgs(kp.Args); ok {
		if e, ok := netEdge(origin, t.endpointRef(), at); ok {
			dispatch(e)
		}
		if t.matchesEndpoint(s.cfg.otlpEndpoints) {
			s.evasion.observeCooperative(pi, at)
		} else {
			s.evasion.observeAccess(pi, at)
		}
	}
}
