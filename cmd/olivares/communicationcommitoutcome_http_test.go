// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

// ---- HC: the commit outcome on the real communication wire ------------------
//
// These controls run the PRODUCTION router, the production store and a real
// PostgreSQL 16 database. The only thing added is the test-owned wire proxy
// (communicationcommitoutcome_proxy_test.go), and only on the application
// connection, so a COMMIT can be held or its answer withheld.
//
// What they answer is the question the whole change exists for: when the engine
// sent COMMIT and never learned the result, what does the CALLER get told, and
// does the durable state match that answer once the transaction has settled?
//
// Nothing in this file was executed by its author. Every class below is an
// expectation for the hosted run.

const (
	// commitOutcomeUnknownCode is the wire code under test.
	commitOutcomeUnknownCode = "commit_outcome_unknown"
	// commitOutcomeGrantRelation is the route's own relation, used to attribute a
	// COMMIT to the transaction that took a row lock on it.
	commitOutcomeGrantRelation = "sessions_channel_grant"
	// commitOutcomeChannelRelation is the Channel table, whose row the same
	// transaction moves.
	commitOutcomeChannelRelation = "sessions_channel"
	// commitOutcomeAuditRelation is the evidence ledger.
	commitOutcomeAuditRelation = "audit_events"
	// commitOutcomeReceiptRelation is the command-receipt table every keyed route
	// writes exactly one row into per command. It is both how a keyed COMMIT is
	// attributed and how "exactly one durable effect" is counted, so the count
	// does not depend on guessing each route's own projection tables.
	commitOutcomeReceiptRelation = "sessions_communication_command"
	// commitOutcomeReturnLimit bounds how long a control waits for ServeHTTP to
	// return after it cancels. It is a hold limit, never an oracle.
	commitOutcomeReturnLimit = 10 * time.Second
)

// commitOutcomeHTTPFixture is one proxied estate with one Channel the owner
// administers, plus the two direct observation channels.
type commitOutcomeHTTPFixture struct {
	estate   channelAdministrationHTTPEstate
	proxy    *commitOutcomeProxy
	owner    *sql.DB // pg_locks and backend settlement
	admin    *sql.DB // row presence and xmin (BYPASSRLS)
	channel  model.ID
	wsQuery  string
	auditKey int64
}

func newCommitOutcomeHTTPFixture(t *testing.T, slug string) *commitOutcomeHTTPFixture {
	t.Helper()
	backing, proxy, dsns := commitOutcomeProxiedPostgresEstate(t)
	estate := bootChannelAdministrationHTTPEstate(t, backing)
	f := &commitOutcomeHTTPFixture{
		estate:  estate,
		proxy:   proxy,
		owner:   commitOutcomeDirectConn(t, dsns.Owner, "owner"),
		admin:   commitOutcomeDirectConn(t, dsns.Admin, "admin"),
		wsQuery: "workspace_id=" + estate.workspace.String(),
	}
	ownerAll := channelAdministrationGrant(
		channelAdministrationSubject("user", estate.owner.id), true, true, true)
	created := estate.createChannel(t, estate.workspace, slug, []map[string]any{ownerAll})
	f.channel = created.Channel.ID
	// The tenant audit advisory key is EXCLUDED from every lock-wait observation:
	// two requests of the same tenant always contend on it, so counting it would
	// let a route with no fence of its own look serialized.
	// The grant fixture keeps the audit key only as a whole value: HC-3 matches it
	// by name. The split halves belong to the KEYED fixture, which is where the
	// per-route fence comparison lives; assigning them here named fields this type
	// does not declare.
	f.auditKey = commitOutcomeAdvisoryKey(t, f.owner, estate.tenant.String())
	return f
}

func (f *commitOutcomeHTTPFixture) currentETag(t *testing.T) string {
	t.Helper()
	return f.estate.sheetPage(t, f.estate.owner.token, f.channel,
		f.wsQuery+"&state=active&limit=1").ETag
}

func commitOutcomeGrantBody(subject model.ID) map[string]any {
	return map[string]any{
		"subject":  channelAdministrationSubject("user", subject),
		"can_read": true, "can_write": false, "can_admin": false,
	}
}

func (f *commitOutcomeHTTPFixture) grantPath() string {
	return "/v1/m/sessions/channels/" + f.channel.String() + "/grants"
}

// commitOutcomeServeInBackground runs one request on a caller-owned context in
// its own goroutine. It never calls a testing method from that goroutine: a
// marshal failure comes back as status 0 with the reason in the body, so the
// control fails on its own goroutine where t.Fatalf is legal.
func commitOutcomeServeInBackground(
	ctx context.Context,
	eng *engine,
	method, path, token string,
	tenant model.TenantID,
	body any,
	headers map[string]string,
) <-chan communicationHTTPTestResponse {
	out := make(chan communicationHTTPTestResponse, 1)
	go func() {
		var raw []byte
		if body != nil {
			encoded, err := json.Marshal(body)
			if err != nil {
				out <- communicationHTTPTestResponse{
					status: 0, header: http.Header{},
					raw: []byte("marshal " + method + " " + path + ": " + err.Error()),
				}
				return
			}
			raw = encoded
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(raw)).WithContext(ctx)
		req.RemoteAddr = "127.0.0.1:43210"
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if !tenant.IsZero() {
			req.Header.Set("X-Olivares-Tenant", tenant.String())
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		recorder := httptest.NewRecorder()
		eng.api.Handler().ServeHTTP(recorder, req)
		out <- communicationHTTPTestResponse{
			status: recorder.Code, header: recorder.Header().Clone(), raw: recorder.Body.Bytes(),
		}
	}()
	return out
}

func commitOutcomeAwait(
	t *testing.T, responses <-chan communicationHTTPTestResponse, what string,
) communicationHTTPTestResponse {
	t.Helper()
	select {
	case response := <-responses:
		if response.status == 0 {
			t.Fatalf("COULD_NOT_LOOK: %s could not be issued: %s", what, response.raw)
		}
		return response
	case <-time.After(commitOutcomeReturnLimit):
		t.Fatalf("COULD_NOT_LOOK: %s did not return within %s", what, commitOutcomeReturnLimit)
		return communicationHTTPTestResponse{}
	}
}

// commitOutcomeAssertUnknown is the wire assertion: 503, the code, and the
// existing UNKNOWN verdict. It is deliberately all three — a 503 alone would not
// tell a caller apart from an availability refusal, and the code alone would not
// keep the status in the band the console already treats as "could not look".
func commitOutcomeAssertUnknown(t *testing.T, response communicationHTTPTestResponse, what string) {
	t.Helper()
	if response.status != http.StatusServiceUnavailable {
		t.Fatalf("%s = %d: %s\nwant 503 %s — the caller's write may be durable, so a definite failure is the one answer that must not be sent",
			what, response.status, response.raw, commitOutcomeUnknownCode)
	}
	var body struct {
		Verdict string `json:"verdict"`
		Code    string `json:"code"`
		Error   struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.raw, &body); err != nil {
		t.Fatalf("%s: decode %q: %v", what, response.raw, err)
	}
	if body.Code != commitOutcomeUnknownCode || body.Error.Code != commitOutcomeUnknownCode {
		t.Fatalf("%s codes = %q / %q, want %q in both", what, body.Code, body.Error.Code, commitOutcomeUnknownCode)
	}
	if body.Verdict != string(sessions.VerdictUnknown) {
		t.Fatalf("%s verdict = %q, want %q", what, body.Verdict, sessions.VerdictUnknown)
	}
	if got := response.header.Get("Retry-After"); got != "" {
		t.Fatalf("%s advertised Retry-After %q: nothing here is safe to retry on a timer", what, got)
	}
}

// commitOutcomeGrantXmin reads the durable rows of one attempt through the
// BYPASSRLS admin role and returns the three transaction ids. Equality is the
// attribution: the Channel change, the grant row and the audit act were written
// by ONE transaction, or the effect is not the one this attempt asked for.
func (f *commitOutcomeHTTPFixture) grantXmin(
	t *testing.T, subject model.ID,
) (grantXmin, channelXmin, auditXmin string, present bool) {
	t.Helper()
	ctx := context.Background()
	err := f.admin.QueryRowContext(ctx, `
		SELECT xmin::text FROM `+commitOutcomeGrantRelation+`
		WHERE tenant_id = $1 AND subject_ref = $2 AND state = 'active'`,
		f.estate.tenant.String(), subject.String()).Scan(&grantXmin)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", "", "", false
	case err != nil:
		t.Fatalf("COULD_NOT_LOOK: read the grant row for %s: %v", subject, err)
	}
	if err := f.admin.QueryRowContext(ctx, `
		SELECT xmin::text FROM `+commitOutcomeChannelRelation+`
		WHERE tenant_id = $1 AND id = $2`,
		f.estate.tenant.String(), f.channel.String()).Scan(&channelXmin); err != nil {
		t.Fatalf("COULD_NOT_LOOK: read the Channel row: %v", err)
	}
	if err := f.admin.QueryRowContext(ctx, `
		SELECT xmin::text FROM `+commitOutcomeAuditRelation+`
		WHERE tenant_id = $1 AND action LIKE 'sessions.communication.channel.%'
		ORDER BY seq DESC LIMIT 1`,
		f.estate.tenant.String()).Scan(&auditXmin); err != nil {
		t.Fatalf("COULD_NOT_LOOK: read the newest communication audit row: %v", err)
	}
	return grantXmin, channelXmin, auditXmin, true
}

// assertThreeWayAttribution is F-H: one transaction wrote all three effects.
func (f *commitOutcomeHTTPFixture) assertThreeWayAttribution(t *testing.T, subject model.ID) {
	t.Helper()
	grant, channel, audit, present := f.grantXmin(t, subject)
	if !present {
		t.Fatalf("the attributed grant for %s is absent after settlement", subject)
	}
	if grant != channel || grant != audit {
		t.Fatalf("the attempt's three effects carry different transaction ids (grant=%s channel=%s audit=%s): a durable effect that was not written by the attributed transaction is an unexplained row",
			grant, channel, audit)
	}
}

func (f *commitOutcomeHTTPFixture) subjectPresent(t *testing.T, subject model.ID) bool {
	t.Helper()
	var count int
	if err := f.admin.QueryRowContext(context.Background(), `
		SELECT count(*) FROM `+commitOutcomeGrantRelation+`
		WHERE tenant_id = $1 AND subject_ref = $2 AND state = 'active'`,
		f.estate.tenant.String(), subject.String()).Scan(&count); err != nil {
		t.Fatalf("COULD_NOT_LOOK: count the grants of %s: %v", subject, err)
	}
	if count > 1 {
		t.Fatalf("subject %s holds %d active grants: exactly one effect may survive an uncertain commit", subject, count)
	}
	return count == 1
}

