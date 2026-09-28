// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance"
)

// countingLegacyOperationAuthorizer wraps the fixture's identity-only
// operation authorizer, which allows. A bound workflow effect must never
// consult it: every call is counted and the tests require zero.
type countingLegacyOperationAuthorizer struct {
	next  CoreEntityOperationAuthorizer
	calls atomic.Int64
}

func (c *countingLegacyOperationAuthorizer) AuthorizeEntityOperation(
	ctx context.Context,
	principal CommunicationPrincipal,
	entity EntityRef,
	operation CommunicationOperation,
) (ReadWitness, error) {
	c.calls.Add(1)
	return c.next.AuthorizeEntityOperation(ctx, principal, entity, operation)
}

// workflowBindingFixture is the production composition of a bound workflow
// effect: the real Authenticator as resolver, the composed Authorizer
// (governance request evaluator and scoped grants) as evidence source, bound
// through the public UseCommunicationRequestAuthority call boot makes, and a
// run bound to the sender's exact session by core/auth.
type workflowBindingFixture struct {
	workflowCommunicationFixture
	tt      *testing.T
	legacy  *countingLegacyOperationAuthorizer
	runID   model.ID
	binding auth.CredentialBinding
	bound   WorkflowCommunicationActor
}

func newWorkflowBindingFixture(
	t *testing.T,
	backend communicationSchemaBackend,
	withLease bool,
) *workflowBindingFixture {
	t.Helper()
	gov := governance.New()
	backend.registerSchema = func(reg store.ExtensionRegistry) error {
		if err := New().RegisterSchema(reg); err != nil {
			return err
		}
		return gov.RegisterSchema(reg)
	}
	direct := newDirectNoticeFixtureForBackendWithClock(
		t, backend, AckPolicyNone, 0, true, true, true, directNoticeSetupClock{},
	)
	wf := newWorkflowCommunicationFixtureFromDirect(t, withLease, direct)
	gov.UseData(api.NewModuleData(wf.st))
	wf.m.UseCommunicationRequestAuthority(
		wf.authr, auth.NewAuthorizer(gov.RequestEvaluator(), auth.WithScopedGrants(gov.ScopedGrants())),
	)
	// The composed Authorizer observes database time. The fixture's historical
	// test clock and directory fakes would place every reader observation before
	// it, so they follow real time here.
	// Database time is the engine's wall clock (millisecond precision on
	// SQLite), and the composed Authorizer observes it. The module reads real
	// time, and the directory fakes observe one second earlier, so no local
	// observation can land after the transaction that consumes it.
	wf.m.clock = model.SystemClock{}
	legacy := &countingLegacyOperationAuthorizer{next: wf.m.communicationOperationAuthorizer}
	wf.m.communicationOperationAuthorizer = legacy
	f := &workflowBindingFixture{
		workflowCommunicationFixture: wf, tt: t, legacy: legacy, runID: model.NewID(),
	}
	f.resync()
	f.binding = f.bind(f.authUser, f.runID)
	f.bound = f.actorFor(f.binding, f.runID)
	return f
}

// a2aRefusalReason is what every A2A protocol path now answers
// (D-DECISIONS-CM10, Fixed 4): a protocol actor is built from a stored account
// id and carries no run credential binding, so the exact pair never runs and
// nothing is prepared or written. Production never bound the identity-only
// operation port these paths used to reach in their fixtures.
const a2aRefusalReason = "the run's credential binding does not resolve"

func requireA2ARefused(t *testing.T, err error, what string) {
	t.Helper()
	if err == nil || !errors.Is(err, ErrWorkflowReauthenticationRequired) ||
		!strings.Contains(err.Error(), a2aRefusalReason) {
		t.Fatalf("%s answered %v, want the A2A refusal: %s", what, err, a2aRefusalReason)
	}
}

// a2aRowCounts counts every row an A2A effect would write.
func a2aRowCounts(t *testing.T, fixture directNoticeFixture) map[model.Kind]int {
	t.Helper()
	counts := map[model.Kind]int{}
	for _, kind := range []model.Kind{
		messageKind, messageDeliveryKind, workEventKind, workOutboxKind, communicationCommandKind,
		messageAckKind, protocolReplayGuardKind, protocolInterruptKind,
	} {
		counts[kind] = len(communicationRowsForTest(t, fixture, kind))
	}
	return counts
}

