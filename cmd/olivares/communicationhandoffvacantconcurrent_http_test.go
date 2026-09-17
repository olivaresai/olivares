// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
)

// The OT-V concurrency controls, with two public requests ACTUALLY IN FLIGHT.
//
// The sequential cases in communicationhandoffvacant_http_test.go prove the CAS
// and state rules; they do not prove anything about overlap, because each of
// them finishes one request before starting the next. These do: a barrier holds
// the store's OWN tenant write gate, both requests are then observed genuinely
// waiting on it through pg_locks, and only then is the barrier released.
//
// The serializer is not introduced here and is not a product lock this lot
// added. sqlstore's Mutate starts the lineage writer BEFORE the callback
// (core/internal/store/sqlstore/store.go), and on PostgreSQL that writer takes a
// tenant-scoped advisory transaction lock keyed on
// "core.lineage.writer.tenant.v8:<tenant>" (lineage_writer.go). Every tenant
// write transaction in the process queues on it, so two writers in one tenant
// never interleave INSIDE their transactions — which is why no lock-order
// argument about K2 and K3 is needed, and why this file makes none.
//
// The key is not taken on trust: the barrier takes exactly that advisory key and
// the test refuses to continue unless the real requests are observed waiting on
// THAT key, blocked by THIS holder. A wrong key produces no waiter and fails.
//
// PostgreSQL only, and REQUIRED: the gate is an advisory lock, the observation
// is pg_locks, and both are PostgreSQL facts. On SQLite the same journey is the
// sequential family, which is honest about being sequential.
const vacantHandoffTenantGateKeyPrefix = "core.lineage.writer.tenant.v8:"

// vacantHandoffSerializerBarrier holds the tenant write gate on its own
// connection, so requests that reach Mutate stop there until it is released.
type vacantHandoffSerializerBarrier struct {
	tx        *sql.Tx
	monitor   *sql.DB
	holderPID int
	classID   int64
	objID     int64
	released  bool
}

// holdVacantHandoffTenantGate opens the barrier transaction and takes the gate.
func holdVacantHandoffTenantGate(
	t *testing.T,
	ctx context.Context,
	app *sql.DB,
	monitor *sql.DB,
	tenant model.TenantID,
) *vacantHandoffSerializerBarrier {
	t.Helper()
	tx, err := app.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin the tenant-gate barrier: %v", err)
	}
	var key int64
	if err := tx.QueryRowContext(ctx, "SELECT pg_catalog.hashtextextended($1,0)",
		vacantHandoffTenantGateKeyPrefix+tenant.String()).Scan(&key); err != nil {
		_ = tx.Rollback()
		t.Fatalf("derive the tenant-gate advisory key: %v", err)
	}
	barrier := &vacantHandoffSerializerBarrier{
		tx: tx, monitor: monitor,
		// pg_locks splits a bigint advisory key into (classid, objid) with
		// objsubid 1; deriving the halves here is what lets the wait be observed
		// on the EXACT key rather than on "some advisory lock".
		classID: int64(uint32(uint64(key) >> 32)), objID: int64(uint32(uint64(key))),
	}
	if err := tx.QueryRowContext(ctx, "SELECT pg_catalog.pg_backend_pid()").Scan(
		&barrier.holderPID); err != nil {
		_ = tx.Rollback()
		t.Fatalf("read the barrier backend pid: %v", err)
	}
	if _, err := tx.ExecContext(
		ctx, "SELECT pg_catalog.pg_advisory_xact_lock($1)", key); err != nil {
		_ = tx.Rollback()
		t.Fatalf("take the tenant write gate: %v", err)
	}
	t.Logf("K3_OTV_GATE held holder_pid=%d classid=%d objid=%d tenant=%s",
		barrier.holderPID, barrier.classID, barrier.objID, tenant)
	return barrier
}

// waiters lists the distinct backends currently blocked on the gate BY this
// holder. It reads pg_locks, which is not filtered by row-level security.
func (b *vacantHandoffSerializerBarrier) waiters(ctx context.Context) ([]int, error) {
	rows, err := b.monitor.QueryContext(ctx, `SELECT DISTINCT l.pid
FROM pg_catalog.pg_locks l
WHERE l.locktype = 'advisory' AND NOT l.granted AND l.objsubid = 1
  AND l.classid::bigint = $1 AND l.objid::bigint = $2
  AND l.database = (SELECT d.oid FROM pg_catalog.pg_database d
                    WHERE d.datname = pg_catalog.current_database())
  AND $3 = ANY(pg_catalog.pg_blocking_pids(l.pid))
ORDER BY 1`, b.classID, b.objID, b.holderPID)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	var pids []int
	for rows.Next() {
		var pid int
		if err := rows.Scan(&pid); err != nil {
			return nil, err
		}
		pids = append(pids, pid)
	}
	return pids, rows.Err()
}

