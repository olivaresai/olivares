// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The scenario these controls pin is a Session offering its WorkItem while an
// earlier request with the same key is still open. The resend observes the
// sender's Claim at version N and then waits behind the original. The original's
// own authority lock renews that lease to N+1 and commits with the offer. The
// resend's authority phase then finds N+1 where it observed N and fails with a
// LockedLeasedVersionConflict on the sender's own Claim, wrapped in the unknown
// sentinel. The original's receipt is durable by then, but the resend is never
// allowed to read it.
//
// Response already recovers from this exact failure through one recognition
// admission. These controls hold the offer to the same contract: exactly that
// cause is eligible, the identity must not move, and a recognition refuses
// before it reads anything when its own preconditions fail.

func handoffOfferRecognitionClaim(t *testing.T) (store.AuthorizationFactRef, handoffOfferNormalized, CommunicationClaimAuthoritySnapshot) {
	t.Helper()
	deadline := model.NewTimestamp(time.Now().Add(time.Hour))
	fact, err := store.NewLeaseFenceAuthorizationFactRef(claimKind, model.NewID(), 1, "osn_"+model.NewID().String(), 7, deadline)
	if err != nil {
		t.Fatal(err)
	}
	sid, fence, _, _ := fact.LeaseFenceWitness()
	normalized := handoffOfferNormalized{handoffCommandIdentity: handoffCommandIdentity{
		principal: CommunicationPrincipal{SessionID: sid, SessionFence: fence},
	}}
	return fact, normalized, CommunicationClaimAuthoritySnapshot{facts: []store.AuthorizationFactRef{fact}}
}