// TestCommunicationCommitOutcomeHTTPPostgres holds HC-1a, HC-1a-d, HC-1b,
// HC-1b+, HC-2, HC-3, HC-4 and the HC-5g grant-recovery ladder.
//
// Each subtest boots its OWN estate. That is not caution for its own sake: a
// control that severs a pooled connection has changed the pool, and reusing it
// would let one control's damage show up as another's result.
func TestCommunicationCommitOutcomeHTTPPostgres(t *testing.T) {
	// HC-1a — the server answered COMMIT and the client never read the answer.
	// B-RED at C0 (the caller is told 500) and M-RED for M1, M2 and M5.
	t.Run("ack_lost_cancel", func(t *testing.T) {
		f := newCommitOutcomeHTTPFixture(t, "ack-lost-cancel")
		before := censusChannelGrantHistory(t, f.estate.eng, f.estate.tenant)
		etag := f.currentETag(t)
		subject := model.NewID()

		f.proxy.arm(commitOutcomeProxyHoldAck,
			commitOutcomeIdentifyByRelation(t, f.owner, commitOutcomeGrantRelation))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		responses := commitOutcomeServeInBackground(ctx, f.estate.eng, http.MethodPost,
			f.grantPath(), f.estate.owner.token, f.estate.tenant,
			commitOutcomeGrantBody(subject), map[string]string{"If-Match": etag})

		// W: the server's own CommandComplete tag for the COMMIT. Without it this
		// control would only be measuring a cancellation.
		event := f.proxy.waitForEvent("ack_held")
		if !strings.EqualFold(event.tag, "COMMIT") {
			t.Fatalf("COULD_NOT_LOOK: the server answered the COMMIT with %q, not COMMIT, so this schedule is not a lost acknowledgement", event.tag)
		}
		cancel()
		response := commitOutcomeAwait(t, responses, "the canceled grant")
		f.proxy.sever()
		commitOutcomeWaitForBackendSettlement(t, f.owner, event.pid)

		commitOutcomeAssertUnknown(t, response, "a grant whose COMMIT was answered and never read")
		after := censusChannelGrantHistory(t, f.estate.eng, f.estate.tenant)
		if after.grants != before.grants+1 || after.audits != before.audits+1 {
			t.Fatalf("the durable effect of an acknowledged COMMIT is missing: %+v -> %+v", before, after)
		}
		if !f.subjectPresent(t, subject) {
			t.Fatal("the acknowledged COMMIT left no grant for the attempt's subject")
		}
		f.assertThreeWayAttribution(t, subject)
		commitOutcomeAssertProxyCustody(t, f.proxy)
	})

	// HC-1a-d — the same lost answer, reached by a DEADLINE rather than a cancel.
	// It is the S1 shape, and it must not be classified apart from S2: the caller
	// learns exactly as little either way.
	t.Run("ack_lost_deadline", func(t *testing.T) {
		f := newCommitOutcomeHTTPFixture(t, "ack-lost-deadline")
		before := censusChannelGrantHistory(t, f.estate.eng, f.estate.tenant)
		etag := f.currentETag(t)
		subject := model.NewID()

		f.proxy.arm(commitOutcomeProxyHoldAck,
			commitOutcomeIdentifyByRelation(t, f.owner, commitOutcomeGrantRelation))
		ctx, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		responses := commitOutcomeServeInBackground(ctx, f.estate.eng, http.MethodPost,
			f.grantPath(), f.estate.owner.token, f.estate.tenant,
			commitOutcomeGrantBody(subject), map[string]string{"If-Match": etag})

		event := f.proxy.waitForEvent("ack_held")
		if !strings.EqualFold(event.tag, "COMMIT") {
			t.Fatalf("COULD_NOT_LOOK: the server produced no COMMIT tag before the deadline (%q), so the deadline shape was not established", event.tag)
		}
		response := commitOutcomeAwait(t, responses, "the expired grant")
		f.proxy.sever()
		commitOutcomeWaitForBackendSettlement(t, f.owner, event.pid)

		commitOutcomeAssertUnknown(t, response, "a grant whose COMMIT outlived its deadline")
		after := censusChannelGrantHistory(t, f.estate.eng, f.estate.tenant)
		if after.grants != before.grants+1 || after.audits != before.audits+1 {
			t.Fatalf("the durable effect of an acknowledged COMMIT is missing: %+v -> %+v", before, after)
		}
		f.assertThreeWayAttribution(t, subject)
	})

	// HC-1b — the answer is RELEASED after the driver has abandoned the socket.
	// The server decided, the bytes were written, and no part of the product ever
	// read them. A definite status here with committed rows is the exact defect.
	t.Run("ack_late", func(t *testing.T) {
		f := newCommitOutcomeHTTPFixture(t, "ack-late")
		before := censusChannelGrantHistory(t, f.estate.eng, f.estate.tenant)
		etag := f.currentETag(t)
		subject := model.NewID()

		f.proxy.arm(commitOutcomeProxyHoldAck,
			commitOutcomeIdentifyByRelation(t, f.owner, commitOutcomeGrantRelation))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		responses := commitOutcomeServeInBackground(ctx, f.estate.eng, http.MethodPost,
			f.grantPath(), f.estate.owner.token, f.estate.tenant,
			commitOutcomeGrantBody(subject), map[string]string{"If-Match": etag})

		event := f.proxy.waitForEvent("ack_held")
		cancel()
		response := commitOutcomeAwait(t, responses, "the canceled grant")
		// The held answer is delivered to a socket the driver has given up on.
		f.proxy.release()
		f.proxy.sever()
		commitOutcomeWaitForBackendSettlement(t, f.owner, event.pid)

		commitOutcomeAssertUnknown(t, response, "a grant whose COMMIT answer arrived too late to be read")
		after := censusChannelGrantHistory(t, f.estate.eng, f.estate.tenant)
		if after.grants != before.grants+1 || after.audits != before.audits+1 {
			t.Fatalf("the late answer named an effect that is not there: %+v -> %+v", before, after)
		}
		f.assertThreeWayAttribution(t, subject)
	})

	// HC-1b+ — the positive of the pair: the answer is released BEFORE the
	// cancellation, so the outcome is known and the caller is told the truth,
	// which here is success.
	t.Run("ack_before_cancel", func(t *testing.T) {
		f := newCommitOutcomeHTTPFixture(t, "ack-before-cancel")
		before := censusChannelGrantHistory(t, f.estate.eng, f.estate.tenant)
		etag := f.currentETag(t)
		subject := model.NewID()

		f.proxy.arm(commitOutcomeProxyHoldAck,
			commitOutcomeIdentifyByRelation(t, f.owner, commitOutcomeGrantRelation))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		responses := commitOutcomeServeInBackground(ctx, f.estate.eng, http.MethodPost,
			f.grantPath(), f.estate.owner.token, f.estate.tenant,
			commitOutcomeGrantBody(subject), map[string]string{"If-Match": etag})

		f.proxy.waitForEvent("ack_held")
		f.proxy.release()
		response := commitOutcomeAwait(t, responses, "the released grant")
		cancel()
		if response.status != http.StatusOK {
			t.Fatalf("a grant whose answer was read = %d: %s\nwant 200 — a known success must not be reported as uncertain",
				response.status, response.raw)
		}
		result := communicationHTTPTestDecode[sessions.ChannelMutationResult](t, response)
		if result.Grant.ID.IsZero() {
			t.Fatalf("the 200 named no grant: %s", response.raw)
		}
		after := censusChannelGrantHistory(t, f.estate.eng, f.estate.tenant)
		if after.grants != before.grants+1 || after.audits != before.audits+1 {
			t.Fatalf("a 200 grant moved the census by %+v -> %+v", before, after)
		}
		f.assertThreeWayAttribution(t, subject)
	})

	// HC-2 — COMMIT never reached the server at all. The outcome is STILL reported
	// unknown, and that is the honest answer: the client cannot prove the bytes
	// were not delivered. The census is what proves nothing happened, and it is
	// read only after settlement.
	t.Run("commit_undelivered", func(t *testing.T) {
		f := newCommitOutcomeHTTPFixture(t, "commit-undelivered")
		before := censusChannelGrantHistory(t, f.estate.eng, f.estate.tenant)
		etag := f.currentETag(t)
		subject := model.NewID()

		f.proxy.arm(commitOutcomeProxyHoldCommit,
			commitOutcomeIdentifyByRelation(t, f.owner, commitOutcomeGrantRelation))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		responses := commitOutcomeServeInBackground(ctx, f.estate.eng, http.MethodPost,
			f.grantPath(), f.estate.owner.token, f.estate.tenant,
			commitOutcomeGrantBody(subject), map[string]string{"If-Match": etag})

		event := f.proxy.waitForEvent("commit_held")
		cancel()
		response := commitOutcomeAwait(t, responses, "the undelivered grant")
		f.proxy.sever()
		commitOutcomeWaitForBackendSettlement(t, f.owner, event.pid)

		commitOutcomeAssertUnknown(t, response, "a grant whose COMMIT was never delivered")
		after := censusChannelGrantHistory(t, f.estate.eng, f.estate.tenant)
		if after != before {
			t.Fatalf("a COMMIT the server never saw left durable state: %+v -> %+v", before, after)
		}
		if f.subjectPresent(t, subject) {
			t.Fatal("a COMMIT the server never saw produced a grant")
		}
	})

	// HC-3 — a cancellation BEFORE the write is a definite refusal, and it must
	// stay one. Widening the unknown answer to cover refusals would make the
	// sentinel useless: a caller could no longer tell "nothing happened" from
	// "something may have happened".
	t.Run("prewrite_cancel", func(t *testing.T) {
		f := newCommitOutcomeHTTPFixture(t, "prewrite-cancel")
		before := censusChannelGrantHistory(t, f.estate.eng, f.estate.tenant)
		etag := f.currentETag(t)
		subject := model.NewID()

		// The owner session holds the tenant audit advisory lock, so the request
		// blocks before it writes anything.
		holder, err := f.owner.Conn(context.Background())
		if err != nil {
			t.Fatalf("COULD_NOT_LOOK: take the blocking session: %v", err)
		}
		defer func() { _ = holder.Close() }()
		if _, err := holder.ExecContext(context.Background(),
			`SELECT pg_catalog.pg_advisory_lock(pg_catalog.hashtextextended($1, 0))`,
			f.estate.tenant.String()); err != nil {
			t.Fatalf("COULD_NOT_LOOK: hold the tenant audit advisory lock: %v", err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		responses := commitOutcomeServeInBackground(ctx, f.estate.eng, http.MethodPost,
			f.grantPath(), f.estate.owner.token, f.estate.tenant,
			commitOutcomeGrantBody(subject), map[string]string{"If-Match": etag})
		if !commitOutcomeWaitForAdvisoryHolderWait(t, f.owner, f.auditKey) {
			t.Fatal("COULD_NOT_LOOK: the request never waited on the tenant audit lock, so it was not stopped before its write")
		}
		cancel()
		response := commitOutcomeAwait(t, responses, "the pre-write grant")
		if _, err := holder.ExecContext(context.Background(),
			`SELECT pg_catalog.pg_advisory_unlock(pg_catalog.hashtextextended($1, 0))`,
			f.estate.tenant.String()); err != nil {
			t.Fatalf("COULD_NOT_LOOK: release the tenant audit advisory lock: %v", err)
		}

		if response.status == http.StatusOK {
			t.Fatalf("a request canceled before its write returned 200: %s", response.raw)
		}
		var body struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(response.raw, &body)
		if body.Code == commitOutcomeUnknownCode {
			t.Fatalf("a cancellation before any write was reported as an undetermined commit (%d: %s): this outcome is KNOWN, and blurring it makes the unknown answer meaningless",
				response.status, response.raw)
		}
		if after := censusChannelGrantHistory(t, f.estate.eng, f.estate.tenant); after != before {
			t.Fatalf("a refused pre-write attempt changed durable state: %+v -> %+v", before, after)
		}
	})

	// HC-4 — the positive with no interference at all. It is also where the
	// three-way attribution is asserted on a plain success, so mutant M8 (write
	// the Channel change in its own transaction) dies here.
	t.Run("positive", func(t *testing.T) {
		f := newCommitOutcomeHTTPFixture(t, "commit-outcome-positive")
		before := censusChannelGrantHistory(t, f.estate.eng, f.estate.tenant)
		subject := model.NewID()
		response := f.estate.grant(t, f.estate.owner.token, f.channel,
			commitOutcomeGrantBody(subject), f.currentETag(t))
		if response.status != http.StatusOK {
			t.Fatalf("an unimpeded grant = %d: %s", response.status, response.raw)
		}
		after := censusChannelGrantHistory(t, f.estate.eng, f.estate.tenant)
		if after.grants != before.grants+1 || after.audits != before.audits+1 {
			t.Fatalf("a committed grant moved the census by %+v -> %+v", before, after)
		}
		f.assertThreeWayAttribution(t, subject)
	})

	// HC-5g1 — recovery after an UNKNOWN that turned out to be committed. The
	// remedy is the same request with the same precondition; it waits on the
	// original's Channel row lock and then reads the settled result. 409 is the
	// truthful answer, and the census must not move again.
	t.Run("retry_after_commit", func(t *testing.T) {
		f := newCommitOutcomeHTTPFixture(t, "retry-after-commit")
		etag := f.currentETag(t)
		subject := model.NewID()
		body := commitOutcomeGrantBody(subject)

		f.proxy.arm(commitOutcomeProxyHoldAck,
			commitOutcomeIdentifyByRelation(t, f.owner, commitOutcomeGrantRelation))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		responses := commitOutcomeServeInBackground(ctx, f.estate.eng, http.MethodPost,
			f.grantPath(), f.estate.owner.token, f.estate.tenant, body,
			map[string]string{"If-Match": etag})
		event := f.proxy.waitForEvent("ack_held")
		cancel()
		commitOutcomeAwait(t, responses, "the canceled grant")
		f.proxy.sever()
		commitOutcomeWaitForBackendSettlement(t, f.owner, event.pid)

		settled := censusChannelGrantHistory(t, f.estate.eng, f.estate.tenant)
		retry := f.estate.grant(t, f.estate.owner.token, f.channel, body, etag)
		if retry.status != http.StatusConflict {
			t.Fatalf("the same request with the same precondition after a committed original = %d: %s\nwant 409 — the precondition it was sent with no longer holds",
				retry.status, retry.raw)
		}
		if after := censusChannelGrantHistory(t, f.estate.eng, f.estate.tenant); after != settled {
			t.Fatalf("the remedy produced a second effect: %+v -> %+v", settled, after)
		}
		if !f.subjectPresent(t, subject) {
			t.Fatal("the committed original's grant is absent after settlement")
		}
	})

	// HC-5g2 — recovery after an UNKNOWN that turned out NOT to be committed. The
	// same remedy answers 200 and produces exactly one effect. The pair 5g1/5g2 is
	// what makes the remedy safe WITHOUT the caller having to know which happened.
	t.Run("retry_after_abort", func(t *testing.T) {
		f := newCommitOutcomeHTTPFixture(t, "retry-after-abort")
		before := censusChannelGrantHistory(t, f.estate.eng, f.estate.tenant)
		etag := f.currentETag(t)
		subject := model.NewID()
		body := commitOutcomeGrantBody(subject)

		f.proxy.arm(commitOutcomeProxyHoldCommit,
			commitOutcomeIdentifyByRelation(t, f.owner, commitOutcomeGrantRelation))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		responses := commitOutcomeServeInBackground(ctx, f.estate.eng, http.MethodPost,
			f.grantPath(), f.estate.owner.token, f.estate.tenant, body,
			map[string]string{"If-Match": etag})
		event := f.proxy.waitForEvent("commit_held")
		cancel()
		commitOutcomeAwait(t, responses, "the undelivered grant")
		f.proxy.sever()
		commitOutcomeWaitForBackendSettlement(t, f.owner, event.pid)
		if after := censusChannelGrantHistory(t, f.estate.eng, f.estate.tenant); after != before {
			t.Fatalf("the aborted original left durable state: %+v -> %+v", before, after)
		}

		retry := f.estate.grant(t, f.estate.owner.token, f.channel, body, etag)
		if retry.status != http.StatusOK {
			t.Fatalf("the same request after an aborted original = %d: %s\nwant 200", retry.status, retry.raw)
		}
		after := censusChannelGrantHistory(t, f.estate.eng, f.estate.tenant)
		if after.grants != before.grants+1 || after.audits != before.audits+1 {
			t.Fatalf("the remedy produced %+v -> %+v, want exactly one effect", before, after)
		}
		f.assertThreeWayAttribution(t, subject)
	})

	// HC-5g3 — ABSENCE IS NOT PROOF, and this is the control that says so in
	// measurements rather than prose. While the original is unresolved, a fresh
	// read shows the subject missing; the remedy then waits on the original's lock
	// and answers 409 once it settles. A caller that treated the first read as an
	// abort would have re-sent into a committed write.
	t.Run("list_is_not_proof", func(t *testing.T) {
		f := newCommitOutcomeHTTPFixture(t, "list-is-not-proof")
		etag := f.currentETag(t)
		subject := model.NewID()
		body := commitOutcomeGrantBody(subject)

		f.proxy.arm(commitOutcomeProxyHoldCommit,
			commitOutcomeIdentifyByRelation(t, f.owner, commitOutcomeGrantRelation))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		responses := commitOutcomeServeInBackground(ctx, f.estate.eng, http.MethodPost,
			f.grantPath(), f.estate.owner.token, f.estate.tenant, body,
			map[string]string{"If-Match": etag})
		event := f.proxy.waitForEvent("commit_held")
		cancel()
		commitOutcomeAwait(t, responses, "the held grant")

		// The read that proves nothing. It is logged rather than asserted as an
		// abort, because that is exactly the inference this control forbids.
		if f.subjectPresent(t, subject) {
			t.Fatal("COULD_NOT_LOOK: the subject was already visible while its COMMIT was still held, so this schedule is not the window it needs")
		}
		t.Logf("K3_ABSENCE_NOT_PROOF|subject=%s|state=absent_while_unresolved", subject)

		// The remedy starts while the original is STILL unresolved and must wait.
		retryCtx, stopRetry := context.WithCancel(context.Background())
		defer stopRetry()
		retries := commitOutcomeServeInBackground(retryCtx, f.estate.eng, http.MethodPost,
			f.grantPath(), f.estate.owner.token, f.estate.tenant, body,
			map[string]string{"If-Match": etag})
		if !commitOutcomeWaitForRowLockWait(t, f.owner, event.pid, commitOutcomeChannelRelation) {
			t.Fatal("COULD_NOT_LOOK: the remedy never waited on the original's Channel row lock, so convergence was not exercised")
		}
		f.proxy.forward()
		retry := commitOutcomeAwait(t, retries, "the waiting remedy")
		if retry.status != http.StatusConflict {
			t.Fatalf("the remedy that waited for a committed original = %d: %s\nwant 409",
				retry.status, retry.raw)
		}
		if !f.subjectPresent(t, subject) {
			t.Fatal("the forwarded COMMIT produced no grant, so the 409 named an effect that is not there")
		}
	})
}

// commitOutcomeWaitForAdvisoryHolderWait observes ANY backend waiting on the
// named advisory key. It is used only by HC-3, where the test itself is the
// holder and the key is known.
func commitOutcomeWaitForAdvisoryHolderWait(t *testing.T, owner *sql.DB, key int64) bool {
	t.Helper()
	// Matched on the two halves for the same reason the exclusion is: pg_locks
	// stores them as unsigned oid and the key is a signed bigint, so rebuilding
	// one from the other inside SQL is wrong for every negative key.
	classID, objID := commitOutcomeAdvisoryKeyHalves(key)
	return commitOutcomePoll(context.Background(), commitOutcomeReturnLimit,
		func(callCtx context.Context) (bool, error) {
			var waiting int
			if err := owner.QueryRowContext(callCtx, `
				SELECT count(*) FROM pg_locks
				WHERE locktype = 'advisory' AND NOT granted
				  AND classid::bigint = $1 AND objid::bigint = $2`,
				classID, objID).Scan(&waiting); err != nil {
				return false, err
			}
			return waiting > 0, nil
		})
}

// commitOutcomeWaitForRowLockWait observes a backend waiting on a tuple or
// relation lock another backend holds on the named relation. It is the OCC
// routes' fence: the remedy waits for the original rather than reading around it.
func commitOutcomeWaitForRowLockWait(
	t *testing.T, owner *sql.DB, holderPID int, relation string,
) bool {
	t.Helper()
	return commitOutcomePoll(context.Background(), commitOutcomeReturnLimit,
		func(callCtx context.Context) (bool, error) {
			var waiting int
			if err := owner.QueryRowContext(callCtx, `
				SELECT count(*) FROM pg_locks waiter
				WHERE NOT waiter.granted
				  AND waiter.pid <> $1
				  AND (
				    waiter.locktype = 'transactionid'
				    OR (waiter.locktype IN ('tuple', 'relation')
				        AND waiter.relation = (SELECT oid FROM pg_class WHERE relname = $2))
				  )`, holderPID, relation).Scan(&waiting); err != nil {
				return false, err
			}
			return waiting > 0, nil
		})
}

// ---- HC-5k / HC-5u: convergence behind an unresolved original ---------------

// commitOutcomeKeyedRoute is one operation-specific remedy. prepare runs the
// setup the route needs and returns the request the control will send twice with
// the SAME key, body and precondition.
type commitOutcomeKeyedRoute struct {
	// name is the subtest name and the route's short id.
	name string
	// relation is the table this route's transaction takes a row lock on, which is
	// how the proxy attributes its COMMIT.
	relation string
	// fenceKey builds the EXACT lock name this route passes to tx.lockTransaction,
	// from the request's own intent and BEFORE the request runs.
	//
	// An earlier form claimed only `create` could be resolved here and returned an
	// unknown key for the rest. That was wrong: every input is already known in
	// the fixture — tenant, workspace, actor, idempotency key, method and path —
	// and the only product helper needed is the already-exported
	// sessions.CanonicalCursorFilter. No new accessor is required, and deriving
	// from intent is what keeps the oracle independent of the locks that appear.
	fenceKey func(t *testing.T, f *commitOutcomeKeyedFixture, request commitOutcomeKeyedRequest) commitOutcomeFenceKey
	// prepare returns the method, path, body, headers and the caller's token.
	prepare func(t *testing.T, f *commitOutcomeKeyedFixture) commitOutcomeKeyedRequest
	// replayStatus is the status a resend must answer once the original committed.
	replayStatus int
	// abortStatus is the status a resend must answer once the original aborted.
	abortStatus int
	// wantReplayed is whether the committed-original resend must report replayed.
	wantReplayed bool
}

type commitOutcomeKeyedRequest struct {
	method  string
	path    string
	token   string
	body    any
	headers map[string]string
	// effect counts the route's own durable effect for this key.
	effect func(t *testing.T) int
}

// commitOutcomeKeyedFixture is the estate the keyed routes share: one Channel,
// an owner, a recipient, a session and the direct observation channels.
type commitOutcomeKeyedFixture struct {
	estate   incomingHandoffHTTPEstate
	proxy    *commitOutcomeProxy
	owner    *sql.DB
	admin    *sql.DB
	auditKey int64
	// The audit key's two halves, kept so a fence witness can REFUSE it by value
	// instead of trusting a caller not to pass it.
	auditClassID int64
	auditObjID   int64
}

func newCommitOutcomeKeyedFixture(t *testing.T) *commitOutcomeKeyedFixture {
	t.Helper()
	backing, proxy, dsns := commitOutcomeProxiedPostgresEstate(t)
	estate := bootIncomingHandoffHTTPEstate(t, backing)
	f := &commitOutcomeKeyedFixture{
		estate: estate,
		proxy:  proxy,
		owner:  commitOutcomeDirectConn(t, dsns.Owner, "owner"),
		admin:  commitOutcomeDirectConn(t, dsns.Admin, "admin"),
	}
	f.auditKey = commitOutcomeAdvisoryKey(t, f.owner, estate.tenant.String())
	f.auditClassID, f.auditObjID = commitOutcomeAdvisoryKeyHalves(f.auditKey)
	return f
}

// receipts counts the command receipts of this tenant. One more receipt is one
// more durable effect of a keyed route, whatever projection rows it also wrote.
func (f *commitOutcomeKeyedFixture) receipts(t *testing.T) int {
	t.Helper()
	return f.countRows(t, `SELECT count(*) FROM `+commitOutcomeReceiptRelation+`
		WHERE tenant_id = $1`, f.estate.tenant.String())
}

func (f *commitOutcomeKeyedFixture) countRows(t *testing.T, query string, args ...any) int {
	t.Helper()
	var count int
	if err := f.admin.QueryRowContext(context.Background(), query, args...).Scan(&count); err != nil {
		t.Fatalf("COULD_NOT_LOOK: count the route's effect: %v", err)
	}
	return count
}

// TestCommunicationCommitOutcomeRetryHTTPPostgres is HC-5k and HC-5u: for each
// keyed route and for create, a resend of the SAME operation behind an
// unresolved original produces exactly ONE durable effect.
//
// THE POINT IS THE FENCE, not the key. Each route takes its lock BEFORE it reads
// its receipt, inside the transaction that writes the effect — so a resend waits
// for the unresolved original and then observes what it did. Schedule (b) OBSERVES
// that wait in pg_locks, on the same advisory key the original holds and
// excluding the tenant audit key, which is what makes this a measured lock-custody
// property. Mutant M11 deletes one route's tx.lockTransaction and must break
// exactly that observation, per route.
//
// Class: P-GREEN at every cut. A red at C0 is a pre-existing defect filed outside
// C32 and is never weakened to make this pass.
func TestCommunicationCommitOutcomeRetryHTTPPostgres(t *testing.T) {
	for _, route := range commitOutcomeKeyedRoutes() {
		t.Run(route.name, func(t *testing.T) {
			// (a) the original COMMITTED and its answer was lost.
			t.Run("replay_after_commit", func(t *testing.T) {
				f := newCommitOutcomeKeyedFixture(t)
				request := route.prepare(t, f)
				before := request.effect(t)

				f.proxy.arm(commitOutcomeProxyHoldAck,
					commitOutcomeIdentifyByRelation(t, f.owner, route.relation))
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				responses := commitOutcomeServeInBackground(ctx, f.estate.eng,
					request.method, request.path, request.token, f.estate.tenant,
					request.body, request.headers)
				event := f.proxy.waitForEvent("ack_held")
				cancel()
				original := commitOutcomeAwait(t, responses, route.name+" original")
				f.proxy.sever()
				commitOutcomeWaitForBackendSettlement(t, f.owner, event.pid)
				// The original's own status is NOT asserted here: HC-1a and HC-2 own
				// the wire assertion, and at C0 it is still 500. What this control
				// owns is the EFFECT.
				t.Logf("K3_KEYED_ORIGINAL|route=%s|status=%d", route.name, original.status)

				committed := request.effect(t)
				if committed != before+1 {
					t.Fatalf("the acknowledged original produced %d effects, want exactly one", committed-before)
				}
				resend := communicationHTTPTestRequest(t, f.estate.eng, request.method,
					request.path, request.token, f.estate.tenant, request.body, request.headers)
				if resend.status != route.replayStatus {
					t.Fatalf("the resend of the same operation after a committed original = %d: %s\nwant %d",
						resend.status, resend.raw, route.replayStatus)
				}
				if route.wantReplayed && !commitOutcomeReportsReplayed(resend) {
					t.Fatalf("the resend did not report itself a replay: %s", resend.raw)
				}
				if after := request.effect(t); after != committed {
					t.Fatalf("the resend produced a SECOND durable effect (%d -> %d): convergence is the whole remedy",
						committed, after)
				}
			})

			// (b) F1: the original OWNS its route fence, and the real resend is
			// genuinely serialized behind it.
			//
			// These are two separate questions and they are answered separately.
			//
			// A — OWNERSHIP. The expected lock name is derived from the request's own
			// intent BEFORE it runs, and the original transaction must hold that
			// exact advisory tuple: current database, the causally identified
			// original PID, classid/objid of the name, objsubid=1, ExclusiveLock,
			// granted. objsubid=1 comes from the one-bigint call at
			// sqlstore/scope.go:120-135, so a two-int namespace lock cannot pass; the
			// database and PID stop an unrelated backend standing in; and because the
			// name came from intent, a tenant-audit, message or cursor-identity lock
			// is simply a different key. Per-route M11 deletes only that route's
			// tx.lockTransaction and must fail THIS assertion while the original still
			// reaches the same held-COMMIT state.
			//
			// B — REAL RECOVERY. The same-intent resend is sent for real, its backend
			// is identified from its own announced pid, and PostgreSQL itself is asked
			// who blocks it. No synthetic contender is created and no authority is
			// bypassed. Whether the blocker is the earlier authority row lock or the
			// advisory fence, it must be the ORIGINAL, and the response must still be
			// pending while that is true.
			//
			// NI, recorded rather than hidden: every route merges bound request and
			// claim facts before its fence (communication_tx.go:698-699,747-753), so
			// deleting the later advisory lock need not change what blocks THIS
			// resend. That causal question is non-identifiable under redundant earlier
			// serialization. It is not a green M11 — M11 is killed by A — and it is
			// not a reason to weaken B.
			t.Run("original_holds_route_fence_and_resend_recovers", func(t *testing.T) {
				f := newCommitOutcomeKeyedFixture(t)
				request := route.prepare(t, f)
				before := request.effect(t)

				// Derived from intent, BEFORE the request runs.
				want := route.fenceKey(t, f, request)

				f.proxy.arm(commitOutcomeProxyHoldCommit,
					commitOutcomeIdentifyByRelation(t, f.owner, route.relation))
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				responses := commitOutcomeServeInBackground(ctx, f.estate.eng,
					request.method, request.path, request.token, f.estate.tenant,
					request.body, request.headers)
				event := f.proxy.waitForEvent("commit_held")
				cancel()
				commitOutcomeAwait(t, responses, route.name+" held original")

				// --- A: exact ownership -------------------------------------
				commitOutcomeAssertExactHolder(t, f.owner, event.pid, want)
				t.Logf("K3_FENCE_OWNED|route=%s|name=%s|pid=%d|key=(%d,%d,1)",
					route.name, want.name, event.pid, want.classID, want.objID)

				// --- B: the real resend, and what actually blocks it ---------
				resendCtx, stopResend := context.WithCancel(context.Background())
				defer stopResend()
				resends := commitOutcomeServeInBackground(resendCtx, f.estate.eng,
					request.method, request.path, request.token, f.estate.tenant,
					request.body, request.headers)

				resendPID := commitOutcomeAwaitDistinctBackend(t, f.owner, f.proxy, event.pid, route.name)
				blockers := commitOutcomeBlockingPIDs(t, f.owner, resendPID)
				if len(blockers) == 0 {
					t.Fatalf("COULD_NOT_LOOK: PostgreSQL reports nothing blocking the %s resend (backend %d), so this schedule never serialized behind the original",
						route.name, resendPID)
				}
				if !commitOutcomeContainsPID(blockers, event.pid) {
					t.Fatalf("the %s resend (backend %d) is blocked by %v, which does not include the original backend %d: it is waiting on something other than the transaction under test",
						route.name, resendPID, blockers, event.pid)
				}
				select {
				case early := <-resends:
					t.Fatalf("the %s resend answered %d while the original was still held and blocked: it never serialized",
						route.name, early.status)
				default:
				}
				t.Logf("K3_RESEND_BLOCKED|route=%s|resend_pid=%d|blockers=%v|original=%d",
					route.name, resendPID, blockers, event.pid)

				// Settle the original; the resend must now converge.
				f.proxy.forward()
				resend := commitOutcomeAwait(t, resends, route.name+" waiting resend")
				if resend.status != route.replayStatus {
					t.Fatalf("the resend that waited for a committed original = %d: %s\nwant %d",
						resend.status, resend.raw, route.replayStatus)
				}
				if route.wantReplayed && !commitOutcomeReportsReplayed(resend) {
					t.Fatalf("the %s resend did not report itself a replay: %s", route.name, resend.raw)
				}
				if after := request.effect(t); after != before+1 {
					t.Fatalf("the pair produced %d effects, want exactly one", after-before)
				}
			})

			// (b') the original was SEVERED before its COMMIT reached the server, so
			// the resend is a first attempt and answers as one.
			t.Run("resend_after_severed_original", func(t *testing.T) {
				f := newCommitOutcomeKeyedFixture(t)
				request := route.prepare(t, f)
				before := request.effect(t)

				f.proxy.arm(commitOutcomeProxyHoldCommit,
					commitOutcomeIdentifyByRelation(t, f.owner, route.relation))
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				responses := commitOutcomeServeInBackground(ctx, f.estate.eng,
					request.method, request.path, request.token, f.estate.tenant,
					request.body, request.headers)
				event := f.proxy.waitForEvent("commit_held")
				cancel()
				commitOutcomeAwait(t, responses, route.name+" severed original")
				f.proxy.sever()
				commitOutcomeWaitForBackendSettlement(t, f.owner, event.pid)
				if mid := request.effect(t); mid != before {
					t.Fatalf("the severed original left %d effects, want none", mid-before)
				}

				// THE PERMIT MUST ALREADY BE SPENT. Severing the first transaction
				// ends the proxy's ownership of it, and that must not re-open
				// interception: if it did, this recovery positive would be measuring a
				// SECOND hold nobody armed, and the product would never have been
				// asked to recover at all.
				if f.proxy.isArmed() {
					t.Fatalf("the interception permit is still outstanding after the %s original was severed: the resend below would be intercepted rather than served",
						route.name)
				}

				resend := communicationHTTPTestRequest(t, f.estate.eng, request.method,
					request.path, request.token, f.estate.tenant, request.body, request.headers)
				if resend.status != route.abortStatus {
					t.Fatalf("the resend after an aborted original = %d: %s\nwant %d",
						resend.status, resend.raw, route.abortStatus)
				}
				if after := request.effect(t); after != before+1 {
					t.Fatalf("the resend produced %d effects, want exactly one", after-before)
				}
				// Exactly ONE interception happened in this window. The resend's own
				// COMMIT matched the same identify predicate on the same route, so a
				// permit that was not consumed would show up here as two.
				if taken := f.proxy.armConsumptions(); taken != 1 {
					t.Fatalf("%d interception permits were consumed for %s, want exactly 1: the recovery positive must exercise the product, not a second hold",
						taken, route.name)
				}
			})
		})
	}
}

// commitOutcomeReportsReplayed reads the replayed flag every keyed route returns.
func commitOutcomeReportsReplayed(response communicationHTTPTestResponse) bool {
	var body struct {
		Replayed bool `json:"replayed"`
	}
	if err := json.Unmarshal(response.raw, &body); err != nil {
		return false
	}
	return body.Replayed
}

// TestCommunicationCommitOutcomeKeyReuseHTTPPostgres is HC-5k-reuse: the same key
// with a DIFFERENT body is refused. Convergence must not become "the key wins":
// an idempotency key binds one operation, and rebinding it is a conflict with no
// effect.
func TestCommunicationCommitOutcomeKeyReuseHTTPPostgres(t *testing.T) {
	f := newCommitOutcomeKeyedFixture(t)
	request := commitOutcomeSendRoute().prepare(t, f)
	before := request.effect(t)

	first := communicationHTTPTestRequest(t, f.estate.eng, request.method, request.path,
		request.token, f.estate.tenant, request.body, request.headers)
	if first.status != http.StatusCreated && first.status != http.StatusOK {
		t.Fatalf("the first send = %d: %s", first.status, first.raw)
	}
	changed, ok := request.body.(map[string]any)
	if !ok {
		t.Fatalf("COULD_NOT_LOOK: the send body is %T, not a JSON object", request.body)
	}
	rebound := map[string]any{}
	for key, value := range changed {
		rebound[key] = value
	}
	rebound["content"] = map[string]any{
		"subject": "rebound", "blocks": []map[string]any{{"type": "text", "format": "plain", "text": "a different body"}},
	}
	reuse := communicationHTTPTestRequest(t, f.estate.eng, request.method, request.path,
		request.token, f.estate.tenant, rebound, request.headers)
	if reuse.status != http.StatusConflict {
		t.Fatalf("the same key with a different body = %d: %s\nwant 409 — a key binds ONE operation",
			reuse.status, reuse.raw)
	}
	if after := request.effect(t); after != before+1 {
		t.Fatalf("the refused rebind produced %d effects, want exactly one (the first send)", after-before)
	}
}

// commitOutcomeKeyedRoutes is the six fenced routes of R2/M11: the five keyed
// operations and create.
func commitOutcomeKeyedRoutes() []commitOutcomeKeyedRoute {
	return []commitOutcomeKeyedRoute{
		commitOutcomeSendRoute(),
		commitOutcomeAckRoute(),
		commitOutcomeCursorRoute(),
		commitOutcomeOfferRoute(),
		commitOutcomeResponseRoute(),
		commitOutcomeCreateRoute(),
	}
}

func commitOutcomeSendRoute() commitOutcomeKeyedRoute {
	return commitOutcomeKeyedRoute{
		name: "send", relation: commitOutcomeReceiptRelation,
		fenceKey: func(t *testing.T, f *commitOutcomeKeyedFixture, request commitOutcomeKeyedRequest) commitOutcomeFenceKey {
			t.Helper()
			// tenant | hex(actor fingerprint) | hex(idempotency hash). The sender is
			// the estate owner, which is the token this route sends with.
			actor := commitOutcomeActorFingerprint(t, "user", f.estate.owner.id.String())
			key := commitOutcomeKeyHash(request.headers["Idempotency-Key"])
			return commitOutcomeNamedFenceKey(t, f.owner, fmt.Sprintf(
				"sessions_communication_command|%s|%x|%x", f.estate.tenant, actor, key))
		},
		replayStatus: http.StatusOK, abortStatus: http.StatusCreated, wantReplayed: true,
		prepare: func(t *testing.T, f *commitOutcomeKeyedFixture) commitOutcomeKeyedRequest {
			t.Helper()
			key := model.NewID().String()
			return commitOutcomeKeyedRequest{
				method: http.MethodPost, path: "/v1/m/sessions/messages/send",
				token: f.estate.owner.token,
				body: map[string]any{
					"channel_id": f.estate.channelID,
					"recipient": map[string]any{
						"kind": "user", "ref": f.estate.recipient.id.String(),
					},
					"content": map[string]any{
						"subject": "commit-outcome-send-" + key,
						"blocks":  []map[string]any{{"type": "text", "format": "plain", "text": "commit outcome " + key}},
					},
				},
				headers: map[string]string{"Idempotency-Key": key},
				effect:  f.receipts,
			}
		},
	}
}

func commitOutcomeAckRoute() commitOutcomeKeyedRoute {
	return commitOutcomeKeyedRoute{
		name: "ack", relation: commitOutcomeReceiptRelation,
		fenceKey: func(t *testing.T, f *commitOutcomeKeyedFixture, request commitOutcomeKeyedRequest) commitOutcomeFenceKey {
			t.Helper()
			// The acknowledging principal is the RECIPIENT, which is the token this
			// route sends with; the scope is the ack method and path.
			actor := commitOutcomeActorFingerprint(t, "user", f.estate.recipient.id.String())
			scope := commitOutcomeCommandScope(request.method, request.path, f.estate.workspace)
			return commitOutcomeNamedFenceKey(t, f.owner, commitOutcomeIdemDigestName(
				"sessions:communication:ack:idem:", f.estate.tenant, actor, scope,
				commitOutcomeKeyHash(request.headers["Idempotency-Key"])))
		},
		replayStatus: http.StatusOK, abortStatus: http.StatusOK, wantReplayed: true,
		prepare: func(t *testing.T, f *commitOutcomeKeyedFixture) commitOutcomeKeyedRequest {
			t.Helper()
			delivery := commitOutcomeSendOneDelivery(t, f)
			key := model.NewID().String()
			return commitOutcomeKeyedRequest{
				method: http.MethodPost,
				path:   "/v1/m/sessions/deliveries/" + delivery.id.String() + "/ack",
				token:  f.estate.recipient.token,
				headers: map[string]string{
					"If-Match": delivery.ifMatch, "Idempotency-Key": key,
				},
				effect: f.receipts,
			}
		},
	}
}

func commitOutcomeCursorRoute() commitOutcomeKeyedRoute {
	return commitOutcomeKeyedRoute{
		name: "cursor", relation: commitOutcomeReceiptRelation,
		fenceKey: func(t *testing.T, f *commitOutcomeKeyedFixture, request commitOutcomeKeyedRequest) commitOutcomeFenceKey {
			t.Helper()
			// Same digest shape as ack, with the fixed personal DirectNotice filter
			// hash appended to the scope by the product.
			actor := commitOutcomeActorFingerprint(t, "user", f.estate.recipient.id.String())
			scope := commitOutcomeCommandScope(request.method, request.path, f.estate.workspace) +
				";filter=" + base64.RawURLEncoding.EncodeToString(commitOutcomeCursorFilterHash(t))
			return commitOutcomeNamedFenceKey(t, f.owner, commitOutcomeIdemDigestName(
				"sessions:communication:cursor:idem:", f.estate.tenant, actor, scope,
				commitOutcomeKeyHash(request.headers["Idempotency-Key"])))
		},
		replayStatus: http.StatusOK, abortStatus: http.StatusOK, wantReplayed: true,
		prepare: func(t *testing.T, f *commitOutcomeKeyedFixture) commitOutcomeKeyedRequest {
			t.Helper()
			delivery := commitOutcomeSendOneDelivery(t, f)
			inbox := communicationHTTPTestRequest(t, f.estate.eng, http.MethodGet,
				communicationHTTPTestInboxPath(f.estate.workspace, 10, ""),
				f.estate.recipient.token, f.estate.tenant, nil, nil)
			if inbox.status != http.StatusOK {
				t.Fatalf("COULD_NOT_LOOK: the recipient inbox = %d: %s", inbox.status, inbox.raw)
			}
			page := communicationHTTPTestDecode[sessions.DirectNoticeInboxPage](t, inbox)
			cursorGet := communicationHTTPTestRequest(t, f.estate.eng, http.MethodGet,
				communicationHTTPTestCursorPath(
					f.estate.recipient.id.String(), f.estate.workspace, page.CursorTarget),
				f.estate.recipient.token, f.estate.tenant, nil, nil)
			if cursorGet.status != http.StatusOK {
				t.Fatalf("COULD_NOT_LOOK: the cursor token = %d: %s", cursorGet.status, cursorGet.raw)
			}
			cursor := communicationHTTPTestDecode[sessions.DirectNoticeCursorTokenResult](t, cursorGet)
			key := model.NewID().String()
			return commitOutcomeKeyedRequest{
				method: http.MethodPut,
				path:   "/v1/m/sessions/inbox/cursors/personal/" + f.estate.recipient.id.String(),
				token:  f.estate.recipient.token,
				body: map[string]any{
					"cursor": cursor.CursorToken, "delivery_id": delivery.id,
				},
				headers: map[string]string{
					"If-Match": cursor.ETag, "Idempotency-Key": key,
				},
				effect: f.receipts,
			}
		},
	}
}

func commitOutcomeOfferRoute() commitOutcomeKeyedRoute {
	return commitOutcomeKeyedRoute{
		name: "offer", relation: commitOutcomeReceiptRelation,
		fenceKey: func(t *testing.T, f *commitOutcomeKeyedFixture, request commitOutcomeKeyedRequest) commitOutcomeFenceKey {
			t.Helper()
			// The handoff key carries NO actor fingerprint: workspace, command scope
			// and key hash only.
			scope := commitOutcomeCommandScope(request.method, request.path, f.estate.workspace)
			return commitOutcomeNamedFenceKey(t, f.owner, fmt.Sprintf(
				"sessions.communication.handoff.idempotency/%s/%s/%x",
				f.estate.workspace, scope,
				commitOutcomeKeyHash(request.headers["Idempotency-Key"])))
		},
		replayStatus: http.StatusOK, abortStatus: http.StatusCreated, wantReplayed: true,
		prepare: func(t *testing.T, f *commitOutcomeKeyedFixture) commitOutcomeKeyedRequest {
			t.Helper()
			source := createCommunicationHTTPTestSession(t, f.estate.eng, f.estate.tenant,
				f.estate.workspace, "commit-outcome-offer-"+model.NewID().String())
			f.estate.grantChannel(t,
				map[string]any{"kind": "session", "ref": source.sid}, "commit outcome offering session")
			work, leased := createCommunicationHTTPSessionOwnedWork(
				t, f.estate.eng, f.estate.owner, f.estate.tenant, source, "Commit outcome offer")
			key := model.NewID().String()
			summary := "commit-outcome-offer-" + key
			return commitOutcomeKeyedRequest{
				method: http.MethodPost, path: "/v1/m/sessions/handoffs",
				token: source.communication.Token,
				body: map[string]any{
					"channel_id": f.estate.channelID, "work_item_id": work.ResultID,
					"recipient": map[string]any{
						"kind": "user", "ref": f.estate.recipient.id.String(),
					},
					"handoff": map[string]any{
						"summary": summary, "next_action": "Continue " + summary,
					},
					"ack_deadline":         time.Now().UTC().Add(5 * time.Minute),
					"expected_owner_epoch": 1,
				},
				headers: map[string]string{
					"If-Match":        fmt.Sprintf(`"v%d"`, leased.Version),
					"Idempotency-Key": key,
				},
				effect: f.receipts,
			}
		},
	}
}

func commitOutcomeResponseRoute() commitOutcomeKeyedRoute {
	return commitOutcomeKeyedRoute{
		name: "response", relation: commitOutcomeReceiptRelation,
		fenceKey: func(t *testing.T, f *commitOutcomeKeyedFixture, request commitOutcomeKeyedRequest) commitOutcomeFenceKey {
			t.Helper()
			// Same owner and shape as offer, with the response path.
			scope := commitOutcomeCommandScope(request.method, request.path, f.estate.workspace)
			return commitOutcomeNamedFenceKey(t, f.owner, fmt.Sprintf(
				"sessions.communication.handoff.idempotency/%s/%s/%x",
				f.estate.workspace, scope,
				commitOutcomeKeyHash(request.headers["Idempotency-Key"])))
		},
		replayStatus: http.StatusOK, abortStatus: http.StatusOK, wantReplayed: true,
		prepare: func(t *testing.T, f *commitOutcomeKeyedFixture) commitOutcomeKeyedRequest {
			t.Helper()
			offered := f.estate.offer(t,
				map[string]any{"kind": "user", "ref": f.estate.recipient.id.String()},
				"commit-outcome-response-"+model.NewID().String(), 5*time.Minute)
			key := model.NewID().String()
			return commitOutcomeKeyedRequest{
				method: http.MethodPost,
				path:   "/v1/m/sessions/handoffs/" + offered.HandoffID.String() + "/responses",
				token:  f.estate.recipient.token,
				body: map[string]any{"transition": "reject", "reason": map[string]any{
					"code": "not_mine", "text": "The recipient declines the transfer",
				}},
				headers: map[string]string{
					"If-Match": offered.ETag, "Idempotency-Key": key,
				},
				effect: f.receipts,
			}
		},
	}
}

func commitOutcomeCreateRoute() commitOutcomeKeyedRoute {
	return commitOutcomeKeyedRoute{
		// Create has no command key: its fence is the slug lock and its effect is
		// the Channel row, so it is counted by slug rather than by receipt.
		name: "create", relation: commitOutcomeChannelRelation,
		// CORRECTION, recorded where it was wrong. An earlier record claimed create
		// reaches its fence as the first contended lock because
		// lockAuthoritySnapshot is called with nil refs
		// (communication_channel_service.go:285). That nil is only the LOCAL fact
		// list: communication_tx.go:698-699,747-753 merges request and claim facts
		// before LockAuthoritySnapshot, and communication_authority.go:611-650
		// requires directory and authorization epochs. Create may therefore hold
		// the same earlier epoch rows as every other route and is NOT exempt, so it
		// gets no special M11 treatment.
		fenceKey: func(t *testing.T, f *commitOutcomeKeyedFixture, request commitOutcomeKeyedRequest) commitOutcomeFenceKey {
			t.Helper()
			body, ok := request.body.(map[string]any)
			if !ok {
				t.Fatalf("COULD_NOT_LOOK: the create body is %T, not a JSON object", request.body)
			}
			slug, ok := body["slug"].(string)
			if !ok {
				t.Fatalf("COULD_NOT_LOOK: the create body carries no slug to key the fence on")
			}
			return commitOutcomeNamedFenceKey(t, f.owner, fmt.Sprintf(
				"sessions_channel_create|%s|%s|%s",
				f.estate.tenant, f.estate.workspace, slug))
		},
		replayStatus: http.StatusConflict, abortStatus: http.StatusCreated, wantReplayed: false,
		prepare: func(t *testing.T, f *commitOutcomeKeyedFixture) commitOutcomeKeyedRequest {
			t.Helper()
			slug := "commit-outcome-create-" + model.NewID().String()
			return commitOutcomeKeyedRequest{
				method: http.MethodPost, path: "/v1/m/sessions/channels",
				token: f.estate.owner.token,
				body: map[string]any{
					"workspace_id": f.estate.workspace.String(), "slug": slug,
					"name": "Channel " + slug,
					"initial_grants": []map[string]any{channelAdministrationGrant(
						channelAdministrationSubject("user", f.estate.owner.id), true, true, true)},
				},
				effect: func(t *testing.T) int {
					return f.countRows(t, `
						SELECT count(*) FROM `+commitOutcomeChannelRelation+`
						WHERE tenant_id = $1 AND slug = $2`,
						f.estate.tenant.String(), slug)
				},
			}
		},
	}
}

// commitOutcomeDelivery is one delivered notice, with the exact If-Match its Ack
// needs. The precondition is carried as the served string rather than a parsed
// number, so no control depends on the version's Go type.
type commitOutcomeDelivery struct {
	id      model.ID
	ifMatch string
}

// commitOutcomeSendOneDelivery sends one notice to the estate's recipient and
// opens it, which is what yields the Delivery version the Ack precondition needs.
func commitOutcomeSendOneDelivery(t *testing.T, f *commitOutcomeKeyedFixture) commitOutcomeDelivery {
	t.Helper()
	key := model.NewID().String()
	sent := communicationHTTPTestRequest(t, f.estate.eng, http.MethodPost,
		"/v1/m/sessions/messages/send", f.estate.owner.token, f.estate.tenant,
		map[string]any{
			"channel_id": f.estate.channelID,
			"recipient": map[string]any{
				"kind": "user", "ref": f.estate.recipient.id.String(),
			},
			"content": map[string]any{
				"subject": "commit-outcome-seed-" + key,
				"blocks":  []map[string]any{{"type": "text", "format": "plain", "text": "seed " + key}},
			},
		}, map[string]string{"Idempotency-Key": key})
	if sent.status != http.StatusCreated {
		t.Fatalf("COULD_NOT_LOOK: seed the delivery = %d: %s", sent.status, sent.raw)
	}
	published := communicationHTTPTestDecode[sessions.DirectNoticePublishResult](t, sent)
	read := communicationHTTPTestRequest(t, f.estate.eng, http.MethodGet,
		"/v1/m/sessions/deliveries/"+published.DeliveryID.String(),
		f.estate.recipient.token, f.estate.tenant, nil, nil)
	if read.status != http.StatusOK {
		t.Fatalf("COULD_NOT_LOOK: open the seeded delivery = %d: %s", read.status, read.raw)
	}
	opened := communicationHTTPTestDecode[sessions.DirectNoticeReadResult](t, read)
	return commitOutcomeDelivery{
		id:      published.DeliveryID,
		ifMatch: fmt.Sprintf(`"v%d"`, opened.Delivery.Version),
	}
}

// ---- S1/S2: the proxy's own release and arm transitions --------------------
//
// These controls need no database. They speak protocol 3.0 to the proxy over
// loopback with a minimal responder on the other side, which is what makes them
// DETERMINISTIC: the order of CommandComplete, ReadyForQuery and release is
// chosen by the control rather than raced for. A defect in the instrument that
// only shows up under a real server's timing is a defect that gets blamed on the
// product.

// commitOutcomeFrame builds one typed protocol message.
func commitOutcomeFrame(kind byte, payload []byte) []byte {
	out := make([]byte, 5+len(payload))
	out[0] = kind
	binary.BigEndian.PutUint32(out[1:], uint32(4+len(payload)))
	copy(out[5:], payload)
	return out
}

func commitOutcomeStringPayload(s string) []byte { return append([]byte(s), 0) }

// commitOutcomeFakeServer is the smallest backend that can complete a startup,
// name a backend pid and answer COMMIT. It sends ReadyForQuery only when the
// control releases it, so both arrival orders are reachable on purpose.
type commitOutcomeFakeServer struct {
	listener net.Listener
	pid      int
	// sendReady gates the ReadyForQuery that follows each CommandComplete.
	sendReady chan struct{}
	// commits counts the COMMITs that actually reached this server.
	commits chan string
	wg      sync.WaitGroup

	// THE INSTRUMENT OWNS ITS OWN SHUTDOWN. A fake session that parks on an
	// uncancelable permit receive cannot be joined, so a control that fails
	// BEFORE it grants the permit would strand cleanup and the run would end in a
	// hang rather than the failure the control actually found.
	ctx    context.Context
	cancel context.CancelFunc

	connMu sync.Mutex
	conns  []net.Conn
	closed bool
}

func newCommitOutcomeFakeServer(t *testing.T, pid int) *commitOutcomeFakeServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("COULD_NOT_LOOK: listen for the fake backend: %v", err)
	}
	serverCtx, serverCancel := context.WithCancel(context.Background())
	server := &commitOutcomeFakeServer{
		listener: listener, pid: pid,
		sendReady: make(chan struct{}, 8),
		commits:   make(chan string, 8),
		ctx:       serverCtx, cancel: serverCancel,
	}
	server.wg.Add(1)
	go server.serve()
	t.Cleanup(server.close)
	return server
}