// awaitWaiters blocks until exactly `want` backends are waiting on the gate. A
// request that CONCLUDES instead of waiting is a failure, not a slow poll: it
// would mean the case never overlapped.
func (b *vacantHandoffSerializerBarrier) awaitWaiters(
	t *testing.T,
	ctx context.Context,
	want int,
	inFlight []*vacantHandoffInFlight,
	label string,
) []int {
	t.Helper()
	for {
		pids, err := b.waiters(ctx)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("%s: observe the tenant-gate wait: %v", label, err)
		}
		if len(pids) == want {
			t.Logf("K3_OTV_GATE observed_wait label=%s waiters=%v holder_pid=%d",
				label, pids, b.holderPID)
			return pids
		}
		if len(pids) > want {
			t.Fatalf("%s: %d backends wait on the tenant gate, want %d: %v",
				label, len(pids), want, pids)
		}
		for _, request := range inFlight {
			select {
			case response := <-request.done:
				request.response, request.concluded = response, true
				t.Fatalf("%s: %s concluded (%d) before it was observed waiting on the gate: %s",
					label, request.label, response.status, response.raw)
			default:
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("%s: no tenant-gate wait was observed (waiters %v)", label, pids)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// release ends the barrier transaction. It writes nothing, so a rollback is the
// honest end: the requests behind it then run in the order they queued.
func (b *vacantHandoffSerializerBarrier) release(t *testing.T) {
	t.Helper()
	if b.released {
		return
	}
	b.released = true
	if err := b.tx.Rollback(); err != nil {
		t.Fatalf("release the tenant write gate: %v", err)
	}
}

// vacantHandoffInFlight is one public request built on the test goroutine and
// served on its own. Building it here matters: a helper that calls t.Fatalf
// inside the serving goroutine would be reporting from the wrong goroutine.
type vacantHandoffInFlight struct {
	label     string
	done      chan communicationHTTPTestResponse
	started   time.Time
	response  communicationHTTPTestResponse
	concluded bool
}

func launchVacantHandoffRequest(
	t *testing.T,
	ctx context.Context,
	estate incomingHandoffHTTPEstate,
	label string,
	method string,
	path string,
	token string,
	body any,
	headers map[string]string,
) *vacantHandoffInFlight {
	t.Helper()
	var raw []byte
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal %s: %v", label, err)
		}
		raw = encoded
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(raw)).WithContext(ctx)
	request.RemoteAddr = "127.0.0.1:43210"
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-Olivares-Tenant", estate.tenant.String())
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	flight := &vacantHandoffInFlight{
		label: label, done: make(chan communicationHTTPTestResponse, 1), started: time.Now(),
	}
	go func() {
		recorder := httptest.NewRecorder()
		estate.eng.api.Handler().ServeHTTP(recorder, request)
		flight.done <- communicationHTTPTestResponse{
			status: recorder.Code, header: recorder.Header().Clone(), raw: recorder.Body.Bytes(),
		}
	}()
	return flight
}

// join collects the response under a deadline. The deadline IS the bounded
// completion assertion: a request that never returns after the gate is released
// is a hang, and a hang is what this family exists to rule out.
func (f *vacantHandoffInFlight) join(t *testing.T, within time.Duration) communicationHTTPTestResponse {
	t.Helper()
	if f.concluded {
		return f.response
	}
	select {
	case response := <-f.done:
		f.response, f.concluded = response, true
		t.Logf("K3_OTV_GATE concluded label=%s status=%d after=%s",
			f.label, response.status, time.Since(f.started).Round(time.Millisecond))
		return response
	case <-time.After(within):
		t.Fatalf("%s did not conclude within %s of the gate being released", f.label, within)
		return communicationHTTPTestResponse{}
	}
}

// openVacantHandoffConnections opens the barrier (application role) and monitor
// (admin role) connections from the fixture's own DSN files.
func openVacantHandoffConnections(
	t *testing.T,
	backing communicationHTTPTestStore,
) (*sql.DB, *sql.DB) {
	t.Helper()
	appDSN, err := os.ReadFile(backing.dsnFile)
	if err != nil {
		t.Fatalf("read the fixture application DSN: %v", err)
	}
	app, err := sql.Open("pgx", string(appDSN))
	if err != nil {
		t.Fatalf("open the fixture application connection: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	adminDSN, err := os.ReadFile(backing.adminDSNFile)
	if err != nil {
		t.Fatalf("read the fixture admin DSN: %v", err)
	}
	monitor, err := sql.Open("pgx", string(adminDSN))
	if err != nil {
		t.Fatalf("open the fixture monitor connection: %v", err)
	}
	t.Cleanup(func() { _ = monitor.Close() })
	return app, monitor
}

// TestCommunicationHandoffVacantGenerationConcurrentHTTPPostgres runs the six
// overlapping cases the ratification names — vacant accept against acquire in
// both queue orders, and competing accepts under one key and two, for a human
// and for a session recipient — on one owned split-owner estate.
func TestCommunicationHandoffVacantGenerationConcurrentHTTPPostgres(t *testing.T) {
	backing := communicationHTTPTestPostgresStore(t)
	estate := bootIncomingHandoffHTTPEstate(t, backing)
	app, monitor := openVacantHandoffConnections(t, backing)

	t.Run("accept_wins_over_competing_acquire", func(t *testing.T) {
		exerciseVacantAcceptWinsOverAcquire(t, estate, app, monitor)
	})
	t.Run("acquire_wins_over_competing_accept", func(t *testing.T) {
		exerciseVacantAcquireWinsOverAccept(t, estate, app, monitor)
	})
	// Both keys AND both recipient kinds: the acceptance journey's recipient is a
	// human and the breadth family's is a session, and a case that measured only
	// one of them would be claiming more than it saw.
	for _, recipient := range []string{"user", "session"} {
		for _, sameKey := range []bool{true, false} {
			name := "competing_accepts_new_key_" + recipient
			if sameKey {
				name = "competing_accepts_same_key_" + recipient
			}
			t.Run(name, func(t *testing.T) {
				exerciseVacantCompetingAccepts(t, estate, app, monitor, recipient, sameKey)
			})
		}
	}
}

// vacantHandoffConcurrentOffer is one session-owned vacant item with an open
// offer, plus the coordinates both racers present. The OWNER is always a session
// so a competing lease.acquire is admissible; the RECIPIENT is varied, because
// the acceptance journey's recipient is a human and the ownership check reaches
// a different verdict for each.
type vacantHandoffConcurrentOffer struct {
	owner            communicationHTTPTestSession
	recipientSession communicationHTTPTestSession
	recipientKind    string
	recipientRef     string
	recipientToken   string
	work             vacantHandoffWork
	offer            sessions.HandoffOfferResult
	itemETag         string
	offerETag        string
}

// prepareVacantHandoffConcurrentOffer builds the estate for one case: a
// session-owned WorkItem (so a competing lease.acquire is ADMISSIBLE — a
// user-owned item is refused 422 before any ordering is reached), readied and
// never leased, offered to the requested recipient kind.
func prepareVacantHandoffConcurrentOffer(
	t *testing.T,
	estate incomingHandoffHTTPEstate,
	label string,
	recipientKind string,
) vacantHandoffConcurrentOffer {
	t.Helper()
	prepared := vacantHandoffConcurrentOffer{
		owner:         newVacantHandoffOwningSession(t, estate, label+"-owner"),
		recipientKind: recipientKind,
	}
	switch recipientKind {
	case "session":
		prepared.recipientSession = newVacantHandoffOwningSession(t, estate, label+"-recipient")
		prepared.recipientRef = prepared.recipientSession.sid
		prepared.recipientToken = prepared.recipientSession.communication.Token
	case "user":
		prepared.recipientRef = estate.recipient.id.String()
		prepared.recipientToken = estate.recipient.token
	default:
		t.Fatalf("%s: unsupported recipient kind %q", label, recipientKind)
	}
	prepared.work = createVacantHandoffWork(t, estate, estate.owner, "session", prepared.owner.sid,
		"OT-V concurrent "+label)
	item, itemETag := readVacantHandoffItem(t, estate, estate.owner, prepared.work.id)
	// CLAIMABLE is the point, not an accident: the item carries its one vacant
	// generation and is owned by a session, so a competing lease.acquire is
	// admissible and the race is about ordering rather than about admission.
	if item.Leased || !item.Claimable {
		t.Fatalf("%s: the item is not the claimable vacant generation this case needs: %+v",
			label, item)
	}
	if lease := readVacantHandoffLease(
		t, estate, estate.owner, prepared.work.id); lease.State != "vacant" ||
		lease.Fence != 0 || lease.Live {
		t.Fatalf("%s: the generation before the race = %+v, want vacant at fence 0", label, lease)
	}
	prepared.offer = offerVacantHandoff(t, estate, prepared.owner.communication.Token,
		prepared.work.id, itemETag,
		map[string]any{"kind": recipientKind, "ref": prepared.recipientRef},
		"OT-V concurrent "+label, 1)
	read := communicationHTTPTestDecode[sessions.IncomingHandoffReadResult](
		t, estate.detail(t, prepared.recipientToken, prepared.offer.DeliveryID))
	if read.OfferContext != sessions.IncomingHandoffContextCurrent {
		t.Fatalf("%s: offer context before the race = %q", label, read.OfferContext)
	}
	prepared.offerETag = read.Handoff.ETag
	// The offer advanced the item, so the coordinate a competing acquire would
	// present is read AFTER it — the race must be about the two writes, never
	// about a version the offer itself moved.
	_, prepared.itemETag = readVacantHandoffItem(t, estate, estate.owner, prepared.work.id)
	return prepared
}

// acquirePath is the public lease.acquire route for one item.
func vacantHandoffAcquirePath(itemID model.ID) string {
	return "/v1/m/sessions/work-items/" + itemID.String() + "/lease/acquire?mode=apply"
}

func vacantHandoffAcquireBody(session communicationHTTPTestSession) map[string]any {
	return map[string]any{
		"holder_sid": session.sid, "holder_run_ref": session.runRef, "ttl_seconds": 300,
	}
}

func vacantHandoffResponsePath(handoffID model.ID) string {
	return "/v1/m/sessions/handoffs/" + handoffID.String() + "/responses"
}

// exerciseVacantAcceptWinsOverAcquire overlaps the vacant accept with the new
// owner's own lease.acquire, with the accept queued FIRST. The transfer commits,
// the acquire then meets the WorkItem version the transfer published and is
// refused by its own If-Match — the C2 outcome, this time with both requests
// genuinely in flight.
func exerciseVacantAcceptWinsOverAcquire(
	t *testing.T,
	estate incomingHandoffHTTPEstate,
	app *sql.DB,
	monitor *sql.DB,
) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	prepared := prepareVacantHandoffConcurrentOffer(t, estate, "accept-first", "session")
	leaseBefore := readVacantHandoffLease(t, estate, estate.owner, prepared.work.id)
	epochBefore := readVacantHandoffClockGuardEpoch(t, estate)

	before := communicationHTTPTestEffects(t, estate.eng, estate.tenant)
	barrier := holdVacantHandoffTenantGate(t, ctx, app, monitor, estate.tenant)
	defer barrier.release(t)
	accept := launchVacantHandoffRequest(t, ctx, estate, "accept",
		http.MethodPost, vacantHandoffResponsePath(prepared.offer.HandoffID),
		prepared.recipientToken, map[string]any{"transition": "accept"},
		map[string]string{
			"If-Match": prepared.offerETag, "Idempotency-Key": model.NewID().String(),
		})
	acceptPID := barrier.awaitWaiters(t, ctx, 1,
		[]*vacantHandoffInFlight{accept}, "accept queued first")
	// The competing acquire is the CURRENT owner's, and it is admissible when it
	// is issued: the transfer has not committed, so the route's ownership check
	// (which happens on the read side, before the write transaction) passes. What
	// it meets after the release is the transfer, inside the transaction.
	acquire := launchVacantHandoffRequest(t, ctx, estate, "acquire",
		http.MethodPost, vacantHandoffAcquirePath(prepared.work.id),
		prepared.owner.work.Token, vacantHandoffAcquireBody(prepared.owner),
		map[string]string{
			"If-Match": prepared.itemETag, "Idempotency-Key": model.NewID().String(),
		})
	both := barrier.awaitWaiters(t, ctx, 2,
		[]*vacantHandoffInFlight{accept, acquire}, "acquire queued behind the accept")
	if len(both) != 2 || both[0] == both[1] {
		t.Fatalf("the two requests share a backend: %v", both)
	}
	t.Logf("K3_OTV_RACE case=accept_first accept_pid=%d waiters=%v", acceptPID[0], both)

	barrier.release(t)
	accepted := accept.join(t, 90*time.Second)
	refused := acquire.join(t, 90*time.Second)

	if accepted.status != http.StatusOK {
		t.Fatalf("the accept that queued first = %d: %s", accepted.status, accepted.raw)
	}
	assertResultingLeaseFenceAbsent(t, accepted, "concurrent accept")
	result := communicationHTTPTestDecode[sessions.HandoffResponseResult](t, accepted)
	if result.OwnerEpoch != 2 || result.State != sessions.HandoffAccepted || result.Replayed {
		t.Fatalf("the concurrent accept = %+v, want one fresh accepted transfer at epoch 2", result)
	}
	// The exact refusal, measured rather than assumed: the transfer moved the
	// WorkItem version the acquire presented, and that CAS is what the write
	// transaction reaches first — 412 version_mismatch, not the 422
	// owner_ineligible the changed owner would also justify. This is the C2
	// outcome, reached here with the two requests genuinely overlapping.
	if refused.status != http.StatusPreconditionFailed ||
		!strings.Contains(string(refused.raw), "version_mismatch") {
		t.Fatalf("the acquire behind the transfer = %d: %s", refused.status, refused.raw)
	}
	assertVacantHandoffExactlyOneAccept(t, estate, before,
		"the accept that won over a competing acquire")

	// Ownership moved exactly once and the GENERATION did not move at all: the
	// item is still unleased and still claimable, which is the whole OT-V claim
	// restated on the far side of a race.
	item, _ := readVacantHandoffItem(t, estate, estate.owner, prepared.work.id)
	if item.OwnerKind != "session" || item.OwnerRef != prepared.recipientRef ||
		item.OwnerEpoch != 2 || item.Leased || !item.Claimable {
		t.Fatalf("ownership after the race = %+v, want exactly one transfer to the recipient", item)
	}
	lease := readVacantHandoffLease(t, estate, estate.owner, prepared.work.id)
	if lease.State != "vacant" || lease.Fence != 0 || lease.Live || lease.HolderSID != "" ||
		lease.Version != leaseBefore.Version || lease.RenewalCount != 0 {
		t.Fatalf("the lease moved under the race: before=%+v after=%+v", leaseBefore, lease)
	}
	if epoch := readVacantHandoffClockGuardEpoch(t, estate); epoch != epochBefore {
		t.Fatalf("the workspace lease clock advanced under the race: %d -> %d",
			epochBefore, epoch)
	}
}

// exerciseVacantAcquireWinsOverAccept is the same overlap with the queue the
// other way round: the owner's lease.acquire commits first, so the offer that
// was sealed against the vacant generation is stale and the accept is refused
// with zero effects — the C1 outcome, genuinely overlapped.
func exerciseVacantAcquireWinsOverAccept(
	t *testing.T,
	estate incomingHandoffHTTPEstate,
	app *sql.DB,
	monitor *sql.DB,
) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	prepared := prepareVacantHandoffConcurrentOffer(t, estate, "acquire-first", "session")

	barrier := holdVacantHandoffTenantGate(t, ctx, app, monitor, estate.tenant)
	defer barrier.release(t)
	acquire := launchVacantHandoffRequest(t, ctx, estate, "acquire",
		http.MethodPost, vacantHandoffAcquirePath(prepared.work.id),
		prepared.owner.work.Token, vacantHandoffAcquireBody(prepared.owner),
		map[string]string{
			"If-Match": prepared.itemETag, "Idempotency-Key": model.NewID().String(),
		})
	barrier.awaitWaiters(t, ctx, 1, []*vacantHandoffInFlight{acquire}, "acquire queued first")
	accept := launchVacantHandoffRequest(t, ctx, estate, "accept",
		http.MethodPost, vacantHandoffResponsePath(prepared.offer.HandoffID),
		prepared.recipientToken, map[string]any{"transition": "accept"},
		map[string]string{
			"If-Match": prepared.offerETag, "Idempotency-Key": model.NewID().String(),
		})
	both := barrier.awaitWaiters(t, ctx, 2,
		[]*vacantHandoffInFlight{acquire, accept}, "accept queued behind the acquire")
	t.Logf("K3_OTV_RACE case=acquire_first waiters=%v", both)

	// Effects are sampled while BOTH are still blocked, so what follows is
	// measured across the whole overlap and not merely after it.
	before := communicationHTTPTestEffects(t, estate.eng, estate.tenant)
	barrier.release(t)
	acquired := acquire.join(t, 90*time.Second)
	refused := accept.join(t, 90*time.Second)

	if acquired.status != http.StatusOK {
		t.Fatalf("the acquire that queued first = %d: %s", acquired.status, acquired.raw)
	}
	if got := communicationHTTPTestDecode[sessions.CommandResult](t, acquired); got.LeaseFence != 1 {
		t.Fatalf("the winning acquire minted fence %d, want 1", got.LeaseFence)
	}
	if refused.status != http.StatusConflict {
		t.Fatalf("the accept behind the acquire = %d: %s", refused.status, refused.raw)
	}
	// The WINNER legitimately wrote its own WorkEvent and its outbox row, so a
	// blanket "no effects" would be measuring the acquire, not the refusal. Every
	// other counter — and every communication counter — is unchanged.
	assertVacantHandoffLoserWroteNothing(t, estate, before, 1, 1,
		"the accept refused behind a competing acquire")

	item, _ := readVacantHandoffItem(t, estate, estate.owner, prepared.work.id)
	if item.OwnerRef != prepared.owner.sid || item.OwnerEpoch != 1 {
		t.Fatalf("the refused accept moved ownership: %+v", item)
	}
	lease := readVacantHandoffLease(t, estate, estate.owner, prepared.work.id)
	if lease.State != "active" || lease.Fence != 1 || lease.HolderSID != prepared.owner.sid {
		t.Fatalf("the winning acquire did not take the generation: %+v", lease)
	}
}

// exerciseVacantCompetingAccepts pins same-key replay and different-key refusal
// while both public requests hold the same captured authority generation.
func exerciseVacantCompetingAccepts(
	t *testing.T,
	estate incomingHandoffHTTPEstate,
	app *sql.DB,
	monitor *sql.DB,
	recipientKind string,
	sameKey bool,
) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	label := "new-key-" + recipientKind
	if sameKey {
		label = "same-key-" + recipientKind
	}
	prepared := prepareVacantHandoffConcurrentOffer(t, estate, "accepts-"+label, recipientKind)
	leaseBefore := readVacantHandoffLease(t, estate, estate.owner, prepared.work.id)
	epochBefore := readVacantHandoffClockGuardEpoch(t, estate)

	firstKey := model.NewID().String()
	secondKey := firstKey
	if !sameKey {
		secondKey = model.NewID().String()
	}
	barrier := holdVacantHandoffTenantGate(t, ctx, app, monitor, estate.tenant)
	defer barrier.release(t)
	first := launchVacantHandoffRequest(t, ctx, estate, "accept-1",
		http.MethodPost, vacantHandoffResponsePath(prepared.offer.HandoffID),
		prepared.recipientToken, map[string]any{"transition": "accept"},
		map[string]string{"If-Match": prepared.offerETag, "Idempotency-Key": firstKey})
	barrier.awaitWaiters(t, ctx, 1, []*vacantHandoffInFlight{first}, label+": first accept")
	second := launchVacantHandoffRequest(t, ctx, estate, "accept-2",
		http.MethodPost, vacantHandoffResponsePath(prepared.offer.HandoffID),
		prepared.recipientToken, map[string]any{"transition": "accept"},
		map[string]string{"If-Match": prepared.offerETag, "Idempotency-Key": secondKey})
	both := barrier.awaitWaiters(t, ctx, 2,
		[]*vacantHandoffInFlight{first, second}, label+": second accept")
	t.Logf("K3_OTV_RACE case=competing_accepts_%s waiters=%v", label, both)

	before := communicationHTTPTestEffects(t, estate.eng, estate.tenant)
	barrier.release(t)
	winner := first.join(t, 90*time.Second)
	loser := second.join(t, 90*time.Second)

	if winner.status != http.StatusOK {
		t.Fatalf("%s: the accept that queued first = %d: %s", label, winner.status, winner.raw)
	}
	assertResultingLeaseFenceAbsent(t, winner, label+" winning accept")
	won := communicationHTTPTestDecode[sessions.HandoffResponseResult](t, winner)
	if won.OwnerEpoch != 2 || won.Replayed || won.State != sessions.HandoffAccepted {
		t.Fatalf("%s: the winning accept = %+v, want a fresh transfer at epoch 2", label, won)
	}
	assertVacantHandoffOverlappingSecondAccept(t, label, recipientKind, sameKey, winner, loser, won)
	assertVacantHandoffExactlyOneAccept(t, estate, before,
		label+": two overlapping accepts")

	item, _ := readVacantHandoffItem(t, estate, estate.owner, prepared.work.id)
	if item.OwnerKind != recipientKind || item.OwnerRef != prepared.recipientRef ||
		item.OwnerEpoch != 2 {
		t.Fatalf("%s: ownership after two overlapping accepts = %+v, want exactly one transfer",
			label, item)
	}
	lease := readVacantHandoffLease(t, estate, estate.owner, prepared.work.id)
	if lease.State != "vacant" || lease.Fence != 0 || lease.Live ||
		lease.Version != leaseBefore.Version {
		t.Fatalf("%s: the lease moved under two overlapping accepts: %+v", label, lease)
	}
	if epoch := readVacantHandoffClockGuardEpoch(t, estate); epoch != epochBefore {
		t.Fatalf("%s: the workspace lease clock advanced: %d -> %d", label, epochBefore, epoch)
	}
	// The winner's command identity and the loser's status, so the per-family
	// split above is legible in the raw output. Nothing is read again here — the
	// ownership, lease and clock assertions immediately above are the checks.
	t.Logf("K3_OTV_RACE case=competing_accepts_%s winner_command=%s loser_status=%d",
		label, won.CommandID, loser.status)
}

