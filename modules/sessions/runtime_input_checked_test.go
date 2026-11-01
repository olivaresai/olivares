// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type runtimeInputChecksForTest struct {
	target func(context.Context, store.Scope) error
	send   func(context.Context, store.Scope) error
}

func (c *runtimeInputChecksForTest) BeforeTarget(ctx context.Context, sc store.Scope) error {
	if c.target == nil {
		return ErrRuntimeInputRefused
	}
	return c.target(ctx, sc)
}
func (c *runtimeInputChecksForTest) BeforeSend(ctx context.Context, sc store.Scope) error {
	if c.send == nil {
		return ErrRuntimeInputRefused
	}
	return c.send(ctx, sc)
}
func runtimeInputChecksAllowForTest() *runtimeInputChecksForTest {
	return &runtimeInputChecksForTest{target: func(context.Context, store.Scope) error { return nil }, send: func(context.Context, store.Scope) error { return nil }}
}
func TestRuntimeInputCheckedRequiresChecksAndOriginalDeadline(t *testing.T) {
	f, proc, request := runtimeInputRawFixture(t)
	var nilChecks *runtimeInputChecksForTest
	for _, checks := range []RuntimeInputChecks{nil, nilChecks, &runtimeInputChecksForTest{}} {
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		result, err := f.m.DeliverRuntimeInputChecked(ctx, request, checks)
		cancel()
		if err == nil || result.Attempted || proc.sentCount() != 0 {
			t.Fatalf("missing checks reached transport: %#v %v", result, err)
		}
	}
	for _, duration := range []time.Duration{0, -time.Second, time.Second} {
		ctx := context.Background()
		cancel := func() {}
		if duration != 0 {
			ctx, cancel = context.WithTimeout(ctx, duration)
		}
		result, err := f.m.DeliverRuntimeInputChecked(ctx, request, runtimeInputChecksAllowForTest())
		cancel()
		if err == nil || result.Attempted || proc.sentCount() != 0 {
			t.Fatalf("invalid original deadline %s: %#v %v", duration, result, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	result, err := f.m.DeliverRuntimeInputChecked(ctx, request, runtimeInputChecksAllowForTest())
	if err != nil || !result.Attempted || result.Outcome != RuntimeInputAccepted || proc.sentCount() != 1 {
		t.Fatalf("valid checked input: %#v %v", result, err)
	}
	proc.setSendErrorAfterWrite(errors.New("provider secret must not escape"))
	result, err = checkedRuntimeInputForTest(f.m, request, runtimeInputChecksAllowForTest())
	if !errors.Is(err, ErrRuntimeInputUnknown) || result.Outcome != RuntimeInputUnknown || !result.Attempted || proc.sentCount() != 2 {
		t.Fatalf("unknown after attempt: %#v %v", result, err)
	}
}

// This decorator counts the real transaction and delegates the actual barrier.
// It cannot grant authorization or substitute an in-memory version check.
// Only the exact request context is observed; runtime event/cleanup transactions
// are independent and must not make the one-admission assertion nondeterministic.
type runtimeInputObservedData struct {
	api.ModuleData
	watchMu             sync.Mutex
	watched             context.Context
	mutations, barriers int
	bundle              store.AuthoritySnapshotBundle
	scope               store.Scope
	afterCommit         func()
}

func (d *runtimeInputObservedData) watch(ctx context.Context) {
	d.watchMu.Lock()
	defer d.watchMu.Unlock()
	d.watched = ctx
}

func (d *runtimeInputObservedData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	d.watchMu.Lock()
	watched := ctx == d.watched
	d.watchMu.Unlock()
	if !watched {
		return d.ModuleData.Mutate(ctx, tenant, fn)
	}
	d.mutations++
	err := d.ModuleData.Mutate(ctx, tenant, func(sc store.Scope) error {
		wrapped := &runtimeInputObservedScope{Scope: sc, TransactionClock: sc.(store.TransactionClock), TransactionLocker: sc.(store.TransactionLocker), AuthorizationEpochReader: sc.(store.AuthorizationEpochReader), actual: sc.(store.DirectoryAuthoritySnapshotLocker), owner: d}
		d.scope = wrapped
		return fn(wrapped)
	})
	if err == nil && d.afterCommit != nil {
		d.afterCommit()
	}
	return err
}

type runtimeInputObservedScope struct {
	store.Scope
	store.TransactionClock
	store.TransactionLocker
	store.AuthorizationEpochReader
	actual store.DirectoryAuthoritySnapshotLocker
	owner  *runtimeInputObservedData
}

func (s *runtimeInputObservedScope) LockDirectoryAuthoritySnapshot(ctx context.Context, bundle store.AuthoritySnapshotBundle) error {
	s.owner.barriers++
	s.owner.bundle = bundle
	return s.actual.LockDirectoryAuthoritySnapshot(ctx, bundle)
}
func checkedRuntimeInputForTest(m *Module, request RuntimeInputRequest, checks RuntimeInputChecks) (RuntimeInputResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	return m.DeliverRuntimeInputChecked(ctx, request, checks)
}
func TestRuntimeInputCheckedOneCompleteBarrierAndScopeOrder(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			if _, err := runtimeInputCheckedBarrierRetryingForTest(t, be.config, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// checkedBarrierFault injects one failure into one attempt of the barrier test,
// so the retry rule below is proved against the real DeliverRuntimeInputChecked.
type checkedBarrierFault struct {
	sendEarly     bool // a byte reaches the transport before BeforeTarget: a real order violation
	misorder      bool // a second directory barrier is counted before BeforeTarget: a violation with nothing sent
	spendLease    bool // BeforeTarget returns only after the lease ran out
	unknownCommit bool // COMMIT answers outcome-unknown after the lease ran out
	sendBetween   bool // a byte reaches the transport between BeforeTarget and BeforeSend
	sendUnknown   bool // the send fails after its write and the lease runs out meanwhile
}

const maxCheckedBarrierAttempts = 3

// retryWhileLeaseSpent runs attempt (numbered from 1) until one is not spent. One
// 250ms lease (maxCheckedRuntimeInputLease) covers the run lock, the whole
// authority transaction and its COMMIT. A slow build on PostgreSQL can spend it,
// e.g. inside COMMIT, where the store answers commit-outcome-unknown, the correct
// answer to an expired lease, which says nothing about what the attempt asserts.
// Only such an attempt (see checkedLeaseSpent) is repeated, on a fresh fixture;
// an invariant violation, any other failure, or three spent leases in a row
// fails. A pass therefore always ran the attempt's assertions to the end.
func retryWhileLeaseSpent(t *testing.T, attempt func(n int) (spent bool, err error)) (attempts int, err error) {
	t.Helper()
	for attempts = 1; attempts <= maxCheckedBarrierAttempts; attempts++ {
		spent, err := attempt(attempts)
		if err != nil || !spent {
			return attempts, err
		}
		t.Logf("attempt %d spent the whole lease; retrying on a fresh fixture", attempts)
	}
	return maxCheckedBarrierAttempts, errors.New("three checked deliveries in a row spent the whole lease")
}

// runtimeInputCheckedBarrierRetryingForTest runs the barrier attempt under
// retryWhileLeaseSpent. faults[i] is injected into attempt i+1.
func runtimeInputCheckedBarrierRetryingForTest(t *testing.T, config func(*testing.T) store.Config, faults []checkedBarrierFault) (int, error) {
	t.Helper()
	return retryWhileLeaseSpent(t, func(n int) (bool, error) {
		var fault checkedBarrierFault
		if n <= len(faults) {
			fault = faults[n-1]
		}
		return runtimeInputCheckedBarrierAttemptForTest(t, config(t), fault)
	})
}

// checkedLeaseSpent reports whether a delivery that was not accepted says only
// that the original deadline ran out: the deadline expired, nothing was sent,
// and the outcome is a deadline refusal or an unknown commit with Attempted=false.
// callbackErr is the callbacks' own non-invariant error (BeforeTarget/BeforeSend
// errors reach the caller only as ErrRuntimeInputRefused). The store flattens a
// deadline met while reading the epoch with %v, so that refusal carries only
// ErrAuthorizationEpochUnavailable; with the deadline expired and nothing sent
// it says the same thing.
func checkedLeaseSpent(ctx context.Context, result RuntimeInputResult, err, callbackErr error, sent int) bool {
	if result.Attempted || sent != 0 || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return false
	}
	switch result.Outcome {
	case RuntimeInputUnknown:
		return errors.Is(err, ErrRuntimeInputUnknown) && errors.Is(err, store.ErrCommitOutcomeUnknown)
	case RuntimeInputRefused:
		return errors.Is(err, context.DeadlineExceeded) ||
			errors.Is(err, store.ErrAuthorizationEpochUnavailable) ||
			(errors.Is(err, ErrRuntimeInputRefused) && errors.Is(callbackErr, context.DeadlineExceeded))
	}
	return false
}

// commitUnknownData answers the watched Mutate as a COMMIT whose lease ran out.
type commitUnknownData struct {
	api.ModuleData
	watched context.Context
}

func (d commitUnknownData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	err := d.ModuleData.Mutate(ctx, tenant, fn)
	if err != nil || ctx != d.watched {
		return err
	}
	<-ctx.Done()
	return fmt.Errorf("%w: %w", store.ErrCommitOutcomeUnknown, ctx.Err())
}

// runtimeInputCheckedBarrierAttemptForTest reports spent=true only when the
// original deadline expired before the delivery was accepted and checkedLeaseSpent
// holds; every other failure, and every recorded invariant violation, is err.
func runtimeInputCheckedBarrierAttemptForTest(t *testing.T, cfg store.Config, fault checkedBarrierFault) (bool, error) {
	t.Helper()
	f, proc, request := runtimeInputRawFixtureWith(t, cfg)
	request.Authority = runtimeInputAuthorityWithoutEpochForTest(t, f, request.Target, request.Authority)
	before := runtimeInputClaimVersionForTest(t, f, request.Target)
	original := bytes.Clone(request.Raw)
	p := request.Authority.Entry.Principal
	evidence, ok := p.AuthenticationEvidence()
	if !ok {
		t.Fatal("fixture lacks actual authentication evidence")
	}
	authentication, err := evidence.AuthorityFor(time.Now(), p, f.tenant)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := request.Authority.EntryAuthorization.AuthorityFor(time.Now(), request.Authority.Entry)
	if err != nil {
		t.Fatal(err)
	}
	run, err := request.Authority.RunAuthorization.AuthorityFor(time.Now(), RuntimeInputRunQuestion(p, request.Target))
	if err != nil {
		t.Fatal(err)
	}
	expected, err := auth.MergeAuthoritySnapshotBundles(authentication, entry)
	if err != nil {
		t.Fatal(err)
	}
	expected, err = auth.MergeAuthoritySnapshotBundles(expected, run)
	if err != nil {
		t.Fatal(err)
	}
	var epoch store.AuthorizationFactRef
	if err := f.st.View(context.Background(), f.tenant, func(sc store.Scope) error {
		var err error
		epoch, err = sc.(store.AuthorizationEpochReader).ReadAuthorizationEpoch(context.Background())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	expected, err = auth.MergeAuthoritySnapshotBundles(expected, store.AuthoritySnapshotBundle{Facts: []store.AuthorizationFactRef{epoch}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	observed := &runtimeInputObservedData{ModuleData: f.m.Data, watched: ctx}
	f.m.Data = observed
	if fault.unknownCommit {
		f.m.Data = commitUnknownData{ModuleData: observed, watched: ctx}
	}
	if fault.sendUnknown {
		proc.setSendErrorAfterWrite(errors.New("send failed after its write"))
		proc.setAfterSend(func() { <-ctx.Done() })
	}
	// violations are kept apart from callbackErr: a violated invariant fails the
	// test whatever else happened, deadline included.
	var order, violations []string
	var callbackErr error
	violation := func(msg string) error {
		violations = append(violations, msg)
		return errors.New(msg)
	}
	checks := &runtimeInputChecksForTest{
		target: func(inner context.Context, sc store.Scope) error {
			if fault.spendLease {
				defer func() { <-ctx.Done() }()
			}
			if fault.misorder {
				observed.barriers++
			}
			if fault.sendEarly {
				_ = proc.Send(inner, []byte(`{"type":"early"}`))
			}
			if inner != ctx || sc != observed.scope || observed.barriers != 1 || proc.sentCount() != 0 {
				return violation("wrong pre-target order or scope")
			}
			claim, found, err := findClaim(inner, sc, request.Target.ExpectedSID)
			if err != nil {
				callbackErr = err
				return err
			}
			if !found || claim.Int(model.ColVersion) != before {
				return violation("Claim touched before BeforeTarget")
			}
			order = append(order, "target")
			request.Raw[0] = 'x' // The caller's mutable slice must already have been cloned.
			if fault.sendBetween {
				_ = proc.Send(inner, []byte(`{"type":"early"}`))
			}
			return nil
		},
		send: func(inner context.Context, sc store.Scope) error {
			if inner != ctx || sc != observed.scope || observed.barriers != 1 || proc.sentCount() != 0 {
				return violation("wrong pre-send order or scope")
			}
			claim, found, err := findClaim(inner, sc, request.Target.ExpectedSID)
			if err != nil {
				callbackErr = err
				return err
			}
			if !found || claim.Int(model.ColVersion) <= before {
				return violation("BeforeSend preceded Claim CAS")
			}
			order = append(order, "send")
			return nil
		},
	}
	result, err := f.m.DeliverRuntimeInputChecked(ctx, request, checks)
	if len(violations) != 0 {
		return false, fmt.Errorf("invariant violated: %v (delivery %#v %v)", violations, result, err)
	}
	if err != nil || result.Outcome != RuntimeInputAccepted {
		if checkedLeaseSpent(ctx, result, err, callbackErr, proc.sentCount()) {
			t.Logf("lease spent: %#v %v", result, err)
			return true, nil
		}
		return false, fmt.Errorf("checked delivery: %#v %v (sent %d)", result, err, proc.sentCount())
	}
	if !result.Attempted || proc.sentCount() != 1 {
		return false, fmt.Errorf("checked delivery: %#v %v (sent %d)", result, err, proc.sentCount())
	}
	if observed.mutations != 1 || observed.barriers != 1 || !reflect.DeepEqual(expected, observed.bundle) || !slices.Equal(order, []string{"target", "send"}) {
		return false, fmt.Errorf("complete barrier/order: mutations=%d barriers=%d order=%v", observed.mutations, observed.barriers, order)
	}
	proc.fakeProc.mu.Lock()
	sent := bytes.Clone(proc.fakeProc.sent[0])
	proc.fakeProc.mu.Unlock()
	if !bytes.Equal(sent, original) {
		return false, fmt.Errorf("caller slice changed admitted bytes: %q", sent)
	}
	return false, nil
}

// The barrier test repeats an attempt only when the lease alone was spent. Each
// case runs the real DeliverRuntimeInputChecked with a clean attempt after the
// injected one, so a retry that forgave the fault would pass. A case that must
// fail injects its fault into every attempt: a lease spent before the fault
// could fire (a loaded runner) then cannot turn it into a pass.
func TestRuntimeInputCheckedBarrierRetriesOnlyASpentLease(t *testing.T) {
	sqlite := func(*testing.T) store.Config {
		return store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}
	}
	always := func(f checkedBarrierFault) []checkedBarrierFault { return []checkedBarrierFault{f, f, f} }
	for _, tc := range []struct {
		name     string
		faults   []checkedBarrierFault
		attempts int    // attempts used at least; 0: any
		wantErr  string // empty: the test passes
	}{
		{"deadline refusal retries and passes", []checkedBarrierFault{{spendLease: true}}, 2, ""},
		{"unknown commit retries and passes", []checkedBarrierFault{{unknownCommit: true}}, 2, ""},
		{"three spent leases fail", []checkedBarrierFault{{spendLease: true}, {unknownCommit: true}, {spendLease: true}}, 3, "three checked deliveries in a row spent the whole lease"},
		{"invariant failure at the deadline fails", always(checkedBarrierFault{sendEarly: true, spendLease: true}), 0, "invariant violated"},
		{"invariant failure with nothing sent at the deadline fails", always(checkedBarrierFault{misorder: true, spendLease: true}), 0, "invariant violated"},
		{"invariant failure before the deadline fails", always(checkedBarrierFault{sendEarly: true}), 0, "invariant violated"},
		{"BeforeSend invariant failure fails", always(checkedBarrierFault{sendBetween: true}), 0, "invariant violated"},
		{"bytes sent before the deadline fail", always(checkedBarrierFault{sendBetween: true, spendLease: true}), 0, "checked delivery"},
		{"unknown result after a send fails", always(checkedBarrierFault{sendUnknown: true}), 0, "checked delivery"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attempts, err := runtimeInputCheckedBarrierRetryingForTest(t, sqlite, tc.faults)
			if attempts < tc.attempts {
				t.Fatalf("attempts = %d, want at least %d (err %v)", attempts, tc.attempts, err)
			}
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("unexpected failure: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("error = %v, want one containing %q", err, tc.wantErr)
			}
		})
	}
}

// checkedLeaseSpent forgives a delivery only when every condition of a spent
// lease holds; each row below drops exactly one.
func TestCheckedLeaseSpentNeedsEveryCondition(t *testing.T) {
	expired, cancelExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelExpired()
	canceled, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	other := errors.New("claim read failed")
	unknown := errors.Join(ErrRuntimeInputUnknown, store.ErrCommitOutcomeUnknown)
	// The shape the store returns: the deadline cause is flattened with %v.
	epochUnavailable := fmt.Errorf("%w: read: %v", store.ErrAuthorizationEpochUnavailable, context.DeadlineExceeded)
	for _, tc := range []struct {
		name        string
		ctx         context.Context
		result      RuntimeInputResult
		err         error
		callbackErr error
		sent        int
		want        bool
	}{
		{"unknown commit at the deadline", expired, RuntimeInputResult{Outcome: RuntimeInputUnknown}, unknown, nil, 0, true},
		{"deadline refusal", expired, RuntimeInputResult{Outcome: RuntimeInputRefused}, context.DeadlineExceeded, nil, 0, true},
		{"callback read ran out of time", expired, RuntimeInputResult{Outcome: RuntimeInputRefused}, ErrRuntimeInputRefused, context.DeadlineExceeded, 0, true},
		{"epoch read ran out of time", expired, RuntimeInputResult{Outcome: RuntimeInputRefused}, epochUnavailable, nil, 0, true},
		{"epoch unavailable, deadline not expired", context.Background(), RuntimeInputResult{Outcome: RuntimeInputRefused}, epochUnavailable, nil, 0, false},
		{"epoch unavailable with bytes sent", expired, RuntimeInputResult{Outcome: RuntimeInputRefused}, epochUnavailable, nil, 1, false},
		{"epoch unavailable after an attempt", expired, RuntimeInputResult{Outcome: RuntimeInputRefused, Attempted: true}, epochUnavailable, nil, 0, false},
		{"deadline not expired", context.Background(), RuntimeInputResult{Outcome: RuntimeInputUnknown}, unknown, nil, 0, false},
		{"canceled, not expired", canceled, RuntimeInputResult{Outcome: RuntimeInputUnknown}, unknown, nil, 0, false},
		{"unknown without the commit sentinel", expired, RuntimeInputResult{Outcome: RuntimeInputUnknown}, ErrRuntimeInputUnknown, nil, 0, false},
		{"unknown after an attempt", expired, RuntimeInputResult{Outcome: RuntimeInputUnknown, Attempted: true}, unknown, nil, 0, false},
		{"unknown with bytes sent", expired, RuntimeInputResult{Outcome: RuntimeInputUnknown}, unknown, nil, 1, false},
		{"refusal for another reason", expired, RuntimeInputResult{Outcome: RuntimeInputRefused}, ErrRuntimeInputAuthority, nil, 0, false},
		{"callback refusal without a deadline cause", expired, RuntimeInputResult{Outcome: RuntimeInputRefused}, ErrRuntimeInputRefused, nil, 0, false},
		{"callback refusal from another error", expired, RuntimeInputResult{Outcome: RuntimeInputRefused}, ErrRuntimeInputRefused, other, 0, false},
		{"deadline refusal with bytes sent", expired, RuntimeInputResult{Outcome: RuntimeInputRefused}, context.DeadlineExceeded, nil, 1, false},
		{"unsupported", expired, RuntimeInputResult{Outcome: RuntimeInputUnsupported}, ErrRuntimeInputUnsupported, nil, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := checkedLeaseSpent(tc.ctx, tc.result, tc.err, tc.callbackErr, tc.sent); got != tc.want {
				t.Fatalf("checkedLeaseSpent = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRuntimeInputCheckedCallbackRefusalRollsBackClaim(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			for _, phase := range []string{"target", "send"} {
				t.Run(phase, func(t *testing.T) {
					f, proc, request := runtimeInputRawFixtureWith(t, be.config(t))
					before := runtimeInputClaimVersionForTest(t, f, request.Target)
					calls := 0
					checks := runtimeInputChecksAllowForTest()
					refusal := func(context.Context, store.Scope) error {
						calls++
						return errors.New("private callback detail must not escape")
					}
					if phase == "target" {
						checks.target = refusal
						checks.send = func(context.Context, store.Scope) error { t.Error("BeforeSend ran after refusal"); return nil }
					} else {
						checks.send = refusal
					}
					result, err := checkedRuntimeInputForTest(f.m, request, checks)
					if err != ErrRuntimeInputRefused || result.Attempted || result.Outcome != RuntimeInputRefused || proc.sentCount() != 0 || calls != 1 {
						t.Fatalf("callback refusal: %#v %v calls=%d", result, err, calls)
					}
					if after := runtimeInputClaimVersionForTest(t, f, request.Target); after != before {
						t.Fatalf("refusal committed Claim CAS: %d -> %d", before, after)
					}
				})
			}
		})
	}
}
func TestRuntimeInputCheckedUnknownCommitAndCancellation(t *testing.T) {
	for _, failure := range []string{"unknown-commit", "before-send-cancel", "after-send-cancel"} {
		t.Run(failure, func(t *testing.T) {
			f, proc, request := runtimeInputRawFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			defer cancel()
			checks := runtimeInputChecksAllowForTest()
			switch failure {
			case "unknown-commit":
				f.m.Data = &runtimeInputLostCommitData{ModuleData: f.m.Data}
			case "before-send-cancel":
				checks.send = func(context.Context, store.Scope) error { cancel(); return nil }
			case "after-send-cancel":
				proc.setAfterSend(cancel)
			}
			result, err := f.m.DeliverRuntimeInputChecked(ctx, request, checks)
			if err == nil {
				t.Fatal("injected failure accepted")
			}
			if failure == "after-send-cancel" {
				if result.Outcome != RuntimeInputUnknown || !result.Attempted || proc.sentCount() != 1 || err != ErrRuntimeInputUnknown {
					t.Fatalf("post-send cancellation: %#v %v", result, err)
				}
			} else if result.Attempted || proc.sentCount() != 0 {
				t.Fatalf("pre-send failure wrote: %#v %v", result, err)
			}
			if failure == "unknown-commit" && (result.Outcome != RuntimeInputUnknown || !errors.Is(err, store.ErrCommitOutcomeUnknown)) {
				t.Fatalf("lost commit became refusal: %#v %v", result, err)
			}
		})
	}
}
func TestRuntimeInputCheckedChangedAuthorityAndTarget(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			for _, change := range []string{"credential", "account", "grant", "launch", "sid", "claim", "claim-deadline"} {
				t.Run(change, func(t *testing.T) {
					f, proc, request := runtimeInputRawFixtureWith(t, be.config(t))
					ctx := context.Background()
					p := request.Authority.Entry.Principal
					var err error
					switch change {
					case "credential":
						err = f.authr.RevokeSession(ctx, p, p.CredID)
					case "account":
						admin := f.admin()
						err = f.st.AuthMutate(ctx, func(sc store.AuthScope) error {
							_, err := f.authr.OffboardFromTenant(ctx, sc, admin, p.UserID, f.tenant, "checked input retirement")
							return err
						})
					case "grant":
						err = f.st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
							epoch := sc.(store.AuthorizationEpochStore)
							fact, err := epoch.ReadAuthorizationEpoch(ctx)
							if err != nil {
								return err
							}
							_, err = epoch.BumpAuthorizationEpoch(ctx, fact)
							return err
						})
					case "launch":
						err = mutateRunForWorkTest(f.m, f.tenant, request.Target.RunRef, func(row model.Record) { row[colRuntimeLaunchID] = model.NewID().String() })
					case "sid":
						request.Target.ExpectedSID = newSID()
					case "claim":
						live, _ := f.m.rt.getLive(f.tenant, request.Target.RunRef)
						err = f.m.Release(ctx, f.tenant, live.claim.SID, live.claim.Holder, live.claim.Fence)
					case "claim-deadline":
						err = f.st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
							rec, found, err := findClaim(ctx, sc, request.Target.ExpectedSID)
							if err != nil {
								return err
							}
							if !found {
								return errors.New("missing Claim")
							}
							rec[colLeaseExpires] = model.NewTimestamp(time.Now().Add(100 * time.Millisecond)).String()
							repo, err := sc.Ext(claimKind)
							if err != nil {
								return err
							}
							_, err = repo.Update(ctx, rec)
							return err
						})
					}
					if err != nil {
						t.Fatal(err)
					}
					result, err := checkedRuntimeInputForTest(f.m, request, runtimeInputChecksAllowForTest())
					if err == nil || result.Attempted || proc.sentCount() != 0 {
						t.Fatalf("changed %s crossed: %#v %v", change, result, err)
					}
				})
			}
		})
	}
}
func TestRuntimeInputCheckedPayloadAndUnsupportedKeepClaimUntouched(t *testing.T) {
	f, proc, request := runtimeInputRawFixture(t)
	before := runtimeInputClaimVersionForTest(t, f, request.Target)
	for _, change := range []func(*RuntimeInputRequest){
		func(r *RuntimeInputRequest) { r.Raw = []byte("one\ntwo") },
		func(r *RuntimeInputRequest) { r.Raw = []byte(strings.Repeat("x", maxWorkTextInputBytes+1)) },
		func(r *RuntimeInputRequest) { r.Mode = RuntimeInputText; r.Raw = nil; r.Text = "requires owned driver" },
		func(r *RuntimeInputRequest) { r.Mode = "unknown" },
	} {
		bad := request
		change(&bad)
		result, err := checkedRuntimeInputForTest(f.m, bad, runtimeInputChecksAllowForTest())
		if err == nil || result.Attempted || proc.sentCount() != 0 {
			t.Fatalf("invalid payload crossed: %#v %v", result, err)
		}
		if after := runtimeInputClaimVersionForTest(t, f, request.Target); after != before {
			t.Fatalf("invalid input touched Claim: %d -> %d", before, after)
		}
	}
	if err := mutateRunForWorkTest(f.m, f.tenant, request.Target.RunRef, func(row model.Record) { row[colTransport] = string(TransportRemoteControl) }); err != nil {
		t.Fatal(err)
	}
	result, err := checkedRuntimeInputForTest(f.m, request, runtimeInputChecksAllowForTest())
	if !errors.Is(err, ErrRuntimeInputUnsupported) || result.Attempted || proc.sentCount() != 0 || runtimeInputClaimVersionForTest(t, f, request.Target) != before {
		t.Fatalf("remote transport crossed: %#v %v", result, err)
	}
}

