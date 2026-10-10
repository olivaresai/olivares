// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
)

// The messaging estate is the consent estate with the communication kernel
// activated the way an operator activates it. Deliveries, audience arcs and
// handoffs are written by the product routes that write them; a decision
// request, which no route creates, is written through the guarded store seam
// over a product-created work item and channel.

// messagingGraph is what the messaging cases share in T: the member who owns
// the channel and sends, a control member no case retires, and the control rows
// that name one of them.
type messagingGraph struct {
	sender  communicationHTTPTestUser
	control communicationHTTPTestUser
	// handoffs carries the channel's coordinates in the shape the product
	// handoff helpers take, with the sender as the channel's administrator.
	handoffs incomingHandoffHTTPEstate
	controls map[string]messagingControl
}

// messagingControl is a row of a case's kind that names a member other than the
// subject, and the state it was written with.
type messagingControl struct {
	id    model.ID
	names model.ID
	state string
}

// messagingSeeders are the counted communication rows only the activated
// communication kernel writes. Their cases run on the messaging estate, whose
// subjects sign in and are granted its channel.
var messagingSeeders = map[string]retirementSeeder{
	"sessions.message_delivery.recipient_ref": {"sessions.message_delivery", retireKeeps, func(e *consentEstate, s retirementSubject) model.ID {
		return e.sendNotice(s.id)
	}},
	"sessions.message_audience_recipient.recipient_ref": {"sessions.message_audience_recipient", retireKeeps, func(e *consentEstate, s retirementSubject) model.ID {
		return e.audienceRecipientOf(e.sendNotice(s.id))
	}},
	"sessions.decision_request.requester_ref": {"sessions.decision_request", retireBlocks, func(e *consentEstate, s retirementSubject) model.ID {
		return e.seedDecisionRequest(s.id, e.comm.sender.id, s.id.String())
	}},
	"sessions.decision_request.owner_ref": {"sessions.decision_request", retireBlocks, func(e *consentEstate, s retirementSubject) model.ID {
		return e.seedDecisionRequest(e.comm.sender.id, s.id, s.id.String())
	}},
	"sessions.handoff.from_ref": {"sessions.handoff", retireBlocks, func(e *consentEstate, s retirementSubject) model.ID {
		return e.offerHandoff(communicationHTTPTestUser{id: s.id, token: s.token}, e.comm.sender.id)
	}},
	"sessions.handoff.to_ref": {"sessions.handoff", retireBlocks, func(e *consentEstate, s retirementSubject) model.ID {
		return e.offerHandoff(e.comm.sender, s.id)
	}},
}

// messagingSiblings names, for each case of a two-party row, the column of the
// other party, which the case's row fills with the sender: the subject is named
// through the case's own column alone.
var messagingSiblings = map[string]string{
	"sessions.decision_request.requester_ref": "owner_ref",
	"sessions.decision_request.owner_ref":     "requester_ref",
	"sessions.handoff.from_ref":               "to_ref",
	"sessions.handoff.to_ref":                 "from_ref",
}

