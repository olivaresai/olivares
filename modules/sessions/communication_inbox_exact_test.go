// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

var _ func(
	*Module,
	context.Context,
	DirectoryScopeRef,
	auth.PrincipalRef,
	DirectNoticeInboxQuery,
) (DirectNoticeInboxPage, error) = (*Module).ListDirectNoticeInbox

type directNoticeInboxEvidenceSource struct {
	base     auth.AuthorizationEvidence
	outcomes map[model.ID]auth.EvidenceOutcome
	calls    int
	requests []auth.Request
	trace    *[]string
}

func (s *directNoticeInboxEvidenceSource) AuthorizeEvidence(
	_ context.Context,
	request auth.Request,
) auth.AuthorizationEvidence {
	s.calls++
	s.requests = append(s.requests, request)
	if s.trace != nil {
		*s.trace = append(*s.trace, "authorize")
	}
	evidence := s.base
	evidence.Facts = append([]store.AuthorizationFactRef(nil), s.base.Facts...)
	id, _ := model.ParseID(request.Resource.ID)
	outcome, configured := s.outcomes[id]
	if !configured {
		return evidence
	}
	switch outcome {
	case auth.EvidenceDeny:
		evidence.Outcome = auth.EvidenceDeny
		evidence.CorePermission = auth.CheckEvidence{
			Verdict: auth.CheckBroken, Code: "candidate_denied",
		}
	case auth.EvidenceUnknown:
		evidence = auth.AuthorizationEvidence{
			Outcome: auth.EvidenceUnknown,
			CorePermission: auth.CheckEvidence{
				Verdict: auth.CheckUnknown, Code: "candidate_unknown",
			},
			ResourceGuard: auth.CheckEvidence{
				Verdict: auth.CheckClean, Code: "resource_guard_clean",
			},
			ForbidAbsence: auth.CheckEvidence{
				Verdict: auth.CheckClean, Code: "forbid_absence_clean",
			},
		}
	}
	return evidence
}

func addDirectNoticeInboxMessage(
	t *testing.T,
	fixture directNoticeExactReadFixture,
	text string,
) DirectNoticePublishResult {
	t.Helper()
	result, err := fixture.m.publishDirectNotice(
		context.Background(), fixture.scope,
		CommunicationPrincipal{UserID: fixture.sender},
		fixture.command(model.NewID(), text),
	)
	if err != nil {
		t.Fatalf("add direct notice inbox message: %v", err)
	}
	return result
}

func TestDirectNoticeExactInboxClosesPageInOneMutation(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeExactReadFixture(t)
	second := addDirectNoticeInboxMessage(t, fixture, "exact inbox second")
	fixture.source.calls = 0
	fixture.source.requests = nil

	base := fixture.m.data
	trace := &directNoticeAuthorityTrace{}
	authorityFirst := &directNoticeExactAuthorityFirstData{inner: base}
	observer := &directNoticeMutateObserverData{inner: &directNoticeAuthorityTraceData{
		inner: authorityFirst, trace: trace,
	}}
	fixture.m.data = observer
	var opens int
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	page, err := fixture.m.listDirectNoticeInboxWithAuthorityAndOpener(
		ctx, fixture.scope, fixture.readerRef, DirectNoticeInboxQuery{Limit: 1},
		func(
			ctx context.Context,
			sealer CommunicationContentSealer,
			plan ProtectedPayloadOpenPlan,
		) (json.RawMessage, error) {
			opens++
			if observer.inMutate.Load() {
				return nil, errors.New("inbox payload opened before commit")
			}
			return OpenProtectedPayload(ctx, sealer, plan)
		},
	)
	if err != nil {
		t.Fatalf("list exact direct notice inbox: %v", err)
	}
	if len(page.Items) != 1 || !page.HasMore || opens != 1 ||
		page.Items[0].Delivery.ID != fixture.published.DeliveryID ||
		page.Items[0].Message.Content.Blocks[0].Text != "exact read canary" ||
		page.NextAfterDeliverySeq != page.Items[0].Delivery.DeliverySeq {
		t.Fatalf("exact inbox page = %+v; opens=%d; second=%s", page, opens, second.DeliveryID)
	}
	if observer.views.Load() != 1 || observer.mutates.Load() != 1 ||
		authorityFirst.earlyObservation.Load() || fixture.legacy.calls.Load() != 0 ||
		len(trace.authorityFacts) != 1 || len(trace.steps) != 1 ||
		trace.steps[0] != "authority" || trace.nowCalls != 3 {
		t.Fatalf(
			"exact inbox views/mutates/early/legacy/locks/steps/clock = %d/%d/%t/%d/%d/%v/%d",
			observer.views.Load(), observer.mutates.Load(),
			authorityFirst.earlyObservation.Load(), fixture.legacy.calls.Load(),
			len(trace.authorityFacts), trace.steps, trace.nowCalls,
		)
	}
	if fixture.source.calls != 2 || len(fixture.source.requests) != 2 {
		t.Fatalf(
			"exact inbox authority calls/requests = %d/%d",
			fixture.source.calls, len(fixture.source.requests),
		)
	}
	for _, request := range fixture.source.requests {
		if request.Permission != permDeliveryRead ||
			request.Resource.Kind != string(messageDeliveryKind) ||
			request.Resource.ID == "" || request.Resource.WorkspaceID != fixture.workspace ||
			len(request.Resource.Extra) != 0 {
			t.Fatalf("exact inbox candidate question = %#v", request)
		}
	}
}

