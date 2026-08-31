// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package hubble

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	observer "github.com/cilium/cilium/api/v1/observer"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/olivaresai/olivares/connectors/internal/tlsx"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique dotted identifier.
const Name = "olivares.hubble"

// SignalHubble is the connector's provenance value (an open-string SignalSource): an
// L7 flow observed by Cilium Hubble, distinct from the kernel "ebpf" backstop so the
// operator sees which plane corroborated an edge (ARCHITECTURE.md).
const SignalHubble model.SignalSource = "hubble"

// defaultRelayAddr is the Hubble Relay default gRPC port. The operator may instead
// point at a unix socket ("unix:///var/run/cilium/hubble.sock").
const defaultRelayAddr = "localhost:4245"

// Source is the Cilium Hubble flow observation connector. Open builds the (lazy) gRPC
// client; Gather streams Observer.GetFlows and emits an edge/finding per flow until
// ctx is canceled (a streaming source).
type Source struct {
	relayAddr     string
	tlsOpts       tlsx.Options
	allowInsecure bool

	conn   *grpc.ClientConn
	client observer.ObserverClient
	now    func() time.Time
}

// Compile-time proof that Source satisfies the source-connector contract.
var _ sdk.SourceConnector = (*Source)(nil)

// New returns a Hubble connector with default configuration (local relay, secure
// dial when TLS material is supplied). It is not usable until Open resolves config.
func New() *Source { return &Source{now: time.Now} }

// Descriptor returns the connector's self-description and declared configuration.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Cilium Hubble flows + FQDN policy",
		Description: "Observes Cilium Hubble L7 flows and egress allow/deny verdicts as read-first mesh edges/findings (extends the eBPF backstop)..",
		ConfigFields: []sdk.ConfigField{
			{Key: "relay_addr", Type: sdk.FieldString, Default: defaultRelayAddr, Description: "Hubble Relay gRPC target (host:port) or unix socket (unix:///path)."},
			{Key: "tls", Type: sdk.FieldBool, Default: "false", Description: "Connect over TLS. Recommended for a networked relay; with ca_file/cert_file/key_file enables mTLS."},
			{Key: "ca_file", Type: sdk.FieldString, Description: "PEM CA bundle to verify the relay (TLS)."},
			{Key: "cert_file", Type: sdk.FieldString, Description: "Client certificate for mutual TLS (with key_file)."},
			{Key: "key_file", Type: sdk.FieldString, Description: "Client private key for mutual TLS (with cert_file)."},
			{Key: "insecure_skip_verify", Type: sdk.FieldBool, Default: "false", Description: "Disable TLS verification — an explicit, documented opt-in, never a default."},
			{Key: "allow_insecure_remote", Type: sdk.FieldBool, Default: "false", Description: "Permit a plaintext connection to a NON-local relay. Off by default (secure default); plaintext is otherwise allowed only to a loopback/unix-socket relay."},
		},
	}
}

// Open parses configuration and builds the gRPC client (lazily; no network I/O). It
// refuses a plaintext connection to a non-local relay unless explicitly allowed
// (secure default, docs/SECURITY-HARDENING.md).
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	if s.now == nil {
		s.now = time.Now
	}
	s.relayAddr = strings.TrimSpace(cfg.Get("relay_addr"))
	if s.relayAddr == "" {
		s.relayAddr = defaultRelayAddr
	}
	// insecure_skip_verify IMPLIES TLS: without this, setting only insecure_skip_verify
	// (no tls/ca/cert) would leave tlsx unconfigured and silently fall back to plaintext,
	// ignoring the operator's intent. Folding it into Enable makes the flag take effect.
	insecureSkip := cfg.GetBool("insecure_skip_verify", false)
	s.tlsOpts = tlsx.Options{
		Enable:             cfg.GetBool("tls", false) || insecureSkip,
		CAFile:             strings.TrimSpace(cfg.Get("ca_file")),
		CertFile:           strings.TrimSpace(cfg.Get("cert_file")),
		KeyFile:            strings.TrimSpace(cfg.Get("key_file")),
		InsecureSkipVerify: insecureSkip,
	}
	s.allowInsecure = cfg.GetBool("allow_insecure_remote", false)

	tlsCfg, err := tlsx.Build(s.tlsOpts)
	if err != nil {
		return fmt.Errorf("hubble: %w", err)
	}
	var creds credentials.TransportCredentials
	if tlsCfg != nil {
		creds = credentials.NewTLS(tlsCfg)
	} else {
		if !relayIsLocal(s.relayAddr) && !s.allowInsecure {
			return fmt.Errorf("hubble: refusing plaintext connection to non-local relay %q (secure default); set tls=true or allow_insecure_remote=true to accept the risk", s.relayAddr)
		}
		creds = insecure.NewCredentials()
	}
	conn, err := grpc.NewClient(s.relayAddr, grpc.WithTransportCredentials(creds))
	if err != nil {
		return fmt.Errorf("hubble: build client for %s: %w", s.relayAddr, err)
	}
	s.conn = conn
	s.client = observer.NewObserverClient(conn)
	return nil
}

// Gather streams flows from Hubble Relay and emits an edge/finding per flow until ctx
// is canceled. It is a streaming source: it blocks in Recv and returns on cancel/EOF.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	if s.client == nil {
		return fmt.Errorf("hubble: connector not opened")
	}
	stream, err := s.client.GetFlows(ctx, &observer.GetFlowsRequest{Follow: true})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("hubble: GetFlows: %w", err)
	}
	for {
		resp, err := stream.Recv()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("hubble: recv: %w", err)
		}
		fl := resp.GetFlow()
		if fl == nil {
			continue
		}
		rec, ok := flowToRecord(fl, s.clock)
		if !ok {
			continue
		}
		if err := rec.Emit(ctx, sink); err != nil {
			return err
		}
	}
}

// Close releases the gRPC client connection. It is idempotent.
func (s *Source) Close(context.Context) error {
	if s.conn != nil {
		err := s.conn.Close()
		s.conn = nil
		return err
	}
	return nil
}

// clock returns the connector's time source (injectable in tests).
func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// relayIsLocal reports whether addr is a unix socket or a loopback host:port — the
// only targets to which a plaintext connection is accepted without an explicit opt-in.
func relayIsLocal(addr string) bool {
	if strings.HasPrefix(addr, "unix:") {
		return true
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