func requireA2ANoRows(t *testing.T, fixture directNoticeFixture, before map[model.Kind]int, what string) {
	t.Helper()
	for kind, n := range a2aRowCounts(t, fixture) {
		if n != before[kind] {
			t.Fatalf("%s wrote %s rows: %d -> %d", what, kind, before[kind], n)
		}
	}
}

// requireA2ABindingUnchanged proves a refused A2A path left its protocol
// binding at the version it had.
func requireA2ABindingUnchanged(t *testing.T, fixture workflowCommunicationFixture, binding ProtocolBinding) {
	t.Helper()
	after, err := fixture.m.GetProtocolBinding(context.Background(), fixture.tenant, ProtocolBindingRef{ID: binding.ID})
	if err != nil || after.Version != binding.Version || after.LastEventSeq != binding.LastEventSeq {
		t.Fatalf("protocol binding after the refusal = version %d, event %d, %v; want version %d, event %d",
			after.Version, after.LastEventSeq, err, binding.Version, binding.LastEventSeq)
	}
}

// resync points the fixture's directory fakes at the tenant's current
// directory epoch after a test changed accounts or sessions, as the real
// directory resolver would.
func (f *workflowBindingFixture) resync() {
	f.t().Helper()
	ctx := context.Background()
	if err := f.m.viewCommunication(ctx, f.scope, func(sc store.Scope) error {
		epoch, err := sc.(store.DirectorySnapshotReader).ReadDirectoryEpoch(ctx)
		if err == nil {
			f.epoch = epoch.Version
		}
		return err
	}); err != nil {
		f.t().Fatalf("read directory epoch: %v", err)
	}
	f.attestor.epoch = f.epoch
	f.m.communicationAudienceAttestor = f.attestor
	earlier := func() time.Time { return time.Now().UTC().Add(-time.Second) }
	f.m.communicationDirectoryResolver = &directNoticeReadDirectoryResolver{nowFn: earlier, epoch: f.epoch}
	f.m.communicationGrantClosure = &directNoticeReadClosureResolver{nowFn: earlier, epoch: f.epoch}
}

func (f *workflowBindingFixture) send(ctx context.Context, cmd WorkflowWorkTaskCommand) (WorkflowWorkTaskResult, error) {
	return f.m.SendWorkflowWorkTask(ctx, f.tenant, cmd)
}

func (f *workflowBindingFixture) offer(ctx context.Context, cmd WorkflowHandoffCommand) (WorkflowHandoffResult, error) {
	return f.m.OfferWorkflowHandoff(ctx, f.tenant, cmd)
}

func (f *workflowBindingFixture) observe(ctx context.Context, query WorkflowAckQuery) (WorkflowAckObservation, error) {
	return f.m.ObserveWorkflowAck(ctx, f.tenant, query)
}

func (f *workflowBindingFixture) ack(ctx context.Context, cmd WorkflowMessageAckCommand) (WorkflowMessageAckResult, error) {
	return f.m.AcknowledgeWorkflowMessage(ctx, f.tenant, cmd)
}

// setTargetRole changes the target account's tenant role and resyncs the
// directory fakes to the epoch the change moved.
func (f *workflowBindingFixture) setTargetRole(role string) {
	f.t().Helper()
	ctx := context.Background()
	if err := f.st.AuthMutate(ctx, func(as store.AuthScope) error {
		rows, _, err := as.Memberships().List(ctx, model.Query{Limit: 100})
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.UserID.String() == f.target.Ref && row.TargetTenantID == f.tenant {
				row.Role = role
				_, err = as.Memberships().Update(ctx, row)
				return err
			}
		}
		return errors.New("target membership not found")
	}); err != nil {
		f.t().Fatalf("set target role: %v", err)
	}
	f.resync()
}

func (f *workflowBindingFixture) subject(run model.ID) auth.CredentialBindingSubject {
	return auth.CredentialBindingSubject{
		Tenant: f.tenant, Kind: auth.CredentialBindingWorkflowRun, Ref: run, User: f.sender,
	}
}

