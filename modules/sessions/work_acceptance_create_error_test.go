// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

var errAcceptanceCriterionCreateTest = errors.New("forced acceptance criterion create failure")

// acceptanceCreateErrorData keeps the real SQLite transaction and every real
// repository operation. It fails only the first Create on the acceptance
// repository, without poisoning SQLite, so a missing return cannot be hidden by
// a driver-side transaction failure.
type acceptanceCreateErrorData struct {
	inner    api.ModuleData
	failures atomic.Int32
	attempts atomic.Int32
}

func (d *acceptanceCreateErrorData) View(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	return d.inner.View(ctx, tenant, fn)
}

func (d *acceptanceCreateErrorData) Mutate(
	ctx context.Context,
	tenant model.TenantID,
	fn func(store.Scope) error,
) error {
	return d.inner.Mutate(ctx, tenant, func(sc store.Scope) error {
		clock, ok := sc.(store.TransactionClock)
		if !ok {
			return errors.New("acceptance create error fixture lacks transaction clock")
		}
		return fn(&acceptanceCreateErrorScope{Scope: sc, clock: clock, data: d})
	})
}

type acceptanceCreateErrorScope struct {
	store.Scope
	clock store.TransactionClock
	data  *acceptanceCreateErrorData
}

func (s *acceptanceCreateErrorScope) TransactionNow(ctx context.Context) (model.Timestamp, error) {
	return s.clock.TransactionNow(ctx)
}

func (s *acceptanceCreateErrorScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	repo, err := s.Scope.Ext(kind)
	if err != nil || kind != workAcceptanceKind {
		return repo, err
	}
	return &acceptanceCreateErrorRepo{GenericRepo: repo, data: s.data}, nil
}

type acceptanceCreateErrorRepo struct {
	store.GenericRepo
	data *acceptanceCreateErrorData
}

func (r *acceptanceCreateErrorRepo) Create(
	ctx context.Context,
	record model.Record,
) (model.Record, error) {
	r.data.attempts.Add(1)
	if r.data.failures.CompareAndSwap(1, 0) {
		return nil, errAcceptanceCriterionCreateTest
	}
	return r.GenericRepo.Create(ctx, record)
}

type acceptanceCreateDurableState struct {
	version            int64
	acceptanceRevision int64
	eventSeq           int64
	criteria           []string
	events             int
	receipts           int
}

func readAcceptanceCreateDurableState(
	t *testing.T,
	f workFixture,
	itemID model.ID,
) acceptanceCreateDurableState {
	t.Helper()
	snapshot, err := f.m.Get(context.Background(), f.tenant, f.principal, itemID)
	if err != nil {
		t.Fatalf("read work snapshot: %v", err)
	}
	criteria := make([]string, len(snapshot.Acceptance))
	for i := range snapshot.Acceptance {
		criteria[i] = string(snapshot.Acceptance[i])
	}
	return acceptanceCreateDurableState{
		version:            snapshot.Item.Version,
		acceptanceRevision: snapshot.Item.AcceptanceRevision,
		eventSeq:           snapshot.Item.LastEventSeq,
		criteria:           criteria,
		events:             workCount(t, f, workEventKind),
		receipts:           workCount(t, f, workCommandKind),
	}
}

