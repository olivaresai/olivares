// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type incomingHandoffFixture struct {
	handoffServiceFixture
	ring *communicationCursorTokenKeyring
}

func newIncomingHandoffFixture(t *testing.T) incomingHandoffFixture {
	t.Helper()
	return newIncomingHandoffFixtureWithDurableAckDelay(t, 0)
}

func newIncomingHandoffFixtureWithDurableAckDelay(
	t *testing.T,
	durableAckDelay time.Duration,
) incomingHandoffFixture {
	t.Helper()
	base := newHandoffServiceFixtureWithDurableAckDelay(t, durableAckDelay)
	ring := newChannelCatalogNavigationKeyring(t, "k3handoffread")
	base.m.communicationCursorKeyring = ring
	return incomingHandoffFixture{handoffServiceFixture: base, ring: ring}
}

func (f incomingHandoffFixture) offer(t *testing.T, content HandoffContent) HandoffOfferResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)
	offer, err := f.m.offerHandoffWithAuthority(ctx, f.scope, f.ref, HandoffOfferCommand{
		ChannelID: f.channel.ID, WorkItemID: f.workID,
		MessageID: f.message.ID, DeliveryID: f.delivery.ID,
		Content: content, IfMatch: "\"v1\"", IdempotencyKey: model.NewID().String(),
	})
	if err != nil {
		t.Fatalf("offer Handoff: %v", err)
	}
	return offer
}

// revokeIncomingHandoffTestGrant revokes the recipient's own read grant on the
// carrier Channel through a real durable row change, so the next authority close
// observes a revocation instead of a stubbed verdict.
func (f incomingHandoffFixture) revokeRecipientGrant(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	for _, row := range communicationRowsForTest(t, f.directNoticeFixture, channelGrantKind) {
		grant, err := channelGrantFromRecord(row)
		if err != nil {
			t.Fatalf("decode ChannelGrant: %v", err)
		}
		if grant.Subject.Ref != f.delivery.Recipient.Ref || grant.State != ChannelGrantActive {
			continue
		}
		grant.State = ChannelGrantRevoked
		grant.RevokedBy = &CommunicationActorRef{Kind: ActorUser, Ref: f.sender.String()}
		record, encodeErr := channelGrantToRecord(grant)
		if encodeErr != nil {
			t.Fatalf("encode revoked ChannelGrant: %v", encodeErr)
		}
		if _, err := communicationUpdate(ctx, f.m, f.tenant, channelGrantKind, record); err != nil {
			t.Fatalf("revoke recipient ChannelGrant: %v", err)
		}
		return
	}
	t.Fatal("recipient has no active ChannelGrant to revoke")
}

