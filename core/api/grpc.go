// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/olivaresai/olivares/core/api/genpb/apiv1"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	olv1 "github.com/olivaresai/olivares/sdk/plugin/genpb/olivaresv1"
)

// grpcService implements the ControlPlane gRPC service over the same Server, so
// gRPC reuses the exact authenticate → resolve-tenant → authorize path as REST.
type grpcService struct {
	s *Server
	apiv1.UnimplementedControlPlaneServer
}

// NewGRPCServer builds a *grpc.Server serving the ControlPlane service (and the
// standard health service) with the authentication interceptors installed — unary
// for the ControlPlane RPCs, streaming for the collector IngestService. When an
// ObservationPublisher was configured (Options.Ingest), the collector→core
// IngestService is registered too (CB-1 option C). The caller wraps it in TLS
// credentials and Serve()s it.
func (s *Server) NewGRPCServer(opts ...grpc.ServerOption) *grpc.Server {
	// Metrics first (outermost), so auth rejections are measured too (gRPC
	// SLIs); then authentication; then — only in the HA leader-routing layout —
	// the leader gate, so an invalid credential still reports Unauthenticated
	// rather than Unavailable (the REST chain orders them the same way).
	unary := []grpc.UnaryServerInterceptor{s.grpcMetricsUnaryInterceptor, s.grpcAuthInterceptor}
	stream := []grpc.StreamServerInterceptor{s.grpcMetricsStreamInterceptor, s.grpcStreamAuthInterceptor}
	if s.leaderRouteGate {
		unary = append(unary, s.grpcLeaderGateUnaryInterceptor)
		stream = append(stream, s.grpcLeaderGateStreamInterceptor)
	}
	opts = append(opts,
		grpc.ChainUnaryInterceptor(unary...),
		grpc.ChainStreamInterceptor(stream...),
	)
	gs := grpc.NewServer(opts...)
	apiv1.RegisterControlPlaneServer(gs, &grpcService{s: s})
	if s.ingest != nil {
		olv1.RegisterIngestServiceServer(gs, &ingestService{s: s, pub: s.ingest})
	}
	hs := health.NewServer()
	hs.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(gs, hs)
	return gs
}

// grpcAuthInterceptor authenticates the bearer token in request metadata and puts
// the principal in the context. A present-but-invalid token is rejected; an absent
// one leaves the request anonymous (GetServerInfo allows it).
func (s *Server) grpcAuthInterceptor(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok {
		if vals := md.Get("authorization"); len(vals) > 0 {
			token, hasPrefix := strings.CutPrefix(vals[0], "Bearer ")
			if !hasPrefix {
				return nil, grpcError(auth.ErrUnauthenticated)
			}
			p, err := s.authr.Authenticate(ctx, strings.TrimSpace(token))
			if err != nil {
				return nil, grpcError(auth.ErrUnauthenticated)
			}
			ctx = withPrincipal(ctx, p)
		}
	}
	return handler(ctx, req)
}

// grpcStreamAuthInterceptor authenticates a streaming RPC's bearer token (the
// IngestService Push) identically to the unary path: a present-but-invalid token
// is rejected, an absent one leaves the stream anonymous (and a per-RPC authorize
// then denies it). The authenticated principal is threaded to the handler through
// a context-replacing stream wrapper.
func (s *Server) grpcStreamAuthInterceptor(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	ctx := ss.Context()
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if vals := md.Get("authorization"); len(vals) > 0 {
			token, hasPrefix := strings.CutPrefix(vals[0], "Bearer ")
			if !hasPrefix {
				return grpcError(auth.ErrUnauthenticated)
			}
			p, err := s.authr.Authenticate(ctx, strings.TrimSpace(token))
			if err != nil {
				return grpcError(auth.ErrUnauthenticated)
			}
			ctx = withPrincipal(ctx, p)
		}
	}
	return handler(srv, &wrappedServerStream{ServerStream: ss, ctx: ctx})
}

// grpcLeaderGateExempt reports whether a full gRPC method name is operational —
// the standard health service, which a probe/mesh must reach on EVERY pod exactly
// as the kubelet reaches /livez and /pod-readyz. Everything else is application
// surface and is subject to the leader gate.
func grpcLeaderGateExempt(fullMethod string) bool {
	return strings.HasPrefix(fullMethod, "/grpc.health.v1.Health/")
}

