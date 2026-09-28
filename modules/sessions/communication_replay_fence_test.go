// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// joinedReplayBound is the time an owning replay must answer within. A read
// that waits on the owning transaction's connection takes the whole
// workflowCommunicationTimeout instead.
const joinedReplayBound = 2 * time.Second

// errPortCalledInsideReplay is the answer of a checked port called while a
// replay transaction is open for its tenant.
var errPortCalledInsideReplay = errors.New("a port was called inside the owning replay transaction")

// joinedPortCalls records the port calls made while a replay transaction is
// open for the tenant, and counts the standing reads. A checked port fails
// such a call at once, so a read left inside the owning transaction shows as
// a named call instead of a wait on the transaction's connection.
type joinedPortCalls struct {
	tenant model.TenantID

	mu       sync.Mutex
	inside   []string
	standing int
}

func (c *joinedPortCalls) called(ctx context.Context, port string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if port == "Standing" {
		c.standing++
	}
	if _, active := protocolReplayScopeFromContext(ctx, c.tenant); !active {
		return false
	}
	c.inside = append(c.inside, port)
	return true
}

func (c *joinedPortCalls) insideCalls() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.inside...)
}

func (c *joinedPortCalls) standingReads() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.standing
}

type joinedCheckedDirectory struct {
	next  DirectorySnapshotResolver
	calls *joinedPortCalls
}

func (r *joinedCheckedDirectory) ResolveAudience(
	ctx context.Context, scope DirectoryScopeRef, selectors []AudienceSelector,
) (DirectorySnapshot, error) {
	if r.calls.called(ctx, "ResolveAudience") {
		return DirectorySnapshot{}, errPortCalledInsideReplay
	}
	return r.next.ResolveAudience(ctx, scope, selectors)
}

func (r *joinedCheckedDirectory) ResolveRecipient(
	ctx context.Context, scope DirectoryScopeRef, recipient RecipientRef,
) (RecipientSnapshot, error) {
	if r.calls.called(ctx, "ResolveRecipient") {
		return RecipientSnapshot{}, errPortCalledInsideReplay
	}
	return r.next.ResolveRecipient(ctx, scope, recipient)
}

func (r *joinedCheckedDirectory) ResolvePrincipal(
	ctx context.Context, scope DirectoryScopeRef, principal CommunicationPrincipal,
) (PrincipalResolution, error) {
	if r.calls.called(ctx, "ResolvePrincipal") {
		return PrincipalResolution{}, errPortCalledInsideReplay
	}
	return r.next.ResolvePrincipal(ctx, scope, principal)
}

type joinedCheckedAttestor struct {
	next  PublicationAudienceAttestor
	calls *joinedPortCalls
}

func (a *joinedCheckedAttestor) AttestPublicationAudience(
	ctx context.Context, request PublicationAudienceRequest,
) (DirectorySnapshot, PublicationAudienceAttestation, error) {
	if a.calls.called(ctx, "AttestPublicationAudience") {
		return DirectorySnapshot{}, PublicationAudienceAttestation{}, errPortCalledInsideReplay
	}
	return a.next.AttestPublicationAudience(ctx, request)
}

type joinedCheckedClosure struct {
	next  ChannelGrantSubjectClosureResolver
	calls *joinedPortCalls
}

func (c *joinedCheckedClosure) ResolveChannelGrantSubjects(
	ctx context.Context, scope DirectoryScopeRef, principal CommunicationPrincipal,
) (ChannelGrantSubjectClosure, error) {
	if c.calls.called(ctx, "ResolveChannelGrantSubjects") {
		return ChannelGrantSubjectClosure{}, errPortCalledInsideReplay
	}
	return c.next.ResolveChannelGrantSubjects(ctx, scope, principal)
}

type joinedCheckedAuthorizer struct {
	next  CoreEntityOperationAuthorizer
	calls *joinedPortCalls
}