// assertVacantHandoffLoserWroteNothing states the refusal precisely when the
// WINNER of the same race legitimately wrote something. Every communication
// counter must be untouched; the work-side counters may advance by exactly the
// winner's own writes and by nothing else.
func assertVacantHandoffLoserWroteNothing(
	t *testing.T,
	estate incomingHandoffHTTPEstate,
	before communicationHTTPTestEffectCounts,
	winnerWorkEvents int,
	winnerOutbox int,
	label string,
) {
	t.Helper()
	after := communicationHTTPTestEffects(t, estate.eng, estate.tenant)
	if after.channels != before.channels || after.grants != before.grants ||
		after.messages != before.messages || after.deliveries != before.deliveries ||
		after.acks != before.acks || after.handoffs != before.handoffs ||
		after.commands != before.commands || after.workItems != before.workItems ||
		after.workLeases != before.workLeases || after.cursors != before.cursors ||
		after.barriers != before.barriers || after.successAudits != before.successAudits ||
		after.workEvents != before.workEvents+winnerWorkEvents ||
		after.outbox != before.outbox+winnerOutbox {
		t.Fatalf("%s: effects beyond the winner's own: before=%+v after=%+v (winner +%d events, +%d outbox)",
			label, before, after, winnerWorkEvents, winnerOutbox)
	}
}