func TestDirectNoticeExactInboxFiltersDenyAndStopsOnUnknown(t *testing.T) {
	t.Parallel()

	t.Run("deny is hidden", func(t *testing.T) {
		fixture := newDirectNoticeExactReadFixture(t)
		second := addDirectNoticeInboxMessage(t, fixture, "visible after deny")
		source := &directNoticeInboxEvidenceSource{
			base: fixture.source.evidence,
			outcomes: map[model.ID]auth.EvidenceOutcome{
				fixture.published.DeliveryID: auth.EvidenceDeny,
			},
		}
		fixture.m.useCommunicationRequestAuthoritySources(fixture.authr, source)
		observer := &directNoticeMutateObserverData{inner: fixture.m.data}
		fixture.m.data = observer
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		page, err := fixture.m.listDirectNoticeInboxWithAuthority(
			ctx, fixture.scope, fixture.readerRef, DirectNoticeInboxQuery{Limit: 1},
		)
		if err != nil || len(page.Items) != 1 || page.HasMore ||
			page.Items[0].Delivery.ID != second.DeliveryID ||
			page.Items[0].Message.Content.Blocks[0].Text != "visible after deny" {
			t.Fatalf("inbox after exact deny = %+v, %v", page, err)
		}
		if source.calls != 2 || observer.mutates.Load() != 1 ||
			fixture.legacy.calls.Load() != 0 {
			t.Fatalf(
				"deny calls/mutates/legacy = %d/%d/%d",
				source.calls, observer.mutates.Load(), fixture.legacy.calls.Load(),
			)
		}
	})

	t.Run("unknown aborts", func(t *testing.T) {
		fixture := newDirectNoticeExactReadFixture(t)
		source := &directNoticeInboxEvidenceSource{
			base: fixture.source.evidence,
			outcomes: map[model.ID]auth.EvidenceOutcome{
				fixture.published.DeliveryID: auth.EvidenceUnknown,
			},
		}
		fixture.m.useCommunicationRequestAuthoritySources(fixture.authr, source)
		observer := &directNoticeMutateObserverData{inner: fixture.m.data}
		fixture.m.data = observer
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		page, err := fixture.m.listDirectNoticeInboxWithAuthority(
			ctx, fixture.scope, fixture.readerRef, DirectNoticeInboxQuery{Limit: 1},
		)
		if !errors.Is(err, ErrCommunicationEvidenceUnknown) ||
			!reflect.DeepEqual(page, DirectNoticeInboxPage{}) || source.calls != 1 ||
			observer.mutates.Load() != 0 || fixture.resolver.calls.Load() != 0 ||
			fixture.closure.calls.Load() != 0 || fixture.legacy.calls.Load() != 0 {
			t.Fatalf(
				"unknown inbox = %+v, %v; calls/mutates/resolver/closure/legacy=%d/%d/%d/%d/%d",
				page, err, source.calls, observer.mutates.Load(), fixture.resolver.calls.Load(),
				fixture.closure.calls.Load(), fixture.legacy.calls.Load(),
			)
		}
	})
}

