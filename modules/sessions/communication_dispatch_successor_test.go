// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"crypto/sha256"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
)

type deliveryDispatchSuccessorTestAuthorizer struct {
	evidence AuthorityEvidence
	calls    int
}

func (a *deliveryDispatchSuccessorTestAuthorizer) AuthorizeDeliveryDispatchSuccessor(
	_ context.Context,
	_ DirectoryScopeRef,
	_ model.ID,
	_ DispatchRouteIdentity,
) (AuthorityEvidence, error) {
	a.calls++
	return a.evidence, nil
}

type deliveryDispatchSuccessorTestAttestor struct {
	fixture *deliveryDispatchServiceFixture
	calls   int
}

func (a *deliveryDispatchSuccessorTestAttestor) AttestDispatchRoute(
	_ context.Context,
	_ DirectoryScopeRef,
	dispatchID model.ID,
) (DispatchRouteAttestation, error) {
	a.calls++
	for _, dispatch := range deliveryDispatchSuccessorRowsForTest(nil, *a.fixture) {
		if dispatch.ID == dispatchID {
			return DispatchRouteAttestation{
				DispatchID: dispatch.ID, Route: dispatchRouteIdentity(dispatch),
				ObservedAt: dispatch.UpdatedAt,
				Evidence: AuthorityEvidence{
					Verdict: VerdictClean, Code: "route_attested",
					EvidenceRef: "route:" + dispatch.ID.String(),
				},
			}, nil
		}
	}
	return DispatchRouteAttestation{}, store.ErrNotFound
}

func deliveryDispatchSuccessorRowsForTest(
	t *testing.T,
	fixture deliveryDispatchServiceFixture,
) []DeliveryDispatch {
	if t != nil {
		t.Helper()
	}
	rows := communicationRowsForTestFixture(fixture.directNoticeFixture, deliveryDispatchKind)
	dispatches := make([]DeliveryDispatch, 0, len(rows))
	for _, row := range rows {
		dispatch, err := deliveryDispatchFromRecord(row)
		if err != nil {
			if t != nil {
				t.Fatalf("decode dispatch successor row: %v", err)
			}
			return nil
		}
		dispatches = append(dispatches, dispatch)
	}
	sort.Slice(dispatches, func(i, j int) bool {
		return dispatches[i].DispatchGeneration < dispatches[j].DispatchGeneration
	})
	return dispatches
}

func communicationRowsForTestFixture(
	fixture directNoticeFixture,
	kind model.Kind,
) []model.Record {
	var rows []model.Record
	err := fixture.m.viewCommunication(context.Background(), fixture.scope, func(sc store.Scope) error {
		repo, err := sc.Ext(kind)
		if err != nil {
			return err
		}
		rows, _, err = repo.List(context.Background(), model.Query{Limit: 256})
		return err
	})
	if err != nil {
		return nil
	}
	return rows
}

func newDeliveryDispatchSuccessorFixture(
	t *testing.T,
) (deliveryDispatchServiceFixture, *deliveryDispatchSuccessorService) {
	t.Helper()
	fixture := newDeliveryDispatchServiceFixture(t)
	fixture.service.config.ResolutionTTL = time.Nanosecond
	refused, err := sdk.NewDeliveryAttemptResult(
		sdk.DeliveryAttemptRefusedBeforeBoundary,
		sdk.DeliveryBoundaryNotCrossed,
		nil,
	)
	if err != nil {
		t.Fatalf("refused attempt result: %v", err)
	}
	fixture.notifier.notifyResult = refused
	result, err := fixture.service.PumpOne(
		context.Background(), fixture.scope,
		deliveryDispatchPumpClaim{NodeID: "node-successor", Epoch: 31},
	)
	if err != nil || result.State != DispatchFailed {
		t.Fatalf("prepare failed dispatch = %+v, %v", result, err)
	}
	authorizer := &deliveryDispatchSuccessorTestAuthorizer{evidence: AuthorityEvidence{
		Verdict: VerdictClean, Code: "successor_authorized", EvidenceRef: "successor:test",
	}}
	attestor := &deliveryDispatchSuccessorTestAttestor{fixture: &fixture}
	service, err := newDeliveryDispatchSuccessorService(
		fixture.m, authorizer, attestor, model.NewID,
	)
	if err != nil {
		t.Fatalf("new successor service: %v", err)
	}
	return fixture, service
}

