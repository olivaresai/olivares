// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package runtime

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
	"github.com/olivaresai/olivares/sdk/model"
)

// Permanent regression from the independent b649736 review. Only the fixture
// and test names differ from the preserved causal; its ordering is unchanged.

// Independent causal: a blocking first Notify holds the real in-process output
// subscriber while the second event is queued. No timer/sleep creates the order.
type admissionBlockedOutput struct {
	entered chan struct{}
	release chan struct{}
	got     chan sdk.Notification
	once    sync.Once
}

func (*admissionBlockedOutput) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: "test.review.budget.output", Type: sdk.TypeOutput, APIVersion: sdk.APIVersion}
}
func (*admissionBlockedOutput) Open(context.Context, sdk.Config) error { return nil }
func (*admissionBlockedOutput) Close(context.Context) error            { return nil }
func (o *admissionBlockedOutput) Notify(ctx context.Context, n sdk.Notification) error {
	o.once.Do(func() {
		close(o.entered)
		select {
		case <-o.release:
		case <-ctx.Done():
		}
	})
	o.got <- n
	return nil
}

func TestBudgetEvidenceLegacyPointerAdmissionFrozen(t *testing.T) {
	r, bus := budgetRuntime(t)
	other := &budgetModule{name: "test.review.foreign"}
	out := &admissionBlockedOutput{
		entered: make(chan struct{}), release: make(chan struct{}),
		got: make(chan sdk.Notification, 2),
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(out.release) }) }
	defer release()
	if err := r.AddModule(other, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := r.AddOutput(out, sdk.Config{}, []event.Type{event.TypeFindingReported}); err != nil {
		t.Fatal(err)
	}
	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	// This legitimate legacy event occupies the sole output worker in Notify.
	primer := runtimeBudgetFinding()
	primer.BudgetEvidence = nil
	primer.SubjectRef = "f099e9ee-b91d-452f-b344-dc265e398628"
	if err := other.host.Publish(context.Background(), event.FromObservation("tenant-a", "test.review.foreign", primer)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-out.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("output barrier was not reached")
	}

	f := runtimeBudgetFinding()
	proof := f.BudgetEvidence
	f.BudgetEvidence = nil
	target := event.Event{Type: event.TypeFindingReported, Tenant: "tenant-a", Source: model.BudgetEvidenceProducer, Payload: &f}
	if err := other.host.Publish(context.Background(), target); err != nil {
		t.Fatalf("legacy publication rejected: %v", err)
	}
	queued := bus.snapshot()
	if len(queued) != 2 {
		t.Fatalf("events queued=%d, want 2", len(queued))
	}
	atAdmission, ok := event.FindingOf(queued[1])
	if !ok || atAdmission.BudgetEvidence != nil {
		t.Fatal("target was not legacy at admission")
	}
	t.Logf("admitted through foreign host: source=%s payload=%T presence=%s", queued[1].Source, queued[1].Payload, atAdmission.BudgetEvidenceValidity())

	// Publish has returned, and the output worker cannot inspect the target until
	// release below. Adding the extension is therefore ordered, not a Go race.
	f.BudgetEvidence = proof
	release()
	var targetNotification sdk.Notification
	for i := 0; i < 2; i++ {
		select {
		case n := <-out.got:
			if n.Fields["subject"] == f.SubjectRef {
				targetNotification = n
			}
		case <-time.After(3 * time.Second):
			t.Fatal("queued notification did not arrive")
		}
	}
	if targetNotification.Fields == nil {
		t.Fatal("target notification missing")
	}
	t.Logf("delivered target fields: %v", targetNotification.Fields)
	if got := targetNotification.Fields["budget_evidence_validation"]; got != "" {
		t.Fatalf("foreign legacy pointer gained financial evidence after admission: validation=%q alert_id=%q", got, targetNotification.Fields["budget_evidence_alert_id"])
	}
}