// grpcLeaderGateUnaryInterceptor is the gRPC half of the HA leader-routing backstop
// (stage-2; the REST half is middleware.go leaderGate). In the leader-routing
// layout standbys are Pod-Ready and therefore dialable, so every application RPC
// re-checks leadership and returns the retryable codes.Unavailable / not_leader a
// caller already handles from the store's write fence. The predicate is IsLeader()
// (ESTABLISHED leadership), not Active(); the store's private bootstrap write gate
// can run during promotion, but neither public predicate publishes service yet.
func (s *Server) grpcLeaderGateUnaryInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	if !grpcLeaderGateExempt(info.FullMethod) && !s.st.Leader().IsLeader() {
		return nil, grpcError(store.ErrNotLeader)
	}
	return handler(ctx, req)
}

// grpcLeaderGateStreamInterceptor gates streaming RPCs — the collector's
// IngestService.Push above all. Checking only at stream CREATION would be a hole:
// a collector that opened its stream against the leader keeps pushing observations
// through the same long-lived stream after that pod is demoted, so two pods would
// be accepting application traffic at once (and the store's write fence does not
// cover the in-memory bus path). The stream is therefore wrapped so EVERY received
// message re-checks leadership and the stream fails closed the moment it is lost.
func (s *Server) grpcLeaderGateStreamInterceptor(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	if grpcLeaderGateExempt(info.FullMethod) {
		return handler(srv, ss)
	}
	if !s.st.Leader().IsLeader() {
		return grpcError(store.ErrNotLeader)
	}
	return handler(srv, &leaderGatedStream{ServerStream: ss, s: s})
}

// leaderGatedStream re-checks leadership on every inbound message of a long-lived
// application stream, so demotion ends the stream instead of silently letting a
// standby keep ingesting.
type leaderGatedStream struct {
	grpc.ServerStream
	s *Server
}

func (l *leaderGatedStream) RecvMsg(m any) error {
	if !l.s.st.Leader().IsLeader() {
		return grpcError(store.ErrNotLeader)
	}
	return l.ServerStream.RecvMsg(m)
}

// grpcAuthorize resolves the single canonical tenant from the request's tenant
// field and authorizes perm at COLLECTION level, after the setup gate. Identical
// semantics to REST's authzTenant.
func (s *Server) grpcAuthorize(ctx context.Context, perm auth.Permission, tenantStr string) (auth.Principal, model.TenantID, error) {
	return s.grpcAuthorizeResource(ctx, perm, tenantStr, auth.ResourceFor(perm))
}

// grpcAuthorizeEntity authorizes perm for a SPECIFIC entity id, seeding
// Resource.ID so the scoped-grant engine resolves the entity's scope from the
// store. The gRPC analog of REST's authzTenantEntity.
func (s *Server) grpcAuthorizeEntity(ctx context.Context, perm auth.Permission, tenantStr, id string) (auth.Principal, model.TenantID, error) {
	res := auth.ResourceFor(perm)
	res.ID = id
	return s.grpcAuthorizeResource(ctx, perm, tenantStr, res)
}

// grpcAuthorizeResource is the shared core: tenant resolution, the per-tenant rate
// limiter, and authorization of perm against res. Identical semantics to REST.
func (s *Server) grpcAuthorizeResource(ctx context.Context, perm auth.Permission, tenantStr string, res auth.ResourceAttrs) (auth.Principal, model.TenantID, error) {
	if !s.isSetupComplete(ctx) {
		return auth.Principal{}, "", errSetupRequired
	}
	p, ok := principalFrom(ctx)
	if !ok {
		return auth.Principal{}, "", auth.ErrUnauthenticated
	}
	tenant, err := s.resolveTenantValue(p, tenantStr)
	if err != nil {
		return auth.Principal{}, "", err
	}
	// rate-limit the gRPC ControlPlane surface with the SAME limiter, identity
	// and class keying as REST, so a tenant cannot escape per-tenant quotas by moving
	// from /v1 to gRPC (CreateAgent et al. reach the same store + audit hash-chain).
	// Runs before authorize, mirroring REST (the limiter precedes the handler's authz).
	// The collector IngestService (ingest:write) ALSO funnels through grpcAuthorize, but
	// is EXCLUDED here: it is a trusted mTLS push channel with its own flow-control
	// backpressure, high-volume by design — metering it against a tenant's API quota
	// would throttle observability, and (being memoized per-stream, ingest.go) would only
	// ever deny a reconnecting collector, never bound throughput. Returns errRateLimited
	// -> codes.ResourceExhausted; the retry hint + advisory limits ride response metadata.
	if s.rl != nil && perm != auth.PermIngestWrite {
		key, tier := s.rlIdentityFor(p, tenant)
		if rd := s.rl.Allow(ctx, key, tier, rlClassForPerm(perm)); !rd.OK {
			_ = grpc.SetHeader(ctx, metadata.Pairs(
				"retry-after", strconv.Itoa(rd.RetryAfter),
				"ratelimit-limit", strconv.Itoa(rd.Limit),
				"ratelimit-remaining", strconv.Itoa(rd.Remaining),
				"ratelimit-reset", strconv.Itoa(rd.Reset),
			))
			return auth.Principal{}, "", errRateLimited
		}
	}
	if dec := s.authz.Authorize(ctx, auth.Request{Principal: p, Permission: perm, Tenant: tenant, Resource: res}); !dec.Allow {
		return auth.Principal{}, "", errForbidden
	}
	return p, tenant, nil
}