// Hold the real setup handoff between intake and outbox settlement. Returning
// the estate here would let a no-effects baseline race the setup publication.
func TestMessagingEstateWaitsForSetupOutboxSettlement(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	resume := sync.OnceFunc(func() { close(release) })
	done := make(chan struct{})
	defer func() { // Release and join before engine cleanup joins the pump.
		resume()
		<-done
	}()
	ready := make(chan *consentEstate, 1)
	go func() {
		defer close(done)
		ready <- bootMessagingEstateWithSink(t, "sqlite", func(event sessions.WorkEventEnvelope) {
			if event.Type == "work.handoff.offered" {
				close(entered)
				<-release
			}
		})
	}()
	select {
	case <-entered:
	case <-ready:
		t.Fatal("messaging estate returned before setup handoff publication settled")
	case <-time.After(2 * time.Minute): // Restarted estate setup also runs under -race.
		t.Fatal("setup handoff did not reach the real sink")
	}
	select {
	case <-ready:
		t.Fatal("messaging estate returned while setup handoff settlement was held")
	case <-time.After(time.Second):
	}
	resume()
	select {
	case e := <-ready:
		states := communicationHTTPTestOutboxStates(t, e.eng, e.tT)
		if len(states) != 1 || states["published"] == 0 {
			t.Fatalf("messaging setup outbox is still unsettled: %v", states)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("messaging estate did not return after setup publication settled")
	}
}

// bootMessagingEstate boots the composition on engineName with the
// communication kernel activated, claims it, creates T and B and restarts it,
// as a configured installation does after its first tenants. In T it creates
// the sender (an owner) and the control member (an editor), a storage channel
// the sender administers and has granted to both, and the control rows.
func bootMessagingEstate(t *testing.T, engineName string) *consentEstate {
	t.Helper()
	return bootMessagingEstateWithSink(t, engineName, nil)
}

// bootMessagingEstateWithSink allows a test to pause settlement after the real
// sink accepts a setup event. Publication and storage still use the real ports.
func bootMessagingEstateWithSink(t *testing.T, engineName string, after func(sessions.WorkEventEnvelope)) *consentEstate {
	t.Helper()
	backing := consentBacking(t, engineName)
	eng := bootActivatedCommunicationHTTPTestEngine(t, backing)
	e := &consentEstate{t: t, engine: engineName, eng: eng, h: eng.api.Handler()}
	e.setupRoot()
	e.tT = e.createOrg("consent-t")
	e.tB = e.createOrg("consent-b")
	sender := createCommunicationHTTPTestUser(t, eng, e.admin, e.tT, "sender@messaging.test", auth.RoleOwner)
	control := createCommunicationHTTPTestUser(t, eng, e.admin, e.tT, "control@messaging.test", auth.RoleEditor)
	if err := eng.Close(); err != nil {
		t.Fatalf("close the bootstrapped estate: %v", err)
	}
	e.eng = bootCommunicationHTTPTestEngine(t, backing)
	t.Cleanup(func() { _ = e.eng.Close() })
	e.h = e.eng.api.Handler()
	if readiness, err := e.eng.sessionsMod.EvaluateCommunicationReadiness(context.Background()); err != nil || !readiness.Effective {
		t.Fatalf("communication readiness after the bootstrap restart = %+v, err %v", readiness, err)
	}
	e.signInRoot()
	sender = loginCommunicationHTTPTestUser(t, e.eng, sender.id, "sender@messaging.test")
	control = loginCommunicationHTTPTestUser(t, e.eng, control.id, "control@messaging.test")
	stepUpCommunicationHTTPTestUser(t, e.eng, sender.token)
	e.comm = &messagingGraph{sender: sender, control: control, handoffs: incomingHandoffHTTPEstate{
		eng: e.eng, tenant: e.tT, workspace: e.defaultWorkspace(e.tT), owner: sender,
	}}
	e.comm.handoffs.channelID = e.createStorageChannel()
	e.comm.handoffs.grantChannel(t, map[string]any{"kind": "user", "ref": control.id}, "control")
	if after != nil {
		sessions.WithWorkEventSink(stoppingWorkSink{inner: e.eng.workSink, after: after})(e.eng.sessionsMod)
	}
	e.seedControls()
	// Finish setup effects before callers snapshot rows to detect new effects.
	eventually(t, "messaging setup outbox publication", func() bool {
		states := communicationHTTPTestOutboxStates(t, e.eng, e.tT)
		return len(states) == 1 && states["published"] > 0
	})
	return e
}

// createStorageChannel creates, as the sender, a storage-protected channel in
// T's default workspace, granted to the sender alone.
func (e *consentEstate) createStorageChannel() model.ID {
	e.t.Helper()
	g := e.comm
	created := communicationHTTPTestRequest(e.t, e.eng, http.MethodPost, "/v1/m/sessions/channels",
		g.sender.token, e.tT, map[string]any{
			"workspace_id": g.handoffs.workspace.String(), "slug": "consent-retirement",
			"name": "Consent retirement", "content_protection": "storage",
			"default_ack_policy": "each_required", "default_ack_timeout_ms": 600_000, "max_fanout": 1,
			"initial_grants": []map[string]any{{
				"subject":  map[string]any{"kind": "user", "ref": g.sender.id},
				"can_read": true, "can_write": true, "can_admin": true,
			}},
		}, nil)
	if created.status != http.StatusCreated {
		e.t.Fatalf("create the storage channel = %d: %s", created.status, created.raw)
	}
	return communicationHTTPTestDecode[sessions.ChannelMutationResult](e.t, created).Channel.ID
}

// seedControls writes, for every messaging case, a row of its kind that names
// the sender or the control member: a notice to the control member, a decision
// request the sender asks of it and a handoff the sender offers it.
func (e *consentEstate) seedControls() {
	e.t.Helper()
	g := e.comm
	delivery := e.sendNotice(g.control.id)
	request := e.seedDecisionRequest(g.sender.id, g.control.id, "control")
	handoff := e.offerHandoff(g.sender, g.control.id)
	rows := map[string]messagingControl{
		"sessions.message_delivery.recipient_ref":           {id: delivery, names: g.control.id},
		"sessions.message_audience_recipient.recipient_ref": {id: e.audienceRecipientOf(delivery), names: g.control.id},
		"sessions.decision_request.requester_ref":           {id: request, names: g.sender.id},
		"sessions.decision_request.owner_ref":               {id: request, names: g.control.id},
		"sessions.handoff.from_ref":                         {id: handoff, names: g.sender.id},
		"sessions.handoff.to_ref":                           {id: handoff, names: g.control.id},
	}
	g.controls = make(map[string]messagingControl, len(rows))
	for key, c := range rows {
		c.state = e.rowState(e.tT, messagingSeeders[key].kind, c.id)
		g.controls[key] = c
	}
}

// messagingSubject creates a member of T with a password, signs it in with a
// stepped-up session so a case can act as it, and grants it the channel.
func (e *consentEstate) messagingSubject(label string) retirementSubject {
	e.t.Helper()
	email := label + "@messaging.test"
	u := createCommunicationHTTPTestUser(e.t, e.eng, e.admin, e.tT, email, auth.RoleEditor)
	stepUpCommunicationHTTPTestUser(e.t, e.eng, u.token)
	e.comm.handoffs.grantChannel(e.t, map[string]any{"kind": "user", "ref": u.id}, label)
	return retirementSubject{id: u.id, email: email, token: u.token}
}

// sendNotice publishes, as the sender, a direct notice to recipient over the
// product route and returns its delivery.
func (e *consentEstate) sendNotice(recipient model.ID) model.ID {
	e.t.Helper()
	g := e.comm
	sent := communicationHTTPTestRequest(e.t, e.eng, http.MethodPost, "/v1/m/sessions/messages/send",
		g.sender.token, e.tT, map[string]any{
			"channel_id": g.handoffs.channelID,
			"recipient":  map[string]any{"kind": "user", "ref": recipient},
			"content": map[string]any{
				"subject": "Retirement notice",
				"blocks":  []map[string]any{{"type": "text", "format": "plain", "text": "A notice to one member."}},
			},
		}, map[string]string{"Idempotency-Key": model.NewID().String()})
	if sent.status != http.StatusCreated {
		e.t.Fatalf("send a notice = %d: %s", sent.status, sent.raw)
	}
	result := communicationHTTPTestDecode[sessions.DirectNoticePublishResult](e.t, sent)
	if result.DeliveryID.IsZero() || result.DeliveryCount != 1 {
		e.t.Fatalf("the notice = %+v, want one delivery", result)
	}
	return result.DeliveryID
}

// audienceRecipientOf returns the audience arc delivery was fanned out from.
func (e *consentEstate) audienceRecipientOf(delivery model.ID) model.ID {
	e.t.Helper()
	ctx := context.Background()
	var id model.ID
	if err := e.eng.store.View(ctx, e.tT, func(sc store.Scope) error {
		repo, err := sc.Ext("sessions.message_audience_recipient")
		if err != nil {
			return err
		}
		arcs, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{
			{Column: "message_delivery_id", Op: model.OpEq, Value: delivery.String()},
		}, Limit: 2})
		if err != nil {
			return err
		}
		if len(arcs) != 1 {
			return fmt.Errorf("the delivery has %d audience arcs, want one", len(arcs))
		}
		id = model.ID(arcs[0].String(model.ColID))
		return nil
	}); err != nil {
		e.t.Fatalf("read the audience arc: %v", err)
	}
	return id
}