func TestDeliveryDispatchSuccessorCommitsAtomicLineageAndReplays(t *testing.T) {
	t.Parallel()

	fixture, service := newDeliveryDispatchSuccessorFixture(t)
	failed := deliveryDispatchSuccessorRowsForTest(t, fixture)[0]
	command := deliveryDispatchSuccessorCommand{
		PredecessorID: failed.ID, ExpectedVersion: failed.Version,
		SuccessorRoute: dispatchRouteIdentity(failed), IdempotencyKey: model.NewID().String(),
	}

	result, err := service.CreateSuccessor(context.Background(), fixture.scope, command)
	if err != nil {
		t.Fatalf("create dispatch successor: %v", err)
	}
	rows := deliveryDispatchSuccessorRowsForTest(t, fixture)
	if len(rows) != 2 || rows[0].State != DispatchSuperseded ||
		rows[1].State != DispatchPending || rows[1].PredecessorID != rows[0].ID ||
		rows[1].RootDispatchID != rows[0].RootDispatchID ||
		rows[1].DispatchGeneration != 2 || rows[1].RerouteRung != 0 ||
		result.PredecessorID != rows[0].ID || result.SuccessorID != rows[1].ID ||
		result.SuccessorGeneration != 2 || result.RerouteRung != 0 || result.AuditSeq < 1 {
		t.Fatalf("dispatch successor result=%+v rows=%+v", result, rows)
	}
	if got := len(communicationRowsForTestFixture(
		fixture.directNoticeFixture, communicationCommandKind,
	)); got != 2 { // publish receipt + successor receipt
		t.Fatalf("command receipts=%d, want publish+successor", got)
	}

	replay, err := service.CreateSuccessor(context.Background(), fixture.scope, command)
	if err != nil || !replay.Replayed || replay.CommandID != result.CommandID ||
		replay.SuccessorID != result.SuccessorID || replay.AuditSeq != result.AuditSeq ||
		len(deliveryDispatchSuccessorRowsForTest(t, fixture)) != 2 {
		t.Fatalf("dispatch successor replay=%+v, %v", replay, err)
	}

	changed := command
	changed.IdempotencyKey = model.NewID().String()
	if _, err = service.CreateSuccessor(context.Background(), fixture.scope, changed); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("second key against superseded dispatch = %v", err)
	}
}

func TestDeliveryDispatchSuccessorRerouteAdvancesGenerationAndRung(t *testing.T) {
	t.Parallel()

	fixture, service := newDeliveryDispatchSuccessorFixture(t)
	failed := deliveryDispatchSuccessorRowsForTest(t, fixture)[0]
	first, err := service.CreateSuccessor(context.Background(), fixture.scope,
		deliveryDispatchSuccessorCommand{
			PredecessorID: failed.ID, ExpectedVersion: failed.Version,
			SuccessorRoute: dispatchRouteIdentity(failed), IdempotencyKey: model.NewID().String(),
		})
	if err != nil {
		t.Fatalf("create retry generation: %v", err)
	}
	result, err := fixture.service.PumpOne(
		context.Background(), fixture.scope,
		deliveryDispatchPumpClaim{NodeID: "node-reroute", Epoch: 32},
	)
	if err != nil || result.State != DispatchFailed || result.DispatchID != first.SuccessorID {
		t.Fatalf("fail retry generation = %+v, %v", result, err)
	}
	secondEndpoint := fixture.endpoint
	secondEndpoint.ID = model.NewID()
	secondEndpoint.Version = 1
	secondEndpoint.Generation = 1
	secondEndpoint.EndpointRef = "test-reroute-endpoint"
	fingerprint := sha256.Sum256([]byte("successor-reroute-endpoint"))
	secondEndpoint.TransportFingerprint = fmtHex(fingerprint[:])
	record, err := communicationEndpointToRecord(secondEndpoint)
	if err != nil {
		t.Fatalf("encode reroute endpoint: %v", err)
	}
	if _, err = communicationCreateWithID(
		context.Background(), fixture.m, fixture.tenant,
		communicationEndpointKind, secondEndpoint.ID, record,
	); err != nil {
		t.Fatalf("create reroute endpoint: %v", err)
	}
	failedRetry := deliveryDispatchSuccessorRowsForTest(t, fixture)[1]
	route := dispatchRouteIdentity(failedRetry)
	route.EndpointID = secondEndpoint.ID
	route.EndpointGeneration = secondEndpoint.Generation
	route.PolicyGeneration++
	rerouted, err := service.CreateSuccessor(context.Background(), fixture.scope,
		deliveryDispatchSuccessorCommand{
			PredecessorID: failedRetry.ID, ExpectedVersion: failedRetry.Version,
			SuccessorRoute: route, IdempotencyKey: model.NewID().String(),
		})
	if err != nil {
		t.Fatalf("create rerouted generation: %v", err)
	}
	rows := deliveryDispatchSuccessorRowsForTest(t, fixture)
	if len(rows) != 3 || rows[1].State != DispatchSuperseded ||
		rows[2].State != DispatchPending || rows[2].DispatchGeneration != 3 ||
		rows[2].RerouteRung != 1 || rows[2].EndpointID != secondEndpoint.ID ||
		rerouted.SuccessorGeneration != 3 || rerouted.RerouteRung != 1 {
		t.Fatalf("rerouted dispatch result=%+v rows=%+v", rerouted, rows)
	}
}

