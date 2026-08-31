// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package envoy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	accesslogv3 "github.com/envoyproxy/go-control-plane/envoy/service/accesslog/v3"
	authv3 "github.com/envoyproxy/go-control-plane/envoy/service/auth/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"google.golang.org/grpc"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
	"github.com/olivaresai/olivares/sdk/netbind"
)

// Name is the connector's globally unique dotted identifier.
const Name = "olivares.envoy"

// The package-local SignalSource values (open strings, ARCHITECTURE.md). Each Envoy
// observation surface is tagged distinctly so the operator sees WHICH plane produced
// an edge (an ALS log entry, an ext_authz decision, an ext_proc phase) rather than
// collapsing them — the same per-tech provenance the eBPF/pgAudit/CloudTrail sources
// use.
const (
	SignalEnvoyALS      model.SignalSource = "envoy_als"
	SignalEnvoyExtAuthz model.SignalSource = "envoy_ext_authz"
	SignalEnvoyExtProc  model.SignalSource = "envoy_ext_proc"
)

// The selectable observation services.
const (
	serviceALS      = "als"
	serviceExtAuthz = "ext_authz"
	serviceExtProc  = "ext_proc"
)

// defaultListenAddr is the loopback gRPC endpoint Envoy connects to by default. The
// operator points the mesh's ALS / ext_authz / ext_proc cluster at this address and
// overrides it as needed; a non-loopback override is refused unless allow_public_bind
// is set (secure default).
const defaultListenAddr = "127.0.0.1:5557"

// Source is the Envoy L7 observation connector. It binds a loopback gRPC listener in
// Open and, in Gather, serves the configured observation services until ctx is
// canceled (a streaming source). It holds no state between runs beyond the listener.
type Source struct {
	listenAddr      string
	services        map[string]bool
	allowPublicBind bool

	lis net.Listener
	srv *grpc.Server
	now func() time.Time
}

// Compile-time proof that Source satisfies the source-connector contract.
var _ sdk.SourceConnector = (*Source)(nil)

// New returns an Envoy observation connector with default configuration (ALS only,
// loopback bind). It is not usable until Open resolves its config.
func New() *Source { return &Source{now: time.Now} }

// Descriptor returns the connector's self-description and declared configuration.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Envoy mesh observation (ALS / ext_authz / ext_proc)",
		Description: "Observes Envoy L7 access logs (ALS), authorization checks (ext_authz, always-allow) and external processing (ext_proc, always-continue) as read-first mesh edges/findings..",
		ConfigFields: []sdk.ConfigField{
			{Key: "listen_addr", Type: sdk.FieldString, Default: defaultListenAddr, Description: "Loopback gRPC endpoint Envoy connects to for the enabled services. Non-loopback bind refused unless allow_public_bind=true."},
			{Key: "services", Type: sdk.FieldString, Default: serviceALS, Description: "Comma-separated set of observation services to host: als | ext_authz | ext_proc (default als)."},
			{Key: "allow_public_bind", Type: sdk.FieldBool, Default: "false", Description: "Accept a non-loopback listen_addr. Off by default (secure default); turning it on opens a port the mesh data plane reaches."},
		},
	}
}

// Open parses configuration, validates the service set, and binds the gRPC listener.
// It refuses a non-loopback bind unless allow_public_bind is set. Binding here
// (not in Gather) surfaces a misconfiguration at wiring time.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	if s.now == nil {
		s.now = time.Now
	}
	s.listenAddr = strings.TrimSpace(cfg.Get("listen_addr"))
	if s.listenAddr == "" {
		s.listenAddr = defaultListenAddr
	}
	s.allowPublicBind = cfg.GetBool("allow_public_bind", false)

	svcSpec := strings.TrimSpace(cfg.Get("services"))
	if svcSpec == "" {
		svcSpec = serviceALS
	}
	s.services = map[string]bool{}
	for _, raw := range strings.Split(svcSpec, ",") {
		svc := strings.ToLower(strings.TrimSpace(raw))
		switch svc {
		case serviceALS, serviceExtAuthz, serviceExtProc:
			s.services[svc] = true
		case "":
			// tolerate trailing/empty commas
		default:
			return fmt.Errorf("envoy: unknown service %q (want %s|%s|%s)", svc, serviceALS, serviceExtAuthz, serviceExtProc)
		}
	}
	if len(s.services) == 0 {
		return fmt.Errorf("envoy: no observation services enabled")
	}

	// One admission point for every socket this product opens. The xDS
	// endpoint speaks plaintext gRPC to the mesh data plane, so the decision is
	// made on that basis and BEFORE the syscall.
	lis, err := netbind.Listen(context.Background(), "tcp", s.listenAddr, netbind.Policy{
		Component:   "envoy",
		Purpose:     "xDS gRPC endpoint",
		AllowPublic: s.allowPublicBind,
		OptIn:       "allow_public_bind",
	})
	if err != nil {
		return fmt.Errorf("envoy: bind %s: %w", s.listenAddr, err)
	}
	s.lis = lis
	return nil
}