func (g *grpcService) GetServerInfo(ctx context.Context, _ *apiv1.Empty) (*apiv1.ServerInfo, error) {
	lic := g.s.licenseStatus()
	return &apiv1.ServerInfo{
		Version:            g.s.version,
		Engine:             string(g.s.st.Engine()),
		SetupRequired:      !g.s.isSetupComplete(ctx),
		LicenseStatus:      lic.Status,
		LicenseLicensee:    lic.Licensee,
		LicensePlan:        lic.Plan,
		LicenseSupportTier: lic.SupportTier,
	}, nil
}

func (g *grpcService) ListAgents(ctx context.Context, req *apiv1.ListAgentsRequest) (*apiv1.ListAgentsResponse, error) {
	_, tenant, err := g.s.grpcAuthorize(ctx, "agent:read", req.GetTenant())
	if err != nil {
		return nil, grpcError(err)
	}
	out := &apiv1.ListAgentsResponse{}
	err = g.s.st.View(ctx, tenant, func(sc store.Scope) error {
		agents, page, err := sc.Agents().List(ctx, model.Query{Limit: int(req.GetLimit()), Cursor: req.GetCursor()})
		if err != nil {
			return err
		}
		for _, a := range agents {
			out.Agents = append(out.Agents, toPBAgent(a))
		}
		out.Cursor, out.HasMore = page.Cursor, page.HasMore
		return nil
	})
	if err != nil {
		return nil, grpcError(err)
	}
	return out, nil
}

func (g *grpcService) GetAgent(ctx context.Context, req *apiv1.GetAgentRequest) (*apiv1.Agent, error) {
	_, tenant, err := g.s.grpcAuthorizeEntity(ctx, "agent:read", req.GetTenant(), req.GetId())
	if err != nil {
		return nil, grpcError(err)
	}
	var out *apiv1.Agent
	err = g.s.st.View(ctx, tenant, func(sc store.Scope) error {
		a, err := sc.Agents().Get(ctx, model.ID(req.GetId()))
		if err != nil {
			return err
		}
		out = toPBAgent(a)
		return nil
	})
	if err != nil {
		return nil, grpcError(err)
	}
	return out, nil
}

func (g *grpcService) CreateAgent(ctx context.Context, req *apiv1.CreateAgentRequest) (*apiv1.Agent, error) {
	p, tenant, err := g.s.grpcAuthorize(ctx, "agent:write", req.GetTenant())
	if err != nil {
		return nil, grpcError(err)
	}
	var out *apiv1.Agent
	err = g.s.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		a := model.Agent{Name: req.GetName(), Kind: req.GetKind(), ExternalID: req.GetExternalId(),
			Status: model.LifecycleStatus(req.GetStatus())}
		if a.Status == "" {
			a.Status = model.StatusActive
		}
		created, err := sc.Agents().Create(ctx, a)
		if err != nil {
			return err
		}
		out = toPBAgent(created)
		return appendAudit(ctx, sc, p, "agent.create", "core.agent", created.ID)
	})
	if err != nil {
		return nil, grpcError(err)
	}
	return out, nil
}

