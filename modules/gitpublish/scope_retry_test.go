// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package gitpublish

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	gp "github.com/olivaresai/olivares/connectors/gitpublish"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// scopeBarrierBound only keeps a serialized engine from deadlocking the barrier (the
// paused transaction waits for the resume the test sends). It never decides a
// verdict: the assertions read the final dispatches and answers.
const scopeBarrierBound = 2 * time.Second

// scopePushGate is the barrier at the fake host. Every push is reported on arrived
// when it reaches the host. A push whose commit is held waits for release,
// ignoring the dispatcher's context, as a host still processing the request
// would; any other push applies at once.
type scopePushGate struct {
	*fakeGit
	held    map[string]bool
	release chan struct{}
	arrived chan string
	mu      sync.Mutex
	commits []string
}

func newScopePushGate(h *harness, held ...string) *scopePushGate {
	g := &scopePushGate{fakeGit: h.git, held: map[string]bool{}, release: make(chan struct{}), arrived: make(chan string, 8)}
	for _, c := range held {
		g.held[c] = true
	}
	h.m.opts.Git = g
	return g
}

func (g *scopePushGate) Push(_ context.Context, r gp.PushRequest) (gp.Result, error) {
	g.mu.Lock()
	g.commits = append(g.commits, r.Commit)
	g.mu.Unlock()
	g.arrived <- r.Commit
	if g.held[r.Commit] {
		<-g.release
	}
	return g.fakeGit.apply(r), nil
}

// dispatched lists the commits that reached the host, in arrival order.
func (g *scopePushGate) dispatched() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.commits...)
}

type scopePushOutcome struct {
	r   Receipt
	err error
}

func pushInScope(ctx context.Context, h *harness, op, ref, commit string) (Receipt, error) {
	return h.m.Push(ctx, h.user(), PushInput{Target: h.target.ID, OperationID: op, Ref: ref, ExpectedOld: shaBase, Commit: commit, Tree: shaTree})
}

// seedNotDispatched records op as a proven no-dispatch: A4 refuses once.
func seedNotDispatched(t *testing.T, h *harness, op, ref, commit string) Intent {
	t.Helper()
	h.authz.recheck = func() error { return auth.ErrRouteDenied }
	r, err := pushInScope(context.Background(), h, op, ref, commit)
	h.authz.recheck = nil
	if r.Intent.State != StateNotDispatched {
		t.Fatalf("setup: %s = %q (%v), want not_dispatched", op, r.Intent.State, err)
	}
	return r.Intent
}

// A is proven not_dispatched. B, a new operation
// for the same target and ref, is then dispatched and held at the host. A is
// retried (same operation, fresh authority) while B is held: B holds the
// conflict scope, so the retry must answer unresolved_intent and dispatch
// nothing.
//   - early decision: B is held before A's retry starts, so A2 can see B.
//   - transactional claim: B is claimed and held after A's A2 and before A's
//     W1, so only the claim transaction can see B.
func TestRearmRefusedWhileScopeHeld(t *testing.T) {
	for _, c := range []struct {
		name        string
		insideClaim bool
	}{
		{"early decision", false},
		{"transactional claim", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			ctx := context.Background()
			const branch = "olivares/retry-held"
			const ref = "refs/heads/" + branch
			h.host.refs[branch] = shaBase
			gate := newScopePushGate(h, shaOther) // B's write is held at the host
			a := seedNotDispatched(t, h, "op-a", ref, shaCommit)

			bDone := make(chan scopePushOutcome, 1)
			bStarted, bReturned := false, false
			defer func() {
				close(gate.release)
				if bStarted && !bReturned {
					<-bDone
				}
			}()
			startB := func() {
				bStarted = true
				go func() {
					r, err := pushInScope(ctx, h, "op-b", ref, shaOther)
					bDone <- scopePushOutcome{r, err}
				}()
				select {
				case got := <-gate.arrived:
					if got != shaOther {
						t.Errorf("setup: first arrival at the host = %s, want B's commit", got)
					}
				case o := <-bDone:
					bReturned = true
					t.Errorf("setup: B returned without dispatching: %+v %v", o.r.Intent, o.err)
				}
			}
			if c.insideClaim {
				fired := false
				h.m.beforeClaim = func() {
					if fired {
						return // B's own W1
					}
					fired = true
					startB()
				}
			} else {
				startB()
			}

			r, err := pushInScope(ctx, h, "op-a", ref, shaCommit)
			host := h.host.ref(branch)
			t.Logf("A=%s attempt 1 not_dispatched; retry while B is held: state=%q attempt=%d err=%v; dispatches=%v; host %s=%s",
				a.ID, r.Intent.State, r.Intent.Attempt, err, gate.dispatched(), branch, host)
			if code := codeOf(err); code != "unresolved_intent" {
				t.Errorf("retry of A while B holds the scope = %q (state %q, attempt %d), want unresolved_intent", code, r.Intent.State, r.Intent.Attempt)
			}
			if d := gate.dispatched(); len(d) != 1 {
				t.Errorf("dispatches while B is held = %v, want only B's", d)
			}
			if host != shaBase {
				t.Errorf("host %s = %s while B is held, want %s: A's write landed", branch, host, shaBase)
			}
		})
	}
}

