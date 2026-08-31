// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package envoy

import (
	"context"
	"strings"

	authv3 "github.com/envoyproxy/go-control-plane/envoy/service/auth/v3"
	rpcstatus "google.golang.org/genproto/googleapis/rpc/status"

	"github.com/olivaresai/olivares/connectors/internal/meshobs"
	"github.com/olivaresai/olivares/connectors/internal/tracecontext"
)

// Check is the Envoy external authorization server. It OBSERVES the
// request and ALWAYS RETURNS OK — it is read-first, never an inline enforcer
// (docs/SECURITY-HARDENING.md): the operator runs the filter with failure_mode_allow so this
// collector can never block production. The verdict it records is the observation,
// not a decision. It NEVER returns a DeniedResponse (a test asserts this invariant),
// and an observation failure NEVER fails the (allowed) request.
func (o *obsServer) Check(ctx context.Context, req *authv3.CheckRequest) (*authv3.CheckResponse, error) {
	o.observeCheck(ctx, req)
	return &authv3.CheckResponse{
		// codes.OK == 0; an OK status + OkHttpResponse lets the request proceed.
		Status:       &rpcstatus.Status{Code: 0},
		HttpResponse: &authv3.CheckResponse_OkResponse{OkResponse: &authv3.OkHttpResponse{}},
	}, nil
}

// observeCheck emits the observed L7 edge for an ext_authz request. It is best-effort:
// any sink error is swallowed so observation can never turn an allowed request into a
// failure (read-first, docs/SECURITY-HARDENING.md).
func (o *obsServer) observeCheck(ctx context.Context, req *authv3.CheckRequest) {
	http := req.GetAttributes().GetRequest().GetHttp()
	if http == nil {
		return
	}
	headers := http.GetHeaders()
	rec := meshobs.Record{
		FQDN:       http.GetHost(),
		Method:     http.GetMethod(),
		Source:     SignalEnvoyExtAuthz,
		Tool:       "envoy.ext_authz",
		Trace:      tracecontext.FromHeaderMap(headers),
		ObservedAt: o.now(),
	}
	rec.OriginRef, rec.OriginVerified = peerOrigin(req.GetAttributes().GetSource())
	_ = rec.Emit(ctx, o.sink)
}

// peerOrigin extracts the source identity from an ext_authz peer. A Principal is the
// mTLS-authenticated identity (a SPIFFE URI the mesh validated) — Attributed; a bare
// Service name is descriptive, not cryptographically verified — Approximate. Empty
// when the peer carries neither.
func peerOrigin(p *authv3.AttributeContext_Peer) (string, bool) {
	if pr := strings.TrimSpace(p.GetPrincipal()); pr != "" {
		return pr, true
	}
	if svc := strings.TrimSpace(p.GetService()); svc != "" {
		return svc, false
	}
	return "", false
}