// TestIncomingHandoffNavigationIsItsOwnTokenDomain proves the h3n1 family is
// separate from the direct-notice cursor and the channel catalog: neither
// verifier accepts the other's token even under the SAME keyring, and the filter
// hash binds the exact state selector.
func TestIncomingHandoffNavigationIsItsOwnTokenDomain(t *testing.T) {
	t.Parallel()

	ring := newChannelCatalogNavigationKeyring(t, "h3n1-domain")
	observedAt := time.Now().UTC()
	filter, err := incomingHandoffFilterHash(HandoffOffered)
	if err != nil {
		t.Fatalf("offered filter hash: %v", err)
	}
	claims := communicationIncomingHandoffNavigationClaims{
		tenantID: model.TenantID(model.NewID()), workspaceID: model.NewID(),
		recipient: RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()},
		state:     HandoffOffered, filterHash: filter[:],
		anchorDeadline:  model.NewTimestamp(observedAt.Add(time.Hour)).String(),
		anchorHandoffID: model.NewID(),
	}
	token, err := ring.mintIncomingHandoffNavigation(claims, observedAt)
	if err != nil {
		t.Fatalf("mint h3n1: %v", err)
	}
	if !strings.HasPrefix(token, communicationIncomingHandoffNavigationPrefix+".") {
		t.Fatalf("h3n1 token has a foreign prefix: %q", token)
	}
	verified, err := ring.verifyIncomingHandoffNavigation(token, observedAt)
	if err != nil {
		t.Fatalf("verify h3n1: %v", err)
	}
	if verified.recipient != claims.recipient || verified.state != claims.state ||
		verified.anchorDeadline != claims.anchorDeadline ||
		verified.anchorHandoffID != claims.anchorHandoffID {
		t.Fatalf("h3n1 round trip = %+v", verified)
	}

	// Cross-family, both directions, same keyring.
	if _, err := ring.verifyInboxNavigation(token, observedAt); err == nil {
		t.Fatal("the direct-notice cursor verifier accepted an h3n1 token")
	}
	if _, err := ring.verifyChannelCatalogNavigation(token, observedAt); err == nil {
		t.Fatal("the channel catalog verifier accepted an h3n1 token")
	}
	catalogToken, err := ring.mintChannelCatalogNavigation(
		channelCatalogNavigationTestClaims(t), observedAt,
	)
	if err != nil {
		t.Fatalf("mint c3n1: %v", err)
	}
	if _, err := ring.verifyIncomingHandoffNavigation(catalogToken, observedAt); err == nil {
		t.Fatal("the incoming handoff verifier accepted a c3n1 token")
	}

	// The filter hash is per state: a token minted for one selector cannot be
	// re-labelled as another, because the label is inside the MAC and the hash
	// commits the state it was minted for.
	acceptedFilter, err := incomingHandoffFilterHash(HandoffAccepted)
	if err != nil {
		t.Fatalf("accepted filter hash: %v", err)
	}
	if bytes.Equal(acceptedFilter[:], filter[:]) {
		t.Fatal("two state filters share one hash")
	}
	crossed := claims
	crossed.state = HandoffAccepted
	if _, err := ring.mintIncomingHandoffNavigation(crossed, observedAt); err == nil {
		t.Fatal("minted an h3n1 token whose state disagrees with its filter hash")
	}

	// A session recipient carries its exact claim generation; nobody else may.
	sessionClaims := claims
	sessionClaims.recipient = RecipientRef{Kind: RecipientSession, Ref: "osn_" + model.NewID().String()}
	if _, err := ring.mintIncomingHandoffNavigation(sessionClaims, observedAt); err == nil {
		t.Fatal("minted a session anchor without its claim generation")
	}
	sessionClaims.sessionSID = sessionClaims.recipient.Ref
	sessionClaims.sessionFence = 7
	sessionToken, err := ring.mintIncomingHandoffNavigation(sessionClaims, observedAt)
	if err != nil {
		t.Fatalf("mint session h3n1: %v", err)
	}
	sessionVerified, err := ring.verifyIncomingHandoffNavigation(sessionToken, observedAt)
	if err != nil || sessionVerified.sessionFence != 7 {
		t.Fatalf("session h3n1 = %+v, err %v", sessionVerified, err)
	}
	foreignSession := claims
	foreignSession.sessionSID = "osn_" + model.NewID().String()
	foreignSession.sessionFence = 3
	if _, err := ring.mintIncomingHandoffNavigation(foreignSession, observedAt); err == nil {
		t.Fatal("minted a user anchor carrying a foreign session binding")
	}

	// A non-canonical anchor instant never reaches a query.
	truncated := claims
	truncated.anchorDeadline = observedAt.Add(time.Hour).UTC().Format(time.RFC3339)
	if _, err := ring.mintIncomingHandoffNavigation(truncated, observedAt); err == nil {
		t.Fatal("minted an h3n1 token with a non-canonical anchor instant")
	}

	// Expiry and forgery.
	if _, err := ring.verifyIncomingHandoffNavigation(
		token, observedAt.Add(communicationCursorTokenTTL+communicationCursorTokenClockSkew),
	); !errors.Is(err, errCommunicationCursorTokenExpired) {
		t.Fatalf("expired h3n1 = %v", err)
	}
	forged := token[:len(token)-1] + string(rune(token[len(token)-1]^0x01))
	if _, err := ring.verifyIncomingHandoffNavigation(forged, observedAt); err == nil {
		t.Fatal("verified a forged h3n1 MAC")
	}
}

// TestIncomingHandoffOfferContextMeasuresTheWorkItPromises pins offer_context to
// exactly the predicate the response mutation uses to refuse a stale offer.
func TestIncomingHandoffOfferContextMeasuresTheWorkItPromises(t *testing.T) {
	t.Parallel()

	scope := DirectoryScopeRef{TenantID: model.TenantID(model.NewID()), WorkspaceID: model.NewID()}
	workID := model.NewID()
	handoff := Handoff{
		WorkItemID:     workID,
		From:           RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()},
		To:             RecipientRef{Kind: RecipientUser, Ref: model.NewID().String()},
		FromOwnerEpoch: 3, OfferedLeaseFence: 7, ContextEventSeq: 11,
		State: HandoffOffered,
	}
	current := func() handoffLockedWork {
		return handoffLockedWork{
			item: model.Record{
				model.ColID: workID.String(), colWorkWorkspaceID: scope.WorkspaceID.String(),
				colWorkStatus: "in_progress", colWorkOwnerKind: string(RecipientUser),
				colWorkOwnerRef: handoff.From.Ref, colWorkOwnerEpoch: int64(3),
				colWorkLastEventSeq: int64(12),
			},
			lease: model.Record{
				colWorkWorkspaceID: scope.WorkspaceID.String(), colWorkItemID: workID.String(),
			},
			leaseState: fenceState{Fence: 7},
		}
	}
	if got, err := incomingHandoffOfferContext(scope, handoff, current()); err != nil ||
		got != IncomingHandoffContextCurrent {
		t.Fatalf("coherent offer context = %q, err %v", got, err)
	}
	for _, terminal := range []HandoffState{
		HandoffAccepted, HandoffRejected, HandoffWithdrawn, HandoffExpired,
	} {
		done := handoff
		done.State = terminal
		if got, err := incomingHandoffOfferContext(scope, done, handoffLockedWork{}); err != nil ||
			got != IncomingHandoffContextTerminal {
			t.Fatalf("%s offer context = %q, err %v", terminal, got, err)
		}
	}
	drifts := map[string]func(*handoffLockedWork){
		"owner changed":         func(w *handoffLockedWork) { w.item[colWorkOwnerRef] = model.NewID().String() },
		"owner kind changed":    func(w *handoffLockedWork) { w.item[colWorkOwnerKind] = string(RecipientSession) },
		"owner epoch advanced":  func(w *handoffLockedWork) { w.item[colWorkOwnerEpoch] = int64(4) },
		"context sequence past": func(w *handoffLockedWork) { w.item[colWorkLastEventSeq] = int64(13) },
		"lease refenced":        func(w *handoffLockedWork) { w.leaseState.Fence = 8 },
		"work terminal":         func(w *handoffLockedWork) { w.item[colWorkStatus] = "completed" },
		"work left workspace": func(w *handoffLockedWork) {
			w.item[colWorkWorkspaceID] = model.NewID().String()
		},
		"lease left workspace": func(w *handoffLockedWork) {
			w.lease[colWorkWorkspaceID] = model.NewID().String()
		},
		"lease names other work": func(w *handoffLockedWork) {
			w.lease[colWorkItemID] = model.NewID().String()
		},
		"work row is another item": func(w *handoffLockedWork) {
			w.item[model.ColID] = model.NewID().String()
		},
	}
	for name, drift := range drifts {
		work := current()
		drift(&work)
		got, err := incomingHandoffOfferContext(scope, handoff, work)
		if err != nil || got != IncomingHandoffContextStale {
			t.Fatalf("%s offer context = %q, err %v, want stale", name, got, err)
		}
	}
	// Missing work rows are UNKNOWN, never a quiet "current".
	if _, err := incomingHandoffOfferContext(scope, handoff, handoffLockedWork{}); !errors.Is(
		err, ErrCommunicationEvidenceUnknown,
	) {
		t.Fatalf("offer context without work rows = %v", err)
	}
}

