// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
)

// incomingHandoffCommitBarrier is a TEST-ONLY decorator over the module's own
// data port. It counts committed read-write transactions and runs one action
// after the Nth of them, which is how a test lands a real, committed change
// BETWEEN two closes of the same in-flight request without a production hook:
// the seam is `api.DataConsumer`, which the engine itself uses at boot.
//
// The action's own transactions are not counted (it re-enters through this same
// port), so the barrier fires exactly once.
type incomingHandoffCommitBarrier struct {
	inner   api.ModuleData
	mu      sync.Mutex
	at      int
	commits int
	firing  bool
	fired   bool
	action  func()
}

func (d *incomingHandoffCommitBarrier) View(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	return d.inner.View(ctx, tenant, fn)
}

func (d *incomingHandoffCommitBarrier) Mutate(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	err := d.inner.Mutate(ctx, tenant, fn)
	if err != nil {
		return err
	}
	d.mu.Lock()
	if d.firing || d.fired || d.action == nil {
		d.mu.Unlock()
		return nil
	}
	d.commits++
	if d.commits != d.at {
		d.mu.Unlock()
		return nil
	}
	d.firing = true
	d.mu.Unlock()
	d.action()
	d.mu.Lock()
	d.firing, d.fired = false, true
	d.mu.Unlock()
	return nil
}

func (d *incomingHandoffCommitBarrier) ran() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.fired
}

// installIncomingHandoffCommitBarrier binds the decorator over a data handle
// built exactly as boot builds it, and restores the plain handle afterwards.
func installIncomingHandoffCommitBarrier(
	t *testing.T,
	eng *engine,
	after int,
	action func(),
) *incomingHandoffCommitBarrier {
	t.Helper()
	barrier := &incomingHandoffCommitBarrier{
		inner: api.NewModuleData(eng.store), at: after, action: action,
	}
	eng.sessionsMod.UseData(barrier)
	t.Cleanup(func() { eng.sessionsMod.UseData(api.NewModuleData(eng.store)) })
	return barrier
}

// activeIncomingHandoffGrant returns the recipient's current active grant on one
// Channel, read through the store the same way an operator would list it.
func activeIncomingHandoffGrant(
	t *testing.T,
	eng *engine,
	tenant model.TenantID,
	channelID model.ID,
	subject model.ID,
) model.ID {
	t.Helper()
	ctx := context.Background()
	var id model.ID
	if err := eng.store.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext("sessions.channel_grant")
		if err != nil {
			return err
		}
		rows, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{
			{Column: "channel_id", Op: model.OpEq, Value: channelID.String()},
			{Column: "subject_ref", Op: model.OpEq, Value: subject.String()},
			{Column: "state", Op: model.OpEq, Value: "active"},
		}, Limit: 2})
		if err != nil {
			return err
		}
		if len(rows) != 1 {
			return fmt.Errorf("active grant count = %d, want 1", len(rows))
		}
		id = model.ID(rows[0].String("id"))
		return nil
	}); err != nil {
		t.Fatalf("locate the recipient's active grant: %v", err)
	}
	return id
}

// revokeIncomingHandoffGrant revokes one grant through the REAL owner route with
// the Channel's current ETag. Nothing is written behind the API's back.
func revokeIncomingHandoffGrant(
	t *testing.T,
	e incomingHandoffHTTPEstate,
	channelID model.ID,
	grantID model.ID,
) {
	t.Helper()
	read := communicationHTTPTestRequest(t, e.eng, http.MethodGet,
		"/v1/m/sessions/channels/"+channelID.String(), e.owner.token, e.tenant, nil, nil)
	if read.status != http.StatusOK {
		t.Fatalf("read Channel before revoking = %d: %s", read.status, read.raw)
	}
	channel := communicationHTTPTestDecode[sessions.Channel](t, read)
	revoked := communicationHTTPTestRequest(t, e.eng, http.MethodPost,
		"/v1/m/sessions/channels/"+channelID.String()+"/grants/"+grantID.String()+"/revoke",
		e.owner.token, e.tenant, nil,
		map[string]string{"If-Match": fmt.Sprintf(`"v%d"`, channel.Version)})
	if revoked.status != http.StatusOK {
		t.Fatalf("revoke grant through the owner route = %d: %s", revoked.status, revoked.raw)
	}
}

