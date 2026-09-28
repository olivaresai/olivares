// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver "pgx" for the admission observer

	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// workCreateSteps is a workflow graph of one work-create step whose owner is
// the account owner.
func workCreateSteps(t *testing.T, owner model.ID) string {
	t.Helper()
	steps := []map[string]any{{
		"ref": "create", "kind": "work-create", "depends_on": []string{},
		"config": map[string]any{
			"workspace_id": model.NewID().String(), "work_kind": "task", "title": "T", "brief_md": "b",
			"priority": "p2", "owner": map[string]any{"kind": "user", "ref": owner.String()},
			"criteria":   []map[string]any{{"key": "done", "ordinal": 1, "statement": "s", "required": true}},
			"provenance": map[string]any{"kind": "human", "ref": "test"},
		},
	}}
	b, err := json.Marshal(steps)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// userBinding is a raw source-scope binding row for the user tree.
func userBinding(sourceRef, userRef string) model.Record {
	return model.Record{
		"source_type": "mcp", "source_ref": sourceRef, "scope_tree": "user", "scope_ref": userRef,
		"enabled": true, "created_by": "test",
	}
}

// TestTheWriteSeamRefusesAnUnfencedUserReference: a module row whose counted
// columns name an existing account is written only by a transaction that pinned
// that account's authority version; an id that names no account passes; and the
// policy writer refuses a kind no registry knows.
func TestTheWriteSeamRefusesAnUnfencedUserReference(t *testing.T) {
	onConsentEngines(t, func(t *testing.T, e *consentEstate) {
		ctx := context.Background()
		v := e.createUser("seam@consent.test")
		e.seedMembership(v, e.tT, "viewer")
		unfenced := func(kind model.Kind, rec model.Record) error {
			return e.eng.store.Mutate(ctx, e.tT, func(sc store.Scope) error {
				repo, err := sc.Ext(kind)
				if err != nil {
					return err
				}
				_, err = repo.Create(ctx, rec)
				return err
			})
		}
		countBindings := func(sourceRef string) int {
			n := 0
			if err := e.eng.store.View(ctx, e.tT, func(sc store.Scope) error {
				repo, err := sc.Ext("sourcescope.binding")
				if err != nil {
					return err
				}
				recs, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{
					{Column: "source_ref", Op: model.OpEq, Value: sourceRef},
				}, Limit: 10})
				n = len(recs)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			return n
		}

		if err := unfenced("sourcescope.binding", userBinding("seam-unfenced", v.String())); !errors.Is(err, store.ErrUserAuthorityNotHeld) {
			t.Errorf("an unfenced user binding naming an account = %v, want the not-held refusal", err)
		}
		if n := countBindings("seam-unfenced"); n != 0 {
			t.Errorf("the refused binding was written (%d rows)", n)
		}
		e.seedFenced(e.tT, "sourcescope.binding", userBinding("seam-fenced", v.String()), v)
		if n := countBindings("seam-fenced"); n != 1 {
			t.Errorf("the fenced binding was not written (%d rows)", n)
		}
		if err := unfenced("sourcescope.binding", userBinding("seam-ghost", model.NewID().String())); err != nil {
			t.Errorf("a binding naming an id with no account = %v, want it written", err)
		}
		if err := unfenced("orchestration.schedule", model.Record{
			"name": "seam-schedule", "subject_kind": "agent", "subject_ref": "agent-1", "trigger_kind": "manual",
			"expected_interval_seconds": int64(0), "grace_factor": int64(2), "desired_status": "active",
			"owner_actor": "user:" + v.String(), "owner_actor_kind": "user",
		}); !errors.Is(err, store.ErrUserAuthorityNotHeld) {
			t.Errorf("an unfenced schedule owned by user:<id> = %v, want the not-held refusal", err)
		}
		if err := unfenced("orchestration.workflow", model.Record{
			"name": "seam-workflow", "enabled": true, "steps": workCreateSteps(t, v),
			"owner_actor": "user:" + e.super.UserID.String(), "owner_actor_kind": "user",
		}); !errors.Is(err, store.ErrUserAuthorityNotHeld) {
			t.Errorf("an unfenced workflow whose step names the account = %v, want the not-held refusal", err)
		}
		if err := e.eng.store.Mutate(ctx, e.tT, func(sc store.Scope) error {
			_, err := sc.Policies().Create(ctx, model.Policy{Name: "seam-guardrail", Kind: "guardrail", Enabled: true})
			return err
		}); !errors.Is(err, model.ErrUnknownKind) {
			t.Errorf("a policy of an unregistered kind = %v, want the unknown-kind refusal", err)
		}
	})
}

// fenceSubject is the account a fenced writer names, as its case provisioned it,
// with the account's own stepped-up session for a write the account makes itself.
type fenceSubject struct {
	id         model.ID
	email      string
	externalID string
	token      string
}

// aliases are the values a writer's row may name the account by.
func (s fenceSubject) aliases() []string {
	out := []string{s.id.String()}
	if s.externalID != "" {
		out = append(out, s.externalID)
	}
	return out
}

// fenceWriter is one fenced product writer the probe drives.
type fenceWriter struct {
	name string
	// messaging marks a writer only the activated communication kernel serves:
	// its cases run on the messaging estate.
	messaging bool
	// kind and column are where the writer's row names the account.
	kind   model.Kind
	column string
	// recheck marks a writer that, run again after a conflict at its barrier,
	// checks its recipient's membership before it reads the standing again, so a
	// removal in between is answered by that check.
	recheck bool
	// subject provisions the account the writer names; nil provisions the
	// estate's default subject.
	subject func(e *consentEstate, label string) fenceSubject
	// prepare does what the write needs besides the write itself (a parent row,
	// a current version), before any lock is observed, and returns the write.
	prepare func(e *consentEstate, s fenceSubject) func() consentResp
}

// fenceWriters are the product writers of counted references the probe drives
// on the consent estate, one per writer class a product route reaches: a
// user-subject grant, a Cedar permit, a managed document, a user source-scope
// binding, a model-access allow, a workflow step, a subscription and a schedule
// the account owns, an agent it sponsors, and a spend cap on it. The
// communication writers are messagingFenceWriters. collector is the endpoint the
// subscription delivers to.
func fenceWriters(collector string) []fenceWriter {
	admin := func(method, path string, body func(s fenceSubject) map[string]any) func(e *consentEstate, s fenceSubject) func() consentResp {
		return func(e *consentEstate, s fenceSubject) func() consentResp {
			return func() consentResp { return e.do(method, path, e.admin, e.tT, body(s)) }
		}
	}
	own := func(method, path string, body func(s fenceSubject) map[string]any) func(e *consentEstate, s fenceSubject) func() consentResp {
		return func(e *consentEstate, s fenceSubject) func() consentResp {
			return func() consentResp { return e.do(method, path, s.token, e.tT, body(s)) }
		}
	}
	return []fenceWriter{
		{name: "a user-subject grant", kind: "governance.scoped_grant", column: "subject_ref",
			prepare: admin("POST", "/v1/m/governance/rbac/grants", func(s fenceSubject) map[string]any {
				return map[string]any{"subject_kind": "user", "subject_ref": s.id.String(), "role": "editor", "scope_tree": "tenant"}
			})},
		{name: "a Cedar permit naming the account", kind: "governance.policy_revision", column: "content",
			prepare: admin("POST", "/v1/m/governance/pdp/publish", func(s fenceSubject) map[string]any {
				return map[string]any{
					"engine": "cedar", "source": `permit(principal == User::"` + s.id.String() + `", action == Action::"agent:read", resource);`,
				}
			})},
		{name: "a managed document naming the account", kind: "governance.policy_revision", column: "content",
			prepare: func(e *consentEstate, s fenceSubject) func() consentResp {
				return func() consentResp {
					return e.publishManaged(e.tT, "managed-settings", managedNaming("managed-settings", s.id.String()))
				}
			}},
		{name: "a user source-scope binding", kind: "sourcescope.binding", column: "scope_ref",
			prepare: admin("POST", "/v1/m/sourcescope/bindings", func(s fenceSubject) map[string]any {
				return map[string]any{
					"source_type": "mcp", "source_ref": "fence-" + s.id.String(), "scope_tree": "user",
					"scope_ref": s.id.String(), "enabled": true,
				}
			})},
		{name: "a model-access allow", kind: "models.model_access", column: "subject_ref",
			prepare: admin("POST", "/v1/m/models/model-access", func(s fenceSubject) map[string]any {
				return map[string]any{
					"subject_kind": "user", "subject_ref": s.id.String(), "target_kind": "model",
					"target_ref": "fence-" + s.id.String(), "effect": "allow",
				}
			})},
		{name: "a workflow whose step names the account", kind: "orchestration.workflow", column: "steps",
			prepare: func(e *consentEstate, s fenceSubject) func() consentResp {
				var steps []map[string]any
				if err := json.Unmarshal([]byte(workCreateSteps(e.t, s.id)), &steps); err != nil {
					e.t.Fatal(err)
				}
				return func() consentResp {
					return e.do("POST", "/v1/m/orchestration/workflows", e.admin, e.tT, map[string]any{
						"name": "fence-" + s.id.String(), "steps": steps,
					})
				}
			}},
		{name: "a subscription the account creates and owns", kind: "eventing.subscription", column: "owner_actor",
			prepare: own("POST", "/v1/m/eventing/subscriptions", func(s fenceSubject) map[string]any {
				return map[string]any{
					"name": "fence-" + s.id.String(), "endpoint": collector,
					"event_types": []string{"finding.reported"}, "role": "viewer",
				}
			})},
		{name: "a schedule the account declares and owns", kind: "orchestration.schedule", column: "owner_actor",
			prepare: own("POST", "/v1/m/orchestration/schedules", func(s fenceSubject) map[string]any {
				return map[string]any{
					"name": "fence-" + s.id.String(), "subject_kind": "agent", "subject_ref": "agent-fence",
					"trigger_kind": "manual",
				}
			})},
		{name: "an agent the account sponsors", kind: "governance.nhi_lifecycle", column: "sponsor_ref",
			prepare: admin("POST", "/v1/m/governance/agents", func(s fenceSubject) map[string]any {
				return map[string]any{"identity_ref": "fence-agent-" + s.id.String(), "sponsor_ref": s.externalID}
			})},
		{name: "a spend cap on the account", kind: "core.policy", column: "spec",
			prepare: func(e *consentEstate, s fenceSubject) func() consentResp {
				gateway := e.spendLimitGateway()
				return func() consentResp { return e.putSpendCap(gateway, s.id) }
			}},
	}
}

// fenceMember provisions the account a fenced writer names on the consent
// estate: an administrator of T onboarded with a password, known to the
// directory by an external id that T's roster lists as a person, and signed in
// with a stepped-up session, so a writer may also be the account itself.
func (e *consentEstate) fenceMember(label string) fenceSubject {
	e.t.Helper()
	email := label + "@fence.test"
	ext := "ext-" + label
	id := e.onboard(e.tT, email, "admin")
	if r := e.scim("PATCH", "/v1/scim/v2/Users/"+id.String(), e.scimToken(e.tT),
		`{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"replace","path":"externalId","value":"`+ext+`"}]}`); r.code != http.StatusOK {
		e.t.Fatalf("give %s its external id = %d %s", email, r.code, r.raw)
	}
	e.rosterHuman(e.tT, ext)
	token := e.login(email, consentMemberPassword)
	stepUpCommunicationHTTPTestUser(e.t, e.eng, token)
	return fenceSubject{id: id, email: email, externalID: ext, token: token}
}

// fenceSubjectFor provisions the account w names: its own subject, or the
// estate's default one.
func (e *consentEstate) fenceSubjectFor(w fenceWriter, label string) fenceSubject {
	e.t.Helper()
	if w.messaging != (e.comm != nil) {
		e.t.Fatalf("%s runs on the other estate", w.name)
	}
	switch {
	case w.subject != nil:
		return w.subject(e, label)
	case e.comm != nil:
		return e.messagingFenceSubject(label)
	}
	return e.fenceMember(label)
}

// spendLimitGateway is the apps gateway's spend-limit administration over the
// composition's own finops module, answering for T.
func (e *consentEstate) spendLimitGateway() http.Handler {
	e.t.Helper()
	spend, ok := e.eng.finops.(spendLimitAdmin)
	if !ok {
		e.t.Fatal("the composition's finops module administers no spend limits")
	}
	mux := http.NewServeMux()
	mountAppsGatewayHandlers(mux, newAppsGatewayHandler(inferenceProxyConfig{}, e.tT, e.eng.authr, nil, spend, nil, time.Now, "test"))
	return mux
}

// putSpendCap sets a monthly spend cap on user through the gateway, as the
// deployment's administrator.
func (e *consentEstate) putSpendCap(gateway http.Handler, user model.ID) consentResp {
	e.t.Helper()
	body := `{"scope":{"type":"user","user_id":"user:` + user.String() + `"},"amount":"100","period":"monthly"}`
	req := httptest.NewRequest(http.MethodPost, appsGatewaySpendLimitPath, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+e.admin)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	gateway.ServeHTTP(rec, req)
	out := consentResp{code: rec.Code, raw: rec.Body.String()}
	_ = json.Unmarshal(rec.Body.Bytes(), &out.body)
	return out
}

// admissionLock is the statement that takes the deployment's directory
// admission, as the directory writer takes it.
const admissionLock = `SELECT pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended('core.directory.writer', 0))`

// observeAdmission holds the deployment's directory admission in a separate
// PostgreSQL transaction until release is closed.
func observeAdmission(t *testing.T, dsn string, held chan<- struct{}, release <-chan struct{}) {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Errorf("open observer: %v", err)
		close(held)
		return
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Errorf("begin observer: %v", err)
		close(held)
		return
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, admissionLock); err != nil {
		t.Errorf("take directory admission: %v", err)
	}
	close(held)
	<-release
}