// TestIncomingHandoffRecipientDiscoversAndReadsItsOwnOffer is the personal
// discovery path the sender receipt cannot provide, with the sender, a
// non-recipient and the public boundary all refused, and no effect written.
func TestIncomingHandoffRecipientDiscoversAndReadsItsOwnOffer(t *testing.T) {
	t.Parallel()

	fixture := newIncomingHandoffFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)
	content := HandoffContent{
		Summary: "Transfer the K3 read surface", NextAction: "Continue with the recipient list",
		Risk: "The offer expires in four minutes",
	}
	offer := fixture.offer(t, content)
	before := incomingHandoffEffectDigest(t, fixture)

	// The public boundary is still gated by the aggregate readiness conjunction.
	if _, err := fixture.m.ListIncomingHandoffs(
		ctx, fixture.scope, fixture.targetRef, IncomingHandoffRequest{},
	); !errors.Is(err, ErrCommunicationEvidenceUnknown) {
		t.Fatalf("public incoming handoff listing before readiness = %v", err)
	}
	if _, err := fixture.m.GetIncomingHandoffByDelivery(
		ctx, fixture.scope, fixture.targetRef, fixture.delivery.ID,
	); !errors.Is(err, ErrCommunicationEvidenceUnknown) {
		t.Fatalf("public incoming handoff read before readiness = %v", err)
	}

	page, err := fixture.m.listIncomingHandoffsWithAuthority(
		ctx, fixture.scope, fixture.targetRef, IncomingHandoffRequest{},
	)
	if err != nil {
		t.Fatalf("recipient listing: %v", err)
	}
	if len(page.Items) != 1 || page.HasMore || page.Continuation != "" {
		t.Fatalf("recipient page = %+v", page)
	}
	item := page.Items[0]
	if item.Handoff.ID != offer.HandoffID || item.Handoff.Version != offer.Version ||
		item.Handoff.ETag != offer.ETag || item.Handoff.State != HandoffOffered ||
		item.Handoff.To != fixture.delivery.Recipient ||
		item.Carrier.DeliveryID != fixture.delivery.ID ||
		item.Carrier.MessageID != fixture.message.ID ||
		item.Carrier.ChannelID != fixture.channel.ID ||
		item.WorkItem.ID != fixture.workID ||
		item.WorkItem.Presentation != incomingHandoffWorkItemPresentation ||
		item.ObservedAt.IsZero() || item.DeadlineElapsed {
		t.Fatalf("recipient card = %+v", item)
	}
	// The listing never opens content: the card has no field that could carry it.
	if raw, marshalErr := json.Marshal(item); marshalErr != nil ||
		strings.Contains(string(raw), content.Summary) ||
		strings.Contains(string(raw), content.NextAction) ||
		strings.Contains(string(raw), content.Risk) {
		t.Fatalf("listing card leaked protected content: %s (%v)", raw, marshalErr)
	}

	read, err := fixture.m.getIncomingHandoffByDeliveryWithAuthority(
		ctx, fixture.scope, fixture.targetRef, fixture.delivery.ID,
	)
	if err != nil {
		t.Fatalf("recipient detail read: %v", err)
	}
	if !reflect.DeepEqual(read.Content, content) {
		t.Fatalf("recipient content = %+v, want %+v", read.Content, content)
	}
	if read.OfferContext != IncomingHandoffContextCurrent || read.TerminalReason != nil ||
		read.Handoff.ID != offer.HandoffID || read.Handoff.ETag != offer.ETag ||
		read.WorkItem.Presentation != incomingHandoffWorkItemPresentation {
		t.Fatalf("recipient detail = %+v", read)
	}
	// The WorkItem is a reference, never the record: its stored title and brief
	// are not reachable through this surface.
	workRecord := handoffStoredRecord(t, fixture.handoffServiceFixture, workItemKind, fixture.workID)
	raw, err := json.Marshal(read)
	if err != nil {
		t.Fatalf("marshal recipient detail: %v", err)
	}
	for _, column := range []string{colWorkTitle, colWorkBrief} {
		if value := workRecord.String(column); value != "" && strings.Contains(string(raw), value) {
			t.Fatalf("detail projected WorkItem %s: %s", column, raw)
		}
	}

	// The sender holds the receipt and is not the recipient; a non-recipient
	// member of the same workspace is not either. Neither discovers this offer.
	senderPage, err := fixture.m.listIncomingHandoffsWithAuthority(
		ctx, fixture.scope, fixture.ref, IncomingHandoffRequest{},
	)
	if err != nil || len(senderPage.Items) != 0 || senderPage.HasMore {
		t.Fatalf("sender listing = %+v, err %v", senderPage, err)
	}
	if _, err := fixture.m.getIncomingHandoffByDeliveryWithAuthority(
		ctx, fixture.scope, fixture.ref, fixture.delivery.ID,
	); !errors.Is(err, ErrCommunicationNotFound) {
		t.Fatalf("sender detail read = %v, want not found", err)
	}

	// A terminal state filter does not show an offered Handoff, and an unknown
	// selector is refused rather than silently defaulted.
	for _, state := range []HandoffState{
		HandoffAccepted, HandoffRejected, HandoffWithdrawn, HandoffExpired,
	} {
		filtered, filterErr := fixture.m.listIncomingHandoffsWithAuthority(
			ctx, fixture.scope, fixture.targetRef, IncomingHandoffRequest{State: state},
		)
		if filterErr != nil || len(filtered.Items) != 0 {
			t.Fatalf("%s listing = %+v, err %v", state, filtered, filterErr)
		}
	}
	if _, err := fixture.m.listIncomingHandoffsWithAuthority(
		ctx, fixture.scope, fixture.targetRef, IncomingHandoffRequest{State: "OFFERED"},
	); !errors.Is(err, ErrInvalidCommunicationModel) {
		t.Fatalf("non-canonical state selector = %v", err)
	}

	if after := incomingHandoffEffectDigest(t, fixture); after != before {
		t.Fatal("a personal handoff read wrote a durable effect")
	}

	// The recipient responds through the EXISTING endpoint with the ETag this
	// surface handed it, and the response is the first effect of the journey.
	response, err := fixture.m.respondHandoffWithAuthority(
		ctx, fixture.scope, fixture.targetRef, read.Handoff.ID,
		HandoffResponseCommand{
			Transition: HandoffAccept, IfMatch: read.Handoff.ETag,
			IdempotencyKey: model.NewID().String(),
		},
	)
	if err != nil {
		t.Fatalf("accept with the ETag the personal read published: %v", err)
	}
	if response.State != HandoffAccepted || response.Version != read.Handoff.Version+1 {
		t.Fatalf("accept result = %+v", response)
	}
	// After acceptance the offer leaves the offered filter and appears in its own,
	// still without a receipt for the recipient.
	offered, err := fixture.m.listIncomingHandoffsWithAuthority(
		ctx, fixture.scope, fixture.targetRef, IncomingHandoffRequest{State: HandoffOffered},
	)
	if err != nil || len(offered.Items) != 0 {
		t.Fatalf("offered listing after accept = %+v, err %v", offered, err)
	}
	accepted, err := fixture.m.listIncomingHandoffsWithAuthority(
		ctx, fixture.scope, fixture.targetRef, IncomingHandoffRequest{State: HandoffAccepted},
	)
	if err != nil || len(accepted.Items) != 1 ||
		accepted.Items[0].Handoff.State != HandoffAccepted ||
		accepted.Items[0].Handoff.TerminalAt == nil {
		t.Fatalf("accepted listing = %+v, err %v", accepted, err)
	}
	acceptedRead, err := fixture.m.getIncomingHandoffByDeliveryWithAuthority(
		ctx, fixture.scope, fixture.targetRef, fixture.delivery.ID,
	)
	if err != nil || acceptedRead.OfferContext != IncomingHandoffContextTerminal ||
		!reflect.DeepEqual(acceptedRead.Content, content) {
		t.Fatalf("accepted detail = %+v, err %v", acceptedRead, err)
	}
}