func (a *joinedCheckedAuthorizer) AuthorizeEntityOperation(
	ctx context.Context, principal CommunicationPrincipal, entity EntityRef, operation CommunicationOperation,
) (ReadWitness, error) {
	if a.calls.called(ctx, "AuthorizeEntityOperation") {
		return ReadWitness{}, errPortCalledInsideReplay
	}
	return a.next.AuthorizeEntityOperation(ctx, principal, entity, operation)
}

type joinedCheckedStanding struct {
	next  auth.StandingReader
	calls *joinedPortCalls
}

func (s *joinedCheckedStanding) Standing(
	ctx context.Context, tenant model.TenantID, users []model.ID,
) (map[model.ID]auth.Standing, error) {
	if s.calls.called(ctx, "Standing") {
		return nil, errPortCalledInsideReplay
	}
	return s.next.Standing(ctx, tenant, users)
}

// installJoinedPortChecks wraps every evidence and standing port of m, so a
// call made inside an owning replay transaction is recorded and refused.
func installJoinedPortChecks(m *Module, tenant model.TenantID) *joinedPortCalls {
	calls := &joinedPortCalls{tenant: tenant}
	m.communicationDirectoryResolver = &joinedCheckedDirectory{next: m.communicationDirectoryResolver, calls: calls}
	m.communicationAudienceAttestor = &joinedCheckedAttestor{next: m.communicationAudienceAttestor, calls: calls}
	m.communicationGrantClosure = &joinedCheckedClosure{next: m.communicationGrantClosure, calls: calls}
	m.communicationOperationAuthorizer = &joinedCheckedAuthorizer{next: m.communicationOperationAuthorizer, calls: calls}
	m.standing = &joinedCheckedStanding{next: m.standing, calls: calls}
	return calls
}

func joinedInterruptCommand(
	fixture workflowCommunicationFixture,
	binding ProtocolBinding,
	key string,
) ProtocolInterruptCommand {
	return ProtocolInterruptCommand{
		BindingID: binding.ID, Generation: binding.Generation,
		Route: ProtocolInterruptRoute{
			ChannelID: fixture.channel.ID, SenderUserID: fixture.sender,
			RecipientUserID: model.ID(fixture.target.Ref),
		},
		RemoteState: "input_required",
		Requests: []ProtocolInterruptRequestRef{{
			KeyDigest:     protocolReplyDigestForTest(key),
			ContentDigest: protocolReplyDigestForTest(key + "-content"),
		}},
	}
}

func joinedInterruptClaim(
	fixture workflowCommunicationFixture,
	binding ProtocolBinding,
	replayID string,
) ProtocolReplayClaim {
	return ProtocolReplayClaim{
		WorkspaceID: fixture.workspace, Protocol: BindingProtocolA2A,
		PeerAuthority: binding.PeerAuthority, Kind: ProtocolReplayJTI,
		ReplayID: replayID, ExpiresAt: time.Now().UTC().Add(time.Hour),
		ExpectedBindingID: binding.ID,
	}
}

// protocolInterruptPlanForTest declares what recording command reads, as the
// A2A push settlement declares it for input_required: the route's sender and
// recipient are fenced, and the interrupt's directory evidence is prepared.
func protocolInterruptPlanForTest(command ProtocolInterruptCommand) ProtocolReplayPlan {
	return ProtocolReplayPlan{
		Accounts:  []model.ID{command.Route.SenderUserID, command.Route.RecipientUserID},
		Publishes: []ProtocolReplayPublish{ProtocolInterruptPublish(command.Route)},
	}
}

// applyJoinedInterrupt records the interrupt inside an owning replay, as the
// A2A push settlement does for input_required, under a delivery's context, and
// reports how long the replay took to answer.
func applyJoinedInterrupt(
	fixture workflowCommunicationFixture,
	claim ProtocolReplayClaim,
	command ProtocolInterruptCommand,
) (ProtocolReplayResult, time.Duration, error) {
	ctx, cancel := context.WithTimeout(context.Background(), workflowCommunicationTimeout)
	defer cancel()
	started := time.Now()
	result, err := fixture.m.ApplyPreparedProtocolReplay(ctx, fixture.tenant, claim, protocolInterruptPlanForTest(command),
		func(joined context.Context) (ProtocolReplaySettlement, error) {
			if _, err := fixture.m.RecordProtocolInterrupt(joined, fixture.tenant, command); err != nil {
				return ProtocolReplaySettlement{}, err
			}
			return ProtocolReplaySettlement{BindingID: command.BindingID}, nil
		})
	return result, time.Since(started), err
}

