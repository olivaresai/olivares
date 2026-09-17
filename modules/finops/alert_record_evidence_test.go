// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// -----------------------------------------------------------------------------
// A4.2 groups 2, 3, 4 and 7: a real ingestion writes the crossing, its envelope and
// its digest in one insert; an unknown evaluation writes nothing; a storage failure
// aborts everything and publishes nothing; and the stored evidence survives a round
// trip, a later policy change and the dedup guard.
// -----------------------------------------------------------------------------

// alertRows returns the tenant's alert rows.
func alertRows(t testing.TB, st store.Store, tenant model.TenantID) []model.Record {
	t.Helper()
	var out []model.Record
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(budgetAlertKind)
		if err != nil {
			return err
		}
		out, _, err = repo.List(context.Background(), model.Query{Limit: listCap})
		return err
	}); err != nil {
		t.Fatalf("list alerts: %v", err)
	}
	return out
}

// TestProvenBoundIsRecordedWithItsEvidence is A4.2 group 2, the R4 case end to end:
// zero cost, a 12 USD static reserve and a dynamic ledger whose enumeration stopped
// early with valid rows prove at least 12 USD against a 10 USD limit. A real
// ingestion writes ONE alert whose evidence says lower_bound and proven — never an
// exact total of 12 — and the finding is published after the commit.
func TestProvenBoundIsRecordedWithItsEvidence(t *testing.T) {
	m, st, tenant, host := newFin(t)
	m.clock = &fakeClock{t: baseTime}
	id := createBudget(t, st, tenant, "r4-bound", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD,
		ReservedMicroUSD: 12 * oneUSD, Action: "block", Thresholds: []float64{1},
	})
	// The dynamic ledger stops early with nothing observed that contradicts the
	// non-negative invariant, so the floor stands.
	forcePager(m, func(int, model.Query) ([]model.Record, model.Page) {
		return nil, model.Page{HasMore: true}
	})
	// A zero-cost sample still drives an evaluation of this period.
	m.ingest(t, tenant, mkCost("openai", "gpt-x", "s-1", 1, 1, 0, baseTime))

	rows := alertRows(t, st, tenant)
	if len(rows) != 1 {
		t.Fatalf("alert rows = %d, want exactly 1", len(rows))
	}
	row := rows[0]
	ev := interpretAlertEvidence(row, tenant)
	t.Logf("legacy_spend=%d evidence.state=%s class=%s decision=%s", row.Int(colAlertSpend), ev.State, ev.Envelope.Amount.Class, ev.Envelope.Decision.Result)

	if ev.State != evidenceValid {
		t.Fatalf("evidence did not verify: %+v", ev)
	}
	if ev.Envelope.Amount.Class != string(amountLowerBound) {
		t.Fatalf("amount class = %s, want lower_bound: the total was never established", ev.Envelope.Amount.Class)
	}
	if ev.Envelope.Amount.Value == nil || *ev.Envelope.Amount.Value != "12000000" {
		t.Fatalf("amount = %v, want the proven bound 12000000", ev.Envelope.Amount.Value)
	}
	if ev.Envelope.Decision.Result != string(crossingProven) {
		t.Fatalf("decision = %s, want proven: a bound that reaches the target IS a crossing", ev.Envelope.Decision.Result)
	}
	if ev.Envelope.Legacy.ValueKind != legacyValueLowerBound {
		t.Fatalf("legacy kind = %s, want lower_bound: the historical column is not an exact total", ev.Envelope.Legacy.ValueKind)
	}
	if ev.Envelope.BudgetID != id.String() || ev.Envelope.TenantID != tenant.String() || ev.Envelope.AlertID != row.String(model.ColID) {
		t.Fatalf("envelope identity does not bind this row: %+v", ev.Envelope)
	}
	if ev.Envelope.Components.Dynamic.State != string(dynamicNonNegativeUnknown) || ev.Envelope.Components.Dynamic.Value != nil {
		t.Fatalf("dynamic component = %+v, want a non-negative unknown with no value", ev.Envelope.Components.Dynamic)
	}
	// The finding was published after the commit and carries the durable digest.
	findings := host.findings()
	if len(findings) != 1 || findings[0].DetailHash != ev.Digest {
		t.Fatalf("findings = %+v, want one carrying the durable digest %s", findings, ev.Digest)
	}
	if findings[0].Kind != "finops_budget_cap" {
		t.Errorf("kind = %s, want the cap signal preserved for a proven limit crossing", findings[0].Kind)
	}
}

// TestAnUnknownEvaluationWritesNoAlert is A4.2 group 3: a 12 USD cost prefix with a
// page still to come could be cancelled by an unread credit, so it proves nothing
// and no row is written; the same ledger read completely with the credit present is
// exactly 6 USD and does not cross 10.
func TestAnUnknownEvaluationWritesNoAlert(t *testing.T) {
	t.Run("an incomplete cost read writes nothing and reports why", func(t *testing.T) {
		m, st, tenant, host := newFin(t)
		m.clock = &fakeClock{t: baseTime}
		createBudget(t, st, tenant, "prefix", budgetSpec{
			Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD,
			Action: "block", Thresholds: []float64{1},
		})
		// The evaluation runs inside the ingestion's transaction, so the staged read
		// has to cover it: one page of 12 USD with another page still to come.
		forceCostPages(m, func(int, model.Query) ([]model.Record, model.Page) {
			return []model.Record{costSampleRow(tenant, baseTime, 12*oneUSD)}, model.Page{HasMore: true}
		})
		m.ingest(t, tenant, mkCost("openai", "gpt-x", "s-1", 1, 1, 12*oneUSD, baseTime))

		if rows := alertRows(t, st, tenant); len(rows) != 0 {
			t.Fatalf("a signed prefix fabricated %d alert rows", len(rows))
		}
		var diagnostics int
		for _, f := range host.findings() {
			if f.Kind == "finops_budget_evaluation_incomplete" {
				diagnostics++
			}
			if f.Kind == "finops_budget_cap" {
				t.Errorf("an unknown evaluation produced a cap signal")
			}
		}
		if diagnostics != 1 {
			t.Fatalf("incomplete-evaluation findings = %d, want 1", diagnostics)
		}
	})

	t.Run("the same ledger read completely is exact 6 and does not cross", func(t *testing.T) {
		m, st, tenant, host := newFin(t)
		m.clock = &fakeClock{t: baseTime}
		id := createBudget(t, st, tenant, "credited", budgetSpec{
			Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD,
			Action: "block", Thresholds: []float64{1},
		})
		// The 12 USD charge genuinely crosses when it is ingested, and that proven
		// alert is NOT deleted or degraded when the credit arrives afterwards: each
		// row describes the evaluation that produced it.
		m.ingest(t, tenant, mkCost("openai", "gpt-x", "s-1", 1, 1, 12*oneUSD, baseTime))
		crossings := len(alertRows(t, st, tenant))
		if crossings != 1 {
			t.Fatalf("the 12 USD charge produced %d alert rows, want 1", crossings)
		}
		m.ingest(t, tenant, mkCost("openai", "gpt-x", "s-1", 0, 0, -6*oneUSD, baseTime.Add(time.Minute)))

		if rows := alertRows(t, st, tenant); len(rows) != 1 {
			t.Fatalf("alert rows = %d after the credit, want the first row preserved and no new one", len(rows))
		}
		// And the ledger read COMPLETELY is now exactly 6 USD, which does not cross.
		eval := evaluateOne(t, m, tenant, id, baseTime)
		t.Logf("class=%s amount=%s decisions=%+v", eval.Amount.Class, eval.Amount.Decimal(), eval.Thresholds)
		if eval.Amount.Class != amountExact || eval.Amount.Decimal() != "6000000" {
			t.Fatalf("amount = %s/%s, want an exact 6000000", eval.Amount.Class, eval.Amount.Decimal())
		}
		for _, te := range eval.Thresholds {
			if te.Decision.Result != crossingNotReached {
				t.Fatalf("threshold %v = %s, want not_reached at 6 of 10 USD", te.Configured, te.Decision.Result)
			}
		}
		caps := 0
		for _, f := range host.findings() {
			if f.Kind == "finops_budget_cap" {
				caps++
			}
		}
		if caps != 1 {
			t.Fatalf("cap findings = %d, want exactly the one the 12 USD charge proved", caps)
		}
	})

	t.Run("an unresolved group does not borrow the tenant's spend", func(t *testing.T) {
		m, st, tenant, _ := newFin(t)
		m.clock = &fakeClock{t: baseTime}
		missing := model.NewID().String()
		createBudget(t, st, tenant, "group", budgetSpec{
			Dimension: "user_group", Key: missing, Period: "monthly",
			LimitMicroUSD: 10 * oneUSD, Action: "block", Thresholds: []float64{1},
		})
		m.ingest(t, tenant, mkCost("openai", "gpt-x", "s-1", 1, 1, 99*oneUSD, baseTime))

		if rows := alertRows(t, st, tenant); len(rows) != 0 {
			t.Fatalf("an unresolved group produced %d alert rows from tenant-wide spend", len(rows))
		}
	})
}