// TestIncomingHandoffRefusesBytesWhenAuthorityIsWithdrawnDuringTheOpen uses the
// content opener itself as the barrier: the recipient's read grant is revoked
// while the payload is being opened, and the answer is unavailable with no
// content, not the bytes the first close authorized.
func TestIncomingHandoffRefusesBytesWhenAuthorityIsWithdrawnDuringTheOpen(t *testing.T) {
	t.Parallel()

	fixture := newIncomingHandoffFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)
	fixture.offer(t, HandoffContent{Summary: "Withdrawn mid-open", NextAction: "Publish nothing"})

	opened := 0
	revoked := make(chan struct{})
	opener := func(
		openCtx context.Context,
		sealer CommunicationContentSealer,
		plan ProtectedPayloadOpenPlan,
	) (json.RawMessage, error) {
		opened++
		if opened == 1 {
			// The revocation COMMITS before the opener returns, so the second
			// authority close cannot avoid observing it. No sleep is involved.
			fixture.revokeRecipientGrant(t)
			close(revoked)
		}
		return OpenProtectedPayload(openCtx, sealer, plan)
	}
	result, err := fixture.m.getIncomingHandoffByDeliveryWithAuthorityAndOpener(
		ctx, fixture.scope, fixture.targetRef, fixture.delivery.ID, opener,
	)
	select {
	case <-revoked:
	default:
		t.Fatal("the barrier never ran: the content opener was not reached")
	}
	if !errors.Is(err, ErrCommunicationEvidenceUnknown) {
		t.Fatalf("read across a revocation = %v, want evidence unavailable", err)
	}
	if !reflect.DeepEqual(result, IncomingHandoffReadResult{}) {
		t.Fatalf("read across a revocation published a partial result: %+v", result)
	}
	// And the revocation is durable: the offer is simply not visible any more.
	page, err := fixture.m.listIncomingHandoffsWithAuthority(
		ctx, fixture.scope, fixture.targetRef, IncomingHandoffRequest{},
	)
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("listing after revocation = %+v, err %v", page, err)
	}
}