func TestRuntimeInputCheckedWorkGeneration(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			f, runner := newManagedStopWorkFixture(t, be.config(t))
			managed, _, _, _ := f.boundRun(t)
			target := runtimeInputTargetForTest(t, f, managed.RunRef)
			request := RuntimeInputRequest{Target: target, Authority: runtimeInputAuthorityForTest(t, f, target), Mode: RuntimeInputRaw, Raw: []byte(`{"type":"user"}`)}
			for _, change := range []func(*RuntimeInputWork){
				func(w *RuntimeInputWork) { w.LeaseFence++ }, func(w *RuntimeInputWork) { w.OwnerEpoch++ },
				func(w *RuntimeInputWork) { w.HolderAgentID = model.NewID() }, func(w *RuntimeInputWork) { w.HolderSID = newSID() },
				func(w *RuntimeInputWork) { w.WorkItemID = model.NewID() }, func(w *RuntimeInputWork) { w.HolderRunRef = "other" },
				func(w *RuntimeInputWork) { w.DispatchKey[0] ^= 1 }, func(w *RuntimeInputWork) { w.LaunchSpecHash[0] ^= 1 },
				func(w *RuntimeInputWork) {
					w.LeaseExpiresAt = time.Now().Add(100 * time.Millisecond).Format(time.RFC3339Nano)
				},
			} {
				bad := request
				work := *target.Work
				change(&work)
				bad.Target.Work = &work
				result, err := checkedRuntimeInputForTest(f.m, bad, runtimeInputChecksAllowForTest())
				if err == nil || result.Attempted || runner.lastProc().sentCount() != 0 {
					t.Fatalf("changed work generation crossed: %#v %v", result, err)
				}
			}
			checks := runtimeInputChecksAllowForTest()
			checks.target = func(context.Context, store.Scope) error {
				request.Target.Work.LeaseFence++
				request.Target.Work.LaunchSpecHash[0] ^= 1
				return nil
			}
			result, err := checkedRuntimeInputForTest(f.m, request, checks)
			if err != nil || result.Outcome != RuntimeInputAccepted || !result.Attempted || runner.lastProc().sentCount() != 1 {
				t.Fatalf("exact work generation: %#v %v", result, err)
			}
			if countNamedRunEvents(t, f.st, f.tenant, managed.RunRef, workInputAccepted) != 1 {
				t.Fatal("checked work did not retain settlement")
			}
		})
	}
}
func TestRuntimeInputCheckedOwnedDriverAndProfile(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			// The commit is a synchronous write inside the checked section, and a loaded
			// runner can stall it past the production 250ms ceiling. Widen the ceiling for
			// this test only; the seam never narrows it and never reaches a production binary.
			t.Cleanup(SetTestCheckedRuntimeInputCeiling(2 * time.Second))
			f := newManagedStopFixture(t, be.config(t))
			observed := &runtimeInputObservedData{ModuleData: f.m.Data}
			f.m.Data = observed
			dto, live := f.launch("checked-exact-driver")
			target := runtimeInputTargetForTest(t, f, dto.RunRef)
			request := RuntimeInputRequest{Target: target, Authority: runtimeInputAuthorityForTest(t, f, target), Mode: RuntimeInputRaw, Raw: []byte(`{"jsonrpc":"2.0"}`)}
			path := filepath.Join(f.profile.ConfigHome, codexFixtureRecordFile)
			before := readFixtureRecord(t, path)
			version := runtimeInputClaimVersionForTest(t, f, target)
			result, err := checkedRuntimeInputForTest(f.m, request, runtimeInputChecksAllowForTest())
			if !errors.Is(err, ErrRuntimeInputUnsupported) || result.Attempted || runtimeInputClaimVersionForTest(t, f, target) != version {
				t.Fatalf("raw driver request: %#v %v", result, err)
			}
			request.Mode, request.Raw, request.Text = RuntimeInputText, nil, preservedOperatorText
			ctx, cancel := context.WithTimeout(context.Background(), CheckedRuntimeInputCeiling())
			defer cancel()
			observed.watch(ctx)
			result, err = f.m.DeliverRuntimeInputChecked(ctx, request, runtimeInputChecksAllowForTest())
			if err != nil || result.Outcome != RuntimeInputAccepted || !result.Attempted {
				t.Fatalf("owned driver: %#v %v", result, err)
			}
			if observed.mutations != 1 || observed.barriers != 1 {
				t.Fatalf("driver opened a second admission: mutations=%d barriers=%d", observed.mutations, observed.barriers)
			}
			fact, err := f.m.loadRun(context.Background(), f.tenant, target.RunRef)
			if err != nil || !fact.Bool(colRunCodexInputAttempted) {
				t.Fatalf("checked input fact was not committed with admission: %v", err)
			}
			after := readFixtureRecord(t, path)
			if !slices.Equal(after.Methods, append(before.Methods, codexMethodTurnStart)) || !slices.Equal(after.Inputs, []string{preservedOperatorText}) {
				t.Fatalf("driver changed input or sent twice: methods=%v inputs=%q", after.Methods, after.Inputs)
			}
			driver, ok := live.session.(*codexSession)
			if !ok {
				t.Fatal("expected real Codex driver")
			}
			driver.setAuthState(AuthStateRequired)
			result, err = checkedRuntimeInputForTest(f.m, request, runtimeInputChecksAllowForTest())
			if err == nil || result.Attempted {
				t.Fatalf("auth-required driver accepted input: %#v %v", result, err)
			}
			driver.setAuthState(AuthStateReady)
			retired := ProfileRetired
			if _, err := f.m.PatchProfile(context.Background(), f.tenant, f.profile.Ref, ProfilePatch{State: &retired}); err != nil {
				t.Fatal(err)
			}
			result, err = checkedRuntimeInputForTest(f.m, request, runtimeInputChecksAllowForTest())
			if err == nil || result.Attempted || len(readFixtureRecord(t, path).Inputs) != 1 {
				t.Fatalf("retired profile reached driver: %#v %v", result, err)
			}
		})
	}
}
func TestRuntimeInputCheckedOriginalDeadlineWhileRunLocked(t *testing.T) {
	f, proc, request := runtimeInputRawFixture(t)
	release, err := f.m.rt.lockRunContext(context.Background(), liveKey(f.tenant, request.Target.RunRef))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	observed := &runtimeInputObservedData{ModuleData: f.m.Data, watched: ctx}
	f.m.Data = observed
	result, err := f.m.DeliverRuntimeInputChecked(ctx, request, runtimeInputChecksAllowForTest())
	if err == nil || result.Attempted || proc.sentCount() != 0 || observed.mutations != 0 {
		t.Fatalf("expired run-lock wait continued: %#v %v mutations=%d", result, err, observed.mutations)
	}
}
func TestRuntimeInputCheckedOriginalDeadlineWhileStoreBlocked(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			f, proc, request := runtimeInputRawFixtureWith(t, be.config(t))
			held, release := make(chan struct{}), make(chan struct{})
			holderDone := make(chan error, 1)
			var once sync.Once
			unlock := func() { once.Do(func() { close(release) }) }
			defer unlock()
			go func() {
				holderDone <- f.st.Mutate(context.Background(), f.tenant, func(store.Scope) error { close(held); <-release; return nil })
			}()
			select {
			case <-held:
			case err := <-holderDone:
				t.Fatalf("store holder failed: %v", err)
			case <-time.After(5 * time.Second):
				t.Fatal("store holder did not enter")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			type reply struct {
				result RuntimeInputResult
				err    error
			}
			done := make(chan reply, 1)
			go func() {
				result, err := f.m.DeliverRuntimeInputChecked(ctx, request, runtimeInputChecksAllowForTest())
				done <- reply{result, err}
			}()
			var got reply
			select {
			case got = <-done:
			case <-time.After(2 * time.Second):
				unlock()
				got = <-done
				t.Error("store wait failed to honor request cancellation promptly")
			}
			unlock()
			if err := <-holderDone; err != nil {
				t.Fatal(err)
			}
			if got.err == nil || got.result.Attempted || proc.sentCount() != 0 {
				t.Fatalf("expired database wait continued: %#v %v", got.result, got.err)
			}
		})
	}
}