type scopePauseKey struct{}

// scopePausingData pauses the one claim transaction whose context carries
// scopePauseKey, at its first use of the scope table. In the new-operation claim
// that is lockScope. Pausing before the row is acquired lets another claim
// proceed as far as the store's earlier locks permit.
type scopePausingData struct {
	api.ModuleData
	once    sync.Once
	reached chan struct{}
	resume  chan struct{}
}

func (d *scopePausingData) Mutate(ctx context.Context, tn model.TenantID, fn func(store.Scope) error) error {
	if ctx.Value(scopePauseKey{}) == nil {
		return d.ModuleData.Mutate(ctx, tn, fn)
	}
	return d.ModuleData.Mutate(ctx, tn, func(sc store.Scope) error { return fn(scopePausingScope{Scope: sc, d: d}) })
}

type scopePausingScope struct {
	store.Scope
	d *scopePausingData
}

func (s scopePausingScope) Ext(k model.Kind) (store.GenericRepo, error) {
	if k == kindScope {
		s.d.once.Do(func() {
			close(s.d.reached)
			<-s.d.resume
		})
	}
	return s.Scope.Ext(k)
}

// TransactionNow keeps the claim on the database clock, as without the wrapper.
func (s scopePausingScope) TransactionNow(ctx context.Context) (model.Timestamp, error) {
	if c, ok := s.Scope.(store.TransactionClock); ok {
		return c.TransactionNow(ctx)
	}
	var zero model.Timestamp
	return zero, errors.New("no transaction clock")
}