// failingAlertData makes the alert repository's write fail, so the atomicity of the
// ingestion can be measured rather than assumed.
type failingAlertData struct {
	api ModuleDataShim
	err error
}

// ModuleDataShim is the minimal data interface this fixture wraps.
type ModuleDataShim interface {
	View(context.Context, model.TenantID, func(store.Scope) error) error
	Mutate(context.Context, model.TenantID, func(store.Scope) error) error
}

func (d failingAlertData) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.api.View(ctx, tenant, fn)
}

func (d failingAlertData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.api.Mutate(ctx, tenant, func(sc store.Scope) error {
		return fn(failingAlertScope{Scope: sc, err: d.err})
	})
}

type failingAlertScope struct {
	store.Scope
	err error
}

// LockTransaction forwards the real capability: the wrapper must break the alert
// WRITE it was built to break, and nothing else.
func (s failingAlertScope) LockTransaction(ctx context.Context, key string) error {
	locker, ok := s.Scope.(store.TransactionLocker)
	if !ok {
		return errors.New("finops-test: wrapped scope provides no transaction lock")
	}
	return locker.LockTransaction(ctx, key)
}

func (s failingAlertScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	repo, err := s.Scope.Ext(kind)
	if err != nil || kind != budgetAlertKind {
		return repo, err
	}
	return failingAlertRepo{GenericRepo: repo, err: s.err}, nil
}

type failingAlertRepo struct {
	store.GenericRepo
	err error
}

func (r failingAlertRepo) CreateWithID(context.Context, model.ID, model.Record) (model.Record, error) {
	return nil, r.err
}

// TestAFailedAlertWriteAbortsTheWholeIngestion is A4.2 group 4: the alert row, the
// cost sample and the ledger record share one transaction, so a storage failure at
// the alert leaves NOTHING behind and publishes nothing.
func TestAFailedAlertWriteAbortsTheWholeIngestion(t *testing.T) {
	m, st, tenant, host := newFin(t)
	m.clock = &fakeClock{t: baseTime}
	createBudget(t, st, tenant, "aborting", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: oneUSD,
		Action: "block", Thresholds: []float64{1},
	})
	boom := errors.New("finops-test: alert write failed")
	m.UseData(failingAlertData{api: m.data, err: boom})

	err := m.onCost(context.Background(), tenant, mkCost("openai", "gpt-x", "s-1", 1, 1, 5*oneUSD, baseTime), nil)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the storage failure to abort the ingestion", err)
	}
	if n := countCosts(t, st, tenant); n != 0 {
		t.Fatalf("%d cost records survived an aborted ingestion", n)
	}
	if rows := costSampleRows(t, st, tenant); len(rows) != 0 {
		t.Fatalf("%d cost samples survived an aborted ingestion", len(rows))
	}
	if rows := alertRows(t, st, tenant); len(rows) != 0 {
		t.Fatalf("%d alert rows survived", len(rows))
	}
	if f := host.findings(); len(f) != 0 {
		t.Fatalf("an aborted ingestion published %d findings", len(f))
	}
}

// TestEvidenceSurvivesStorageAndLaterPolicyChanges is A4.2 group 7: the digest
// verifies after a real round trip through the store, a later policy edit does not
// touch the captured evidence, the dedup guard neither rewrites nor re-emits, and a
// missing, unsupported or altered envelope reads as unknown with its own cause.
func TestEvidenceSurvivesStorageAndLaterPolicyChanges(t *testing.T) {
	m, st, tenant, host := newFin(t)
	m.clock = &fakeClock{t: baseTime}
	id := createBudget(t, st, tenant, "durable", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD,
		Action: "block", Thresholds: []float64{1},
	})
	m.ingest(t, tenant, mkCost("openai", "gpt-x", "s-1", 1, 1, 12*oneUSD, baseTime))

	rows := alertRows(t, st, tenant)
	if len(rows) != 1 {
		t.Fatalf("alert rows = %d, want 1", len(rows))
	}
	first := interpretAlertEvidence(rows[0], tenant)
	if first.State != evidenceValid {
		t.Fatalf("evidence did not verify after the round trip: %+v", first)
	}
	if first.Envelope.Amount.Class != string(amountExact) || *first.Envelope.Amount.Value != "12000000" {
		t.Fatalf("amount = %s/%v, want an exact 12000000", first.Envelope.Amount.Class, first.Envelope.Amount.Value)
	}
	capturedVersion := first.Envelope.Policy.Version
	findingsAfterFirst := len(host.findings())

	// The dedup identity already holds this crossing: a second ingestion in the same
	// period neither rewrites the proof nor emits again.
	m.ingest(t, tenant, mkCost("openai", "gpt-x", "s-2", 1, 1, oneUSD, baseTime.Add(time.Minute)))
	rows = alertRows(t, st, tenant)
	if len(rows) != 1 {
		t.Fatalf("alert rows = %d after a second crossing ingest, want the first row only", len(rows))
	}
	again := interpretAlertEvidence(rows[0], tenant)
	if again.Digest != first.Digest || *again.Envelope.Amount.Value != "12000000" {
		t.Fatalf("the first crossing's evidence was rewritten: %s -> %s", first.Digest, again.Digest)
	}
	if len(host.findings()) != findingsAfterFirst {
		t.Fatalf("a deduplicated crossing emitted again")
	}

	// Changing the policy afterwards does not touch the captured evidence.
	if err := st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		p, err := sc.Policies().Get(context.Background(), id)
		if err != nil {
			return err
		}
		spec := parseBudgetSpec(p.Spec)
		spec.LimitMicroUSD = 999 * oneUSD
		p.Spec = spec.toSpecMap()
		_, err = sc.Policies().Update(context.Background(), p)
		return err
	}); err != nil {
		t.Fatalf("update policy: %v", err)
	}
	after := interpretAlertEvidence(alertRows(t, st, tenant)[0], tenant)
	if after.Envelope.Policy.Version != capturedVersion || after.Envelope.Policy.LimitMicroUSD != "10000000" {
		t.Fatalf("the captured policy moved with today's policy: %+v", after.Envelope.Policy)
	}

	// A tampered envelope, an unsupported version and an absent one are unknown,
	// each with its own cause — and none of them repairs itself.
	for _, tc := range []struct {
		name   string
		mutate func(model.Record)
		cause  string
	}{
		{name: "an altered envelope", cause: evidenceCauseHashMismatch, mutate: func(r model.Record) {
			var env alertEvidenceEnvelope
			_ = json.Unmarshal([]byte(r.String(colAlertEvidence)), &env)
			bigger := "999000000"
			env.Amount.Value = &bigger
			body, _ := json.Marshal(env)
			r[colAlertEvidence] = string(body)
		}},
		{name: "an unsupported version", cause: evidenceCauseUnsupported, mutate: func(r model.Record) {
			var env alertEvidenceEnvelope
			_ = json.Unmarshal([]byte(r.String(colAlertEvidence)), &env)
			env.SchemaVersion = 99
			body, _ := json.Marshal(env)
			r[colAlertEvidence] = string(body)
		}},
		{name: "an envelope from another row", cause: evidenceCauseIdentityMismatch, mutate: func(r model.Record) {
			var env alertEvidenceEnvelope
			_ = json.Unmarshal([]byte(r.String(colAlertEvidence)), &env)
			env.AlertID = model.NewID().String()
			body, _ := json.Marshal(env)
			digest, _ := alertEvidenceDigest(env)
			r[colAlertEvidence] = string(body)
			r[colAlertEvidenceHash] = digest
		}},
		{name: "no evidence at all", cause: evidenceCauseLegacyUnversioned, mutate: func(r model.Record) {
			delete(r, colAlertEvidence)
			delete(r, colAlertEvidenceHash)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := model.Record{}
			for k, v := range alertRows(t, st, tenant)[0] {
				row[k] = v
			}
			tc.mutate(row)
			got := interpretAlertEvidence(row, tenant)
			t.Logf("state=%s cause=%s", got.State, got.Cause)
			if got.State != evidenceUnknownRead || got.Cause != tc.cause {
				t.Fatalf("state=%s cause=%s, want unknown/%s", got.State, got.Cause, tc.cause)
			}
			if got.Envelope != nil {
				t.Fatalf("an unverified envelope was still handed back")
			}
		})
	}
}