// These waits use the real protocol driver and owned child's actual procProcess.
// The RPC mutex is deliberately released only AFTER expiry: a delayed RETURN is
// permitted, but the original context must still prevent bytes after that wait.
func TestRuntimeInputCheckedOriginalDeadlineAtActualDriverAndPipe(t *testing.T) {
	for _, wait := range []string{"rpc-mutex", "process-write-gate"} {
		t.Run(wait, func(t *testing.T) {
			f := newManagedStopFixture(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true})
			committed := make(chan struct{}, 1)
			observed := &runtimeInputObservedData{ModuleData: f.m.Data, afterCommit: func() { committed <- struct{}{} }}
			f.m.Data = observed
			dto, live := f.launch("checked-blocked-" + wait)
			target := runtimeInputTargetForTest(t, f, dto.RunRef)
			request := RuntimeInputRequest{Target: target, Authority: runtimeInputAuthorityForTest(t, f, target), Mode: RuntimeInputText, Text: "must not cross after original expiry"}
			path := filepath.Join(f.profile.ConfigHome, codexFixtureRecordFile)
			before := readFixtureRecord(t, path)
			var release func()
			switch wait {
			case "rpc-mutex":
				driver, ok := live.session.(*codexSession)
				if !ok {
					t.Fatal("fixture did not use actual protocol driver")
				}
				driver.conn.mu.Lock()
				release = driver.conn.mu.Unlock
			case "process-write-gate":
				proc, ok := live.proc.(*procProcess)
				if !ok {
					t.Fatal("fixture did not use actual process sink")
				}
				gate, err := proc.acquireWrite(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				release = func() { <-gate }
			}
			var once sync.Once
			unlock := func() { once.Do(release) }
			defer unlock()
			ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			defer cancel()
			observed.watch(ctx)
			type reply struct {
				result RuntimeInputResult
				err    error
			}
			done := make(chan reply, 1)
			go func() {
				result, err := f.m.DeliverRuntimeInputChecked(ctx, request, runtimeInputChecksAllowForTest())
				done <- reply{result, err}
			}()
			select {
			case <-committed:
			case got := <-done:
				t.Fatalf("input never reached known commit: %#v %v", got.result, got.err)
			case <-time.After(3 * time.Second):
				t.Fatal("admission never completed")
			}
			<-ctx.Done()
			unlock()
			var got reply
			select {
			case got = <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("driver failed to return after its gate opened")
			}
			if got.err == nil || got.result.Outcome == RuntimeInputAccepted {
				t.Fatalf("expired original context accepted: %#v %v", got.result, got.err)
			}
			// The driver's conservative Attempted=true stays unknown even when this
			// controlled sink recorded no bytes; do not reinterpret it as retryable.
			if got.result.Attempted && got.result.Outcome != RuntimeInputUnknown {
				t.Fatalf("transport attempt lost uncertainty: %#v", got.result)
			}
			// A later acknowledged input orders the same pipe and proves the negative
			// without relying on an immediate filesystem read racing the peer.
			request.Text = preservedOperatorText
			result, err := checkedRuntimeInputForTest(f.m, request, runtimeInputChecksAllowForTest())
			if err != nil || !result.Attempted || result.Outcome != RuntimeInputAccepted {
				t.Fatalf("control input: %#v %v", result, err)
			}
			after := readFixtureRecord(t, path)
			if !slices.Equal(after.Methods, append(before.Methods, codexMethodTurnStart)) || !slices.Equal(after.Inputs, []string{preservedOperatorText}) {
				t.Fatalf("expired bytes crossed actual process: methods=%v inputs=%q", after.Methods, after.Inputs)
			}
		})
	}
}