func TestDirectNoticeExactInboxPublicBoundaryBindsBeforeReadinessAndStaysOff(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeExactReadFixture(t)
	trace := []string{}
	resolver := &communicationAuthorityResolverRecorder{resolved: fixture.reader, trace: &trace}
	source := &directNoticeInboxEvidenceSource{
		base: fixture.source.evidence, outcomes: make(map[model.ID]auth.EvidenceOutcome),
		trace: &trace,
	}
	fixture.m.useCommunicationRequestAuthoritySources(resolver, source)
	readiness := &communicationReadinessStub{
		storeReady: true, sealerReady: true, trace: &trace,
	}
	fixture.m.UseCommunicationStoreReadinessWitness(readiness)
	fixture.m.UseCommunicationContentSealer(readiness)
	observer := &directNoticeMutateObserverData{inner: fixture.m.data}
	fixture.m.data = observer
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	_, err := fixture.m.ListDirectNoticeInbox(
		ctx, fixture.scope, fixture.readerRef, DirectNoticeInboxQuery{Limit: 1},
	)
	if !errors.Is(err, ErrCommunicationEvidenceUnknown) {
		t.Fatalf("public exact inbox = %v", err)
	}
	if want := []string{"resolve", "store", "sealer"}; !reflect.DeepEqual(trace, want) {
		t.Fatalf("public exact inbox trace = %v, want %v", trace, want)
	}
	if source.calls != 0 || observer.views.Load() != 0 || observer.mutates.Load() != 0 ||
		fixture.resolver.calls.Load() != 0 || fixture.closure.calls.Load() != 0 ||
		fixture.legacy.calls.Load() != 0 || readiness.cryptoCalls != 0 {
		t.Fatalf(
			"OFF inbox source/view/mutate/resolver/closure/legacy/open = %d/%d/%d/%d/%d/%d/%d",
			source.calls, observer.views.Load(), observer.mutates.Load(),
			fixture.resolver.calls.Load(), fixture.closure.calls.Load(),
			fixture.legacy.calls.Load(), readiness.cryptoCalls,
		)
	}
}

func TestDirectNoticeExactInboxRequiresDeadlineBeforeObservers(t *testing.T) {
	t.Parallel()

	fixture := newDirectNoticeExactReadFixture(t)
	resolver := &communicationAuthorityResolverRecorder{resolved: fixture.reader}
	source := &directNoticeInboxEvidenceSource{
		base: fixture.source.evidence, outcomes: make(map[model.ID]auth.EvidenceOutcome),
	}
	fixture.m.useCommunicationRequestAuthoritySources(resolver, source)
	observer := &directNoticeMutateObserverData{inner: fixture.m.data}
	fixture.m.data = observer
	_, err := fixture.m.listDirectNoticeInboxWithAuthority(
		context.Background(), fixture.scope, fixture.readerRef,
		DirectNoticeInboxQuery{Limit: 1},
	)
	if !errors.Is(err, ErrCommunicationEvidenceUnknown) || resolver.calls != 0 ||
		source.calls != 0 || observer.views.Load() != 0 || observer.mutates.Load() != 0 ||
		fixture.resolver.calls.Load() != 0 || fixture.closure.calls.Load() != 0 ||
		fixture.legacy.calls.Load() != 0 {
		t.Fatalf(
			"deadline inbox = %v; resolve/source/view/mutate/local/closure/legacy=%d/%d/%d/%d/%d/%d/%d",
			err, resolver.calls, source.calls, observer.views.Load(), observer.mutates.Load(),
			fixture.resolver.calls.Load(), fixture.closure.calls.Load(), fixture.legacy.calls.Load(),
		)
	}
}