// close cancels, drops every accepted socket, and only then joins. The order is
// the point: joining first is how one parked session strands the whole run.
func (s *commitOutcomeFakeServer) close() {
	s.connMu.Lock()
	if s.closed {
		s.connMu.Unlock()
		return
	}
	s.closed = true
	conns := s.conns
	s.conns = nil
	s.connMu.Unlock()

	s.cancel()
	_ = s.listener.Close()
	for _, c := range conns {
		_ = c.Close()
	}
	s.wg.Wait()
}

func (s *commitOutcomeFakeServer) track(conn net.Conn) bool {
	s.connMu.Lock()
	defer s.connMu.Unlock()
	if s.closed {
		return false
	}
	s.conns = append(s.conns, conn)
	return true
}

// awaitReady waits for the control's permit OR for shutdown, so no session can
// park forever on a permit that will never come.
func (s *commitOutcomeFakeServer) awaitReady() bool {
	select {
	case <-s.sendReady:
		return true
	case <-s.ctx.Done():
		return false
	}
}

func (s *commitOutcomeFakeServer) dsn() string {
	return "postgres://u:p@" + s.listener.Addr().String() + "/db?sslmode=disable"
}

func (s *commitOutcomeFakeServer) serve() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		if !s.track(conn) {
			_ = conn.Close()
			return
		}
		s.wg.Add(1)
		go s.session(conn)
	}
}