// The external policy decision cites the real authentication directory facts,
// not authorization_epoch. These are still actual mutation proofs: no authority
// bytes are fabricated. This distinguishes adding the current epoch from merely
// deduplicating one which happened to be present in the retained decision.
func runtimeInputAuthorityWithoutEpochForTest(t *testing.T, f *managedStopFixture, target RuntimeInputTarget, a RuntimeInputAuthority) RuntimeInputAuthority {
	t.Helper()
	p := a.Entry.Principal
	evidence, ok := p.AuthenticationEvidence()
	if !ok {
		t.Fatal("missing real authentication")
	}
	bundle, err := evidence.AuthorityFor(time.Now(), p, f.tenant)
	if err != nil {
		t.Fatal(err)
	}
	for _, fact := range bundle.Facts {
		if fact.Kind == model.AuthorizationEpochKind {
			t.Fatal("authentication unexpectedly includes authorization epoch; fixture no longer discriminates")
		}
	}
	now := time.Now()
	scoped := &communicationAuthorityScopedEvidenceAuthorizer{decision: auth.ScopedEvidenceDecision{Effect: auth.EffectGrant, ResourceGuard: auth.CheckEvidence{Verdict: auth.CheckClean, Code: "resource_guard_clean"}, ForbidAbsence: auth.CheckEvidence{Verdict: auth.CheckClean, Code: "scoped_forbid_absent"}, Facts: bundle.Facts, ObservedAt: now.Add(-time.Second), FreshUntil: now.Add(time.Minute)}}
	az := auth.NewAuthorizer(nil, auth.WithScopedGrants(scoped))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	a.EntryAuthorization, err = az.AuthorizeRouteMutation(ctx, a.Entry)
	if err != nil {
		t.Fatal(err)
	}
	a.RunAuthorization, err = az.AuthorizeRouteMutation(ctx, RuntimeInputRunQuestion(p, target))
	if err != nil {
		t.Fatal(err)
	}
	f.clock.set(time.Now().UTC())
	return a
}