// TestAlertDTOMarksItsLegacyNumber is part of A4.2 group 8's boundary: the DTO's
// historical number is always classified, and a row without evidence is unverified
// rather than exact.
func TestAlertDTOMarksItsLegacyNumber(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	m.clock = &fakeClock{t: baseTime}
	createBudget(t, st, tenant, "dto", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD,
		Action: "block", Thresholds: []float64{1},
	})
	m.ingest(t, tenant, mkCost("openai", "gpt-x", "s-1", 1, 1, 12*oneUSD, baseTime))

	row := alertRows(t, st, tenant)[0]
	dto := toAlertDTO(row, tenant)
	if dto.ID == "" || dto.LegacyValueKind != legacyValueExact || dto.Evidence.State != evidenceValid {
		t.Fatalf("dto = %+v, want an id, an exact legacy kind and valid evidence", dto)
	}

	legacy := model.Record{}
	for k, v := range row {
		legacy[k] = v
	}
	delete(legacy, colAlertEvidence)
	delete(legacy, colAlertEvidenceHash)
	old := toAlertDTO(legacy, tenant)
	if old.LegacyValueKind != legacyValueUnverified || old.Evidence.State != evidenceUnknownRead {
		t.Fatalf("a row without evidence = %+v, want unverified/unknown", old)
	}
	if old.SpendMicroUSD != row.Int(colAlertSpend) {
		t.Errorf("the historical number was altered by the reader")
	}
}

// -----------------------------------------------------------------------------
// A4.2 CORRECTION. Everything below is a control the independent review named as
// missing: the envelope's SEMANTICS (a digest commits to bytes, not to meaning),
// the binding between the row and the evidence that authorized its numbers, the
// serialized dedup that a PostgreSQL transaction cannot survive without, and the
// ingestion-boundary cases the delivered group 4 did not reach.
// -----------------------------------------------------------------------------

// resealed returns a copy of an alert row whose envelope has been mutated and whose
// digest has been RECOMPUTED over the mutation — the shape a tamper check cannot
// see, and the whole point of validating content.
func resealed(t *testing.T, row model.Record, mutate func(*alertEvidenceEnvelope)) model.Record {
	t.Helper()
	out := model.Record{}
	for k, v := range row {
		out[k] = v
	}
	var env alertEvidenceEnvelope
	if err := json.Unmarshal([]byte(row.String(colAlertEvidence)), &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	mutate(&env)
	body, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("encode envelope: %v", err)
	}
	digest, err := alertEvidenceDigest(env)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	out[colAlertEvidence] = string(body)
	out[colAlertEvidenceHash] = digest
	return out
}

// recordedCrossing ingests one 12 USD charge against a 10 USD limit and returns the
// row it wrote, its module and its store.
func recordedCrossing(t *testing.T) (*Module, store.Store, model.TenantID, *fakeHost, model.Record) {
	t.Helper()
	m, st, tenant, host := newFin(t)
	m.clock = &fakeClock{t: baseTime}
	createBudget(t, st, tenant, "sealed", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD,
		Action: "block", Thresholds: []float64{1},
	})
	m.ingest(t, tenant, mkCost("openai", "gpt-x", "s-1", 1, 1, 12*oneUSD, baseTime))
	rows := alertRows(t, st, tenant)
	if len(rows) != 1 {
		t.Fatalf("alert rows = %d, want 1", len(rows))
	}
	return m, st, tenant, host, rows[0]
}