// offerHandoff has offerer create a work item it owns, ready it and offer it to
// recipient over the product routes, and returns the handoff.
func (e *consentEstate) offerHandoff(offerer communicationHTTPTestUser, recipient model.ID) model.ID {
	e.t.Helper()
	g := e.comm
	work := createVacantHandoffWork(e.t, g.handoffs, offerer, "user", offerer.id.String(), "Retirement handoff")
	offered := offerVacantHandoff(e.t, g.handoffs, offerer.token, work.id, work.etag,
		map[string]any{"kind": "user", "ref": recipient}, "Retirement handoff", 1)
	if offered.State != sessions.HandoffOffered || offered.HandoffID.IsZero() {
		e.t.Fatalf("the handoff offer = %+v", offered)
	}
	return offered.HandoffID
}

// seedDecisionRequest writes a pending decision request that requester asks of
// owner, keyed key, on a work item the sender created over the product route.
// No product route creates a decision request, so it is written through the
// guarded store seam: its carrier message, a draft of kind decision_request on
// the storage channel at the channel's protection generation, and the request,
// in one transaction that pinned both accounts under the directory barrier.
func (e *consentEstate) seedDecisionRequest(requester, owner model.ID, key string) model.ID {
	e.t.Helper()
	g := e.comm
	work := createVacantHandoffWork(e.t, g.handoffs, g.sender, "user", g.sender.id.String(), "Retirement decision")
	generation := e.channelGeneration()
	workspace := g.handoffs.workspace.String()
	message := model.NewID()
	content := `{"subject":"Decision","blocks":[{"type":"text","format":"plain","text":"Proceed?"}]}`
	request := `{"question":"Proceed?","choices":[{"key":"yes","label":"Yes"},{"key":"no","label":"No"}]}`
	contentDigest, requestDigest := sha256.Sum256([]byte(content)), sha256.Sum256([]byte(request))
	now := time.Now().UTC()
	carrier := model.Record{
		"workspace_id": workspace, "channel_id": g.handoffs.channelID.String(), "work_item_id": work.id.String(),
		"thread_id": message.String(), "kind": "decision_request", "state": "draft",
		"sender_kind": "user", "sender_ref": requester.String(), "urgency": "normal",
		"ack_policy": "none", "ack_quorum": int64(0),
		"available_at":     model.NewTimestamp(now.Add(time.Minute)).String(),
		"automation_depth": int64(0), "last_event_seq": int64(0),
		"payload_encoding": "plain_json", "payload_plain_json": content,
		"payload_schema": "communication.message.v1", "payload_digest": contentDigest[:],
		"payload_protection_generation": generation,
	}
	return e.seedFencedWith(e.tT, "sessions.decision_request", model.Record{
		"workspace_id": workspace, "message_id": message.String(), "work_item_id": work.id.String(),
		"decision_key": key, "requester_kind": "user", "requester_ref": requester.String(),
		"owner_kind": "user", "owner_ref": owner.String(), "state": "pending",
		"authority_requirement": "sessions.decision",
		"due_at":                model.NewTimestamp(now.Add(time.Hour)).String(), "last_response_seq": int64(0),
		"request_encoding": "plain_json", "request_plain_json": request,
		"request_schema": "communication.decision-request.v1", "request_digest": requestDigest[:],
		"request_protection_generation": generation,
	}, func(ctx context.Context, sc store.Scope) error {
		messages, err := sc.Ext("sessions.message")
		if err != nil {
			return err
		}
		_, err = messages.CreateWithID(ctx, message, carrier)
		return err
	}, requester, owner)
}