func (g *grpcService) VerifyAudit(ctx context.Context, req *apiv1.VerifyAuditRequest) (*apiv1.VerifyAuditResponse, error) {
	_, tenant, err := g.s.grpcAuthorize(ctx, "audit:read", req.GetTenant())
	if err != nil {
		return nil, grpcError(err)
	}
	from := req.GetFrom()
	if from < 1 {
		from = 1
	}
	out := &apiv1.VerifyAuditResponse{}
	err = g.s.st.View(ctx, tenant, func(sc store.Scope) error {
		rep, err := sc.Audit().Verify(ctx, from)
		if err != nil {
			return err
		}
		cr, err := audit.VerifyCheckpoints(ctx, sc.Audit(), g.s.signer.PublicKey())
		if err != nil {
			return err
		}
		out.ChainOk, out.Checked = rep.OK, rep.Checked
		out.CheckpointsOk, out.CheckpointCount = cr.OK, int32(cr.Checkpoints)
		out.LatestAttestedSeq = cr.LatestAttestedSeq
		// THE SAME PREDICATE AS REST, not a second one that resembles it (2026-08-06).
		// This said "Mirror handleAuditVerify" and then computed something else:
		// `cr.OK || cr.Reason == "no-checkpoints"`, a string comparison, against REST's
		// `Status() != CheckpointStatusFailed`. Status() also requires Checkpoints == 0
		// before it will call a report pending, so the two disagree on any report that
		// carries checkpoints AND that reason — latent today, and a comment claiming
		// parity is exactly how it would stay unnoticed. Both surfaces now ask the same
		// typed question of the same value.
		cpStatus := cr.Status()
		out.CheckpointStatus = string(cpStatus)
		out.Ok = rep.OK && rep.Checked > 0 && cpStatus != audit.CheckpointStatusFailed
		return nil
	})
	if err != nil {
		return nil, grpcError(err)
	}
	return out, nil
}

func toPBAgent(a model.Agent) *apiv1.Agent {
	return &apiv1.Agent{
		Id: a.ID.String(), TenantId: a.TenantID.String(), Name: a.Name, Kind: a.Kind,
		ExternalId: a.ExternalID, Status: string(a.Status), Version: a.Version,
		CreatedAt: a.CreatedAt.String(), UpdatedAt: a.UpdatedAt.String(),
	}
}

// grpcError maps an internal error to a gRPC status using the same status table
// as REST (so the two surfaces never diverge), preserving not-found==other-tenant.
func grpcError(err error) error {
	httpStatus, code := statusFor(err)
	var c codes.Code
	switch httpStatus {
	case http.StatusUnauthorized:
		c = codes.Unauthenticated
	case http.StatusForbidden:
		c = codes.PermissionDenied
	case http.StatusNotFound:
		c = codes.NotFound
	case http.StatusConflict:
		c = codes.Aborted
	case http.StatusBadRequest:
		c = codes.InvalidArgument
	case http.StatusTooManyRequests:
		c = codes.ResourceExhausted
	case http.StatusLocked:
		// the tenant's service is withdrawn (store.ErrTenantSuspended), or a
		// governance stop is engaged. A deliberate refusal, not a server fault —
		// without this it fell through to codes.Internal and every suspended-tenant
		// gRPC call reported a commercial decision as a crash.
		c = codes.FailedPrecondition
	case http.StatusServiceUnavailable:
		// HA standby write-gate (store.ErrNotLeader): retryable against the
		// current leader, not an internal fault.
		c = codes.Unavailable
	case http.StatusNotImplemented:
		// an honest-seam refusal (a capability not configured on this
		// deployment) is Unimplemented, not Internal. Reporting it as Internal told
		// a gRPC client "this server is broken" about a refusal the engine made on
		// purpose — the same lie the message below used to tell.
		c = codes.Unimplemented
	default:
		c = codes.Internal
	}
	msg := err.Error()
	// this used to read `httpStatus >= 500 && httpStatus != 503`, which is the
	// shape writeError carried until 2026-08-05 and this twin kept. It was wrong in
	// BOTH directions, and the comment claiming "the same status table as REST (so
	// the two surfaces never diverge)" was true of the status and false of the
	// message:
	//
	//   - every honest-seam 501 was reported as "internal error", which is exactly
	//     the muteness the 2026-08-05 fix removed from REST; and
	//   - 503 echoed err.Error() VERBATIM, so a wrapped sentinel leaked its
	//     wrapper's text. The property errors_honestseam_test.go proves for REST —
	//     the message is keyed on the CODE and can never echo a wrapper — simply did
	//     not hold on this surface.
	//
	// Use the same curated table, so a deliberate refusal reads the same on both
	// surfaces and neither can leak. A genuine 500 still says nothing.
	switch {
	case httpStatus == http.StatusInternalServerError:
		msg = "internal error"
	case httpStatus > http.StatusInternalServerError:
		if m, ok := honestSeamMessage[code]; ok {
			msg = m
		} else {
			msg = humanisedCode(code)
		}
	}
	if errors.Is(err, errSetupRequired) {
		c = codes.FailedPrecondition
	}
	return status.Error(c, code+": "+msg)
}