// TestAConsistentHashDoesNotMakeAnEnvelopeValid is the A4.2-R3 control. Every case
// below carries a digest computed over its OWN bytes, so the hash verifies; what
// fails is what the bytes SAY. The reader must refuse each with its precise cause,
// repair nothing, and consult no policy.
func TestAConsistentHashDoesNotMakeAnEnvelopeValid(t *testing.T) {
	_, _, tenant, _, row := recordedCrossing(t)
	if ev := interpretAlertEvidence(row, tenant); ev.State != evidenceValid {
		t.Fatalf("the control row is not valid to begin with: %+v", ev)
	}

	for _, tc := range []struct {
		name   string
		mutate func(*alertEvidenceEnvelope)
		cause  string
	}{
		{
			// The review's own example: an envelope whose components, context and
			// decision are empty, hashed over that emptiness.
			name:  "an empty envelope with a consistent hash",
			cause: evidenceCauseEnvelopeIncomplete,
			mutate: func(e *alertEvidenceEnvelope) {
				*e = alertEvidenceEnvelope{
					SchemaVersion: alertEvidenceSchemaVersion, DigestVersion: alertEvidenceDigestVersion,
					AlertID: e.AlertID, TenantID: e.TenantID, BudgetID: e.BudgetID,
				}
			},
		},
		{
			name:   "components wiped out",
			cause:  evidenceCauseEnumInvalid,
			mutate: func(e *alertEvidenceEnvelope) { e.Components = alertEvidenceComponents{} },
		},
		{
			name:  "an unknown amount claiming a crossing",
			cause: evidenceCauseInconsistent,
			mutate: func(e *alertEvidenceEnvelope) {
				e.Amount.Class, e.Amount.Value = string(amountUnknown), nil
			},
		},
		{
			name:   "an amount class outside the closed vocabulary",
			cause:  evidenceCauseEnumInvalid,
			mutate: func(e *alertEvidenceEnvelope) { e.Amount.Class = "approximately" },
		},
		{
			name:  "a non-canonical decimal",
			cause: evidenceCauseDecimalNotCanon,
			mutate: func(e *alertEvidenceEnvelope) {
				padded := "012000000"
				e.Amount.Value = &padded
			},
		},
		{
			name:  "an amount that is not the sum of its components",
			cause: evidenceCauseInconsistent,
			mutate: func(e *alertEvidenceEnvelope) {
				bigger := "99000000"
				e.Amount.Value = &bigger
				e.Legacy.SpendMicroUSD = 99 * oneUSD
			},
		},
		{
			name:   "a decision that was never proven",
			cause:  evidenceCauseInconsistent,
			mutate: func(e *alertEvidenceEnvelope) { e.Decision.Result = string(crossingNotReached) },
		},
		{
			name:   "a decision outside the closed vocabulary",
			cause:  evidenceCauseEnumInvalid,
			mutate: func(e *alertEvidenceEnvelope) { e.Decision.Result = "probably" },
		},
		{
			name:  "a target that is not threshold x limit",
			cause: evidenceCauseInconsistent,
			mutate: func(e *alertEvidenceEnvelope) {
				e.Decision.TargetNumerator, e.Decision.TargetDenominator = "1", "1"
				e.Decision.TargetMicroUSD = "1"
			},
		},
		{
			name:   "a legacy kind contradicting the class",
			cause:  evidenceCauseInconsistent,
			mutate: func(e *alertEvidenceEnvelope) { e.Legacy.ValueKind = legacyValueLowerBound },
		},
		{
			name:   "a legacy figure the envelope did not authorize",
			cause:  evidenceCauseInconsistent,
			mutate: func(e *alertEvidenceEnvelope) { e.Legacy.SpendMicroUSD = 1 },
		},
		{
			name:   "a read consistency stronger than the read",
			cause:  evidenceCauseEnumInvalid,
			mutate: func(e *alertEvidenceEnvelope) { e.Context.ReadConsistency = "transactional_snapshot" },
		},
		{
			name:   "a scope that was never resolved",
			cause:  evidenceCauseInconsistent,
			mutate: func(e *alertEvidenceEnvelope) { e.Context.ScopeResolved = false },
		},
		{
			name:   "an unparseable evaluation instant",
			cause:  evidenceCauseEnvelopeIncomplete,
			mutate: func(e *alertEvidenceEnvelope) { e.Context.EvaluatedAt = "yesterday" },
		},
		{
			name:   "an inverted window",
			cause:  evidenceCauseInconsistent,
			mutate: func(e *alertEvidenceEnvelope) { e.Context.WindowEnd = e.Context.WindowStart },
		},
		{
			name:   "a config fault outside the closed vocabulary",
			cause:  evidenceCauseEnumInvalid,
			mutate: func(e *alertEvidenceEnvelope) { e.Policy.ConfigFault = "something_went_wrong" },
		},
		{
			// Two REAL causes in the wrong order. The case used to carry two invented
			// words, so it demonstrated the ordering rule and said nothing about the
			// vocabulary — which is the gap the next case closes.
			name:  "causes that are not the sorted set this writer produces",
			cause: evidenceCauseInconsistent,
			mutate: func(e *alertEvidenceEnvelope) {
				e.Amount.Causes = []string{string(causeStaticUnknown), string(causeDynamicUnknown)}
			},
		},
		{
			name:   "a policy identity that is not this budget's",
			cause:  evidenceCauseInconsistent,
			mutate: func(e *alertEvidenceEnvelope) { e.Policy.ID = model.NewID().String() },
		},

		// ---- A4.2 R3 residual: the relations between policy, components and context.
		// Every case below keeps the sum, the decision, the identity and the row intact,
		// and re-hashes. They are the reviewer's own examples.

		{
			// The envelope states the effective reserve TWICE and may not disagree with
			// itself. This is the reviewer's example: only the policy's reserve moves.
			name:   "an effective reserve the components do not carry",
			cause:  evidenceCauseInconsistent,
			mutate: func(e *alertEvidenceEnvelope) { e.Policy.ReservedMicroUSD = "999" },
		},
		{
			name:   "an effective reserve missing while the static component is known",
			cause:  evidenceCauseInconsistent,
			mutate: func(e *alertEvidenceEnvelope) { e.Policy.ReservedMicroUSD = "" },
		},
		{
			// The reviewer's example: a negative dynamic reserve with the sum preserved
			// by moving the cost. The sum, the decision and the row all still agree.
			name:  "a negative dynamic reserve, with the sum preserved",
			cause: evidenceCauseInconsistent,
			mutate: func(e *alertEvidenceEnvelope) {
				negative, cost := "-1", "12000001"
				e.Components.Dynamic.Value = &negative
				e.Components.Cost.Value = &cost
			},
		},
		{
			name:  "a negative static reserve, with the sum preserved",
			cause: evidenceCauseInconsistent,
			mutate: func(e *alertEvidenceEnvelope) {
				negative, cost := "-1000000", "13000000"
				e.Components.Static.Value = &negative
				e.Policy.ReservedMicroUSD = negative
				e.Components.Cost.Value = &cost
			},
		},
		{
			// A reserve is an int64 at its producer. The cost that balances it is wide
			// and signed, which stays legitimate — the positive controls prove that.
			name:  "a dynamic reserve wider than int64",
			cause: evidenceCauseInconsistent,
			mutate: func(e *alertEvidenceEnvelope) {
				wide, cost := "9223372036854775808", "-9223372036842775808"
				e.Components.Dynamic.Value = &wide
				e.Components.Cost.Value = &cost
			},
		},
		{
			// The reviewer's example: a fault that would have prevented this very
			// crossing, carried beside it. Belonging to the enum is not the question.
			name:   "a configuration fault the crossing rules out",
			cause:  evidenceCauseInconsistent,
			mutate: func(e *alertEvidenceEnvelope) { e.Policy.ConfigFault = string(configFaultStaticNull) },
		},
		{
			// The reviewer's example: a still-increasing interval whose end is not the
			// end of the sample's monthly period.
			name:  "a window end that is not the period's",
			cause: evidenceCauseInconsistent,
			mutate: func(e *alertEvidenceEnvelope) {
				e.Context.WindowEnd = model.NewTimestamp(time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)).String()
			},
		},
		{
			name:  "a window that is not the sample's bucket at all",
			cause: evidenceCauseInconsistent,
			mutate: func(e *alertEvidenceEnvelope) {
				e.Context.WindowStart = model.NewTimestamp(time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)).String()
				e.Context.WindowEnd = model.NewTimestamp(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)).String()
			},
		},
		{
			name:   "a window on a captured period that has none",
			cause:  evidenceCauseInconsistent,
			mutate: func(e *alertEvidenceEnvelope) { e.Policy.Period = "total" },
		},
		{
			name:  "a scope the captured policy does not express",
			cause: evidenceCauseInconsistent,
			mutate: func(e *alertEvidenceEnvelope) {
				e.Context.ScopeColumn, e.Context.ScopeValue = dimensionColumn("provider"), "openai"
			},
		},
		{
			name:  "a captured dimension whose scope cannot be expressed",
			cause: evidenceCauseInconsistent,
			mutate: func(e *alertEvidenceEnvelope) {
				e.Policy.Dimension, e.Policy.Key = "user_group", model.NewID().String()
			},
		},
		{
			// The reviewer's example: ONE unknown word. It was sorted, non-empty and
			// unique, so every structural rule accepted it.
			name:   "a single cause outside the closed vocabulary",
			cause:  evidenceCauseEnumInvalid,
			mutate: func(e *alertEvidenceEnvelope) { e.Amount.Causes = []string{"z_cause"} },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mutated := resealed(t, row, tc.mutate)
			// The hash really does verify: without this the case would only be proving
			// the digest check again.
			var env alertEvidenceEnvelope
			if err := json.Unmarshal([]byte(mutated.String(colAlertEvidence)), &env); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if again, err := alertEvidenceDigest(env); err != nil || again != mutated.String(colAlertEvidenceHash) {
				t.Fatalf("the case did not reseal its digest: %v %v", again, err)
			}
			got := interpretAlertEvidence(mutated, tenant)
			t.Logf("state=%s cause=%s", got.State, got.Cause)
			if got.State != evidenceUnknownRead || got.Cause != tc.cause {
				t.Fatalf("state=%s cause=%s, want unknown/%s", got.State, got.Cause, tc.cause)
			}
			if got.Envelope != nil {
				t.Fatalf("an invalid envelope was still handed back")
			}
			// And the DTO stops calling the row's number exact.
			if dto := toAlertDTO(mutated, tenant); dto.LegacyValueKind != legacyValueUnverified {
				t.Fatalf("legacy_value_kind = %q, want unverified", dto.LegacyValueKind)
			}
		})
	}
}