// Same-key requests recognize the recorded result after current authority is
// reestablished. A SESSION loser with a different key has no receipt to recognize
// and remains unknown; an ordinary USER loser reaches the existing version CAS.
func assertVacantHandoffOverlappingSecondAccept(
	t *testing.T,
	label string,
	recipientKind string,
	sameKey bool,
	winner communicationHTTPTestResponse,
	loser communicationHTTPTestResponse,
	won sessions.HandoffResponseResult,
) {
	t.Helper()
	switch {
	case recipientKind == "session" && !sameKey:
		if loser.status != http.StatusServiceUnavailable ||
			!strings.Contains(string(loser.raw), "evidence_unavailable") {
			t.Fatalf("%s: the overlapping second accept = %d: %s", label, loser.status, loser.raw)
		}
	case sameKey:
		if loser.status != http.StatusOK {
			t.Fatalf("%s: the overlapping same-key accept = %d: %s", label, loser.status, loser.raw)
		}
		replayed := communicationHTTPTestDecode[sessions.HandoffResponseResult](t, loser)
		if !replayed.Replayed {
			t.Fatalf("%s: the overlapping same-key accept did not report a replay: %s",
				label, loser.raw)
		}
		// Everything the receipt persisted is identical; `replayed` is transport
		// metadata and is the ONLY field allowed to differ. Comparing the whole
		// struct is what says so — a new command_id, event_id, ack_id or audit_seq
		// would mean a second transaction had run.
		normalized := replayed
		normalized.Replayed = won.Replayed
		if normalized != won {
			t.Fatalf("%s: the overlapping second accept is not the recorded replay: %+v vs %+v",
				label, replayed, won)
		}
		assertResultingLeaseFenceAbsent(t, loser, label+" replay")
		if !bytes.Equal(
			bytes.ReplaceAll(loser.raw, []byte(`"replayed":true`), []byte(`"replayed":false`)),
			winner.raw,
		) {
			t.Fatalf("%s: the replay body differs beyond its replay flag:\n%s\n%s",
				label, winner.raw, loser.raw)
		}
	default:
		if loser.status != http.StatusConflict {
			t.Fatalf("%s: the overlapping new-key accept = %d: %s", label, loser.status, loser.raw)
		}
	}
}