// epochBumpFault shapes one attempt of the epoch-bump test.
type epochBumpFault struct {
	bumpDelay       time.Duration // holds the bump back, as a slow host does
	sendSpendsLease bool          // the send returns only after the lease ran out
}

// runtimeInputCheckedEpochBumpAttemptForTest admits an input that waits on the run
// lock while the authorization epoch is bumped, and asserts the admission pins
// the bumped epoch. The bump commits before the lock is released, so whenever the
// barrier ran its bundle is checked, whatever the outcome. spent=true only when
// the lease ran out and epochAttemptLeaseSpent holds.
func runtimeInputCheckedEpochBumpAttemptForTest(t *testing.T, cfg store.Config, fault epochBumpFault) (bool, error) {
	t.Helper()
	f, proc, request := runtimeInputRawFixtureWith(t, cfg)
	request.Authority = runtimeInputAuthorityWithoutEpochForTest(t, f, request.Target, request.Authority)
	release, err := f.m.rt.lockRunContext(context.Background(), liveKey(f.tenant, request.Target.RunRef))
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	unlock := func() { once.Do(release) }
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	observed := &runtimeInputObservedData{ModuleData: f.m.Data, watched: ctx}
	f.m.Data = observed
	if fault.sendSpendsLease {
		proc.setAfterSend(func() { <-ctx.Done() })
	}
	started := make(chan struct{})
	type reply struct {
		result RuntimeInputResult
		err    error
	}
	done := make(chan reply, 1)
	go func() {
		close(started)
		result, err := f.m.DeliverRuntimeInputChecked(ctx, request, runtimeInputChecksAllowForTest())
		done <- reply{result, err}
	}()
	<-started
	time.Sleep(fault.bumpDelay)
	var bumped store.AuthorizationFactRef
	if err := f.st.Mutate(context.Background(), f.tenant, func(sc store.Scope) error {
		epochs := sc.(store.AuthorizationEpochStore)
		fact, err := epochs.ReadAuthorizationEpoch(context.Background())
		if err != nil {
			return err
		}
		bumped, err = epochs.BumpAuthorizationEpoch(context.Background(), fact)
		return err
	}); err != nil {
		unlock()
		<-done
		t.Fatal(err)
	}
	unlock()
	got := <-done
	spent, err := epochAttemptVerdict(ctx, got.result, got.err, proc.lastSendError(), proc.sentCount(), observed.barriers, observed.bundle, bumped)
	if spent {
		t.Logf("lease spent: %#v %v", got.result, got.err)
	}
	return spent, err
}