func (s *commitOutcomeFakeServer) session(conn net.Conn) {
	defer s.wg.Done()
	defer func() { _ = conn.Close() }()

	// Startup: length-prefixed and untyped.
	var header [4]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return
	}
	length := int(binary.BigEndian.Uint32(header[:]))
	if length < 8 || length > commitOutcomeProxyBufferLimit {
		return
	}
	if _, err := io.ReadFull(conn, make([]byte, length-4)); err != nil {
		return
	}
	key := make([]byte, 8)
	binary.BigEndian.PutUint32(key, uint32(s.pid))
	binary.BigEndian.PutUint32(key[4:], 424242)
	opening := append([]byte(nil), commitOutcomeFrame('R', []byte{0, 0, 0, 0})...)
	opening = append(opening, commitOutcomeFrame('K', key)...)
	opening = append(opening, commitOutcomeFrame('Z', []byte{'I'})...)
	if _, err := conn.Write(opening); err != nil {
		return
	}
	for {
		frame, kind, payload, err := commitOutcomeReadTypedFrame(conn)
		if err != nil {
			return
		}
		_ = frame
		if kind != 'Q' {
			continue
		}
		text := strings.TrimRight(string(payload), "\x00")
		select {
		case s.commits <- text:
		default:
		}
		tag := "SELECT 1"
		if commitOutcomeIsCommitQuery(payload) {
			tag = "COMMIT"
		}
		if _, err := conn.Write(commitOutcomeFrame('C', commitOutcomeStringPayload(tag))); err != nil {
			return
		}
		// ReadyForQuery is deliberately separate: the control decides whether it
		// arrives before, during or after the release.
		if !s.awaitReady() {
			return
		}
		if _, err := conn.Write(commitOutcomeFrame('Z', []byte{'I'})); err != nil {
			return
		}
	}
}