// TestCommunicationIncomingHandoffPageIsOneAuthorityInstant is the permanent
// regression for the defect the independent review reproduced as F1
// (an internal design note (not shipped)):
// a page assembled from SEVERAL closes could publish an offer authorized by an
// early close even though the SAME request's later close had already observed
// the revocation that hides it.
//
// The shape is the review's, reproduced here as a permanent product test with a
// data-port barrier instead of a source hook:
//
//   - the recipient owns THREE offers, and the middle one is an invisible
//     candidate of its own — same recipient, different Channel, grant revoked
//     before the request — so the listing genuinely needs more than one round;
//   - the revocation between rounds is a REAL owner call to the existing
//     revoke route, committed before the request continues;
//   - the untouched variant is the positive control: two rounds, a card plus
//     has_more, and a continuation that returns the last offer exactly once.
func TestCommunicationIncomingHandoffPageIsOneAuthorityInstant(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(fmt.Sprintf("revoke_between_rounds=%t", revoke), func(t *testing.T) {
			estate := bootIncomingHandoffHTTPEstate(t, communicationHTTPTestSQLiteStore(t))
			first := estate.offer(t,
				map[string]any{"kind": "user", "ref": estate.recipient.id}, "first", 30*time.Minute)

			// The recipient's OWN middle candidate, made invisible before the
			// request by revoking its Channel grant. It is a candidate of the
			// recipient (the target index finds it) and is refused by the close,
			// which is what forces the listing into a second round.
			hidden := estate
			created := communicationHTTPTestRequest(t, estate.eng, http.MethodPost,
				"/v1/m/sessions/channels", estate.owner.token, estate.tenant, map[string]any{
					"workspace_id": estate.workspace.String(), "slug": "k3-handoff-hidden",
					"name": "K3 handoff hidden", "content_protection": "application_sealed",
					"default_ack_policy": "each_required", "default_ack_timeout_ms": 600_000,
					"max_fanout": 1,
					"initial_grants": []map[string]any{{
						"subject":  map[string]any{"kind": "user", "ref": estate.owner.id},
						"can_read": true, "can_write": true, "can_admin": true,
					}},
				}, nil)
			if created.status != http.StatusCreated {
				t.Fatalf("create the hidden-offer Channel = %d: %s", created.status, created.raw)
			}
			hidden.channelID = communicationHTTPTestDecode[sessions.ChannelMutationResult](
				t, created).Channel.ID
			hidden.grantChannel(t,
				map[string]any{"kind": "user", "ref": estate.recipient.id}, "hidden recipient")
			hidden.offer(t,
				map[string]any{"kind": "user", "ref": estate.recipient.id}, "hidden", 40*time.Minute)
			last := estate.offer(t,
				map[string]any{"kind": "user", "ref": estate.recipient.id}, "last", 50*time.Minute)
			revokeIncomingHandoffGrant(t, hidden, hidden.channelID,
				activeIncomingHandoffGrant(
					t, estate.eng, estate.tenant, hidden.channelID, estate.recipient.id))
			if page := estate.page(t, estate.recipient.token, "offered", 0, ""); len(page.Items) != 2 {
				t.Fatalf("the hidden candidate is not hidden: %+v", page)
			}

			mainGrant := activeIncomingHandoffGrant(
				t, estate.eng, estate.tenant, estate.channelID, estate.recipient.id)
			barrier := installIncomingHandoffCommitBarrier(t, estate.eng, 1, func() {
				if !revoke {
					return
				}
				revokeIncomingHandoffGrant(t, estate, estate.channelID, mainGrant)
			})

			response := estate.list(t, estate.recipient.token, "offered", 1, "")
			if revoke && !barrier.ran() {
				t.Fatal("the barrier never ran: no close committed during the listing")
			}
			eng := estate.eng
			eng.sessionsMod.UseData(api.NewModuleData(eng.store))

			if !revoke {
				if response.status != http.StatusOK {
					t.Fatalf("positive multi-round page = %d: %s", response.status, response.raw)
				}
				page := communicationHTTPTestDecode[sessions.IncomingHandoffPage](t, response)
				if len(page.Items) != 1 || page.Items[0].Handoff.ID != first.HandoffID ||
					!page.HasMore || page.Continuation == "" {
					t.Fatalf("positive multi-round page = %+v", page)
				}
				next := estate.page(t, estate.recipient.token, "offered", 1, page.Continuation)
				if len(next.Items) != 1 || next.Items[0].Handoff.ID != last.HandoffID ||
					next.HasMore || next.Continuation != "" {
					t.Fatalf("positive continuation page = %+v", next)
				}
				return
			}

			// The revocation committed mid-request is durable: a fresh request of
			// the same shape shows nothing.
			fresh := estate.page(t, estate.recipient.token, "offered", 1, "")
			if len(fresh.Items) != 0 || fresh.HasMore {
				t.Fatalf("the committed revocation did not withdraw authority: %+v", fresh)
			}
			// So the in-flight request must not publish a card its own later close
			// proved invisible. Either it answers with the state at its own final
			// authority instant (an empty page), or it declines; it may not ship a
			// card from an authority moment it has already superseded.
			switch response.status {
			case http.StatusOK:
				page := communicationHTTPTestDecode[sessions.IncomingHandoffPage](t, response)
				if len(page.Items) != 0 || page.HasMore || page.Continuation != "" {
					t.Fatalf(
						"MULTI_ROUND_STALE_CARD: the page published %d card(s) after its own later "+
							"close observed the committed revocation; a fresh query is empty: %s",
						len(page.Items), response.raw,
					)
				}
			case http.StatusServiceUnavailable, http.StatusForbidden, http.StatusNotFound:
			default:
				t.Fatalf("changed-authority page = %d: %s", response.status, response.raw)
			}
			if strings.Contains(string(response.raw), "first") {
				t.Fatalf("the changed-authority page leaked a card: %s", response.raw)
			}
		})
	}
}