func TestHandoffOfferRecognitionRequiresTheSendersOwnStaleClaim(t *testing.T) {
	t.Parallel()
	fact, normalized, claims := handoffOfferRecognitionClaim(t)
	typed := store.NewLockedLeasedVersionConflict(fact)
	newer := fact
	newer.Version++
	recipient, err := store.NewLeaseFenceAuthorizationFactRef(claimKind, model.NewID(), 1,
		"osn_"+model.NewID().String(), 3, model.NewTimestamp(time.Now().Add(time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	withRecipient := func(_ *handoffOfferNormalized, c *CommunicationClaimAuthoritySnapshot) {
		c.facts = append(append([]store.AuthorizationFactRef(nil), c.facts...), recipient)
	}
	for _, tc := range []struct {
		name   string
		cause  error
		change func(*handoffOfferNormalized, *CommunicationClaimAuthoritySnapshot)
		want   bool
	}{
		// The measured failure: communication_tx.go wraps the store's typed
		// conflict and the unknown sentinel in one two-cause error.
		{"measured two-cause wrapper", fmt.Errorf("%w: Claim authority snapshot: %w", ErrCommunicationEvidenceUnknown, typed), nil, true},
		{"direct", typed, nil, true},
		{"single adapter wrapper", fmt.Errorf("adapter: %w", typed), nil, true},
		{"measured cause beside a recipient Claim", typed, withRecipient, true},
		{"joined cancellation", errors.Join(typed, context.Canceled), nil, false},
		{"joined unavailable", errors.Join(typed, errors.New("store unavailable")), nil, false},
		{"duplicate subtype", errors.Join(typed, typed), nil, false},
		{"another version witness", store.NewLockedLeasedVersionConflict(newer), nil, false},
		{"recipient Session Claim", store.NewLockedLeasedVersionConflict(recipient), withRecipient, false},
		{"generic conflict", store.ErrConflict, nil, false},
		{"missing", store.ErrNotFound, nil, false},
		{"digest reuse", errHandoffIdempotencyReused, nil, false},
		{"unknown", ErrCommunicationEvidenceUnknown, nil, false},
		{"forbidden", ErrCommunicationForbidden, nil, false},
		{"USER sender", typed, func(n *handoffOfferNormalized, _ *CommunicationClaimAuthoritySnapshot) {
			n.principal.SessionID, n.principal.UserID = "", model.NewID()
		}, false},
		{"other Session", typed, func(n *handoffOfferNormalized, _ *CommunicationClaimAuthoritySnapshot) {
			n.principal.SessionID = "osn_" + model.NewID().String()
		}, false},
		{"other fence", typed, func(n *handoffOfferNormalized, _ *CommunicationClaimAuthoritySnapshot) {
			n.principal.SessionFence++
		}, false},
		{"Claim absent from the snapshot", typed, func(_ *handoffOfferNormalized, c *CommunicationClaimAuthoritySnapshot) {
			c.facts = nil
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, c := normalized, claims
			if tc.change != nil {
				tc.change(&n, &c)
			}
			got := captureHandoffOfferAuthorityFailure(tc.cause, n, c)
			stale, eligible := handoffResponseRecognitionFailure(got)
			if eligible != tc.want {
				t.Fatalf("recognition eligibility = %t, want %t (%v)", eligible, tc.want, got)
			}
			if !tc.want {
				if got != tc.cause {
					t.Fatalf("an ineligible failure was rewritten to %v; it must stay %v", got, tc.cause)
				}
				return
			}
			if stale.fact != fact {
				t.Fatalf("captured %v, want the sender's own Claim %v", stale.fact, fact)
			}
			if !errors.Is(got, ErrCommunicationEvidenceUnknown) || errors.Is(got, store.ErrConflict) {
				t.Fatal("the private marker changed the wire classification of the failure")
			}
		})
	}
}

func TestHandoffOfferRecognitionIdentityIsExact(t *testing.T) {
	t.Parallel()
	digest := func(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }
	base := workItemHandoffOfferAdmission{
		normalized: handoffOfferNormalized{
			handoffCommandIdentity: handoffCommandIdentity{
				scope:            DirectoryScopeRef{TenantID: model.TenantID(model.NewID()), WorkspaceID: model.NewID()},
				principal:        CommunicationPrincipal{SessionID: "osn_offer", SessionFence: 7},
				actor:            CommunicationActorRef{Kind: ActorSession, Ref: "osn_offer"},
				actorFingerprint: digest(1), idempotencyKeyHash: digest(2), requestDigest: digest(3),
				commandScope: "POST /v1/m/sessions/handoffs;workspace=w", expectedVersion: 4,
			},
			command: HandoffOfferCommand{
				ChannelID: model.NewID(), WorkItemID: model.NewID(), MessageID: model.NewID(),
				DeliveryID: model.NewID(), IfMatch: `"v4"`, IdempotencyKey: "offer-key",
			},
		},
		target:             RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()},
		ackDeadline:        time.Now().UTC().Truncate(time.Second),
		expectedOwnerEpoch: 1,
	}
	if !sameWorkItemHandoffOfferIdentity(base, base) {
		t.Fatal("an identical admission was refused")
	}
	for _, tc := range []struct {
		name   string
		change func(*workItemHandoffOfferAdmission)
	}{
		{"request digest", func(a *workItemHandoffOfferAdmission) { a.normalized.requestDigest = digest(9) }},
		{"idempotency key hash", func(a *workItemHandoffOfferAdmission) { a.normalized.idempotencyKeyHash = digest(9) }},
		{"actor fingerprint", func(a *workItemHandoffOfferAdmission) { a.normalized.actorFingerprint = digest(9) }},
		{"scope", func(a *workItemHandoffOfferAdmission) { a.normalized.scope.WorkspaceID = model.NewID() }},
		{"principal fence", func(a *workItemHandoffOfferAdmission) { a.normalized.principal.SessionFence++ }},
		{"actor", func(a *workItemHandoffOfferAdmission) { a.normalized.actor.Ref = "osn_other" }},
		{"command scope", func(a *workItemHandoffOfferAdmission) { a.normalized.commandScope += "x" }},
		{"expected version", func(a *workItemHandoffOfferAdmission) { a.normalized.expectedVersion++ }},
		{"channel", func(a *workItemHandoffOfferAdmission) { a.normalized.command.ChannelID = model.NewID() }},
		{"work item", func(a *workItemHandoffOfferAdmission) { a.normalized.command.WorkItemID = model.NewID() }},
		{"carrier message", func(a *workItemHandoffOfferAdmission) { a.normalized.command.MessageID = model.NewID() }},
		{"carrier delivery", func(a *workItemHandoffOfferAdmission) { a.normalized.command.DeliveryID = model.NewID() }},
		{"If-Match", func(a *workItemHandoffOfferAdmission) { a.normalized.command.IfMatch = `"v5"` }},
		{"idempotency key", func(a *workItemHandoffOfferAdmission) { a.normalized.command.IdempotencyKey = "other-key" }},
		{"recipient", func(a *workItemHandoffOfferAdmission) { a.target.Ref = model.NewID().String() }},
		{"ack deadline", func(a *workItemHandoffOfferAdmission) { a.ackDeadline = a.ackDeadline.Add(time.Second) }},
		{"expected owner epoch", func(a *workItemHandoffOfferAdmission) { a.expectedOwnerEpoch++ }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := base
			tc.change(&current)
			if sameWorkItemHandoffOfferIdentity(base, current) {
				t.Fatalf("a recognition accepted an admission whose %s changed", tc.name)
			}
		})
	}
}

func TestHandoffOfferRecognitionRefusesBeforeAnyRead(t *testing.T) {
	t.Parallel()
	fact, normalized, claims := handoffOfferRecognitionClaim(t)
	original := workItemHandoffOfferAdmission{normalized: normalized, claims: claims}
	foreign, err := store.NewLeaseFenceAuthorizationFactRef(claimKind, model.NewID(), 1,
		"osn_"+model.NewID().String(), 7, model.NewTimestamp(time.Now().Add(time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	bounded, stop := context.WithTimeout(context.Background(), time.Minute)
	defer stop()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, stopExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stopExpired()
	// The Module has no store, resolver or sealer: a recognition that read
	// anything before refusing would not return the unknown sentinel cleanly.
	m := &Module{}
	for _, tc := range []struct {
		name  string
		ctx   context.Context
		claim store.AuthorizationFactRef
	}{
		{"unbounded request", context.Background(), fact},
		{"canceled request", canceled, fact},
		{"expired request", expired, fact},
		{"another Session's Claim", bounded, foreign},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := m.recognizeWorkItemHandoffOffer(tc.ctx, DirectoryScopeRef{}, auth.PrincipalRef{},
				WorkItemHandoffOfferCommand{}, original, tc.claim)
			if !errors.Is(err, ErrCommunicationEvidenceUnknown) || got != (HandoffOfferResult{}) {
				t.Fatalf("recognition = %+v, %v; want nothing and the unknown sentinel", got, err)
			}
		})
	}
}

// ---- real-store offer recognition --------------------------------------------
//
// The controls above pin the offer's seams. The ones below run OfferWorkItemHandoff
// itself in the response recognition estate. The real SQLite or PostgreSQL
// authority lock produces the stale Claim conflict and the real receipt table is
// read; handoffRecognitionData only gates each admission and records what its
// transaction asked the store for. Four helpers add what that estate lacks for an
// offer by its own Session, and nothing else.

// handoffOfferRecognitionFixture is the response recognition estate prepared for
// an offer by its Session.
type handoffOfferRecognitionFixture struct {
	handoffRecognitionFixture
	workID      model.ID
	ackDeadline time.Time
}

func newHandoffOfferRecognitionFixture(t *testing.T, backend *communicationSchemaBackend) handoffOfferRecognitionFixture {
	t.Helper()
	f := newHandoffRecognitionFixture(t, handoffServiceFixtureSpec{
		backend: backend, vacantLease: true, noClockGuard: true,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	handoffOfferRecognitionGrantWrite(t, ctx, f)
	workID := handoffOfferRecognitionSessionWork(t, ctx, f)
	// The estate's audience attestor still answers at the directory epoch from
	// before the target and the Session were onboarded. The resolver and the
	// closure answer at f.epoch, and the carrier preflight requires they agree.
	f.attestor.epoch = f.epoch
	handoffOfferRecognitionReadiness(t, ctx, f)
	f.m.data = &handoffRecognitionData{inner: f.m.data}
	return handoffOfferRecognitionFixture{
		handoffRecognitionFixture: f, workID: workID,
		// Whole seconds keep the stored deadline equal to the requested one on
		// both engines; every resend of one key sends the same deadline.
		ackDeadline: time.Now().UTC().Add(5 * time.Minute).Truncate(time.Second),
	}
}

func (f handoffOfferRecognitionFixture) offerCommand(key string) WorkItemHandoffOfferCommand {
	return WorkItemHandoffOfferCommand{
		ChannelID: f.channel.ID, WorkItemID: f.workID,
		Recipient:   RecipientRef{Kind: RecipientUser, Ref: f.recipient.String()},
		Content:     HandoffContent{Summary: "Offer recognition", NextAction: "Continue the offered work"},
		AckDeadline: f.ackDeadline, ExpectedOwnerEpoch: 1, IfMatch: `"v1"`, IdempotencyKey: key,
	}
}

// handoffOfferRecognitionGrantWrite gives the estate's Session the ChannelGrant an
// offer needs. The estate grants it read only, and a generation is immutable, so
// the read generation is revoked and its successor grants read and write, the
// chain GrantChannel writes. Both engines' triggers judge that chain.
func handoffOfferRecognitionGrantWrite(t *testing.T, ctx context.Context, f handoffRecognitionFixture) {
	t.Helper()
	var predecessor model.ID
	for _, row := range communicationRowsForTest(t, f.directNoticeFixture, channelGrantKind) {
		if row.String(colCommSubjectRef) != f.sid {
			continue
		}
		if predecessor != "" {
			t.Fatal("COULD_NOT_LOOK: the Session already holds more than one ChannelGrant generation")
		}
		predecessor = recordID(row)
		row[colCommState] = string(ChannelGrantRevoked)
		row[colCommRevokedByKind] = string(ActorUser)
		row[colCommRevokedByRef] = f.sender.String()
		if _, err := communicationUpdate(ctx, f.m, f.tenant, channelGrantKind, row); err != nil {
			t.Fatalf("revoke the Session's read-only ChannelGrant: %v", err)
		}
	}
	if predecessor == "" {
		t.Fatal("COULD_NOT_LOOK: the Session has no ChannelGrant to supersede")
	}
	grant := channelGrantFromRecognitionFixture(t, f.handoffServiceFixture, f.sid)
	grant.Generation, grant.SupersedesID, grant.CanWrite = 2, predecessor, true
	record, err := channelGrantToRecord(grant)
	if err != nil {
		t.Fatalf("encode the Session's write ChannelGrant: %v", err)
	}
	if _, err := communicationCreateWithID(ctx, f.m, f.tenant, channelGrantKind, grant.ID, record); err != nil {
		t.Fatalf("create the Session's write ChannelGrant: %v", err)
	}
}

// handoffOfferRecognitionSessionWork creates a WorkItem owned by the estate's
// Session, with the context event and the vacant WorkLease generation the handoff
// fixture writes for its own USER-owned item.
func handoffOfferRecognitionSessionWork(t *testing.T, ctx context.Context, f handoffRecognitionFixture) model.ID {
	t.Helper()
	workID := model.NewID()
	item := workSchemaItem(f.workspace, "K3 offer recognition")
	item[colWorkOwnerKind] = string(RecipientSession)
	item[colWorkOwnerRef] = f.sid
	item[colWorkLastEventSeq] = int64(1)
	if _, err := communicationCreateWithID(ctx, f.m, f.tenant, workItemKind, workID, item); err != nil {
		t.Fatalf("create the Session-owned WorkItem: %v", err)
	}
	event := workSchemaEvent(f.workspace, workID.String(), model.NewID().String(), 1, "offer-recognition-context")
	event[colEventActorRef] = f.sender.String()
	if _, err := communicationCreateWithID(ctx, f.m, f.tenant, workEventKind, model.NewID(), event); err != nil {
		t.Fatalf("create the Session-owned WorkItem's context event: %v", err)
	}
	lease := model.Record{
		colWorkWorkspaceID: f.workspace.String(), colWorkItemID: workID.String(),
		colLeaseHolderSID: nil, colLeaseHolderRunRef: nil, colLeaseHolderAgentRef: nil,
		colLeaseFence: int64(0), colLeaseState: workLeaseVacant,
		colLeaseAcquiredAt: nil, colLeaseRenewedAt: nil, colLeaseExpiresAt: nil,
		colLeaseEndedAt: nil, colLeaseEndReason: nil, colLeaseRenewalCount: int64(0),
	}
	if _, err := communicationCreateWithID(ctx, f.m, f.tenant, workLeaseKind, model.NewID(), lease); err != nil {
		t.Fatalf("create the Session-owned WorkItem's vacant WorkLease: %v", err)
	}
	return workID
}

// handoffOfferRecognitionReadiness makes the public offer boundary's readiness
// conjunction effective. OfferWorkItemHandoff always evaluates it; the response
// harness enters below its public boundary and never does. Only witnesses this
// estate leaves unbound are added: store, pump and issuer, and a sealer only when
// none is bound. A storage-protected Channel returns plain JSON before
// PrepareProtectedPayload reaches the sealer, and the issuer only mints runtime
// credentials. A missing resolver or permission term is a port the offer reads,
// so it is an inability and is never replaced.
func handoffOfferRecognitionReadiness(t *testing.T, ctx context.Context, f handoffRecognitionFixture) {
	t.Helper()
	witness := &communicationReadinessStub{storeReady: true, sealerReady: true, pumpReady: true}
	before, err := f.m.EvaluateCommunicationReadiness(ctx)
	if err != nil {
		t.Fatalf("COULD_NOT_LOOK: evaluate the estate's communication readiness: %v", err)
	}
	for _, missing := range before.Missing {
		switch missing {
		case CommunicationReadinessStore:
			f.m.UseCommunicationStoreReadinessWitness(witness)
		case CommunicationReadinessPump:
			f.m.UseCommunicationPumpReadinessWitness(witness)
		case CommunicationReadinessIssuer:
			f.m.UseCommunicationSessionCredentialSource(communicationSchemaCredentialSource{})
		case CommunicationReadinessSealer:
			if communicationPortBound(f.m.communicationSealer) {
				t.Fatal("COULD_NOT_LOOK: the estate's sealer has no readiness witness, and replacing it would change what the offer seals")
			}
			f.m.UseCommunicationContentSealer(witness)
		default:
			t.Fatalf("COULD_NOT_LOOK: the estate lacks %s readiness, a port the offer itself reads", missing)
		}
	}
	after, err := f.m.EvaluateCommunicationReadiness(ctx)
	if err != nil || !after.Effective {
		t.Fatalf("COULD_NOT_LOOK: offer readiness = %+v, %v", after, err)
	}
}

type handoffOfferRecognitionOutcome struct {
	result HandoffOfferResult
	err    error
}

func handoffOfferRecognitionStart(
	ctx context.Context, f handoffOfferRecognitionFixture, cmd WorkItemHandoffOfferCommand, a *handoffRecognitionAttempt,
) <-chan handoffOfferRecognitionOutcome {
	out := make(chan handoffOfferRecognitionOutcome, 1)
	go func() {
		result, err := f.m.OfferWorkItemHandoff(
			context.WithValue(ctx, handoffRecognitionAttemptKey{}, a), f.scope, f.targetRef, cmd)
		out <- handoffOfferRecognitionOutcome{result, err}
	}()
	return out
}

func handoffOfferRecognitionWait(
	t *testing.T, ctx context.Context, out <-chan handoffOfferRecognitionOutcome,
) handoffOfferRecognitionOutcome {
	t.Helper()
	select {
	case got := <-out:
		return got
	case <-ctx.Done():
		t.Fatal("the bounded offer recognition fixture expired")
		return handoffOfferRecognitionOutcome{}
	}
}

func handoffOfferRecognitionAwait(
	t *testing.T, ctx context.Context, ready <-chan struct{}, out <-chan handoffOfferRecognitionOutcome,
) {
	t.Helper()
	select {
	case <-ready:
	case got := <-out:
		t.Fatalf("the offer ended before its capture barrier: %+v, %v", got.result, got.err)
	case <-ctx.Done():
		t.Fatal("the offer never reached its transaction")
	}
}

// handoffOfferRecognitionClaimFact is the one Claim in a locked authority union.
func handoffOfferRecognitionClaimFact(t *testing.T, facts []store.AuthorizationFactRef) store.AuthorizationFactRef {
	t.Helper()
	var claims []store.AuthorizationFactRef
	for _, fact := range facts {
		if fact.Kind == claimKind {
			claims = append(claims, fact)
		}
	}
	if len(claims) != 1 {
		t.Fatalf("the locked authority union carries %d Claims, want exactly the sender's", len(claims))
	}
	return claims[0]
}

// handoffOfferRecognitionChallenge is handoffRecognitionChallenge for an offer. The
// admission captures its authority, an independent authorized touch makes the
// captured Claim stale, afterCapture changes whatever the case changes, and the
// admission then meets the real store. Rows and the Claim are compared from the
// quiescent state just before release, so only the recognition's effects count.
func handoffOfferRecognitionChallenge(
	t *testing.T, f handoffOfferRecognitionFixture, cmd WorkItemHandoffOfferCommand,
	configure func(*handoffRecognitionAttempt), afterCapture func(),
	want error, wantAttempts int, wantTouch int64,
) handoffOfferRecognitionOutcome {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	a, ready, release := handoffRecognitionGate()
	defer release()
	if configure != nil {
		configure(a)
	}
	out := handoffOfferRecognitionStart(ctx, f, cmd, a)
	handoffOfferRecognitionAwait(t, ctx, ready, out)
	handoffRecognitionTouch(t, f.handoffRecognitionFixture)
	if afterCapture != nil {
		afterCapture()
	}
	beforeRows := handoffRecognitionRows(t, f.handoffRecognitionFixture)
	beforeClaim := handoffRecognitionClaim(t, f.handoffRecognitionFixture)
	release()
	got := handoffOfferRecognitionWait(t, ctx, out)
	if want == nil {
		if got.err != nil || !got.result.Replayed {
			t.Fatalf("offer recognition = %+v, %v; want the committed receipt replayed", got.result, got.err)
		}
	} else if !errors.Is(got.err, want) || got.result != (HandoffOfferResult{}) {
		t.Fatalf("offer recognition = %+v, %v; want no result and %v: an error must never carry a module result",
			got.result, got.err, want)
	}
	if a.mutations != wantAttempts {
		t.Fatalf("offer admissions = %d, want %d", a.mutations, wantAttempts)
	}
	handoffRecognitionAssertRows(t, beforeRows, handoffRecognitionRows(t, f.handoffRecognitionFixture))
	handoffRecognitionAssertTouch(t, beforeClaim, handoffRecognitionClaim(t, f.handoffRecognitionFixture), wantTouch)
	return got
}

// handoffOfferRecognitionChangeClaim rewrites the estate's Claim row in one
// ordinary transaction, as the response refusals do.
func handoffOfferRecognitionChangeClaim(t *testing.T, f handoffOfferRecognitionFixture, change func(model.Record) error) {
	t.Helper()
	ctx := context.Background()
	err := f.m.communicationData(f.tenant).Mutate(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(claimKind)
		if err != nil {
			return err
		}
		row, err := repo.Get(ctx, f.claim.ID)
		if err != nil {
			return err
		}
		if err := change(row); err != nil {
			return err
		}
		_, err = repo.Update(ctx, row)
		return err
	})
	if err != nil {
		t.Fatalf("change the Session's Claim: %v", err)
	}
}

// TestHandoffOfferSessionRecognitionRealClaimContention is the measured scenario
// in the module, on both engines. Two requests of one key capture the same Claim.
// The original commits and its own authority lock renews the lease. The resend's
// first admission then meets the real stale-Claim conflict before it can read a
// receipt, and its one recognition replays the original's receipt from a fresh
// admission, touching the Claim once and writing nothing else.
func TestHandoffOfferSessionRecognitionRealClaimContention(t *testing.T) {
	for _, engine := range vacantTransferEngines(t) {
		t.Run(engine.name, func(t *testing.T) {
			backend := engine.newBackend(t, "offer-recognition")
			f := newHandoffOfferRecognitionFixture(t, &backend)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			cmd := f.offerCommand(model.NewID().String())
			winner, readyWinner, releaseWinner := handoffRecognitionGate()
			defer releaseWinner()
			winnerOut := handoffOfferRecognitionStart(ctx, f, cmd, winner)
			handoffOfferRecognitionAwait(t, ctx, readyWinner, winnerOut)
			loser, readyLoser, releaseLoser := handoffRecognitionGate()
			defer releaseLoser()
			loserOut := handoffOfferRecognitionStart(ctx, f, cmd, loser)
			handoffOfferRecognitionAwait(t, ctx, readyLoser, loserOut)
			before := handoffRecognitionClaim(t, f.handoffRecognitionFixture)
			releaseWinner()
			won := handoffOfferRecognitionWait(t, ctx, winnerOut)
			if won.err != nil || won.result.Replayed {
				t.Fatalf("the original offer = %+v, %v; want a new offer", won.result, won.err)
			}
			afterWinner := handoffRecognitionClaim(t, f.handoffRecognitionFixture)
			handoffRecognitionAssertTouch(t, before, afterWinner, 1)
			rows := handoffRecognitionRows(t, f.handoffRecognitionFixture)
			releaseLoser()
			got := handoffOfferRecognitionWait(t, ctx, loserOut)
			if got.err != nil || !got.result.Replayed {
				t.Fatalf("the resend = %+v, %v; want the original's receipt replayed", got.result, got.err)
			}
			replayed := got.result
			replayed.Replayed = false
			if replayed != won.result {
				t.Fatalf("the replay %+v differs from the original %+v", replayed, won.result)
			}
			if winner.mutations != 1 || len(winner.facts) == 0 || loser.mutations != 2 ||
				len(loser.facts) != 2 || len(loser.lockErrors) != 2 {
				t.Fatalf("admissions: original %d with %d authority locks, resend %d with %d authority locks and %d lock results; want 1, >0, 2, 2, 2",
					winner.mutations, len(winner.facts), loser.mutations, len(loser.facts), len(loser.lockErrors))
			}
			captured := handoffOfferRecognitionClaimFact(t, loser.facts[0])
			original := handoffOfferRecognitionClaimFact(t, winner.facts[0])
			if captured != original || captured.Version != before.Int(model.ColVersion) {
				t.Fatalf("the requests did not capture one Claim before the original committed: %v and %v, row version %d",
					original, captured, before.Int(model.ColVersion))
			}
			var conflict *store.LockedLeasedVersionConflict
			if !errors.As(loser.lockErrors[0], &conflict) || !conflict.Matches(captured) {
				t.Fatalf("the resend's first authority lock = %v; want the real stale-Claim conflict", loser.lockErrors[0])
			}
			if loser.lockErrors[1] != nil {
				t.Fatalf("the recognition's authority lock = %v", loser.lockErrors[1])
			}
			if fresh := handoffOfferRecognitionClaimFact(t, loser.facts[1]); fresh.Version != afterWinner.Int(model.ColVersion) {
				t.Fatalf("the recognition locked Claim version %d, want the fresh %d: it reused the stale admission",
					fresh.Version, afterWinner.Int(model.ColVersion))
			}
			for _, attempt := range loser.keyAttempts {
				if attempt == 1 {
					t.Fatal("the stale first admission reached a local transaction lock")
				}
			}
			if loser.receiptReads != 1 {
				t.Fatalf("the resend read receipts %d times, want once, in the recognition", loser.receiptReads)
			}
			handoffRecognitionAssertRows(t, rows, handoffRecognitionRows(t, f.handoffRecognitionFixture))
			handoffRecognitionAssertTouch(t, afterWinner, handoffRecognitionClaim(t, f.handoffRecognitionFixture), 1)
		})
	}
}

// TestHandoffOfferRecognitionAtRealStoreSeams drives the recognition's boundaries
// after one committed original. A recognition never creates an offer, ends after
// one attempt, and never returns a result together with an error, even when its
// replay committed and only the outcome was lost.
func TestHandoffOfferRecognitionAtRealStoreSeams(t *testing.T) {
	f := newHandoffOfferRecognitionFixture(t, nil)
	cmd := f.offerCommand(model.NewID().String())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	original := handoffOfferRecognitionWait(t, ctx,
		handoffOfferRecognitionStart(ctx, f, cmd, &handoffRecognitionAttempt{}))
	if original.err != nil || original.result.Replayed {
		t.Fatalf("the original offer = %+v, %v", original.result, original.err)
	}
	t.Run("a committed offer is recognized", func(t *testing.T) {
		got := handoffOfferRecognitionChallenge(t, f, cmd, nil, nil, nil, 2, 1)
		got.result.Replayed = false
		if got.result != original.result {
			t.Fatalf("the recognized receipt %+v differs from the original %+v", got.result, original.result)
		}
	})
	t.Run("an offer that never committed is not created by its recognition", func(t *testing.T) {
		handoffOfferRecognitionChallenge(t, f, f.offerCommand(model.NewID().String()), nil, nil,
			ErrCommunicationEvidenceUnknown, 2, 0)
	})
	t.Run("a receipt the recognition cannot see is not replaced by a new offer", func(t *testing.T) {
		handoffOfferRecognitionChallenge(t, f, cmd, func(a *handoffRecognitionAttempt) {
			a.receipt = func(n int, rows []model.Record, page model.Page, err error) ([]model.Record, model.Page, error) {
				if n == 2 {
					return nil, page, err
				}
				return rows, page, err
			}
		}, nil, ErrCommunicationEvidenceUnknown, 2, 0)
	})
	// A failed read is not a missing receipt. The real read runs, then the hook
	// replaces its outcome with an error at the module repository seam; this is a
	// synthetic read failure, not a measured engine I/O fault. findHandoffReceipt
	// returns a List error unchanged, before the absent-receipt guard, so the
	// recognition must refuse with that error rather than the unknown sentinel.
	t.Run("a receipt read that fails at the module seam refuses with its error", func(t *testing.T) {
		readFailure := fmt.Errorf("%w: synthetic receipt read failure at the module repository seam",
			store.ErrStoreUnavailable)
		got := handoffOfferRecognitionChallenge(t, f, cmd, func(a *handoffRecognitionAttempt) {
			a.receipt = func(n int, rows []model.Record, page model.Page, err error) ([]model.Record, model.Page, error) {
				if n == 2 {
					return nil, model.Page{}, readFailure
				}
				return rows, page, err
			}
		}, nil, readFailure, 2, 0)
		if errors.Is(got.err, ErrCommunicationEvidenceUnknown) {
			t.Fatalf("the failed receipt read was reported as a missing receipt: %v", got.err)
		}
	})
	t.Run("a second real collision ends the one attempt", func(t *testing.T) {
		handoffOfferRecognitionChallenge(t, f, cmd, func(a *handoffRecognitionAttempt) {
			gate := a.before
			a.before = func(n int, ctx context.Context) error {
				if n == 2 {
					handoffRecognitionTouch(t, f.handoffRecognitionFixture)
				}
				return gate(n, ctx)
			}
		}, nil, ErrCommunicationEvidenceUnknown, 2, 1)
	})
	for _, tc := range []struct {
		name string
		lost error
	}{
		{"an uncertain recognition commit returns no result", fmt.Errorf("%w: simulated", store.ErrCommitOutcomeUnknown)},
		{"a lost recognition outcome returns no result", errors.New("simulated committed result loss")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handoffOfferRecognitionChallenge(t, f, cmd, func(a *handoffRecognitionAttempt) {
				a.after = func(n int, err error) error {
					if n == 2 && err == nil {
						return tc.lost
					}
					return err
				}
			}, nil, tc.lost, 2, 1)
		})
	}
}

// TestHandoffOfferRecognitionRefusesChangedCurrentAuthority changes one input of
// the fresh admission after the resend captured its authority. Each case has a
// durable original receipt, so only current authority stands between the
// recognition and a replay; each ends before a second transaction and leaves
// every row and the Claim as they were.
func TestHandoffOfferRecognitionRefusesChangedCurrentAuthority(t *testing.T) {
	for _, tc := range []struct {
		name   string
		want   error
		change func(*testing.T, handoffOfferRecognitionFixture)
	}{
		{"policy denied", ErrCommunicationForbidden, func(_ *testing.T, f handoffOfferRecognitionFixture) {
			f.source.evidence.Outcome = auth.EvidenceDeny
			f.source.evidence.CorePermission.Verdict = auth.CheckBroken
		}},
		{"directory unknown", ErrCommunicationEvidenceUnknown, func(_ *testing.T, f handoffOfferRecognitionFixture) {
			f.directory.outcome = PrincipalUnknown
		}},
		{"write grant revoked", ErrCommunicationForbidden, func(t *testing.T, f handoffOfferRecognitionFixture) {
			for _, row := range communicationRowsForTest(t, f.directNoticeFixture, channelGrantKind) {
				if row.String(colCommSubjectRef) != f.sid || row.String(colCommState) != string(ChannelGrantActive) {
					continue
				}
				row[colCommState] = string(ChannelGrantRevoked)
				row[colCommRevokedByKind] = string(ActorUser)
				row[colCommRevokedByRef] = f.sender.String()
				if _, err := communicationUpdate(context.Background(), f.m, f.tenant, channelGrantKind, row); err != nil {
					t.Fatal(err)
				}
			}
		}},
		{"Claim released", ErrCommunicationEvidenceUnknown, func(t *testing.T, f handoffOfferRecognitionFixture) {
			handoffOfferRecognitionChangeClaim(t, f, func(row model.Record) error {
				row[colClaimState] = claimReleased
				return nil
			})
		}},
		{"Claim fence rotated", ErrCommunicationEvidenceUnknown, func(t *testing.T, f handoffOfferRecognitionFixture) {
			handoffOfferRecognitionChangeClaim(t, f, func(row model.Record) error {
				row[colFence] = row.Int(colFence) + 1
				return nil
			})
		}},
		{"Claim deadline moved", ErrCommunicationEvidenceUnknown, func(t *testing.T, f handoffOfferRecognitionFixture) {
			handoffOfferRecognitionChangeClaim(t, f, func(row model.Record) error {
				deadline, err := model.ParseTimestamp(row.String(colLeaseExpires))
				if err != nil {
					return err
				}
				row[colLeaseExpires] = model.NewTimestamp(deadline.Time().Add(time.Minute)).String()
				return nil
			})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newHandoffOfferRecognitionFixture(t, nil)
			cmd := f.offerCommand(model.NewID().String())
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			original := handoffOfferRecognitionWait(t, ctx,
				handoffOfferRecognitionStart(ctx, f, cmd, &handoffRecognitionAttempt{}))
			if original.err != nil {
				t.Fatalf("the original offer = %+v, %v", original.result, original.err)
			}
			handoffOfferRecognitionChallenge(t, f, cmd, nil, func() { tc.change(t, f) }, tc.want, 1, 0)
		})
	}
}