// offboardForJoinedReplayTest removes user from the fixture's tenant, which
// leaves the account's retirement active until a retirement run completes it.
func offboardForJoinedReplayTest(t *testing.T, fixture workflowCommunicationFixture, user model.ID) {
	t.Helper()
	ctx := context.Background()
	if err := fixture.st.AuthMutate(ctx, func(as store.AuthScope) error {
		_, err := fixture.authr.OffboardFromTenant(ctx, as, fixture.authUser, user, fixture.tenant, "joined replay fence")
		return err
	}); err != nil {
		t.Fatalf("offboard %s: %v", user, err)
	}
	standing, err := fixture.authr.Standing(ctx, fixture.tenant, []model.ID{user})
	if err != nil || !errors.Is(standing[user].Refusal(), auth.ErrSubjectRetirementActive) {
		t.Fatalf("standing of %s after the offboard = %#v, %v, want an active retirement", user, standing[user], err)
	}
}

func wantJoinedInterruptRows(t *testing.T, fixture workflowCommunicationFixture, want int, when string) {
	t.Helper()
	for _, kind := range []model.Kind{messageKind, protocolInterruptKind, protocolReplayGuardKind} {
		if rows := communicationRowsForTest(t, fixture.directNoticeFixture, kind); len(rows) != want {
			t.Fatalf("%s rows %s = %d, want %d", kind, when, len(rows), want)
		}
	}
}

// wantJoinedInterruptRefused: since CM-10 an interrupt recorded inside an
// owning replay is an A2A path. It is refused while its publish is prepared,
// within the bound, and writes no guard, Message or link.
func wantJoinedInterruptRefused(t *testing.T, fixture workflowCommunicationFixture) {
	t.Helper()
	makeProtocolInterruptRecipientWriter(t, fixture)
	binding := protocolInterruptBindingForTest(t, fixture, BindingProtocolA2A)
	result, elapsed, err := applyJoinedInterrupt(fixture,
		joinedInterruptClaim(fixture, binding, "bounded-interrupt"),
		joinedInterruptCommand(fixture, binding, "bounded-request"))
	requireA2ARefused(t, err, "joined interrupt")
	if elapsed >= joinedReplayBound || result.Replayed || !result.Guard.ID.IsZero() {
		t.Fatalf("joined interrupt = %#v after %s, want a refusal within %s and no guard",
			result, elapsed.Round(time.Millisecond), joinedReplayBound)
	}
	wantJoinedInterruptRows(t, fixture, 0, "after the refused joined interrupt")
}

// T1: an interrupt recorded inside an owning replay answers within the bound on
// SQLite, whose one connection the owning transaction holds. Since CM-10 the
// answer is the A2A refusal and nothing is written, which is what the name
// now says. Before CM-10 this test was TestAJoinedInterruptCommitsWithinTheBound;
// the commit it measured is covered on a non-A2A mutation by
// TestAPreparedReplayRetriesAMovedAccountFenceOnceAndCommits.
func TestAJoinedInterruptIsRefusedWithinTheBound(t *testing.T) {
	t.Parallel()

	wantJoinedInterruptRefused(t, newWorkflowCommunicationFixture(t, false))
}