// TestCommunicationIncomingHandoffClaimTakeoverDuringOpen adopts the independent
// review's Claim control (independent-review/overlays/claim_http_test.go,
// claim-open-r2.json) as a permanent test, with the data-port barrier replacing
// the review's source hook. The takeover commits between the detail's first close
// and its final one — that is, across the external content open — and the reader
// must publish nothing.
//
// The review's own finding is preserved: an emptied LISTING under a clean DENY is
// a valid 200, so this asserts "no carrier disclosed", not a particular status.
func TestCommunicationIncomingHandoffClaimTakeoverDuringOpen(t *testing.T) {
	for _, takeover := range []bool{false, true} {
		t.Run(fmt.Sprintf("claim_takeover=%t", takeover), func(t *testing.T) {
			estate := bootIncomingHandoffHTTPEstate(t, communicationHTTPTestSQLiteStore(t))
			estate.offer(t, map[string]any{"kind": "session", "ref": estate.targetSession.sid},
				"session claim canary", 30*time.Minute)
			page := estate.page(t, estate.targetSession.communication.Token, "offered", 1, "")
			if len(page.Items) != 1 {
				t.Fatalf("session recipient discovery = %+v", page)
			}
			delivery := page.Items[0].Carrier.DeliveryID

			barrier := installIncomingHandoffCommitBarrier(t, estate.eng, 1, func() {
				if !takeover {
					return
				}
				ctx := context.Background()
				if err := estate.eng.sessionsMod.Release(ctx, estate.tenant,
					estate.targetSession.sid, estate.targetSession.agentRef,
					estate.targetSession.claim.Fence); err != nil {
					t.Errorf("release the session Claim: %v", err)
					return
				}
				claimed, err := estate.eng.sessionsMod.Claim(ctx, estate.tenant,
					estate.targetSession.sid, estate.targetSession.agentRef, time.Hour)
				if err != nil || claimed.Fence <= estate.targetSession.claim.Fence {
					t.Errorf("Claim takeover = %+v, err %v", claimed, err)
					return
				}
				t.Logf("K3_HANDOFF_CLAIM real_takeover old_fence=%d new_fence=%d",
					estate.targetSession.claim.Fence, claimed.Fence)
			})

			got := estate.detail(t, estate.targetSession.communication.Token, delivery)
			if takeover && !barrier.ran() {
				t.Fatal("the barrier never ran: no close committed during the detail read")
			}
			estate.eng.sessionsMod.UseData(api.NewModuleData(estate.eng.store))

			if !takeover {
				if got.status != http.StatusOK ||
					!strings.Contains(string(got.raw), "session claim canary") {
					t.Fatalf("healthy session detail = %d: %s", got.status, got.raw)
				}
				return
			}
			if got.status == http.StatusOK ||
				strings.Contains(string(got.raw), "session claim canary") {
				t.Fatalf("a superseded Claim received protected content: %d %s", got.status, got.raw)
			}
			if got.status != http.StatusNotFound && got.status != http.StatusServiceUnavailable &&
				got.status != http.StatusForbidden {
				t.Fatalf("superseded-Claim detail = %d: %s", got.status, got.raw)
			}
			listed := estate.list(t, estate.targetSession.communication.Token, "offered", 1, "")
			if listed.status == http.StatusOK {
				empty := communicationHTTPTestDecode[sessions.IncomingHandoffPage](t, listed)
				if len(empty.Items) != 0 || empty.HasMore || empty.Continuation != "" {
					t.Fatalf("superseded-Claim listing disclosed a carrier: %s", listed.raw)
				}
			} else if listed.status != http.StatusNotFound &&
				listed.status != http.StatusForbidden &&
				listed.status != http.StatusServiceUnavailable {
				t.Fatalf("superseded-Claim listing = %d: %s", listed.status, listed.raw)
			}
			t.Logf("K3_HANDOFF_CLAIM detail_status=%d listing_status=%d", got.status, listed.status)
		})
	}
}