func TestWorkAcceptanceAddCreateErrorContract(t *testing.T) {
	t.Parallel()

	f := newWorkFixture(t, ":memory:", nil)
	defer f.st.Close()
	created := applyCreate(t, f, "acceptance create error")
	before := readAcceptanceCreateDurableState(t, f, created.ResultID)

	fault := &acceptanceCreateErrorData{inner: f.m.data}
	fault.failures.Store(1)
	f.m.UseData(fault)
	cmd := WorkCommand{
		Command: "acceptance.add", WorkItemID: created.ResultID,
		Acceptance: []AcceptanceInput{{
			Key: "review", Ordinal: 1,
			Statement: "The change has an independent review", Required: true,
		}},
		ExpectedVersion: created.Version, IdempotencyKey: model.NewID().String(),
		HTTPMethod: http.MethodPost,
	}

	if result, err := f.m.Apply(context.Background(), f.tenant, f.principal, cmd); !errors.Is(err, errAcceptanceCriterionCreateTest) {
		t.Fatalf("acceptance Create failure = %#v, %v; want causal sentinel", result, err)
	}
	if fault.attempts.Load() != 1 {
		t.Fatalf("acceptance Create attempts = %d, want 1", fault.attempts.Load())
	}
	if afterFailure := readAcceptanceCreateDurableState(t, f, created.ResultID); !reflect.DeepEqual(afterFailure, before) {
		t.Fatalf("failed acceptance Create changed durable state: before=%#v after=%#v", before, afterFailure)
	}

	// The failed delivery left no receipt, so the exact command is a fresh apply
	// once the one-shot fault is gone. The next identical delivery is its replay.
	added, err := f.m.Apply(context.Background(), f.tenant, f.principal, cmd)
	if err != nil || added.Replayed || added.ResultID.IsZero() || added.Version != before.version+1 {
		t.Fatalf("healthy retry = %#v, %v", added, err)
	}
	afterAdd := readAcceptanceCreateDurableState(t, f, created.ResultID)
	if afterAdd.version != before.version+1 ||
		afterAdd.acceptanceRevision != before.acceptanceRevision+1 ||
		afterAdd.eventSeq != before.eventSeq+1 || len(afterAdd.criteria) != len(before.criteria)+1 ||
		afterAdd.events != before.events+1 || afterAdd.receipts != before.receipts+1 {
		t.Fatalf("healthy acceptance add state: before=%#v after=%#v", before, afterAdd)
	}
	criterion := workCriterion(t, f, created.ResultID, "review")
	if recordID(criterion) != added.ResultID || criterion.String(colAccState) != "pending" ||
		criterion.String(colAccStatement) != cmd.Acceptance[0].Statement || fault.attempts.Load() != 2 {
		t.Fatalf("healthy acceptance criterion = %#v, result=%#v, attempts=%d", criterion, added, fault.attempts.Load())
	}

	replay, err := f.m.Apply(context.Background(), f.tenant, f.principal, cmd)
	if err != nil || !replay.Replayed || replay.CommandID != added.CommandID ||
		replay.ResultID != added.ResultID || replay.EventID != added.EventID {
		t.Fatalf("exact replay = %#v, %v; original=%#v", replay, err, added)
	}
	if afterReplay := readAcceptanceCreateDurableState(t, f, created.ResultID); !reflect.DeepEqual(afterReplay, afterAdd) {
		t.Fatalf("exact replay changed durable state: before=%#v after=%#v", afterAdd, afterReplay)
	}
	if fault.attempts.Load() != 2 {
		t.Fatalf("exact replay attempted acceptance Create; attempts=%d, want 2", fault.attempts.Load())
	}

	duplicate := cmd
	duplicate.ExpectedVersion = added.Version
	duplicate.IdempotencyKey = model.NewID().String()
	if result, err := f.m.Apply(context.Background(), f.tenant, f.principal, duplicate); err == nil {
		t.Fatalf("duplicate acceptance key applied: %#v", result)
	} else if workErr := asWorkError(err); workErr == nil ||
		workErr.status != http.StatusConflict || workErr.code != "acceptance_duplicate" {
		t.Fatalf("duplicate acceptance key = %v, want 409 acceptance_duplicate", err)
	}
	if afterDuplicate := readAcceptanceCreateDurableState(t, f, created.ResultID); !reflect.DeepEqual(afterDuplicate, afterAdd) {
		t.Fatalf("duplicate changed durable state: before=%#v after=%#v", afterAdd, afterDuplicate)
	}
	if fault.attempts.Load() != 2 {
		t.Fatalf("duplicate reached acceptance Create; attempts=%d, want 2", fault.attempts.Load())
	}
}
