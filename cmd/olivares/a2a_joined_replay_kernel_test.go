// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	a2a "github.com/olivaresai/olivares/connectors/a2a"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

// joinedReplayKernelBound is the time an A2A delivery must be answered within
// on the production composition. A read that waited on the owning replay
// transaction's connection would take the delivery's whole context instead.
const joinedReplayKernelBound = 2 * time.Second

// c5Hold is the refusal every workflow publish answers while the Community
// composition binds no C5 operation authorizer.
const c5Hold = "C5 operation authorizer is unavailable"

// T5: on the production composition, which binds no C5 operation authorizer,
// a push interrupt meets that hold within the bound and before its replay
// writes anything: the same delivery sent again is refused the same way, not
// answered as a replay. O2 does not lift the hold; it answers it promptly.
func TestAPushInterruptMeetsTheC5HoldWithinTheBound(t *testing.T) {
	for _, engineName := range []string{"sqlite", "postgres"} {
		t.Run(engineName, func(t *testing.T) {
			e := bootMessagingEstate(t, engineName)
			g := e.comm
			peer := "https://push-peer.example"
			settlement := &a2aPushSettlement{store: e.eng.sessionsMod, routes: map[string]parsedA2APushRoute{
				peer: {tenant: e.tT, workspace: g.handoffs.workspace, interrupt: sessions.ProtocolInterruptRoute{
					ChannelID: g.handoffs.channelID, SenderUserID: g.sender.id, RecipientUserID: g.control.id,
				}},
			}}
			for _, state := range []a2a.TaskState{a2a.TaskStateInputReq, a2a.TaskStateAuthRequired} {
				update := a2a.TaskUpdate{
					TaskID: "push-task-" + string(state), ContextID: "push-context", State: state, Interrupt: true,
					Sender: peer, ReplayID: "push-jti-" + string(state), ReplayExpiresAt: time.Now().UTC().Add(time.Hour),
				}
				for _, delivery := range []string{"first", "again"} {
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					started := time.Now()
					err := settlement.Record(ctx, update)
					elapsed := time.Since(started)
					cancel()
					if !errors.Is(err, sessions.ErrCommunicationEvidenceUnknown) ||
						!strings.Contains(fmt.Sprint(err), c5Hold) || elapsed >= joinedReplayKernelBound {
						t.Fatalf("%s delivery of %s answered %v after %s, want %q within %s",
							delivery, state, err, elapsed.Round(time.Millisecond), c5Hold, joinedReplayKernelBound)
					}
				}
			}
		})
	}
}

// joinedReplayKernel is the production sessions kernel behind the inbound
// router. It answers the binding spec lookup the router makes before any
// replay from the operator's spec, and records every work command Apply and
// the answer of the prepared replay.
type joinedReplayKernel struct {
	a2aInboundKernel
	spec sessions.ProtocolBindingSpec

	mu        sync.Mutex
	applies   int
	replayErr error
}

func (k *joinedReplayKernel) GetProtocolBindingSpec(
	context.Context, model.TenantID, model.ID,
) (sessions.ProtocolBindingSpec, error) {
	return k.spec, nil
}

func (k *joinedReplayKernel) Apply(
	ctx context.Context, tenant model.TenantID, principal sessions.WorkPrincipal, cmd sessions.WorkCommand,
) (sessions.CommandResult, error) {
	k.mu.Lock()
	k.applies++
	k.mu.Unlock()
	return k.a2aInboundKernel.Apply(ctx, tenant, principal, cmd)
}

func (k *joinedReplayKernel) ApplyPreparedProtocolReplay(
	ctx context.Context,
	tenant model.TenantID,
	claim sessions.ProtocolReplayClaim,
	plan sessions.ProtocolReplayPlan,
	mutation sessions.ProtocolReplayMutation,
) (sessions.ProtocolReplayResult, error) {
	result, err := k.a2aInboundKernel.ApplyPreparedProtocolReplay(ctx, tenant, claim, plan, mutation)
	k.mu.Lock()
	k.replayErr = err
	k.mu.Unlock()
	return result, err
}

// T6: on the production composition, an inbound A2A message meets the C5 hold
// within the bound, for a user-owned and an agent-owned route. The peer sees
// -32005; the sessions answer names the hold, and no work command ran, so the
// owner's resolution and the reply's evidence were read before the owning
// transaction rather than inside it.
func TestAnInboundMessageMeetsTheC5HoldBeforeAnyWork(t *testing.T) {
	e := bootMessagingEstate(t, "sqlite")
	g := e.comm
	for _, owner := range []struct{ kind, ref string }{
		{kind: "user", ref: g.control.id.String()},
		{kind: "agent", ref: model.NewID().String()},
	} {
		t.Run(owner.kind, func(t *testing.T) {
			peer := "https://inbound-" + owner.kind + ".example"
			specID := model.NewID()
			kernel := &joinedReplayKernel{
				a2aInboundKernel: e.eng.sessionsMod,
				spec: a2aInboundSpecForTest(
					t, e.tT, g.handoffs.workspace, specID, peer, 3, sessions.ProtocolBindingSpecActive,
				),
			}
			router, err := newA2AInboundRouter(kernel, []a2aInboundRouteConfig{{
				PeerAuthority: peer, Tenant: e.tT.String(), WorkspaceID: g.handoffs.workspace.String(),
				BindingSpecID: specID.String(), BindingSpecGeneration: 3,
				ChannelID: g.handoffs.channelID.String(), SenderUserID: g.sender.id.String(),
				RecipientUserID: g.control.id.String(),
				OwnerKind:       owner.kind, OwnerRef: owner.ref,
			}}, []string{peer})
			if err != nil {
				t.Fatalf("new router: %v", err)
			}
			message := a2a.InboundMessage{
				PeerAuthority: peer, PeerSubject: "peer-agent", Protocol: a2a.ProtocolVersion,
				MessageID: "held-message-" + owner.kind, ContextID: "held-context", Role: "ROLE_AGENT",
				Parts: []a2a.InboundPart{
					{Kind: "text", Text: "Perform the routed work.", Digest: strings.Repeat("a", 64)},
					{Kind: "data", Data: json.RawMessage(`{"risk":7}`),
						Reference: "a2a-part:" + strings.Repeat("b", 64), Digest: strings.Repeat("b", 64)},
				},
				ReplayID: "held-jti-" + owner.kind, ReplayExpiresAt: time.Now().UTC().Add(time.Hour),
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			started := time.Now()
			_, err = router.RouteInboundA2A(ctx, message)
			elapsed := time.Since(started)
			var routed *a2a.InboundRouteError
			if !errors.As(err, &routed) || routed.Code != -32005 || elapsed >= joinedReplayKernelBound {
				t.Fatalf("inbound message answered %v after %s, want -32005 within %s",
					err, elapsed.Round(time.Millisecond), joinedReplayKernelBound)
			}
			kernel.mu.Lock()
			applies, replayErr := kernel.applies, kernel.replayErr
			kernel.mu.Unlock()
			if !errors.Is(replayErr, sessions.ErrCommunicationEvidenceUnknown) ||
				!strings.Contains(fmt.Sprint(replayErr), c5Hold) {
				t.Fatalf("the prepared replay answered %v, want %q", replayErr, c5Hold)
			}
			if applies != 0 {
				t.Fatalf("the kernel ran %d work commands, want none before the hold answers", applies)
			}
		})
	}
}