// epochAttemptVerdict judges one epoch-bump attempt. The bump commits before the
// run lock is released, so whenever the barrier ran its bundle must pin the bumped
// epoch, whatever the outcome: the pin is checked before any outcome is forgiven.
// spent=true only when no assertion was skipped by a spent lease
// (epochAttemptLeaseSpent).
func epochAttemptVerdict(ctx context.Context, result RuntimeInputResult, err, sendErr error, sent, barriers int, bundle store.AuthoritySnapshotBundle, bumped store.AuthorizationFactRef) (spent bool, _ error) {
	if barriers > 1 {
		return false, fmt.Errorf("fresh admission after epoch bump ran the barrier %d times: %#v %v", barriers, result, err)
	}
	pinned := barriers == 1
	if pinned {
		if perr := epochPinError(bundle, bumped); perr != nil {
			return false, perr
		}
	}
	if err == nil && result.Outcome == RuntimeInputAccepted {
		if sent != 1 || !pinned {
			return false, fmt.Errorf("fresh admission after epoch bump: %#v sent=%d barriers=%d", result, sent, barriers)
		}
		return false, nil
	}
	if epochAttemptLeaseSpent(ctx, result, err, sendErr, sent, pinned) {
		return true, nil
	}
	return false, fmt.Errorf("fresh admission after epoch bump: %#v %v", result, err)
}

