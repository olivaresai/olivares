// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package envoy

import (
	"context"
	"io"
	"time"

	aldata "github.com/envoyproxy/go-control-plane/envoy/data/accesslog/v3"
	accesslogv3 "github.com/envoyproxy/go-control-plane/envoy/service/accesslog/v3"

	"github.com/olivaresai/olivares/connectors/internal/meshobs"
	"github.com/olivaresai/olivares/connectors/internal/tracecontext"
)

// StreamAccessLogs is the Envoy gRPC AccessLogService server. Envoy
// client-streams its access logs; the connector turns each HTTP entry into an
// observed L7 edge and each TCP entry into a net.endpoint edge, then acknowledges the
// stream when Envoy closes it. It is observe-only: it never replies with anything but
// the terminal ack.
func (o *obsServer) StreamAccessLogs(stream accesslogv3.AccessLogService_StreamAccessLogsServer) error {
	ctx := stream.Context()
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			return stream.SendAndClose(&accesslogv3.StreamAccessLogsResponse{})
		}
		if err != nil {
			return err
		}
		if http := msg.GetHttpLogs(); http != nil {
			for _, e := range http.GetLogEntry() {
				if err := o.emitHTTPEntry(ctx, e); err != nil {
					return err
				}
			}
		}
		if tcp := msg.GetTcpLogs(); tcp != nil {
			for _, e := range tcp.GetLogEntry() {
				if err := o.emitTCPEntry(ctx, e); err != nil {
					return err
				}
			}
		}
	}
}

// emitHTTPEntry maps one HTTP access-log entry to an observed L7 edge: the FQDN is
// the request :authority, the mode comes from the HTTP method, and the identity comes
// from the mTLS peer certificate when present (Attributed) else the downstream
// address (Approximate).
func (o *obsServer) emitHTTPEntry(ctx context.Context, e *aldata.HTTPAccessLogEntry) error {
	req := e.GetRequest()
	rec := meshobs.Record{
		FQDN:   req.GetAuthority(),
		Method: req.GetRequestMethod().String(),
		Source: SignalEnvoyALS,
		Tool:   "envoy.als",
		// W3C Trace Context cross-hop (contract §D). ALS carries request
		// headers only when the operator opts in (custom_request_headers_to_log), so
		// this degrades to an empty (inert) trace context when absent.
		Trace:      tracecontext.FromHeaderMap(req.GetRequestHeaders()),
		ObservedAt: entryTime(e.GetCommonProperties(), o.now),
	}
	rec.OriginRef, rec.OriginVerified = alsOrigin(e.GetCommonProperties())
	return rec.Emit(ctx, o.sink)
}

// emitTCPEntry maps one TCP access-log entry to a net.endpoint edge: a TCP connection
// has no L7 authority, so the destination is the upstream remote address (falling back
// to the upstream cluster name) and the mode is the bidirectional-socket default.
func (o *obsServer) emitTCPEntry(ctx context.Context, e *aldata.TCPAccessLogEntry) error {
	c := e.GetCommonProperties()
	host, port := addrHostPort(c.GetUpstreamRemoteAddress())
	if host == "" {
		host = c.GetUpstreamCluster()
		port = 0
	}
	rec := meshobs.Record{
		FQDN:       host,
		Port:       port,
		Source:     SignalEnvoyALS,
		Tool:       "envoy.als.tcp",
		ObservedAt: entryTime(c, o.now),
	}
	rec.OriginRef, rec.OriginVerified = alsOrigin(c)
	return rec.Emit(ctx, o.sink)
}

// alsOrigin extracts the source identity from an access-log entry's common
// properties. A peer-certificate URI SAN (a SPIFFE SVID the mesh validated) or
// Subject is a CRYPTOGRAPHICALLY VERIFIED identity (Attributed); a bare downstream
// address is not an identity (Approximate). Empty when neither is present.
func alsOrigin(c *aldata.AccessLogCommon) (string, bool) {
	if tls := c.GetTlsProperties(); tls != nil {
		if pcp := tls.GetPeerCertificateProperties(); pcp != nil {
			for _, san := range pcp.GetSubjectAltName() {
				if uri := san.GetUri(); uri != "" {
					return uri, true
				}
			}
			if subj := pcp.GetSubject(); subj != "" {
				return subj, true
			}
		}
	}
	if host, _ := addrHostPort(c.GetDownstreamRemoteAddress()); host != "" {
		return host, false
	}
	return "", false
}

// entryTime returns the entry's start time, falling back to the connector clock when
// the access log omits it (so a re-emitted edge still has a natural-key timestamp).
func entryTime(c *aldata.AccessLogCommon, now func() time.Time) time.Time {
	if ts := c.GetStartTime(); ts != nil {
		return ts.AsTime()
	}
	if now != nil {
		return now()
	}
	return time.Now()
}