// waitsForAdmission reports whether another backend is observed waiting on an
// advisory lock while done stays empty: the writer under test is parked on the
// directory admission the observer holds. It answers false as soon as done has
// a result, which means the writer finished without waiting.
func waitsForAdmission(t *testing.T, dsn string, done <-chan consentResp) (bool, consentResp) {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open the lock reader: %v", err)
	}
	defer func() { _ = db.Close() }()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case r := <-done:
			return false, r
		default:
		}
		var waiting int
		if err := db.QueryRowContext(context.Background(),
			`SELECT count(*) FROM pg_catalog.pg_locks WHERE locktype = 'advisory' AND NOT granted`).Scan(&waiting); err != nil {
			t.Fatalf("read the lock table: %v", err)
		}
		if waiting > 0 {
			return true, consentResp{}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the writer neither finished nor waited on the directory admission")
	return false, consentResp{}
}

// TestFencedWritersAndAccountWritersNeverDeadlock: every fenced writer takes the
// deployment's directory admission before its write, so on PostgreSQL it is
// observed waiting on it while another transaction holds it; fenced writers
// interleaved with offboards, retirement steps and account updates all
// complete, concurrently on PostgreSQL and serialized by the single writer on
// SQLite; and a transaction that pins authority before admission is refused on
// both engines. The communication writers run on an estate whose communication
// kernel is activated.
func TestFencedWritersAndAccountWritersNeverDeadlock(t *testing.T) {
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer collector.Close()
	// The subscription writer's endpoint is this loopback collector: the egress
	// policy allows its port, and the loopback posture lets the endpoint check
	// admit plain http, or the writer is refused before it reaches the fence.
	t.Setenv(envEventingEgressPolicy, writeLoopbackEgressPolicy(t, collector.URL))
	t.Setenv(eventingAllowLoopbackEnv, "1")
	writers := fenceWriters(collector.URL)
	onConsentEngines(t, func(t *testing.T, e *consentEstate) {
		if e.engine == "postgres" {
			t.Run("every writer waits for directory admission", func(t *testing.T) {
				waitForAdmissionEach(t, e, writers)
			})
		}

		t.Run("interleaved with offboards, retirement steps and account updates", func(t *testing.T) {
			interleaveWithRemovals(t, e.forSubtest(t), writers)
		})

		t.Run("the communication writers", func(t *testing.T) {
			m := bootMessagingEstate(t, e.engine)
			communication := messagingFenceWriters()
			if e.engine == "postgres" {
				t.Run("every writer waits for directory admission", func(t *testing.T) {
					waitForAdmissionEach(t, m, communication)
				})
			}
			t.Run("interleaved with offboards, retirement steps and account updates", func(t *testing.T) {
				interleaveWithRemovals(t, m.forSubtest(t), communication)
			})
		})

		t.Run("authority pinned before admission is refused", func(t *testing.T) {
			e := e.forSubtest(t)
			ctx := context.Background()
			v := e.createUser("order@fence.test")
			refs := e.authorityRefs(v)
			err := e.eng.store.Mutate(ctx, e.tT, func(sc store.Scope) error {
				fact, err := directoryEpochFact(ctx, sc)
				if err != nil {
					return err
				}
				bundler, ok := sc.(store.AuthoritySnapshotBundleLocker)
				if !ok {
					return errors.New("the scope exposes no bundle locker")
				}
				if err := bundler.LockAuthoritySnapshotBundle(ctx, store.AuthoritySnapshotBundle{
					Facts: []store.AuthorizationFactRef{fact}, UserAuthorities: refs,
				}); err != nil {
					return err
				}
				_, err = sc.Identities().Create(ctx, model.Identity{Name: "after-bundle", Kind: "db_role", ExternalID: "order-1"})
				return err
			})
			if err == nil || !strings.Contains(err.Error(), "directory writer must precede authority row locks") {
				t.Errorf("a directory write after an authority pin = %v, want the order refusal", err)
			}
		})
	})
}

