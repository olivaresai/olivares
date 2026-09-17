// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package runtime

import (
	"context"
	"errors"
	goplugin "github.com/hashicorp/go-plugin"
	"github.com/olivaresai/olivares/core/eventbus"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
	"github.com/olivaresai/olivares/sdk/model"
	"github.com/olivaresai/olivares/sdk/plugin"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

type budgetTap struct {
	eventbus.Bus
	mu     sync.Mutex
	events []event.Event
}

func (b *budgetTap) Publish(ctx context.Context, e event.Event) error {
	b.mu.Lock()
	b.events = append(b.events, e)
	b.mu.Unlock()
	return b.Bus.Publish(ctx, e)
}
func (b *budgetTap) snapshot() []event.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]event.Event(nil), b.events...)
}
func budgetRuntime(t *testing.T) (*Runtime, *budgetTap) {
	t.Helper()
	b := &budgetTap{Bus: eventbus.NewInProc(eventbus.Options{})}
	r := New(Options{Bus: b, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := r.Stop(ctx); err != nil {
			t.Error(err)
		}
		if err := b.Close(); err != nil {
			t.Error(err)
		}
	})
	return r, b
}

type budgetModule struct {
	name string
	host sdk.Host
}

func (m *budgetModule) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: m.name, Type: sdk.TypeModule, APIVersion: sdk.APIVersion}
}
func (m *budgetModule) Init(_ context.Context, h sdk.Host) error { m.host = h; return nil }
func (*budgetModule) Start(context.Context) error                { return nil }
func (*budgetModule) Stop(context.Context) error                 { return nil }

type budgetOutput struct{ got chan sdk.Notification }

func (*budgetOutput) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: "test.budget.output", Type: sdk.TypeOutput, APIVersion: sdk.APIVersion}
}
func (*budgetOutput) Open(context.Context, sdk.Config) error               { return nil }
func (o *budgetOutput) Notify(_ context.Context, n sdk.Notification) error { o.got <- n; return nil }
func (*budgetOutput) Close(context.Context) error                          { return nil }

func runtimeBudgetFinding() model.FindingReport {
	return model.FindingReport{Kind: "finops_budget_cap", SubjectKind: "budget", SubjectRef: "b14c91e9-b593-48d6-94aa-fc8de67c1f51", DetailHash: strings.Repeat("a", 64), Severity: model.SeverityCritical,
		BudgetEvidence: &model.BudgetAlertEvidenceSummary{SchemaVersion: 1, DigestVersion: 1, AlertID: "bd283469-7c26-4256-9798-f3b175a8df2a", AmountClass: "lower_bound", Crossing: "proven", Causes: []string{"dynamic_reservation_scan_incomplete"}}}
}