// assertVacantHandoffExactlyOneAccept pins the durable shape of ONE accepted
// vacant transfer, measured on this engine: the Ack it appends, its command
// receipt, its WorkEvent and the outbox row that carries it, and its one success
// audit. Nothing else moves — no new Handoff, no Delivery, no WorkItem, no
// WorkLease row. Whatever the loser did, it did not do this a second time.
func assertVacantHandoffExactlyOneAccept(
	t *testing.T,
	estate incomingHandoffHTTPEstate,
	before communicationHTTPTestEffectCounts,
	label string,
) {
	t.Helper()
	after := communicationHTTPTestEffects(t, estate.eng, estate.tenant)
	want := before
	want.acks++
	want.commands++
	want.workEvents++
	want.outbox++
	want.successAudits++
	want.digest = after.digest
	if after != want {
		t.Fatalf("%s: durable effects are not exactly one accept: before=%+v after=%+v",
			label, before, after)
	}
}

// A request-local data decorator pauses after capture and after the failed
// transaction has returned. Both pauses sit outside a Store transaction, so
// current readiness and a real owner command can change without a lock cycle.
type handoffRecognitionHTTPAttempt struct {
	captured   chan struct{}
	enter      chan struct{}
	rolledBack chan struct{}
	resume     chan struct{}
	mutations  atomic.Int64
	firstError error
}
type handoffRecognitionHTTPData struct {
	inner    api.ModuleData
	mu       sync.Mutex
	selected context.Context
	attempt  *handoffRecognitionHTTPAttempt
}