// Gather builds the gRPC server, registers the enabled observation services with
// handlers that emit to sink, and serves until ctx is canceled. It is a streaming
// source: it blocks in Serve and returns nil on graceful stop.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	if s.lis == nil {
		return fmt.Errorf("envoy: connector not opened")
	}
	srv := grpc.NewServer()
	s.srv = srv
	obs := &obsServer{sink: sink, now: s.clock}
	if s.services[serviceALS] {
		accesslogv3.RegisterAccessLogServiceServer(srv, obs)
	}
	if s.services[serviceExtAuthz] {
		authv3.RegisterAuthorizationServer(srv, obs)
	}
	if s.services[serviceExtProc] {
		extprocv3.RegisterExternalProcessorServer(srv, obs)
	}

	// Stop serving promptly on cancellation; GracefulStop drains in-flight RPCs.
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			srv.GracefulStop()
		case <-done:
		}
	}()
	err := srv.Serve(s.lis)
	close(done)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// Close stops the server (if running) and releases the listener. It is idempotent
// and safe to call before or after Gather; the runtime owns the single Close.
func (s *Source) Close(context.Context) error {
	if s.srv != nil {
		s.srv.Stop()
	}
	if s.lis != nil {
		err := s.lis.Close()
		s.lis = nil
		if err != nil && !errIsClosed(err) {
			return err
		}
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

// obsServer implements the three Envoy observation gRPC services. It embeds the
// generated Unimplemented servers (required by the mustEmbed contract and for
// forward-compatibility) and overrides the methods this connector observes. It holds
// the Gather sink and a clock; the per-service handlers live in als.go / extauthz.go
// / extproc.go.
type obsServer struct {
	accesslogv3.UnimplementedAccessLogServiceServer
	authv3.UnimplementedAuthorizationServer
	extprocv3.UnimplementedExternalProcessorServer

	sink sdk.Sink
	now  func() time.Time
}

// errIsClosed reports whether err is the benign "listener already closed" error, so
// an idempotent Close does not surface it.
func errIsClosed(err error) bool {
	return errors.Is(err, net.ErrClosed)
}

// headerMapToStrings flattens an Envoy core HeaderMap into a lowercase-keyed map. It
// prefers the string Value and falls back to the raw bytes; it is used only to read
// the method/authority/path pseudo-headers and the W3C traceparent — never to persist
// arbitrary header content (docs/SECURITY-HARDENING.md).
func headerMapToStrings(h *corev3.HeaderMap) map[string]string {
	if h == nil {
		return nil
	}
	out := make(map[string]string, len(h.GetHeaders()))
	for _, hv := range h.GetHeaders() {
		key := strings.ToLower(hv.GetKey())
		if key == "" {
			continue
		}
		val := hv.GetValue()
		if val == "" {
			val = string(hv.GetRawValue())
		}
		out[key] = val
	}
	return out
}

// addrHostPort returns the host and port of an Envoy socket Address, or ("", 0) when
// absent. It is used to render a TCP access-log entry as a net.endpoint resource ref
// (the eBPF-compatible "tcp://host:port" scheme).
func addrHostPort(a *corev3.Address) (string, int) {
	if sa := a.GetSocketAddress(); sa != nil {
		return sa.GetAddress(), int(sa.GetPortValue())
	}
	return "", 0
}