func TestBudgetEvidenceRegisteredHostAndOutput(t *testing.T) {
	r, b := budgetRuntime(t)
	owner := &budgetModule{name: model.BudgetEvidenceProducer}
	other := &budgetModule{name: "test.other"}
	out := &budgetOutput{got: make(chan sdk.Notification, 8)}
	for _, m := range []*budgetModule{owner, other} {
		if err := r.AddModule(m, sdk.Config{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.AddOutput(out, sdk.Config{}, []event.Type{event.TypeFindingReported}); err != nil {
		t.Fatal(err)
	}
	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, pointer := range []bool{false, true} {
		f := runtimeBudgetFinding()
		var payload any = f
		if pointer {
			payload = &f
		}
		e := event.Event{Type: event.TypeFindingReported, Tenant: "tenant-a", Source: "spoofed", Payload: payload}
		if err := owner.host.Publish(context.Background(), e); err != nil {
			t.Fatal(err)
		}
		got := b.snapshot()
		last := got[len(got)-1]
		if last.Source != model.BudgetEvidenceProducer || last.Tenant != e.Tenant {
			t.Fatalf("host failed to stamp ownership: %+v", last)
		}
		select {
		case n := <-out.got:
			want := map[string]string{"detail_hash": f.DetailHash, "budget_evidence_validation": "structurally_valid", "budget_evidence_schema_version": "1", "budget_evidence_digest_version": "1", "budget_evidence_alert_id": f.BudgetEvidence.AlertID, "budget_evidence_amount_class": "lower_bound", "budget_evidence_crossing": "proven", "budget_evidence_causes": "dynamic_reservation_scan_incomplete", "subject": f.SubjectRef}
			for k, v := range want {
				if n.Fields[k] != v {
					t.Fatalf("output lost %s: %v", k, n.Fields)
				}
			}
			if n.Severity != model.SeverityCritical {
				t.Fatal("bound lost existing cap severity")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("direct output not delivered")
		}
		// The host snapshot owns the summary after the publisher returns.
		f.BudgetEvidence.Causes[0] = "mutated"
		published, _ := event.FindingOf(last)
		if published.BudgetEvidence.Causes[0] != "dynamic_reservation_scan_incomplete" {
			t.Fatal("published summary aliases caller")
		}
		f = runtimeBudgetFinding()
		if pointer {
			payload = &f
		} else {
			payload = f
		}
		e.Payload = payload
		e.Source = model.BudgetEvidenceProducer
		before := len(b.snapshot())
		if err := other.host.Publish(context.Background(), e); !errors.Is(err, ErrReservedBudgetEvidence) {
			t.Fatalf("foreign module spoof allowed: %v", err)
		}
		if len(b.snapshot()) != before {
			t.Fatal("refused foreign event reached bus")
		}
	}
	for _, tc := range []string{"empty", "future", "kind", "event_type", "registration"} {
		t.Run(tc, func(t *testing.T) {
			f := runtimeBudgetFinding()
			e := event.Event{Type: event.TypeFindingReported, Source: model.BudgetEvidenceProducer}
			switch tc {
			case "empty":
				f.BudgetEvidence = &model.BudgetAlertEvidenceSummary{}
			case "future":
				f.BudgetEvidence.SchemaVersion = 2
			case "kind":
				f.Kind = "not_finops"
			case "event_type":
				e.Type = event.TypeCostSampled
			case "registration":
				e.SourceRegistration = &event.SourceRegistration{SourceID: "source-id", SourceRevision: 1, EnvironmentRef: "env"}
			}
			e.Payload = &f
			before := len(b.snapshot())
			if err := owner.host.Publish(context.Background(), e); !errors.Is(err, ErrReservedBudgetEvidence) || len(b.snapshot()) != before {
				t.Fatalf("invalid owner publication escaped: %v", err)
			}
		})
	}
	// Historical findings retain their declared, unverified Source behavior.
	legacy := runtimeBudgetFinding()
	legacy.BudgetEvidence = nil
	if err := other.host.Publish(context.Background(), event.FromObservation("tenant-a", model.BudgetEvidenceProducer, legacy)); err != nil {
		t.Fatal(err)
	}
	select {
	case n := <-out.got:
		if _, present := n.Fields["budget_evidence_validation"]; present {
			t.Fatal("legacy output acquired certification")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("legacy output not delivered")
	}
}

type budgetSource struct{ obs model.Observation }

func (*budgetSource) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: model.BudgetEvidenceProducer, Type: sdk.TypeSource, APIVersion: sdk.APIVersion}
}
func (*budgetSource) Open(context.Context, sdk.Config) error            { return nil }
func (s *budgetSource) Gather(ctx context.Context, sink sdk.Sink) error { return sink.Emit(ctx, s.obs) }
func (*budgetSource) Close(context.Context) error                       { return nil }

type budgetObservedSource struct {
	sdk.SourceConnector
	result chan error
}

func (s *budgetObservedSource) Gather(ctx context.Context, sink sdk.Sink) error {
	err := s.SourceConnector.Gather(ctx, sink)
	s.result <- err
	return err
}

func TestBudgetEvidenceActualSourceAndPluginIngress(t *testing.T) {
	for _, transport := range []string{"local", "plugin"} {
		t.Run(transport, func(t *testing.T) {
			for _, shape := range []string{"value", "pointer", "present_empty", "legacy"} {
				t.Run(shape, func(t *testing.T) {
					r, b := budgetRuntime(t)
					f := runtimeBudgetFinding()
					if shape == "present_empty" {
						f.BudgetEvidence = &model.BudgetAlertEvidenceSummary{}
					}
					if shape == "legacy" {
						f.BudgetEvidence = nil
					}
					var obs model.Observation = f
					if shape == "pointer" {
						obs = &f
					}
					var src sdk.SourceConnector = &budgetSource{obs: obs}
					if transport == "plugin" {
						client, _ := goplugin.TestPluginGRPCConn(t, false, map[string]goplugin.Plugin{plugin.SourcePluginName: &plugin.SourcePlugin{Impl: src}})
						// Cleanup order: runtime stops the remote source before closing its client.
						defer client.Close()
						raw, err := client.Dispense(plugin.SourcePluginName)
						if err != nil {
							t.Fatal(err)
						}
						src = raw.(sdk.SourceConnector)
						defer func() {
							ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
							defer cancel()
							if err := r.Stop(ctx); err != nil {
								t.Error(err)
							}
						}()
					}
					observed := &budgetObservedSource{SourceConnector: src, result: make(chan error, 1)}
					if err := r.AddSourceNamed(model.BudgetEvidenceProducer, observed, sdk.Config{}, "tenant-a"); err != nil {
						t.Fatal(err)
					}
					if err := r.Start(context.Background()); err != nil {
						t.Fatal(err)
					}
					select {
					case err := <-observed.result:
						if shape == "legacy" {
							if err != nil || len(b.snapshot()) != 1 {
								t.Fatalf("legacy behavior changed: %v", err)
							}
						} else if !errors.Is(err, ErrReservedBudgetEvidence) || len(b.snapshot()) != 0 {
							t.Fatalf("source acquired financial authority: err=%v events=%d", err, len(b.snapshot()))
						}
					case <-time.After(3 * time.Second):
						t.Fatal("source did not finish")
					}
				})
			}
		})
	}
}

func TestBudgetEvidenceRuntimeIngestPointerAndInvalidProjection(t *testing.T) {
	r, b := budgetRuntime(t)
	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	f := runtimeBudgetFinding()
	for _, obs := range []model.Observation{f, &f} {
		if err := r.Ingest(context.Background(), "tenant-a", model.BudgetEvidenceProducer, obs); !errors.Is(err, ErrReservedBudgetEvidence) {
			t.Fatalf("ingest accepted reserved finding: %v", err)
		}
	}
	if len(b.snapshot()) != 0 {
		t.Fatal("refused ingest reached bus")
	}
	for _, tc := range []string{"origin", "registration", "invalid_hash", "diagnostic"} {
		t.Run(tc, func(t *testing.T) {
			f := runtimeBudgetFinding()
			e := event.Event{Type: event.TypeFindingReported, Source: model.BudgetEvidenceProducer}
			switch tc {
			case "origin":
				e.Source = "connector"
			case "registration":
				e.SourceRegistration = &event.SourceRegistration{SourceID: "source", SourceRevision: 1, EnvironmentRef: "env"}
			case "invalid_hash":
				f.DetailHash = "raw error"
			case "diagnostic":
				f.Kind = "finops_budget_evaluation_incomplete"
				f.Severity = model.SeverityMedium
				f.BudgetEvidence.AlertID = ""
				f.BudgetEvidence.AmountClass = "unknown"
				f.BudgetEvidence.Crossing = "unproven"
				f.BudgetEvidence.Causes = []string{"cost_read_failed"}
			}
			e.Payload = &f
			n := notificationFromEvent(e)
			if tc == "diagnostic" {
				if n.Fields["budget_evidence_validation"] != "structurally_valid" || n.Fields["budget_evidence_alert_id"] != "" || n.Fields["detail_hash"] != f.DetailHash || n.Severity != model.SeverityMedium {
					t.Fatalf("diagnostic changed: %+v", n)
				}
			} else if n.Fields["budget_evidence_validation"] != "invalid" || n.Fields["budget_evidence_amount_class"] != "unknown" || n.Fields["budget_evidence_alert_id"] != "" || n.Fields["detail_hash"] != "" {
				t.Fatalf("invalid projection claims: %v", n.Fields)
			}
		})
	}
}
