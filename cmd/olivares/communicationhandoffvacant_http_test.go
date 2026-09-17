// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
)

// vacantHandoffWork is a WorkItem created and readied over the public work API,
// with the HEADER ETag its last write published. The version is never rebuilt as
// `v${version}` from a body: the two aggregates this journey touches publish
// their coordinates differently and confusing them is the defect the existing
// suite already guards.
type vacantHandoffWork struct {
	id   model.ID
	etag string
}

// createVacantHandoffWork creates a WorkItem with the requested owner and marks
// it ready. It never acquires a lease, so the item keeps the vacant generation
// created with it. Nothing here writes sessions.work_item, sessions.work_lease
// or an owner column outside the public routes.
func createVacantHandoffWork(
	t *testing.T,
	estate incomingHandoffHTTPEstate,
	actor communicationHTTPTestUser,
	ownerKind string,
	ownerRef string,
	title string,
) vacantHandoffWork {
	t.Helper()
	created := communicationHTTPTestRequest(t, estate.eng, http.MethodPost,
		"/v1/m/sessions/work-items?mode=apply", actor.token, estate.tenant, map[string]any{
			"workspace_id": estate.workspace.String(), "work_kind": "implementation",
			"title": title, "brief_md": "Created over the real Work API for the OT-V journey.",
			"context_refs": []any{}, "priority": "p1",
			"owner_kind": ownerKind, "owner_ref": ownerRef,
			"provenance_kind": "human", "provenance_ref": "test:k3-vacant-handoff",
			"acceptance": []map[string]any{{
				"criterion_key": "transfer", "ordinal": 0,
				"statement": "Ownership transfers with no execution in flight", "required": true,
			}},
		}, map[string]string{"Idempotency-Key": model.NewID().String()})
	if created.status != http.StatusOK {
		t.Fatalf("create %s-owned WorkItem = %d: %s", ownerKind, created.status, created.raw)
	}
	result := communicationHTTPTestDecode[sessions.CommandResult](t, created)
	if result.OwnerEpoch != 1 || result.LeaseFence != 0 {
		t.Fatalf("created %s-owned WorkItem = %+v, want owner_epoch 1 and no lease fence",
			ownerKind, result)
	}
	if got := created.header.Get("ETag"); got != `"v1"` {
		t.Fatalf("create published ETag %q, want \"v1\"", got)
	}
	ready := communicationHTTPTestRequest(t, estate.eng, http.MethodPost,
		"/v1/m/sessions/work-items/"+result.ResultID.String()+"/transitions?mode=apply",
		actor.token, estate.tenant, map[string]any{"command": "item.ready"},
		map[string]string{
			"If-Match":        created.header.Get("ETag"),
			"Idempotency-Key": model.NewID().String(),
		})
	if ready.status != http.StatusOK {
		t.Fatalf("ready %s-owned WorkItem = %d: %s", ownerKind, ready.status, ready.raw)
	}
	return vacantHandoffWork{id: result.ResultID, etag: ready.header.Get("ETag")}
}

// readVacantHandoffLease reads the public lease projection for one WorkItem.
func readVacantHandoffLease(
	t *testing.T,
	estate incomingHandoffHTTPEstate,
	actor communicationHTTPTestUser,
	itemID model.ID,
) sessions.WorkLease {
	t.Helper()
	response := communicationHTTPTestRequest(t, estate.eng, http.MethodGet,
		"/v1/m/sessions/work-items/"+itemID.String()+"/lease", actor.token, estate.tenant, nil, nil)
	if response.status != http.StatusOK {
		t.Fatalf("read WorkLease = %d: %s", response.status, response.raw)
	}
	return communicationHTTPTestDecode[sessions.WorkLease](t, response)
}

// readVacantHandoffItem reads the public WorkItem projection and returns it with
// the HEADER ETag, which is the coordinate an offer must present.
func readVacantHandoffItem(
	t *testing.T,
	estate incomingHandoffHTTPEstate,
	actor communicationHTTPTestUser,
	itemID model.ID,
) (sessions.WorkItem, string) {
	t.Helper()
	response := communicationHTTPTestRequest(t, estate.eng, http.MethodGet,
		"/v1/m/sessions/work-items/"+itemID.String(), actor.token, estate.tenant, nil, nil)
	if response.status != http.StatusOK {
		t.Fatalf("read WorkItem = %d: %s", response.status, response.raw)
	}
	return communicationHTTPTestDecode[sessions.WorkSnapshot](t, response).Item,
		response.header.Get("ETag")
}

// offerVacantHandoff makes the offer over the public route as `actor`, which the
// route requires to be the item's current owner.
func offerVacantHandoff(
	t *testing.T,
	estate incomingHandoffHTTPEstate,
	actorToken string,
	itemID model.ID,
	ifMatch string,
	recipient map[string]any,
	summary string,
	expectedOwnerEpoch int64,
) sessions.HandoffOfferResult {
	t.Helper()
	response := communicationHTTPTestRequest(t, estate.eng, http.MethodPost,
		"/v1/m/sessions/handoffs", actorToken, estate.tenant, map[string]any{
			"channel_id": estate.channelID, "work_item_id": itemID, "recipient": recipient,
			"handoff": map[string]any{
				"summary": summary, "next_action": "Continue " + summary,
				"risk": "None recorded for " + summary,
			},
			"ack_deadline":         time.Now().UTC().Add(30 * time.Minute),
			"expected_owner_epoch": expectedOwnerEpoch,
		}, map[string]string{
			"If-Match": ifMatch, "Idempotency-Key": model.NewID().String(),
		})
	if response.status != http.StatusCreated {
		t.Fatalf("offer %q = %d: %s", summary, response.status, response.raw)
	}
	return communicationHTTPTestDecode[sessions.HandoffOfferResult](t, response)
}

