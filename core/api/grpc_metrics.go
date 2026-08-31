// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
)

// gRPC SLIs: per-RPC count/latency/error for the ControlPlane service,
// the collector IngestService and the standard health service — the first
// docs/17 §5 deferred gap. The interceptors sit OUTERMOST in the chain so an
// auth rejection is measured like any other outcome (an SLI that excluded
// auth failures would hide a credential-stuffing flood from the error ratio).
//
// The duration histogram is observed for UNARY RPCs only: the one streaming
// RPC (IngestService.Push) is a long-lived collector channel whose duration is
// its lifetime — every observation would land in the +Inf bucket and tick only
// at stream close, which is noise, not latency signal. Streams land in the
// request counter at completion; their per-observation latency SLI is
// olivares_ingest_duration_seconds.

// grpcMetricsUnaryInterceptor records count+latency+code for unary RPCs.
func (s *Server) grpcMetricsUnaryInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	start := time.Now() // monotonic: a wall-clock step must not skew a duration
	resp, err := handler(ctx, req)
	s.mGRPCDur.Observe(time.Since(start).Seconds(), info.FullMethod)
	s.mGRPCTotal.Inc(info.FullMethod, wireCode(err))
	return resp, err
}

// grpcMetricsStreamInterceptor records count+code for streaming RPCs at
// completion (no duration — see the package comment above).
func (s *Server) grpcMetricsStreamInterceptor(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	err := handler(srv, ss)
	s.mGRPCTotal.Inc(info.FullMethod, wireCode(err))
	return err
}

// wireCode labels err with the code the CLIENT will see: like the grpc server
// itself, a bare context error (a handler returning ctx.Err() directly, which
// status.Code would label Unknown) maps to Canceled/DeadlineExceeded. Every
// in-tree handler already returns status errors via grpcError; this keeps the
// label honest if one ever doesn't.
func wireCode(err error) string {
	if err == nil {
		return status.Code(nil).String() // "OK"
	}
	if s, ok := status.FromError(err); ok {
		return s.Code().String()
	}
	return status.FromContextError(err).Code().String()
}
