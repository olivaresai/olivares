// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	a2a "github.com/olivaresai/olivares/connectors/a2a"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

// realStoreInbound is the production composition with a C5 authorizer bound
// for a test only, a stored inbound A2A spec and a user-owned inbound route
// through the real router.
type realStoreInbound struct {
	e      *consentEstate
	sm     *sessions.Module
	peer   string
	kernel *joinedReplayKernel
	router *a2aInboundRouter
}

func bootRealStoreInbound(t *testing.T, engineName, label string) realStoreInbound {
	t.Helper()
	e := bootMessagingEstate(t, engineName)
	g := e.comm
	sm := e.eng.sessionsMod
	sm.UseCommunicationCoreEntityOperationAuthorizer(liveEpochOperationAuthorizer{st: e.eng.store})
	sm.UseProtocolBindingSpecValidator(sessions.BindingProtocolA2A, acceptingA2ASpecValidator{})
	peer := "https://" + label + "-" + engineName + ".example"
	spec := activateA2AInboundSpec(t, sm, e.tT, g.handoffs.workspace, peer)
	kernel := &joinedReplayKernel{a2aInboundKernel: sm, spec: spec}
	router, err := newA2AInboundRouter(kernel, []a2aInboundRouteConfig{{
		PeerAuthority: peer, Tenant: e.tT.String(), WorkspaceID: g.handoffs.workspace.String(),
		BindingSpecID: spec.ID.String(), BindingSpecGeneration: spec.Generation,
		ChannelID: g.handoffs.channelID.String(), SenderUserID: g.sender.id.String(),
		RecipientUserID: g.control.id.String(),
		OwnerKind:       "user", OwnerRef: g.control.id.String(),
	}}, []string{peer})
	if err != nil {
		t.Fatalf("new router: %v", err)
	}
	return realStoreInbound{e: e, sm: sm, peer: peer, kernel: kernel, router: router}
}

func (r realStoreInbound) message(label string) a2a.InboundMessage {
	return a2a.InboundMessage{
		PeerAuthority: r.peer, PeerSubject: "peer-agent", Protocol: a2a.ProtocolVersion,
		MessageID: label + "-message", ContextID: label + "-context", Role: "ROLE_AGENT",
		Parts: []a2a.InboundPart{
			{Kind: "text", Text: "Perform the routed work.", Digest: strings.Repeat("a", 64)},
		},
		ReplayID: label + "-jti", ReplayExpiresAt: time.Now().UTC().Add(time.Hour),
	}
}

func (r realStoreInbound) route(message a2a.InboundMessage) (a2a.InboundResult, time.Duration, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	started := time.Now()
	result, err := r.router.RouteInboundA2A(ctx, message)
	return result, time.Since(started), err
}

// ownerStandingReads counts the standing reads that name owner, through the
// store's own authenticator.
type ownerStandingReads struct {
	next  auth.StandingReader
	owner model.ID

	mu    sync.Mutex
	reads int
}

func (s *ownerStandingReads) Standing(
	ctx context.Context, tenant model.TenantID, users []model.ID,
) (map[model.ID]auth.Standing, error) {
	for _, user := range users {
		if user == s.owner {
			s.mu.Lock()
			s.reads++
			s.mu.Unlock()
			break
		}
	}
	return s.next.Standing(ctx, tenant, users)
}

func (s *ownerStandingReads) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}

// channelGrantAfterReplyAttestation runs grant once, after the first audience
// attestation of a protocol-sourced publish: the last evidence read of an
// inbound reply's preparation, which the owning transaction's replay of the
// record never repeats. So grant runs between the preparation and the owning
// transaction.
type channelGrantAfterReplyAttestation struct {
	next  sessions.PublicationAudienceAttestor
	grant func()

	mu    sync.Mutex
	fired bool
}

func (a *channelGrantAfterReplyAttestation) AttestPublicationAudience(
	ctx context.Context, request sessions.PublicationAudienceRequest,
) (sessions.DirectorySnapshot, sessions.PublicationAudienceAttestation, error) {
	snapshot, attestation, err := a.next.AttestPublicationAudience(ctx, request)
	if err == nil && request.SourceKind == sessions.RouteSourceProtocol {
		a.mu.Lock()
		fire := !a.fired
		a.fired = true
		a.mu.Unlock()
		if fire {
			a.grant()
		}
	}
	return snapshot, attestation, err
}

func (a *channelGrantAfterReplyAttestation) hasFired() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.fired
}

// Missing credential authority stops before publication audience attestation.
// Installing a legacy C5 port cannot make the channel-grant hook reachable.
func TestAJoinedInboundWithoutCredentialCannotReachChannelGrantAttestation(t *testing.T) {
	for _, engineName := range []string{"sqlite", "postgres"} {
		t.Run(engineName, func(t *testing.T) {
			r := bootRealStoreInbound(t, engineName, "channel-grant")
			g := r.e.comm
			extra := createCommunicationHTTPTestUser(t, r.e.eng, r.e.admin, r.e.tT,
				"channel-grant-"+engineName+"@messaging.test", auth.RoleEditor)
			standing := &ownerStandingReads{next: auth.NewAuthenticator(r.e.eng.store, nil), owner: g.control.id}
			r.sm.UseStanding(standing)
			lifecycle, ok := r.e.eng.nhiEnforcer.(workAgentLifecycle)
			if !ok {
				t.Fatalf("booted governance module %T does not serve agent lifecycle", r.e.eng.nhiEnforcer)
			}
			reads := newCommunicationDirectoryReads(r.e.eng.store, r.sm, lifecycle)
			resolver := newCommunicationDirectoryResolver(newDirectoryScopeRunner(r.e.eng.store), reads, time.Now)
			attestor, err := sessions.NewCommunicationPublicationAudienceAttestor(r.sm, resolver, nil)
			if err != nil {
				t.Fatalf("build the store's publication attestor: %v", err)
			}
			hook := &channelGrantAfterReplyAttestation{next: attestor, grant: func() {
				g.handoffs.grantChannel(t, map[string]any{"kind": "user", "ref": extra.id}, "the channel-grant move")
			}}
			r.sm.UseCommunicationPublicationAudienceAttestor(hook)

			before, protocol := communicationHTTPTestEffects(t, r.e.eng, r.e.tT), a2aProtocolRows(t, r.e)
			_, elapsed, err := r.route(r.message("channel-grant"))
			requireA2ACredentialHold(t, err, elapsed)
			if hook.hasFired() {
				t.Fatal("unbound protocol reached publication attestation")
			}
			requireA2ANoEffects(t, r.e, before, protocol)
		})
	}
}