// respondToVacantHandoff submits one accept/reject over the public response
// route with the ETag the recipient's own personal read published.
func respondToVacantHandoff(
	t *testing.T,
	estate incomingHandoffHTTPEstate,
	token string,
	handoffID model.ID,
	ifMatch string,
	body map[string]any,
	idempotencyKey string,
) communicationHTTPTestResponse {
	t.Helper()
	return communicationHTTPTestRequest(t, estate.eng, http.MethodPost,
		"/v1/m/sessions/handoffs/"+handoffID.String()+"/responses", token, estate.tenant, body,
		map[string]string{"If-Match": ifMatch, "Idempotency-Key": idempotencyKey})
}

// assertResultingLeaseFenceAbsent proves the wire fact, not the decoded zero:
// `resulting_lease_fence` is omitempty in every published client, so its ABSENCE
// is what says "no execution generation was ended". A decoded 0 cannot tell an
// absent key from a present one.
func assertResultingLeaseFenceAbsent(t *testing.T, response communicationHTTPTestResponse, label string) {
	t.Helper()
	var body map[string]json.RawMessage
	if err := json.Unmarshal(response.raw, &body); err != nil {
		t.Fatalf("%s body is not a JSON object: %v (%s)", label, err, response.raw)
	}
	if raw, present := body["resulting_lease_fence"]; present {
		t.Fatalf("%s published resulting_lease_fence %s; the vacant generation ended nothing",
			label, raw)
	}
}

func assertResultingLeaseFenceAtLeast(
	t *testing.T,
	response communicationHTTPTestResponse,
	want int64,
	label string,
) {
	t.Helper()
	var body map[string]json.RawMessage
	if err := json.Unmarshal(response.raw, &body); err != nil {
		t.Fatalf("%s body is not a JSON object: %v (%s)", label, err, response.raw)
	}
	raw, present := body["resulting_lease_fence"]
	if !present {
		t.Fatalf("%s published no resulting_lease_fence; an active generation must be fenced", label)
	}
	var got int64
	if err := json.Unmarshal(raw, &got); err != nil || got < want {
		t.Fatalf("%s resulting_lease_fence = %s (err %v), want >= %d", label, raw, err, want)
	}
}

func TestCommunicationHandoffVacantGenerationTransferHTTP(t *testing.T) {
	exerciseCommunicationHandoffVacantGenerationTransferHTTP(
		t, communicationHTTPTestSQLiteStore(t))
}

// TestCommunicationHandoffVacantGenerationTransferHTTPPostgres is the same
// authenticated product journey on an owned IsolatedPostgresSplitOwner estate.
func TestCommunicationHandoffVacantGenerationTransferHTTPPostgres(t *testing.T) {
	exerciseCommunicationHandoffVacantGenerationTransferHTTP(
		t, communicationHTTPTestPostgresStore(t))
}

func exerciseCommunicationHandoffVacantGenerationTransferHTTP(
	t *testing.T,
	backing communicationHTTPTestStore,
) {
	t.Helper()
	estate := bootIncomingHandoffHTTPEstate(t, backing)
	// Armed FIRST so its minimum-TTL generation can lapse while the rest of the
	// journey runs; the ended-generation controls collect it at the end.
	expiring := armVacantHandoffExpiringLease(t, estate)

	exerciseVacantGenerationPublicSequence(t, estate)
	exerciseVacantGenerationActiveControl(t, estate)
	exerciseVacantGenerationRecipientBreadth(t, estate)
	exerciseVacantGenerationEndedGenerations(t, estate, expiring)
	exerciseVacantGenerationConcurrency(t, estate)
}