// TestIncomingHandoffRefusesUnopenableOrNonCanonicalContent covers the custody
// failures that must answer 503 with no partial payload: an unavailable sealer,
// bytes that are not canonical slot content, and bytes for another slot.
func TestIncomingHandoffRefusesUnopenableOrNonCanonicalContent(t *testing.T) {
	t.Parallel()

	fixture := newIncomingHandoffFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)
	fixture.offer(t, HandoffContent{Summary: "Custody", NextAction: "Refuse partial bytes"})

	cases := map[string]directNoticePayloadOpener{
		"sealer unavailable": func(
			context.Context, CommunicationContentSealer, ProtectedPayloadOpenPlan,
		) (json.RawMessage, error) {
			return nil, communicationContentUnavailable("content sealer is not configured", nil)
		},
		"non-canonical bytes": func(
			context.Context, CommunicationContentSealer, ProtectedPayloadOpenPlan,
		) (json.RawMessage, error) {
			// Canonical slot content orders its keys; these are the same values in
			// declaration order, which the canonical form is not.
			return json.RawMessage(`{"summary":"a","next_action":"b"}`), nil
		},
		"foreign slot bytes": func(
			context.Context, CommunicationContentSealer, ProtectedPayloadOpenPlan,
		) (json.RawMessage, error) {
			return json.RawMessage(`{"code":"not-a-handoff"}`), nil
		},
		"trailing values": func(
			context.Context, CommunicationContentSealer, ProtectedPayloadOpenPlan,
		) (json.RawMessage, error) {
			return json.RawMessage(`{"summary":"a","next_action":"b"}{}`), nil
		},
	}
	for name, opener := range cases {
		result, err := fixture.m.getIncomingHandoffByDeliveryWithAuthorityAndOpener(
			ctx, fixture.scope, fixture.targetRef, fixture.delivery.ID, opener,
		)
		if !errors.Is(err, ErrCommunicationEvidenceUnknown) {
			t.Fatalf("%s = %v, want evidence unavailable", name, err)
		}
		if !reflect.DeepEqual(result, IncomingHandoffReadResult{}) {
			t.Fatalf("%s published a partial result: %+v", name, result)
		}
	}
	// The open plan carries the Handoff AAD, not the Message AAD: a plan built
	// for the carrier Message could never open this payload.
	planned, hidden, err := fixture.m.closeIncomingHandoffPointRead(
		ctx, fixture.scope, fixture.targetRef, fixture.delivery.ID,
	)
	if err != nil || hidden {
		t.Fatalf("close point read: hidden=%t err=%v", hidden, err)
	}
	if planned.openPlan.AAD.EntityKind != handoffKind ||
		planned.openPlan.AAD.EntityID != planned.summary.Handoff.ID ||
		planned.openPlan.AAD.ChannelID != fixture.channel.ID ||
		planned.openPlan.AAD.TenantID != fixture.tenant ||
		planned.openPlan.AAD.WorkspaceID != fixture.workspace ||
		planned.openPlan.Slot != PayloadSlotHandoff {
		t.Fatalf("handoff open plan AAD = %+v", planned.openPlan.AAD)
	}
	if planned.reasonPlan != nil {
		t.Fatalf("offered Handoff planned a terminal reason: %+v", planned.reasonPlan)
	}
}

