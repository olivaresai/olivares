// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"errors"
	"io"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
	sdkplugin "github.com/olivaresai/olivares/sdk/plugin"
	olv1 "github.com/olivaresai/olivares/sdk/plugin/genpb/olivaresv1"
)

// ObservationPublisher lifts a single pushed observation onto the engine's event
// bus. The runtime implements it (its Ingest method); the API's IngestService
// authorizes the push, then calls it. It stays an interface so core/api does not
// import core/runtime (the composition root wires the concrete runtime in).
type ObservationPublisher interface {
	Ingest(ctx context.Context, tenant, source string, obs sdkmodel.Observation) error
}

// ingestService implements the collector→core IngestService (CB-1 option C,
// ARCHITECTURE.md). It rides the SAME hardened gRPC server as the ControlPlane service:
// TLS — mutual TLS when the operator sets --grpc-client-ca, the secure default for
// a remote collector (docs/SECURITY-HARDENING.md) — plus the bearer-auth stream interceptor and
// the shared authorize path. Push is the genuine server→core push the SDK did not
// expose before: the collector streams observations and the core lifts each onto
// the bus exactly as an in-process source's Sink would.
type ingestService struct {
	olv1.UnimplementedIngestServiceServer
	s   *Server
	pub ObservationPublisher
}

// Push receives a collector's observation stream. It authorizes ingest:write for
// each envelope's tenant ONCE per stream (memoized: a collector pushing thousands
// of observations for one tenant pays a single authorization), decodes the sealed
// observation, and lifts it onto the bus the moment it arrives — so an abruptly
// dropped stream still delivered everything it sent (at-least-once). The bearer
// principal was authenticated by the stream interceptor; the collector's mTLS
// client certificate was verified by the transport.
func (g *ingestService) Push(stream olv1.IngestService_PushServer) error {
	ctx := stream.Context()
	// tenant ref -> resolved canonical tenant, memoized after the first authorize.
	authorized := map[string]model.TenantID{}
	var accepted uint64
	for {
		env, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return stream.SendAndClose(&olv1.IngestSummary{Accepted: accepted})
		}
		if err != nil {
			return err
		}
		ref := env.GetTenant()
		tenant, ok := authorized[ref]
		if !ok {
			_, t, aerr := g.s.grpcAuthorize(ctx, auth.PermIngestWrite, ref)
			if aerr != nil {
				return grpcError(aerr)
			}
			tenant = t
			authorized[ref] = t
		}
		obs, derr := sdkplugin.ObservationFromPB(env.GetObservation())
		if derr != nil {
			g.s.mIngestRej.Inc() // OBS-06: rejected after authorize (decode failure)
			return status.Error(codes.InvalidArgument, "ingest: "+derr.Error())
		}
		start := time.Now()
		if ierr := g.pub.Ingest(ctx, tenant.String(), env.GetSource(), obs); ierr != nil {
			g.s.mIngestRej.Inc() // OBS-06: rejected after authorize (publish/backpressure failure)
			return grpcError(ierr)
		}
		g.s.mIngestDur.Observe(time.Since(start).Seconds()) // OBS-06: ingest-latency SLI (rises under backpressure)
		g.s.mIngestObs.Inc(string(obs.ObservationType()))   // OBS-06: ingest throughput by kind
		accepted++
	}
}

// wrappedServerStream lets a stream interceptor replace the stream's context (the
// gRPC ServerStream.Context is otherwise read-only), so the authenticated
// principal reaches the handler exactly as the unary interceptor delivers it.
type wrappedServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *wrappedServerStream) Context() context.Context { return w.ctx }