func (f *workflowBindingFixture) bind(p auth.Principal, run model.ID) auth.CredentialBinding {
	f.t().Helper()
	binding, err := f.authr.BindCredential(context.Background(), p, f.subject(run))
	if err != nil {
		f.t().Fatalf("bind run credential: %v", err)
	}
	return binding
}

func (f *workflowBindingFixture) actorFor(binding auth.CredentialBinding, run model.ID) WorkflowCommunicationActor {
	actor := f.actor
	actor.CredentialBinding, actor.CredentialSubject = binding, run
	return actor
}

// freshSession logs the sender in again: a new credential of the same account.
func (f *workflowBindingFixture) freshSession() auth.Principal {
	f.t().Helper()
	token, _, err := f.authr.Login(
		context.Background(), "sender@direct-notice.test", "direct-notice-password", "127.0.0.9",
	)
	if err != nil {
		f.t().Fatalf("log the sender in again: %v", err)
	}
	p, err := f.authr.Authenticate(context.Background(), token)
	if err != nil {
		f.t().Fatalf("authenticate the fresh session: %v", err)
	}
	return p
}

func (f *workflowBindingFixture) t() *testing.T { return f.tt }

func (f *workflowBindingFixture) task(actor WorkflowCommunicationActor, key string) WorkflowWorkTaskCommand {
	return WorkflowWorkTaskCommand{
		Actor: actor, WorkItemID: f.workID, ChannelID: f.channel.ID, Recipient: f.target,
		Content: MessageContent{Subject: "Continue K4", Blocks: []MessageContentBlock{{
			Type: ContentBlockText, Format: TextPlain, Text: "Apply the next owned work step.",
		}}},
		IdempotencyKey: key,
	}
}

// effectCounts is every row a workflow effect writes, so a refusal can prove
// it wrote nothing: Messages, Deliveries, WorkEvents, the work outbox, command
// receipts, Handoffs and the tenant audit chain.
func (f *workflowBindingFixture) effectCounts() map[string]int {
	f.t().Helper()
	counts := map[string]int{}
	for _, kind := range []model.Kind{
		messageKind, messageDeliveryKind, workEventKind, workOutboxKind,
		communicationCommandKind, handoffKind, messageAckKind,
	} {
		counts[string(kind)] = len(communicationRowsForTest(f.t(), f.directNoticeFixture, kind))
	}
	audit := 0
	if err := f.st.View(context.Background(), f.tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(context.Background(), 1, func(model.AuditEvent) error {
			audit++
			return nil
		})
	}); err != nil {
		f.t().Fatalf("walk tenant audit: %v", err)
	}
	counts["audit"] = audit
	return counts
}

func (f *workflowBindingFixture) requireNoEffect(before map[string]int, what string) {
	f.t().Helper()
	after := f.effectCounts()
	for kind, n := range before {
		if after[kind] != n {
			f.t().Fatalf("%s wrote %s rows: %d -> %d", what, kind, n, after[kind])
		}
	}
}

func (f *workflowBindingFixture) requireLegacyUnused() {
	f.t().Helper()
	if calls := f.legacy.calls.Load(); calls != 0 {
		f.t().Fatalf("bound workflow effects consulted the identity-only operation port %d times", calls)
	}
}

// requireHandleAbsent greps every durable output of the effects for the
// handle's storage value: rows of each written kind and the tenant audit meta.
func (f *workflowBindingFixture) requireHandleAbsent(handles ...auth.CredentialBinding) {
	f.t().Helper()
	var dump strings.Builder
	for _, kind := range []model.Kind{
		messageKind, messageDeliveryKind, workEventKind, workOutboxKind,
		communicationCommandKind, handoffKind, messageAckKind,
	} {
		for _, row := range communicationRowsForTest(f.t(), f.directNoticeFixture, kind) {
			raw, _ := json.Marshal(row)
			dump.Write(raw)
		}
	}
	if err := f.st.View(context.Background(), f.tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(context.Background(), 1, func(e model.AuditEvent) error {
			raw, _ := json.Marshal(e)
			dump.Write(raw)
			return nil
		})
	}); err != nil {
		f.t().Fatalf("walk tenant audit: %v", err)
	}
	for _, handle := range handles {
		if strings.Contains(dump.String(), handle.StorageValue()) {
			f.t().Fatal("a credential-binding handle reached a durable output")
		}
	}
}