// TestCommunicationIncomingHandoffPostgresGrantWaitPostgres adopts the
// independent review's PostgreSQL row-lock control
// (independent-review/overlays/independent_http_test.go, pg-wait-r2.json and
// pg-expiry-r4.json) as a permanent test. A holder transaction locks the
// recipient's own grant row FOR UPDATE, the reader is observed genuinely waiting
// on it through pg_locks/pg_blocking_pids, and only then does the holder change
// the row and commit.
//
// The review's two DIFFERENT valid outcomes are preserved on purpose:
//   - revoke → the reader refuses (unavailable / not found), never content;
//   - expire → the DB clock crosses a real API-created expiry, and a clean DENY
//     is correct: an empty 200 listing and a 404 detail.
func TestCommunicationIncomingHandoffPostgresGrantWaitPostgres(t *testing.T) {
	for _, route := range []string{"list", "detail"} {
		for _, change := range []string{"keep", "revoke", "expire"} {
			t.Run(route+"/"+change, func(t *testing.T) {
				backing := communicationHTTPTestPostgresStore(t)
				estate := bootIncomingHandoffHTTPEstate(t, backing)
				estate.offer(t, map[string]any{"kind": "user", "ref": estate.recipient.id},
					"postgres row wait", 30*time.Minute)
				page := estate.page(t, estate.recipient.token, "offered", 1, "")
				if len(page.Items) != 1 {
					t.Fatalf("recipient discovery before the wait = %+v", page)
				}
				delivery := page.Items[0].Carrier.DeliveryID

				appDSN, err := os.ReadFile(backing.dsnFile)
				if err != nil {
					t.Fatalf("read the fixture application DSN: %v", err)
				}
				app, err := sql.Open("pgx", string(appDSN))
				if err != nil {
					t.Fatalf("open the fixture application connection: %v", err)
				}
				defer app.Close() //nolint:errcheck
				adminDSN, err := os.ReadFile(backing.adminDSNFile)
				if err != nil {
					t.Fatalf("read the fixture admin DSN: %v", err)
				}
				monitor, err := sql.Open("pgx", string(adminDSN))
				if err != nil {
					t.Fatalf("open the fixture monitor connection: %v", err)
				}
				defer monitor.Close() //nolint:errcheck
				ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				defer cancel()

				var expiry time.Time
				if change == "expire" {
					expiry = replaceIncomingHandoffGrantWithExpiring(t, estate, monitor, ctx)
				}

				tx, err := app.BeginTx(ctx, nil)
				if err != nil {
					t.Fatalf("begin the holder transaction: %v", err)
				}
				defer tx.Rollback() //nolint:errcheck
				if _, err := tx.ExecContext(
					ctx, "SELECT set_config('app.tenant_id',$1,true)", estate.tenant.String(),
				); err != nil {
					t.Fatalf("pin the holder tenant: %v", err)
				}
				var holder int
				if err := tx.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&holder); err != nil {
					t.Fatalf("read the holder backend pid: %v", err)
				}
				var grantID string
				if err := tx.QueryRowContext(ctx, `SELECT id FROM sessions_channel_grant
					WHERE tenant_id=$1 AND channel_id=$2 AND subject_kind='user'
					  AND subject_ref=$3 AND state='active' FOR UPDATE`,
					estate.tenant.String(), estate.channelID.String(),
					estate.recipient.id.String()).Scan(&grantID); err != nil {
					t.Fatalf("lock the recipient grant row: %v", err)
				}

				path := incomingHandoffListPath(estate.workspace, "offered", 1, "")
				if route == "detail" {
					path = "/v1/m/sessions/deliveries/" + delivery.String() + "/handoff"
				}
				request := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
				request.RemoteAddr = "127.0.0.1:43210"
				request.Header.Set("Authorization", "Bearer "+estate.recipient.token)
				request.Header.Set("X-Olivares-Tenant", estate.tenant.String())
				done := make(chan communicationHTTPTestResponse, 1)
				go func() {
					recorder := httptest.NewRecorder()
					estate.eng.api.Handler().ServeHTTP(recorder, request)
					done <- communicationHTTPTestResponse{
						status: recorder.Code, raw: recorder.Body.Bytes(),
						header: recorder.Header().Clone(),
					}
				}()
				joined := false
				defer func() {
					_ = tx.Rollback()
					cancel()
					if joined {
						return
					}
					select {
					case <-done:
					case <-time.After(10 * time.Second):
						t.Error("the HTTP reader was not joined during cleanup")
					}
				}()

				var waiter int
				for {
					err = monitor.QueryRowContext(ctx, `SELECT pid FROM pg_catalog.pg_locks
						WHERE NOT granted AND locktype IN ('transactionid','tuple')
						  AND $1 = ANY(pg_catalog.pg_blocking_pids(pid)) LIMIT 1`,
						holder).Scan(&waiter)
					if err == nil {
						break
					}
					if !errors.Is(err, sql.ErrNoRows) {
						t.Fatalf("observe the row wait: %v", err)
					}
					select {
					case response := <-done:
						joined = true
						t.Fatalf("the reader concluded before waiting on the row: %d", response.status)
					case <-ctx.Done():
						t.Fatal("no row wait was observed")
					case <-time.After(10 * time.Millisecond):
					}
				}
				t.Logf("K3_HANDOFF_PG observed_row_wait=true route=%s change=%s waiter=%d holder=%d",
					route, change, waiter, holder)

				switch change {
				case "revoke":
					var now time.Time
					if err := tx.QueryRowContext(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
						t.Fatalf("read the holder clock: %v", err)
					}
					if _, err := tx.ExecContext(ctx, `UPDATE sessions_channel_grant
						SET state='revoked', revoked_by_kind='user', revoked_by_ref=$1,
						    version=version+1, updated_at=$3 WHERE id=$2`,
						estate.owner.id.String(), grantID,
						model.NewTimestamp(now).String()); err != nil {
						t.Fatalf("revoke the locked grant row: %v", err)
					}
				case "expire":
					// An expiry is immutable once written, so the control waits for the
					// real database clock to cross the deadline the API created.
					for {
						var observed time.Time
						if err := monitor.QueryRowContext(
							ctx, "SELECT clock_timestamp()",
						).Scan(&observed); err != nil {
							t.Fatalf("observe the database clock: %v", err)
						}
						if !observed.Before(expiry) {
							t.Log("K3_HANDOFF_PG expiry_crossed_on_database_clock_before_commit=true")
							break
						}
						select {
						case <-ctx.Done():
							t.Fatal("the database clock never crossed the expiry")
						case <-time.After(10 * time.Millisecond):
						}
					}
				}
				if err := tx.Commit(); err != nil {
					t.Fatalf("commit the holder transaction: %v", err)
				}

				var response communicationHTTPTestResponse
				select {
				case response = <-done:
					joined = true
				case <-ctx.Done():
					t.Fatal("the reader did not conclude after the holder committed")
				}
				t.Logf("K3_HANDOFF_PG change=%s route=%s status=%d", change, route, response.status)
				if strings.Contains(string(response.raw), "postgres row wait") && change != "keep" {
					t.Fatalf("a withdrawn reader received content: %s", response.raw)
				}
				switch change {
				case "keep":
					if response.status != http.StatusOK {
						t.Fatalf("unchanged-authority control = %d: %s", response.status, response.raw)
					}
					if route == "list" {
						kept := communicationHTTPTestDecode[sessions.IncomingHandoffPage](t, response)
						if len(kept.Items) != 1 {
							t.Fatalf("unchanged-authority list = %+v", kept)
						}
					} else {
						read := communicationHTTPTestDecode[sessions.IncomingHandoffReadResult](
							t, response)
						if read.Content.Summary != "postgres row wait" {
							t.Fatalf("unchanged-authority detail = %+v", read)
						}
					}
				default:
					if route == "list" && response.status == http.StatusOK {
						empty := communicationHTTPTestDecode[sessions.IncomingHandoffPage](t, response)
						if len(empty.Items) != 0 || empty.HasMore {
							t.Fatalf("withdrawn list exposed a card: %s", response.raw)
						}
					} else if response.status != http.StatusNotFound &&
						response.status != http.StatusServiceUnavailable {
						t.Fatalf("withdrawn %s = %d: %s", route, response.status, response.raw)
					}
				}
			})
		}
	}
}

