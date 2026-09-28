// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	a2a "github.com/olivaresai/olivares/connectors/a2a"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
)

// joinedReplayKernelBound is the time an A2A delivery must be answered within
// on the production composition. A read that waited on the owning replay
// transaction's connection would take the delivery's whole context instead.
const joinedReplayKernelBound = 2 * time.Second

// A protocol actor has no durable run credential binding. Neither a user id
// nor the legacy C5 operation port may manufacture that authority.
const a2aCredentialHold = "the run's credential binding does not resolve"

func requireA2ACredentialHold(t *testing.T, err error, elapsed time.Duration) {
	t.Helper()
	if !errors.Is(err, sessions.ErrWorkflowReauthenticationRequired) ||
		!strings.Contains(fmt.Sprint(err), a2aCredentialHold) || elapsed >= joinedReplayKernelBound {
		t.Fatalf("A2A answered %v after %s, want credential refusal within %s", err, elapsed, joinedReplayKernelBound)
	}
}

// Include protocol carriers and replay guards as well as communication effects:
// a refusal must neither mutate a binding nor consume a provider delivery.
func a2aProtocolRows(t *testing.T, e *consentEstate) map[model.Kind][]model.Record {
	t.Helper()
	rows := make(map[model.Kind][]model.Record)
	if err := e.eng.store.View(context.Background(), e.tT, func(sc store.Scope) error {
		for _, kind := range []model.Kind{"sessions.communication_binding", "sessions.communication_replay_guard", "sessions.protocol_interrupt"} {
			repo, err := sc.Ext(kind)
			if err != nil {
				return err
			}
			r, page, err := repo.List(context.Background(), model.Query{Limit: 1000})
			if err != nil {
				return err
			}
			if page.HasMore {
				return fmt.Errorf("protocol census exceeds bound: %s", kind)
			}
			rows[kind] = r
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return rows
}

func requireA2ANoEffects(t *testing.T, e *consentEstate, before communicationHTTPTestEffectCounts, protocol map[model.Kind][]model.Record) {
	t.Helper()
	assertCommunicationHTTPTestNoEffects(t, e.eng, e.tT, before, "refused A2A")
	if after := a2aProtocolRows(t, e); !reflect.DeepEqual(after, protocol) {
		t.Fatalf("refused A2A changed protocol rows: before=%v after=%v", protocol, after)
	}
}

// The same interrupt must be refused promptly on both attempts; no replay is consumed.
func TestAPushInterruptWithoutCredentialIsRefusedWithinTheBound(t *testing.T) {
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
			before, protocol := communicationHTTPTestEffects(t, e.eng, e.tT), a2aProtocolRows(t, e)
			for _, state := range []a2a.TaskState{a2a.TaskStateInputReq, a2a.TaskStateAuthRequired} {
				update := a2a.TaskUpdate{
					TaskID: "push-task-" + string(state), ContextID: "push-context", State: state, Interrupt: true,
					Sender: peer, ReplayID: "push-jti-" + string(state), ReplayExpiresAt: time.Now().UTC().Add(time.Hour),
				}
				for range []string{"first", "again"} {
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					started := time.Now()
					err := settlement.Record(ctx, update)
					elapsed := time.Since(started)
					cancel()
					requireA2ACredentialHold(t, err, elapsed)
					requireA2ANoEffects(t, e, before, protocol)
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

// User and agent routes without a credential stop before any work command.
func TestAnInboundMessageWithoutCredentialIsRefusedBeforeAnyWork(t *testing.T) {
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
			before, protocol := communicationHTTPTestEffects(t, e.eng, e.tT), a2aProtocolRows(t, e)
			started := time.Now()
			_, err = router.RouteInboundA2A(ctx, message)
			elapsed := time.Since(started)
			requireA2ACredentialHold(t, err, elapsed)
			requireA2ANoEffects(t, e, before, protocol)
			kernel.mu.Lock()
			applies, replayErr := kernel.applies, kernel.replayErr
			kernel.mu.Unlock()
			requireA2ACredentialHold(t, replayErr, elapsed)
			if applies != 0 {
				t.Fatalf("the kernel ran %d work commands, want none before the hold answers", applies)
			}
		})
	}
}