func TestWorkflowBindingProductionCompositionAllowsEveryEffect(t *testing.T) {
	for _, backend := range communicationSchemaBackends(t) {
		t.Run(backend.name, func(t *testing.T) {
			f := newWorkflowBindingFixture(t, backend, true)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()

			cmd := f.task(f.bound, "workflow-bound-task:"+model.NewID().String())
			published, err := f.send(ctx, cmd)
			if err != nil {
				t.Fatalf("bound publish: %v", err)
			}
			if published.Replayed || published.MessageID.IsZero() || published.State != MessagePublished {
				t.Fatalf("bound publish = %+v", published)
			}
			replayed, err := f.send(ctx, cmd)
			if err != nil || !replayed.Replayed || replayed.MessageID != published.MessageID ||
				replayed.EventSeq != published.EventSeq {
				t.Fatalf("bound replay = %+v, %v; want %+v", replayed, err, published)
			}
			observed, err := f.observe(ctx, WorkflowAckQuery{
				Actor: f.bound, TargetKind: WorkflowAckTargetMessage, TargetID: published.MessageID,
			})
			if err != nil || observed.Status != WorkflowAckPending {
				t.Fatalf("bound Ack observation = %+v, %v", observed, err)
			}
			handoff, err := f.offer(ctx, WorkflowHandoffCommand{
				Actor: f.bound, WorkItemID: f.workID, ChannelID: f.channel.ID, Target: f.target,
				Content:     HandoffContent{Summary: "Transfer K4 work", NextAction: "Continue the owned step"},
				AckDeadline: f.now.Add(4 * time.Minute), ExpectedOwnerEpoch: 1,
				IdempotencyKey: "workflow-bound-handoff:" + model.NewID().String(),
			})
			if err != nil || handoff.HandoffID.IsZero() || handoff.State != HandoffOffered {
				t.Fatalf("bound handoff = %+v, %v", handoff, err)
			}
			f.requireLegacyUnused()
			f.requireHandleAbsent(f.binding)
		})
	}
}

