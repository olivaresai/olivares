// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Gate only the first query, after prepareAttempt has taken the physical writer
// lock and read DB time. The other contender uses a DIFFERENT engine handle.
type t0GateVerifier struct {
	AttemptEvidenceVerifier
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (v *t0GateVerifier) VerifyAttemptEvidence(ctx context.Context, sc store.Scope, c EvidenceCheck) (VerifiedAttemptActor, error) {
	if c.Operation == opQuery {
		var err error
		v.once.Do(func() {
			close(v.entered)
			select {
			case <-v.release:
			case <-ctx.Done():
				err = ctx.Err()
			}
		})
		if err != nil {
			return VerifiedAttemptActor{}, err
		}
	}
	return v.AttemptEvidenceVerifier.VerifyAttemptEvidence(ctx, sc, c)
}

type t0EnteredData struct {
	api.ModuleData
	entered chan struct{}
	once    sync.Once
}

func (d *t0EnteredData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	d.once.Do(func() { close(d.entered) })
	return d.ModuleData.Mutate(ctx, tenant, fn)
}

func TestT0BackendTwoHandlesAtomicAdmission(t *testing.T) {
	eachAttemptBackend(t, func(t *testing.T, cfg store.Config) {
		for _, sameIdentity := range []bool{false, true} {
			t.Run(fmt.Sprintf("same_identity_%t", sameIdentity), func(t *testing.T) {
				m1, st1, tenant, _ := openFinCfg(t, cfg)
				st2 := openAttemptStore(t, cfg, New().RegisterSchema)
				t.Cleanup(func() {
					if err := st2.Close(); err != nil {
						t.Error(err)
					}
				})
				if st1 == st2 {
					t.Fatal("not two actual engine handles")
				}
				v1 := t0Wire(m1, tenant)
				req1 := t0Request(t, st1, tenant)
				req1.ReviewAfter = model.NewTimestamp(time.Now().Add(time.Hour))
				t0Policy(t, st1, tenant, policyKindBudget, t0Budget(10, "block"))
				t0Policy(t, st1, tenant, policyKindBudget, t0Budget(10, "throttle"))
				t0Active(t, m1, st1, tenant)
				// Concurrency qualification uses the engines' ACTUAL TransactionNow, not
				// t0Scope's deterministic unit-test instant or the module's process clock.
				m1.UseData(api.NewModuleData(st1))
				v1.approve(req1)
				m2, _ := finModuleOn(t, st2, tenant)
				v2 := &t0Verifier{tenant: tenant, query: true, allowed: map[AttemptRef]AttemptBinding{}}
				WithAttemptEvidenceVerifier(v2)(m2)
				req2 := req1
				if !sameIdentity {
					req2 = t0Fresh(req2)
				}
				v2.approve(req2)
				gate := &t0GateVerifier{AttemptEvidenceVerifier: v1, entered: make(chan struct{}), release: make(chan struct{})}
				WithAttemptEvidenceVerifier(gate)(m1)
				data2 := &t0EnteredData{ModuleData: api.NewModuleData(st2), entered: make(chan struct{})}
				m2.UseData(data2)
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				type outcome struct {
					r   prepareAttemptResult
					err error
				}
				done1, done2 := make(chan outcome, 1), make(chan outcome, 1)
				started := time.Now()
				go func() { r, err := m1.prepareAttempt(ctx, tenant, req1); done1 <- outcome{r, err} }()
				select {
				case <-gate.entered:
				case <-ctx.Done():
					t.Fatal("first contender did not hold lock")
				}
				go func() { r, err := m2.prepareAttempt(ctx, tenant, req2); done2 <- outcome{r, err} }()
				select {
				case <-data2.entered:
				case <-ctx.Done():
					close(gate.release)
					t.Fatal("second contender did not enter Mutate")
				}
				// Both producers have entered, the first still holds the database lock.
				select {
				case early := <-done2:
					close(gate.release)
					<-done1
					t.Fatalf("second completed while first held lock: %+v", early)
				case <-time.After(100 * time.Millisecond):
				}
				v2.mu.Lock()
				checksBefore := len(v2.checks)
				v2.mu.Unlock()
				close(gate.release)
				a, b := <-done1, <-done2
				if checksBefore != 0 {
					t.Fatalf("second queried authority before acquiring first contender's lock: %d", checksBefore)
				}
				if a.err != nil || b.err != nil {
					t.Fatalf("contenders: first=%+v second=%+v", a, b)
				}
				if a.r.Decision != "admitted" || a.r.Attempt == nil {
					t.Fatalf("first: %+v", a)
				}
				if sameIdentity {
					if !b.r.Replayed || b.r.Attempt.ID != a.r.Attempt.ID {
						t.Fatalf("same identity: %+v", b)
					}
				} else if b.r.Decision != "denied" || b.r.Denial.Action != "block" {
					t.Fatalf("capacity oversold: %+v", b)
				}
				if len(attemptRows(t, st2, tenant)) != 1 || len(countReservations(t, st2, tenant)) != 2 {
					t.Fatal("atomic fanout or replay count violated")
				}
				at := a.r.Attempt.AccountingAt.Time()
				if at.Before(started.Add(-time.Second)) || at.After(time.Now().Add(time.Second)) {
					t.Fatalf("not database qualification instant: %v", at)
				}
				if _, err := m2.GetAttempt(ctx, tenant, req1.AttemptRef); err != nil {
					t.Fatal(err)
				}
				for _, row := range countReservations(t, st2, tenant) {
					if row.Int(colResvSeq) != 1 || row.Int(colResvAmount) != 7 {
						t.Fatalf("not original complete children: %v", row)
					}
				}
				t.Logf("backend=%s independent_handles=2 db_accounting_at=%s same_identity=%t parent=1 children=2", cfg.Engine, a.r.Attempt.AccountingAt.String(), sameIdentity)
			})
		}
	})
}

func TestT0BackendRollbackRetryAndUnknownCommit(t *testing.T) {
	eachAttemptBackend(t, func(t *testing.T, cfg store.Config) {
		for _, mode := range []string{"second child failure", "known conflict then success", "conflicts exhausted", "lost commit acknowledgement"} {
			t.Run(mode, func(t *testing.T) {
				m, st, tenant, host := openFinCfg(t, cfg)
				v := t0Wire(m, tenant)
				req := t0Request(t, st, tenant)
				v.approve(req)
				t0Policy(t, st, tenant, policyKindBudget, t0Budget(100, "block"))
				t0Policy(t, st, tenant, policyKindBudget, t0Budget(100, "block"))
				t0Active(t, m, st, tenant)
				real := m.data
				calls := 0
				transactions := 0
				if mode == "lost commit acknowledgement" {
					m.UseData(&lostAckData{ModuleData: real, err: errors.New("fixture acknowledgement lost after real commit")})
				} else {
					m.UseData(t0Data{ModuleData: real, wrap: func(sc store.Scope) store.Scope {
						transactions++
						local := 0
						return t0Scope{Scope: sc, now: baseTime, ext: func(kind model.Kind, repo store.GenericRepo) store.GenericRepo {
							if kind != budgetReservationKind {
								return repo
							}
							return t0Repo{GenericRepo: repo, createID: func(ctx context.Context, id model.ID, row model.Record) (model.Record, error) {
								local++
								calls++
								if local == 2 {
									switch mode {
									case "second child failure":
										return nil, errors.New("fixture second child refused")
									case "known conflict then success":
										if transactions == 1 {
											return nil, store.ErrConflict
										}
									case "conflicts exhausted":
										return nil, store.ErrConflict
									}
								}
								return repo.CreateWithID(ctx, id, row)
							}}
						}}
					}})
				}
				r, err := m.prepareAttempt(context.Background(), tenant, req)
				switch mode {
				case "second child failure":
					if attemptCode(err) != errCodeStoreUnavailable || calls != 2 {
						t.Fatalf("rollback: %+v %v calls=%d", r, err, calls)
					}
					t0NoRows(t, st, tenant)
				case "conflicts exhausted":
					if attemptCode(err) != errCodeConcurrencyExhausted || transactions != 64 {
						t.Fatalf("retry bound: %v transactions=%d", err, transactions)
					}
					t0NoRows(t, st, tenant)
				case "known conflict then success":
					if err != nil || r.Decision != "admitted" || transactions != 2 {
						t.Fatalf("retry: %+v %v transactions=%d", r, err, transactions)
					}
				case "lost commit acknowledgement":
					var ae *AttemptError
					if !errors.As(err, &ae) || ae.Code != errCodeWriteOutcomeUnknown || !ae.MayHaveCommitted || ae.AttemptRef == nil || *ae.AttemptRef != req.AttemptRef || r.Attempt != nil {
						t.Fatalf("unknown commit: %+v %v", r, err)
					}
				}
				m.UseData(real)
				if mode == "known conflict then success" || mode == "lost commit acknowledgement" {
					view, err := m.GetAttempt(context.Background(), tenant, req.AttemptRef)
					if err != nil || len(view.Targets) != 2 {
						t.Fatalf("durable read: %+v %v", view, err)
					}
					before := countReservations(t, st, tenant)
					replay, err := m.prepareAttempt(context.Background(), tenant, req)
					if err != nil || !replay.Replayed || replay.Attempt.ID != view.ID {
						t.Fatalf("replay: %+v %v", replay, err)
					}
					if len(attemptRows(t, st, tenant)) != 1 || len(before) != 2 || !reflect.DeepEqual(before, countReservations(t, st, tenant)) {
						t.Fatal("uncertain result duplicated or rewrote group")
					}
				}
				host.mu.Lock()
				events := len(host.events)
				host.mu.Unlock()
				if events != 0 {
					t.Fatalf("T0 dispatched or published %d events", events)
				}
			})
		}
	})
}