// The reply fixture creates its durable task through the work and binding APIs.
// It does not depend on the inbound protocol effect that is deliberately refused.
func TestAPushReplyWithoutCredentialIsRefusedWithinTheBound(t *testing.T) {
	for _, engineName := range []string{"sqlite", "postgres"} {
		t.Run(engineName, func(t *testing.T) {
			r := bootRealStoreInbound(t, engineName, "push-reply")
			g := r.e.comm
			work := createVacantHandoffWork(t, g.handoffs, g.sender, "user", g.control.id.String(), "A2A reply fixture")
			ownerDigest := sha256.Sum256([]byte("user\x00" + g.control.id.String()))
			binding, err := r.sm.ReserveProtocolBinding(context.Background(), r.e.tT, sessions.ProtocolBindingReservation{
				WorkspaceID: g.handoffs.workspace, BindingSpecID: r.kernel.spec.ID,
				BindingSpecGeneration: r.kernel.spec.Generation, ExpectedDirection: sessions.BindingInbound,
				WorkItemID: work.id, DispatchKey: "push-reply-fixture", ExpectedExternalKind: "task", Generation: 1,
				OwnerKind: "user", OwnerRef: g.control.id.String(), OwnerDigest: ownerDigest[:], OwnerEpoch: 1,
			})
			if err != nil {
				t.Fatalf("reserve reply fixture: %v", err)
			}
			binding, err = r.sm.SettleProtocolBinding(context.Background(), r.e.tT, sessions.ProtocolBindingSettlement{
				BindingID: binding.ID, Generation: binding.Generation, ExpectedVersion: binding.Version,
				DispatchKey: "push-reply-fixture", ResultKind: sessions.ProtocolBindingResultTask,
				ExternalID: binding.ID.String(), ContextID: "push-reply-context", LocalState: "active", RemoteState: "submitted",
				Verdict: sessions.ProtocolObservationClean, Code: "fixture_accepted", Observed: true, DetailHash: ownerDigest[:],
			})
			if err != nil {
				t.Fatalf("settle reply fixture: %v", err)
			}
			r.sm.UseCommunicationCoreEntityOperationAuthorizer(nil)
			before, protocol := communicationHTTPTestEffects(t, r.e.eng, r.e.tT), a2aProtocolRows(t, r.e)
			settlement := &a2aPushSettlement{store: r.sm, routes: map[string]parsedA2APushRoute{
				r.peer: {tenant: r.e.tT, workspace: g.handoffs.workspace, interrupt: sessions.ProtocolInterruptRoute{
					ChannelID: g.handoffs.channelID, SenderUserID: g.sender.id, RecipientUserID: g.control.id,
				}},
			}}
			reply := a2a.ReplyEvent{
				Kind: a2a.ReplyEventMessage, TaskID: binding.ExternalID, ContextID: binding.ContextID,
				MessageID: "push-reply-remote-message",
				Parts: []a2a.MessageResultPart{{
					Kind: "text", Text: "The remote result.", Digest: strings.Repeat("c", 64),
				}},
				Digest: strings.Repeat("d", 64), Sender: r.peer,
				ReplayID: "push-reply-jti", ReplayExpiresAt: time.Now().UTC().Add(time.Hour),
			}
			for range []string{"first", "again"} {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				started := time.Now()
				err := settlement.RecordReply(ctx, reply)
				elapsed := time.Since(started)
				cancel()
				requireA2ACredentialHold(t, err, elapsed)
				requireA2ANoEffects(t, r.e, before, protocol)
			}
		})
	}
}

// m2: only an authority that moved on both attempts of a prepared replay maps
// to -32007, a retryable answer the peer can resend; any other unavailable
// evidence keeps its answer, and a claim conflict stays -32006.
func TestOnlyAMovedAuthorityMapsToTheRetryableInboundCode(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code int
	}{
		{name: "authority moved", err: fmt.Errorf("prepared replay: %w",
			fmt.Errorf("%w: auth: a pinned authority moved", sessions.ErrProtocolReplayAuthorityMoved)), code: -32007},
		{name: "claim conflict", err: fmt.Errorf("%w: claim_conflict", sessions.ErrProtocolReplayConflict), code: -32006},
		{name: "observation unavailable", err: fmt.Errorf("%w: observation_unavailable", sessions.ErrProtocolReplayUnknown), code: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var routed *a2a.InboundRouteError
			mapped := normalizeInboundA2AError(tc.err)
			got := 0
			if errors.As(mapped, &routed) {
				got = routed.Code
			}
			if got != tc.code {
				t.Fatalf("%v maps to code %d (%v), want %d", tc.err, got, mapped, tc.code)
			}
		})
	}
}
