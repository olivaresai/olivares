// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package plugin

import (
	"context"
	"errors"
	"fmt"

	goplugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"

	"github.com/olivaresai/olivares/sdk"
	pb "github.com/olivaresai/olivares/sdk/plugin/genpb/olivaresv1"
)

// OutputPlugin adapts an OutputConnector to hashicorp/go-plugin. On the plugin
// side Impl is the real connector; on the host side it is nil and GRPCClient
// returns a client satisfying sdk.OutputConnector.
type OutputPlugin struct {
	goplugin.NetRPCUnsupportedPlugin
	// Impl is the connector served by a plugin process; nil on the host.
	Impl sdk.OutputConnector
}

var _ goplugin.GRPCPlugin = (*OutputPlugin)(nil)

// GRPCServer registers the connector's gRPC service on the plugin side.
func (p *OutputPlugin) GRPCServer(_ *goplugin.GRPCBroker, s *grpc.Server) error {
	pb.RegisterOutputServiceServer(s, &outputServer{impl: p.Impl})
	return nil
}

// GRPCClient builds the host-side adapter, caching the descriptor (see
// SourcePlugin.GRPCClient for why the fetch is eager).
func (p *OutputPlugin) GRPCClient(ctx context.Context, _ *goplugin.GRPCBroker, c *grpc.ClientConn) (any, error) {
	client := pb.NewOutputServiceClient(c)
	dctx, cancel := context.WithTimeout(ctx, describeTimeout)
	defer cancel()
	resp, err := client.Describe(dctx, &pb.Empty{})
	if err != nil {
		return nil, fmt.Errorf("plugin: output describe failed at dispense: %w", err)
	}
	return &outputClient{client: client, desc: descriptorFromPB(resp.GetDescriptor_())}, nil
}

// --- server side (runs in the plugin process) ---------------------------------

type outputServer struct {
	pb.UnimplementedOutputServiceServer
	impl sdk.OutputConnector
}

func (s *outputServer) Describe(_ context.Context, _ *pb.Empty) (*pb.DescribeResponse, error) {
	return &pb.DescribeResponse{Descriptor_: descriptorToPB(s.impl.Descriptor())}, nil
}

func (s *outputServer) Open(ctx context.Context, req *pb.OpenRequest) (*pb.Empty, error) {
	if err := s.impl.Open(ctx, configFromPB(req.GetConfig())); err != nil {
		return nil, err
	}
	return &pb.Empty{}, nil
}

func (s *outputServer) Notify(ctx context.Context, req *pb.NotifyRequest) (*pb.NotifyResponse, error) {
	err := s.impl.Notify(ctx, notificationFromPB(req.GetNotification()))
	if err == nil {
		return &pb.NotifyResponse{Outcome: uint32(sdk.OutcomeDelivered)}, nil
	}
	// Carry the VERDICT across the process boundary alongside the error. A gRPC
	// error is only a string once it crosses, so without this the host could not
	// tell a deterministic refusal from a temporary outage and retried both — which
	// for OTLP the specification forbids outright. A plugin that returns a plain
	// error still works: ReportFor yields indeterminate, the safe reading, and the
	// host keeps its previous behavior.
	r := sdk.ReportFor(err)
	return &pb.NotifyResponse{
		Outcome:       uint32(r.Outcome),
		Sent:          int32(r.Sent),
		Rejected:      int32(r.Rejected),
		Locator:       uint32(r.Locator),
		FirstRejected: int32(r.FirstRejected),
		Code:          int32(r.Code),
		ErrorMessage:  err.Error(),
	}, nil
}

func (s *outputServer) Close(ctx context.Context, _ *pb.Empty) (*pb.Empty, error) {
	if err := s.impl.Close(ctx); err != nil {
		return nil, err
	}
	return &pb.Empty{}, nil
}

// --- client side (runs in the host process) -----------------------------------

type outputClient struct {
	client pb.OutputServiceClient
	desc   sdk.Descriptor
}

var _ sdk.OutputConnector = (*outputClient)(nil)

func (c *outputClient) Descriptor() sdk.Descriptor { return c.desc }

func (c *outputClient) Open(ctx context.Context, cfg sdk.Config) error {
	_, err := c.client.Open(ctx, &pb.OpenRequest{Config: configToPB(cfg)})
	return err
}

func (c *outputClient) Notify(ctx context.Context, n sdk.Notification) error {
	res, err := c.client.Notify(ctx, &pb.NotifyRequest{Notification: notificationToPB(n)})
	if err != nil {
		// A TRANSPORT fault: the plugin crashed, the stream broke, the deadline
		// expired. It says nothing about what any destination did, so it stays an
		// ordinary error and reads as indeterminate — retryable, which is what an
		// unreachable plugin should be.
		return fmt.Errorf("plugin notify: %w", err)
	}
	// An application-level verdict rides in the RESPONSE, never beside a gRPC error:
	// gRPC discards the response message when the handler returns an error, so a
	// report returned that way would be lost at exactly the boundary it exists to
	// cross. Rebuilding it here makes errors.As behave identically for an
	// out-of-process plugin and an in-process connector.
	if res.GetErrorMessage() == "" {
		return nil
	}
	return sdk.NewDeliveryError(sdk.DeliveryReport{
		Outcome:       sdk.DeliveryOutcome(res.GetOutcome()),
		Sent:          int(res.GetSent()),
		Rejected:      int(res.GetRejected()),
		Locator:       sdk.RejectionLocator(res.GetLocator()),
		FirstRejected: int(res.GetFirstRejected()),
		Code:          int(res.GetCode()),
	}, errors.New(res.GetErrorMessage()))
}

func (c *outputClient) Close(ctx context.Context) error {
	_, err := c.client.Close(ctx, &pb.Empty{})
	return err
}
