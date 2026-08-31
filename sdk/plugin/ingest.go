// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package plugin

import (
	"context"
	"sync"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
	pb "github.com/olivaresai/olivares/sdk/plugin/genpb/olivaresv1"
)

// This file is the collector-side transport glue for the distributed
// collector→core push plane (CB-1 option C, ARCHITECTURE.md). It reuses the same
// sealed-Observation wire codec as the SourceService plugin path (convert.go), so
// a collector that PUSHES and a plugin the host PULLS speak the identical
// observation contract — there is no second wire shape and no raw-payload path.
// The CORE-side Push server (auth + per-stream authorization + lift onto the bus)
// lives in the AGPL engine (core/api), next to the rest of the gRPC surface; this
// Apache package owns only the wire codec and the client Sink.

// ObservationToPB encodes a sealed model.Observation onto the wire oneof. It is
// the exported entry point the collector's Sink and the engine's ingest server
// use; the codec itself lives in convert.go so all wire encoding is reviewed in
// one place.
func ObservationToPB(obs model.Observation) (*pb.Observation, error) { return observationToPB(obs) }

// ObservationFromPB decodes a wire Observation back to a sealed model.Observation.
// An empty/unknown oneof is a contract error, never a silently dropped fact.
func ObservationFromPB(o *pb.Observation) (model.Observation, error) { return observationFromPB(o) }

// IngestSink is the collector-side sdk.Sink: every observation a locally-run
// SourceConnector emits is streamed to the remote core's IngestService over the
// already-dialed (gRPC+mTLS, bearer-authenticated) client. Because it satisfies
// sdk.Sink, the engine's runtime drives a collector's sources through the SAME
// gatherLoop, scheduler and failure isolation as a single-node deployment — B is
// the substrate of C. One sink owns one Push stream, opened lazily on first Emit
// (bound to that Emit's context, the runtime's run context) and torn down when
// that context is canceled on Stop.
type IngestSink struct {
	client pb.IngestServiceClient
	tenant string
	source string

	mu     sync.Mutex
	stream pb.IngestService_PushClient
}

var _ sdk.Sink = (*IngestSink)(nil)

// NewIngestSink builds a push Sink for (tenant, source) over an already-connected
// IngestService client. The collector constructs one per registered source via
// the runtime's sink factory.
func NewIngestSink(client pb.IngestServiceClient, tenant, source string) *IngestSink {
	return &IngestSink{client: client, tenant: tenant, source: source}
}

// Emit streams one observation to the core. It blocks on the underlying stream's
// flow control (backpressure is intentional, mirroring the in-process bus). A
// send error is returned to the connector as fatal to the current Gather run, per
// the Sink contract.
func (s *IngestSink) Emit(ctx context.Context, obs model.Observation) error {
	st, err := s.ensureStream(ctx)
	if err != nil {
		return err
	}
	msg, err := ObservationToPB(obs)
	if err != nil {
		return err
	}
	return st.Send(&pb.IngestEnvelope{Tenant: s.tenant, Source: s.source, Observation: msg})
}

func (s *IngestSink) ensureStream(ctx context.Context) (pb.IngestService_PushClient, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stream != nil {
		return s.stream, nil
	}
	st, err := s.client.Push(ctx)
	if err != nil {
		return nil, err
	}
	s.stream = st
	return st, nil
}

// CloseAndRecv half-closes the stream and waits for the core's summary. The
// collector calls it on graceful shutdown to flush; an unclosed stream is still
// safe (the core lifted every observation as it arrived). It is a no-op if Emit
// was never called.
func (s *IngestSink) CloseAndRecv() (uint64, error) {
	s.mu.Lock()
	st := s.stream
	s.stream = nil
	s.mu.Unlock()
	if st == nil {
		return 0, nil
	}
	sum, err := st.CloseAndRecv()
	if err != nil {
		return 0, err
	}
	return sum.GetAccepted(), nil
}