// TestIncomingHandoffPointReadRefusesAForeignDelivery proves the surface answers
// by the DELIVERY it was asked about and never by a neighbouring carrier.
func TestIncomingHandoffPointReadRefusesAForeignDelivery(t *testing.T) {
	t.Parallel()

	fixture := newIncomingHandoffFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)
	fixture.offer(t, HandoffContent{Summary: "Exact carrier", NextAction: "Refuse the neighbour"})

	for name, target := range map[string]model.ID{
		"absent delivery":   model.NewID(),
		"message id":        fixture.message.ID,
		"non-canonical":     model.ID("not-a-uuid"),
		"channel id":        fixture.channel.ID,
		"work item id":      fixture.workID,
		"zero-valued input": model.ID(""),
	} {
		if _, err := fixture.m.getIncomingHandoffByDeliveryWithAuthority(
			ctx, fixture.scope, fixture.targetRef, target,
		); !errors.Is(err, ErrCommunicationNotFound) &&
			!errors.Is(err, ErrCommunicationEvidenceUnknown) {
			t.Fatalf("%s point read = %v", name, err)
		}
	}
}

func incomingHandoffEffectDigest(t *testing.T, fixture incomingHandoffFixture) string {
	t.Helper()
	var out strings.Builder
	for _, kind := range []model.Kind{
		handoffKind, messageKind, messageDeliveryKind, messageAckKind, workItemKind,
		workLeaseKind, workEventKind, communicationCommandKind, inboxCursorKind,
		inboxCursorBarrierKind, workOutboxKind,
	} {
		rows := communicationRowsForTest(t, fixture.directNoticeFixture, kind)
		raw, err := json.Marshal(rows)
		if err != nil {
			t.Fatalf("marshal %s effect census: %v", kind, err)
		}
		out.WriteString(string(kind))
		out.Write(raw)
	}
	var audits int
	if err := fixture.m.data.View(
		context.Background(), fixture.tenant, func(sc store.Scope) error {
			return sc.Audit().Walk(context.Background(), 1, func(model.AuditEvent) error {
				audits++
				return nil
			})
		},
	); err != nil {
		t.Fatalf("walk audit census: %v", err)
	}
	out.WriteString("audits:")
	out.WriteString(model.NewTimestamp(time.Unix(int64(audits), 0)).String())
	return out.String()
}

// TestIncomingHandoffReportsAnElapsedDeadlineWithoutTransitioningIt waits on the
// DURABLE clock — the same observable barrier the deadline reaper test uses, not
// a sleep offered as proof — and shows the read REPORTS the elapsed window while
// leaving the persisted state exactly as it was: no reaper, no expiry transition,
// no effect of any kind.
func TestIncomingHandoffReportsAnElapsedDeadlineWithoutTransitioningIt(t *testing.T) {
	t.Parallel()

	fixture := newIncomingHandoffFixtureWithDurableAckDelay(t, 5*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)
	offer := fixture.offer(t, HandoffContent{
		Summary: "Elapsed window", NextAction: "Report, do not transition",
	})

	live, err := fixture.m.getIncomingHandoffByDeliveryWithAuthority(
		ctx, fixture.scope, fixture.targetRef, fixture.delivery.ID,
	)
	if err != nil || live.OfferContext != IncomingHandoffContextCurrent {
		t.Fatalf("read inside the window = %+v, err %v", live, err)
	}
	before := incomingHandoffEffectDigest(t, fixture)
	waitHandoffDurableDeadline(t, fixture.handoffServiceFixture, live.Handoff.AckDeadline)

	elapsed, err := fixture.m.getIncomingHandoffByDeliveryWithAuthority(
		ctx, fixture.scope, fixture.targetRef, fixture.delivery.ID,
	)
	if err != nil {
		t.Fatalf("read after the window: %v", err)
	}
	if !elapsed.DeadlineElapsed || elapsed.Handoff.State != HandoffOffered ||
		elapsed.Handoff.Version != offer.Version || elapsed.Handoff.TerminalAt != nil ||
		elapsed.Handoff.TerminalCode != "" || elapsed.TerminalReason != nil {
		t.Fatalf("read after the window = %+v", elapsed)
	}
	// The persisted state is still `offered`, so it is still what the offered
	// listing shows: an elapsed window is a fact about time, not a transition.
	page, err := fixture.m.listIncomingHandoffsWithAuthority(
		ctx, fixture.scope, fixture.targetRef, IncomingHandoffRequest{State: HandoffOffered},
	)
	if err != nil || len(page.Items) != 1 || !page.Items[0].DeadlineElapsed ||
		page.Items[0].Handoff.State != HandoffOffered {
		t.Fatalf("offered listing after the window = %+v, err %v", page, err)
	}
	if after := incomingHandoffEffectDigest(t, fixture); after != before {
		t.Fatal("reading an elapsed offer transitioned or wrote something")
	}
	// And the mutation still refuses it: the read never promised acceptance.
	if _, err := fixture.m.respondHandoffWithAuthority(
		ctx, fixture.scope, fixture.targetRef, offer.HandoffID,
		HandoffResponseCommand{
			Transition: HandoffAccept, IfMatch: elapsed.Handoff.ETag,
			IdempotencyKey: model.NewID().String(),
		},
	); err == nil {
		t.Fatal("accepted a Handoff whose response window had elapsed")
	}
	if after := incomingHandoffEffectDigest(t, fixture); after != before {
		t.Fatal("the refused response wrote a durable effect")
	}
}