// TestTheReserveDomainIsNotTheCostDomain is the positive half of the R3 residual.
// The reader now refuses a negative or wider-than-int64 RESERVE — and the same
// numbers are ordinary in the COST component, where a credit is a real credit and a
// total outside int64 keeps its exact wide decimal. If these were refused too, the
// tightening would have broken exactly what A4.1 exists to preserve.
//
// Each case re-hashes and carries the row's legacy cell with it, so what is being
// measured is the envelope's own semantics rather than the row binding.
func TestTheReserveDomainIsNotTheCostDomain(t *testing.T) {
	_, _, tenant, _, row := recordedCrossing(t)

	t.Run("a legitimate credit in the cost component", func(t *testing.T) {
		// cost -1 USD + static 13 USD + dynamic 0 = the same 12 USD, still proven.
		got := interpretAlertEvidence(resealedWithRow(t, row, func(e *alertEvidenceEnvelope) {
			credit, reserve := "-1000000", "13000000"
			e.Components.Cost.Value = &credit
			e.Components.Static.Value = &reserve
			e.Policy.ReservedMicroUSD = reserve
		}), tenant)
		t.Logf("state=%s cause=%s", got.State, got.Cause)
		if got.State != evidenceValid {
			t.Fatalf("a signed cost was refused: %+v", got)
		}
		if got.Envelope.Amount.Value == nil || *got.Envelope.Amount.Value != "12000000" {
			t.Fatalf("the amount moved: %+v", got.Envelope.Amount)
		}
	})

	t.Run("a cost total wider than int64 keeps its exact decimal", func(t *testing.T) {
		// The exact amount does not fit the historical column, so the envelope's own
		// projection is the explicit bound MaxInt64 — and the row carries that.
		wide := "9223372036854775808" // 2^63, one past the int64 range
		got := interpretAlertEvidence(resealedWithRow(t, row, func(e *alertEvidenceEnvelope) {
			e.Components.Cost.Value = &wide
			e.Amount.Value = &wide
			e.Legacy.ValueKind = legacyValueLowerBound
			e.Legacy.SpendMicroUSD = math.MaxInt64
			e.Legacy.Note = "the exact amount is outside int64; the legacy column carries an explicit lower bound"
		}), tenant)
		t.Logf("state=%s cause=%s", got.State, got.Cause)
		if got.State != evidenceValid {
			t.Fatalf("a wide exact cost was refused: %+v", got)
		}
		if got.Envelope.Amount.Class != string(amountExact) || *got.Envelope.Amount.Value != wide {
			t.Fatalf("the wide amount did not survive: %+v", got.Envelope.Amount)
		}
	})

	t.Run("a zero reserve stays a known zero", func(t *testing.T) {
		// The control row itself: reserve zero, stated in both places. A known zero is
		// a figure, not an absence, and the reader must not confuse the two.
		got := interpretAlertEvidence(row, tenant)
		if got.State != evidenceValid {
			t.Fatalf("the control row stopped verifying: %+v", got)
		}
		if got.Envelope.Policy.ReservedMicroUSD != "0" ||
			got.Envelope.Components.Static.State != "known" ||
			*got.Envelope.Components.Static.Value != "0" {
			t.Fatalf("the zero reserve is not stated as a known zero: %+v", got.Envelope.Components.Static)
		}
	})
}

// TestTheContextIsDerivedFromTheCapturedPolicy is the positive half of the context
// relation: the window and the subject must be re-derivable from the captured policy
// and the sample instant, and the shapes that are NOT the control's — a scoped
// dimension, an unbounded period — have to keep verifying through a real ingestion
// and a real storage round trip.
func TestTheContextIsDerivedFromTheCapturedPolicy(t *testing.T) {
	for _, tc := range []struct {
		name       string
		spec       budgetSpec
		wantColumn string
		wantValue  string
		wantWindow bool
	}{
		{
			name: "a provider-scoped monthly budget",
			spec: budgetSpec{
				Dimension: "provider", Key: "openai", Period: "monthly",
				LimitMicroUSD: 10 * oneUSD, Action: "block", Thresholds: []float64{1},
			},
			wantColumn: dimensionColumn("provider"), wantValue: "openai", wantWindow: true,
		},
		{
			name: "a global budget over an unbounded period",
			spec: budgetSpec{
				Dimension: "global", Period: "total",
				LimitMicroUSD: 10 * oneUSD, Action: "block", Thresholds: []float64{1},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, st, tenant, _ := newFin(t)
			m.clock = &fakeClock{t: baseTime}
			createBudget(t, st, tenant, "context-"+uniqueSlugSuffix(t), tc.spec)
			m.ingest(t, tenant, mkCost("openai", "gpt-x", "s-1", 1, 1, 12*oneUSD, baseTime))

			rows := alertRows(t, st, tenant)
			if len(rows) != 1 {
				t.Fatalf("alert rows = %d, want 1", len(rows))
			}
			got := interpretAlertEvidence(rows[0], tenant)
			t.Logf("state=%s cause=%s window=[%s,%s) scope=%s=%s", got.State, got.Cause,
				got.Envelope.Context.WindowStart, got.Envelope.Context.WindowEnd,
				got.Envelope.Context.ScopeColumn, got.Envelope.Context.ScopeValue)
			if got.State != evidenceValid {
				t.Fatalf("a legitimate context was refused: %+v", got)
			}
			if got.Envelope.Context.ScopeColumn != tc.wantColumn || got.Envelope.Context.ScopeValue != tc.wantValue {
				t.Fatalf("scope = %s=%s, want %s=%s", got.Envelope.Context.ScopeColumn,
					got.Envelope.Context.ScopeValue, tc.wantColumn, tc.wantValue)
			}
			if hasWindow := got.Envelope.Context.WindowStart != ""; hasWindow != tc.wantWindow {
				t.Fatalf("window present = %v, want %v", hasWindow, tc.wantWindow)
			}
			// And the causal control beside it: moving the window off the sample's
			// bucket is refused for THIS shape too, not only for the global monthly one.
			if tc.wantWindow {
				off := resealed(t, rows[0], func(e *alertEvidenceEnvelope) {
					e.Context.WindowEnd = model.NewTimestamp(baseTime.AddDate(0, 2, 0)).String()
				})
				if bad := interpretAlertEvidence(off, tenant); bad.State != evidenceUnknownRead || bad.Cause != evidenceCauseInconsistent {
					t.Fatalf("a window off the bucket was accepted: %+v", bad)
				}
			} else {
				off := resealed(t, rows[0], func(e *alertEvidenceEnvelope) {
					e.Context.WindowStart = model.NewTimestamp(baseTime).String()
					e.Context.WindowEnd = model.NewTimestamp(baseTime.AddDate(0, 1, 0)).String()
				})
				if bad := interpretAlertEvidence(off, tenant); bad.State != evidenceUnknownRead || bad.Cause != evidenceCauseInconsistent {
					t.Fatalf("a window on an unbounded period was accepted: %+v", bad)
				}
			}
		})
	}
}