func TestDeliveryDispatchSuccessorRollsBackBothRowsOnCreateFailure(t *testing.T) {
	t.Parallel()

	fixture, service := newDeliveryDispatchSuccessorFixture(t)
	failed := deliveryDispatchSuccessorRowsForTest(t, fixture)[0]
	failure := errors.New("successor create failed")
	fixture.m.data = &directNoticeExactAckWriteFailureData{
		inner: fixture.m.data, kind: deliveryDispatchKind,
		operation: "create_with_id", failure: failure,
	}
	_, err := service.CreateSuccessor(context.Background(), fixture.scope,
		deliveryDispatchSuccessorCommand{
			PredecessorID: failed.ID, ExpectedVersion: failed.Version,
			SuccessorRoute: dispatchRouteIdentity(failed), IdempotencyKey: model.NewID().String(),
		})
	if !errors.Is(err, ErrCommunicationEvidenceUnknown) || errors.Is(err, failure) {
		t.Fatalf("successor create failure = %v", err)
	}
	rows := deliveryDispatchSuccessorRowsForTest(t, fixture)
	if len(rows) != 1 || rows[0].State != DispatchFailed || rows[0].Version != failed.Version {
		t.Fatalf("failed successor left partial lineage: %+v", rows)
	}
}

func TestDeliveryDispatchFailedDeadlineSettlesWithoutSuccessor(t *testing.T) {
	t.Parallel()

	fixture, service := newDeliveryDispatchSuccessorFixture(t)
	failed := deliveryDispatchSuccessorRowsForTest(t, fixture)[0]
	if failed.ResolutionDeadlineAt == nil {
		t.Fatal("failed dispatch has no resolution deadline")
	}
	time.Sleep(time.Millisecond)
	result, err := service.DeadLetterExpired(
		context.Background(), fixture.scope, failed.ID,
	)
	if err != nil || !result.Changed || result.State != DispatchDeadLetter ||
		result.DispatchID != failed.ID || result.AttemptID == "" || result.AuditSeq < 1 {
		t.Fatalf("dead-letter expired dispatch = %+v, %v", result, err)
	}
	rows := deliveryDispatchSuccessorRowsForTest(t, fixture)
	if len(rows) != 1 || rows[0].State != DispatchDeadLetter ||
		rows[0].ResolutionDeadlineAt != nil || rows[0].ResolutionCode != "resolution_deadline_elapsed" {
		t.Fatalf("dead-letter rows = %+v", rows)
	}
	replay, err := service.DeadLetterExpired(
		context.Background(), fixture.scope, failed.ID,
	)
	if err != nil || replay.Changed || replay.State != DispatchDeadLetter ||
		replay.Version != rows[0].Version {
		t.Fatalf("dead-letter replay = %+v, %v", replay, err)
	}
}

func fmtHex(value []byte) string {
	const digits = "0123456789abcdef"
	text := make([]byte, len(value)*2)
	for index, one := range value {
		text[index*2] = digits[one>>4]
		text[index*2+1] = digits[one&0x0f]
	}
	return string(text)
}