// epochPinError reports whether the barrier's bundle pins the epoch the bump committed.
func epochPinError(bundle store.AuthoritySnapshotBundle, bumped store.AuthorizationFactRef) error {
	found := false
	for _, fact := range bundle.Facts {
		if fact.Kind == model.AuthorizationEpochKind {
			if fact != bumped {
				return fmt.Errorf("pinned stale epoch: %#v want %#v", fact, bumped)
			}
			found = true
		}
	}
	if !found {
		return errors.New("complete barrier omitted the epoch committed while input waited")
	}
	return nil
}

// epochAttemptLeaseSpent is checkedLeaseSpent plus the one shape only this attempt
// may call spent: the barrier ran and pinned the bumped epoch (pinned), the send
// went through and returned no error (sendErr nil), and the post-send deadline
// check turned the result unknown.
func epochAttemptLeaseSpent(ctx context.Context, result RuntimeInputResult, err, sendErr error, sent int, pinned bool) bool {
	if checkedLeaseSpent(ctx, result, err, nil, sent) {
		return true
	}
	return pinned && result.Outcome == RuntimeInputUnknown && result.Attempted && sent == 1 && sendErr == nil &&
		errors.Is(ctx.Err(), context.DeadlineExceeded) && errors.Is(err, ErrRuntimeInputUnknown)
}

// runtimeInputCheckedEpochBumpRetryingForTest runs the epoch attempt under
// retryWhileLeaseSpent. faults[i] is injected into attempt i+1.
func runtimeInputCheckedEpochBumpRetryingForTest(t *testing.T, config func(*testing.T) store.Config, faults []epochBumpFault) (int, error) {
	t.Helper()
	return retryWhileLeaseSpent(t, func(n int) (bool, error) {
		var fault epochBumpFault
		if n <= len(faults) {
			fault = faults[n-1]
		}
		return runtimeInputCheckedEpochBumpAttemptForTest(t, config(t), fault)
	})
}

func TestRuntimeInputCheckedEpochBumpDuringRunWait(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			if _, err := runtimeInputCheckedEpochBumpRetryingForTest(t, be.config, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// A bump slower than the lease, or a send that returns after it, costs a retry on
// a fresh fixture, never a skipped epoch assertion: a pass always ran it, and a
// host where every attempt is that slow fails after three. A slower host may spend
// more leases than a row names, so a row bounds the attempts from below.
func TestRuntimeInputCheckedEpochBumpRetriesOnlyASpentLease(t *testing.T) {
	sqlite := func(*testing.T) store.Config {
		return store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true}
	}
	slow := epochBumpFault{bumpDelay: 300 * time.Millisecond}
	late := epochBumpFault{sendSpendsLease: true}
	for _, tc := range []struct {
		name        string
		faults      []epochBumpFault
		minAttempts int
		wantErr     string // empty: the test passes
	}{
		{"fast bump passes", nil, 1, ""},
		{"slow bump retries and passes", []epochBumpFault{slow}, 2, ""},
		{"two slow bumps retry twice and pass", []epochBumpFault{slow, slow}, 3, ""},
		{"three slow bumps fail", []epochBumpFault{slow, slow, slow}, 3, "three checked deliveries in a row spent the whole lease"},
		{"send after the lease retries and passes", []epochBumpFault{late}, 2, ""},
		{"three late sends fail", []epochBumpFault{late, late, late}, 3, "three checked deliveries in a row spent the whole lease"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attempts, err := runtimeInputCheckedEpochBumpRetryingForTest(t, sqlite, tc.faults)
			if attempts < tc.minAttempts {
				t.Fatalf("attempts = %d, want at least %d (err %v)", attempts, tc.minAttempts, err)
			}
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("unexpected failure: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("error = %v, want one containing %q", err, tc.wantErr)
			}
		})
	}
}