// exerciseVacantGenerationPublicSequence is the acceptance sequence: A creates a
// user-owned WorkItem, readies it, never leases it (and cannot), offers it to B,
// and B accepts. Ownership moves; the lease and the workspace clock are untouched.
func exerciseVacantGenerationPublicSequence(t *testing.T, estate incomingHandoffHTTPEstate) {
	t.Helper()
	work := createVacantHandoffWork(
		t, estate, estate.owner, "user", estate.owner.id.String(), "OT-V user to user")

	lease := readVacantHandoffLease(t, estate, estate.owner, work.id)
	if lease.State != "vacant" || lease.Fence != 0 || lease.Live ||
		lease.HolderSID != "" || lease.RenewalCount != 0 {
		t.Fatalf("lease before transfer = %+v, want the vacant generation at fence 0", lease)
	}

	item, etag := readVacantHandoffItem(t, estate, estate.owner, work.id)
	if item.OwnerKind != "user" || item.OwnerRef != estate.owner.id.String() ||
		item.OwnerEpoch != 1 || item.Status != "ready" ||
		item.Leased || item.Claimable || item.Orphaned {
		t.Fatalf("readied user-owned WorkItem = %+v", item)
	}
	if etag != work.etag {
		t.Fatalf("WorkItem GET ETag %q differs from the ready ETag %q", etag, work.etag)
	}

	// NEGATIVE CONTROL: a human is not a holder, and this lot does not make one.
	assertLeaseAcquireOwnerIneligible(t, estate, estate.owner, work.id, etag,
		estate.targetSession.sid, "user-owned work before transfer")

	offered := offerVacantHandoff(t, estate, estate.owner.token, work.id, etag,
		map[string]any{"kind": "user", "ref": estate.recipient.id}, "OT-V user to user", 1)
	if offered.State != sessions.HandoffOffered {
		t.Fatalf("offer = %+v", offered)
	}
	if beforeAccept, _ := readVacantHandoffItem(t, estate, estate.owner, work.id); beforeAccept.OwnerEpoch != 1 ||
		beforeAccept.OwnerRef != estate.owner.id.String() {
		t.Fatalf("an offer transferred ownership: %+v", beforeAccept)
	}

	// The recipient discovers the offer on its own listing and opens it.
	page := estate.page(t, estate.recipient.token, "offered", 0, "")
	found := false
	for _, card := range page.Items {
		if card.Handoff.ID == offered.HandoffID {
			found = true
		}
	}
	if !found {
		t.Fatalf("the recipient did not discover the vacant-generation offer: %+v", page)
	}
	detail := estate.detail(t, estate.recipient.token, offered.DeliveryID)
	if detail.status != http.StatusOK {
		t.Fatalf("recipient detail = %d: %s", detail.status, detail.raw)
	}
	assertIncomingHandoffPrivateHeaders(t, detail, "vacant-generation detail")
	read := communicationHTTPTestDecode[sessions.IncomingHandoffReadResult](t, detail)
	if read.OfferContext != sessions.IncomingHandoffContextCurrent ||
		read.Handoff.ETag != offered.ETag {
		t.Fatalf("recipient read = %+v", read)
	}

	acceptKey := model.NewID().String()
	accepted := respondToVacantHandoff(t, estate, estate.recipient.token, offered.HandoffID,
		read.Handoff.ETag, map[string]any{"transition": "accept"}, acceptKey)
	if accepted.status != http.StatusOK {
		t.Fatalf("accept of a vacant generation = %d: %s", accepted.status, accepted.raw)
	}
	result := communicationHTTPTestDecode[sessions.HandoffResponseResult](t, accepted)
	if result.State != sessions.HandoffAccepted || result.OwnerEpoch != 2 ||
		result.Replayed || result.AckID == "" {
		t.Fatalf("vacant-generation accept = %+v", result)
	}
	assertResultingLeaseFenceAbsent(t, accepted, "vacant-generation accept")

	// Replay: same key, same body, same ETag. One command, one set of effects.
	before := communicationHTTPTestEffects(t, estate.eng, estate.tenant)
	replay := respondToVacantHandoff(t, estate, estate.recipient.token, offered.HandoffID,
		read.Handoff.ETag, map[string]any{"transition": "accept"}, acceptKey)
	if replay.status != http.StatusOK {
		t.Fatalf("vacant-generation accept replay = %d: %s", replay.status, replay.raw)
	}
	replayed := communicationHTTPTestDecode[sessions.HandoffResponseResult](t, replay)
	if !replayed.Replayed || replayed.CommandID != result.CommandID ||
		replayed.Version != result.Version || replayed.OwnerEpoch != result.OwnerEpoch {
		t.Fatalf("vacant-generation accept replay = %+v", replayed)
	}
	assertResultingLeaseFenceAbsent(t, replay, "vacant-generation accept replay")
	assertCommunicationHTTPTestNoEffects(t, estate.eng, estate.tenant, before,
		"replayed vacant-generation accept")

	// Ownership moved and is fenced by the fence that governs transfers.
	transferred, transferredETag := readVacantHandoffItem(t, estate, estate.owner, work.id)
	if transferred.OwnerKind != "user" || transferred.OwnerRef != estate.recipient.id.String() ||
		transferred.OwnerEpoch != 2 || transferred.Status != "ready" ||
		transferred.Leased || transferred.Claimable || transferred.Orphaned {
		t.Fatalf("transferred WorkItem = %+v", transferred)
	}

	// THE POINT: the execution lease is exactly what it was. No phantom
	// generation, no fence minted for a holder that never existed.
	after := readVacantHandoffLease(t, estate, estate.owner, work.id)
	if after.State != "vacant" || after.Fence != 0 || after.Live ||
		after.HolderSID != "" || after.EndReason != "" || after.RenewalCount != 0 ||
		after.Version != lease.Version {
		t.Fatalf("lease after transfer = %+v, want the untouched vacant generation %+v", after, lease)
	}

	// NEGATIVE CONTROL: the new owner is a human too, and is still not a holder.
	assertLeaseAcquireOwnerIneligible(t, estate, estate.recipient, work.id, transferredETag,
		estate.targetSession.sid, "user-owned work after transfer")
}

// assertLeaseAcquireOwnerIneligible proves that this lot did not touch K2 lease
// admission: a user-owned WorkItem is not claimable by anyone, before or after
// its ownership moves.
func assertLeaseAcquireOwnerIneligible(
	t *testing.T,
	estate incomingHandoffHTTPEstate,
	actor communicationHTTPTestUser,
	itemID model.ID,
	ifMatch string,
	holderSID string,
	label string,
) {
	t.Helper()
	response := communicationHTTPTestRequest(t, estate.eng, http.MethodPost,
		"/v1/m/sessions/work-items/"+itemID.String()+"/lease/acquire?mode=apply",
		actor.token, estate.tenant,
		map[string]any{"holder_sid": holderSID, "ttl_seconds": 300},
		map[string]string{"If-Match": ifMatch, "Idempotency-Key": model.NewID().String()})
	if response.status != http.StatusUnprocessableEntity {
		t.Fatalf("lease.acquire on %s = %d: %s", label, response.status, response.raw)
	}
	if !strings.Contains(string(response.raw), "owner_ineligible") {
		t.Fatalf("lease.acquire on %s refused with %s, want owner_ineligible", label, response.raw)
	}
}

