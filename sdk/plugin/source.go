// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package plugin

import (
	"context"
	"fmt"
	"io"
	"time"

	goplugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
	pb "github.com/olivaresai/olivares/sdk/plugin/genpb/olivaresv1"
)

// describeTimeout bounds the eager Describe call made when a plugin client is
// dispensed, so a hung plugin surfaces as a connect error instead of blocking
// the engine forever.
const describeTimeout = 30 * time.Second

// SourcePlugin adapts a SourceConnector to hashicorp/go-plugin. On the plugin
// (server) side Impl is the real connector; on the host (client) side Impl is
// nil and GRPCClient returns a client that satisfies sdk.SourceConnector.
type SourcePlugin struct {
	goplugin.NetRPCUnsupportedPlugin
	// Impl is the connector served by a plugin process; nil on the host.
	Impl sdk.SourceConnector
}

var _ goplugin.GRPCPlugin = (*SourcePlugin)(nil)

// GRPCServer registers the connector's gRPC service on the plugin side.
func (p *SourcePlugin) GRPCServer(_ *goplugin.GRPCBroker, s *grpc.Server) error {
	pb.RegisterSourceServiceServer(s, &sourceServer{impl: p.Impl})
	return nil
}

// GRPCClient builds the host-side adapter. It eagerly fetches and caches the
// descriptor (sdk.SourceConnector.Descriptor has no error return, so a failure
// must surface here, at dispense time, not later).
func (p *SourcePlugin) GRPCClient(ctx context.Context, _ *goplugin.GRPCBroker, c *grpc.ClientConn) (any, error) {
	client := pb.NewSourceServiceClient(c)
	dctx, cancel := context.WithTimeout(ctx, describeTimeout)
	defer cancel()
	resp, err := client.Describe(dctx, &pb.Empty{})
	if err != nil {
		return nil, fmt.Errorf("plugin: source describe failed at dispense: %w", err)
	}
	return &sourceClient{client: client, desc: descriptorFromPB(resp.GetDescriptor_())}, nil
}

// --- server side (runs in the plugin process) ---------------------------------

type sourceServer struct {
	pb.UnimplementedSourceServiceServer
	impl sdk.SourceConnector
}

func (s *sourceServer) Describe(_ context.Context, _ *pb.Empty) (*pb.DescribeResponse, error) {
	return &pb.DescribeResponse{Descriptor_: descriptorToPB(s.impl.Descriptor())}, nil
}

func (s *sourceServer) Open(ctx context.Context, req *pb.OpenRequest) (*pb.Empty, error) {
	if err := s.impl.Open(ctx, configFromPB(req.GetConfig())); err != nil {
		return nil, err
	}
	return &pb.Empty{}, nil
}

// Gather runs the real connector, streaming each emitted observation back to the
// host. The connector's context is the stream's context, so when the host
// cancels the Gather RPC (e.g. on shutdown) the connector's Gather sees ctx done.
func (s *sourceServer) Gather(_ *pb.Empty, stream grpc.ServerStreamingServer[pb.Observation]) error {
	sink := &streamSink{stream: stream}
	return s.impl.Gather(stream.Context(), sink)
}

func (s *sourceServer) Close(ctx context.Context, _ *pb.Empty) (*pb.Empty, error) {
	if err := s.impl.Close(ctx); err != nil {
		return nil, err
	}
	return &pb.Empty{}, nil
}

// streamSink is the Sink handed to the connector on the plugin side; Emit
// encodes the observation and sends it back over the gRPC stream. An observation
// the wire cannot encode is a contract error returned to the connector, never a
// silently dropped fact.
type streamSink struct {
	stream grpc.ServerStreamingServer[pb.Observation]
}

var _ sdk.Sink = (*streamSink)(nil)

func (s *streamSink) Emit(_ context.Context, obs model.Observation) error {
	msg, err := observationToPB(obs)
	if err != nil {
		return err
	}
	return s.stream.Send(msg)
}

// --- client side (runs in the host process) -----------------------------------

type sourceClient struct {
	client pb.SourceServiceClient
	desc   sdk.Descriptor
}

var _ sdk.SourceConnector = (*sourceClient)(nil)

func (c *sourceClient) Descriptor() sdk.Descriptor { return c.desc }

func (c *sourceClient) Open(ctx context.Context, cfg sdk.Config) error {
	_, err := c.client.Open(ctx, &pb.OpenRequest{Config: configToPB(cfg)})
	return err
}

func (c *sourceClient) Gather(ctx context.Context, sink sdk.Sink) error {
	stream, err := c.client.Gather(ctx, &pb.Empty{})
	if err != nil {
		return err
	}
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		obs, err := observationFromPB(msg)
		if err != nil {
			return err
		}
		if err := sink.Emit(ctx, obs); err != nil {
			return err
		}
	}
}

func (c *sourceClient) Close(ctx context.Context) error {
	_, err := c.client.Close(ctx, &pb.Empty{})
	return err
}