// T2: the same interrupt on PostgreSQL, where the link's barrier must be the
// owning transaction's first lock; since CM-10 it is refused there too (before
// CM-10: TestAJoinedInterruptCommitsWithinTheBoundOnPostgreSQL). A race build runs against a server, so it
// fails rather than skips when none is configured.
func TestAJoinedInterruptIsRefusedWithinTheBoundOnPostgreSQL(t *testing.T) {
	t.Parallel()

	if !enginetest.PostgresAvailable(t) {
		if raceDetectorEnabled {
			t.Fatalf("%s names no PostgreSQL server: the joined interrupt's lock order is decided only there",
				enginetest.EnvSuperuserDSN)
		}
		t.Skipf("%s unset: the joined interrupt's PostgreSQL lock order is NOT exercised", enginetest.EnvSuperuserDSN)
	}
	pg := enginetest.IsolatedPostgres(t)
	direct := newDirectNoticeFixtureForBackend(t, communicationSchemaBackend{
		name: "postgres-joined-interrupt", engineName: store.EnginePostgres, dsn: pg.App,
	}, AckPolicyNone, 0, true, true, true)
	wantJoinedInterruptRefused(t, newWorkflowCommunicationFixtureFromDirect(t, false, direct))
}

// T3: no evidence or standing port is called while the owning replay
// transaction is open; every such read belongs before it.
func TestAJoinedInterruptCallsNoPortInsideTheOwningTransaction(t *testing.T) {
	t.Parallel()

	fixture := newWorkflowCommunicationFixture(t, false)
	makeProtocolInterruptRecipientWriter(t, fixture)
	binding := protocolInterruptBindingForTest(t, fixture, BindingProtocolA2A)
	calls := installJoinedPortChecks(fixture.m, fixture.tenant)
	_, elapsed, err := applyJoinedInterrupt(fixture,
		joinedInterruptClaim(fixture, binding, "port-checked-interrupt"),
		joinedInterruptCommand(fixture, binding, "port-checked-request"))
	if inside := calls.insideCalls(); len(inside) != 0 {
		t.Fatalf("ports called inside the owning replay transaction: %v (the replay answered %v after %s)",
			inside, err, elapsed.Round(time.Millisecond))
	}
	// CM-10: the interrupt is an A2A path and is refused, within the bound,
	// before anything is written.
	requireA2ARefused(t, err, "joined interrupt")
	if elapsed >= joinedReplayBound {
		t.Fatalf("the refusal took %s, want an answer within %s", elapsed.Round(time.Millisecond), joinedReplayBound)
	}
	wantJoinedInterruptRows(t, fixture, 0, "after the refused joined interrupt")
}

// T4 (a) and (b): an interrupt that names a retiring recipient or sender is
// refused with the retirement's refusal before anything is written, within the
// bound.
func TestAJoinedInterruptNamingARetiringAccountIsRefusedWithinTheBound(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		retiree func(workflowCommunicationFixture) model.ID
	}{
		{name: "recipient", retiree: func(f workflowCommunicationFixture) model.ID { return model.ID(f.target.Ref) }},
		{name: "sender", retiree: func(f workflowCommunicationFixture) model.ID { return f.sender }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fixture := newWorkflowCommunicationFixture(t, false)
			makeProtocolInterruptRecipientWriter(t, fixture)
			binding := protocolInterruptBindingForTest(t, fixture, BindingProtocolA2A)
			offboardForJoinedReplayTest(t, fixture, tc.retiree(fixture))
			_, elapsed, err := applyJoinedInterrupt(fixture,
				joinedInterruptClaim(fixture, binding, "retiring-"+tc.name),
				joinedInterruptCommand(fixture, binding, "retiring-"+tc.name+"-request"))
			if !errors.Is(err, auth.ErrSubjectRetirementActive) || elapsed >= joinedReplayBound {
				t.Fatalf("interrupt naming a retiring %s answered %v after %s, want %v within %s",
					tc.name, err, elapsed.Round(time.Millisecond), auth.ErrSubjectRetirementActive, joinedReplayBound)
			}
			wantJoinedInterruptRows(t, fixture, 0, "after the refusal")
		})
	}
}