// replaceIncomingHandoffGrantWithExpiring revokes the recipient's grant and
// creates, through the real owner routes, a replacement that expires shortly.
// It returns the deadline the API recorded, read from the database clock.
func replaceIncomingHandoffGrantWithExpiring(
	t *testing.T,
	estate incomingHandoffHTTPEstate,
	monitor *sql.DB,
	ctx context.Context,
) time.Time {
	t.Helper()
	revokeIncomingHandoffGrant(t, estate, estate.channelID,
		activeIncomingHandoffGrant(
			t, estate.eng, estate.tenant, estate.channelID, estate.recipient.id))
	var now time.Time
	if err := monitor.QueryRowContext(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
		t.Fatalf("read the database clock: %v", err)
	}
	deadline := now.Add(4 * time.Second)
	read := communicationHTTPTestRequest(t, estate.eng, http.MethodGet,
		"/v1/m/sessions/channels/"+estate.channelID.String(),
		estate.owner.token, estate.tenant, nil, nil)
	if read.status != http.StatusOK {
		t.Fatalf("read Channel before the expiring grant = %d: %s", read.status, read.raw)
	}
	channel := communicationHTTPTestDecode[sessions.Channel](t, read)
	granted := communicationHTTPTestRequest(t, estate.eng, http.MethodPost,
		"/v1/m/sessions/channels/"+estate.channelID.String()+"/grants",
		estate.owner.token, estate.tenant, map[string]any{
			"subject":  map[string]any{"kind": "user", "ref": estate.recipient.id},
			"can_read": true, "can_write": true, "expires_at": deadline,
		}, map[string]string{"If-Match": fmt.Sprintf(`"v%d"`, channel.Version)})
	if granted.status != http.StatusOK {
		t.Fatalf("create the expiring grant through the API = %d: %s", granted.status, granted.raw)
	}
	before := estate.page(t, estate.recipient.token, "offered", 1, "")
	if len(before.Items) != 1 {
		t.Fatalf("the expiring grant is not readable before its deadline: %+v", before)
	}
	return deadline
}