// resealedWithRow reseals the envelope like resealed and brings the row's historical
// spend cell with it, so a case about the envelope's own semantics is not decided by
// the row binding instead.
func resealedWithRow(t *testing.T, row model.Record, mutate func(*alertEvidenceEnvelope)) model.Record {
	t.Helper()
	out := resealed(t, row, mutate)
	var env alertEvidenceEnvelope
	if err := json.Unmarshal([]byte(out.String(colAlertEvidence)), &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	out[colAlertSpend] = env.Legacy.SpendMicroUSD
	return out
}

// TestAChangedRowCellIsNotDescribedByItsEvidence is the other half of A4.2-R3, and
// the cheaper defect: the envelope is untouched and verifies, and ONE cell of the row
// beside it is changed. The reader used to return valid, so the DTO copied
// `legacy_value_kind=exact` from the envelope and served the changed number.
func TestAChangedRowCellIsNotDescribedByItsEvidence(t *testing.T) {
	_, _, tenant, _, row := recordedCrossing(t)

	for _, tc := range []struct {
		name   string
		mutate func(model.Record)
	}{
		{name: "the historical spend", mutate: func(r model.Record) { r[colAlertSpend] = int64(99 * oneUSD) }},
		{name: "the limit", mutate: func(r model.Record) { r[colAlertLimit] = int64(999 * oneUSD) }},
		{name: "the threshold percent", mutate: func(r model.Record) { r[colThresholdPct] = int64(50) }},
		{name: "the severity", mutate: func(r model.Record) { r[colSeverity] = "low" }},
		{name: "the dimension", mutate: func(r model.Record) { r[colDimension] = "provider" }},
		{name: "the scope key", mutate: func(r model.Record) { r[colDimKey] = "openai" }},
		{name: "the period", mutate: func(r model.Record) { r[colPeriod] = "daily" }},
		{name: "the period bucket", mutate: func(r model.Record) {
			r[colPeriodStart] = model.NewTimestamp(baseTime.AddDate(0, -1, 0)).String()
		}},
		{name: "the trigger instant", mutate: func(r model.Record) {
			r[colTriggeredAt] = model.NewTimestamp(baseTime.Add(time.Hour)).String()
		}},
		{name: "a spend cell that is no longer a number", mutate: func(r model.Record) { r[colAlertSpend] = "12000000" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := model.Record{}
			for k, v := range row {
				changed[k] = v
			}
			tc.mutate(changed)
			got := interpretAlertEvidence(changed, tenant)
			t.Logf("state=%s cause=%s", got.State, got.Cause)
			if got.State != evidenceUnknownRead || got.Cause != evidenceCauseRowMismatch {
				t.Fatalf("state=%s cause=%s, want unknown/%s", got.State, got.Cause, evidenceCauseRowMismatch)
			}
			dto := toAlertDTO(changed, tenant)
			if dto.LegacyValueKind != legacyValueUnverified {
				t.Fatalf("legacy_value_kind = %q, want unverified: the envelope does not describe this row", dto.LegacyValueKind)
			}
		})
	}

	// The control that keeps the binding honest: the untouched row still verifies, and
	// a re-read of the SAME bytes is not a repair of anything.
	if ev := interpretAlertEvidence(row, tenant); ev.State != evidenceValid {
		t.Fatalf("the untouched row stopped verifying: %+v", ev)
	}
}

// --- A4.2-R5: the serialized alert writer -----------------------------------

// lockSpy records the order in which the alert writer takes its transaction lock
// and touches the alert repository, so "the lock is held before the dedup decision"
// is measured rather than assumed.
type lockSpy struct {
	mu           sync.Mutex
	seq          int
	keys         []string
	lockAt       int
	alertReadAt  int
	alertWriteAt int
	lockErr      error
}

func (s *lockSpy) mark(field *int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	if *field == 0 {
		*field = s.seq
	}
	return s.seq
}

type lockSpyData struct {
	api.ModuleData
	spy *lockSpy
	// hide removes the optional capability entirely, which is how a decorator that
	// dropped it would look to this module.
	hide bool
}

func (d lockSpyData) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.ModuleData.View(ctx, tenant, func(sc store.Scope) error { return fn(d.wrap(sc)) })
}

func (d lockSpyData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.ModuleData.Mutate(ctx, tenant, func(sc store.Scope) error { return fn(d.wrap(sc)) })
}

func (d lockSpyData) wrap(sc store.Scope) store.Scope {
	if d.hide {
		return noLockScope{Scope: sc}
	}
	return lockSpyScope{Scope: sc, spy: d.spy}
}

// noLockScope hides the optional capability by embedding only store.Scope. It is
// exactly what a wrapper written without thinking about optional methods produces.
type noLockScope struct{ store.Scope }

type lockSpyScope struct {
	store.Scope
	spy *lockSpy
}

func (s lockSpyScope) LockTransaction(ctx context.Context, key string) error {
	s.spy.mu.Lock()
	s.spy.keys = append(s.spy.keys, key)
	err := s.spy.lockErr
	s.spy.mu.Unlock()
	s.spy.mark(&s.spy.lockAt)
	if err != nil {
		return err
	}
	locker, ok := s.Scope.(store.TransactionLocker)
	if !ok {
		return errors.New("finops-test: wrapped scope provides no transaction lock")
	}
	return locker.LockTransaction(ctx, key)
}

func (s lockSpyScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	repo, err := s.Scope.Ext(kind)
	if err != nil || kind != budgetAlertKind {
		return repo, err
	}
	return lockSpyRepo{GenericRepo: repo, spy: s.spy}, nil
}

type lockSpyRepo struct {
	store.GenericRepo
	spy *lockSpy
}

func (r lockSpyRepo) List(ctx context.Context, q model.Query) ([]model.Record, model.Page, error) {
	r.spy.mark(&r.spy.alertReadAt)
	return r.GenericRepo.List(ctx, q)
}

func (r lockSpyRepo) CreateWithID(ctx context.Context, id model.ID, rec model.Record) (model.Record, error) {
	r.spy.mark(&r.spy.alertWriteAt)
	return r.GenericRepo.CreateWithID(ctx, id, rec)
}