// waitForAdmissionEach runs each writer on the PostgreSQL estate e while another
// transaction holds the deployment's directory admission: the writer must be
// observed waiting on it, and must complete once it is released.
func waitForAdmissionEach(t *testing.T, e *consentEstate, writers []fenceWriter) {
	t.Helper()
	appDSN := consentAppDSN(t, e)
	for _, w := range writers {
		t.Run(w.name, func(t *testing.T) {
			e := e.forSubtest(t)
			write := w.prepare(e, e.fenceSubjectFor(w, strings.ReplaceAll(w.name, " ", "-")))
			held := make(chan struct{})
			release := make(chan struct{})
			var observer sync.WaitGroup
			observer.Add(1)
			go func() {
				defer observer.Done()
				observeAdmission(t, appDSN, held, release)
			}()
			<-held
			done := make(chan consentResp, 1)
			go func() { done <- write() }()
			waited, early := waitsForAdmission(t, appDSN, done)
			close(release)
			observer.Wait()
			if !waited {
				t.Fatalf("%s finished (%d %s) while another transaction held directory admission", w.name, early.code, early.raw)
			}
			if r := <-done; r.code >= 300 {
				t.Errorf("%s after admission was released = %d %s", w.name, r.code, r.raw)
			}
		})
	}
}