// commitOutcomeProxyClient is the driver side: it speaks just enough to open a
// connection through the proxy and read whole frames back.
type commitOutcomeProxyClient struct {
	conn net.Conn
}

func newCommitOutcomeProxyClient(t *testing.T, addr string) *commitOutcomeProxyClient {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, commitOutcomeProxyWriteLimit)
	if err != nil {
		t.Fatalf("COULD_NOT_LOOK: dial the proxy: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	startup := make([]byte, 0, 32)
	body := make([]byte, 4)
	binary.BigEndian.PutUint32(body, commitOutcomeProxyStartupMagic)
	body = append(body, commitOutcomeStringPayload("user")...)
	body = append(body, commitOutcomeStringPayload("test")...)
	body = append(body, 0)
	length := make([]byte, 4)
	binary.BigEndian.PutUint32(length, uint32(4+len(body)))
	startup = append(startup, length...)
	startup = append(startup, body...)
	if _, err := conn.Write(startup); err != nil {
		t.Fatalf("COULD_NOT_LOOK: send the startup message: %v", err)
	}
	return &commitOutcomeProxyClient{conn: conn}
}

func (c *commitOutcomeProxyClient) query(t *testing.T, text string) {
	t.Helper()
	if _, err := c.conn.Write(commitOutcomeFrame('Q', commitOutcomeStringPayload(text))); err != nil {
		t.Fatalf("COULD_NOT_LOOK: send %q: %v", text, err)
	}
}

// expectKinds reads exactly len(want) frames and returns the type bytes it saw,
// bounded so a swallowed frame is a named inability rather than a hung test.
func (c *commitOutcomeProxyClient) expectKinds(t *testing.T, want string) string {
	t.Helper()
	if err := c.conn.SetReadDeadline(time.Now().Add(commitOutcomeProxyWriteLimit)); err != nil {
		t.Fatalf("COULD_NOT_LOOK: bound the client read: %v", err)
	}
	var got []byte
	for range want {
		_, kind, _, err := commitOutcomeReadTypedFrame(c.conn)
		if err != nil {
			t.Fatalf("COULD_NOT_LOOK: after %q the proxy delivered nothing more (want %q): %v",
				string(got), want, err)
		}
		got = append(got, kind)
	}
	return string(got)
}

// commitOutcomeArmedProxy wires one fake backend to one proxy and one client.
func commitOutcomeArmedProxy(
	t *testing.T, mode commitOutcomeProxyMode,
) (*commitOutcomeProxy, *commitOutcomeFakeServer, *commitOutcomeProxyClient) {
	t.Helper()
	server := newCommitOutcomeFakeServer(t, 4242)
	proxy, redirected := newCommitOutcomeProxy(t, server.dsn())
	address := strings.TrimPrefix(redirected, "postgres://u:p@")
	address = strings.TrimSuffix(address, "/db?sslmode=disable")
	proxy.arm(mode, func(context.Context, int) bool { return true })
	client := newCommitOutcomeProxyClient(t, address)
	// The opening exchange is written by the startup path in one piece and
	// consumes NO permit. The earlier form handed one over here, so the first
	// query could take it and answer ReadyForQuery without the schedule the
	// control believed it had imposed — the permit was a spare, and a spare permit
	// is how an arrival-order control stops discriminating anything.
	if kinds := client.expectKinds(t, "RKZ"); kinds != "RKZ" {
		t.Fatalf("opening exchange = %q, want RKZ", kinds)
	}
	return proxy, server, client
}

// commitOutcomeAwaitBufferedFrames is the ACKNOWLEDGMENT that the proxy really
// withheld a frame. A control that merely sent a permit and moved on would be
// asserting its own ordering rather than the instrument's observed state.
func commitOutcomeAwaitBufferedFrames(t *testing.T, proxy *commitOutcomeProxy, want int) {
	t.Helper()
	session := proxy.heldSession()
	if session == nil {
		t.Fatal("COULD_NOT_LOOK: no session owns the held answer, so nothing can be buffered")
	}
	deadline := time.Now().Add(commitOutcomeProxyWriteLimit)
	for time.Now().Before(deadline) {
		if session.bufferedFrames() >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("COULD_NOT_LOOK: the proxy buffered %d frames within %s, want at least %d; the arrival this control depends on never happened",
		session.bufferedFrames(), commitOutcomeProxyWriteLimit, want)
}

// TestCommitOutcomeProxyReleaseDrainsInWireOrder is S1.
//
// The released answer must arrive COMPLETE, IN ORDER, and leave a usable
// connection. Three schedules are forced, because the two defects this replaces
// each lived in a different one:
//
//   - AFTER: ReadyForQuery arrives once the release has finished. The first
//     defect swallowed it — the session was still buffering into a hold that had
//     already been cleared — so a correct product could hang the positive that
//     proves it recovered.
//   - BEFORE: ReadyForQuery is buffered while the answer is held, ACKNOWLEDGED as
//     buffered, and then drained. Acknowledging it is the point: without that the
//     control would be asserting its own send order rather than the proxy's
//     observed state.
//   - DURING: the release's lock ownership is observed before ReadyForQuery is
//     admitted. Arrival, attempted write and completed write are distinct events.
//     When the release owns the lock, the control waits only for the attempt;
//     when the release-wide lock is deleted, it waits for the completed Z write
//     before allowing C to drain. Neither path guesses from a pre-lock counter.
//
// Causal mutant: remove the release-wide output lock, so the transition no longer
// holds writeMu across the state change. The DURING case must then fail on the
// ORDER assertion — the relay writes Z first — rather than hang.
func TestCommitOutcomeProxyReleaseDrainsInWireOrder(t *testing.T) {
	t.Run("ready_for_query_after_release", func(t *testing.T) {
		proxy, server, client := commitOutcomeArmedProxy(t, commitOutcomeProxyHoldAck)
		client.query(t, "commit")
		event := proxy.waitForEvent("ack_held")
		if !strings.EqualFold(event.tag, "COMMIT") {
			t.Fatalf("held tag = %q, want COMMIT", event.tag)
		}
		// Release FIRST, then let the backend send ReadyForQuery.
		proxy.release()
		server.sendReady <- struct{}{}
		if kinds := client.expectKinds(t, "CZ"); kinds != "CZ" {
			t.Fatalf("delivered %q, want CZ: the completion that arrived after the release was swallowed", kinds)
		}
	})

	t.Run("ready_for_query_before_release", func(t *testing.T) {
		proxy, server, client := commitOutcomeArmedProxy(t, commitOutcomeProxyHoldAck)
		client.query(t, "commit")
		proxy.waitForEvent("ack_held")
		// Let ReadyForQuery arrive while the answer is held, and REQUIRE the proxy
		// to have buffered both frames before releasing.
		server.sendReady <- struct{}{}
		commitOutcomeAwaitBufferedFrames(t, proxy, 2)
		proxy.release()
		if kinds := client.expectKinds(t, "CZ"); kinds != "CZ" {
			t.Fatalf("delivered %q, want CZ in wire order", kinds)
		}
	})

	t.Run("ready_for_query_during_release", func(t *testing.T) {
		proxy, server, client := commitOutcomeArmedProxy(t, commitOutcomeProxyHoldAck)
		client.query(t, "commit")
		proxy.waitForEvent("ack_held")
		session := proxy.heldSession()
		if session == nil {
			t.Fatal("COULD_NOT_LOOK: no session owns the held answer")
		}
		arrived := make(chan error, 1)
		attempted := make(chan error, 1)
		written := make(chan error, 1)
		// The callbacks never block a relay. This fixture admits one Z and sends
		// no other query until the controlled release has completed.
		emit := func(events chan<- error, err error) {
			select {
			case events <- err:
			default:
			}
		}
		session.mu.Lock()
		session.onRelayFrame = func(kind byte) {
			if kind == 'Z' {
				emit(arrived, nil)
			}
		}
		session.onRelayWriteAttempt = func() { emit(attempted, nil) }
		session.onRelayWriteComplete = func(err error) { emit(written, err) }
		session.onDrainLocked = func() {
			deadline := time.Now().Add(commitOutcomeProxyWriteLimit)
			await := func(events <-chan error, phase string) {
				t.Helper()
				remaining := time.Until(deadline)
				if remaining <= 0 {
					t.Fatalf("COULD_NOT_LOOK: release schedule expired before ReadyForQuery %s", phase)
				}
				timer := time.NewTimer(remaining)
				defer timer.Stop()
				select {
				case err := <-events:
					if err != nil {
						t.Fatalf("COULD_NOT_LOOK: ReadyForQuery %s failed: %v", phase, err)
					}
				case <-timer.C:
					t.Fatalf("COULD_NOT_LOOK: ReadyForQuery %s was not observed within the release schedule", phase)
				}
			}
			// Observe BEFORE admitting Z: no relay can own writeMu here because
			// startup has completed and the backend is still awaiting its permit.
			// TryLock therefore distinguishes the real release-wide lock from its
			// deletion without waiting on a writer that the correct code blocks.
			canWrite := session.writeMu.TryLock()
			if canWrite {
				session.writeMu.Unlock()
			}
			server.sendReady <- struct{}{}
			await(arrived, "arrival")
			await(attempted, "write attempt")
			if canWrite {
				// The missing-lock mutant must finish Z before the hook returns
				// and C drains. An attempt alone cannot establish that order.
				await(written, "completed write")
			}
		}
		session.mu.Unlock()
		proxy.release()
		if kinds := client.expectKinds(t, "CZ"); kinds != "CZ" {
			t.Fatalf("delivered %q, want CZ: a frame that arrived during the release overtook the buffered completion, so the release and its drain are not one operation",
				kinds)
		}
	})

	t.Run("subsequent_query_is_usable", func(t *testing.T) {
		proxy, server, client := commitOutcomeArmedProxy(t, commitOutcomeProxyHoldAck)
		client.query(t, "commit")
		proxy.waitForEvent("ack_held")
		proxy.release()
		server.sendReady <- struct{}{}
		if kinds := client.expectKinds(t, "CZ"); kinds != "CZ" {
			t.Fatalf("delivered %q, want CZ", kinds)
		}
		// The release RESUMES forwarding. A session left in its holding state
		// would capture this exchange too, and the connection the pool believes it
		// returned would be unusable.
		client.query(t, "select 1")
		server.sendReady <- struct{}{}
		if kinds := client.expectKinds(t, "CZ"); kinds != "CZ" {
			t.Fatalf("the query after the release delivered %q, want CZ: the session never resumed forwarding", kinds)
		}
	})
}

// TestCommitOutcomeProxyArmIsSingleUse is S2.
//
// One arm authorizes ONE interception. After the first transaction is settled or
// severed, the next matching COMMIT — which in every recovery positive is the
// resend, on the same route and the same predicate — must be forwarded. If the
// permit survived, the recovery positives would be measuring a second hold that
// nobody requested, and the product would never be asked to recover.
func TestCommitOutcomeProxyArmIsSingleUse(t *testing.T) {
	proxy, server, client := commitOutcomeArmedProxy(t, commitOutcomeProxyHoldCommit)
	client.query(t, "commit")
	proxy.waitForEvent("commit_held")
	if taken := proxy.armConsumptions(); taken != 1 {
		t.Fatalf("permits consumed = %d, want 1", taken)
	}
	if proxy.isArmed() {
		t.Fatal("the permit is still outstanding while its hold is owned: arming and holding are not the same state")
	}
	// Settle the first transaction. Ownership of the hold ends here; the permit
	// must NOT come back with it.
	proxy.forward()
	select {
	case text := <-server.commits:
		if !strings.EqualFold(strings.TrimSpace(text), "commit") {
			t.Fatalf("forwarded %q, want the held commit", text)
		}
	case <-time.After(commitOutcomeProxyWriteLimit):
		t.Fatal("COULD_NOT_LOOK: the forwarded COMMIT never reached the backend")
	}
	server.sendReady <- struct{}{}
	if kinds := client.expectKinds(t, "CZ"); kinds != "CZ" {
		t.Fatalf("the forwarded commit delivered %q, want CZ", kinds)
	}

	// A SECOND matching COMMIT: this is the resend every recovery positive sends.
	client.query(t, "commit")
	select {
	case text := <-server.commits:
		if !strings.EqualFold(strings.TrimSpace(text), "commit") {
			t.Fatalf("second COMMIT reached the backend as %q", text)
		}
	case <-time.After(commitOutcomeProxyWriteLimit):
		t.Fatal("the second COMMIT never reached the backend: a spent permit intercepted the resend")
	}
	server.sendReady <- struct{}{}
	if kinds := client.expectKinds(t, "CZ"); kinds != "CZ" {
		t.Fatalf("the resend delivered %q, want CZ", kinds)
	}
	if taken := proxy.armConsumptions(); taken != 1 {
		t.Fatalf("permits consumed = %d after the resend, want exactly 1", taken)
	}
}

// TestCommitOutcomeProxyNonReadingPeerIsBounded is the receiver half of S3.
//
// A CONNECTED peer that has stopped reading must not be able to pin a held write
// past the instrument's own limit. The distinction matters: the control this
// replaces CLOSED the socket first, and a write to a closed socket returns
// promptly on its own — so a build with the write deadline removed would still
// have passed. net.Pipe is unbuffered, so a write blocks until the far side
// reads, which is the real stalled receiver.
//
// Causal mutant: remove the write deadline in writeLocked. The bounded case must
// then fail to return within the limit.
func TestCommitOutcomeProxyNonReadingPeerIsBounded(t *testing.T) {
	frame := commitOutcomeFrame('C', commitOutcomeStringPayload("COMMIT"))
	// Register before each worker starts. Completion is separate from its result
	// so cleanup can join even after the test already consumed that result, or
	// Fatalf left the result unread. Closing both ends releases a no-deadline
	// mutant as well as a stalled reader; cleanup never relies on its own success.
	own := func(t *testing.T, left, right net.Conn, stopped <-chan struct{}) {
		t.Helper()
		t.Cleanup(func() {
			_ = left.Close()
			_ = right.Close()
			timer := time.NewTimer(commitOutcomeProxyWriteLimit)
			defer timer.Stop()
			select {
			case <-stopped:
			case <-timer.C:
				t.Error("COULD_NOT_LOOK: the pipe worker did not stop after both endpoints closed")
			}
		})
	}

	t.Run("stalled_receiver_is_bounded", func(t *testing.T) {
		left, right := net.Pipe()
		session := &commitOutcomeProxySession{client: left}
		// The far side is OPEN and simply never reads.
		started := make(chan struct{})
		done := make(chan error, 1)
		stopped := make(chan struct{})
		own(t, left, right, stopped)
		go func() {
			defer close(stopped)
			close(started)
			done <- session.writeClient(frame)
		}()
		select {
		case <-started:
		case <-time.After(commitOutcomeProxyWriteLimit):
			t.Fatal("COULD_NOT_LOOK: the write never started, so nothing was bounded")
		}

		select {
		case err := <-done:
			if err == nil {
				t.Fatal("the write to a peer that never reads returned success: an unbuffered pipe cannot have accepted it")
			}
			var netErr net.Error
			if !errors.As(err, &netErr) || !netErr.Timeout() {
				t.Fatalf("the write failed with %v, want a deadline error: the bound must come from the write deadline, not from a closed socket", err)
			}
		case <-time.After(commitOutcomeProxyHoldLimit):
			t.Fatalf("the write did not return within %s against a connected peer that stopped reading: the held write is not bounded",
				commitOutcomeProxyHoldLimit)
		}
	})

	t.Run("reading_receiver_is_the_positive", func(t *testing.T) {
		left, right := net.Pipe()
		session := &commitOutcomeProxySession{client: left}
		read := make(chan error, 1)
		stopped := make(chan struct{})
		own(t, left, right, stopped)
		go func() {
			defer close(stopped)
			buf := make([]byte, len(frame))
			_, err := io.ReadFull(right, buf)
			read <- err
		}()
		if err := session.writeClient(frame); err != nil {
			t.Fatalf("the write to a peer that DOES read failed: %v", err)
		}
		select {
		case err := <-read:
			if err != nil {
				t.Fatalf("the reading peer did not receive the frame: %v", err)
			}
		case <-time.After(commitOutcomeProxyWriteLimit):
			t.Fatal("COULD_NOT_LOOK: the reading peer never received the frame")
		}
	})
}

// TestCommitOutcomeFenceWitnessRefusesASharedKey is the R2 negative.
//
// The witness must refuse to be satisfied by a key that an original and its
// resend both take for a reason other than this route's fence. The tenant audit
// advisory key is exactly such a key — every request of a tenant contends on it —
// so handing it to the witness AS the intended fence must fail loudly rather than
// quietly turn the control back into the weaker "any non-audit key" form it
// replaced.
//
// It is a pure guard control: it needs no backend, because what it measures is
// the witness's refusal, not the database's behavior.
func TestCommitOutcomeFenceWitnessRefusesASharedKey(t *testing.T) {
	shared := commitOutcomeFenceKey{name: "tenant audit", classID: 7, objID: 11, known: true}
	intended := commitOutcomeFenceKey{name: "sessions_channel_create|t|w|s", classID: 7, objID: 11, known: true}

	guard := &commitOutcomeRecordingT{T: t}
	func() {
		defer func() { _ = recover() }()
		commitOutcomeWaitForExactFence(guard, nil, 1, intended, []commitOutcomeFenceKey{shared})
	}()
	if !guard.failed {
		t.Fatal("the fence witness accepted a key that is also the tenant audit key: a lock both the original and its resend take for another reason is not the route's fence, and accepting it would let M11 pass with the fence deleted")
	}
}

// commitOutcomeRecordingT captures whether the witness refused, without failing
// the surrounding test. It is deliberately tiny and local: a general test
// framework is not what this one guard needs.
type commitOutcomeRecordingT struct {
	*testing.T
	failed bool
}

func (r *commitOutcomeRecordingT) Fatalf(string, ...any) {
	r.failed = true
	panic("commit outcome fence witness refused")
}

func (r *commitOutcomeRecordingT) Helper() {}

// ---- F1(A): expected route-lock names, derived from REQUEST INTENT ----------
//
// Every name below is built from what the control is ABOUT TO SEND — tenant,
// workspace, actor, idempotency key, method and path — before the request runs.
// That direction is the whole point. Reading a name back from whatever locks
// appeared would make the oracle agree with any lock the route happens to take,
// which is exactly how the replaced witness could stay green with the route's own
// fence deleted.
//
// These helpers reproduce ONLY the small key shapes, not the product normalizer.
// Their owners were read at this pin:
//   - send      communication_service.go:1431-1438
//   - ack       communication_ack_service.go:660-666, scope :593-596
//   - cursor    communication_cursor_service.go:1232-1240, scope :511-515
//   - offer     communication_handoff_apply.go:2111-2115, scope handoff_service.go:1069
//   - response  same owner, with the response path
//   - create    communication_channel_service.go:288-290

// commitOutcomeCanonicalActor reproduces canonicalJSON for the two-field actor.
//
// work_state.go:149-173 marshals, decodes into a generic value and re-encodes
// with SetEscapeHTML(false), trimming the trailing newline. Re-encoding through a
// map sorts the keys, and "kind" sorts before "ref", so for the ASCII kind/ref
// pairs these fixtures use the result is exactly this encoder's output. It is
// deliberately narrow: no other shape is claimed.
func commitOutcomeCanonicalActor(t *testing.T, kind, ref string) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(map[string]string{"kind": kind, "ref": ref}); err != nil {
		t.Fatalf("COULD_NOT_LOOK: canonicalize the actor {%s,%s}: %v", kind, ref, err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte{'\n'})
}

func commitOutcomeActorFingerprint(t *testing.T, kind, ref string) []byte {
	t.Helper()
	sum := sha256.Sum256(commitOutcomeCanonicalActor(t, kind, ref))
	return sum[:]
}

func commitOutcomeKeyHash(key string) []byte {
	sum := sha256.Sum256([]byte(key))
	return sum[:]
}

// commitOutcomeCommandScope is the shared "<METHOD> <path>;workspace=<ws>" form.
func commitOutcomeCommandScope(method, path string, workspace model.ID) string {
	return fmt.Sprintf("%s %s;workspace=%s", method, path, workspace)
}

// commitOutcomeIdemDigestName is the ack/cursor shape: a SHA-256 over
// tenant, actor fingerprint, command scope and key hash joined by NUL, rendered
// RawURLBase64 behind the route's own prefix.
func commitOutcomeIdemDigestName(
	prefix string, tenant model.TenantID, actorFingerprint []byte,
	commandScope string, keyHash []byte,
) string {
	digest := sha256.Sum256(bytes.Join([][]byte{
		[]byte(tenant.String()), actorFingerprint, []byte(commandScope), keyHash,
	}, []byte{0}))
	return prefix + base64.RawURLEncoding.EncodeToString(digest[:])
}

// commitOutcomeCursorFilterHash uses the EXISTING exported product helper for the
// fixed personal DirectNotice filter. No new accessor is added: this one is
// already public, so the derivation stays independent of any test-only export.
func commitOutcomeCursorFilterHash(t *testing.T) []byte {
	t.Helper()
	_, hash, err := sessions.CanonicalCursorFilter(sessions.CursorFilter{
		CarrierClass: sessions.CursorCarrierDirectNoticeV1,
		MailboxKind:  sessions.MailboxPersonal,
	})
	if err != nil {
		t.Fatalf("COULD_NOT_LOOK: canonicalize the personal DirectNotice cursor filter: %v", err)
	}
	return hash
}

// commitOutcomeAwaitDistinctBackend identifies the RESEND's backend causally, or
// refuses.
//
// The candidates are the backends this proxy's OWN connections announced in
// BackendKeyData, minus the original. Among those, exactly one must be blocked
// according to PostgreSQL. Both halves matter: the proxy's list is what ties a
// backend to a connection this instrument created, and the blocked predicate is
// what ties it to this schedule. "Any other waiter in the database" is not an
// identification and is never accepted here — if zero or more than one candidate
// qualifies, this reports inability instead of guessing.
func commitOutcomeAwaitDistinctBackend(
	t *testing.T, owner *sql.DB, proxy *commitOutcomeProxy, originalPID int, route string,
) int {
	t.Helper()
	deadline := time.Now().Add(commitOutcomeProxyHoldLimit)
	var last []int
	for time.Now().Before(deadline) {
		var blocked []int
		for _, pid := range proxy.observedBackendPIDs() {
			if pid == originalPID {
				continue
			}
			if len(commitOutcomeBlockingPIDsOnce(t, owner, pid)) > 0 {
				blocked = append(blocked, pid)
			}
		}
		last = blocked
		if len(blocked) == 1 {
			return blocked[0]
		}
		if len(blocked) > 1 {
			t.Fatalf("COULD_NOT_LOOK: %d proxy-owned backends are blocked for %s (%v); the resend cannot be attributed to exactly one, and guessing would make the blocker evidence meaningless",
				len(blocked), route, blocked)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("COULD_NOT_LOOK: no proxy-owned backend other than the original %d became blocked for %s within %s (last saw %v), so the resend never reached a contended lock",
		originalPID, route, commitOutcomeProxyHoldLimit, last)
	return 0
}

// commitOutcomeContainsPID reports whether the blocker set names this backend.
func commitOutcomeContainsPID(blockers []int64, pid int) bool {
	for _, blocker := range blockers {
		if blocker == int64(pid) {
			return true
		}
	}
	return false
}