// epochAttemptLeaseSpent adds one shape to checkedLeaseSpent; each row drops one
// condition of it.
func TestEpochAttemptLeaseSpentNeedsEveryCondition(t *testing.T) {
	expired, cancelExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelExpired()
	canceled, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	late := RuntimeInputResult{Outcome: RuntimeInputUnknown, Attempted: true}
	for _, tc := range []struct {
		name    string
		ctx     context.Context
		result  RuntimeInputResult
		err     error
		sendErr error
		sent    int
		pinned  bool
		want    bool
	}{
		{"send answered after the deadline", expired, late, ErrRuntimeInputUnknown, nil, 1, true, true},
		{"send failed after its write", expired, late, ErrRuntimeInputUnknown, errors.New("response lost after write"), 1, true, false},
		{"epoch not pinned", expired, late, ErrRuntimeInputUnknown, nil, 1, false, false},
		{"deadline not expired", context.Background(), late, ErrRuntimeInputUnknown, nil, 1, true, false},
		{"canceled, not expired", canceled, late, ErrRuntimeInputUnknown, nil, 1, true, false},
		{"not attempted", expired, RuntimeInputResult{Outcome: RuntimeInputUnknown}, ErrRuntimeInputUnknown, nil, 1, true, false},
		{"nothing sent", expired, late, ErrRuntimeInputUnknown, nil, 0, true, false},
		{"two sends", expired, late, ErrRuntimeInputUnknown, nil, 2, true, false},
		{"refused after an attempt", expired, RuntimeInputResult{Outcome: RuntimeInputRefused, Attempted: true}, ErrRuntimeInputRefused, nil, 1, true, false},
		{"unknown without the sentinel", expired, late, errors.New("send failed"), nil, 1, true, false},
		{"unspent lease rows still apply", expired, RuntimeInputResult{Outcome: RuntimeInputRefused}, context.DeadlineExceeded, nil, 0, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := epochAttemptLeaseSpent(tc.ctx, tc.result, tc.err, tc.sendErr, tc.sent, tc.pinned); got != tc.want {
				t.Fatalf("epochAttemptLeaseSpent = %v, want %v", got, tc.want)
			}
		})
	}
}

// The pin is judged before the outcome: a stale or missing epoch fails whatever
// the outcome was, even one a spent lease would forgive.
func TestEpochAttemptVerdictPinsBeforeForgiving(t *testing.T) {
	expired, cancelExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelExpired()
	bumped := store.AuthorizationFactRef{Kind: model.AuthorizationEpochKind, ID: "epoch", Version: 7}
	stale := store.AuthorizationFactRef{Kind: model.AuthorizationEpochKind, ID: "epoch", Version: 6}
	other := store.AuthorizationFactRef{Kind: model.Kind("other"), ID: "x", Version: 1}
	pins := func(f ...store.AuthorizationFactRef) store.AuthoritySnapshotBundle {
		return store.AuthoritySnapshotBundle{Facts: f}
	}
	accepted := RuntimeInputResult{Outcome: RuntimeInputAccepted, Attempted: true}
	late := RuntimeInputResult{Outcome: RuntimeInputUnknown, Attempted: true}
	for _, tc := range []struct {
		name     string
		result   RuntimeInputResult
		err      error
		sendErr  error
		sent     int
		barriers int
		bundle   store.AuthoritySnapshotBundle
		wantSpnt bool
		wantErr  string // empty: no error
	}{
		{"accepted and pinned", accepted, nil, nil, 1, 1, pins(other, bumped), false, ""},
		{"accepted with a stale pin", accepted, nil, nil, 1, 1, pins(stale), false, "pinned stale epoch"},
		{"accepted without an epoch fact", accepted, nil, nil, 1, 1, pins(other), false, "omitted the epoch"},
		{"accepted without the barrier", accepted, nil, nil, 1, 0, pins(), false, "barriers=0"},
		{"accepted without a send", accepted, nil, nil, 0, 1, pins(bumped), false, "sent=0"},
		{"late send, pinned", late, ErrRuntimeInputUnknown, nil, 1, 1, pins(bumped), true, ""},
		{"late send that failed", late, ErrRuntimeInputUnknown, errors.New("response lost after write"), 1, 1, pins(bumped), false, "fresh admission"},
		{"late send with a stale pin", late, ErrRuntimeInputUnknown, nil, 1, 1, pins(stale), false, "pinned stale epoch"},
		{"late send without an epoch fact", late, ErrRuntimeInputUnknown, nil, 1, 1, pins(other), false, "omitted the epoch"},
		{"refusal before the barrier", RuntimeInputResult{Outcome: RuntimeInputRefused}, context.DeadlineExceeded, nil, 0, 0, pins(), true, ""},
		{"refusal after a stale pin", RuntimeInputResult{Outcome: RuntimeInputRefused}, context.DeadlineExceeded, nil, 0, 1, pins(stale), false, "pinned stale epoch"},
		{"refusal for another reason", RuntimeInputResult{Outcome: RuntimeInputRefused}, ErrRuntimeInputAuthority, nil, 0, 1, pins(bumped), false, "fresh admission"},
		{"barrier ran twice", RuntimeInputResult{Outcome: RuntimeInputRefused}, context.DeadlineExceeded, nil, 0, 2, pins(bumped), false, "2 times"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spent, err := epochAttemptVerdict(expired, tc.result, tc.err, tc.sendErr, tc.sent, tc.barriers, tc.bundle, bumped)
			if spent != tc.wantSpnt {
				t.Fatalf("spent = %v, want %v (err %v)", spent, tc.wantSpnt, err)
			}
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("error = %v, want one containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestRuntimeInputCheckedActualWorkExpiryBeforeAdmission(t *testing.T) {
	for _, be := range managedStopBackends(t) {
		t.Run(be.name, func(t *testing.T) {
			f, runner := newManagedStopWorkFixture(t, be.config(t))
			managed, _, _, lease := f.boundRun(t)
			target := runtimeInputTargetForTest(t, f, managed.RunRef)
			request := RuntimeInputRequest{Target: target, Authority: runtimeInputAuthorityForTest(t, f, target), Mode: RuntimeInputRaw, Raw: []byte(`{"type":"user"}`)}
			ctx := context.Background()
			if err := f.st.Mutate(ctx, f.tenant, func(sc store.Scope) error {
				repo, err := sc.Ext(workLeaseKind)
				if err != nil {
					return err
				}
				row, err := repo.Get(ctx, lease.ID)
				if err != nil {
					return err
				}
				past := time.Now().UTC().Add(-2 * time.Minute)
				row[colLeaseAcquiredAt] = model.NewTimestamp(past).String()
				row[colLeaseRenewedAt] = model.NewTimestamp(past.Add(30 * time.Second)).String()
				row[colLeaseExpiresAt] = model.NewTimestamp(past.Add(time.Minute)).String()
				row[colLeaseRenewalCount] = row.Int(colLeaseRenewalCount) + 1
				_, err = repo.Update(ctx, row)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			result, err := checkedRuntimeInputForTest(f.m, request, runtimeInputChecksAllowForTest())
			if err == nil || result.Attempted || runner.lastProc().sentCount() != 0 {
				t.Fatalf("changed durable WorkLease crossed: %#v %v", result, err)
			}
		})
	}
}