// exerciseVacantGenerationActiveControl is the other half of the split, in the
// same run: an accept that found an execution generation still revokes it,
// advances its fence and advances the workspace clock guard.
func exerciseVacantGenerationActiveControl(t *testing.T, estate incomingHandoffHTTPEstate) {
	t.Helper()
	source := createCommunicationHTTPTestSession(
		t, estate.eng, estate.tenant, estate.workspace, "otv-active-"+model.NewID().String())
	estate.grantChannel(t, map[string]any{"kind": "session", "ref": source.sid}, "active offering session")
	work, leased := createCommunicationHTTPSessionOwnedWork(
		t, estate.eng, estate.owner, estate.tenant, source, "OT-V active control")
	if leased.LeaseFence < 1 {
		t.Fatalf("active control lease = %+v", leased)
	}
	activeLease := readVacantHandoffLease(t, estate, estate.owner, work.ResultID)
	if activeLease.State != "active" || activeLease.Fence != leased.LeaseFence || !activeLease.Live {
		t.Fatalf("active control lease projection = %+v", activeLease)
	}
	guardBefore := readVacantHandoffClockGuardEpoch(t, estate)

	offered := offerVacantHandoff(t, estate, source.communication.Token, work.ResultID,
		fmt.Sprintf(`"v%d"`, leased.Version),
		map[string]any{"kind": "user", "ref": estate.recipient.id}, "OT-V active control", 1)

	read := communicationHTTPTestDecode[sessions.IncomingHandoffReadResult](
		t, estate.detail(t, estate.recipient.token, offered.DeliveryID))
	accepted := respondToVacantHandoff(t, estate, estate.recipient.token, offered.HandoffID,
		read.Handoff.ETag, map[string]any{"transition": "accept"}, model.NewID().String())
	if accepted.status != http.StatusOK {
		t.Fatalf("accept of an active generation = %d: %s", accepted.status, accepted.raw)
	}
	result := communicationHTTPTestDecode[sessions.HandoffResponseResult](t, accepted)
	if result.State != sessions.HandoffAccepted || result.OwnerEpoch != 2 ||
		result.ResultingLeaseFence != activeLease.Fence+1 {
		t.Fatalf("active-generation accept = %+v", result)
	}
	assertResultingLeaseFenceAtLeast(t, accepted, 1, "active-generation accept")

	ended := readVacantHandoffLease(t, estate, estate.owner, work.ResultID)
	if ended.State != "revoked" || ended.Fence != activeLease.Fence+1 || ended.Live ||
		ended.EndedAt == "" || ended.HolderSID != source.sid {
		t.Fatalf("lease after an active-generation accept = %+v", ended)
	}
	if guardAfter := readVacantHandoffClockGuardEpoch(t, estate); guardAfter != guardBefore+1 {
		t.Fatalf("lease clock guard epoch = %d, want %d", guardAfter, guardBefore+1)
	}
}

// readVacantHandoffClockGuardEpoch reads the workspace lease-clock guard epoch.
// There is no public projection of it, and this is a read: no row is written and
// no work/lease/owner state is fabricated.
func readVacantHandoffClockGuardEpoch(t *testing.T, estate incomingHandoffHTTPEstate) int64 {
	t.Helper()
	var epoch int64
	if err := estate.eng.store.View(context.Background(), estate.tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("sessions.work_guard")
		if err != nil {
			return err
		}
		rows, _, err := repo.List(context.Background(), model.Query{
			Filters: []model.Filter{
				{Column: "workspace_id", Op: model.OpEq, Value: estate.workspace.String()},
				{Column: "guard_kind", Op: model.OpEq, Value: "lease_clock"},
			},
			Limit: 2,
		})
		if err != nil {
			return err
		}
		switch len(rows) {
		case 0:
			epoch = 0
		case 1:
			epoch = rows[0].Int("epoch")
		default:
			return fmt.Errorf("workspace has %d lease clock guards", len(rows))
		}
		return nil
	}); err != nil {
		t.Fatalf("read lease clock guard: %v", err)
	}
	return epoch
}