// newScopeHarnessOn is newHarness on another engine, with fresh org slugs so it can
// share a PostgreSQL database with earlier runs. wrap, when given, edits the
// schema on its way to the registry, to create an earlier schema state.
func newScopeHarnessOn(t *testing.T, cfg store.Config, wrap ...func(store.ExtensionRegistry) store.ExtensionRegistry) *harness {
	t.Helper()
	ctx := context.Background()
	h := &harness{t: t, host: newFakeHost(), authz: &fakeAuthority{deny: map[model.ID]error{}}}
	h.git = &fakeGit{host: h.host, treeFor: map[string]string{shaCommit: shaTree, shaBase: shaTree, shaOther: shaTree}}
	h.custody = &fakeCustody{host: h.host, cbVer: 1, rbVer: 1, owners: []string{"acme"}, approved: map[string]bool{"cb1": true, "rb1": true, "rb2": true}}
	h.m = New(Options{Custody: h.custody, Git: h.git, Authority: h.authz, DispatchTimeout: 2 * time.Second})
	st, err := engine.Open(ctx, cfg, func(reg store.ExtensionRegistry) error {
		for _, w := range wrap {
			reg = w(reg)
		}
		return h.m.RegisterSchema(reg)
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	h.st = st
	n := time.Now().UnixNano()
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, e := sys.EnsureSystemTenant(ctx); e != nil {
			return e
		}
		a, e := sys.CreateOrg(ctx, model.Org{Name: "acme", Slug: fmt.Sprintf("acme-%d", n), Status: model.StatusActive})
		if e != nil {
			return e
		}
		b, e := sys.CreateOrg(ctx, model.Org{Name: "beta", Slug: fmt.Sprintf("beta-%d", n), Status: model.StatusActive})
		if e != nil {
			return e
		}
		h.tenant, h.other = a.TenantID, b.TenantID
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	h.m.UseData(api.NewModuleData(st))
	h.ws = model.NewID()
	h.custody.tenant, h.custody.workspace = h.tenant, h.ws
	tg, err := h.m.CreateTarget(ctx, h.admin(), TargetInput{Workspace: h.ws, CredentialBinding: "cb1", RepositoryBinding: "rb1", PushPrefix: "olivares/", MergeBases: []string{"main"}})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	h.target = tg
	return h
}

func countScopeRows(t *testing.T, h *harness, scope string) int {
	t.Helper()
	var n int
	if err := h.m.data.View(context.Background(), h.tenant, func(sc store.Scope) error {
		recs, err := listAll(context.Background(), sc, kindScope, eq("target_id", h.target.ID.String()), eq("scope_key", scope))
		n = len(recs)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

// The scope row already exists: an earlier operation P took it and was
// proven not dispatched. T2's claim transaction pauses before taking the
// scope row; T1 then runs a whole publish for a new operation in that scope, as
// far as the engine lets it; then T2 continues. Both writes are held at the
// host, so at most one of T1 and T2 may dispatch and the other must answer
// unresolved_intent. SQLite serializes every Mutate on its single connection,
// so there the interleaving is unreachable and a PASS decides nothing; the
// PostgreSQL leg runs when OLIVARES_TEST_POSTGRES_DSN is set.
func TestExistingScopeRowClaimsSerialize(t *testing.T) {
	for _, e := range []struct {
		name string
		open func(t *testing.T) *harness
	}{
		{"sqlite", newHarness},
		{"postgres", func(t *testing.T) *harness {
			dsn := os.Getenv("OLIVARES_TEST_POSTGRES_DSN")
			if dsn == "" {
				t.Skip("PostgreSQL leg: set OLIVARES_TEST_POSTGRES_DSN (no server on this machine)")
			}
			return newScopeHarnessOn(t, store.Config{Engine: store.EnginePostgres, DSN: dsn, MaxConns: 16})
		}},
	} {
		t.Run(e.name, func(t *testing.T) {
			h := e.open(t)
			ctx := context.Background()
			const branch = "olivares/claim-scope"
			const ref = "refs/heads/" + branch
			h.host.refs[branch] = shaBase
			h.git.treeFor[shaMerged] = shaTree
			seedNotDispatched(t, h, "op-p", ref, shaMerged)
			if n := countScopeRows(t, h, "push:"+ref); n != 1 {
				t.Fatalf("setup: scope rows = %d, want the one P left", n)
			}
			who := map[string]string{shaCommit: "T1", shaOther: "T2"}
			gate := newScopePushGate(h, shaCommit, shaOther) // both writes are held at the host
			pd := &scopePausingData{ModuleData: h.m.data, reached: make(chan struct{}), resume: make(chan struct{})}
			h.m.data = pd
			resume := sync.OnceFunc(func() { close(pd.resume) })
			var mu sync.Mutex
			claims := 0
			t1AtClaim := make(chan struct{})
			h.m.beforeClaim = func() {
				mu.Lock()
				defer mu.Unlock()
				if claims++; claims == 2 {
					close(t1AtClaim) // T2 reached W1 first
				}
			}
			done := map[string]chan scopePushOutcome{"T1": make(chan scopePushOutcome, 1), "T2": make(chan scopePushOutcome, 1)}
			started, returned := map[string]bool{}, map[string]bool{}
			start := func(name string, ctx context.Context, op, commit string) {
				started[name] = true
				go func() {
					r, err := pushInScope(ctx, h, op, ref, commit)
					done[name] <- scopePushOutcome{r, err}
				}()
			}
			defer func() {
				resume()
				close(gate.release)
				for name := range started {
					if !returned[name] {
						<-done[name]
					}
				}
			}()
			out := map[string]string{}
			note := func(name string, o scopePushOutcome) {
				returned[name] = true
				if _, ok := out[name]; !ok {
					out[name] = "returned " + codeOf(o.err)
				}
			}

			start("T2", context.WithValue(ctx, scopePauseKey{}, true), "op-t2", shaOther)
			select {
			case <-pd.reached:
			case o := <-done["T2"]:
				note("T2", o)
				t.Fatalf("setup: T2 returned before its scope lock: %+v %v", o.r.Intent, o.err)
			}
			start("T1", ctx, "op-t1", shaCommit)
			path := "T1 held back before W1 while T2's claim transaction was open"
			select {
			case <-t1AtClaim:
				path = "T1 held back inside W1 while T2's claim transaction was open"
				select {
				case c := <-gate.arrived:
					out[who[c]] = "dispatched"
					path = "T1 claimed and dispatched while T2 was paused before its scope lock"
				case o := <-done["T1"]:
					note("T1", o)
					path = "T1 returned from W1 while T2's claim transaction was open"
				case <-time.After(scopeBarrierBound):
				}
			case o := <-done["T1"]:
				note("T1", o)
				path = "T1 returned before W1 while T2's claim transaction was open"
			case <-time.After(scopeBarrierBound):
			}
			resume()
			// Each of T1 and T2 either reaches the host (and stays held) or returns.
			stall := time.After(60 * time.Second)
			for len(out) < 2 {
				select {
				case c := <-gate.arrived:
					out[who[c]] = "dispatched"
				case o := <-done["T1"]:
					note("T1", o)
				case o := <-done["T2"]:
					note("T2", o)
				case <-stall:
					t.Fatalf("no progress after resume: %v (%s)", out, path)
				}
			}
			dispatches := 0
			for _, v := range out {
				if v == "dispatched" {
					dispatches++
				}
			}
			t.Logf("%s: %s; T1=%s T2=%s; host dispatches=%v", e.name, path, out["T1"], out["T2"], gate.dispatched())
			if dispatches != 1 {
				t.Errorf("dispatches in one conflict scope with the writes held = %d, want exactly 1 (%s)", dispatches, path)
			}
			for _, name := range []string{"T1", "T2"} {
				if v := out[name]; v != "dispatched" && v != "returned unresolved_intent" {
					t.Errorf("%s = %s, want dispatched or unresolved_intent", name, v)
				}
			}
		})
	}
}

// B's write went uncertain and a target admin
// abandoned it: the scope stays held until a target admin at AAL3 names B in
// acknowledge_intent. A plain retry of the not_dispatched A by its AAL1
// subject must not re-arm past it while B's write may still land.
func TestRearmRefusedWhileScopeAbandoned(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	const branch = "olivares/retry-abandoned"
	const ref = "refs/heads/" + branch
	h.host.refs[branch] = shaBase
	seedNotDispatched(t, h, "op-a", ref, shaCommit)
	h.m.opts.DispatchTimeout = 50 * time.Millisecond
	hold := make(chan struct{}) // B's write is held at the host and lands only after the test
	defer close(hold)
	h.git.mu.Lock()
	h.git.hold = hold
	h.git.mu.Unlock()
	b, _ := pushInScope(ctx, h, "op-b", ref, shaOther)
	if b.Intent.State != StateUncertain {
		t.Fatalf("setup: B = %q, want uncertain", b.Intent.State)
	}
	if _, err := h.m.Abandon(ctx, h.admin(), b.Intent.ID, "host outage"); err != nil {
		t.Fatalf("setup: abandon B: %v", err)
	}
	h.git.mu.Lock()
	h.git.hold = nil // a new write would not be held
	h.git.mu.Unlock()
	r, err := pushInScope(ctx, h, "op-a", ref, shaCommit)
	t.Logf("retry of A past abandoned B: state=%q attempt=%d err=%v; dispatches=%d; host %s=%s", r.Intent.State, r.Intent.Attempt, err, h.git.count(), branch, h.host.ref(branch))
	if code := codeOf(err); code != "unresolved_intent" {
		t.Errorf("retry of A past the abandoned, unacknowledged B = %q (state %q), want unresolved_intent", code, r.Intent.State)
	}
	if n := h.git.count(); n != 1 {
		t.Errorf("dispatches = %d, want only B's", n)
	}
}