// channelGeneration reads the protection generation of the messaging channel.
func (e *consentEstate) channelGeneration() int64 {
	e.t.Helper()
	ctx := context.Background()
	var generation int64
	if err := e.eng.store.View(ctx, e.tT, func(sc store.Scope) error {
		repo, err := sc.Ext("sessions.channel")
		if err != nil {
			return err
		}
		rec, err := repo.Get(ctx, e.comm.handoffs.channelID)
		generation = rec.Int("protection_generation")
		return err
	}); err != nil {
		e.t.Fatalf("read the channel: %v", err)
	}
	if generation < 1 {
		e.t.Fatalf("the channel's protection generation is %d", generation)
	}
	return generation
}

// messagingFenceWriters are the fenced communication writers a product route
// reaches, each naming the account in its own reference: a channel grant, a work
// item's owner and a handoff's recipient. They run on the messaging estate.
func messagingFenceWriters() []fenceWriter {
	return []fenceWriter{
		{name: "a channel grant to the account", messaging: true, kind: "sessions.channel_grant", column: "subject_ref",
			// The account is granted nothing yet, so the only grant naming it is the
			// writer's.
			subject: func(e *consentEstate, label string) fenceSubject {
				email := label + "@messaging.test"
				u := createCommunicationHTTPTestUser(e.t, e.eng, e.admin, e.tT, email, auth.RoleEditor)
				return fenceSubject{id: u.id, email: email, token: u.token}
			},
			prepare: func(e *consentEstate, s fenceSubject) func() consentResp {
				g := e.comm
				ifMatch := e.channelETag()
				return func() consentResp {
					return e.doWith("POST", "/v1/m/sessions/channels/"+g.handoffs.channelID.String()+"/grants",
						g.sender.token, e.tT, map[string]any{
							"subject": map[string]any{"kind": "user", "ref": s.id}, "can_read": true, "can_write": true,
						}, map[string]string{"If-Match": ifMatch})
				}
			}},
		{name: "a work item the account owns", messaging: true, kind: "sessions.work_item", column: "owner_ref",
			prepare: func(e *consentEstate, s fenceSubject) func() consentResp {
				g := e.comm
				return func() consentResp {
					return e.doWith("POST", "/v1/m/sessions/work-items?mode=apply", g.sender.token, e.tT, map[string]any{
						"workspace_id": g.handoffs.workspace.String(), "work_kind": "implementation",
						"title": "Fenced work", "brief_md": "A work item the account owns.",
						"context_refs": []any{}, "priority": "p1",
						"owner_kind": "user", "owner_ref": s.id.String(),
						"provenance_kind": "human", "provenance_ref": "test:fence",
						"acceptance": []map[string]any{{
							"criterion_key": "done", "ordinal": 0, "statement": "The work is done", "required": true,
						}},
					}, map[string]string{"Idempotency-Key": model.NewID().String()})
				}
			}},
		{name: "a handoff offered to the account", messaging: true, recheck: true, kind: "sessions.handoff", column: "to_ref",
			prepare: func(e *consentEstate, s fenceSubject) func() consentResp {
				g := e.comm
				work := createVacantHandoffWork(e.t, g.handoffs, g.sender, "user", g.sender.id.String(), "Fenced handoff")
				return func() consentResp {
					return e.doWith("POST", "/v1/m/sessions/handoffs", g.sender.token, e.tT, map[string]any{
						"channel_id": g.handoffs.channelID, "work_item_id": work.id,
						"recipient": map[string]any{"kind": "user", "ref": s.id},
						"handoff": map[string]any{
							"summary": "Fenced handoff", "next_action": "Continue the fenced handoff",
							"risk": "None recorded for the fenced handoff",
						},
						"ack_deadline":         time.Now().UTC().Add(30 * time.Minute),
						"expected_owner_epoch": 1,
					}, map[string]string{"If-Match": work.etag, "Idempotency-Key": model.NewID().String()})
				}
			}},
	}
}