// exerciseVacantGenerationRecipientBreadth keeps every recipient kind in the
// vocabulary on BOTH lease generations. The predicate this lot introduced is
// keyed on the lease generation and never on a participant kind, and these four
// combinations are what says so over the public endpoint.
func exerciseVacantGenerationRecipientBreadth(t *testing.T, estate incomingHandoffHTTPEstate) {
	t.Helper()
	recipients := []struct {
		label     string
		recipient map[string]any
		token     func() string
	}{
		{
			label:     "agent",
			recipient: map[string]any{"kind": "agent", "ref": estate.agent.identityID},
			token:     func() string { return estate.agent.token },
		},
		{
			label:     "session",
			recipient: map[string]any{"kind": "session", "ref": estate.targetSession.sid},
			token:     func() string { return estate.targetSession.communication.Token },
		},
	}
	for _, subject := range recipients {
		// A. vacant generation, user-owned item.
		work := createVacantHandoffWork(t, estate, estate.owner, "user",
			estate.owner.id.String(), "OT-V vacant to "+subject.label)
		_, etag := readVacantHandoffItem(t, estate, estate.owner, work.id)
		offered := offerVacantHandoff(t, estate, estate.owner.token, work.id, etag,
			subject.recipient, "OT-V vacant to "+subject.label, 1)
		read := communicationHTTPTestDecode[sessions.IncomingHandoffReadResult](
			t, estate.detail(t, subject.token(), offered.DeliveryID))
		if read.OfferContext != sessions.IncomingHandoffContextCurrent {
			t.Fatalf("%s recipient offer context on a vacant generation = %q",
				subject.label, read.OfferContext)
		}
		accepted := respondToVacantHandoff(t, estate, subject.token(), offered.HandoffID,
			read.Handoff.ETag, map[string]any{"transition": "accept"}, model.NewID().String())
		if accepted.status != http.StatusOK {
			t.Fatalf("%s recipient accept of a vacant generation = %d: %s",
				subject.label, accepted.status, accepted.raw)
		}
		assertResultingLeaseFenceAbsent(t, accepted,
			subject.label+" recipient vacant-generation accept")
		item, _ := readVacantHandoffItem(t, estate, estate.owner, work.id)
		wantRef := fmt.Sprintf("%v", subject.recipient["ref"])
		if item.OwnerKind != subject.label || item.OwnerRef != wantRef || item.OwnerEpoch != 2 {
			t.Fatalf("%s recipient did not receive ownership: %+v", subject.label, item)
		}
		if lease := readVacantHandoffLease(t, estate, estate.owner, work.id); lease.State != "vacant" ||
			lease.Fence != 0 || lease.Live {
			t.Fatalf("%s recipient transfer wrote the lease: %+v", subject.label, lease)
		}
		// The new owner IS eligible to hold this one: it is an agent or a session,
		// and its first acquire mints fence 1 through the ordinary K2 path. That is
		// the state the vacant arm deliberately left available.
		if subject.label == "session" {
			assertFirstAcquireMintsFenceOne(t, estate, work.id)
		}

		// B. active generation, session-owned item.
		source := createCommunicationHTTPTestSession(t, estate.eng, estate.tenant,
			estate.workspace, "otv-breadth-"+model.NewID().String())
		estate.grantChannel(t,
			map[string]any{"kind": "session", "ref": source.sid}, "breadth offering session")
		activeWork, leased := createCommunicationHTTPSessionOwnedWork(
			t, estate.eng, estate.owner, estate.tenant, source, "OT-V active to "+subject.label)
		activeOffer := offerVacantHandoff(t, estate, source.communication.Token,
			activeWork.ResultID, fmt.Sprintf(`"v%d"`, leased.Version), subject.recipient,
			"OT-V active to "+subject.label, 1)
		activeRead := communicationHTTPTestDecode[sessions.IncomingHandoffReadResult](
			t, estate.detail(t, subject.token(), activeOffer.DeliveryID))
		activeAccepted := respondToVacantHandoff(t, estate, subject.token(),
			activeOffer.HandoffID, activeRead.Handoff.ETag,
			map[string]any{"transition": "accept"}, model.NewID().String())
		if activeAccepted.status != http.StatusOK {
			t.Fatalf("%s recipient accept of an active generation = %d: %s",
				subject.label, activeAccepted.status, activeAccepted.raw)
		}
		assertResultingLeaseFenceAtLeast(t, activeAccepted, leased.LeaseFence+1,
			subject.label+" recipient active-generation accept")
	}
}

// assertFirstAcquireMintsFenceOne proves the transferred vacant generation is
// still a normal one: the new session owner's first ordinary lease.acquire mints
// fence 1, exactly as it would have before the transfer.
func assertFirstAcquireMintsFenceOne(
	t *testing.T,
	estate incomingHandoffHTTPEstate,
	itemID model.ID,
) {
	t.Helper()
	_, etag := readVacantHandoffItem(t, estate, estate.owner, itemID)
	response := communicationHTTPTestRequest(t, estate.eng, http.MethodPost,
		"/v1/m/sessions/work-items/"+itemID.String()+"/lease/acquire?mode=apply",
		estate.targetSession.work.Token, estate.tenant, map[string]any{
			"holder_sid":     estate.targetSession.sid,
			"holder_run_ref": estate.targetSession.runRef, "ttl_seconds": 300,
		}, map[string]string{"If-Match": etag, "Idempotency-Key": model.NewID().String()})
	if response.status != http.StatusOK {
		t.Fatalf("first acquire after a vacant transfer = %d: %s", response.status, response.raw)
	}
	acquired := communicationHTTPTestDecode[sessions.CommandResult](t, response)
	if acquired.LeaseFence != 1 {
		t.Fatalf("first acquire after a vacant transfer minted fence %d, want 1", acquired.LeaseFence)
	}
}

// vacantHandoffExpiringLease is the generation armed at the start of the run so
// its minimum TTL can lapse while the rest of the journey runs.
type vacantHandoffExpiringLease struct {
	source   communicationHTTPTestSession
	work     sessions.CommandResult
	leased   sessions.CommandResult
	acquired time.Time
}

func armVacantHandoffExpiringLease(
	t *testing.T,
	estate incomingHandoffHTTPEstate,
) vacantHandoffExpiringLease {
	t.Helper()
	source := createCommunicationHTTPTestSession(
		t, estate.eng, estate.tenant, estate.workspace, "otv-expiring-"+model.NewID().String())
	estate.grantChannel(t,
		map[string]any{"kind": "session", "ref": source.sid}, "expiring offering session")
	work, _ := createCommunicationHTTPSessionOwnedWork(
		t, estate.eng, estate.owner, estate.tenant, source, "OT-V expiring generation")
	// createCommunicationHTTPSessionOwnedWork already leased it, at the default
	// five-minute TTL. Renew it down to the policy MINIMUM (30 s, and the floor is
	// refused below it rather than clamped) so the generation lapses inside this
	// run instead of outliving it.
	_, etag := readVacantHandoffItem(t, estate, estate.owner, work.ResultID)
	held := readVacantHandoffLease(t, estate, estate.owner, work.ResultID)
	renewed := communicationHTTPTestRequest(t, estate.eng, http.MethodPost,
		"/v1/m/sessions/work-items/"+work.ResultID.String()+"/lease/renew?mode=apply",
		source.work.Token, estate.tenant, map[string]any{
			"holder_sid": source.sid, "holder_run_ref": source.runRef,
			"fence": held.Fence, "ttl_seconds": 30,
		}, map[string]string{"If-Match": etag, "Idempotency-Key": model.NewID().String()})
	if renewed.status != http.StatusOK {
		t.Fatalf("renew to the minimum TTL = %d: %s", renewed.status, renewed.raw)
	}
	return vacantHandoffExpiringLease{
		source: source, work: work,
		leased:   communicationHTTPTestDecode[sessions.CommandResult](t, renewed),
		acquired: time.Now(),
	}
}