// interleaveWithRemovals runs, for four accounts that are members of T and B,
// every writer naming the account together with its offboard from T, an
// account update through B and a retirement pass: concurrently on PostgreSQL,
// and one after another on SQLite, whose single writer serializes them, writers
// first for one account and last for the next. No operation may fail with a
// server error, and none may deadlock.
func interleaveWithRemovals(t *testing.T, e *consentEstate, writers []fenceWriter) {
	t.Helper()
	scimT, scimB := e.scimToken(e.tT), e.scimToken(e.tB)
	failures := make(chan string, 256)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		label := fmt.Sprintf("interleave-%d", i)
		var s fenceSubject
		if e.comm != nil {
			s = e.messagingFenceSubject(label)
		} else {
			s = e.fenceMember(label)
		}
		v := s.id
		e.seedMembership(v, e.tB, "viewer")
		writes := make([]func() consentResp, len(writers))
		for j, w := range writers {
			writes[j] = w.prepare(e, s)
		}
		ops := []func(){
			func() {
				for j, write := range writes {
					if r := write(); r.code >= 500 {
						failures <- writers[j].name + ": " + r.raw
					}
				}
			},
			func() {
				if r := e.scim("DELETE", "/v1/scim/v2/Users/"+v.String(), scimT, ""); r.code >= 500 {
					failures <- "offboard: " + r.raw
				}
			},
			func() {
				if r := e.scim("PATCH", "/v1/scim/v2/Users/"+v.String(), scimB,
					`{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"replace","path":"displayName","value":"x"}]}`); r.code >= 500 {
					failures <- "account update: " + r.raw
				}
			},
			func() {
				worker := &retirementPump{authr: e.eng.retirementPump.authr, modules: e.eng.retirementPump.modules}
				advancePumpClock(worker)
				if _, err := worker.runOnce(context.Background()); err != nil {
					failures <- "retirement step: " + err.Error()
				}
			},
		}
		if e.engine != "postgres" {
			if i%2 == 1 {
				ops[0], ops[1] = ops[1], ops[0]
			}
			for _, op := range ops {
				op()
			}
			continue
		}
		for _, op := range ops {
			wg.Add(1)
			go func() {
				defer wg.Done()
				op()
			}()
		}
	}
	wg.Wait()
	close(failures)
	for f := range failures {
		t.Errorf("an interleaved writer failed: %s", f)
		if strings.Contains(f, "40P01") || strings.Contains(strings.ToLower(f), "deadlock") {
			t.Errorf("a deadlock was reported")
		}
	}
}