// TestIncomingHandoffTargetIndexPinsTheScannedShape is the index positive
// control: the recipient scan adds NO index and NO migration, so its exactness
// depends on the EXISTING sessions_work_handoff_target column order. Both
// candidate queries — the anchor's tie group and everything strictly after it —
// bind that order as a prefix: tenant and workspace lineage the store forces,
// then the recipient, then the state, then the deadline, then the id tiebreaker
// the store always appends. A change to either side fails here instead of
// silently degrading a personal listing into a table scan.
func TestIncomingHandoffTargetIndexPinsTheScannedShape(t *testing.T) {
	t.Parallel()

	reg := communicationCaptureSchema(t)
	descriptor := communicationDescriptor(t, reg, handoffKind)
	want := []string{
		"tenant_id", "workspace_id", "to_kind", "to_ref", "state", "ack_deadline", "id",
	}
	var found *model.IndexSpec
	for index := range descriptor.Indexes {
		if descriptor.Indexes[index].Name == "sessions_work_handoff_target" {
			found = &descriptor.Indexes[index]
		}
	}
	if found == nil {
		t.Fatalf("sessions_work_handoff_target is not declared on %s", handoffKind)
	}
	if found.Unique || !reflect.DeepEqual(found.Columns, want) {
		t.Fatalf("target index = %+v, want non-unique %v", *found, want)
	}
	// The columns the two candidate queries bind, in the order the index declares
	// them. The tenant and workspace terms are supplied by the confined scope, so
	// they are named here rather than repeated in every filter list.
	scanned := []string{
		model.ColTenantID, colWorkWorkspaceID, colCommToKind, colCommToRef,
		colCommState, colCommAckDeadline, model.ColID,
	}
	if !reflect.DeepEqual(scanned, want) {
		t.Fatalf("scanned columns %v differ from the index %v", scanned, want)
	}
	// The delivery lookup of the point read is the OTHER existing index, and it is
	// unique: one Delivery carries at most one Handoff, which is why an ambiguous
	// answer is an evidence failure rather than a choice.
	var delivery *model.IndexSpec
	for index := range descriptor.Indexes {
		if descriptor.Indexes[index].Name == "sessions_work_handoff_delivery_uniq" {
			delivery = &descriptor.Indexes[index]
		}
	}
	if delivery == nil || !delivery.Unique ||
		!reflect.DeepEqual(delivery.Columns, []string{model.ColTenantID, colCommDeliveryID}) {
		t.Fatalf("delivery uniqueness index = %+v", delivery)
	}
	// No index was added for this surface: the Handoff descriptor still declares
	// exactly the five it declared before, plus the workspace lineage index the
	// helper contributes.
	names := make([]string, 0, len(descriptor.Indexes))
	for _, index := range descriptor.Indexes {
		names = append(names, index.Name)
	}
	sort.Strings(names)
	wantNames := []string{
		"sessions_work_handoff_delivery_uniq", "sessions_work_handoff_due",
		"sessions_work_handoff_message_uniq", "sessions_work_handoff_target",
		"sessions_work_handoff_work", "sessions_work_handoff_workspace",
	}
	if !reflect.DeepEqual(names, wantNames) {
		t.Fatalf("Handoff indexes = %v, want exactly %v", names, wantNames)
	}
}

// TestIncomingHandoffSpentBudgetNeverClaimsExhaustion is the other half of the
// has_more contract: a scan that runs out of candidate budget must report that it
// did NOT reach the end of the recipient's rows. Only exhaustion licenses
// has_more=false, so a budget that says "exhausted" would be the exact lie the
// listing exists to avoid.
func TestIncomingHandoffSpentBudgetNeverClaimsExhaustion(t *testing.T) {
	t.Parallel()

	fixture := newIncomingHandoffFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)
	fixture.offer(t, HandoffContent{Summary: "Budget", NextAction: "Never claim exhaustion"})

	identity, err := fixture.m.bindCurrentCommunicationIdentity(
		ctx, fixture.scope, fixture.targetRef, requireIncomingHandoffRecipientPrincipal,
	)
	if err != nil {
		t.Fatalf("bind recipient identity: %v", err)
	}
	reader, err := fixture.m.preflightDirectNoticeReaderIdentity(
		ctx, fixture.scope, identity.principal, nil,
	)
	if err != nil {
		t.Fatalf("preflight recipient: %v", err)
	}
	spent, progress, exhausted, err := fixture.m.discoverIncomingHandoffCandidates(
		ctx, identity, reader, HandoffOffered, incomingHandoffAnchor{}, 1, 0,
	)
	if err != nil || len(spent) != 0 || progress.scanned != 0 || exhausted {
		t.Fatalf("spent budget = %d candidates, scanned %d, exhausted %t, err %v",
			len(spent), progress.scanned, exhausted, err)
	}
	// With budget the same scan finds the offer AND reports exhaustion honestly,
	// so the control above is not passing for want of anything to find.
	found, progress, exhausted, err := fixture.m.discoverIncomingHandoffCandidates(
		ctx, identity, reader, HandoffOffered, incomingHandoffAnchor{}, 2, 128,
	)
	if err != nil || len(found) != 1 || progress.scanned != 1 || !exhausted {
		t.Fatalf("funded scan = %d candidates, scanned %d, exhausted %t, err %v",
			len(found), progress.scanned, exhausted, err)
	}
}