func TestBudgetEvidenceAdmissionOwnsFindingFields(t *testing.T) {
	for _, shape := range []string{"owner_value", "owner_pointer", "legacy_value", "source_value", "source_pointer"} {
		t.Run(shape, func(t *testing.T) {
			r, bus := budgetRuntime(t)
			owner := shape == "owner_value" || shape == "owner_pointer"
			source := shape == "source_value" || shape == "source_pointer"
			mod := &budgetModule{name: "test.snapshot.foreign"}
			if owner {
				mod.name = model.BudgetEvidenceProducer
			}
			out := &admissionBlockedOutput{entered: make(chan struct{}), release: make(chan struct{}), got: make(chan sdk.Notification, 2)}
			var once sync.Once
			release := func() { once.Do(func() { close(out.release) }) }
			defer release()
			if err := r.AddModule(mod, sdk.Config{}); err != nil {
				t.Fatal(err)
			}
			if err := r.AddOutput(out, sdk.Config{}, []event.Type{event.TypeFindingReported}); err != nil {
				t.Fatal(err)
			}
			if err := r.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			primer := runtimeBudgetFinding()
			primer.BudgetEvidence = nil
			if err := mod.host.Publish(context.Background(), event.FromObservation("tenant-a", mod.name, primer)); err != nil {
				t.Fatal(err)
			}
			select {
			case <-out.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("output barrier not reached")
			}

			f, want := runtimeBudgetFinding(), runtimeBudgetFinding()
			for _, p := range []*model.FindingReport{&f, &want} {
				p.Title = "admitted title"
				p.OccurredAt = time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
				p.OWASPLLM = []string{"LLM01:2025"}
				p.OWASPASI = []string{"ASI01"}
				p.ATLAS = []string{"AML.T0051"}
				if !owner {
					p.BudgetEvidence = nil
				}
			}
			var obs model.Observation = f
			if shape == "owner_pointer" || shape == "source_pointer" {
				obs = &f
			}
			reg := &event.SourceRegistration{SourceID: "source-id", SourceRevision: 1, EnvironmentRef: "environment", BindingRef: "admitted-binding"}
			wantReg := *reg
			if source {
				if err := r.Ingest(context.Background(), "tenant-a", model.BudgetEvidenceProducer, obs); err != nil {
					t.Fatal(err)
				}
			} else {
				e := event.Event{Type: event.TypeFindingReported, Tenant: "tenant-a", Source: model.BudgetEvidenceProducer, Payload: obs}
				if !owner {
					e.SourceRegistration = reg
				}
				if err := mod.host.Publish(context.Background(), e); err != nil {
					t.Fatal(err)
				}
			}

			// All writes are AFTER admission and BEFORE releasing the real output
			// worker. No sleep or concurrent DTO write is needed to expose aliasing.
			f.Kind = "changed"
			f.Severity = model.SeverityLow
			f.SubjectKind = "changed"
			f.SubjectRef = "changed"
			f.Title = "changed"
			f.DetailHash = "changed"
			f.OccurredAt = time.Time{}
			f.OWASPLLM[0] = "changed"
			f.OWASPASI[0] = "changed"
			f.ATLAS[0] = "changed"
			if f.BudgetEvidence != nil {
				f.BudgetEvidence.AlertID = "changed"
				f.BudgetEvidence.Causes[0] = "changed"
				f.BudgetEvidence.AmountClass = "unknown"
			}
			f.BudgetEvidence = &model.BudgetAlertEvidenceSummary{}
			reg.BindingRef = "changed"
			reg.SourceID = "changed"
			reg.SourceRevision = 99
			reg.EnvironmentRef = "changed"
			queued := bus.snapshot()
			if len(queued) != 2 {
				t.Fatalf("publications=%d", len(queued))
			}
			got, ok := event.FindingOf(queued[1])
			if !ok || !reflect.DeepEqual(got, want) {
				t.Fatalf("admitted finding changed: got=%+v want=%+v", got, want)
			}
			if !source && !owner && !reflect.DeepEqual(queued[1].SourceRegistration, &wantReg) {
				t.Fatal("admitted source attribution changed")
			}
			release()
			var delivered sdk.Notification
			for i := 0; i < 2; i++ {
				select {
				case n := <-out.got:
					if n.Title == want.Title {
						delivered = n
					}
				case <-time.After(3 * time.Second):
					t.Fatal("queued notification missing")
				}
			}
			if delivered.Title != want.Title || delivered.Fields["subject"] != want.SubjectRef || delivered.Fields["detail_hash"] != want.DetailHash {
				t.Fatalf("output changed from admission: %+v", delivered)
			}
			if owner {
				if delivered.Fields["budget_evidence_validation"] != "structurally_valid" || delivered.Fields["budget_evidence_alert_id"] != want.BudgetEvidence.AlertID || delivered.Fields["budget_evidence_crossing"] != "proven" {
					t.Fatalf("owner evidence changed: %v", delivered.Fields)
				}
			} else if _, present := delivered.Fields["budget_evidence_validation"]; present {
				t.Fatalf("legacy acquired evidence: %v", delivered.Fields)
			}
		})
	}
}

func TestBudgetEvidenceAdmissionRejectsTypedNilFinding(t *testing.T) {
	r, bus := budgetRuntime(t)
	owner, other := &budgetModule{name: model.BudgetEvidenceProducer}, &budgetModule{name: "test.nil.foreign"}
	for _, mod := range []*budgetModule{owner, other} {
		if err := r.AddModule(mod, sdk.Config{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	var f *model.FindingReport
	for _, mod := range []*budgetModule{owner, other} {
		if err := mod.host.Publish(context.Background(), event.Event{Type: event.TypeFindingReported, Source: model.BudgetEvidenceProducer, Payload: f}); err == nil {
			t.Fatal("typed nil finding admitted by host")
		}
	}
	if err := r.Ingest(context.Background(), "tenant-a", model.BudgetEvidenceProducer, f); err == nil {
		t.Fatal("typed nil finding admitted by ingest")
	}
	if len(bus.snapshot()) != 0 {
		t.Fatal("typed nil reached the event bus")
	}
}