func TestWorkflowBindingAcknowledgesThroughTheExactPair(t *testing.T) {
	f := newWorkflowBindingFixture(t, communicationSchemaBackend{
		name: "sqlite", engineName: store.EngineSQLite, dsn: t.TempDir() + "/ack.db",
	}, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := f.task(f.bound, "workflow-bound-self-task:"+model.NewID().String())
	cmd.Recipient = RecipientRef{Kind: RecipientUser, Ref: f.sender.String()}
	published, err := f.send(ctx, cmd)
	if err != nil {
		t.Fatalf("bound self publish: %v", err)
	}
	ack := WorkflowMessageAckCommand{
		Actor: f.bound, WorkItemID: f.workID, ChannelID: f.channel.ID,
		DeliveryID: published.DeliveryID, ExpectedVersion: 1,
		IdempotencyKey: "workflow-bound-ack:" + model.NewID().String(),
	}
	// Without its binding the same actor is refused before any Ack is written.
	unbound := ack
	unbound.Actor = f.actor
	before := f.effectCounts()
	if _, err := f.ack(ctx, unbound); !errors.Is(err, ErrWorkflowReauthenticationRequired) {
		t.Fatalf("unbound Ack = %v, want ErrWorkflowReauthenticationRequired", err)
	}
	f.requireNoEffect(before, "unbound Ack")
	acked, err := f.ack(ctx, ack)
	if err != nil || acked.AckID.IsZero() || acked.State != DeliveryAcknowledged {
		t.Fatalf("bound Ack = %+v, %v", acked, err)
	}
	// A repeated Ack replays its committed receipt and writes nothing.
	before = f.effectCounts()
	again, err := f.ack(ctx, ack)
	if err != nil || !again.Replayed || again.AckID != acked.AckID || again.EventID != acked.EventID ||
		again.CommandID != acked.CommandID {
		t.Fatalf("bound Ack replay = %+v, %v; want %+v", again, err, acked)
	}
	f.requireNoEffect(before, "Ack replay")
	f.requireLegacyUnused()
	f.requireHandleAbsent(f.binding)
}

func TestWorkflowBindingRefusalsHappenBeforeMutation(t *testing.T) {
	for _, backend := range communicationSchemaBackends(t) {
		t.Run(backend.name, func(t *testing.T) {
			f := newWorkflowBindingFixture(t, backend, false)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			other := model.NewID()
			otherBinding := f.bind(f.authUser, other)
			wrongUser := f.bound
			wrongUser.AuditRef = "user:" + f.target.Ref
			cases := map[string]WorkflowCommunicationActor{
				"missing binding":      f.actor,
				"cross-run binding":    f.actorFor(otherBinding, f.runID),
				"binding of other run": f.actorFor(f.binding, other),
				"other account":        wrongUser,
			}
			for name, actor := range cases {
				before := f.effectCounts()
				_, err := f.send(ctx, f.task(actor, "refused:"+name))
				// An actor naming another account is refused before its binding is
				// read (that account holds no ChannelGrant.write here); every other
				// case is a binding that does not resolve.
				if err == nil || (name != "other account" && !errors.Is(err, ErrWorkflowReauthenticationRequired)) {
					t.Fatalf("%s: publish = %v, want ErrWorkflowReauthenticationRequired", name, err)
				}
				if strings.Contains(err.Error(), f.binding.StorageValue()) ||
					strings.Contains(err.Error(), otherBinding.StorageValue()) {
					t.Fatalf("%s: refusal names a handle: %v", name, err)
				}
				f.requireNoEffect(before, name)
				if _, err := f.observe(ctx, WorkflowAckQuery{
					Actor: actor, TargetKind: WorkflowAckTargetMessage, TargetID: model.NewID(),
				}); err == nil {
					t.Fatalf("%s: Ack observation succeeded", name)
				}
			}

			// The exact credential stops being current: refresh, then revocation.
			before := f.effectCounts()
			if _, _, err := f.authr.RefreshSession(ctx, f.authUser); err != nil {
				t.Fatalf("refresh the bound session: %v", err)
			}
			if _, err := f.send(ctx, f.task(f.bound, "refreshed")); !errors.Is(err, ErrWorkflowReauthenticationRequired) {
				t.Fatalf("publish after refresh = %v, want ErrWorkflowReauthenticationRequired", err)
			}
			fresh := f.freshSession()
			revokedRun := model.NewID()
			revoked := f.actorFor(f.bind(fresh, revokedRun), revokedRun)
			if err := f.authr.RevokeSession(ctx, fresh, fresh.CredID); err != nil {
				t.Fatalf("revoke session: %v", err)
			}
			if _, err := f.send(ctx, f.task(revoked, "revoked")); !errors.Is(err, ErrWorkflowReauthenticationRequired) {
				t.Fatalf("publish after revocation = %v, want ErrWorkflowReauthenticationRequired", err)
			}
			f.requireNoEffect(before, "credential no longer current")
			f.requireLegacyUnused()
		})
	}
}

func TestWorkflowBindingPolicyDenialStaysDistinct(t *testing.T) {
	f := newWorkflowBindingFixture(t, communicationSchemaBackend{
		name: "sqlite", engineName: store.EngineSQLite, dsn: t.TempDir() + "/policy.db",
	}, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	// The same valid credential, under a current policy that no longer grants
	// the send: the sender's membership drops to viewer, then is confined to
	// another workspace.
	setMembership := func(edit func(*model.Membership)) {
		t.Helper()
		if err := f.st.AuthMutate(ctx, func(as store.AuthScope) error {
			rows, _, err := as.Memberships().List(ctx, model.Query{Limit: 100})
			if err != nil {
				return err
			}
			for _, row := range rows {
				if row.UserID == f.sender && row.TargetTenantID == f.tenant {
					edit(&row)
					_, err = as.Memberships().Update(ctx, row)
					return err
				}
			}
			return errors.New("sender membership not found")
		}); err != nil {
			t.Fatalf("edit sender membership: %v", err)
		}
	}
	for name, edit := range map[string]func(*model.Membership){
		"role narrowed":      func(m *model.Membership) { m.Role = auth.RoleViewer },
		"confined elsewhere": func(m *model.Membership) { m.Role, m.WorkspaceID = auth.RoleOwner, model.NewID() },
	} {
		setMembership(edit)
		before := f.effectCounts()
		_, err := f.send(ctx, f.task(f.bound, "policy:"+name))
		if err == nil || errors.Is(err, ErrWorkflowReauthenticationRequired) {
			t.Fatalf("%s: publish = %v, want a policy refusal distinct from reauthentication", name, err)
		}
		f.requireNoEffect(before, name)
	}
	f.requireLegacyUnused()
}

func TestSameSubjectReauthPreservesReceipt(t *testing.T) {
	for _, backend := range communicationSchemaBackends(t) {
		t.Run(backend.name, func(t *testing.T) {
			f := newWorkflowBindingFixture(t, backend, true)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			cmd := f.task(f.bound, "reauth-receipt:"+model.NewID().String())
			published, err := f.send(ctx, cmd)
			if err != nil {
				t.Fatalf("publish under the original credential: %v", err)
			}
			// A task the initiator addressed to itself, acknowledged under the
			// original credential: its Ack receipt must survive reauthorization.
			selfCmd := f.task(f.bound, "reauth-self:"+model.NewID().String())
			selfCmd.Recipient = RecipientRef{Kind: RecipientUser, Ref: f.sender.String()}
			self, err := f.send(ctx, selfCmd)
			if err != nil {
				t.Fatalf("self-addressed publish under the original credential: %v", err)
			}
			ackCmd := WorkflowMessageAckCommand{
				Actor: f.bound, WorkItemID: f.workID, ChannelID: f.channel.ID,
				DeliveryID: self.DeliveryID, ExpectedVersion: 1,
				IdempotencyKey: "reauth-ack:" + model.NewID().String(),
			}
			acked, err := f.ack(ctx, ackCmd)
			if err != nil {
				t.Fatalf("Ack under the original credential: %v", err)
			}
			handoffCmd := WorkflowHandoffCommand{
				Actor: f.bound, WorkItemID: f.workID, ChannelID: f.channel.ID, Target: f.target,
				Content:     HandoffContent{Summary: "Transfer K4 work", NextAction: "Continue the owned step"},
				AckDeadline: f.now.Add(4 * time.Minute), ExpectedOwnerEpoch: 1,
				IdempotencyKey: "reauth-handoff:" + model.NewID().String(),
			}
			handoff, err := f.offer(ctx, handoffCmd)
			if err != nil {
				t.Fatalf("handoff under the original credential: %v", err)
			}
			if _, _, err := f.authr.RefreshSession(ctx, f.authUser); err != nil {
				t.Fatalf("refresh: %v", err)
			}
			if _, err := f.send(ctx, cmd); !errors.Is(err, ErrWorkflowReauthenticationRequired) {
				t.Fatalf("replay before reauthorization = %v, want ErrWorkflowReauthenticationRequired", err)
			}
			if _, err := f.ack(ctx, ackCmd); !errors.Is(err, ErrWorkflowReauthenticationRequired) {
				t.Fatalf("Ack replay before reauthorization = %v, want ErrWorkflowReauthenticationRequired", err)
			}
			// The same account's fresh credential continues the run; the target's
			// credential cannot.
			targetSession, err := f.authr.Authenticate(ctx, mustLoginWorkflowTarget(t, f))
			if err != nil {
				t.Fatalf("authenticate target: %v", err)
			}
			if _, err := f.authr.RebindCredential(ctx, f.binding, 1, targetSession, f.subject(f.runID), targetSession); !errors.Is(err, auth.ErrCredentialBindingInvalid) {
				t.Fatalf("another account's credential = %v, want ErrCredentialBindingInvalid", err)
			}
			fresh := f.freshSession()
			successor, err := f.authr.RebindCredential(ctx, f.binding, 1, fresh, f.subject(f.runID), fresh)
			if err != nil {
				t.Fatalf("same-account reauthorization: %v", err)
			}
			continued := f.actorFor(successor, f.runID)
			cmd.Actor, handoffCmd.Actor, ackCmd.Actor = continued, continued, continued
			f.resync()
			before := f.effectCounts()
			again, err := f.send(ctx, cmd)
			if err != nil || !again.Replayed || again.MessageID != published.MessageID ||
				again.EventID != published.EventID || again.EventSeq != published.EventSeq {
				t.Fatalf("publish replay after reauthorization = %+v, %v; want %+v", again, err, published)
			}
			handoffAgain, err := f.offer(ctx, handoffCmd)
			if err != nil || !handoffAgain.Replayed || handoffAgain.HandoffID != handoff.HandoffID ||
				handoffAgain.MessageID != handoff.MessageID || handoffAgain.EventSeq != handoff.EventSeq {
				t.Fatalf("handoff replay after reauthorization = %+v, %v; want %+v", handoffAgain, err, handoff)
			}
			ackAgain, err := f.ack(ctx, ackCmd)
			if err != nil || !ackAgain.Replayed || ackAgain.AckID != acked.AckID ||
				ackAgain.EventID != acked.EventID || ackAgain.CommandID != acked.CommandID {
				t.Fatalf("Ack replay after reauthorization = %+v, %v; want %+v", ackAgain, err, acked)
			}
			f.requireNoEffect(before, "receipt replay after reauthorization")
			f.requireLegacyUnused()
			f.requireHandleAbsent(f.binding, successor)
		})
	}
}

func TestSupersededBindingAtEffectBoundary(t *testing.T) {
	f := newWorkflowBindingFixture(t, communicationSchemaBackend{
		name: "sqlite", engineName: store.EngineSQLite, dsn: t.TempDir() + "/superseded.db",
	}, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	// The old credential stays valid; only the binding is superseded.
	fresh := f.freshSession()
	successor, err := f.authr.RebindCredential(ctx, f.binding, 1, fresh, f.subject(f.runID), fresh)
	if err != nil {
		t.Fatalf("supersede the binding: %v", err)
	}
	// The supersession moved the tenant's directory epoch (the F2 fence).
	f.resync()
	before := f.effectCounts()
	if _, err := f.send(ctx, f.task(f.bound, "superseded")); !errors.Is(err, ErrWorkflowReauthenticationRequired) {
		t.Fatalf("effect under the superseded binding = %v, want ErrWorkflowReauthenticationRequired", err)
	}
	f.requireNoEffect(before, "superseded binding")
	key := "current-binding:" + model.NewID().String()
	first, err := f.send(ctx, f.task(f.actorFor(successor, f.runID), key))
	if err != nil || first.Replayed {
		t.Fatalf("effect under the current binding = %+v, %v", first, err)
	}
	second, err := f.send(ctx, f.task(f.actorFor(successor, f.runID), key))
	if err != nil || !second.Replayed || second.MessageID != first.MessageID {
		t.Fatalf("current binding repeated its effect: %+v, %v", second, err)
	}
	f.requireLegacyUnused()
}

func TestWorkflowBindingLeaseLossRefusesBeforeCarrier(t *testing.T) {
	f := newWorkflowBindingFixture(t, communicationSchemaBackend{
		name: "sqlite", engineName: store.EngineSQLite, dsn: t.TempDir() + "/lease.db",
	}, true)
	before := f.effectCounts()
	_, err := f.offer(context.Background(), WorkflowHandoffCommand{
		Actor: f.bound, WorkItemID: f.workID, ChannelID: f.channel.ID, Target: f.target,
		Content:     HandoffContent{Summary: "Stale transfer", NextAction: "Must not be offered"},
		AckDeadline: f.now.Add(4 * time.Minute), ExpectedOwnerEpoch: 2,
		IdempotencyKey: "bound-stale-owner:" + model.NewID().String(),
	})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale owner epoch with a valid binding = %v, want conflict", err)
	}
	f.requireNoEffect(before, "stale owner epoch")
	f.requireLegacyUnused()
}

func mustLoginWorkflowTarget(t *testing.T, f *workflowBindingFixture) string {
	t.Helper()
	token, _, err := f.authr.Login(
		context.Background(), "workflow-target@communication.test", "workflow-target-password", "127.0.0.10",
	)
	if err != nil {
		t.Fatalf("log the target in: %v", err)
	}
	return token
}

// outageAuthStore fails the auth partition's reads, as a database outage would.
type outageAuthStore struct{ store.Store }

func (outageAuthStore) AuthView(context.Context, func(store.AuthScope) error) error {
	return errors.New("auth partition outage")
}

// STD-1/F1: an outage while the run's binding is resolved is unavailable
// evidence, never a reauthentication pause, and writes nothing.
func TestWorkflowBindingOutageIsNotReauthentication(t *testing.T) {
	f := newWorkflowBindingFixture(t, workflowSQLiteBackend(t, "outage"), false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	source := f.m.communicationAuthoritySources.source
	outage := auth.NewAuthenticator(outageAuthStore{Store: f.st}, nil)
	f.m.useCommunicationRequestAuthoritySources(outage, source)
	before := f.effectCounts()
	_, err := f.send(ctx, f.task(f.bound, "outage:"+model.NewID().String()))
	if err == nil || errors.Is(err, ErrWorkflowReauthenticationRequired) ||
		!errors.Is(err, ErrCommunicationEvidenceUnknown) {
		t.Fatalf("publish during an auth outage = %v, want unavailable evidence, not reauthentication", err)
	}
	f.requireNoEffect(before, "publish during an auth outage")
}

// F2 barrier oracle, the effect's own commit: a supersession published after
// an effect's preflight must refuse that effect at its commit, because the
// commit consumes the authority proof the supersession moves; an effect that
// committed first stays. Either way the successor then acts exactly once.
func TestSupersessionIsSerializedWithTheEffectCommit(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		supersedeAfterPreflight bool
	}{
		{name: "effect before supersession", supersedeAfterPreflight: false},
		{name: "supersession after the effect's preflight", supersedeAfterPreflight: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newWorkflowBindingFixture(t, workflowSQLiteBackend(t, "fence"), false)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			var successor auth.CredentialBinding
			supersede := func() {
				fresh := f.freshSession()
				var err error
				successor, err = f.authr.RebindCredential(ctx, f.binding, 1, fresh, f.subject(f.runID), fresh)
				if err != nil {
					t.Errorf("supersede the binding: %v", err)
				}
			}
			if tc.supersedeAfterPreflight {
				f.m.communicationAudienceAttestor = &afterWorkTaskAttestation{
					next: f.m.communicationAudienceAttestor, tenant: f.tenant,
					hook: func(context.Context) { supersede() },
				}
			}
			before := f.effectCounts()
			old, err := f.send(ctx, f.task(f.bound, "fence-old:"+model.NewID().String()))
			if tc.supersedeAfterPreflight {
				if err == nil || !errors.Is(err, ErrCommunicationEvidenceUnknown) {
					t.Fatalf("effect whose binding was superseded after its preflight = %+v, %v; want it refused at commit", old, err)
				}
				f.requireNoEffect(before, "an effect superseded after its preflight")
			} else {
				if err != nil || old.MessageID.IsZero() {
					t.Fatalf("effect before supersession = %+v, %v", old, err)
				}
				supersede()
				f.resync()
				if _, err := f.send(ctx, f.task(f.bound, "fence-after:"+model.NewID().String())); !errors.Is(err, ErrWorkflowReauthenticationRequired) {
					t.Fatalf("old binding after supersession = %v, want ErrWorkflowReauthenticationRequired", err)
				}
			}
			f.resync()
			key := "fence-successor:" + model.NewID().String()
			first, err := f.send(ctx, f.task(f.actorFor(successor, f.runID), key))
			if err != nil || first.Replayed {
				t.Fatalf("successor effect = %+v, %v", first, err)
			}
			second, err := f.send(ctx, f.task(f.actorFor(successor, f.runID), key))
			if err != nil || !second.Replayed || second.MessageID != first.MessageID {
				t.Fatalf("successor effect repeated: %+v, %v", second, err)
			}
			f.requireLegacyUnused()
		})
	}
}