func (d *handoffRecognitionHTTPData) arm(a *handoffRecognitionHTTPAttempt) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.selected = nil
	d.attempt = a
}

func (d *handoffRecognitionHTTPData) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.inner.View(ctx, tenant, fn)
}
func (d *handoffRecognitionHTTPData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	d.mu.Lock()
	a := d.attempt
	if a != nil && d.selected == nil {
		d.selected = ctx
	}
	if d.selected != ctx {
		a = nil
	}
	d.mu.Unlock()
	if a == nil {
		return d.inner.Mutate(ctx, tenant, fn)
	}
	n := a.mutations.Add(1)
	if n == 1 {
		close(a.captured)
		select {
		case <-a.enter:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	err := d.inner.Mutate(ctx, tenant, fn)
	if n == 1 && err != nil {
		a.firstError = err
		close(a.rolledBack)
		select {
		case <-a.resume:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}
func awaitHandoffRecognitionHTTP(t *testing.T, ctx context.Context, signal <-chan struct{}, flight *vacantHandoffInFlight) {
	t.Helper()
	select {
	case <-signal:
	case got := <-flight.done:
		t.Fatalf("request concluded before recognition barrier: %d %s", got.status, got.raw)
	case <-ctx.Done():
		t.Fatal("recognition HTTP barrier deadline")
	}
}

func handoffRecognitionHTTPRows(t *testing.T, e incomingHandoffHTTPEstate) map[model.Kind][]model.Record {
	t.Helper()
	rows := map[model.Kind][]model.Record{}
	err := e.eng.store.View(context.Background(), e.tenant, func(sc store.Scope) error {
		for _, kind := range []model.Kind{"sessions.handoff", "sessions.work_item", "sessions.work_lease", "sessions.work_guard",
			"sessions.channel", "sessions.channel_grant", "sessions.message", "sessions.message_delivery", "sessions.message_audience",
			"sessions.message_audience_recipient", "sessions.message_ack", "sessions.communication_command", "sessions.work_event", "sessions.work_outbox"} {
			repo, err := sc.Ext(kind)
			if err != nil {
				return err
			}
			records, page, err := repo.List(context.Background(), model.Query{Sort: []model.Sort{{Column: model.ColID}}, Limit: 1000})
			if err != nil {
				return err
			}
			if page.HasMore {
				return fmt.Errorf("%s row snapshot truncated", kind)
			}
			rows[kind] = records
		}
		return sc.Audit().Walk(context.Background(), 1, func(event model.AuditEvent) error {
			if !strings.HasPrefix(event.Action, "sessions.communication.") {
				return nil
			}
			encoded, err := json.Marshal(event)
			if err != nil {
				return err
			}
			rows["domain.audit"] = append(rows["domain.audit"], model.Record{"event": string(encoded)})
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestCommunicationHandoffSessionRecognitionCurrentAuthorityHTTPPostgres(t *testing.T) {
	estate := bootIncomingHandoffHTTPEstate(t, communicationHTTPTestPostgresStore(t))
	data := &handoffRecognitionHTTPData{inner: api.NewModuleData(estate.eng.store)}
	estate.eng.sessionsMod.UseData(data)
	t.Cleanup(func() { estate.eng.sessionsMod.UseData(api.NewModuleData(estate.eng.store)) })
	// Readiness withdrawal is last because stopping this witness is permanent
	// for its composed engine generation.
	for _, name := range []string{"later valid ownership change", "current grant denial", "readiness withdrawn"} {
		t.Run(name, func(t *testing.T) {
			prepared := prepareVacantHandoffConcurrentOffer(t, estate, strings.ReplaceAll(name, " ", "-"), "session")
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			a := &handoffRecognitionHTTPAttempt{captured: make(chan struct{}), enter: make(chan struct{}), rolledBack: make(chan struct{}), resume: make(chan struct{})}
			var enterOnce, resumeOnce sync.Once
			enter := func() { enterOnce.Do(func() { close(a.enter) }) }
			resume := func() { resumeOnce.Do(func() { close(a.resume) }) }
			defer enter()
			defer resume()
			key := model.NewID().String()
			headers := map[string]string{"If-Match": prepared.offerETag, "Idempotency-Key": key}
			data.arm(a)
			loser := launchVacantHandoffRequest(t, ctx, estate, "same-key recognition", http.MethodPost,
				vacantHandoffResponsePath(prepared.offer.HandoffID), prepared.recipientToken, map[string]any{"transition": "accept"}, headers)
			awaitHandoffRecognitionHTTP(t, ctx, a.captured, loser)
			winner := communicationHTTPTestRequest(t, estate.eng, http.MethodPost, vacantHandoffResponsePath(prepared.offer.HandoffID), prepared.recipientToken, estate.tenant, map[string]any{"transition": "accept"}, headers)
			if winner.status != http.StatusOK {
				t.Fatalf("winner: %d %s", winner.status, winner.raw)
			}
			won := communicationHTTPTestDecode[sessions.HandoffResponseResult](t, winner)
			if won.Replayed {
				t.Fatal("winner was already replayed")
			}
			enter()
			awaitHandoffRecognitionHTTP(t, ctx, a.rolledBack, loser)
			if !errors.Is(a.firstError, sessions.ErrCommunicationEvidenceUnknown) || errors.Is(a.firstError, store.ErrConflict) {
				t.Fatalf("original rollback classification: %v", a.firstError)
			}
			wantStatus, wantAttempts := http.StatusOK, int64(2)
			switch name {
			case "later valid ownership change":
				item, etag := readVacantHandoffItem(t, estate, estate.owner, prepared.work.id)
				assignment := communicationHTTPTestRequest(t, estate.eng, http.MethodPost, "/v1/m/sessions/work-items/"+prepared.work.id.String()+"/assignments?mode=apply", estate.owner.token, estate.tenant,
					map[string]any{"owner_kind": "user", "owner_ref": estate.owner.id}, map[string]string{"If-Match": etag, "Idempotency-Key": model.NewID().String()})
				if assignment.status != http.StatusOK {
					t.Fatalf("later owner assignment: %d %s (epoch %d)", assignment.status, assignment.raw, item.OwnerEpoch)
				}
			case "current grant denial":
				grant := activeIncomingHandoffGrant(t, estate.eng, estate.tenant, estate.channelID, model.ID(prepared.recipientRef))
				revokeIncomingHandoffGrant(t, estate, estate.channelID, grant)
				wantStatus = http.StatusForbidden
			case "readiness withdrawn":
				estate.eng.communicationPump.stop()
				wantStatus, wantAttempts = http.StatusServiceUnavailable, 1
				readiness, err := estate.eng.sessionsMod.EvaluateCommunicationReadiness(ctx)
				if err != nil || readiness.Effective || readiness.Components.PumpReady {
					t.Fatalf("pump withdrawal did not change full readiness: %+v %v", readiness, err)
				}
			}
			before := handoffRecognitionHTTPRows(t, estate)
			resume()
			got := loser.join(t, 30*time.Second)
			if got.status != wantStatus || a.mutations.Load() != wantAttempts {
				t.Fatalf("%s: status=%d attempts=%d body=%s", name, got.status, a.mutations.Load(), got.raw)
			}
			if got.status == http.StatusOK {
				replayed := communicationHTTPTestDecode[sessions.HandoffResponseResult](t, got)
				if !replayed.Replayed {
					t.Fatal("same-key response did not replay")
				}
				replayed.Replayed = false
				if replayed != won {
					t.Fatal("later owner transition changed historical receipt")
				}
			}
			if got.status == http.StatusServiceUnavailable && !strings.Contains(string(got.raw), "evidence_unavailable") {
				t.Fatal("readiness lost its established wire code")
			}
			if after := handoffRecognitionHTTPRows(t, estate); !reflect.DeepEqual(before, after) {
				t.Fatal("HTTP recognition changed domain rows or success audit anchors")
			}
			outer := communicationHTTPTestRequest(t, estate.eng, http.MethodPost, vacantHandoffResponsePath(prepared.offer.HandoffID), "invalid-credential", estate.tenant, map[string]any{"transition": "accept"}, headers)
			if outer.status != http.StatusUnauthorized {
				t.Fatalf("outer authentication mapping=%d", outer.status)
			}
		})
	}
}