// TestTheAlertWriterSerializesOrRefuses is A4.2-R5. The writer takes ONE
// transaction lock per tenant before it reads or writes any alert row, and it fails
// CLOSED when that capability is absent or fails — because a writer that proceeds
// without it restores the race the lock exists to remove, and on PostgreSQL the
// losing side of that race takes its cost sample and ledger row down with it.
func TestTheAlertWriterSerializesOrRefuses(t *testing.T) {
	t.Run("the lock is held before the dedup decision and the insert", func(t *testing.T) {
		m, st, tenant, _ := newFin(t)
		m.clock = &fakeClock{t: baseTime}
		createBudget(t, st, tenant, "serialized", budgetSpec{
			Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD,
			Action: "block", Thresholds: []float64{1},
		})
		spy := &lockSpy{}
		m.UseData(lockSpyData{ModuleData: m.data, spy: spy})
		m.ingest(t, tenant, mkCost("openai", "gpt-x", "s-1", 1, 1, 12*oneUSD, baseTime))

		t.Logf("keys=%v lockAt=%d readAt=%d writeAt=%d", spy.keys, spy.lockAt, spy.alertReadAt, spy.alertWriteAt)
		if len(spy.keys) != 1 {
			t.Fatalf("lock keys = %v, want exactly one per ingestion", spy.keys)
		}
		if want := alertWriterLockKeyPrefix + tenant.String(); spy.keys[0] != want {
			t.Fatalf("lock key = %q, want %q", spy.keys[0], want)
		}
		if spy.lockAt == 0 || spy.alertReadAt == 0 || spy.alertWriteAt == 0 {
			t.Fatalf("the ingestion did not reach all three steps: %+v", spy)
		}
		if !(spy.lockAt < spy.alertReadAt && spy.alertReadAt < spy.alertWriteAt) {
			t.Fatalf("order was lock=%d read=%d write=%d, want lock before the lookup before the insert",
				spy.lockAt, spy.alertReadAt, spy.alertWriteAt)
		}
	})

	for _, tc := range []struct {
		name string
		hide bool
		err  error
	}{
		{name: "a scope without the capability", hide: true},
		{name: "a lock that fails", err: errors.New("finops-test: lock unavailable")},
	} {
		t.Run(tc.name+" aborts the ingestion", func(t *testing.T) {
			m, st, tenant, host := newFin(t)
			m.clock = &fakeClock{t: baseTime}
			createBudget(t, st, tenant, "unserializable", budgetSpec{
				Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD,
				Action: "block", Thresholds: []float64{1},
			})
			spy := &lockSpy{lockErr: tc.err}
			m.UseData(lockSpyData{ModuleData: m.data, spy: spy, hide: tc.hide})

			err := m.onCost(context.Background(), tenant, mkCost("openai", "gpt-x", "s-1", 1, 1, 12*oneUSD, baseTime), nil)
			t.Logf("err=%v", err)
			if err == nil {
				t.Fatalf("the ingestion proceeded without a usable writer lock")
			}
			if tc.err != nil && !errors.Is(err, tc.err) {
				t.Fatalf("err = %v, want the underlying lock failure", err)
			}
			// Fail-closed means the whole ingestion, not just the alert.
			if n := countCosts(t, st, tenant); n != 0 {
				t.Errorf("%d cost records survived", n)
			}
			if rows := costSampleRows(t, st, tenant); len(rows) != 0 {
				t.Errorf("%d cost samples survived", len(rows))
			}
			if rows := alertRows(t, st, tenant); len(rows) != 0 {
				t.Errorf("%d alert rows survived", len(rows))
			}
			if f := host.findings(); len(f) != 0 {
				t.Errorf("an aborted ingestion published %d findings", len(f))
			}
		})
	}
}

// runTwoDistinctIngestionsDedup is the causal control for A4.2-R5, shared by the
// SQLite and PostgreSQL legs because the defect is a BACKEND one: two DIFFERENT cost
// samples that both cross the same (budget, period, threshold).
//
// The first ingestion records the crossing. The second must still commit its own
// sample and ledger row while the alert is merely deduplicated — which is exactly
// what the delivered writer could not do on PostgreSQL: it reached the INSERT, the
// unique index raised a violation, the violation aborted the transaction, and
// absorbing the error and returning "not created" left the caller committing a dead
// transaction. The exact lookup under the writer lock never attempts that INSERT.
func runTwoDistinctIngestionsDedup(t *testing.T, cfg store.Config) {
	m, st, tenant, host := openFinCfg(t, cfg)
	m.clock = &fakeClock{t: baseTime}
	createBudget(t, st, tenant, "dedup-"+uniqueSlugSuffix(t), budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD,
		Action: "block", Thresholds: []float64{1},
	})

	m.ingest(t, tenant, mkCost("openai", "gpt-x", "s1", 1, 1, 12*oneUSD, baseTime))
	rows := alertRows(t, st, tenant)
	if len(rows) != 1 {
		t.Fatalf("first ingestion wrote %d alert rows, want 1", len(rows))
	}
	first := interpretAlertEvidence(rows[0], tenant)
	if first.State != evidenceValid {
		t.Fatalf("first evidence did not verify: %+v", first)
	}
	firstID := rows[0].String(model.ColID)

	// A DIFFERENT sample — another session, so another natural key — in the same
	// period. It crosses the same threshold, which is already recorded.
	if err := m.onCost(context.Background(), tenant,
		mkCost("openai", "gpt-x", "s2", 1, 1, oneUSD, baseTime.Add(time.Minute)), nil); err != nil {
		t.Fatalf("the second ingestion was lost to the deduplicated alert: %v", err)
	}

	// Its own rows committed.
	if n := countCosts(t, st, tenant); n != 2 {
		t.Fatalf("cost records = %d, want both ingestions", n)
	}
	if samples := costSampleRows(t, st, tenant); len(samples) != 2 {
		t.Fatalf("cost samples = %d, want both ingestions", len(samples))
	}
	// The alert is untouched: same row, same id, same digest, same historical number.
	rows = alertRows(t, st, tenant)
	if len(rows) != 1 {
		t.Fatalf("alert rows = %d after the second ingestion, want the first only", len(rows))
	}
	again := interpretAlertEvidence(rows[0], tenant)
	if rows[0].String(model.ColID) != firstID || again.Digest != first.Digest {
		t.Fatalf("the deduplicated crossing rewrote its row: %s/%s -> %s/%s",
			firstID, first.Digest, rows[0].String(model.ColID), again.Digest)
	}
	if again.State != evidenceValid || *again.Envelope.Amount.Value != "12000000" {
		t.Fatalf("the first crossing's evidence moved: %+v", again)
	}
	// One emission, not two.
	caps := 0
	for _, f := range host.findings() {
		if f.Kind == "finops_budget_cap" {
			caps++
		}
	}
	if caps != 1 {
		t.Fatalf("cap findings = %d, want exactly one", caps)
	}
	// And the money the second sample brought is accounted for: 13 USD strictly.
	var total strictCostTotal
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		total = readStrictCostTotal(context.Background(), sc, strictCostWindowForBudget(tenant, budgetSpec{
			Dimension: "global", Period: "monthly",
		}, baseTime))
		return total.Err
	}); err != nil {
		t.Fatalf("strict read: %v", err)
	}
	if !total.Complete || total.Total.String() != "13000000" {
		t.Fatalf("strict total = %+v, want a complete 13000000", total)
	}
}

// TestTwoDistinctIngestionsDedupSQLite runs that control on SQLite.
func TestTwoDistinctIngestionsDedupSQLite(t *testing.T) {
	runTwoDistinctIngestionsDedup(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true})
}

// TestTwoDistinctIngestionsDedupPostgres runs the SAME control on a real, isolated
// PostgreSQL database — the engine whose aborted-transaction semantics make the
// defect observable. Without the backend the leg is SKIPPED: not run is not a pass.
func TestTwoDistinctIngestionsDedupPostgres(t *testing.T) {
	if !enginetest.PostgresAvailable(t) {
		t.Skipf("%s unset: the PostgreSQL dedup leg is NOT RUN. This is not a pass.", enginetest.EnvSuperuserDSN)
	}
	dsns := enginetest.IsolatedPostgres(t)
	runTwoDistinctIngestionsDedup(t, store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4})
}

// --- A4.2-R6: the ingestion boundary cases the delivered group 4 did not reach ---

// failingReadData breaks a READ inside the ingestion transaction, after the sample
// has been written and while the evaluation is running.
type failingReadData struct {
	api.ModuleData
	err error
}

func (d failingReadData) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.ModuleData.View(ctx, tenant, fn)
}

func (d failingReadData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.ModuleData.Mutate(ctx, tenant, func(sc store.Scope) error {
		return fn(failingReadScope{Scope: sc, err: d.err})
	})
}

type failingReadScope struct {
	store.Scope
	err error
}

func (s failingReadScope) LockTransaction(ctx context.Context, key string) error {
	locker, ok := s.Scope.(store.TransactionLocker)
	if !ok {
		return errors.New("finops-test: wrapped scope provides no transaction lock")
	}
	return locker.LockTransaction(ctx, key)
}

func (s failingReadScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	repo, err := s.Scope.Ext(kind)
	if err != nil || kind != budgetReservationKind {
		return repo, err
	}
	return failingReadRepo{GenericRepo: repo, err: s.err}, nil
}

type failingReadRepo struct {
	store.GenericRepo
	err error
}