// exerciseVacantGenerationEndedGenerations is the deferral of proposal 7.1 made
// executable. A released, expired or revoked generation names a prior holder,
// carries an offered fence >= 1 and is NOT the vacant witness, so every accept
// against one keeps today's 409. This lot does not silently widen to them.
func exerciseVacantGenerationEndedGenerations(
	t *testing.T,
	estate incomingHandoffHTTPEstate,
	expiring vacantHandoffExpiringLease,
) {
	t.Helper()
	assertEndedGenerationRefusesAccept(t, estate, endVacantHandoffGeneration(
		t, estate, "released", nil))
	assertEndedGenerationRefusesAccept(t, estate, endVacantHandoffGeneration(
		t, estate, "revoked", nil))
	assertEndedGenerationRefusesAccept(t, estate, endVacantHandoffGeneration(
		t, estate, "expired", &expiring))
}

type endedGenerationOffer struct {
	itemID    model.ID
	handoffID model.ID
	delivery  model.ID
	etag      string
	state     string
}

// endVacantHandoffGeneration drives one lease into an ended lifecycle through
// the product's own paths — the public release and revoke routes, and the
// module's own lease reaper for expiry — then offers the item to the recipient.
func endVacantHandoffGeneration(
	t *testing.T,
	estate incomingHandoffHTTPEstate,
	want string,
	expiring *vacantHandoffExpiringLease,
) endedGenerationOffer {
	t.Helper()
	var source communicationHTTPTestSession
	var work sessions.CommandResult
	switch want {
	case "expired":
		source, work = expiring.source, expiring.work
		// The minimum TTL is a policy floor, so the generation cannot lapse sooner
		// than the policy allows. Wait out only what the run has not already spent.
		if remaining := 31*time.Second - time.Since(expiring.acquired); remaining > 0 {
			time.Sleep(remaining)
		}
		reaped, err := estate.eng.sessionsMod.ReapWorkLeases(
			context.Background(), estate.tenant, 10)
		if err != nil || reaped < 1 {
			t.Fatalf("reap the lapsed generation = %d, %v", reaped, err)
		}
	default:
		source = createCommunicationHTTPTestSession(t, estate.eng, estate.tenant,
			estate.workspace, "otv-"+want+"-"+model.NewID().String())
		estate.grantChannel(t,
			map[string]any{"kind": "session", "ref": source.sid}, want+" offering session")
		work, _ = createCommunicationHTTPSessionOwnedWork(
			t, estate.eng, estate.owner, estate.tenant, source, "OT-V "+want+" generation")
		_, etag := readVacantHandoffItem(t, estate, estate.owner, work.ResultID)
		held := readVacantHandoffLease(t, estate, estate.owner, work.ResultID)
		var response communicationHTTPTestResponse
		if want == "released" {
			response = communicationHTTPTestRequest(t, estate.eng, http.MethodPost,
				"/v1/m/sessions/work-items/"+work.ResultID.String()+"/lease/release?mode=apply",
				source.work.Token, estate.tenant, map[string]any{
					"holder_sid": source.sid, "holder_run_ref": source.runRef,
					"fence": held.Fence,
				},
				map[string]string{"If-Match": etag, "Idempotency-Key": model.NewID().String()})
		} else {
			response = communicationHTTPTestRequest(t, estate.eng, http.MethodPost,
				"/v1/m/sessions/work-items/"+work.ResultID.String()+"/lease/revoke?mode=apply",
				estate.owner.token, estate.tenant, map[string]any{
					"fence": held.Fence, "reason": "OT-V ended-generation control",
				},
				map[string]string{"If-Match": etag, "Idempotency-Key": model.NewID().String()})
		}
		if response.status != http.StatusOK {
			t.Fatalf("end the generation as %s = %d: %s", want, response.status, response.raw)
		}
	}
	lease := readVacantHandoffLease(t, estate, estate.owner, work.ResultID)
	if lease.State != want || lease.Fence < 1 || lease.Live || lease.HolderSID != source.sid {
		t.Fatalf("lease driven to %s = %+v", want, lease)
	}
	_, etag := readVacantHandoffItem(t, estate, estate.owner, work.ResultID)
	offered := offerVacantHandoff(t, estate, source.communication.Token, work.ResultID, etag,
		map[string]any{"kind": "user", "ref": estate.recipient.id}, "OT-V "+want+" generation", 1)
	read := communicationHTTPTestDecode[sessions.IncomingHandoffReadResult](
		t, estate.detail(t, estate.recipient.token, offered.DeliveryID))
	return endedGenerationOffer{
		itemID: work.ResultID, handoffID: offered.HandoffID, delivery: offered.DeliveryID,
		etag: read.Handoff.ETag, state: want,
	}
}

func assertEndedGenerationRefusesAccept(
	t *testing.T,
	estate incomingHandoffHTTPEstate,
	offer endedGenerationOffer,
) {
	t.Helper()
	before := communicationHTTPTestEffects(t, estate.eng, estate.tenant)
	refused := respondToVacantHandoff(t, estate, estate.recipient.token, offer.handoffID,
		offer.etag, map[string]any{"transition": "accept"}, model.NewID().String())
	if refused.status != http.StatusConflict {
		t.Fatalf("accept against a %s generation = %d: %s",
			offer.state, refused.status, refused.raw)
	}
	assertCommunicationHTTPTestNoEffects(t, estate.eng, estate.tenant, before,
		"refused accept against a "+offer.state+" generation")
	item, _ := readVacantHandoffItem(t, estate, estate.owner, offer.itemID)
	if item.OwnerEpoch != 1 {
		t.Fatalf("a refused %s-generation accept moved ownership: %+v", offer.state, item)
	}
}