// TestIncomingHandoffPublishesTwoAggregateVersionsAndOnlyAcceptsItsOwn makes the
// Handoff and its Delivery genuinely DIVERGE — a reject advances the Handoff and
// deliberately leaves the Delivery untouched — and then uses the REAL current
// value of each, never an invented one.
//
// The independent review is right that presenting `delivery_version + 1` only
// proves a made-up CAS is refused: while the two numbers coincide, a service that
// wrongly took the Delivery version would still pass. After the divergence the
// two values are 2 and 1, and the refusals separate cleanly at the source:
// the real Delivery version fails the VERSION check, while the real Handoff ETag
// passes it and fails later on state. That is the discrimination the flat HTTP
// status cannot show, because both answer 409.
func TestIncomingHandoffPublishesTwoAggregateVersionsAndOnlyAcceptsItsOwn(t *testing.T) {
	t.Parallel()

	fixture := newIncomingHandoffFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)
	offer := fixture.offer(t, HandoffContent{
		Summary: "Two aggregates", NextAction: "Diverge their versions",
	})

	before, err := fixture.m.getIncomingHandoffByDeliveryWithAuthority(
		ctx, fixture.scope, fixture.targetRef, fixture.delivery.ID,
	)
	if err != nil {
		t.Fatalf("read before the divergence: %v", err)
	}
	if before.Handoff.Version != 1 || before.Carrier.DeliveryVersion != 1 {
		t.Fatalf("the two versions do not start equal: %+v", before)
	}

	// Reject: the Handoff transitions, and the Delivery is deliberately NOT
	// touched (only an accept acknowledges it), so the two aggregates diverge.
	rejected, err := fixture.m.respondHandoffWithAuthority(
		ctx, fixture.scope, fixture.targetRef, offer.HandoffID,
		HandoffResponseCommand{
			Transition: HandoffReject, IfMatch: before.Handoff.ETag,
			Reason:         &CommunicationReasonContent{Code: "not_mine"},
			IdempotencyKey: model.NewID().String(),
		},
	)
	if err != nil {
		t.Fatalf("reject with the published Handoff ETag: %v", err)
	}
	if rejected.State != HandoffRejected || rejected.Version != 2 {
		t.Fatalf("reject result = %+v", rejected)
	}
	storedDelivery, err := messageDeliveryFromRecord(handoffStoredRecord(
		t, fixture.handoffServiceFixture, messageDeliveryKind, fixture.delivery.ID,
	))
	if err != nil {
		t.Fatalf("decode the stored Delivery: %v", err)
	}
	if storedDelivery.Version != 1 {
		t.Fatalf("the reject moved the Delivery to v%d; the divergence is not real",
			storedDelivery.Version)
	}

	after, err := fixture.m.getIncomingHandoffByDeliveryWithAuthority(
		ctx, fixture.scope, fixture.targetRef, fixture.delivery.ID,
	)
	if err != nil {
		t.Fatalf("read after the divergence: %v", err)
	}
	if after.Handoff.Version != 2 || after.Handoff.ETag != "\"v2\"" ||
		after.Carrier.DeliveryVersion != 1 ||
		after.Carrier.DeliveryVersion != storedDelivery.Version {
		t.Fatalf("the projection does not publish both real, divergent values: %+v", after)
	}

	// The REAL current Delivery version, presented as the Handoff CAS coordinate.
	// It must fail the VERSION check, not some later one.
	deliveryAsETag := communicationVersionETag(after.Carrier.DeliveryVersion)
	if deliveryAsETag == after.Handoff.ETag {
		t.Fatal("the two aggregates did not diverge, so this control proves nothing")
	}
	_, err = fixture.m.respondHandoffWithAuthority(
		ctx, fixture.scope, fixture.targetRef, offer.HandoffID,
		HandoffResponseCommand{
			Transition: HandoffReject, IfMatch: deliveryAsETag,
			Reason:         &CommunicationReasonContent{Code: "delivery_version"},
			IdempotencyKey: model.NewID().String(),
		},
	)
	if !errors.Is(err, errHandoffVersionMismatch) {
		t.Fatalf("the real Delivery version was not refused as a version mismatch: %v", err)
	}
	if errors.Is(err, errHandoffStaleOffer) {
		t.Fatalf("the real Delivery version reached the state check: %v", err)
	}

	// The REAL current Handoff ETag passes the version check and is refused later,
	// on the terminal state. That is what proves the ETag is the coordinate the
	// aggregate recognises.
	_, err = fixture.m.respondHandoffWithAuthority(
		ctx, fixture.scope, fixture.targetRef, offer.HandoffID,
		HandoffResponseCommand{
			Transition: HandoffReject, IfMatch: after.Handoff.ETag,
			Reason:         &CommunicationReasonContent{Code: "handoff_version"},
			IdempotencyKey: model.NewID().String(),
		},
	)
	if !errors.Is(err, errHandoffStaleOffer) {
		t.Fatalf("the real Handoff ETag did not reach the state check: %v", err)
	}
	if errors.Is(err, errHandoffVersionMismatch) {
		t.Fatalf("the real Handoff ETag failed the version check: %v", err)
	}
}