// deliveryNaming is a raw MessageDelivery row whose recipient is user; it names a
// Message that does not exist, so only the write seam is under test.
func deliveryNaming(workspace, user model.ID) model.Record {
	return model.Record{
		"workspace_id": workspace.String(), "message_id": model.NewID().String(),
		"recipient_kind": "user", "recipient_ref": user.String(), "recipient_epoch": int64(1),
		"delivery_seq": int64(1), "required": false, "route_reasons_json": `["direct"]`,
		"wake_policy": "none", "state": "available",
		"available_at": model.NewTimestamp(time.Now().Add(time.Minute)).String(),
	}
}

// TestOnlyTheDeliveryRecipientsPassTheSeamUnderTheDirectoryFact: the write seam
// admits an unpinned account reference only in the two membership-bound delivery
// columns, and only in a transaction that locked the tenant's directory fact; a
// member-bound declaration anywhere else leaves the composition unready.
func TestOnlyTheDeliveryRecipientsPassTheSeamUnderTheDirectoryFact(t *testing.T) {
	onConsentEngines(t, func(t *testing.T, e *consentEstate) {
		ctx := context.Background()
		v := e.createUser("member-bound@consent.test")
		e.seedMembership(v, e.tT, "viewer")
		workspace := e.defaultWorkspace(e.tT)
		write := func(kind model.Kind, rec model.Record, directoryFact bool) error {
			return e.eng.store.Mutate(ctx, e.tT, func(sc store.Scope) error {
				if directoryFact {
					fact, err := directoryEpochFact(ctx, sc)
					if err != nil {
						return err
					}
					locker, ok := sc.(store.AuthoritySnapshotLocker)
					if !ok {
						return errors.New("the scope exposes no authority snapshot locker")
					}
					if err := locker.LockAuthoritySnapshot(ctx, []store.AuthorizationFactRef{fact}); err != nil {
						return err
					}
				}
				repo, err := sc.Ext(kind)
				if err != nil {
					return err
				}
				_, err = repo.Create(ctx, rec)
				return err
			})
		}
		if err := write("sessions.message_delivery", deliveryNaming(workspace, v), false); !errors.Is(err, store.ErrUserAuthorityNotHeld) {
			t.Errorf("a delivery naming an account without the directory fact = %v, want the not-held refusal", err)
		}
		if err := write("sessions.message_delivery", deliveryNaming(workspace, v), true); errors.Is(err, store.ErrUserAuthorityNotHeld) {
			t.Errorf("a delivery naming an account under the directory fact was refused by the seam: %v", err)
		}
		if err := write("sourcescope.binding", userBinding("member-bound-control", v.String()), true); !errors.Is(err, store.ErrUserAuthorityNotHeld) {
			t.Errorf("an unrelated counted column under the directory fact alone = %v, want the not-held refusal", err)
		}
	})

	t.Run("a member-bound declaration elsewhere leaves the composition unready", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		fixture := model.EntityDescriptor{
			Kind: "censusfx.memberbound", Table: "censusfx_memberbound",
			Fields: []model.FieldSpec{{
				Name: "owner", Kind: model.KindText,
				Principal: model.Ref(model.EncodeUserID, model.ClassAuthority).BoundToMembers(),
			}},
		}
		st, err := coreengine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"},
			func(reg store.ExtensionRegistry) error { return reg.Register(fixture) })
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = st.Close() }()
		census, ok := st.(store.CompositionCensus)
		if !ok {
			t.Fatal("the store exposes no census")
		}
		found := false
		for _, c := range census.CompositionReadiness().Causes {
			if strings.Contains(c, "censusfx.memberbound.owner") && strings.Contains(c, "member-bound") {
				found = true
			}
		}
		if !found {
			t.Errorf("the readiness verdict does not refuse the member-bound fixture: %v", census.CompositionReadiness().Causes)
		}
	})
}

// consentAppDSN returns the application-role DSN of a PostgreSQL estate.
func consentAppDSN(t *testing.T, e *consentEstate) string {
	t.Helper()
	path := e.eng.dataDir + "/store/app.dsn"
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the estate's application DSN: %v", err)
	}
	dsn := strings.TrimSpace(string(b))
	if dsn == "" {
		t.Fatal("the estate's application DSN is empty")
	}
	return dsn
}