// exerciseVacantGenerationConcurrency makes the causal cases of proposal 10
// executable rather than argued. Every request here runs to completion before
// the next one starts: these are SEQUENTIAL state/CAS controls, and naming them
// that is the point. Two requests actually in flight are a different control and
// live in TestCommunicationHandoffVacantGenerationConcurrentHTTPPostgres, which
// blocks them both on the store's existing tenant serializer.
//
// The inherited claim that the vacant arm "takes a SUBSET of the same locks in
// the same relative order" is NOT the reason these pass, and it is withdrawn:
// K3's accept locks the work item first (communication_handoff_apply.go, via
// lockHandoffWorkState) and only then coordinates the clock and the lease item,
// while K2's acquire (work_service.go) coordinates the clock and lease before
// its item observation. That is not the same relative order, so no general
// deadlock-freedom follows from it. What actually serializes these paths is the
// store's tenant write gate: sqlstore Mutate starts the lineage writer before
// the callback, and on PostgreSQL that takes a tenant-scoped advisory
// transaction lock, so two writers in the same tenant never interleave inside
// their transactions at all. This lot changed neither.
func exerciseVacantGenerationConcurrency(t *testing.T, estate incomingHandoffHTTPEstate) {
	t.Helper()

	// C1: a lease acquired between the offer and the accept. The offer sealed
	// fence 0; the item now carries fence 1, so the accept is stale. The owner is
	// a session here precisely so the acquire can succeed. Each case gets its own
	// session because one runtime run carries at most one live work binding.
	c1Session := newVacantHandoffOwningSession(t, estate, "otv-c1")
	acquireBetween := createVacantHandoffWork(t, estate, estate.owner, "session",
		c1Session.sid, "OT-V C1 acquire between offer and accept")
	_, c1ETag := readVacantHandoffItem(t, estate, estate.owner, acquireBetween.id)
	c1Offer := offerVacantHandoff(t, estate, c1Session.communication.Token,
		acquireBetween.id, c1ETag,
		map[string]any{"kind": "user", "ref": estate.recipient.id}, "OT-V C1", 1)
	c1Read := communicationHTTPTestDecode[sessions.IncomingHandoffReadResult](
		t, estate.detail(t, estate.recipient.token, c1Offer.DeliveryID))
	if c1Read.OfferContext != sessions.IncomingHandoffContextCurrent {
		t.Fatalf("C1 offer context before the acquire = %q", c1Read.OfferContext)
	}
	_, c1AfterOffer := readVacantHandoffItem(t, estate, estate.owner, acquireBetween.id)
	acquired := communicationHTTPTestRequest(t, estate.eng, http.MethodPost,
		"/v1/m/sessions/work-items/"+acquireBetween.id.String()+"/lease/acquire?mode=apply",
		c1Session.work.Token, estate.tenant, map[string]any{
			"holder_sid":     c1Session.sid,
			"holder_run_ref": c1Session.runRef, "ttl_seconds": 300,
		}, map[string]string{
			"If-Match": c1AfterOffer, "Idempotency-Key": model.NewID().String(),
		})
	if acquired.status != http.StatusOK {
		t.Fatalf("C1 acquire between offer and accept = %d: %s", acquired.status, acquired.raw)
	}
	c1Before := communicationHTTPTestEffects(t, estate.eng, estate.tenant)
	c1Refused := respondToVacantHandoff(t, estate, estate.recipient.token, c1Offer.HandoffID,
		c1Read.Handoff.ETag, map[string]any{"transition": "accept"}, model.NewID().String())
	if c1Refused.status != http.StatusConflict {
		t.Fatalf("C1 accept after a competing acquire = %d: %s", c1Refused.status, c1Refused.raw)
	}
	assertCommunicationHTTPTestNoEffects(t, estate.eng, estate.tenant, c1Before,
		"C1 accept after a competing acquire")

	// C2: the accept committed first, so the competing acquire meets a WorkItem
	// version the transfer advanced and is refused by its own If-Match. The
	// recipient is a SESSION on purpose: with a human recipient the ownership
	// check refuses first (422 owner_ineligible) and the version check is never
	// reached, so it would not measure the ordering this case is about.
	c2Session := newVacantHandoffOwningSession(t, estate, "otv-c2")
	c2Recipient := newVacantHandoffOwningSession(t, estate, "otv-c2-recipient")
	staleAcquire := createVacantHandoffWork(t, estate, estate.owner, "session",
		c2Session.sid, "OT-V C2 stale acquire")
	_, c2ETag := readVacantHandoffItem(t, estate, estate.owner, staleAcquire.id)
	c2Offer := offerVacantHandoff(t, estate, c2Session.communication.Token,
		staleAcquire.id, c2ETag,
		map[string]any{"kind": "session", "ref": c2Recipient.sid}, "OT-V C2", 1)
	_, c2Held := readVacantHandoffItem(t, estate, estate.owner, staleAcquire.id)
	c2Read := communicationHTTPTestDecode[sessions.IncomingHandoffReadResult](
		t, estate.detail(t, c2Recipient.communication.Token, c2Offer.DeliveryID))
	c2Accepted := respondToVacantHandoff(t, estate, c2Recipient.communication.Token,
		c2Offer.HandoffID, c2Read.Handoff.ETag,
		map[string]any{"transition": "accept"}, model.NewID().String())
	if c2Accepted.status != http.StatusOK {
		t.Fatalf("C2 accept = %d: %s", c2Accepted.status, c2Accepted.raw)
	}
	assertResultingLeaseFenceAbsent(t, c2Accepted, "C2 accept")
	c2Stale := communicationHTTPTestRequest(t, estate.eng, http.MethodPost,
		"/v1/m/sessions/work-items/"+staleAcquire.id.String()+"/lease/acquire?mode=apply",
		c2Recipient.work.Token, estate.tenant, map[string]any{
			"holder_sid":     c2Recipient.sid,
			"holder_run_ref": c2Recipient.runRef, "ttl_seconds": 300,
		}, map[string]string{
			"If-Match": c2Held, "Idempotency-Key": model.NewID().String(),
		})
	if c2Stale.status != http.StatusPreconditionFailed {
		t.Fatalf("C2 acquire with the pre-transfer WorkItem version = %d: %s",
			c2Stale.status, c2Stale.raw)
	}
	if lease := readVacantHandoffLease(t, estate, estate.owner, staleAcquire.id); lease.State != "vacant" ||
		lease.Fence != 0 {
		t.Fatalf("C2 refused acquire still wrote the lease: %+v", lease)
	}
	// Control against a refusal that rejects everything: with the version the
	// transfer published, the SAME acquire succeeds and mints fence 1.
	_, c2Current := readVacantHandoffItem(t, estate, estate.owner, staleAcquire.id)
	c2Fresh := communicationHTTPTestRequest(t, estate.eng, http.MethodPost,
		"/v1/m/sessions/work-items/"+staleAcquire.id.String()+"/lease/acquire?mode=apply",
		c2Recipient.work.Token, estate.tenant, map[string]any{
			"holder_sid":     c2Recipient.sid,
			"holder_run_ref": c2Recipient.runRef, "ttl_seconds": 300,
		}, map[string]string{
			"If-Match": c2Current, "Idempotency-Key": model.NewID().String(),
		})
	if c2Fresh.status != http.StatusOK {
		t.Fatalf("C2 acquire with the post-transfer version = %d: %s", c2Fresh.status, c2Fresh.raw)
	}
	if got := communicationHTTPTestDecode[sessions.CommandResult](t, c2Fresh); got.LeaseFence != 1 {
		t.Fatalf("C2 first acquire after the transfer minted fence %d, want 1", got.LeaseFence)
	}

	// C6: a second accept under a DIFFERENT idempotency key meets the Handoff
	// version the first one advanced and is refused with zero effects.
	c6Before := communicationHTTPTestEffects(t, estate.eng, estate.tenant)
	c6 := respondToVacantHandoff(t, estate, c2Recipient.communication.Token, c2Offer.HandoffID,
		c2Read.Handoff.ETag, map[string]any{"transition": "accept"}, model.NewID().String())
	if c6.status != http.StatusConflict {
		t.Fatalf("C6 second accept under a new key = %d: %s", c6.status, c6.raw)
	}
	assertCommunicationHTTPTestNoEffects(t, estate.eng, estate.tenant, c6Before,
		"C6 second accept under a new key")

	// C8: the owner changes under an offer that is still open. The new owner is
	// not the recipient and the accept is refused without effects.
	reassigned := createVacantHandoffWork(t, estate, estate.owner, "user",
		estate.owner.id.String(), "OT-V C8 changed owner")
	_, c8ETag := readVacantHandoffItem(t, estate, estate.owner, reassigned.id)
	c8Offer := offerVacantHandoff(t, estate, estate.owner.token, reassigned.id, c8ETag,
		map[string]any{"kind": "user", "ref": estate.recipient.id}, "OT-V C8", 1)
	c8Read := communicationHTTPTestDecode[sessions.IncomingHandoffReadResult](
		t, estate.detail(t, estate.recipient.token, c8Offer.DeliveryID))
	_, c8Current := readVacantHandoffItem(t, estate, estate.owner, reassigned.id)
	assignment := communicationHTTPTestRequest(t, estate.eng, http.MethodPost,
		"/v1/m/sessions/work-items/"+reassigned.id.String()+"/assignments?mode=apply",
		estate.owner.token, estate.tenant, map[string]any{
			"owner_kind": "user", "owner_ref": estate.bystander.id,
		}, map[string]string{
			"If-Match": c8Current, "Idempotency-Key": model.NewID().String(),
		})
	if assignment.status != http.StatusOK {
		t.Fatalf("C8 reassignment = %d: %s", assignment.status, assignment.raw)
	}
	c8Stale := communicationHTTPTestDecode[sessions.IncomingHandoffReadResult](
		t, estate.detail(t, estate.recipient.token, c8Offer.DeliveryID))
	if c8Stale.OfferContext != sessions.IncomingHandoffContextStale {
		t.Fatalf("C8 offer context after the owner changed = %q", c8Stale.OfferContext)
	}
	c8Before := communicationHTTPTestEffects(t, estate.eng, estate.tenant)
	c8Refused := respondToVacantHandoff(t, estate, estate.recipient.token, c8Offer.HandoffID,
		c8Read.Handoff.ETag, map[string]any{"transition": "accept"}, model.NewID().String())
	if c8Refused.status != http.StatusConflict {
		t.Fatalf("C8 accept after the owner changed = %d: %s", c8Refused.status, c8Refused.raw)
	}
	assertCommunicationHTTPTestNoEffects(t, estate.eng, estate.tenant, c8Before,
		"C8 accept after the owner changed")
}

// newVacantHandoffOwningSession mints one live canonical session, grants it the
// Channel and returns it. One runtime run carries at most one live work binding,
// so every case that acquires a lease brings its own.
func newVacantHandoffOwningSession(
	t *testing.T,
	estate incomingHandoffHTTPEstate,
	label string,
) communicationHTTPTestSession {
	t.Helper()
	session := createCommunicationHTTPTestSession(t, estate.eng, estate.tenant,
		estate.workspace, label+"-"+model.NewID().String())
	estate.grantChannel(t,
		map[string]any{"kind": "session", "ref": session.sid}, label+" owning session")
	return session
}