// messagingFenceSubject provisions the account a communication writer names by
// default: an editor of T with a stepped-up session, granted the channel.
func (e *consentEstate) messagingFenceSubject(label string) fenceSubject {
	e.t.Helper()
	s := e.messagingSubject(label)
	return fenceSubject{id: s.id, email: s.email, token: s.token}
}

// channelETag reads the messaging channel's current version as the If-Match a
// change to it presents.
func (e *consentEstate) channelETag() string {
	e.t.Helper()
	g := e.comm
	read := communicationHTTPTestRequest(e.t, e.eng, http.MethodGet,
		"/v1/m/sessions/channels/"+g.handoffs.channelID.String(), g.sender.token, e.tT, nil, nil)
	if read.status != http.StatusOK {
		e.t.Fatalf("read the channel = %d: %s", read.status, read.raw)
	}
	return fmt.Sprintf(`"v%d"`, communicationHTTPTestDecode[sessions.Channel](e.t, read).Version)
}

// wantNames fails unless the row id of kind is stored in T and its column names
// user, reading the column's full declaration as the step that retires the row
// reads it (namesThrough).
func (e *consentEstate) wantNames(t *testing.T, kind model.Kind, column string, id, user model.ID) {
	t.Helper()
	ctx := context.Background()
	account := e.aliasesOf(t, user)
	named, found := false, false
	var stored any
	if err := e.eng.store.View(ctx, e.tT, func(sc store.Scope) error {
		repo, err := sc.Ext(kind)
		if err != nil {
			return err
		}
		var decl *model.ColumnDecl
		for _, f := range repo.Descriptor().Fields {
			if f.Name == column {
				decl = f.Principal
			}
		}
		if decl == nil || !decl.Counted() {
			return fmt.Errorf("%s.%s declares no counted reference", kind, column)
		}
		rec, err := repo.Get(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		stored = rec[column]
		named, err = namesThrough(decl, rec, column, account)
		return err
	}); err != nil {
		t.Fatalf("read the %s row: %v", kind, err)
	}
	if !found {
		t.Fatalf("the %s row %s is not stored", kind, id)
	}
	if !named {
		t.Fatalf("the %s row %s does not name %s through %s, which holds %v", kind, id, user, column, stored)
	}
}

// accountAliases are the values a stored reference may name an account by.
type accountAliases struct {
	id                model.ID
	email, externalID string
	credentials       []model.ID
}

// aliasesOf reads the aliases of user from its account record.
func (e *consentEstate) aliasesOf(t *testing.T, user model.ID) accountAliases {
	t.Helper()
	ctx := context.Background()
	a := accountAliases{id: user}
	if err := e.eng.store.AuthView(ctx, func(as store.AuthScope) error {
		u, err := as.Users().Get(ctx, user)
		if err != nil {
			return err
		}
		a.email, a.externalID = u.Email, u.ExternalID
		q := model.Query{Filters: []model.Filter{{Column: "user_id", Op: model.OpEq, Value: user.String()}}, Limit: 500}
		for {
			sessions, page, err := as.Sessions().List(ctx, q)
			if err != nil {
				return err
			}
			for _, session := range sessions {
				a.credentials = append(a.credentials, session.ID)
			}
			if !page.HasMore || page.Cursor == "" {
				break
			}
			q.Cursor = page.Cursor
		}
		q.Cursor = ""
		for {
			tokens, page, err := as.Tokens().List(ctx, q)
			if err != nil {
				return err
			}
			for _, token := range tokens {
				a.credentials = append(a.credentials, token.ID)
			}
			if !page.HasMore || page.Cursor == "" {
				break
			}
			q.Cursor = page.Cursor
		}
		return nil
	}); err != nil {
		t.Fatalf("read the aliases of %s: %v", user, err)
	}
	return a
}

// namesThrough reports whether the value of column in rec names the account
// under decl, read the way the retirement steps read it. A union is read by the
// variant its discriminator selects. An external-id reference names the account
// whose roster external id it holds, as the governance step compares it. A
// scanned text names the account through one of its aliases. Every other
// reference is resolved by the store's own reader.
func namesThrough(decl *model.ColumnDecl, rec model.Record, column string, a accountAliases) (bool, error) {
	switch {
	case decl.Form == model.FormUnion:
		kind := rec.String(decl.Discriminator)
		variant, ok := decl.Kinds.Variant(kind)
		if !ok {
			return false, fmt.Errorf("%s selects the %q variant, which no kind table knows", decl.Discriminator, kind)
		}
		return namesThrough(variant, model.Record{column: rec[column]}, column, a)
	case decl.Form == model.FormScan:
		return textNamesAccount(rec.String(column), a), nil
	case decl.Form == model.FormRef && decl.Encoding == model.EncodeExternalID:
		ref := strings.TrimSpace(rec.String(column))
		return ref != "" && ref == a.externalID, nil
	}
	ids, err := decl.CountedUserIDs(rec, column)
	if err != nil || slices.Contains(ids, a.id) {
		return slices.Contains(ids, a.id), err
	}
	for _, credential := range decl.CountedCredentialIDs(rec, column) {
		if slices.Contains(a.credentials, credential) {
			return true, nil
		}
	}
	return false, nil
}

// textNamesAccount reports whether untyped text names the account, as the
// retirement steps match such text: the canonical id wherever it appears, in any
// case, and the email, in any case, or the external id, each as a whole token.
func textNamesAccount(text string, a accountAliases) bool {
	lower := strings.ToLower(text)
	if strings.Contains(lower, strings.ToLower(a.id.String())) {
		return true
	}
	return (a.email != "" && containsToken(lower, strings.ToLower(a.email))) ||
		(a.externalID != "" && containsToken(text, a.externalID))
}

// containsToken reports whether needle occurs in hay not run together with a
// neighboring identifier character.
func containsToken(hay, needle string) bool {
	for i := 0; ; {
		j := strings.Index(hay[i:], needle)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(needle)
		if !tokenChar(hay, start-1) && !tokenChar(hay, end) {
			return true
		}
		i = start + 1
	}
}

// tokenChar reports whether s[i] exists and would run together with an alias.
func tokenChar(s string, i int) bool {
	if i < 0 || i >= len(s) {
		return false
	}
	c := s[i]
	return c == '_' || c == '-' || c == '.' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// wantControlUntouched fails unless the control row of case key, which names a
// member other than subject, is still stored and still names that member, a
// control the step would block on keeps its state, and the subject's
// retirement record does not list it.
func (e *consentEstate) wantControlUntouched(t *testing.T, key string, subject model.ID) {
	t.Helper()
	c, ok := e.comm.controls[key]
	if !ok {
		t.Fatalf("the case %s has no control row", key)
	}
	s := retirementSeeders[key]
	e.wantNames(t, s.kind, strings.TrimPrefix(key, string(s.kind)+"."), c.id, c.names)
	if s.outcome == retireBlocks {
		if state := e.rowState(e.tT, s.kind, c.id); state != c.state {
			t.Errorf("the control %s row is %q after another member's retirement, want %q", s.kind, state, c.state)
		}
	}
	if rec, _ := e.record(subject, e.tT); strings.Contains(rec.BlockingRefs, c.id.String()) {
		t.Errorf("the retirement record lists the control %s row %s, which names another member", s.kind, c.id)
	}
}