func (r failingReadRepo) List(context.Context, model.Query) ([]model.Record, model.Page, error) {
	return nil, model.Page{}, r.err
}

// auditingIngest is the audit hook a privileged HTTP ingest appends in the SAME
// transaction. The delivered atomicity case passed nil, so the audited fact was
// never part of what it proved was rolled back.
func auditingIngest(t *testing.T) auditHook {
	t.Helper()
	return func(ctx context.Context, sc store.Scope) error {
		ev, err := sc.Audit().Append(ctx, model.AuditDraft{
			Actor: "finops-test-principal", ActorKind: "user",
			Action: "finops.cost.ingest", TargetKind: costSampleKind,
		})
		if err != nil {
			return err
		}
		if ev.Seq == 0 {
			return errors.New("finops-test: the audit evidence was dropped")
		}
		return nil
	}
}

// TestAnIngestionThatFailsRollsBackItsAuditToo covers the two group-4 gaps the
// review named: the audit hook was nil, so the audited fact was outside the claim,
// and no case broke a READ inside the ingestion.
func TestAnIngestionThatFailsRollsBackItsAuditToo(t *testing.T) {
	for _, tc := range []struct {
		name   string
		break_ func(m *Module, boom error)
	}{
		{
			name: "the alert write fails",
			break_: func(m *Module, boom error) {
				m.UseData(failingAlertData{api: m.data, err: boom})
			},
		},
		{
			name: "a read inside the ingestion fails",
			break_: func(m *Module, boom error) {
				m.UseData(failingReadData{ModuleData: m.data, err: boom})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, st, tenant, host := newFin(t)
			m.clock = &fakeClock{t: baseTime}
			createBudget(t, st, tenant, "aborting", budgetSpec{
				Dimension: "global", Period: "monthly", LimitMicroUSD: oneUSD,
				Action: "block", Thresholds: []float64{1},
			})
			boom := errors.New("finops-test: storage failure")
			tc.break_(m, boom)

			err := m.onCost(context.Background(), tenant,
				mkCost("openai", "gpt-x", "s-1", 1, 1, 5*oneUSD, baseTime), auditingIngest(t))
			t.Logf("err=%v", err)
			if !errors.Is(err, boom) {
				t.Fatalf("err = %v, want the storage failure to abort the ingestion", err)
			}
			if n := countCosts(t, st, tenant); n != 0 {
				t.Errorf("%d cost records survived an aborted ingestion", n)
			}
			if rows := costSampleRows(t, st, tenant); len(rows) != 0 {
				t.Errorf("%d cost samples survived", len(rows))
			}
			if rows := alertRows(t, st, tenant); len(rows) != 0 {
				t.Errorf("%d alert rows survived", len(rows))
			}
			// The audited fact went with them: a rolled-back ingest leaves no phantom
			// audit, which is the property the hook exists to have.
			if n := countAuditAction(t, st, tenant, "finops.cost.ingest"); n != 0 {
				t.Errorf("%d audit events survived an aborted ingestion", n)
			}
			if f := host.findings(); len(f) != 0 {
				t.Errorf("an aborted ingestion published %d findings", len(f))
			}
		})
	}

	// And the control that makes the two above mean something: a SUCCESSFUL ingest
	// keeps its audit and its alert together.
	t.Run("a committed ingestion keeps both", func(t *testing.T) {
		m, st, tenant, _ := newFin(t)
		m.clock = &fakeClock{t: baseTime}
		createBudget(t, st, tenant, "committing", budgetSpec{
			Dimension: "global", Period: "monthly", LimitMicroUSD: oneUSD,
			Action: "block", Thresholds: []float64{1},
		})
		if err := m.onCost(context.Background(), tenant,
			mkCost("openai", "gpt-x", "s-1", 1, 1, 5*oneUSD, baseTime), auditingIngest(t)); err != nil {
			t.Fatalf("onCost: %v", err)
		}
		if n := countAuditAction(t, st, tenant, "finops.cost.ingest"); n != 1 {
			t.Fatalf("audit events = %d, want 1", n)
		}
		if rows := alertRows(t, st, tenant); len(rows) != 1 {
			t.Fatalf("alert rows = %d, want 1", len(rows))
		}
	})
}

// TestAPublishFailureDoesNotTouchWhatIsAlreadyCommitted: publication happens after
// the commit and is best effort. A delivery failure must not fail the ingestion, and
// it must not alter or remove the row and evidence that are already durable — nor may
// it be reported as a delivery that happened.
func TestAPublishFailureDoesNotTouchWhatIsAlreadyCommitted(t *testing.T) {
	m, st, tenant, host := newFin(t)
	m.clock = &fakeClock{t: baseTime}
	createBudget(t, st, tenant, "publish-fails", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD,
		Action: "block", Thresholds: []float64{1},
	})
	host.failPublishing(errors.New("finops-test: bus unavailable"))

	if err := m.onCost(context.Background(), tenant,
		mkCost("openai", "gpt-x", "s-1", 1, 1, 12*oneUSD, baseTime), nil); err != nil {
		t.Fatalf("a post-commit publish failure failed the ingestion: %v", err)
	}
	rows := alertRows(t, st, tenant)
	if len(rows) != 1 {
		t.Fatalf("alert rows = %d, want the committed crossing", len(rows))
	}
	ev := interpretAlertEvidence(rows[0], tenant)
	if ev.State != evidenceValid || *ev.Envelope.Amount.Value != "12000000" {
		t.Fatalf("the committed evidence changed after a failed publish: %+v", ev)
	}
	if n := countCosts(t, st, tenant); n != 1 {
		t.Fatalf("cost records = %d, want the committed one", n)
	}
	// The attempt was made and it FAILED: this increment does not claim delivery.
	if len(host.findings()) != 1 {
		t.Fatalf("findings attempted = %d, want 1", len(host.findings()))
	}
}

// TestAMatchingGroupBudgetReachesTheStrictEvaluation closes the gap the review named
// in group 3: its group case used a key that did not match the attribution at all, so
// the budget was filtered out before any evaluation and the strict group refusal was
// never exercised. Here the attribution DOES carry the group, the evaluation runs,
// and the group's scope is still unresolved — so no crossing is formed and the batch
// says why.
func TestAMatchingGroupBudgetReachesTheStrictEvaluation(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	m.clock = &fakeClock{t: baseTime}
	group := model.NewID().String()
	id := createBudget(t, st, tenant, "matching-group", budgetSpec{
		Dimension: "user_group", Key: group, Period: "monthly",
		LimitMicroUSD: 10 * oneUSD, Action: "block", Thresholds: []float64{1},
	})

	var (
		pending     []pendingAlert
		diagnostics []pendingDiagnostic
	)
	if err := m.data.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		var err error
		pending, diagnostics, err = m.evaluateBudgets(context.Background(), sc, attribution{
			ProviderRef: "openai", ModelRef: "gpt-x", SessionRef: "s-1",
			CostMicroUSD: 99 * oneUSD, OccurredAt: baseTime,
			UserGroupRefs: []string{group},
		})
		return err
	}); err != nil {
		t.Fatalf("evaluateBudgets: %v", err)
	}
	t.Logf("pending=%d diagnostics=%+v", len(pending), diagnostics)
	if len(pending) != 0 {
		t.Fatalf("an unresolved group produced %d crossings from tenant-wide spend", len(pending))
	}
	found := false
	for _, d := range diagnostics {
		if d.BudgetID != id {
			continue
		}
		for _, c := range d.Causes {
			if c == causeScopeGroupUnresolved {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("the matching group budget did not report scope_group_unresolved: %+v", diagnostics)
	}
	if rows := alertRows(t, st, tenant); len(rows) != 0 {
		t.Fatalf("%d alert rows were written for an unresolved group", len(rows))
	}
}