// T4 (c): an exact replay answers Replayed after its recipient began to
// retire, and reads no standing.
func TestAnExactJoinedReplayReadsNoStandingAfterTheRecipientRetires(t *testing.T) {
	t.Parallel()

	fixture := newWorkflowCommunicationFixture(t, false)
	makeProtocolInterruptRecipientWriter(t, fixture)
	binding := protocolInterruptBindingForTest(t, fixture, BindingProtocolA2A)
	claim := joinedInterruptClaim(fixture, binding, "exact-replay")
	first, err := fixture.m.ApplyProtocolReplay(context.Background(), fixture.tenant, claim,
		func(context.Context) (ProtocolReplaySettlement, error) {
			return ProtocolReplaySettlement{BindingID: binding.ID}, nil
		})
	if err != nil || first.Replayed {
		t.Fatalf("first delivery = %#v, %v", first, err)
	}
	offboardForJoinedReplayTest(t, fixture, model.ID(fixture.target.Ref))
	calls := installJoinedPortChecks(fixture.m, fixture.tenant)
	replayed, elapsed, err := applyJoinedInterrupt(fixture, claim,
		joinedInterruptCommand(fixture, binding, "exact-replay-request"))
	if err != nil || !replayed.Replayed || replayed.Guard.ID != first.Guard.ID || elapsed >= joinedReplayBound {
		t.Fatalf("exact replay = %#v, %v after %s, want the first guard replayed within %s",
			replayed, err, elapsed.Round(time.Millisecond), joinedReplayBound)
	}
	if reads := calls.standingReads(); reads != 0 {
		t.Fatalf("exact replay read standing %d times, want none", reads)
	}
}

// T4 (d): a redelivery under a fresh token whose message identity was already
// settled answers the nested replay without reading any standing, as the
// inbound router's nested MessageID replay does.
func TestANestedMessageReplayReadsNoStanding(t *testing.T) {
	t.Parallel()

	fixture := newWorkflowCommunicationFixture(t, false)
	binding := protocolInterruptBindingForTest(t, fixture, BindingProtocolA2A)
	nested := ProtocolReplayClaim{
		WorkspaceID: fixture.workspace, Protocol: BindingProtocolA2A,
		PeerAuthority: binding.PeerAuthority, Kind: ProtocolReplayMessageID,
		ReplayID: "nested-message-1", ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if first, err := fixture.m.ApplyProtocolReplay(context.Background(), fixture.tenant, nested,
		func(context.Context) (ProtocolReplaySettlement, error) {
			return ProtocolReplaySettlement{}, nil
		}); err != nil || first.Replayed {
		t.Fatalf("first message delivery = %#v, %v", first, err)
	}
	offboardForJoinedReplayTest(t, fixture, model.ID(fixture.target.Ref))
	calls := installJoinedPortChecks(fixture.m, fixture.tenant)
	owning := ProtocolReplayClaim{
		WorkspaceID: fixture.workspace, Protocol: BindingProtocolA2A,
		PeerAuthority: binding.PeerAuthority, Kind: ProtocolReplayJTI,
		ReplayID: "fresh-token-1", ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	ctx, cancel := context.WithTimeout(context.Background(), workflowCommunicationTimeout)
	defer cancel()
	var inner ProtocolReplayResult
	plan := ProtocolReplayPlan{Accounts: []model.ID{model.ID(fixture.target.Ref)}, Nested: []ProtocolReplayClaim{nested}}
	result, err := fixture.m.ApplyPreparedProtocolReplay(ctx, fixture.tenant, owning, plan,
		func(joined context.Context) (ProtocolReplaySettlement, error) {
			var nestedErr error
			inner, nestedErr = fixture.m.ApplyProtocolReplay(joined, fixture.tenant, nested,
				func(context.Context) (ProtocolReplaySettlement, error) {
					return ProtocolReplaySettlement{}, errors.New("the settled message ran again")
				})
			return ProtocolReplaySettlement{}, nestedErr
		})
	if err != nil || result.Replayed || !inner.Replayed {
		t.Fatalf("redelivery under a fresh token = %#v (nested %#v), %v, want the nested message replayed",
			result, inner, err)
	}
	if reads := calls.standingReads(); reads != 0 {
		t.Fatalf("nested message replay read standing %d times, want none", reads)
	}
}
